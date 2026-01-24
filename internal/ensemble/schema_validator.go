package ensemble

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ValidationError represents a schema validation error with context.
type ValidationError struct {
	// Field is the path to the field that failed validation (e.g., "findings[2].confidence").
	Field string `json:"field"`

	// Message describes what is wrong.
	Message string `json:"message"`

	// Value is the invalid value (may be nil for missing required fields).
	Value interface{} `json:"value,omitempty"`
}

// Error implements the error interface.
func (e ValidationError) Error() string {
	if e.Value != nil {
		return fmt.Sprintf("%s: %s (got %v)", e.Field, e.Message, e.Value)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// SchemaValidator validates ModeOutput structures against the required schema.
type SchemaValidator struct {
	// CheckFileExists controls whether evidence pointers are validated against
	// the filesystem. Defaults to false for safety in untrusted environments.
	CheckFileExists bool

	// BaseDir is the root directory for resolving relative file paths in
	// evidence pointers. Only used when CheckFileExists is true.
	BaseDir string
}

// NewSchemaValidator creates a new validator with default settings.
func NewSchemaValidator() *SchemaValidator {
	return &SchemaValidator{
		CheckFileExists: false,
		BaseDir:         ".",
	}
}

// Validate performs comprehensive validation on a ModeOutput.
// Returns a slice of ValidationErrors, which is empty if validation passes.
func (v *SchemaValidator) Validate(output *ModeOutput) []ValidationError {
	var errs []ValidationError

	// Required field: mode_id
	if output.ModeID == "" {
		errs = append(errs, ValidationError{
			Field:   "mode_id",
			Message: "required field is missing",
		})
	}

	// Required field: thesis
	if output.Thesis == "" {
		errs = append(errs, ValidationError{
			Field:   "thesis",
			Message: "required field is missing",
		})
	}

	// Required field: top_findings (at least one)
	if len(output.TopFindings) == 0 {
		errs = append(errs, ValidationError{
			Field:   "top_findings",
			Message: "at least one finding is required",
		})
	}

	// Validate confidence range
	if err := output.Confidence.Validate(); err != nil {
		errs = append(errs, ValidationError{
			Field:   "confidence",
			Message: "must be between 0.0 and 1.0",
			Value:   float64(output.Confidence),
		})
	}

	// Validate findings
	errs = append(errs, v.ValidateFindings(output.TopFindings)...)

	// Validate evidence pointers if enabled
	if v.CheckFileExists {
		errs = append(errs, v.ValidateEvidencePointers(output.TopFindings)...)
	}

	// Validate risks
	for i, r := range output.Risks {
		errs = append(errs, v.validateRisk(i, &r)...)
	}

	// Validate recommendations
	for i, r := range output.Recommendations {
		errs = append(errs, v.validateRecommendation(i, &r)...)
	}

	// Validate questions
	for i, q := range output.QuestionsForUser {
		errs = append(errs, v.validateQuestion(i, &q)...)
	}

	// Validate failure modes
	for i, f := range output.FailureModesToWatch {
		errs = append(errs, v.validateFailureMode(i, &f)...)
	}

	return errs
}

// ValidateFindings validates a slice of findings.
func (v *SchemaValidator) ValidateFindings(findings []Finding) []ValidationError {
	var errs []ValidationError

	for i, f := range findings {
		prefix := fmt.Sprintf("top_findings[%d]", i)

		// Required field: finding description
		if f.Finding == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".finding",
				Message: "required field is missing",
			})
		}

		// Validate impact level enum
		if !f.Impact.IsValid() {
			errs = append(errs, ValidationError{
				Field:   prefix + ".impact",
				Message: "must be one of: high, medium, low",
				Value:   string(f.Impact),
			})
		}

		// Validate confidence range
		if err := f.Confidence.Validate(); err != nil {
			errs = append(errs, ValidationError{
				Field:   prefix + ".confidence",
				Message: "must be between 0.0 and 1.0",
				Value:   float64(f.Confidence),
			})
		}
	}

	return errs
}

// ValidateEvidencePointers checks that evidence pointers reference real files.
// Evidence pointers should be in the format "path/to/file.go:42" or just "path/to/file.go".
func (v *SchemaValidator) ValidateEvidencePointers(findings []Finding) []ValidationError {
	var errs []ValidationError

	for i, f := range findings {
		if f.EvidencePointer == "" {
			continue
		}

		// Parse evidence pointer (file:line or just file)
		parts := strings.SplitN(f.EvidencePointer, ":", 2)
		filePath := parts[0]

		// Resolve relative paths
		if !strings.HasPrefix(filePath, "/") {
			filePath = v.BaseDir + "/" + filePath
		}

		// Check file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("top_findings[%d].evidence_pointer", i),
				Message: "file does not exist",
				Value:   f.EvidencePointer,
			})
		}
	}

	return errs
}

