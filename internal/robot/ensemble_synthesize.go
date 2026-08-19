// Package robot provides machine-readable output for AI agents.
// ensemble_synthesize.go implements --robot-ensemble-synthesize for triggering synthesis.
package robot

// Error codes for synthesis operations.
const (
	// ErrCodeSynthesisNotReady indicates outputs are incomplete.
	ErrCodeSynthesisNotReady = "SYNTHESIS_NOT_READY"

	// ErrCodeOutputSchemaInvalid indicates collected output failed validation.
	ErrCodeOutputSchemaInvalid = "OUTPUT_SCHEMA_INVALID"
)

// EnsembleSynthesizeOutput is the structured response for --robot-ensemble-synthesize.
type EnsembleSynthesizeOutput struct {
	RobotResponse
	Action     string           `json:"action"`
	Session    string           `json:"session"`
	Status     string           `json:"status"`
	Report     *SynthesisReport `json:"report,omitempty"`
	Audit      *SynthesisAudit  `json:"audit,omitempty"`
	AgentHints *AgentHints      `json:"_agent_hints,omitempty"`
}

// SynthesisReport contains the synthesis output details.
type SynthesisReport struct {
	Summary              string  `json:"summary"`
	Strategy             string  `json:"strategy"`
	OutputPath           string  `json:"output_path,omitempty"`
	Format               string  `json:"format"`
	FindingsCount        int     `json:"findings_count"`
	RecommendationsCount int     `json:"recommendations_count"`
	RisksCount           int     `json:"risks_count"`
	QuestionsCount       int     `json:"questions_count"`
	Confidence           float64 `json:"confidence"`
	GeneratedAt          string  `json:"generated_at"`
	// SynthesizedBy records which path produced the result ("agent" or "mechanical").
	SynthesizedBy string `json:"synthesized_by,omitempty"`
	// FallbackReason explains why an agent-based strategy degraded to
	// mechanical merging; empty when the requested path ran.
	FallbackReason string `json:"fallback_reason,omitempty"`
}

// SynthesisAudit summarizes the disagreement analysis.
type SynthesisAudit struct {
	ConflictCount     int      `json:"conflict_count"`
	UnresolvedCount   int      `json:"unresolved_count"`
	HighConflictPairs []string `json:"high_conflict_pairs"`
}

// EnsembleSynthesizeOptions configures the synthesize operation.
type EnsembleSynthesizeOptions struct {
	Session  string
	Strategy string
	Format   string
	Output   string
	Force    bool
}
