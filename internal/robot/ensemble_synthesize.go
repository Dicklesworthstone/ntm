// Package robot provides machine-readable output for AI agents.
// ensemble_synthesize.go implements --robot-ensemble-synthesize for triggering synthesis.
package robot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

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

// GetEnsembleSynthesize triggers synthesis for an ensemble session.
func GetEnsembleSynthesize(opts EnsembleSynthesizeOptions) (*EnsembleSynthesizeOutput, error) {
	output := &EnsembleSynthesizeOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_synthesize",
		Session:       opts.Session,
		Status:        "pending",
	}

	// Validate session name
	if strings.TrimSpace(opts.Session) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-ensemble-synthesize=myproject",
		)
		return output, nil
	}

	// Load ensemble state
	state, err := ensemble.LoadSession(opts.Session)
	sessionLive := tmux.IsInstalled() && tmux.SessionExists(opts.Session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("session '%s' not found", opts.Session),
					ErrCodeSessionNotFound,
					"Use 'ntm list' to see available sessions",
				)
				return output, nil
			}
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("ensemble state not found for session '%s'", opts.Session),
				ErrCodeEnsembleNotFound,
				"Spawn an ensemble first: ntm ensemble <preset> <question>",
			)
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to load ensemble state: %w", err),
			ErrCodeInternalError,
			"Check state store availability",
		)
		return output, nil
	}

	// Check if outputs are ready
	readyCount := 0
	pendingCount := 0
	errorCount := 0
	for _, assignment := range state.Assignments {
		switch assignment.Status {
		case ensemble.AssignmentDone:
			readyCount++
		case ensemble.AssignmentError:
			errorCount++
		default:
			pendingCount++
		}
	}

	totalModes := len(state.Assignments)
	if !opts.Force && (pendingCount > 0 || readyCount == 0) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("synthesis not ready: %d/%d modes complete, %d pending, %d errors",
				readyCount, totalModes, pendingCount, errorCount),
			ErrCodeSynthesisNotReady,
			"Wait for all modes to complete or use --force to synthesize partial outputs",
		)
		output.Status = "not_ready"
		return output, nil
	}

	// Resolve strategy
	strategy := opts.Strategy
	if strategy == "" {
		strategy = state.SynthesisStrategy.String()
	}
	if strategy == "" {
		strategy = "manual"
	}

	// Resolve format
	format := strings.ToLower(opts.Format)
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "json" && format != "yaml" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid format '%s'", format),
			ErrCodeInvalidFlag,
			"Supported formats: markdown, json, yaml",
		)
		return output, nil
	}

	// Build synthesis config
	synthConfig := ensemble.SynthesisConfig{
		Strategy:           ensemble.SynthesisStrategy(strategy),
		IncludeExplanation: true,
	}

	// Create synthesis engine
	engine, err := ensemble.NewSynthesisEngine(synthConfig)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to create synthesis engine: %w", err),
			ErrCodeInternalError,
			"Check synthesis configuration",
		)
		return output, nil
	}

	// Collect outputs from assignments
	collector := ensemble.NewOutputCollector(ensemble.DefaultOutputCollectorConfig())
	var captured []ensemble.CapturedOutput
	if sessionLive {
		capture := ensemble.NewOutputCapture(tmux.DefaultClient)
		captured, _ = capture.CaptureAll(state)
	}

	collected, err := ensemble.CollectModeOutputs(state, captured)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to collect mode outputs: %w", err),
			ErrCodeInternalError,
			"Check ensemble output files and tmux session state",
		)
		output.Status = "error"
		return output, nil
	}
	for _, modeOutput := range collected.Outputs {
		if err := collector.Add(modeOutput); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("failed to collect output %s: %w", modeOutput.ModeID, err),
				ErrCodeInternalError,
				"Check collected outputs",
			)
			output.Status = "error"
			return output, nil
		}
	}

	if len(collector.Outputs) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no valid outputs collected from %d completed modes (errors: %d)", readyCount, collected.ErrorCount()),
			ErrCodeOutputSchemaInvalid,
			"Mode outputs may not have been captured correctly",
		)
		output.Status = "error"
		return output, nil
	}

	// Build synthesis input
	synthInput, err := collector.BuildSynthesisInput(state.Question, nil, synthConfig)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to build synthesis input: %w", err),
			ErrCodeInternalError,
			"Check collected outputs",
		)
		output.Status = "error"
		return output, nil
	}

	// Run synthesis directly using the collected outputs
	result, err := engine.Synthesizer.Synthesize(synthInput)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("synthesis failed: %w", err),
			ErrCodeInternalError,
			"Review mode outputs for completeness",
		)
		output.Status = "error"
		return output, nil
	}

	// Run disagreement audit on the collected outputs
	var auditReport *ensemble.AuditReport
	auditor := ensemble.NewDisagreementAuditor(collector.Outputs, result)
	if auditor != nil {
		auditReport, _ = auditor.Audit()
	}

	// Format output if path specified
	outputPath := opts.Output
	if outputPath != "" {
		formatter := ensemble.NewSynthesisFormatter(ensemble.OutputFormat(format))
		formatter.IncludeAudit = true
		formatter.IncludeExplanation = true

		var buf bytes.Buffer
		if err := formatter.FormatResult(&buf, result, auditReport); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("failed to format output: %w", err),
				ErrCodeInternalError,
				"Check output format",
			)
			output.Status = "error"
			return output, nil
		}

		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("failed to write output file: %w", err),
				ErrCodeInternalError,
				"Check file permissions",
			)
			output.Status = "error"
			return output, nil
		}
	}

	// Build response
	output.Status = "complete"
	output.Report = &SynthesisReport{
		Summary:              result.Summary,
		Strategy:             strategy,
		OutputPath:           outputPath,
		Format:               format,
		FindingsCount:        len(result.Findings),
		RecommendationsCount: len(result.Recommendations),
		RisksCount:           len(result.Risks),
		QuestionsCount:       len(result.QuestionsForUser),
		Confidence:           float64(result.Confidence),
		GeneratedAt:          FormatTimestamp(result.GeneratedAt),
	}

	// Build audit summary
	if auditReport != nil {
		output.Audit = &SynthesisAudit{
			ConflictCount:     len(auditReport.Conflicts),
			HighConflictPairs: []string{},
		}

		unresolvedCount := 0
		for _, conflict := range auditReport.Conflicts {
			if conflict.Severity == ensemble.ConflictHigh {
				// Format as "Mode1↔Mode2"
				if len(conflict.Positions) >= 2 {
					pair := fmt.Sprintf("%s↔%s", conflict.Positions[0].ModeID, conflict.Positions[1].ModeID)
					output.Audit.HighConflictPairs = append(output.Audit.HighConflictPairs, pair)
				}
			}
			if conflict.ResolutionPath == "" {
				unresolvedCount++
			}
		}
		output.Audit.UnresolvedCount = unresolvedCount
	} else {
		output.Audit = &SynthesisAudit{
			ConflictCount:     0,
			UnresolvedCount:   0,
			HighConflictPairs: []string{},
		}
	}

	// Build agent hints
	output.AgentHints = buildSynthesizeHints(output)

	return output, nil
}

