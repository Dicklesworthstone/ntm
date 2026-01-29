// Package robot provides machine-readable output for AI agents.
// env.go implements the --robot-env command for environment discovery.
package robot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Robot Environment Info Command (bd-18gwh)
// =============================================================================
//
// The env command exposes environment quirks and configuration that AI agents
// need to know for correct operation. This file implements tmux environment
// detection as part of bd-35xyt.

// TmuxEnvInfo contains tmux environment detection results
type TmuxEnvInfo struct {
	BinaryPath         string `json:"binary_path"`           // Full path to tmux binary
	Version            string `json:"version"`               // tmux version string
	ShellAliasDetected bool   `json:"shell_alias_detected"`  // True if shell has tmux alias/function
	RecommendedPath    string `json:"recommended_path"`      // Always /usr/bin/tmux
	Warning            string `json:"warning,omitempty"`     // Warning message if alias detected
	OhMyZshTmuxPlugin  bool   `json:"oh_my_zsh_tmux_plugin"` // oh-my-zsh tmux plugin detected
	TmuxinatorDetected bool   `json:"tmuxinator_detected"`   // tmuxinator detected
	TmuxResurrect      bool   `json:"tmux_resurrect"`        // tmux-resurrect detected
}

// EnvOutput is the response for --robot-env
type EnvOutput struct {
	RobotResponse
	Session          string                `json:"session,omitempty"`
	Tmux             TmuxEnvInfo           `json:"tmux"`
	SessionStructure *SessionStructureInfo `json:"session_structure,omitempty"`
	Shell            *ShellEnvInfo         `json:"shell,omitempty"`
	Timing           *TimingInfo           `json:"timing,omitempty"`
	Targeting        *TargetingInfo        `json:"targeting,omitempty"`
}

// SessionStructureInfo describes session window/pane structure
type SessionStructureInfo struct {
	WindowIndex     int `json:"window_index"`      // The window where agents live
	ControlPane     int `json:"control_pane"`      // Pane 1 is control shell
	AgentPaneStart  int `json:"agent_pane_start"`  // Agents start at this pane index
	AgentPaneEnd    int `json:"agent_pane_end"`    // Last agent pane index
	TotalAgentPanes int `json:"total_agent_panes"` // Count of agent panes
}

// ShellEnvInfo describes shell environment
type ShellEnvInfo struct {
	Type               string `json:"type"`                  // bash, zsh, fish, etc.
	TmuxPluginDetected bool   `json:"tmux_plugin_detected"`  // May cause issues
	OhMyZshDetected    bool   `json:"oh_my_zsh_detected"`    // oh-my-zsh installed
	ConfigPath         string `json:"config_path,omitempty"` // Where to look for aliases (~/.zshrc, ~/.bashrc)
}

// TimingInfo contains recommended timing constants
type TimingInfo struct {
	CtrlCGapMs          int `json:"ctrl_c_gap_ms"`          // Recommended gap between Ctrl-Cs
	PostExitWaitMs      int `json:"post_exit_wait_ms"`      // Wait after exit before launching
	CCInitWaitMs        int `json:"cc_init_wait_ms"`        // Wait for cc to initialize
	PromptSubmitDelayMs int `json:"prompt_submit_delay_ms"` // Delay before submitting prompts
}

// TargetingInfo provides pane targeting examples
type TargetingInfo struct {
	PaneFormat         string `json:"pane_format"`          // e.g., "session:window.pane"
	ExampleAgentPane   string `json:"example_agent_pane"`   // e.g., "myproject:1.2"
	ExampleControlPane string `json:"example_control_pane"` // e.g., "myproject:1.1"
}

// DetectTmuxEnv detects tmux environment information
func DetectTmuxEnv() TmuxEnvInfo {
	info := TmuxEnvInfo{
		RecommendedPath: "/usr/bin/tmux",
	}

	// Find binary path
	info.BinaryPath = findTmuxBinaryPath()

	// Get version
	info.Version = getTmuxVersion(info.BinaryPath)

	// Detect shell alias
	info.ShellAliasDetected = detectTmuxAlias()

	// Detect plugins
	info.OhMyZshTmuxPlugin = detectOhMyZshTmuxPlugin()
	info.TmuxinatorDetected = detectTmuxinator()
	info.TmuxResurrect = detectTmuxResurrect()

	// Set warning if alias detected
	if info.ShellAliasDetected {
		info.Warning = "Use binary_path to avoid shell plugin interference"
	}

	return info
}

