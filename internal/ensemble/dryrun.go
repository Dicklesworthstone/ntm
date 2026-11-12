package ensemble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DryRunEnsemble computes the spawn plan without creating any sessions, panes, or state.
// Types are defined in dryrun_types.go (no build tag) for testability.
func (m *EnsembleManager) DryRunEnsemble(ctx context.Context, cfg *EnsembleConfig, opts DryRunOptions) (*DryRunPlan, error) {
	if cfg == nil {
		return nil, errors.New("ensemble config is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	logger := m.logger()
	logger.Info("dry-run ensemble",
		"session", cfg.SessionName,
		"ensemble", cfg.Ensemble,
		"modes_count", len(cfg.Modes),
	)

	// Validate basic inputs (same as SpawnEnsemble but skip tmux checks)
	if cfg.SessionName == "" {
		return nil, errors.New("session name is required")
	}
	if strings.TrimSpace(cfg.Question) == "" {
		return nil, errors.New("question is required")
	}
	if cfg.Ensemble == "" && len(cfg.Modes) == 0 {
		return nil, errors.New("either ensemble name or explicit modes are required")
	}
	if cfg.Ensemble != "" && len(cfg.Modes) > 0 {
		return nil, errors.New("ensemble name and explicit modes are mutually exclusive")
	}

	catalog, err := m.catalog()
	if err != nil {
		return nil, fmt.Errorf("load mode catalog: %w", err)
	}
	registry, err := m.registry(catalog)
	if err != nil {
		return nil, fmt.Errorf("load ensemble registry: %w", err)
	}

	// Resolve modes and config
	modeIDs, resolvedCfg, explicitSpecs, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	plan := &DryRunPlan{
		GeneratedAt: time.Now().UTC(),
		SessionName: cfg.SessionName,
		Question:    cfg.Question,
		PresetUsed:  resolvedCfg.presetName,
		Modes:       make([]DryRunMode, 0, len(modeIDs)),
		Assignments: make([]DryRunAssign, 0, len(modeIDs)),
		Validation:  DryRunValidation{Valid: true},
	}

	// Build mode details
	for _, modeID := range modeIDs {
		mode := catalog.GetMode(modeID)
		if mode == nil {
			plan.Validation.Valid = false
			plan.Validation.Errors = append(plan.Validation.Errors,
				fmt.Sprintf("mode %q not found in catalog", modeID))
			continue
		}

		plan.Modes = append(plan.Modes, DryRunMode{
			ID:        mode.ID,
			Code:      mode.Code,
			Name:      mode.Name,
			Category:  mode.Category.String(),
			Tier:      mode.Tier.String(),
			ShortDesc: mode.ShortDesc,
		})
	}

	// Build assignments preview
	agentList := expandAgentMix(cfg.AgentMix)
	if len(agentList) == 0 {
		// Default to cc agents
		agentList = make([]string, len(modeIDs))
		for i := range agentList {
			agentList[i] = "cc"
		}
	}

	// Match modes to agents based on assignment strategy
	assignments := buildDryRunAssignments(cfg.Assignment, modeIDs, explicitSpecs, agentList, catalog, resolvedCfg.budget.MaxTokensPerMode)
	plan.Assignments = assignments

	// Budget summary
	plan.Budget = DryRunBudget{
		MaxTokensPerMode:       resolvedCfg.budget.MaxTokensPerMode,
		MaxTotalTokens:         resolvedCfg.budget.MaxTotalTokens,
		SynthesisReserveTokens: resolvedCfg.budget.SynthesisReserveTokens,
		ContextReserveTokens:   resolvedCfg.budget.ContextReserveTokens,
		EstimatedTotalTokens:   resolvedCfg.budget.MaxTokensPerMode * len(modeIDs),
		ModeCount:              len(modeIDs),
	}

	// Synthesis config
	plan.Synthesis = DryRunSynthesis{
		Strategy:           resolvedCfg.synthesis.Strategy.String(),
		SynthesizerModeID:  "", // Synthesizer mode determined at runtime by strategy
		MinConfidence:      float64(resolvedCfg.synthesis.MinConfidence),
		MaxFindings:        resolvedCfg.synthesis.MaxFindings,
		IncludeRawOutputs:  resolvedCfg.synthesis.IncludeRawOutputs,
		ConflictResolution: resolvedCfg.synthesis.ConflictResolution,
	}

	// Validation warnings
	if len(modeIDs) > 10 {
		plan.Validation.Warnings = append(plan.Validation.Warnings,
			fmt.Sprintf("large ensemble with %d modes may be expensive", len(modeIDs)))
	}
	if plan.Budget.EstimatedTotalTokens > plan.Budget.MaxTotalTokens && plan.Budget.MaxTotalTokens > 0 {
		plan.Validation.Warnings = append(plan.Validation.Warnings,
			fmt.Sprintf("estimated tokens (%d) exceed budget (%d)",
				plan.Budget.EstimatedTotalTokens, plan.Budget.MaxTotalTokens))
	}
	if len(agentList) < len(modeIDs) {
		plan.Validation.Warnings = append(plan.Validation.Warnings,
			fmt.Sprintf("agent mix provides %d agents for %d modes (some modes will share agents)",
				len(agentList), len(modeIDs)))
	}

	// Optional preamble previews
	if opts.IncludePreambles {
		plan.Preambles = m.generateDryRunPreambles(ctx, modeIDs, cfg, catalog, opts.PreamblePreviewLength)
	}

	logger.Info("dry-run complete",
		"session", cfg.SessionName,
		"modes", len(plan.Modes),
		"assignments", len(plan.Assignments),
		"estimated_tokens", plan.Budget.EstimatedTotalTokens,
	)

	return plan, nil
}

// buildDryRunAssignments creates assignment previews without actual panes.
func buildDryRunAssignments(strategy string, modeIDs []string, explicitSpecs []string, agents []string, catalog *ModeCatalog, tokenBudget int) []DryRunAssign {
	assignments := make([]DryRunAssign, 0, len(modeIDs))

	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		strategy = "affinity"
	}

	switch strategy {
	case "explicit":
		// Parse explicit specs (mode:agent)
		for i, spec := range explicitSpecs {
			parts := strings.SplitN(spec, ":", 2)
			if len(parts) != 2 {
				continue
			}
			modeID := parts[0]
			agentType := parts[1]

			modeCode := ""
			if mode := catalog.GetMode(modeID); mode != nil {
				modeCode = mode.Code
			}

			assignments = append(assignments, DryRunAssign{
				ModeID:      modeID,
				ModeCode:    modeCode,
				AgentType:   agentType,
				PaneIndex:   i + 1,
				TokenBudget: tokenBudget,
			})
		}

	case "category", "affinity":
		// Group modes by category for affinity assignment
		categoryGroups := make(map[ModeCategory][]string)
		for _, modeID := range modeIDs {
			mode := catalog.GetMode(modeID)
			if mode == nil {
				continue
			}
			categoryGroups[mode.Category] = append(categoryGroups[mode.Category], modeID)
		}

		paneIdx := 1
		agentIdx := 0
		for _, modes := range categoryGroups {
			for _, modeID := range modes {
				agentType := "cc"
				if agentIdx < len(agents) {
					agentType = agents[agentIdx]
					agentIdx++
				}

				modeCode := ""
				if mode := catalog.GetMode(modeID); mode != nil {
					modeCode = mode.Code
				}

				assignments = append(assignments, DryRunAssign{
					ModeID:      modeID,
					ModeCode:    modeCode,
					AgentType:   agentType,
					PaneIndex:   paneIdx,
					TokenBudget: tokenBudget,
				})
				paneIdx++
			}
		}

	default: // round-robin
		for i, modeID := range modeIDs {
			agentType := "cc"
			if i < len(agents) {
				agentType = agents[i]
			}

			modeCode := ""
			if mode := catalog.GetMode(modeID); mode != nil {
				modeCode = mode.Code
			}

			assignments = append(assignments, DryRunAssign{
				ModeID:      modeID,
				ModeCode:    modeCode,
				AgentType:   agentType,
				PaneIndex:   i + 1,
				TokenBudget: tokenBudget,
			})
		}
	}

	return assignments
}

// generateDryRunPreambles creates preview preambles for each mode.
func (m *EnsembleManager) generateDryRunPreambles(ctx context.Context, modeIDs []string, cfg *EnsembleConfig, catalog *ModeCatalog, previewLen int) []DryRunPreamble {
	engine := NewPreambleEngine()
	preambles := make([]DryRunPreamble, 0, len(modeIDs))

	for _, modeID := range modeIDs {
		mode := catalog.GetMode(modeID)
		if mode == nil {
			continue
		}

		data := &PreambleData{
			Problem:  cfg.Question,
			Mode:     mode,
			TokenCap: cfg.Budget.MaxTokensPerMode,
		}

		rendered, err := engine.Render(data)
		if err != nil {
			m.logger().Warn("preamble render failed",
				"mode", modeID,
				"error", err,
			)
			continue
		}

		preview := rendered
		if previewLen > 0 && len(rendered) > previewLen {
			preview = rendered[:previewLen] + "..."
		}

		preambles = append(preambles, DryRunPreamble{
			ModeID:   mode.ID,
			ModeCode: mode.Code,
			Preview:  preview,
			Length:   len(rendered),
		})
	}

	return preambles
}
