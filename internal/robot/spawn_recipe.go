package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// A recipe supplies counts and per-instance launch specifications, not another
// spawn engine. Keys use the shared engine's agent type and per-type ordinal;
// grouping by type therefore preserves ordinary spawn's pane/name ordering.
type spawnRecipeKey struct {
	agentType string
	number    int
}

type spawnRecipeLaunch struct {
	command     string
	model       spawnLaunchModel
	environment map[string]string
}

// prepareSpawnPlugins resolves aliases and freezes installed manifests before
// recipe loading or pane mutation. A registered name alone is insufficient:
// the selected config must actually supply its launch definition.
func prepareSpawnPlugins(opts SpawnOptions) (SpawnOptions, error) {
	if len(opts.PluginAgents) == 0 && opts.Preset == "" {
		return opts, nil
	}
	path := opts.ConfigPath
	if path == "" {
		path = config.DefaultPath()
	}
	loaded, err := plugins.LoadAgentPlugins(filepath.Join(filepath.Dir(path), "agents"))
	if err != nil {
		if len(opts.PluginAgents) == 0 {
			return opts, nil // Built-in recipes do not depend on plugin files.
		}
		return opts, fmt.Errorf("load spawn agent plugins: %w", err)
	}
	definitions := make(map[string]plugins.AgentPlugin)
	definitionErrors := make(map[string]error)
	for _, plugin := range loaded {
		plugin.Name = strings.ToLower(strings.TrimSpace(plugin.Name))
		if agentpkg.AgentType(plugin.Name).IsValid() || plugin.Name == "unknown" {
			continue // Shipped compatibility presets never override built-ins.
		}
		if _, duplicate := definitions[plugin.Name]; duplicate {
			definitionErrors[plugin.Name] = fmt.Errorf("agent plugin name %q is ambiguous", plugin.Name)
			continue
		}
		for _, raw := range []string{plugin.Name, plugin.Alias} {
			name := strings.ToLower(strings.TrimSpace(raw))
			if name == "" || name == "unknown" || agentpkg.AgentType(name).IsValid() {
				continue
			}
			if previous, exists := definitions[name]; exists && previous.Name != plugin.Name {
				definitionErrors[name] = fmt.Errorf("agent plugin name or alias %q is ambiguous", name)
				continue
			}
			if err := agentpkg.RegisterPlugin(name, plugin.Readiness.IdlePatterns, plugin.Readiness.WorkingPatterns, plugin.Readiness.ErrorPatterns); err != nil {
				definitionErrors[name] = err
				// Keep the type available to recipe decoding, matching the CLI's
				// fallback. Selecting this definition still fails before launch.
				_ = agentpkg.RegisterPlugin(name, nil, nil, nil)
			}
			definitions[name] = plugin
		}
	}
	var requested map[string]PluginSpawnSpec
	if len(opts.PluginAgents) > 0 {
		requested = make(map[string]PluginSpawnSpec, len(opts.PluginAgents))
	}
	for raw, spec := range opts.PluginAgents {
		name := strings.ToLower(strings.TrimSpace(raw))
		if err := definitionErrors[name]; err != nil {
			return opts, err
		}
		plugin, ok := definitions[name]
		if !ok {
			return opts, fmt.Errorf("spawn agent plugin %q is not installed in the selected config", raw)
		}
		if err := definitionErrors[plugin.Name]; err != nil {
			return opts, err
		}
		if _, duplicate := requested[plugin.Name]; duplicate {
			return opts, fmt.Errorf("spawn agent plugin %q was selected more than once by name or alias", plugin.Name)
		}
		requested[plugin.Name] = spec
	}
	opts.PluginAgents = requested
	opts.pluginDefinitions = definitions
	opts.pluginErrors = definitionErrors
	return opts, nil
}

