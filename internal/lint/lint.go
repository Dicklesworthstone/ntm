package lint

import (
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// Linter performs prompt validation against configured rules.
type Linter struct {
	rules    *RuleSet
	redactor *redaction.Config
}

// Option configures a Linter.
type Option func(*Linter)

// WithRuleSet sets the rule set for the linter.
func WithRuleSet(rs *RuleSet) Option {
	return func(l *Linter) {
		l.rules = rs
	}
}

// WithRedactionConfig sets the redaction config for secret detection.
func WithRedactionConfig(cfg *redaction.Config) Option {
	return func(l *Linter) {
		l.redactor = cfg
	}
}

// New creates a new Linter with the given options.
func New(opts ...Option) *Linter {
	l := &Linter{
		rules: DefaultRuleSet(),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Lint checks a prompt against all enabled rules and returns the result.
func (l *Linter) Lint(prompt string) *Result {
	result := &Result{
		Success:   true,
		Findings:  make([]Finding, 0),
		Stats:     l.computeStats(prompt),
		CheckedAt: time.Now(),
	}

	// Track which rules we check
	var appliedRules []RuleID

	// Check size rules
	if l.rules.Rules[RuleOversizedPromptBytes] != nil && l.rules.Rules[RuleOversizedPromptBytes].Enabled {
		appliedRules = append(appliedRules, RuleOversizedPromptBytes)
	}
	if l.rules.Rules[RuleOversizedPromptTokens] != nil && l.rules.Rules[RuleOversizedPromptTokens].Enabled {
		appliedRules = append(appliedRules, RuleOversizedPromptTokens)
	}
	sizeFindings := CheckSize(prompt, l.rules)
	result.Findings = append(result.Findings, sizeFindings...)

	// Check for secrets using the redaction engine
	if rule, ok := l.rules.Rules[RuleSecretDetected]; ok && rule.Enabled {
		appliedRules = append(appliedRules, RuleSecretDetected)
		secretFindings := l.checkSecrets(prompt, rule.Severity)
		result.Findings = append(result.Findings, secretFindings...)
	}

	// Check for destructive commands
	if rule, ok := l.rules.Rules[RuleDestructiveCommand]; ok && rule.Enabled {
		appliedRules = append(appliedRules, RuleDestructiveCommand)
		destructiveFindings := CheckDestructive(prompt, rule.Severity)
		result.Findings = append(result.Findings, destructiveFindings...)
	}

	// Check for missing context markers
	if l.rules.Rules[RuleMissingContext] != nil && l.rules.Rules[RuleMissingContext].Enabled {
		appliedRules = append(appliedRules, RuleMissingContext)
		contextFindings := CheckMissingContext(prompt, l.rules)
		result.Findings = append(result.Findings, contextFindings...)
	}

	// Check for PII (basic patterns)
	if rule, ok := l.rules.Rules[RulePIIDetected]; ok && rule.Enabled {
		appliedRules = append(appliedRules, RulePIIDetected)
		piiFindings := l.checkPII(prompt, rule.Severity)
		result.Findings = append(result.Findings, piiFindings...)
	}

	result.RulesApplied = appliedRules

	// Set success based on whether there are any errors
	result.Success = !result.HasErrors()

	return result
}

// LintWithRedaction performs linting and returns a redacted version of the prompt.
func (l *Linter) LintWithRedaction(prompt string) (*Result, string) {
	result := l.Lint(prompt)

	// If redaction is configured and there are secret findings, redact them
	if l.redactor != nil && len(result.FindingsByID(RuleSecretDetected)) > 0 {
		cfg := *l.redactor
		cfg.Mode = redaction.ModeRedact
		redactResult := redaction.ScanAndRedact(prompt, cfg)
		return result, redactResult.Output
	}

	return result, prompt
}

// checkSecrets uses the redaction engine to detect secrets.
func (l *Linter) checkSecrets(prompt string, severity Severity) []Finding {
	// Use warn mode to detect without modifying
	cfg := redaction.DefaultConfig()
	cfg.Mode = redaction.ModeWarn
	if l.redactor != nil {
		cfg = *l.redactor
		cfg.Mode = redaction.ModeWarn
	}

	scanResult := redaction.ScanAndRedact(prompt, cfg)

	var findings []Finding
	for _, f := range scanResult.Findings {
		findings = append(findings, Finding{
			ID:       RuleSecretDetected,
			Severity: severity,
			Message:  "Potential secret detected: " + string(f.Category),
			Help:     "Remove or redact the secret before sending. Use environment variables or config files instead.",
			Start:    f.Start,
			End:      f.End,
			Line:     f.Line,
			Metadata: map[string]any{
				"category":    string(f.Category),
				"redacted_as": f.Redacted,
			},
		})
	}

	return findings
}

// checkPII performs basic PII detection.
// This is a simplified check; for comprehensive PII detection,
// consider using a dedicated PII library.
func (l *Linter) checkPII(prompt string, severity Severity) []Finding {
	var findings []Finding

	// Email pattern
	emailPattern := `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`
	findings = append(findings, findPatternMatches(prompt, emailPattern, "email_address", severity,
		"Email address detected",
		"Consider redacting personal email addresses")...)

	// Phone patterns (US and international)
	phonePattern := `\b(?:\+?1[-.\s]?)?\(?[0-9]{3}\)?[-.\s]?[0-9]{3}[-.\s]?[0-9]{4}\b`
	findings = append(findings, findPatternMatches(prompt, phonePattern, "phone_number", severity,
		"Phone number detected",
		"Consider redacting personal phone numbers")...)

	// SSN pattern
	ssnPattern := `\b[0-9]{3}[-.\s]?[0-9]{2}[-.\s]?[0-9]{4}\b`
	findings = append(findings, findPatternMatches(prompt, ssnPattern, "ssn", severity,
		"Potential SSN detected",
		"Remove Social Security Numbers from prompts")...)

	// Credit card patterns
	ccPattern := `\b(?:4[0-9]{12}(?:[0-9]{3})?|5[1-5][0-9]{14}|3[47][0-9]{13}|6(?:011|5[0-9]{2})[0-9]{12})\b`
	findings = append(findings, findPatternMatches(prompt, ccPattern, "credit_card", severity,
		"Potential credit card number detected",
		"Remove credit card numbers from prompts")...)

	return findings
}

// findPatternMatches finds all matches of a pattern and creates findings.
func findPatternMatches(text, pattern, piiType string, severity Severity, message, help string) []Finding {
	var findings []Finding
	re, err := compilePattern(pattern)
	if err != nil {
		return nil
	}

	matches := re.FindAllStringIndex(text, -1)
	for _, match := range matches {
		findings = append(findings, Finding{
			ID:       RulePIIDetected,
			Severity: severity,
			Message:  message,
			Help:     help,
			Start:    match[0],
			End:      match[1],
			Metadata: map[string]any{
				"pii_type": piiType,
			},
		})
	}

	return findings
}

// computeStats calculates statistics about the prompt.
func (l *Linter) computeStats(prompt string) Stats {
	return Stats{
		ByteCount:     len(prompt),
		TokenEstimate: EstimateTokens(prompt),
		LineCount:     strings.Count(prompt, "\n") + 1,
	}
}

// patternCache caches compiled regex patterns.
var patternCache = make(map[string]*compiledPattern)

type compiledPattern struct {
	re  *regexp.Regexp
	err error
}

// compilePattern compiles a pattern with caching.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if cached, ok := patternCache[pattern]; ok {
		return cached.re, cached.err
	}
	re, err := regexp.Compile(pattern)
	patternCache[pattern] = &compiledPattern{re: re, err: err}
	return re, err
}
