package webhook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// BusBridge subscribes to an events.EventBus and dispatches webhook-compatible
// events to a WebhookManager.
type BusBridge struct {
	session     string
	manager     *WebhookManager
	unsubscribe events.UnsubscribeFunc
}

// StartBridgeFromProjectConfig loads .ntm.yaml/.ntm.yml webhooks from projectDir,
// starts a WebhookManager, and subscribes it to the provided event bus.
//
// If no webhooks are configured for the project, it returns (nil, nil).
func StartBridgeFromProjectConfig(projectDir, session string, bus *events.EventBus, redactionCfg *redaction.Config) (*BusBridge, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, nil
	}
	if bus == nil {
		bus = events.DefaultBus
	}

	projectCfgs, err := config.LoadProjectWebhooks(projectDir)
	if err != nil {
		return nil, err
	}
	if len(projectCfgs) == 0 {
		return nil, nil
	}

	var mgr *WebhookManager
	if redactionCfg != nil && redactionCfg.Mode != redaction.ModeOff {
		cfgCopy := *redactionCfg
		mgr = NewManagerWithRedaction(DefaultManagerConfig(), cfgCopy)
	} else {
		mgr = NewManager(DefaultManagerConfig())
	}
	mgr.Logger = func(format string, args ...interface{}) {
		slog.Default().Debug("webhook", "msg", fmt.Sprintf(format, args...))
	}

	for _, c := range projectCfgs {
		wh, err := projectConfigToWebhookConfig(c)
		if err != nil {
			return nil, err
		}
		if err := mgr.Register(wh); err != nil {
			return nil, err
		}
	}

	if err := mgr.Start(); err != nil {
		return nil, err
	}

	unsub := bus.SubscribeAll(func(e events.BusEvent) {
		if strings.TrimSpace(session) != "" && e.EventSession() != session {
			return
		}

		ev, ok := toWebhookEvent(e)
		if !ok {
			return
		}

		// Dispatch is already non-blocking; ignore errors for best-effort delivery.
		_ = mgr.Dispatch(ev)
	})

	return &BusBridge{
		session:     session,
		manager:     mgr,
		unsubscribe: unsub,
	}, nil
}

func toWebhookEvent(e events.BusEvent) (Event, bool) {
	if e == nil {
		return Event{}, false
	}

	switch v := e.(type) {
	case events.WebhookEvent:
		return Event{
			Type:      v.Type,
			Timestamp: v.Timestamp,
			Session:   v.Session,
			Pane:      v.Pane,
			Agent:     v.Agent,
			Message:   v.Message,
			Details:   v.Details,
		}, true
	case *events.WebhookEvent:
		if v == nil {
			return Event{}, false
		}
		return Event{
			Type:      v.Type,
			Timestamp: v.Timestamp,
			Session:   v.Session,
			Pane:      v.Pane,
			Agent:     v.Agent,
			Message:   v.Message,
			Details:   v.Details,
		}, true
	default:
		// Fall through to best-effort conversion for well-known webhook event types.
	}

	if !isWebhookDispatchType(e.EventType()) {
		return Event{}, false
	}

	// Best-effort extraction: marshal to JSON and pick common fields.
	raw, err := json.Marshal(e)
	if err != nil {
		return Event{
			Type:      e.EventType(),
			Timestamp: e.EventTimestamp(),
			Session:   e.EventSession(),
			Message:   e.EventType(),
		}, true
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return Event{
			Type:      e.EventType(),
			Timestamp: e.EventTimestamp(),
			Session:   e.EventSession(),
			Message:   e.EventType(),
		}, true
	}

	pane := firstNonEmptyString(
		stringFromAny(m["pane"]),
		stringFromAny(m["agent_id"]),
	)

	message := stringFromAny(m["message"])
	if strings.TrimSpace(message) == "" {
		message = e.EventType()
	}

	details := make(map[string]string)
	if nested, ok := m["details"].(map[string]any); ok {
		for k, v := range nested {
			if k == "" {
				continue
			}
			details[k] = fmt.Sprint(v)
		}
	}
	for _, key := range []string{"agent_id", "prev_status", "new_status", "prev_type", "new_type"} {
		if v := stringFromAny(m[key]); strings.TrimSpace(v) != "" {
			details[key] = v
		}
	}

	agent := stringFromAny(m["agent"])
	if strings.TrimSpace(agent) == "" {
		agent = stringFromAny(m["agent_type"])
	}
	if strings.TrimSpace(agent) == "" {
		agent = details["agent_type"]
	}

	return Event{
		Type:      e.EventType(),
		Timestamp: e.EventTimestamp(),
		Session:   e.EventSession(),
		Pane:      pane,
		Agent:     agent,
		Message:   message,
		Details:   details,
	}, true
}

func isWebhookDispatchType(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case strings.ToLower(events.WebhookAgentError),
		strings.ToLower(events.WebhookAgentStarted),
		strings.ToLower(events.WebhookAgentStopped),
		strings.ToLower(events.WebhookAgentCrashed),
		strings.ToLower(events.WebhookAgentRestarted),
		strings.ToLower(events.WebhookAgentIdle),
		strings.ToLower(events.WebhookAgentBusy),
		strings.ToLower(events.WebhookAgentRateLimit),
		strings.ToLower(events.WebhookAgentCompleted),
		strings.ToLower(events.WebhookRotationNeeded),
		strings.ToLower(events.WebhookSessionCreated),
		strings.ToLower(events.WebhookSessionKilled),
		strings.ToLower(events.WebhookSessionEnded),
		strings.ToLower(events.WebhookBeadAssigned),
		strings.ToLower(events.WebhookBeadCompleted),
		strings.ToLower(events.WebhookBeadFailed),
		strings.ToLower(events.WebhookHealthDegraded):
		return true
	default:
		return false
	}
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func projectConfigToWebhookConfig(cfg config.WebhookConfig) (WebhookConfig, error) {
	out := WebhookConfig{
		ID:      stableWebhookID(cfg.Name),
		Name:    cfg.Name,
		URL:     cfg.URL,
		Events:  trimStrings(cfg.Events),
		Enabled: true,
		Format:  strings.TrimSpace(cfg.Formatter),
		Secret:  strings.TrimSpace(cfg.Secret),
	}

	if strings.TrimSpace(cfg.Timeout) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(cfg.Timeout))
		if err != nil {
			return WebhookConfig{}, err
		}
		out.Timeout = d
	}

	// Retry policy (best-effort mapping; backoff strategy is currently fixed inside manager).
	if cfg.Retry.MaxAttempts > 0 {
		out.Retry = RetryConfig{
			Enabled:    true,
			MaxRetries: cfg.Retry.MaxAttempts,
		}
	}

	return out, nil
}

func stableWebhookID(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("wh_")
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(s))
	b.WriteByte('_')
	b.WriteString(fmt.Sprintf("%x", sum[:4]))
	return b.String()
}

func trimStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// Close unsubscribes from the event bus and stops the underlying webhook manager.
func (b *BusBridge) Close() error {
	if b == nil {
		return nil
	}
	if b.unsubscribe != nil {
		b.unsubscribe()
	}
	if b.manager != nil {
		return b.manager.Stop()
	}
	return nil
}
