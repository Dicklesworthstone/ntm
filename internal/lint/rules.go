package lint

import (
	"regexp"
	"strings"
)

// Default thresholds for size-based rules.
const (
	// DefaultMaxBytes is the default byte limit for prompts (100KB).
	DefaultMaxBytes = 100 * 1024
	// DefaultWarnBytes is the byte threshold for warnings (50KB).
	DefaultWarnBytes = 50 * 1024
	// DefaultMaxTokens is the default token estimate limit.
	DefaultMaxTokens = 32000
	// DefaultWarnTokens is the token threshold for warnings.
	DefaultWarnTokens = 16000
	// TokensPerChar is the approximate tokens per character ratio for English text.
	// Claude/GPT tokenizers average ~4 chars per token for English.
	TokensPerChar = 0.25
)

// Config keys for rule-specific configuration.
const (
	ConfigKeyMaxBytes      = "max_bytes"
	ConfigKeyWarnBytes     = "warn_bytes"
	ConfigKeyMaxTokens     = "max_tokens"
	ConfigKeyWarnTokens    = "warn_tokens"
	ConfigKeyRequiredTags  = "required_tags"
	ConfigKeySecretMode    = "secret_mode" // "warn" or "block"
	ConfigKeyAllowPatterns = "allow_patterns"
)

// destructivePatterns contains patterns for dangerous command detection.
// These patterns are checked against prompt content to identify risky instructions.
var destructivePatterns = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	// File deletion patterns
	{regexp.MustCompile(`rm\s+(-[rRf]+\s+)*[-rRf]*\s*/`), "recursive delete from root"},
	{regexp.MustCompile(`rm\s+(-[rRf]+\s+)+~`), "recursive delete of home directory"},
	{regexp.MustCompile(`rm\s+(-[rRf]+\s+)+\*`), "recursive delete with wildcard"},
	{regexp.MustCompile(`rm\s+-rf\s+\.\.`), "recursive delete of parent directory"},

	// Git destructive patterns
	{regexp.MustCompile(`git\s+reset\s+--hard`), "hard reset loses uncommitted changes"},
	{regexp.MustCompile(`git\s+clean\s+-fd`), "removes untracked files permanently"},
	{regexp.MustCompile(`git\s+push\s+.*--force(\s|$)`), "force push can overwrite remote history"},
	{regexp.MustCompile(`git\s+push\s+(.*\s)?-f(\s|$)`), "force push shorthand"},
	{regexp.MustCompile(`git\s+branch\s+-D`), "force delete branch loses unmerged work"},
	{regexp.MustCompile(`git\s+stash\s+drop`), "dropping stash loses saved work"},
	{regexp.MustCompile(`git\s+stash\s+clear`), "clearing all stashes"},

	// Database destructive patterns
	{regexp.MustCompile(`(?i)DROP\s+(DATABASE|TABLE|SCHEMA)\s+`), "dropping database objects"},
	{regexp.MustCompile(`(?i)TRUNCATE\s+TABLE\s+`), "truncating table data"},
	{regexp.MustCompile(`(?i)DELETE\s+FROM\s+\w+\s*(WHERE\s+1\s*=\s*1)?$`), "deleting all rows"},

	// Kubernetes/container patterns
	{regexp.MustCompile(`kubectl\s+delete\s+(ns|namespace)\s+`), "deleting Kubernetes namespace"},
	{regexp.MustCompile(`docker\s+system\s+prune\s+-a`), "pruning all Docker resources"},
	{regexp.MustCompile(`docker\s+rm\s+-f`), "force removing Docker containers"},

	// System destructive patterns
	{regexp.MustCompile(`chmod\s+-R\s+777\s+/`), "overly permissive chmod from root"},
	{regexp.MustCompile(`chown\s+-R\s+.*\s+/`), "recursive chown from root"},
	{regexp.MustCompile(`mkfs\.`), "formatting filesystem"},
	{regexp.MustCompile(`dd\s+.*of=/dev/`), "direct device write"},
}

// safePatterns are exceptions that should not trigger destructive warnings.
var safePatterns = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`git\s+push\s+.*--force-with-lease`), "force with lease is safer"},
	{regexp.MustCompile(`git\s+reset\s+--soft`), "soft reset preserves changes"},
	{regexp.MustCompile(`git\s+reset\s+HEAD~?\d*$`), "mixed reset preserves working directory"},
	{regexp.MustCompile(`rm\s+-rf\s+node_modules`), "removing node_modules is common"},
	{regexp.MustCompile(`rm\s+-rf\s+\.git/`), "removing .git subdirs is intentional"},
	{regexp.MustCompile(`rm\s+-rf\s+dist/`), "removing dist is common build cleanup"},
	{regexp.MustCompile(`rm\s+-rf\s+build/`), "removing build is common build cleanup"},
	{regexp.MustCompile(`rm\s+-rf\s+target/`), "removing target is common Rust cleanup"},
	{regexp.MustCompile(`rm\s+-rf\s+__pycache__`), "removing pycache is common"},
	{regexp.MustCompile(`rm\s+-rf\s+\.cache`), "removing cache dirs is common"},
}

