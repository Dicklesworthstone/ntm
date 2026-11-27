// Package notify provides notification support for NTM events.
// Supports desktop notifications, webhooks, shell commands, and log files.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	defaultAlertRetryMaxRetries   = 3
	defaultAlertRetryInitialDelay = time.Second
	maxAlertWebhookRetryDelay     = 30 * time.Second
)

var (
	notificationCommandTimeout = 10 * time.Second

	// Alert/notification webhook retry policy governed by [retry] / [retry.alerts]
	// via ApplyAlertRetryPolicy (called after config load in internal/cli/policy_wiring.go).
	// Compiled-in defaults preserve historical retry semantics (3 retries after the
	// initial attempt with 1s initial backoff, doubling up to 30s).
	alertRetryMu           sync.RWMutex
	alertRetryMaxRetries   = defaultAlertRetryMaxRetries
	alertRetryInitialDelay = defaultAlertRetryInitialDelay
)

// ApplyAlertRetryPolicy configures the alert/notification webhook retry loop from
// cfg.Retry.RetryPolicyFor("alerts"). maxAttempts is the number of retries after
// the first request; initialDelayMs is the base delay in milliseconds (doubling
// per retry). Non-positive values restore the compiled-in defaults.
func ApplyAlertRetryPolicy(maxAttempts, initialDelayMs int) {
	alertRetryMu.Lock()
	defer alertRetryMu.Unlock()
	if maxAttempts > 0 {
		alertRetryMaxRetries = maxAttempts
	} else {
		alertRetryMaxRetries = defaultAlertRetryMaxRetries
	}
	const maxDelayMs = int(maxAlertWebhookRetryDelay / time.Millisecond)
	if initialDelayMs >= maxDelayMs {
		alertRetryInitialDelay = maxAlertWebhookRetryDelay
	} else if initialDelayMs > 0 {
		alertRetryInitialDelay = time.Duration(initialDelayMs) * time.Millisecond
	} else {
		alertRetryInitialDelay = defaultAlertRetryInitialDelay
	}
}

// AlertRetryPolicy returns the active alert webhook retry budget and initial backoff.
func AlertRetryPolicy() (maxRetries int, initialDelay time.Duration) {
	alertRetryMu.RLock()
	defer alertRetryMu.RUnlock()
	return alertRetryMaxRetries, alertRetryInitialDelay
}

// EventType represents the type of notification event
type EventType string

