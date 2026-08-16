//go:build ensemble_experimental
// +build ensemble_experimental

package ensemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ValidatePipelineConfig checks if the config is valid for running the pipeline.
func ValidatePipelineConfig(cfg *EnsembleConfig) error {
	if cfg == nil {
		return errors.New("config is nil")
	}

	var errs []string

	if cfg.SessionName == "" {
		errs = append(errs, "session name is required")
	}
	if strings.TrimSpace(cfg.Question) == "" {
		errs = append(errs, "question is required")
	}
	if cfg.Ensemble == "" && len(cfg.Modes) == 0 {
		errs = append(errs, "either ensemble name or explicit modes are required")
	}
	if cfg.Ensemble != "" && len(cfg.Modes) > 0 {
		errs = append(errs, "ensemble name and explicit modes are mutually exclusive")
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid pipeline config: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Run executes the complete 3-stage ensemble pipeline:
// Stage 1 (Intake) → Stage 2 (Mode Run) → Stage 3 (Synthesis)
func (m *EnsembleManager) Run(ctx context.Context, cfg *EnsembleConfig, runCfg RunConfig) (*EnsembleResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	result := &EnsembleResult{
		SessionName: cfg.SessionName,
		Question:    cfg.Question,
		Stage:       StageIntake,
		StartedAt:   time.Now().UTC(),
		Metrics:     &PipelineMetrics{},
	}

	logger := m.logger()
	logger.Info("ensemble pipeline starting",
		"session", cfg.SessionName,
		"ensemble", cfg.Ensemble,
		"skip_stage1", runCfg.SkipStage1,
		"skip_stage3", runCfg.SkipStage3,
	)

	// Validate config upfront
	if err := ValidatePipelineConfig(cfg); err != nil {
		result.Stage = StageFailed
		result.Error = err.Error()
		result.CompletedAt = time.Now().UTC()
		return result, err
	}

	// Stage 1: Intake
	var contextPack *ContextPack
	stage1Start := time.Now()

	if runCfg.SkipStage1 && runCfg.PrebuiltContext != nil {
		logger.Info("stage1: using prebuilt context pack")
		contextPack = runCfg.PrebuiltContext
	} else {
		pack, err := m.RunStage1(ctx, cfg)
		if err != nil {
			result.Stage = StageFailed
			result.Error = fmt.Sprintf("stage1 failed: %v", err)
			result.CompletedAt = time.Now().UTC()
			return result, NewPipelineError(StageIntake, "context generation failed", err)
		}
		contextPack = pack
	}

	result.Context = contextPack
	result.Metrics.IntakeDuration = time.Since(stage1Start)
	result.Metrics.ContextTokens = contextPack.TokenEstimate
	result.Stage = StageModeRun

	logger.Info("stage1 complete",
		"duration", result.Metrics.IntakeDuration,
		"tokens", contextPack.TokenEstimate,
	)

	// Stage 2: Mode Run
	stage2Start := time.Now()
	stage2Result, err := m.RunStage2(ctx, cfg, contextPack)
	if err != nil {
		result.Stage = StageFailed
		result.Error = fmt.Sprintf("stage2 failed: %v", err)
		result.CompletedAt = time.Now().UTC()
		return result, NewPipelineError(StageModeRun, "mode run failed", err)
	}

	result.ModeOutputs = stage2Result.Outputs
	result.PresetUsed = cfg.Ensemble
	result.Metrics.ModeRunDuration = time.Since(stage2Start)
	result.Metrics.ModesAttempted = stage2Result.ModesAttempted
	result.Metrics.ModesSucceeded = stage2Result.ModesSucceeded
	result.Metrics.ModesFailed = stage2Result.ModesFailed
	result.Metrics.EarlyStopTriggered = stage2Result.EarlyStopped
	result.Metrics.EarlyStopReason = stage2Result.StopReason
	result.Stage = StageSynthesis

	logger.Info("stage2 complete",
		"duration", result.Metrics.ModeRunDuration,
		"outputs", len(stage2Result.Outputs),
		"attempted", stage2Result.ModesAttempted,
		"succeeded", stage2Result.ModesSucceeded,
	)

	// Stage 3: Synthesis (skip if configured)
	if runCfg.SkipStage3 {
		logger.Info("stage3: skipped by config")
		result.Stage = StageComplete
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	if len(result.ModeOutputs) == 0 {
		logger.Warn("stage3: no outputs to synthesize")
		result.Stage = StageComplete
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	stage3Start := time.Now()
	stage3Result, err := m.RunStage3(ctx, cfg, result.ModeOutputs)
	if err != nil {
		// Synthesis failure is non-fatal - we still have mode outputs
		logger.Warn("stage3 failed, returning partial result",
			"error", err,
		)
		result.Error = fmt.Sprintf("stage3 warning: %v", err)
	} else if stage3Result != nil {
		result.Synthesis = stage3Result.Report
		result.Metrics.SynthesisDuration = time.Since(stage3Start)
		result.Metrics.SynthesisTokens = stage3Result.TokensUsed
	}

	result.Stage = StageComplete
	result.CompletedAt = time.Now().UTC()

	// Calculate total tokens
	result.Metrics.TotalTokens = result.Metrics.ContextTokens +
		result.Metrics.ModeTokens +
		result.Metrics.SynthesisTokens

	logger.Info("ensemble pipeline complete",
		"session", cfg.SessionName,
		"duration", result.Duration(),
		"outputs", len(result.ModeOutputs),
		"synthesis", result.Synthesis != nil,
	)

	return result, nil
}

// RunStage1 executes the Intake stage: generates or retrieves a context pack.
func (m *EnsembleManager) RunStage1(ctx context.Context, cfg *EnsembleConfig) (*ContextPack, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	logger := m.logger()
	logger.Info("stage1: intake starting",
		"session", cfg.SessionName,
		"project", cfg.ProjectDir,
	)

	// Get resolved config for cache settings
	catalog, err := m.catalog()
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	registry, err := m.registry(catalog)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	_, resolvedCfg, _, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	// Generate context pack
	generator, cacheCfg := m.contextPackGenerator(cfg.ProjectDir, resolvedCfg.cache)
	pack, err := generator.Generate(cfg.Question, "", cacheCfg)
	if err != nil {
		return nil, fmt.Errorf("generate context pack: %w", err)
	}

	// Check for thin context and log questions
	if len(pack.Questions) > 0 {
		logger.Info("stage1: thin context detected",
			"questions", len(pack.Questions),
		)
	}

	logger.Info("stage1: intake complete",
		"tokens", pack.TokenEstimate,
		"hash", pack.Hash,
	)

	return pack, nil
}

// RunStage2 executes the Mode Run stage: spawns agents, injects prompts, collects outputs.
func (m *EnsembleManager) RunStage2(ctx context.Context, cfg *EnsembleConfig, contextPack *ContextPack) (*Stage2Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return nil, errors.New("config is nil")
	}

	logger := m.logger()
	start := time.Now()

	logger.Info("stage2: mode run starting",
		"session", cfg.SessionName,
	)

	// First spawn the ensemble (creates session, panes, injects prompts)
	session, err := m.SpawnEnsemble(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("spawn ensemble: %w", err)
	}

	result := &Stage2Result{
		SessionName:    session.SessionName,
		Assignments:    session.Assignments,
		ModesAttempted: len(session.Assignments),
		Outputs:        make([]ModeOutput, 0),
	}

	// Count successes/failures from assignments
	for _, a := range session.Assignments {
		switch a.Status {
		case AssignmentActive:
			result.ModesSucceeded++
		case AssignmentError:
			result.ModesFailed++
		}
	}

	// If no early stop config, just return with spawned state
	// Output collection happens asynchronously via robot API or manual retrieval
	if cfg.EarlyStop.Enabled {
		logger.Info("stage2: early stop monitoring enabled",
			"findings_threshold", cfg.EarlyStop.FindingsThreshold,
			"similarity_threshold", cfg.EarlyStop.SimilarityThreshold,
		)
		// Note: Full output collection with early stop monitoring
		// would poll panes and parse outputs. This is tracked separately
		// as it requires the output collector component.
	}

	result.Duration = time.Since(start)

	logger.Info("stage2: mode run complete",
		"session", result.SessionName,
		"attempted", result.ModesAttempted,
		"succeeded", result.ModesSucceeded,
		"failed", result.ModesFailed,
		"duration", result.Duration,
	)

	return result, nil
}

// RunStage3 executes the Synthesis stage: combines mode outputs into a unified report.
// Note: Full synthesis implementation is in a separate task (bd-2qwm8).
func (m *EnsembleManager) RunStage3(ctx context.Context, cfg *EnsembleConfig, outputs []ModeOutput) (*Stage3Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	if len(outputs) == 0 {
		return nil, errors.New("no outputs to synthesize")
	}

	logger := m.logger()
	start := time.Now()

	logger.Info("stage3: synthesis starting",
		"outputs", len(outputs),
		"strategy", cfg.Synthesis.Strategy,
	)

	// For now, create a basic mechanical synthesis // placebo-waiver: bd-d7z7i
	// Full synthesis with synthesizer agent is in task bd-2qwm8
	report, err := m.mechanicalSynthesis(outputs, cfg.Synthesis)
	if err != nil {
		return nil, fmt.Errorf("mechanical synthesis: %w", err)
	}

	result := &Stage3Result{
		Report:   report,
		Duration: time.Since(start),
	}

	logger.Info("stage3: synthesis complete",
		"findings", len(report.TopFindings),
		"agreements", len(report.Agreements),
		"disagreements", len(report.Disagreements),
		"duration", result.Duration,
	)

	return result, nil
}

// mechanicalSynthesis performs basic merging without an AI synthesizer agent.
// This is a fallback/baseline implementation - full AI synthesis is in bd-2qwm8.
func (m *EnsembleManager) mechanicalSynthesis(outputs []ModeOutput, synthCfg SynthesisConfig) (*SynthesisReport, error) {
	report := &SynthesisReport{
		GeneratedAt: time.Now().UTC(),
		Strategy:    "mechanical",
		TopFindings: make([]Finding, 0),
		Agreements:  make([]SynthesisAgreement, 0),
	}

	// Collect all findings with their source modes
	type findingSource struct {
		finding Finding
		modeID  string
	}
	allFindings := make([]findingSource, 0)

	for _, output := range outputs {
		for _, f := range output.TopFindings {
			allFindings = append(allFindings, findingSource{
				finding: f,
				modeID:  output.ModeID,
			})
		}

		// Track mode contributions
		report.ModeContributions = append(report.ModeContributions, ModeContribution{
			ModeID:              output.ModeID,
			FindingsContributed: len(output.TopFindings),
			OverallWeight:       float64(output.Confidence),
		})
	}

	// Simple deduplication by grouping similar findings
	// A more sophisticated approach would use semantic similarity
	seen := make(map[string][]string) // finding text -> mode IDs
	for _, fs := range allFindings {
		key := fs.finding.Finding
		seen[key] = append(seen[key], fs.modeID)

		// Add to top findings if not already there
		found := false
		for i := range report.TopFindings {
			if report.TopFindings[i].Finding == fs.finding.Finding {
				found = true
				// Boost confidence if multiple modes agree
				if len(seen[key]) > 1 {
					currentConf := float64(report.TopFindings[i].Confidence)
					report.TopFindings[i].Confidence = Confidence(min(currentConf*1.1, 1.0))
				}
				break
			}
		}
		if !found {
			report.TopFindings = append(report.TopFindings, fs.finding)
		}
	}

	// Record agreements (findings seen by multiple modes)
	for title, modes := range seen {
		if len(modes) > 1 {
			report.Agreements = append(report.Agreements, SynthesisAgreement{
				Finding:    title,
				ModeIDs:    modes,
				Confidence: 0.8, // Higher confidence when multiple modes agree
			})
		}
	}

	// Limit findings to max configured
	maxFindings := synthCfg.MaxFindings
	if maxFindings <= 0 {
		maxFindings = 20
	}
	if len(report.TopFindings) > maxFindings {
		report.TopFindings = report.TopFindings[:maxFindings]
	}

	// Create consolidated thesis from mode theses
	theses := make([]string, 0, len(outputs))
	for _, o := range outputs {
		if o.Thesis != "" {
			theses = append(theses, o.Thesis)
		}
	}
	if len(theses) > 0 {
		report.ConsolidatedThesis = theses[0] // Simple: use first thesis
		// Full synthesis would generate a unified thesis using AI
	}

	// Merge risks and recommendations
	for _, output := range outputs {
		report.UnifiedRisks = append(report.UnifiedRisks, output.Risks...)
		report.UnifiedRecommendations = append(report.UnifiedRecommendations, output.Recommendations...)
		report.OpenQuestions = append(report.OpenQuestions, output.QuestionsForUser...)
	}

	// Calculate overall confidence
	if len(outputs) > 0 {
		var totalConf float64
		for _, o := range outputs {
			totalConf += float64(o.Confidence)
		}
		report.Confidence = Confidence(totalConf / float64(len(outputs)))
	}

	// Add audit entry
	report.AuditLog = append(report.AuditLog, AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    "mechanical_synthesis",
		Details:   fmt.Sprintf("combined %d mode outputs", len(outputs)),
	})

	return report, nil
}
