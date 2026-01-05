// Package robot provides machine-readable output for AI agents.
// alerts.go implements the alerting system for health state changes (ntm-caib).
package robot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// AlertType categorizes alert events
type AlertType string

const (
	AlertUnhealthy     AlertType = "unhealthy"
	AlertDegraded      AlertType = "degraded"
	AlertRateLimited   AlertType = "rate_limited"
	AlertRestart       AlertType = "restart"
	AlertRestartFailed AlertType = "restart_failed"
	AlertMaxRestarts   AlertType = "max_restarts"
	AlertRecovered     AlertType = "recovered"
)

// Alert represents a single alert event
type Alert struct {
	Timestamp   time.Time              `json:"timestamp"`
	Type        AlertType              `json:"type"`
	Session     string                 `json:"session"`
	PaneID      string                 `json:"pane_id"`
	AgentType   string                 `json:"agent_type"`
	PrevState   HealthState            `json:"prev_state,omitempty"`
	NewState    HealthState            `json:"new_state,omitempty"`
	Message     string                 `json:"message"`
	Suggestion  string                 `json:"suggestion,omitempty"`
	ContextLoss bool                   `json:"context_loss,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// AlertChannel is the interface for alert delivery mechanisms
type AlertChannel interface {
	Name() string
	Send(ctx context.Context, alert *Alert) error
	Available() bool
}

// AlerterConfig configures the alerting system
type AlerterConfig struct {
	Enabled          bool          `json:"enabled"`
	DebounceInterval time.Duration `json:"debounce_interval"` // Min interval between same alerts
	AlertOn          []AlertType   `json:"alert_on"`          // Which events to alert on

	// Desktop settings
	DesktopEnabled bool   `json:"desktop_enabled"`
	DesktopUrgency string `json:"desktop_urgency"` // low, normal, critical

	// Webhook settings
	Webhooks []WebhookConfig `json:"webhooks"`

	// Logging
	LogToStderr bool `json:"log_to_stderr"`
}

// WebhookConfig configures a single webhook
type WebhookConfig struct {
	URL        string      `json:"url"`
	Events     []AlertType `json:"events"` // Empty means all events
	MaxRetries int         `json:"max_retries"`
	Timeout    time.Duration
}

// DefaultAlerterConfig returns sensible defaults
func DefaultAlerterConfig() AlerterConfig {
	return AlerterConfig{
		Enabled:          true,
		DebounceInterval: 60 * time.Second,
		AlertOn: []AlertType{
			AlertUnhealthy,
			AlertRateLimited,
			AlertRestart,
			AlertRestartFailed,
			AlertMaxRestarts,
		},
		DesktopEnabled: true,
		DesktopUrgency: "normal",
		LogToStderr:    true,
		Webhooks:       []WebhookConfig{},
	}
}

// Alerter manages alert delivery with debouncing
type Alerter struct {
	mu       sync.RWMutex
	config   AlerterConfig
	channels []AlertChannel

	// Debouncing: track last alert time per pane+type
	lastAlerts map[string]time.Time
}

// NewAlerter creates a new alerter with the given configuration
func NewAlerter(config *AlerterConfig) *Alerter {
	cfg := DefaultAlerterConfig()
	if config != nil {
		cfg = *config
	}

	a := &Alerter{
		config:     cfg,
		channels:   []AlertChannel{},
		lastAlerts: make(map[string]time.Time),
	}

	// Add enabled channels
	if cfg.DesktopEnabled {
		a.channels = append(a.channels, NewDesktopChannel(cfg.DesktopUrgency))
	}

	if cfg.LogToStderr {
		a.channels = append(a.channels, &LogChannel{})
	}

	for _, wc := range cfg.Webhooks {
		a.channels = append(a.channels, NewWebhookChannel(wc))
	}

	return a
}

// shouldAlert checks if the alert type is configured to fire
func (a *Alerter) shouldAlert(alertType AlertType) bool {
	if !a.config.Enabled {
		return false
	}

	for _, t := range a.config.AlertOn {
		if t == alertType {
			return true
		}
	}
	return false
}

// isDebounced checks if an alert should be suppressed due to debouncing
func (a *Alerter) isDebounced(paneID string, alertType AlertType) bool {
	key := fmt.Sprintf("%s:%s", paneID, alertType)

	a.mu.RLock()
	lastTime, ok := a.lastAlerts[key]
	a.mu.RUnlock()

	if !ok {
		return false
	}

	return time.Since(lastTime) < a.config.DebounceInterval
}

// recordAlert updates the debounce tracking
func (a *Alerter) recordAlert(paneID string, alertType AlertType) {
	key := fmt.Sprintf("%s:%s", paneID, alertType)

	a.mu.Lock()
	a.lastAlerts[key] = time.Now()
	a.mu.Unlock()
}

// Send dispatches an alert to all configured channels
func (a *Alerter) Send(ctx context.Context, alert *Alert) error {
	if !a.shouldAlert(alert.Type) {
		return nil
	}

	if a.isDebounced(alert.PaneID, alert.Type) {
		return nil // Suppressed by debouncing
	}

	a.recordAlert(alert.PaneID, alert.Type)

	var errs []error
	for _, ch := range a.channels {
		if !ch.Available() {
			continue
		}
		if err := ch.Send(ctx, alert); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.Name(), err))
		}
	}

	if len(errs) > 0 {
		// Return first error but all were attempted
		return errs[0]
	}
	return nil
}

// SendStateChange creates and sends an alert for a health state change
func (a *Alerter) SendStateChange(ctx context.Context, session, paneID, agentType string, prevState, newState HealthState, reason string) error {
	var alertType AlertType
	switch newState {
	case HealthUnhealthy:
		alertType = AlertUnhealthy
	case HealthDegraded:
		alertType = AlertDegraded
	case HealthRateLimited:
		alertType = AlertRateLimited
	case HealthHealthy:
		if prevState != HealthHealthy {
			alertType = AlertRecovered
		} else {
			return nil // No alert for healthy->healthy
		}
	default:
		return nil
	}

	alert := &Alert{
		Timestamp:  time.Now().UTC(),
		Type:       alertType,
		Session:    session,
		PaneID:     paneID,
		AgentType:  agentType,
		PrevState:  prevState,
		NewState:   newState,
		Message:    fmt.Sprintf("Agent %s in %s: %s -> %s", agentType, session, prevState, newState),
		Suggestion: getSuggestion(alertType),
	}

	if reason != "" {
		alert.Metadata = map[string]interface{}{"reason": reason}
	}

	return a.Send(ctx, alert)
}

// SendRestart sends an alert for agent restart events
func (a *Alerter) SendRestart(ctx context.Context, session, paneID, agentType string, contextLoss bool, success bool) error {
	alertType := AlertRestart
	if !success {
		alertType = AlertRestartFailed
	}

	alert := &Alert{
		Timestamp:   time.Now().UTC(),
		Type:        alertType,
		Session:     session,
		PaneID:      paneID,
		AgentType:   agentType,
		ContextLoss: contextLoss,
		Message:     fmt.Sprintf("Agent %s in %s restarted", agentType, session),
		Suggestion:  getSuggestion(alertType),
	}

	if contextLoss {
		alert.Message += " (context lost)"
		alert.Suggestion = "Agent lost its conversation context. You may need to re-explain the task."
	}

	return a.Send(ctx, alert)
}

// SendMaxRestarts sends an alert when restart limit is reached
func (a *Alerter) SendMaxRestarts(ctx context.Context, session, paneID, agentType string, restartCount int) error {
	alert := &Alert{
		Timestamp:  time.Now().UTC(),
		Type:       AlertMaxRestarts,
		Session:    session,
		PaneID:     paneID,
		AgentType:  agentType,
		Message:    fmt.Sprintf("Agent %s in %s exceeded max restarts (%d)", agentType, session, restartCount),
		Suggestion: "Agent is unstable. Consider killing and respawning, or investigating the underlying issue.",
		Metadata:   map[string]interface{}{"restart_count": restartCount},
	}

	return a.Send(ctx, alert)
}

// ClearDebounce clears debounce state for a pane (useful after restart)
func (a *Alerter) ClearDebounce(paneID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Clear all alert types for this pane
	for key := range a.lastAlerts {
		if len(key) > len(paneID) && key[:len(paneID)+1] == paneID+":" {
			delete(a.lastAlerts, key)
		}
	}
}

// getSuggestion returns a helpful suggestion for the alert type
func getSuggestion(alertType AlertType) string {
	switch alertType {
	case AlertUnhealthy:
		return "Check agent logs. May need restart or intervention."
	case AlertDegraded:
		return "Agent is slow but working. Monitor for improvement."
	case AlertRateLimited:
		return "Agent hit API rate limits. Will auto-backoff."
	case AlertRestart:
		return "Agent was restarted automatically."
	case AlertRestartFailed:
		return "Automatic restart failed. Manual intervention needed."
	case AlertMaxRestarts:
		return "Too many restarts. Check for underlying issues."
	case AlertRecovered:
		return "Agent is healthy again."
	default:
		return ""
	}
}

// =============================================================================
// Desktop Channel
// =============================================================================

// DesktopChannel sends desktop notifications
type DesktopChannel struct {
	urgency string
}

// NewDesktopChannel creates a desktop notification channel
func NewDesktopChannel(urgency string) *DesktopChannel {
	if urgency == "" {
		urgency = "normal"
	}
	return &DesktopChannel{urgency: urgency}
}

func (d *DesktopChannel) Name() string { return "desktop" }

func (d *DesktopChannel) Available() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	default:
		return false
	}
}

func (d *DesktopChannel) Send(ctx context.Context, alert *Alert) error {
	title := fmt.Sprintf("NTM: %s", alert.Type)
	body := alert.Message

	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, escapeAppleScript(body), escapeAppleScript(title))
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		return cmd.Run()

	case "linux":
		urgencyFlag := d.urgency
		if urgencyFlag == "" {
			urgencyFlag = "normal"
		}
		cmd := exec.CommandContext(ctx, "notify-send", "-u", urgencyFlag, title, body)
		return cmd.Run()

	default:
		return fmt.Errorf("desktop notifications not supported on %s", runtime.GOOS)
	}
}

// escapeAppleScript escapes a string for use in AppleScript
func escapeAppleScript(s string) string {
	// Escape backslashes and quotes
	result := ""
	for _, c := range s {
		switch c {
		case '\\':
			result += "\\\\"
		case '"':
			result += "\\\""
		case '\n':
			result += "\\n"
		default:
			result += string(c)
		}
	}
	return result
}

// =============================================================================
// Webhook Channel
// =============================================================================

// WebhookChannel sends alerts via HTTP webhooks
type WebhookChannel struct {
	config     WebhookConfig
	httpClient *http.Client
}

// NewWebhookChannel creates a webhook notification channel
func NewWebhookChannel(config WebhookConfig) *WebhookChannel {
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}

	return &WebhookChannel{
		config: config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

func (w *WebhookChannel) Name() string { return "webhook:" + w.config.URL }

func (w *WebhookChannel) Available() bool {
	return w.config.URL != ""
}

func (w *WebhookChannel) Send(ctx context.Context, alert *Alert) error {
	// Check if this webhook handles this event type
	if len(w.config.Events) > 0 {
		found := false
		for _, e := range w.config.Events {
			if e == alert.Type {
				found = true
				break
			}
		}
		if !found {
			return nil // This webhook doesn't handle this event
		}
	}

	payload, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("marshal alert: %w", err)
	}

	// Retry with exponential backoff
	var lastErr error
	for attempt := 0; attempt <= w.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Backoff: 1s, 2s, 4s, ...
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", w.config.URL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "ntm-alerter/1.0")

		resp, err := w.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil // Success
		}

		lastErr = fmt.Errorf("webhook returned status %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			// Don't retry client errors
			return lastErr
		}
	}

	return fmt.Errorf("webhook failed after %d attempts: %w", w.config.MaxRetries+1, lastErr)
}

// =============================================================================
// Log Channel
// =============================================================================

// LogChannel logs alerts to stderr as JSON
type LogChannel struct{}

func (l *LogChannel) Name() string    { return "log" }
func (l *LogChannel) Available() bool { return true }

func (l *LogChannel) Send(ctx context.Context, alert *Alert) error {
	payload, err := json.Marshal(alert)
	if err != nil {
		return err
	}

	// Write directly to stderr to avoid modifying global log state
	// which would be a race condition with other log users
	fmt.Fprintf(os.Stderr, "[ALERT] %s\n", string(payload))
	return nil
}

// =============================================================================
// Global Alerter Registry
// =============================================================================

var (
	globalAlerter   *Alerter
	globalAlerterMu sync.RWMutex
)

// GetAlerter returns the global alerter, creating one if needed
func GetAlerter() *Alerter {
	globalAlerterMu.RLock()
	a := globalAlerter
	globalAlerterMu.RUnlock()

	if a != nil {
		return a
	}

	globalAlerterMu.Lock()
	defer globalAlerterMu.Unlock()

	if globalAlerter != nil {
		return globalAlerter
	}

	globalAlerter = NewAlerter(nil)
	return globalAlerter
}

// SetGlobalAlerter sets the global alerter (useful for testing)
func SetGlobalAlerter(a *Alerter) {
	globalAlerterMu.Lock()
	defer globalAlerterMu.Unlock()
	globalAlerter = a
}
