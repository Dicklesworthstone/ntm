package ensemble

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// OutputCollector aggregates and validates mode outputs for synthesis.
type OutputCollector struct {
	// Outputs holds the collected mode outputs.
	Outputs []ModeOutput

	// ValidationErrors tracks schema violations per mode.
	ValidationErrors map[string][]string

	// CollectedAt is when collection completed.
	CollectedAt time.Time

	// Config controls collection behavior.
	Config OutputCollectorConfig
}

// CollectorResult wraps the collected outputs with metadata.
type CollectorResult struct {
	// ValidOutputs are outputs that passed validation.
	ValidOutputs []ModeOutput `json:"valid_outputs"`

	// InvalidOutputs are outputs that failed validation.
	InvalidOutputs []InvalidOutput `json:"invalid_outputs,omitempty"`

	// Stats provides collection statistics.
	Stats CollectorStats `json:"stats"`

	// CollectedAt is when collection completed.
	CollectedAt time.Time `json:"collected_at"`
}

// InvalidOutput pairs a mode output with its validation errors.
type InvalidOutput struct {
	ModeID string   `json:"mode_id"`
	Errors []string `json:"errors"`
	// RawOutput preserves the original for debugging.
	RawOutput string `json:"raw_output,omitempty"`
}

// CollectorStats summarizes the collection.
type CollectorStats struct {
	TotalReceived int `json:"total_received"`
	ValidCount    int `json:"valid_count"`
	InvalidCount  int `json:"invalid_count"`
	MissingCount  int `json:"missing_count,omitempty"`
	TotalTokens   int `json:"total_tokens,omitempty"`
	AverageTokens int `json:"average_tokens,omitempty"`
}

// SynthesisInput is the validated input for the synthesis stage.
type SynthesisInput struct {
	// Outputs are the validated mode outputs.
	Outputs []ModeOutput `json:"outputs"`

	// ContextPack is the shared context for synthesis.
	ContextPack *ContextPack `json:"context_pack,omitempty"`

	// OriginalQuestion is the problem being analyzed.
	OriginalQuestion string `json:"original_question"`

	// Config is the synthesis configuration.
	Config SynthesisConfig `json:"config"`

	// AuditReport is the pre-computed disagreement analysis.
	AuditReport *AuditReport `json:"audit_report,omitempty"`

	// Provenance tracks finding lineage across merge and synthesis.
	Provenance *ProvenanceTracker `json:"-" yaml:"-"`
}

// NewOutputCollector creates a collector with the given config.
func NewOutputCollector(cfg OutputCollectorConfig) *OutputCollector {
	return &OutputCollector{
		Outputs:          make([]ModeOutput, 0),
		ValidationErrors: make(map[string][]string),
		Config:           cfg,
	}
}

// Add adds an output to the collector, validating it.
func (c *OutputCollector) Add(output ModeOutput) error {
	if c == nil {
		return errors.New("collector is nil")
	}

	errs := c.validate(output)
	if len(errs) > 0 {
		c.ValidationErrors[output.ModeID] = errs
		if c.Config.RequireAll {
			return fmt.Errorf("output validation failed for %s: %s", output.ModeID, strings.Join(errs, "; "))
		}
		// Non-fatal: track error but continue
		return nil
	}

	c.Outputs = append(c.Outputs, output)
	return nil
}

// AddRaw parses and adds a raw JSON output.
func (c *OutputCollector) AddRaw(modeID string, rawJSON string) error {
	if c == nil {
		return errors.New("collector is nil")
	}
	if strings.TrimSpace(rawJSON) == "" {
		c.ValidationErrors[modeID] = []string{"empty output"}
		return nil
	}

	var output ModeOutput
	if err := json.Unmarshal([]byte(rawJSON), &output); err != nil {
		c.ValidationErrors[modeID] = []string{fmt.Sprintf("JSON parse error: %v", err)}
		return nil
	}

	if output.ModeID == "" {
		output.ModeID = modeID
	}
	output.RawOutput = rawJSON

	return c.Add(output)
}

// Collect finalizes collection and returns the result.
func (c *OutputCollector) Collect() (*CollectorResult, error) {
	if c == nil {
		return nil, errors.New("collector is nil")
	}

	c.CollectedAt = time.Now().UTC()

	result := &CollectorResult{
		ValidOutputs:   c.Outputs,
		InvalidOutputs: c.buildInvalidOutputs(),
		Stats: CollectorStats{
			TotalReceived: len(c.Outputs) + len(c.ValidationErrors),
			ValidCount:    len(c.Outputs),
			InvalidCount:  len(c.ValidationErrors),
		},
		CollectedAt: c.CollectedAt,
	}

	if len(result.ValidOutputs) > 0 {
		totalTokens := EstimateModeOutputsTokens(result.ValidOutputs)
		result.Stats.TotalTokens = totalTokens
		result.Stats.AverageTokens = totalTokens / len(result.ValidOutputs)
	}

	// Check minimum outputs
	if len(c.Outputs) < c.Config.MinOutputs {
		return result, fmt.Errorf(
			"insufficient valid outputs: got %d, need %d",
			len(c.Outputs),
			c.Config.MinOutputs,
		)
	}

	return result, nil
}

