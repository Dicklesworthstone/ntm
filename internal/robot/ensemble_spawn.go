//go:build ensemble_experimental
// +build ensemble_experimental

// Package robot provides machine-readable output for AI agents.
// ensemble_spawn.go implements --robot-ensemble-spawn for creating ensembles.
package robot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// EnsembleSpawnOptions configures --robot-ensemble-spawn.
type EnsembleSpawnOptions struct {
	Session       string
	Preset        string
	Modes         string
	Question      string
	Agents        string
	Assignment    string
	AllowAdvanced bool
	BudgetTotal   int
	BudgetPerMode int
	NoCache       bool
	NoQuestions   bool
	ProjectDir    string
}

// EnsembleSpawnOutput is the structured output for --robot-ensemble-spawn.
type EnsembleSpawnOutput struct {
	RobotResponse
	Action     string              `json:"action"`
	Session    string              `json:"session"`
	Preset     string              `json:"preset,omitempty"`
	Question   string              `json:"question,omitempty"`
	ProjectDir string              `json:"project_dir,omitempty"`
	Assignment string              `json:"assignment,omitempty"`
	Agents     map[string]int      `json:"agents,omitempty"`
	Modes      []EnsembleSpawnMode `json:"modes"`
	Budget     EnsembleSpawnBudget `json:"budget"`
	Status     string              `json:"status,omitempty"`
	Warnings   []string            `json:"warnings,omitempty"`
}

