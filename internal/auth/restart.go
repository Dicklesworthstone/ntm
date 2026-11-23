package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/rotation"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Orchestrator manages the restart process
type Orchestrator struct {
	cfg                 *config.Config
	authFlows           map[string]AuthFlow
	captureOutput       func(string, int) (string, error)
	sendKeys            func(string, string, bool) error
	sendKeysForAgent    func(string, string, bool, tmux.AgentType) error
	sendInterrupt       func(string) error
	buildPaneCommand    func(string, string) (string, error)
	sanitizePaneCommand func(string) (string, error)
	setLaunchSpec       func(context.Context, string, tmux.AgentLaunchSpec) error
	promptBrowserAuth   func(string) error
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
		sendKeysForAgent:    tmux.SendKeysForAgent,
		sendInterrupt:       tmux.SendInterrupt,
		buildPaneCommand:    tmux.BuildPaneCommand,
		sanitizePaneCommand: tmux.SanitizePaneCommand,
		setLaunchSpec:       tmux.SetPaneLaunchSpecContext,
		promptBrowserAuth: func(email string) error {
			return promptBrowserAuth(os.Stdin, os.Stdout, email)
		},
		sleep: time.Sleep,
	}
}

// RestartContext holds context for restarting an agent
type RestartContext struct {
	PaneID   string
	Provider string
	// AgentType is the canonical agent-type short form of the pane being
	// restarted (cc/cod/gmi/agy/...). It is preferred over Provider when
	// choosing the relaunch command, because several agent types can share a
	// single auth provider — notably Antigravity (agy) and the legacy Gemini
	// CLI both authenticate through Google. Optional; falls back to Provider.
	AgentType   string
	TargetEmail string
	ModelAlias  string
	SessionName string
	PaneIndex   int
	ProjectDir  string
}

// ExecuteRestartStrategy performs the terminate-switch-restart flow under the
// unified rotation.LifecycleCoordinator pane lease so auth restart cannot race
// with context rotation or CAAM failover on the same pane.
func (o *Orchestrator) ExecuteRestartStrategy(ctx RestartContext) error {
	if err := validateAutomatedRestartContext(ctx); err != nil {
		return err
	}

	fsm := rotation.DefaultLifecycleCoordinator()
	lease, err := fsm.Acquire(ctx.PaneID, "auth-restart", "", "auth_restart")
	if err != nil {
		return fmt.Errorf("acquire auth restart lifecycle lease: %w", err)
	}
	leaseClosed := false
	fsmCheckpoint := "draining"
	defer func() {
		if !leaseClosed {
			_ = fsm.FailRecoverable(lease, fsmCheckpoint, "unexpected abort or panic during auth restart", "verify pane process state and retry auth restart")
		}
	}()

	// 1. Terminate existing session gracefully (draining)
	fsmCheckpoint = "terminate_session"
	if err := o.TerminateSession(ctx.PaneID, ctx.Provider); err != nil {
		_ = fsm.FailRecoverable(lease, "terminate_session", err.Error(), "verify pane process state and retry auth restart")
		leaseClosed = true
		return fmt.Errorf("terminating session: %w", err)
	}

	// 2. Wait for shell prompt (draining)
	fsmCheckpoint = "wait_for_shell"
	if err := o.WaitForShellPrompt(ctx.PaneID, 10*time.Second); err != nil {
		_ = fsm.FailRecoverable(lease, "wait_for_shell", err.Error(), "verify shell prompt in pane and retry auth restart")
		leaseClosed = true
		return fmt.Errorf("session did not terminate: %w", err)
	}

	// 3. Transition to handing_off before browser auth + relaunch
	fsmCheckpoint = "browser_auth"
	if err := fsm.Transition(lease, rotation.StateHandingOff, "browser_auth_and_relaunch"); err != nil {
		_ = fsm.FailRecoverable(lease, "handing_off", err.Error(), "verify pane lifecycle state and retry auth restart")
		leaseClosed = true
		return err
	}
	if err := o.PromptBrowserAuth(ctx.TargetEmail); err != nil {
		_ = fsm.FailRecoverable(lease, "browser_auth", err.Error(), "complete browser account switch and retry auth restart")
		leaseClosed = true
		return fmt.Errorf("browser auth prompt: %w", err)
	}

	// 4. Start new agent session and mark verified/active
	fsmCheckpoint = "start_new_session"
	if err := o.StartNewAgentSession(ctx); err != nil {
		_ = fsm.FailRecoverable(lease, "start_new_session", err.Error(), "relaunch agent command in pane")
		leaseClosed = true
		return err
	}
	fsmCheckpoint = "verifying"
	if err := fsm.Transition(lease, rotation.StateVerifying, "relaunch_submitted"); err != nil {
		_ = fsm.FailRecoverable(lease, "verifying", err.Error(), "verify pane lifecycle state")
		leaseClosed = true
		return err
	}
	completeErr := fsm.Complete(lease, "auth_restart_completed")
	if completeErr == nil {
		leaseClosed = true
	}
	return completeErr
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

	// Send a second Ctrl+C unconditionally: agent CLIs commonly need two
	// interrupts to exit, and the extra interrupt is harmless at a shell
	// prompt (cheaper and more reliable than probing the pane process).
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
			output, err := o.captureOutput(paneID, 5) // Capture last 5 lines
			if err != nil {
				return fmt.Errorf("capture pane output: %w", err)
			}
			for _, re := range shellPromptRegexps {
				if re.MatchString(output) {
					return nil
				}
			}
		}
	}
}