// BuildSynthesisInput creates the input for synthesis from collected outputs.
func (c *OutputCollector) BuildSynthesisInput(question string, pack *ContextPack, cfg SynthesisConfig) (*SynthesisInput, error) {
	result, err := c.Collect()
	if err != nil {
		return nil, err
	}

	if len(result.ValidOutputs) == 0 {
		return nil, errors.New("no valid outputs for synthesis")
	}

	// Run disagreement audit
	auditor := NewDisagreementAuditor(result.ValidOutputs, nil)
	auditReport, _ := auditor.Audit() // Ignore audit errors; it's informational

	return &SynthesisInput{
		Outputs:          result.ValidOutputs,
		ContextPack:      pack,
		OriginalQuestion: question,
		Config:           cfg,
		AuditReport:      auditReport,
	}, nil
}

// validate checks an output against the schema.
func (c *OutputCollector) validate(output ModeOutput) []string {
	var errs []string

	// Core required fields
	if strings.TrimSpace(output.ModeID) == "" {
		errs = append(errs, "mode_id is required")
	}
	if strings.TrimSpace(output.Thesis) == "" {
		errs = append(errs, "thesis is required")
	}
	if len(output.TopFindings) == 0 {
		errs = append(errs, "at least one finding is required")
	}

	// Confidence validation
	if err := output.Confidence.Validate(); err != nil {
		errs = append(errs, fmt.Sprintf("invalid confidence: %v", err))
	}

	// Validate individual findings
	for i, f := range output.TopFindings {
		if err := f.Validate(); err != nil {
			errs = append(errs, fmt.Sprintf("finding[%d]: %v", i, err))
		}
	}

	// Validate risks
	for i, r := range output.Risks {
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Sprintf("risk[%d]: %v", i, err))
		}
	}

	// Validate recommendations
	for i, r := range output.Recommendations {
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Sprintf("recommendation[%d]: %v", i, err))
		}
	}

	return errs
}

// buildInvalidOutputs converts validation errors to InvalidOutput records.
func (c *OutputCollector) buildInvalidOutputs() []InvalidOutput {
	if len(c.ValidationErrors) == 0 {
		return nil
	}

	invalid := make([]InvalidOutput, 0, len(c.ValidationErrors))
	for modeID, errs := range c.ValidationErrors {
		invalid = append(invalid, InvalidOutput{
			ModeID: modeID,
			Errors: errs,
		})
	}
	return invalid
}

// Count returns the number of collected outputs.
func (c *OutputCollector) Count() int {
	if c == nil {
		return 0
	}
	return len(c.Outputs)
}

// ErrorCount returns the number of validation errors.
func (c *OutputCollector) ErrorCount() int {
	if c == nil {
		return 0
	}
	return len(c.ValidationErrors)
}

// HasEnough returns true if minimum output threshold is met.
func (c *OutputCollector) HasEnough() bool {
	if c == nil {
		return false
	}
	return len(c.Outputs) >= c.Config.MinOutputs
}

// Reset clears all collected outputs and errors.
func (c *OutputCollector) Reset() {
	if c == nil {
		return
	}
	c.Outputs = make([]ModeOutput, 0)
	c.ValidationErrors = make(map[string][]string)
	c.CollectedAt = time.Time{}
}

// CollectFromSession uses OutputCapture to collect outputs from an EnsembleSession.
func (c *OutputCollector) CollectFromSession(session *EnsembleSession, capture *OutputCapture) error {
	if c == nil {
		return errors.New("collector is nil")
	}
	if session == nil {
		return errors.New("session is nil")
	}
	if capture == nil {
		return errors.New("output capture is nil")
	}

	captured, err := capture.CaptureAll(session)
	if err != nil {
		return fmt.Errorf("capture all: %w", err)
	}

	return c.CollectFromCaptures(captured)
}

// CollectFromCaptures processes pre-captured outputs and adds them to the collector.
func (c *OutputCollector) CollectFromCaptures(captured []CapturedOutput) error {
	return c.CollectFromCapturesFiltered(captured, nil)
}

// CollectFromCapturesFiltered processes captured outputs that match the include filter.
func (c *OutputCollector) CollectFromCapturesFiltered(captured []CapturedOutput, include func(CapturedOutput) bool) error {
	if c == nil {
		return errors.New("collector is nil")
	}

	for _, cap := range captured {
		if include != nil && !include(cap) {
			continue
		}
		if cap.Parsed != nil {
			// Use pre-parsed output
			output := *cap.Parsed
			if output.ModeID == "" {
				output.ModeID = cap.ModeID
			}
			normalizeOutput(&output)
			if err := c.Add(output); err != nil {
				return fmt.Errorf("add output %s: %w", cap.ModeID, err)
			}
		} else if cap.RawOutput != "" {
			// Try to parse raw output as JSON
			if err := c.AddRaw(cap.ModeID, cap.RawOutput); err != nil {
				return fmt.Errorf("add raw output %s: %w", cap.ModeID, err)
			}
		} else {
			// Record capture errors
			errStrs := make([]string, 0, len(cap.ParseErrors))
			for _, e := range cap.ParseErrors {
				errStrs = append(errStrs, e.Error())
			}
			if len(errStrs) == 0 {
				errStrs = append(errStrs, "empty output")
			}
			c.ValidationErrors[cap.ModeID] = errStrs
		}
	}

	return nil
}