// renderSpawnPlugin prepares each slot, including variables that differ by
// pane. Commands and metadata exclude environment values; the launcher sends
// those through the existing private environment transport.
func renderSpawnPlugin(opts SpawnOptions, cfg *config.Config, name string, spec PluginSpawnSpec, number int, dir string) (spawnRecipeLaunch, error) {
	if err := opts.pluginErrors[name]; err != nil {
		return spawnRecipeLaunch{}, err
	}
	plugin, ok := opts.pluginDefinitions[name]
	if !ok {
		return spawnRecipeLaunch{}, fmt.Errorf("spawn agent plugin %q has no frozen launch definition", name)
	}
	if err := tmux.ValidateLaunchEnvironment(plugin.Env); err != nil {
		return spawnRecipeLaunch{}, fmt.Errorf("plugin %s environment: %w", name, err)
	}
	model := spawnLaunchModel{ModelAlias: spec.Model, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}
	if cfg != nil {
		model.Model = cfg.Models.GetModelName(name, spec.Model)
	}
	if model.Model == "" {
		model.Model = strings.TrimSpace(plugin.Defaults.Model)
	}
	command, err := config.GenerateAgentCommand(plugin.Command, config.AgentTemplateVars{
		AgentType: name, Model: model.Model, ModelAlias: model.ModelAlias,
		ModelRequested: spec.Model != "", ReasoningEffort: spec.ReasoningEffort,
		ReasoningEffortRequested: spec.ReasoningEffort != "",
		SessionName:              opts.Session, PaneIndex: number, ProjectDir: dir,
	})
	if err != nil {
		return spawnRecipeLaunch{}, fmt.Errorf("rendering %s launch command: %w", name, err)
	}
	command, err = tmux.SanitizePaneCommand(command)
	if err != nil || strings.TrimSpace(command) == "" {
		return spawnRecipeLaunch{}, fmt.Errorf("plugin %s rendered an empty or invalid launch command", name)
	}
	return spawnRecipeLaunch{command: command, model: model, environment: plugin.Env}, nil
}

func renderSpawnPluginRequests(opts SpawnOptions, cfg *config.Config, dir string) (map[spawnRecipeKey]spawnRecipeLaunch, error) {
	var launches map[spawnRecipeKey]spawnRecipeLaunch
	if len(opts.PluginAgents) > 0 {
		launches = make(map[spawnRecipeKey]spawnRecipeLaunch)
	}
	names := make([]string, 0, len(opts.PluginAgents))
	for name := range opts.PluginAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := opts.PluginAgents[name]
		for number := 1; number <= spec.Count; number++ {
			launch, err := renderSpawnPlugin(opts, cfg, name, spec, number, dir)
			if err != nil {
				return nil, err
			}
			launches[spawnRecipeKey{agentType: name, number: number}] = launch
		}
	}
	return launches, nil
}

func spawnWorkingDirectory(opts SpawnOptions, cfg *config.Config) (string, error) {
	if opts.WorkingDir != "" {
		return opts.WorkingDir, nil
	}
	if cfg != nil {
		if dir := cfg.GetProjectDir(opts.Session); dir != "" {
			return dir, nil
		}
	}
	return os.Getwd()
}

// loadSpawnRecipe freezes the selected recipe before any tmux mutation. The
// existing loader owns builtin < user < project precedence and strict decoding.
// In particular, project recipes come from the launch directory, not serve's CWD.
func loadSpawnRecipe(opts SpawnOptions, cfg *config.Config) (SpawnOptions, *recipe.Recipe, error) {
	if opts.Preset == "" {
		return opts, nil, nil
	}
	name := strings.TrimSpace(opts.Preset)
	if name == "" {
		return opts, nil, errors.New("spawn preset must name a recipe")
	}
	// Adding counts to a preset is ambiguous (replace, add, or multiply).
	// Refuse rather than silently ignoring either explicit request.
	for _, spec := range opts.agentCounts() {
		if spec.count != 0 {
			return opts, nil, errors.New("spawn preset cannot be combined with explicit agent counts")
		}
	}
	dir, err := spawnWorkingDirectory(opts, cfg)
	if err != nil {
		return opts, nil, fmt.Errorf("resolve preset project: %w", err)
	}
	loader := recipe.NewLoader()
	loader.ProjectDir = dir
	selected, err := loader.Get(name)
	if err != nil {
		return opts, nil, fmt.Errorf("load spawn preset %q: %w", name, err)
	}
	expanded, err := expandSpawnRecipe(opts, selected)
	if err != nil {
		return opts, nil, err
	}
	// Reuse exactly the same directory for loading, admission, and execution.
	expanded.WorkingDir = dir
	frozen := *selected
	frozen.Agents = append([]recipe.AgentSpec(nil), selected.Agents...)
	return expanded, &frozen, nil
}

