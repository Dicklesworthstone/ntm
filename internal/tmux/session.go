package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// AgentType represents the type of AI agent
type AgentType string

const (
	AgentClaude AgentType = "cc"
	AgentCodex  AgentType = "cod"
	AgentGemini AgentType = "gmi"
	AgentUser   AgentType = "user"
)

// Pane represents a tmux pane
type Pane struct {
	ID       string
	Index    int
	Title    string
	Type     AgentType
	Command  string
	Width    int
	Height   int
	Active   bool
}

// Session represents a tmux session
type Session struct {
	Name       string
	Directory  string
	Windows    int
	Panes      []Pane
	Attached   bool
	Created    string
}

// IsInstalled checks if tmux is available
func IsInstalled() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// EnsureInstalled returns an error if tmux is not installed
func EnsureInstalled() error {
	if !IsInstalled() {
		return errors.New("tmux is not installed. Install it with: brew install tmux (macOS) or apt install tmux (Linux)")
	}
	return nil
}

// InTmux returns true if currently inside a tmux session
func InTmux() bool {
	return os.Getenv("TMUX") != ""
}

// run executes a tmux command and returns stdout
func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// runSilent executes a tmux command ignoring output
func runSilent(args ...string) error {
	cmd := exec.Command("tmux", args...)
	return cmd.Run()
}

// SessionExists checks if a session exists
func SessionExists(name string) bool {
	err := runSilent("has-session", "-t", name)
	return err == nil
}

// ListSessions returns all tmux sessions
func ListSessions() ([]Session, error) {
	output, err := run("list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_attached}:#{session_created_string}")
	if err != nil {
		// No sessions is not an error
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}

	if output == "" {
		return nil, nil
	}

	var sessions []Session
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 4 {
			continue
		}

		windows, _ := strconv.Atoi(parts[1])
		attached := parts[2] == "1"

		sessions = append(sessions, Session{
			Name:     parts[0],
			Windows:  windows,
			Attached: attached,
			Created:  parts[3],
		})
	}

	return sessions, nil
}

// GetSession returns detailed info about a session
func GetSession(name string) (*Session, error) {
	if !SessionExists(name) {
		return nil, fmt.Errorf("session '%s' not found", name)
	}

	// Get session info
	output, err := run("list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_attached}", "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(output, ":", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected session format")
	}

	windows, _ := strconv.Atoi(parts[1])
	attached := parts[2] == "1"

	session := &Session{
		Name:     name,
		Windows:  windows,
		Attached: attached,
	}

	// Get panes
	panes, err := GetPanes(name)
	if err != nil {
		return nil, err
	}
	session.Panes = panes

	return session, nil
}

// GetPanes returns all panes in a session
func GetPanes(session string) ([]Pane, error) {
	output, err := run("list-panes", "-s", "-t", session, "-F", "#{pane_id}:#{pane_index}:#{pane_title}:#{pane_current_command}:#{pane_width}:#{pane_height}:#{pane_active}")
	if err != nil {
		return nil, err
	}

	var panes []Pane
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 7)
		if len(parts) < 7 {
			continue
		}

		index, _ := strconv.Atoi(parts[1])
		width, _ := strconv.Atoi(parts[4])
		height, _ := strconv.Atoi(parts[5])
		active := parts[6] == "1"

		pane := Pane{
			ID:      parts[0],
			Index:   index,
			Title:   parts[2],
			Command: parts[3],
			Width:   width,
			Height:  height,
			Active:  active,
		}

		// Determine agent type from title
		pane.Type = AgentUser
		if strings.Contains(pane.Title, "__cc") {
			pane.Type = AgentClaude
		} else if strings.Contains(pane.Title, "__cod") {
			pane.Type = AgentCodex
		} else if strings.Contains(pane.Title, "__gmi") {
			pane.Type = AgentGemini
		}

		panes = append(panes, pane)
	}

	return panes, nil
}

// GetFirstWindow returns the first window index for a session
func GetFirstWindow(session string) (int, error) {
	output, err := run("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, errors.New("no windows found")
	}

	return strconv.Atoi(lines[0])
}

// GetDefaultPaneIndex returns the default pane index (respects pane-base-index)
func GetDefaultPaneIndex(session string) (int, error) {
	firstWin, err := GetFirstWindow(session)
	if err != nil {
		return 0, err
	}

	output, err := run("list-panes", "-t", fmt.Sprintf("%s:%d", session, firstWin), "-F", "#{pane_index}")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, errors.New("no panes found")
	}

	return strconv.Atoi(lines[0])
}

// CreateSession creates a new tmux session
func CreateSession(name, directory string) error {
	return runSilent("new-session", "-d", "-s", name, "-c", directory)
}

