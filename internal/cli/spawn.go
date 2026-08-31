package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agent/ollama"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/gemini"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/models"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

// optionalDurationValue implements pflag.Value for a duration flag with optional value.
// When the flag is used without a value, it uses the default duration.
// When the flag is used with a value, it parses the duration.
// When the flag is not used, enabled remains false.
type optionalDurationValue struct {
	defaultDuration time.Duration
	duration        *time.Duration
	enabled         *bool
}

const maxStaggerInterval = 5 * time.Minute

func newOptionalDurationValue(defaultDur time.Duration, dur *time.Duration, enabled *bool) *optionalDurationValue {
	*dur = defaultDur // Set default
	return &optionalDurationValue{
		defaultDuration: defaultDur,
		duration:        dur,
		enabled:         enabled,
	}
}

func (v *optionalDurationValue) String() string {
	if v.duration != nil && *v.enabled {
		return v.duration.String()
	}
	return ""
}

func (v *optionalDurationValue) Set(s string) error {
	*v.enabled = true
	if s == "" {
		*v.duration = v.defaultDuration
		return nil
	}
	// Handle "0" as disable
	if s == "0" {
		*v.enabled = false
		*v.duration = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	if dur < 0 {
		return fmt.Errorf("stagger duration cannot be negative")
	}
	if dur > maxStaggerInterval {
		return fmt.Errorf("stagger duration cannot exceed %s", maxStaggerInterval)
	}
	*v.duration = dur
	return nil
}

type spawnTestPacing struct {
	paneDelay  time.Duration
	agentDelay time.Duration
}

const (
	spawnPromptReadyTimeout = 30 * time.Second
	spawnReadyPollInterval  = 200 * time.Millisecond
	// spawnVerifyBootTimeout bounds the --verify-boot post-launch readiness
	// gate (ntm-mr4k): OC-038's manual validator used a 30s budget.
	spawnVerifyBootTimeout = 30 * time.Second
)

type spawnSessionObserver interface {
	Observe(context.Context, string) (statuspkg.SessionObservation, error)
}

var newSpawnSessionObserver = func() spawnSessionObserver {
	return statuspkg.NewSessionObserver(statuspkg.NewDetector())
}

type spawnPromptDispatcher interface {
	Dispatch(context.Context, string, string) (dispatchsvc.Receipt, error)
}

type canonicalSpawnPromptDispatcher struct {
	session        string
	observer       spawnSessionObserver
	listPanes      func(context.Context, string) ([]tmux.Pane, error)
	serviceFactory func() (*dispatchsvc.Service, error)
}

func newCanonicalSpawnPromptDispatcher(session string, observer spawnSessionObserver) *canonicalSpawnPromptDispatcher {
	if observer == nil {
		observer = newSpawnSessionObserver()
	}
	return &canonicalSpawnPromptDispatcher{
		session:   session,
		observer:  observer,
		listPanes: tmux.GetPanesContext,
		serviceFactory: func() (*dispatchsvc.Service, error) {
			return dispatchsvc.NewService(dispatchsvc.Ports{
				Redactor:  shellFinalMessageRedactor(activeShellDispatchRedactionConfig()),
				Protocols: shellDispatchProtocolPlanner{},
				Deliverer: dispatchsvc.TMUXDeliverer{},
			})
		},
	}
}

// Dispatch preflights the final redacted message against an exact pane ID,
// then re-observes that pane immediately before actuation. A failed, stale, or
// non-idle observation is a guaranteed no-send result.
func (d *canonicalSpawnPromptDispatcher) Dispatch(ctx context.Context, paneID, message string) (dispatchsvc.Receipt, error) {
	if d == nil || strings.TrimSpace(d.session) == "" {
		return dispatchsvc.Receipt{}, errors.New("spawn prompt dispatcher requires a session")
	}
	if d.observer == nil {
		return dispatchsvc.Receipt{}, errors.New("spawn prompt dispatcher requires a session observer")
	}
	if d.listPanes == nil || d.serviceFactory == nil {
		return dispatchsvc.Receipt{}, errors.New("spawn prompt dispatcher requires topology and delivery services")
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return dispatchsvc.Receipt{}, errors.New("spawn prompt dispatcher requires an exact pane ID")
	}
	selector, selectorErr := tmux.ParsePaneSelector(paneID)
	if selectorErr != nil || selector.Kind != tmux.PaneSelectorID {
		return dispatchsvc.Receipt{}, fmt.Errorf("spawn prompt dispatcher requires an exact %%N pane ID, got %q", paneID)
	}

	panes, err := d.listPanes(ctx, d.session)
	if err != nil {
		return dispatchsvc.Receipt{}, fmt.Errorf("load spawn prompt topology: %w", err)
	}
	service, err := d.serviceFactory()
	if err != nil {
		return dispatchsvc.Receipt{}, err
	}
	prepared, err := service.Prepare(ctx, dispatchsvc.Request{
		Session:               d.session,
		Panes:                 panes,
		Selectors:             []string{paneID},
		RequireSingleSelector: true,
		IncludeUser:           true,
		Message:               message,
		Submit:                true,
		StopOnFailure:         true,
	})
	if err != nil {
		return dispatchsvc.Receipt{}, err
	}

	observation, observeErr := d.observer.Observe(ctx, d.session)
	if observeErr != nil {
		return dispatchsvc.Receipt{}, fmt.Errorf("re-observe pane %s before spawn prompt dispatch: %w", paneID, observeErr)
	}
	if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) {
		return dispatchsvc.Receipt{}, fmt.Errorf("pane %s spawn prompt observation is stale", paneID)
	}
	if !spawnObservationSafeToDispatch(observation, paneID) {
		if pane, ok := observation.PaneByID(paneID); ok {
			if pane.Current.Error != "" {
				return dispatchsvc.Receipt{}, fmt.Errorf(
					"pane %s is not freshly and confidently idle for spawn prompt dispatch: %s",
					paneID, pane.Current.Error,
				)
			}
			if spawnPaneCommandIsShell(pane.Metadata.Command) {
				return dispatchsvc.Receipt{}, fmt.Errorf(
					"pane %s agent process has not replaced shell %q", paneID, pane.Metadata.Command,
				)
			}
		}
		return dispatchsvc.Receipt{}, fmt.Errorf("pane %s is not freshly and confidently idle for spawn prompt dispatch", paneID)
	}

	result, dispatchErr := service.Dispatch(ctx, prepared)
	if dispatchErr != nil {
		return dispatchsvc.Receipt{}, dispatchErr
	}
	receipt, err := validateSinglePaneDispatchResult(result, paneID)
	if err != nil {
		return dispatchsvc.Receipt{}, err
	}
	return receipt, nil
}

type spawnPromptStep struct {
	Kind    string
	Message string
	Delay   time.Duration
}

func resolveSpawnTestPacing() (spawnTestPacing, error) {
	if os.Getenv("NTM_TEST_MODE") == "" && os.Getenv("NTM_E2E") == "" {
		return spawnTestPacing{}, nil
	}

	defaultDelay, err := parseEnvDurationMs("NTM_TEST_SPAWN_DELAY_MS")
	if err != nil {
		return spawnTestPacing{}, err
	}

	paneDelay, err := parseEnvDurationMs("NTM_TEST_SPAWN_PANE_DELAY_MS")
	if err != nil {
		return spawnTestPacing{}, err
	}
	if paneDelay == 0 {
		paneDelay = defaultDelay
	}

	agentDelay, err := parseEnvDurationMs("NTM_TEST_SPAWN_AGENT_DELAY_MS")
	if err != nil {
		return spawnTestPacing{}, err
	}
	if agentDelay == 0 {
		agentDelay = defaultDelay
	}

	if paneDelay == 0 && agentDelay == 0 {
		return spawnTestPacing{}, nil
	}

	return spawnTestPacing{
		paneDelay:  paneDelay,
		agentDelay: agentDelay,
	}, nil
}

func parseEnvDurationMs(key string) (time.Duration, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer millisecond value, got %q", key, val)
	}
	if ms < 0 {
		return 0, fmt.Errorf("%s must be non-negative, got %d", key, ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func resolveSpawnAssignAgentType(agent string, ccOnly, codOnly, gmiOnly, agyOnly bool) string {
	if strings.TrimSpace(agent) != "" {
		return robot.ResolveAgentType(agent)
	}
	if ccOnly {
		return "claude"
	}
	if codOnly {
		return "codex"
	}
	if gmiOnly {
		return "gemini"
	}
	if agyOnly {
		return "antigravity"
	}
	return ""
}

func parseLocalFallbackProvider(raw string) (AgentType, error) {
	provider := strings.TrimSpace(raw)
	if provider == "" {
		return AgentTypeCodex, nil
	}
	switch agentpkg.AgentType(provider).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return AgentTypeClaude, nil
	case agentpkg.AgentTypeCodex:
		return AgentTypeCodex, nil
	case agentpkg.AgentTypeGemini:
		return AgentTypeGemini, nil
	case agentpkg.AgentTypeAntigravity:
		return AgentTypeAntigravity, nil
	default:
		return "", fmt.Errorf("invalid --local-fallback-provider %q (expected one of: cc|cod|gmi|agy)", raw)
	}
}

func canonicalSpawnAgentType(raw string) (AgentType, bool) {
	switch agentpkg.AgentType(raw).Canonical() {
	case agentpkg.AgentTypeClaudeCode:
		return AgentTypeClaude, true
	case agentpkg.AgentTypeCodex:
		return AgentTypeCodex, true
	case agentpkg.AgentTypeGemini:
		return AgentTypeGemini, true
	case agentpkg.AgentTypeAntigravity:
		return AgentTypeAntigravity, true
	case agentpkg.AgentTypeGrok:
		return AgentTypeGrok, true
	case agentpkg.AgentTypeCursor:
		return AgentTypeCursor, true
	case agentpkg.AgentTypeWindsurf:
		return AgentTypeWindsurf, true
	case agentpkg.AgentTypeAider:
		return AgentTypeAider, true
	case agentpkg.AgentTypeOpencode:
		return AgentTypeOpencode, true
	case agentpkg.AgentTypeOllama:
		return AgentTypeOllama, true
	default:
		return "", false
	}
}

func orderedSpawnAgentTypes() []AgentType {
	return []AgentType{
		AgentTypeClaude,
		AgentTypeCodex,
		AgentTypeGemini,
		AgentTypeAntigravity,
		AgentTypeGrok,
		AgentTypeCursor,
		AgentTypeWindsurf,
		AgentTypeAider,
		AgentTypeOpencode,
		AgentTypeOllama,
	}
}

func existingSpawnAgentTypes(specs AgentSpecs) map[AgentType]bool {
	existing := make(map[AgentType]bool, len(specs))
	for _, spec := range specs {
		existing[spec.Type] = true
	}
	return existing
}

func canonicalSpawnCountTotals(counts map[string]int) map[AgentType]int {
	totals := make(map[AgentType]int)
	for rawType, count := range counts {
		if count <= 0 {
			continue
		}
		agentType, ok := canonicalSpawnAgentType(rawType)
		if !ok {
			continue
		}
		totals[agentType] += count
	}
	return totals
}

func formatSpawnCountSummary(counts map[string]int) string {
	totals := canonicalSpawnCountTotals(counts)
	parts := make([]string, 0, len(totals))
	for _, agentType := range orderedSpawnAgentTypes() {
		count := totals[agentType]
		if count <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, agentType))
	}
	return strings.Join(parts, ", ")
}

func appendMissingCountMapAgentSpecs(agentSpecs *AgentSpecs, counts map[string]int) {
	if agentSpecs == nil || len(counts) == 0 {
		return
	}

	existing := existingSpawnAgentTypes(*agentSpecs)
	totals := canonicalSpawnCountTotals(counts)
	for _, agentType := range orderedSpawnAgentTypes() {
		count := totals[agentType]
		if count <= 0 || existing[agentType] {
			continue
		}
		*agentSpecs = append(*agentSpecs, AgentSpec{Type: agentType, Count: count})
	}
}

// mergeRecipeSettingsIntoExistingSpecs applies one recipe row's model,
// reasoning effort, and persona to the CLI-provided specs of the same agent
// type, without touching their counts. Per-field semantics: a CLI-explicit
// model or effort wins; the recipe's value fills only empty fields. A recipe
// persona always attaches (CLI count flags cannot carry a persona), with the
// merged model — CLI's if set, else the recipe row's — becoming the persona's
// model override, mirroring the append path below.
func mergeRecipeSettingsIntoExistingSpecs(
	agentSpecs *AgentSpecs,
	personaMap map[string]*persona.Persona,
	loadRegistry func() (*persona.Registry, error),
	recipeName string,
	rowIdx int,
	agentType AgentType,
	recipeAgent recipe.AgentSpec,
) error {
	recipeModel := strings.TrimSpace(recipeAgent.Model)
	recipeEffort := strings.TrimSpace(recipeAgent.ReasoningEffort)
	personaName := strings.TrimSpace(recipeAgent.Persona)
	if recipeModel == "" && recipeEffort == "" && personaName == "" {
		return nil
	}

	var recipePersona *persona.Persona
	if personaName != "" {
		if personaMap == nil {
			return fmt.Errorf("recipe %q requires persona support for %q", recipeName, personaName)
		}
		reg, err := loadRegistry()
		if err != nil {
			return fmt.Errorf("loading persona registry for recipe %q: %w", recipeName, err)
		}
		p, found := reg.Get(personaName)
		if !found {
			return fmt.Errorf("recipe %q references unknown persona %q", recipeName, recipeAgent.Persona)
		}
		recipePersona = p
	}

	for si := range *agentSpecs {
		spec := &(*agentSpecs)[si]
		if spec.Type != agentType {
			continue
		}
		if spec.ReasoningEffort == "" && recipeEffort != "" {
			spec.ReasoningEffort = recipeEffort
		}
		if recipePersona == nil {
			if spec.Model == "" && recipeModel != "" {
				spec.Model = recipeModel
			}
			continue
		}
		model := spec.Model
		if model == "" {
			model = recipeModel
		}
		personaKey := strings.ToLower(personaName)
		if model != "" {
			// Unique key per merged spec: two CLI specs of the same type may
			// merge with different models, so they need distinct overrides.
			personaKey = fmt.Sprintf("__recipe_%s_%d_%d_%s", strings.ToLower(recipeName), rowIdx, si, personaKey)
			override := *recipePersona
			override.Model = model
			personaMap[personaKey] = &override
		} else if _, exists := personaMap[personaKey]; !exists {
			personaMap[personaKey] = recipePersona
		}
		spec.Model = personaKey
	}
	return nil
}

func appendMissingRecipeAgentSpecs(agentSpecs *AgentSpecs, personaMap map[string]*persona.Persona, recipeName, projectDir string, recipeAgents []recipe.AgentSpec) error {
	if agentSpecs == nil || len(recipeAgents) == 0 {
		return nil
	}

	existing := existingSpawnAgentTypes(*agentSpecs)

	var registry *persona.Registry
	loadRegistry := func() (*persona.Registry, error) {
		if registry != nil {
			return registry, nil
		}
		loaded, err := persona.LoadRegistry(projectDir)
		if err != nil {
			return nil, err
		}
		registry = loaded
		return registry, nil
	}

	mergedTypes := make(map[AgentType]bool)
	for i, recipeAgent := range recipeAgents {
		if recipeAgent.Count <= 0 {
			continue
		}

		agentType, ok := canonicalSpawnAgentType(recipeAgent.Type)
		if !ok {
			return fmt.Errorf("recipe %q uses unsupported agent type %q", recipeName, recipeAgent.Type)
		}
		if existing[agentType] {
			// An explicit CLI count (--cc=N, ...) overrides the recipe's COUNT
			// for this type, but must not silently drop the recipe row's other
			// settings: the row's model/effort/persona still merge onto the
			// CLI-provided specs (CLI-explicit model/effort win; recipe fills
			// the gaps). Dropping the whole row meant `-r review-team --cc=3`
			// quietly shed the recipe's persona and model with no signal
			// (bd-ws7-docs-ux-truth-tqh3l.5). Only the first recipe row of an
			// overridden type merges; later duplicate-type rows would fight
			// over the same CLI specs.
			if mergedTypes[agentType] {
				continue
			}
			mergedTypes[agentType] = true
			if err := mergeRecipeSettingsIntoExistingSpecs(agentSpecs, personaMap, loadRegistry, recipeName, i, agentType, recipeAgent); err != nil {
				return err
			}
			continue
		}

		spec := AgentSpec{
			Type:            agentType,
			Count:           recipeAgent.Count,
			Model:           strings.TrimSpace(recipeAgent.Model),
			ReasoningEffort: strings.TrimSpace(recipeAgent.ReasoningEffort),
		}

		personaName := strings.TrimSpace(recipeAgent.Persona)
		if personaName != "" {
			if personaMap == nil {
				return fmt.Errorf("recipe %q requires persona support for %q", recipeName, personaName)
			}
			reg, err := loadRegistry()
			if err != nil {
				return fmt.Errorf("loading persona registry for recipe %q: %w", recipeName, err)
			}
			p, found := reg.Get(personaName)
			if !found {
				return fmt.Errorf("recipe %q references unknown persona %q", recipeName, recipeAgent.Persona)
			}

			personaKey := strings.ToLower(personaName)
			if spec.Model != "" {
				personaKey = fmt.Sprintf("__recipe_%s_%d_%s", strings.ToLower(recipeName), i, personaKey)
				override := *p
				override.Model = spec.Model
				personaMap[personaKey] = &override
			} else if _, exists := personaMap[personaKey]; !exists {
				personaMap[personaKey] = p
			}
			spec.Model = personaKey
		}

		*agentSpecs = append(*agentSpecs, spec)
	}

	return nil
}

func recomputeSpawnAgentCounts(opts *SpawnOptions) {
	opts.CCCount = 0
	opts.CodCount = 0
	opts.GmiCount = 0
	opts.AgyCount = 0
	opts.GrokCount = 0
	opts.CursorCount = 0
	opts.WindsurfCount = 0
	opts.AiderCount = 0
	opts.OpencodeCount = 0
	opts.OllamaCount = 0

	for _, agent := range opts.Agents {
		switch agent.Type {
		case AgentTypeClaude:
			opts.CCCount++
		case AgentTypeCodex:
			opts.CodCount++
		case AgentTypeGemini:
			opts.GmiCount++
		case AgentTypeAntigravity:
			opts.AgyCount++
		case AgentTypeGrok:
			opts.GrokCount++
		case AgentTypeCursor:
			opts.CursorCount++
		case AgentTypeWindsurf:
			opts.WindsurfCount++
		case AgentTypeAider:
			opts.AiderCount++
		case AgentTypeOpencode:
			opts.OpencodeCount++
		case AgentTypeOllama:
			opts.OllamaCount++
		}
	}
}

func populateSpawnAgentsFromCounts(opts *SpawnOptions) {
	if opts == nil || len(opts.Agents) > 0 {
		return
	}

	specs := AgentSpecs{}
	legacyCounts := []struct {
		agentType AgentType
		count     int
	}{
		{agentType: AgentTypeClaude, count: opts.CCCount},
		{agentType: AgentTypeCodex, count: opts.CodCount},
		{agentType: AgentTypeGemini, count: opts.GmiCount},
		{agentType: AgentTypeAntigravity, count: opts.AgyCount},
		{agentType: AgentTypeGrok, count: opts.GrokCount},
		{agentType: AgentTypeCursor, count: opts.CursorCount},
		{agentType: AgentTypeWindsurf, count: opts.WindsurfCount},
		{agentType: AgentTypeAider, count: opts.AiderCount},
		{agentType: AgentTypeOpencode, count: opts.OpencodeCount},
		{agentType: AgentTypeOllama, count: opts.OllamaCount},
	}
	for _, entry := range legacyCounts {
		if entry.count <= 0 {
			continue
		}
		specs = append(specs, AgentSpec{Type: entry.agentType, Count: entry.count})
	}
	opts.Agents = specs.Flatten()
}

func normalizeSpawnOptions(opts *SpawnOptions) {
	if opts == nil {
		return
	}
	populateSpawnAgentsFromCounts(opts)
	if len(opts.Agents) > 0 {
		recomputeSpawnAgentCounts(opts)
	}
}

func validateSpawnStaggerOptions(opts SpawnOptions) error {
	switch opts.StaggerMode {
	case "", "none", "fixed", "smart":
	default:
		return fmt.Errorf("--stagger-mode must be one of none, fixed, or smart; got %q", opts.StaggerMode)
	}
	if opts.Stagger < 0 || opts.Stagger > maxStaggerInterval {
		return fmt.Errorf("--stagger must be between 0 and %s", maxStaggerInterval)
	}
	if opts.StaggerMode == "fixed" && (opts.StaggerDelay < 0 || opts.StaggerDelay > maxStaggerInterval) {
		return fmt.Errorf("--stagger-delay must be between 0 and %s", maxStaggerInterval)
	}
	return nil
}

func validateSpawnPaneEnv(env map[string]string) error {
	for key, value := range env {
		if !isSpawnEnvironmentName(key) {
			return fmt.Errorf("invalid --pane-env name %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("invalid --pane-env value for %q: contains NUL", key)
		}
	}
	return nil
}

func isSpawnEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && r != '_' && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func expandSpawnPaneEnv(env map[string]string, project string, pane int, role AgentType) map[string]string {
	if len(env) == 0 {
		return nil
	}
	replacer := strings.NewReplacer("{project}", project, "{pane}", strconv.Itoa(pane), "{role}", string(role))
	expanded := make(map[string]string, len(env))
	for key, value := range env {
		expanded[key] = replacer.Replace(value)
	}
	return expanded
}

func prependSpawnPaneEnv(command string, env map[string]string) string {
	if len(env) == 0 {
		return command
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var prefix strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&prefix, "%s=%s ", key, tmux.ShellQuote(env[key]))
	}
	return prefix.String() + command
}

func validateSpawnAgentTypes(agents []FlatAgent, pluginMap map[string]plugins.AgentPlugin, personaMap map[string]*persona.Persona) error {
	if err := validateAgentModelOverrides(agents, personaMap); err != nil {
		return err
	}
	for _, agent := range agents {
		switch agent.Type {
		case AgentTypeClaude, AgentTypeCodex, AgentTypeGemini, AgentTypeAntigravity, AgentTypeGrok,
			AgentTypeOllama, AgentTypeCursor, AgentTypeWindsurf, AgentTypeAider, AgentTypeOpencode:
			continue
		default:
			if _, ok := pluginMap[string(agent.Type)]; !ok {
				return fmt.Errorf("unknown agent type %q", agent.Type)
			}
		}
	}
	return nil
}

// spawnAgentCommandTemplate resolves the launch command template and any env
// vars for one agent type. Shared by the launch loop and the preflight
// validation pass so both see identical template selection.
func spawnAgentCommandTemplate(agentType AgentType, pluginMap map[string]plugins.AgentPlugin, ollamaHost string) (string, map[string]string, error) {
	switch agentType {
	case AgentTypeClaude:
		return cfg.Agents.Claude, nil, nil
	case AgentTypeCodex:
		return cfg.Agents.Codex, nil, nil
	case AgentTypeGemini:
		return cfg.Agents.Gemini, nil, nil
	case AgentTypeAntigravity:
		tmpl := cfg.Agents.Antigravity
		if tmpl == "" {
			// Default when [agents] antigravity isn't configured (e.g. a
			// config.toml that predates this provider). The model is pinned by
			// ResolveModel; --dangerously-skip-permissions is agy's autonomous
			// flag, which the dcg agy guard (F5) backstops. {{agyBinary}} resolves
			// agy-locked (when on PATH) over the frequently-aliased `agy`.
			tmpl = config.DefaultAgentTemplates().Antigravity
		}
		return tmpl, nil, nil
	case AgentTypeGrok:
		tmpl := cfg.Agents.Grok
		if tmpl == "" {
			tmpl = config.DefaultAgentTemplates().Grok
		}
		return tmpl, nil, nil
	case AgentTypeOllama:
		var envVars map[string]string
		if ollamaHost != "" {
			envVars = map[string]string{"OLLAMA_HOST": ollamaHost}
		}
		return cfg.Agents.Ollama, envVars, nil
	case AgentTypeCursor:
		return cfg.Agents.Cursor, nil, nil
	case AgentTypeWindsurf:
		return cfg.Agents.Windsurf, nil, nil
	case AgentTypeAider:
		return cfg.Agents.Aider, nil, nil
	case AgentTypeOpencode:
		// Falls back to the model-aware default when [agents] oc is unset,
		// so `--oc=N:provider/model` works and Agent Mail registration
		// receives a non-empty model. Users can override via
		// `[agents] oc = "..."` to point at a wrapper script or pin a
		// specific provider/model. See ntm#193.
		return opencodeCommandOrDefault(cfg.Agents.Opencode), nil, nil
	default:
		if p, ok := pluginMap[string(agentType)]; ok {
			return p.Command, p.Env, nil
		}
		return "", nil, fmt.Errorf("unknown agent type %q", agentType)
	}
}

// validateSpawnAgentCommands is the preflight counterpart to the launch loop's
// per-agent config.GenerateAgentCommand call. It renders every agent's command
// before any mutation (worktrees, session, panes) so a config/spec conflict —
// e.g. an explicit `--cod=N:model` variant against an [agents] command that
// hardcodes -m with no {{.Model}} reference — aborts the spawn while there is
// still nothing to clean up, instead of stranding a half-launched session
// (ntm-akaq). Persona system-prompt files are not prepared yet at this stage,
// so a stand-in path is substituted whenever the agent's persona carries a
// system prompt: it exercises the same template branches (and the
// silent-persona-drop guard — see config.GenerateAgentCommand) as the real
// file will at launch, so a persona the template cannot deliver aborts the
// spawn before any pane exists. The rendered preflight output is discarded.
// personaPreflightPromptFile is the stand-in path preflight substitutes for
// a persona's not-yet-written system-prompt file, purely to exercise the
// template's {{if .SystemPromptFile}} branches and the silent-drop guard.
const personaPreflightPromptFile = "/dev/null"

func validateSpawnAgentCommands(opts SpawnOptions, ollamaHost string) error {
	for _, agent := range opts.Agents {
		tmpl, _, err := spawnAgentCommandTemplate(agent.Type, opts.PluginMap, ollamaHost)
		if err != nil {
			return err
		}

		resolvedModel := resolveAgentModel(agent.Type, agent.Model, opts.PluginMap)
		modelRequested := strings.TrimSpace(agent.Model) != ""
		reasoningEffort := agent.ReasoningEffort
		systemPromptPlaceholder := ""
		if agent.Persona == nil && opts.PersonaMap != nil {
			if p, ok := opts.PersonaMap[agent.Model]; ok {
				modelRequested = strings.TrimSpace(p.Model) != ""
				resolvedModel = resolveAgentModel(agent.Type, p.Model, opts.PluginMap)
				if strings.TrimSpace(p.ReasoningEffort) != "" {
					reasoningEffort = p.ReasoningEffort
				}
				if strings.TrimSpace(p.SystemPrompt) != "" {
					systemPromptPlaceholder = personaPreflightPromptFile
				}
			}
		}
		if agent.Persona != nil {
			if strings.TrimSpace(agent.Persona.Model) != "" {
				modelRequested = true
				resolvedModel = resolveAgentModel(agent.Type, agent.Persona.Model, opts.PluginMap)
			}
			if strings.TrimSpace(agent.Persona.ReasoningEffort) != "" {
				reasoningEffort = agent.Persona.ReasoningEffort
			}
			if strings.TrimSpace(agent.Persona.SystemPrompt) != "" {
				systemPromptPlaceholder = personaPreflightPromptFile
			}
		}

		if _, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{
			Model:            resolvedModel,
			ModelAlias:       agent.Model,
			ModelRequested:   modelRequested,
			SessionName:      opts.Session,
			PaneIndex:        agent.Index,
			AgentType:        string(agent.Type),
			ReasoningEffort:  reasoningEffort,
			SystemPromptFile: systemPromptPlaceholder,
		}); err != nil {
			return fmt.Errorf("agent command preflight for %s agent %d: %w", agent.Type, agent.Index, err)
		}
	}
	return nil
}

