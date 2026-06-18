package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// restartPaneSettleDelay is how long to wait after respawn-pane -k before
	// typing into the fresh shell (the shell needs a moment to initialize).
	restartPaneSettleDelay = 750 * time.Millisecond
	// restartPaneReadyTimeout bounds the post-relaunch ready-gate: we poll for
	// the agent TUI instead of sleeping a fixed interval (#187).
	restartPaneReadyTimeout = 15 * time.Second
	// restartPaneReadyPollInterval is the ready-gate poll cadence.
	restartPaneReadyPollInterval = 400 * time.Millisecond
)

// RestartPaneOutput is the structured output for --robot-restart-pane
type RestartPaneOutput struct {
	RobotResponse
	Session      string          `json:"session"`
	RestartedAt  time.Time       `json:"restarted_at"`
	Restarted    []string        `json:"restarted"`
	Failed       []RestartError  `json:"failed"`
	DryRun       bool            `json:"dry_run,omitempty"`
	WouldAffect  []string        `json:"would_affect,omitempty"`
	BeadAssigned string          `json:"bead_assigned,omitempty"` // Bead ID if --bead was used
	PromptSent   bool            `json:"prompt_sent,omitempty"`   // True if prompt was delivered after the agent ready-gate passed
	PromptError  string          `json:"prompt_error,omitempty"`  // Non-fatal prompt send error
	ProcessAlive map[string]bool `json:"process_alive,omitempty"` // Post-restart liveness per pane (agent panes require a live agent child, not just the shell)
	// AgentRelaunched reports, per agent pane, whether the agent CLI was
	// relaunched after respawn and became ready. respawn-pane -k only restores
	// the pane's default command (the login shell); in ntm sessions the agent
	// CLI is started by keystroke after spawn, so it must be relaunched
	// explicitly (#187). User/unknown panes are not included.
	AgentRelaunched map[string]bool `json:"agent_relaunched,omitempty"`
}

// RestartError represents a failed restart attempt
type RestartError struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// RestartPaneOptions configures the PrintRestartPane operation
type RestartPaneOptions struct {
	Session string   // Target session name
	Panes   []string // Specific pane indices to restart (empty = all agents)
	Type    string   // Filter by agent type (e.g., "claude", "cc")
	All     bool     // Include all panes (including user)
	DryRun  bool     // Preview mode
	Bead    string   // Bead ID to assign after restart (fetches info via br show --json)
	Prompt  string   // Custom prompt to send after restart (overrides --bead template)
}

type restartPromptTarget struct {
	Pane         string
	Target       string
	AgentType    tmux.AgentType
	ResolvedType string // restartPaneAgentType result: "claude", "codex", ..., "user", "unknown"
}