func expandSpawnRecipe(opts SpawnOptions, selected *recipe.Recipe) (SpawnOptions, error) {
	if selected == nil {
		return opts, errors.New("spawn preset returned no recipe")
	}
	if err := selected.Validate(); err != nil {
		return opts, fmt.Errorf("invalid spawn preset: %w", err)
	}
	// Work on a value copy so failure never partially rewrites the request.
	expanded := opts
	expanded.CCCount, expanded.CodCount, expanded.GmiCount, expanded.AgyCount = 0, 0, 0, 0
	expanded.GrokCount, expanded.OmpCount, expanded.OcCount = 0, 0, 0
	expanded.PluginAgents = nil
	total := 0
	for i, spec := range selected.Agents {
		if strings.TrimSpace(spec.Persona) != "" {
			return opts, fmt.Errorf("spawn preset %q agent[%d]: personas are not supported by robot spawning; use ntm spawn --recipe", selected.Name, i)
		}
		// Validate above bounds each count, but independently bound the sum
		// before addition rather than trusting Recipe.TotalAgents arithmetic.
		if spec.Count > 50-total {
			return opts, fmt.Errorf("spawn preset %q exceeds 50 agents", selected.Name)
		}
		total += spec.Count
		switch tmux.AgentType(spec.Type).Canonical() {
		case tmux.AgentClaude:
			expanded.CCCount += spec.Count
		case tmux.AgentCodex:
			expanded.CodCount += spec.Count
		case tmux.AgentGemini:
			expanded.GmiCount += spec.Count
		case tmux.AgentAntigravity:
			expanded.AgyCount += spec.Count
		case tmux.AgentGrok:
			expanded.GrokCount += spec.Count
		case tmux.AgentOmp:
			expanded.OmpCount += spec.Count
		case tmux.AgentOpencode:
			expanded.OcCount += spec.Count
		default:
			name := strings.ToLower(strings.TrimSpace(spec.Type))
			if err := opts.pluginErrors[name]; err != nil {
				return opts, fmt.Errorf("spawn preset %q agent[%d]: %w", selected.Name, i, err)
			}
			plugin, ok := opts.pluginDefinitions[name]
			if !ok {
				return opts, fmt.Errorf("spawn preset %q agent[%d]: robot spawning does not support agent type %q", selected.Name, i, spec.Type)
			}
			if expanded.PluginAgents == nil {
				expanded.PluginAgents = make(map[string]PluginSpawnSpec)
			}
			request := expanded.PluginAgents[plugin.Name]
			request.Count += spec.Count
			expanded.PluginAgents[plugin.Name] = request
		}
	}
	expanded.Preset = selected.Name
	return expanded, nil
}