// validateGrokPhaseOneSpawn retains the last deliberate Grok Build spawn
// refusal after the GH#251 phase-2 flip: persona prompt injection. The Grok
// Build CLI exposes no system-prompt flag or env var (verified against
// `grok --help`), so a persona cannot be delivered faithfully — pretending
// via first-prompt prepending is a different contract than cc/cod personas.
// Prompt delivery, CASS context, marching orders, --assign, and
// --auto-restart now flow through the grok-aware readiness/composer/verify
// protocol implemented from live grok 1.0.5 captures.
func validateGrokPhaseOneSpawn(opts SpawnOptions, _ *config.Config) error {
	for _, spec := range opts.Agents {
		if spec.Type != AgentTypeGrok {
			continue
		}
		if spec.Persona != nil {
			return errors.New("Grok Build spawn does not support persona prompt injection: the Grok Build CLI has no system-prompt flag or env var")
		}
		if profile, ok := opts.PersonaMap[spec.Model]; ok && profile != nil {
			return errors.New("Grok Build spawn does not support persona prompt injection: the Grok Build CLI has no system-prompt flag or env var")
		}
	}
	return nil
}

func validateSpawnPaneCapacity(panes []tmux.Pane, startIdx, agentCount int) error {
	available := len(panes) - startIdx
	if available < 0 {
		available = 0
	}
	if available < agentCount {
		return fmt.Errorf(
			"spawn requires %d agent pane(s), but only %d are available after reserving %d user pane(s)",
			agentCount, available, startIdx,
		)
	}
	return nil
}

func validateSpawnGrokPaneBaselines(panes []tmux.Pane, startIdx int, agents []FlatAgent) error {
	for agentOffset, launch := range agents {
		if launch.Type != AgentTypeGrok {
			continue
		}
		paneOffset := startIdx + agentOffset
		if paneOffset < 0 || paneOffset >= len(panes) {
			return fmt.Errorf(
				"no assigned pane for Grok Build agent %d at offset %d",
				launch.Index,
				paneOffset,
			)
		}
		if err := tmux.ValidatePaneLaunchBaseline(panes[paneOffset]); err != nil {
			return fmt.Errorf("validate Grok Build agent %d: %w", launch.Index, err)
		}
	}
	return nil
}

// expandProfileAgents converts an ordered persona list (from --profile-set or
// --profiles) into concrete spawn agents — one agent per persona, in
// persona-set order, with each agent's Type taken from the persona's own
// agent_type and the persona attached. This makes --profile-set a first-class
// spawn contract (persona drives the agent) instead of an order-dependent
// overlay on generic agents (ntm#149).
//
// When the caller also supplied explicit generic counts (--cc/--cod/--gmi/...),
// the persona set's per-type distribution must match those counts exactly;
// otherwise expansion fails closed so a pane can never silently run the wrong
// agent CLI or receive the wrong persona.
func expandProfileAgents(profiles []*persona.Persona, requested AgentSpecs) ([]FlatAgent, error) {
	if len(profiles) == 0 {
		return nil, nil
	}

	agents := make([]FlatAgent, 0, len(profiles))
	indices := make(map[AgentType]int)
	personaCounts := make(map[AgentType]int)
	for _, p := range profiles {
		if p == nil {
			continue
		}
		at := AgentType(p.AgentTypeFlag())
		indices[at]++
		personaCounts[at]++
		agents = append(agents, FlatAgent{
			Type:            at,
			Index:           indices[at],
			Model:           p.Model,
			ReasoningEffort: p.ReasoningEffort,
			Persona:         p,
		})
	}

	// Validate against any explicitly requested generic counts. An empty
	// request means "let the persona set fully drive the spawn".
	requestedCounts := make(map[AgentType]int)
	for _, s := range requested {
		requestedCounts[s.Type] += s.Count
	}
	if len(requestedCounts) > 0 {
		if err := validateProfileAgentDistribution(personaCounts, requestedCounts); err != nil {
			return nil, err
		}
	}

	return agents, nil
}

// validateProfileAgentDistribution fails closed when the per-type agent
// distribution implied by a persona set does not match the explicitly
// requested generic counts. This catches both a raw count mismatch
// (--cod=3 vs a 2-codex set) and an agent-type conflict (a claude persona
// dropped into a codex-only request).
func validateProfileAgentDistribution(personaCounts, requestedCounts map[AgentType]int) error {
	typeSet := make(map[AgentType]struct{})
	for t := range personaCounts {
		typeSet[t] = struct{}{}
	}
	for t := range requestedCounts {
		typeSet[t] = struct{}{}
	}
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, string(t))
	}
	sort.Strings(types)

	var mismatches []string
	for _, ts := range types {
		t := AgentType(ts)
		if personaCounts[t] != requestedCounts[t] {
			mismatches = append(mismatches, fmt.Sprintf("%s: profile-set defines %d, you requested %d", ts, personaCounts[t], requestedCounts[t]))
		}
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("profile-set agent distribution conflicts with the requested agent counts (%s); either drop the per-type counts and let --profile-set drive the spawn, or make the counts match the persona set exactly", strings.Join(mismatches, "; "))
	}
	return nil
}

// sortPanesForAssignment orders panes deterministically — by window index, then
// pane index — so agent[i] always lands on the same pane regardless of the
// order tmux list-panes happens to return. This is what makes persona→pane
// assignment reproducible for role-based --profile-set spawns (ntm#149).
func sortPanesForAssignment(panes []tmux.Pane) {
	sort.SliceStable(panes, func(i, j int) bool {
		if panes[i].WindowIndex != panes[j].WindowIndex {
			return panes[i].WindowIndex < panes[j].WindowIndex
		}
		return panes[i].Index < panes[j].Index
	})
}

func legacySpawnTotalAgentCount(opts SpawnOptions) int {
	return opts.CCCount + opts.CodCount + opts.GmiCount + opts.AgyCount + opts.GrokCount + opts.CursorCount + opts.WindsurfCount + opts.AiderCount + opts.OpencodeCount + opts.OllamaCount
}

func spawnHookCountEnv(totalAgents int, opts SpawnOptions) map[string]string {
	return map[string]string{
		"NTM_AGENT_COUNT_CC":       fmt.Sprintf("%d", opts.CCCount),
		"NTM_AGENT_COUNT_COD":      fmt.Sprintf("%d", opts.CodCount),
		"NTM_AGENT_COUNT_GMI":      fmt.Sprintf("%d", opts.GmiCount),
		"NTM_AGENT_COUNT_AGY":      fmt.Sprintf("%d", opts.AgyCount),
		"NTM_AGENT_COUNT_GROK":     fmt.Sprintf("%d", opts.GrokCount),
		"NTM_AGENT_COUNT_CURSOR":   fmt.Sprintf("%d", opts.CursorCount),
		"NTM_AGENT_COUNT_WINDSURF": fmt.Sprintf("%d", opts.WindsurfCount),
		"NTM_AGENT_COUNT_AIDER":    fmt.Sprintf("%d", opts.AiderCount),
		"NTM_AGENT_COUNT_OC":       fmt.Sprintf("%d", opts.OpencodeCount),
		"NTM_AGENT_COUNT_OLLAMA":   fmt.Sprintf("%d", opts.OllamaCount),
		"NTM_AGENT_COUNT_TOTAL":    fmt.Sprintf("%d", totalAgents),
	}
}

func spawnSessionCreatedEventFields(opts SpawnOptions, dir string) map[string]string {
	return map[string]string{
		"project_dir":    dir,
		"recipe":         opts.RecipeName,
		"agent_count":    fmt.Sprintf("%d", legacySpawnTotalAgentCount(opts)),
		"agent_cc":       fmt.Sprintf("%d", opts.CCCount),
		"agent_cod":      fmt.Sprintf("%d", opts.CodCount),
		"agent_gmi":      fmt.Sprintf("%d", opts.GmiCount),
		"agent_agy":      fmt.Sprintf("%d", opts.AgyCount),
		"agent_grok":     fmt.Sprintf("%d", opts.GrokCount),
		"agent_cursor":   fmt.Sprintf("%d", opts.CursorCount),
		"agent_windsurf": fmt.Sprintf("%d", opts.WindsurfCount),
		"agent_aider":    fmt.Sprintf("%d", opts.AiderCount),
		"agent_oc":       fmt.Sprintf("%d", opts.OpencodeCount),
		"agent_ollama":   fmt.Sprintf("%d", opts.OllamaCount),
	}
}

func applyLocalFallback(opts *SpawnOptions, provider AgentType) int {
	if opts == nil || len(opts.Agents) == 0 {
		return 0
	}

	replaced := 0
	indices := make(map[AgentType]int)
	for i := range opts.Agents {
		if opts.Agents[i].Type == AgentTypeOllama {
			opts.Agents[i].Type = provider
			// Always use the provider default model on fallback.
			opts.Agents[i].Model = ""
			replaced++
		}
		indices[opts.Agents[i].Type]++
		opts.Agents[i].Index = indices[opts.Agents[i].Type]
	}

	if replaced > 0 {
		recomputeSpawnAgentCounts(opts)
		opts.LocalHost = ""
	}

	return replaced
}

func handleOllamaPreflightError(opts *SpawnOptions, preflightErr error) (bool, string, error) {
	if preflightErr == nil {
		return false, "", nil
	}
	if opts == nil || !opts.LocalFallback {
		return false, "", preflightErr
	}

	provider := opts.LocalFallbackProvider
	if provider == "" {
		provider = AgentTypeCodex
	}

	replaced := applyLocalFallback(opts, provider)
	if replaced == 0 {
		return false, "", preflightErr
	}

	msg := fmt.Sprintf("Ollama preflight failed (%v); falling back %d local agent(s) to %s", preflightErr, replaced, provider)
	return true, msg, nil
}

func (v *optionalDurationValue) Type() string {
	return "duration"
}

// IsBoolFlag allows --stagger without =value
func (v *optionalDurationValue) IsBoolFlag() bool {
	return true
}

// NoOptDefVal is the default when --stagger is used without a value
func (v *optionalDurationValue) NoOptDefVal() string {
	return v.defaultDuration.String()
}

func resolveEffectiveStaggerMode(opts SpawnOptions) string {
	effective := opts.StaggerMode
	if effective == "" || effective == "none" {
		if opts.StaggerEnabled && opts.Stagger > 0 {
			return "legacy"
		}
	}
	return effective
}

func resolveStaggerInterval(mode string, opts SpawnOptions, tracker *ratelimit.RateLimitTracker) time.Duration {
	interval := opts.Stagger
	switch mode {
	case "fixed":
		interval = opts.StaggerDelay
	case "smart":
		if tracker != nil {
			// Determine provider priority: Anthropic > OpenAI > Google
			provider := "anthropic" // Default to strictest

			hasAnthropic := opts.CCCount > 0
			hasOpenAI := opts.CodCount > 0
			hasGoogle := opts.GmiCount > 0 || opts.AgyCount > 0

			// Check detailed agent list if available (source of truth)
			if len(opts.Agents) > 0 {
				hasAnthropic = false
				hasOpenAI = false
				hasGoogle = false
				for _, a := range opts.Agents {
					switch a.Type {
					case AgentTypeClaude:
						hasAnthropic = true
					case AgentTypeCodex:
						hasOpenAI = true
					case AgentTypeGemini:
						hasGoogle = true
					case AgentTypeAntigravity:
						hasGoogle = true
					}
				}
			}

			if hasAnthropic {
				provider = "anthropic"
			} else if hasOpenAI {
				provider = "openai"
			} else if hasGoogle {
				provider = "google"
			}

			interval = tracker.GetOptimalDelay(provider)
		}
	}
	return interval
}

func codexCooldownRemaining(tracker *ratelimit.RateLimitTracker, alreadyWaited bool) (time.Duration, bool) {
	if tracker == nil || alreadyWaited {
		return 0, alreadyWaited
	}
	return tracker.CooldownRemaining("openai"), true
}

// SpawnOptions configures session creation and agent spawning
type SpawnOptions struct {
	Session            string
	ProjectDirOverride string
	PaneEnv            map[string]string
	Agents             []FlatAgent
	CCCount            int
	CodCount           int
	GmiCount           int
	AgyCount           int
	GrokCount          int
	CursorCount        int
	WindsurfCount      int
	AiderCount         int
	OpencodeCount      int
	OllamaCount        int
	UserPane           bool
	AutoRestart        bool
	RecipeName         string
	PersonaMap         map[string]*persona.Persona
	PluginMap          map[string]plugins.AgentPlugin

	// Profiles from --profile-set/--profiles are expanded into concrete ordered
	// agents (each carrying its persona) via expandProfileAgents before this
	// struct is built, so they ride in Agents — there is no separate list here.
	//
	// ProfileSetName is the --profile-set name, surfaced in the post-launch
	// persona→pane mapping. Empty for --profiles (comma list) spawns.
	ProfileSetName string

	// CASS Context
	CassContextQuery string
	NoCassContext    bool

	// Recovery suppression (independent of CASS)
	NoRecovery bool
	Prompt     string
	InitPrompt string
	// InitPromptWithAgentName, when true, prepends a deterministic
	// `You are agent <session>_<type>_<idx>` preamble to the init prompt
	// for each pane. Helps multi-agent prompts that key off identity
	// (Agent Mail handles, beads ownership, etc.). See ntm#138.
	InitPromptWithAgentName bool
	LocalModel              string
	LocalHost               string
	LocalFallback           bool
	LocalFallbackProvider   AgentType

	// Hooks
	NoHooks bool

	// Safety mode: fail if session already exists
	Safety bool

	// Stagger configuration for thundering herd prevention
	// StaggerMode: "smart", "fixed", or "none" (default)
	// - smart: Use learned optimal delays from RateLimitTracker
	// - fixed: Use fixed delay (StaggerDelay)
	// - none: No staggering (backward compatible default)
	StaggerMode  string        // "smart", "fixed", or "none"
	StaggerDelay time.Duration // Delay for fixed mode (default 30s)

	// Legacy stagger fields (deprecated, kept for backward compatibility)
	Stagger        time.Duration // Delay between agent prompt delivery
	StaggerEnabled bool          // True if --stagger flag was provided

	// Assignment configuration for spawn+assign workflow
	Assign             bool          // Enable auto-assignment after spawn
	AssignStrategy     string        // Assignment strategy: balanced, speed, quality, dependency, round-robin
	AssignLimit        int           // Maximum assignments (0 = unlimited)
	AssignReadyTimeout time.Duration // Timeout waiting for agents to become ready
	AssignVerbose      bool          // Show detailed scoring/decision logs during assignment
	AssignQuiet        bool          // Suppress non-essential assignment output
	AssignTimeout      time.Duration // Timeout for external calls during assignment (bv, br, Agent Mail)
	AssignAgentType    string        // Filter assignment to specific agent type (claude, codex, gemini)
	AssignCCOnly       bool          // Only assign to Claude agents (alias for --assign-agent=claude)
	AssignCodOnly      bool          // Only assign to Codex agents (alias for --assign-agent=codex)
	AssignGmiOnly      bool          // Only assign to Gemini agents (alias for --assign-agent=gemini)
	AssignAgyOnly      bool          // Only assign to Antigravity agents (alias for --assign-agent=antigravity)
	// A non-nil admission means policy and the full actionable set were
	// verified before spawn, including the valid no-work case of an empty set.
	assignAdmission *spawnAssignmentAdmission

	// CreateDir opts into creating a missing project directory without an
	// interactive confirm. In non-TTY contexts a missing directory is a
	// structured error instead of a prompt (ntm-5ni5): an orchestrator loop
	// that misspells a session name must fail fast, not hang on [y/N].
	CreateDir bool

	// VerifyBoot blocks after agent launch until every agent reaches a
	// ready state (bounded by spawnVerifyBootTimeout), failing the spawn
	// loudly otherwise (ntm-mr4k). Opt-in: default-on would block every
	// spawn ~30s for non-interactive fake/plugin agents and duplicate the
	// robot surface's --spawn-wait contract.
	VerifyBoot bool

	// Git worktree isolation configuration
	UseWorktrees bool // Enable git worktree isolation for agents
	// WorktreeName, when set, overrides the auto-derived worktree directory
	// name (the default is `<agent-type>_<index>`, e.g., `cc_1`). External
	// orchestrators that drive ntm spawn for multiple labels but share an
	// agent slot (`--cc=1` across `--label foo|bar|baz`) get matching
	// `cc_1` directory names and silently share a single worktree — see
	// ntm#145. Passing `--worktree-name foo` per spawn keeps the paths
	// distinct. Only honored when len(Agents) == 1 (multi-agent spawns
	// still need per-agent paths for the isolation contract to hold).
	WorktreeName string

	// Privacy mode configuration (bd-2u3tv)
	PrivacyMode  bool // Enable privacy mode (no persistence)
	AllowPersist bool // Allow persistence even in privacy mode

	// Marching orders: pane-specific initialization prompts (bd-2lodn)
	MarchingOrders map[int]string // agent pane order (0-based, excludes user pane) -> prompt

	// Per-agent-type default prompts (bd-2ywo)
	DefaultPrompts config.PromptsConfig

	// Goal label for multi-session support (bd-1933u)
	Label string
}

type spawnAssignmentAdmission struct {
	projectDir string
	actionable []bv.TriageRecommendation
}

type spawnAssignmentPreflightDependencies struct {
	configurePolicy func(string) error
	fetchActionable func(context.Context, string, int) ([]bv.TriageRecommendation, error)
}

func preflightWorktreeProject(ctx context.Context, projectDir string) error {
	if ctx == nil {
		return errors.New("worktree preflight requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("worktree preflight canceled: %w", err)
	}

	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return errors.New("worktree preflight requires a project directory")
	}
	info, err := os.Stat(projectDir)
	if err != nil {
		return fmt.Errorf("cannot create isolated agent worktrees: inspect project %s: %w", projectDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cannot create isolated agent worktrees: project path is not a directory: %s", projectDir)
	}

	runGit := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", projectDir}, args...)...)
		output, commandErr := cmd.CombinedOutput()
		trimmedOutput := strings.TrimSpace(string(output))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trimmedOutput, fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		return trimmedOutput, commandErr
	}

	insideWorktree, err := runGit("rev-parse", "--is-inside-work-tree")
	if err != nil || insideWorktree != "true" {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		detail := insideWorktree
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return fmt.Errorf(
			"cannot create isolated agent worktrees.\n\n"+
				"Project: %s\n"+
				"Problem: this path is not an initialized Git working tree (%s).\n\n"+
				"Initialize the repository and create its first commit, or rerun without --worktrees",
			projectDir, detail,
		)
	}

	headDetail, err := runGit("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("worktree preflight canceled: %w", ctxErr)
		}
		if headDetail == "" {
			headDetail = err.Error()
		}
		return fmt.Errorf(
			"cannot create isolated agent worktrees.\n\n"+
				"Project: %s\n"+
				"Problem: the Git repository has no valid HEAD commit (%s).\n\n"+
				"Create the initial commit, or rerun without --worktrees",
			projectDir, headDetail,
		)
	}

	return nil
}

func validateSpawnWorktreeOptions(opts SpawnOptions) error {
	if !opts.UseWorktrees {
		return nil
	}
	if opts.WorktreeName != "" && len(opts.Agents) > 1 {
		return fmt.Errorf(
			"--worktree-name is only valid for single-agent spawns; got %d agents",
			len(opts.Agents),
		)
	}
	return nil
}

func spawnSafetySessionExistsError(session string) error {
	return fmt.Errorf("session '%s' already exists (--safety mode prevents reuse; use 'ntm kill %s' first)", session, session)
}

func defaultSpawnAssignmentPreflightDependencies() spawnAssignmentPreflightDependencies {
	return spawnAssignmentPreflightDependencies{
		configurePolicy: configureAuthoritativeAssignmentPolicy,
		fetchActionable: bv.GetActionableRecommendationsContext,
	}
}

func preflightSpawnAssignment(
	ctx context.Context,
	projectDir string,
	timeout time.Duration,
	deps spawnAssignmentPreflightDependencies,
) (*spawnAssignmentAdmission, error) {
	if ctx == nil {
		return nil, errors.New("spawn assignment preflight requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("spawn assignment preflight canceled: %w", err)
	}
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, errors.New("spawn assignment preflight requires an authoritative project directory")
	}
	projectDir = filepath.Clean(projectDir)
	if deps.configurePolicy == nil || deps.fetchActionable == nil {
		return nil, errors.New("spawn assignment preflight dependencies are incomplete")
	}
	if err := deps.configurePolicy(projectDir); err != nil {
		return nil, fmt.Errorf("spawn assignment policy preflight: %w", err)
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	planningCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actionable, err := deps.fetchActionable(planningCtx, projectDir, 100)
	if err != nil {
		if ctxErr := planningCtx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("spawn assignment planning preflight canceled: %w", ctxErr)
		}
		return nil, fmt.Errorf("spawn assignment stopped because actionable work could not be verified: %w", err)
	}

	return &spawnAssignmentAdmission{
		projectDir: projectDir,
		actionable: cloneSpawnActionableRecommendations(actionable),
	}, nil
}

func cloneSpawnActionableRecommendations(source []bv.TriageRecommendation) []bv.TriageRecommendation {
	if source == nil {
		return []bv.TriageRecommendation{}
	}
	cloned := make([]bv.TriageRecommendation, len(source))
	for index, recommendation := range source {
		recommendation.Labels = append([]string(nil), recommendation.Labels...)
		recommendation.Reasons = append([]string(nil), recommendation.Reasons...)
		recommendation.UnblocksIDs = append([]string(nil), recommendation.UnblocksIDs...)
		recommendation.BlockedBy = append([]string(nil), recommendation.BlockedBy...)
		if recommendation.Breakdown != nil {
			breakdown := *recommendation.Breakdown
			recommendation.Breakdown = &breakdown
		}
		cloned[index] = recommendation
	}
	return cloned
}

// RecoveryContext holds all the information needed to help an agent recover
// from a previous session, including beads, messages, and procedural memories.
type RecoveryContext struct {
	// Checkpoint contains checkpoint info for recovery
	Checkpoint *RecoveryCheckpoint `json:"checkpoint,omitempty"`
	// Beads contains in-progress beads from BV
	Beads []RecoveryBead `json:"beads,omitempty"`
	// CompletedBeads contains recently completed beads for context
	CompletedBeads []RecoveryBead `json:"completed_beads,omitempty"`
	// BlockedBeads contains blocked beads for awareness
	BlockedBeads []RecoveryBead `json:"blocked_beads,omitempty"`
	// Messages contains recent Agent Mail messages
	Messages []RecoveryMessage `json:"messages,omitempty"`
	// CMMemories contains procedural memories from CM
	CMMemories *RecoveryCMMemories `json:"cm_memories,omitempty"`
	// FileReservations contains files currently reserved by this session
	FileReservations []string `json:"file_reservations,omitempty"`
	// ReservationTransfer contains results from attempting to transfer reservations
	ReservationTransfer *handoff.ReservationTransferResult `json:"reservation_transfer,omitempty"`
	// Sessions contains past sessions for recovery context
	Sessions []RecoverySession `json:"sessions,omitempty"`
	// Summary is a human-readable summary of the recovery context
	Summary string `json:"summary,omitempty"`
	// TokenCount is an estimate of the total token count
	TokenCount int `json:"token_count,omitempty"`
	// Error contains error info if recovery was partial
	Error *RecoveryError `json:"error,omitempty"`
}

// RecoveryError represents an error during recovery context building.
type RecoveryError struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Component   string   `json:"component"` // Which component failed
	Recoverable bool     `json:"recoverable"`
	Details     []string `json:"details,omitempty"`
}

// RecoveryCheckpoint represents checkpoint info for recovery.
type RecoveryCheckpoint struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	CreatedAt   time.Time                  `json:"created_at"`
	PaneCount   int                        `json:"pane_count"`
	HasGitPatch bool                       `json:"has_git_patch"`
	WorkingDir  string                     `json:"working_dir,omitempty"`
	Assignments *RecoveryAssignmentSummary `json:"assignments_summary,omitempty"`
	BVSummary   *RecoveryBVSummary         `json:"bv_summary,omitempty"`
}

// RecoveryAssignmentSummary captures assignment status counts from a checkpoint.
type RecoveryAssignmentSummary struct {
	Total      int `json:"total"`
	Assigned   int `json:"assigned,omitempty"`
	Working    int `json:"working,omitempty"`
	Completed  int `json:"completed,omitempty"`
	Failed     int `json:"failed,omitempty"`
	Reassigned int `json:"reassigned,omitempty"`
}

// RecoveryBVSummary captures BV snapshot counts from a checkpoint.
type RecoveryBVSummary struct {
	OpenCount       int       `json:"open_count"`
	ActionableCount int       `json:"actionable_count"`
	BlockedCount    int       `json:"blocked_count"`
	InProgressCount int       `json:"in_progress_count"`
	TopPicks        []string  `json:"top_picks,omitempty"`
	CapturedAt      time.Time `json:"captured_at"`
}

// RecoverySession represents a previous session for recovery.
type RecoverySession struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	AgentType string    `json:"agent_type"`
}

// RecoveryBead represents a bead in recovery context
type RecoveryBead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Assignee string `json:"assignee,omitempty"`
}

