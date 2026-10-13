package webhook

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// EventFilter narrows a webhook's event subscriptions. Dimensions are ANDed;
// values within either allowlist are ORed. An absent dimension is unrestricted.
// Agent filters require known provider metadata, not an inferred identity name.
type EventFilter struct {
	Session   string   `toml:"session" json:"session,omitempty"`
	AgentType []string `toml:"agent_type" json:"agent_type,omitempty"`
	Severity  []string `toml:"severity" json:"severity,omitempty"`
}

// normalizeEventFilter takes ownership of no caller memory. Canonical ordering
// keeps durable fingerprints stable for equivalent aliases and allowlist order.
// Empty filters remain nil so adding this feature does not invalidate existing
// unfiltered receipts. Invalid restrictions must not turn into an empty allowlist.
func normalizeEventFilter(in *EventFilter) (*EventFilter, error) {
	if in == nil {
		return nil, nil
	}
	out := &EventFilter{Session: strings.TrimSpace(in.Session)}
	if _, err := path.Match(out.Session, ""); err != nil {
		return nil, fmt.Errorf("invalid webhook filter.session: %w", err)
	}
	for _, value := range in.AgentType {
		canonical := webhookAgentType(value)
		if canonical == "" {
			return nil, fmt.Errorf("invalid webhook filter.agent_type %q", value)
		}
		out.AgentType = append(out.AgentType, canonical)
	}
	for _, value := range in.Severity {
		canonical := strings.ToLower(strings.TrimSpace(value))
		if canonical == "warn" {
			canonical = string(severityWarning)
		}
		switch eventSeverity(canonical) {
		case severityInfo, severitySuccess, severityWarning, severityError:
		default:
			return nil, fmt.Errorf("invalid webhook filter.severity %q", value)
		}
		out.Severity = append(out.Severity, canonical)
	}
	slices.Sort(out.AgentType)
	out.AgentType = slices.Compact(out.AgentType)
	slices.Sort(out.Severity)
	out.Severity = slices.Compact(out.Severity)
	if out.Session == "" && len(out.AgentType) == 0 && len(out.Severity) == 0 {
		return nil, nil
	}
	return out, nil
}

func webhookAgentType(value string) string {
	t := agent.AgentType(value).Canonical()
	if !t.IsValid() || t == agent.AgentTypeUser {
		return ""
	}
	return string(t)
}

// eventAgentType prefers explicit metadata, including an explicit unknown.
// Only legacy events lacking it may use Agent as a provider alias. In particular
// names such as BlueFox, cc_1, and a pane title never imply a provider.
func eventAgentType(event Event) string {
	if strings.TrimSpace(event.AgentType) != "" {
		return webhookAgentType(event.AgentType)
	}
	if value := strings.TrimSpace(event.Details["agent_type"]); value != "" {
		return webhookAgentType(value)
	}
	return webhookAgentType(event.Agent)
}

// matchesWebhook is shared by fresh admission, durable replay and operator
// recovery. Severity uses the same event-type classification as the built-in
// formatters; arbitrary message text or details cannot relabel an event.
func (m *WebhookManager) matchesWebhook(wh *WebhookConfig, event Event) bool {
	if !m.matchesEvent(wh, event.Type) {
		return false
	}
	filter := wh.Filter
	if filter == nil {
		return true
	}
	if filter.Session != "" {
		matched, err := path.Match(filter.Session, event.Session)
		if err != nil || !matched {
			return false
		}
	}
	if len(filter.AgentType) > 0 && !slices.Contains(filter.AgentType, eventAgentType(event)) {
		return false
	}
	return len(filter.Severity) == 0 || slices.Contains(filter.Severity, string(classifySeverity(event.Type)))
}
