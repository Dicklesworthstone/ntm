package ensemble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/util"
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

	if runCfg.CollectTimeout > 0 && len(stage2Result.Outputs) == 0 && len(stage2Result.Assignments) > 0 {
		var ctrl *EarlyStopController
		if cfg.EarlyStop.Enabled {
			ctrl = NewEarlyStopController(stage2Result.SessionName, cfg.EarlyStop)
		}
		collected, _, stopSummary, collectErr := m.collectAssignedModeOutputsWithEarlyStop(ctx, stage2Result.SessionName, stage2Result.Assignments, runCfg.CollectTimeout, ctrl)
		if collectErr != nil {
			logger.Warn("stage2 output collection incomplete", "session", stage2Result.SessionName, "error", collectErr)
		}
		if stopSummary.Triggered {
			stage2Result.EarlyStopped = true
			stage2Result.StopReason = stopSummary.Reason
		}
		if len(collected) > 0 {
			stage2Result.Outputs = collected
			stage2Result.ModesSucceeded = len(collected)
			stage2Result.ModesFailed = max(0, stage2Result.ModesAttempted-len(collected))
			result.Metrics.ModeTokens = EstimateModeOutputsTokens(collected)
		}
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
	if resolvedCfg.budget.ContextReserveTokens > 0 {
		generator.MaxTokens = resolvedCfg.budget.ContextReserveTokens
	}
	pack, err := generator.Generate(cfg.Question, "", cacheCfg)
	if err != nil {
		return nil, fmt.Errorf("generate context pack: %w", err)
	}
	if resolvedCfg.budget.ContextReserveTokens > 0 {
		pack = CapContextPackTokens(pack, resolvedCfg.budget.ContextReserveTokens)
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

	report, err := m.synthesizeStage3Report(ctx, cfg, outputs)
	if err != nil {
		return nil, err
	}

	result := &Stage3Result{
		Report:     report,
		Duration:   time.Since(start),
		TokensUsed: report.TokensUsed,
	}

	logger.Info("stage3: synthesis complete",
		"findings", len(report.TopFindings),
		"agreements", len(report.Agreements),
		"disagreements", len(report.Disagreements),
		"duration", result.Duration,
	)

	return result, nil
}

// synthesizeStage3Report produces the stage-3 synthesis report.
// Agent-based strategies dispatch the synthesis prompt to a designated lead
// pane through the gated dispatch path and parse its response. When the lead
// path is unavailable or fails, the report degrades to mechanical merging
// with an explicit "fell back to mechanical: <reason>" audit entry - never
// silently. Manual strategies use mechanical merging directly (unchanged).
func (m *EnsembleManager) synthesizeStage3Report(ctx context.Context, cfg *EnsembleConfig, outputs []ModeOutput) (*SynthesisReport, error) {
	logger := m.logger()

	effectiveCfg := *cfg
	if effectiveCfg.Synthesis.MaxSynthesisTokens <= 0 && effectiveCfg.Budget.SynthesisReserveTokens > 0 {
		effectiveCfg.Synthesis.MaxSynthesisTokens = effectiveCfg.Budget.SynthesisReserveTokens
	}

	strategyName := string(effectiveCfg.Synthesis.Strategy)
	if strategyName == "" {
		strategyName = string(StrategyManual)
	}

	strategy, stratErr := GetStrategy(strategyName)
	if stratErr != nil || !strategy.RequiresAgent {
		report, err := m.mechanicalSynthesis(outputs, effectiveCfg.Synthesis)
		if err != nil {
			return nil, fmt.Errorf("mechanical synthesis: %w", err)
		}
		if stratErr != nil {
			reason := fmt.Sprintf("fell back to mechanical: %v", stratErr)
			logger.Warn("stage3: unknown synthesis strategy; using mechanical merge",
				"strategy", strategyName, "reason", stratErr)
			report.AuditLog = append(report.AuditLog, AuditEntry{
				Timestamp: time.Now().UTC(),
				Action:    "fallback_to_mechanical",
				Details:   reason,
			})
		}
		return report, nil
	}

	result, agentErr := m.runLeadSynthesis(ctx, &effectiveCfg, outputs)
	if agentErr == nil {
		logger.Info("stage3: lead agent synthesis complete",
			"strategy", strategyName,
			"findings", len(result.Findings),
		)
		report := synthesisResultToReport(result, strategy)
		finalizeSynthesisReportPolicy(report, outputs, effectiveCfg.Synthesis)
		return report, nil
	}

	reason := fmt.Sprintf("fell back to mechanical: %v", agentErr)
	logger.Warn("stage3: lead agent synthesis unavailable; using mechanical merge",
		"strategy", strategyName,
		"reason", agentErr,
	)
	report, err := m.mechanicalSynthesis(outputs, effectiveCfg.Synthesis)
	if err != nil {
		return nil, fmt.Errorf("mechanical synthesis: %w", err)
	}
	report.AuditLog = append(report.AuditLog, AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    "fallback_to_mechanical",
		Details:   reason,
	})
	return report, nil
}

// runLeadSynthesis dispatches the synthesis prompt to the designated lead
// pane and collects/parses the response.
func (m *EnsembleManager) runLeadSynthesis(ctx context.Context, cfg *EnsembleConfig, outputs []ModeOutput) (*SynthesisResult, error) {
	lead, err := m.leadAgentConfig(cfg)
	if err != nil {
		return nil, err
	}

	synth, err := NewSynthesizer(cfg.Synthesis)
	if err != nil {
		return nil, fmt.Errorf("create synthesizer: %w", err)
	}
	synth.Lead = lead

	input := &SynthesisInput{
		Outputs:          outputs,
		OriginalQuestion: cfg.Question,
		Config:           cfg.Synthesis,
	}

	return synth.agentSynthesize(ctx, input)
}

// leadAgentConfig designates the lead pane for agent-based synthesis: the
// first active/done assignment in the persisted session whose pane still
// exists. Dispatch goes through the gated swarm.PromptInjector path.
func (m *EnsembleManager) leadAgentConfig(cfg *EnsembleConfig) (*LeadAgentConfig, error) {
	session, err := LoadSession(cfg.SessionName)
	if err != nil {
		return nil, fmt.Errorf("no lead pane available: load session state: %w", err)
	}
	if session == nil || len(session.Assignments) == 0 {
		return nil, errors.New("no lead pane available: session has no assignments")
	}

	panes, err := m.tmuxClient().GetPanes(cfg.SessionName)
	if err != nil {
		return nil, fmt.Errorf("no lead pane available: get panes: %w", err)
	}
	targets := buildPaneTargetMap(cfg.SessionName, panes)

	var chosen *ModeAssignment
	for i := range session.Assignments {
		a := &session.Assignments[i]
		if a.Status != AssignmentActive && a.Status != AssignmentDone {
			continue
		}
		if targets[a.PaneName] == "" {
			continue
		}
		chosen = a
		break
	}
	if chosen == nil {
		return nil, errors.New("no lead pane available: no responsive active/done assignment pane found")
	}

	injector := m.promptInjector()
	injector.TmuxClient = m.tmuxClient()

	timeout := cfg.Budget.TimeoutPerMode
	if timeout <= 0 {
		timeout = DefaultLeadSynthesisTimeout
	}

	paneTarget := targets[chosen.PaneName]
	collector := NewPaneSynthesisCollector(m.tmuxClient())
	if timeout > 0 && timeout < collector.PollInterval {
		collector.PollInterval = max(100*time.Millisecond, timeout/5)
	}
	if baseline, err := m.tmuxClient().CapturePaneOutput(paneTarget, collector.MaxLines); err == nil {
		collector.Baseline = baseline
	}

	return &LeadAgentConfig{
		PaneTarget: paneTarget,
		AgentType:  chosen.AgentType,
		Timeout:    timeout,
		Dispatcher: promptInjectorDispatcher{injector: injector},
		Collector:  collector,
	}, nil
}