// RecoveryMessage represents an Agent Mail message in recovery context
type RecoveryMessage struct {
	ID         int       `json:"id"`
	From       string    `json:"from"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body,omitempty"`
	Importance string    `json:"importance,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// RecoveryCMMemories contains procedural memories from CASS Memory (CM)
type RecoveryCMMemories struct {
	Rules        []RecoveryCMRule `json:"rules,omitempty"`
	AntiPatterns []RecoveryCMRule `json:"anti_patterns,omitempty"`
}

// RecoveryCMRule represents a rule from CM playbook
type RecoveryCMRule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

func newSpawnCmd() *cobra.Command {
	var noUserPane bool
	var recipeName string
	var templateName string
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs
	var autoRestart bool
	var contextQuery string
	var noCassContext bool
	var noRecovery bool
	var contextLimit int
	var contextDays int
	var prompt string
	var initPrompt string
	var initPromptWithAgentName bool
	var noHooks bool
	var profilesFlag string
	var profileSetFlag string
	var sessionProfileName string // bd-29kr: session profile
	var staggerDuration time.Duration
	var staggerEnabled bool
	var safety bool
	var localCount int
	var ollamaCount int
	var localModel string
	var localHost string
	var localFallback bool
	var localFallbackProvider string
	var paneEnv map[string]string

	// New stagger flags for bd-2wih
	var staggerMode string         // smart, fixed, or none
	var staggerDelay time.Duration // delay for fixed mode

	// Assignment flags for spawn+assign workflow (bd-3nde)
	var assignEnabled bool
	var assignStrategy string
	var assignLimit int
	var assignReadyTimeout time.Duration
	var assignVerbose bool
	var assignQuiet bool
	var assignTimeout time.Duration
	var assignAgentType string
	var assignCCOnly bool
	var assignCodOnly bool
	var assignGmiOnly bool
	var assignAgyOnly bool

	// Git worktree isolation flag
	var useWorktrees bool
	var createDir bool
	var verifyBoot bool
	var worktreeName string

	// Privacy mode flag (bd-2u3tv)
	var privacyMode bool
	var allowPersist bool

	// Marching orders flag (bd-2lodn)
	var marchingOrdersFile string

	// Goal label for multi-session support (bd-1933u)
	var label string

	// Interactive wizard flag
	var interactive bool

	// Load plugins eagerly — required for dynamic flag registration on the cobra
	// command. Plugin loading is fast (directory scan + TOML parse) but if it
	// becomes a bottleneck, consider lazy flag registration via cobra's
	// TraverseRunHooks or a PersistentPreRunE on the spawn subcommand.
	pluginsDir := pluginAgentsDirForArgs(os.Args[1:])
	loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
	preloadedPluginMap := make(map[string]plugins.AgentPlugin)
	for _, p := range loadedPlugins {
		preloadedPluginMap[p.Name] = p
	}

	cmd := &cobra.Command{
		Use:   "spawn <session-name>",
		Short: "Create session and spawn AI agents in panes",
		Long: `Create a new tmux session and launch AI coding agents in separate panes.

The session name determines the project working directory (projects_base/session_name)
which is also used as the Agent Mail project key. For cross-agent messaging to work,
the session name must match an actual directory under projects_base.

By default, the first pane is reserved for the user. Agent panes are created
and titled with their type (e.g., myproject__cc_1, myproject__cod_1).

You can use a recipe to quickly spawn a predefined set of agents:
  ntm spawn myproject -r full-stack    # Use the 'full-stack' recipe

Or use a workflow template for coordination patterns:
  ntm spawn myproject -t red-green     # Use the 'red-green' TDD template

Agent count syntax: N or N:model where N is count and model is optional.
Multiple flags of the same type accumulate.

Built-in recipes: quick-claude, full-stack, minimal, codex-heavy, balanced, review-team
Built-in templates: red-green, review-pipeline, specialist-team, parallel-explore
Use 'ntm recipes list' or 'ntm workflows list' to see all available options.

Auto-restart mode (--auto-restart):
  Monitors agent health and automatically restarts crashed agents.
  Configure via [resilience] section in config.toml:
    max_restarts = 3         # Max restart attempts per agent
    restart_delay_seconds = 30  # Delay before restart
    health_check_seconds = 10   # Health check interval

Assignment mode (--assign):
  Spawns agents, waits for them to become ready, then assigns work using ntm assign.
  Optional init prompt is sent only after agents are ready.

  Examples:
    ntm spawn myproject --cc=4 --assign
    ntm spawn myproject --cc=2 --cod=2 --assign --strategy=dependency
    ntm spawn myproject --cc=4 --assign --init-prompt='Read AGENTS.md first'
    ntm spawn myproject --cc=4 --assign --limit=8

Persona mode:
  Use --persona to spawn agents with predefined roles and system prompts.
  Format: --persona=name or --persona=name:count
  Built-in personas: architect, implementer, reviewer, tester, documenter

CASS Context Injection:
  Automatically finds relevant past sessions and injects context into agents.
  Use --cass-context="query" to be specific, or rely on prompt/recipe context.

Stagger mode (--stagger-mode):
  Prevents thundering herd and rate limiting when spawning multiple agents.
  All panes are created immediately for dashboard visibility, but prompts
  are delivered with delays between agents.

  Modes:
    - smart: Adaptive delays from rate limit tracker (learns optimal spacing)
    - fixed: Use fixed delay (--stagger-delay, default 30s)
    - none:  No staggering (default, backward compatible)

  Legacy --stagger flag still works for duration-based staggering.
  Smart mode automatically backs off on rate limits and speeds up on success.

Worktree isolation (--worktrees):
  Creates separate Git worktrees for each agent, allowing safe parallel work.
  Each agent gets its own branch (ntm/<session>/<agent>) and working directory.
  Reduces conflicts and isolates destructive operations to individual worktrees.

  Examples:
    ntm spawn myproject --cc=3 --worktrees
    ntm worktrees list                    # View created worktrees
    ntm worktrees merge claude_1          # Merge agent's work back to main

Local fallback (--local-fallback):
  If Ollama is unavailable or model preflight fails, local agents can be
  converted to cloud agents instead of failing spawn.
  --local-fallback-provider selects fallback target (cc, cod, gmi, agy).

For running multiple agent swarms on the same project with different goals,
use --label:

  ntm spawn myproject --label frontend --cc=3
  ntm spawn myproject --label backend --cc=2

This creates separate sessions (myproject--frontend, myproject--backend) that
share the same project directory. Use ntm list --project myproject to see all.

Examples:
  ntm spawn myproject --cc=2 --cod=2           # 2 Claude, 2 Codex + user pane
  ntm spawn myproject --cc=3 --cod=3 --agy=1   # 3 Claude, 3 Codex, 1 Antigravity
  ntm spawn myproject --cc=3 --cod=3 --gmi=1   # legacy: Gemini CLI instead of Antigravity
  ntm spawn myproject --cc=4 --no-user         # 4 Claude, no user pane
  ntm spawn myproject -r full-stack            # Use full-stack recipe
  ntm spawn myproject -t red-green             # Use red-green workflow template
  ntm spawn myproject -t parallel-explore --cc=4  # Template with count override
  ntm spawn myproject --cc=2:opus --cc=1:sonnet  # 2 Opus + 1 Sonnet
  ntm spawn myproject --cc=2 --auto-restart    # With auto-restart enabled
  ntm spawn myproject --persona=architect --persona=implementer:2  # Using personas
  ntm spawn myproject --cc=1 --prompt="fix auth" # Inject context about auth
  ntm spawn myproject --cc=3 --stagger --prompt="find bugs"  # Staggered prompts (legacy)
  ntm spawn myproject --cc=5 --stagger-mode=smart  # Adaptive rate limit avoidance
  ntm spawn myproject --cc=4 --stagger-mode=fixed --stagger-delay=20s  # Fixed 20s delay
  ntm spawn myproject --local=2 --local-fallback --local-fallback-provider=cod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]

			// Reject project names containing "--" (reserved separator) (bd-1933u)
			if err := config.ValidateProjectName(sessionName); err != nil {
				return err
			}

			// Apply goal label to session name (bd-1933u)
			if label != "" {
				if err := config.ValidateLabel(label); err != nil {
					return fmt.Errorf("invalid label: %w", err)
				}
				sessionName = config.FormatSessionName(sessionName, label)
			}

			dir, resolveErr := resolveCreationProjectDirForSession(sessionName)
			if resolveErr != nil {
				return resolveErr
			}

			// Interactive wizard: triggered by --interactive flag or when no agents specified and TTY available
			if interactive && len(agentSpecs) == 0 && recipeName == "" && templateName == "" && len(personaSpecs) == 0 {
				wizResult, err := runSpawnWizard(sessionName)
				if err != nil {
					return err
				}
				if !wizResult.Confirmed {
					return fmt.Errorf("spawn cancelled")
				}
				agentSpecs = append(agentSpecs, wizardLaunchAgentSpecs(wizResult)...)
				if wizResult.Recipe != "" {
					recipeName = wizResult.Recipe
				}
				if wizResult.Template != "" {
					templateName = wizResult.Template
				}
				autoRestart = wizResult.AutoRestart
			}

			// Update CASS config from flags
			if contextLimit > 0 {
				cfg.CASS.Context.MaxSessions = contextLimit
			}
			if contextDays > 0 {
				cfg.CASS.Context.LookbackDays = contextDays
			}

			// Use pre-loaded plugins
			pluginMap := preloadedPluginMap

			// Handle personas first
			personaMap := make(map[string]*persona.Persona)
			if len(personaSpecs) > 0 {
				resolved, err := ResolvePersonas(personaSpecs, dir)
				if err != nil {
					return err
				}
				personaAgents := FlattenPersonas(resolved)
				for _, pa := range personaAgents {
					agentSpecs = append(agentSpecs, AgentSpec{
						Type:  pa.AgentType,
						Count: 1,
						Model: pa.PersonaName,
					})
				}
				for _, r := range resolved {
					personaMap[r.Persona.Name] = r.Persona
				}
				if !IsJSONOutput() {
					fmt.Printf("Resolved %d persona agent(s)\n", len(personaAgents))
				}
			}

			// Handle recipe
			if recipeName != "" {
				loader := recipe.NewLoader()
				r, err := loader.Get(recipeName)
				if err != nil {
					available := recipe.BuiltinNames()
					return fmt.Errorf("%w\n\nAvailable built-in recipes: %s",
						err, strings.Join(available, ", "))
				}
				if err := r.Validate(); err != nil {
					return fmt.Errorf("invalid recipe %q: %w", recipeName, err)
				}
				if err := appendMissingRecipeAgentSpecs(&agentSpecs, personaMap, r.Name, dir, r.Agents); err != nil {
					return err
				}
				if !IsJSONOutput() {
					fmt.Printf("Using recipe '%s': %s\n", r.Name, r.Description)
				}
			}

			// Handle workflow template (similar to recipe but uses workflow templates)
			if templateName != "" {
				if recipeName != "" {
					return fmt.Errorf("cannot use both --recipe and --template; pick one")
				}
				wfLoader := workflow.NewLoader()
				tmpl, err := wfLoader.Get(templateName)
				if err != nil {
					available := workflow.BuiltinNames()
					return fmt.Errorf("%w\n\nAvailable built-in templates: %s",
						err, strings.Join(available, ", "))
				}
				if err := tmpl.Validate(); err != nil {
					return fmt.Errorf("invalid template %q: %w", templateName, err)
				}
				appendMissingCountMapAgentSpecs(&agentSpecs, tmpl.AgentCounts())
				if !IsJSONOutput() {
					fmt.Printf("Using template '%s': %s (%s coordination)\n",
						tmpl.Name, tmpl.Description, tmpl.Coordination)
				}
			}

			var err error
			localModel, err = appendOllamaAgentSpecs(&agentSpecs, localCount, ollamaCount, localModel)
			if err != nil {
				return err
			}
			fallbackProvider, err := parseLocalFallbackProvider(localFallbackProvider)
			if err != nil {
				return err
			}

			// Extract simple counts
			ccCount := agentSpecs.ByType(AgentTypeClaude).TotalCount()
			codCount := agentSpecs.ByType(AgentTypeCodex).TotalCount()
			gmiCount := agentSpecs.ByType(AgentTypeGemini).TotalCount()

			// Apply implicit project count defaults only when the user did not
			// request a persona set/list. With --profile-set/--profiles the
			// persona set is the authoritative agent spec (ntm#149), so folding
			// in ProjectDefaults here would make expandProfileAgents validate
			// the set against counts the user never asked for and fail closed.
			if len(agentSpecs) == 0 && len(cfg.ProjectDefaults) > 0 && profilesFlag == "" && profileSetFlag == "" {
				appendMissingCountMapAgentSpecs(&agentSpecs, cfg.ProjectDefaults)
				ccCount = agentSpecs.ByType(AgentTypeClaude).TotalCount()
				codCount = agentSpecs.ByType(AgentTypeCodex).TotalCount()
				gmiCount = agentSpecs.ByType(AgentTypeGemini).TotalCount()
				if !IsJSONOutput() && len(agentSpecs) > 0 {
					fmt.Printf("Using default configuration: %s\n", formatSpawnCountSummary(cfg.ProjectDefaults))
				}
			}

			// Handle --profiles and --profile-set flags for profile assignment
			var profileList []*persona.Persona
			if profilesFlag != "" && profileSetFlag != "" {
				return fmt.Errorf("cannot use both --profiles and --profile-set; pick one")
			}
			if profilesFlag != "" || profileSetFlag != "" {
				registry, err := persona.LoadRegistry(dir)
				if err != nil {
					return fmt.Errorf("loading persona registry: %w", err)
				}

				var profileNames []string
				if profileSetFlag != "" {
					// Resolve profile set to list of names
					pset, ok := registry.GetSet(profileSetFlag)
					if !ok {
						sets := registry.ListSets()
						var available []string
						for _, s := range sets {
							available = append(available, s.Name)
						}
						return fmt.Errorf("profile set %q not found; available: %s", profileSetFlag, strings.Join(available, ", "))
					}
					profileNames = pset.Personas
				} else {
					// Parse comma-separated profile names
					profileNames = strings.Split(profilesFlag, ",")
					for i := range profileNames {
						profileNames[i] = strings.TrimSpace(profileNames[i])
					}
				}

				// Look up each persona in registry
				for _, name := range profileNames {
					if name == "" {
						continue
					}
					p, ok := registry.Get(name)
					if !ok {
						return fmt.Errorf("profile %q not found in registry", name)
					}
					profileList = append(profileList, p)
				}

			}

			// Parse marching orders file if provided (bd-2lodn)
			var marchingOrders map[int]string
			if marchingOrdersFile != "" {
				var err error
				marchingOrders, err = ParseMarchingOrders(marchingOrdersFile)
				if err != nil {
					return fmt.Errorf("--marching-orders: %w", err)
				}
			}

			assignAgentFilter := resolveSpawnAssignAgentType(assignAgentType, assignCCOnly, assignCodOnly, assignGmiOnly, assignAgyOnly)

			// Build the concrete agent list. When a persona set/list is
			// requested (--profile-set/--profiles), expand it into ordered
			// concrete agents (ntm#149); the persona drives each agent's type,
			// model, and prompt. normalizeSpawnOptions recomputes per-type
			// counts from this list, so pane creation/preflight stay consistent.
			agentsFlat := agentSpecs.Flatten()
			if len(profileList) > 0 {
				expanded, expandErr := expandProfileAgents(profileList, agentSpecs)
				if expandErr != nil {
					return expandErr
				}
				agentsFlat = expanded
			}

			opts := SpawnOptions{
				Session:                 sessionName,
				PaneEnv:                 paneEnv,
				Agents:                  agentsFlat,
				CCCount:                 ccCount,
				CodCount:                codCount,
				GmiCount:                gmiCount,
				UserPane:                !noUserPane,
				AutoRestart:             autoRestart,
				RecipeName:              recipeName,
				PersonaMap:              personaMap,
				PluginMap:               pluginMap,
				CassContextQuery:        contextQuery,
				NoCassContext:           noCassContext,
				NoRecovery:              noRecovery,
				Prompt:                  prompt,
				InitPrompt:              initPrompt,
				InitPromptWithAgentName: initPromptWithAgentName,
				LocalModel:              localModel,
				LocalHost:               localHost,
				LocalFallback:           localFallback,
				LocalFallbackProvider:   fallbackProvider,
				NoHooks:                 noHooks,
				Safety:                  safety,
				StaggerMode:             staggerMode,
				StaggerDelay:            staggerDelay,
				Stagger:                 staggerDuration,
				StaggerEnabled:          staggerEnabled,
				ProfileSetName:          profileSetFlag,
				Assign:                  assignEnabled,
				AssignStrategy:          assignStrategy,
				AssignLimit:             assignLimit,
				AssignReadyTimeout:      assignReadyTimeout,
				AssignVerbose:           assignVerbose,
				AssignQuiet:             assignQuiet,
				AssignTimeout:           assignTimeout,
				AssignAgentType:         assignAgentFilter,
				UseWorktrees:            useWorktrees,
				WorktreeName:            worktreeName,
				CreateDir:               createDir,
				VerifyBoot:              verifyBoot,
				PrivacyMode:             privacyMode,
				AllowPersist:            allowPersist,
				MarchingOrders:          marchingOrders,
				DefaultPrompts:          cfg.Prompts,
				Label:                   label,
			}

			normalizeSpawnOptions(&opts)

			// Apply session profile if specified (bd-29kr).
			// Profile provides defaults; explicit flags override.
			if sessionProfileName != "" {
				profile, err := LoadSessionProfile(sessionProfileName)
				if err != nil {
					return fmt.Errorf("loading profile %q: %w", sessionProfileName, err)
				}
				ApplySessionProfileToSpawnOptions(&opts, profile)
				normalizeSpawnOptions(&opts)
			}
			return spawnSessionLogicContext(cmd.Context(), opts)
		},
	}

	// Use custom flag values that accumulate specs with type info
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeClaude, &agentSpecs), "cc", "Claude agents (N, N:model, N:model:effort, or N:model@effort)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCodex, &agentSpecs), "cod", "Codex agents (N, N:model, N:model:effort, or N:model@effort)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGemini, &agentSpecs), "gmi", "Gemini agents (N or N:model, model charset: a-zA-Z0-9._/@:+-)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeAntigravity, &agentSpecs), "agy", "Antigravity (agy) agents (N; model is pinned to Gemini 3.7 Flash (High))")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGrok, &agentSpecs), "grok", "Grok Build agents (N, N:model, N:model:effort, or N:model@effort)")
	cmd.Flags().IntVar(&localCount, "local", 0, "Local agents via Ollama (alias: --ollama)")
	cmd.Flags().IntVar(&ollamaCount, "ollama", 0, "Alias for --local (explicit Ollama)")
	cmd.Flags().StringVar(&localModel, "local-model", "codellama:latest", "Ollama model to run for --local/--ollama agents")
	cmd.Flags().StringVar(&localHost, "local-host", "", "Ollama host URL for --local/--ollama agents (overrides OLLAMA_HOST/NTM_OLLAMA_HOST)")
	cmd.Flags().BoolVar(&localFallback, "local-fallback", false, "Fallback local Ollama agents to cloud provider when preflight fails")
	cmd.Flags().StringVar(&localFallbackProvider, "local-fallback-provider", "cod", "Provider for --local-fallback: cc|cod|gmi|agy")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCursor, &agentSpecs), "cursor", "Cursor agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeWindsurf, &agentSpecs), "windsurf", "Windsurf agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeAider, &agentSpecs), "aider", "Aider agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeOpencode, &agentSpecs), "oc", "Opencode agents (N or N:model)")
	cmd.Flags().Var(&personaSpecs, "persona", "Persona-defined agents (name or name:count)")
	cmd.Flags().BoolVar(&noUserPane, "no-user", false, "don't reserve a pane for the user")
	cmd.Flags().StringVarP(&recipeName, "recipe", "r", "", "use a recipe for agent configuration")
	cmd.Flags().StringVarP(&templateName, "template", "t", "", "size the session from a workflow template's agent counts (counts only; run the coordination with 'ntm workflow run')")
	cmd.Flags().BoolVar(&autoRestart, "auto-restart", false, "monitor and auto-restart crashed agents")

	// Goal label for multi-session support (bd-1933u)
	cmd.Flags().StringVarP(&label, "label", "l", "", "Goal label for multi-session support (e.g., --label frontend creates session PROJECT--frontend)")

	// Stagger flag for thundering herd prevention
	// Custom handling: --stagger enables with the documented 90s default;
	// --stagger=2m supplies a custom duration.
	staggerValue := newOptionalDurationValue(90*time.Second, &staggerDuration, &staggerEnabled)
	cmd.Flags().Var(staggerValue, "stagger", "Stagger prompt delivery between agents (default 90s when enabled)")
	cmd.Flags().Lookup("stagger").NoOptDefVal = staggerValue.NoOptDefVal()

	// New stagger mode flags (bd-2wih)
	cmd.Flags().StringVar(&staggerMode, "stagger-mode", "none", "Stagger mode: smart (adaptive), fixed, or none")
	cmd.Flags().DurationVar(&staggerDelay, "stagger-delay", 30*time.Second, "Fixed delay between agents (used with --stagger-mode=fixed)")

	// CASS context flags
	cmd.Flags().StringVar(&contextQuery, "cass-context", "", "Explicit context query for CASS")
	cmd.Flags().BoolVar(&noCassContext, "no-cass-context", false, "Disable CASS context injection (does not affect session recovery)")
	cmd.Flags().BoolVar(&noRecovery, "no-recovery", false, "Disable session recovery prompt injection (does not affect CASS context)")
	cmd.Flags().IntVar(&contextLimit, "cass-context-limit", 0, "Max past sessions to include")
	cmd.Flags().IntVar(&contextDays, "cass-context-days", 0, "Look back N days")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to initialize agents with")
	cmd.Flags().StringVar(&initPrompt, "init-prompt", "", "Prompt to send after agents are ready (used with --assign)")
	cmd.Flags().BoolVar(&initPromptWithAgentName, "with-agent-name", false, "Prepend a `You are agent <name>` preamble to --init-prompt for each pane so agents know their deterministic identity. See ntm#138.")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&safety, "safety", false, "Fail if session already exists (prevents accidental reuse)")

	// Assignment flags for spawn+assign workflow
	cmd.Flags().BoolVar(&assignEnabled, "assign", false, "Auto-assign beads to spawned agents after ready")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum beads to assign (0 = unlimited)")
	cmd.Flags().DurationVar(&assignReadyTimeout, "ready-timeout", 60*time.Second, "Timeout waiting for agents to become ready")
	cmd.Flags().BoolVarP(&assignVerbose, "assign-verbose", "", false, "Show detailed scoring/decision logs during assignment")
	cmd.Flags().BoolVarP(&assignQuiet, "assign-quiet", "", false, "Suppress non-essential assignment output")
	cmd.Flags().DurationVar(&assignTimeout, "assign-timeout", 30*time.Second, "Timeout for external calls during assignment (bv, br, Agent Mail)")
	cmd.Flags().StringVar(&assignAgentType, "assign-agent", "", "Filter assignment to specific agent type: claude, codex, gemini, antigravity")
	cmd.Flags().BoolVar(&assignCCOnly, "assign-cc-only", false, "Only assign to Claude agents (alias for --assign-agent=claude)")
	cmd.Flags().BoolVar(&assignCodOnly, "assign-cod-only", false, "Only assign to Codex agents (alias for --assign-agent=codex)")
	cmd.Flags().BoolVar(&assignGmiOnly, "assign-gmi-only", false, "Only assign to Gemini agents (alias for --assign-agent=gemini)")
	cmd.Flags().BoolVar(&assignAgyOnly, "assign-agy-only", false, "Only assign to Antigravity agents (alias for --assign-agent=antigravity)")

	// Git worktree isolation flag
	cmd.Flags().BoolVar(&useWorktrees, "worktrees", false, "Enable git worktree isolation for agents (each agent gets isolated working directory)")
	cmd.Flags().BoolVar(&createDir, "create-dir", false, "Create the project directory if it does not exist (non-TTY spawns error instead of prompting)")
	cmd.Flags().BoolVar(&verifyBoot, "verify-boot", false, "After launching, wait up to 30s for every agent to reach a working prompt; exit non-zero naming what failed to boot")
	cmd.Flags().StringVar(&worktreeName, "worktree-name", "", "Override the auto-derived worktree directory name (single-agent spawns only). Use this when external orchestrators spawn the same agent slot across multiple --label values — without it, the `cc_1` / `cod_1` paths collide. See ntm#145.")
	cmd.Flags().StringToStringVar(&paneEnv, "pane-env", nil, "NTM-owned pane environment (KEY=TEMPLATE; templates: {project}, {pane}, {role})")

	// Privacy mode flags (bd-2u3tv)
	cmd.Flags().BoolVar(&privacyMode, "privacy", false, "Enable privacy mode (disables persistence of session data)")
	cmd.Flags().BoolVar(&allowPersist, "allow-persist", false, "Allow persistence operations even in privacy mode")

	// Marching orders: pane-specific initialization prompts (bd-2lodn)
	cmd.Flags().StringVar(&marchingOrdersFile, "marching-orders", "", "File with agent-pane prompts in spawn order, excluding any user pane (format: pane:N <prompt>)")

	// Profile flags for mapping personas to agents
	cmd.Flags().StringVar(&profilesFlag, "profiles", "", "Comma-separated list of profile/persona names to map to agents in order")
	cmd.Flags().StringVar(&profileSetFlag, "profile-set", "", "Predefined profile set name (e.g., backend-team, review-team)")

	// Interactive wizard flag
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Launch interactive wizard for agent configuration")

	// Session profile flag (bd-29kr): load saved spawn config
	cmd.Flags().StringVar(&sessionProfileName, "profile", "", "Load a saved session profile (see: ntm profile save)")

	// Register plugin flags dynamically
	// Note: We scan for plugins here to register flags.
	for _, p := range loadedPlugins {
		registerPluginAgentFlags(cmd, p, &agentSpecs)
	}

	return cmd
}

// registerPluginAgentFlags registers a plugin's --<name> (and optional
// --<alias>) agent-spec flags on cmd, skipping any that would collide with a
// flag that is already defined.
//
// Without this guard a user-installed agent plugin whose name or alias matches
// a built-in agent flag makes pflag panic with "flag redefined: <name>" while
// the command tree is constructed at process startup. The most common trigger
// is a leftover "oc" (Opencode) plugin from before Opencode became a
// first-class agent type: the static --oc flag and the plugin's --oc flag
// collide. Because the command tree is built in init(), that panic crashes
// *every* ntm invocation, not just `spawn`/`add` — and in particular it makes
// `ntm update` roll back, since the freshly installed binary aborts during
// post-install verification (see #200).
//
// Built-ins win: when a plugin name/alias already exists as a flag we keep the
// built-in and drop the colliding plugin flag with a warning rather than
// panicking.
func registerPluginAgentFlags(cmd *cobra.Command, p plugins.AgentPlugin, specs *AgentSpecs) {
	agentType := AgentType(p.Name)
	if cmd.Flags().Lookup(p.Name) == nil {
		cmd.Flags().Var(NewAgentSpecsValue(agentType, specs), p.Name, p.Description)
	} else {
		slog.Warn("skipping agent plugin flag that collides with an existing flag; keeping built-in",
			"plugin", p.Name, "flag", "--"+p.Name)
	}
	if p.Alias != "" {
		if cmd.Flags().Lookup(p.Alias) == nil {
			cmd.Flags().Var(NewAgentSpecsValue(agentType, specs), p.Alias, p.Description+" (alias)")
		} else {
			slog.Warn("skipping agent plugin alias that collides with an existing flag; keeping built-in",
				"plugin", p.Name, "alias", "--"+p.Alias)
		}
	}
}

func spawnSessionLogicContext(ctx context.Context, opts SpawnOptions) (err error) {
	return spawnSessionLogicContextWithOutput(ctx, opts, true)
}

// spawnSessionLogicComposable runs the complete spawn lifecycle without
// claiming stdout. It is used by commands such as resume that own a larger JSON
// envelope and must remain the sole encoder for that invocation.
func spawnSessionLogicComposable(ctx context.Context, opts SpawnOptions) error {
	return spawnSessionLogicContextWithOutput(ctx, opts, false)
}

