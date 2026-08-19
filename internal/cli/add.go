package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
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

// promptSendFailure distinguishes a requested prompt delivery failure from
// unrelated lifecycle errors so JSON callers can make a reliable retry
// decision without parsing human-readable text.
type promptSendFailure struct {
	err error
}

func (e *promptSendFailure) Error() string {
	if e == nil || e.err == nil {
		return "prompt delivery failed"
	}
	return e.err.Error()
}

func (e *promptSendFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newPromptSendFailure(err error) error {
	if err == nil {
		return nil
	}
	return &promptSendFailure{err: err}
}

type agentLifecycleFailureResponse struct {
	output.TimestampedResponse
	Success         bool     `json:"success"`
	Session         string   `json:"session,omitempty"`
	Error           string   `json:"error"`
	ErrorCode       string   `json:"error_code"`
	Code            string   `json:"code"`
	PartialMutation bool     `json:"partial_mutation"`
	SessionMayExist bool     `json:"session_may_exist"`
	AffectedPaneIDs []string `json:"affected_pane_ids"`
	// AffectedWorktreePaths is present when spawn provisioned one or more
	// isolated checkouts before a later lifecycle failure.
	AffectedWorktreePaths []string `json:"affected_worktree_paths,omitempty"`
}

func newAgentLifecycleFailureResponse(
	err error,
	session string,
	partialMutation, sessionMayExist bool,
	affectedPaneIDs []string,
	affectedWorktreePaths []string,
) agentLifecycleFailureResponse {
	code := robot.ErrCodeInternalError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = robot.ErrCodeTimeout
	} else {
		var promptErr *promptSendFailure
		switch {
		case errors.As(err, &promptErr):
			code = robot.ErrCodePromptSendFailed
		case errors.Is(err, errCLIInvalidInput):
			code = robot.ErrCodeInvalidFlag
		}
	}
	normalizedPaneIDs := make([]string, 0, len(affectedPaneIDs))
	seenPaneIDs := make(map[string]struct{}, len(affectedPaneIDs))
	for _, paneID := range affectedPaneIDs {
		paneID = strings.TrimSpace(paneID)
		if paneID == "" {
			continue
		}
		if _, seen := seenPaneIDs[paneID]; seen {
			continue
		}
		seenPaneIDs[paneID] = struct{}{}
		normalizedPaneIDs = append(normalizedPaneIDs, paneID)
	}
	normalizedWorktreePaths := make([]string, 0, len(affectedWorktreePaths))
	seenWorktreePaths := make(map[string]struct{}, len(affectedWorktreePaths))
	for _, worktreePath := range affectedWorktreePaths {
		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath == "" {
			continue
		}
		if _, seen := seenWorktreePaths[worktreePath]; seen {
			continue
		}
		seenWorktreePaths[worktreePath] = struct{}{}
		normalizedWorktreePaths = append(normalizedWorktreePaths, worktreePath)
	}
	return agentLifecycleFailureResponse{
		TimestampedResponse:   output.NewTimestamped(),
		Success:               false,
		Session:               session,
		Error:                 err.Error(),
		ErrorCode:             code,
		Code:                  code,
		PartialMutation:       partialMutation,
		SessionMayExist:       sessionMayExist,
		AffectedPaneIDs:       normalizedPaneIDs,
		AffectedWorktreePaths: normalizedWorktreePaths,
	}
}

func prepareRequiredPersonaSystemPrompt(p *persona.Persona, projectDir string) (string, error) {
	promptFile, err := persona.PrepareSystemPrompt(p, projectDir)
	if err != nil {
		name := "<unknown>"
		if p != nil && strings.TrimSpace(p.Name) != "" {
			name = p.Name
		}
		return "", fmt.Errorf("prepare required system prompt for %s: %w", name, err)
	}
	return promptFile, nil
}

// opencodeCommandOrDefault returns the configured [agents] oc launch command,
// falling back to config.DefaultOpencodeCommand (a model-aware template)
// when it is unset. Centralizing this keeps the spawn, add, restart, and
// session-resume dispatch paths in lockstep so a model override is honored and
// Agent Mail registration receives a non-empty model everywhere. See ntm#193.
func opencodeCommandOrDefault(configured string) string {
	if configured == "" {
		return config.DefaultOpencodeCommand
	}
	return configured
}