// PrintEnsembleSynthesize outputs the synthesize result as JSON.
func PrintEnsembleSynthesize(opts EnsembleSynthesizeOptions) error {
	output, err := GetEnsembleSynthesize(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// buildSynthesizeHints creates agent hints for the synthesize output.
func buildSynthesizeHints(output *EnsembleSynthesizeOutput) *AgentHints {
	if output == nil {
		return nil
	}

	hints := &AgentHints{}

	switch output.Status {
	case "complete":
		hints.Summary = fmt.Sprintf("Synthesis complete: %d findings, %d recommendations",
			output.Report.FindingsCount, output.Report.RecommendationsCount)
		if output.Report.OutputPath != "" {
			hints.Notes = append(hints.Notes, fmt.Sprintf("Report saved to: %s", output.Report.OutputPath))
		}
		if output.Audit != nil && output.Audit.UnresolvedCount > 0 {
			hints.Warnings = append(hints.Warnings,
				fmt.Sprintf("%d unresolved conflicts - review high-conflict pairs", output.Audit.UnresolvedCount))
		}

	case "not_ready":
		hints.Summary = "Synthesis not ready - waiting for mode outputs"
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "wait",
			Target:   "ensemble modes",
			Reason:   "modes are still running",
			Priority: 1,
		})

	case "error":
		hints.Summary = "Synthesis failed"
		hints.SuggestedActions = append(hints.SuggestedActions, RobotAction{
			Action:   "check_status",
			Target:   output.Session,
			Reason:   "review ensemble state",
			Priority: 1,
		})
	}

	if hints.Summary == "" && len(hints.SuggestedActions) == 0 && len(hints.Warnings) == 0 {
		return nil
	}
	return hints
}