func spawnSessionLogicContextWithOutput(ctx context.Context, opts SpawnOptions, emitOutput bool) (err error) {
	structuredOutput := IsJSONOutput() || !emitOutput
	// Deliberately shadow the process-level helper inside this legacy lifecycle:
	// composable callers need JSON's noninteractive control flow without
	// mutating global output state or redirecting process stdout.
	IsJSONOutput := func() bool { return structuredOutput }
	lifecycleSessionMayExist := false
	lifecyclePartialMutation := false
	lifecycleAffectedPaneIDs := []string{}
	lifecycleAffectedWorktreePaths := []string{}

	// Helper for JSON error output
	outputError := func(err error) error {
		if IsJSONOutput() {
			if !emitOutput {
				return err
			}
			response := newAgentLifecycleFailureResponse(
				err,
				opts.Session,
				lifecyclePartialMutation,
				lifecycleSessionMayExist,
				lifecycleAffectedPaneIDs,
				lifecycleAffectedWorktreePaths,
			)
			if encodeErr := output.PrintJSON(response); encodeErr != nil {
				return errors.Join(fmt.Errorf("encode spawn error response: %w", encodeErr), err)
			}
			return errors.Join(jsonFailureExit(), err)
		}
		return err
	}
	if ctx == nil {
		return outputError(errors.New("spawn requires a command context"))
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("spawn canceled: %w", err))
	}

	normalizeSpawnOptions(&opts)
	if err := validateSpawnStaggerOptions(opts); err != nil {
		return outputError(err)
	}
	if err := validateSpawnPaneEnv(opts.PaneEnv); err != nil {
		return outputError(err)
	}
	if err := validateSpawnAgentTypes(opts.Agents, opts.PluginMap, opts.PersonaMap); err != nil {
		return outputError(err)
	}
	if err := validateGrokPhaseOneSpawn(opts, cfg); err != nil {
		return outputError(err)
	}

	if err := tmux.ValidateSessionName(opts.Session); err != nil {
		return outputError(err)
	}

	// Calculate total agents before external preflight so malformed spawn
	// requests fail without consulting assignment tooling.
	var totalAgents int
	if len(opts.Agents) == 0 {
		totalAgents = legacySpawnTotalAgentCount(opts)
		if totalAgents == 0 {
			return outputError(fmt.Errorf("no agents specified (use --cc, --cod, --gmi, --agy, --grok, --cursor, --windsurf, --aider, --ollama or plugin flags)"))
		}
	} else {
		totalAgents = len(opts.Agents)
	}

	dir, err := resolveSpawnProjectDir(opts)
	if err != nil {
		return outputError(err)
	}
	if err := validateSpawnWorktreeOptions(opts); err != nil {
		return outputError(err)
	}
	if opts.Assign {
		admission, preflightErr := preflightSpawnAssignment(
			ctx,
			dir,
			opts.AssignTimeout,
			defaultSpawnAssignmentPreflightDependencies(),
		)
		if preflightErr != nil {
			return outputError(preflightErr)
		}
		opts.assignAdmission = admission
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	ollamaHost, err := preflightOllamaSpawnContext(ctx, opts)
	if err != nil {
		applied, fallbackMsg, fallbackErr := handleOllamaPreflightError(&opts, err)
		if fallbackErr != nil {
			return outputError(fallbackErr)
		}
		if applied && !IsJSONOutput() {
			output.PrintWarning(fallbackMsg)
		}
	}

	// Preflight every agent's launch command before creating worktrees,
	// sessions, or panes. A model/effort spec that the configured [agents]
	// command would silently drop must abort here, with nothing to clean up
	// (ntm-akaq).
	if err := validateSpawnAgentCommands(opts, ollamaHost); err != nil {
		return outputError(err)
	}

	// Safety check: fail if session already exists (when --safety is enabled)
	if opts.Safety {
		exists, err := tmux.SessionExistsContext(ctx, opts.Session)
		if err != nil {
			return outputError(fmt.Errorf("checking spawn safety for session %s: %w", opts.Session, err))
		}
		if exists {
			return outputError(spawnSafetySessionExistsError(opts.Session))
		}
	}

	// Codex/ChatGPT preflight: when spawning an explicit `gpt-*-codex` Codex
	// agent on a ChatGPT-billed login, advise about the "pane looks alive but
	// rejects the first prompt" failure mode some accounts hit (ntm#142). It is
	// only an advisory by default — that failure is not universal (ntm#155), so
	// we let the local Codex CLI be the source of truth and proceed. Returns an
	// error only in strict opt-in mode (NTM_CODEX_PREFLIGHT_STRICT).
	if err := preflightCodexAccountSupportContext(ctx, opts.Agents); err != nil {
		return outputError(err)
	}

	// Complete deterministic worktree admission before running hooks. Hooks are
	// user code and must not run when the requested checkout set is already
	// known to be invalid. CreateForAgent repeats the mutable checks after hooks
	// so a hook or concurrent process cannot invalidate this admission silently.
	if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
		if opts.UseWorktrees {
			return outputError(fmt.Errorf(
				"cannot create isolated agent worktrees.\n\n"+
					"Project: %s\n"+
					"Problem: this directory does not exist (projects_base resolution may not match your intended project location).\n\n"+
					"Worktree isolation requires an existing Git repository with at least one commit.\n\n"+
					"Suggested remediation:\n"+
					"  - If the project lives elsewhere, run ntm from that directory or fix projects_base in config.toml\n"+
					"  - Otherwise create and initialize it first:\n"+
					"      mkdir -p %s && cd %s && git init && git add . && git commit -m 'Initial commit'\n\n"+
					"Or rerun without --worktrees", dir, dir, dir))
		}
		switch {
		case IsJSONOutput() || opts.CreateDir:
			if err := os.MkdirAll(dir, 0755); err != nil {
				return outputError(fmt.Errorf("creating directory: %w", err))
			}
		case !isTTY() || os.Getenv("NTM_NONINTERACTIVE") != "":
			// Never block a non-interactive caller on a [y/N] prompt: a
			// scripted spawn with a mistyped session name must fail fast
			// with a decidable error (ntm-5ni5).
			return outputError(fmt.Errorf(
				"project directory %s does not exist; pass --create-dir to create it, or scaffold with 'ntm quick %s'",
				dir, opts.Session))
		default:
			fmt.Printf("Directory not found: %s\n", dir)
			if !confirm("Create it?") {
				fmt.Println("Aborted.")
				return nil
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return outputError(fmt.Errorf("creating directory: %w", err))
			}
			fmt.Printf("Created %s\n", dir)
		}
	} else if statErr != nil {
		return outputError(fmt.Errorf("inspect spawn project directory %s: %w", dir, statErr))
	}

	var worktreeManager *worktrees.WorktreeManager
	if opts.UseWorktrees {
		if err := preflightWorktreeProject(ctx, dir); err != nil {
			return outputError(err)
		}
		worktreeManager = worktrees.NewManager(dir, opts.Session)
		for _, agent := range opts.Agents {
			agentName := worktreeAgentName(agent, opts.WorktreeName)
			if err := worktreeManager.PreflightForAgent(ctx, agentName); err != nil {
				return outputError(fmt.Errorf("preflight worktree for agent %s: %w", agentName, err))
			}
		}
	}

	auditStart := time.Now()
	auditSessionCreated := false
	auditPanesAdded := 0
	auditAgentsLaunched := 0
	_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorUser, "session.spawn", map[string]interface{}{
		"phase":              "start",
		"session":            opts.Session,
		"total_agents":       totalAgents,
		"user_pane":          opts.UserPane,
		"recipe":             opts.RecipeName,
		"prompt_length":      len(opts.Prompt),
		"init_prompt_length": len(opts.InitPrompt),
		"stagger_mode":       opts.StaggerMode,
		"assign_enabled":     opts.Assign,
		"worktrees":          opts.UseWorktrees,
		"working_dir":        dir,
		"correlation_id":     auditCorrelationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":           "finish",
			"session":         opts.Session,
			"total_agents":    totalAgents,
			"session_created": auditSessionCreated,
			"panes_added":     auditPanesAdded,
			"agents_launched": auditAgentsLaunched,
			"success":         err == nil,
			"duration_ms":     time.Since(auditStart).Milliseconds(),
			"working_dir":     dir,
			"correlation_id":  auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(opts.Session, audit.EventTypeSpawn, audit.ActorUser, "session.spawn", payload, nil)
	}()

	testPacing, err := resolveSpawnTestPacing()
	if err != nil {
		return outputError(err)
	}

	// Initialize hook executor
	var hookExec *hooks.Executor
	if !opts.NoHooks {
		var err error
		hookExec, err = hooks.NewExecutorFromConfig()
		if err != nil {
			// Log warning but don't fail if hooks can't be loaded
			if !IsJSONOutput() {
				fmt.Printf("⚠ Warning: could not load hooks config: %v\n", err)
			}
			hookExec = hooks.NewExecutor(nil) // Use empty config
		}
	}

	// Build execution context for hooks
	hookCtx := hooks.ExecutionContext{
		SessionName:   opts.Session,
		ProjectDir:    dir,
		AdditionalEnv: spawnHookCountEnv(totalAgents, opts),
	}

	// Run pre-spawn hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreSpawn) {
		steps := output.NewSteps()
		if !IsJSONOutput() {
			steps.Start("Running pre-spawn hooks")
		}
		hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(hookRunCtx, hooks.EventPreSpawn, hookCtx)
		cancel()
		if err != nil {
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf("pre-spawn hook failed: %w", err))
		}
		if hooks.AnyFailed(results) {
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf("pre-spawn hook failed: %w", hooks.AllErrors(results)))
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	steps := output.NewSteps()
	// Snapshot the session's previous isolation mode BEFORE provisioning
	// worktrees: creating them below would make a shared→worktree transition
	// invisible to the build-slot lease reconciliation (ntm-83dz).
	preSpawnHadWorktrees := worktrees.SessionHasWorktrees(dir, opts.Session)

	provisionedWorktreePaths := make(map[string]string, len(opts.Agents))
	if opts.UseWorktrees {
		if !IsJSONOutput() {
			steps.Start("Creating Git worktrees for agent isolation")
		}
		for _, agent := range opts.Agents {
			if err := ctx.Err(); err != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("worktree creation canceled: %w", err))
			}
			agentName := worktreeAgentName(agent, opts.WorktreeName)
			worktreeInfo, createErr := worktreeManager.CreateForAgent(ctx, agentName)
			if worktreeInfo != nil && worktreeInfo.Created && strings.TrimSpace(worktreeInfo.Path) != "" {
				lifecyclePartialMutation = true
				lifecycleAffectedWorktreePaths = append(lifecycleAffectedWorktreePaths, worktreeInfo.Path)
			}
			if createErr != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("failed to create worktree for agent %s: %w", agentName, createErr))
			}
			if worktreeInfo == nil || strings.TrimSpace(worktreeInfo.Path) == "" {
				return outputError(fmt.Errorf("failed to create worktree for agent %s: manager returned no path", agentName))
			}
			provisionedWorktreePaths[agentName] = worktreeInfo.Path
		}
		// Revalidate the complete provisioned set before any tmux lifecycle
		// operation. This catches wrappers, hooks, or concurrent actors that
		// leave a checkout detached, stale, or on the wrong branch.
		for _, agent := range opts.Agents {
			agentName := worktreeAgentName(agent, opts.WorktreeName)
			worktreeInfo, lookupErr := worktreeManager.GetWorktreeForAgent(ctx, agentName)
			if lookupErr != nil {
				return outputError(fmt.Errorf("validate provisioned worktree for agent %s: %w", agentName, lookupErr))
			}
			expectedPath := provisionedWorktreePaths[agentName]
			if worktreeInfo == nil || !worktreeInfo.Created || worktreeInfo.Error != "" || worktreeInfo.Path != expectedPath {
				detail := "manager returned no worktree"
				if worktreeInfo != nil {
					detail = worktreeInfo.Error
					if detail == "" {
						detail = fmt.Sprintf("path %q does not match provisioned path %q", worktreeInfo.Path, expectedPath)
					}
				}
				return outputError(fmt.Errorf("validate provisioned worktree for agent %s: %s", agentName, detail))
			}
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	// Calculate total panes needed
	totalPanes := totalAgents
	if opts.UserPane {
		totalPanes++
	}

	// Create or use existing session only after every requested worktree is
	// ready. A worktree failure must not leave a partially created tmux session.
	sessionExists, err := tmux.SessionExistsContext(ctx, opts.Session)
	if err != nil {
		return outputError(fmt.Errorf("checking spawn session %s: %w", opts.Session, err))
	}
	if opts.Safety && sessionExists {
		lifecycleSessionMayExist = true
		return outputError(spawnSafetySessionExistsError(opts.Session))
	}
	if !sessionExists {
		if !IsJSONOutput() {
			steps.Start(fmt.Sprintf("Creating session '%s'", opts.Session))
		}
		historyLimit := tmux.DefaultHistoryLimit
		if cfg != nil && cfg.Tmux.HistoryLimit > 0 {
			historyLimit = cfg.Tmux.HistoryLimit
		}
		if err := tmux.CreateSessionWithHistoryLimitContext(ctx, opts.Session, dir, historyLimit); err != nil {
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if existsAfterError, probeErr := tmux.SessionExistsContext(probeCtx, opts.Session); probeErr == nil && existsAfterError {
				lifecycleSessionMayExist = true
				lifecyclePartialMutation = true
			}
			probeCancel()
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf("creating session: %w", err))
		}
		auditSessionCreated = true
		lifecyclePartialMutation = true
		if !IsJSONOutput() {
			steps.Done()
		}
	}
	lifecycleSessionMayExist = true

	// Make pane titles visible on stock tmux: pane-border-status defaults to
	// "off", which hides every title NTM sets. Session-local only, and an
	// existing user preference (top/bottom, local or inherited) is respected
	// (bd-ws7-docs-ux-truth-tqh3l.8). Best-effort: cosmetic failure must not
	// abort the spawn.
	if err := tmux.EnsurePaneBorderStatusContext(ctx, opts.Session); err != nil && !IsJSONOutput() {
		output.PrintWarningf("Could not enable pane-border-status: %v", err)
	}

	getPanesWithRetry := func(session string, attempts int, delay time.Duration) ([]tmux.Pane, error) {
		var lastErr error
		for i := 0; i < attempts; i++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("pane discovery canceled: %w", err)
			}
			panes, err := tmux.GetPanesContext(ctx, session)
			if err == nil {
				return panes, nil
			}
			lastErr = err
			if i == attempts-1 {
				break
			}
			msg := err.Error()
			if !strings.Contains(msg, "can't find window") && !strings.Contains(msg, "can't find session") {
				break
			}
			if err := waitContextDelay(ctx, delay); err != nil {
				return nil, fmt.Errorf("pane discovery retry canceled: %w", err)
			}
		}
		return nil, lastErr
	}

	// Get current pane count
	panes, err := getPanesWithRetry(opts.Session, 5, 100*time.Millisecond)
	if err != nil {
		return outputError(err)
	}
	// Creating a session also creates its first pane. Record that physical
	// mutation before recovery or prompt preparation can fail so JSON callers
	// receive the actual affected pane IDs even when no split was needed.
	if auditSessionCreated {
		for _, pane := range panes {
			if pane.ID != "" {
				lifecycleAffectedPaneIDs = append(lifecycleAffectedPaneIDs, pane.ID)
			}
		}
	}
	// If this spawn flips the session's worktree isolation mode, release
	// Agent Mail build-slot leases held by identities NTM registered whose
	// panes no longer exist (ntm-83dz). Best-effort: never blocks the spawn.
	reconcileBuildSlotLeasesOnSpawn(ctx, opts.Session, dir, preSpawnHadWorktrees, opts.UseWorktrees, panes)

	existingPanes := len(panes)
	paneInitDelay := time.Duration(cfg.Tmux.PaneInitDelayMs) * time.Millisecond
	if flag.Lookup("test.v") != nil {
		// Under `go test`, avoid the full init delay but keep a small floor to reduce
		// flakiness on busy tmux servers (pane IDs can transiently fail).
		const testPaneInitDelay = 50 * time.Millisecond
		if paneInitDelay > testPaneInitDelay {
			paneInitDelay = testPaneInitDelay
		}
	}
	panesAdded := 0

	// Add more panes if needed
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		panesAdded = toAdd
		auditPanesAdded = panesAdded
		if !IsJSONOutput() {
			steps.Start(fmt.Sprintf("Creating %d pane(s)", toAdd))
		}
		for i := 0; i < toAdd; i++ {
			if testPacing.paneDelay > 0 && i > 0 {
				if err := waitContextDelay(ctx, testPacing.paneDelay); err != nil {
					return outputError(fmt.Errorf("pane creation pacing canceled: %w", err))
				}
			}
			if err := ctx.Err(); err != nil {
				return outputError(fmt.Errorf("spawn canceled before splitting pane %d: %w", i+1, err))
			}
			paneID, err := tmux.SplitWindowContext(ctx, opts.Session, dir)
			if paneID != "" {
				lifecyclePartialMutation = true
				lifecycleAffectedPaneIDs = append(lifecycleAffectedPaneIDs, paneID)
			}
			if err != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("creating pane: %w", err))
			}
			if (testPacing.paneDelay > 0 || testPacing.agentDelay > 0) && !IsJSONOutput() {
				eventTime := time.Now().UTC()
				fmt.Printf("[E2E-SPAWN] event=pane_split session=%s seq=%d ts_ms=%d\n",
					opts.Session, i+1, eventTime.UnixMilli())
			}
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}
	if panesAdded > 0 && paneInitDelay > 0 {
		if !IsJSONOutput() {
			steps.Start("Waiting for panes to initialize")
		}
		if err := waitContextDelay(ctx, paneInitDelay); err != nil {
			return outputError(fmt.Errorf("pane initialization canceled: %w", err))
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	// Get updated pane list
	panes, err = getPanesWithRetry(opts.Session, 5, 100*time.Millisecond)
	if err != nil {
		return outputError(err)
	}

	// Assign panes in a deterministic order rather than relying on tmux
	// list-panes ordering, so agent[i] always lands on the same pane and
	// persona→pane assignment is reproducible for --profile-set spawns (ntm#149).
	sortPanesForAssignment(panes)

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if opts.UserPane {
		startIdx = 1
		// Title the reserved user pane. Without this the pane keeps the host's
		// default title, so the "which pane is mine?" label never exists
		// (bd-ws7-docs-ux-truth-tqh3l.8). Uses the canonical maker in
		// internal/config/label.go. Best-effort: a title failure is cosmetic.
		if len(panes) > 0 {
			if err := tmux.SetPaneTitleContext(ctx, panes[0].ID, config.UserPaneTitle(opts.Session)); err != nil {
				if !IsJSONOutput() {
					output.PrintWarningf("Could not title user pane: %v", err)
				}
			} else {
				lifecyclePartialMutation = true
				lifecycleAffectedPaneIDs = append(lifecycleAffectedPaneIDs, panes[0].ID)
			}
		}
	}
	if err := validateSpawnPaneCapacity(panes, startIdx, len(opts.Agents)); err != nil {
		return outputError(err)
	}
	if err := validateSpawnGrokPaneBaselines(panes, startIdx, opts.Agents); err != nil {
		return outputError(fmt.Errorf("validating Grok Build launch panes: %w", err))
	}

	agentNum := startIdx
	if !IsJSONOutput() {
		steps.Start(fmt.Sprintf("Launching %d agent(s)", len(opts.Agents)))
	}

	// Track launched agents for resilience monitor
	type launchedAgent struct {
		paneID        string
		paneIndex     int
		paneTitle     string // e.g., "myproject__cc_1"
		agentType     string
		model         string // alias
		resolvedModel string // full name
		persona       string // persona name when launched from --profile-set/--profiles (ntm#149)
		// personaPromptSource is the prepared system-prompt file path so the
		// spawn JSON output can surface *which* prompt source seeded each pane,
		// not just the persona's display name. Lets orchestrators verify the
		// persona→pane→prompt mapping after a --profile-set launch (ntm#159).
		personaPromptSource string
		command             string        // excludes --pane-env values; persisted for resilience restarts
		promptDelay         time.Duration // Stagger delay before prompt delivery
	}
	var launchedAgents []launchedAgent

	// Track agent index for stagger calculation (0-based, regardless of user pane)
	staggerAgentIdx := 0

	// Create spawn context for agent coordination (environment vars and prompt annotation)
	spawnCtx := NewSpawnContext(len(opts.Agents))

	// WaitGroup for staggered prompt delivery - ensures all prompts are sent before returning
	var setupWg sync.WaitGroup
	var maxStaggerDelay time.Duration
	setupCtx, cancelSetup := context.WithCancel(ctx)
	defer setupWg.Wait()
	defer cancelSetup()
	spawnObserver := newSpawnSessionObserver()
	spawnDispatcher := newCanonicalSpawnPromptDispatcher(opts.Session, spawnObserver)
	var setupErrorsMu sync.Mutex
	setupErrors := make(map[string]error)
	recordSetupError := func(paneID string, setupErr error) {
		if setupErr == nil {
			return
		}
		setupErrorsMu.Lock()
		setupErrors[paneID] = errors.Join(setupErrors[paneID], setupErr)
		setupErrorsMu.Unlock()
	}

	// Initialize rate limit tracker for smart stagger mode or Codex cooldown gating (bd-3qoly)
	var rateLimitTracker *ratelimit.RateLimitTracker
	hasCodex := opts.CodCount > 0
	if len(opts.Agents) > 0 {
		hasCodex = false
		for _, a := range opts.Agents {
			if a.Type == AgentTypeCodex {
				hasCodex = true
				break
			}
		}
	}
	if opts.StaggerMode == "smart" || hasCodex {
		rateLimitTracker = ratelimit.NewRateLimitTracker(dir)
		if err := rateLimitTracker.LoadFromDir(dir); err != nil {
			if !IsJSONOutput() {
				output.PrintWarningf("Failed to load rate limit history: %v", err)
			}
		}
	}

	// Determine effective stagger mode (new mode takes precedence over legacy)
	effectiveStaggerMode := resolveEffectiveStaggerMode(opts)

	// Spawn state for dashboard display (only used when stagger is enabled)
	var spawnState *SpawnState
	staggerInterval := resolveStaggerInterval(effectiveStaggerMode, opts, rateLimitTracker)
	if effectiveStaggerMode != "none" && effectiveStaggerMode != "" && spawnHasPromptDelivery(opts) {
		spawnState = NewSpawnState(spawnCtx.BatchID, int(staggerInterval.Seconds()), len(opts.Agents))
	}
	isStaggered := effectiveStaggerMode != "none" && effectiveStaggerMode != "" && staggerInterval > 0
	openAICooldownWaited := false

	// Resolve CASS context if enabled
	var cassContext string
	if !opts.NoCassContext && cfg.CASS.Context.Enabled && spawnHasAutomatedPromptDeliveryTarget(opts.Agents) {
		query := opts.CassContextQuery
		if query == "" {
			query = opts.Prompt // Use prompt if available
		}
		if query == "" && opts.RecipeName != "" {
			// Use recipe name as fallback context topic
			query = opts.RecipeName
		}

		if query != "" {
			cassResult, err := ResolveCassContextWithContext(ctx, query, dir)
			if err == nil {
				cassContext = cassResult
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("CASS context resolution canceled: %w", err))
	}

	// Build recovery context if enabled (smart session recovery)
	// Note: rc is kept as a pointer so we can format per-agent-type in the goroutines
	// Gated by --no-recovery flag (independent of --no-cass-context)
	var rc *RecoveryContext
	recoveryEnabled := !opts.NoRecovery && cfg.SessionRecovery.Enabled && cfg.SessionRecovery.AutoInjectOnSpawn && spawnHasAutomatedPromptDeliveryTarget(opts.Agents)
	if recoveryEnabled {
		recoveryCtx, cancelCtx := context.WithTimeout(ctx, cfg.SessionRecovery.GetTimeout())
		var recoveryErr error
		rc, recoveryErr = buildRecoveryContext(recoveryCtx, opts.Session, dir, cfg.SessionRecovery)
		cancelCtx()
		if recoveryErr != nil {
			return outputError(fmt.Errorf("spawn recovery canceled: %w", recoveryErr))
		}
		if rc != nil {
			// Check if there's meaningful content by testing with a dummy type
			if FormatRecoveryPrompt(rc, AgentTypeClaude) != "" {
				if !IsJSONOutput() {
					fmt.Println("✓ Recovery context prepared for session")
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("spawn recovery canceled: %w", err))
	}

	// Spawn-scoped Agent Mail identity coordinator (gh#255): each pane's
	// identity is prepared and published immediately before its agent command
	// is sent, so the process can resolve its assigned name at its first
	// instruction instead of racing the post-launch registration that used to
	// run here. Lazily initialized — no Agent Mail traffic when registration
	// is disabled or no agents launch.
	identityCoordinator := newSpawnIdentityCoordinator(dir, opts.Session)

	// Launch agents using flattened specs (preserves model info for pane naming)
	for _, agent := range opts.Agents {
		if agentNum >= len(panes) {
			return outputError(fmt.Errorf(
				"spawn pane assignment invariant failed for agent %q index %d: pane offset %d exceeds %d discovered panes",
				agent.Type, agent.Index, agentNum, len(panes),
			))
		}

		pane := panes[agentNum]
		if agent.Type == AgentTypeGrok {
			if err := tmux.ValidatePaneLaunchBaseline(pane); err != nil {
				return outputError(fmt.Errorf("launching %s agent: %w", agent.Type, err))
			}
		}

		if testPacing.agentDelay > 0 && staggerAgentIdx > 0 {
			if err := waitContextDelay(ctx, testPacing.agentDelay); err != nil {
				return outputError(fmt.Errorf("agent launch pacing canceled: %w", err))
			}
		}
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("spawn canceled before configuring agent %d: %w", agent.Index, err))
		}

		// Format pane title with optional model variant
		// Format: {session}__{type}_{index} or {session}__{type}_{index}_{variant}
		// The reasoning effort is folded into the variant once it is resolved,
		// further down — see the bd-qs6rj comment there.
		title := tmux.FormatPaneName(opts.Session, string(agent.Type), agent.Index, agent.Model)
		if err := tmux.SetPaneAgentIdentityContext(ctx, pane.ID, title, tmux.AgentType(agent.Type)); err != nil {
			return outputError(fmt.Errorf("setting pane identity: %w", err))
		}
		lifecyclePartialMutation = true
		lifecycleAffectedPaneIDs = append(lifecycleAffectedPaneIDs, pane.ID)

		// Get agent command template based on type
		agentCmdTemplate, envVars, err := spawnAgentCommandTemplate(agent.Type, opts.PluginMap, ollamaHost)
		if err != nil {
			return outputError(err)
		}

		// Configure Claude hooks for DCG and RCH integrations
		if agent.Type == AgentTypeClaude {
			var preToolHooks []dcg.HookEntry
			var hookSources []string

			if cfg.Integrations.DCG.Enabled && dcg.ShouldConfigureHooks(cfg.Integrations.DCG.Enabled, cfg.Integrations.DCG.BinaryPath) {
				dcgOpts := dcg.DCGHookOptions{
					BinaryPath:      cfg.Integrations.DCG.BinaryPath,
					AuditLog:        cfg.Integrations.DCG.AuditLog,
					Timeout:         5,
					CustomBlocklist: cfg.Integrations.DCG.CustomBlocklist,
					CustomWhitelist: cfg.Integrations.DCG.CustomWhitelist,
				}
				dcgConfig, err := dcg.GenerateHookConfig(dcgOpts)
				if err == nil {
					preToolHooks = append(preToolHooks, dcgConfig.Hooks.PreToolUse...)
					hookSources = append(hookSources, "dcg")
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure DCG hooks for agent %d: %v", agent.Index, err)
				}
			}

			if dcg.ShouldConfigureRCHHooks(cfg.Integrations.RCH.Enabled, cfg.Integrations.RCH.InterceptPatterns) {
				rchHook, err := dcg.GenerateRCHHookEntry(dcg.RCHHookOptions{
					BinaryPath: cfg.Integrations.RCH.BinaryPath,
					Patterns:   cfg.Integrations.RCH.InterceptPatterns,
					Timeout:    5,
				})
				if err == nil {
					preToolHooks = append(preToolHooks, rchHook)
					hookSources = append(hookSources, "rch")
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure RCH hooks for agent %d: %v", agent.Index, err)
				}
			}

			if len(preToolHooks) > 0 {
				hookConfig := dcg.ClaudeHookConfig{
					Hooks: dcg.HooksSection{
						PreToolUse: preToolHooks,
					},
				}
				hookJSON, err := json.Marshal(hookConfig)
				if err == nil {
					if envVars == nil {
						envVars = make(map[string]string)
					}
					envVars["CLAUDE_CODE_HOOKS"] = string(hookJSON)
					if !IsJSONOutput() {
						output.PrintInfof("Claude hooks configured for agent %d (%s)", agent.Index, strings.Join(hookSources, ", "))
					}
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure Claude hooks for agent %d: %v", agent.Index, err)
				}
			}
		}

		// Resolve model alias to full model name (falling back to the plugin's
		// declared default for bare plugin specs — see resolveAgentModel).
		resolvedModel := resolveAgentModel(agent.Type, agent.Model, opts.PluginMap)
		modelRequested := strings.TrimSpace(agent.Model) != ""
		if agent.Type == AgentTypeOllama && resolvedModel == "" {
			resolvedModel = strings.TrimSpace(opts.LocalModel)
			if resolvedModel == "" {
				resolvedModel = "codellama:latest"
			}
		}

		// Check if this is a persona agent and prepare system prompt.
		// The model-keyed PersonaMap (recipe/quick-spawn personas) is skipped
		// when this agent already carries a persona from --profile-set/--profiles
		// expansion (handled below), so the two persona sources never overlap.
		var systemPromptFile string
		var personaName string
		// Reasoning effort resolves from the persona when this agent carries one,
		// otherwise from the (direct-spec) value already on the FlatAgent. The
		// persona's setting always wins so `reasoning_effort` in personas.toml is
		// honored for both PersonaMap and --profile-set spawns (ntm#171).
		resolvedReasoningEffort := agent.ReasoningEffort
		if agent.Persona == nil && opts.PersonaMap != nil {
			if p, ok := opts.PersonaMap[agent.Model]; ok {
				personaName = p.Name
				modelRequested = strings.TrimSpace(p.Model) != ""
				if strings.TrimSpace(p.ReasoningEffort) != "" {
					resolvedReasoningEffort = p.ReasoningEffort
				}
				// Prepare system prompt file
				promptFile, err := prepareRequiredPersonaSystemPrompt(p, dir)
				if err != nil {
					return outputError(fmt.Errorf(
						"preparing system prompt for persona %s after configuring pane %s: %w; the session and pane still exist",
						p.Name, pane.ID, err,
					))
				}
				systemPromptFile = promptFile
				// For persona agents, resolve the model from the persona config
				resolvedModel = resolveAgentModel(agent.Type, p.Model, opts.PluginMap)
			}
		}

		// Persona attached during --profile-set/--profiles expansion (ntm#149).
		// agent.Type already reflects the persona's own agent_type, so the
		// command template selected above launches the right CLI. This is the
		// counterpart to the PersonaMap branch above (the two are mutually
		// exclusive: PersonaMap is skipped whenever agent.Persona is set).
		if agent.Persona != nil {
			profile := agent.Persona
			personaName = profile.Name
			if strings.TrimSpace(profile.Model) != "" {
				modelRequested = true
				resolvedModel = resolveAgentModel(agent.Type, profile.Model, opts.PluginMap)
			}
			if strings.TrimSpace(profile.ReasoningEffort) != "" {
				resolvedReasoningEffort = profile.ReasoningEffort
			}
			// Prepare system prompt file for the profile
			promptFile, err := prepareRequiredPersonaSystemPrompt(profile, dir)
			if err != nil {
				return outputError(fmt.Errorf(
					"preparing system prompt for profile %s after configuring pane %s: %w; the session and pane still exist",
					profile.Name, pane.ID, err,
				))
			}
			systemPromptFile = promptFile
			if !IsJSONOutput() {
				fmt.Printf("  → persona '%s' → pane %s_%d\n", profile.Name, agent.Type, agent.Index)
			}
		}

		// Advisory model did-you-mean (bd-uh7la item 6): an explicitly
		// requested model that resolves to an ID the model registry doesn't
		// know, but that sits a small edit away from a known registry ID, is
		// almost always a typo. Warn with the suggestion and proceed so
		// custom/self-hosted model IDs keep working.
		if modelRequested && agent.Type != AgentTypeAntigravity && agent.Type != AgentTypeOllama && !IsJSONOutput() {
			if suggestion := models.SuggestModel(resolvedModel); suggestion != "" {
				output.PrintWarningf("model %q is not in the model registry; did you mean %q? (spawning with the requested model)",
					resolvedModel, suggestion)
			}
		}

		// Settle the pane title now that model, persona, and reasoning effort
		// are all resolved. The variant carries model AND effort, because the
		// title is the only record of the launch spec that survives into a
		// respawn: encoding just the model let a recovery silently relaunch the
		// pane on the config DEFAULT effort, with no operator signal that the
		// swarm's reasoning budget had changed underneath them (bd-qs6rj).
		// A persona name replaces the model in the variant, as before.
		titleVariant := tmux.FormatPaneVariant(agent.Model, resolvedReasoningEffort)
		if personaName != "" {
			titleVariant = personaName
		}
		if finalTitle := tmux.FormatPaneName(opts.Session, string(agent.Type), agent.Index, titleVariant); finalTitle != title {
			title = finalTitle
			if err := tmux.SetPaneTitleContext(ctx, pane.ID, title); err != nil {
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: could not update pane title: %v\n", err)
				}
			}
		}

		// Generate command using template
		agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
			Model:            resolvedModel,
			ModelAlias:       agent.Model,
			ModelRequested:   modelRequested,
			SessionName:      opts.Session,
			PaneIndex:        agent.Index,
			AgentType:        string(agent.Type),
			ProjectDir:       dir,
			SystemPromptFile: systemPromptFile,
			PersonaName:      personaName,
			ReasoningEffort:  resolvedReasoningEffort,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Per-pane Claude credential isolation (GH#237). Claude Code rewrites
		// the shared ~/.claude/.credentials.json on every OAuth refresh, so N
		// panes on one subscription invalidate each other's refresh token and
		// 401 in cascade. Opt-in via [agents] claude_isolate_credentials.
		var claudeEnv swarm.ClaudeLaunchEnv
		if agent.Type == AgentTypeClaude {
			claudeEnv, err = swarm.ProvisionClaudeIsolation(cfg, dir, opts.Session, agent.Index)
			if err != nil {
				return outputError(fmt.Errorf("isolating credentials for claude pane %d: %w", agent.Index, err))
			}
		}

		// Credential isolation is applied FIRST so its assignments end up
		// RIGHTMOST, closest to the command. In `A=1 A=2 cmd` the shell keeps
		// the LAST assignment, so rightmost wins — applying isolation last
		// would have put it leftmost and let a plugin env var named
		// CLAUDE_CONFIG_DIR silently defeat it.
		agentCmd = claudeEnv.ApplyToCommand(agentCmd)

		// A worktree pane runs with a cwd whose derived project key differs
		// from the session key Agent Mail registered, so tell the agent's
		// tooling which key NTM published its identity under (ntm#257).
		// AGENT_MAIL_PROJECT is the variable the Agent Mail CLI already honours
		// ahead of cwd derivation. A plugin env var or an explicit --pane-env
		// value of the same name wins.
		if opts.UseWorktrees {
			if _, fromPlugin := envVars["AGENT_MAIL_PROJECT"]; !fromPlugin {
				if _, fromUser := opts.PaneEnv["AGENT_MAIL_PROJECT"]; !fromUser {
					if envVars == nil {
						envVars = make(map[string]string)
					}
					envVars["AGENT_MAIL_PROJECT"] = dir
				}
			}
		}

		// Apply plugin env vars if any
		if len(envVars) > 0 {
			var envPrefix string
			for k, v := range envVars {
				envPrefix += fmt.Sprintf("%s=%s ", k, tmux.ShellQuote(v))
			}
			agentCmd = envPrefix + agentCmd
		}

		// The resilience manifest is durable and must never carry arbitrary
		// --pane-env values. Keep the restart command before the ephemeral pane
		// environment prefix; a restarted pane therefore requires its caller to
		// provide sensitive values again rather than recovering them from disk.
		manifestAgentCmd, err := tmux.SanitizePaneCommand(agentCmd)
		if err != nil {
			return outputError(fmt.Errorf("invalid %s resilience command: %w", agent.Type, err))
		}

		paneEnv := expandSpawnPaneEnv(opts.PaneEnv, dir, agent.Index, agent.Type)
		agentCmd = prependSpawnPaneEnv(agentCmd, paneEnv)

		// Calculate stagger delay for this agent (used for spawn context)
		var promptDelay time.Duration
		if isStaggered {
			promptDelay = time.Duration(staggerAgentIdx) * staggerInterval
		}

		// Create agent-specific spawn context with order (1-based) and stagger delay
		agentSpawnCtx := spawnCtx.ForAgent(staggerAgentIdx+1, promptDelay)

		// Apply spawn context environment variables
		// These allow agents to programmatically access their spawn position
		agentCmd = agentSpawnCtx.EnvVarPrefix() + agentCmd

		safeAgentCmd, err := tmux.SanitizePaneCommand(agentCmd)
		if err != nil {
			return outputError(fmt.Errorf("invalid %s agent command: %w", agent.Type, err))
		}

		if agent.Type == AgentTypeCodex {
			var cooldown time.Duration
			cooldown, openAICooldownWaited = codexCooldownRemaining(rateLimitTracker, openAICooldownWaited)
			if cooldown > 0 {
				if !IsJSONOutput() {
					output.PrintWarningf("Codex cooldown active; waiting %s before launching", ratelimit.FormatDelay(cooldown))
				}
				if err := waitContextDelay(ctx, cooldown); err != nil {
					return outputError(fmt.Errorf("codex cooldown canceled before launching agent %d: %w", agent.Index, err))
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("spawn canceled before launching agent %d: %w", agent.Index, err))
		}

		// Resolve the launch directory at the final command-construction
		// boundary. Worktree isolation must never degrade to the shared project
		// directory when a checkout disappears, detaches, or changes branch.
		workingDir := dir
		if opts.UseWorktrees {
			if worktreeManager == nil {
				return outputError(fmt.Errorf("building %s agent command: worktree manager is unavailable", agent.Type))
			}
			agentName := worktreeAgentName(agent, opts.WorktreeName)
			expectedPath, provisioned := provisionedWorktreePaths[agentName]
			if !provisioned || strings.TrimSpace(expectedPath) == "" {
				return outputError(fmt.Errorf("building %s agent command: no provisioned worktree for agent %s", agent.Type, agentName))
			}
			worktreeInfo, lookupErr := worktreeManager.GetWorktreeForAgent(ctx, agentName)
			if lookupErr != nil {
				return outputError(fmt.Errorf("building %s agent command: validate worktree for agent %s: %w", agent.Type, agentName, lookupErr))
			}
			if worktreeInfo == nil || !worktreeInfo.Created || worktreeInfo.Error != "" || worktreeInfo.Path != expectedPath {
				detail := "manager returned no worktree"
				if worktreeInfo != nil {
					detail = worktreeInfo.Error
					if detail == "" {
						detail = fmt.Sprintf("path %q does not match provisioned path %q", worktreeInfo.Path, expectedPath)
					}
				}
				return outputError(fmt.Errorf("building %s agent command: worktree for agent %s is not launchable: %s", agent.Type, agentName, detail))
			}
			workingDir = expectedPath
		}

		cmd, err := tmux.BuildPaneCommand(workingDir, safeAgentCmd)
		if err != nil {
			return outputError(fmt.Errorf("building %s agent command: %w", agent.Type, err))
		}

		// Publish this pane's Agent Mail identity BEFORE launching the agent
		// so a process that resolves its identity during startup reads its
		// assigned name, not a previous occupant's (gh#255). Failures degrade
		// gracefully and never block the launch. The registration key stays
		// `dir` (the historical project key); a worktree pane's identity is
		// additionally published under its own directory so cwd-derived
		// resolution inside the worktree finds it too (ntm#257).
		paneDir := ""
		if workingDir != dir {
			paneDir = workingDir
		}
		identityCoordinator.prepareAgent(ctx, spawnedAgentInfo{
			paneIndex:     pane.Index,
			paneID:        pane.ID,
			paneTitle:     title,
			paneDir:       paneDir,
			agentType:     string(agent.Type),
			model:         agent.Model,
			resolvedModel: resolvedModel,
		})

		if err := tmux.SendKeysContext(ctx, pane.ID, cmd, true); err != nil {
			launchErr := fmt.Errorf(
				"launching %s agent in pane %s: %w; the session and pane still exist",
				agent.Type, pane.ID, err,
			)
			if personaName != "" {
				launchErr = newPromptSendFailure(fmt.Errorf("sending persona/profile %s launch prompt: %w", personaName, launchErr))
			}
			return outputError(launchErr)
		}
		if agent.Type == AgentTypeGrok {
			if _, err := tmux.WaitForPaneProcessStartContext(ctx, opts.Session, pane.ID); err != nil {
				return outputError(fmt.Errorf(
					"launching %s agent in pane %s did not start a stable process: %w",
					agent.Type, pane.ID, err,
				))
			}
		}
		if rateLimitTracker != nil && agent.Type == AgentTypeCodex {
			rateLimitTracker.RecordSuccess("openai")
			if err := rateLimitTracker.SaveToDir(dir); err != nil && !IsJSONOutput() {
				output.PrintWarningf("Failed to persist rate limit history: %v", err)
			}
		}

		// Parallelize post-launch setup and prompt delivery
		// This prevents sequential blocking and ensures correct ordering (Context -> Prompt)
		setupWg.Add(1)

		panePrompt, promptResolveErr := resolveSpawnPanePrompt(opts, agent.Type, staggerAgentIdx)
		if promptResolveErr != nil {
			recordSetupError(pane.ID, fmt.Errorf(
				"agent %d (%s) default prompt resolution: %w",
				agent.Index, agent.Type, promptResolveErr,
			))
		}
		hasPrompt := panePrompt != ""

		// Capture vars for closure
		pID := pane.ID
		pTitle := title
		idx := agent.Index

		go func(paneID, paneTitle string, idx int, agentType AgentType, agent FlatAgent, panePrompt string, hasPrompt bool) {
			defer setupWg.Done()
			// Gemini post-spawn setup: auto-select Pro model
			if agentType == AgentTypeGemini && cfg.GeminiSetup.AutoSelectProModel {
				geminiCfg := gemini.SetupConfig{
					AutoSelectProModel: cfg.GeminiSetup.AutoSelectProModel,
					ReadyTimeout:       time.Duration(cfg.GeminiSetup.ReadyTimeoutSeconds) * time.Second,
					ModelSelectTimeout: time.Duration(cfg.GeminiSetup.ModelSelectTimeoutSeconds) * time.Second,
					PollInterval:       500 * time.Millisecond,
					Verbose:            cfg.GeminiSetup.Verbose,
				}
				setupCtx, setupCancel := context.WithTimeout(setupCtx, geminiCfg.ReadyTimeout+geminiCfg.ModelSelectTimeout+10*time.Second)
				defer setupCancel()
				if err := gemini.PostSpawnSetup(setupCtx, paneID, geminiCfg); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: Gemini Pro model setup failed for agent %d: %v\n", idx, err)
						fmt.Printf("  (Agent is running with default model. To disable auto-setup: set gemini_setup.auto_select_pro_model = false in config)\n")
					}
					// Don't fail spawn - agent is still running, just possibly with default model
				} else {
					if !IsJSONOutput() && cfg.GeminiSetup.Verbose {
						fmt.Printf("✓ Gemini %d configured for Pro model\n", idx)
					}
				}
			}

			recoveryPrompt := ""
			if rc != nil {
				// Format per agent type so provider-specific escaping remains
				// deterministic before final-message redaction.
				recoveryPrompt = FormatRecoveryPrompt(rc, agentType)
			}
			userDelay := time.Duration(0)
			if isStaggered && panePrompt != "" {
				userDelay = promptDelay
			}
			promptSteps := buildSpawnPromptSequenceForAgent(agentType, cassContext, recoveryPrompt, panePrompt, userDelay)
			if isStaggered {
				for stepIndex := range promptSteps {
					if promptSteps[stepIndex].Kind == "user_prompt" {
						promptSteps[stepIndex].Message = agentSpawnCtx.AnnotatePrompt(promptSteps[stepIndex].Message, true)
					}
				}
			}
			_, promptErr := dispatchSpawnPromptSequence(
				setupCtx, opts.Session, paneID, promptSteps,
				spawnObserver, spawnDispatcher, spawnPromptReadyTimeout, spawnReadyPollInterval,
			)
			if promptErr != nil {
				if spawnPromptSequenceIsCassOnly(promptSteps) {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to inject CASS context for agent %d: %v\n", idx, promptErr)
					}
					return
				}
				recordSetupError(paneID, newPromptSendFailure(fmt.Errorf("agent %d (%s): %w", idx, agentType, promptErr)))
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: spawn prompt delivery failed for agent %d: %v\n", idx, promptErr)
				}
				return
			}

			// Update spawn state only after the user prompt has a canonical
			// delivered receipt. Context-only sequences are not scheduled there.
			if isStaggered && hasPrompt && spawnState != nil {
				spawnState.MarkSent(paneID)
				if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
					fmt.Printf("⚠ Warning: failed to update spawn state: %v\n", err)
				}
			}
		}(pID, pTitle, idx, agent.Type, agent, panePrompt, hasPrompt)

		// Schedule staggered prompt delivery in spawn state (Main Thread)
		if isStaggered && hasPrompt {
			scheduledAt := time.Now().Add(promptDelay)

			// Add to spawn state for dashboard display
			if spawnState != nil {
				spawnState.AddPrompt(pTitle, pID, staggerAgentIdx+1, scheduledAt)
			}

			// Track max delay for the final wait message
			if promptDelay > maxStaggerDelay {
				maxStaggerDelay = promptDelay
			}
			if !IsJSONOutput() {
				fmt.Printf("  → Agent %d prompt scheduled in %v\n", staggerAgentIdx+1, promptDelay)
			}
		}

		// Track for resilience monitor
		launchedAgents = append(launchedAgents, launchedAgent{
			paneID:              pane.ID,
			paneIndex:           pane.Index,
			paneTitle:           title,
			agentType:           string(agent.Type),
			model:               agent.Model,
			resolvedModel:       resolvedModel,
			persona:             personaName,
			personaPromptSource: systemPromptFile,
			command:             manifestAgentCmd,
			promptDelay:         promptDelay,
		})
		auditAgentsLaunched = len(launchedAgents)

		staggerAgentIdx++
		agentNum++
	}

	// Complete the launching step
	if !IsJSONOutput() {
		steps.Done()
	}

	// Save initial spawn state for dashboard display
	if spawnState != nil {
		if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
			fmt.Printf("⚠ Warning: failed to save spawn state: %v\n", err)
		}
	}

	// Start session monitor (handles resilience and daemons)
	// Always started regardless of auto-restart config
	// Note: Started BEFORE waiting for staggered prompts so that resilience is active
	// even if the user interrupts the wait.
	// Manifest construction + monitor launch go through the ONE shared code
	// path (resilience.StartSessionMonitor) also used by robot spawn
	// (WS0-G6 single-definition contract, bd-ws1-truth-safety-l5ddi.8).
	monitorAgents := make([]resilience.AgentConfig, 0, len(launchedAgents))
	for _, agent := range launchedAgents {
		monitorAgents = append(monitorAgents, resilience.AgentConfig{
			PaneID:    agent.paneID,
			PaneIndex: agent.paneIndex,
			Type:      agent.agentType,
			Model:     agent.model,
			Command:   agent.command,
		})
	}
	monitorResult, monitorErr := resilience.StartSessionMonitor(ctx, resilience.SpawnMonitorRequest{
		Session:     opts.Session,
		ProjectDir:  dir,
		AutoRestart: opts.AutoRestart || cfg.Resilience.AutoRestart,
		Agents:      monitorAgents,
	})
	switch {
	case monitorErr == nil:
		if !IsJSONOutput() && monitorResult != nil {
			if monitorResult.Manifest != nil && monitorResult.Manifest.AutoRestart {
				output.PrintInfof("Session monitor started (auto-restart enabled, pid: %d)", monitorResult.MonitorPID)
			} else {
				output.PrintInfof("Session monitor started (pid: %d)", monitorResult.MonitorPID)
			}
		}
	case errors.Is(monitorErr, resilience.ErrInternalMonitorDisabled):
		// Disabled by env/test guard: silent, matching prior behavior.
	case ctx.Err() != nil:
		return outputError(fmt.Errorf("session monitor replacement canceled: %w", monitorErr))
	default:
		if !IsJSONOutput() {
			output.PrintWarningf("Failed to start session monitor: %v", monitorErr)
		}
	}

	// Set up signal handling for graceful interruption during stagger wait.
	// Return an error instead of calling os.Exit so deferred audit and cleanup still run.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Wait for staggered prompt delivery to complete (if any)
	if maxStaggerDelay > 0 {
		if !IsJSONOutput() {
			fmt.Printf("⏳ Waiting for staggered prompts (max %v)...\n", maxStaggerDelay)
		}
	}

	setupDone := make(chan struct{})
	go func() {
		setupWg.Wait()
		close(setupDone)
	}()
	if err := waitForSpawnPromptWorkers(ctx, setupDone, sigChan, IsJSONOutput(), cancelSetup); err != nil {
		return outputError(err)
	}
	setupErrorsMu.Lock()
	failedPaneIDs := make([]string, 0, len(setupErrors))
	for paneID := range setupErrors {
		failedPaneIDs = append(failedPaneIDs, paneID)
	}
	sort.Strings(failedPaneIDs)
	setupErrorMessages := make([]string, 0, len(failedPaneIDs))
	setupErrorList := make([]error, 0, len(failedPaneIDs))
	for _, paneID := range failedPaneIDs {
		setupErrorMessages = append(setupErrorMessages, fmt.Sprintf("pane %s: %v", paneID, setupErrors[paneID]))
		setupErrorList = append(setupErrorList, setupErrors[paneID])
	}
	setupErrorsMu.Unlock()
	if len(setupErrorMessages) > 0 {
		return outputError(fmt.Errorf(
			"spawn prompt setup failed: %s: %w; the session and affected panes still exist",
			strings.Join(setupErrorMessages, "; "), errors.Join(setupErrorList...),
		))
	}

	if maxStaggerDelay > 0 {
		if !IsJSONOutput() {
			fmt.Println("✓ All staggered prompts delivered")
		}
		// Clean up spawn state file now that all prompts are sent
		if spawnState != nil {
			spawnState.MarkComplete()
			if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
				fmt.Printf("⚠ Warning: failed to save final spawn state: %v\n", err)
			}
		}
	}

	// Get final pane list for output
	finalPanes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		return outputError(fmt.Errorf("getting final spawn panes: %w", err))
	}
	// Order deterministically so the emitted panes array (and the post-launch
	// persona→pane mapping) is reproducible across runs rather than dependent
	// on tmux list-panes ordering (ntm#149).
	sortPanesForAssignment(finalPanes)

	// Enable project webhooks (if configured) for this session and emit
	// webhook-compatible lifecycle events (always, regardless of JSON mode).
	//
	// Note: This is best-effort and should never fail the spawn command.
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, err := webhook.StartBridgeFromProjectConfig(dir, opts.Session, events.DefaultBus, &redactCfg)
		if err != nil {
			slog.Default().Debug("webhook bridge init failed", "session", opts.Session, "error", err)
		} else if bridge != nil {
			defer bridge.Close()
		}
	}

	events.DefaultEmitter().Emit(events.NewWebhookEvent(
		events.WebhookSessionCreated,
		opts.Session,
		"",
		"",
		fmt.Sprintf("Session %s created", opts.Session),
		spawnSessionCreatedEventFields(opts, dir),
	))

	for _, agent := range launchedAgents {
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookAgentStarted,
			opts.Session,
			agent.paneID,
			agent.agentType,
			fmt.Sprintf("Agent started (%s)", agent.agentType),
			map[string]string{
				"project_dir":    dir,
				"pane_index":     fmt.Sprintf("%d", agent.paneIndex),
				"pane_title":     agent.paneTitle,
				"model":          agent.model,
				"resolved_model": agent.resolvedModel,
			},
		))
	}

	// Emit analytics events (JSONL) for session creation and agent spawns.
	events.EmitSessionCreate(opts.Session, events.SessionCreateData{
		ClaudeCount:      opts.CCCount,
		CodexCount:       opts.CodCount,
		GeminiCount:      opts.GmiCount,
		AntigravityCount: opts.AgyCount,
		GrokCount:        opts.GrokCount,
		CursorCount:      opts.CursorCount,
		WindsurfCount:    opts.WindsurfCount,
		AiderCount:       opts.AiderCount,
		OpencodeCount:    opts.OpencodeCount,
		OllamaCount:      opts.OllamaCount,
		WorkDir:          dir,
		Recipe:           opts.RecipeName,
	})
	for _, agent := range launchedAgents {
		events.Emit(events.EventAgentSpawn, opts.Session, events.AgentSpawnData{
			AgentType: agent.agentType,
			Model:     agent.resolvedModel,
			Variant:   agent.model,
			PaneIndex: agent.paneIndex,
		})
	}

	// --verify-boot: block until every launched agent reaches a ready state
	// or fail loudly naming what did not boot (ntm-mr4k / OC-038). A wrong
	// model default can brick every pane while spawn exits 0; verification
	// turns that 10-minute diagnosis into an immediate structured error.
	if opts.VerifyBoot && len(opts.Agents) > 0 {
		if !IsJSONOutput() {
			steps.Start(fmt.Sprintf("Verifying %d agent(s) reached a working prompt", len(opts.Agents)))
		}
		readyCount, bootErr := waitForAgentsReadyWithObserver(
			ctx, opts.Session, spawnVerifyBootTimeout, spawnReadyPollInterval, spawnObserver,
		)
		if bootErr != nil {
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf(
				"spawn --verify-boot: %d/%d agent(s) ready within %s: %w; the session and panes still exist — inspect with --robot-tail",
				readyCount, len(opts.Agents), spawnVerifyBootTimeout, bootErr))
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	// JSON output mode
	if IsJSONOutput() {
		// Build maps keyed by stable tmux pane ID. Window-local indices can repeat,
		// so they cannot safely carry persona or stagger attribution in a
		// multi-window session. The persona map yields the deterministic persona→pane
		// mapping orchestrators need after a --profile-set launch (ntm#149).
		paneDelays := make(map[string]time.Duration)
		panePersonas := make(map[string]string)
		panePersonaPromptSources := make(map[string]string)
		for _, agent := range launchedAgents {
			paneDelays[agent.paneID] = agent.promptDelay
			if agent.persona != "" {
				panePersonas[agent.paneID] = agent.persona
			}
			if agent.personaPromptSource != "" {
				panePersonaPromptSources[agent.paneID] = agent.personaPromptSource
			}
		}

		paneResponses := make([]output.PaneResponse, len(finalPanes))
		agentCounts := output.AgentCountsResponse{}
		for i, p := range finalPanes {
			paneResponses[i] = paneResponseFromTMUX(p)
			paneResponses[i].Persona = panePersonas[p.ID]
			paneResponses[i].PersonaPromptSource = panePersonaPromptSources[p.ID]
			paneResponses[i].PromptDelayMs = paneDelays[p.ID].Milliseconds()
			incrementAgentCounts(&agentCounts, p.Type)
		}

		// Build stagger config if enabled
		var staggerCfg *output.StaggerConfig
		if opts.StaggerEnabled {
			staggerCfg = &output.StaggerConfig{
				Enabled:    true,
				IntervalMs: opts.Stagger.Milliseconds(),
			}
		}

		// Register session coordinator with Agent Mail (creates agent.json for ntm lock)
		registerSessionAgent(ctx, opts.Session, dir)

		// Agent Mail identities were prepared and published per-pane before
		// each launch (gh#255); assemble the accumulated status for output.
		agentMailStatus := identityCoordinator.finalStatus()
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("spawn registration canceled: %w", err))
		}

		spawnResponse := &output.SpawnResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             opts.Session,
			Created:             true, // spawn always creates or reuses
			WorkingDirectory:    dir,
			Panes:               paneResponses,
			AgentCounts:         agentCounts,
			Stagger:             staggerCfg,
			AgentMail:           agentMailStatus,
			Recovery:            newRecoverySpawnStatus(recoveryEnabled, rc),
			ProfileSet:          opts.ProfileSetName,
		}

		// If assignment is enabled, wait for agents and run assignment phase
		if opts.Assign {
			// Wait for agents to become ready
			readyCount, waitErr := waitForAgentsReadyWithObserver(
				ctx, opts.Session, opts.AssignReadyTimeout, spawnReadyPollInterval, spawnObserver,
			)

			var assignResult *AssignOutputEnhanced
			var assignErrors []string

			if waitErr != nil {
				assignErrors = append(assignErrors, fmt.Sprintf("ready wait failed: %v", waitErr))
			}
			if err := ctx.Err(); err != nil {
				return outputError(fmt.Errorf("spawn assignment canceled: %w", err))
			}

			var initResult *SpawnInitResult
			if opts.InitPrompt != "" {
				initReceipts, initErr := sendInitPromptToReadyAgentsWith(
					ctx, dir, opts.Session, opts.InitPrompt, opts.InitPromptWithAgentName,
					spawnObserver, spawnDispatcher,
				)
				initResult = &SpawnInitResult{
					PromptSent:    initErr == nil,
					AgentsReached: len(initReceipts),
					Receipts:      initReceipts,
				}
				if initErr != nil {
					assignErrors = append(assignErrors, fmt.Sprintf("init prompt failed: %v", initErr))
				}
				if err := ctx.Err(); err != nil {
					return outputError(fmt.Errorf("spawn init prompt canceled: %w", err))
				}
			}

			// Run assignment phase (even if ready wait timed out)
			result, err := runAssignmentPhaseContext(ctx, opts.Session, opts)
			if err != nil {
				assignErrors = append(assignErrors, fmt.Sprintf("assignment failed: %v", err))
			} else {
				assignResult = result
			}
			_ = readyCount // Used for logging in non-JSON mode

			// Return combined result
			combinedResult := SpawnAssignResult{
				Success: true,
				Spawn:   spawnResponse,
				Init:    initResult,
				Assign:  assignResult,
			}
			if len(assignErrors) > 0 {
				if assignResult == nil {
					assignResult = &AssignOutputEnhanced{Strategy: opts.AssignStrategy}
					combinedResult.Assign = assignResult
				}
				assignResult.Errors = append(assignResult.Errors, assignErrors...)
			}
			if !emitOutput {
				return nil
			}
			return emitSpawnAssignJSON(combinedResult)
		}

		if !emitOutput {
			return nil
		}
		return output.PrintJSON(spawnResponse)
	}

	// Register session coordinator with Agent Mail (creates agent.json for ntm lock)
	registerSessionAgent(ctx, opts.Session, dir)

	// Agent Mail identities were prepared and published per-pane before each
	// launch (gh#255); no post-launch registration pass is needed here.
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("spawn registration canceled: %w", err))
	}

	// Run post-spawn hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPostSpawn) {
		postSteps := output.NewSteps()
		if !IsJSONOutput() {
			postSteps.Start("Running post-spawn hooks")
		}

		// Enrich hook context with final spawn state
		hookCtx.AdditionalEnv["NTM_PANE_COUNT"] = fmt.Sprintf("%d", len(finalPanes))

		// Build list of pane titles for hooks
		var paneTitles []string
		for _, p := range finalPanes {
			if p.Title != "" {
				paneTitles = append(paneTitles, p.Title)
			}
		}
		hookCtx.AdditionalEnv["NTM_PANE_TITLES"] = strings.Join(paneTitles, ",")
		hookCtx.AdditionalEnv["NTM_SPAWN_SUCCESS"] = "true"

		hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		results, postErr := hookExec.RunHooksForEvent(hookRunCtx, hooks.EventPostSpawn, hookCtx)
		cancel()
		if postErr != nil {
			// Log error but don't fail (spawn already succeeded)
			if !IsJSONOutput() {
				postSteps.Warn()
				output.PrintWarningf("Post-spawn hook error: %v", postErr)
			}
		} else if hooks.AnyFailed(results) {
			// Log failures but don't fail (spawn already succeeded)
			if !IsJSONOutput() {
				postSteps.Warn()
				output.PrintWarningf("Post-spawn hook failed: %v", hooks.AllErrors(results))
			}
		} else if !IsJSONOutput() {
			postSteps.Done()
		}
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("post-spawn hooks canceled: %w", err))
	}

	// NOTE: an earlier revision registered agents with Agent Mail a second
	// time here, duplicating the registration above (gh#255). Identities are
	// now published once per pane, before each launch.

	// Start timeline tracking and persistence for this session
	if err := state.StartSessionTimeline(opts.Session); err != nil {
		// Log but don't fail - timeline tracking is not critical for session operation
		if !IsJSONOutput() {
			output.PrintWarningf("Timeline tracking failed to start: %v", err)
		}
	}

	// Run assignment phase if enabled (non-JSON mode)
	if opts.Assign {
		if err := runSpawnAssignmentTextContext(
			ctx, opts.Session, opts, spawnObserver, spawnDispatcher, defaultSpawnAssignmentOps(),
		); err != nil {
			return outputError(err)
		}
	}

	// Print "What's next?" only after every requested terminal phase succeeds.
	output.SuccessFooter(output.SpawnSuggestions(opts.Session)...)

	return nil
}