// GetRestartPane restarts panes (respawn-pane -k) and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRestartPane(opts RestartPaneOptions) (*RestartPaneOutput, error) {
	output := &RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		RestartedAt:   time.Now().UTC(),
		Restarted:     []string{},
		Failed:        []RestartError{},
	}

	// If --bead is provided, validate it before restarting anything
	var beadPrompt string
	if opts.Bead != "" {
		prompt, err := buildBeadPrompt(opts.Bead)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				err,
				ErrCodeInvalidFlag,
				fmt.Sprintf("Bead %s not found or not readable. Use: br show %s", opts.Bead, opts.Bead),
			)
			return output, nil
		}
		beadPrompt = prompt
	}

	// Determine which prompt to send (explicit --prompt overrides --bead template)
	promptToSend := opts.Prompt
	if promptToSend == "" && beadPrompt != "" {
		promptToSend = beadPrompt
	}

	if !tmux.SessionExists(opts.Session) {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "session",
			Reason: fmt.Sprintf("session '%s' not found", opts.Session),
		})
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use --robot-status to list available sessions",
		)
		return output, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.Failed = append(output.Failed, RestartError{
			Pane:   "panes",
			Reason: fmt.Sprintf("failed to get panes: %v", err),
		})
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	// Build pane filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	// Topology-aware keys (#172): canonical "window.pane" on multi-window sessions.
	multiWindow := paneSessionIsMultiWindow(panes)
	targetPanes := selectRestartPaneTargets(panes, paneFilterMap, opts.Type, opts.All)

	if len(targetPanes) == 0 {
		return output, nil
	}

	// Dry-run mode
	if opts.DryRun {
		output.DryRun = true
		for _, pane := range targetPanes {
			paneKey := paneTargetKey(pane, multiWindow)
			output.WouldAffect = append(output.WouldAffect, paneKey)
		}
		if opts.Bead != "" {
			output.BeadAssigned = opts.Bead
		}
		return output, nil
	}

	// Restart targets — track pane IDs for post-restart relaunch/liveness steps
	restartedPaneInfo := make(map[string]restartPromptTarget) // paneKey -> prompt target info
	for _, pane := range targetPanes {
		paneKey := paneTargetKey(pane, multiWindow)

		// Always use kill=true for restart to ensure process is cycled
		err := tmux.RespawnPane(pane.ID, true)
		if err != nil {
			output.Failed = append(output.Failed, RestartError{
				Pane:   paneKey,
				Reason: fmt.Sprintf("failed to respawn: %v", err),
			})
		} else {
			output.Restarted = append(output.Restarted, paneKey)
			restartedPaneInfo[paneKey] = restartPromptTarget{
				Pane:         paneKey,
				Target:       pane.ID,
				AgentType:    pane.Type,
				ResolvedType: restartPaneAgentType(pane),
			}
		}
	}

	// Relaunch agent CLIs in respawned agent panes (#187). respawn-pane -k
	// only restores the pane's default command — the login shell. In ntm
	// sessions the agent CLI is launched by keystroke after spawn, so without
	// an explicit relaunch the pane is left at a bare shell and any restart
	// prompt would be typed into zsh instead of an agent.
	agentPaneReady := make(map[string]bool, len(output.Restarted))
	if len(output.Restarted) > 0 {
		cfg, cfgErr := config.LoadMerged(mustGetwd(), config.DefaultPath())
		if cfgErr != nil {
			cfg = config.Default()
		}

		// Let fresh shells initialize before typing into them.
		time.Sleep(restartPaneSettleDelay)

		output.AgentRelaunched = make(map[string]bool)
		output.ProcessAlive = make(map[string]bool, len(output.Restarted))
		for _, paneKey := range output.Restarted {
			info := restartedPaneInfo[paneKey]

			if !restartTargetIsAgent(info.ResolvedType) {
				// User/unknown panes have no agent CLI to relaunch; the fresh
				// shell is the fully restored state.
				pid := paneShellPID(info.Target)
				output.ProcessAlive[paneKey] = pid > 0 && process.IsAlive(pid)
				continue
			}

			launchCmd := restartAgentLaunchCommand(cfg, info.ResolvedType)
			if err := tmux.SendKeysForAgent(info.Target, launchCmd, true, info.AgentType); err != nil {
				output.AgentRelaunched[paneKey] = false
				output.ProcessAlive[paneKey] = false
				output.Failed = append(output.Failed, RestartError{
					Pane:   paneKey,
					Reason: fmt.Sprintf("failed to relaunch agent: %v", err),
				})
				continue
			}

			// Ready-gate: poll until the agent TUI is up instead of sleeping
			// a fixed interval, so prompts are never typed into a bare shell.
			// Query the fresh pane_pid from tmux (respawn assigns a new shell
			// PID) so the process check sees the post-respawn shell.
			shellPID := paneShellPID(info.Target)
			ready := waitForPaneAgentReady(info.Target, shellPID, info.ResolvedType, restartPaneReadyTimeout)
			agentPaneReady[paneKey] = ready
			output.AgentRelaunched[paneKey] = ready
			// Agent panes are only "alive" with a live agent child under the
			// shell — mere shell liveness is exactly the false success this
			// command used to report (#187).
			output.ProcessAlive[paneKey] = shellPID > 0 && process.HasChildAlive(shellPID)
			if !ready {
				output.Failed = append(output.Failed, RestartError{
					Pane:   paneKey,
					Reason: fmt.Sprintf("agent not ready within %s after relaunch", restartPaneReadyTimeout),
				})
			}
		}
	}

	// Send prompt to successfully restarted panes. Agent panes only receive
	// the prompt once the ready-gate has passed; prompt_sent stays false when
	// any eligible delivery was skipped or failed (#187).
	if promptToSend != "" && len(output.Restarted) > 0 {
		if opts.Bead != "" {
			output.BeadAssigned = opts.Bead
		}

		promptTargets := make([]restartPromptTarget, 0, len(output.Restarted))
		var promptErrors []string
		for _, paneKey := range output.Restarted {
			info := restartedPaneInfo[paneKey]
			if restartTargetIsAgent(info.ResolvedType) && !agentPaneReady[paneKey] {
				promptErrors = append(promptErrors, fmt.Sprintf("pane %s: agent not ready, prompt not sent", paneKey))
				continue
			}
			promptTargets = append(promptTargets, info)
		}
		promptErrors = append(promptErrors, sendRestartPrompts(promptTargets, promptToSend, tmux.SendKeysForAgentDoubleEnter)...)

		if len(promptErrors) > 0 {
			output.PromptSent = false
			output.PromptError = strings.Join(promptErrors, "; ")
		} else {
			output.PromptSent = len(promptTargets) > 0
		}
	}

	// Honest overall status (#187): any per-pane failure (respawn, relaunch,
	// or readiness) degrades overall success instead of reporting success:true.
	if len(output.Failed) > 0 {
		output.Success = false
		if output.Error == "" {
			output.Error = fmt.Sprintf("%d pane(s) failed to restart cleanly", len(output.Failed))
			output.ErrorCode = ErrCodeInternalError
		}
	}

	return output, nil
}

