// Package notify provides notification support for NTM events.
// Supports desktop notifications, webhooks, shell commands, and log files.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/template"
	"time"
)

// EventType represents the type of notification event
type EventType string

const (
	EventAgentError     EventType = "agent.error"      // Agent hit error state
	EventAgentCrashed   EventType = "agent.crashed"    // Agent process exited
	EventAgentRestarted EventType = "agent.restarted"  // Agent was auto-restarted
	EventAgentIdle      EventType = "agent.idle"       // Agent waiting for input
	EventRateLimit      EventType = "agent.rate_limit" // Agent hit rate limit
	EventRotationNeeded EventType = "rotation.needed"  // Account rotation recommended
	EventSessionCreated EventType = "session.created"  // New session spawned
	EventSessionKilled  EventType = "session.killed"   // Session terminated
	EventHealthDegraded EventType = "health.degraded"  // Overall health dropped
)

// Event represents a notification event
type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Session   string            `json:"session,omitempty"`
	Pane      string            `json:"pane,omitempty"`
	Agent     string            `json:"agent,omitempty"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
}

// Config holds notification configuration
type Config struct {
	Enabled bool     `toml:"enabled"`
	Events  []string `toml:"events"` // Which events to notify on

	// Routing configuration (optional, for advanced routing)
	Primary  string              `toml:"primary"`  // Primary channel name
	Fallback string              `toml:"fallback"` // Fallback channel if primary fails
	Routing  map[string][]string `toml:"routing"`  // Event type -> ordered channel list

	Desktop DesktopConfig `toml:"desktop"`
	Webhook WebhookConfig `toml:"webhook"`
	Shell   ShellConfig   `toml:"shell"`
	Log     LogConfig     `toml:"log"`
	FileBox FileBoxConfig `toml:"filebox"` // File inbox for offline review
}

// DesktopConfig configures desktop notifications
type DesktopConfig struct {
	Enabled bool   `toml:"enabled"`
	Title   string `toml:"title"` // Default title prefix
}

// WebhookConfig configures webhook notifications
type WebhookConfig struct {
	Enabled  bool              `toml:"enabled"`
	URL      string            `toml:"url"`
	Template string            `toml:"template"` // Go template for payload
	Method   string            `toml:"method"`   // HTTP method (default POST)
	Headers  map[string]string `toml:"headers"`
}

// ShellConfig configures shell command notifications
type ShellConfig struct {
	Enabled  bool   `toml:"enabled"`
	Command  string `toml:"command"`   // Command to run
	PassJSON bool   `toml:"pass_json"` // Pass event as JSON stdin
}

// LogConfig configures log file notifications
type LogConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"` // Log file path
}

// FileBoxConfig configures file inbox for offline human review
type FileBoxConfig struct {
	Enabled bool   `toml:"enabled"`
	Path    string `toml:"path"` // Directory for inbox files (default: .ntm/human_inbox/)
}

// DefaultConfig returns a default notification configuration
func DefaultConfig() Config {
	return Config{
		Enabled:  true,
		Events:   []string{string(EventAgentError), string(EventAgentCrashed)},
		Primary:  "desktop",
		Fallback: "filebox",
		Routing:  nil, // Use default (all enabled channels in parallel)
		Desktop: DesktopConfig{
			Enabled: true,
			Title:   "NTM",
		},
		Webhook: WebhookConfig{
			Enabled:  false,
			Method:   "POST",
			Template: `{"text": "NTM: {{.Type}} - {{jsonEscape .Message}}"}`,
		},
		Shell: ShellConfig{
			Enabled:  false,
			PassJSON: true,
		},
		Log: LogConfig{
			Enabled: false,
			Path:    "~/.config/ntm/notifications.log",
		},
		FileBox: FileBoxConfig{
			Enabled: true,
			Path:    ".ntm/human_inbox",
		},
	}
}

// ChannelName identifies a notification channel
type ChannelName string

const (
	ChannelDesktop ChannelName = "desktop"
	ChannelWebhook ChannelName = "webhook"
	ChannelShell   ChannelName = "shell"
	ChannelLog     ChannelName = "log"
	ChannelFileBox ChannelName = "filebox"
)