// findTmuxBinaryPath finds the actual tmux binary path
func findTmuxBinaryPath() string {
	// Try standard paths first
	standardPaths := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
	}

	for _, path := range standardPaths {
		if fileExists(path) {
			return path
		}
	}

	// Fall back to which command
	out, err := exec.Command("which", "tmux").Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if path != "" && fileExists(path) {
			return path
		}
	}

	// Default fallback
	return "/usr/bin/tmux"
}

// getTmuxVersion returns the tmux version string
func getTmuxVersion(binaryPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, binaryPath, "-V").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectTmuxAlias checks if tmux is aliased or wrapped in the shell
func detectTmuxAlias() bool {
	// Get current shell
	shell := os.Getenv("SHELL")
	if shell == "" {
		return false
	}

	home := os.Getenv("HOME")
	if home == "" {
		return false
	}

	// Avoid invoking an interactive shell here; user shell init files can hang.
	// Instead, do a best-effort scan of common RC files for an alias/function.
	var rcFiles []string
	switch {
	case strings.Contains(shell, "zsh"):
		rcFiles = []string{
			filepath.Join(home, ".zshrc"),
			filepath.Join(home, ".zshrc.local"),
		}
	case strings.Contains(shell, "bash"):
		rcFiles = []string{
			filepath.Join(home, ".bashrc"),
			filepath.Join(home, ".bash_profile"),
		}
	default:
		return false
	}

	aliasRe := regexp.MustCompile(`(?m)^\s*alias\s+tmux=`)
	funcRe := regexp.MustCompile(`(?m)^\s*(?:function\s+)?tmux\s*\(\)\s*\{`)

	for _, rc := range rcFiles {
		content, err := os.ReadFile(rc)
		if err != nil {
			continue
		}
		if aliasRe.Match(content) || funcRe.Match(content) {
			return true
		}
	}

	return false
}

// detectOhMyZshTmuxPlugin checks for oh-my-zsh tmux plugin
func detectOhMyZshTmuxPlugin() bool {
	home := os.Getenv("HOME")
	if home == "" {
		return false
	}

	// Check if oh-my-zsh is installed
	omzDir := filepath.Join(home, ".oh-my-zsh")
	if !dirExists(omzDir) {
		return false
	}

	// Check .zshrc for tmux plugin
	zshrc := filepath.Join(home, ".zshrc")
	content, err := os.ReadFile(zshrc)
	if err != nil {
		return false
	}

	// Look for plugins=(... tmux ...)
	pluginRegex := regexp.MustCompile(`plugins\s*=\s*\([^)]*\btmux\b[^)]*\)`)
	return pluginRegex.Match(content)
}

// detectTmuxinator checks if tmuxinator is installed
func detectTmuxinator() bool {
	_, err := exec.LookPath("tmuxinator")
	return err == nil
}

// detectTmuxResurrect checks if tmux-resurrect is installed
func detectTmuxResurrect() bool {
	home := os.Getenv("HOME")
	if home == "" {
		return false
	}

	// Check common tmux-resurrect paths
	resurrectPaths := []string{
		filepath.Join(home, ".tmux", "plugins", "tmux-resurrect"),
		filepath.Join(home, ".tmux", "resurrect"),
	}

	for _, path := range resurrectPaths {
		if dirExists(path) {
			return true
		}
	}

	// Check tmux.conf for resurrect plugin
	tmuxConf := filepath.Join(home, ".tmux.conf")
	content, err := os.ReadFile(tmuxConf)
	if err != nil {
		return false
	}

	return strings.Contains(string(content), "tmux-resurrect")
}

// GetEnv returns environment info for a session (or global if no session).
// This function returns the data struct directly, enabling CLI/REST parity.
func GetEnv(session string) (*EnvOutput, error) {
	output := &EnvOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		Tmux:          DetectTmuxEnv(),
	}

	// Add timing constants (recommended defaults)
	output.Timing = &TimingInfo{
		CtrlCGapMs:          100,  // 0.1s gap between Ctrl-Cs
		PostExitWaitMs:      3000, // 3s wait after exit
		CCInitWaitMs:        6000, // 6s for cc to initialize
		PromptSubmitDelayMs: 1000, // 1s before submitting prompts
	}

	// If session specified, add session-specific info
	if session != "" {
		structure, err := detectSessionStructure(session)
		if err == nil {
			output.SessionStructure = structure
		}

		output.Targeting = &TargetingInfo{
			PaneFormat:         "session:window.pane",
			ExampleAgentPane:   fmt.Sprintf("%s:1.2", session),
			ExampleControlPane: fmt.Sprintf("%s:1.1", session),
		}
	}

	// Detect shell environment
	output.Shell = detectShellEnv()

	return output, nil
}

