package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

var controllerCreateDetachedWindow = func(ctx context.Context, session, directory string) (string, error) {
	return tmux.DefaultClient.RunContext(ctx, newControllerWindowArgs(session, directory)...)
}

var controllerPaneCurrentDir = func(paneID string) (string, error) {
	return tmux.DefaultClient.Run("display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
}

func newControllerWindowArgs(session, directory string) []string {
	return []string{"new-window", "-d", "-t", tmux.TargetSession(session), "-c", directory, "-n", "ntm-controller", "-P", "-F", "#{pane_id}"}
}

func createDedicatedControllerPane(ctx context.Context, session, directory string, existing []tmux.Pane) (string, error) {
	paneID, err := controllerCreateDetachedWindow(ctx, session, directory)
	if err != nil {
		return "", err
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("detached controller window returned no pane id")
	}
	for _, pane := range existing {
		if pane.ID == paneID {
			return "", fmt.Errorf("detached controller window returned existing pane id %s", paneID)
		}
	}
	return paneID, nil
}

// ControllerInput is the kernel input for sessions.controller.
type ControllerInput struct {
	Session         string `json:"session"`
	AgentType       string `json:"agent_type,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	PromptFile      string `json:"prompt_file,omitempty"`
	NoPrompt        bool   `json:"no_prompt,omitempty"`
}

// ControllerResponse is the JSON output for the controller command.
type ControllerResponse struct {
	output.TimestampedResponse
	Session         string `json:"session"`
	PaneID          string `json:"pane_id"`
	PaneIndex       int    `json:"pane_index"`
	AgentType       string `json:"agent_type"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	PromptUsed      string `json:"prompt_used,omitempty"`
	AgentCount      int    `json:"agent_count"`
	AgentList       string `json:"agent_list,omitempty"`
}

// Default controller prompt template
const defaultControllerPrompt = `You are the controller agent for session {{.Session}}.

Current agents in this session:
{{.AgentList}}

Your role is to coordinate work among the agents, prevent conflicts, and ensure quality.

Key responsibilities:
1. Monitor agent progress using ntm's machine-readable --robot-* commands
2. Detect and resolve conflicts between agents working on related code
3. Ensure comprehensive test coverage
4. Track overall progress toward session goals

Available coordination commands (prefer --robot-* for structured state; avoid interactive TUIs):

State inspection (read-only, safe to call in a loop; note the flag forms vary):
- ntm --robot-snapshot                                     - JSON snapshot of all sessions, agents, work, and health
- ntm --robot-status                                       - Tmux sessions, panes, and agent states (start here)
- ntm --robot-activity={{.Session}}                        - Per-agent activity states (idle/busy/error) for this session
- ntm --robot-tail={{.Session}} --panes=N --lines=50       - Capture recent output from pane N
- ntm --robot-attention --attention-session={{.Session}}   - Block until an agent needs attention (drives monitor loop)
- ntm mail inbox {{.Session}} --json                       - Check Agent Mail inbox for pending messages

Actions (mutating; use deliberately):
- ntm send {{.Session}} --pane W.P "message"               - Send to one exact pane (also accepts %pane_id; bare N is topology-dependent)
- ntm send {{.Session}} --panes=W.P,%N "message"            - Send to exact panes without window-local index collisions
- ntm --robot-send={{.Session}} --panes=N --msg="..."      - Robot equivalent with structured JSON response
- ntm --robot-interrupt={{.Session}} --panes=N             - Interrupt a pane without killing it

Do NOT use 'ntm view' from controller context — it changes the human operator's visual layout
and does not return output to you. Use --robot-tail or --robot-snapshot for inspection.

Start by calling ntm --robot-snapshot to survey the current state, then ntm --robot-attention
to wait for the first event that needs coordination.`

func init() {
	robot.MustRegisterSchemaCommand("controller_spawn", ControllerResponse{})

	// Register sessions.controller command
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.controller",
		Description: "Launch a dedicated controller agent in a detached window",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "ControllerInput",
			Ref:  "cli.ControllerInput",
		},
		Output: &kernel.SchemaRef{
			Name: "ControllerResponse",
			Ref:  "cli.ControllerResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/sessions/{session}/controller",
		},
		Examples: []kernel.Example{
			{
				Name:        "controller-default",
				Description: "Launch controller with default prompt",
				Command:     "ntm controller myproject",
			},
			{
				Name:        "controller-custom",
				Description: "Launch controller with custom prompt",
				Command:     "ntm controller myproject --prompt=controller.txt",
			},
			{
				Name:        "controller-codex",
				Description: "Launch controller using Codex agent",
				Command:     "ntm controller myproject --agent-type=cod",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  false,
	})
	kernel.MustRegisterHandler("sessions.controller", func(ctx context.Context, input any) (any, error) {
		opts := ControllerInput{}
		switch value := input.(type) {
		case ControllerInput:
			opts = value
		case *ControllerInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		return buildControllerResponse(ctx, opts)
	})
}

func newControllerCmd() *cobra.Command {
	var agentType string
	var model string
	var reasoningEffort string
	var promptFile string
	var noPrompt bool

	cmd := &cobra.Command{
		Use:   "controller <session>",
		Short: "Launch a dedicated controller agent in a detached window",
		Long: `Launch a controller agent in a new detached window of an existing session.

The controller agent coordinates work among other agents in the session,
prevents conflicts, and ensures quality.

By default, a Claude agent is launched with a coordination-focused prompt.
You can customize the agent type and prompt as needed.

Examples:
  ntm controller myproject                    # Default Claude controller
  ntm controller myproject --agent-type=cod   # Use Codex as controller
  ntm controller myproject --agent-type=cod --model=gpt-5.6-sol --reasoning-effort=ultra
  ntm controller myproject --prompt=ctrl.txt  # Custom prompt from file

The default prompt includes:
  - Session name and agent list
  - Coordination responsibilities
  - Available ntm commands for monitoring

Custom prompt files support template variables:
  {{.Session}}   - Session name
  {{.AgentList}} - List of other agents in the session
  {{.ProjectDir}} - Project directory path`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := ControllerInput{
				Session:         args[0],
				AgentType:       agentType,
				Model:           model,
				ReasoningEffort: reasoningEffort,
				PromptFile:      promptFile,
				NoPrompt:        noPrompt,
			}
			return runController(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&agentType, "agent-type", "cc", "Agent type: cc, cod, gmi, agy, cursor, windsurf|ws, aider, oc, or ollama")
	cmd.Flags().StringVar(&model, "model", "", "Exact model for the controller agent")
	cmd.Flags().StringVar(&reasoningEffort, "reasoning-effort", "", "Exact reasoning effort for the controller agent")
	cmd.Flags().StringVar(&promptFile, "prompt", "", "Custom prompt file (supports template variables)")
	cmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "Skip sending initial prompt")
	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

func runController(ctx context.Context, opts ControllerInput) error {
	// Use kernel for JSON output mode
	if IsJSONOutput() {
		result, err := kernel.Run(ctx, "sessions.controller", opts)
		if err != nil {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return output.PrintJSON(result)
	}

	resp, err := buildControllerResponse(ctx, opts)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Controller agent launched in session '%s'\n", resp.Session)
	fmt.Printf("  Pane: %d (%s)\n", resp.PaneIndex, resp.PaneID)
	fmt.Printf("  Agent type: %s\n", resp.AgentType)
	if resp.Model != "" {
		fmt.Printf("  Model: %s\n", resp.Model)
	}
	if resp.ReasoningEffort != "" {
		fmt.Printf("  Reasoning effort: %s\n", resp.ReasoningEffort)
	}
	if resp.PromptUsed != "" {
		fmt.Printf("  Prompt: %s\n", resp.PromptUsed)
	}
	if resp.AgentCount > 0 {
		fmt.Printf("  Coordinating %d agent(s)\n", resp.AgentCount)
	}

	return nil
}

func buildControllerResponse(ctx context.Context, opts ControllerInput) (*ControllerResponse, error) {
	session := opts.Session

	if err := tmux.EnsureInstalled(); err != nil {
		return nil, err
	}

	{
		res, err := ResolveSession(session, nil)
		if err != nil {
			return nil, err
		}
		if res.Session == "" {
			return nil, fmt.Errorf("session is required")
		}
		session = res.Session
		opts.Session = res.Session
	}

	if !tmux.SessionExists(session) {
		return nil, fmt.Errorf("session '%s' not found", session)
	}

	// Get existing panes
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("getting panes: %w", err)
	}

	// Build agent list for prompt.
	agentList, agentCount := controllerAgentList(panes)

	// Determine agent type
	agentType := opts.AgentType
	if agentType == "" {
		agentType = "cc"
	}

	// Resolve agent type to full name
	var agentTypeFull string
	var agentCmdTemplate string
	switch robot.ResolveAgentType(agentType) {
	case "claude":
		agentTypeFull = "claude"
		agentCmdTemplate = cfg.Agents.Claude
	case "codex":
		agentTypeFull = "codex"
		agentCmdTemplate = cfg.Agents.Codex
	case "gemini":
		agentTypeFull = "gemini"
		agentCmdTemplate = cfg.Agents.Gemini
	case "antigravity":
		agentTypeFull = "antigravity"
		agentCmdTemplate = cfg.Agents.Antigravity
	case "cursor":
		agentTypeFull = "cursor"
		agentCmdTemplate = cfg.Agents.Cursor
	case "windsurf", "ws":
		agentTypeFull = "windsurf"
		agentCmdTemplate = cfg.Agents.Windsurf
	case "aider":
		agentTypeFull = "aider"
		agentCmdTemplate = cfg.Agents.Aider
	case "oc":
		agentTypeFull = "opencode"
		// Mirror the spawn/add dispatch fallback so model injection works on
		// restart too. See ntm#193.
		agentCmdTemplate = opencodeCommandOrDefault(cfg.Agents.Opencode)
	case "ollama":
		agentTypeFull = "ollama"
		agentCmdTemplate = cfg.Agents.Ollama
	default:
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}

	dir, err := resolveControllerProjectDir(ctx, session, panes)
	if err != nil {
		return nil, err
	}

	// Render the agent command template (fixes raw {{}} being sent to shell)
	agentCmd, err := renderControllerAgentCommand(agentCmdTemplate, opts, agentType, agentTypeFull, session, dir)
	if err != nil {
		return nil, fmt.Errorf("rendering agent command template: %w", err)
	}

	// Per-pane Claude credential isolation (GH#237, bd-4tz2d). Every path that
	// renders a Claude launch command must apply it, or the relaunched pane
	// rejoins the shared rotating credential and its next refresh invalidates
	// every other pane's token. Returns the zero value when the feature is
	// disabled, so this is safe to call unconditionally.
	if agentTypeFull == "claude" {
		claudeEnv, isoErr := swarm.ProvisionClaudeIsolation(cfg, dir, session, 1)
		if isoErr != nil {
			return nil, fmt.Errorf("isolating credentials for claude controller pane: %w", isoErr)
		}
		agentCmd = claudeEnv.ApplyToCommand(agentCmd)
	}

	// Always create a detached dedicated window and target only the pane ID
	// returned by tmux. Pane indices repeat across windows; reusing "pane 1"
	// can overwrite a running agent in an unrelated window.
	targetPaneID, err := createDedicatedControllerPane(ctx, session, dir, panes)
	if err != nil {
		return nil, fmt.Errorf("creating detached controller window: %w", err)
	}
	targetPaneIndex := 0
	updatedPanes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("getting updated panes: %w", err)
	}
	for _, p := range updatedPanes {
		if p.ID == targetPaneID {
			targetPaneIndex = p.Index
			break
		}
	}

	// Persist the controller provider separately from its mutable display title.
	title := tmux.FormatPaneName(session, "controller_"+agentTypeFull, 1, "")
	if err := tmux.SetPaneAgentIdentity(targetPaneID, title, tmux.AgentType(agentTypeFull)); err != nil {
		return nil, fmt.Errorf("setting controller pane identity: %w", err)
	}

	// Launch the agent
	if err := tmux.SendKeys(targetPaneID, agentCmd, true); err != nil {
		return nil, fmt.Errorf("launching agent: %w", err)
	}

	// Wait briefly for agent to start
	time.Sleep(2 * time.Second)

	// Prepare and send prompt (unless --no-prompt)
	promptUsed := ""
	if !opts.NoPrompt {
		promptContent, source, err := resolveControllerPrompt(opts, session, strings.Join(agentList, "\n"), dir)
		if err != nil {
			return nil, fmt.Errorf("resolving prompt: %w", err)
		}
		promptUsed = source

		// Send the prompt
		if err := tmux.SendKeys(targetPaneID, promptContent, true); err != nil {
			return nil, fmt.Errorf("sending prompt: %w", err)
		}
	}

	return &ControllerResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		PaneID:              targetPaneID,
		PaneIndex:           targetPaneIndex,
		AgentType:           agentTypeFull,
		Model:               strings.TrimSpace(opts.Model),
		ReasoningEffort:     strings.TrimSpace(opts.ReasoningEffort),
		PromptUsed:          promptUsed,
		AgentCount:          agentCount,
		AgentList:           strings.Join(agentList, "\n"),
	}, nil
}

