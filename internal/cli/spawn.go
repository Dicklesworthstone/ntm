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
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/gemini"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	InitPrompt       string

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
	StaggerDelay time.Duration // Delay for fixed mode (default 15s)

	// Legacy stagger fields (deprecated, kept for backward compatibility)
	Stagger        time.Duration // Delay between agent prompt delivery
	StaggerEnabled bool          // True if --stagger flag was provided

	// Assignment configuration for spawn+assign workflow
	Assign             bool          // Enable auto-assignment after spawn
	AssignStrategy     string        // Assignment strategy: balanced, speed, quality, dependency, round-robin
	AssignLimit        int           // Maximum assignments (0 = unlimited)
	AssignReadyTimeout time.Duration // Timeout waiting for agents to become ready

	// Git worktree isolation configuration
	UseWorktrees bool // Enable git worktree isolation for agents
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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	PaneCount   int       `json:"pane_count"`
	HasGitPatch bool      `json:"has_git_patch"`
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
	var contextLimit int
	var contextDays int
	var prompt string
	var initPrompt string
	var noHooks bool
	var profilesFlag string
	var profileSetFlag string
	var staggerDuration time.Duration
	var staggerEnabled bool
	var safety bool

	// New stagger flags for bd-2wih
	var staggerMode string         // smart, fixed, or none
	var staggerDelay time.Duration // delay for fixed mode

	// Assignment flags for spawn+assign workflow (bd-3nde)
	var assignEnabled bool
	var assignStrategy string
	var assignLimit int
	var assignReadyTimeout time.Duration

	// Git worktree isolation flag
	var useWorktrees bool

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
    - fixed: Use fixed delay (--stagger-delay, default 15s)
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

