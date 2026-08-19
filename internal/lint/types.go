// Package lint provides prompt validation rules for the preflight system.
// It defines lint rules that detect issues like oversized prompts, secrets,
// destructive commands, and missing context markers.
package lint

import "time"

// Severity indicates the importance of a lint finding.
type Severity string

const (
	// SeverityInfo is for informational findings that don't require action.
	SeverityInfo Severity = "info"
	// SeverityWarning indicates potential issues that should be reviewed.
	SeverityWarning Severity = "warning"
	// SeverityError indicates critical issues that should block the operation.
	SeverityError Severity = "error"
)

// RuleID is a stable identifier for a lint rule.
type RuleID string

const (
	// RuleOversizedPromptBytes triggers when prompt exceeds byte limit.
	RuleOversizedPromptBytes RuleID = "oversized_prompt_bytes"
	// RuleOversizedPromptTokens triggers when prompt exceeds token estimate.
	RuleOversizedPromptTokens RuleID = "oversized_prompt_tokens"
	// RuleSecretDetected triggers when secrets/API keys are found.
	RuleSecretDetected RuleID = "secret_detected"
	// RuleDestructiveCommand triggers when dangerous commands are found.
	RuleDestructiveCommand RuleID = "destructive_command"
	// RuleMissingContext triggers when required context markers are missing.
	RuleMissingContext RuleID = "missing_context"
	// RulePIIDetected triggers when potential PII is found.
	RulePIIDetected RuleID = "pii_detected"
)

// Finding represents a single lint rule violation.
type Finding struct {
	// ID is the stable rule identifier.
	ID RuleID `json:"id"`
	// Severity indicates the importance of this finding.
	Severity Severity `json:"severity"`
	// Message is a human-readable description of the issue.
	Message string `json:"message"`
	// Help provides guidance on how to fix the issue.
	Help string `json:"help"`
	// Metadata contains additional context about the finding.
	Metadata map[string]any `json:"metadata,omitempty"`
	// Start is the byte offset where the issue begins (optional).
	Start int `json:"start,omitempty"`
	// End is the byte offset where the issue ends (optional).
	End int `json:"end,omitempty"`
	// Line is the 1-indexed line number (optional).
	Line int `json:"line,omitempty"`
}

// Rule defines a lint rule configuration.
type Rule struct {
	// ID is the stable rule identifier.
	ID RuleID `json:"id"`
	// Enabled controls whether this rule is active.
	Enabled bool `json:"enabled"`
	// Severity is the severity level for violations.
	Severity Severity `json:"severity"`
	// Config contains rule-specific configuration.
	Config map[string]any `json:"config,omitempty"`
}

// RuleSet represents a collection of lint rules.
type RuleSet struct {
	// Rules maps rule IDs to their configurations.
	Rules map[RuleID]*Rule `json:"rules"`
}

// Result contains the outcome of a lint operation.
type Result struct {
	// Success is true if no blocking errors were found.
	Success bool `json:"success"`
	// Findings is the list of detected issues.
	Findings []Finding `json:"findings"`
	// Stats contains metrics about the prompt.
	Stats Stats `json:"stats"`
	// CheckedAt is when the lint was performed.
	CheckedAt time.Time `json:"checked_at"`
	// RulesApplied lists which rules were checked.
	RulesApplied []RuleID `json:"rules_applied"`
}

// Stats contains metrics about the prompt.
type Stats struct {
	// ByteCount is the size of the prompt in bytes.
	ByteCount int `json:"byte_count"`
	// TokenEstimate is the estimated token count.
	TokenEstimate int `json:"token_estimate"`
	// LineCount is the number of lines in the prompt.
	LineCount int `json:"line_count"`
}

// HasErrors returns true if there are any error-severity findings.
func (r *Result) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}