func resolveAddAgentCommandTemplate(agentType AgentType, pluginMap map[string]plugins.AgentPlugin, ollamaHost string) (string, map[string]string, error) {
	switch agentType {
	case AgentTypeClaude:
		return cfg.Agents.Claude, nil, nil
	case AgentTypeCodex:
		return cfg.Agents.Codex, nil, nil
	case AgentTypeGemini:
		return cfg.Agents.Gemini, nil, nil
	case AgentTypeAntigravity:
		return cfg.Agents.Antigravity, nil, nil
	case AgentTypeGrok:
		return cfg.Agents.Grok, nil, nil
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
	case AgentTypeOpencode:
		// Falls back to the model-aware default when [agents] oc is unset, so
		// `ntm spawn --oc=N` and `ntm add --oc=N` behave identically and a
		// model override is honored. See ntm#193.
		return opencodeCommandOrDefault(cfg.Agents.Opencode), nil, nil
	default:
		if p, ok := pluginMap[string(agentType)]; ok {
			return p.Command, p.Env, nil
		}
		return "", nil, fmt.Errorf("unknown agent type: %s", agentType)
	}
}

// validateGrokPhaseOneAdd retains the last deliberate Grok Build add refusal
// after the GH#251 phase-2 flip: persona prompt injection (the Grok Build CLI
// has no system-prompt flag or env var). --prompt and CASS context now flow
// through the grok-aware readiness/composer/verify protocol.
func validateGrokPhaseOneAdd(opts AddOptions) error {
	for _, spec := range opts.Agents.Flatten() {
		if spec.Type != AgentTypeGrok {
			continue
		}
		if profile, ok := opts.PersonaMap[spec.Model]; ok && profile != nil {
			return errors.New("Grok Build add does not support persona prompt injection: the Grok Build CLI has no system-prompt flag or env var")
		}
	}
	return nil
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
			resolvedSessionName, dir, err := resolveWorkspaceAddSetupScope(cmd.Context(), sessionName)
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

			return runAdd(cmd.Context(), opts)
		},
	}

	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeClaude, &agentSpecs), "cc", "Claude agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCodex, &agentSpecs), "cod", "Codex agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGemini, &agentSpecs), "gmi", "Gemini agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeAntigravity, &agentSpecs), "agy", "Antigravity (agy) agents (N; model pinned to Gemini 3.1 Pro (High))")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeGrok, &agentSpecs), "grok", "Grok Build agents (N or N:model[:effort])")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeOllama, &agentSpecs), "ollama", "Ollama agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeCursor, &agentSpecs), "cursor", "Cursor agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeWindsurf, &agentSpecs), "windsurf", "Windsurf agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeAider, &agentSpecs), "aider", "Aider agents (N or N:model)")
	cmd.Flags().Var(NewAgentSpecsValue(AgentTypeOpencode, &agentSpecs), "oc", "Opencode agents (N or N:model)")
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
		registerPluginAgentFlags(cmd, p, &agentSpecs)
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

func runAdd(ctx context.Context, opts AddOptions) error {
	return executeAdd(ctx, opts, true)
}

