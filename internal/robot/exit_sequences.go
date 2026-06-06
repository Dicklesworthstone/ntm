// Package robot provides machine-readable output for AI agents.
// exit_sequences.go implements agent-specific exit methods for smart restart.
package robot

import (
	"os/exec"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Agent Exit Sequences (bd-2c7f4)
// =============================================================================
//
// Each AI coding agent has different exit methods:
// - Claude Code (cc): Double Ctrl+C with CRITICAL 0.1s timing
// - Codex (cod): /exit command
// - Gemini (gmi): Escape (exit shell mode if active) then /exit
// - Unknown: Try Ctrl+C as fallback

// exitAgent exits the current agent using the appropriate method. win is the
// pane's tmux window index (#172) so the exit keys target the correct window on
// multi-window / window-per-agent layouts rather than always window 1.
func exitAgent(session string, win, pane int, agentType string, seq *RestartSequence) error {
	switch restartCanonicalAgentType(agentType) {
	case "cc":
		return exitClaudeCode(session, win, pane, seq)
	case "cod":
		return exitCodex(session, win, pane, seq)
	case "gmi":
		return exitGemini(session, win, pane, seq)
	default:
		return exitUnknown(session, win, pane, seq)
	}
}

// exitClaudeCode exits Claude Code with double Ctrl+C.
// CRITICAL: The 0.1s timing between Ctrl+Cs is essential!
func exitClaudeCode(session string, win, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "double_ctrl_c"

	// First Ctrl+C
	if err := sendCtrlC(session, win, pane); err != nil {
		return wrapError("first ctrl-c failed", err)
	}

	// CRITICAL: 100ms pause between Ctrl+Cs
	time.Sleep(100 * time.Millisecond)

	// Second Ctrl+C
	if err := sendCtrlC(session, win, pane); err != nil {
		return wrapError("second ctrl-c failed", err)
	}

	return nil
}

// exitCodex exits Codex CLI with /exit command.
func exitCodex(session string, win, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "exit_command"

	if err := sendKeys(session, win, pane, "/exit\n"); err != nil {
		return wrapError("exit command failed", err)
	}

	return nil
}

// exitGemini exits Gemini CLI with Escape (to exit shell mode) then /exit.
func exitGemini(session string, win, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "escape_then_exit"

	// Send Escape to exit shell mode if active
	if err := sendEscape(session, win, pane); err != nil {
		return wrapError("escape failed", err)
	}

	// Brief pause
	time.Sleep(100 * time.Millisecond)

	// Send /exit command
	if err := sendKeys(session, win, pane, "/exit\n"); err != nil {
		return wrapError("exit failed", err)
	}

	return nil
}

// exitUnknown tries Ctrl+C as a fallback for unknown agent types.
func exitUnknown(session string, win, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "ctrl_c_fallback"

	if err := sendCtrlC(session, win, pane); err != nil {
		return wrapError("ctrl-c failed", err)
	}

	return nil
}

// sendCtrlC sends Ctrl+C to a tmux pane.
func sendCtrlC(session string, win, pane int) error {
	return runTmuxCommand("send-keys", "-t", formatTargetWin(session, win, pane), "C-c")
}

// sendEscape sends Escape key to a tmux pane.
func sendEscape(session string, win, pane int) error {
	return runTmuxCommand("send-keys", "-t", formatTargetWin(session, win, pane), "Escape")
}

// sendKeys sends literal keys to a tmux pane.
func sendKeys(session string, win, pane int, keys string) error {
	return runTmuxCommand("send-keys", "-t", formatTargetWin(session, win, pane), "-l", keys)
}

// formatTarget creates a tmux target string for a session and pane, assuming
// window 1 (the historical single-window NTM layout). Retained for callers and
// tests that do not carry a window index.
func formatTarget(session string, pane int) string {
	return formatTargetWin(session, 1, pane)
}

// formatTargetWin creates a tmux target string for a session, window, and pane
// (#172). A win < 1 is treated as window 1 to preserve the legacy behavior.
func formatTargetWin(session string, win, pane int) string {
	if win < 1 {
		win = 1
	}
	return session + ":" + strconv.Itoa(win) + "." + strconv.Itoa(pane)
}

// runTmuxCommand executes a tmux command.
func runTmuxCommand(args ...string) error {
	cmd := exec.Command(tmux.BinaryPath(), args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return wrapError(string(output), err)
		}
		return err
	}
	return nil
}

// =============================================================================
// Hard Kill Fallback (bd-bh74z)
// =============================================================================
// When soft exit fails, we need to forcefully kill the agent process.
// This uses kill -9 to terminate the child process of the pane's shell.

// HardKillResult contains information about the hard kill operation.
type HardKillResult struct {
	ShellPID   int    `json:"shell_pid,omitempty"`
	ChildPID   int    `json:"child_pid,omitempty"`
	KillMethod string `json:"kill_method"`
	Success    bool   `json:"success"`
}

// hardKillAgent performs a forceful kill -9 on the agent process.
// It should be called when soft exit methods fail.
func hardKillAgent(session string, win, pane int, seq *RestartSequence) (*HardKillResult, error) {
	result := &HardKillResult{
		KillMethod: "kill_9",
	}

	// Step 1: Get shell PID from tmux
	shellPID, err := getShellPID(session, win, pane)
	if err != nil {
		return result, wrapError("failed to get shell PID", err)
	}
	result.ShellPID = shellPID

	// Step 2: Get child PID via pgrep
	childPID := process.GetChildPID(shellPID)
	if childPID <= 0 {
		// No child process might mean agent already exited
		result.KillMethod = "no_child_process"
		result.Success = true
		return result, nil
	}
	result.ChildPID = childPID

	// Step 3: kill -9 the child process
	if err := killProcess(childPID); err != nil {
		return result, wrapError("kill -9 failed", err)
	}

	// Update sequence info
	seq.ExitMethod = "hard_kill"
	result.Success = true
	return result, nil
}

// getShellPID retrieves the PID of the shell process in a tmux pane.
// Uses: tmux list-panes -t session:window -F '#{pane_index} #{pane_pid}'
// win is the pane's tmux window index (#172); win < 1 falls back to window 1.
func getShellPID(session string, win, pane int) (int, error) {
	if win < 1 {
		win = 1
	}
	target := session + ":" + strconv.Itoa(win)
	cmd := exec.Command(tmux.BinaryPath(), "list-panes", "-t", target, "-F", "#{pane_index} #{pane_pid}")
	output, err := cmd.Output()
	if err != nil {
		return 0, wrapError("tmux list-panes failed", err)
	}

	// Parse output to find our pane
	lines := splitLines(string(output))
	for _, line := range lines {
		parts := splitBySpace(line)
		if len(parts) >= 2 {
			paneIdx, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			if paneIdx == pane {
				pid, err := strconv.Atoi(parts[1])
				if err != nil {
					return 0, wrapError("invalid PID format", err)
				}
				return pid, nil
			}
		}
	}

	return 0, newError("pane not found")
}

// Note: getChildPID is now in the shared process package (internal/process)

// killProcess sends SIGKILL (kill -9) to a process.
func killProcess(pid int) error {
	cmd := exec.Command("kill", "-9", strconv.Itoa(pid))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return wrapError(trimSpace(string(output)), err)
		}
		return err
	}
	return nil
}

// splitBySpace splits a string by whitespace, handling multiple spaces.
func splitBySpace(s string) []string {
	var result []string
	var current string
	for _, c := range s {
		if c == ' ' || c == '\t' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}
