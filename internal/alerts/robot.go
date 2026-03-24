package alerts

import (
	"encoding/json"
	"os"
	"time"
)

// AlertsOutput provides machine-readable alert information
type AlertsOutput struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Active      []Alert      `json:"active"`
	Resolved    []Alert      `json:"resolved,omitempty"`
	Summary     AlertSummary `json:"summary"`
	Config      Config       `json:"config"`
}

// GenerateAndTrack generates new alerts and updates the tracker
func GenerateAndTrack(cfg Config) *Tracker {
	tracker := GetGlobalTracker()
	SetGlobalTrackerConfig(cfg)

	generator := NewGenerator(cfg)
	detected, failed := generator.GenerateAll()
	tracker.Update(detected, failed)

	return tracker
}

// PrintAlerts outputs all alerts in JSON format
func PrintAlerts(cfg Config, includeResolved bool) error {
	tracker := GenerateAndTrack(cfg)

	active, resolved := tracker.GetAll()

	output := AlertsOutput{
		GeneratedAt: time.Now().UTC(),
		Active:      active,
		Summary:     tracker.Summary(),
		Config:      cfg,
	}

	if includeResolved {
		output.Resolved = resolved
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

// GetActiveAlerts returns all currently active alerts
func GetActiveAlerts(cfg Config) []Alert {
	tracker := GenerateAndTrack(cfg)
	return tracker.GetActive()
}

// GetAlertStrings returns active alerts as simple string messages
// This is useful for integration with existing code that expects []string
func GetAlertStrings(cfg Config) []string {
	alerts := GetActiveAlerts(cfg)
	messages := make([]string, len(alerts))
	for i, alert := range alerts {
		messages[i] = formatAlertString(alert)
	}
	return messages
}

func formatAlertString(alert Alert) string {
	msg := alert.Message
	if alert.Session != "" {
		msg = alert.Session + ": " + msg
	}
	if alert.Pane != "" {
		msg = msg + " (pane " + alert.Pane + ")"
	}
	return msg
}

// ToConfigAlerts converts config.AlertsConfig to alerts.Config
func ToConfigAlerts(enabled bool, agentStuckMinutes int, diskLowThresholdGB float64, mailBacklogThreshold, beadStaleHours int, contextWarningThreshold float64, resolvedPruneMinutes int, projectsDir string) Config {
	return Config{
		Enabled:                 enabled,
		AgentStuckMinutes:       agentStuckMinutes,
		DiskLowThresholdGB:      diskLowThresholdGB,
		MailBacklogThreshold:    mailBacklogThreshold,
		BeadStaleHours:          beadStaleHours,
		ContextWarningThreshold: contextWarningThreshold,
		ResolvedPruneMinutes:    resolvedPruneMinutes,
		ProjectsDir:             projectsDir,
	}
}
