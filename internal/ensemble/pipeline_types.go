package ensemble

import (
	"fmt"
	"time"
)

// PipelineStage represents the current stage of ensemble execution.
type PipelineStage string

const (
	StageIntake    PipelineStage = "intake"
	StageModeRun   PipelineStage = "mode_run"
	StageSynthesis PipelineStage = "synthesis"
	StageComplete  PipelineStage = "complete"
	StageFailed    PipelineStage = "failed"
)

// EnsembleResult is the final result from a complete ensemble run.
type EnsembleResult struct {
	// SessionName is the tmux session created for this ensemble.
	SessionName string `json:"session_name"`

	// Question is the problem statement that was analyzed.
	Question string `json:"question"`

	// PresetUsed is the ensemble preset name if one was used.
	PresetUsed string `json:"preset_used,omitempty"`

	// Stage indicates how far the pipeline progressed.
	Stage PipelineStage `json:"stage"`

	// Context is the generated context pack from Stage 1.
	Context *ContextPack `json:"context,omitempty"`

	// ModeOutputs are the collected outputs from Stage 2.
	ModeOutputs []ModeOutput `json:"mode_outputs,omitempty"`

	// Synthesis is the combined report from Stage 3.
	Synthesis *SynthesisReport `json:"synthesis,omitempty"`

	// Error is any error that halted the pipeline.
	Error string `json:"error,omitempty"`

	// StartedAt is when the ensemble run began.
	StartedAt time.Time `json:"started_at"`

	// CompletedAt is when the ensemble run finished.
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// Metrics captures execution telemetry.
	Metrics *PipelineMetrics `json:"metrics,omitempty"`
}

// PipelineMetrics captures execution telemetry for an ensemble run.
type PipelineMetrics struct {
	// Stage durations
	IntakeDuration    time.Duration `json:"intake_duration,omitempty"`
	ModeRunDuration   time.Duration `json:"mode_run_duration,omitempty"`
	SynthesisDuration time.Duration `json:"synthesis_duration,omitempty"`

	// Token usage
	ContextTokens   int `json:"context_tokens,omitempty"`
	ModeTokens      int `json:"mode_tokens,omitempty"`
	SynthesisTokens int `json:"synthesis_tokens,omitempty"`
	TotalTokens     int `json:"total_tokens,omitempty"`

	// Mode statistics
	ModesAttempted int `json:"modes_attempted"`
	ModesSucceeded int `json:"modes_succeeded"`
	ModesFailed    int `json:"modes_failed"`
	ModesTimedOut  int `json:"modes_timed_out"`

	// Early stop
	EarlyStopTriggered bool   `json:"early_stop_triggered,omitempty"`
	EarlyStopReason    string `json:"early_stop_reason,omitempty"`
}

// SynthesisReport is the combined analysis from multiple mode outputs.
// The full synthesis implementation is in a separate task (bd-2qwm8).
type SynthesisReport struct {
	// GeneratedAt is when the synthesis was produced.
	GeneratedAt time.Time `json:"generated_at"`

	// Strategy indicates which synthesis approach was used.
	Strategy string `json:"strategy"`

	// ConsolidatedThesis is the unified conclusion across modes.
	ConsolidatedThesis string `json:"consolidated_thesis"`

	// TopFindings are the highest-confidence findings across all modes.
	TopFindings []Finding `json:"top_findings"`

	// Agreements are conclusions where multiple modes concur.
	Agreements []SynthesisAgreement `json:"agreements,omitempty"`

	// Disagreements are conclusions where modes conflict.
	Disagreements []SynthesisDisagreement `json:"disagreements,omitempty"`

	// UnifiedRisks are consolidated risks from all modes.
	UnifiedRisks []Risk `json:"unified_risks,omitempty"`

	// UnifiedRecommendations are consolidated recommendations.
	UnifiedRecommendations []Recommendation `json:"unified_recommendations,omitempty"`

	// OpenQuestions are unresolved queries needing user input.
	OpenQuestions []Question `json:"open_questions,omitempty"`

	// Confidence is the overall confidence in the synthesis.
	Confidence Confidence `json:"confidence"`

	// ModeContributions shows how each mode contributed.
	ModeContributions []ModeContribution `json:"mode_contributions,omitempty"`

	// AuditLog records synthesis decisions for transparency.
	AuditLog []AuditEntry `json:"audit_log,omitempty"`
}

