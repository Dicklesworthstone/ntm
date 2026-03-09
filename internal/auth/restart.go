package auth

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/rotation"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Orchestrator manages the restart process
type Orchestrator struct {
	cfg                 *config.Config
	authFlows           map[string]AuthFlow
	captureOutput       func(string, int) (string, error)
	sendKeys            func(string, string, bool) error
	sendInterrupt       func(string) error
	buildPaneCommand    func(string, string) (string, error)
	sanitizePaneCommand func(string) (string, error)
	sleep               func(time.Duration)
}

// AuthFlow interface for provider-specific auth actions
type AuthFlow interface {
	InitiateAuth(paneID string) error
	// Add other methods as needed
}

// NewOrchestrator creates a new Orchestrator
func NewOrchestrator(cfg *config.Config) *Orchestrator {
	return &Orchestrator{
		cfg:                 cfg,
		authFlows:           make(map[string]AuthFlow),
		captureOutput:       tmux.CapturePaneOutput,
		sendKeys:            tmux.SendKeys,
		sendInterrupt:       tmux.SendInterrupt,
		buildPaneCommand:    tmux.BuildPaneCommand,
		sanitizePaneCommand: tmux.SanitizePaneCommand,
		sleep:               time.Sleep,
	}
}

// RegisterAuthFlow registers a flow for a provider
func (o *Orchestrator) RegisterAuthFlow(provider string, flow AuthFlow) {
	o.authFlows[provider] = flow
}

// RestartContext holds context for restarting an agent
type RestartContext struct {
	PaneID      string
	Provider    string
	TargetEmail string
	ModelAlias  string
	SessionName string
	PaneIndex   int
	ProjectDir  string
}

// ExecuteRestartStrategy performs the terminate-switch-restart flow
func (o *Orchestrator) ExecuteRestartStrategy(ctx RestartContext) error {
	// 1. Terminate existing session gracefully
	if err := o.TerminateSession(ctx.PaneID, ctx.Provider); err != nil {
		return fmt.Errorf("terminating session: %w", err)
	}

	// 2. Wait for shell prompt
	if err := o.WaitForShellPrompt(ctx.PaneID, 10*time.Second); err != nil {
		return fmt.Errorf("session did not terminate: %w", err)
	}

	// 3. Prompt user for browser auth (simulated here, would interact with UI/TUI in real app)
	o.PromptBrowserAuth(ctx.TargetEmail)

	// 4. Start new agent session
	return o.StartNewAgentSession(ctx)
}

// TerminateSession tries to gracefully stop the agent, then force kills if needed
func (o *Orchestrator) TerminateSession(paneID string, provider string) error {
	prov := rotation.GetProvider(provider)

	// Try provider-specific exit command first if available
	if prov != nil && prov.ExitCommand() != "" {
		_ = o.sendKeys(paneID, prov.ExitCommand(), true)
		o.sleep(1 * time.Second)
	}

	// Try graceful exit (Ctrl+C)
	if err := o.sendInterrupt(paneID); err != nil {
		return err
	}
	o.sleep(1 * time.Second)

	// Check if still active (heuristic: check process or output)
	// For now, assume we need a second Ctrl+C or explicit exit
	if err := o.sendInterrupt(paneID); err != nil {
		return err
	}
	o.sleep(1 * time.Second)

	return nil
}

var shellPromptRegexps = []*regexp.Regexp{
	regexp.MustCompile(`\$\s*$`), // bash prompt
	regexp.MustCompile(`%\s*$`),  // zsh prompt
	regexp.MustCompile(`>\s*$`),  // generic prompt
}

// WaitForShellPrompt waits until the pane shows a shell prompt
func (o *Orchestrator) WaitForShellPrompt(paneID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			output, _ := o.captureOutput(paneID, 5) // Capture last 5 lines
			for _, re := range shellPromptRegexps {
				if re.MatchString(output) {
					return nil
				}
			}
		}
	}
}

// PromptBrowserAuth simulates prompting the user
func (o *Orchestrator) PromptBrowserAuth(email string) {
	// In a real CLI/TUI, this might print to the user pane or show a dialog.
	// For now, we log the message - caller is expected to handle actual UI prompts.
	log.Printf("Auth prompt: Please log into %s in your browser", email)
}

// StartNewAgentSession launches the agent command in the pane
func (o *Orchestrator) StartNewAgentSession(ctx RestartContext) error {
	prov := rotation.GetProvider(ctx.Provider)
	if prov == nil {
		return fmt.Errorf("unknown provider: %s", ctx.Provider)
	}

	var agentCmdTemplate string
	var agentType string

	switch prov.Name() {
	case "Claude":
		agentCmdTemplate = o.cfg.Agents.Claude
		agentType = "cc"
	case "Codex":
		agentCmdTemplate = o.cfg.Agents.Codex
		agentType = "cod"
	case "Gemini":
		agentCmdTemplate = o.cfg.Agents.Gemini
		agentType = "gmi"
	default:
		return fmt.Errorf("unsupported provider: %s", prov.Name())
	}

	// Resolve model
	resolvedModel := o.cfg.Models.GetModelName(agentType, ctx.ModelAlias)

	// Generate command
	agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, config.AgentTemplateVars{
		Model:          resolvedModel,
		ModelAlias:     ctx.ModelAlias,
		ModelRequested: strings.TrimSpace(ctx.ModelAlias) != "",
		SessionName:    ctx.SessionName,
		PaneIndex:      ctx.PaneIndex,
		AgentType:      agentType,
		ProjectDir:     ctx.ProjectDir,
	})
	if err != nil {
		return fmt.Errorf("generating command: %w", err)
	}

	// Sanitize and build proper shell command with cd
	safeAgentCmd, err := o.sanitizePaneCommand(agentCmd)
	if err != nil {
		return fmt.Errorf("invalid agent command: %w", err)
	}

	cmd, err := o.buildPaneCommand(ctx.ProjectDir, safeAgentCmd)
	if err != nil {
		return fmt.Errorf("building pane command: %w", err)
	}

	// For now, just run the agent command
	return o.sendKeys(ctx.PaneID, cmd, true)
}