// synthesisResultToReport converts an agent SynthesisResult into the
// pipeline's SynthesisReport shape.
func synthesisResultToReport(result *SynthesisResult, strategy *StrategyConfig) *SynthesisReport {
	if result == nil {
		return nil
	}
	strategyName := "agent"
	if strategy != nil {
		strategyName = "agent:" + string(strategy.Name)
	}
	generatedAt := result.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	report := &SynthesisReport{
		GeneratedAt:            generatedAt,
		Strategy:               strategyName,
		ConsolidatedThesis:     result.Summary,
		TopFindings:            result.Findings,
		UnifiedRisks:           result.Risks,
		UnifiedRecommendations: result.Recommendations,
		OpenQuestions:          result.QuestionsForUser,
		Confidence:             result.Confidence,
		RawOutputs:             result.RawOutputs,
		TokensUsed:             result.TokensUsed,
	}
	report.AuditLog = append(report.AuditLog, AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    "agent_synthesis",
		Details:   fmt.Sprintf("lead agent synthesized %d findings via strategy %s", len(result.Findings), strategyName),
	})
	return report
}

// promptInjectorDispatcher adapts swarm.PromptInjector to the
// SynthesisDispatcher port (the gated dispatch path).
type promptInjectorDispatcher struct {
	injector *swarm.PromptInjector
}

func (d promptInjectorDispatcher) DispatchSynthesisPrompt(paneTarget, agentType, prompt string) error {
	if d.injector == nil {
		return errors.New("prompt injector is nil")
	}
	return d.injector.InjectPrompt(paneTarget, agentType, prompt)
}

// mechanicalSynthesis performs basic merging without an AI synthesizer agent.
// This is the baseline implementation and the honest-degradation fallback
// for agent-based strategies when no lead pane is available.
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
				Confidence: 0.8,
			})
		}
	}

	// Create consolidated thesis from mode theses
	report.ConsolidatedThesis = ConsolidateTheses(outputs)
	if report.ConsolidatedThesis == "" {
		for _, o := range outputs {
			if o.Thesis != "" {
				report.ConsolidatedThesis = o.Thesis
				break
			}
		}
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

	finalizeSynthesisReportPolicy(report, outputs, synthCfg)

	// Add audit entry
	report.AuditLog = append(report.AuditLog, AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    "mechanical_synthesis",
		Details:   fmt.Sprintf("combined %d mode outputs", len(outputs)),
	})

	return report, nil
}

func finalizeSynthesisReportPolicy(report *SynthesisReport, outputs []ModeOutput, synthCfg SynthesisConfig) {
	if report == nil {
		return
	}

	seen := make(map[string]int, len(report.TopFindings))
	for _, o := range outputs {
		for _, f := range o.TopFindings {
			seen[f.Finding]++
		}
	}

	if synthCfg.MinConfidence > 0 && len(report.TopFindings) > 0 {
		filtered := make([]Finding, 0, len(report.TopFindings))
		for _, f := range report.TopFindings {
			if f.Confidence >= synthCfg.MinConfidence {
				filtered = append(filtered, f)
			}
		}
		report.TopFindings = filtered
	}

	normConflict := normalizeConflictResolutionPolicy(synthCfg.ConflictResolution)
	switch normConflict {
	case "consensus", "majority":
		hasMulti := false
		for _, f := range report.TopFindings {
			if seen[f.Finding] > 1 {
				hasMulti = true
				break
			}
		}
		sort.SliceStable(report.TopFindings, func(i, j int) bool {
			ci, cj := seen[report.TopFindings[i].Finding], seen[report.TopFindings[j].Finding]
			if ci != cj {
				return ci > cj
			}
			return report.TopFindings[i].Confidence > report.TopFindings[j].Confidence
		})
		if hasMulti && normConflict == "consensus" {
			consensus := make([]Finding, 0, len(report.TopFindings))
			for _, f := range report.TopFindings {
				if seen[f.Finding] > 1 {
					consensus = append(consensus, f)
				}
			}
			if len(consensus) > 0 {
				report.TopFindings = consensus
			}
		}
	case "confidence", "highest_confidence", "weighted":
		sort.SliceStable(report.TopFindings, func(i, j int) bool {
			return report.TopFindings[i].Confidence > report.TopFindings[j].Confidence
		})
	}

	maxFindings := synthCfg.MaxFindings
	if maxFindings <= 0 {
		maxFindings = 20
	}
	if len(report.TopFindings) > maxFindings {
		report.TopFindings = report.TopFindings[:maxFindings]
	}

	if len(outputs) >= 2 {
		if audit, err := NewDisagreementAuditor(outputs, nil).Audit(); err == nil && audit != nil {
			resolvedAudit := applyConflictResolutionToAudit(audit, outputs, synthCfg.ConflictResolution)
			if resolvedAudit == nil || len(resolvedAudit.Conflicts) == 0 {
				report.Disagreements = nil
			} else {
				method := normConflict
				if method == "" {
					method = "highlight"
				}
				disagreements := make([]SynthesisDisagreement, 0, len(resolvedAudit.Conflicts))
				for _, c := range resolvedAudit.Conflicts {
					positions := make([]DisagreementPosition, 0, len(c.Positions))
					for _, p := range c.Positions {
						positions = append(positions, DisagreementPosition{
							ModeID:     p.ModeID,
							Position:   p.Position,
							Confidence: p.Confidence,
							Evidence:   p.Evidence,
						})
					}
					disagreements = append(disagreements, SynthesisDisagreement{
						Topic:            c.Topic,
						Positions:        positions,
						Resolution:       c.ResolutionPath,
						ResolutionMethod: method,
					})
				}
				report.Disagreements = disagreements
			}
		}
	}

	if synthCfg.IncludeRawOutputs {
		report.RawOutputs = buildRawOutputsMap(outputs)
	} else {
		report.RawOutputs = nil
	}

	report.TokensUsed = estimateSynthesisReportTokens(report)
	if synthCfg.MaxSynthesisTokens > 0 && report.TokensUsed > synthCfg.MaxSynthesisTokens {
		capSynthesisReportTokens(report, synthCfg.MaxSynthesisTokens)
	}
}

func estimateSynthesisReportTokens(report *SynthesisReport) int {
	if report == nil {
		return 0
	}
	clone := *report
	clone.TokensUsed = 0
	data, err := json.Marshal(clone)
	if err != nil {
		return tokenpkg.EstimateTokensWithLanguageHint(report.ConsolidatedThesis, tokenpkg.ContentMarkdown)
	}
	return tokenpkg.EstimateTokensWithLanguageHint(string(data), tokenpkg.ContentJSON)
}