Examples:
  ntm spawn myproject --cc=2 --cod=2           # 2 Claude, 2 Codex + user pane
  ntm spawn myproject --cc=3 --cod=3 --gmi=1   # 3 Claude, 3 Codex, 1 Gemini
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
  ntm spawn myproject --cc=4 --stagger-mode=fixed --stagger-delay=20s  # Fixed 20s delay`,
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
				counts := tmpl.AgentCounts()
				// Apply template agent counts (CLI flags override these)
				if agentSpecs.ByType(AgentTypeClaude).TotalCount() == 0 && counts["cc"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeClaude, Count: counts["cc"]})
				}
				if agentSpecs.ByType(AgentTypeCodex).TotalCount() == 0 && counts["cod"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeCodex, Count: counts["cod"]})
				}
				if agentSpecs.ByType(AgentTypeGemini).TotalCount() == 0 && counts["gmi"] > 0 {
					agentSpecs = append(agentSpecs, AgentSpec{Type: AgentTypeGemini, Count: counts["gmi"]})
				}
				if !IsJSONOutput() {
					fmt.Printf("Using template '%s': %s (%s coordination)\n",
						tmpl.Name, tmpl.Description, tmpl.Coordination)
				}
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
				Session:            sessionName,
				Agents:             agentSpecs.Flatten(),
				CCCount:            ccCount,
				CodCount:           codCount,
				GmiCount:           gmiCount,
				UserPane:           !noUserPane,
				AutoRestart:        autoRestart,
				RecipeName:         recipeName,
				PersonaMap:         personaMap,
				PluginMap:          pluginMap,
				CassContextQuery:   contextQuery,
				NoCassContext:      noCassContext,
				Prompt:             prompt,
				InitPrompt:         initPrompt,
				NoHooks:            noHooks,
				Safety:             safety,
				StaggerMode:        staggerMode,
				StaggerDelay:       staggerDelay,
				Stagger:            staggerDuration,
				StaggerEnabled:     staggerEnabled,
				ProfileList:        profileList,
				Assign:             assignEnabled,
				AssignStrategy:     assignStrategy,
				AssignLimit:        assignLimit,
				AssignReadyTimeout: assignReadyTimeout,
				UseWorktrees:       useWorktrees,
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
	cmd.Flags().StringVarP(&templateName, "template", "t", "", "use a workflow template for agent configuration")
	cmd.Flags().BoolVar(&autoRestart, "auto-restart", false, "monitor and auto-restart crashed agents")

	// Stagger flag for thundering herd prevention
	// Custom handling: --stagger enables with default 90s, --stagger=2m for custom duration
	staggerValue := newOptionalDurationValue(90*time.Second, &staggerDuration, &staggerEnabled)
	cmd.Flags().Var(staggerValue, "stagger", "Stagger prompt delivery between agents (default 90s when enabled)")

	// New stagger mode flags (bd-2wih)
	cmd.Flags().StringVar(&staggerMode, "stagger-mode", "none", "Stagger mode: smart (adaptive), fixed, or none")
	cmd.Flags().DurationVar(&staggerDelay, "stagger-delay", 15*time.Second, "Fixed delay between agents (used with --stagger-mode=fixed)")

	// CASS context flags
	cmd.Flags().StringVar(&contextQuery, "cass-context", "", "Explicit context query for CASS")
	cmd.Flags().BoolVar(&noCassContext, "no-cass-context", false, "Disable CASS context injection")
	cmd.Flags().IntVar(&contextLimit, "cass-context-limit", 0, "Max past sessions to include")
	cmd.Flags().IntVar(&contextDays, "cass-context-days", 0, "Look back N days")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to initialize agents with")
	cmd.Flags().StringVar(&initPrompt, "init-prompt", "", "Prompt to send after agents are ready (used with --assign)")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&safety, "safety", false, "Fail if session already exists (prevents accidental reuse)")

	// Assignment flags for spawn+assign workflow
	cmd.Flags().BoolVar(&assignEnabled, "assign", false, "Auto-assign beads to spawned agents after ready")
	cmd.Flags().StringVar(&assignStrategy, "strategy", "balanced", "Assignment strategy: balanced, speed, quality, dependency, round-robin")
	cmd.Flags().IntVar(&assignLimit, "limit", 0, "Maximum beads to assign (0 = unlimited)")
	cmd.Flags().DurationVar(&assignReadyTimeout, "ready-timeout", 60*time.Second, "Timeout waiting for agents to become ready")

	// Git worktree isolation flag
	cmd.Flags().BoolVar(&useWorktrees, "worktrees", false, "Enable git worktree isolation for agents (each agent gets isolated working directory)")

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

	// Create worktrees if enabled
	var worktreeManager *worktrees.WorktreeManager
	if opts.UseWorktrees {
		worktreeManager = worktrees.NewManager(dir, opts.Session)
		if !IsJSONOutput() {
			steps.Start("Creating Git worktrees for agent isolation")
		}

		// Create worktree for each agent
		for _, agent := range opts.Agents {
			agentName := fmt.Sprintf("%s_%d", strings.ToLower(string(agent.Type)), agent.Index)
			if _, err := worktreeManager.CreateForAgent(agentName); err != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("failed to create worktree for agent %s: %w", agentName, err))
			}
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

	// Initialize rate limit tracker for smart stagger mode (bd-2wih)
	var rateLimitTracker *ratelimit.RateLimitTracker
	if opts.StaggerMode == "smart" {
		rateLimitTracker = ratelimit.NewRateLimitTracker(dir)
		if err := rateLimitTracker.LoadFromDir(dir); err != nil {
			if !IsJSONOutput() {
				output.PrintWarningf("Failed to load rate limit history: %v", err)
			}
		}
	}

	// Determine effective stagger mode (new mode takes precedence over legacy)
	effectiveStaggerMode := opts.StaggerMode
	if effectiveStaggerMode == "" || effectiveStaggerMode == "none" {
		if opts.StaggerEnabled && opts.Stagger > 0 {
			effectiveStaggerMode = "legacy" // Use legacy duration-based stagger
		}
	}

	// Spawn state for dashboard display (only used when stagger is enabled)
	var spawnState *SpawnState
	staggerInterval := opts.Stagger
	if effectiveStaggerMode == "fixed" {
		staggerInterval = opts.StaggerDelay
	} else if effectiveStaggerMode == "smart" && rateLimitTracker != nil {
		staggerInterval = rateLimitTracker.GetOptimalDelay("anthropic") // Default to Anthropic timing
	}
	if effectiveStaggerMode != "none" && effectiveStaggerMode != "" && opts.Prompt != "" {
		spawnState = NewSpawnState(spawnCtx.BatchID, int(staggerInterval.Seconds()), len(opts.Agents))
	}

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

	// Build recovery context if enabled (smart session recovery)
	var recoveryPrompt string
	if cfg.SessionRecovery.Enabled && cfg.SessionRecovery.AutoInjectOnSpawn {
		ctx, cancelCtx := context.WithTimeout(context.Background(), 5*time.Second)
		rc, err := buildRecoveryContext(ctx, opts.Session, dir, cfg.SessionRecovery)
		cancelCtx()
		if err == nil && rc != nil {
			recoveryPrompt = FormatRecoveryPrompt(rc)
			if !IsJSONOutput() && recoveryPrompt != "" {
				fmt.Println("✓ Recovery context prepared for session")
			}
		}
	}

	// Git worktree isolation setup
	var worktreeManager *git.WorktreeManager
	agentWorktrees := make(map[string]string) // agentKey -> worktree path
	if opts.UseWorktrees {
		if !IsJSONOutput() {
			steps.Start("Setting up git worktree isolation")
		}

		wm, err := git.NewWorktreeManager(dir)
		if err != nil {
			if !IsJSONOutput() {
				steps.Fail()
			}
			return outputError(fmt.Errorf("failed to initialize worktree manager: %w", err))
		}
		worktreeManager = wm

		// Pre-provision worktrees for all agents
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		for _, agent := range opts.Agents {
			agentKey := fmt.Sprintf("%s_%d", string(agent.Type), agent.Index)
			sessionID := opts.Session

			worktree, err := worktreeManager.ProvisionWorktree(ctx, agentKey, sessionID)
			if err != nil {
				if !IsJSONOutput() {
					steps.Fail()
				}
				return outputError(fmt.Errorf("failed to provision worktree for %s: %w", agentKey, err))
			}

			agentWorktrees[agentKey] = worktree.Path
			if !IsJSONOutput() {
				fmt.Printf("  ✓ Worktree for %s: %s\n", agentKey, worktree.Path)
			}
		}

		if !IsJSONOutput() {
			steps.Success()
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

		// Use worktree directory if worktree isolation is enabled
		workingDir := dir
		if opts.UseWorktrees && worktreeManager != nil {
			agentName := fmt.Sprintf("%s_%d", strings.ToLower(string(agent.Type)), agent.Index)
			if wtInfo, err := worktreeManager.GetWorktreeForAgent(agentName); err == nil && wtInfo.Created && wtInfo.Error == "" {
				workingDir = wtInfo.Path
			}
		}

		cmd, err := tmux.BuildPaneCommand(workingDir, safeAgentCmd)
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

			// Inject recovery prompt if available (smart session recovery)
			if recoveryPrompt != "" {
				// Small delay to let agent initialize
				time.Sleep(300 * time.Millisecond)
				if err := tmux.SendKeys(paneID, recoveryPrompt, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to inject recovery context for agent %d: %v\n", idx, err)
					}
				}
			}


			// Inject user prompt if provided (Immediate delivery only)
			// Staggered delivery is handled by the main thread's staggerWg logic
			if opts.Prompt != "" && (!opts.StaggerEnabled || opts.Stagger <= 0) {
				time.Sleep(200 * time.Millisecond)

				// Combine CASS context with user prompt
				finalPrompt := opts.Prompt
				if cassContext != "" {
					finalPrompt = cassContext + "\n\n" + opts.Prompt
				}

				if err := tmux.SendKeys(paneID, finalPrompt, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to send prompt to agent %d: %v\n", idx, err)
					}
				}
			}
		}(pane.ID, agent.Index, agent.Type, agent)

		// Schedule staggered prompt delivery with spawn context annotation
		if opts.StaggerEnabled && opts.Stagger > 0 && opts.Prompt != "" {
			pID := pane.ID
			pTitle := title
			// Combine CASS context with user prompt for staggered delivery
			finalPromptForStagger := opts.Prompt
			if cassContext != "" {
				finalPromptForStagger = cassContext + "\n\n" + opts.Prompt
			}
			// Annotate prompt with spawn context when stagger is enabled
			// This helps agents understand their position in the spawn order
			annotatedPrompt := agentSpawnCtx.AnnotatePrompt(finalPromptForStagger, true)
			delay := promptDelay
			scheduledAt := time.Now().Add(delay)

			// Add to spawn state for dashboard display
			if spawnState != nil {
				spawnState.AddPrompt(pTitle, pID, staggerAgentIdx+1, scheduledAt)
			}

			staggerWg.Add(1)
			go func() {
				defer staggerWg.Done()
				time.Sleep(delay)
				if err := tmux.SendKeys(pID, annotatedPrompt, true); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: staggered prompt delivery failed for pane %s: %v\n", pID, err)
					}
				}
				// Mark as sent in state
				if spawnState != nil {
					spawnState.MarkSent(pID)
					if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
						fmt.Printf("⚠ Warning: failed to update spawn state: %v\n", err)
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

	// Save initial spawn state for dashboard display
	if spawnState != nil {
		if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
			fmt.Printf("⚠ Warning: failed to save spawn state: %v\n", err)
		}
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
		// Clean up spawn state file now that all prompts are sent
		if spawnState != nil {
			spawnState.MarkComplete()
			if err := spawnState.Save(dir); err != nil && !IsJSONOutput() {
				fmt.Printf("⚠ Warning: failed to save final spawn state: %v\n", err)
			}
			// Remove state file after a short delay to let dashboard catch the completion
			go func() {
				time.Sleep(5 * time.Second)
				_ = ClearSpawnState(dir)
			}()
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

		// Register spawned agents with Agent Mail
		var agentMailStatus *output.AgentMailSpawnStatus
		if len(launchedAgents) > 0 {
			spawnedAgents := make([]spawnedAgentInfo, len(launchedAgents))
			for i, agent := range launchedAgents {
				spawnedAgents[i] = spawnedAgentInfo{
					paneIndex:     agent.paneIndex,
					agentType:     agent.agentType,
					model:         agent.model,
					resolvedModel: agent.resolvedModel,
				}
			}
			agentMailStatus = registerSpawnedAgents(dir, spawnedAgents)
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
		}

		// If assignment is enabled, wait for agents and run assignment phase
		if opts.Assign {
			// Wait for agents to become ready
			readyCount, waitErr := waitForAgentsReady(opts.Session, opts.AssignReadyTimeout)

			var assignResult *AssignOutputEnhanced
			var assignErrors []string

			if waitErr != nil {
				assignErrors = append(assignErrors, fmt.Sprintf("ready wait failed: %v", waitErr))
			}

			var initResult *SpawnInitResult
			if opts.InitPrompt != "" {
				agentsReached, initErr := sendInitPromptToReadyAgents(opts.Session, opts.InitPrompt)
				initResult = &SpawnInitResult{
					PromptSent:    initErr == nil,
					AgentsReached: agentsReached,
				}
				if initErr != nil {
					assignErrors = append(assignErrors, fmt.Sprintf("init prompt failed: %v", initErr))
				}
			}

			// Run assignment phase (even if ready wait timed out)
			result, err := runAssignmentPhase(opts.Session, opts)
			if err != nil {
				assignErrors = append(assignErrors, fmt.Sprintf("assignment failed: %v", err))
			} else {
				assignResult = result
			}
			_ = readyCount // Used for logging in non-JSON mode

			// Return combined result
			combinedResult := SpawnAssignResult{
				Spawn:  spawnResponse,
				Init:   initResult,
				Assign: assignResult,
			}
			if len(assignErrors) > 0 {
				if assignResult == nil {
					assignResult = &AssignOutputEnhanced{Strategy: opts.AssignStrategy}
					combinedResult.Assign = assignResult
				}
				assignResult.Errors = append(assignResult.Errors, assignErrors...)
			}
			return output.PrintJSON(combinedResult)
		}

		return output.PrintJSON(spawnResponse)
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

	// Register spawned agents with Agent Mail (non-JSON mode)
	if len(launchedAgents) > 0 {
		spawnedAgents := make([]spawnedAgentInfo, len(launchedAgents))
		for i, agent := range launchedAgents {
			spawnedAgents[i] = spawnedAgentInfo{
				paneIndex:     agent.paneIndex,
				agentType:     agent.agentType,
				model:         agent.model,
				resolvedModel: agent.resolvedModel,
			}
		}
		_ = registerSpawnedAgents(dir, spawnedAgents) // Ignore result in non-JSON mode
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

	// Run assignment phase if enabled (non-JSON mode)
	if opts.Assign {
		steps := output.NewSteps()
		steps.Start("Waiting for agents to become ready")

		readyCount, err := waitForAgentsReady(opts.Session, opts.AssignReadyTimeout)
		if err != nil {
			steps.Warn()
			output.PrintWarningf("Ready wait failed: %v (continuing with %d ready agents)", err, readyCount)
		} else {
			steps.Done()
			output.PrintInfof("%d agents ready", readyCount)
		}

		if opts.InitPrompt != "" {
			steps.Start("Sending init prompt to ready agents")
			agentsReached, initErr := sendInitPromptToReadyAgents(opts.Session, opts.InitPrompt)
			if initErr != nil {
				steps.Warn()
				output.PrintWarningf("Init prompt failed: %v", initErr)
			} else {
				steps.Done()
				output.PrintInfof("Init prompt sent to %d agents", agentsReached)
			}
		}

		steps.Start("Assigning work to agents")
		assignResult, err := runAssignmentPhase(opts.Session, opts)
		if err != nil {
			steps.Warn()
			output.PrintWarningf("Assignment failed: %v", err)
		} else {
			steps.Done()
			output.PrintInfof("Assigned %d tasks (strategy: %s)", len(assignResult.Assigned), assignResult.Strategy)
		}
	}

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

// spawnedAgentInfo holds agent info for registration with Agent Mail.
type spawnedAgentInfo struct {
	paneIndex     int
	agentType     string
	model         string
	resolvedModel string
}

// registerSpawnedAgents registers each spawned agent with Agent Mail and returns status.
// This function implements graceful degradation - Agent Mail unavailability does not
// cause spawn to fail. Returns nil if Agent Mail is not available or disabled.
func registerSpawnedAgents(workingDir string, agents []spawnedAgentInfo) *output.AgentMailSpawnStatus {
	// Check if Agent Mail integration is enabled
	if cfg != nil && !cfg.AgentMail.Enabled {
		return nil
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

	// Check availability first (uses cached result)
	if !client.IsAvailable() {
		return &output.AgentMailSpawnStatus{
			Available:         false,
			ProjectRegistered: false,
			AgentsRegistered:  0,
			AgentsFailed:      len(agents),
		}
	}

	status := &output.AgentMailSpawnStatus{
		Available: true,
		AgentMap:  make(map[string]string),
	}

	// Ensure project exists
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.EnsureProject(ctx, workingDir)
	if err != nil {
		if !IsJSONOutput() {
			output.PrintWarningf("Agent Mail project registration failed: %v", err)
		}
		status.ProjectRegistered = false
		status.AgentsFailed = len(agents)
		return status
	}
	status.ProjectRegistered = true

	// Register each agent
	for _, agent := range agents {
		// Map agent type to program name
		program := agentTypeToProgram(agent.agentType)
		model := agent.resolvedModel
		if model == "" {
			model = agent.model
		}

		regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
		registered, err := client.CreateAgentIdentity(regCtx, agentmail.RegisterAgentOptions{
			ProjectKey: workingDir,
			Program:    program,
			Model:      model,
		})
		regCancel()

		if err != nil {
			status.AgentsFailed++
			if !IsJSONOutput() {
				output.PrintWarningf("Agent Mail registration failed for pane %d: %v", agent.paneIndex, err)
			}
			continue
		}

		status.AgentsRegistered++
		status.AgentMap[fmt.Sprintf("%d", agent.paneIndex)] = registered.Name
		if !IsJSONOutput() {
			output.PrintInfof("Registered agent pane %d as %s", agent.paneIndex, registered.Name)
		}
	}

	return status
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
	default:
		return agentType
	}
}

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
	maxRules := 10   // Default limit for rules
	maxSnippets := 3 // Default limit for history snippets

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := cmClient.GetRecoveryContext(ctx, queryTask, maxRules, maxSnippets)
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

// buildRecoveryContext builds the full recovery context for session recovery.
// It gathers information from BV (beads), Agent Mail (messages), and CM (memories).
func buildRecoveryContext(ctx context.Context, sessionName, workingDir string, recoveryCfg config.SessionRecoveryConfig) (*RecoveryContext, error) {
	if !recoveryCfg.Enabled {
		return nil, nil
	}

	rc := &RecoveryContext{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string

	// Load beads if enabled
	if recoveryCfg.IncludeBeadsContext {
		wg.Add(1)
		go func() {
			defer wg.Done()
			beads, completed, blocked, err := loadRecoveryBeads(workingDir)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("beads: %v", err))
			} else {
				rc.Beads = beads
				rc.CompletedBeads = completed
				rc.BlockedBeads = blocked
			}
		}()
	}

	// Load Agent Mail messages if enabled
	if recoveryCfg.IncludeAgentMail {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, reservations, err := loadRecoveryMessages(ctx, sessionName, workingDir)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("agent mail: %v", err))
			} else {
				rc.Messages = msgs
				rc.FileReservations = reservations
			}
		}()
	}

	// Load CM memories if enabled
	if recoveryCfg.IncludeCMMemories {
		wg.Add(1)
		go func() {
			defer wg.Done()
			memories, err := loadRecoveryCMMemories(ctx, workingDir)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("cm memories: %v", err))
			} else {
				rc.CMMemories = memories
			}
		}()
	}

	wg.Wait()

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

