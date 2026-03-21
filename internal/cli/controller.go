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
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ControllerInput is the kernel input for sessions.controller.
type ControllerInput struct {
	Session    string `json:"session"`
	AgentType  string `json:"agent_type,omitempty"`
	PromptFile string `json:"prompt_file,omitempty"`
	NoPrompt   bool   `json:"no_prompt,omitempty"`
}

// ControllerResponse is the JSON output for the controller command.
type ControllerResponse struct {
	output.TimestampedResponse
	Session    string `json:"session"`
	PaneID     string `json:"pane_id"`
	PaneIndex  int    `json:"pane_index"`
	AgentType  string `json:"agent_type"`
	PromptUsed string `json:"prompt_used,omitempty"`
	AgentCount int    `json:"agent_count"`
	AgentList  string `json:"agent_list,omitempty"`
}

// Default controller prompt template
const defaultControllerPrompt = `You are the controller agent for session {{.Session}}.

Current agents in this session:
{{.AgentList}}

Your role is to coordinate work among the agents, prevent conflicts, and ensure quality.

Key responsibilities:
1. Monitor agent progress using ntm commands (ntm status, ntm view)
2. Detect and resolve conflicts between agents working on related code
3. Ensure comprehensive test coverage
4. Track overall progress toward session goals

Available coordination commands:
- ntm status {{.Session}} - Check session state
- ntm view {{.Session}} - View agent outputs
- ntm send {{.Session}} --panes=N --msg="..." - Send targeted messages
- ntm interrupt {{.Session}} - Stop all agents if needed

Start by reviewing the current state of the session.`

func init() {
	// Register sessions.controller command
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.controller",
		Description: "Launch a dedicated controller agent in pane 1",
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
		return buildControllerResponse(opts)
	})
}

func newControllerCmd() *cobra.Command {
	var agentType string
	var promptFile string
	var noPrompt bool

	cmd := &cobra.Command{
		Use:   "controller <session>",
		Short: "Launch a dedicated controller agent in pane 1",
		Long: `Launch a controller agent in pane 1 of an existing session.

The controller agent coordinates work among other agents in the session,
prevents conflicts, and ensures quality.

By default, a Claude agent is launched with a coordination-focused prompt.
You can customize the agent type and prompt as needed.

Examples:
  ntm controller myproject                    # Default Claude controller
  ntm controller myproject --agent-type=cod   # Use Codex as controller
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
				Session:    args[0],
				AgentType:  agentType,
				PromptFile: promptFile,
				NoPrompt:   noPrompt,
			}
			return runController(opts)
		},
	}

	cmd.Flags().StringVar(&agentType, "agent-type", "cc", "Agent type: cc (Claude), cod (Codex), gmi (Gemini)")
	cmd.Flags().StringVar(&promptFile, "prompt", "", "Custom prompt file (supports template variables)")
	cmd.Flags().BoolVar(&noPrompt, "no-prompt", false, "Skip sending initial prompt")
	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

func runController(opts ControllerInput) error {
	// Use kernel for JSON output mode
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.controller", opts)
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return output.PrintJSON(result)
	}

	resp, err := buildControllerResponse(opts)
	if err != nil {
		return err
	}

	fmt.Printf("✓ Controller agent launched in session '%s'\n", resp.Session)
	fmt.Printf("  Pane: %d (%s)\n", resp.PaneIndex, resp.PaneID)
	fmt.Printf("  Agent type: %s\n", resp.AgentType)
	if resp.PromptUsed != "" {
		fmt.Printf("  Prompt: %s\n", resp.PromptUsed)
	}
	if resp.AgentCount > 0 {
		fmt.Printf("  Coordinating %d agent(s)\n", resp.AgentCount)
	}

	return nil
}

func buildControllerResponse(opts ControllerInput) (*ControllerResponse, error) {
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

	// Build agent list for prompt
	var agentList []string
	agentCount := 0
	for _, p := range panes {
		if p.Type == tmux.AgentClaude || p.Type == tmux.AgentCodex || p.Type == tmux.AgentGemini || p.Type == tmux.AgentCursor || p.Type == tmux.AgentWindsurf || p.Type == tmux.AgentAider {
			agentCount++
			agentList = append(agentList, fmt.Sprintf("- Pane %d: %s", p.Index, p.Type))
		}
	}

	// Determine agent type
	agentType := opts.AgentType
	if agentType == "" {
		agentType = "cc"
	}

	// Resolve agent type to full name
	var agentTypeFull string
	var agentCmdTemplate string
	switch agentType {
	case "cc", "claude":
		agentTypeFull = "claude"
		agentCmdTemplate = cfg.Agents.Claude
	case "cod", "codex":
		agentTypeFull = "codex"
		agentCmdTemplate = cfg.Agents.Codex
	case "gmi", "gemini":
		agentTypeFull = "gemini"
		agentCmdTemplate = cfg.Agents.Gemini
	default:
		return nil, fmt.Errorf("unknown agent type: %s (use cc, cod, or gmi)", agentType)
	}

	dir := cfg.GetProjectDir(session)

	// Render the agent command template (fixes raw {{}} being sent to shell)
	agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
		AgentType:   agentType,
		SessionName: session,
		PaneIndex:   1,
		ProjectDir:  dir,
	})
	if err != nil {
		return nil, fmt.Errorf("rendering agent command template: %w", err)
	}

	// Find or create pane 1
	var targetPaneID string
	var targetPaneIndex int
	pane1Found := false

	for _, p := range panes {
		if p.Index == 1 {
			pane1Found = true
			targetPaneID = p.ID
			targetPaneIndex = p.Index
			break
		}
	}

	if !pane1Found {
		// Create a new pane which will become the controller pane
		newPaneID, err := tmux.SplitWindow(session, dir)
		if err != nil {
			return nil, fmt.Errorf("creating controller pane: %w", err)
		}
		targetPaneID = newPaneID

		// Get updated pane list to find the new pane's index
		updatedPanes, err := tmux.GetPanes(session)
		if err != nil {
			return nil, fmt.Errorf("getting updated panes: %w", err)
		}
		for _, p := range updatedPanes {
			if p.ID == newPaneID {
				targetPaneIndex = p.Index
				break
			}
		}
	}

	// Set pane title
	title := tmux.FormatPaneName(session, "controller_"+agentTypeFull, 1, "")
	if err := tmux.SetPaneTitle(targetPaneID, title); err != nil {
		return nil, fmt.Errorf("setting pane title: %w", err)
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
		PromptUsed:          promptUsed,
		AgentCount:          agentCount,
		AgentList:           strings.Join(agentList, "\n"),
	}, nil
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
