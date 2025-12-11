package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// paneNameRegex matches the NTM pane naming convention:
// session__type_index or session__type_index_variant, optionally with tags [tag1,tag2]
// Examples:
//
//	session__cc_1
//	session__cc_1[frontend]
//	session__cc_1_opus[backend,api]
var paneNameRegex = regexp.MustCompile(`^.+__(\w+)_\d+(?:_([A-Za-z0-9._/@:+-]+))?(?:[^\]]*)?$`)

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
	ID      string
	Index   int
	Title   string
	Type    AgentType
	Variant string   // Model alias or persona name (from pane title)
	Tags    []string // User-defined tags (from pane title, e.g., [frontend,api])
	Command string
	Width   int
	Height  int
	Active  bool
}

// Session represents a tmux session
type Session struct {
	Name      string
	Directory string
	Windows   int
	Panes     []Pane
	Attached  bool
	Created   string
}

// parseAgentFromTitle extracts agent type, variant, and tags from a pane title.
// Title format: {session}__{type}_{index}[tags] or {session}__{type}_{index}_{variant}[tags]
// Returns AgentUser, empty variant, and nil tags if title doesn't match NTM format.
func parseAgentFromTitle(title string) (AgentType, string, []string) {
	matches := paneNameRegex.FindStringSubmatch(title)
	if matches == nil {
		// Not an NTM-formatted title, default to user
		return AgentUser, "", nil
	}

	// matches[1] = type (cc, cod, gmi)
	// matches[2] = variant (may be empty)
	// matches[3] = tags string (may be empty, may be absent if regex didn't capture)
	agentType := AgentType(matches[1])
	variant := matches[2]
	var tags []string
	if len(matches) >= 4 {
		tags = parseTags(matches[3])
	}

	// Validate agent type
	switch agentType {
	case AgentClaude, AgentCodex, AgentGemini:
		return agentType, variant, tags
	default:
		return AgentUser, "", nil
	}
}

// parseTags parses a comma-separated tag string into a slice.
// Returns nil for empty input.
func parseTags(tagStr string) []string {
	if tagStr == "" {
		return nil
	}
	parts := strings.Split(tagStr, ",")
	var tags []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			tags = append(tags, p)
		}
	}
	return tags
}

// FormatTags formats tags as a bracket-enclosed string for pane titles.
// Returns empty string if no tags.
func FormatTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return "[" + strings.Join(tags, ",") + "]"
}