// loadRecoveryBeads loads in-progress, completed, and blocked beads from BV.
func loadRecoveryBeads(workingDir string) (inProgress, completed, blocked []RecoveryBead, err error) {
	const limit = 10 // reasonable limit for recovery context

	// Get in-progress beads
	ipList := bv.GetInProgressList(workingDir, limit)
	for _, b := range ipList {
		inProgress = append(inProgress, RecoveryBead{
			ID:       b.ID,
			Title:    b.Title,
			Assignee: b.Assignee,
		})
	}

	// Get recently completed beads
	completedList := bv.GetRecentlyCompletedList(workingDir, limit)
	for _, b := range completedList {
		completed = append(completed, RecoveryBead{
			ID:    b.ID,
			Title: b.Title,
		})
	}

	// Get blocked beads
	blockedList := bv.GetBlockedList(workingDir, limit)
	for _, b := range blockedList {
		blocked = append(blocked, RecoveryBead{
			ID:    b.ID,
			Title: b.Title,
		})
	}

	return inProgress, completed, blocked, nil
}

// loadRecoveryMessages loads recent Agent Mail messages and file reservations.
func loadRecoveryMessages(ctx context.Context, sessionName, workingDir string) ([]RecoveryMessage, []string, error) {
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

	if !client.IsAvailable() {
		return nil, nil, nil // Graceful degradation
	}

	// Fetch inbox
	inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey:    workingDir,
		AgentName:     sessionName,
		Limit:         10,
		IncludeBodies: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch inbox: %w", err)
	}

	var msgs []RecoveryMessage
	for _, m := range inbox {
		msgs = append(msgs, RecoveryMessage{
			ID:         m.ID,
			From:       m.From,
			Subject:    m.Subject,
			Body:       m.BodyMD,
			Importance: m.Importance,
			CreatedAt:  m.CreatedTS,
		})
	}

	// Fetch file reservations
	reservations, err := client.ListReservations(ctx, workingDir, sessionName, false)
	if err != nil {
		// Non-fatal, return messages only
		return msgs, nil, nil
	}

	var paths []string
	for _, r := range reservations {
		paths = append(paths, r.PathPattern)
	}

	return msgs, paths, nil
}