// renderSpawnRecipe runs after the authoritative assignment configuration (when
// requested) is loaded, and before admission/lifecycle mutation. Every slot is
// rendered now: a bad model/effort in the last entry cannot leave half a fleet.
// Explicit request model/effort overrides win over the recipe, independently.
func renderSpawnRecipe(opts SpawnOptions, selected *recipe.Recipe, cfg *config.Config) (map[spawnRecipeKey]spawnRecipeLaunch, error) {
	if selected == nil {
		return nil, nil
	}
	if cfg == nil {
		// Unconfigured embedding callers still need real default templates to
		// honor recipe models; naked "claude"/"codex" cannot render overrides.
		// This does not implicitly read or replace their admission configuration.
		cfg = config.Default()
	}
	launches := make(map[spawnRecipeKey]spawnRecipeLaunch)
	ordinals := make(map[string]int)
	for i, spec := range selected.Agents {
		if plugin, ok := opts.pluginDefinitions[strings.ToLower(strings.TrimSpace(spec.Type))]; ok {
			for n := 0; n < spec.Count; n++ {
				ordinals[plugin.Name]++
				request := PluginSpawnSpec{Count: 1, Model: strings.TrimSpace(spec.Model), ReasoningEffort: strings.TrimSpace(spec.ReasoningEffort)}
				launch, err := renderSpawnPlugin(opts, cfg, plugin.Name, request, ordinals[plugin.Name], opts.WorkingDir)
				if err != nil {
					return nil, fmt.Errorf("spawn preset %q agent[%d]: %w", selected.Name, i, err)
				}
				launches[spawnRecipeKey{agentType: plugin.Name, number: ordinals[plugin.Name]}] = launch
			}
			continue
		}
		perType := opts
		model, effort := strings.TrimSpace(spec.Model), strings.TrimSpace(spec.ReasoningEffort)
		var modelOverride, effortOverride *string
		kind := ""
		switch tmux.AgentType(spec.Type).Canonical() {
		case tmux.AgentClaude:
			kind, modelOverride, effortOverride = "claude", &perType.CCModel, &perType.CCReasoningEffort
		case tmux.AgentCodex:
			kind, modelOverride, effortOverride = "codex", &perType.CodModel, &perType.CodReasoningEffort
		case tmux.AgentGemini:
			kind, modelOverride = "gemini", &perType.GmiModel
		case tmux.AgentAntigravity:
			kind = "antigravity"
		case tmux.AgentGrok:
			kind, modelOverride, effortOverride = "grok", &perType.GrokModel, &perType.GrokReasoningEffort
		case tmux.AgentOmp:
			kind, modelOverride, effortOverride = "omp", &perType.OmpModel, &perType.OmpReasoningEffort
		case tmux.AgentOpencode:
			kind, modelOverride = string(tmux.AgentOpencode), &perType.OcModel
		default:
			return nil, fmt.Errorf("spawn preset %q agent[%d]: unsupported robot agent type %q", selected.Name, i, spec.Type)
		}
		if (model != "" && modelOverride == nil) || (effort != "" && effortOverride == nil) {
			return nil, fmt.Errorf("spawn preset %q agent[%d]: %s does not support the supplied model/effort override", selected.Name, i, kind)
		}
		if modelOverride != nil && *modelOverride == "" {
			*modelOverride = model
		}
		if effortOverride != nil && *effortOverride == "" {
			*effortOverride = effort
		}
		commands, err := getAgentCommandsWithOverrides(cfg, perType)
		if err != nil {
			return nil, fmt.Errorf("spawn preset %q agent[%d]: %w", selected.Name, i, err)
		}
		launch := spawnRecipeLaunch{command: commands[kind], model: spawnLaunchModels(cfg, perType)[kind]}
		for n := 0; n < spec.Count; n++ {
			ordinals[kind]++
			launches[spawnRecipeKey{agentType: kind, number: ordinals[kind]}] = launch
		}
	}
	return launches, nil
}

func applySpawnRecipePreview(agents []SpawnedAgent, launches map[spawnRecipeKey]spawnRecipeLaunch) {
	ordinals := make(map[string]int)
	for i := range agents {
		kind := agents[i].Type
		if kind == "user" {
			continue
		}
		ordinals[kind]++
		if launch, ok := launches[spawnRecipeKey{agentType: kind, number: ordinals[kind]}]; ok {
			agents[i].Variant = launch.model.ModelAlias
		}
	}
}

// The final resolved model is immutable per launch. Carry it with the existing
// lifecycle context so progress/pacing/readiness decorators cannot accidentally
// replace a model-aware launcher with an unconfigured launcher. Cancellation,
// deadlines and durable job reporters are inherited from the same parent.
type spawnLaunchModelContextKey struct{}

type spawnPluginEnvironmentContextKey struct{}

func spawnLaunchModelFromContext(ctx context.Context) spawnLaunchModel {
	if ctx == nil {
		return spawnLaunchModel{}
	}
	model, _ := ctx.Value(spawnLaunchModelContextKey{}).(spawnLaunchModel)
	return model
}