// DefaultRuleSet returns the default set of lint rules.
func DefaultRuleSet() *RuleSet {
	return &RuleSet{
		Rules: map[RuleID]*Rule{
			RuleOversizedPromptBytes: {
				ID:       RuleOversizedPromptBytes,
				Enabled:  true,
				Severity: SeverityWarning,
				Config: map[string]any{
					ConfigKeyMaxBytes:  DefaultMaxBytes,
					ConfigKeyWarnBytes: DefaultWarnBytes,
				},
			},
			RuleOversizedPromptTokens: {
				ID:       RuleOversizedPromptTokens,
				Enabled:  true,
				Severity: SeverityWarning,
				Config: map[string]any{
					ConfigKeyMaxTokens:  DefaultMaxTokens,
					ConfigKeyWarnTokens: DefaultWarnTokens,
				},
			},
			RuleSecretDetected: {
				ID:       RuleSecretDetected,
				Enabled:  true,
				Severity: SeverityError, // Secrets should block by default
				Config: map[string]any{
					ConfigKeySecretMode: "block",
				},
			},
			RuleDestructiveCommand: {
				ID:       RuleDestructiveCommand,
				Enabled:  true,
				Severity: SeverityWarning, // Warn by default, can escalate
				Config:   nil,
			},
			RuleMissingContext: {
				ID:       RuleMissingContext,
				Enabled:  false, // Disabled by default, opt-in
				Severity: SeverityInfo,
				Config: map[string]any{
					ConfigKeyRequiredTags: []string{}, // User configures
				},
			},
			RulePIIDetected: {
				ID:       RulePIIDetected,
				Enabled:  true,
				Severity: SeverityWarning,
				Config:   nil,
			},
		},
	}
}

// StrictRuleSet returns a strict set of lint rules for high-security environments.
func StrictRuleSet() *RuleSet {
	rs := DefaultRuleSet()
	// Escalate severities
	rs.Rules[RuleOversizedPromptBytes].Severity = SeverityError
	rs.Rules[RuleOversizedPromptTokens].Severity = SeverityError
	rs.Rules[RuleDestructiveCommand].Severity = SeverityError
	rs.Rules[RulePIIDetected].Severity = SeverityError
	// Enable optional rules
	rs.Rules[RuleMissingContext].Enabled = true
	rs.Rules[RuleMissingContext].Severity = SeverityWarning
	return rs
}

// EstimateTokens provides a rough token estimate for a string.
// This uses a simple heuristic; for accurate counts, use a real tokenizer.
func EstimateTokens(s string) int {
	// Simple heuristic: ~4 chars per token for English/code
	// Account for whitespace, which tends to create token boundaries
	chars := len(s)
	if chars == 0 {
		return 0
	}
	// Whitespace and punctuation create more tokens
	spaces := strings.Count(s, " ")
	newlines := strings.Count(s, "\n")
	// Rough estimate: base + boundary tokens
	estimate := int(float64(chars)*TokensPerChar) + (spaces+newlines)/4
	return estimate
}

// CheckDestructive checks a prompt for destructive command patterns.
// Returns a list of findings for each detected pattern.
func CheckDestructive(prompt string, severity Severity) []Finding {
	var findings []Finding

	// Check each destructive pattern
	for _, dp := range destructivePatterns {
		matches := dp.pattern.FindAllStringIndex(prompt, -1)
		for _, match := range matches {
			// Check if this match is covered by a safe pattern
			matchText := prompt[match[0]:match[1]]
			if isSafeMatch(matchText) {
				continue
			}

			findings = append(findings, Finding{
				ID:       RuleDestructiveCommand,
				Severity: severity,
				Message:  "Destructive command detected: " + dp.reason,
				Help:     "Review this command carefully. Consider using safer alternatives or explicit confirmation.",
				Start:    match[0],
				End:      match[1],
				Metadata: map[string]any{
					"pattern": dp.pattern.String(),
					"match":   matchText,
					"reason":  dp.reason,
				},
			})
		}
	}

	return findings
}

// isSafeMatch checks if a matched command is covered by a safe pattern.
func isSafeMatch(matchText string) bool {
	for _, sp := range safePatterns {
		if sp.pattern.MatchString(matchText) {
			return true
		}
	}
	return false
}