func capSynthesisReportTokens(report *SynthesisReport, maxTokens int) {
	if report == nil || maxTokens <= 0 {
		return
	}
	recompute := func() bool {
		report.TokensUsed = estimateSynthesisReportTokens(report)
		return report.TokensUsed <= maxTokens
	}
	if recompute() {
		return
	}
	if len(report.RawOutputs) > 0 {
		keys := make([]string, 0, len(report.RawOutputs))
		for k := range report.RawOutputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if recompute() {
				return
			}
			report.RawOutputs[k] = util.SafeSlice(report.RawOutputs[k], 64)
		}
	}
	for len(report.OpenQuestions) > 0 && !recompute() {
		report.OpenQuestions = report.OpenQuestions[:len(report.OpenQuestions)-1]
	}
	for len(report.UnifiedRecommendations) > 0 && !recompute() {
		report.UnifiedRecommendations = report.UnifiedRecommendations[:len(report.UnifiedRecommendations)-1]
	}
	for len(report.UnifiedRisks) > 0 && !recompute() {
		report.UnifiedRisks = report.UnifiedRisks[:len(report.UnifiedRisks)-1]
	}
	for len(report.Disagreements) > 0 && !recompute() {
		report.Disagreements = report.Disagreements[:len(report.Disagreements)-1]
	}
	for len(report.Agreements) > 0 && !recompute() {
		report.Agreements = report.Agreements[:len(report.Agreements)-1]
	}
	for len(report.TopFindings) > 1 && !recompute() {
		report.TopFindings = report.TopFindings[:len(report.TopFindings)-1]
	}
	if len(report.TopFindings) == 1 && !recompute() {
		report.TopFindings[0].Reasoning = ""
		report.TopFindings[0].Finding = util.SafeSlice(report.TopFindings[0].Finding, 64)
	}
	for len(report.ConsolidatedThesis) > 24 && !recompute() {
		nextLen := len(report.ConsolidatedThesis) / 2
		if nextLen < 24 {
			nextLen = 24
		}
		report.ConsolidatedThesis = util.SafeSlice(report.ConsolidatedThesis, nextLen)
		if nextLen == 24 {
			break
		}
	}
	if !recompute() && report.TokensUsed > maxTokens {
		report.TokensUsed = maxTokens
	}
}

// ResumeOptions configures checkpoint resume execution.
type ResumeOptions struct {
	SkipDone       bool
	ProjectDir     string
	AgentType      string
	CollectTimeout time.Duration
	Synthesis      SynthesisConfig
	Budget         BudgetConfig
	Cache          CacheConfig
	EarlyStop      EarlyStopConfig
}

// ResumeResult summarizes the outcome of resuming an ensemble run from a checkpoint.
type ResumeResult struct {
	RunID              string           `json:"run_id"`
	SessionName        string           `json:"session_name"`
	Question           string           `json:"question"`
	Status             EnsembleStatus   `json:"status"`
	SkipDone           bool             `json:"skip_done"`
	CompletedModes     []string         `json:"completed_modes"`
	PendingModes       []string         `json:"pending_modes"`
	ErrorModes         []string         `json:"error_modes,omitempty"`
	ModesToRerun       []string         `json:"modes_to_rerun"`
	ModesExecuted      []string         `json:"modes_executed"`
	ContextHash        string           `json:"context_hash,omitempty"`
	SynthesisReport    *SynthesisReport `json:"synthesis_report,omitempty"`
	UpdatedCheckpoints int              `json:"updated_checkpoints"`
}

// RerunModeOptions configures single-mode re-execution within an existing checkpoint run.
type RerunModeOptions struct {
	ProjectDir     string
	AgentType      string
	Synthesize     bool
	CollectTimeout time.Duration
	Synthesis      SynthesisConfig
	Budget         BudgetConfig
	Cache          CacheConfig
}

// RerunModeResult summarizes the outcome of re-running a single mode.
type RerunModeResult struct {
	RunID           string           `json:"run_id"`
	SessionName     string           `json:"session_name"`
	ModeID          string           `json:"mode_id"`
	AgentType       string           `json:"agent_type"`
	PaneName        string           `json:"pane_name,omitempty"`
	PreviousStatus  string           `json:"previous_status"`
	CurrentStatus   string           `json:"current_status"`
	TokensUsed      int              `json:"tokens_used"`
	ContextHash     string           `json:"context_hash,omitempty"`
	CapturedAt      time.Time        `json:"captured_at"`
	Synthesized     bool             `json:"synthesized"`
	SynthesisReport *SynthesisReport `json:"synthesis_report,omitempty"`
}