// Notifier sends notifications through configured channels
type Notifier struct {
	config     Config
	enabledSet map[EventType]bool
	channels   map[ChannelName]bool // Which channels are enabled
	mu         sync.Mutex
	httpClient *http.Client
}

// expandEnvVars expands environment variables in a string (${VAR} or $VAR format)
func expandEnvVars(s string) string {
	return os.ExpandEnv(s)
}

// New creates a new Notifier with the given configuration
func New(cfg Config) *Notifier {
	// Expand environment variables in config values
	cfg.Webhook.URL = expandEnvVars(cfg.Webhook.URL)
	cfg.Shell.Command = expandEnvVars(cfg.Shell.Command)
	cfg.Log.Path = expandEnvVars(cfg.Log.Path)
	cfg.FileBox.Path = expandEnvVars(cfg.FileBox.Path)
	for k, v := range cfg.Webhook.Headers {
		cfg.Webhook.Headers[k] = expandEnvVars(v)
	}

	n := &Notifier{
		config:     cfg,
		enabledSet: make(map[EventType]bool),
		channels:   make(map[ChannelName]bool),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Build set of enabled events
	for _, e := range cfg.Events {
		n.enabledSet[EventType(e)] = true
	}

	// Build set of enabled channels
	if cfg.Desktop.Enabled {
		n.channels[ChannelDesktop] = true
	}
	if cfg.Webhook.Enabled && cfg.Webhook.URL != "" {
		n.channels[ChannelWebhook] = true
	}
	if cfg.Shell.Enabled && cfg.Shell.Command != "" {
		n.channels[ChannelShell] = true
	}
	if cfg.Log.Enabled && cfg.Log.Path != "" {
		n.channels[ChannelLog] = true
	}
	if cfg.FileBox.Enabled {
		n.channels[ChannelFileBox] = true
	}

	return n
}

// sendToChannel sends an event to a specific channel
func (n *Notifier) sendToChannel(ch ChannelName, event Event) error {
	if !n.channels[ch] {
		return fmt.Errorf("channel %s not enabled", ch)
	}

	switch ch {
	case ChannelDesktop:
		return n.sendDesktop(event)
	case ChannelWebhook:
		return n.sendWebhook(event)
	case ChannelShell:
		return n.sendShell(event)
	case ChannelLog:
		return n.sendLog(event)
	case ChannelFileBox:
		return n.sendFileBox(event)
	default:
		return fmt.Errorf("unknown channel: %s", ch)
	}
}

// getChannelsForEvent returns the ordered list of channels for an event type
func (n *Notifier) getChannelsForEvent(eventType EventType) []ChannelName {
	// Check for routing rules first
	if n.config.Routing != nil {
		if channels, ok := n.config.Routing[string(eventType)]; ok && len(channels) > 0 {
			result := make([]ChannelName, 0, len(channels))
			for _, ch := range channels {
				result = append(result, ChannelName(ch))
			}
			return result
		}
	}

	// Fall back to primary/fallback if specified
	if n.config.Primary != "" {
		channels := []ChannelName{ChannelName(n.config.Primary)}
		if n.config.Fallback != "" {
			channels = append(channels, ChannelName(n.config.Fallback))
		}
		return channels
	}

	// Default: return all enabled channels
	var channels []ChannelName
	for ch := range n.channels {
		channels = append(channels, ch)
	}
	return channels
}

// Notify sends a notification for the given event
func (n *Notifier) Notify(event Event) error {
	if !n.config.Enabled {
		return nil
	}

	// Check if this event type is enabled
	if !n.enabledSet[event.Type] {
		return nil
	}

	// Set timestamp if not set
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	// Get channels for this event type
	channels := n.getChannelsForEvent(event.Type)
	if len(channels) == 0 {
		return nil
	}

	// If routing is configured, try channels in order (stop on first success)
	if n.config.Routing != nil || n.config.Primary != "" {
		var lastErr error
		for _, ch := range channels {
			if err := n.sendToChannel(ch, event); err != nil {
				lastErr = err
				continue
			}
			// Success on this channel
			return nil
		}
		// All channels failed
		if lastErr != nil {
			return fmt.Errorf("all channels failed, last error: %w", lastErr)
		}
		return nil
	}

	// Default behavior: send through all enabled channels in parallel
	var (
		wg    sync.WaitGroup
		errs  []error
		errMu sync.Mutex
	)

	addErr := func(err error) {
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	}

	for _, ch := range channels {
		ch := ch // Capture for goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendToChannel(ch, event); err != nil {
				addErr(fmt.Errorf("%s: %w", ch, err))
			}
		}()
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %v", errs)
	}

	return nil
}

