package ensemble

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
)

// EstimateOptions controls estimation behavior.
type EstimateOptions struct {
	// ContextPack overrides context generation when set.
	ContextPack *ContextPack
	// DisableContext skips context pack generation when true.
	DisableContext bool
}

// EstimateInput provides configuration for estimate calculations.
type EstimateInput struct {
	ModeIDs       []string
	Question      string
	ProjectDir    string
	Budget        BudgetConfig
	Cache         CacheConfig
	AllowAdvanced bool
}

// Estimator computes context-aware token estimates.
type Estimator struct {
	Catalog *ModeCatalog
	Logger  *slog.Logger
}

// NewEstimator creates an estimator with the provided catalog and logger.
func NewEstimator(catalog *ModeCatalog, logger *slog.Logger) *Estimator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Estimator{
		Catalog: catalog,
		Logger:  logger,
	}
}

// EnsembleEstimate summarizes token estimates for an ensemble run.
type EnsembleEstimate struct {
	GeneratedAt          time.Time      `json:"generated_at"`
	Question             string         `json:"question,omitempty"`
	PresetUsed           string         `json:"preset_used,omitempty"`
	ModeCount            int            `json:"mode_count"`
	Budget               BudgetConfig   `json:"budget"`
	EstimatedTotalTokens int            `json:"estimated_total_tokens"`
	OverBudget           bool           `json:"over_budget"`
	OverBy               int            `json:"over_by,omitempty"`
	Warnings             []string       `json:"warnings,omitempty"`
	Modes                []ModeEstimate `json:"modes"`
}

// ModeEstimate captures token estimates for a single mode.
type ModeEstimate struct {
	ID                  string            `json:"id"`
	Code                string            `json:"code,omitempty"`
	Name                string            `json:"name,omitempty"`
	Category            string            `json:"category,omitempty"`
	Tier                string            `json:"tier,omitempty"`
	PromptTokens        int               `json:"prompt_tokens"`
	BasePromptTokens    int               `json:"base_prompt_tokens,omitempty"`
	ContextTokens       int               `json:"context_tokens,omitempty"`
	OutputTokens        int               `json:"output_tokens"`
	TypicalOutputTokens int               `json:"typical_output_tokens"`
	TotalTokens         int               `json:"total_tokens"`
	ValueScore          float64           `json:"value_score"`
	ValuePerToken       float64           `json:"value_per_token"`
	Alternatives        []ModeAlternative `json:"alternatives,omitempty"`
}

// ModeAlternative suggests a lower-cost alternative to a mode.
type ModeAlternative struct {
	ID              string  `json:"id"`
	Code            string  `json:"code,omitempty"`
	Name            string  `json:"name,omitempty"`
	EstimatedTokens int     `json:"estimated_tokens"`
	Savings         int     `json:"savings"`
	ValueScore      float64 `json:"value_score"`
	ValuePerToken   float64 `json:"value_per_token"`
	Reason          string  `json:"reason,omitempty"`
}

