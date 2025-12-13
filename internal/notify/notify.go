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

	Desktop DesktopConfig `toml:"desktop"`
	Webhook WebhookConfig `toml:"webhook"`
	Shell   ShellConfig   `toml:"shell"`
	Log     LogConfig     `toml:"log"`
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

// DefaultConfig returns a default notification configuration
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Events:  []string{string(EventAgentError), string(EventAgentCrashed)},
		Desktop: DesktopConfig{
			Enabled: true,
			Title:   "NTM",
		},
		Webhook: WebhookConfig{
			Enabled:  false,
			Method:   "POST",
			Template: `{"text": "NTM: {{.Type}} - {{.Message}}"}`,
		},
		Shell: ShellConfig{
			Enabled:  false,
			PassJSON: true,
		},
		Log: LogConfig{
			Enabled: false,
			Path:    "~/.config/ntm/notifications.log",
		},
	}
}

// Notifier sends notifications through configured channels
type Notifier struct {
	config     Config
	enabledSet map[EventType]bool
	mu         sync.Mutex
	httpClient *http.Client
}

// New creates a new Notifier with the given configuration
func New(cfg Config) *Notifier {
	n := &Notifier{
		config:     cfg,
		enabledSet: make(map[EventType]bool),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	// Build set of enabled events
	for _, e := range cfg.Events {
		n.enabledSet[EventType(e)] = true
	}

	return n
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

	var (
		wg    sync.WaitGroup
		errs  []error
		errMu sync.Mutex
	)

	// Helper to collect errors safely
	addErr := func(err error) {
		if err != nil {
			errMu.Lock()
			errs = append(errs, err)
			errMu.Unlock()
		}
	}

	// Send through each enabled channel in parallel
	if n.config.Desktop.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendDesktop(event); err != nil {
				addErr(fmt.Errorf("desktop: %w", err))
			}
		}()
	}

	if n.config.Webhook.Enabled && n.config.Webhook.URL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendWebhook(event); err != nil {
				addErr(fmt.Errorf("webhook: %w", err))
			}
		}()
	}

	if n.config.Shell.Enabled && n.config.Shell.Command != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendShell(event); err != nil {
				addErr(fmt.Errorf("shell: %w", err))
			}
		}()
	}

	if n.config.Log.Enabled && n.config.Log.Path != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.sendLog(event); err != nil {
				addErr(fmt.Errorf("log: %w", err))
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
	cmd := exec.Command("notify-send", title, message)
	return cmd.Run()
}

// sendWebhook sends a webhook notification
func (n *Notifier) sendWebhook(event Event) error {
	// Parse and execute template
	tmplStr := n.config.Webhook.Template
	if tmplStr == "" {
		// Default JSON template
		tmplStr = `{"event":"{{.Type}}","message":"{{.Message}}","session":"{{.Session}}","timestamp":"{{.Timestamp}}"}`
	}

	tmpl, err := template.New("webhook").Parse(tmplStr)
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