// PrintEnv outputs environment info for a session (or global if no session).
// This is a thin wrapper around GetEnv() for CLI output.
func PrintEnv(session string) error {
	output, err := GetEnv(session)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// detectSessionStructure detects session window/pane structure
func detectSessionStructure(session string) (*SessionStructureInfo, error) {
	tmuxPath := tmux.BinaryPath()

	// Determine primary window (prefer window 1 if present)
	windowOut, err := exec.Command(tmuxPath, "list-windows", "-t", session, "-F", "#{window_index}").Output()
	if err != nil {
		return nil, err
	}
	windowLines := strings.Split(strings.TrimSpace(string(windowOut)), "\n")
	if len(windowLines) == 0 || (len(windowLines) == 1 && strings.TrimSpace(windowLines[0]) == "") {
		return nil, fmt.Errorf("no windows found")
	}
	windowIDs := make([]int, 0, len(windowLines))
	for _, line := range windowLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		windowIDs = append(windowIDs, idx)
	}
	if len(windowIDs) == 0 {
		return nil, fmt.Errorf("no windows found")
	}
	primaryWindow := (&SessionStructure{WindowIDs: windowIDs}).findPrimaryWindow()

	target := fmt.Sprintf("%s:%d", session, primaryWindow)

	// Get pane indices from primary window
	out, err := exec.Command(tmuxPath, "list-panes", "-t", target, "-F", "#{pane_index}").Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && strings.TrimSpace(lines[0]) == "") {
		return nil, fmt.Errorf("no panes found")
	}

	// Parse pane indices
	minPane := -1
	maxPane := -1
	paneIndices := make([]int, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		paneIndices = append(paneIndices, idx)
		if minPane == -1 || idx < minPane {
			minPane = idx
		}
		if idx > maxPane {
			maxPane = idx
		}
	}
	if len(paneIndices) == 0 || minPane == -1 {
		return nil, fmt.Errorf("no panes found")
	}

	controlPane := minPane
	totalAgentPanes := 0
	for _, idx := range paneIndices {
		if idx != controlPane {
			totalAgentPanes++
		}
	}

	agentPaneStart := 0
	agentPaneEnd := 0
	if totalAgentPanes > 0 {
		agentPaneStart = controlPane + 1
		agentPaneEnd = maxPane
	}

	return &SessionStructureInfo{
		WindowIndex:     primaryWindow,
		ControlPane:     controlPane,
		AgentPaneStart:  agentPaneStart,
		AgentPaneEnd:    agentPaneEnd,
		TotalAgentPanes: totalAgentPanes,
	}, nil
}

// detectShellEnv detects shell environment info
func detectShellEnv() *ShellEnvInfo {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return nil
	}

	shellType := filepath.Base(shell)
	info := &ShellEnvInfo{
		Type: shellType,
	}

	home := os.Getenv("HOME")
	if home != "" {
		info.OhMyZshDetected = dirExists(filepath.Join(home, ".oh-my-zsh"))

		// Set config path based on shell type
		switch shellType {
		case "zsh":
			info.ConfigPath = filepath.Join(home, ".zshrc")
		case "bash":
			// Prefer .bashrc, fall back to .bash_profile
			bashrc := filepath.Join(home, ".bashrc")
			if fileExists(bashrc) {
				info.ConfigPath = bashrc
			} else {
				info.ConfigPath = filepath.Join(home, ".bash_profile")
			}
		case "fish":
			info.ConfigPath = filepath.Join(home, ".config", "fish", "config.fish")
		default:
			// For unknown shells, try common rc pattern
			info.ConfigPath = filepath.Join(home, "."+shellType+"rc")
		}
	}

	info.TmuxPluginDetected = detectTmuxAlias() || detectOhMyZshTmuxPlugin()

	return info
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// dirExists checks if a directory exists
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
