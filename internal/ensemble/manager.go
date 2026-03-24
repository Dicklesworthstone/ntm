//go:build ensemble_experimental
// +build ensemble_experimental

package ensemble

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	assignmentRoundRobin = "round-robin"
	assignmentAffinity   = "affinity"
	assignmentCategory   = "category"
	assignmentExplicit   = "explicit"
)

// EnsembleConfig defines the user-facing configuration for spawning an ensemble.
type EnsembleConfig struct {
	SessionName string
	Question    string
	Ensemble    string   // built-in or user-defined ensemble name
	Modes       []string // explicit mode IDs/codes or explicit specs (mode:agent)

	// AllowAdvanced permits advanced/experimental modes (default: core only).
	AllowAdvanced bool

	// ProjectDir sets the working directory for spawned panes.
	ProjectDir string

	AgentMix   map[string]int
	Assignment string // round-robin, affinity, explicit

	// SkipInject prevents prompt injection (creates session and assignments only).
	SkipInject bool

	Synthesis SynthesisConfig
	Budget    BudgetConfig
	Cache     CacheConfig
	// CacheOverride forces Cache config to apply even when disabling.
	CacheOverride bool
	EarlyStop     EarlyStopConfig
}

// EnsembleManager orchestrates ensemble session lifecycle steps.
type EnsembleManager struct {
	TmuxClient          *tmux.Client
	SessionOrchestrator *swarm.SessionOrchestrator
	PaneLauncher        *swarm.PaneLauncher
	PromptInjector      *swarm.PromptInjector
	Logger              *slog.Logger

	Catalog  *ModeCatalog
	Registry *EnsembleRegistry
}

// NewEnsembleManager creates a manager with default dependencies.
func NewEnsembleManager() *EnsembleManager {
	return &EnsembleManager{
		TmuxClient:          nil,
		SessionOrchestrator: swarm.NewSessionOrchestrator(),
		PaneLauncher:        swarm.NewPaneLauncher(),
		PromptInjector:      swarm.NewPromptInjector(),
		Logger:              slog.Default(),
	}
}

