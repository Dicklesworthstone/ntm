// Package robot provides machine-readable output for AI agents.
// ensemble_synthesize.go implements --robot-ensemble-synthesize for triggering synthesis.
package robot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
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
	Action           string                       `json:"action"`
	Session          string                       `json:"session"`
	Status           string                       `json:"status"`
	Report           *SynthesisReport             `json:"report,omitempty"`
	Audit            *SynthesisAudit              `json:"audit,omitempty"`
	Result           *ensemble.SynthesisResult    `json:"result,omitempty"`
	AuditReport      *ensemble.AuditReport        `json:"audit_report,omitempty"`
	Redundancy       *ensemble.RedundancyAnalysis `json:"redundancy,omitempty"`
	FormattedOutput  string                       `json:"formatted_output,omitempty"`
	ValidationErrors map[string][]string          `json:"validation_errors,omitempty"`
	AgentHints       *AgentHints                  `json:"_agent_hints,omitempty"`
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
	FallbackReason string                       `json:"fallback_reason,omitempty"`
	Redundancy     *ensemble.RedundancyAnalysis `json:"redundancy,omitempty"`
}

// SynthesisAudit summarizes the disagreement analysis.
type SynthesisAudit struct {
	ConflictCount     int      `json:"conflict_count"`
	UnresolvedCount   int      `json:"unresolved_count"`
	HighConflictPairs []string `json:"high_conflict_pairs"`
}

// EnsembleSynthesizeOptions configures the synthesize operation.
type EnsembleSynthesizeOptions struct {
	Session    string
	Strategy   string
	Format     string
	Output     string
	Force      bool
	ProjectDir string
	// OnProgress optionally observes lifecycle stages ("collecting_outputs", "synthesizing")
	// so async REST jobs can report durable intermediate progress without duplicating the engine.
	OnProgress func(stage string) error `json:"-"`
}