// Estimate computes token usage and budget fit for the provided input.
func (e *Estimator) Estimate(_ context.Context, input EstimateInput, opts EstimateOptions) (*EnsembleEstimate, error) {
	if e == nil || e.Catalog == nil {
		return nil, fmt.Errorf("mode catalog is nil")
	}

	if len(input.ModeIDs) == 0 {
		return nil, fmt.Errorf("no modes to estimate")
	}

	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}

	question := strings.TrimSpace(input.Question)
	projectDir := strings.TrimSpace(input.ProjectDir)
	if projectDir == "" {
		projectDir = "."
	}

	budget := mergeBudgetDefaults(input.Budget, DefaultBudgetConfig())
	cacheCfg := input.Cache
	if cacheCfg == (CacheConfig{}) {
		cacheCfg = DefaultCacheConfig()
		cacheCfg.Enabled = false
	}

	var pack *ContextPack
	if opts.ContextPack != nil {
		pack = opts.ContextPack
	} else if !opts.DisableContext {
		var cache *ContextPackCache
		if cacheCfg.Enabled {
			created, cacheErr := NewContextPackCache(cacheCfg, logger)
			if cacheErr != nil {
				logger.Warn("context pack cache init failed", "error", cacheErr)
				cacheCfg.Enabled = false
			} else {
				cache = created
			}
		}
		generator := NewContextPackGenerator(projectDir, cache, logger)
		generator.MaxTokens = budget.ContextReserveTokens
		if generated, genErr := generator.Generate(question, "", cacheCfg); genErr == nil {
			pack = generated
		} else {
			logger.Warn("context pack generation failed", "error", genErr)
		}
	}
	if pack != nil && budget.ContextReserveTokens > 0 {
		pack = CapContextPackTokens(pack, budget.ContextReserveTokens)
	}

	engine := NewPreambleEngine()
	estimateCache := make(map[string]ModeEstimate, len(input.ModeIDs))

	estimateMode := func(mode *ReasoningMode) (ModeEstimate, error) {
		if cached, ok := estimateCache[mode.ID]; ok {
			return cached, nil
		}

		preamble, err := engine.Render(&PreambleData{
			Problem:     question,
			ContextPack: pack,
			Mode:        mode,
			TokenCap:    budget.MaxTokensPerMode,
		})
		if err != nil {
			return ModeEstimate{}, fmt.Errorf("render preamble for %s: %w", mode.ID, err)
		}

		promptTokens := tokenpkg.EstimateTokensWithLanguageHint(preamble, tokenpkg.ContentMarkdown)
		contextTokens := 0
		if pack != nil {
			contextTokens = pack.TokenEstimate
		}
		basePromptTokens := promptTokens
		if contextTokens > 0 && promptTokens > contextTokens {
			basePromptTokens = promptTokens - contextTokens
		}

		typicalOutput := estimateTypicalCost(mode)
		outputTokens := typicalOutput
		if budget.MaxTokensPerMode > 0 && outputTokens > budget.MaxTokensPerMode {
			outputTokens = budget.MaxTokensPerMode
		}

		totalTokens := promptTokens + outputTokens
		valueScore := modeValueScore(mode)
		valuePerToken := 0.0
		if totalTokens > 0 {
			valuePerToken = valueScore / float64(totalTokens)
		}

		estimate := ModeEstimate{
			ID:                  mode.ID,
			Code:                mode.Code,
			Name:                mode.Name,
			Category:            mode.Category.String(),
			Tier:                mode.Tier.String(),
			PromptTokens:        promptTokens,
			BasePromptTokens:    basePromptTokens,
			ContextTokens:       contextTokens,
			OutputTokens:        outputTokens,
			TypicalOutputTokens: typicalOutput,
			TotalTokens:         totalTokens,
			ValueScore:          valueScore,
			ValuePerToken:       valuePerToken,
		}

		estimateCache[mode.ID] = estimate

		logger.Info("ensemble estimate mode",
			"mode_id", mode.ID,
			"prompt_tokens", promptTokens,
			"output_tokens", outputTokens,
			"typical_output_tokens", typicalOutput,
			"calibration_delta", outputTokens-typicalOutput,
			"total_tokens", totalTokens,
		)

		return estimate, nil
	}

	result := &EnsembleEstimate{
		GeneratedAt: time.Now().UTC(),
		Question:    question,
		ModeCount:   len(input.ModeIDs),
		Budget:      budget,
		Modes:       make([]ModeEstimate, 0, len(input.ModeIDs)),
	}

	for _, modeID := range input.ModeIDs {
		mode := e.Catalog.GetMode(modeID)
		if mode == nil {
			return nil, fmt.Errorf("mode %q not found in catalog", modeID)
		}
		estimate, err := estimateMode(mode)
		if err != nil {
			return nil, err
		}
		result.Modes = append(result.Modes, estimate)
		result.EstimatedTotalTokens += estimate.TotalTokens
	}

	reserveTokens := budget.SynthesisReserveTokens + budget.ContextReserveTokens
	if reserveTokens > 0 {
		result.EstimatedTotalTokens += reserveTokens
	}

	if budget.MaxTotalTokens > 0 && result.EstimatedTotalTokens > budget.MaxTotalTokens {
		result.OverBudget = true
		result.OverBy = result.EstimatedTotalTokens - budget.MaxTotalTokens
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("estimated tokens (%d) exceed budget (%d) by %d",
				result.EstimatedTotalTokens, budget.MaxTotalTokens, result.OverBy),
		)
	}

	for _, est := range result.Modes {
		if budget.MaxTokensPerMode > 0 && est.TypicalOutputTokens > budget.MaxTokensPerMode {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("mode %s typical output (%d) exceeds per-mode cap (%d)",
					est.ID, est.TypicalOutputTokens, budget.MaxTokensPerMode),
			)
		}
	}

	allowAdvanced := input.AllowAdvanced
	if !allowAdvanced {
		for _, est := range result.Modes {
			if est.Tier != string(TierCore) {
				allowAdvanced = true
				break
			}
		}
	}

	if result.OverBudget {
		for i := range result.Modes {
			mode := e.Catalog.GetMode(result.Modes[i].ID)
			if mode == nil {
				continue
			}
			result.Modes[i].Alternatives = suggestAlternatives(mode, result.Modes[i], e.Catalog, allowAdvanced, estimateMode)
		}
	}

	logger.Info("ensemble estimate summary",
		"modes", len(result.Modes),
		"estimated_total_tokens", result.EstimatedTotalTokens,
		"budget_total", budget.MaxTotalTokens,
		"over_budget", result.OverBudget,
	)

	return result, nil
}

