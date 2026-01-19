package handoff

import (
	"log/slog"
	"regexp"
	"time"
)

var (
	// sessionNameRegex matches valid session names: alphanumeric with _ or -.
	sessionNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	// dateRegex matches YYYY-MM-DD format.
	dateRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Validate checks the handoff for required fields and correct values.
// Returns ALL validation errors (not just the first) for comprehensive logging.
// Use IsValid() for a simple boolean check.
func (h *Handoff) Validate() ValidationErrors {
	var errs ValidationErrors

	// Required fields - Goal and Now are required for status line
	if h.Goal == "" {
		errs = append(errs, ValidationError{
			Field:   "goal",
			Message: "required field missing - status line depends on this",
		})
	}
	if h.Now == "" {
		errs = append(errs, ValidationError{
			Field:   "now",
			Message: "required field missing - status line depends on this",
		})
	}

	// Session validation (if provided and not "general")
	if h.Session != "" && h.Session != "general" {
		if !sessionNameRegex.MatchString(h.Session) {
			errs = append(errs, ValidationError{
				Field:   "session",
				Message: "must be alphanumeric with _ or - only",
				Value:   h.Session,
			})
		}
	}

	// Date format validation (if provided)
	if h.Date != "" && !dateRegex.MatchString(h.Date) {
		errs = append(errs, ValidationError{
			Field:   "date",
			Message: "must be YYYY-MM-DD format",
			Value:   h.Date,
		})
	}

	// Status validation
	if !ValidStatuses[h.Status] {
		errs = append(errs, ValidationError{
			Field:   "status",
			Message: "must be complete, partial, or blocked",
			Value:   h.Status,
		})
	}

	// Outcome validation
	if !ValidOutcomes[h.Outcome] {
		errs = append(errs, ValidationError{
			Field:   "outcome",
			Message: "must be SUCCEEDED, PARTIAL_PLUS, PARTIAL_MINUS, or FAILED",
			Value:   h.Outcome,
		})
	}

	// AgentType validation (if provided)
	if h.AgentType != "" && !ValidAgentTypes[h.AgentType] {
		errs = append(errs, ValidationError{
			Field:   "agent_type",
			Message: "must be cc, cod, or gmi",
			Value:   h.AgentType,
		})
	}

	// TokensPct validation (if tokens are set)
	if h.TokensMax > 0 && h.TokensUsed > 0 {
		if h.TokensPct < 0 || h.TokensPct > 100 {
			errs = append(errs, ValidationError{
				Field:   "tokens_pct",
				Message: "must be between 0 and 100",
				Value:   h.TokensPct,
			})
		}
	}

	// Version validation (if provided)
	if h.Version != "" && h.Version != HandoffVersion {
		slog.Debug("handoff version mismatch",
			"expected", HandoffVersion,
			"actual", h.Version,
			"session", h.Session,
		)
		// Not an error - we support older versions for migration
	}

	// Log all validation errors at DEBUG level for troubleshooting
	for _, err := range errs {
		slog.Debug("handoff validation error",
			"field", err.Field,
			"message", err.Message,
			"value", err.Value,
			"session", h.Session,
		)
	}

	return errs
}

// IsValid returns true if the handoff passes validation.
func (h *Handoff) IsValid() bool {
	return len(h.Validate()) == 0
}

// SetDefaults populates default values for optional fields.
// This should be called before serialization to ensure consistent output.
func (h *Handoff) SetDefaults() {
	now := time.Now()

	if h.Version == "" {
		h.Version = HandoffVersion
	}
	if h.Date == "" {
		h.Date = now.Format("2006-01-02")
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = now
	}
	h.UpdatedAt = now

	slog.Debug("handoff defaults set",
		"version", h.Version,
		"date", h.Date,
		"created_at", h.CreatedAt,
		"updated_at", h.UpdatedAt,
		"session", h.Session,
	)
}

// ValidateAndSetDefaults combines SetDefaults and Validate for convenience.
// Returns validation errors after setting defaults.
func (h *Handoff) ValidateAndSetDefaults() ValidationErrors {
	h.SetDefaults()
	errs := h.Validate()

	if len(errs) == 0 {
		slog.Info("handoff validation passed",
			"session", h.Session,
			"goal", truncate(h.Goal, 50),
			"status", h.Status,
		)
	}

	return errs
}

// truncate shortens a string for logging purposes.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ValidateMinimal performs a minimal validation check for just the required fields.
// Use this for quick checks where full validation is not needed.
func (h *Handoff) ValidateMinimal() error {
	if h.Goal == "" {
		return ValidationError{
			Field:   "goal",
			Message: "required field missing",
		}
	}
	if h.Now == "" {
		return ValidationError{
			Field:   "now",
			Message: "required field missing",
		}
	}
	return nil
}

// MustValidate panics if validation fails. Use for testing or when validation
// failure should be impossible (e.g., programmatically constructed handoffs).
func (h *Handoff) MustValidate() {
	if errs := h.Validate(); len(errs) > 0 {
		panic("handoff validation failed: " + errs.Error())
	}
}