// restartTargetIsAgent reports whether a resolved pane type identifies an
// agent CLI pane (as opposed to a user shell or an unidentifiable pane).
func restartTargetIsAgent(resolvedType string) bool {
	switch resolvedType {
	case "", "user", "unknown":
		return false
	default:
		return true
	}
}

// restartAgentLaunchCommand resolves the command used to relaunch an agent CLI
// in a respawned pane. It prefers the configured (template-rendered) agent
// command — the same command robot-spawn delivers by keystroke — and falls
// back to the canonical launch alias (cc/cod/gmi/...) when no usable command
// is configured (#187).
func restartAgentLaunchCommand(cfg *config.Config, agentType string) string {
	alias := restartLaunchAlias(agentType)

	var tmpl string
	if cfg != nil {
		switch ResolveAgentType(agentType) {
		case "claude":
			tmpl = cfg.Agents.Claude
		case "codex":
			tmpl = cfg.Agents.Codex
		case "gemini":
			tmpl = cfg.Agents.Gemini
		case "antigravity":
			tmpl = cfg.Agents.Antigravity
		case "cursor":
			tmpl = cfg.Agents.Cursor
		case "windsurf":
			tmpl = cfg.Agents.Windsurf
		case "aider":
			tmpl = cfg.Agents.Aider
		case "oc":
			// Fall back to the model/variant-aware default when [agents] oc is
			// unset so a respawn launches the real `opencode` binary rather
			// than the bare `oc` alias. See ntm#193.
			tmpl = cfg.Agents.Opencode
			if strings.TrimSpace(tmpl) == "" {
				tmpl = config.DefaultOpencodeCommand
			}
		case "ollama":
			tmpl = cfg.Agents.Ollama
		}
	}
	if strings.TrimSpace(tmpl) == "" {
		return alias
	}

	rendered, err := config.GenerateAgentCommand(tmpl, config.AgentTemplateVars{})
	if err != nil || strings.TrimSpace(rendered) == "" {
		return alias
	}
	if _, err := tmux.SanitizePaneCommand(rendered); err != nil {
		return alias
	}
	return rendered
}

