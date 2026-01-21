// Package robot provides machine-readable output for AI agents.
// exit_sequences.go implements agent-specific exit methods for smart restart.
package robot

import (
	"os/exec"
	"strconv"
	"time"
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

// exitAgent exits the current agent using the appropriate method.
func exitAgent(session string, pane int, agentType string, seq *RestartSequence) error {
	switch agentType {
	case "cc":
		return exitClaudeCode(session, pane, seq)
	case "cod":
		return exitCodex(session, pane, seq)
	case "gmi":
		return exitGemini(session, pane, seq)
	default:
		return exitUnknown(session, pane, seq)
	}
}

// exitClaudeCode exits Claude Code with double Ctrl+C.
// CRITICAL: The 0.1s timing between Ctrl+Cs is essential!
func exitClaudeCode(session string, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "double_ctrl_c"

	// First Ctrl+C
	if err := sendCtrlC(session, pane); err != nil {
		return wrapError("first ctrl-c failed", err)
	}

	// CRITICAL: 100ms pause between Ctrl+Cs
	time.Sleep(100 * time.Millisecond)

	// Second Ctrl+C
	if err := sendCtrlC(session, pane); err != nil {
		return wrapError("second ctrl-c failed", err)
	}

	return nil
}

// exitCodex exits Codex CLI with /exit command.
func exitCodex(session string, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "exit_command"

	if err := sendKeys(session, pane, "/exit\n"); err != nil {
		return wrapError("exit command failed", err)
	}

	return nil
}

// exitGemini exits Gemini CLI with Escape (to exit shell mode) then /exit.
func exitGemini(session string, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "escape_then_exit"

	// Send Escape to exit shell mode if active
	if err := sendEscape(session, pane); err != nil {
		return wrapError("escape failed", err)
	}

	// Brief pause
	time.Sleep(100 * time.Millisecond)

	// Send /exit command
	if err := sendKeys(session, pane, "/exit\n"); err != nil {
		return wrapError("exit failed", err)
	}

	return nil
}

// exitUnknown tries Ctrl+C as a fallback for unknown agent types.
func exitUnknown(session string, pane int, seq *RestartSequence) error {
	seq.ExitMethod = "ctrl_c_fallback"

	if err := sendCtrlC(session, pane); err != nil {
		return wrapError("ctrl-c failed", err)
	}

	return nil
}

// sendCtrlC sends Ctrl+C to a tmux pane.
func sendCtrlC(session string, pane int) error {
	return runTmuxCommand("send-keys", "-t", formatTarget(session, pane), "C-c")
}

// sendEscape sends Escape key to a tmux pane.
func sendEscape(session string, pane int) error {
	return runTmuxCommand("send-keys", "-t", formatTarget(session, pane), "Escape")
}

// sendKeys sends literal keys to a tmux pane.
func sendKeys(session string, pane int, keys string) error {
	return runTmuxCommand("send-keys", "-t", formatTarget(session, pane), "-l", keys)
}

// formatTarget creates a tmux target string for a session and pane.
func formatTarget(session string, pane int) string {
	return session + ":" + strconv.Itoa(pane)
}

// runTmuxCommand executes a tmux command.
func runTmuxCommand(args ...string) error {
	cmd := exec.Command("tmux", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return wrapError(string(output), err)
		}
		return err
	}
	return nil
}