// PromptBrowserAuth asks the user to switch browser accounts before restart.
func (o *Orchestrator) PromptBrowserAuth(email string) error {
	if o.promptBrowserAuth != nil {
		return o.promptBrowserAuth(email)
	}
	return promptBrowserAuth(os.Stdin, os.Stdout, email)
}

func promptBrowserAuth(input io.Reader, output io.Writer, email string) error {
	target := strings.TrimSpace(email)
	if target == "" {
		target = "the target account"
	}

	fmt.Fprintln(output, "Browser authentication required")
	fmt.Fprintf(output, "  Switch your browser to: %s\n", target)
	fmt.Fprintln(output, "  Press ENTER to continue after the browser account is ready.")

	if _, err := bufio.NewReader(input).ReadString('\n'); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("browser account confirmation not received")
		}
		return err
	}
	return nil
}

// StartNewAgentSession launches the agent command in the pane
func (o *Orchestrator) StartNewAgentSession(ctx RestartContext) error {
	if err := validateAutomatedRestartContext(ctx); err != nil {
		return err
	}

	prov := rotation.GetProvider(ctx.Provider)
	if prov == nil {
		return fmt.Errorf("unknown provider: %s", ctx.Provider)
	}

	var agentCmdTemplate string
	var agentType string

	// Prefer the explicit pane agent type for the relaunch command. Multiple
	// agent types can share one auth provider (agy and gemini both map to the
	// Google/Gemini provider), so selecting by prov.Name() alone would relaunch
	// an Antigravity pane with the Gemini binary and model.
	switch tmux.AgentType(ctx.AgentType).Canonical() {
	case tmux.AgentClaude:
		agentCmdTemplate, agentType = o.cfg.Agents.Claude, "cc"
	case tmux.AgentCodex:
		agentCmdTemplate, agentType = o.cfg.Agents.Codex, "cod"
	case tmux.AgentGemini:
		agentCmdTemplate, agentType = o.cfg.Agents.Gemini, "gmi"
	case tmux.AgentAntigravity:
		agentCmdTemplate, agentType = o.cfg.Agents.Antigravity, "agy"
	case tmux.AgentGrok:
		agentCmdTemplate, agentType = o.cfg.Agents.Grok, "grok"
	case tmux.AgentOllama:
		agentCmdTemplate, agentType = o.cfg.Agents.Ollama, "ollama"
	case tmux.AgentCursor:
		agentCmdTemplate, agentType = o.cfg.Agents.Cursor, "cursor"
	case tmux.AgentWindsurf:
		agentCmdTemplate, agentType = o.cfg.Agents.Windsurf, "windsurf"
	case tmux.AgentAider:
		agentCmdTemplate, agentType = o.cfg.Agents.Aider, "aider"
	case tmux.AgentOpencode:
		tmpl := o.cfg.Agents.Opencode
		if strings.TrimSpace(tmpl) == "" {
			tmpl = config.DefaultOpencodeCommand
		}
		agentCmdTemplate, agentType = tmpl, "oc"
	case tmux.AgentOmp:
		agentCmdTemplate, agentType = config.OmpCommandOrDefault(o.cfg.Agents.Omp), "omp"
	case tmux.AgentPi:
		tmpl := plugins.ShippedPiPlugin().Command
		if loadedPlugins, err := plugins.LoadAgentPluginsWithDefaults(filepath.Join(filepath.Dir(config.DefaultPath()), "agents")); err == nil {
			for _, p := range loadedPlugins {
				if (strings.EqualFold(strings.TrimSpace(p.Name), "pi") || strings.EqualFold(strings.TrimSpace(p.Alias), "pi")) && strings.TrimSpace(p.Command) != "" {
					tmpl = p.Command
					break
				}
			}
		}
		agentCmdTemplate, agentType = tmpl, "pi"
	default:
		// No usable agent type recorded — fall back to the auth provider name.
		switch prov.Name() {
		case "Claude":
			agentCmdTemplate, agentType = o.cfg.Agents.Claude, "cc"
		case "Codex":
			agentCmdTemplate, agentType = o.cfg.Agents.Codex, "cod"
		case "Gemini":
			agentCmdTemplate, agentType = o.cfg.Agents.Gemini, "gmi"
		default:
			return fmt.Errorf("unsupported provider: %s", prov.Name())
		}
	}

	// Resolve model
	resolvedModel := o.cfg.Models.GetModelName(agentType, ctx.ModelAlias)

	// Generate command
	vars := config.AgentTemplateVars{
		Model:          resolvedModel,
		ModelAlias:     ctx.ModelAlias,
		ModelRequested: len(strings.TrimSpace(ctx.ModelAlias)) > 0,
		SessionName:    ctx.SessionName,
		PaneIndex:      ctx.PaneIndex,
		AgentType:      agentType,
		ProjectDir:     ctx.ProjectDir,
	}
	if agentType == "cc" {
		// The relaunched Claude agent keeps ntm's PreToolUse hooks.
		vars.ClaudeSettings = policy.ClaudeAgentLaunchSettings(o.cfg).Settings
	}
	agentCmd, err := config.GenerateAgentCommand(agentCmdTemplate, vars)
	if err != nil {
		return fmt.Errorf("generating command: %w", err)
	}
	policy.LogClaudeHooksNotApplied("auth-restart", agentCmd, vars.ClaudeSettings)
	// An auth restart deliberately changes the account in the current launch
	// environment. Replace any earlier pane record with this actual command;
	// retaining its old CAAM profile would undo that account switch on recovery.
	launchSpec := tmux.AgentLaunchSpec{
		Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType(agentType),
		Command: agentCmd, Model: resolvedModel, ModelAlias: ctx.ModelAlias,
	}
	if agentType == "cc" && o.cfg.Agents.ClaudeIsolateCredentials {
		launchSpec.ClaudeIsolateCredentials = true
		launchSpec.ClaudeTokenFile, err = swarm.ResolveClaudeSetupTokenFile(o.cfg.Agents.ClaudeTokenFile)
		if err != nil {
			return fmt.Errorf("capture auth restart token file reference: %w", err)
		}
	}

	// Per-pane Claude credential isolation (GH#237). This path is the one that
	// most needs it: it fires precisely when a pane has hit an auth or limit
	// error, which is the symptom of the shared-credential cascade. Relaunching
	// without the isolated CLAUDE_CONFIG_DIR put the pane straight back onto
	// the rotating credential, where its next refresh invalidates every other
	// pane's token — so isolation used to evaporate at the exact moment it was
	// doing its job.
	if agentType == "cc" {
		claudeEnv, isoErr := swarm.ProvisionClaudeIsolation(o.cfg, ctx.ProjectDir, ctx.SessionName, ctx.PaneIndex)
		if isoErr != nil {
			return fmt.Errorf("isolating credentials for claude pane %d: %w", ctx.PaneIndex, isoErr)
		}
		agentCmd = claudeEnv.ApplyToCommand(agentCmd)
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
	metadataCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.setLaunchSpec(metadataCtx, ctx.PaneID, launchSpec); err != nil {
		return fmt.Errorf("record auth restart launch settings: %w", err)
	}

	// Launch agent command using the specialized robust sender
	agentTypeEnum := tmux.AgentType(agentType)
	return o.sendKeysForAgent(ctx.PaneID, cmd, true, agentTypeEnum)
}

func validateAutomatedRestartContext(ctx RestartContext) error {
	rawAgentType := strings.TrimSpace(ctx.AgentType)
	if rawAgentType == "" {
		rawAgentType = ctx.Provider
	}
	agentType := agent.AgentType(rawAgentType)
	if err := agentType.ValidateAutomatedRelaunch(); err != nil {
		return fmt.Errorf("agent type %s does not support automated auth restart: %w", agentType.Canonical(), err)
	}
	return nil
}