// executeAdd performs the add workflow. Composing commands such as scale pass
// emitResult=false so the outer command remains the sole owner of terminal
// JSON output while add still returns the underlying execution error.
func executeAdd(ctx context.Context, opts AddOptions, emitResult bool) error {
	session := opts.Session
	sessionMayExist := false
	partialMutation := false
	affectedPaneIDs := []string{}
	// Install the terminal-output policy before any validation so a direct JSON
	// invocation cannot fail without an envelope, while composed callers still
	// receive the underlying error without nested output.
	outputError := func(err error) error {
		if IsJSONOutput() && emitResult {
			response := newAgentLifecycleFailureResponse(
				err, session, partialMutation, sessionMayExist, affectedPaneIDs, nil,
			)
			if encodeErr := output.PrintJSON(response); encodeErr != nil {
				return errors.Join(fmt.Errorf("encode add error response: %w", encodeErr), err)
			}
			return errors.Join(jsonFailureExit(), err)
		}
		return err
	}
	if ctx == nil {
		return outputError(errors.New("add requires a command context"))
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("add canceled: %w", err))
	}
	totalAgents := opts.Agents.TotalCount()
	if err := validateGrokPhaseOneAdd(opts); err != nil {
		return outputError(err)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	resolvedSession, err := resolveAddSession(ctx, session)
	if err != nil {
		return outputError(err)
	}
	session = resolvedSession
	opts.Session = session

	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		return outputError(fmt.Errorf("checking session %q before add: %w", session, err))
	}
	if !exists {
		return outputError(fmt.Errorf("session '%s' does not exist (use 'ntm spawn' to create)", session))
	}
	sessionMayExist = true

	if totalAgents == 0 {
		return outputError(fmt.Errorf("no agents specified"))
	}

	dir, err := resolveWorkspaceProjectDirForExplicitSession(ctx, session)
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
	// Track newly added agents so they can be registered with Agent Mail after
	// the launch loop, exactly like spawn does (#240). Without this, panes
	// added to a live session never got an Agent Mail identity, so they could
	// not send or receive mail and `ntm lock` had no identity to attribute.
	var addedAgents []spawnedAgentInfo

	// Get existing panes to determine next indices
	panes, err := tmux.GetPanesContext(ctx, session)
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
			cassResult, err := ResolveCassContextWithContext(ctx, query, dir)
			if err == nil {
				cassContext = cassResult
			}
		}
	}

	promptObserver := newSpawnSessionObserver()
	promptDispatcher := newCanonicalSpawnPromptDispatcher(session, promptObserver)
	dispatchAddedAgentPrompt := func(paneID, message string) error {
		if err := waitForSpawnPaneReady(
			ctx, session, paneID, spawnPromptReadyTimeout, spawnReadyPollInterval, promptObserver,
		); err != nil {
			return fmt.Errorf("waiting for added pane %s readiness: %w", paneID, err)
		}
		_, err := promptDispatcher.Dispatch(ctx, paneID, message)
		return err
	}

	// Add agents
	flatAgents := opts.Agents.Flatten()
	ccCount, codCount, gmiCount, agyCount, grokCount, ollamaCount, cursorCount, windsurfCount, aiderCount, opencodeCount := 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
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
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("add canceled before creating %s pane: %w", agentTypeStr, err))
		}

		paneID, err := tmux.SplitWindowContext(ctx, session, dir)
		if paneID != "" {
			partialMutation = true
			affectedPaneIDs = append(affectedPaneIDs, paneID)
		}
		if err != nil {
			return outputError(fmt.Errorf("creating pane: %w", err))
		}

		// Wait for pane to initialize before sending commands (fixes #37)
		if paneInitDelay > 0 {
			if err := waitContextDelay(ctx, paneInitDelay); err != nil {
				return outputError(fmt.Errorf("added pane initialization canceled: %w", err))
			}
		}

		// Increment index for this type
		maxIndices[agentTypeStr]++
		num := maxIndices[agentTypeStr]

		title := tmux.FormatPaneName(session, agentTypeStr, num, agent.Model)
		if err := tmux.SetPaneTitleContext(ctx, paneID, title); err != nil {
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
		case AgentTypeAntigravity:
			agyCount++
		case AgentTypeGrok:
			grokCount++
		case AgentTypeOllama:
			ollamaCount++
		case AgentTypeCursor:
			cursorCount++
		case AgentTypeWindsurf:
			windsurfCount++
		case AgentTypeAider:
			aiderCount++
		case AgentTypeOpencode:
			opencodeCount++
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
					output.PrintWarningf("Failed to configure DCG hooks for agent %d: %v", num, err)
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

		// Resolve model alias to full model name (falling back to the plugin's
		// declared default for bare plugin specs — see resolveAgentModel).
		resolvedModel := resolveAgentModel(agent.Type, agent.Model, opts.PluginMap)
		modelRequested := strings.TrimSpace(agent.Model) != ""
		// Reasoning effort comes from the direct spec (`--cc=N:model:effort`)
		// parsed onto the FlatAgent, and is overridden by the persona below when
		// one is attached — mirroring spawn.go's threading. Without this the
		// Claude template's `{{if .ReasoningEffort}} --effort ...{{end}}` clause
		// rendered nothing and an added pane silently launched at the CLI
		// default (ntm#195; same class as the spawn fix from ntm#188).
		resolvedReasoningEffort := agent.ReasoningEffort

		// Check if this is a persona agent and prepare system prompt
		var systemPromptFile string
		var personaName string
		if opts.PersonaMap != nil {
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
						"preparing system prompt for persona %s after creating pane %s: %w; the pane still exists",
						p.Name, paneID, err,
					))
				}
				systemPromptFile = promptFile
				// For persona agents, resolve the model from the persona config
				resolvedModel = resolveAgentModel(agent.Type, p.Model, opts.PluginMap)
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
			ReasoningEffort:  resolvedReasoningEffort,
		})
		if err != nil {
			return outputError(fmt.Errorf("generating command for %s agent: %w", agent.Type, err))
		}

		// Per-pane Claude credential isolation (GH#237). A pane added to an
		// existing swarm joins the same subscription as the panes already in
		// it, so skipping isolation here would put the whole session back into
		// the refresh-token race that spawn set it up to avoid.
		//
		// Applied BEFORE the plugin vars so its assignments end up rightmost,
		// closest to the command: the shell keeps the LAST of two assignments
		// to the same name, so leftmost loses.
		if agent.Type == AgentTypeClaude {
			claudeEnv, err := swarm.ProvisionClaudeIsolation(cfg, dir, session, num)
			if err != nil {
				return outputError(fmt.Errorf("isolating credentials for claude pane %d: %w", num, err))
			}
			finalCmd = claudeEnv.ApplyToCommand(finalCmd)
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
				if err := waitContextDelay(ctx, cooldown); err != nil {
					return outputError(fmt.Errorf("codex cooldown canceled before added-agent launch: %w", err))
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("add canceled before launching agent: %w", err))
		}

		cmd, err := tmux.BuildPaneCommand(dir, safeCmd)
		if err != nil {
			return outputError(fmt.Errorf("building agent command: %w", err))
		}

		if err := tmux.SendKeysContext(ctx, paneID, cmd, true); err != nil {
			launchErr := fmt.Errorf("launching agent in pane %s: %w; the pane still exists", paneID, err)
			if personaName != "" {
				launchErr = newPromptSendFailure(fmt.Errorf("sending persona %s launch prompt: %w", personaName, launchErr))
			}
			return outputError(launchErr)
		}
		if agent.Type == AgentTypeGrok {
			if _, err := tmux.WaitForPaneProcessStartContext(ctx, session, paneID); err != nil {
				return outputError(fmt.Errorf(
					"launching %s agent in pane %s did not start a stable process: %w",
					agent.Type, paneID, err,
				))
			}
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
			setupCtx, setupCancel := context.WithTimeout(ctx, geminiCfg.ReadyTimeout+geminiCfg.ModelSelectTimeout+10*time.Second)
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
			if err := waitContextDelay(ctx, 500*time.Millisecond); err != nil {
				return outputError(fmt.Errorf("CASS context injection canceled: %w", err))
			}
			if err := dispatchAddedAgentPrompt(paneID, cassContext); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return outputError(fmt.Errorf("CASS context injection canceled: %w", ctxErr))
				}
				if !IsJSONOutput() {
					fmt.Printf("⚠ Warning: failed to inject context: %v\n", err)
				}
			}
		}

		// Inject user prompt if provided
		if opts.Prompt != "" {
			if err := waitContextDelay(ctx, 200*time.Millisecond); err != nil {
				return outputError(fmt.Errorf("added-agent prompt canceled: %w", err))
			}
			if err := dispatchAddedAgentPrompt(paneID, opts.Prompt); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return outputError(fmt.Errorf("added-agent prompt canceled: %w", ctxErr))
				}
				return outputError(newPromptSendFailure(fmt.Errorf(
					"sending explicit prompt to added pane %s: %w; the pane and launched agent still exist",
					paneID, err,
				)))
			}
			// Account the delivered prompt bytes: analytics chars_sent must
			// cover every send surface, including the added-pane prompt
			// (bd-ws7-docs-ux-truth-tqh3l.4).
			events.EmitPromptSend(session, 1, len(opts.Prompt), "", agentTypeStr, false)
		}

		// Emit agent_add event. `ntm add` registrations use the dedicated
		// agent_add type (NOT agent_spawn, which analytics deliberately skips
		// because spawn-created agents are already counted by the
		// session_create provider counts) so added agents are counted exactly
		// once (bd-ws7-docs-ux-truth-tqh3l.4).
		events.Emit(events.EventAgentAdd, session, events.AgentSpawnData{
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
			PaneID:  paneID,
			Title:   title,
			Type:    agentTypeStr,
			Variant: agent.Model,
			Command: cmd,
		})

		// Track for Agent Mail registration (#240)
		addedAgents = append(addedAgents, spawnedAgentInfo{
			paneIndex:     num,
			paneID:        paneID,
			paneTitle:     title,
			agentType:     agentTypeStr,
			model:         agent.Model,
			resolvedModel: resolvedModel,
		})
	}

	// Register the newly added agents with Agent Mail so panes added to a live
	// session get identities and inboxes just like spawned ones (#240). The
	// helper self-guards on a disabled config or an unreachable server, and it
	// merges into the session's existing registry rather than replacing it, so
	// identities created by the original spawn are preserved.
	var agentMailStatus *output.AgentMailSpawnStatus
	if len(addedAgents) > 0 {
		// Session-level identity first, mirroring spawn (bd-vb7s3): idempotent
		// (reuses a previously registered coordinator identity) and best-effort,
		// so it repairs sessions whose spawn ran before Agent Mail was enabled.
		registerSessionAgent(ctx, session, dir)
		agentMailStatus = registerSpawnedAgents(ctx, dir, session, addedAgents)
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
		if !emitResult {
			return nil
		}
		livePanes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			return outputError(fmt.Errorf("refreshing added panes for JSON output: %w", err))
		}
		liveByID := make(map[string]tmux.Pane, len(livePanes))
		for _, pane := range livePanes {
			liveByID[pane.ID] = pane
		}
		for i := range newPanes {
			pane, ok := liveByID[newPanes[i].PaneID]
			if !ok {
				return outputError(fmt.Errorf("added pane %s disappeared before JSON output", newPanes[i].PaneID))
			}
			command := newPanes[i].Command
			variant := newPanes[i].Variant
			newPanes[i] = paneResponseFromTMUX(pane)
			newPanes[i].Command = command
			newPanes[i].Variant = variant
		}
		return output.PrintJSON(output.AddResponse{
			TimestampedResponse: output.NewTimestamped(),
			Session:             session,
			AddedClaude:         ccCount,
			AddedCodex:          codCount,
			AddedGemini:         gmiCount,
			AddedAntigravity:    agyCount,
			AddedGrok:           grokCount,
			AddedOllama:         ollamaCount,
			AddedCursor:         cursorCount,
			AddedWindsurf:       windsurfCount,
			AddedAider:          aiderCount,
			AddedOpencode:       opencodeCount,
			TotalAdded:          totalAgents,
			NewPanes:            newPanes,
			AgentMail:           agentMailStatus,
		})
	}

	fmt.Printf("✓ Added %d agent(s) (total %d panes now)\n", totalAgents, len(panes)+totalAgents)

	// Show "What's next?" suggestions
	output.SuccessFooter(output.AddSuggestions(session, totalAgents)...)
	return nil
}

