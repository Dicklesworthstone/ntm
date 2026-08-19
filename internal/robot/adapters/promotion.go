package adapters

import (
	"crypto/rand"
	"fmt"
	"time"
)

// IncidentItem represents an incident promoted from alerts
type IncidentItem struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Severity      string   `json:"severity"` // P0, P1, P2
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	Session       string   `json:"session,omitempty"`
	Panes         []string `json:"panes,omitempty"`
	Agents        []string `json:"agents,omitempty"`
	Status        string   `json:"status"` // investigating, mitigating, resolved
	AlertCount    int      `json:"alert_count,omitempty"`
	EventCount    int      `json:"event_count,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	ResolvedAt    string   `json:"resolved_at,omitempty"`
	DetectedBy    string   `json:"detected_by"`
	AssignedTo    string   `json:"assigned_to,omitempty"`
	Resolution    string   `json:"resolution,omitempty"`
	RootCause     string   `json:"root_cause,omitempty"`
	RelatedAlerts []string `json:"related_alerts,omitempty"`
	RelatedBeads  []string `json:"related_beads,omitempty"`
}

// PromotionRule defines when an alert should promote to an incident
type PromotionRule struct {
	// MinDuration is the minimum alert duration before promotion
	MinDuration time.Duration
	// MinSeverity is the minimum severity for promotion
	MinSeverity Severity
	// MinScope is the minimum number of affected agents
	MinScope int
	// Types are specific alert types that always promote
	Types []string
	// RepeatCount is the number of re-raises before promotion
	RepeatCount int
}

// DefaultPromotionRules returns the standard promotion rules
func DefaultPromotionRules() *PromotionRule {
	return &PromotionRule{
		MinDuration: 30 * time.Minute,
		MinSeverity: SeverityCritical,
		MinScope:    3,
		Types:       []string{"agent_crashed", "rotation_failed", "compaction_failed"},
		RepeatCount: 3,
	}
}

// ShouldPromote checks if an alert should be promoted to an incident
func ShouldPromote(alert AlertItem, rule *PromotionRule) (bool, string) {
	if rule == nil {
		rule = DefaultPromotionRules()
	}

	// Check severity
	alertSeverity := Severity(alert.Severity)
	if alertSeverity == SeverityCritical {
		return true, "critical_severity"
	}

	// Check duration
	if alert.DurationMs >= rule.MinDuration.Milliseconds() {
		return true, "duration_exceeded"
	}

	// Check specific types
	for _, t := range rule.Types {
		if alert.Type == t {
			return true, "type_match"
		}
	}

	// Check repeat count
	if alert.Count >= rule.RepeatCount {
		return true, "repeated_alert"
	}

	return false, ""
}

// PromoteToIncident creates an incident from a qualifying alert
func PromoteToIncident(alert AlertItem, reason string) *IncidentItem {
	now := time.Now()

	panes := []string{}
	if alert.Pane != "" {
		panes = append(panes, alert.Pane)
	}

	return &IncidentItem{
		ID:            GenerateIncidentID(),
		Type:          alert.Type,
		Severity:      alertToIncidentSeverity(alert.Severity),
		Title:         alert.Message,
		Description:   fmt.Sprintf("Promoted from alert %s: %s", alert.ID, reason),
		Session:       alert.Session,
		Panes:         panes,
		Status:        "investigating",
		AlertCount:    1,
		CreatedAt:     FormatTimestamp(now),
		UpdatedAt:     FormatTimestamp(now),
		DetectedBy:    "alert_promotion",
		RelatedAlerts: []string{alert.ID},
	}
}

func alertToIncidentSeverity(alertSeverity string) string {
	switch alertSeverity {
	case "critical":
		return "P0"
	case "error":
		return "P1"
	default:
		return "P2"
	}
}

// GenerateIncidentID creates a unique incident identifier
func GenerateIncidentID() string {
	now := time.Now().UTC()
	dateStr := now.Format("20060102")

	// Generate random suffix
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based suffix if crypto/rand fails
		ts := now.UnixNano()
		b[0] = byte(ts)
		b[1] = byte(ts >> 8)
		b[2] = byte(ts >> 16)
		b[3] = byte(ts >> 24)
	}

	return fmt.Sprintf("inc-%s-%x", dateStr, b)
}

// IncidentsSummary provides aggregate incident statistics
type IncidentsSummary struct {
	TotalActive int            `json:"total_active"`
	BySeverity  map[string]int `json:"by_severity"`
	ByType      map[string]int `json:"by_type"`
	ByStatus    map[string]int `json:"by_status"`
	MttrMinutes float64        `json:"mttr_minutes,omitempty"`
}