// sendDesktop sends a desktop notification
func (n *Notifier) sendDesktop(event Event) error {
	title := n.config.Desktop.Title
	if title == "" {
		title = "NTM"
	}
	if event.Session != "" {
		title = fmt.Sprintf("%s [%s]", title, event.Session)
	}

	message := event.Message
	if message == "" {
		message = string(event.Type)
	}

	switch runtime.GOOS {
	case "darwin":
		return sendMacOSNotification(title, message)
	case "linux":
		return sendLinuxNotification(title, message)
	default:
		return fmt.Errorf("desktop notifications not supported on %s", runtime.GOOS)
	}
}

// sendMacOSNotification sends a notification on macOS using osascript
func sendMacOSNotification(title, message string) error {
	script := fmt.Sprintf(`display notification %q with title %q`, message, title)
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Run()
}

// sendLinuxNotification sends a notification on Linux using notify-send
func sendLinuxNotification(title, message string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return fmt.Errorf("notify-send not found")
	}
	cmd := exec.Command("notify-send", title, message)
	return cmd.Run()
}

// jsonEscape escapes a string for safe embedding in JSON.
// This is needed when using text/template to generate JSON.
func jsonEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	// json.Marshal wraps in quotes, remove them for template use
	return string(b[1 : len(b)-1])
}

// sendWebhook sends a webhook notification
func (n *Notifier) sendWebhook(event Event) error {
	// Parse and execute template with JSON escape function
	tmplStr := n.config.Webhook.Template
	if tmplStr == "" {
		// Default JSON template using jsonEscape for user-controlled fields
		tmplStr = `{"event":"{{.Type}}","message":"{{jsonEscape .Message}}","session":"{{jsonEscape .Session}}","timestamp":"{{.Timestamp}}"}`
	}

	funcMap := template.FuncMap{
		"jsonEscape": jsonEscape,
	}

	tmpl, err := template.New("webhook").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, event); err != nil {
		return fmt.Errorf("template execution failed: %w", err)
	}

	// Create request
	method := n.config.Webhook.Method
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequest(method, n.config.Webhook.URL, &body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.config.Webhook.Headers {
		req.Header.Set(k, v)
	}

	// Send request
	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// sendShell executes a shell command notification
func (n *Notifier) sendShell(event Event) error {
	cmdStr := n.config.Shell.Command

	// Expand ~ in path
	if strings.HasPrefix(cmdStr, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			cmdStr = filepath.Join(home, cmdStr[1:])
		}
	}

	cmd := exec.Command("sh", "-c", cmdStr)

	// Pass event as JSON via stdin if configured
	if n.config.Shell.PassJSON {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal event: %w", err)
		}
		cmd.Stdin = bytes.NewReader(eventJSON)
	}

	// Set environment variables
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NTM_EVENT_TYPE=%s", event.Type),
		fmt.Sprintf("NTM_EVENT_MESSAGE=%s", event.Message),
		fmt.Sprintf("NTM_EVENT_SESSION=%s", event.Session),
		fmt.Sprintf("NTM_EVENT_PANE=%s", event.Pane),
		fmt.Sprintf("NTM_EVENT_AGENT=%s", event.Agent),
	)

	return cmd.Run()
}

// sendLog appends to a log file
func (n *Notifier) sendLog(event Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	path := n.config.Log.Path
	// Expand ~ in path
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[1:])
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open file in append mode
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	// Format log line
	line := fmt.Sprintf("[%s] %s: %s",
		event.Timestamp.Format(time.RFC3339),
		event.Type,
		event.Message,
	)
	if event.Session != "" {
		line = fmt.Sprintf("[%s] [%s] %s: %s",
			event.Timestamp.Format(time.RFC3339),
			event.Session,
			event.Type,
			event.Message,
		)
	}

	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("failed to write to log: %w", err)
	}

	return nil
}

