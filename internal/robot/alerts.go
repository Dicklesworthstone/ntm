// Package robot provides machine-readable output for AI agents.
// alerts.go implements the alerting system for health state changes (ntm-caib).
package robot

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
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

// Alerter manages alert delivery with debouncing
type Alerter struct {
	mu       sync.RWMutex
	config   AlerterConfig
	channels []AlertChannel

	// Debouncing: track last alert time per pane+type
	lastAlerts map[string]time.Time
}

// =============================================================================
// Desktop Channel
// =============================================================================

// DesktopChannel sends desktop notifications
type DesktopChannel struct {
	urgency string
}

// =============================================================================
// Webhook Channel
// =============================================================================

// WebhookChannel sends alerts via HTTP webhooks
type WebhookChannel struct {
	config     WebhookConfig
	baseDelay  time.Duration
	httpClient *http.Client
}

// Alert-delivery retry policy (WS6-wire, bd-ws6-config-truth-ienmd.1): the
// alert webhook retry loop is governed by the central [retry] policy via
// cfg.Retry.RetryPolicyFor("alerts") — globals plus the [retry.alerts]
// override. ApplyAlertRetryPolicy is invoked once per process after config
// load (internal/cli/root.go); compiled-in defaults preserve historical
// behavior exactly (3 retries after the first call, 1s base doubling).
var (
	alertRetryMu           sync.RWMutex
	alertRetryMaxRetries   = 3
	alertRetryInitialDelay = time.Second
)

// ApplyAlertRetryPolicy configures the alert webhook retry defaults from
// cfg.Retry.RetryPolicyFor("alerts"). Non-positive values keep the
// compiled-in defaults.
func ApplyAlertRetryPolicy(maxAttempts, initialDelayMs int) {
	alertRetryMu.Lock()
	defer alertRetryMu.Unlock()
	if maxAttempts > 0 {
		alertRetryMaxRetries = maxAttempts
	}
	if initialDelayMs > 0 {
		alertRetryInitialDelay = time.Duration(initialDelayMs) * time.Millisecond
	}
}

func init() {
	// G2 config-key liveness claims: this package reads the [retry.alerts]
	// override via ApplyAlertRetryPolicy at startup.
	config.RegisterReader("retry.alerts.max_attempts", ApplyAlertRetryPolicy)
	config.RegisterReader("retry.alerts.initial_delay_ms", ApplyAlertRetryPolicy)
}

// =============================================================================
// Log Channel
// =============================================================================

// LogChannel logs alerts to stderr as JSON
type LogChannel struct{}

// =============================================================================
// Global Alerter Registry
// =============================================================================

var (
	globalAlerter   *Alerter
	globalAlerterMu sync.RWMutex
)