type spawnAssignmentOps struct {
	waitForReady func(context.Context, string, time.Duration, time.Duration, spawnSessionObserver) (int, error)
	sendInit     func(context.Context, string, string, string, bool, spawnSessionObserver, spawnPromptDispatcher) ([]dispatchsvc.Receipt, error)
	assign       func(context.Context, string, SpawnOptions) (*AssignOutputEnhanced, error)
}

func defaultSpawnAssignmentOps() spawnAssignmentOps {
	return spawnAssignmentOps{
		waitForReady: waitForAgentsReadyWithObserver,
		sendInit:     sendInitPromptToReadyAgentsWith,
		assign:       runAssignmentPhaseContext,
	}
}

func runSpawnAssignmentTextContext(
	ctx context.Context,
	session string,
	opts SpawnOptions,
	observer spawnSessionObserver,
	dispatcher spawnPromptDispatcher,
	ops spawnAssignmentOps,
) error {
	if ctx == nil {
		return errors.New("spawn assignment requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("spawn assignment canceled: %w", err)
	}
	if ops.waitForReady == nil || ops.sendInit == nil || ops.assign == nil {
		return errors.New("spawn assignment requires complete phase operations")
	}

	steps := output.NewSteps()
	steps.Start("Waiting for agents to become ready")
	readyCount, err := ops.waitForReady(
		ctx, session, opts.AssignReadyTimeout, spawnReadyPollInterval, observer,
	)
	if err != nil {
		steps.Fail()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("spawn assignment canceled: %w", ctxErr)
		}
		return fmt.Errorf("ready wait failed: %w", err)
	}
	steps.Done()
	output.PrintInfof("%d agents ready", readyCount)

	if opts.InitPrompt != "" {
		steps.Start("Sending init prompt to ready agents")
		// Best-effort project dir: the registered-identity lookup inside
		// sendInit degrades to the synthetic name when the dir is unknown.
		initProjectDir, initDirErr := resolveSpawnProjectDir(opts)
		if initDirErr != nil {
			initProjectDir = ""
		}
		initReceipts, initErr := ops.sendInit(
			ctx, initProjectDir, session, opts.InitPrompt, opts.InitPromptWithAgentName, observer, dispatcher,
		)
		if initErr != nil {
			steps.Fail()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("spawn init prompt canceled: %w", ctxErr)
			}
			return fmt.Errorf("init prompt failed: %w", initErr)
		}
		steps.Done()
		output.PrintInfof("Init prompt sent to %d agents", len(initReceipts))
	}

	steps.Start("Assigning work to agents")
	assignResult, err := ops.assign(ctx, session, opts)
	if err != nil {
		steps.Fail()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("spawn assignment canceled: %w", ctxErr)
		}
		return fmt.Errorf("assignment failed: %w", err)
	}
	if assignResult == nil {
		steps.Fail()
		return errors.New("assignment failed: assignment returned no result")
	}
	steps.Done()
	output.PrintInfof("Assigned %d tasks (strategy: %s)", len(assignResult.Assignments), assignResult.Strategy)
	return nil
}