// sendFileBox creates a markdown file in the file inbox for offline human review
func (n *Notifier) sendFileBox(event Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	path := n.config.FileBox.Path
	if path == "" {
		path = ".ntm/human_inbox"
	}

	// Expand ~ in path
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[1:])
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create inbox directory: %w", err)
	}

	// Create filename with timestamp and event type
	filename := fmt.Sprintf("%s_%s.md",
		event.Timestamp.Format("2006-01-02_15-04-05"),
		strings.ReplaceAll(string(event.Type), ".", "_"),
	)
	filePath := filepath.Join(path, filename)

	// Format as markdown
	var content strings.Builder
	content.WriteString(fmt.Sprintf("# %s\n\n", event.Type))
	content.WriteString(fmt.Sprintf("**Time:** %s\n\n", event.Timestamp.Format(time.RFC3339)))

	if event.Session != "" {
		content.WriteString(fmt.Sprintf("**Session:** %s\n\n", event.Session))
	}
	if event.Agent != "" {
		content.WriteString(fmt.Sprintf("**Agent:** %s\n\n", event.Agent))
	}
	if event.Pane != "" {
		content.WriteString(fmt.Sprintf("**Pane:** %s\n\n", event.Pane))
	}

	content.WriteString("## Message\n\n")
	content.WriteString(event.Message)
	content.WriteString("\n")

	if len(event.Details) > 0 {
		content.WriteString("\n## Details\n\n")
		for k, v := range event.Details {
			content.WriteString(fmt.Sprintf("- **%s:** %s\n", k, v))
		}
	}

	content.WriteString("\n---\n")
	content.WriteString("*This notification was saved for offline review.*\n")

	// Write to file
	if err := os.WriteFile(filePath, []byte(content.String()), 0644); err != nil {
		return fmt.Errorf("failed to write inbox file: %w", err)
	}

	return nil
}

// Close closes any open resources.
// Currently a no-op as log files are opened/closed per write, but retained
// for future extensibility (e.g., cached file handles, persistent connections).
func (n *Notifier) Close() error {
	return nil
}

// Helper functions for creating common events

// NewAgentErrorEvent creates an agent error notification event
func NewAgentErrorEvent(session, pane, agent, message string) Event {
	return Event{
		Type:    EventAgentError,
		Session: session,
		Pane:    pane,
		Agent:   agent,
		Message: message,
	}
}

// NewAgentCrashedEvent creates an agent crashed notification event
func NewAgentCrashedEvent(session, pane, agent string) Event {
	return Event{
		Type:    EventAgentCrashed,
		Session: session,
		Pane:    pane,
		Agent:   agent,
		Message: fmt.Sprintf("Agent %s in pane %s crashed", agent, pane),
	}
}

// NewRateLimitEvent creates a rate limit notification event
func NewRateLimitEvent(session, pane, agent string, waitSeconds int) Event {
	return Event{
		Type:    EventRateLimit,
		Session: session,
		Pane:    pane,
		Agent:   agent,
		Message: fmt.Sprintf("Agent %s hit rate limit (wait %ds)", agent, waitSeconds),
		Details: map[string]string{
			"wait_seconds": fmt.Sprintf("%d", waitSeconds),
		},
	}
}

// NewRotationNeededEvent creates a rotation needed notification event
func NewRotationNeededEvent(session string, paneIndex int, agent, command string) Event {
	return Event{
		Type:    EventRotationNeeded,
		Session: session,
		Agent:   agent,
		Message: fmt.Sprintf("Rate limit hit! Run: %s", command),
		Details: map[string]string{
			"pane_index": fmt.Sprintf("%d", paneIndex),
			"command":    command,
		},
	}
}

// NewHealthDegradedEvent creates a health degraded notification event
func NewHealthDegradedEvent(session string, healthy, warning, error int) Event {
	return Event{
		Type:    EventHealthDegraded,
		Session: session,
		Message: fmt.Sprintf("Session health degraded: %d healthy, %d warning, %d error", healthy, warning, error),
		Details: map[string]string{
			"healthy": fmt.Sprintf("%d", healthy),
			"warning": fmt.Sprintf("%d", warning),
			"error":   fmt.Sprintf("%d", error),
		},
	}
}