// resolveControllerProjectDir permits an intentionally mixed session only
// when at least one live pane still proves the configured session checkout.
// The dedicated controller is created in that checkout; unrelated utility
// panes neither redefine the project nor become controller targets.
func resolveControllerProjectDir(ctx context.Context, session string, panes []tmux.Pane) (string, error) {
	activeCfg := cfg
	if activeCfg == nil {
		activeCfg = config.Default()
	}
	if activeCfg != nil {
		configured := filepath.Clean(activeCfg.GetProjectDir(session))
		if filepath.IsAbs(configured) && util.ProjectDirScore(configured) > 0 {
			for _, pane := range panes {
				paneDir, err := controllerPaneCurrentDir(pane.ID)
				if err != nil {
					continue
				}
				if same, _ := coordinatorProjectDirsMatch(ctx, configured, paneDir); same {
					return configured, nil
				}
			}
		}
	}
	return resolveExplicitProjectDirForSessionContext(ctx, session)
}

func renderControllerAgentCommand(agentCmdTemplate string, opts ControllerInput, agentType, agentTypeFull, session, projectDir string) (string, error) {
	model := strings.TrimSpace(opts.Model)
	reasoningEffort := strings.TrimSpace(opts.ReasoningEffort)
	agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
		AgentType:       agentType,
		Model:           model,
		ModelRequested:  model != "",
		ReasoningEffort: reasoningEffort,
		SessionName:     session,
		PaneIndex:       1,
		ProjectDir:      projectDir,
	})
	if err != nil {
		return "", err
	}
	// A controller window is born inside the existing tmux server and does not
	// reliably inherit arbitrary client variables. Freeze an explicitly supplied
	// Codex profile into the one-shot pane command instead of falling back to the
	// profile embedded in the user's agent template.
	if agentTypeFull == "codex" {
		if profile := strings.TrimSpace(os.Getenv("NTM_CODEX_PROFILE")); profile != "" {
			agentCmd = "export NTM_CODEX_PROFILE=" + config.ShellQuote(profile) + "; " + agentCmd
		}
	}
	return agentCmd, nil
}

