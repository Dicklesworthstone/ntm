package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
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
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
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

func resolveAddAgentCommandTemplate(agentType AgentType, pluginMap map[string]plugins.AgentPlugin, ollamaHost string) (string, map[string]string, error) {
	switch agentType {
	case AgentTypeClaude:
		return cfg.Agents.Claude, nil, nil
	case AgentTypeCodex:
		return cfg.Agents.Codex, nil, nil
	case AgentTypeGemini:
		return cfg.Agents.Gemini, nil, nil
	case AgentTypeOllama:
		if ollamaHost == "" {
			return cfg.Agents.Ollama, nil, nil
		}
		return cfg.Agents.Ollama, map[string]string{"OLLAMA_HOST": ollamaHost}, nil
	case AgentTypeCursor:
		return cfg.Agents.Cursor, nil, nil
	case AgentTypeWindsurf:
		return cfg.Agents.Windsurf, nil, nil
	case AgentTypeAider:
		return cfg.Agents.Aider, nil, nil
	default:
		if p, ok := pluginMap[string(agentType)]; ok {
			return p.Command, p.Env, nil
		}
		return "", nil, fmt.Errorf("unknown agent type: %s", agentType)
	}
}

func newAddCmd() *cobra.Command {
	var agentSpecs AgentSpecs
	var personaSpecs PersonaSpecs
	var contextQuery string
	var noCassContext bool
	var contextLimit int
	var contextDays int
	var prompt string
	var label string

	cmd := &cobra.Command{
		Use:   "add <session-name>",
		Short: "Add more agents to an existing session",
		Long: `Add additional AI agents to an existing tmux session.

		You can specify agent counts and optional model variants:
	  ntm add myproject --cc=2           # Add 2 Claude agents (default model)
	  ntm add myproject --cc=1:opus      # Add 1 Claude Opus agent
	  ntm add myproject --cod=1 --gmi=1  # Add 1 Codex, 1 Gemini

		With --label, target a labeled session:
	  ntm add myproject --label frontend --cc=1  # Add to myproject--frontend

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

			// Validate project name unconditionally: "--" is reserved for labels.
			if err := config.ValidateProjectName(sessionName); err != nil {
				return err
			}

			// Apply and validate optional label (bd-1933u)
			if label != "" {
				if err := config.ValidateLabel(label); err != nil {
					return fmt.Errorf("invalid label: %w", err)
				}
				sessionName = config.FormatSessionName(sessionName, label)
			}
			resolvedSessionName, dir, err := resolveAddSetupScope(sessionName)
			if err != nil {
				return err
			}
			sessionName = resolvedSessionName

			// Update CASS config from flags
			if contextLimit > 0 {
				cfg.CASS.Context.MaxSessions = contextLimit
			}
			if contextDays > 0 {
				cfg.CASS.Context.LookbackDays = contextDays
			}

			// Load plugins (re-load here to ensure latest state and to pass map)
			// Ideally we should share this logic or load once.
			pluginsDir := filepath.Join(selectedConfigDir(), "agents")
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
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeOllama, &agentSpecs), "ollama", "Ollama agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCursor, &agentSpecs), "cursor", "Cursor agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeWindsurf, &agentSpecs), "windsurf", "Windsurf agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeAider, &agentSpecs), "aider", "Aider agents (N or N:model)")
	cmd.Flags().Var(&personaSpecs, "persona", "Persona-defined agents (name or name:count)")

	// Goal label for multi-session support (bd-1933u)
	cmd.Flags().StringVarP(&label, "label", "l", "", "Goal label for multi-session support (e.g., --label frontend targets session PROJECT--frontend)")

	// CASS context flags
	cmd.Flags().StringVar(&contextQuery, "cass-context", "", "Explicit context query for CASS")
	cmd.Flags().BoolVar(&noCassContext, "no-cass-context", false, "Disable CASS context injection")
	cmd.Flags().IntVar(&contextLimit, "cass-context-limit", 0, "Max past sessions to include")
	cmd.Flags().IntVar(&contextDays, "cass-context-days", 0, "Look back N days")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to initialize agents with")

	// Register plugin flags
	pluginsDir := pluginAgentsDirForArgs(os.Args[1:])
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

func paneTitleTypeAndIndex(title string) (string, int, bool) {
	suffix := tmux.PaneTitleSuffix(title)
	if suffix == "" {
		return "", 0, false
	}
	if idx := strings.LastIndex(suffix, "["); idx >= 0 && strings.HasSuffix(suffix, "]") {
		suffix = suffix[:idx]
	}

	parts := strings.Split(suffix, "_")
	for i, part := range parts {
		num, err := strconv.Atoi(part)
		if err != nil || num <= 0 {
			continue
		}
		typeStr := strings.Join(parts[:i], "_")
		if typeStr == "" {
			return "", 0, false
		}
		if canonical := tmux.AgentType(typeStr).Canonical(); canonical.IsValid() {
			typeStr = string(canonical)
		}
		return typeStr, num, true
	}

	return "", 0, false
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

	resolvedSession, err := resolveAddSession(session)
	if err != nil {
		return outputError(err)
	}
	session = resolvedSession
	opts.Session = session

	if !tmux.SessionExists(session) {
		return outputError(fmt.Errorf("session '%s' does not exist (use 'ntm spawn' to create)", session))
	}

	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified"))
	}

	dir, err := resolveExplicitProjectDirForSession(session)
	if err != nil {
		return outputError(err)
	}

	// Enable project webhooks (if configured) so add lifecycle events can fan out.
	// Best-effort: failures should not block add.
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, err := webhook.StartBridgeFromProjectConfig(dir, session, events.DefaultBus, &redactCfg)
		if err != nil {
			slog.Default().Debug("webhook bridge init failed", "session", session, "error", err)
		} else if bridge != nil {
			defer bridge.Close()
		}
	}

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
		typeStr, num, ok := paneTitleTypeAndIndex(title)
		if ok && num > maxIndices[typeStr] {
			maxIndices[typeStr] = num
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
	ccCount, codCount, gmiCount, ollamaCount, cursorCount, windsurfCount, aiderCount := 0, 0, 0, 0, 0, 0, 0
	var rateLimitTracker *ratelimit.RateLimitTracker
	openAICooldownWaited := false
	ollamaHost := ""
	needsCodexTracker := false
	needsOllamaHost := false

	for _, agent := range flatAgents {
		switch agent.Type {
		case AgentTypeCodex:
			needsCodexTracker = true
		case AgentTypeOllama:
			needsOllamaHost = true
		}
	}

	if needsCodexTracker {
		rateLimitTracker = ratelimit.NewRateLimitTracker(dir)
		if err := rateLimitTracker.LoadFromDir(dir); err != nil && !IsJSONOutput() {
			output.PrintWarningf("Failed to load rate limit history: %v", err)
		}
	}

	if needsOllamaHost {
		ollamaHost = resolveOllamaHost("")
	}

	// Get pane initialization delay from config (same as spawn command)
	paneInitDelay := time.Duration(cfg.Tmux.PaneInitDelayMs) * time.Millisecond
	if flag.Lookup("test.v") != nil {
		// Under `go test`, avoid the full init delay but keep a small floor
		const testPaneInitDelay = 50 * time.Millisecond
		if paneInitDelay > testPaneInitDelay {
			paneInitDelay = testPaneInitDelay
		}
	}

	for _, agent := range flatAgents {
		agentTypeStr := string(agent.Type)

		paneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		// Wait for pane to initialize before sending commands (fixes #37)
		if paneInitDelay > 0 {
			time.Sleep(paneInitDelay)
		}

		// Increment index for this type
		maxIndices[agentTypeStr]++
		num := maxIndices[agentTypeStr]

		title := tmux.FormatPaneName(session, agentTypeStr, num, agent.Model)
		if err := tmux.SetPaneTitle(paneID, title); err != nil {
			return outputError(fmt.Errorf("setting pane title: %w", err))
		}

		// Generate command
		agentCmd, envVars, err := resolveAddAgentCommandTemplate(agent.Type, opts.PluginMap, ollamaHost)
		if err != nil {
			return outputError(err)
		}

		switch agent.Type {
		case AgentTypeClaude:
			ccCount++
		case AgentTypeCodex:
			codCount++
		case AgentTypeGemini:
			gmiCount++
		case AgentTypeOllama:
			ollamaCount++
		case AgentTypeCursor:
			cursorCount++
		case AgentTypeWindsurf:
			windsurfCount++
		case AgentTypeAider:
			aiderCount++
		}

		// Configure Claude hooks for DCG and RCH integrations
		if agent.Type == AgentTypeClaude {
			var preToolHooks []dcg.HookEntry
			var hookSources []string

			if cfg.Integrations.DCG.Enabled && dcg.ShouldConfigureHooks(cfg.Integrations.DCG.Enabled, cfg.Integrations.DCG.BinaryPath) {
				customWhitelist := cfg.Integrations.DCG.CustomWhitelist
				if cfg.Integrations.RCH.Enabled && cfg.Integrations.RCH.DCGWhitelist {
					customWhitelist = dcg.AppendRCHWhitelist(customWhitelist)
				}
				dcgOpts := dcg.DCGHookOptions{
					BinaryPath:      cfg.Integrations.DCG.BinaryPath,
					AuditLog:        cfg.Integrations.DCG.AuditLog,
					Timeout:         5000, // 5 second timeout for hook
					CustomBlocklist: cfg.Integrations.DCG.CustomBlocklist,
					CustomWhitelist: customWhitelist,
				}
				dcgConfig, err := dcg.GenerateHookConfig(dcgOpts)
				if err == nil {
					preToolHooks = append(preToolHooks, dcgConfig.Hooks.PreToolUse...)
					hookSources = append(hookSources, "dcg")
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure DCG hooks for agent %d: %v", num, err)
				}
			}

			if dcg.ShouldConfigureRCHHooks(cfg.Integrations.RCH.Enabled, cfg.Integrations.RCH.InterceptPatterns) {
				rchHook, err := dcg.GenerateRCHHookEntry(dcg.RCHHookOptions{
					BinaryPath: cfg.Integrations.RCH.BinaryPath,
					Patterns:   cfg.Integrations.RCH.InterceptPatterns,
					Timeout:    5000,
				})
				if err == nil {
					preToolHooks = append(preToolHooks, rchHook)
					hookSources = append(hookSources, "rch")
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure RCH hooks for agent %d: %v", num, err)
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
						output.PrintInfof("Claude hooks configured for agent %d (%s)", num, strings.Join(hookSources, ", "))
					}
				} else if !IsJSONOutput() {
					output.PrintWarningf("Failed to configure Claude hooks for agent %d: %v", num, err)
				}
			}
		}

		// Resolve model alias to full model name
		resolvedModel := ResolveModel(agent.Type, agent.Model)
		modelRequested := strings.TrimSpace(agent.Model) != ""

		// Check if this is a persona agent and prepare system prompt
		var systemPromptFile string
		var personaName string
		if opts.PersonaMap != nil {
			if p, ok := opts.PersonaMap[agent.Model]; ok {
				personaName = p.Name
				modelRequested = strings.TrimSpace(p.Model) != ""
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
			ModelRequested:   modelRequested,
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

		if agent.Type == AgentTypeCodex {
			var cooldown time.Duration
			cooldown, openAICooldownWaited = codexCooldownRemaining(rateLimitTracker, openAICooldownWaited)
			if cooldown > 0 {
				if !IsJSONOutput() {
					output.PrintWarningf("Codex cooldown active; waiting %s before launching", ratelimit.FormatDelay(cooldown))
				}
				time.Sleep(cooldown)
			}
		}

		cmd, err := tmux.BuildPaneCommand(dir, safeCmd)
		if err != nil {
			return outputError(fmt.Errorf("building agent command: %w", err))
		}

		if err := tmux.SendKeys(paneID, cmd, true); err != nil {
			return outputError(fmt.Errorf("launching agent: %w", err))
		}
		if rateLimitTracker != nil && agent.Type == AgentTypeCodex {
			rateLimitTracker.RecordSuccess("openai")
			if err := rateLimitTracker.SaveToDir(dir); err != nil && !IsJSONOutput() {
				output.PrintWarningf("Failed to persist rate limit history: %v", err)
			}
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
						fmt.Printf("  (Agent is running with default model. To disable auto-setup: set gemini_setup.auto_select_pro_model = false in config)\n")
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

		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookAgentStarted,
			session,
			paneID,
			agentTypeStr,
			fmt.Sprintf("Agent started (%s)", agentTypeStr),
			map[string]string{
				"project_dir":    dir,
				"pane_index":     fmt.Sprintf("%d", num),
				"pane_title":     title,
				"model":          agent.Model,
				"resolved_model": resolvedModel,
			},
		))

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
			AddedOllama:         ollamaCount,
			AddedCursor:         cursorCount,
			AddedWindsurf:       windsurfCount,
			AddedAider:          aiderCount,
			TotalAdded:          totalAgents,
			NewPanes:            newPanes,
		})
	}

	fmt.Printf("✓ Added %d agent(s) (total %d panes now)\n", totalAgents, len(panes)+totalAgents)

	// Show "What's next?" suggestions
	output.SuccessFooter(output.AddSuggestions(session, totalAgents)...)
	return nil
}

func resolveAddSession(session string) (string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			return "", fmt.Errorf("invalid session name: %w", err)
		}
	}

	res, err := ResolveSessionWithOptions(session, nil, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		return "", err
	}
	if res.Session == "" {
		return "", fmt.Errorf("session is required")
	}
	return res.Session, nil
}

func resolveAddSetupScope(session string) (string, string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", "", err
	}

	resolvedSession, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
	if err != nil {
		return "", "", err
	}

	projectDir, err := resolveExplicitProjectDirForSession(resolvedSession)
	if err != nil {
		return "", "", err
	}

	return resolvedSession, projectDir, nil
}