// ResumeRun executes remaining (or all when !opts.SkipDone) modes for a checkpointed run,
// persists captured mode outputs into CheckpointStore, updates _meta.json, and runs Stage 3 synthesis.
func (m *EnsembleManager) ResumeRun(ctx context.Context, store *CheckpointStore, runID string, opts ResumeOptions) (*ResumeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return nil, errors.New("checkpoint store is nil")
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint metadata %q: %w", runID, err)
	}
	checkpoints, err := store.LoadAllCheckpoints(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoints %q: %w", runID, err)
	}

	catalog, err := m.catalog()
	if err != nil {
		return nil, fmt.Errorf("load mode catalog: %w", err)
	}

	completedSet := make(map[string]struct{}, len(meta.CompletedIDs))
	pendingSet := make(map[string]struct{}, len(meta.PendingIDs))
	errorSet := make(map[string]struct{}, len(meta.ErrorIDs))
	var orderedAll []string
	seenAll := make(map[string]struct{})
	addOrdered := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seenAll[id]; !ok {
			seenAll[id] = struct{}{}
			orderedAll = append(orderedAll, id)
		}
	}

	for _, id := range meta.CompletedIDs {
		completedSet[id] = struct{}{}
		addOrdered(id)
	}
	for _, id := range meta.PendingIDs {
		pendingSet[id] = struct{}{}
		addOrdered(id)
	}
	for _, id := range meta.ErrorIDs {
		errorSet[id] = struct{}{}
		addOrdered(id)
	}
	for _, cp := range checkpoints {
		addOrdered(cp.ModeID)
		switch cp.Status {
		case string(AssignmentDone):
			if cp.Output != nil {
				completedSet[cp.ModeID] = struct{}{}
			}
		case string(AssignmentError):
			if _, done := completedSet[cp.ModeID]; !done {
				errorSet[cp.ModeID] = struct{}{}
			}
		default:
			if _, done := completedSet[cp.ModeID]; !done {
				pendingSet[cp.ModeID] = struct{}{}
			}
		}
	}

	modesToRun := make([]string, 0, len(orderedAll))
	if opts.SkipDone {
		for _, id := range orderedAll {
			if _, done := completedSet[id]; done {
				continue
			}
			modesToRun = append(modesToRun, id)
		}
	} else {
		modesToRun = append(modesToRun, orderedAll...)
	}

	resolvedModes := make([]*ReasoningMode, 0, len(modesToRun))
	canonicalToRun := make([]string, 0, len(modesToRun))
	for _, rawID := range modesToRun {
		mode := resolveCatalogMode(catalog, rawID)
		if mode == nil {
			return nil, fmt.Errorf("mode %q in checkpoint %q not found in catalog", rawID, meta.RunID)
		}
		resolvedModes = append(resolvedModes, mode)
		canonicalToRun = append(canonicalToRun, mode.ID)
	}

	sessionName := strings.TrimSpace(meta.SessionName)
	if sessionName == "" {
		sessionName = "ensemble-" + meta.RunID
		meta.SessionName = sessionName
	}

	contextPack, err := m.loadOrBuildCheckpointContext(ctx, store, meta, orderedAll, opts.ProjectDir, opts.Synthesis, opts.Budget, opts.Cache)
	if err != nil {
		return nil, err
	}
	if contextPack != nil && contextPack.Hash != "" {
		meta.ContextHash = contextPack.Hash
	}

	res := &ResumeResult{
		RunID:        meta.RunID,
		SessionName:  sessionName,
		Question:     meta.Question,
		SkipDone:     opts.SkipDone,
		ModesToRerun: canonicalToRun,
		ContextHash:  meta.ContextHash,
	}

	if len(canonicalToRun) > 0 {
		meta.Status = EnsembleActive
		if err := store.SaveMetadata(*meta); err != nil {
			return nil, fmt.Errorf("update checkpoint metadata active: %w", err)
		}

		agentType := strings.TrimSpace(opts.AgentType)
		if agentType == "" {
			agentType = string(tmux.AgentClaude)
		}

		assignments, targets, err := m.ensureSessionPanesForModes(ctx, sessionName, opts.ProjectDir, canonicalToRun, agentType)
		if err != nil {
			meta.Status = EnsembleError
			_ = store.SaveMetadata(*meta)
			return nil, err
		}

		injector := m.ensembleInjector()
		tokenCap := opts.Budget.MaxTokensPerMode
		if tokenCap <= 0 {
			tokenCap = DefaultBudgetConfig().MaxTokensPerMode
		}

		injectTargets := make([]ModeAssignment, len(assignments))
		for i, a := range assignments {
			injectTargets[i] = a
			if t := targets[a.PaneName]; t != "" {
				injectTargets[i].PaneName = t
			}
		}

		var activeAssignments []ModeAssignment
		injectErrors := make(map[string]string)
		if len(injectTargets) > 1 {
			batchRes, batchErr := injector.InjectEnsemble(injectTargets, meta.Question, catalog, contextPack, tokenCap)
			for i, a := range assignments {
				if batchRes != nil && i < len(batchRes.Results) && batchRes.Results[i].Success {
					a.Status = AssignmentActive
					activeAssignments = append(activeAssignments, a)
				} else {
					errMsg := "prompt injection failed"
					if batchRes != nil && i < len(batchRes.Results) && batchRes.Results[i].Error != "" {
						errMsg = batchRes.Results[i].Error
					} else if batchErr != nil {
						errMsg = batchErr.Error()
					}
					injectErrors[a.ModeID] = errMsg
				}
			}
		} else if len(injectTargets) == 1 {
			injRes, injErr := injector.InjectWithMode(
				injectTargets[0].PaneName,
				resolvedModes[0],
				meta.Question,
				assignments[0].AgentType,
				contextPack,
				tokenCap,
				"",
			)
			if injErr == nil && injRes != nil && injRes.Success {
				assignments[0].Status = AssignmentActive
				activeAssignments = append(activeAssignments, assignments[0])
			} else {
				errMsg := "prompt injection failed"
				if injErr != nil {
					errMsg = injErr.Error()
				} else if injRes != nil && injRes.Error != "" {
					errMsg = injRes.Error
				}
				injectErrors[assignments[0].ModeID] = errMsg
			}
		}

		collectTimeout := opts.CollectTimeout
		if collectTimeout <= 0 {
			collectTimeout = opts.Budget.TimeoutPerMode
		}
		if collectTimeout <= 0 {
			collectTimeout = 30 * time.Second
		}

		var collectedByMode map[string]ModeOutput
		if len(activeAssignments) > 0 {
			var ctrl *EarlyStopController
			if opts.EarlyStop.Enabled {
				ctrl = NewEarlyStopController(sessionName, opts.EarlyStop)
				for _, cp := range checkpoints {
					if cp.Output != nil && cp.Status == string(AssignmentDone) {
						raw := cp.Output.RawOutput
						if strings.TrimSpace(raw) == "" {
							raw = cp.Output.Thesis
						}
						ctrl.RecordCompletedMode(cp.ModeID, raw)
					}
				}
			}
			_, byMode, _, _ := m.collectAssignedModeOutputsWithEarlyStop(ctx, sessionName, activeAssignments, collectTimeout, ctrl)
			collectedByMode = byMode
		}

		now := time.Now().UTC()
		var runErrs []error
		for i, modeID := range canonicalToRun {
			rawID := modesToRun[i]
			delete(pendingSet, rawID)
			delete(pendingSet, modeID)
			delete(errorSet, rawID)
			delete(errorSet, modeID)

			if out, ok := collectedByMode[modeID]; ok {
				outCopy := out
				if outCopy.GeneratedAt.IsZero() {
					outCopy.GeneratedAt = now
				}
				cp := ModeCheckpoint{
					ModeID:      modeID,
					Output:      &outCopy,
					Status:      string(AssignmentDone),
					CapturedAt:  now,
					ContextHash: meta.ContextHash,
					TokensUsed:  EstimateModeOutputTokens(&outCopy),
				}
				if err := store.SaveCheckpoint(meta.RunID, cp); err != nil {
					return nil, fmt.Errorf("save mode checkpoint %q: %w", modeID, err)
				}
				completedSet[modeID] = struct{}{}
				res.ModesExecuted = append(res.ModesExecuted, modeID)
				res.UpdatedCheckpoints++
				for j := range assignments {
					if assignments[j].ModeID == modeID {
						assignments[j].Status = AssignmentDone
						assignments[j].CompletedAt = &now
					}
				}
			} else {
				errMsg := injectErrors[modeID]
				if errMsg == "" {
					errMsg = fmt.Sprintf("timed out after %s waiting for valid mode output", collectTimeout)
				}
				cp := ModeCheckpoint{
					ModeID:      modeID,
					Status:      string(AssignmentError),
					CapturedAt:  now,
					ContextHash: meta.ContextHash,
					Error:       errMsg,
				}
				_ = store.SaveCheckpoint(meta.RunID, cp)
				delete(completedSet, modeID)
				errorSet[modeID] = struct{}{}
				runErrs = append(runErrs, fmt.Errorf("mode %s: %s", modeID, errMsg))
				for j := range assignments {
					if assignments[j].ModeID == modeID {
						assignments[j].Status = AssignmentError
						assignments[j].Error = errMsg
						assignments[j].CompletedAt = &now
					}
				}
			}
		}

		m.syncSessionAssignments(sessionName, meta.Question, opts.Synthesis.Strategy, assignments)
		rebuildMetadataModeSlices(meta, orderedAll, completedSet, pendingSet, errorSet)
		if err := store.SaveMetadata(*meta); err != nil {
			return nil, fmt.Errorf("save checkpoint progress: %w", err)
		}
		if len(res.ModesExecuted) == 0 && len(runErrs) > 0 {
			meta.Status = EnsembleError
			_ = store.SaveMetadata(*meta)
			return nil, errors.Join(runErrs...)
		}
	} else {
		rebuildMetadataModeSlices(meta, orderedAll, completedSet, pendingSet, errorSet)
	}

	completedPtrs, err := store.GetCompletedOutputs(meta.RunID)
	if err != nil {
		return nil, fmt.Errorf("load completed outputs: %w", err)
	}
	completedOutputs := derefModeOutputs(completedPtrs)

	if len(canonicalToRun) == 0 {
		if existing, loadErr := store.LoadSynthesisReport(meta.RunID); loadErr == nil && existing != nil {
			res.SynthesisReport = existing
		}
	}

	if res.SynthesisReport == nil && len(completedOutputs) > 0 {
		meta.Status = EnsembleSynthesizing
		if err := store.SaveMetadata(*meta); err != nil {
			return nil, fmt.Errorf("save synthesizing status: %w", err)
		}

		stageBudget := opts.Budget
		if opts.CollectTimeout > 0 {
			stageBudget.TimeoutPerMode = opts.CollectTimeout
		}
		stageCfg := &EnsembleConfig{
			SessionName:   sessionName,
			Question:      meta.Question,
			Modes:         extractModeIDsFromOutputs(completedOutputs),
			AllowAdvanced: true,
			ProjectDir:    opts.ProjectDir,
			Synthesis:     opts.Synthesis,
			Budget:        stageBudget,
			Cache:         opts.Cache,
		}
		stage3Res, err := m.RunStage3(ctx, stageCfg, completedOutputs)
		if err != nil {
			meta.Status = EnsembleError
			_ = store.SaveMetadata(*meta)
			return nil, fmt.Errorf("stage3 synthesis: %w", err)
		}
		if stage3Res != nil && stage3Res.Report != nil {
			if err := store.SaveSynthesisReport(meta.RunID, stage3Res.Report); err != nil {
				return nil, fmt.Errorf("save synthesis report: %w", err)
			}
			res.SynthesisReport = stage3Res.Report
			m.recordSessionSynthesis(sessionName, stage3Res.Report)
		}
	}

	if len(meta.PendingIDs) == 0 && len(meta.ErrorIDs) == 0 {
		meta.Status = EnsembleComplete
	} else {
		meta.Status = EnsembleActive
	}
	if err := store.SaveMetadata(*meta); err != nil {
		return nil, fmt.Errorf("save final checkpoint metadata: %w", err)
	}

	res.Status = meta.Status
	res.CompletedModes = nonNilStrings(meta.CompletedIDs)
	res.PendingModes = nonNilStrings(meta.PendingIDs)
	res.ErrorModes = meta.ErrorIDs
	if res.ModesToRerun == nil {
		res.ModesToRerun = []string{}
	}
	if res.ModesExecuted == nil {
		res.ModesExecuted = []string{}
	}
	return res, nil
}

