package handoff

import "fmt"

// Status values for handoff status field.
const (
	// StatusComplete indicates the session completed all planned work.
	StatusComplete = "complete"
	// StatusPartial indicates the session completed some work but not all.
	StatusPartial = "partial"
	// StatusBlocked indicates the session is blocked and cannot proceed.
	StatusBlocked = "blocked"
)

// ValidStatuses is the set of valid status values.
var ValidStatuses = map[string]bool{
	StatusComplete: true,
	StatusPartial:  true,
	StatusBlocked:  true,
	"":             true, // Empty is allowed (optional field)
}

// Outcome values for handoff outcome field.
// These provide more granular detail than status.
const (
	// OutcomeSucceeded indicates all goals were achieved.
	OutcomeSucceeded = "SUCCEEDED"
	// OutcomePartialPlus indicates most goals achieved, some work remains.
	OutcomePartialPlus = "PARTIAL_PLUS"
	// OutcomePartialMinus indicates some goals achieved but significant work remains.
	OutcomePartialMinus = "PARTIAL_MINUS"
	// OutcomeFailed indicates the session failed to achieve its goals.
	OutcomeFailed = "FAILED"
)

// ValidOutcomes is the set of valid outcome values.
var ValidOutcomes = map[string]bool{
	OutcomeSucceeded:    true,
	OutcomePartialPlus:  true,
	OutcomePartialMinus: true,
	OutcomeFailed:       true,
	"":                  true, // Empty is allowed (optional field)
}

// AgentType values for identifying agent types.
const (
	AgentTypeClaude = "cc"
	AgentTypeCodex  = "cod"
	AgentTypeGemini = "gmi"
)

// ValidAgentTypes is the set of valid agent type values.
var ValidAgentTypes = map[string]bool{
	AgentTypeClaude: true,
	AgentTypeCodex:  true,
	AgentTypeGemini: true,
	"":              true, // Empty is allowed (optional field)
}

// ValidationError represents a validation error with structured fields.
type ValidationError struct {
	Field   string      // The field that failed validation
	Message string      // Human-readable error message
	Value   interface{} // The invalid value (for logging)
}

// Error implements the error interface.
func (e ValidationError) Error() string {
	if e.Value != nil {
		return fmt.Sprintf("validation error: field=%s msg=%s value=%v", e.Field, e.Message, e.Value)
	}
	return fmt.Sprintf("validation error: field=%s msg=%s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation errors.
type ValidationErrors []ValidationError

// Error implements the error interface for multiple errors.
func (errs ValidationErrors) Error() string {
	if len(errs) == 0 {
		return "no validation errors"
	}
	if len(errs) == 1 {
		return errs[0].Error()
	}
	return fmt.Sprintf("%d validation errors: first=%s", len(errs), errs[0].Error())
}

// HasErrors returns true if there are any validation errors.
func (errs ValidationErrors) HasErrors() bool {
	return len(errs) > 0
}

// FieldNames returns the list of fields with errors.
func (errs ValidationErrors) FieldNames() []string {
	names := make([]string, len(errs))
	for i, err := range errs {
		names[i] = err.Field
	}
	return names
}

// ForField returns all errors for a specific field.
func (errs ValidationErrors) ForField(field string) ValidationErrors {
	var result ValidationErrors
	for _, err := range errs {
		if err.Field == field {
			result = append(result, err)
		}
	}
	return result
}
