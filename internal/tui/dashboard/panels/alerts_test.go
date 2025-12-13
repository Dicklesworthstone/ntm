package panels

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
)

func TestNewAlertsPanel(t *testing.T) {
	panel := NewAlertsPanel()
	if panel == nil {
		t.Fatal("NewAlertsPanel returned nil")
	}
}

func TestAlertsPanelConfig(t *testing.T) {
	panel := NewAlertsPanel()
	cfg := panel.Config()

	if cfg.ID != "alerts" {
		t.Errorf("expected ID 'alerts', got %q", cfg.ID)
	}
	if cfg.Title != "Active Alerts" {
		t.Errorf("expected Title 'Active Alerts', got %q", cfg.Title)
	}
	if cfg.Priority != PriorityCritical {
		t.Errorf("expected PriorityCritical, got %v", cfg.Priority)
	}
	if cfg.RefreshInterval != 3*time.Second {
		t.Errorf("expected 3s refresh, got %v", cfg.RefreshInterval)
	}
	if cfg.Collapsible {
		t.Error("expected Collapsible to be false for alerts")
	}
}

func TestAlertsPanelSetSize(t *testing.T) {
	panel := NewAlertsPanel()
	panel.SetSize(80, 20)

	if panel.Width() != 80 {
		t.Errorf("expected width 80, got %d", panel.Width())
	}
	if panel.Height() != 20 {
		t.Errorf("expected height 20, got %d", panel.Height())
	}
}

func TestAlertsPanelFocusBlur(t *testing.T) {
	panel := NewAlertsPanel()

	panel.Focus()
	if !panel.IsFocused() {
		t.Error("expected IsFocused to be true after Focus()")
	}

	panel.Blur()
	if panel.IsFocused() {
		t.Error("expected IsFocused to be false after Blur()")
	}
}

func TestAlertsPanelSetData(t *testing.T) {
	panel := NewAlertsPanel()

	testAlerts := []alerts.Alert{
		{Severity: alerts.SeverityCritical, Message: "Critical error"},
		{Severity: alerts.SeverityWarning, Message: "Warning message"},
	}

	panel.SetData(testAlerts, nil)

	if len(panel.alerts) != 2 {
		t.Errorf("expected 2 alerts, got %d", len(panel.alerts))
	}
}

func TestAlertsPanelKeybindings(t *testing.T) {
	panel := NewAlertsPanel()
	bindings := panel.Keybindings()

	if len(bindings) == 0 {
		t.Error("expected non-empty keybindings")
	}

	// Check for expected actions
	actions := make(map[string]bool)
	for _, b := range bindings {
		actions[b.Action] = true
	}

	if !actions["dismiss"] {
		t.Error("expected 'dismiss' action in keybindings")
	}
	if !actions["ack_all"] {
		t.Error("expected 'ack_all' action in keybindings")
	}
}

func TestAlertsPanelInit(t *testing.T) {
	panel := NewAlertsPanel()
	cmd := panel.Init()
	if cmd != nil {
		t.Error("expected Init() to return nil")
	}
}

func TestAlertsPanelViewZeroWidth(t *testing.T) {
	panel := NewAlertsPanel()
	panel.SetSize(0, 10)

	view := panel.View()
	if view != "" {
		t.Errorf("expected empty view for zero width, got: %s", view)
	}
}

func TestAlertsPanelViewNoAlerts(t *testing.T) {
	panel := NewAlertsPanel()
	panel.SetSize(80, 20)
	panel.SetData([]alerts.Alert{}, nil)

	view := panel.View()

	if !strings.Contains(view, "System Healthy") {
		t.Error("expected view to contain 'System Healthy' when no alerts")
	}
	if !strings.Contains(view, "No active alerts") {
		t.Error("expected view to contain 'No active alerts' when no alerts")
	}
}

func TestAlertsPanelViewWithAlerts(t *testing.T) {
	panel := NewAlertsPanel()
	panel.SetSize(80, 20)

	testAlerts := []alerts.Alert{
		{Severity: alerts.SeverityCritical, Message: "Critical error occurred"},
		{Severity: alerts.SeverityWarning, Message: "Warning about disk space"},
		{Severity: alerts.SeverityInfo, Message: "Informational message"},
	}
	panel.SetData(testAlerts, nil)

	view := panel.View()

	// Should contain title
	if !strings.Contains(view, "Active Alerts") {
		t.Error("expected view to contain title 'Active Alerts'")
	}

	// Should contain stats
	if !strings.Contains(view, "Crit:") || !strings.Contains(view, "Warn:") || !strings.Contains(view, "Info:") {
		t.Error("expected view to contain alert stats")
	}
}