// RerunMode re-executes a single mode in an isolated tmux pane, persists the updated
// mode output in CheckpointStore, invalidates stale synthesis, and optionally re-runs Stage 3 synthesis.
func (m *EnsembleManager) RerunMode(ctx context.Context, store *CheckpointStore, runID, modeArg string, opts RerunModeOptions) (*RerunModeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return nil, errors.New("checkpoint store is nil")
	}

	meta, err := store.LoadMetadata(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint metadata %q: %w", runID, err)
	}
	checkpoints, err := store.LoadAllCheckpoints(runID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoints %q: %w", runID, err)
	}

	catalog, err := m.catalog()
	if err != nil {
		return nil, fmt.Errorf("load mode catalog: %w", err)
	}

	mode := resolveCatalogMode(catalog, modeArg)
	targetModeID := strings.TrimSpace(modeArg)
	if mode != nil {
		targetModeID = mode.ID
	}

	inRun, prevStatus := findModeInCheckpointRun(meta, checkpoints, modeArg, targetModeID)
	if !inRun {
		return nil, fmt.Errorf("mode %q not found in checkpoint %q", modeArg, meta.RunID)
	}
	if mode == nil {
		return nil, fmt.Errorf("mode %q not found in mode catalog", modeArg)
	}

	sessionName := strings.TrimSpace(meta.SessionName)
	if sessionName == "" {
		sessionName = "ensemble-" + meta.RunID
		meta.SessionName = sessionName
	}

	agentType := strings.TrimSpace(opts.AgentType)
	if agentType == "" {
		if existing, loadErr := LoadSession(sessionName); loadErr == nil && existing != nil {
			for _, a := range existing.Assignments {
				if a.ModeID == mode.ID && strings.TrimSpace(a.AgentType) != "" {
					agentType = a.AgentType
					break
				}
			}
		}
	}
	if agentType == "" {
		agentType = string(tmux.AgentClaude)
	}

	orderedAll := collectOrderedCheckpointModes(meta, checkpoints)
	contextPack, err := m.loadOrBuildCheckpointContext(ctx, store, meta, orderedAll, opts.ProjectDir, opts.Synthesis, opts.Budget, opts.Cache)
	if err != nil {
		return nil, err
	}
	if contextPack != nil && contextPack.Hash != "" {
		meta.ContextHash = contextPack.Hash
	}

	// Invalidate any stale synthesis report before updating the mode output.
	if err := store.InvalidateSynthesis(meta.RunID); err != nil {
		return nil, fmt.Errorf("invalidate synthesis: %w", err)
	}

	meta.Status = EnsembleActive
	if err := store.SaveMetadata(*meta); err != nil {
		return nil, fmt.Errorf("update metadata active: %w", err)
	}

	assignments, targets, err := m.ensureSessionPanesForModes(ctx, sessionName, opts.ProjectDir, []string{mode.ID}, agentType)
	if err != nil {
		return nil, err
	}
	assignment := assignments[0]
	paneTarget := targets[assignment.PaneName]
	if paneTarget == "" {
		paneTarget = assignment.PaneName
	}

	tokenCap := opts.Budget.MaxTokensPerMode
	if tokenCap <= 0 {
		tokenCap = DefaultBudgetConfig().MaxTokensPerMode
	}

	injRes, injErr := m.ensembleInjector().InjectWithMode(
		paneTarget,
		mode,
		meta.Question,
		assignment.AgentType,
		contextPack,
		tokenCap,
		"",
	)
	if injErr != nil || injRes == nil || !injRes.Success {
		errMsg := "prompt injection failed"
		if injErr != nil {
			errMsg = injErr.Error()
		} else if injRes != nil && injRes.Error != "" {
			errMsg = injRes.Error
		}
		return nil, fmt.Errorf("inject prompt for mode %s: %s", mode.ID, errMsg)
	}
	assignment.Status = AssignmentActive

	collectTimeout := opts.CollectTimeout
	if collectTimeout <= 0 {
		collectTimeout = opts.Budget.TimeoutPerMode
	}
	if collectTimeout <= 0 {
		collectTimeout = 30 * time.Second
	}

	_, byMode, _ := m.collectAssignedModeOutputs(ctx, sessionName, []ModeAssignment{assignment}, collectTimeout)
	capturedOutput, ok := byMode[mode.ID]
	if !ok {
		return nil, fmt.Errorf("timed out after %s waiting for mode %q output", collectTimeout, mode.ID)
	}

	now := time.Now().UTC()
	if capturedOutput.GeneratedAt.IsZero() {
		capturedOutput.GeneratedAt = now
	}
	tokensUsed := EstimateModeOutputTokens(&capturedOutput)
	cp := ModeCheckpoint{
		ModeID:      mode.ID,
		Output:      &capturedOutput,
		Status:      string(AssignmentDone),
		CapturedAt:  now,
		ContextHash: meta.ContextHash,
		TokensUsed:  tokensUsed,
	}
	if err := store.SaveCheckpoint(meta.RunID, cp); err != nil {
		return nil, fmt.Errorf("save mode checkpoint %q: %w", mode.ID, err)
	}

	assignment.Status = AssignmentDone
	assignment.CompletedAt = &now
	m.syncSessionAssignments(sessionName, meta.Question, opts.Synthesis.Strategy, []ModeAssignment{assignment})

	updateMetadataModeDone(meta, modeArg, mode.ID)
	if err := store.SaveMetadata(*meta); err != nil {
		return nil, fmt.Errorf("save updated metadata: %w", err)
	}

	result := &RerunModeResult{
		RunID:          meta.RunID,
		SessionName:    sessionName,
		ModeID:         mode.ID,
		AgentType:      assignment.AgentType,
		PaneName:       assignment.PaneName,
		PreviousStatus: prevStatus,
		CurrentStatus:  string(AssignmentDone),
		TokensUsed:     tokensUsed,
		ContextHash:    meta.ContextHash,
		CapturedAt:     now,
	}

	shouldSynthesize := opts.Synthesize || (len(meta.PendingIDs) == 0 && len(meta.ErrorIDs) == 0)
	if shouldSynthesize {
		completedPtrs, err := store.GetCompletedOutputs(meta.RunID)
		if err != nil {
			return nil, fmt.Errorf("load completed outputs: %w", err)
		}
		completedOutputs := derefModeOutputs(completedPtrs)
		if len(completedOutputs) > 0 {
			meta.Status = EnsembleSynthesizing
			_ = store.SaveMetadata(*meta)

			stageBudget := opts.Budget
			if opts.CollectTimeout > 0 {
				stageBudget.TimeoutPerMode = opts.CollectTimeout
			}
			stageCfg := &EnsembleConfig{
				SessionName:   sessionName,
				Question:      meta.Question,
				Modes:         extractModeIDsFromOutputs(completedOutputs),
				AllowAdvanced: true,
				ProjectDir:    opts.ProjectDir,
				Synthesis:     opts.Synthesis,
				Budget:        stageBudget,
				Cache:         opts.Cache,
			}
			stage3Res, err := m.RunStage3(ctx, stageCfg, completedOutputs)
			if err != nil {
				return nil, fmt.Errorf("stage3 synthesis: %w", err)
			}
			if stage3Res != nil && stage3Res.Report != nil {
				if err := store.SaveSynthesisReport(meta.RunID, stage3Res.Report); err != nil {
					return nil, fmt.Errorf("save synthesis report: %w", err)
				}
				result.Synthesized = true
				result.SynthesisReport = stage3Res.Report
				m.recordSessionSynthesis(sessionName, stage3Res.Report)
			}
		}
	}

	if len(meta.PendingIDs) == 0 && len(meta.ErrorIDs) == 0 && result.Synthesized {
		meta.Status = EnsembleComplete
	} else {
		meta.Status = EnsembleActive
	}
	if err := store.SaveMetadata(*meta); err != nil {
		return nil, fmt.Errorf("save final metadata: %w", err)
	}

	return result, nil
}