// SplitWindow creates a new pane in the session
func SplitWindow(session string, directory string) (string, error) {
	firstWin, err := GetFirstWindow(session)
	if err != nil {
		return "", err
	}

	target := fmt.Sprintf("%s:%d", session, firstWin)

	// Split and get the new pane ID
	paneID, err := run("split-window", "-t", target, "-c", directory, "-P", "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}

	// Apply tiled layout
	_ = runSilent("select-layout", "-t", target, "tiled")

	return paneID, nil
}

// SetPaneTitle sets the title of a pane
func SetPaneTitle(paneID, title string) error {
	return runSilent("select-pane", "-t", paneID, "-T", title)
}

// SendKeys sends keys to a pane
func SendKeys(target, keys string, enter bool) error {
	if err := runSilent("send-keys", "-t", target, "-l", keys); err != nil {
		return err
	}
	if enter {
		return runSilent("send-keys", "-t", target, "C-m")
	}
	return nil
}

// SendInterrupt sends Ctrl+C to a pane
func SendInterrupt(target string) error {
	return runSilent("send-keys", "-t", target, "C-c")
}

// AttachOrSwitch attaches to a session or switches if already in tmux
func AttachOrSwitch(session string) error {
	if InTmux() {
		return runSilent("switch-client", "-t", session)
	}

	cmd := exec.Command("tmux", "attach", "-t", session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// KillSession kills a tmux session
func KillSession(session string) error {
	return runSilent("kill-session", "-t", session)
}

// ApplyTiledLayout applies tiled layout to all windows
func ApplyTiledLayout(session string) error {
	output, err := run("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return err
	}

	for _, winIdx := range strings.Split(output, "\n") {
		if winIdx == "" {
			continue
		}

		target := fmt.Sprintf("%s:%s", session, winIdx)

		// Unzoom if zoomed
		zoomed, _ := run("display-message", "-t", target, "-p", "#{window_zoomed_flag}")
		if zoomed == "1" {
			_ = runSilent("resize-pane", "-t", target, "-Z")
		}

		// Apply tiled layout
		_ = runSilent("select-layout", "-t", target, "tiled")
	}

	return nil
}

// ZoomPane zooms a specific pane
func ZoomPane(session string, paneIndex int) error {
	firstWin, err := GetFirstWindow(session)
	if err != nil {
		return err
	}

	target := fmt.Sprintf("%s:%d.%d", session, firstWin, paneIndex)

	if err := runSilent("select-pane", "-t", target); err != nil {
		return err
	}

	return runSilent("resize-pane", "-t", target, "-Z")
}

// CapturePaneOutput captures the output of a pane
func CapturePaneOutput(target string, lines int) (string, error) {
	return run("capture-pane", "-t", target, "-p", "-S", fmt.Sprintf("-%d", lines))
}

// GetCurrentSession returns the current session name (if in tmux)
func GetCurrentSession() string {
	if !InTmux() {
		return ""
	}
	output, err := run("display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return output
}

// ValidateSessionName checks if a session name is valid
func ValidateSessionName(name string) error {
	if name == "" {
		return errors.New("session name cannot be empty")
	}
	if strings.ContainsAny(name, ":.") {
		return errors.New("session name cannot contain ':' or '.'")
	}
	return nil
}

// GetPaneActivity returns the last activity time for a pane
func GetPaneActivity(paneID string) (time.Time, error) {
	output, err := run("display-message", "-p", "-t", paneID, "#{pane_last_activity}")
	if err != nil {
		return time.Time{}, err
	}

	timestamp, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse pane activity timestamp: %w", err)
	}

	return time.Unix(timestamp, 0), nil
}

// PaneActivity contains pane info with activity timestamp
type PaneActivity struct {
	Pane         Pane
	LastActivity time.Time
}

// GetPanesWithActivity returns all panes in a session with their activity times
func GetPanesWithActivity(session string) ([]PaneActivity, error) {
	output, err := run("list-panes", "-s", "-t", session, "-F",
		"#{pane_id}:#{pane_index}:#{pane_title}:#{pane_current_command}:#{pane_width}:#{pane_height}:#{pane_active}:#{pane_last_activity}")
	if err != nil {
		return nil, err
	}

	var panes []PaneActivity
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 8)
		if len(parts) < 8 {
			continue
		}

		index, _ := strconv.Atoi(parts[1])
		width, _ := strconv.Atoi(parts[4])
		height, _ := strconv.Atoi(parts[5])
		active := parts[6] == "1"
		timestamp, _ := strconv.ParseInt(parts[7], 10, 64)

		pane := Pane{
			ID:      parts[0],
			Index:   index,
			Title:   parts[2],
			Command: parts[3],
			Width:   width,
			Height:  height,
			Active:  active,
		}

		// Determine agent type from title
		pane.Type = AgentUser
		if strings.Contains(pane.Title, "__cc") {
			pane.Type = AgentClaude
		} else if strings.Contains(pane.Title, "__cod") {
			pane.Type = AgentCodex
		} else if strings.Contains(pane.Title, "__gmi") {
			pane.Type = AgentGemini
		}

		panes = append(panes, PaneActivity{
			Pane:         pane,
			LastActivity: time.Unix(timestamp, 0),
		})
	}

	return panes, nil
}

// IsRecentlyActive checks if a pane has had activity within the threshold
func IsRecentlyActive(paneID string, threshold time.Duration) (bool, error) {
	lastActivity, err := GetPaneActivity(paneID)
	if err != nil {
		return false, err
	}

	return time.Since(lastActivity) <= threshold, nil
}

// GetPaneLastActivityAge returns how long ago the pane was last active
func GetPaneLastActivityAge(paneID string) (time.Duration, error) {
	lastActivity, err := GetPaneActivity(paneID)
	if err != nil {
		return 0, err
	}

	return time.Since(lastActivity), nil
}