// paneShellPID queries the pane's current shell PID from tmux. Returns 0 when
// unavailable. After respawn-pane the shell PID changes, so this must be
// queried fresh rather than taken from the pre-restart pane snapshot.
func paneShellPID(target string) int {
	pidStr, err := tmux.DefaultClient.Run("display-message", "-t", target, "-p", "#{pane_pid}")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidStr))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// waitForPaneAgentReady polls a pane until isAgentReady reports the agent TUI
// is up AND the pane shell has a live child process (the agent CLI). The
// process check guards against isAgentReady matching a bare shell prompt
// glyph (#187). A shellPID <= 0 skips the process check. Returns false when
// the timeout elapses first.
func waitForPaneAgentReady(target string, shellPID int, agentType string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		ready := false
		if captured, err := tmux.CapturePaneOutput(target, 50); err == nil && isAgentReady(captured, agentType) {
			ready = true
		}
		if ready && shellPID > 0 && !process.HasChildAlive(shellPID) {
			// Content looks ready but nothing is running under the shell —
			// a bare-prompt false positive. Keep polling.
			ready = false
		}
		if ready {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(restartPaneReadyPollInterval)
	}
}

func selectRestartPaneTargets(panes []tmux.Pane, paneFilterMap map[string]bool, filterType string, all bool) []tmux.Pane {
	hasPaneFilter := len(paneFilterMap) > 0
	targetType := translateAgentTypeForStatus(filterType)

	// Topology-aware --panes matching (#172): a bare index selects a whole window
	// on multi-window layouts instead of broadcasting or no-op'ing.
	multiWindow := paneSessionIsMultiWindow(panes)
	filterTokens := make([]string, 0, len(paneFilterMap))
	for k := range paneFilterMap {
		filterTokens = append(filterTokens, k)
	}

	var targetPanes []tmux.Pane
	for _, pane := range panes {
		if hasPaneFilter && !paneMatchesAnyToken(pane, filterTokens, multiWindow) {
			continue
		}

		currentType := translateAgentTypeForStatus(restartPaneAgentType(pane))
		if targetType != "" && targetType != currentType {
			continue
		}

		// By default only restart agent panes. Explicit pane filters and --all opt out.
		if !all && !hasPaneFilter && targetType == "" {
			agentType := restartPaneAgentType(pane)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	return targetPanes
}

func restartPaneAgentType(pane tmux.Pane) string {
	if resolved := ResolveAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return detectAgentType(pane.Title)
}

func sendRestartPrompts(targets []restartPromptTarget, prompt string, send func(target, keys string, agentType tmux.AgentType) error) []string {
	var promptErrors []string
	for _, target := range targets {
		if err := send(target.Target, prompt, target.AgentType); err != nil {
			promptErrors = append(promptErrors, fmt.Sprintf("pane %s: %v", target.Pane, err))
		}
	}
	return promptErrors
}

// restartPaneBeadPromptTemplate is the default prompt template for --bead assignment.
const restartPaneBeadPromptTemplate = "Read AGENTS.md, register with Agent Mail. Work on: {bead_id} - {bead_title}.\nUse br show {bead_id} for details. Mark in_progress when starting. Use ultrathink."

// buildBeadPrompt fetches bead info via br show --json and builds the assignment prompt.
func buildBeadPrompt(beadID string) (string, error) {
	cmd := exec.Command("br", "show", beadID, "--json")
	cmd.Dir, _ = os.Getwd()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("br show %s failed: %w", beadID, err)
	}

	var issues []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return "", fmt.Errorf("parse br show output: %w", err)
	}
	if len(issues) == 0 || issues[0].Title == "" {
		return "", fmt.Errorf("bead %s not found", beadID)
	}

	prompt := strings.NewReplacer(
		"{bead_id}", beadID,
		"{bead_title}", issues[0].Title,
	).Replace(restartPaneBeadPromptTemplate)

	return prompt, nil
}

// PrintRestartPane handles the --robot-restart-pane command.
// This is a thin wrapper around GetRestartPane() for CLI output.
func PrintRestartPane(opts RestartPaneOptions) error {
	output, err := GetRestartPane(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}