func resolveSpawnPanePrompt(opts SpawnOptions, agentType AgentType, agentOrder int) (string, error) {
	panePrompt := opts.Prompt
	if mo, ok := opts.MarchingOrders[agentOrder]; ok {
		panePrompt = mo
	}

	defaultPrompt, err := opts.DefaultPrompts.ResolveForType(string(agentType))
	if err != nil {
		return panePrompt, err
	}
	if defaultPrompt == "" {
		return panePrompt, nil
	}
	if panePrompt == "" {
		return defaultPrompt, nil
	}
	return defaultPrompt + "\n\n" + panePrompt, nil
}

func spawnHasPromptDelivery(opts SpawnOptions) bool {
	for agentOrder, agent := range opts.Agents {
		panePrompt, err := resolveSpawnPanePrompt(opts, agent.Type, agentOrder)
		if panePrompt != "" {
			return true
		}
		if err != nil {
			continue
		}
	}
	return false
}

func spawnHasAutomatedPromptDeliveryTarget(agents []FlatAgent) bool {
	for _, spec := range agents {
		if err := agentpkg.AgentType(spec.Type).ValidateAutomatedPromptDelivery(); err == nil {
			return true
		}
	}
	return false
}

func buildSpawnPromptSequenceForAgent(agentType AgentType, cassContext, recoveryPrompt, userPrompt string, userDelay time.Duration) []spawnPromptStep {
	if err := agentpkg.AgentType(agentType).ValidateAutomatedPromptDelivery(); err != nil {
		return []spawnPromptStep{}
	}
	return buildSpawnPromptSequence(cassContext, recoveryPrompt, userPrompt, userDelay)
}

func buildSpawnPromptSequence(cassContext, recoveryPrompt, userPrompt string, userDelay time.Duration) []spawnPromptStep {
	steps := make([]spawnPromptStep, 0, 3)
	if userPrompt == "" && cassContext != "" {
		steps = append(steps, spawnPromptStep{Kind: "cass_context", Message: cassContext})
	}
	if recoveryPrompt != "" {
		steps = append(steps, spawnPromptStep{Kind: "recovery_context", Message: recoveryPrompt})
	}
	if userPrompt != "" {
		finalPrompt := userPrompt
		if cassContext != "" {
			finalPrompt = cassContext + "\n\n" + userPrompt
		}
		steps = append(steps, spawnPromptStep{Kind: "user_prompt", Message: finalPrompt, Delay: userDelay})
	}
	return steps
}

func spawnPromptSequenceIsCassOnly(steps []spawnPromptStep) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		if step.Kind != "cass_context" {
			return false
		}
	}
	return true
}