func resolveAddSession(ctx context.Context, session string) (string, error) {
	if ctx == nil {
		return "", errors.New("add session resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	session = strings.TrimSpace(session)
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			return "", fmt.Errorf("invalid session name: %w", err)
		}
	}

	res, err := ResolveSessionWithOptionsContext(ctx, session, nil, SessionResolveOptions{TreatAsJSON: IsJSONOutput()})
	if err != nil {
		return "", err
	}
	if res.Session == "" {
		return "", fmt.Errorf("session is required")
	}
	return res.Session, nil
}

func resolveAddSetupScope(ctx context.Context, session string) (string, string, error) {
	session = strings.TrimSpace(session)
	if session == "" {
		return "", "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", "", err
	}

	resolvedSession, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
	if err != nil {
		return "", "", err
	}

	projectDir, err := resolveExplicitProjectDirForSessionContext(ctx, resolvedSession)
	if err != nil {
		return "", "", err
	}

	return resolvedSession, projectDir, nil
}

func resolveWorkspaceAddSetupScope(ctx context.Context, session string) (string, string, error) {
	resolvedSession, projectDir, err := resolveAddSetupScope(ctx, session)
	if err == nil {
		return resolvedSession, projectDir, nil
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		return "", "", err
	}

	session = strings.TrimSpace(session)
	if session == "" {
		return "", "", fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", "", err
	}

	resolvedSession, err = normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
	if err != nil {
		return "", "", err
	}

	projectDir, err = resolveWorkspaceProjectDirForExplicitSession(ctx, resolvedSession)
	if err != nil {
		return "", "", err
	}

	return resolvedSession, projectDir, nil
}