// CollectFromSavedOutputs loads completed mode outputs from persisted output files.
// This is used when an ensemble session is no longer live in tmux but saved mode
// outputs are still available on disk.
func (c *OutputCollector) CollectFromSavedOutputs(session *EnsembleSession) error {
	if c == nil {
		return errors.New("collector is nil")
	}
	if session == nil {
		return errors.New("ensemble session is nil")
	}

	validator := NewSchemaValidator()

	for _, assignment := range session.Assignments {
		if assignment.Status != AssignmentDone {
			continue
		}

		path := strings.TrimSpace(assignment.OutputPath)
		if path == "" {
			c.ValidationErrors[assignment.ModeID] = []string{"missing output_path for completed mode"}
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			c.ValidationErrors[assignment.ModeID] = []string{fmt.Sprintf("read saved output %q: %v", path, err)}
			continue
		}

		parsed, _, err := validator.ParseNormalizeAndValidate(string(raw), assignment.ModeID)
		if err != nil {
			c.ValidationErrors[assignment.ModeID] = []string{fmt.Sprintf("parse saved output %q: %v", path, err)}
			continue
		}
		if parsed == nil {
			c.ValidationErrors[assignment.ModeID] = []string{fmt.Sprintf("empty saved output %q", path)}
			continue
		}

		parsed.RawOutput = string(raw)
		normalizeOutput(parsed)
		if err := c.Add(*parsed); err != nil {
			return fmt.Errorf("add saved output %s: %w", assignment.ModeID, err)
		}
	}

	return nil
}

// CollectModeOutputs merges live-captured and saved outputs for a session.
// Captured outputs win per mode when both sources exist; saved outputs fill in
// missing modes. Validation errors for a mode are cleared once a valid output
// for that mode is available from either source.
func CollectModeOutputs(session *EnsembleSession, captured []CapturedOutput) (*OutputCollector, error) {
	if session == nil {
		return nil, errors.New("ensemble session is nil")
	}

	collector := NewOutputCollector(DefaultOutputCollectorConfig())
	if err := collector.CollectFromCapturesFiltered(captured, nil); err != nil {
		return nil, fmt.Errorf("collect captured outputs: %w", err)
	}

	collectedModes := make(map[string]struct{}, len(collector.Outputs))
	for _, output := range collector.Outputs {
		collectedModes[output.ModeID] = struct{}{}
		delete(collector.ValidationErrors, output.ModeID)
	}

	savedCollector := NewOutputCollector(DefaultOutputCollectorConfig())
	if err := savedCollector.CollectFromSavedOutputs(session); err != nil {
		return nil, fmt.Errorf("collect saved outputs: %w", err)
	}

	for _, output := range savedCollector.Outputs {
		if _, exists := collectedModes[output.ModeID]; exists {
			delete(collector.ValidationErrors, output.ModeID)
			continue
		}
		if err := collector.Add(output); err != nil {
			return nil, fmt.Errorf("add saved output %s: %w", output.ModeID, err)
		}
		collectedModes[output.ModeID] = struct{}{}
		delete(collector.ValidationErrors, output.ModeID)
	}

	for modeID, errs := range savedCollector.ValidationErrors {
		if _, exists := collectedModes[modeID]; exists {
			continue
		}
		if _, exists := collector.ValidationErrors[modeID]; !exists {
			collector.ValidationErrors[modeID] = errs
		}
	}

	sort.Slice(collector.Outputs, func(i, j int) bool {
		if collector.Outputs[i].ModeID == collector.Outputs[j].ModeID {
			return collector.Outputs[i].GeneratedAt.Before(collector.Outputs[j].GeneratedAt)
		}
		return collector.Outputs[i].ModeID < collector.Outputs[j].ModeID
	})

	return collector, nil
}

// normalizeOutput applies default values to zero-valued fields.
// String-to-float conversion is handled by the schema validator during YAML/JSON parsing.
func normalizeOutput(output *ModeOutput) {
	if output == nil {
		return
	}

	// Set default confidence if zero
	if output.Confidence == 0 {
		output.Confidence = 0.5 // default moderate confidence
	}

	// Ensure risk likelihoods have sensible defaults
	for i := range output.Risks {
		if output.Risks[i].Likelihood == 0 {
			output.Risks[i].Likelihood = 0.5 // default moderate likelihood
		}
	}

	// Ensure finding confidence has sensible defaults
	for i := range output.TopFindings {
		if output.TopFindings[i].Confidence == 0 {
			output.TopFindings[i].Confidence = 0.5 // default moderate confidence
		}
	}
}