const (
	EventError          EventType = "error"            // Generic error event
	EventAgentStarted   EventType = "agent.started"    // Agent process started
	EventAgentError     EventType = "agent.error"      // Agent hit error state
	EventAgentCrashed   EventType = "agent.crashed"    // Agent process exited
	EventAgentRestarted EventType = "agent.restarted"  // Agent was auto-restarted
	EventAgentIdle      EventType = "agent.idle"       // Agent waiting for input
	EventRateLimit      EventType = "agent.rate_limit" // Agent hit rate limit
	EventBeadAssigned   EventType = "bead.assigned"    // Bead assigned to an agent
	EventBeadCompleted  EventType = "bead.completed"   // Bead completed by an agent
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
		Primary:  "",
		Fallback: "",
		Routing:  nil, // Use default (all enabled channels in parallel)
		Desktop: DesktopConfig{
			Enabled: true,
			Title:   "NTM",
		},
		Webhook: WebhookConfig{
			Enabled:  false,
			Method:   "POST",
			Template: `{"event":"{{jsonEscape (printf "%s" .Type)}}","message":"{{jsonEscape .Message}}","session":"{{jsonEscape .Session}}","pane":"{{jsonEscape .Pane}}","agent":"{{jsonEscape .Agent}}","agent_type":"{{jsonEscape .Agent}}","bead_id":"{{jsonEscape (detail .Details "bead_id")}}","timestamp":"{{rfc3339 .Timestamp}}","details":{{jsonMap .Details}}}`,
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

	redactionCfg *redaction.Config
}

// expandEnvVars expands environment variables in a string (${VAR} or $VAR format)
func expandEnvVars(s string) string {
	return os.ExpandEnv(s)
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneRoutingMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneConfig(cfg Config) Config {
	cloned := cfg
	cloned.Events = append([]string(nil), cfg.Events...)
	cloned.Routing = cloneRoutingMap(cfg.Routing)
	cloned.Webhook.Headers = cloneStringMap(cfg.Webhook.Headers)
	return cloned
}

// New creates a new Notifier with the given configuration
func New(cfg Config) *Notifier {
	cfg = cloneConfig(cfg)

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
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:               nil,
				DialContext:         safeNotifyDialContext(true),
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     60 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
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

// NewWithRedaction creates a notifier that redacts event content (message + details)
// before emitting it to any channel.
func NewWithRedaction(cfg Config, redactionCfg redaction.Config) *Notifier {
	n := New(cfg)
	cfgCopy := redactionCfg
	n.redactionCfg = &cfgCopy
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

	event = n.sanitizeEvent(event)

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

func (n *Notifier) sanitizeEvent(event Event) Event {
	if n.redactionCfg == nil || n.redactionCfg.Mode == redaction.ModeOff {
		return event
	}

	cfg := *n.redactionCfg
	if cfg.Mode != redaction.ModeOff {
		// Notifications/webhooks are an outbound channel; always redact content to
		// prevent accidental secret leakage regardless of "warn"/"block" modes.
		cfg.Mode = redaction.ModeRedact
	}

	out := event
	if out.Message != "" {
		out.Message = redaction.ScanAndRedact(out.Message, cfg).Output
	}
	if len(out.Details) > 0 {
		redactedDetails := make(map[string]string, len(out.Details))
		for k, v := range out.Details {
			if v == "" {
				redactedDetails[k] = v
				continue
			}
			redactedDetails[k] = redaction.ScanAndRedact(v, cfg).Output
		}
		out.Details = redactedDetails
	}
	return out
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
	ctx, cancel := context.WithTimeout(context.Background(), notificationCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("osascript timed out after %s", notificationCommandTimeout)
		}
		return err
	}
	return nil
}

// sendLinuxNotification sends a notification on Linux using notify-send
func sendLinuxNotification(title, message string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return fmt.Errorf("notify-send not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), notificationCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "notify-send", title, message)
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("notify-send timed out after %s", notificationCommandTimeout)
		}
		return err
	}
	return nil
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

func detailValue(details map[string]string, key string) string {
	if len(details) == 0 {
		return ""
	}
	return details[key]
}

func jsonMap(details map[string]string) string {
	if len(details) == 0 {
		return "{}"
	}
	b, err := json.Marshal(details)
	if err != nil {
		return "{}"
	}
	return string(b)
}

var (
	notifyBlockedMetadataIPs = []net.IP{
		net.ParseIP("fd00:ec2::254"),   // AWS IMDSv2 IPv6 endpoint
		net.ParseIP("100.100.100.200"), // Alibaba Cloud metadata endpoint
	}
	notifyCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")
)

func parseLegacyNotifyIPv4Literal(host string) (net.IP, bool) {
	if host == "" || strings.Contains(host, ":") {
		return nil, false
	}
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return nil, false
	}
	vals := make([]uint64, len(parts))
	for i, part := range parts {
		if part == "" || strings.Contains(part, "_") {
			return nil, false
		}
		var (
			v   uint64
			err error
		)
		switch {
		case strings.HasPrefix(part, "0x") || strings.HasPrefix(part, "0X"):
			if len(part) <= 2 {
				return nil, false
			}
			v, err = strconv.ParseUint(part[2:], 16, 64)
		case len(part) > 1 && part[0] == '0':
			v, err = strconv.ParseUint(part[1:], 8, 64)
		default:
			v, err = strconv.ParseUint(part, 10, 64)
		}
		if err != nil {
			return nil, false
		}
		vals[i] = v
	}
	var ipUint uint32
	switch len(vals) {
	case 1:
		if vals[0] > 0xFFFFFFFF {
			return nil, false
		}
		ipUint = uint32(vals[0])
	case 2:
		if vals[0] > 0xFF || vals[1] > 0xFFFFFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | vals[1])
	case 3:
		if vals[0] > 0xFF || vals[1] > 0xFF || vals[2] > 0xFFFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | (vals[1] << 16) | vals[2])
	case 4:
		if vals[0] > 0xFF || vals[1] > 0xFF || vals[2] > 0xFF || vals[3] > 0xFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | (vals[1] << 16) | (vals[2] << 8) | vals[3])
	}
	return net.IPv4(byte(ipUint>>24), byte(ipUint>>16), byte(ipUint>>8), byte(ipUint)), true
}