func (m *EnsembleManager) loadOrBuildCheckpointContext(
	ctx context.Context,
	store *CheckpointStore,
	meta *CheckpointMetadata,
	allModeIDs []string,
	projectDir string,
	synthCfg SynthesisConfig,
	budgetCfg BudgetConfig,
	cacheCfg CacheConfig,
) (*ContextPack, error) {
	if pack, err := store.LoadContextPack(meta.RunID); err == nil && pack != nil {
		if budgetCfg.ContextReserveTokens > 0 {
			pack = CapContextPackTokens(pack, budgetCfg.ContextReserveTokens)
		}
		return pack, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logger().Warn("failed to load saved context pack; regenerating", "run_id", meta.RunID, "error", err)
	}

	catalog, _ := m.catalog()
	validModes := make([]string, 0, len(allModeIDs))
	for _, raw := range allModeIDs {
		if mode := resolveCatalogMode(catalog, raw); mode != nil {
			validModes = append(validModes, mode.ID)
		}
	}

	var pack *ContextPack
	if len(validModes) > 0 && strings.TrimSpace(meta.Question) != "" {
		stageCfg := &EnsembleConfig{
			SessionName:   meta.SessionName,
			Question:      meta.Question,
			Modes:         validModes,
			AllowAdvanced: true,
			ProjectDir:    projectDir,
			Synthesis:     synthCfg,
			Budget:        budgetCfg,
			Cache:         cacheCfg,
		}
		if generated, err := m.RunStage1(ctx, stageCfg); err == nil {
			pack = generated
		}
	}
	if pack == nil {
		gen, resolvedCache := m.contextPackGenerator(projectDir, cacheCfg)
		if budgetCfg.ContextReserveTokens > 0 {
			gen.MaxTokens = budgetCfg.ContextReserveTokens
		}
		generated, err := gen.Generate(meta.Question, "", resolvedCache)
		if err != nil {
			return nil, fmt.Errorf("generate context pack: %w", err)
		}
		pack = CapContextPackTokens(generated, budgetCfg.ContextReserveTokens)
	}

	if err := store.SaveContextPack(meta.RunID, pack); err != nil {
		m.logger().Warn("failed to save checkpoint context pack", "run_id", meta.RunID, "error", err)
	}
	return pack, nil
}

func (m *EnsembleManager) ensureSessionPanesForModes(
	ctx context.Context,
	sessionName string,
	projectDir string,
	modeIDs []string,
	agentType string,
) ([]ModeAssignment, map[string]string, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return nil, nil, err
	}
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return nil, nil, fmt.Errorf("invalid session name %q: %w", sessionName, err)
	}
	if strings.TrimSpace(agentType) == "" {
		agentType = string(tmux.AgentClaude)
	}

	client := m.tmuxClient()
	orchestrator := m.sessionOrchestrator()
	orchestrator.TmuxClient = client
	launcher := m.paneLauncher()
	launcher.TmuxClient = client
	launcher.SessionOrchestrator = orchestrator

	now := time.Now().UTC()

	if !client.SessionExists(sessionName) {
		paneSpecs := make([]swarm.PaneSpec, len(modeIDs))
		for i := range modeIDs {
			paneSpecs[i] = swarm.PaneSpec{
				Index:     i + 1,
				AgentType: agentType,
				Project:   projectDir,
			}
		}
		spec := swarm.SessionSpec{
			Name:      sessionName,
			AgentType: "ensemble",
			PaneCount: len(paneSpecs),
			Panes:     paneSpecs,
		}
		plan := &swarm.SwarmPlan{Sessions: []swarm.SessionSpec{spec}}
		createRes, createErr := orchestrator.CreateSessionsContext(ctx, plan)
		if createErr != nil || createRes == nil || len(createRes.Sessions) == 0 || createRes.Sessions[0].Error != nil {
			if createErr == nil && createRes != nil && len(createRes.Sessions) > 0 {
				createErr = createRes.Sessions[0].Error
			}
			return nil, nil, fmt.Errorf("create ensemble session %q: %w", sessionName, createErr)
		}
		if firstWin, winErr := client.GetFirstWindowContext(ctx, sessionName); winErr == nil {
			_, _ = client.RunContext(ctx, "resize-window", "-t", tmux.ExactTarget(fmt.Sprintf("%s:%d", sessionName, firstWin)), "-x", "240", "-y", "80")
		}
		if _, launchErr := launcher.LaunchSession(ctx, spec, 150*time.Millisecond); launchErr != nil {
			return nil, nil, fmt.Errorf("launch agents in session %q: %w", sessionName, launchErr)
		}
		panes, err := client.GetPanesContext(ctx, sessionName)
		if err != nil {
			return nil, nil, fmt.Errorf("get panes for session %q: %w", sessionName, err)
		}
		targets := buildPaneTargetMap(sessionName, panes)
		assignments := make([]ModeAssignment, 0, len(modeIDs))
		for i, modeID := range modeIDs {
			title := tmux.FormatPaneName(sessionName, agentType, i+1, "")
			assignments = append(assignments, ModeAssignment{
				ModeID:     modeID,
				PaneName:   title,
				AgentType:  agentType,
				Status:     AssignmentPending,
				AssignedAt: now,
			})
		}
		return assignments, targets, nil
	}

	if firstWin, winErr := client.GetFirstWindowContext(ctx, sessionName); winErr == nil {
		_, _ = client.RunContext(ctx, "resize-window", "-t", tmux.ExactTarget(fmt.Sprintf("%s:%d", sessionName, firstWin)), "-x", "240", "-y", "80")
	}

	assignments := make([]ModeAssignment, 0, len(modeIDs))
	targets := make(map[string]string, len(modeIDs)*2)
	for _, modeID := range modeIDs {
		title, paneID, err := m.spawnIsolatedModePane(ctx, sessionName, projectDir, agentType)
		if err != nil {
			return nil, nil, err
		}
		targets[title] = paneID
		targets[paneID] = paneID
		assignments = append(assignments, ModeAssignment{
			ModeID:     modeID,
			PaneName:   title,
			AgentType:  agentType,
			Status:     AssignmentPending,
			AssignedAt: now,
		})
	}
	return assignments, targets, nil
}

