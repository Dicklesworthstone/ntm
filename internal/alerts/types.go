// Package alerts provides alert generation and tracking for NTM.
// Alerts surface problems proactively so orchestrators can react.
package alerts

import (
	"time"
)

// AlertType identifies the category of alert
type AlertType string

const (
	// AlertAgentStuck indicates no output from agent for configured duration
	AlertAgentStuck AlertType = "agent_stuck"
	// AlertAgentCrashed indicates the agent's pane process has exited
	AlertAgentCrashed AlertType = "agent_crashed"
	// AlertAgentError indicates an error state detected in agent output
	AlertAgentError AlertType = "agent_error"
	// AlertHighCPU indicates excessive CPU consumption (reserved for future)
	AlertHighCPU AlertType = "high_cpu"
	// AlertDiskLow indicates low disk space on the system
	AlertDiskLow AlertType = "disk_low"
	// AlertBeadStale indicates an in-progress bead with no recent activity
	AlertBeadStale AlertType = "bead_stale"
	// AlertMailBacklog indicates many unread messages for an agent
	AlertMailBacklog AlertType = "mail_backlog"
	// AlertDependencyCycle indicates a cycle in bead dependencies
	AlertDependencyCycle AlertType = "dependency_cycle"
	// AlertRateLimit indicates rate limiting detected in agent output
	AlertRateLimit AlertType = "rate_limit"
)

// Severity indicates the urgency of an alert
type Severity string

const (
	// SeverityInfo is for informational alerts
	SeverityInfo Severity = "info"
	// SeverityWarning is for conditions that may need attention
	SeverityWarning Severity = "warning"
	// SeverityError is for conditions that likely need intervention
	SeverityError Severity = "error"
	// SeverityCritical is for conditions requiring immediate action
	SeverityCritical Severity = "critical"
)

// Alert represents a detected problem condition
type Alert struct {
	// ID is a unique identifier for this alert instance
	ID string `json:"id"`
	// Type categorizes the alert
	Type AlertType `json:"type"`
	// Severity indicates urgency
	Severity Severity `json:"severity"`
	// Message is a human-readable description
	Message string `json:"message"`
	// Session is the tmux session this alert relates to (if applicable)
	Session string `json:"session,omitempty"`
	// Pane is the specific pane involved (if applicable)
	Pane string `json:"pane,omitempty"`
	// BeadID is the bead this alert relates to (if applicable)
	BeadID string `json:"bead_id,omitempty"`
	// Context provides additional structured data about the alert
	Context map[string]interface{} `json:"context,omitempty"`
	// CreatedAt is when the alert was first detected
	CreatedAt time.Time `json:"created_at"`
	// ResolvedAt is when the alert condition was no longer detected
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// LastSeenAt is when the condition was last observed (for ongoing alerts)
	LastSeenAt time.Time `json:"last_seen_at"`
	// Count tracks how many times this alert has been refreshed
	Count int `json:"count"`
}

// IsResolved returns true if the alert has been resolved
func (a *Alert) IsResolved() bool {
	return a.ResolvedAt != nil
}

// Duration returns how long the alert has been active
func (a *Alert) Duration() time.Duration {
	if a.ResolvedAt != nil {
		return a.ResolvedAt.Sub(a.CreatedAt)
	}
	return time.Since(a.CreatedAt)
}

// Config holds configuration for alert thresholds
type Config struct {
	// AgentStuckMinutes is how long without output before alerting
	AgentStuckMinutes int `toml:"agent_stuck_minutes" json:"agent_stuck_minutes"`
	// DiskLowThresholdGB is minimum free disk space before alerting
	DiskLowThresholdGB float64 `toml:"disk_low_threshold_gb" json:"disk_low_threshold_gb"`
	// MailBacklogThreshold is how many unread messages trigger an alert
	MailBacklogThreshold int `toml:"mail_backlog_threshold" json:"mail_backlog_threshold"`
	// BeadStaleHours is how long an in-progress bead can be inactive
	BeadStaleHours int `toml:"bead_stale_hours" json:"bead_stale_hours"`
	// ResolvedPruneMinutes is how long to keep resolved alerts
	ResolvedPruneMinutes int `toml:"resolved_prune_minutes" json:"resolved_prune_minutes"`
	// Enabled controls whether alert generation is active
	Enabled bool `toml:"enabled" json:"enabled"`
	// ProjectsDir is the base directory for projects (used for disk space check)
	ProjectsDir string `json:"projects_dir,omitempty"`
}

// DefaultConfig returns sensible default alert thresholds
func DefaultConfig() Config {
	return Config{
		AgentStuckMinutes:    5,
		DiskLowThresholdGB:   5.0,
		MailBacklogThreshold: 10,
		BeadStaleHours:       24,
		ResolvedPruneMinutes: 60,
		Enabled:              true,
	}
}

// AlertSummary provides aggregate statistics about alerts
type AlertSummary struct {
	TotalActive   int            `json:"total_active"`
	TotalResolved int            `json:"total_resolved"`
	BySeverity    map[string]int `json:"by_severity"`
	ByType        map[string]int `json:"by_type"`
}