// SpawnEnsemble orchestrates the ensemble lifecycle: spawn -> assign -> inject -> persist.
func (m *EnsembleManager) SpawnEnsemble(ctx context.Context, cfg *EnsembleConfig) (*EnsembleSession, error) {
	if cfg == nil {
		return nil, errors.New("ensemble config is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	logger := m.logger()

	if cfg.SessionName == "" {
		return nil, errors.New("session name is required")
	}
	if err := tmux.ValidateSessionName(cfg.SessionName); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
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
		return nil, err
	}
	registry, err := m.registry(catalog)
	if err != nil {
		return nil, err
	}

	modeIDs, resolvedCfg, explicitSpecs, err := resolveEnsembleConfig(cfg, catalog, registry)
	if err != nil {
		return nil, err
	}

	state := &EnsembleSession{
		SessionName:       cfg.SessionName,
		Question:          cfg.Question,
		PresetUsed:        resolvedCfg.presetName,
		Assignments:       nil,
		Status:            EnsembleSpawning,
		SynthesisStrategy: resolvedCfg.synthesis.Strategy,
		CreatedAt:         time.Now().UTC(),
	}

	if saveErr := SaveSession(cfg.SessionName, state); saveErr != nil {
		logger.Warn("ensemble state save failed", "session", cfg.SessionName, "error", saveErr)
	}

	paneSpecs, err := buildPaneSpecs(cfg, len(modeIDs))
	if err != nil {
		state.Status = EnsembleError
		state.Error = err.Error()
		_ = SaveSession(cfg.SessionName, state)
		return state, err
	}

	sessionSpec := swarm.SessionSpec{
		Name:      cfg.SessionName,
		AgentType: "ensemble",
		PaneCount: len(paneSpecs),
		Panes:     paneSpecs,
	}

	orchestrator := m.sessionOrchestrator()
	orchestrator.TmuxClient = m.tmuxClient()

	plan := &swarm.SwarmPlan{Sessions: []swarm.SessionSpec{sessionSpec}}
	createResult, createErr := orchestrator.CreateSessions(plan)
	if createErr != nil || createResult == nil {
		state.Status = EnsembleError
		state.Error = fmt.Sprintf("session creation failed: %v", createErr)
		_ = SaveSession(cfg.SessionName, state)
		return state, fmt.Errorf("create ensemble session: %w", createErr)
	}
	if len(createResult.Sessions) == 0 || createResult.Sessions[0].Error != nil {
		err := errors.New("failed to create ensemble session")
		if len(createResult.Sessions) > 0 && createResult.Sessions[0].Error != nil {
			err = createResult.Sessions[0].Error
		}
		state.Status = EnsembleError
		state.Error = err.Error()
		_ = SaveSession(cfg.SessionName, state)
		return state, err
	}

	launcher := m.paneLauncher()
	launcher.TmuxClient = m.tmuxClient()
	if _, err := launcher.LaunchSession(ctx, sessionSpec, 300*time.Millisecond); err != nil {
		logger.Warn("ensemble agent launch errors", "session", cfg.SessionName, "error", err)
	}

	panes, err := m.tmuxClient().GetPanes(cfg.SessionName)
	if err != nil {
		state.Status = EnsembleError
		state.Error = fmt.Sprintf("get panes: %v", err)
		_ = SaveSession(cfg.SessionName, state)
		return state, err
	}

	assignments, err := assignModes(cfg.Assignment, modeIDs, explicitSpecs, panes, catalog)
	if err != nil {
		state.Status = EnsembleError
		state.Error = err.Error()
		_ = SaveSession(cfg.SessionName, state)
		return state, err
	}

	state.Assignments = assignments
	state.Status = EnsembleInjecting
	if saveErr := SaveSession(cfg.SessionName, state); saveErr != nil {
		logger.Warn("ensemble state save failed", "session", cfg.SessionName, "error", saveErr)
	}

	if cfg.SkipInject {
		logger.Info("ensemble prompt injection skipped", "session", cfg.SessionName)
		return state, nil
	}

	injector := m.ensembleInjector()
	contextGenerator, cacheCfg := m.contextPackGenerator(cfg.ProjectDir, resolvedCfg.cache)
	var sharedContext *ContextPack
	if cacheCfg.ShareAcrossModes {
		if pack, err := contextGenerator.Generate(cfg.Question, "", cacheCfg); err == nil {
			sharedContext = pack
		} else {
			logger.Warn("context pack generation failed", "session", cfg.SessionName, "error", err)
		}
	}

	targets := buildPaneTargetMap(cfg.SessionName, panes)
	var injectErrors []error
	successes := 0
	skippedModes := []string{}

	order := orderAssignmentsForTimebox(state.Assignments, catalog)
	orderedModeIDs := make([]string, 0, len(order))
	for _, idx := range order {
		if idx >= 0 && idx < len(state.Assignments) {
			orderedModeIDs = append(orderedModeIDs, state.Assignments[idx].ModeID)
		}
	}

	deadline := timeboxDeadline(time.Now(), resolvedCfg.budget.TotalTimeout)
	if !deadline.IsZero() {
		logger.Info("ensemble timebox configured",
			"session", cfg.SessionName,
			"total_timeout", resolvedCfg.budget.TotalTimeout,
			"deadline", deadline,
			"ordered_modes", orderedModeIDs,
		)
	} else {
		logger.Debug("ensemble timebox disabled",
			"session", cfg.SessionName,
			"ordered_modes", orderedModeIDs,
		)
	}

	for orderIndex, assignmentIndex := range order {
		if timeboxExpired(deadline, time.Now()) {
			skippedModes = append(skippedModes, markAssignmentsSkipped(
				state.Assignments,
				order[orderIndex:],
				"skipped: total timeout reached before injection",
			)...)
			break
		}

		assignment := &state.Assignments[assignmentIndex]
		mode := catalog.GetMode(assignment.ModeID)
		if mode == nil {
			err := fmt.Errorf("mode not found: %s", assignment.ModeID)
			assignment.Status = AssignmentError
			assignment.Error = err.Error()
			injectErrors = append(injectErrors, err)
			continue
		}

		target := targets[assignment.PaneName]
		if target == "" {
			target = assignment.PaneName
		}

		assignment.Status = AssignmentInjecting
		contextPack := sharedContext
		if contextPack == nil && !cacheCfg.ShareAcrossModes {
			if pack, err := contextGenerator.Generate(cfg.Question, assignment.ModeID, cacheCfg); err == nil {
				contextPack = pack
			} else {
				logger.Warn("context pack generation failed", "session", cfg.SessionName, "mode", assignment.ModeID, "error", err)
			}
		}
		injResult, err := injector.InjectWithMode(
			target,
			mode,
			cfg.Question,
			assignment.AgentType,
			contextPack,
			resolvedCfg.budget.MaxTokensPerMode,
			"",
		)
		if err != nil || injResult == nil || !injResult.Success {
			assignment.Status = AssignmentError
			if err != nil {
				assignment.Error = err.Error()
				injectErrors = append(injectErrors, err)
			} else if injResult != nil {
				assignment.Error = injResult.Error
				injectErrors = append(injectErrors, fmt.Errorf("inject failed for %s: %s", assignment.PaneName, injResult.Error))
			} else {
				assignment.Error = "inject failed"
				injectErrors = append(injectErrors, fmt.Errorf("inject failed for %s", assignment.PaneName))
			}
			continue
		}

		assignment.Status = AssignmentActive
		successes++
	}

	if successes == 0 && len(injectErrors) > 0 {
		state.Status = EnsembleError
		state.Error = "all injections failed"
	} else if successes == 0 && len(skippedModes) > 0 {
		state.Status = EnsembleError
		state.Error = "all injections skipped due to timeout"
	} else {
		state.Status = EnsembleActive
	}

	if len(skippedModes) > 0 {
		logger.Info("ensemble timebox reached; skipping remaining modes",
			"session", cfg.SessionName,
			"skipped", skippedModes,
			"completed", successes,
			"total", len(state.Assignments),
			"timeout", resolvedCfg.budget.TotalTimeout,
		)
	}

	if saveErr := SaveSession(cfg.SessionName, state); saveErr != nil {
		logger.Warn("ensemble state save failed", "session", cfg.SessionName, "error", saveErr)
	}

	return state, errors.Join(injectErrors...)
}

func (m *EnsembleManager) tmuxClient() *tmux.Client {
	if m.TmuxClient != nil {
		return m.TmuxClient
	}
	return tmux.DefaultClient
}

func (m *EnsembleManager) sessionOrchestrator() *swarm.SessionOrchestrator {
	if m.SessionOrchestrator != nil {
		return m.SessionOrchestrator
	}
	return swarm.NewSessionOrchestrator()
}

func (m *EnsembleManager) paneLauncher() *swarm.PaneLauncher {
	if m.PaneLauncher != nil {
		return m.PaneLauncher
	}
	return swarm.NewPaneLauncher()
}

func (m *EnsembleManager) promptInjector() *swarm.PromptInjector {
	if m.PromptInjector != nil {
		return m.PromptInjector
	}
	return swarm.NewPromptInjector()
}

func (m *EnsembleManager) ensembleInjector() *EnsembleInjector {
	basicInjector := m.promptInjector()
	basicInjector.TmuxClient = m.tmuxClient()
	return NewEnsembleInjector().
		WithBasicInjector(basicInjector).
		WithLogger(m.logger())
}

func (m *EnsembleManager) contextPackGenerator(projectDir string, cacheCfg CacheConfig) (*ContextPackGenerator, CacheConfig) {
	cfg := cacheCfg
	var cache *ContextPackCache
	if cfg.Enabled {
		created, err := NewContextPackCache(cfg, m.logger())
		if err != nil {
			m.logger().Warn("context pack cache init failed", "error", err)
			cfg.Enabled = false
		} else {
			cache = created
		}
	}

	return NewContextPackGenerator(projectDir, cache, m.logger()), cfg
}

func (m *EnsembleManager) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m *EnsembleManager) catalog() (*ModeCatalog, error) {
	if m.Catalog != nil {
		return m.Catalog, nil
	}
	return LoadModeCatalog()
}