// loadRecoveryCMMemories loads procedural memories from CM.
func loadRecoveryCMMemories(ctx context.Context, workingDir string) (*RecoveryCMMemories, error) {
	client := cm.NewCLIClient()
	if !client.IsInstalled() {
		return nil, nil // Graceful degradation
	}

	// Get recovery context with reasonable limits
	projectName := filepath.Base(workingDir)
	result, err := client.GetRecoveryContext(ctx, projectName, 10, 3)
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

// estimateRecoveryTokens estimates the token count of a recovery context.
// Uses a simple heuristic: ~4 characters per token.
func estimateRecoveryTokens(rc *RecoveryContext) int {
	if rc == nil {
		return 0
	}

	chars := 0

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
	if rc.CMMemories != nil {
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
func FormatRecoveryPrompt(rc *RecoveryContext) string {
	if rc == nil {
		return ""
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
			sb.WriteString(fmt.Sprintf("- You were working on: [%s] %s\n", rc.Beads[0].ID, rc.Beads[0].Title))
		}

		if rc.Checkpoint != nil {
			sb.WriteString(fmt.Sprintf("- Last checkpoint: %s — %s\n",
				rc.Checkpoint.CreatedAt.Format("2006-01-02 15:04"),
				rc.Checkpoint.Description))
			if rc.Checkpoint.HasGitPatch {
				sb.WriteString("- Uncommitted changes: preserved in checkpoint\n")
			}
		}

		if len(rc.FileReservations) > 0 {
			sb.WriteString("- Files you were editing: ")
			sb.WriteString(strings.Join(rc.FileReservations, ", "))
			sb.WriteString("\n")
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
			sb.WriteString(fmt.Sprintf("- [x] Completed: [%s] %s\n", bead.ID, bead.Title))
		}

		for _, bead := range rc.Beads {
			sb.WriteString(fmt.Sprintf("- [ ] In progress: [%s] %s\n", bead.ID, bead.Title))
		}

		for _, bead := range rc.BlockedBeads {
			sb.WriteString(fmt.Sprintf("- [ ] Blocked: [%s] %s\n", bead.ID, bead.Title))
		}

		sb.WriteString("\n")
	}

	sb.WriteString("Reread AGENTS.md and continue from where you left off.\n")

	return sb.String()
}

// SpawnAssignResult holds the combined result of spawn+assign workflow.
type SpawnAssignResult struct {
	Spawn  *output.SpawnResponse `json:"spawn"`
	Init   *SpawnInitResult      `json:"init,omitempty"`
	Assign *AssignOutputEnhanced `json:"assign,omitempty"`
}

// SpawnInitResult describes the init phase result.
type SpawnInitResult struct {
	PromptSent    bool `json:"prompt_sent"`
	AgentsReached int  `json:"agents_reached"`
}

// waitForAgentsReady waits for spawned agents to show ready/idle prompts.
// Returns the number of ready agents and any error.
func waitForAgentsReady(session string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second
	lastReady := 0
	lastAgents := 0

	for {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return 0, fmt.Errorf("failed to get panes: %w", err)
		}

		readyCount := 0
		agentCount := 0

		for _, pane := range panes {
			at := detectAgentTypeFromTitle(pane.Title)
			if at == "user" || at == "unknown" {
				continue
			}
			agentCount++

			scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
			state := determineAgentState(scrollback, at)
			if state == "idle" {
				readyCount++
			}
		}

		lastReady = readyCount
		lastAgents = agentCount

		if agentCount > 0 && readyCount == agentCount {
			return readyCount, nil
		}

		if time.Now().After(deadline) {
			return lastReady, fmt.Errorf("timeout waiting for agents to become ready (%d/%d ready)", lastReady, lastAgents)
		}

		time.Sleep(pollInterval)
	}
}

// sendInitPromptToReadyAgents sends the init prompt to agents that appear idle.
// Returns the number of agents that received the prompt.
func sendInitPromptToReadyAgents(session, prompt string) (int, error) {
	if strings.TrimSpace(prompt) == "" {
		return 0, nil
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return 0, fmt.Errorf("failed to get panes: %w", err)
	}

	agentsReached := 0
	var errs []string

	for _, pane := range panes {
		at := detectAgentTypeFromTitle(pane.Title)
		if at == "user" || at == "unknown" {
			continue
		}

		scrollback, _ := tmux.CapturePaneOutput(pane.ID, 10)
		state := determineAgentState(scrollback, at)
		if state != "idle" {
			continue
		}

		if err := tmux.SendKeys(pane.ID, prompt, true); err != nil {
			errs = append(errs, fmt.Sprintf("pane %d: %v", pane.Index, err))
			continue
		}
		agentsReached++
	}

	if len(errs) > 0 {
		return agentsReached, fmt.Errorf("init prompt delivery issues: %s", strings.Join(errs, "; "))
	}

	return agentsReached, nil
}

// runAssignmentPhase executes the assignment phase after spawn.
// Returns the assignment result or error.
func runAssignmentPhase(session string, opts SpawnOptions) (*AssignOutputEnhanced, error) {
	assignOpts := &AssignCommandOptions{
		Session:  session,
		Strategy: opts.AssignStrategy,
		Limit:    opts.AssignLimit,
		Verbose:  !IsJSONOutput(),
		Quiet:    IsJSONOutput(),
		Timeout:  30 * time.Second,
	}

	// Get assignment recommendations
	assignOutput, err := getAssignOutputEnhanced(assignOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignments: %w", err)
	}

	// Execute assignments (send prompts to agents)
	if err := executeAssignmentsEnhanced(session, assignOutput, assignOpts); err != nil {
		return nil, fmt.Errorf("failed to execute assignments: %w", err)
	}

	return assignOutput, nil
}