// SynthesisAgreement records where multiple modes reached the same conclusion.
type SynthesisAgreement struct {
	Finding      string   `json:"finding"`
	ModeIDs      []string `json:"mode_ids"`
	Confidence   float64  `json:"confidence"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// SynthesisDisagreement records where modes reached conflicting conclusions.
type SynthesisDisagreement struct {
	Topic            string                 `json:"topic"`
	Positions        []DisagreementPosition `json:"positions"`
	Resolution       string                 `json:"resolution,omitempty"`
	ResolutionMethod string                 `json:"resolution_method,omitempty"`
}

// DisagreementPosition represents one side of a disagreement.
type DisagreementPosition struct {
	ModeID     string  `json:"mode_id"`
	Position   string  `json:"position"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence,omitempty"`
}

// ModeContribution summarizes how a single mode contributed to synthesis.
type ModeContribution struct {
	ModeID              string  `json:"mode_id"`
	FindingsContributed int     `json:"findings_contributed"`
	UniqueInsights      int     `json:"unique_insights"`
	AgreementCount      int     `json:"agreement_count"`
	DisagreementCount   int     `json:"disagreement_count"`
	OverallWeight       float64 `json:"overall_weight"`
}

// AuditEntry records a synthesis decision for transparency.
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Details   string    `json:"details"`
	ModeID    string    `json:"mode_id,omitempty"`
}

// RunConfig customizes a full ensemble run.
type RunConfig struct {
	// SkipStage1 uses a pre-generated context pack instead.
	SkipStage1 bool
	// PrebuiltContext is used when SkipStage1 is true.
	PrebuiltContext *ContextPack

	// SkipStage3 collects outputs but skips synthesis.
	SkipStage3 bool

	// CollectTimeout is how long to wait for mode outputs.
	CollectTimeout time.Duration

	// EarlyStopConfig enables early stopping conditions.
	EarlyStop EarlyStopConfig
}

// Stage2Result captures the outputs from the mode run stage.
type Stage2Result struct {
	// SessionName is the tmux session for this run.
	SessionName string `json:"session_name"`

	// Outputs are the collected mode outputs.
	Outputs []ModeOutput `json:"outputs"`

	// Assignments records which mode ran on which pane.
	Assignments []ModeAssignment `json:"assignments"`

	// Metrics captures stage-specific telemetry.
	ModesAttempted int           `json:"modes_attempted"`
	ModesSucceeded int           `json:"modes_succeeded"`
	ModesFailed    int           `json:"modes_failed"`
	Duration       time.Duration `json:"duration"`

	// EarlyStopped indicates if early stop triggered.
	EarlyStopped bool   `json:"early_stopped,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
}

// Stage3Result wraps the synthesis report with metadata.
type Stage3Result struct {
	// Report is the synthesis output.
	Report *SynthesisReport `json:"report"`

	// Duration is how long synthesis took.
	Duration time.Duration `json:"duration"`

	// TokensUsed is the synthesis token consumption.
	TokensUsed int `json:"tokens_used"`
}

// OutputCollectorConfig configures output collection behavior.
type OutputCollectorConfig struct {
	// PollInterval is how often to check for completed outputs.
	PollInterval time.Duration `json:"poll_interval"`

	// Timeout is the maximum time to wait for all outputs.
	Timeout time.Duration `json:"timeout"`

	// RequireAll waits for all modes even if some fail.
	RequireAll bool `json:"require_all"`

	// MinOutputs is the minimum outputs needed to proceed.
	MinOutputs int `json:"min_outputs"`
}

// DefaultOutputCollectorConfig returns sensible defaults.
func DefaultOutputCollectorConfig() OutputCollectorConfig {
	return OutputCollectorConfig{
		PollInterval: 5 * time.Second,
		Timeout:      10 * time.Minute,
		RequireAll:   false,
		MinOutputs:   1,
	}
}

// PipelineError wraps an error with stage context.
type PipelineError struct {
	Stage   PipelineStage
	Message string
	Cause   error
}

// Duration returns how long the ensemble run took.
func (r *EnsembleResult) Duration() time.Duration {
	if r == nil {
		return 0
	}
	if r.CompletedAt.IsZero() {
		return time.Since(r.StartedAt)
	}
	return r.CompletedAt.Sub(r.StartedAt)
}

func (e *PipelineError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("stage %s: %s: %v", e.Stage, e.Message, e.Cause)
	}
	return fmt.Sprintf("stage %s: %s", e.Stage, e.Message)
}

func (e *PipelineError) Unwrap() error {
	return e.Cause
}

// NewPipelineError creates a stage-scoped error.
func NewPipelineError(stage PipelineStage, message string, cause error) *PipelineError {
	return &PipelineError{
		Stage:   stage,
		Message: message,
		Cause:   cause,
	}
}