func mergeBudgetDefaults(current, defaults BudgetConfig) BudgetConfig {
	if current.MaxTokensPerMode == 0 {
		current.MaxTokensPerMode = defaults.MaxTokensPerMode
	}
	if current.MaxTotalTokens == 0 {
		current.MaxTotalTokens = defaults.MaxTotalTokens
	}
	if current.SynthesisReserveTokens == 0 {
		current.SynthesisReserveTokens = defaults.SynthesisReserveTokens
	}
	if current.ContextReserveTokens == 0 {
		current.ContextReserveTokens = defaults.ContextReserveTokens
	}
	if current.TimeoutPerMode == 0 {
		current.TimeoutPerMode = defaults.TimeoutPerMode
	}
	if current.TotalTimeout == 0 {
		current.TotalTimeout = defaults.TotalTimeout
	}
	if current.MaxRetries == 0 {
		current.MaxRetries = defaults.MaxRetries
	}
	return current
}

func modeValueScore(mode *ReasoningMode) float64 {
	if mode == nil {
		return 0.0
	}

	score := 1.0
	switch mode.Tier {
	case TierCore:
		score *= 1.0
	case TierAdvanced:
		score *= 0.85
	case TierExperimental:
		score *= 0.7
	default:
		score *= 0.9
	}

	if len(mode.BestFor) > 0 {
		score += math.Min(0.3, 0.03*float64(len(mode.BestFor)))
	}
	if strings.TrimSpace(mode.Differentiator) != "" {
		score += 0.05
	}

	return score
}

func suggestAlternatives(
	mode *ReasoningMode,
	current ModeEstimate,
	catalog *ModeCatalog,
	allowAdvanced bool,
	estimateMode func(*ReasoningMode) (ModeEstimate, error),
) []ModeAlternative {
	if mode == nil || catalog == nil || estimateMode == nil {
		return nil
	}

	candidates := catalog.ListByCategory(mode.Category)
	if len(candidates) == 0 {
		return nil
	}

	minSavings := int(math.Max(200, float64(current.TotalTokens)*0.1))
	alternatives := make([]ModeAlternative, 0, 3)

	for i := range candidates {
		candidate := candidates[i]
		if candidate.ID == mode.ID {
			continue
		}
		if !allowAdvanced && candidate.Tier != TierCore {
			continue
		}

		estimate, err := estimateMode(&candidate)
		if err != nil {
			continue
		}
		if estimate.TotalTokens >= current.TotalTokens {
			continue
		}

		savings := current.TotalTokens - estimate.TotalTokens
		if savings < minSavings {
			continue
		}

		alternatives = append(alternatives, ModeAlternative{
			ID:              candidate.ID,
			Code:            candidate.Code,
			Name:            candidate.Name,
			EstimatedTokens: estimate.TotalTokens,
			Savings:         savings,
			ValueScore:      estimate.ValueScore,
			ValuePerToken:   estimate.ValuePerToken,
			Reason:          fmt.Sprintf("lower-cost %s-tier mode in %s category", candidate.Tier, candidate.Category),
		})
	}

	sort.Slice(alternatives, func(i, j int) bool {
		if alternatives[i].ValuePerToken == alternatives[j].ValuePerToken {
			return alternatives[i].Savings > alternatives[j].Savings
		}
		return alternatives[i].ValuePerToken > alternatives[j].ValuePerToken
	})

	if len(alternatives) > 3 {
		alternatives = alternatives[:3]
	}

	return alternatives
}
