package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/gemini"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	*v.duration = dur
	return nil
}

func (v *optionalDurationValue) Type() string {
	return "duration"
}

// IsBoolFlag allows --stagger without =value
func (v *optionalDurationValue) IsBoolFlag() bool {
	return false
}

// NoOptDefVal is the default when --stagger is used without a value
func (v *optionalDurationValue) NoOptDefVal() string {
	return v.defaultDuration.String()
}

// SpawnOptions configures session creation and agent spawning
type SpawnOptions struct {
	Session     string
	Agents      []FlatAgent
	CCCount     int
	CodCount    int
	GmiCount    int
	UserPane    bool
	AutoRestart bool
	RecipeName  string
	PersonaMap  map[string]*persona.Persona
	PluginMap   map[string]plugins.AgentPlugin

	// Profile mapping: list of persona names to map to agents in order
	ProfileList []*persona.Persona

	// CASS Context
	CassContextQuery string
	NoCassContext    bool
	Prompt           string

	// Hooks
	NoHooks bool

	// Safety mode: fail if session already exists
	Safety bool

	// Stagger configuration for thundering herd prevention
	Stagger        time.Duration // Delay between agent prompt delivery
	StaggerEnabled bool          // True if --stagger flag was provided
}

func newSpawnCmd() *cobra.Command {
	var noUserPane bool
	var recipeName string
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs
	var autoRestart bool
	var contextQuery string
	var noCassContext bool
	var contextLimit int
	var contextDays int
	var prompt string
	var noHooks bool
	var profilesFlag string
	var profileSetFlag string
	var staggerDuration time.Duration
	var staggerEnabled bool
	var safety bool

	// Pre-load plugins to avoid double loading in RunE
	// TODO: This runs eagerly during init() which slows down startup for all commands.
	// Fixing this requires refactoring how dynamic flags are registered.
	configDir := filepath.Dir(config.DefaultPath())
	pluginsDir := filepath.Join(configDir, "agents")
	loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
	preloadedPluginMap := make(map[string]plugins.AgentPlugin)
	for _, p := range loadedPlugins {
		preloadedPluginMap[p.Name] = p
	}

	cmd := &cobra.Command{
		Use:   "spawn <session-name>",
		Short: "Create session and spawn AI agents in panes",
		Long: `Create a new tmux session and launch AI coding agents in separate panes.

By default, the first pane is reserved for the user. Agent panes are created
and titled with their type (e.g., myproject__cc_1, myproject__cod_1).

You can use a recipe to quickly spawn a predefined set of agents:
  ntm spawn myproject -r full-stack    # Use the 'full-stack' recipe

Agent count syntax: N or N:model where N is count and model is optional.
Multiple flags of the same type accumulate.

Built-in recipes: quick-claude, full-stack, minimal, codex-heavy, balanced, review-team
Use 'ntm recipes list' to see all available recipes.

Auto-restart mode (--auto-restart):
  Monitors agent health and automatically restarts crashed agents.
  Configure via [resilience] section in config.toml:
    max_restarts = 3         # Max restart attempts per agent
    restart_delay_seconds = 30  # Delay before restart
    health_check_seconds = 10   # Health check interval

Persona mode:
  Use --persona to spawn agents with predefined roles and system prompts.
  Format: --persona=name or --persona=name:count
  Built-in personas: architect, implementer, reviewer, tester, documenter

CASS Context Injection:
  Automatically finds relevant past sessions and injects context into agents.
  Use --cass-context="query" to be specific, or rely on prompt/recipe context.

Stagger mode (--stagger):
  Prevents thundering herd when agents receive identical prompts. All panes
  are created immediately for dashboard visibility, but prompts are delivered
  with delays: Agent 1 immediately, Agent 2 after 90s, Agent 3 after 180s, etc.
  Use --stagger for default 90s interval, --stagger=2m for custom duration.

Examples:
  ntm spawn myproject --cc=2 --cod=2           # 2 Claude, 2 Codex + user pane
  ntm spawn myproject --cc=3 --cod=3 --gmi=1   # 3 Claude, 3 Codex, 1 Gemini
  ntm spawn myproject --cc=4 --no-user         # 4 Claude, no user pane
  ntm spawn myproject -r full-stack            # Use full-stack recipe
  ntm spawn myproject --cc=2:opus --cc=1:sonnet  # 2 Opus + 1 Sonnet
  ntm spawn myproject --cc=2 --auto-restart    # With auto-restart enabled
  ntm spawn myproject --persona=architect --persona=implementer:2  # Using personas
  ntm spawn myproject --cc=1 --prompt="fix auth" # Inject context about auth
  ntm spawn myproject --cc=3 --stagger --prompt="find bugs"  # Staggered prompts`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]
			dir := cfg.GetProjectDir(sessionName)

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
				counts := r.AgentCounts()
				if agentSpecs.ByType(AgentTypeClaude).TotalCount() == 0 && counts["cc"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeClaude, Count: counts["cc"]})
				}
				if agentSpecs.ByType(AgentTypeCodex).TotalCount() == 0 && counts["cod"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeCodex, Count: counts["cod"]})
				}
				if agentSpecs.ByType(AgentTypeGemini).TotalCount() == 0 && counts["gmi"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeGemini, Count: counts["gmi"]})
				}
				fmt.Printf("Using recipe '%s': %s\n", r.Name, r.Description)
			}

			// Extract simple counts
			ccCount := agentSpecs.ByType(AgentTypeClaude).TotalCount()
			codCount := agentSpecs.ByType(AgentTypeCodex).TotalCount()
			gmiCount := agentSpecs.ByType(AgentTypeGemini).TotalCount()

			// Apply defaults
			if len(agentSpecs) == 0 && len(cfg.ProjectDefaults) > 0 {
				if v, ok := cfg.ProjectDefaults["cc"]; ok && v > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeClaude, Count: v})
				}
				if v, ok := cfg.ProjectDefaults["cod"]; ok && v > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeCodex, Count: v})
				}
				if v, ok := cfg.ProjectDefaults["gmi"]; ok && v > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeGemini, Count: v})
				}
				ccCount = agentSpecs.ByType(AgentTypeClaude).TotalCount()
				codCount = agentSpecs.ByType(AgentTypeCodex).TotalCount()
				gmiCount = agentSpecs.ByType(AgentTypeGemini).TotalCount()
				if !IsJSONOutput() && len(agentSpecs) > 0 {
					fmt.Printf("Using default configuration: %d cc, %d cod, %d gmi\n", ccCount, codCount, gmiCount)
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

				// Warn if profile count doesn't match agent count
				totalAgents := ccCount + codCount + gmiCount
				if len(profileList) > 0 && totalAgents > 0 && len(profileList) != totalAgents {
					if !IsJSONOutput() {
						fmt.Printf("Warning: %d profiles for %d agents; profiles will be assigned in order\n",
							len(profileList), totalAgents)
					}
				}
			}

			opts := SpawnOptions{
				Session:          sessionName,
				Agents:           agentSpecs.Flatten(),
				CCCount:          ccCount,
				CodCount:         codCount,
				GmiCount:         gmiCount,
				UserPane:         !noUserPane,
				AutoRestart:      autoRestart,
				RecipeName:       recipeName,
				PersonaMap:       personaMap,
				PluginMap:        pluginMap,
				CassContextQuery: contextQuery,
				NoCassContext:    noCassContext,
				Prompt:           prompt,
				NoHooks:          noHooks,
				Safety:           safety,
				Stagger:          staggerDuration,
				StaggerEnabled:   staggerEnabled,
				ProfileList:      profileList,
			}

			return spawnSessionLogic(opts)
		},
	}

	// Use custom flag values that accumulate specs with type info
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeClaude, &agentSpecs), "cc", "Claude agents (N or N:model, model charset: a-zA-Z0-9._/@:+-)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCodex, &agentSpecs), "cod", "Codex agents (N or N:model, model charset: a-zA-Z0-9._/@:+-)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGemini, &agentSpecs), "gmi", "Gemini agents (N or N:model, model charset: a-zA-Z0-9._/@:+-)")
	cmd.Flags().Var(&personaSpecs, "persona", "Persona-defined agents (name or name:count)")
	cmd.Flags().BoolVar(&noUserPane, "no-user", false, "don't reserve a pane for the user")
	cmd.Flags().StringVarP(&recipeName, "recipe", "r", "", "use a recipe for agent configuration")
	cmd.Flags().BoolVar(&autoRestart, "auto-restart", false, "monitor and auto-restart crashed agents")

	// Stagger flag for thundering herd prevention
	// Custom handling: --stagger enables with default 90s, --stagger=2m for custom duration
	staggerValue := newOptionalDurationValue(90*time.Second, &staggerDuration, &staggerEnabled)
	cmd.Flags().Var(staggerValue, "stagger", "Stagger prompt delivery between agents (default 90s when enabled)")

	// CASS context flags
	cmd.Flags().StringVar(&contextQuery, "cass-context", "", "Explicit context query for CASS")
	cmd.Flags().BoolVar(&noCassContext, "no-cass-context", false, "Disable CASS context injection")
	cmd.Flags().IntVar(&contextLimit, "cass-context-limit", 0, "Max past sessions to include")
	cmd.Flags().IntVar(&contextDays, "cass-context-days", 0, "Look back N days")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to initialize agents with")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&safety, "safety", false, "Fail if session already exists (prevents accidental reuse)")

	// Profile flags for mapping personas to agents
	cmd.Flags().StringVar(&profilesFlag, "profiles", "", "Comma-separated list of profile/persona names to map to agents in order")
	cmd.Flags().StringVar(&profileSetFlag, "profile-set", "", "Predefined profile set name (e.g., backend-team, review-team)")

	// Register plugin flags dynamically
	// Note: We scan for plugins here to register flags.
	for _, p := range loadedPlugins {
		// Use p.Name as the AgentType so we can identify it later
		agentType := AgentType(p.Name)
		cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Name, p.Description)
		if p.Alias != "" {
			cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Alias, p.Description+" (alias)")
		}
	}

	return cmd
}