func validateNotifyWebhookURL(rawURL string, allowLoopback bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported webhook URL scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("webhook URL missing host")
	}
	lowerHost := strings.TrimRight(strings.ToLower(host), ".")
	if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" {
		return fmt.Errorf("webhook target %q blocked by SSRF policy", host)
	}
	if !allowLoopback && (lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost")) {
		return fmt.Errorf("webhook loopback target %q blocked by SSRF policy", host)
	}
	if addr, err := netip.ParseAddr(lowerHost); err == nil {
		if err := validateNotifyWebhookIP(net.IP(addr.AsSlice()), allowLoopback); err != nil {
			return err
		}
	} else if ip := net.ParseIP(lowerHost); ip != nil {
		if err := validateNotifyWebhookIP(ip, allowLoopback); err != nil {
			return err
		}
	} else if _, ok := parseLegacyNotifyIPv4Literal(lowerHost); ok {
		return fmt.Errorf("webhook non-canonical IPv4 literal %q blocked by SSRF policy", host)
	}
	return nil
}

func validateNotifyWebhookIP(ip net.IP, allowLoopback bool) error {
	if addr, ok := netip.AddrFromSlice(ip); ok {
		addr = addr.Unmap()
		if addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
			addr.IsMulticast() || addr.IsPrivate() || notifyCGNATPrefix.Contains(addr) {
			return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
		}
		if !allowLoopback && addr.IsLoopback() {
			return fmt.Errorf("webhook loopback IP %s blocked by SSRF policy", ip.String())
		}
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
	}
	for _, blocked := range notifyBlockedMetadataIPs {
		if blocked != nil && ip.Equal(blocked) {
			return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
		}
	}
	if !allowLoopback && ip.IsLoopback() {
		return fmt.Errorf("webhook loopback IP %s blocked by SSRF policy", ip.String())
	}
	return nil
}

func safeNotifyDialContext(allowLoopback bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return func(dctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(dctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses resolved for webhook host %q", host)
		}
		for _, ipAddr := range ips {
			if err := validateNotifyWebhookIP(ipAddr.IP, allowLoopback); err != nil {
				return nil, err
			}
		}
		var lastErr error
		for _, ipAddr := range ips {
			conn, dialErr := dialer.DialContext(dctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// sendWebhook sends a webhook notification
func (n *Notifier) sendWebhook(event Event) error {
	webhookURL := strings.TrimSpace(n.config.Webhook.URL)
	if err := validateNotifyWebhookURL(webhookURL, true); err != nil {
		return err
	}

	// Parse and execute template with JSON escape function
	tmplStr := n.config.Webhook.Template
	if tmplStr == "" {
		// Default JSON template using jsonEscape for user-controlled fields
		tmplStr = `{"event":"{{jsonEscape (printf "%s" .Type)}}","message":"{{jsonEscape .Message}}","session":"{{jsonEscape .Session}}","pane":"{{jsonEscape .Pane}}","agent":"{{jsonEscape .Agent}}","agent_type":"{{jsonEscape .Agent}}","bead_id":"{{jsonEscape (detail .Details "bead_id")}}","timestamp":"{{rfc3339 .Timestamp}}","details":{{jsonMap .Details}}}`
	}

	funcMap := template.FuncMap{
		"jsonEscape": jsonEscape,
		"detail":     detailValue,
		"jsonMap":    jsonMap,
		"rfc3339": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
	}

	tmpl, err := template.New("webhook").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, event); err != nil {
		return fmt.Errorf("template execution failed: %w", err)
	}

	// Create request template inputs once so retries replay the exact body.
	method := n.config.Webhook.Method
	if method == "" {
		method = "POST"
	}
	payloadBytes := body.Bytes()

	// Send request (never follow redirects that could forward credentials or hit metadata IPs)
	clientCopy := *n.httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	maxRetries, initialDelay := AlertRetryPolicy()
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := initialDelay
			if delay > maxAlertWebhookRetryDelay {
				delay = maxAlertWebhookRetryDelay
			}
			for step := 1; step < attempt && delay < maxAlertWebhookRetryDelay; step++ {
				delay *= 2
				if delay > maxAlertWebhookRetryDelay {
					delay = maxAlertWebhookRetryDelay
				}
			}
			time.Sleep(delay)
		}

		req, err := http.NewRequest(method, webhookURL, bytes.NewReader(payloadBytes))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range n.config.Webhook.Headers {
			req.Header.Set(k, v)
		}

		resp, err := clientCopy.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			if isRetryableNotifyError(err) && attempt < maxRetries {
				continue
			}
			return lastErr
		}

		if resp.StatusCode < 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		lastErr = fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(respBody))
		if isRetryableNotifyStatus(resp.StatusCode) && attempt < maxRetries {
			continue
		}
		return lastErr
	}

	return lastErr
}

func isRetryableNotifyStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

func isRetryableNotifyError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "blocked by SSRF policy") {
		return false
	}
	return true
}

// sendShell executes a shell command notification
func (n *Notifier) sendShell(event Event) error {
	cmdStr := n.config.Shell.Command
	cmdStr = util.ExpandPath(cmdStr)

	ctx, cancel := context.WithTimeout(context.Background(), notificationCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)

	// Pass event as JSON via stdin if configured
	if n.config.Shell.PassJSON {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("failed to marshal event: %w", err)
		}
		cmd.Stdin = bytes.NewReader(eventJSON)
	}

	// Set environment variables (sanitize newlines to prevent shell confusion)
	sanitize := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	}
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NTM_EVENT_TYPE=%s", sanitize(string(event.Type))),
		fmt.Sprintf("NTM_EVENT_MESSAGE=%s", sanitize(event.Message)),
		fmt.Sprintf("NTM_EVENT_SESSION=%s", sanitize(event.Session)),
		fmt.Sprintf("NTM_EVENT_PANE=%s", sanitize(event.Pane)),
		fmt.Sprintf("NTM_EVENT_AGENT=%s", sanitize(event.Agent)),
	)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("shell notification timed out after %s", notificationCommandTimeout)
		}
		return err
	}
	return nil
}

// sendLog appends to a log file
func (n *Notifier) sendLog(event Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	path := n.config.Log.Path
	path = util.ExpandPath(path)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Open file in append mode
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
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
	path = util.ExpandPath(path)

	// Ensure directory exists
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("failed to create inbox directory: %w", err)
	}

	// Create filename with timestamp and event type (ensure unique file per event).
	baseName := fmt.Sprintf("%s_%s",
		event.Timestamp.Format("2006-01-02_15-04-05"),
		util.SanitizeFilename(string(event.Type)),
	)
	basePath := filepath.Join(path, baseName+".md")
	var file *os.File
	for i := 0; i < 1000; i++ {
		target := basePath
		if i > 0 {
			target = filepath.Join(path, fmt.Sprintf("%s_%d.md", baseName, i))
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("failed to create inbox file: %w", err)
		}
		file = f
		break
	}
	if file == nil {
		return fmt.Errorf("failed to allocate unique inbox file for event %s", event.Type)
	}
	defer file.Close()

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
	if _, err := file.WriteString(content.String()); err != nil {
		return fmt.Errorf("failed to write inbox file: %w", err)
	}

	return nil
}

// Close closes any open resources.
// Log and filebox writes open/close per operation, but webhook delivery owns an
// http.Client and may retain idle keepalive connections.
func (n *Notifier) Close() error {
	if n.httpClient != nil {
		n.httpClient.CloseIdleConnections()
	}
	return nil
}

// Helper functions for creating common events

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