func (m *EnsembleManager) spawnIsolatedModePane(ctx context.Context, sessionName, projectDir, agentType string) (string, string, error) {
	client := m.tmuxClient()
	orchestrator := m.sessionOrchestrator()
	orchestrator.TmuxClient = client
	launcher := m.paneLauncher()
	launcher.TmuxClient = client
	launcher.SessionOrchestrator = orchestrator

	existingPanes, err := client.GetPanesContext(ctx, sessionName)
	if err != nil {
		return "", "", fmt.Errorf("list panes in session %q: %w", sessionName, err)
	}
	usedTitles := make(map[string]bool, len(existingPanes))
	nextIdx := len(existingPanes) + 1
	for _, p := range existingPanes {
		usedTitles[p.Title] = true
		if p.NTMIndex >= nextIdx {
			nextIdx = p.NTMIndex + 1
		}
		if p.Index >= nextIdx {
			nextIdx = p.Index + 1
		}
	}
	title := tmux.FormatPaneName(sessionName, agentType, nextIdx, "")
	for usedTitles[title] {
		nextIdx++
		title = tmux.FormatPaneName(sessionName, agentType, nextIdx, "")
	}

	splitDir := projectDir
	if strings.TrimSpace(splitDir) == "" {
		splitDir, _ = os.Getwd()
	}
	paneID, err := client.SplitWindowContext(ctx, sessionName, splitDir)
	if err != nil {
		return "", "", fmt.Errorf("split pane in session %q: %w", sessionName, err)
	}
	paneID = strings.TrimSpace(paneID)

	var idErr error
	for attempt := 0; attempt < 3; attempt++ {
		if idErr = client.SetPaneAgentIdentityContext(ctx, paneID, title, tmux.AgentType(agentType)); idErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if idErr != nil {
		return "", "", fmt.Errorf("set pane identity %q: %w", title, idErr)
	}

	paneSpec := swarm.PaneSpec{
		Index:     nextIdx,
		AgentType: agentType,
		Project:   projectDir,
	}
	launchRes, err := launcher.LaunchAgentInPane(ctx, sessionName, paneSpec)
	if err != nil || launchRes == nil || !launchRes.Success {
		if err == nil && launchRes != nil {
			err = errors.New(launchRes.Error)
		}
		return "", "", fmt.Errorf("launch agent in pane %q: %w", title, err)
	}
	return title, paneID, nil
}

type earlyStopCollectSummary struct {
	Triggered bool
	Reason    string
}

func (m *EnsembleManager) collectAssignedModeOutputs(
	ctx context.Context,
	sessionName string,
	assignments []ModeAssignment,
	timeout time.Duration,
) ([]ModeOutput, map[string]ModeOutput, error) {
	outputs, byMode, _, err := m.collectAssignedModeOutputsWithEarlyStop(ctx, sessionName, assignments, timeout, nil)
	return outputs, byMode, err
}

func (m *EnsembleManager) collectAssignedModeOutputsWithEarlyStop(
	ctx context.Context,
	sessionName string,
	assignments []ModeAssignment,
	timeout time.Duration,
	ctrl *EarlyStopController,
) ([]ModeOutput, map[string]ModeOutput, earlyStopCollectSummary, error) {
	var stopSummary earlyStopCollectSummary
	if len(assignments) == 0 {
		return nil, nil, stopSummary, nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	var auditStore *state.Store
	if ctrl != nil {
		if st, err := state.Open(""); err == nil {
			if migErr := st.Migrate(); migErr == nil {
				auditStore = st
				defer func() { _ = st.Close() }()
			} else {
				_ = st.Close()
			}
		}
	}

	capture := NewOutputCapture(m.tmuxClient())
	tempSession := &EnsembleSession{
		SessionName: sessionName,
		Assignments: assignments,
	}

	byMode := make(map[string]ModeOutput, len(assignments))
	stoppedModes := make(map[string]EarlyStopDecision)
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		captured, err := capture.CaptureAll(tempSession)
		if err != nil {
			lastErr = err
		}
		for _, item := range captured {
			if _, stopped := stoppedModes[item.ModeID]; stopped {
				continue
			}
			if item.Parsed != nil {
				out := *item.Parsed
				if strings.TrimSpace(out.RawOutput) == "" {
					out.RawOutput = item.RawOutput
				}
				if _, alreadyDone := byMode[item.ModeID]; !alreadyDone {
					byMode[item.ModeID] = out
					if ctrl != nil {
						ctrl.RecordCompletedMode(item.ModeID, out.RawOutput)
					}
				}
				continue
			}
			if ctrl != nil && strings.TrimSpace(item.RawOutput) != "" {
				tokens := item.TokenEstimate
				if tokens <= 0 {
					tokens = EstimateOutputTokens(item.RawOutput)
				}
				if ctrl.WindowReady(item.ModeID, tokens) {
					dec := ctrl.EvaluateWindow(item.ModeID, item.RawOutput, tokens)
					_, _ = PersistEarlyStopDecision(auditStore, sessionName, dec)
					if dec.ShouldStop {
						stoppedModes[item.ModeID] = dec
						stopSummary.Triggered = true
						if stopSummary.Reason == "" {
							stopSummary.Reason = fmt.Sprintf("mode %s stopped early (e_value=%.2f >= %.2f)", item.ModeID, dec.EValue, dec.Threshold)
						}
						if item.PaneName != "" {
							_ = m.tmuxClient().SendKeys(item.PaneName, "\x03", false)
						}
					}
				}
			}
		}
		if len(byMode)+len(stoppedModes) >= len(assignments) {
			break
		}

		select {
		case <-pollCtx.Done():
			outputs := orderedModeOutputsFromMap(assignments, byMode)
			if lastErr != nil {
				return outputs, byMode, stopSummary, fmt.Errorf("collect mode outputs timed out (%d/%d collected): %w", len(byMode), len(assignments), lastErr)
			}
			return outputs, byMode, stopSummary, fmt.Errorf("collect mode outputs timed out (%d/%d collected)", len(byMode), len(assignments))
		case <-ticker.C:
		}
	}

	return orderedModeOutputsFromMap(assignments, byMode), byMode, stopSummary, nil
}

func orderedModeOutputsFromMap(assignments []ModeAssignment, byMode map[string]ModeOutput) []ModeOutput {
	outputs := make([]ModeOutput, 0, len(byMode))
	seen := make(map[string]struct{}, len(byMode))
	for _, a := range assignments {
		if out, ok := byMode[a.ModeID]; ok {
			if _, dup := seen[a.ModeID]; !dup {
				seen[a.ModeID] = struct{}{}
				outputs = append(outputs, out)
			}
		}
	}
	return outputs
}

func resolveCatalogMode(catalog *ModeCatalog, ref string) *ReasoningMode {
	if catalog == nil {
		return nil
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if m := catalog.GetMode(strings.ToLower(ref)); m != nil {
		return m
	}
	if isModeCode(ref) {
		return catalog.GetModeByCode(strings.ToUpper(ref))
	}
	return nil
}

func findModeInCheckpointRun(meta *CheckpointMetadata, checkpoints []ModeCheckpoint, rawRef, canonicalID string) (bool, string) {
	if meta != nil {
		for _, id := range meta.CompletedIDs {
			if strings.EqualFold(id, rawRef) || strings.EqualFold(id, canonicalID) {
				return true, string(AssignmentDone)
			}
		}
		for _, id := range meta.PendingIDs {
			if strings.EqualFold(id, rawRef) || strings.EqualFold(id, canonicalID) {
				return true, string(AssignmentPending)
			}
		}
		for _, id := range meta.ErrorIDs {
			if strings.EqualFold(id, rawRef) || strings.EqualFold(id, canonicalID) {
				return true, string(AssignmentError)
			}
		}
	}
	for _, cp := range checkpoints {
		if strings.EqualFold(cp.ModeID, rawRef) || strings.EqualFold(cp.ModeID, canonicalID) {
			if cp.Status != "" {
				return true, cp.Status
			}
			return true, "unknown"
		}
	}
	return false, "unknown"
}

func collectOrderedCheckpointModes(meta *CheckpointMetadata, checkpoints []ModeCheckpoint) []string {
	var ordered []string
	seen := make(map[string]struct{})
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ordered = append(ordered, id)
		}
	}
	if meta != nil {
		for _, id := range meta.CompletedIDs {
			add(id)
		}
		for _, id := range meta.PendingIDs {
			add(id)
		}
		for _, id := range meta.ErrorIDs {
			add(id)
		}
	}
	for _, cp := range checkpoints {
		add(cp.ModeID)
	}
	return ordered
}