func (m *EnsembleManager) registry(catalog *ModeCatalog) (*EnsembleRegistry, error) {
	if m.Registry != nil {
		return m.Registry, nil
	}
	loader := NewEnsembleLoader(catalog)
	ensembles, err := loader.Load()
	if err != nil {
		return nil, err
	}
	return NewEnsembleRegistry(ensembles, catalog), nil
}

type resolvedEnsembleConfig struct {
	presetName    string
	synthesis     SynthesisConfig
	budget        BudgetConfig
	cache         CacheConfig
	explicitSpecs []string
}

func resolveEnsembleConfig(cfg *EnsembleConfig, catalog *ModeCatalog, registry *EnsembleRegistry) ([]string, resolvedEnsembleConfig, []string, error) {
	resolved := resolvedEnsembleConfig{
		synthesis: DefaultSynthesisConfig(),
		budget:    DefaultBudgetConfig(),
		cache:     DefaultCacheConfig(),
	}

	if cfg.Ensemble != "" {
		preset := registry.Get(cfg.Ensemble)
		if preset == nil {
			return nil, resolved, nil, fmt.Errorf("ensemble %q not found", cfg.Ensemble)
		}
		effectivePreset := *preset
		if cfg.AllowAdvanced {
			effectivePreset.AllowAdvanced = true
		}
		if err := effectivePreset.Validate(catalog); err != nil {
			return nil, resolved, nil, err
		}
		modeIDs, err := effectivePreset.ResolveIDs(catalog)
		if err != nil {
			return nil, resolved, nil, err
		}
		resolved.presetName = effectivePreset.Name
		resolved.synthesis = effectivePreset.Synthesis
		resolved.budget = effectivePreset.Budget
		resolved.cache = effectivePreset.Cache
		applyConfigOverrides(cfg, &resolved)
		if err := validateResolvedConfig(&resolved, modeIDs, catalog, effectivePreset.AllowAdvanced); err != nil {
			return nil, resolved, nil, err
		}
		return modeIDs, resolved, nil, nil
	}

	if normalizeAssignment(cfg.Assignment) == assignmentExplicit {
		specs, err := normalizeExplicitSpecs(cfg.Modes, catalog)
		if err != nil {
			return nil, resolved, nil, err
		}
		resolved.explicitSpecs = specs
		applyConfigOverrides(cfg, &resolved)
		modeIDs := explicitModeIDs(specs)
		if err := validateResolvedConfig(&resolved, modeIDs, catalog, cfg.AllowAdvanced); err != nil {
			return nil, resolved, specs, err
		}
		return modeIDs, resolved, specs, nil
	}

	refs, err := parseModeRefs(cfg.Modes)
	if err != nil {
		return nil, resolved, nil, err
	}
	modeIDs, err := ResolveModeRefs(refs, catalog)
	if err != nil {
		return nil, resolved, nil, err
	}

	applyConfigOverrides(cfg, &resolved)
	if err := validateResolvedConfig(&resolved, modeIDs, catalog, cfg.AllowAdvanced); err != nil {
		return nil, resolved, nil, err
	}

	return modeIDs, resolved, nil, nil
}