// spawnSessionLogic handles the creation of the session and spawning of agents
func spawnSessionLogic(opts SpawnOptions) error {
	// Helper for JSON error output
	outputError := func(err error) error {
		if IsJSONOutput() {
			_ = output.PrintJSON(output.NewError(err.Error()))
			return err
		}
		return err
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	if err := tmux.ValidateSessionName(opts.Session); err != nil {
		return outputError(err)
	}

	// Safety check: fail if session already exists (when --safety is enabled)
	if opts.Safety && tmux.SessionExists(opts.Session) {
		return outputError(fmt.Errorf("session '%s' already exists (--safety mode prevents reuse; use 'ntm kill %s' first)", opts.Session, opts.Session))
	}

	// Calculate total agents - either from Agents slice or explicit counts (legacy path)
	var totalAgents int
	if len(opts.Agents) == 0 {
		totalAgents = opts.CCCount + opts.CodCount + opts.GmiCount
		if totalAgents == 0 {
			return outputError(fmt.Errorf("no agents specified (use --cc, --cod, --gmi, or plugin flags)"))
		}
	} else {
		totalAgents = len(opts.Agents)
	}

	dir := cfg.GetProjectDir(opts.Session)

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
		SessionName: opts.Session,
		ProjectDir:  dir,
		AdditionalEnv: map[string]string{
			"NTM_AGENT_COUNT_CC":    fmt.Sprintf("%d", opts.CCCount),
			"NTM_AGENT_COUNT_COD":   fmt.Sprintf("%d", opts.CodCount),
			"NTM_AGENT_COUNT_GMI":   fmt.Sprintf("%d", opts.GmiCount),
			"NTM_AGENT_COUNT_TOTAL": fmt.Sprintf("%d", totalAgents),
		},
	}

	// Run pre-spawn hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreSpawn) {
		steps := output.NewSteps()
		if !IsJSONOutput() {
			steps.Start("Running pre-spawn hooks")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreSpawn, hookCtx)
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

	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if IsJSONOutput() {
			// Auto-create directory without prompting in JSON mode
			if err := os.MkdirAll(dir, 0755); err != nil {
				return outputError(fmt.Errorf("creating directory: %w", err))
			}
		} else {
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
	}

	// Calculate total panes needed
	totalPanes := totalAgents
	if opts.UserPane {
		totalPanes++
	}

	// Create or use existing session
	steps := output.NewSteps()
	if !tmux.SessionExists(opts.Session) {
		if !IsJSONOutput() {
			steps.Start(fmt.Sprintf("Creating session '%s'", opts.Session))
		}
		if err := tmux.CreateSession(opts.Session, dir); err != nil {
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf("creating session: %w", err))
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	// Get current pane count
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return outputError(err)
	}
	existingPanes := len(panes)

	// Add more panes if needed
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		if !IsJSONOutput() {
			steps.Start(fmt.Sprintf("Creating %d pane(s)", toAdd))
		}
		for i := 0; i < toAdd; i++ {
			if _, err := tmux.SplitWindow(opts.Session, dir); err != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("creating pane: %w", err))
			}
		}
		if !IsJSONOutput() {
			steps.Done()
		}
	}

	// Get updated pane list
	panes, err = tmux.GetPanes(opts.Session)
	if err != nil {
		return outputError(err)
	}

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if opts.UserPane {
		startIdx = 1
	}

	agentNum := startIdx
	profileIdx := 0 // Track which profile from ProfileList to assign
	if !IsJSONOutput() {
		steps.Start(fmt.Sprintf("Launching %d agent(s)", len(opts.Agents)))
	}

	// Track launched agents for resilience monitor
	type launchedAgent struct {
		paneID        string
		paneIndex     int
		agentType     string
		model         string // alias
		resolvedModel string // full name
		command       string
		promptDelay   time.Duration // Stagger delay before prompt delivery
	}
	var launchedAgents []launchedAgent

	// Track agent index for stagger calculation (0-based, regardless of user pane)
	staggerAgentIdx := 0

	// Create spawn context for agent coordination (environment vars and prompt annotation)
	spawnCtx := NewSpawnContext(len(opts.Agents))

	// WaitGroup for staggered prompt delivery - ensures all prompts are sent before returning
	var staggerWg sync.WaitGroup
	var setupWg sync.WaitGroup
	var maxStaggerDelay time.Duration

	// Resolve CASS context if enabled
	var cassContext string
	if !opts.NoCassContext && cfg.CASS.Context.Enabled {
		query := opts.CassContextQuery
		if query == "" {
			query = opts.Prompt // Use prompt if available
		}
		if query == "" && opts.RecipeName != "" {
			// Use recipe name as fallback context topic
			query = opts.RecipeName
		}

		if query != "" {
			ctx, err := ResolveCassContext(query, cfg.GetProjectDir(opts.Session))
			if err == nil {
				cassContext = ctx
			}
		}
	}

	// Launch agents using flattened specs (preserves model info for pane naming)
	for _, agent := range opts.Agents {
		if agentNum >= len(panes) {
			break
		}
		pane := panes[agentNum]

		// Format pane title with optional model variant
		// Format: {session}__{type}_{index} or {session}__{type}_{index}_{variant}
		title := tmux.FormatPaneName(opts.Session, string(agent.Type), agent.Index, agent.Model)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Get agent command template based on type
		var agentCmdTemplate string
		var envVars map[string]string

		switch agent.Type {
		case AgentTypeClaude:
			agentCmdTemplate = cfg.Agents.Claude
		case AgentTypeCodex:
			agentCmdTemplate = cfg.Agents.Codex
		case AgentTypeGemini:
			agentCmdTemplate = cfg.Agents.Gemini
		default:
			// Check plugins
			if p, ok := opts.PluginMap[string(agent.Type)]; ok {
				agentCmdTemplate = p.Command
				envVars = p.Env
			} else {
				// Unknown type, skip
				fmt.Printf("⚠ Warning: unknown agent type %s\n", agent.Type)
				continue
			}
		}

		// Resolve model alias to full model name
		resolvedModel := ResolveModel(agent.Type, agent.Model)

		// Check if this is a persona agent and prepare system prompt
		var systemPromptFile string
		var personaName string
		if opts.PersonaMap != nil {
			if p, ok := opts.PersonaMap[agent.Model]; ok {
				personaName = p.Name
				// Prepare system prompt file
				promptFile, err := persona.PrepareSystemPrompt(p, dir)
				if err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: could not prepare system prompt for %s: %v\n", p.Name, err)
					}
				} else {
					systemPromptFile = promptFile
				}
				// For persona agents, resolve the model from the persona config
				resolvedModel = ResolveModel(agent.Type, p.Model)
			}
		}

		// Check if there's a profile to assign from ProfileList (--profiles/--profile-set)
		// ProfileList takes precedence over PersonaMap for system prompt
		if len(opts.ProfileList) > profileIdx {
			profile := opts.ProfileList[profileIdx]
			personaName = profile.Name
			// Prepare system prompt file for the profile
			promptFile, err := persona.PrepareSystemPrompt(profile, dir)
			if err != nil {
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: could not prepare system prompt for profile %s: %v\n", profile.Name, err)
				}
			} else {
				systemPromptFile = promptFile
			}
			if !IsJSONOutput() {
				fmt.Printf("  → Assigning profile '%s' to agent %d\n", profile.Name, profileIdx+1)
			}
		}

		// Update pane title with profile name if assigned
		if personaName != "" {
			title := tmux.FormatPaneName(opts.Session, string(agent.Type), agent.Index, personaName)
			if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: could not update pane title with profile name: %v\n", err)
				}
			}
		}

		// Generate command using template
		agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
			Model:            resolvedModel,
			ModelAlias:       agent.Model,
			SessionName:      opts.Session,
			PaneIndex:        agent.Index,
			AgentType:        string(agent.Type),
			ProjectDir:       dir,
			SystemPromptFile: systemPromptFile,
			PersonaName:      personaName,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Apply plugin env vars if any
		if len(envVars) > 0 {
			var envPrefix string
			for k, v := range envVars {
				envPrefix += fmt.Sprintf("%s=%q ", k, v)
			}
			agentCmd = envPrefix + agentCmd
		}

		// Calculate stagger delay for this agent (used for spawn context)
		var promptDelay time.Duration
		if opts.StaggerEnabled && opts.Stagger > 0 {
			promptDelay = time.Duration(staggerAgentIdx) * opts.Stagger
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

		cmd, err := tmux.BuildPaneCommand(dir, safeAgentCmd)
		if err != nil {
			return outputError(fmt.Errorf("building %s agent command: %w", agent.Type, err))
		}

		if err := tmux.SendKeys(pane.ID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching %s agent: %w", agent.Type, err))
		}

		// Parallelize post-launch setup (Gemini setup, CASS context, immediate prompts)
		// This prevents sequential blocking which can make spawn very slow
		setupWg.Add(1)
		go func(paneID string, idx int, agentType AgentType, agent FlatAgent) {
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
				setupCtx, setupCancel := context.WithTimeout(context.Background(), geminiCfg.ReadyTimeout+geminiCfg.ModelSelectTimeout+10*time.Second)
				if err := gemini.PostSpawnSetup(setupCtx, paneID, geminiCfg); err != nil {
					setupCancel()
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: Gemini Pro model setup failed for agent %d: %v\n", idx, err)
					}
					// Don't fail spawn - agent is still running, just possibly with default model
				} else {
					setupCancel()
					if !IsJSONOutput() && cfg.GeminiSetup.Verbose {
						fmt.Printf("✓ Gemini %d configured for Pro model\n", idx)
					}
				}
			}

			// Inject CASS context if available
			if cassContext != "" {
				// Wait a bit for agent to start (simple heuristic)
				time.Sleep(500 * time.Millisecond)
				if err := tmux.SendKeys(paneID, cassContext, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to inject context for agent %d: %v\n", idx, err)
					}
				}
			}

			// Inject user prompt if provided (Immediate delivery only)
			// Staggered delivery is handled by the main thread's staggerWg logic
			if opts.Prompt != "" && (!opts.StaggerEnabled || opts.Stagger <= 0) {
				time.Sleep(200 * time.Millisecond)
				if err := tmux.SendKeys(paneID, opts.Prompt, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to send prompt to agent %d: %v\n", idx, err)
					}
				}
			}
		}(pane.ID, agent.Index, agent.Type, agent)

		// Schedule staggered prompt delivery with spawn context annotation
		if opts.StaggerEnabled && opts.Stagger > 0 && opts.Prompt != "" {
			pID := pane.ID
			// Annotate prompt with spawn context when stagger is enabled
			// This helps agents understand their position in the spawn order
			annotatedPrompt := agentSpawnCtx.AnnotatePrompt(opts.Prompt, true)
			delay := promptDelay
			staggerWg.Add(1)
			go func() {
				defer staggerWg.Done()
				time.Sleep(delay)
				if err := tmux.SendKeys(pID, annotatedPrompt, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: staggered prompt delivery failed for pane %s: %v\n", pID, err)
					}
				}
			}()
			// Track max delay
			if delay > maxStaggerDelay {
				maxStaggerDelay = delay
			}
			if !IsJSONOutput() {
				fmt.Printf("  → Agent %d prompt scheduled in %v\n", staggerAgentIdx+1, delay)
			}
		}

		// Track for resilience monitor
		launchedAgents = append(launchedAgents, launchedAgent{
			paneID:        pane.ID,
			paneIndex:     pane.Index,
			agentType:     string(agent.Type),
			model:         agent.Model,
			resolvedModel: resolvedModel,
			command:       safeAgentCmd,
			promptDelay:   promptDelay,
		})

		staggerAgentIdx++
		profileIdx++
		agentNum++
	}

	// Complete the launching step
	if !IsJSONOutput() {
		steps.Done()
	}

	// Wait for parallel setup tasks to complete
	setupWg.Wait()

	// Wait for staggered prompt delivery to complete
	if maxStaggerDelay > 0 {
		if !IsJSONOutput() {
			fmt.Printf("⏳ Waiting for staggered prompts (max %v)...\n", maxStaggerDelay)
		}
		staggerWg.Wait()
		if !IsJSONOutput() {
			fmt.Println("✓ All staggered prompts delivered")
		}
	}

	// Get final pane list for output
	finalPanes, _ := tmux.GetPanes(opts.Session)

	// JSON output mode
	if IsJSONOutput() {
		// Build map of pane index -> stagger delay for lookup
		paneDelays := make(map[int]time.Duration)
		for _, agent := range launchedAgents {
			paneDelays[agent.paneIndex] = agent.promptDelay
		}

		paneResponses := make([]output.PaneResponse, len(finalPanes))
		agentCounts := output.AgentCountsResponse{}
		for i, p := range finalPanes {
			paneResponses[i] = output.PaneResponse{
				Index:         p.Index,
				Title:         p.Title,
				Type:          agentTypeToString(p.Type),
				Variant:       p.Variant, // Model alias or persona name
				Active:        p.Active,
				Width:         p.Width,
				Height:        p.Height,
				Command:       p.Command,
				PromptDelayMs: paneDelays[p.Index].Milliseconds(),
			}
			switch p.Type {
			case tmux.AgentClaude:
				agentCounts.Claude++
			case tmux.AgentCodex:
				agentCounts.Codex++
			case tmux.AgentGemini:
				agentCounts.Gemini++
			default:
				// Other/plugin agents
				agentCounts.User++ // Maybe separate category?
			}
		}
		agentCounts.Total = agentCounts.Claude + agentCounts.Codex + agentCounts.Gemini

		// Build stagger config if enabled
		var staggerCfg *output.StaggerConfig
		if opts.StaggerEnabled {
			staggerCfg = &output.StaggerConfig{
				Enabled:    true,
				IntervalMs: opts.Stagger.Milliseconds(),
			}
		}

		return output.PrintJSON(output.SpawnResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             opts.Session,
			Created:             true, // spawn always creates or reuses
			WorkingDirectory:    dir,
			Panes:               paneResponses,
			AgentCounts:         agentCounts,
			Stagger:             staggerCfg,
		})
	}

	// Print "What's next?" suggestions
	output.SuccessFooter(output.SpawnSuggestions(opts.Session)...)

	// Emit session_create event
	events.EmitSessionCreate(opts.Session, opts.CCCount, opts.CodCount, opts.GmiCount, dir, opts.RecipeName)

	// Emit agent_spawn events for each agent
	for _, agent := range launchedAgents {
		events.Emit(events.EventAgentSpawn, opts.Session, events.AgentSpawnData{
			AgentType: agent.agentType,
			Model:     agent.resolvedModel,
			Variant:   agent.model,
			PaneIndex: agent.paneIndex,
		})
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

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, postErr := hookExec.RunHooksForEvent(ctx, hooks.EventPostSpawn, hookCtx)
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

	// Start session monitor (handles resilience and daemons)
	// Always started regardless of auto-restart config
	{
		// Save manifest for the monitor process
		manifest := &resilience.SpawnManifest{
			Session:     opts.Session,
			ProjectDir:  dir,
			AutoRestart: opts.AutoRestart || cfg.Resilience.AutoRestart,
		}
		for _, agent := range launchedAgents {
			manifest.Agents = append(manifest.Agents, resilience.AgentConfig{
				PaneID:    agent.paneID,
				PaneIndex: agent.paneIndex,
				Type:      agent.agentType,
				Model:     agent.model,
				Command:   agent.command,
			})
		}
		if err := resilience.SaveManifest(manifest); err != nil {
			if !IsJSONOutput() {
				output.PrintWarningf("Failed to save resilience manifest: %v", err)
			}
		} else {
			// Launch monitor in background
			exe, err := os.Executable()
			if err == nil {
				cmd := exec.Command(exe, "internal-monitor", opts.Session)
				
				// Setup logging
				logDir := resilience.LogDir()
				if err := os.MkdirAll(logDir, 0755); err == nil {
					logPath := filepath.Join(logDir, fmt.Sprintf("%s-monitor.log", opts.Session))
					if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
						cmd.Stdout = logFile
						cmd.Stderr = logFile
					}
				}

				// Detach from terminal so it survives when ntm spawn exits
				setDetachedProcess(cmd)
				if err := cmd.Start(); err != nil {
					if !IsJSONOutput() {
						output.PrintWarningf("Failed to start session monitor: %v", err)
					}
				} else {
					if !IsJSONOutput() {
						if manifest.AutoRestart {
							output.PrintInfof("Session monitor started (auto-restart enabled, pid: %d)", cmd.Process.Pid)
						} else {
							output.PrintInfof("Session monitor started (pid: %d)", cmd.Process.Pid)
						}
					}
				}
			}
		}
	}

	// Register session as Agent Mail agent (non-blocking)
	registerSessionAgent(opts.Session, dir)

	return nil
}

// registerSessionAgent registers the session with Agent Mail.
// This is non-blocking and logs but does not fail if unavailable.
func registerSessionAgent(sessionName, workingDir string) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