func rebuildMetadataModeSlices(
	meta *CheckpointMetadata,
	orderedAll []string,
	completedSet, pendingSet, errorSet map[string]struct{},
) {
	if meta == nil {
		return
	}
	var completed, pending, errored []string
	seen := make(map[string]struct{})
	allCandidates := append([]string(nil), orderedAll...)
	for k := range completedSet {
		allCandidates = append(allCandidates, k)
	}
	for k := range pendingSet {
		allCandidates = append(allCandidates, k)
	}
	for k := range errorSet {
		allCandidates = append(allCandidates, k)
	}
	for _, id := range allCandidates {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := completedSet[id]; ok {
			completed = append(completed, id)
		} else if _, ok := errorSet[id]; ok {
			errored = append(errored, id)
		} else if _, ok := pendingSet[id]; ok {
			pending = append(pending, id)
		}
	}
	if completed == nil {
		completed = []string{}
	}
	if pending == nil {
		pending = []string{}
	}
	meta.CompletedIDs = completed
	meta.PendingIDs = pending
	meta.ErrorIDs = errored
	total := len(completed) + len(pending) + len(errored)
	if total > meta.TotalModes {
		meta.TotalModes = total
	}
}

func updateMetadataModeDone(meta *CheckpointMetadata, rawRef, canonicalID string) {
	if meta == nil {
		return
	}
	filterOut := func(items []string) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			if strings.EqualFold(item, rawRef) || strings.EqualFold(item, canonicalID) {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	meta.PendingIDs = filterOut(meta.PendingIDs)
	meta.ErrorIDs = filterOut(meta.ErrorIDs)
	alreadyDone := false
	for i, item := range meta.CompletedIDs {
		if strings.EqualFold(item, rawRef) || strings.EqualFold(item, canonicalID) {
			meta.CompletedIDs[i] = canonicalID
			alreadyDone = true
			break
		}
	}
	if !alreadyDone {
		meta.CompletedIDs = append(meta.CompletedIDs, canonicalID)
	}
	if meta.PendingIDs == nil {
		meta.PendingIDs = []string{}
	}
	total := len(meta.CompletedIDs) + len(meta.PendingIDs) + len(meta.ErrorIDs)
	if total > meta.TotalModes {
		meta.TotalModes = total
	}
}

func derefModeOutputs(ptrs []*ModeOutput) []ModeOutput {
	outputs := make([]ModeOutput, 0, len(ptrs))
	for _, p := range ptrs {
		if p != nil {
			outputs = append(outputs, *p)
		}
	}
	return outputs
}

func extractModeIDsFromOutputs(outputs []ModeOutput) []string {
	ids := make([]string, 0, len(outputs))
	seen := make(map[string]struct{}, len(outputs))
	for _, o := range outputs {
		if o.ModeID == "" {
			continue
		}
		if _, ok := seen[o.ModeID]; !ok {
			seen[o.ModeID] = struct{}{}
			ids = append(ids, o.ModeID)
		}
	}
	return ids
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func (m *EnsembleManager) syncSessionAssignments(sessionName, question string, strategy SynthesisStrategy, updated []ModeAssignment) {
	if strings.TrimSpace(sessionName) == "" {
		return
	}
	state, err := LoadSession(sessionName)
	if err != nil || state == nil {
		if strategy == "" {
			strategy = DefaultSynthesisConfig().Strategy
		}
		state = &EnsembleSession{
			SessionName:       sessionName,
			Question:          question,
			Status:            EnsembleActive,
			SynthesisStrategy: strategy,
			CreatedAt:         time.Now().UTC(),
		}
	}
	for _, u := range updated {
		replaced := false
		for i := range state.Assignments {
			if state.Assignments[i].ModeID == u.ModeID {
				state.Assignments[i] = u
				replaced = true
				break
			}
		}
		if !replaced {
			state.Assignments = append(state.Assignments, u)
		}
	}
	_ = SaveSession(sessionName, state)
}

func (m *EnsembleManager) recordSessionSynthesis(sessionName string, report *SynthesisReport) {
	if strings.TrimSpace(sessionName) == "" || report == nil {
		return
	}
	state, err := LoadSession(sessionName)
	if err != nil || state == nil {
		return
	}
	now := time.Now().UTC()
	state.Status = EnsembleComplete
	state.SynthesizedAt = &now
	state.SynthesisOutput = report.ConsolidatedThesis
	_ = SaveSession(sessionName, state)
}