func TestAlertsPanelViewGroupsBySeverity(t *testing.T) {
	panel := NewAlertsPanel()
	panel.SetSize(120, 30)

	testAlerts := []alerts.Alert{
		{Severity: alerts.SeverityCritical, Message: "First critical"},
		{Severity: alerts.SeverityCritical, Message: "Second critical"},
		{Severity: alerts.SeverityWarning, Message: "First warning"},
		{Severity: alerts.SeverityInfo, Message: "First info"},
	}
	panel.SetData(testAlerts, nil)

	view := panel.View()

	// Stats should show correct counts
	if !strings.Contains(view, "Crit: 2") {
		t.Error("expected 'Crit: 2' in view")
	}
	if !strings.Contains(view, "Warn: 1") {
		t.Error("expected 'Warn: 1' in view")
	}
	if !strings.Contains(view, "Info: 1") {
		t.Error("expected 'Info: 1' in view")
	}
}

func TestAlertsPanelUpdate(t *testing.T) {
	panel := NewAlertsPanel()

	newModel, cmd := panel.Update(nil)

	if newModel != panel {
		t.Error("expected Update to return same model")
	}
	if cmd != nil {
		t.Error("expected Update to return nil cmd")
	}
}

func TestAlertsPanelTracksFirstSeenByID(t *testing.T) {
	panel := NewAlertsPanel()

	t0 := time.Date(2025, 12, 12, 12, 0, 0, 0, time.UTC)
	panel.now = func() time.Time { return t0 }

	a := alerts.Alert{ID: "a1", Severity: alerts.SeverityWarning, Message: "Warning message"}
	panel.SetData([]alerts.Alert{a}, nil)

	first, ok := panel.firstSeen["a1"]
	if !ok {
		t.Fatal("expected firstSeen to contain a1")
	}
	if !first.Equal(t0) {
		t.Fatalf("expected firstSeen[a1]=%s, got %s", t0, first)
	}

	// Refresh the same alert later; firstSeen should remain stable.
	panel.now = func() time.Time { return t0.Add(10 * time.Second) }
	panel.SetData([]alerts.Alert{a}, nil)

	first2, ok := panel.firstSeen["a1"]
	if !ok {
		t.Fatal("expected firstSeen to still contain a1")
	}
	if !first2.Equal(first) {
		t.Fatalf("expected firstSeen[a1] to remain %s, got %s", first, first2)
	}
}

func TestAlertsPanelPrunesFirstSeenWhenAlertsRemoved(t *testing.T) {
	panel := NewAlertsPanel()

	t0 := time.Date(2025, 12, 12, 12, 0, 0, 0, time.UTC)
	panel.now = func() time.Time { return t0 }

	a1 := alerts.Alert{ID: "a1", Severity: alerts.SeverityCritical, Message: "Critical"}
	a2 := alerts.Alert{ID: "a2", Severity: alerts.SeverityInfo, Message: "Info"}
	panel.SetData([]alerts.Alert{a1, a2}, nil)

	if len(panel.firstSeen) != 2 {
		t.Fatalf("expected firstSeen to have 2 entries, got %d", len(panel.firstSeen))
	}

	panel.now = func() time.Time { return t0.Add(1 * time.Second) }
	panel.SetData([]alerts.Alert{a2}, nil)

	if _, ok := panel.firstSeen["a1"]; ok {
		t.Fatal("expected firstSeen to prune removed alert a1")
	}
	if _, ok := panel.firstSeen["a2"]; !ok {
		t.Fatal("expected firstSeen to retain existing alert a2")
	}
}

func TestAlertsPanelAlertKeyFallbackWhenIDMissing(t *testing.T) {
	panel := NewAlertsPanel()

	t0 := time.Date(2025, 12, 12, 12, 0, 0, 0, time.UTC)
	panel.now = func() time.Time { return t0 }

	a := alerts.Alert{
		Type:     alerts.AlertDiskLow,
		Severity: alerts.SeverityWarning,
		Message:  "Disk is low",
		Session:  "sess",
		Pane:     "%1",
	}
	key := panel.alertKey(a)
	if key == "" {
		t.Fatal("expected non-empty fallback key")
	}

	panel.SetData([]alerts.Alert{a}, nil)
	if _, ok := panel.firstSeen[key]; !ok {
		t.Fatalf("expected firstSeen to contain fallback key %q", key)
	}
}