// CheckSize checks prompt size against configured thresholds.
func CheckSize(prompt string, rules *RuleSet) []Finding {
	var findings []Finding
	byteCount := len(prompt)
	tokenEstimate := EstimateTokens(prompt)

	// Check byte limits
	if rule, ok := rules.Rules[RuleOversizedPromptBytes]; ok && rule.Enabled {
		warnBytes := getConfigInt(rule.Config, ConfigKeyWarnBytes, DefaultWarnBytes)
		maxBytes := getConfigInt(rule.Config, ConfigKeyMaxBytes, DefaultMaxBytes)

		if byteCount > maxBytes {
			findings = append(findings, Finding{
				ID:       RuleOversizedPromptBytes,
				Severity: SeverityError,
				Message:  "Prompt exceeds maximum size limit",
				Help:     "Reduce prompt size by removing unnecessary content or splitting into multiple messages.",
				Metadata: map[string]any{
					"byte_count": byteCount,
					"max_bytes":  maxBytes,
					"over_by":    byteCount - maxBytes,
				},
			})
		} else if byteCount > warnBytes {
			findings = append(findings, Finding{
				ID:       RuleOversizedPromptBytes,
				Severity: rule.Severity,
				Message:  "Prompt is approaching size limit",
				Help:     "Consider reducing prompt size to leave room for responses.",
				Metadata: map[string]any{
					"byte_count":  byteCount,
					"warn_bytes":  warnBytes,
					"max_bytes":   maxBytes,
					"usage_pct":   float64(byteCount) / float64(maxBytes) * 100,
					"room_before": maxBytes - byteCount,
				},
			})
		}
	}

	// Check token limits
	if rule, ok := rules.Rules[RuleOversizedPromptTokens]; ok && rule.Enabled {
		warnTokens := getConfigInt(rule.Config, ConfigKeyWarnTokens, DefaultWarnTokens)
		maxTokens := getConfigInt(rule.Config, ConfigKeyMaxTokens, DefaultMaxTokens)

		if tokenEstimate > maxTokens {
			findings = append(findings, Finding{
				ID:       RuleOversizedPromptTokens,
				Severity: SeverityError,
				Message:  "Prompt exceeds estimated token limit",
				Help:     "Reduce prompt content. Consider extracting code to files instead of inline.",
				Metadata: map[string]any{
					"token_estimate": tokenEstimate,
					"max_tokens":     maxTokens,
					"over_by":        tokenEstimate - maxTokens,
				},
			})
		} else if tokenEstimate > warnTokens {
			findings = append(findings, Finding{
				ID:       RuleOversizedPromptTokens,
				Severity: rule.Severity,
				Message:  "Prompt is approaching token limit",
				Help:     "Consider being more concise to leave room for model responses.",
				Metadata: map[string]any{
					"token_estimate": tokenEstimate,
					"warn_tokens":    warnTokens,
					"max_tokens":     maxTokens,
					"usage_pct":      float64(tokenEstimate) / float64(maxTokens) * 100,
				},
			})
		}
	}

	return findings
}

// CheckMissingContext checks for required context markers in the prompt.
func CheckMissingContext(prompt string, rules *RuleSet) []Finding {
	rule, ok := rules.Rules[RuleMissingContext]
	if !ok || !rule.Enabled {
		return nil
	}

	requiredTags, ok := rule.Config[ConfigKeyRequiredTags].([]string)
	if !ok || len(requiredTags) == 0 {
		return nil
	}

	var findings []Finding
	for _, tag := range requiredTags {
		if !strings.Contains(prompt, tag) {
			findings = append(findings, Finding{
				ID:       RuleMissingContext,
				Severity: rule.Severity,
				Message:  "Required context marker missing: " + tag,
				Help:     "Include the required marker in your prompt to ensure proper context.",
				Metadata: map[string]any{
					"missing_tag": tag,
				},
			})
		}
	}

	return findings
}

// getConfigInt safely retrieves an integer from config with a default.
func getConfigInt(config map[string]any, key string, defaultVal int) int {
	if config == nil {
		return defaultVal
	}
	v, ok := config[key]
	if !ok {
		return defaultVal
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return defaultVal
	}
}

// SetSeverity updates the severity for a rule.
func (rs *RuleSet) SetSeverity(id RuleID, severity Severity) {
	if rule, ok := rs.Rules[id]; ok {
		rule.Severity = severity
	}
}

// Disable disables a rule.
func (rs *RuleSet) Disable(id RuleID) {
	if rule, ok := rs.Rules[id]; ok {
		rule.Enabled = false
	}
}