func applyConfigOverrides(cfg *EnsembleConfig, resolved *resolvedEnsembleConfig) {
	if cfg.Synthesis.Strategy != "" {
		resolved.synthesis.Strategy = cfg.Synthesis.Strategy
	}
	if cfg.Budget.MaxTokensPerMode > 0 {
		resolved.budget.MaxTokensPerMode = cfg.Budget.MaxTokensPerMode
	}
	if cfg.Budget.MaxTotalTokens > 0 {
		resolved.budget.MaxTotalTokens = cfg.Budget.MaxTotalTokens
	}
	if cfg.Budget.SynthesisReserveTokens > 0 {
		resolved.budget.SynthesisReserveTokens = cfg.Budget.SynthesisReserveTokens
	}
	if cfg.Budget.ContextReserveTokens > 0 {
		resolved.budget.ContextReserveTokens = cfg.Budget.ContextReserveTokens
	}
	if cfg.Budget.TimeoutPerMode > 0 {
		resolved.budget.TimeoutPerMode = cfg.Budget.TimeoutPerMode
	}
	if cfg.Budget.TotalTimeout > 0 {
		resolved.budget.TotalTimeout = cfg.Budget.TotalTimeout
	}
	if cfg.Budget.MaxRetries > 0 {
		resolved.budget.MaxRetries = cfg.Budget.MaxRetries
	}
	if cfg.CacheOverride || cfg.Cache.Enabled || cfg.Cache.MaxEntries > 0 || cfg.Cache.TTL > 0 || cfg.Cache.CacheDir != "" {
		resolved.cache = cfg.Cache
	}
}