// EnsembleSpawnMode represents a spawned mode assignment.
type EnsembleSpawnMode struct {
	ID        string `json:"id"`
	Code      string `json:"code,omitempty"`
	Tier      string `json:"tier,omitempty"`
	Pane      string `json:"pane,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
}

// EnsembleSpawnBudget reports budget inputs for the spawn.
type EnsembleSpawnBudget struct {
	TotalTokens   int `json:"total_tokens,omitempty"`
	PerModeTokens int `json:"per_mode_tokens,omitempty"`
}

// GetEnsembleSpawn spawns a reasoning ensemble session and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetEnsembleSpawn(opts EnsembleSpawnOptions, cfg *config.Config) (*EnsembleSpawnOutput, error) {
	opts = applyEnsembleSpawnDefaults(opts, cfg)

	output := &EnsembleSpawnOutput{
		RobotResponse: NewRobotResponse(true),
		Action:        "ensemble_spawn",
		Session:       strings.TrimSpace(opts.Session),
		Preset:        strings.TrimSpace(opts.Preset),
		Question:      strings.TrimSpace(opts.Question),
		Assignment:    normalizeEnsembleAssignment(opts.Assignment),
		Modes:         []EnsembleSpawnMode{},
		Budget:        EnsembleSpawnBudget{},
	}

	if output.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-ensemble-spawn=myproject",
		)
		return output, nil
	}
	if err := tmux.ValidateSessionName(output.Session); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Use a valid tmux session name")
		return output, nil
	}
	if err := tmux.EnsureInstalled(); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeDependencyMissing, "Install tmux to spawn ensembles")
		return output, nil
	}
	if tmux.SessionExists(output.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' already exists", output.Session),
			ErrCodeInvalidFlag,
			"Choose a new session name or terminate the existing session",
		)
		return output, nil
	}
	if output.Question == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("question is required"),
			ErrCodeInvalidFlag,
			"Provide a question: --question='What should we analyze?'",
		)
		return output, nil
	}

	rawModes := splitModeList(opts.Modes)
	if output.Preset == "" && len(rawModes) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("preset or modes required"),
			ErrCodeInvalidFlag,
			"Provide --preset or --modes",
		)
		return output, nil
	}
	if output.Preset != "" && len(rawModes) > 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("preset and modes are mutually exclusive"),
			ErrCodeInvalidFlag,
			"Use either --preset or --modes, not both",
		)
		return output, nil
	}

	if !isValidEnsembleAssignment(output.Assignment) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid assignment strategy %q", output.Assignment),
			ErrCodeInvalidFlag,
			"Use assignment: round-robin, affinity, category, or explicit",
		)
		return output, nil
	}
	if output.Assignment == "explicit" && len(rawModes) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("explicit assignment requires mode:agent specs"),
			ErrCodeInvalidFlag,
			"Provide --modes entries like deductive:cc,abductive:cod",
		)
		return output, nil
	}
	if opts.BudgetPerMode < 0 || opts.BudgetTotal < 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("budget overrides must be non-negative"),
			ErrCodeInvalidFlag,
			"Use non-negative values for --budget-total and --budget-per-agent",
		)
		return output, nil
	}

	projectDir, err := resolveEnsembleSpawnProjectDir(opts.ProjectDir)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Provide a valid project directory")
		return output, nil
	}
	output.ProjectDir = projectDir

	agentMix, err := parseEnsembleAgentMix(opts.Agents)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Provide agents as cc=2,cod=1,gmi=1")
		return output, nil
	}
	output.Agents = agentMix

	manager, catalog, registry, err := buildEnsembleSpawnManager(projectDir)
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Failed to load ensemble catalog")
		return output, nil
	}

	if output.Preset != "" {
		preset := registry.Get(output.Preset)
		if preset == nil {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("preset %q not found", output.Preset),
				ErrCodeInvalidFlag,
				"Use a valid ensemble preset name",
			)
			return output, nil
		}
		effective := *preset
		if opts.AllowAdvanced {
			effective.AllowAdvanced = true
		}
		if err := effective.Validate(catalog); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Preset validation failed")
			return output, nil
		}
	}

	if output.Preset == "" && output.Assignment != "explicit" {
		modeIDs, err := validateEnsembleModeRefs(rawModes, catalog, opts.AllowAdvanced)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Invalid mode references")
			return output, nil
		}
		if len(modeIDs) < 2 || len(modeIDs) > 10 {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("mode count must be between 2 and 10 (got %d)", len(modeIDs)),
				ErrCodeInvalidFlag,
				"Use between 2 and 10 modes",
			)
			return output, nil
		}
	}
	if output.Preset == "" && output.Assignment == "explicit" {
		normalized, err := normalizeExplicitModeSpecs(rawModes, catalog, opts.AllowAdvanced)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Invalid explicit mode specs")
			return output, nil
		}
		if len(normalized) < 2 || len(normalized) > 10 {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("mode count must be between 2 and 10 (got %d)", len(normalized)),
				ErrCodeInvalidFlag,
				"Use between 2 and 10 modes",
			)
			return output, nil
		}
		rawModes = normalized
	}

	ensembleCfg := &ensemble.EnsembleConfig{
		SessionName:   output.Session,
		Question:      output.Question,
		Ensemble:      output.Preset,
		Modes:         rawModes,
		AllowAdvanced: opts.AllowAdvanced,
		ProjectDir:    projectDir,
		AgentMix:      agentMix,
		Assignment:    output.Assignment,
	}

	if cfg == nil {
		cfg = config.Default()
	}
	applyEnsembleConfigOverrides(ensembleCfg, cfg.Ensemble)

	if opts.NoCache {
		ensembleCfg.Cache = ensemble.CacheConfig{Enabled: false}
		ensembleCfg.CacheOverride = true
	}
	if opts.NoQuestions {
		output.Warnings = append(output.Warnings, "no-questions is not implemented yet; flag ignored")
	}
	if opts.BudgetPerMode > 0 {
		ensembleCfg.Budget.MaxTokensPerMode = opts.BudgetPerMode
	}
	if opts.BudgetTotal > 0 {
		ensembleCfg.Budget.MaxTotalTokens = opts.BudgetTotal
	}

	output.Budget = resolveEnsembleSpawnBudget(ensembleCfg, registry)

	state, spawnErr := manager.SpawnEnsemble(context.Background(), ensembleCfg)
	if state != nil {
		output.Status = state.Status.String()
		output.Modes = buildSpawnModes(state.Assignments, catalog)
	}

	if spawnErr != nil {
		output.RobotResponse = NewErrorResponse(spawnErr, ErrCodeInternalError, "Ensemble spawn encountered errors")
	}

	return output, nil
}

// PrintEnsembleSpawn spawns a reasoning ensemble session and returns structured JSON.
func PrintEnsembleSpawn(opts EnsembleSpawnOptions, cfg *config.Config) error {
	output, err := GetEnsembleSpawn(opts, cfg)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

func applyEnsembleSpawnDefaults(opts EnsembleSpawnOptions, cfg *config.Config) EnsembleSpawnOptions {
	if cfg == nil {
		cfg = config.Default()
	}
	ensCfg := cfg.Ensemble

	if strings.TrimSpace(opts.Preset) == "" && strings.TrimSpace(opts.Modes) == "" && strings.TrimSpace(ensCfg.DefaultEnsemble) != "" {
		opts.Preset = ensCfg.DefaultEnsemble
	}
	if strings.TrimSpace(opts.Agents) == "" && strings.TrimSpace(ensCfg.AgentMix) != "" {
		opts.Agents = ensCfg.AgentMix
	}
	if strings.TrimSpace(opts.Assignment) == "" && strings.TrimSpace(ensCfg.Assignment) != "" {
		opts.Assignment = ensCfg.Assignment
	}
	if !opts.AllowAdvanced {
		allow := ensCfg.AllowAdvanced
		switch strings.ToLower(strings.TrimSpace(ensCfg.ModeTierDefault)) {
		case "advanced", "experimental":
			allow = true
		}
		opts.AllowAdvanced = allow
	}
	if opts.BudgetTotal == 0 && ensCfg.Budget.Total > 0 {
		opts.BudgetTotal = ensCfg.Budget.Total
	}
	if opts.BudgetPerMode == 0 && ensCfg.Budget.PerAgent > 0 {
		opts.BudgetPerMode = ensCfg.Budget.PerAgent
	}
	if !opts.NoCache && !ensCfg.Cache.Enabled {
		opts.NoCache = true
	}

	return opts
}

func applyEnsembleConfigOverrides(target *ensemble.EnsembleConfig, ensCfg config.EnsembleConfig) {
	if target == nil {
		return
	}

	if target.Synthesis.Strategy == "" && strings.TrimSpace(ensCfg.Synthesis.Strategy) != "" {
		target.Synthesis.Strategy = ensemble.SynthesisStrategy(strings.TrimSpace(ensCfg.Synthesis.Strategy))
	}
	if ensCfg.Synthesis.MinConfidence > 0 {
		target.Synthesis.MinConfidence = ensemble.Confidence(ensCfg.Synthesis.MinConfidence)
	}
	if ensCfg.Synthesis.MaxFindings > 0 {
		target.Synthesis.MaxFindings = ensCfg.Synthesis.MaxFindings
	}
	if ensCfg.Synthesis.IncludeRawOutputs {
		target.Synthesis.IncludeRawOutputs = true
	}
	if strings.TrimSpace(ensCfg.Synthesis.ConflictResolution) != "" {
		target.Synthesis.ConflictResolution = strings.TrimSpace(ensCfg.Synthesis.ConflictResolution)
	}

	if ensCfg.Budget.Synthesis > 0 {
		target.Budget.SynthesisReserveTokens = ensCfg.Budget.Synthesis
	}
	if ensCfg.Budget.ContextPack > 0 {
		target.Budget.ContextReserveTokens = ensCfg.Budget.ContextPack
	}

	target.Cache.Enabled = ensCfg.Cache.Enabled
	if ensCfg.Cache.TTLMinutes > 0 {
		target.Cache.TTL = time.Duration(ensCfg.Cache.TTLMinutes) * time.Minute
	}
	if strings.TrimSpace(ensCfg.Cache.CacheDir) != "" {
		target.Cache.CacheDir = config.ExpandHome(strings.TrimSpace(ensCfg.Cache.CacheDir))
	}
	if ensCfg.Cache.MaxEntries > 0 {
		target.Cache.MaxEntries = ensCfg.Cache.MaxEntries
	}
	target.Cache.ShareAcrossModes = ensCfg.Cache.ShareAcrossModes
	if ensCfg.Cache.CacheDir != "" || ensCfg.Cache.TTLMinutes > 0 || ensCfg.Cache.MaxEntries > 0 || !ensCfg.Cache.Enabled {
		target.CacheOverride = true
	}

	target.EarlyStop = ensemble.EarlyStopConfig{
		Enabled:             ensCfg.EarlyStop.Enabled,
		MinAgentsBeforeStop: ensCfg.EarlyStop.MinAgents,
		FindingsThreshold:   ensCfg.EarlyStop.FindingsThreshold,
		SimilarityThreshold: ensCfg.EarlyStop.SimilarityThreshold,
		WindowSize:          ensCfg.EarlyStop.WindowSize,
	}
}

func resolveEnsembleSpawnBudget(cfg *ensemble.EnsembleConfig, registry *ensemble.EnsembleRegistry) EnsembleSpawnBudget {
	budget := ensemble.DefaultBudgetConfig()
	if registry != nil && cfg != nil && cfg.Ensemble != "" {
		if preset := registry.Get(cfg.Ensemble); preset != nil {
			budget = mergeBudgetDefaults(preset.Budget, budget)
		}
	}
	if cfg != nil {
		if cfg.Budget.MaxTokensPerMode > 0 {
			budget.MaxTokensPerMode = cfg.Budget.MaxTokensPerMode
		}
		if cfg.Budget.MaxTotalTokens > 0 {
			budget.MaxTotalTokens = cfg.Budget.MaxTotalTokens
		}
	}
	return EnsembleSpawnBudget{
		TotalTokens:   budget.MaxTotalTokens,
		PerModeTokens: budget.MaxTokensPerMode,
	}
}

func mergeBudgetDefaults(current, defaults ensemble.BudgetConfig) ensemble.BudgetConfig {
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

func buildSpawnModes(assignments []ensemble.ModeAssignment, catalog *ensemble.ModeCatalog) []EnsembleSpawnMode {
	modes := make([]EnsembleSpawnMode, 0, len(assignments))
	for _, assignment := range assignments {
		modeOut := EnsembleSpawnMode{
			ID:        assignment.ModeID,
			Pane:      assignment.PaneName,
			AgentType: normalizeEnsembleAgentType(assignment.AgentType),
		}
		if catalog != nil {
			if mode := catalog.GetMode(assignment.ModeID); mode != nil {
				modeOut.Code = mode.Code
				modeOut.Tier = mode.Tier.String()
			}
		}
		modes = append(modes, modeOut)
	}
	return modes
}

func buildEnsembleSpawnManager(projectDir string) (*ensemble.EnsembleManager, *ensemble.ModeCatalog, *ensemble.EnsembleRegistry, error) {
	modeLoader := ensemble.NewModeLoader()
	if projectDir != "" {
		modeLoader.ProjectDir = projectDir
	}
	catalog, err := modeLoader.Load()
	if err != nil {
		return nil, nil, nil, err
	}

	ensembleLoader := ensemble.NewEnsembleLoader(catalog)
	if projectDir != "" {
		ensembleLoader.ProjectDir = projectDir
	}
	presets, err := ensembleLoader.Load()
	if err != nil {
		return nil, nil, nil, err
	}

	manager := ensemble.NewEnsembleManager()
	manager.Catalog = catalog
	manager.Registry = ensemble.NewEnsembleRegistry(presets, catalog)
	return manager, catalog, manager.Registry, nil
}

func resolveEnsembleSpawnProjectDir(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		return dir, nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	return abs, nil
}

var modeCodePattern = regexp.MustCompile(`^[A-L][0-9]+$`)

func splitModeList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func validateEnsembleModeRefs(values []string, catalog *ensemble.ModeCatalog, allowAdvanced bool) ([]string, error) {
	refs := make([]ensemble.ModeRef, 0, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("modes[%d]: empty mode reference", i)
		}
		if strings.Contains(value, ":") {
			return nil, fmt.Errorf("modes[%d]: explicit assignment requires mode:agent only when assignment=explicit", i)
		}
		if isModeCode(value) {
			refs = append(refs, ensemble.ModeRefFromCode(strings.ToUpper(value)))
		} else {
			refs = append(refs, ensemble.ModeRefFromID(strings.ToLower(value)))
		}
	}
	ids, err := ensemble.ResolveModeRefs(refs, catalog)
	if err != nil {
		return nil, err
	}
	if allowAdvanced || catalog == nil {
		return ids, nil
	}
	for _, id := range ids {
		mode := catalog.GetMode(id)
		if mode != nil && mode.Tier != ensemble.TierCore {
			return nil, fmt.Errorf("mode %q is tier %q but allow_advanced is false", id, mode.Tier)
		}
	}
	return ids, nil
}

func normalizeExplicitModeSpecs(specs []string, catalog *ensemble.ModeCatalog, allowAdvanced bool) ([]string, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("explicit assignment requires at least one mapping")
	}
	modeRefs := make([]string, 0, len(specs))
	agentTypes := make([]string, 0, len(specs))
	for i, spec := range specs {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("modes[%d]: expected mode:agent", i)
		}
		modeRef := strings.TrimSpace(parts[0])
		if modeRef == "" {
			return nil, fmt.Errorf("modes[%d]: empty mode reference", i)
		}
		agentType := normalizeEnsembleAgentType(parts[1])
		if agentType == "" {
			return nil, fmt.Errorf("modes[%d]: invalid agent type %q", i, strings.TrimSpace(parts[1]))
		}
		modeRefs = append(modeRefs, modeRef)
		agentTypes = append(agentTypes, agentType)
	}
	ids, err := validateEnsembleModeRefs(modeRefs, catalog, allowAdvanced)
	if err != nil {
		return nil, err
	}
	normalized := make([]string, 0, len(ids))
	for i, id := range ids {
		normalized = append(normalized, fmt.Sprintf("%s:%s", id, agentTypes[i]))
	}
	return normalized, nil
}

func isModeCode(value string) bool {
	return modeCodePattern.MatchString(strings.ToUpper(strings.TrimSpace(value)))
}

func normalizeEnsembleAssignment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "affinity"
	}
	return value
}

func isValidEnsembleAssignment(value string) bool {
	switch value {
	case "round-robin", "affinity", "category", "explicit":
		return true
	default:
		return false
	}
}

func parseEnsembleAgentMix(value string) (map[string]int, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	mix := make(map[string]int)
	parts := strings.Split(value, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("agents entry %q must be type=count", part)
		}
		agentType := normalizeEnsembleAgentType(kv[0])
		if agentType == "" {
			return nil, fmt.Errorf("agents entry %q has invalid agent type", part)
		}
		count, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if err != nil {
			return nil, fmt.Errorf("agents entry %q has invalid count: %v", part, err)
		}
		if count < 1 {
			return nil, fmt.Errorf("agents entry %q must be >= 1", part)
		}
		mix[agentType] += count
	}
	if len(mix) == 0 {
		return nil, nil
	}
	return mix, nil
}

func normalizeEnsembleAgentType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cc", "claude", "claude-code", "claude_code":
		return "cc"
	case "cod", "codex", "codex-cli", "codex_cli":
		return "cod"
	case "gmi", "gemini", "gemini-cli", "gemini_cli":
		return "gmi"
	case "cursor":
		return "cursor"
	case "windsurf", "ws":
		return "windsurf"
	case "aider":
		return "aider"
	case "ollama":
		return "ollama"
	default:
		return ""
	}
}