func dispatchSpawnPromptSequence(
	ctx context.Context,
	session, paneID string,
	steps []spawnPromptStep,
	observer spawnSessionObserver,
	dispatcher spawnPromptDispatcher,
	readyTimeout, pollInterval time.Duration,
) ([]dispatchsvc.Receipt, error) {
	if len(steps) == 0 {
		return []dispatchsvc.Receipt{}, nil
	}
	if observer == nil {
		return nil, errors.New("spawn prompt sequence requires a session observer")
	}
	if dispatcher == nil {
		return nil, errors.New("spawn prompt sequence requires a dispatcher")
	}

	receipts := make([]dispatchsvc.Receipt, 0, len(steps))
	for _, step := range steps {
		if err := waitContextDelay(ctx, step.Delay); err != nil {
			return receipts, fmt.Errorf("%s delay for pane %s: %w", step.Kind, paneID, err)
		}
		if err := waitForSpawnPaneReady(ctx, session, paneID, readyTimeout, pollInterval, observer); err != nil {
			return receipts, fmt.Errorf("%s readiness for pane %s: %w", step.Kind, paneID, err)
		}
		receipt, err := dispatcher.Dispatch(ctx, paneID, step.Message)
		if err != nil {
			return receipts, fmt.Errorf("%s dispatch to pane %s: %w", step.Kind, paneID, err)
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func waitContextDelay(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("spawn delay requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForSpawnSetupCompletionContext(ctx context.Context, setupDone <-chan struct{}, sigChan <-chan os.Signal, isJSON bool) error {
	if ctx == nil {
		return errors.New("spawn setup wait requires a command context")
	}
	select {
	case <-setupDone:
		return nil
	case <-sigChan:
		if !isJSON {
			fmt.Println("\n⚠ Spawn interrupted. Some prompts may not have been delivered.")
			fmt.Println("ℹ Session monitor is running (agents will auto-restart if they crash).")
		}
		return errors.New("spawn interrupted before all prompts were delivered")
	case <-ctx.Done():
		return fmt.Errorf("spawn canceled before all prompts were delivered: %w", ctx.Err())
	}
}

// waitForSpawnPromptWorkers is the setup lifecycle boundary: an interrupted
// wait cancels every prompt worker and does not return until all workers have
// observed cancellation and exited.
func waitForSpawnPromptWorkers(
	ctx context.Context,
	setupDone <-chan struct{},
	sigChan <-chan os.Signal,
	isJSON bool,
	cancelSetup context.CancelFunc,
) error {
	waitErr := waitForSpawnSetupCompletionContext(ctx, setupDone, sigChan, isJSON)
	if waitErr == nil && ctx != nil && ctx.Err() != nil {
		waitErr = fmt.Errorf("spawn canceled before all prompts were delivered: %w", ctx.Err())
	}
	if waitErr == nil {
		return nil
	}
	if cancelSetup != nil {
		cancelSetup()
	}
	<-setupDone
	return waitErr
}

func resolveSpawnProjectDir(opts SpawnOptions) (string, error) {
	projectDir := strings.TrimSpace(opts.ProjectDirOverride)
	if projectDir != "" {
		if !filepath.IsAbs(projectDir) {
			return "", fmt.Errorf("spawn project dir override must be absolute")
		}
		return filepath.Clean(projectDir), nil
	}
	return resolveCreationProjectDirForSession(opts.Session)
}

func appendOllamaAgentSpecs(agentSpecs *AgentSpecs, localCount, ollamaCount int, localModel string) (string, error) {
	// --ollama is an alias for --local (both spawn the same "ollama" agent type).
	if localCount < 0 {
		return "", fmt.Errorf("--local must be >= 0, got %d", localCount)
	}
	if ollamaCount < 0 {
		return "", fmt.Errorf("--ollama must be >= 0, got %d", ollamaCount)
	}
	if localCount > 0 && ollamaCount > 0 {
		return "", fmt.Errorf("cannot use both --local and --ollama; pick one")
	}

	model := strings.TrimSpace(localModel)
	if model == "" {
		model = "codellama:latest"
	}

	spawnCount := localCount + ollamaCount
	if spawnCount > 0 {
		if !modelPattern.MatchString(model) {
			return "", fmt.Errorf("invalid characters in --local-model %q; allowed: letters, numbers, . _ / @ : + -", model)
		}
		*agentSpecs = append(*agentSpecs, AgentSpec{
			Type:  AgentTypeOllama,
			Count: spawnCount,
			Model: model,
		})
	}

	return model, nil
}

func preflightOllamaSpawnContext(ctx context.Context, opts SpawnOptions) (string, error) {
	if ctx == nil {
		return "", errors.New("ollama preflight requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("ollama preflight canceled: %w", err)
	}
	if len(opts.Agents) == 0 {
		return "", nil
	}

	var requiredModels []string
	seenModels := make(map[string]struct{})
	for _, a := range opts.Agents {
		if a.Type != AgentTypeOllama {
			continue
		}
		model := strings.TrimSpace(a.Model)
		if model == "" {
			model = strings.TrimSpace(opts.LocalModel)
		}
		if model == "" {
			model = "codellama:latest"
		}
		if !modelPattern.MatchString(model) {
			return "", fmt.Errorf("invalid Ollama model %q; allowed: letters, numbers, . _ / @ : + -", model)
		}
		if _, ok := seenModels[model]; ok {
			continue
		}
		seenModels[model] = struct{}{}
		requiredModels = append(requiredModels, model)
	}

	if len(requiredModels) == 0 {
		return "", nil
	}

	host := strings.TrimSpace(opts.LocalHost)
	if host == "" {
		host = strings.TrimSpace(os.Getenv("NTM_OLLAMA_HOST"))
	}
	if host == "" {
		host = strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	}
	if host == "" {
		host = ollama.DefaultHost
	}

	adapter := ollama.NewAdapter()
	if err := adapter.Connect(host); err != nil {
		return "", err
	}
	defer func() {
		_ = adapter.Close()
	}()

	normalizedHost := adapter.Host()

	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	models, err := adapter.ListModels(listCtx)
	cancel()
	if err != nil {
		return "", err
	}

	available := make(map[string]struct{}, len(models))
	for _, m := range models {
		available[m.Name] = struct{}{}
	}

	for _, model := range requiredModels {
		if _, ok := available[model]; ok {
			continue
		}

		if IsJSONOutput() {
			var names []string
			for name := range available {
				names = append(names, name)
			}
			return "", fmt.Errorf("ollama model %q not found at %s (available: %s)", model, normalizedHost, strings.Join(names, ", "))
		}

		// Offer to pull missing model.
		prompt := fmt.Sprintf("Ollama model %q not found at %s. Pull it now?", model, normalizedHost)
		if !output.ConfirmWithOptions(prompt, output.ConfirmOptions{Style: output.StyleInfo, Default: false}) {
			return "", fmt.Errorf("ollama model %q not found (try: ollama pull %s)", model, model)
		}

		pullCtx, pullCancel := context.WithTimeout(ctx, 30*time.Minute)
		var pullErr error
		if IsJSONOutput() {
			pullErr = adapter.PullModel(pullCtx, model)
		} else {
			fmt.Printf("Pulling %s...\n", model)
			pullErr = adapter.PullModelWithProgress(pullCtx, model, newOllamaPullProgressPrinter("  "))
		}
		pullCancel()
		if pullErr != nil {
			return "", pullErr
		}
	}

	return normalizedHost, nil
}

// agentMailRegistrationEnabled reports whether spawn may contact Agent Mail
// to register identities. It fails closed: a missing config never authorizes
// a network-facing side effect, and both the top-level toggle and the
// auto_register preference must be on. This is a user-preference and privacy
// boundary (#243): registration sends the absolute project path, session
// metadata, and any configured bearer token to the configured endpoint.
func agentMailRegistrationEnabled() bool {
	return cfg != nil && cfg.AgentMail.Enabled && cfg.AgentMail.AutoRegister
}

// registerSessionAgent registers the session with Agent Mail.
// This is non-blocking and logs but does not fail if unavailable.
func registerSessionAgent(parentCtx context.Context, sessionName, workingDir string) {
	if !agentMailRegistrationEnabled() {
		return
	}
	if parentCtx == nil {
		if !IsJSONOutput() {
			output.PrintWarning("Agent Mail registration skipped: missing command context")
		}
		return
	}
	var opts []agentmail.Option
	if cfg != nil {
		if cfg.AgentMail.URL != "" {
			opts = append(opts, agentmail.WithBaseURL(cfg.AgentMail.URL))
		}
		if cfg.AgentMail.Token != "" {
			opts = append(opts, agentmail.WithToken(cfg.AgentMail.Token))
		}
	}
	client := agentmail.NewClient(opts...)
	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	info, err := client.RegisterSessionAgent(ctx, sessionName, workingDir)
	if err != nil {
		// Log but don't fail
		if !IsJSONOutput() {
			output.PrintWarningf("Agent Mail registration failed: %v", err)
		}
		return
	}
	if info != nil && !IsJSONOutput() {
		output.PrintInfof("Registered with Agent Mail as %s", info.AgentName)
	}
}

// spawnedAgentInfo holds agent info for registration with Agent Mail.
type spawnedAgentInfo struct {
	paneIndex     int
	paneID        string
	paneTitle     string
	agentType     string
	model         string
	resolvedModel string
	// paneDir is the directory the agent process is launched in when it
	// differs from the session's project key (linked worktrees). Empty for
	// ordinary panes. The identity is additionally published under this key
	// so cwd-derived resolution finds it (ntm#257).
	paneDir string
}

// registerSpawnedAgents registers each spawned agent with Agent Mail and returns status.
// This function implements graceful degradation - Agent Mail unavailability does not
// cause spawn to fail. Returns nil if Agent Mail is not available or disabled.
//
// It is the batch (post-launch) entry point used by add, adopt, and relaunch,
// where the agent processes already exist. The spawn path instead uses a
// spawnIdentityCoordinator directly so each pane's identity is published
// BEFORE its agent command is sent (gh#255); both paths share the same
// per-pane logic below.
func registerSpawnedAgents(parentCtx context.Context, workingDir, sessionName string, agents []spawnedAgentInfo) *output.AgentMailSpawnStatus {
	if parentCtx == nil {
		return &output.AgentMailSpawnStatus{AgentsFailed: len(agents)}
	}
	coordinator := newSpawnIdentityCoordinator(workingDir, sessionName)
	for _, agent := range agents {
		coordinator.prepareAgent(parentCtx, agent)
	}
	return coordinator.finalStatus()
}

// spawnIdentityCoordinator owns one Agent Mail client, availability probe,
// project registration, session registry, and transient-busy reconciliation
// set for the lifetime of a spawn (or batch registration). Initialization is
// lazy: nothing contacts Agent Mail until the first prepareAgent call, so a
// spawn with zero agents (or with registration disabled) pays no cost.
//
// The coordinator exists to fix a startup race (gh#255): identities used to
// be created and published only after every agent had been launched, so an
// agent that resolved its pane identity during boot could read a previous
// occupant's name — or none. prepareAgent is designed to run immediately
// before tmux.SendKeysContext for each pane, making the identity file and
// registry entry durable before the agent process starts.
//
// Failure policy is unchanged from the historical batch path: Agent Mail
// being disabled, unavailable, or failing per-pane never blocks the launch
// (graceful degradation); failures are counted and warned, not fatal.
type spawnIdentityCoordinator struct {
	workingDir  string
	sessionName string

	initialized bool
	enabled     bool
	client      *agentmail.Client
	available   bool
	projectOK   bool
	registry    *agentmail.SessionAgentRegistry
	// reconciledIDs tracks agent IDs already claimed by transient-busy
	// reconciliation so two panes never match the same server-side agent.
	reconciledIDs map[int]bool
	status        *output.AgentMailSpawnStatus
	// livePanes maps pane id -> #{pane_pid} for the session as observed when
	// the coordinator initialized (refreshed on a miss); liveness derives from
	// it. Both are nil when the topology could not be read, which makes every
	// recorded holder count as live — the fail-safe is a fresh identity,
	// never a shared one (ntm#256).
	livePanes map[string]int
	liveness  agentmail.PaneLiveness
}

// spawnIdentityPaneProbe lists a session's panes for liveness judgement. It
// is a variable so tests can supply a topology without a tmux server.
var spawnIdentityPaneProbe = tmux.GetPanesContext

func newSpawnIdentityCoordinator(workingDir, sessionName string) *spawnIdentityCoordinator {
	return &spawnIdentityCoordinator{
		workingDir:    workingDir,
		sessionName:   sessionName,
		reconciledIDs: make(map[int]bool),
	}
}

// ensureInit performs the one-time spawn-scoped setup: config gate, client
// construction, availability probe, registry load, and project registration.
func (c *spawnIdentityCoordinator) ensureInit(parentCtx context.Context) {
	if c.initialized {
		return
	}
	c.initialized = true

	// Fail closed: no config never authorizes contacting Agent Mail, and both
	// enabled and auto_register must be on (#243).
	if !agentMailRegistrationEnabled() {
		return
	}
	c.enabled = true

	var opts []agentmail.Option
	if cfg != nil {
		if cfg.AgentMail.URL != "" {
			opts = append(opts, agentmail.WithBaseURL(cfg.AgentMail.URL))
		}
		if cfg.AgentMail.Token != "" {
			opts = append(opts, agentmail.WithToken(cfg.AgentMail.Token))
		}
	}
	c.client = agentmail.NewClient(opts...)

	// Check availability first (uses cached result)
	if !c.client.IsAvailable() {
		c.status = &output.AgentMailSpawnStatus{
			Available:         false,
			ProjectRegistered: false,
		}
		return
	}
	c.available = true
	c.status = &output.AgentMailSpawnStatus{
		Available: true,
		AgentMap:  make(map[string]string),
	}

	// Load existing registry to reuse identities on respawn (#69),
	// falling back to a fresh registry if none exists.
	c.registry, _ = agentmail.LoadSessionAgentRegistry(c.sessionName, c.workingDir)
	if c.registry == nil {
		c.registry = agentmail.NewSessionAgentRegistry(c.sessionName, c.workingDir)
	}
	c.observeLivePanes(parentCtx)

	// Ensure project exists
	ctx, cancel := context.WithTimeout(parentCtx, 5*time.Second)
	defer cancel()
	if _, err := c.client.EnsureProject(ctx, c.workingDir); err != nil {
		if !IsJSONOutput() {
			output.PrintWarningf("Agent Mail project registration failed: %v", err)
		}
		return
	}
	c.projectOK = true
	c.status.ProjectRegistered = true
}

// prepareAgent reuses or creates the Agent Mail identity for one pane and
// publishes it (canonical identity file, legacy compat file, session
// registry) so the identity is resolvable before the agent process starts.
// All failures degrade gracefully: they are counted in the aggregate status
// and never block the pane's launch.
func (c *spawnIdentityCoordinator) prepareAgent(parentCtx context.Context, agent spawnedAgentInfo) {
	if parentCtx == nil {
		return
	}
	c.ensureInit(parentCtx)
	if !c.enabled {
		return
	}
	if !c.available || !c.projectOK {
		c.status.AgentsFailed++
		return
	}
	if parentCtx.Err() != nil {
		c.status.AgentsFailed++
		return
	}

	// Reuse the identity of a prior occupant of this slot (#69) — but only a
	// DEAD one. The pane id is the primary key; a matching title whose
	// recorded pane is still live means the slot is occupied and the running
	// agent keeps its name, so this pane gets a fresh identity (ntm#256).
	if existingName, recoveredFrom, ok := c.registry.ResolveForPane(agent.paneTitle, agent.paneID, c.liveness); ok && existingName != "" {
		program := agentTypeToProgram(agent.agentType)
		model := agent.resolvedModel
		if model == "" {
			model = agent.model
		}
		if strings.TrimSpace(model) == "" {
			model = delegatedModelPlaceholder(program)
		}
		c.registry.HydrateClientTokens(c.client)
		regCtx, regCancel := context.WithTimeout(parentCtx, 15*time.Second)
		registered, regErr := c.client.RegisterAgent(regCtx, agentmail.RegisterAgentOptions{
			ProjectKey: c.workingDir,
			Program:    program,
			Model:      model,
			Name:       existingName,
			PaneID:     agent.paneID,
		})
		regCancel()
		if regErr != nil || registered == nil {
			c.status.AgentsFailed++
			return
		}
		if err := c.publishIdentity(agent, registered.Name); err != nil {
			c.status.AgentsFailed++
			return
		}
		c.status.AgentsRegistered++
		c.status.AgentMap[agent.paneID] = existingName
		if !IsJSONOutput() {
			if recoveredFrom != "" {
				output.PrintInfof("Reused existing identity for pane %d: %s (recovered from pane %s, no longer live)", agent.paneIndex, existingName, recoveredFrom)
			} else {
				output.PrintInfof("Reused existing identity for pane %d: %s", agent.paneIndex, existingName)
			}
		}
		c.registry.AddAgent(agent.paneTitle, agent.paneID, existingName)
		c.recordPanePID(parentCtx, agent.paneID)
		c.persistRegistry()
		return
	}

	// Map agent type to program name
	program := agentTypeToProgram(agent.agentType)
	model := agent.resolvedModel
	if model == "" {
		model = agent.model
	}
	// Agent Mail rejects an empty model, but several agent types legitimately
	// delegate model selection to the CLI's own config (bare --oc=N, --grok=N,
	// plugins without a default). Register with a stable, clearly-delegated
	// delegation marker instead of failing the pane's identity (ntm#261).
	if strings.TrimSpace(model) == "" {
		model = delegatedModelPlaceholder(program)
		if !IsJSONOutput() {
			output.PrintInfof("No model resolved for pane %d (%s); registering with Agent Mail as %q — set models.default_%s or pass an explicit model to name it",
				agent.paneIndex, agent.agentType, model, modelDefaultKeyForType(agent.agentType))
		}
	}

	regCtx, regCancel := context.WithTimeout(parentCtx, 15*time.Second)
	registered, err := c.client.CreateAgentIdentity(regCtx, agentmail.RegisterAgentOptions{
		ProjectKey: c.workingDir,
		Program:    program,
		Model:      model,
		PaneID:     agent.paneID,
	})
	regCancel()

	if err != nil {
		// On transient busy errors, the agent may have been created server-side
		// despite the error. Reconcile by listing agents and checking.
		if errors.Is(err, agentmail.ErrTransientBusy) {
			reconcileCtx, reconcileCancel := context.WithTimeout(parentCtx, 5*time.Second)
			allAgents, listErr := c.client.ListAgents(reconcileCtx, c.workingDir)
			reconcileCancel()
			if listErr == nil {
				// Look for a recently-created agent matching our program/model
				// that hasn't already been claimed by a prior pane.
				var found *agentmail.Agent
				for i := range allAgents {
					if allAgents[i].Program == program && allAgents[i].Model == model {
						if !c.reconciledIDs[allAgents[i].ID] {
							if found == nil || allAgents[i].ID > found.ID {
								found = &allAgents[i]
							}
						}
					}
				}
				if found != nil {
					// Agent was actually created — treat as success
					c.reconciledIDs[found.ID] = true
					registered = found
					err = nil
					if !IsJSONOutput() {
						output.PrintInfof("Reconciled busy response for pane %d: agent %s exists", agent.paneIndex, found.Name)
					}
				}
			}
		}
		if err != nil {
			c.status.AgentsFailed++
			if !IsJSONOutput() {
				output.PrintWarningf("Agent Mail registration failed for pane %d: %v", agent.paneIndex, err)
			}
			return
		}
	}

	// Write the per-pane identity file(s) so Agent Mail and notify hooks can
	// resolve AGENT_MAIL_AGENT before the agent process starts.
	if err := c.publishIdentity(agent, registered.Name); err != nil {
		c.status.AgentsFailed++
		if !IsJSONOutput() {
			output.PrintWarningf("Agent Mail registration for pane %d lacked verified generation readback: %v", agent.paneIndex, err)
		}
		return
	}

	c.status.AgentsRegistered++
	c.status.AgentMap[agent.paneID] = registered.Name

	// Add to registry for persistence
	c.registry.AddAgent(agent.paneTitle, agent.paneID, registered.Name)
	c.recordPanePID(parentCtx, agent.paneID)
	// Persist the registration_token alongside the agent name so
	// later ntm processes can re-authenticate as this agent on
	// mcp-agent-mail >=2.13 (ntm#146).
	if registered.RegistrationToken != "" {
		c.registry.SetRegistrationToken(registered.Name, registered.RegistrationToken)
	}
	c.persistRegistry()

	if !IsJSONOutput() {
		output.PrintInfof("Registered agent pane %d as %s", agent.paneIndex, registered.Name)
	}
}

// persistRegistry saves the session registry after each mutation so the
// pane-to-name mapping is durable before the pane's agent process launches.
func (c *spawnIdentityCoordinator) persistRegistry() {
	if c.registry == nil || c.registry.Count() == 0 {
		return
	}
	if err := agentmail.SaveSessionAgentRegistry(c.registry); err != nil {
		if !IsJSONOutput() {
			output.PrintWarningf("Failed to persist agent registry: %v", err)
		}
	}
}

// finalStatus returns the aggregate registration status for output assembly.
// It returns nil when registration is disabled or no agent was ever prepared,
// matching the historical registerSpawnedAgents contract.
func (c *spawnIdentityCoordinator) finalStatus() *output.AgentMailSpawnStatus {
	return c.status
}

// observeLivePanes snapshots the session's pane ids and pids so registry
// bindings can be judged live or dead (ntm#256). When the topology cannot be
// read the snapshot is nil and ResolveForPane treats every recorded holder as
// live: titles then never trigger reuse, pane ids still do.
func (c *spawnIdentityCoordinator) observeLivePanes(ctx context.Context) {
	panes, err := spawnIdentityPaneProbe(ctx, c.sessionName)
	if err != nil {
		c.livePanes = nil
		c.liveness = nil
		return
	}
	live := make(map[string]int, len(panes))
	for _, p := range panes {
		if p.ID != "" {
			live[p.ID] = p.PID
		}
	}
	c.livePanes = live
	c.liveness = livenessFromPanes(panes)
}

// recordPanePID stores the registering pane's current #{pane_pid} beside its
// binding so a later process can tell this incarnation from a recycled %N.
// A pane absent from the init snapshot (created after it) triggers one
// refresh; an unknown pid is simply left unrecorded.
func (c *spawnIdentityCoordinator) recordPanePID(ctx context.Context, paneID string) {
	if paneID == "" || c.registry == nil {
		return
	}
	pid, ok := c.livePanes[paneID]
	if !ok {
		c.observeLivePanes(ctx)
		pid = c.livePanes[paneID]
	}
	c.registry.SetPanePID(paneID, pid)
}

// publishIdentity writes the canonical identity file (XDG-compliant, atomic,
// Agent-Mail-compatible; see agentmail.CanonicalIdentityPath and the
// mcp-agent-mail Rust reference in pane_identity.rs) plus the legacy /tmp
// compat file still read by older hooks, under every project key the pane may
// resolve its identity through: the session key, its symlink-resolved form,
// and the pane's worktree directory (ntm#257). The extra keys are best-effort;
// only a failure on the session key itself is reported.
func (c *spawnIdentityCoordinator) publishIdentity(agent spawnedAgentInfo, name string) error {
	_, receipt, err := agentmail.ReadVerifiedIdentity(c.workingDir, agent.paneID, name)
	if err != nil {
		return err
	}
	for _, key := range identityPublishKeys(c.workingDir, agent.paneDir) {
		if key != c.workingDir {
			if _, writeErr := agentmail.MirrorVerifiedIdentity(key, agent.paneID, receipt); writeErr != nil {
				return writeErr
			}
		}
		_ = agentmail.WriteLegacyCompatIdentity(key, agent.paneID, name)
	}
	return nil
}

// delegatedModelPlaceholder is the model identifier NTM registers with Agent
// Mail when the agent type delegates model selection to its own CLI config
// and no explicit model was given. It is stable per program, so the same
// pane re-registers identically, and unmistakably not a real model name.
func delegatedModelPlaceholder(program string) string {
	program = strings.TrimSpace(program)
	if program == "" {
		program = "agent"
	}
	return program + "/cli-default"
}

// modelDefaultKeyForType names the [models] key a user would set to give the
// type a real default model, for the delegation notice.
func modelDefaultKeyForType(agentType string) string {
	switch agentpkg.AgentType(agentType).Canonical() {
	case agentpkg.AgentTypeOpencode:
		return "opencode"
	case agentpkg.AgentTypeGrok:
		return "grok"
	case agentpkg.AgentTypeClaudeCode:
		return "claude"
	case agentpkg.AgentTypeCodex:
		return "codex"
	case agentpkg.AgentTypeGemini:
		return "gemini"
	case agentpkg.AgentTypeOllama:
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}

// agentTypeToProgram maps NTM agent types to Agent Mail program names.
func agentTypeToProgram(agentType string) string {
	switch agentType {
	case "cc":
		return "claude-code"
	case "cod":
		return "codex-cli"
	case "gmi":
		return "gemini-cli"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	case "oc":
		return "opencode"
	default:
		return agentType
	}
}

// Identity file path helpers moved to internal/agentmail/pane_identity.go so
// they can be shared with the file reservation watcher and so they converge
// on the same contract as the mcp-agent-mail Rust reference implementation.
// See agentmail.CanonicalIdentityPath / agentmail.WriteIdentity.

// getMemoryContext retrieves and formats CM (CASS Memory) memories for agent spawn.
// Returns a formatted markdown string with project-specific rules and anti-patterns
// from past sessions. Returns empty string if CM is unavailable or disabled.
//
// This function implements graceful degradation - CM unavailability does not
// cause spawn to fail, it simply returns an empty string.
func getMemoryContext(projectName, task string) string {
	// Check if memory integration is enabled in config
	if cfg == nil || !cfg.SessionRecovery.IncludeCMMemories {
		return ""
	}

	// Create CM CLI client
	cmClient := cm.NewCLIClient()

	// Check if CM is installed
	if !cmClient.IsInstalled() {
		return ""
	}

	// Determine the query task
	queryTask := task
	if queryTask == "" {
		queryTask = projectName
	}

	// Query CM for context with limits from config
	maxRules := cfg.SessionRecovery.MaxCMRules
	maxSnippets := cfg.SessionRecovery.MaxCMSnippets
	if maxRules == 0 {
		maxRules = 10 // Fallback default
	}
	if maxSnippets == 0 {
		maxSnippets = 3 // Fallback default
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// `getMemoryContext` is invoked from a path that doesn't carry the absolute
	// workspace; an empty workspace falls through to CM's unscoped query, which
	// preserves prior behavior for this surface (the workspace-scoped path is
	// `loadRecoveryCMMemories`, which carries `workingDir`).
	result, err := cmClient.GetRecoveryContext(ctx, queryTask, "", maxRules, maxSnippets)
	if err != nil {
		// Log warning but don't fail - graceful degradation
		if !IsJSONOutput() {
			output.PrintWarningf("CM context retrieval failed: %v", err)
		}
		return ""
	}

	if result == nil {
		return ""
	}

	// Format the result as markdown with the specified structure
	return formatMemoryContext(result)
}

// formatMemoryContext formats CM context result into the standard recovery format.
// Output format:
//
//	# Project Memory from Past Sessions
//
//	## Key Rules for This Project
//	- [b-8f3a2c] Always use structured logging with log/slog
//
//	## Anti-Patterns to Avoid
//	- [b-7d3e8c] Don't add backwards-compatibility shims
func formatMemoryContext(result *cm.CLIContextResponse) string {
	if result == nil {
		return ""
	}

	// Check if there's anything to format
	if len(result.RelevantBullets) == 0 && len(result.AntiPatterns) == 0 {
		return ""
	}

	var buf strings.Builder

	buf.WriteString("# Project Memory from Past Sessions\n\n")

	if len(result.RelevantBullets) > 0 {
		buf.WriteString("## Key Rules for This Project\n")
		for _, rule := range result.RelevantBullets {
			buf.WriteString(fmt.Sprintf("- [%s] %s\n", rule.ID, rule.Content))
		}
		buf.WriteString("\n")
	}

	if len(result.AntiPatterns) > 0 {
		buf.WriteString("## Anti-Patterns to Avoid\n")
		for _, pattern := range result.AntiPatterns {
			buf.WriteString(fmt.Sprintf("- [%s] %s\n", pattern.ID, pattern.Content))
		}
		buf.WriteString("\n")
	}

	return buf.String()
}

func recoveryContextTermination(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// buildRecoveryContext builds the full recovery context for session recovery.
// It gathers information from BV (beads), Agent Mail (messages), and CM (memories).
func buildRecoveryContext(ctx context.Context, sessionName, workingDir string, recoveryCfg config.SessionRecoveryConfig) (*RecoveryContext, error) {
	if !recoveryCfg.Enabled {
		return nil, nil
	}
	if ctx == nil {
		return nil, errors.New("recovery context requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rc := &RecoveryContext{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string
	var terminalErr error
	timedOut := false
	recoveryWindow := recoveryCfg.GetTimeout()
	recordSourceError := func(source string, err error) bool {
		if err == nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		switch contextErr := recoveryContextTermination(err); contextErr {
		case context.Canceled:
			// Cancellation comes from the operator or the parent command, so
			// nothing downstream is going to succeed either: stay terminal.
			terminalErr = contextErr
		case context.DeadlineExceeded:
			// #232: a source that outruns the recovery window is a slow
			// optional source, not a reason to abort the spawn and leave
			// empty panes behind. Degrade to PARTIAL_RECOVERY and name the
			// source that blew the window so the warning is actionable.
			timedOut = true
			errs = append(errs, fmt.Sprintf("%s: timed out after %s (raise recovery.timeout_seconds)", source, recoveryWindow))
		default:
			errs = append(errs, fmt.Sprintf("%s: %v", source, err))
		}
		return true
	}

	// Load beads if enabled
	if recoveryCfg.IncludeBeadsContext {
		wg.Add(1)
		go func() {
			defer wg.Done()
			beads, completed, blocked, err := loadRecoveryBeads(ctx, workingDir)
			if recordSourceError("beads", err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			rc.Beads = beads
			rc.CompletedBeads = completed
			rc.BlockedBeads = blocked
		}()
	}

	// Load Agent Mail messages if enabled
	if recoveryCfg.IncludeAgentMail {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, reservations, transfer, err := loadRecoveryMessages(ctx, sessionName, workingDir)
			if recordSourceError("agent mail", err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			rc.Messages = msgs
			rc.FileReservations = reservations
			rc.ReservationTransfer = transfer
		}()
	}

	// Load CM memories if enabled
	if recoveryCfg.IncludeCMMemories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			memories, err := loadRecoveryCMMemories(ctx, workingDir)
			if recordSourceError("cm memories", err) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			rc.CMMemories = memories
		}()
	}

	// Load latest checkpoint if available. Pass the spawn working directory
	// so a session-name collision across repos cannot surface a checkpoint
	// recorded against a different working directory (#131).
	wg.Add(1)
	go func() {
		defer wg.Done()
		cp, err := loadRecoveryCheckpoint(sessionName, workingDir)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			// Checkpoints are optional; only log if debug
			if cfg != nil && cfg.FileReservation.Debug {
				errs = append(errs, fmt.Sprintf("checkpoint: %v", err))
			}
		} else if cp != nil {
			rc.Checkpoint = cp
		}
	}()

	wg.Wait()
	if err := ctx.Err(); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// The recovery window itself expired (#232). Sources that noticed
		// already recorded a named warning; record a generic one when none
		// did, so the partial result still explains why it is thin.
		if !timedOut {
			timedOut = true
			errs = append(errs, fmt.Sprintf("recovery window: timed out after %s (raise recovery.timeout_seconds)", recoveryWindow))
		}
	}
	if terminalErr != nil {
		return nil, terminalErr
	}

	// bd-wnzhl: errs accumulates from 4 parallel goroutines under a
	// mutex, so without sorting it ends up in goroutine-completion
	// order — sibling of bd-brr6h / bd-c9wr1 / bd-aj2qv. Sort
	// alphabetically so rc.Error.Details (and the printed warnings
	// below) are byte-stable across runs with the same set of source
	// failures.
	sort.Strings(errs)

	// Estimate tokens and truncate if needed
	rc.TokenCount = estimateRecoveryTokens(rc)
	if recoveryCfg.MaxRecoveryTokens > 0 && rc.TokenCount > recoveryCfg.MaxRecoveryTokens {
		truncateRecoveryContext(rc, recoveryCfg.MaxRecoveryTokens)
	}

	// Generate summary
	rc.Summary = generateRecoverySummary(rc)

	// Record any errors for diagnostic purposes
	if len(errs) > 0 {
		rc.Error = &RecoveryError{
			Code:        "PARTIAL_RECOVERY",
			Message:     "Some recovery sources unavailable",
			Recoverable: true,
			Details:     errs,
		}
		if !IsJSONOutput() {
			for _, e := range errs {
				output.PrintWarningf("Recovery context: %s", e)
			}
		}
	}

	return rc, nil
}

func newRecoverySpawnStatus(enabled bool, rc *RecoveryContext) *output.RecoverySpawnStatus {
	if !enabled {
		return nil
	}
	status := &output.RecoverySpawnStatus{
		Enabled:  true,
		Warnings: []string{},
	}
	if rc == nil {
		return status
	}
	status.Applied = FormatRecoveryPrompt(rc, AgentTypeClaude) != ""
	if rc.Error != nil {
		status.Partial = true
		status.ErrorCode = rc.Error.Code
		status.Warnings = append(status.Warnings, rc.Error.Details...)
	}
	return status
}

// loadRecoveryBeads loads in-progress, completed, and blocked beads from BV.
//
// Recovery context is trust-sensitive — surfacing rows from a parent repo's
// .beads/ to a child directory's spawn would steer the new agent toward
// unrelated work. We refuse to walk up: if the spawn working directory has
// no local .beads/, return empty lists rather than letting br discover the
// parent workspace. Generic bv list helpers preserve the walk-up behavior
// for non-recovery callers (alerts, status displays). See #130.
func loadRecoveryBeads(ctx context.Context, workingDir string) (inProgress, completed, blocked []RecoveryBead, err error) {
	const limit = 10 // reasonable limit for recovery context

	if ctx == nil {
		return nil, nil, nil, errors.New("recovery beads requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if !bv.HasLocalBeadsDB(workingDir) {
		return nil, nil, nil, nil
	}

	// Get in-progress beads
	ipList, err := bv.GetInProgressListContext(ctx, workingDir, limit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load in-progress recovery beads: %w", err)
	}
	for _, b := range ipList {
		inProgress = append(inProgress, RecoveryBead{
			ID:       b.ID,
			Title:    b.Title,
			Assignee: b.Assignee,
		})
	}

	// Get recently completed beads
	completedList, err := bv.GetRecentlyCompletedListContext(ctx, workingDir, limit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load recently completed recovery beads: %w", err)
	}
	for _, b := range completedList {
		completed = append(completed, RecoveryBead{
			ID:    b.ID,
			Title: b.Title,
		})
	}

	// Get blocked beads
	blockedList, err := bv.GetBlockedListContext(ctx, workingDir, limit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load blocked recovery beads: %w", err)
	}
	for _, b := range blockedList {
		blocked = append(blocked, RecoveryBead{
			ID:    b.ID,
			Title: b.Title,
		})
	}

	return inProgress, completed, blocked, nil
}

type recoveryMailClient interface {
	FetchInbox(ctx context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error)
	ListReservations(ctx context.Context, projectKey, agentName string, allAgents bool) ([]agentmail.FileReservation, error)
}

// loadRecoveryMessages loads recent Agent Mail messages and file reservations.
func loadRecoveryMessages(ctx context.Context, sessionName, workingDir string) ([]RecoveryMessage, []string, *handoff.ReservationTransferResult, error) {
	if ctx == nil {
		return nil, nil, nil, errors.New("recovery messages requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	client := newAgentMailClient(workingDir)

	if !client.IsAvailable() {
		return nil, nil, nil, nil // Graceful degradation
	}

	// Ensure project exists before fetching inbox. Without this, fetch_inbox
	// fails with "Project '/path' not found" if the project hasn't been
	// registered yet (e.g. on first launch or with a fresh agent mail server).
	// Non-fatal: if ensure_project fails, we still try fetching in case
	// the project already exists and the error is transient.
	if _, err := client.EnsureProject(ctx, workingDir); recoveryContextTermination(err) != nil {
		return nil, nil, nil, err
	}

	agentName := resolveRecoveryAgentName(sessionName, workingDir)

	inbox, effectiveAgentName, err := fetchRecoveryInbox(ctx, client, workingDir, sessionName, agentName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch inbox: %w", err)
	}

	// #108: `fetchRecoveryInbox` signals "fresh project, no registered
	// agents yet" by returning `(nil, "", nil)` — silenced empty-state.
	// In that case the reservation-transfer and reservation-list calls
	// below would hit the same "agent not found" response from the
	// server (because the agent truly doesn't exist), and the transfer
	// path would surface that as a `Reservation transfer:` warning
	// rather than silencing it — trading one fresh-project warning for
	// another. Short-circuit the whole bundle instead.
	if effectiveAgentName == "" && len(inbox) == 0 {
		return nil, nil, nil, nil
	}

	var msgs []RecoveryMessage
	for _, m := range inbox {
		msgs = append(msgs, RecoveryMessage{
			ID:         m.ID,
			From:       m.From,
			Subject:    m.Subject,
			Body:       m.BodyMD,
			Importance: m.Importance,
			CreatedAt:  m.CreatedTS.Time,
		})
	}

	// Attempt reservation transfer using latest handoff, if available.
	transferResult, transferErr := attemptReservationTransfer(ctx, client, sessionName, effectiveAgentName, workingDir)
	if recoveryContextTermination(transferErr) != nil {
		return nil, nil, transferResult, transferErr
	}
	if transferErr != nil && !IsJSONOutput() {
		output.PrintWarningf("Reservation transfer: %v", transferErr)
	}

	reservations, _, err := listRecoveryReservations(ctx, client, workingDir, sessionName, effectiveAgentName)
	if err != nil {
		if recoveryContextTermination(err) != nil {
			return nil, nil, transferResult, err
		}
		// Non-fatal, return messages only
		return msgs, nil, transferResult, nil
	}

	paths := reservationPaths(reservations)
	if transferResult != nil && transferResult.Success && len(transferResult.GrantedPaths) > 0 {
		paths = transferResult.GrantedPaths
	}

	return msgs, paths, transferResult, nil
}

func resolveRecoveryAgentName(sessionName, workingDir string) string {
	info, err := loadResolvedSessionAgent(sessionName, workingDir)
	if err == nil && info != nil && info.AgentName != "" {
		return info.AgentName
	}
	return sessionName
}

func recoveryAgentCandidates(sessionName, agentName string) []string {
	if agentName == "" || agentName == sessionName {
		return []string{sessionName}
	}
	return []string{agentName, sessionName}
}

func fetchRecoveryInbox(ctx context.Context, client recoveryMailClient, projectKey, sessionName, agentName string) ([]agentmail.InboxMessage, string, error) {
	var lastErr error
	for _, candidate := range recoveryAgentCandidates(sessionName, agentName) {
		inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     candidate,
			Limit:         10,
			IncludeBodies: true,
		})
		if err == nil {
			return inbox, candidate, nil
		}
		if recoveryContextTermination(err) != nil {
			return nil, "", err
		}
		lastErr = err
	}
	// #108: on a fresh project with no registered agents yet, every
	// candidate resolves to "agent not found" — which is the expected
	// empty-inbox state, not a warning-worthy failure. Treat it as an
	// empty inbox so `ntm spawn` doesn't print an alarming "Recovery
	// context: agent mail: fetch inbox: ... Agent '...' not found" line
	// on first-run. Real fetch failures (network, auth, server error)
	// still surface normally.
	if isRecoveryEmptyInboxError(lastErr) {
		return nil, "", nil
	}
	return nil, "", lastErr
}

// isRecoveryEmptyInboxError reports whether err indicates that the
// recovery-inbox fetch failed because the target agent (or the
// project's agent list) simply doesn't exist yet. On a brand-new
// project, that is the expected first-run state, not an error worth
// warning the user about.
func isRecoveryEmptyInboxError(err error) bool {
	if err == nil {
		return false
	}
	// Typed-error paths: `agentmail.mapJSONRPCError` wraps the server's
	// "agent not registered" / "not found" JSON-RPC responses through
	// these sentinels. In the recovery context both mean "there's no
	// prior state to restore", which is an expected silent empty-state,
	// not a warning — `ntm spawn` still proceeds normally.
	if errors.Is(err, agentmail.ErrAgentNotRegistered) ||
		errors.Is(err, agentmail.ErrNotFound) {
		return true
	}
	// String fallback for plain errors that never went through the
	// typed-mapping (e.g. a wrapper ate the Unwrap chain, or the
	// server returned the message via a non-standard transport).
	//
	// Narrowly match only the specific server-emitted shapes:
	//
	//   "Agent 'X' not found. Project 'Y' has no registered agents yet."
	//   "Agent 'X' not found in project 'Y'"
	//
	// A loose `contains("agent") && contains("not found")` heuristic
	// would false-match `APIError.Error()` output — every APIError
	// stringifies as `"agentmail: <op> failed: <inner>"`, so a plain
	// "Project 'x' not found" or DNS-level "host not found" wrapped
	// through APIError would incorrectly resolve to empty-inbox and
	// hide real failures.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "has no registered agents") {
		return true
	}
	return strings.Contains(msg, "agent '") && strings.Contains(msg, "' not found")
}

func listRecoveryReservations(ctx context.Context, client recoveryMailClient, projectKey, sessionName, agentName string) ([]agentmail.FileReservation, string, error) {
	var lastErr error
	for _, candidate := range recoveryAgentCandidates(sessionName, agentName) {
		reservations, err := client.ListReservations(ctx, projectKey, candidate, false)
		if err == nil {
			return reservations, candidate, nil
		}
		if recoveryContextTermination(err) != nil {
			return nil, "", err
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func reservationPaths(reservations []agentmail.FileReservation) []string {
	var paths []string
	for _, r := range reservations {
		if r.PathPattern != "" {
			paths = append(paths, r.PathPattern)
		}
	}
	return paths
}

func buildRecoveryReservationTransferOptions(transfer *handoff.ReservationTransfer, targetAgentName, workingDir string) handoff.TransferReservationsOptions {
	projectKey := transfer.ProjectKey
	if projectKey == "" {
		projectKey = workingDir
	}

	ttlSeconds := transfer.TTLSeconds
	if ttlSeconds <= 0 && cfg != nil && cfg.FileReservation.DefaultTTLMin > 0 {
		ttlSeconds = cfg.FileReservation.DefaultTTLMin * 60
	}

	grace := time.Duration(transfer.GracePeriodSeconds) * time.Second
	return handoff.TransferReservationsOptions{
		ProjectKey:   projectKey,
		FromAgent:    transfer.FromAgent,
		ToAgent:      targetAgentName,
		Reservations: transfer.Reservations,
		TTLSeconds:   ttlSeconds,
		GracePeriod:  grace,
	}
}

func attemptReservationTransfer(ctx context.Context, client *agentmail.Client, sessionName, targetAgentName, workingDir string) (*handoff.ReservationTransferResult, error) {
	reader := handoff.NewReader(workingDir)
	h, _, err := reader.FindLatest(sessionName)
	if err != nil || h == nil || h.ReservationTransfer == nil {
		return nil, nil
	}

	transfer := h.ReservationTransfer
	if transfer.FromAgent == "" || len(transfer.Reservations) == 0 {
		return nil, nil
	}

	opts := buildRecoveryReservationTransferOptions(transfer, targetAgentName, workingDir)

	result, err := handoff.TransferReservations(ctx, client, opts)
	if err != nil {
		return result, err
	}
	return result, nil
}

// loadRecoveryCMMemories loads procedural memories from CM.
//
// The absolute `workingDir` is passed through to CM as the workspace scope so
// same-basename workspaces (e.g. `/clientA/app` and `/clientB/app`) do not
// share recovery memories — the basename-derived projectName alone would
// produce identical task text and silently bleed context across unrelated
// projects (#132).
func loadRecoveryCMMemories(ctx context.Context, workingDir string) (*RecoveryCMMemories, error) {
	client := cm.NewCLIClient()
	if !client.IsInstalled() {
		return nil, nil // Graceful degradation
	}

	// Get recovery context with reasonable limits. Use the absolute working
	// directory as the workspace scope so basename collisions across repos
	// are kept distinct.
	projectName := filepath.Base(workingDir)
	absWorkspace := workingDir
	if abs, err := filepath.Abs(workingDir); err == nil {
		absWorkspace = abs
	}
	result, err := client.GetRecoveryContext(ctx, projectName, absWorkspace, 10, 3)
	if err != nil {
		return nil, fmt.Errorf("get recovery context: %w", err)
	}
	if result == nil {
		return nil, nil
	}

	memories := &RecoveryCMMemories{}
	for _, r := range result.RelevantBullets {
		memories.Rules = append(memories.Rules, RecoveryCMRule{
			ID:       r.ID,
			Content:  r.Content,
			Category: r.Category,
		})
	}
	for _, r := range result.AntiPatterns {
		memories.AntiPatterns = append(memories.AntiPatterns, RecoveryCMRule{
			ID:       r.ID,
			Content:  r.Content,
			Category: r.Category,
		})
	}

	return memories, nil
}

// loadRecoveryCheckpoint loads the latest checkpoint for a session, but only
// when the checkpoint's recorded working directory matches the spawn's current
// working directory. This prevents a session-name collision across repos from
// cross-contaminating recovery context (#131).
//
// Working-dir matching rules:
//   - Both empty (legacy data): accept the checkpoint.
//   - Checkpoint has empty working_dir, current spawn has one: accept (legacy).
//   - Both non-empty: accept iff cleaned absolute paths are equal.
//   - Checkpoint has working_dir, current spawn has none: accept (caller did
//     not supply enough context to reject; they will be opting into trust).
func loadRecoveryCheckpoint(sessionName, workingDir string) (*RecoveryCheckpoint, error) {
	storage := checkpoint.NewStorage()
	cp, err := storage.GetLatest(sessionName)
	if err != nil {
		if errors.Is(err, checkpoint.ErrNoCheckpoints) {
			return nil, nil
		}
		return nil, err
	}
	if cp == nil {
		return nil, nil
	}

	if !checkpointWorkingDirMatches(cp.WorkingDir, workingDir) {
		// Recorded working dir disagrees with the spawn directory — refuse to
		// surface the checkpoint as recovery context for a different repo.
		// bd-1cr5k: emit a Warn-level diagnostic so operators investigating
		// "where did my recovery context go?" can correlate the silent drop
		// with a working-dir mismatch instead of staring at empty output.
		slog.Warn("recovery checkpoint rejected: working_dir mismatch",
			"session", sessionName,
			"checkpoint_working_dir", cp.WorkingDir,
			"spawn_working_dir", workingDir,
			"checkpoint_id", cp.ID,
			"hint", "checkpoint was recorded against a different repo with the same session name; recovery context is intentionally suppressed (#131)",
		)
		return nil, nil
	}

	assignSummary := summarizeCheckpointAssignments(cp.Assignments)
	var bvSummary *RecoveryBVSummary
	if cp.BVSummary != nil {
		bvSummary = &RecoveryBVSummary{
			OpenCount:       cp.BVSummary.OpenCount,
			ActionableCount: cp.BVSummary.ActionableCount,
			BlockedCount:    cp.BVSummary.BlockedCount,
			InProgressCount: cp.BVSummary.InProgressCount,
			TopPicks:        append([]string{}, cp.BVSummary.TopPicks...),
			CapturedAt:      cp.BVSummary.CapturedAt,
		}
	}

	return &RecoveryCheckpoint{
		ID:          cp.ID,
		Name:        cp.Name,
		Description: cp.Description,
		CreatedAt:   cp.CreatedAt,
		PaneCount:   cp.PaneCount,
		HasGitPatch: cp.HasGitPatch(),
		WorkingDir:  cp.WorkingDir,
		Assignments: assignSummary,
		BVSummary:   bvSummary,
	}, nil
}

// checkpointWorkingDirMatches returns true when the checkpoint's recorded
// working directory is compatible with the current spawn working directory.
// Empty values on either side are treated as a legacy match — only two
// non-empty paths that disagree are rejected.
func checkpointWorkingDirMatches(checkpointDir, spawnDir string) bool {
	checkpointDir = strings.TrimSpace(checkpointDir)
	spawnDir = strings.TrimSpace(spawnDir)
	if checkpointDir == "" || spawnDir == "" {
		return true
	}

	// bd-5n28k: resolve symlinks so the same physical directory reached via
	// two different paths (symlink, bind mount, macOS /Users vs
	// /private/var/Users, container workspace volumes) compares equal. Fall
	// back to the abs+clean path when EvalSymlinks fails (target gone,
	// permission denied) so we don't regress the symlink-free contract.
	canonical := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return filepath.Clean(p)
		}
		if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(abs)
	}

	return canonical(checkpointDir) == canonical(spawnDir)
}

func summarizeCheckpointAssignments(assignments []checkpoint.AssignmentSnapshot) *RecoveryAssignmentSummary {
	if len(assignments) == 0 {
		return nil
	}

	summary := &RecoveryAssignmentSummary{
		Total: len(assignments),
	}

	for _, a := range assignments {
		switch strings.ToLower(a.Status) {
		case "assigned":
			summary.Assigned++
		case "working":
			summary.Working++
		case "completed":
			summary.Completed++
		case "failed":
			summary.Failed++
		case "reassigned":
			summary.Reassigned++
		}
	}

	if summary.Total == 0 {
		return nil
	}
	return summary
}

// estimateRecoveryTokens estimates the token count of a recovery context.
// Uses a simple heuristic: ~4 characters per token.
func estimateRecoveryTokens(rc *RecoveryContext) int {
	if rc == nil {
		return 0
	}

	chars := 0

	// Count checkpoint
	if rc.Checkpoint != nil {
		chars += len(rc.Checkpoint.Name) + len(rc.Checkpoint.Description)
		if rc.Checkpoint.Assignments != nil {
			chars += 64
		}
		if rc.Checkpoint.BVSummary != nil {
			chars += 64
		}
	}

	// Count beads
	for _, b := range rc.Beads {
		chars += len(b.ID) + len(b.Title) + len(b.Assignee)
	}
	for _, b := range rc.CompletedBeads {
		chars += len(b.ID) + len(b.Title) + len(b.Assignee)
	}
	for _, b := range rc.BlockedBeads {
		chars += len(b.ID) + len(b.Title) + len(b.Assignee)
	}

	// Count messages
	for _, m := range rc.Messages {
		chars += len(m.From) + len(m.Subject) + len(m.Body)
	}

	// Count CM memories
	if rc.CMMemories != nil && (len(rc.CMMemories.Rules) > 0 || len(rc.CMMemories.AntiPatterns) > 0) {
		for _, r := range rc.CMMemories.Rules {
			chars += len(r.ID) + len(r.Content) + len(r.Category)
		}
		for _, r := range rc.CMMemories.AntiPatterns {
			chars += len(r.ID) + len(r.Content) + len(r.Category)
		}
	}

	// Count file reservations
	for _, f := range rc.FileReservations {
		chars += len(f)
	}

	// Count reservation transfer info
	if rc.ReservationTransfer != nil {
		chars += len(rc.ReservationTransfer.FromAgent) + len(rc.ReservationTransfer.ToAgent) + len(rc.ReservationTransfer.Error)
		for _, p := range rc.ReservationTransfer.RequestedPaths {
			chars += len(p)
		}
		for _, c := range rc.ReservationTransfer.Conflicts {
			chars += len(c.Path)
			for _, h := range c.Holders {
				chars += len(h)
			}
		}
	}

	// Add overhead for formatting
	chars += 500

	return chars / 4
}

// truncateRecoveryContext truncates the context to fit within maxTokens.
func truncateRecoveryContext(rc *RecoveryContext, maxTokens int) {
	if rc == nil {
		return
	}

	// Priority order for keeping content:
	// 1. In-progress beads (most important)
	// 2. Recent messages (important for coordination)
	// 3. File reservations (important for conflicts)
	// 4. CM memories (can be regenerated)
	// 5. Completed/blocked beads (nice to have)

	// Start by removing lowest priority items
	if estimateRecoveryTokens(rc) > maxTokens {
		rc.CompletedBeads = nil
		rc.BlockedBeads = nil
	}

	if estimateRecoveryTokens(rc) > maxTokens {
		rc.CMMemories = nil
	}

	if estimateRecoveryTokens(rc) > maxTokens && len(rc.Messages) > 5 {
		rc.Messages = rc.Messages[:5]
	}

	if estimateRecoveryTokens(rc) > maxTokens && len(rc.Messages) > 2 {
		rc.Messages = rc.Messages[:2]
	}

	rc.TokenCount = estimateRecoveryTokens(rc)
}

// generateRecoverySummary generates a human-readable summary of the recovery context.
func generateRecoverySummary(rc *RecoveryContext) string {
	if rc == nil {
		return ""
	}

	var parts []string

	if len(rc.Beads) > 0 {
		parts = append(parts, fmt.Sprintf("%d in-progress bead(s)", len(rc.Beads)))
	}
	if len(rc.Messages) > 0 {
		parts = append(parts, fmt.Sprintf("%d unread message(s)", len(rc.Messages)))
	}
	if len(rc.FileReservations) > 0 {
		parts = append(parts, fmt.Sprintf("%d file reservation(s)", len(rc.FileReservations)))
	}
	if rc.Checkpoint != nil && rc.Checkpoint.Assignments != nil && rc.Checkpoint.Assignments.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d assignment(s)", rc.Checkpoint.Assignments.Total))
	}
	if rc.Checkpoint != nil && rc.Checkpoint.BVSummary != nil {
		parts = append(parts, fmt.Sprintf("%d ready / %d blocked bead(s)",
			rc.Checkpoint.BVSummary.ActionableCount, rc.Checkpoint.BVSummary.BlockedCount))
	}
	if rc.ReservationTransfer != nil {
		if rc.ReservationTransfer.Success {
			parts = append(parts, fmt.Sprintf("reservations transferred (%d paths)", len(rc.ReservationTransfer.GrantedPaths)))
		} else if len(rc.ReservationTransfer.Conflicts) > 0 {
			parts = append(parts, fmt.Sprintf("reservation conflicts (%d)", len(rc.ReservationTransfer.Conflicts)))
		}
	}
	if rc.CMMemories != nil && (len(rc.CMMemories.Rules) > 0 || len(rc.CMMemories.AntiPatterns) > 0) {
		parts = append(parts, fmt.Sprintf("%d procedural memories", len(rc.CMMemories.Rules)+len(rc.CMMemories.AntiPatterns)))
	}

	if len(parts) == 0 {
		return "No recovery context available"
	}

	return strings.Join(parts, ", ")
}

// FormatRecoveryPrompt formats the full recovery context as a prompt injection.
// This combines beads, Agent Mail messages, file reservations, and CM memories
// into a single markdown section for agent injection.
// The agentType parameter controls formatting: Codex agents need brackets escaped
// because zsh interprets [] as glob patterns.
func FormatRecoveryPrompt(rc *RecoveryContext, agentType AgentType) string {
	if rc == nil {
		return ""
	}

	// escapeForShell escapes brackets for Codex agents where zsh interprets [] as globs
	escapeForShell := func(s string) string {
		if agentType == AgentTypeCodex {
			s = strings.ReplaceAll(s, "[", "\\[")
			s = strings.ReplaceAll(s, "]", "\\]")
		}
		return s
	}

	// Check if there's any meaningful content
	hasMeaningfulContent := len(rc.Beads) > 0 ||
		len(rc.CompletedBeads) > 0 ||
		len(rc.BlockedBeads) > 0 ||
		len(rc.Messages) > 0 ||
		len(rc.FileReservations) > 0 ||
		(rc.CMMemories != nil && (len(rc.CMMemories.Rules) > 0 || len(rc.CMMemories.AntiPatterns) > 0)) ||
		rc.Checkpoint != nil

	if !hasMeaningfulContent {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Session Recovery Context\n\n")

	// Your Previous Work section
	if rc.Checkpoint != nil || len(rc.Beads) > 0 || len(rc.FileReservations) > 0 {
		sb.WriteString("## Your Previous Work\n")

		if len(rc.Beads) > 0 {
			sb.WriteString(fmt.Sprintf("- You were working on: %s %s\n",
				escapeForShell("["+rc.Beads[0].ID+"]"),
				escapeForShell(rc.Beads[0].Title)))
		}

		if rc.Checkpoint != nil {
			sb.WriteString(fmt.Sprintf("- Last checkpoint: %s — %s\n",
				rc.Checkpoint.CreatedAt.Format("2006-01-02 15:04"),
				rc.Checkpoint.Description))
			if rc.Checkpoint.HasGitPatch {
				sb.WriteString("- Uncommitted changes: preserved in checkpoint\n")
			}
			if rc.Checkpoint.Assignments != nil && rc.Checkpoint.Assignments.Total > 0 {
				sb.WriteString(fmt.Sprintf("- Assignment summary: %d working, %d assigned, %d failed\n",
					rc.Checkpoint.Assignments.Working,
					rc.Checkpoint.Assignments.Assigned,
					rc.Checkpoint.Assignments.Failed))
			}
			if rc.Checkpoint.BVSummary != nil {
				sb.WriteString(fmt.Sprintf("- Beads summary: %d ready, %d blocked\n",
					rc.Checkpoint.BVSummary.ActionableCount,
					rc.Checkpoint.BVSummary.BlockedCount))
			}
		}

		if len(rc.FileReservations) > 0 {
			sb.WriteString("- Files you were editing: ")
			sb.WriteString(strings.Join(rc.FileReservations, ", "))
			sb.WriteString("\n")
		}
		if rc.ReservationTransfer != nil {
			if rc.ReservationTransfer.Success {
				sb.WriteString(fmt.Sprintf("- Reservation transfer: succeeded (%d paths)\n", len(rc.ReservationTransfer.GrantedPaths)))
			} else if len(rc.ReservationTransfer.Conflicts) > 0 {
				sb.WriteString(fmt.Sprintf("- Reservation transfer: conflicts (%d)\n", len(rc.ReservationTransfer.Conflicts)))
			} else if rc.ReservationTransfer.Error != "" {
				sb.WriteString("- Reservation transfer: failed\n")
			}
		}

		sb.WriteString("\n")
	}

	// Recent Messages section
	if len(rc.Messages) > 0 {
		sb.WriteString("## Recent Messages\n")
		for _, msg := range rc.Messages {
			sb.WriteString(fmt.Sprintf("\n### From %s: %s\n", msg.From, msg.Subject))
			if msg.Body != "" {
				sb.WriteString(msg.Body)
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}

	// Key Decisions from CM
	if rc.CMMemories != nil && len(rc.CMMemories.Rules) > 0 {
		sb.WriteString("## Key Decisions Made\n")
		for _, rule := range rc.CMMemories.Rules {
			sb.WriteString(fmt.Sprintf("- %s\n", rule.Content))
		}
		sb.WriteString("\n")
	}

	// Current Task Status
	if len(rc.Beads) > 0 || len(rc.CompletedBeads) > 0 || len(rc.BlockedBeads) > 0 {
		sb.WriteString("## Current Task Status\n")

		for _, bead := range rc.CompletedBeads {
			sb.WriteString(fmt.Sprintf("- %s Completed: %s %s\n",
				escapeForShell("[x]"),
				escapeForShell("["+bead.ID+"]"),
				escapeForShell(bead.Title)))
		}

		for _, bead := range rc.Beads {
			sb.WriteString(fmt.Sprintf("- %s In progress: %s %s\n",
				escapeForShell("[ ]"),
				escapeForShell("["+bead.ID+"]"),
				escapeForShell(bead.Title)))
		}

		for _, bead := range rc.BlockedBeads {
			sb.WriteString(fmt.Sprintf("- %s Blocked: %s %s\n",
				escapeForShell("[ ]"),
				escapeForShell("["+bead.ID+"]"),
				escapeForShell(bead.Title)))
		}

		sb.WriteString("\n")
	}

	sb.WriteString("Reread AGENTS.md and continue from where you left off.\n")

	return sb.String()
}

// SpawnAssignResult holds the combined result of spawn+assign workflow.
type SpawnAssignResult struct {
	Success   bool                  `json:"success"`
	ErrorCode string                `json:"error_code,omitempty"`
	Error     string                `json:"error,omitempty"`
	Errors    []string              `json:"errors,omitempty"`
	Spawn     *output.SpawnResponse `json:"spawn"`
	Init      *SpawnInitResult      `json:"init,omitempty"`
	Assign    *AssignOutputEnhanced `json:"assign,omitempty"`
}

func emitSpawnAssignJSON(result SpawnAssignResult) error {
	if result.Assign != nil && len(result.Assign.Errors) > 0 {
		result.Success = false
		result.ErrorCode = "ASSIGNMENT_FAILED"
		result.Errors = append([]string(nil), result.Assign.Errors...)
		result.Error = strings.Join(result.Errors, "; ")
		return emitJSONFailureEnvelope(result)
	}
	result.Success = true
	return output.PrintJSON(result)
}

// SpawnInitResult describes the init phase result.
type SpawnInitResult struct {
	PromptSent    bool                  `json:"prompt_sent"`
	AgentsReached int                   `json:"agents_reached"`
	Receipts      []dispatchsvc.Receipt `json:"receipts"`
}

// codexPreflightDecision is the outcome of the Codex/ChatGPT preflight for a
// spawn batch that requests a gpt-*-codex model on a ChatGPT-billed login.
type codexPreflightDecision int

const (
	codexAllow codexPreflightDecision = iota // proceed silently
	codexWarn                                // proceed, but surface a non-blocking advisory
	codexBlock                               // refuse the spawn (strict opt-in)
)

// decideCodexPreflight chooses how to handle a spawn that requests a
// gpt-*-codex model on a ChatGPT-billed Codex login.
//
// This is deliberately NOT a hard rejection by default. The original guard
// (ntm#142) assumed OpenAI rejects every gpt-*-codex id with HTTP 400 on
// ChatGPT-billed accounts, but that is not universally true: recent Codex CLI
// + ChatGPT plans run gpt-5.3-codex and answer prompts fine (proven in
// ntm#155), while only some accounts/plans/regions still get the 400. The
// local Codex CLI is the real source of truth, so blanket-refusing would strip
// capability from operators who can use the model. We default to a
// non-blocking advisory and let Codex return the real error if the account
// can't use it. Operators who want fail-fast (e.g. unattended swarms where a
// silently-rejecting pane is costly) opt into a hard block via strict.
func decideCodexPreflight(unsafeCodexOnChatGPT, strict bool) codexPreflightDecision {
	if !unsafeCodexOnChatGPT {
		return codexAllow
	}
	if strict {
		return codexBlock
	}
	return codexWarn
}

// preflightCodexAccountSupport advises (or, in strict mode, refuses) when a
// Codex agent in the spawn batch requests a gpt-*-codex model on a
// ChatGPT-billed login — the "pane looks alive but rejects the first prompt"
// failure mode some accounts hit (ntm#142). Because that failure is not
// universal (ntm#155), the default is a non-blocking advisory; see
// decideCodexPreflight.
//
// Detection: run `codex login status`. The CLI prints `Logged in using
// ChatGPT` for ChatGPT-billed accounts. When the local codex binary is missing
// or the command fails for any other reason, we silently allow the spawn — we
// don't want to break setups where the operator has a working flow we can't
// introspect.
func preflightCodexAccountSupportContext(ctx context.Context, agents []FlatAgent) error {
	if ctx == nil {
		return errors.New("codex preflight requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Escape hatches so tests and operators with bespoke Codex setups
	// can opt out entirely:
	//   - `NTM_DISABLE_CODEX_PREFLIGHT=1` skips the check (no advisory, no block)
	//   - presence of the `test.v` flag (i.e. `go test`) — the test
	//     harness has no business shelling out to a real codex CLI and
	//     test machines may legitimately be ChatGPT-billed without it
	//     being a real spawn.
	if os.Getenv("NTM_DISABLE_CODEX_PREFLIGHT") != "" {
		return nil
	}
	if flag.Lookup("test.v") != nil {
		return nil
	}

	// A Codex agent only risks the ChatGPT-billed-account failure mode if
	// the effective resolved model is a `gpt-*-codex` family id. A bare
	// `--cod=N` with `a.Model == ""` only means "no CLI override" — the
	// resolved model still comes from `cfg.Models.DefaultCodex` (or, if
	// that's unset, from `config.DefaultModels().DefaultCodex`). The default
	// is a non-`-codex` GPT model, so a bare `--cod=N` is fine even on a
	// ChatGPT account (#147); this only fires for an explicit gpt-*-codex
	// choice.
	hasUnsafeCodex := false
	for _, a := range agents {
		if a.Type != AgentTypeCodex {
			continue
		}
		if isCodexFamilyModel(effectiveCodexModel(a.Model)) {
			hasUnsafeCodex = true
			break
		}
	}
	if !hasUnsafeCodex {
		return nil
	}

	// `codex login status` is fast (<1s) but bound the wait anyway so a
	// broken/hung binary doesn't stall the spawn indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "login", "status").CombinedOutput()
	if err != nil {
		// codex CLI missing or errored — don't second-guess the operator's setup.
		return nil
	}
	isChatGPT := strings.Contains(string(out), "Logged in using ChatGPT")

	strict := strings.TrimSpace(os.Getenv("NTM_CODEX_PREFLIGHT_STRICT")) != ""
	n := countDefaultCodex(agents)
	switch decideCodexPreflight(hasUnsafeCodex && isChatGPT, strict) {
	case codexBlock:
		return fmt.Errorf(
			"refusing to spawn %d `gpt-*-codex` Codex agent(s) on a ChatGPT-billed account (NTM_CODEX_PREFLIGHT_STRICT is set). Some ChatGPT plans reject `gpt-*-codex` with HTTP 400; if yours supports it, unset NTM_CODEX_PREFLIGHT_STRICT, or use `--cod=%d:%s` / a Codex API key (see ntm#155, ntm#142)",
			n, n, config.DefaultCodexModel,
		)
	case codexWarn:
		if !IsJSONOutput() {
			output.PrintWarningf(
				"%d Codex agent(s) request a `gpt-*-codex` model on a ChatGPT-billed login. Most ChatGPT plans run it fine, but some accounts get HTTP 400 — if a pane stays alive yet rejects the first prompt, switch to `--cod=N:%s` or use a Codex API key (see ntm#155). Set NTM_CODEX_PREFLIGHT_STRICT=1 to fail fast instead.",
				n, config.DefaultCodexModel,
			)
		}
	}
	return nil
}

func countDefaultCodex(agents []FlatAgent) int {
	n := 0
	for _, a := range agents {
		if a.Type == AgentTypeCodex && isCodexFamilyModel(effectiveCodexModel(a.Model)) {
			n++
		}
	}
	return n
}

// effectiveCodexModel resolves the Codex model that will actually be
// used for an agent: the explicit CLI override if present, otherwise the
// configured default, otherwise the compiled-in default. Mirrors the
// resolution in modelNameForPane so the preflight check and the actual
// spawn agree on which model the agent will run as.
func effectiveCodexModel(cliModel string) string {
	if cliModel != "" {
		return cliModel
	}
	if cfg != nil && cfg.Models.DefaultCodex != "" {
		return cfg.Models.DefaultCodex
	}
	return config.DefaultModels().DefaultCodex
}

// isCodexFamilyModel reports whether a model id is a `gpt-*-codex`
// family id that OpenAI rejects with HTTP 400 on ChatGPT-billed
// accounts. The check is suffix-based ("-codex") so it matches
// gpt-5-codex, gpt-5.2-codex, gpt-5.3-codex, and any future
// gpt-5.X-codex without an explicit allow-list update. Plain
// `gpt-5`, `gpt-5.6-sol`, etc. do not match and are considered safe.
func isCodexFamilyModel(model string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-codex")
}

// worktreeAgentName builds the directory-name component for an agent's
// worktree. The default is `<agent-type>_<index>` (e.g., `cc_1`), but when
// `override` is non-empty (set via `--worktree-name`), that takes
// precedence. The override is the escape hatch for external orchestrators
// that spawn the same agent slot across multiple `--label` values: see
// ntm#145 for the cross-contamination scenario.
func worktreeAgentName(a FlatAgent, override string) string {
	if override != "" {
		return override
	}
	return fmt.Sprintf("%s_%d", strings.ToLower(string(a.Type)), a.Index)
}

func waitForAgentsReadyWithObserver(
	ctx context.Context,
	session string,
	timeout, pollInterval time.Duration,
	observer spawnSessionObserver,
) (int, error) {
	if observer == nil {
		return 0, errors.New("waiting for agents requires a session observer")
	}
	if ctx == nil {
		return 0, errors.New("waiting for agents requires a command context")
	}
	if pollInterval <= 0 {
		pollInterval = spawnReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	lastReady := 0
	lastAgents := 0
	var lastObservation statuspkg.SessionObservation
	var lastObserveErr error

	for {
		observation, observeErr := observer.Observe(ctx, session)
		lastObservation = observation
		lastObserveErr = observeErr
		readyPanes, agentCount := readyAgentPanesFromObservation(observation)
		lastReady = len(readyPanes)
		lastAgents = agentCount
		if observeErr == nil && agentCount > 0 && lastReady == agentCount {
			return lastReady, nil
		}
		if !time.Now().Before(deadline) {
			return lastReady, spawnReadinessError(
				fmt.Sprintf("timeout waiting for agents to become ready (%d/%d ready)", lastReady, lastAgents),
				lastObservation, lastObserveErr, "",
			)
		}
		if err := waitUntilNextSpawnPoll(ctx, deadline, pollInterval); err != nil {
			return lastReady, fmt.Errorf("waiting for agents to become ready: %w", err)
		}
	}
}

// When `withAgentName` is true, each pane receives a topology-unique per-pane
// identity preamble. Multi-window sessions include both window and pane index.
// See ntm#138.
func sendInitPromptToReadyAgentsWith(
	ctx context.Context,
	projectDir, session, prompt string,
	withAgentName bool,
	observer spawnSessionObserver,
	dispatcher spawnPromptDispatcher,
) ([]dispatchsvc.Receipt, error) {
	if ctx == nil {
		return nil, errors.New("init prompt delivery requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" {
		return []dispatchsvc.Receipt{}, nil
	}
	if observer == nil || dispatcher == nil {
		return nil, errors.New("init prompt delivery requires observer and dispatcher")
	}
	observation, observeErr := observer.Observe(ctx, session)
	if observeErr != nil {
		return nil, fmt.Errorf("observe init prompt targets: %w", observeErr)
	}
	if !statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) {
		return nil, errors.New("init prompt target observation is stale")
	}
	readyPanes, agentCount := readyAgentPanesFromObservation(observation)
	observedAgentPanes := make([]tmux.Pane, 0, agentCount)
	for _, pane := range observation.Panes {
		agentType := detectAgentTypeFromPane(pane.Metadata)
		if agentType != "user" && agentType != "unknown" {
			observedAgentPanes = append(observedAgentPanes, pane.Metadata)
		}
	}
	multiWindow := tmux.PanesSpanMultipleWindows(observedAgentPanes)
	receipts := make([]dispatchsvc.Receipt, 0, len(readyPanes))
	errs := readinessIssuesForAgentPanes(observation)
	for _, pane := range readyPanes {
		paneAgentType := detectAgentTypeFromPane(pane)
		perPanePrompt := prompt
		if withAgentName {
			name := assignmentAgentIdentityForPane(projectDir, session, paneAgentType, pane, multiWindow)
			if name != "" {
				perPanePrompt = fmt.Sprintf(
					"You are agent `%s`. Use this name when registering with Agent Mail or referring to yourself.\n\n%s",
					name, prompt,
				)
			}
		}
		receipt, err := dispatcher.Dispatch(ctx, pane.ID, perPanePrompt)
		if err != nil {
			errs = append(errs, fmt.Sprintf("pane %d: %v", pane.Index, err))
			continue
		}
		receipts = append(receipts, receipt)
	}
	if agentCount == 0 {
		errs = append(errs, "no agent panes were observed")
	} else if len(readyPanes) == 0 && len(errs) == 0 {
		errs = append(errs, "no agent pane was freshly and confidently idle")
	}
	if len(errs) > 0 {
		return receipts, fmt.Errorf("init prompt delivery issues: %s", strings.Join(errs, "; "))
	}
	return receipts, nil
}

func readyAgentPanesFromObservation(observation statuspkg.SessionObservation) ([]tmux.Pane, int) {
	readyPanes := make([]tmux.Pane, 0, len(observation.Panes))
	agentCount := 0
	for _, pane := range observation.Panes {
		agentType := detectAgentTypeFromPane(pane.Metadata)
		if agentType == "user" || agentType == "unknown" {
			continue
		}
		agentCount++
		if spawnPaneObservationSafeToDispatch(pane) && statuspkg.DispatchObservationIsCurrent(pane.Current.ObservedAt, time.Now()) {
			readyPanes = append(readyPanes, pane.Metadata)
		}
	}
	return readyPanes, agentCount
}

func readinessIssuesForAgentPanes(observation statuspkg.SessionObservation) []string {
	issues := make([]string, 0)
	for _, pane := range observation.Panes {
		agentType := detectAgentTypeFromPane(pane.Metadata)
		if agentType == "user" || agentType == "unknown" || spawnPaneObservationSafeToDispatch(pane) {
			continue
		}
		if pane.SafeToDispatch() && spawnPaneCommandIsShell(pane.Metadata.Command) {
			issues = append(issues, fmt.Sprintf(
				"pane %d agent process has not replaced shell %q", pane.Metadata.Index, pane.Metadata.Command,
			))
			continue
		}
		if pane.Current.Error != "" {
			issues = append(issues, fmt.Sprintf("pane %d capture: %s", pane.Metadata.Index, pane.Current.Error))
			continue
		}
		issues = append(issues, fmt.Sprintf(
			"pane %d is not ready (state=%s freshness=%s confidence=%.2f)",
			pane.Metadata.Index, pane.Current.Status.State, pane.Current.Freshness, pane.Current.Confidence,
		))
	}
	return issues
}

func waitForSpawnPaneReady(
	ctx context.Context,
	session, paneID string,
	timeout, pollInterval time.Duration,
	observer spawnSessionObserver,
) error {
	if observer == nil {
		return errors.New("waiting for pane readiness requires a session observer")
	}
	if ctx == nil {
		return errors.New("waiting for pane readiness requires a command context")
	}
	if pollInterval <= 0 {
		pollInterval = spawnReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	var lastObservation statuspkg.SessionObservation
	var lastObserveErr error
	for {
		observation, observeErr := observer.Observe(ctx, session)
		lastObservation = observation
		lastObserveErr = observeErr
		if observeErr == nil &&
			statuspkg.DispatchObservationIsCurrent(observation.ObservedAt, time.Now()) &&
			spawnObservationSafeToDispatch(observation, paneID) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return spawnReadinessError(
				fmt.Sprintf("timeout waiting for pane %s to become ready", paneID),
				lastObservation, lastObserveErr, paneID,
			)
		}
		if err := waitUntilNextSpawnPoll(ctx, deadline, pollInterval); err != nil {
			return fmt.Errorf("waiting for pane %s to become ready: %w", paneID, err)
		}
	}
}

func spawnObservationSafeToDispatch(observation statuspkg.SessionObservation, paneID string) bool {
	pane, ok := observation.PaneByID(paneID)
	return ok && spawnPaneObservationSafeToDispatch(pane)
}

func spawnPaneObservationSafeToDispatch(pane statuspkg.PaneObservation) bool {
	if !pane.SafeToDispatch() || strings.TrimSpace(pane.RawOutput) == "" {
		return false
	}
	parsed, err := agentpkg.NewParser().ParseWithHint(pane.RawOutput, agentpkg.AgentType(pane.AgentType))
	return err == nil && parsed.IsIdle && !parsed.IsWorking && !parsed.IsRateLimited && !parsed.IsInError
}

func spawnPaneCommandIsShell(command string) bool {
	return tmux.PaneCommandIsShell(command)
}

func waitUntilNextSpawnPoll(ctx context.Context, deadline time.Time, pollInterval time.Duration) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil
	}
	if pollInterval > remaining {
		pollInterval = remaining
	}
	return waitContextDelay(ctx, pollInterval)
}

func spawnReadinessError(prefix string, observation statuspkg.SessionObservation, observeErr error, paneID string) error {
	details := make([]string, 0)
	if observeErr != nil {
		details = append(details, "observation: "+observeErr.Error())
	}
	for _, failure := range observation.Failures {
		if paneID != "" && failure.PaneID != "" && failure.PaneID != paneID {
			continue
		}
		target := "session"
		if failure.PaneID != "" {
			target = "pane " + failure.PaneID
		}
		details = append(details, fmt.Sprintf("%s %s: %s", target, failure.Stage, failure.Error))
	}
	if paneID == "" {
		details = append(details, readinessIssuesForAgentPanes(observation)...)
	}
	if paneID != "" {
		if pane, ok := observation.PaneByID(paneID); ok && pane.Current.Error == "" {
			if spawnPaneCommandIsShell(pane.Metadata.Command) {
				details = append(details, fmt.Sprintf("agent process has not replaced shell %q", pane.Metadata.Command))
			}
			details = append(details, fmt.Sprintf(
				"last state=%s freshness=%s confidence=%.2f",
				pane.Current.Status.State, pane.Current.Freshness, pane.Current.Confidence,
			))
		}
	}
	if len(details) == 0 {
		return errors.New(prefix)
	}
	return fmt.Errorf("%s; %s", prefix, strings.Join(details, "; "))
}

func runAssignmentPhaseContext(ctx context.Context, session string, opts SpawnOptions) (*AssignOutputEnhanced, error) {
	if ctx == nil {
		return nil, errors.New("assignment requires a command context")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("assignment canceled: %w", err)
	}
	// Use opts.AssignVerbose/Quiet if explicitly set, otherwise use JSON-based defaults
	verbose := opts.AssignVerbose
	quiet := opts.AssignQuiet
	if !opts.AssignVerbose && !opts.AssignQuiet {
		// Neither explicitly set, use defaults based on output mode
		verbose = !IsJSONOutput()
		quiet = IsJSONOutput()
	}

	// Use opts.AssignTimeout if set, otherwise use default
	timeout := opts.AssignTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	assignOpts := spawnAssignCommandOptions(session, opts, verbose, quiet, timeout)

	// Get assignment recommendations
	assignOutput, err := getAssignOutputEnhanced(ctx, assignOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignments: %w", err)
	}

	// Execute assignments (send prompts to agents)
	if err := executeAssignmentsEnhanced(ctx, session, assignOutput, assignOpts); err != nil {
		return nil, fmt.Errorf("failed to execute assignments: %w", err)
	}

	return assignOutput, nil
}

func spawnAssignCommandOptions(session string, opts SpawnOptions, verbose, quiet bool, timeout time.Duration) *AssignCommandOptions {
	assignOpts := &AssignCommandOptions{
		Session:         session,
		Strategy:        opts.AssignStrategy,
		Limit:           opts.AssignLimit,
		AgentTypeFilter: opts.AssignAgentType,
		Verbose:         verbose,
		Quiet:           quiet,
		Timeout:         timeout,
	}
	if opts.assignAdmission != nil {
		assignOpts.ProjectDir = opts.assignAdmission.projectDir
		assignOpts.policyProject = opts.assignAdmission.projectDir
		assignOpts.actionablePreflightVerified = true
		assignOpts.verifiedActionable = cloneSpawnActionableRecommendations(opts.assignAdmission.actionable)
	}
	return assignOpts
}