// ParseYAML parses raw YAML output into a ModeOutput struct.
func (v *SchemaValidator) ParseYAML(raw string) (*ModeOutput, error) {
	var output ModeOutput
	if err := yaml.Unmarshal([]byte(raw), &output); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}
	output.RawOutput = raw
	return &output, nil
}

// ParseAndValidate combines parsing and validation in a single call.
func (v *SchemaValidator) ParseAndValidate(raw string) (*ModeOutput, []ValidationError, error) {
	output, err := v.ParseYAML(raw)
	if err != nil {
		return nil, nil, err
	}
	errs := v.Validate(output)
	return output, errs, nil
}

// Normalize applies normalization to a ModeOutput, injecting defaults and
// converting flexible input formats to canonical values.
// Returns a slice of ValidationErrors for values that couldn't be normalized.
//
// Normalization includes:
// - Injecting mode_id if missing (from modeID parameter)
// - Normalizing confidence/likelihood strings ("high"/"medium"/"low") to floats
// - Normalizing impact levels (case-insensitive)
func (v *SchemaValidator) Normalize(output *ModeOutput, modeID string) []ValidationError {
	var errs []ValidationError

	// Inject mode_id if missing
	if output.ModeID == "" && modeID != "" {
		output.ModeID = modeID
	}

	// Normalize impact levels (case-insensitive)
	for i := range output.TopFindings {
		normalized := ImpactLevel(strings.ToLower(string(output.TopFindings[i].Impact)))
		if normalized.IsValid() {
			output.TopFindings[i].Impact = normalized
		}
	}

	for i := range output.Risks {
		normalized := ImpactLevel(strings.ToLower(string(output.Risks[i].Impact)))
		if normalized.IsValid() {
			output.Risks[i].Impact = normalized
		}
	}

	for i := range output.Recommendations {
		normalized := ImpactLevel(strings.ToLower(string(output.Recommendations[i].Priority)))
		if normalized.IsValid() {
			output.Recommendations[i].Priority = normalized
		}
	}

	return errs
}

// ParseNormalizeAndValidate combines parsing, normalization, and validation.
// This is the recommended entry point for processing mode output.
func (v *SchemaValidator) ParseNormalizeAndValidate(raw string, modeID string) (*ModeOutput, []ValidationError, error) {
	output, err := v.ParseYAML(raw)
	if err != nil {
		return nil, nil, err
	}
	// Normalize first (e.g., inject mode_id), then validate
	normErrs := v.Normalize(output, modeID)
	valErrs := v.Validate(output)
	return output, append(normErrs, valErrs...), nil
}

// validateRisk validates a single risk entry.
func (v *SchemaValidator) validateRisk(index int, r *Risk) []ValidationError {
	var errs []ValidationError
	prefix := fmt.Sprintf("risks[%d]", index)

	if r.Risk == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".risk",
			Message: "required field is missing",
		})
	}

	if !r.Impact.IsValid() {
		errs = append(errs, ValidationError{
			Field:   prefix + ".impact",
			Message: "must be one of: high, medium, low",
			Value:   string(r.Impact),
		})
	}

	if err := r.Likelihood.Validate(); err != nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".likelihood",
			Message: "must be between 0.0 and 1.0",
			Value:   float64(r.Likelihood),
		})
	}

	return errs
}

// validateRecommendation validates a single recommendation entry.
func (v *SchemaValidator) validateRecommendation(index int, r *Recommendation) []ValidationError {
	var errs []ValidationError
	prefix := fmt.Sprintf("recommendations[%d]", index)

	if r.Recommendation == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".recommendation",
			Message: "required field is missing",
		})
	}

	if !r.Priority.IsValid() {
		errs = append(errs, ValidationError{
			Field:   prefix + ".priority",
			Message: "must be one of: high, medium, low",
			Value:   string(r.Priority),
		})
	}

	return errs
}

// validateQuestion validates a single question entry.
func (v *SchemaValidator) validateQuestion(index int, q *Question) []ValidationError {
	var errs []ValidationError
	prefix := fmt.Sprintf("questions_for_user[%d]", index)

	if q.Question == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".question",
			Message: "required field is missing",
		})
	}

	return errs
}

// validateFailureMode validates a single failure mode warning entry.
func (v *SchemaValidator) validateFailureMode(index int, f *FailureModeWarning) []ValidationError {
	var errs []ValidationError
	prefix := fmt.Sprintf("failure_modes_to_watch[%d]", index)

	if f.Mode == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".mode",
			Message: "required field is missing",
		})
	}

	if f.Description == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".description",
			Message: "required field is missing",
		})
	}

	return errs
}
