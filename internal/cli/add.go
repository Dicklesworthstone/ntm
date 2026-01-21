package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/gemini"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// AddOptions configures agent addition
type AddOptions struct {
	Session          string
	Agents           AgentSpecs
	PluginMap        map[string]plugins.AgentPlugin
	PersonaMap       map[string]*persona.Persona
	CassContextQuery string
	NoCassContext    bool
	Prompt           string
}

func newAddCmd() *cobra.Command {
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs
	var contextQuery string
	var noCassContext bool
	var contextLimit int
	var contextDays int
	var prompt string

	cmd := &cobra.Command{
		Use:   "add <session-name>",
		Short: "Add more agents to an existing session",
		Long: `Add additional AI agents to an existing tmux session.

		You can specify agent counts and optional model variants:
	  ntm add myproject --cc=2           # Add 2 Claude agents (default model)
	  ntm add myproject --cc=1:opus      # Add 1 Claude Opus agent
	  ntm add myproject --cod=1 --gmi=1  # Add 1 Codex, 1 Gemini

		Persona mode:
	  Use --persona to add agents with predefined roles and system prompts.
	  Built-in personas: architect, implementer, reviewer, tester, documenter
	  ntm add myproject --persona=reviewer  # Add 1 reviewer agent

		CASS Context Injection:
	  Automatically finds relevant past sessions and injects context into new agents.
	  Use --cass-context="query" to be specific.

		Agent count syntax: N or N:model where N is count and model is optional.
		Multiple flags of the same type accumulate.`,
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

			// Load plugins (re-load here to ensure latest state and to pass map)
			// Ideally we should share this logic or load once.
			configDir := filepath.Dir(config.DefaultPath())
			pluginsDir := filepath.Join(configDir, "agents")
			loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
			pluginMap := make(map[string]plugins.AgentPlugin)
			for _, p := range loadedPlugins {
				pluginMap[p.Name] = p
				if p.Alias != "" {
					pluginMap[p.Alias] = p
				}
			}

			// Handle personas (they contribute to agentSpecs)
			personaMap := make(map[string]*persona.Persona)
			if len(personaSpecs) > 0 {
				resolved, err := ResolvePersonas(personaSpecs, dir)
				if err != nil {
					return err
				}
				personaAgents := FlattenPersonas(resolved)

				// Add persona agents to agentSpecs with persona name as variant
				for _, pa := range personaAgents {
					agentSpecs = append(agentSpecs, AgentSpec{
						Type:  pa.AgentType,
						Count: 1,
						Model: pa.PersonaName, // Use persona name as variant
					})
				}
				for _, r := range resolved {
					personaMap[r.Persona.Name] = r.Persona
				}

				if !IsJSONOutput() {
					fmt.Printf("Resolved %d persona agent(s)\n", len(personaAgents))
				}
			}

			opts := AddOptions{
				Session:          sessionName,
				Agents:           agentSpecs,
				PluginMap:        pluginMap,
				PersonaMap:       personaMap,
				CassContextQuery: contextQuery,
				NoCassContext:    noCassContext,
				Prompt:           prompt,
			}

			return runAdd(opts)
		},
	}

	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeClaude, &agentSpecs), "cc", "Claude agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCodex, &agentSpecs), "cod", "Codex agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGemini, &agentSpecs), "gmi", "Gemini agents (N or N:model)")
	cmd.Flags().Var(&personaSpecs, "persona", "Persona-defined agents (name or name:count)")

	// CASS context flags
	cmd.Flags().StringVar(&contextQuery, "cass-context", "", "Explicit context query for CASS")
	cmd.Flags().BoolVar(&noCassContext, "no-cass-context", false, "Disable CASS context injection")
	cmd.Flags().IntVar(&contextLimit, "cass-context-limit", 0, "Max past sessions to include")
	cmd.Flags().IntVar(&contextDays, "cass-context-days", 0, "Look back N days")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to initialize agents with")

	// Register plugin flags
	configDir := filepath.Dir(config.DefaultPath())
	pluginsDir := filepath.Join(configDir, "agents")
	loadedPlugins, _ := plugins.LoadAgentPlugins(pluginsDir)
	for _, p := range loadedPlugins {
		agentType := AgentType(p.Name)
		cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Name, p.Description)
		if p.Alias != "" {
			cmd.Flags().Var(NewAgentSpecsValue(agentType, &agentSpecs), p.Alias, p.Description+" (alias)")
		}
	}

	return cmd
}