// GetEnsembleSynthesize collects validated mode outputs for an ensemble session,
// runs synthesis + disagreement audit + redundancy analysis, optionally writes
// formatted output to disk, and returns the structured result for CLI and REST.
func GetEnsembleSynthesize(ctx context.Context, opts EnsembleSynthesizeOptions, cfg *config.Config) (*EnsembleSynthesizeOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session := strings.TrimSpace(opts.Session)
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()

	output := &EnsembleSynthesizeOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_synthesize",
		Session:       session,
		Status:        "failed",
	}

	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorSystem, "ensemble.synthesize", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"strategy":       opts.Strategy,
		"format":         opts.Format,
		"force":          opts.Force,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		success := output != nil && output.Success
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        session,
			"strategy":       opts.Strategy,
			"format":         opts.Format,
			"force":          opts.Force,
			"success":        success,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if output != nil {
			payload["status"] = output.Status
			if output.Error != "" {
				payload["error"] = output.Error
			}
			if output.Report != nil {
				payload["findings_count"] = output.Report.FindingsCount
				payload["synthesized_by"] = output.Report.SynthesizedBy
			}
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorSystem, "ensemble.synthesize", payload, nil)
	}()

	if session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-ensemble-synthesize=myproject",
		)
		return output, nil
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid session name: %w", err),
			ErrCodeInvalidFlag,
			"Session names must satisfy tmux session naming rules",
		)
		return output, nil
	}

	formatName := strings.ToLower(strings.TrimSpace(opts.Format))
	if formatName == "" {
		formatName = "markdown"
	}
	var outputFormat ensemble.OutputFormat
	switch formatName {
	case "markdown", "md":
		formatName = "markdown"
		outputFormat = ensemble.FormatMarkdown
	case "json":
		outputFormat = ensemble.FormatJSON
	case "yaml", "yml":
		formatName = "yaml"
		outputFormat = ensemble.FormatYAML
	default:
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("unsupported output_format %q (use markdown, json, or yaml)", opts.Format),
			ErrCodeInvalidFlag,
			"Use --synth-format=markdown|json|yaml",
		)
		return output, nil
	}

	var explicitStrategy ensemble.SynthesisStrategy
	if strings.TrimSpace(opts.Strategy) != "" {
		validated, stratErr := ensemble.ValidateOrMigrateStrategy(strings.TrimSpace(opts.Strategy))
		if stratErr != nil {
			output.RobotResponse = NewErrorResponse(
				stratErr,
				ErrCodeInvalidFlag,
				"Specify a valid synthesis strategy (e.g., consensus, dialectical, adversarial, bayesian, meta-reasoning)",
			)
			return output, nil
		}
		explicitStrategy = validated
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sessionLive := tmux.IsInstalled() && tmux.SessionExists(session)
	state, err := ensemble.LoadSession(session)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !sessionLive {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("session %q not found", session),
					ErrCodeSessionNotFound,
					"Use 'ntm list' or 'ntm --robot-status' to see available sessions",
				)
				return output, nil
			}
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("no ensemble state found in session %q", session),
				ErrCodeEnsembleNotFound,
				"Spawn an ensemble first with 'ntm --robot-ensemble-spawn'",
			)
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check ensemble session state storage",
		)
		return output, nil
	}

	var captured []ensemble.CapturedOutput
	var refreshFailure error
	if sessionLive {
		captured, err = ensemble.NewOutputCapture(tmux.DefaultClient).CaptureForSynthesis(ctx, state)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if !opts.Force || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				output.RobotResponse = NewErrorResponse(
					fmt.Errorf("refresh ensemble outputs: %w", err),
					ErrCodeInternalError,
					"Retry or pass --synth-force to synthesize previously saved validated outputs",
				)
				return output, nil
			}
			refreshFailure = err
			captured = nil
			slog.Warn("ensemble live output refresh failed; forcing mechanical synthesis from saved outputs",
				"session", session, "error", err)
		}
	}

	ready, pending, working := countEnsembleSessionStates(state)
	if !opts.Force && (pending > 0 || working > 0) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("synthesis not ready: %d pending, %d working", pending, working),
			ErrCodeSynthesisNotReady,
			"Set force=true (--synth-force) to synthesize available outputs before all modes finish",
		)
		return output, nil
	}
	if ready == 0 && !opts.Force {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no completed mode outputs to synthesize"),
			ErrCodeSynthesisNotReady,
			"Wait for reasoning modes to complete or pass force=true (--synth-force) if saved outputs exist",
		)
		return output, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.OnProgress != nil {
		if err := opts.OnProgress("collecting_outputs"); err != nil {
			return nil, err
		}
	}

	availableOutputs, err := ensemble.CollectModeOutputs(state, captured)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("collect mode outputs: %w", err),
			ErrCodeInternalError,
			"Inspect saved mode outputs or run ntm --robot-ensemble=SESSION",
		)
		return output, nil
	}

	collector := ensemble.NewOutputCollector(ensemble.DefaultOutputCollectorConfig())
	for _, modeOutput := range availableOutputs.Outputs {
		if addErr := collector.Add(modeOutput); addErr != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("invalid output from mode %s: %w", modeOutput.ModeID, addErr),
				ErrCodeOutputSchemaInvalid,
				"Re-run the mode with invalid schema output or inspect its raw transcript",
			)
			return output, nil
		}
	}
	for modeID, errs := range availableOutputs.ValidationErrors {
		if _, exists := collector.ValidationErrors[modeID]; !exists {
			collector.ValidationErrors[modeID] = errs
		}
	}

	if collector.Count() == 0 {
		if collector.ErrorCount() > 0 {
			output.ValidationErrors = collector.ValidationErrors
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("no valid mode outputs collected (%d validation errors)", collector.ErrorCount()),
				ErrCodeOutputSchemaInvalid,
				"Fix or re-run modes whose outputs failed schema validation",
			)
			return output, nil
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no mode outputs available for synthesis"),
			ErrCodeSynthesisNotReady,
			"Wait for at least one reasoning mode to produce valid output",
		)
		return output, nil
	}

	if cfg == nil {
		cfg = config.Default()
	}
	budgetCfg := state.EffectiveBudget(ensemble.BudgetConfigFromTOML(ensemble.DefaultBudgetConfig(), cfg.Ensemble.Budget))
	baseSynth := ensemble.SynthesisConfig{
		Strategy:      state.SynthesisStrategy,
		MaxFindings:   20,
		MinConfidence: 0.3,
	}
	synthConfig := state.EffectiveSynthesis(ensemble.SynthesisConfigFromTOML(baseSynth, cfg.Ensemble.Synthesis))
	if state.SynthesisStrategy != "" && explicitStrategy == "" {
		synthConfig.Strategy = state.SynthesisStrategy
	}
	if explicitStrategy != "" {
		synthConfig.Strategy = explicitStrategy
	}
	if state.Synthesis == nil && synthConfig.MaxSynthesisTokens <= 0 && budgetCfg.SynthesisReserveTokens > 0 {
		synthConfig.MaxSynthesisTokens = budgetCfg.SynthesisReserveTokens
	}

	synth, err := ensemble.NewSynthesizer(synthConfig)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInvalidFlag,
			"Check synthesis configuration and strategy parameters",
		)
		return output, nil
	}

	if sessionLive && refreshFailure == nil {
		manager := ensemble.NewEnsembleManager()
		if err := manager.ConfigureSessionSynthesis(ctx, state, synth, budgetCfg.TimeoutPerMode); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("configure lead synthesis: %w", err),
				ErrCodeInternalError,
				"Check synthesizer pane health or retry mechanical synthesis",
			)
			return output, nil
		}
	}

	input, err := collector.BuildSynthesisInput(state.Question, nil, synthConfig)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("build synthesis input: %w", err),
			ErrCodeInternalError,
			"Check collected mode outputs",
		)
		return output, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.OnProgress != nil {
		if err := opts.OnProgress("synthesizing"); err != nil {
			return nil, err
		}
	}

	result, err := synth.SynthesizeContext(ctx, input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("synthesis failed: %w", err),
			ErrCodeInternalError,
			"Inspect synthesizer logs or retry with --synth-strategy=consensus",
		)
		return output, nil
	}
	if refreshFailure != nil {
		result.FallbackReason = fmt.Sprintf("live output refresh failed; used validated saved outputs for mechanical synthesis: %v", refreshFailure)
	}

	catalog, _ := ensemble.GlobalCatalog()
	redundancy := result.Redundancy
	if redundancy == nil {
		redundancy = ensemble.AnalyzeRedundancy(input.Outputs, catalog)
		result.Redundancy = redundancy
	}

	var rendered bytes.Buffer
	formatter := ensemble.NewSynthesisFormatter(outputFormat)
	formatter.IncludeRaw = synthConfig.IncludeRawOutputs
	formatter.IncludeAudit = true
	formatter.IncludeExplanation = synthConfig.IncludeExplanation
	if err := formatter.FormatResult(&rendered, result, input.AuditReport); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("format synthesis output: %w", err),
			ErrCodeInternalError,
			"Retry with --synth-format=json or markdown",
		)
		return output, nil
	}

	var outputPath string
	if rawOut := strings.TrimSpace(opts.Output); rawOut != "" {
		cleanPath := filepath.Clean(rawOut)
		if !filepath.IsAbs(cleanPath) {
			if projDir := strings.TrimSpace(opts.ProjectDir); projDir != "" {
				cleanPath = filepath.Join(projDir, cleanPath)
			}
		}
		if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("create output directory: %w", err),
				ErrCodeInternalError,
				"Provide a writable --synth-output path",
			)
			return output, nil
		}
		if err := util.AtomicWriteFile(cleanPath, rendered.Bytes(), 0o644); err != nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("write synthesis output: %w", err),
				ErrCodeInternalError,
				"Provide a writable --synth-output path",
			)
			return output, nil
		}
		outputPath = cleanPath
	}

	generatedAt := result.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	report := &SynthesisReport{
		Summary:              result.Summary,
		Strategy:             string(synth.Strategy.Name),
		OutputPath:           outputPath,
		Format:               formatName,
		FindingsCount:        len(result.Findings),
		RecommendationsCount: len(result.Recommendations),
		RisksCount:           len(result.Risks),
		QuestionsCount:       len(result.QuestionsForUser),
		Confidence:           float64(result.Confidence),
		GeneratedAt:          generatedAt.UTC().Format(time.RFC3339),
		SynthesizedBy:        result.SynthesizedBy,
		FallbackReason:       result.FallbackReason,
		Redundancy:           redundancy,
	}

	auditSummary := SummarizeEnsembleSynthesisAudit(input.AuditReport)

	var warnings []string
	if result.FallbackReason != "" {
		warnings = append(warnings, result.FallbackReason)
	}
	if auditSummary.UnresolvedCount > 0 {
		warnings = append(warnings, fmt.Sprintf("%d mode conflict(s) remain unresolved", auditSummary.UnresolvedCount))
	}

	output.Status = "complete"
	output.Report = report
	output.Audit = auditSummary
	output.Result = result
	output.AuditReport = input.AuditReport
	output.Redundancy = redundancy
	output.FormattedOutput = rendered.String()
	output.AgentHints = &AgentHints{
		Summary: fmt.Sprintf("Synthesized %d finding(s), %d recommendation(s), %d risk(s) via %s (confidence %.2f)",
			report.FindingsCount, report.RecommendationsCount, report.RisksCount, report.Strategy, report.Confidence),
		Warnings: warnings,
		SuggestedActions: []RobotAction{
			{
				Action:   "stop_ensemble",
				Target:   session,
				Reason:   "Synthesis complete; stop the ensemble session when finished inspecting panes",
				Priority: 2,
			},
		},
	}

	return output, nil
}