func validateResolvedConfig(resolved *resolvedEnsembleConfig, modeIDs []string, catalog *ModeCatalog, allowAdvanced bool) error {
	if resolved == nil {
		return errors.New("resolved config is nil")
	}
	if catalog == nil {
		return errors.New("mode catalog is nil")
	}
	report := NewValidationReport()
	validateModeIDs(modeIDs, catalog, allowAdvanced, report)
	validateSynthesisConfig(resolved.synthesis, catalog, allowAdvanced, report)
	validateBudgetConfig(resolved.budget, report)
	return report.Error()
}

func parseModeRefs(values []string) ([]ModeRef, error) {
	refs := make([]ModeRef, 0, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("modes[%d]: empty mode reference", i)
		}
		if isModeCode(value) {
			refs = append(refs, ModeRefFromCode(strings.ToUpper(value)))
		} else {
			refs = append(refs, ModeRefFromID(strings.ToLower(value)))
		}
	}
	return refs, nil
}

func normalizeExplicitSpecs(specs []string, catalog *ModeCatalog) ([]string, error) {
	var normalized []string
	seen := make(map[string]struct{})
	for _, spec := range specs {
		parts := strings.Split(spec, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			segments := strings.SplitN(part, ":", 2)
			if len(segments) != 2 {
				return nil, fmt.Errorf("explicit assignment %q: expected mode:agent", part)
			}
			modeRef := strings.TrimSpace(segments[0])
			agentType := strings.TrimSpace(segments[1])
			if agentType == "" {
				return nil, fmt.Errorf("explicit assignment %q: empty agent type", part)
			}
			refs, err := parseModeRefs([]string{modeRef})
			if err != nil {
				return nil, err
			}
			modeIDs, err := ResolveModeRefs(refs, catalog)
			if err != nil {
				return nil, err
			}
			modeID := modeIDs[0]
			if _, exists := seen[modeID]; exists {
				return nil, fmt.Errorf("explicit assignment duplicates mode %q", modeID)
			}
			seen[modeID] = struct{}{}
			normalized = append(normalized, fmt.Sprintf("%s:%s", modeID, agentType))
		}
	}
	if len(normalized) == 0 {
		return nil, errors.New("explicit assignment requires at least one mapping")
	}
	return normalized, nil
}

func orderAssignmentsForTimebox(assignments []ModeAssignment, catalog *ModeCatalog) []int {
	order := make([]int, len(assignments))
	for i := range assignments {
		order[i] = i
	}

	sort.Slice(order, func(i, j int) bool {
		aIdx := order[i]
		bIdx := order[j]
		aID := assignments[aIdx].ModeID
		bID := assignments[bIdx].ModeID

		var aMode, bMode *ReasoningMode
		if catalog != nil {
			aMode = catalog.GetMode(aID)
			bMode = catalog.GetMode(bID)
		}

		aPriority := modePriorityWeight(aMode)
		bPriority := modePriorityWeight(bMode)
		if aPriority != bPriority {
			return aPriority > bPriority
		}

		aValuePerSecond := modeValuePerSecond(aMode)
		bValuePerSecond := modeValuePerSecond(bMode)
		if aValuePerSecond != bValuePerSecond {
			return aValuePerSecond > bValuePerSecond
		}

		return aID < bID
	})

	return order
}

func modePriorityWeight(mode *ReasoningMode) int {
	if mode == nil {
		return 0
	}
	switch mode.Tier {
	case TierCore:
		return 3
	case TierAdvanced:
		return 2
	case TierExperimental:
		return 1
	default:
		return 2
	}
}

const (
	timeboxTokensPerSecond   = 25.0
	timeboxMinRuntimeSeconds = 15.0
)