// IsInstalled checks if tmux is available
func IsInstalled() bool {
	return DefaultClient.IsInstalled()
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

// SessionExists checks if a session exists
func (c *Client) SessionExists(name string) bool {
	err := c.RunSilent("has-session", "-t", name)
	return err == nil
}

// SessionExists checks if a session exists (default client)
func SessionExists(name string) bool {
	return DefaultClient.SessionExists(name)
}

// ListSessions returns all tmux sessions
func (c *Client) ListSessions() ([]Session, error) {
	output, err := c.Run("list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_attached}:#{session_created_string}")
	if err != nil {
		// No sessions is not an error - handle various tmux error messages
		errMsg := err.Error()
		if strings.Contains(errMsg, "no server running") ||
			strings.Contains(errMsg, "no sessions") ||
			strings.Contains(errMsg, "No such file or directory") ||
			strings.Contains(errMsg, "error connecting to") {
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

// ListSessions returns all tmux sessions (default client)
func ListSessions() ([]Session, error) {
	return DefaultClient.ListSessions()
}

// GetSession returns detailed info about a session
func (c *Client) GetSession(name string) (*Session, error) {
	if !SessionExists(name) {
		return nil, fmt.Errorf("session '%s' not found", name)
	}

	// Get session info
	output, err := c.Run("list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_attached}", "-f", fmt.Sprintf("#{==:#{session_name},%s}", name))
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
	panes, err := c.GetPanes(name)
	if err != nil {
		return nil, err
	}
	session.Panes = panes

	return session, nil
}

// GetSession returns detailed info about a session (default client)
func GetSession(name string) (*Session, error) {
	return DefaultClient.GetSession(name)
}

// CreateSession creates a new tmux session
func (c *Client) CreateSession(name, directory string) error {
	return c.RunSilent("new-session", "-d", "-s", name, "-c", directory)
}

// CreateSession creates a new tmux session (default client)
func CreateSession(name, directory string) error {
	return DefaultClient.CreateSession(name, directory)
}

// GetPanes returns all panes in a session
func (c *Client) GetPanes(session string) ([]Pane, error) {
	sep := "|#|"
	format := fmt.Sprintf("#{pane_id}%[1]s#{pane_index}%[1]s#{pane_title}%[1]s#{pane_current_command}%[1]s#{pane_width}%[1]s#{pane_height}%[1]s#{pane_active}", sep)
	output, err := c.Run("list-panes", "-s", "-t", session, "-F", format)
	if err != nil {
		return nil, err
	}

	var panes []Pane
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.Split(line, sep)
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

		// Parse pane title using regex to extract type, variant, and tags
		// Format: {session}__{type}_{index} or {session}__{type}_{index}_{variant}
		pane.Type, pane.Variant, pane.Tags = parseAgentFromTitle(pane.Title)

		panes = append(panes, pane)
	}

	return panes, nil
}

// GetPanes returns all panes in a session (default client)
func GetPanes(session string) ([]Pane, error) {
	return DefaultClient.GetPanes(session)
}

// GetFirstWindow returns the first window index for a session
func (c *Client) GetFirstWindow(session string) (int, error) {
	output, err := c.Run("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, errors.New("no windows found")
	}

	return strconv.Atoi(lines[0])
}

// GetFirstWindow returns the first window index for a session (default client)
func GetFirstWindow(session string) (int, error) {
	return DefaultClient.GetFirstWindow(session)
}

// GetDefaultPaneIndex returns the default pane index (respects pane-base-index)
func (c *Client) GetDefaultPaneIndex(session string) (int, error) {
	firstWin, err := c.GetFirstWindow(session)
	if err != nil {
		return 0, err
	}

	output, err := c.Run("list-panes", "-t", fmt.Sprintf("%s:%d", session, firstWin), "-F", "#{pane_index}")
	if err != nil {
		return 0, err
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return 0, errors.New("no panes found")
	}

	return strconv.Atoi(lines[0])
}

// GetDefaultPaneIndex returns the default pane index (default client)
func GetDefaultPaneIndex(session string) (int, error) {
	return DefaultClient.GetDefaultPaneIndex(session)
}

// SplitWindow creates a new pane in the session
func (c *Client) SplitWindow(session string, directory string) (string, error) {
	firstWin, err := c.GetFirstWindow(session)
	if err != nil {
		return "", err
	}

	target := fmt.Sprintf("%s:%d", session, firstWin)

	// Split and get the new pane ID
	paneID, err := c.Run("split-window", "-t", target, "-c", directory, "-P", "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}

	// Apply tiled layout
	_ = c.RunSilent("select-layout", "-t", target, "tiled")

	return paneID, nil
}

// SplitWindow creates a new pane in the session (default client)
func SplitWindow(session string, directory string) (string, error) {
	return DefaultClient.SplitWindow(session, directory)
}

// SetPaneTitle sets the title of a pane
func (c *Client) SetPaneTitle(paneID, title string) error {
	return c.RunSilent("select-pane", "-t", paneID, "-T", title)
}

// SetPaneTitle sets the title of a pane (default client)
func SetPaneTitle(paneID, title string) error {
	return DefaultClient.SetPaneTitle(paneID, title)
}

// GetPaneTitle returns the title of a pane
func (c *Client) GetPaneTitle(paneID string) (string, error) {
	return c.Run("display-message", "-p", "-t", paneID, "#{pane_title}")
}

// GetPaneTitle returns the title of a pane (default client)
func GetPaneTitle(paneID string) (string, error) {
	return DefaultClient.GetPaneTitle(paneID)
}

// GetPaneTags returns the tags for a pane parsed from its title.
// Returns nil if no tags are found.
func (c *Client) GetPaneTags(paneID string) ([]string, error) {
	title, err := c.GetPaneTitle(paneID)
	if err != nil {
		return nil, err
	}
	_, _, tags := parseAgentFromTitle(title)
	return tags, nil
}

// GetPaneTags returns the tags for a pane (default client)
func GetPaneTags(paneID string) ([]string, error) {
	return DefaultClient.GetPaneTags(paneID)
}

// SetPaneTags sets the tags for a pane by updating its title.
// Tags are appended to the title in the format [tag1,tag2,...].
// This replaces any existing tags on the pane.
func (c *Client) SetPaneTags(paneID string, tags []string) error {
	title, err := c.GetPaneTitle(paneID)
	if err != nil {
		return err
	}

	// Strip existing tags from title
	baseTitle := stripTags(title)
	newTitle := baseTitle + FormatTags(tags)

	return c.SetPaneTitle(paneID, newTitle)
}

// SetPaneTags sets the tags for a pane (default client)
func SetPaneTags(paneID string, tags []string) error {
	return DefaultClient.SetPaneTags(paneID, tags)
}

// AddPaneTags adds tags to a pane without removing existing ones.
// Duplicate tags are not added.
func (c *Client) AddPaneTags(paneID string, newTags []string) error {
	existing, err := c.GetPaneTags(paneID)
	if err != nil {
		return err
	}

	// Build set of existing tags
	tagSet := make(map[string]bool)
	for _, t := range existing {
		tagSet[t] = true
	}

	// Add new tags
	for _, t := range newTags {
		if !tagSet[t] {
			existing = append(existing, t)
			tagSet[t] = true
		}
	}

	return c.SetPaneTags(paneID, existing)
}

// AddPaneTags adds tags to a pane (default client)
func AddPaneTags(paneID string, newTags []string) error {
	return DefaultClient.AddPaneTags(paneID, newTags)
}

// RemovePaneTags removes specific tags from a pane.
func (c *Client) RemovePaneTags(paneID string, tagsToRemove []string) error {
	existing, err := c.GetPaneTags(paneID)
	if err != nil {
		return err
	}

	// Build set of tags to remove
	removeSet := make(map[string]bool)
	for _, t := range tagsToRemove {
		removeSet[t] = true
	}

	// Filter out removed tags
	var filtered []string
	for _, t := range existing {
		if !removeSet[t] {
			filtered = append(filtered, t)
		}
	}

	return c.SetPaneTags(paneID, filtered)
}

// RemovePaneTags removes specific tags from a pane (default client)
func RemovePaneTags(paneID string, tagsToRemove []string) error {
	return DefaultClient.RemovePaneTags(paneID, tagsToRemove)
}

// HasPaneTag returns true if the pane has the specified tag.
func (c *Client) HasPaneTag(paneID, tag string) (bool, error) {
	tags, err := c.GetPaneTags(paneID)
	if err != nil {
		return false, err
	}
	for _, t := range tags {
		if t == tag {
			return true, nil
		}
	}
	return false, nil
}

// HasPaneTag returns true if the pane has the specified tag (default client)
func HasPaneTag(paneID, tag string) (bool, error) {
	return DefaultClient.HasPaneTag(paneID, tag)
}

// HasAnyPaneTag returns true if the pane has any of the specified tags (OR logic).
func (c *Client) HasAnyPaneTag(paneID string, tags []string) (bool, error) {
	paneTags, err := c.GetPaneTags(paneID)
	if err != nil {
		return false, err
	}
	tagSet := make(map[string]bool)
	for _, t := range paneTags {
		tagSet[t] = true
	}
	for _, t := range tags {
		if tagSet[t] {
			return true, nil
		}
	}
	return false, nil
}

// HasAnyPaneTag returns true if the pane has any of the specified tags (default client)
func HasAnyPaneTag(paneID string, tags []string) (bool, error) {
	return DefaultClient.HasAnyPaneTag(paneID, tags)
}

// stripTags removes the [tags] suffix from a pane title.
func stripTags(title string) string {
	// Find last '[' that's followed by any characters and ']' at end
	idx := strings.LastIndex(title, "[")
	if idx == -1 {
		return title
	}
	// Check if it ends with ']'
	if strings.HasSuffix(title, "]") && idx < len(title)-1 {
		return title[:idx]
	}
	return title
}

// SendKeys sends keys to a pane
func (c *Client) SendKeys(target, keys string, enter bool) error {
	if err := c.RunSilent("send-keys", "-t", target, "-l", "--", keys); err != nil {
		return err
	}
	if enter {
		return c.RunSilent("send-keys", "-t", target, "C-m")
	}
	return nil
}

// FormatPaneName formats a pane title according to NTM convention
func FormatPaneName(session string, agentType string, index int, variant string) string {
	base := fmt.Sprintf("%s__%s_%d", session, agentType, index)
	if variant != "" {
		return fmt.Sprintf("%s_%s", base, variant)
	}
	return base
}

// SendKeys sends keys to a pane (default client)
func SendKeys(target, keys string, enter bool) error {
	return DefaultClient.SendKeys(target, keys, enter)
}

// SendInterrupt sends Ctrl+C to a pane
func (c *Client) SendInterrupt(target string) error {
	return c.RunSilent("send-keys", "-t", target, "C-c")
}

// SendInterrupt sends Ctrl+C to a pane (default client)
func SendInterrupt(target string) error {
	return DefaultClient.SendInterrupt(target)
}

// DisplayMessage shows a message in the tmux status line
func (c *Client) DisplayMessage(session, msg string, durationMs int) error {
	return c.RunSilent("display-message", "-t", session, "-d", fmt.Sprintf("%d", durationMs), msg)
}

// DisplayMessage shows a message in the tmux status line (default client)
func DisplayMessage(session, msg string, durationMs int) error {
	return DefaultClient.DisplayMessage(session, msg, durationMs)
}

// SanitizePaneCommand rejects control characters that could inject unintended
// key sequences (e.g., newlines, carriage returns, escapes) when sending
// commands into tmux panes.
func SanitizePaneCommand(cmd string) (string, error) {
	for _, r := range cmd {
		switch {
		case r == '\n', r == '\r', r == 0:
			return "", fmt.Errorf("command contains disallowed control characters")
		case r < 0x20 && r != ' ' && r != '\t':
			return "", fmt.Errorf("command contains disallowed control character 0x%02x", r)
		}
	}
	return cmd, nil
}

// BuildPaneCommand constructs a safe cd+command string for execution inside a
// tmux pane, rejecting commands with unsafe control characters.
func BuildPaneCommand(projectDir, agentCommand string) (string, error) {
	safeCommand, err := SanitizePaneCommand(agentCommand)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("cd %q && %s", projectDir, safeCommand), nil
}

// AttachOrSwitch attaches to a session or switches if already in tmux
func (c *Client) AttachOrSwitch(session string) error {
	if c.Remote == "" {
		if InTmux() {
			return c.RunSilent("switch-client", "-t", session)
		}
		// Interactive attach needs stdin/stdout, so use exec directly for local
		cmd := exec.Command("tmux", "attach", "-t", session)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Remote attach
	// ssh -t user@host tmux attach -t session
	sshArgs := []string{"-t", c.Remote, "tmux", "attach", "-t", session}
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// AttachOrSwitch attaches to a session or switches if already in tmux (default client)
func AttachOrSwitch(session string) error {
	return DefaultClient.AttachOrSwitch(session)
}

// KillSession kills a tmux session
func (c *Client) KillSession(session string) error {
	return c.RunSilent("kill-session", "-t", session)
}

// KillSession kills a tmux session (default client)
func KillSession(session string) error {
	return DefaultClient.KillSession(session)
}

// KillPane kills a tmux pane
func (c *Client) KillPane(paneID string) error {
	return c.RunSilent("kill-pane", "-t", paneID)
}

// KillPane kills a tmux pane (default client)
func KillPane(paneID string) error {
	return DefaultClient.KillPane(paneID)
}

// ApplyTiledLayout applies tiled layout to all windows
func (c *Client) ApplyTiledLayout(session string) error {
	output, err := c.Run("list-windows", "-t", session, "-F", "#{window_index}")
	if err != nil {
		return err
	}

	for _, winIdx := range strings.Split(output, "\n") {
		if winIdx == "" {
			continue
		}

		target := fmt.Sprintf("%s:%s", session, winIdx)

		// Unzoom if zoomed
		zoomed, _ := c.Run("display-message", "-t", target, "-p", "#{window_zoomed_flag}")
		if zoomed == "1" {
			_ = c.RunSilent("resize-pane", "-t", target, "-Z")
		}

		// Apply tiled layout
		_ = c.RunSilent("select-layout", "-t", target, "tiled")
	}

	return nil
}

// ApplyTiledLayout applies tiled layout to all windows (default client)
func ApplyTiledLayout(session string) error {
	return DefaultClient.ApplyTiledLayout(session)
}

// ZoomPane zooms a specific pane
func (c *Client) ZoomPane(session string, paneIndex int) error {
	firstWin, err := c.GetFirstWindow(session)
	if err != nil {
		return err
	}

	target := fmt.Sprintf("%s:%d.%d", session, firstWin, paneIndex)

	if err := c.RunSilent("select-pane", "-t", target); err != nil {
		return err
	}

	return c.RunSilent("resize-pane", "-t", target, "-Z")
}

// ZoomPane zooms a specific pane (default client)
func ZoomPane(session string, paneIndex int) error {
	return DefaultClient.ZoomPane(session, paneIndex)
}

// CapturePaneOutput captures the output of a pane
func (c *Client) CapturePaneOutput(target string, lines int) (string, error) {
	return c.Run("capture-pane", "-t", target, "-p", "-S", fmt.Sprintf("-%d", lines))
}

// CapturePaneOutput captures the output of a pane (default client)
func CapturePaneOutput(target string, lines int) (string, error) {
	return DefaultClient.CapturePaneOutput(target, lines)
}

// GetCurrentSession returns the current session name (if in tmux)
func (c *Client) GetCurrentSession() string {
	if c.Remote == "" {
		if !InTmux() {
			return ""
		}
	} else {
		// Remote check logic might differ or be unsupported
		// For now, assume unsupported or return empty
		return ""
	}
	output, err := c.Run("display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return output
}

// GetCurrentSession returns the current session name (default client)
func GetCurrentSession() string {
	return DefaultClient.GetCurrentSession()
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
func (c *Client) GetPaneActivity(paneID string) (time.Time, error) {
	output, err := c.Run("display-message", "-p", "-t", paneID, "#{pane_last_activity}")
	if err != nil {
		return time.Time{}, err
	}

	// Some tmux versions may return an empty string for fresh panes; treat as current time
	if strings.TrimSpace(output) == "" {
		return time.Now(), nil
	}

	timestamp, err := strconv.ParseInt(output, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse pane activity timestamp: %w", err)
	}

	return time.Unix(timestamp, 0), nil
}

// GetPaneActivity returns the last activity time for a pane (default client)
func GetPaneActivity(paneID string) (time.Time, error) {
	return DefaultClient.GetPaneActivity(paneID)
}

// PaneActivity contains pane info with activity timestamp
type PaneActivity struct {
	Pane         Pane
	LastActivity time.Time
}

// GetPanesWithActivity returns all panes in a session with their activity times
func (c *Client) GetPanesWithActivity(session string) ([]PaneActivity, error) {
	sep := "|#|"
	format := fmt.Sprintf("#{pane_id}%[1]s#{pane_index}%[1]s#{pane_title}%[1]s#{pane_current_command}%[1]s#{pane_width}%[1]s#{pane_height}%[1]s#{pane_active}%[1]s#{pane_last_activity}", sep)
	output, err := c.Run("list-panes", "-s", "-t", session, "-F", format)
	if err != nil {
		return nil, err
	}

	var panes []PaneActivity
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		parts := strings.Split(line, sep)
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

		// Parse pane title using regex to extract type, variant, and tags
		pane.Type, pane.Variant, pane.Tags = parseAgentFromTitle(pane.Title)

		panes = append(panes, PaneActivity{
			Pane:         pane,
			LastActivity: time.Unix(timestamp, 0),
		})
	}

	return panes, nil
}

// GetPanesWithActivity returns all panes in a session with their activity times (default client)
func GetPanesWithActivity(session string) ([]PaneActivity, error) {
	return DefaultClient.GetPanesWithActivity(session)
}

// IsRecentlyActive checks if a pane has had activity within the threshold
func (c *Client) IsRecentlyActive(paneID string, threshold time.Duration) (bool, error) {
	lastActivity, err := c.GetPaneActivity(paneID)
	if err != nil {
		return false, err
	}

	return time.Since(lastActivity) <= threshold, nil
}

// IsRecentlyActive checks if a pane has had activity within the threshold (default client)
func IsRecentlyActive(paneID string, threshold time.Duration) (bool, error) {
	return DefaultClient.IsRecentlyActive(paneID, threshold)
}

// GetPaneLastActivityAge returns how long ago the pane was last active
func (c *Client) GetPaneLastActivityAge(paneID string) (time.Duration, error) {
	lastActivity, err := c.GetPaneActivity(paneID)
	if err != nil {
		return 0, err
	}

	return time.Since(lastActivity), nil
}

// GetPaneLastActivityAge returns how long ago the pane was last active (default client)
func GetPaneLastActivityAge(paneID string) (time.Duration, error) {
	return DefaultClient.GetPaneLastActivityAge(paneID)
}

// IsAttached checks if a session is currently attached
func (c *Client) IsAttached(session string) bool {
	output, err := c.Run("list-sessions", "-F", "#{session_name}:#{session_attached}", "-f", fmt.Sprintf("#{==:#{session_name},%s}", session))
	if err != nil || output == "" {
		return false
	}
	parts := strings.Split(output, ":")
	if len(parts) < 2 {
		return false
	}
	return parts[1] == "1"
}

// IsAttached checks if a session is currently attached (default client)
func IsAttached(session string) bool {
	return DefaultClient.IsAttached(session)
}