func runAdd(opts AddOptions) error {
	totalAgents := opts.Agents.TotalCount()
	session := opts.Session

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

	if !tmux.SessionExists(session) {
		return outputError(fmt.Errorf("session '%s' does not exist (use 'ntm spawn' to create)", session))
	}

	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified"))
	}

	dir := cfg.GetProjectDir(session)

	// Initialize hook executor
	hookExec, err := hooks.NewExecutorFromConfig()
	if err != nil {
		if !IsJSONOutput() {
			fmt.Printf("⚠ Warning: could not load hooks config: %v\n", err)
		}
		hookExec = hooks.NewExecutor(nil)
	}

	ctx := context.Background()
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
	}

	// Run pre-add hooks
	if hookExec.HasHooksForEvent(hooks.EventPreAdd) {
		if !IsJSONOutput() {
			fmt.Println("Running pre-add hooks...")
		}
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreAdd, hookCtx)
		if err != nil {
			return outputError(fmt.Errorf("pre-add hooks failed: %w", err))
		}
		if hooks.AnyFailed(results) {
			return outputError(hooks.AllErrors(results))
		}
	}

	if !IsJSONOutput() {
		fmt.Printf("Adding %d agent(s) to session '%s'...\n", totalAgents, session)
	}

	// Auto-checkpoint before adding many agents
	if cfg.Checkpoints.Enabled && cfg.Checkpoints.BeforeAddAgents > 0 && totalAgents >= cfg.Checkpoints.BeforeAddAgents {
		if !IsJSONOutput() {
			fmt.Println("Creating auto-checkpoint before adding agents...")
		}
		autoCP := checkpoint.NewAutoCheckpointer()
		cp, err := autoCP.Create(checkpoint.AutoCheckpointOptions{
			SessionName:     session,
			Reason:          checkpoint.ReasonAddAgents,
			Description:     fmt.Sprintf("before adding %d agents", totalAgents),
			ScrollbackLines: cfg.Checkpoints.ScrollbackLines,
			IncludeGit:      cfg.Checkpoints.IncludeGit,
			MaxCheckpoints:  cfg.Checkpoints.MaxAutoCheckpoints,
		})
		if err != nil {
			// Log warning but continue - auto-checkpoint is best-effort
			if !IsJSONOutput() {
				fmt.Printf("⚠ Auto-checkpoint failed: %v\n", err)
			}
		} else if !IsJSONOutput() {
			fmt.Printf("✓ Auto-checkpoint created: %s\n", cp.ID)
		}
	}

	// Track newly added panes for JSON output
	var newPanes []output.PaneResponse

	// Get existing panes to determine next indices
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}

	maxIndices := make(map[string]int)

	// Helper to parse index from title
	parseIndex := func(title string) {
		parts := strings.Split(title, "__")
		if len(parts) >= 2 {
			sub := parts[1]
			subParts := strings.Split(sub, "_")
			// Iterate to find the index part
			for i, p := range subParts {
				if num, err := strconv.Atoi(p); err == nil && num > 0 {
					typeStr := strings.Join(subParts[:i], "_")
					if num > maxIndices[typeStr] {
						maxIndices[typeStr] = num
					}
					break
				}
			}
		}
	}

	for _, p := range panes {
		parseIndex(p.Title)
	}

	// Resolve CASS context if enabled
	var cassContext string
	if !opts.NoCassContext && cfg.CASS.Context.Enabled {
		query := opts.CassContextQuery
		if query == "" {
			query = opts.Prompt // Use prompt if available
		}
		// Unlike spawn, we don't have a RecipeName fallback for context here easily
		// unless we assume context from session name? No, that's risky.

		if query != "" {
			ctx, err := ResolveCassContext(query, dir)
			if err == nil {
				cassContext = ctx
			}
		}
	}

	// Add agents
	flatAgents := opts.Agents.Flatten()
	ccCount, codCount, gmiCount := 0, 0, 0

	for _, agent := range flatAgents {
		agentTypeStr := string(agent.Type)

		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		// Increment index for this type
		maxIndices[agentTypeStr]++
		num := maxIndices[agentTypeStr]

		title := tmux.FormatPaneName(session, agentTypeStr, num, agent.Model)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command
		var agentCmd string
		var envVars map[string]string

		switch agent.Type {
		case AgentTypeClaude:
			agentCmd = cfg.Agents.Claude
			ccCount++
		case AgentTypeCodex:
			agentCmd = cfg.Agents.Codex
			codCount++
		case AgentTypeGemini:
			agentCmd = cfg.Agents.Gemini
			gmiCount++
		default:
			if p, ok := opts.PluginMap[agentTypeStr]; ok {
				agentCmd = p.Command
				envVars = p.Env
			} else {
				return outputError(fmt.Errorf("unknown agent type: %s", agent.Type))
			}
		}

		// Configure DCG hooks for Claude agents when DCG integration is enabled
		if agent.Type == AgentTypeClaude && cfg.Integrations.DCG.Enabled {
			if dcg.ShouldConfigureHooks(cfg.Integrations.DCG.Enabled, cfg.Integrations.DCG.BinaryPath) {
				dcgOpts := dcg.DCGHookOptions{
					BinaryPath:      cfg.Integrations.DCG.BinaryPath,
					AuditLog:        cfg.Integrations.DCG.AuditLog,
					Timeout:         5000, // 5 second timeout for hook
					CustomBlocklist: cfg.Integrations.DCG.CustomBlocklist,
					CustomWhitelist: cfg.Integrations.DCG.CustomWhitelist,
				}
				dcgEnvVars, err := dcg.HookEnvVars(dcgOpts)
				if err == nil {
					if envVars == nil {
						envVars = make(map[string]string)
					}
					for k, v := range dcgEnvVars {
						envVars[k] = v
					}
					if !IsJSONOutput() {
						output.PrintInfof("DCG hooks configured for agent %d", num)
					}
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure DCG hooks for agent %d: %v", num, err)
				}
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

		finalCmd, err := config.GenerateAgentCommand(agentCmd, config.AgentTemplateVars{
			Model:            resolvedModel,
			ModelAlias:       agent.Model,
			SessionName:      session,
			PaneIndex:        num,
			AgentType:        agentTypeStr,
			ProjectDir:       dir,
			SystemPromptFile: systemPromptFile,
			PersonaName:      personaName,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Apply plugin env vars
		if len(envVars) > 0 {
			var envPrefix string
			for k, v := range envVars {
				envPrefix += fmt.Sprintf("%s=%s ", k, tmux.ShellQuote(v))
			}
			finalCmd = envPrefix + finalCmd
		}

		safeCmd, err := tmux.SanitizePaneCommand(finalCmd)
		if err != nil {
			return outputError(fmt.Errorf("invalid agent command: %w", err))
		}

		cmd, err := tmux.BuildPaneCommand(dir, safeCmd)
		if err != nil {
			return outputError(fmt.Errorf("building agent command: %w", err))
		}

		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}

		// Gemini post-spawn setup: auto-select Pro model
		if agent.Type == AgentTypeGemini && cfg.GeminiSetup.AutoSelectProModel {
			geminiCfg := gemini.SetupConfig{
				AutoSelectProModel: cfg.GeminiSetup.AutoSelectProModel,
				ReadyTimeout:       time.Duration(cfg.GeminiSetup.ReadyTimeoutSeconds) * time.Second,
				ModelSelectTimeout: time.Duration(cfg.GeminiSetup.ModelSelectTimeoutSeconds) * time.Second,
				PollInterval:       500 * time.Millisecond,
				Verbose:            cfg.GeminiSetup.Verbose,
			}
			setupCtx, setupCancel := context.WithTimeout(context.Background(), geminiCfg.ReadyTimeout+geminiCfg.ModelSelectTimeout+10*time.Second)
			// Defer cancel is safer here, but since we are in a loop, defer runs at function exit.
			// So we must cancel manually or wrap in func.
			func() {
				defer setupCancel()
				if err := gemini.PostSpawnSetup(setupCtx, paneID, geminiCfg); err != nil {
					if !IsJSONOutput() {
						fmt.Printf("⚠ Warning: Gemini Pro model setup failed: %v\n", err)
					}
					// Don't fail spawn
				} else {
					if !IsJSONOutput() && cfg.GeminiSetup.Verbose {
						fmt.Printf("✓ Gemini %d configured for Pro model\n", num)
					}
				}
			}()
		}

		// Inject CASS context if available
		if cassContext != "" {
			// Wait a bit for agent to start
			time.Sleep(500 * time.Millisecond)
			if err := sendPromptWithDoubleEnter(paneID, cassContext); err != nil {
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: failed to inject context: %v\n", err)
				}
			}
		}

		// Inject user prompt if provided
		if opts.Prompt != "" {
			time.Sleep(200 * time.Millisecond)
			if err := sendPromptWithDoubleEnter(paneID, opts.Prompt); err != nil {
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: failed to send prompt: %v\n", err)
				}
			}
		}

		// Emit agent_spawn event
		events.Emit(events.EventAgentSpawn, session, events.AgentSpawnData{
			AgentType: agentTypeStr,
			Model:     resolvedModel,
			Variant:   agent.Model,
			PaneIndex: num,
		})

		// Track for JSON output
		newPanes = append(newPanes, output.PaneResponse{
			Title:   title,
			Type:    agentTypeStr,
			Variant: agent.Model,
			Command: cmd,
		})
	}

	// Run post-add hooks
	if hookExec.HasHooksForEvent(hooks.EventPostAdd) {
		if !IsJSONOutput() {
			fmt.Println("Running post-add hooks...")
		}
		// Update context with new pane info? Optional.
		_, _ = hookExec.RunHooksForEvent(ctx, hooks.EventPostAdd, hookCtx)
	}

	// JSON output mode
	if IsJSONOutput() {
		return output.PrintJSON(output.AddResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			AddedClaude:         ccCount,
			AddedCodex:          codCount,
			AddedGemini:         gmiCount,
			TotalAdded:          totalAgents,
			NewPanes:            newPanes,
		})
	}

	fmt.Printf("✓ Added %d agent(s) (total %d panes now)\n", totalAgents, len(panes)+totalAgents)

	// Show "What's next?" suggestions
	output.SuccessFooter(output.AddSuggestions(session, totalAgents)...)
	return nil
}