func estimateModeRuntime(mode *ReasoningMode) time.Duration {
	if mode == nil {
		return 0
	}
	tokens := estimateTypicalCost(mode)
	if tokens <= 0 {
		tokens = 2000
	}
	seconds := float64(tokens) / timeboxTokensPerSecond
	if seconds < timeboxMinRuntimeSeconds {
		seconds = timeboxMinRuntimeSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

func modeValuePerSecond(mode *ReasoningMode) float64 {
	if mode == nil {
		return 0
	}
	runtime := estimateModeRuntime(mode).Seconds()
	if runtime <= 0 {
		return 0
	}
	return modeValueScore(mode) / runtime
}

func timeboxDeadline(start time.Time, total time.Duration) time.Time {
	if total <= 0 {
		return time.Time{}
	}
	return start.Add(total)
}

func timeboxExpired(deadline, now time.Time) bool {
	if deadline.IsZero() {
		return false
	}
	return !now.Before(deadline)
}

func markAssignmentsSkipped(assignments []ModeAssignment, indices []int, reason string) []string {
	if reason == "" {
		reason = "skipped"
	}
	now := time.Now().UTC()
	skipped := make([]string, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(assignments) {
			continue
		}
		assignment := &assignments[idx]
		if assignment.Status != AssignmentPending {
			continue
		}
		assignment.Status = AssignmentError
		if assignment.Error == "" {
			assignment.Error = reason
		}
		assignment.CompletedAt = &now
		skipped = append(skipped, assignment.ModeID)
	}
	return skipped
}

func explicitModeIDs(specs []string) []string {
	modeSet := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) == 0 {
			continue
		}
		mode := strings.TrimSpace(parts[0])
		if mode != "" {
			modeSet[mode] = struct{}{}
		}
	}
	ids := make([]string, 0, len(modeSet))
	for id := range modeSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func buildPaneSpecs(cfg *EnsembleConfig, modeCount int) ([]swarm.PaneSpec, error) {
	if modeCount == 0 {
		return nil, errors.New("no modes resolved")
	}

	agentList := expandAgentMix(cfg.AgentMix)
	if len(agentList) == 0 {
		agentList = make([]string, modeCount)
		for i := range agentList {
			agentList[i] = string(tmux.AgentClaude)
		}
	}
	if len(agentList) < modeCount {
		return nil, fmt.Errorf("agent mix provides %d panes for %d modes", len(agentList), modeCount)
	}

	panes := make([]swarm.PaneSpec, 0, len(agentList))
	for i, agentType := range agentList {
		if strings.TrimSpace(agentType) == "" {
			return nil, fmt.Errorf("agent mix contains empty agent type at index %d", i)
		}
		panes = append(panes, swarm.PaneSpec{
			Index:     i + 1,
			AgentType: agentType,
			Project:   cfg.ProjectDir,
		})
	}

	return panes, nil
}

func expandAgentMix(mix map[string]int) []string {
	if len(mix) == 0 {
		return nil
	}
	keys := make([]string, 0, len(mix))
	for key := range mix {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var agents []string
	for _, key := range keys {
		count := mix[key]
		for i := 0; i < count; i++ {
			agents = append(agents, key)
		}
	}
	return agents
}

func assignModes(strategy string, modeIDs []string, explicitSpecs []string, panes []tmux.Pane, catalog *ModeCatalog) ([]ModeAssignment, error) {
	if len(modeIDs) == 0 {
		return nil, errors.New("no modes to assign")
	}
	if len(panes) == 0 {
		return nil, errors.New("no panes available for assignment")
	}

	switch normalizeAssignment(strategy) {
	case assignmentAffinity, assignmentCategory:
		assignments := AssignByCategory(modeIDs, panes, catalog)
		if len(assignments) == 0 {
			return nil, errors.New("affinity assignment returned no assignments")
		}
		return assignments, nil
	case assignmentExplicit:
		if len(explicitSpecs) == 0 {
			return nil, errors.New("explicit assignment requires mode:agent specs")
		}
		return AssignExplicit(explicitSpecs, panes)
	default:
		assignments := AssignRoundRobin(modeIDs, panes)
		if len(assignments) == 0 {
			return nil, errors.New("round-robin assignment returned no assignments")
		}
		return assignments, nil
	}
}

func normalizeAssignment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return assignmentAffinity
	}
	return value
}

func buildPaneTargetMap(sessionName string, panes []tmux.Pane) map[string]string {
	targets := make(map[string]string, len(panes))
	for _, pane := range panes {
		target := pane.ID
		if target == "" {
			target = swarm.GetPaneTarget(sessionName, pane.Index)
		}
		if pane.Title != "" {
			targets[pane.Title] = target
		}
		if pane.ID != "" {
			targets[pane.ID] = target
		}
	}
	return targets
}

func isModeCode(value string) bool {
	return modeCodeRegex.MatchString(strings.ToUpper(strings.TrimSpace(value)))
}
