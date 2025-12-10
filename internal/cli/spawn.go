package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var noUserPane bool
	var recipeName string
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs
	var autoRestart bool

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

Examples:
  ntm spawn myproject --cc=2 --cod=2           # 2 Claude, 2 Codex + user pane
  ntm spawn myproject --cc=3 --cod=3 --gmi=1   # 3 Claude, 3 Codex, 1 Gemini
  ntm spawn myproject --cc=4 --no-user         # 4 Claude, no user pane
  ntm spawn myproject -r full-stack            # Use full-stack recipe
  ntm spawn myproject --cc=2:opus --cc=1:sonnet  # 2 Opus + 1 Sonnet
  ntm spawn myproject --cc=2 --auto-restart    # With auto-restart enabled
  ntm spawn myproject --persona=architect --persona=implementer:2  # Using personas`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionName := args[0]
			dir := cfg.GetProjectDir(sessionName)

			// Handle personas first (they contribute to agentSpecs)
			// personaMap maps variant name to Persona for system prompt injection
			personaMap := make(map[string]*persona.Persona)
			if len(personaSpecs) > 0 {
				resolved, err := ResolvePersonas(personaSpecs, dir)
				if err != nil {
					return err
				}
				personaAgents := FlattenPersonas(resolved)

				// Add persona agents to agentSpecs with persona name as model variant
				for _, pa := range personaAgents {
					agentSpecs = append(agentSpecs, AgentSpec{
						Type:  pa.AgentType,
						Count: 1,              // Each persona agent is added individually
						Model: pa.PersonaName, // Use persona name as variant for pane naming
					})
				}

				// Build persona map for system prompt lookup
				for _, r := range resolved {
					personaMap[r.Persona.Name] = r.Persona
				}

				if !IsJSONOutput() {
					fmt.Printf("Resolved %d persona agent(s)\n", len(personaAgents))
				}
			}

			// If a recipe is specified, load it and use its agent counts
			if recipeName != "" {
				loader := recipe.NewLoader()
				r, err := loader.Get(recipeName)
				if err != nil {
					// List available recipes to help the user
					available := recipe.BuiltinNames()
					return fmt.Errorf("%w\n\nAvailable built-in recipes: %s",
						err, strings.Join(available, ", "))
				}

				// Validate the recipe
				if err := r.Validate(); err != nil {
					return fmt.Errorf("invalid recipe %q: %w", recipeName, err)
				}

				// Get counts from recipe (add to specs if no specs of that type)
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

			// Extract simple counts for backwards compatible runSpawn
			ccCount := agentSpecs.ByType(AgentTypeClaude).TotalCount()
			codCount := agentSpecs.ByType(AgentTypeCodex).TotalCount()
			gmiCount := agentSpecs.ByType(AgentTypeGemini).TotalCount()

			return runSpawnWithSpecs(sessionName, agentSpecs, ccCount, codCount, gmiCount, !noUserPane, autoRestart, recipeName, personaMap)
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

	return cmd
}

// runSpawnWithSpecs handles agent specs with model-specific pane naming
func runSpawnWithSpecs(session string, specs AgentSpecs, ccCount, codCount, gmiCount int, userPane, autoRestart bool, recipeName string, personaMap map[string]*persona.Persona) error {
	// Flatten specs to get individual agents with their models
	agents := specs.Flatten()
	return runSpawnAgentsWithRestart(session, agents, ccCount, codCount, gmiCount, userPane, autoRestart, recipeName, personaMap)
}

// Backward-compatible helper (no auto-restart)
func runSpawnAgents(session string, agents []FlatAgent, ccCount, codCount, gmiCount int, userPane bool) error {
	return runSpawnAgentsWithRestart(session, agents, ccCount, codCount, gmiCount, userPane, false, "", nil)
}

// runSpawnAgentsWithRestart launches agents with model-specific pane naming and optional auto-restart
func runSpawnAgentsWithRestart(session string, agents []FlatAgent, ccCount, codCount, gmiCount int, userPane, autoRestart bool, recipeName string, personaMap map[string]*persona.Persona) error {
	if err := spawnSessionLogic(session, agents, ccCount, codCount, gmiCount, userPane, autoRestart, recipeName, personaMap); err != nil {
		return err
	}
	return tmux.AttachOrSwitch(session)
}