func controllerAgentList(panes []tmux.Pane) ([]string, int) {
	list := make([]string, 0, len(panes))
	count := 0
	for _, p := range panes {
		canonical := p.Type.Canonical()
		switch canonical {
		case tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini, tmux.AgentCursor, tmux.AgentWindsurf, tmux.AgentAider, tmux.AgentOpencode, tmux.AgentOllama:
			count++
			list = append(list, fmt.Sprintf("- Pane %d: %s", p.Index, canonical))
		}
	}
	return list, count
}

// resolveControllerPrompt resolves the controller prompt from file or default.
// Returns the prompt content, source description, and any error.
func resolveControllerPrompt(opts ControllerInput, session, agentList, projectDir string) (string, string, error) {
	data := struct {
		Session    string
		AgentList  string
		ProjectDir string
	}{
		Session:    session,
		AgentList:  agentList,
		ProjectDir: projectDir,
	}

	var promptTemplate string
	var source string

	if opts.PromptFile != "" {
		// Load from file
		content, err := os.ReadFile(opts.PromptFile)
		if err != nil {
			return "", "", fmt.Errorf("reading prompt file: %w", err)
		}
		promptTemplate = string(content)
		source = filepath.Base(opts.PromptFile)
	} else {
		// Use default
		promptTemplate = defaultControllerPrompt
		source = "default"
	}

	// Parse and execute template
	tmpl, err := template.New("prompt").Parse(promptTemplate)
	if err != nil {
		return "", "", fmt.Errorf("parsing prompt template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), source, nil
}