// PrintEnsembleSynthesize executes ensemble synthesis and prints JSON/TOON output.
func PrintEnsembleSynthesize(ctx context.Context, opts EnsembleSynthesizeOptions, cfg *config.Config) error {
	output, err := GetEnsembleSynthesize(ctx, opts, cfg)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ensemble synthesize failed")
}

// SummarizeEnsembleSynthesisAudit condenses an ensemble AuditReport into a compact Robot summary.
func SummarizeEnsembleSynthesisAudit(auditReport *ensemble.AuditReport) *SynthesisAudit {
	summary := &SynthesisAudit{
		HighConflictPairs: []string{},
	}
	if auditReport == nil {
		return summary
	}
	summary.ConflictCount = len(auditReport.Conflicts)
	pairSet := make(map[string]struct{})
	for _, c := range auditReport.Conflicts {
		if strings.TrimSpace(c.ResolutionPath) == "" {
			summary.UnresolvedCount++
		}
		if c.Severity == ensemble.ConflictHigh && len(c.Positions) >= 2 {
			modes := make([]string, 0, len(c.Positions))
			for _, pos := range c.Positions {
				if m := strings.TrimSpace(pos.ModeID); m != "" {
					modes = append(modes, m)
				}
			}
			sort.Strings(modes)
			if len(modes) >= 2 {
				pairSet[modes[0]+" vs "+modes[1]] = struct{}{}
			}
		}
	}
	for pair := range auditReport.ModeDisagreements {
		if len(auditReport.ModeDisagreements[pair]) >= 2 {
			pairSet[pair] = struct{}{}
		}
	}
	for pair := range pairSet {
		summary.HighConflictPairs = append(summary.HighConflictPairs, pair)
	}
	sort.Strings(summary.HighConflictPairs)
	return summary
}

func countEnsembleSessionStates(state *ensemble.EnsembleSession) (ready, pending, working int) {
	if state == nil {
		return 0, 0, 0
	}
	for _, a := range state.Assignments {
		switch a.Status {
		case ensemble.AssignmentDone:
			ready++
		case ensemble.AssignmentPending, ensemble.AssignmentInjecting:
			pending++
		case ensemble.AssignmentActive:
			working++
		}
	}
	return ready, pending, working
}