// spawnSessionLogic handles the creation of the session and spawning of agents
func spawnSessionLogic(session string, agents []FlatAgent, ccCount, codCount, gmiCount int, userPane, autoRestart bool, recipeName string, personaMap map[string]*persona.Persona) error {
	// Helper for JSON error output
	outputError := func(err error) error {
		if IsJSONOutput() {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return err
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	if err := tmux.ValidateSessionName(session); err != nil {
		return outputError(err)
	}

	totalAgents := ccCount + codCount + gmiCount
	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified (use --cc, --cod, or --gmi)"))
	}

	dir := cfg.GetProjectDir(session)

	// Initialize hook executor
	hookExec, err := hooks.NewExecutorFromConfig()
	if err != nil {
		// Log warning but don't fail if hooks can't be loaded
		if !IsJSONOutput() {
			fmt.Printf("⚠ Warning: could not load hooks config: %v\n", err)
		}
		hookExec = hooks.NewExecutor(nil) // Use empty config
	}

	// Build execution context for hooks
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
		AdditionalEnv: map[string]string{
			"NTM_AGENT_COUNT_CC":    fmt.Sprintf("%d", ccCount),
			"NTM_AGENT_COUNT_COD":   fmt.Sprintf("%d", codCount),
			"NTM_AGENT_COUNT_GMI":   fmt.Sprintf("%d", gmiCount),
			"NTM_AGENT_COUNT_TOTAL": fmt.Sprintf("%d", totalAgents),
		},
	}

	// Run pre-spawn hooks
	if hookExec.HasHooksForEvent(hooks.EventPreSpawn) {
		if !IsJSONOutput() {
			fmt.Println("Running pre-spawn hooks...")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreSpawn, hookCtx)
		cancel()
		if err != nil {
			return outputError(fmt.Errorf("pre-spawn hook failed: %w", err))
		}
		if hooks.AnyFailed(results) {
			return outputError(fmt.Errorf("pre-spawn hook failed: %w", hooks.AllErrors(results)))
		}
		if !IsJSONOutput() {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d pre-spawn hook(s) completed\n", success)
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
				return fmt.Errorf("creating directory: %w", err)
			}
			fmt.Printf("Created %s\n", dir)
		}
	}

	// Calculate total panes needed
	totalPanes := totalAgents
	if userPane {
		totalPanes++
	}

	// Create or use existing session
	if !tmux.SessionExists(session) {
		if !IsJSONOutput() {
			fmt.Printf("Creating session '%s' in %s...\n", session, dir)
		}
		if err := tmux.CreateSession(session, dir); err != nil {
			return outputError(fmt.Errorf("creating session: %w", err))
		}
	}

	// Get current pane count
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}
	existingPanes := len(panes)

	// Add more panes if needed
	if existingPanes < totalPanes {
		toAdd := totalPanes - existingPanes
		if !IsJSONOutput() {
			fmt.Printf("Creating %d pane(s) (%d -> %d)...\n", toAdd, existingPanes, totalPanes)
		}
		for i := 0; i < toAdd; i++ {
			if _, err := tmux.SplitWindow(session, dir); err != nil {
				return outputError(fmt.Errorf("creating pane: %w", err))
			}
		}
	}

	// Get updated pane list
	panes, err = tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}

	// Start assigning agents (skip first pane if user pane)
	startIdx := 0
	if userPane {
		startIdx = 1
	}

	agentNum := startIdx
	if !IsJSONOutput() {
		fmt.Printf("Launching agents: %dx cc, %dx cod, %dx gmi...\n", ccCount, codCount, gmiCount)
	}

	// Track launched agents for resilience monitor
	type launchedAgent struct {
		paneID    string
		paneIndex int
		agentType string
		model     string
		command   string
	}
	var launchedAgents []launchedAgent

	// Launch agents using flattened specs (preserves model info for pane naming)
	for _, agent := range agents {
		if agentNum >= len(panes) {
			break
		}
		pane := panes[agentNum]

		// Format pane title with optional model variant
		// Format: {session}__{type}_{index} or {session}__{type}_{index}_{variant}
		title := FormatPaneName(session, agent.Type, agent.Index, agent.Model)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Get agent command template based on type
		var agentCmdTemplate string
		switch agent.Type {
		case AgentTypeClaude:
			agentCmdTemplate = cfg.Agents.Claude
		case AgentTypeCodex:
			agentCmdTemplate = cfg.Agents.Codex
		case AgentTypeGemini:
			agentCmdTemplate = cfg.Agents.Gemini
		default:
			continue
		}

		// Resolve model alias to full model name
		resolvedModel := ResolveModel(agent.Type, agent.Model)

		// Check if this is a persona agent and prepare system prompt
		var systemPromptFile string
		var personaName string
		if personaMap != nil {
			if p, ok := personaMap[agent.Model]; ok {
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

		// Generate command using template
		agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
			Model:            resolvedModel,
			ModelAlias:       agent.Model,
			SessionName:      session,
			PaneIndex:        agent.Index,
			AgentType:        string(agent.Type),
			ProjectDir:       dir,
			SystemPromptFile: systemPromptFile,
			PersonaName:      personaName,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

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

		// Track for resilience monitor
		launchedAgents = append(launchedAgents, launchedAgent{
			paneID:    pane.ID,
			paneIndex: pane.Index,
			agentType: string(agent.Type),
			model:     agent.Model,
			command:   safeAgentCmd,
		})

		agentNum++
	}

	// Get final pane list for output
	finalPanes, _ := tmux.GetPanes(session)

	// JSON output mode
	if IsJSONOutput() {
		paneResponses := make([]output.PaneResponse, len(finalPanes))
		agentCounts := output.AgentCountsResponse{}
		for i, p := range finalPanes {
			paneResponses[i] = output.PaneResponse{
				Index:   p.Index,
				Title:   p.Title,
				Type:    agentTypeToString(p.Type),
				Variant: p.Variant, // Model alias or persona name
				Active:  p.Active,
				Width:   p.Width,
				Height:  p.Height,
				Command: p.Command,
			}
			switch p.Type {
			case tmux.AgentClaude:
				agentCounts.Claude++
			case tmux.AgentCodex:
				agentCounts.Codex++
			case tmux.AgentGemini:
				agentCounts.Gemini++
			default:
				agentCounts.User++
			}
		}
		agentCounts.Total = agentCounts.Claude + agentCounts.Codex + agentCounts.Gemini

		return output.PrintJSON(output.SpawnResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			Created:             true, // spawn always creates or reuses
			WorkingDirectory:    dir,
			Panes:               paneResponses,
			AgentCounts:         agentCounts,
		})
	}

	fmt.Printf("✓ Launched %d agent(s)\n", totalAgents)

	// Emit session_create event
	events.EmitSessionCreate(session, ccCount, codCount, gmiCount, dir, recipeName)

	// Emit agent_spawn events for each agent
	for _, agent := range launchedAgents {
		events.Emit(events.EventAgentSpawn, session, events.AgentSpawnData{
			AgentType: agent.agentType,
			Model:     agent.model,
			PaneIndex: agent.paneIndex,
		})
	}

	// Run post-spawn hooks
	if hookExec.HasHooksForEvent(hooks.EventPostSpawn) {
		if !IsJSONOutput() {
			fmt.Println("Running post-spawn hooks...")
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
			fmt.Printf("⚠ Post-spawn hook error: %v\n", postErr)
		} else if hooks.AnyFailed(results) {
			// Log failures but don't fail (spawn already succeeded)
			fmt.Printf("⚠ Post-spawn hook failed: %v\n", hooks.AllErrors(results))
		} else if !IsJSONOutput() {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d post-spawn hook(s) completed\n", success)
		}
	}

	// Start resilience monitor if auto-restart is enabled
	if autoRestart || cfg.Resilience.AutoRestart {
		monitor := resilience.NewMonitor(session, dir, cfg)
		for _, agent := range launchedAgents {
			monitor.RegisterAgent(agent.paneID, agent.paneIndex, agent.agentType, agent.model, agent.command)
		}
		monitor.Start(context.Background())
		if !IsJSONOutput() {
			fmt.Printf("✓ Auto-restart enabled (health check every %ds, max %d restarts)\n",
				cfg.Resilience.HealthCheckSeconds, cfg.Resilience.MaxRestarts)
		}
		// Note: monitor runs in background, will continue until tmux session ends
	}

	// Register session as Agent Mail agent (non-blocking)
	registerSessionAgent(session, dir)

	return nil
}

// registerSessionAgent registers the session with Agent Mail.
// This is non-blocking and logs but does not fail if unavailable.
func registerSessionAgent(sessionName, workingDir string) {
	client := agentmail.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.RegisterSessionAgent(ctx, sessionName, workingDir)
	if err != nil {
		// Log but don't fail
		fmt.Printf("⚠ Agent Mail registration failed: %v\n", err)
		return
	}
	if info != nil {
		fmt.Printf("✓ Registered with Agent Mail as %s\n", info.AgentName)
	}
}
