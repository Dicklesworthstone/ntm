package alerts

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.AgentStuckMinutes != 5 {
		t.Errorf("expected AgentStuckMinutes=5, got %d", cfg.AgentStuckMinutes)
	}
	if cfg.DiskLowThresholdGB != 5.0 {
		t.Errorf("expected DiskLowThresholdGB=5.0, got %f", cfg.DiskLowThresholdGB)
	}
	if cfg.MailBacklogThreshold != 10 {
		t.Errorf("expected MailBacklogThreshold=10, got %d", cfg.MailBacklogThreshold)
	}
	if cfg.BeadStaleHours != 24 {
		t.Errorf("expected BeadStaleHours=24, got %d", cfg.BeadStaleHours)
	}
	if cfg.ResolvedPruneMinutes != 60 {
		t.Errorf("expected ResolvedPruneMinutes=60, got %d", cfg.ResolvedPruneMinutes)
	}
	if !cfg.Enabled {
		t.Error("expected Enabled=true")
	}
}

func TestAlertIsResolved(t *testing.T) {
	alert := Alert{
		ID:        "test-1",
		Type:      AlertAgentError,
		Severity:  SeverityWarning,
		Message:   "Test alert",
		CreatedAt: time.Now(),
	}

	if alert.IsResolved() {
		t.Error("expected alert to not be resolved initially")
	}

	now := time.Now()
	alert.ResolvedAt = &now

	if !alert.IsResolved() {
		t.Error("expected alert to be resolved after setting ResolvedAt")
	}
}

func TestAlertDuration(t *testing.T) {
	start := time.Now().Add(-5 * time.Minute)
	alert := Alert{
		ID:        "test-2",
		Type:      AlertDiskLow,
		Severity:  SeverityError,
		Message:   "Low disk",
		CreatedAt: start,
	}

	duration := alert.Duration()
	if duration < 5*time.Minute || duration > 6*time.Minute {
		t.Errorf("expected duration ~5 min, got %v", duration)
	}

	// Test resolved alert duration
	end := start.Add(3 * time.Minute)
	alert.ResolvedAt = &end

	duration = alert.Duration()
	if duration != 3*time.Minute {
		t.Errorf("expected duration 3 min for resolved alert, got %v", duration)
	}
}

func TestTrackerBasic(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	// Initially empty
	active := tracker.GetActive()
	if len(active) != 0 {
		t.Errorf("expected 0 active alerts, got %d", len(active))
	}

	// Add alerts
	alerts := []Alert{
		{
			ID:       "test-a",
			Type:     AlertAgentError,
			Severity: SeverityWarning,
			Message:  "Error A",
		},
		{
			ID:       "test-b",
			Type:     AlertDiskLow,
			Severity: SeverityError,
			Message:  "Disk low",
		},
	}

	tracker.Update(alerts, nil)

	active = tracker.GetActive()
	if len(active) != 2 {
		t.Errorf("expected 2 active alerts, got %d", len(active))
	}

	// Check summary
	summary := tracker.Summary()
	if summary.TotalActive != 2 {
		t.Errorf("expected TotalActive=2, got %d", summary.TotalActive)
	}
	if summary.BySeverity["warning"] != 1 {
		t.Errorf("expected 1 warning alert, got %d", summary.BySeverity["warning"])
	}
	if summary.BySeverity["error"] != 1 {
		t.Errorf("expected 1 error alert, got %d", summary.BySeverity["error"])
	}
}

func TestTrackerResolution(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	// Add alerts
	alerts := []Alert{
		{ID: "keep", Type: AlertAgentError, Severity: SeverityWarning, Message: "Keep"},
		{ID: "remove", Type: AlertDiskLow, Severity: SeverityError, Message: "Remove"},
	}

	tracker.Update(alerts, nil)

	// Update with only one alert - the other should be resolved
	tracker.Update([]Alert{{ID: "keep", Type: AlertAgentError, Severity: SeverityWarning, Message: "Keep"}}, nil)

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Errorf("expected 1 active alert after resolution, got %d", len(active))
	}
	if active[0].ID != "keep" {
		t.Errorf("expected 'keep' alert to remain, got %s", active[0].ID)
	}

	resolved := tracker.GetResolved()
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved alert, got %d", len(resolved))
	}
	if resolved[0].ID != "remove" {
		t.Errorf("expected 'remove' alert to be resolved, got %s", resolved[0].ID)
	}
}

func TestTrackerRefresh(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	// Add alert
	alert := Alert{ID: "refresh", Type: AlertAgentError, Severity: SeverityWarning, Message: "Refresh test"}
	tracker.Update([]Alert{alert}, nil)

	// Get initial count
	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active))
	}
	initialCount := active[0].Count

	// Refresh same alert
	tracker.Update([]Alert{alert}, nil)

	active = tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 alert after refresh, got %d", len(active))
	}
	if active[0].Count != initialCount+1 {
		t.Errorf("expected count to increment, got %d (was %d)", active[0].Count, initialCount)
	}
}

func TestTrackerSeverityEscalation(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	// Add warning alert
	alert := Alert{ID: "escalate", Type: AlertAgentError, Severity: SeverityWarning, Message: "Escalate test"}
	tracker.Update([]Alert{alert}, nil)

	// Escalate to error
	alert.Severity = SeverityError
	tracker.Update([]Alert{alert}, nil)

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(active))
	}
	if active[0].Severity != SeverityError {
		t.Errorf("expected severity to escalate to error, got %s", active[0].Severity)
	}
}

func TestTrackerManualResolve(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	alert := Alert{ID: "manual", Type: AlertAgentError, Severity: SeverityWarning, Message: "Manual resolve"}
	tracker.Update([]Alert{alert}, nil)

	// Manual resolve
	ok := tracker.ManualResolve("manual")
	if !ok {
		t.Error("expected manual resolve to succeed")
	}

	active := tracker.GetActive()
	if len(active) != 0 {
		t.Errorf("expected 0 active alerts after manual resolve, got %d", len(active))
	}

	resolved := tracker.GetResolved()
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved alert, got %d", len(resolved))
	}

	// Try to resolve non-existent
	ok = tracker.ManualResolve("nonexistent")
	if ok {
		t.Error("expected manual resolve of non-existent to fail")
	}
}

func TestTrackerGetByID(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	alert := Alert{ID: "findme", Type: AlertAgentError, Severity: SeverityWarning, Message: "Find me"}
	tracker.Update([]Alert{alert}, nil)

	// Find active alert
	found, ok := tracker.GetByID("findme")
	if !ok {
		t.Error("expected to find alert by ID")
	}
	if found.ID != "findme" {
		t.Errorf("expected ID 'findme', got %s", found.ID)
	}

	// Resolve and find in resolved
	tracker.ManualResolve("findme")
	found, ok = tracker.GetByID("findme")
	if !ok {
		t.Error("expected to find resolved alert by ID")
	}
	if !found.IsResolved() {
		t.Error("expected found alert to be resolved")
	}

	// Not found
	_, ok = tracker.GetByID("notfound")
	if ok {
		t.Error("expected not to find non-existent alert")
	}
}

func TestTrackerClear(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	alerts := []Alert{
		{ID: "a", Type: AlertAgentError, Severity: SeverityWarning, Message: "A"},
		{ID: "b", Type: AlertDiskLow, Severity: SeverityError, Message: "B"},
	}
	tracker.Update(alerts, nil)
	tracker.ManualResolve("a")

	// Verify state before clear
	active, resolved := tracker.GetAll()
	if len(active) != 1 || len(resolved) != 1 {
		t.Fatalf("unexpected state before clear: %d active, %d resolved", len(active), len(resolved))
	}

	// Clear
	tracker.Clear()

	active, resolved = tracker.GetAll()
	if len(active) != 0 || len(resolved) != 0 {
		t.Errorf("expected 0 active and 0 resolved after clear, got %d active, %d resolved", len(active), len(resolved))
	}
}

func TestTrackerFilterByType(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	alerts := []Alert{
		{ID: "err1", Type: AlertAgentError, Severity: SeverityWarning, Message: "Error 1"},
		{ID: "err2", Type: AlertAgentError, Severity: SeverityError, Message: "Error 2"},
		{ID: "disk", Type: AlertDiskLow, Severity: SeverityWarning, Message: "Disk"},
	}
	tracker.Update(alerts, nil)

	// Filter by type
	agentErrorType := AlertAgentError
	filtered := tracker.GetActiveFiltered(&agentErrorType, nil)
	if len(filtered) != 2 {
		t.Errorf("expected 2 agent_error alerts, got %d", len(filtered))
	}

	diskLowType := AlertDiskLow
	filtered = tracker.GetActiveFiltered(&diskLowType, nil)
	if len(filtered) != 1 {
		t.Errorf("expected 1 disk_low alert, got %d", len(filtered))
	}
}

func TestTrackerFilterBySeverity(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	alerts := []Alert{
		{ID: "info", Type: AlertAgentError, Severity: SeverityInfo, Message: "Info"},
		{ID: "warn", Type: AlertAgentError, Severity: SeverityWarning, Message: "Warning"},
		{ID: "err", Type: AlertAgentError, Severity: SeverityError, Message: "Error"},
		{ID: "crit", Type: AlertAgentError, Severity: SeverityCritical, Message: "Critical"},
	}
	tracker.Update(alerts, nil)

	// Filter by minimum severity
	warnSeverity := SeverityWarning
	filtered := tracker.GetActiveFiltered(nil, &warnSeverity)
	if len(filtered) != 3 {
		t.Errorf("expected 3 alerts with severity >= warning, got %d", len(filtered))
	}

	errSeverity := SeverityError
	filtered = tracker.GetActiveFiltered(nil, &errSeverity)
	if len(filtered) != 2 {
		t.Errorf("expected 2 alerts with severity >= error, got %d", len(filtered))
	}
}

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		severity Severity
		expected int
	}{
		{SeverityInfo, 1},
		{SeverityWarning, 2},
		{SeverityError, 3},
		{SeverityCritical, 4},
		{Severity("unknown"), 0},
	}

	for _, tt := range tests {
		got := severityRank(tt.severity)
		if got != tt.expected {
			t.Errorf("severityRank(%s) = %d, want %d", tt.severity, got, tt.expected)
		}
	}
}

func TestGeneratorDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false

	gen := NewGenerator(cfg)
	alerts, _ := gen.GenerateAll()

	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts when disabled, got %d", len(alerts))
	}
}

func TestGenerateAlertID(t *testing.T) {
	id1 := generateAlertID(AlertAgentError, "session1", "pane1")
	id2 := generateAlertID(AlertAgentError, "session1", "pane1")
	id3 := generateAlertID(AlertAgentError, "session1", "pane2")

	// Same inputs should produce same ID
	if id1 != id2 {
		t.Errorf("expected same IDs for same inputs, got %s vs %s", id1, id2)
	}

	// Different inputs should produce different ID
	if id1 == id3 {
		t.Error("expected different IDs for different inputs")
	}

	// ID should be hex string
	if len(id1) != 16 {
		t.Errorf("expected ID length 16, got %d", len(id1))
	}
}

func TestTruncateString(t *testing.T) {
	short := "hello"
	long := "this is a very long string that should be truncated"

	if truncateString(short, 10) != short {
		t.Errorf("expected short string unchanged, got %s", truncateString(short, 10))
	}

	truncated := truncateString(long, 20)
	if len(truncated) != 20 {
		t.Errorf("expected truncated length 20, got %d", len(truncated))
	}
	if truncated[len(truncated)-3:] != "..." {
		t.Error("expected ellipsis at end of truncated string")
	}
}

func TestTrackerAddAlert(t *testing.T) {
	cfg := DefaultConfig()
	tracker := NewTracker(cfg)

	// Add two alerts using AddAlert
	alert1 := Alert{ID: "add-1", Type: AlertAgentError, Severity: SeverityWarning, Message: "Alert 1"}
	alert2 := Alert{ID: "add-2", Type: AlertDiskLow, Severity: SeverityError, Message: "Alert 2"}

	tracker.AddAlert(alert1)
	tracker.AddAlert(alert2)

	active := tracker.GetActive()
	if len(active) != 2 {
		t.Errorf("expected 2 active alerts, got %d", len(active))
	}

	// Add a third alert - should NOT resolve the first two (unlike Update)
	alert3 := Alert{ID: "add-3", Type: AlertBeadStale, Severity: SeverityInfo, Message: "Alert 3"}
	tracker.AddAlert(alert3)

	active = tracker.GetActive()
	if len(active) != 3 {
		t.Errorf("expected 3 active alerts (AddAlert doesn't auto-resolve), got %d", len(active))
	}

	// Verify refresh behavior
	alert1.Severity = SeverityError // Escalate severity
	tracker.AddAlert(alert1)

	active = tracker.GetActive()
	if len(active) != 3 {
		t.Errorf("expected 3 alerts after refresh, got %d", len(active))
	}

	// Find alert1 and check count/severity
	var found *Alert
	for _, a := range active {
		if a.ID == "add-1" {
			found = &a
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find alert add-1")
	}
	if found.Count != 2 {
		t.Errorf("expected count 2 after refresh, got %d", found.Count)
	}
	if found.Severity != SeverityError {
		t.Errorf("expected severity to escalate to error, got %s", found.Severity)
	}
}

// ============ stripANSI tests ============

func TestStripANSI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"empty string", "", ""},
		{"CSI color code", "\x1b[31mred text\x1b[0m", "red text"},
		{"CSI bold", "\x1b[1mbold\x1b[22m", "bold"},
		{"CSI multiple params", "\x1b[1;31;42mcolored\x1b[0m", "colored"},
		{"CSI question mark", "\x1b[?25h", ""}, // Show cursor
		{"OSC with BEL", "\x1b]0;window title\a", ""},
		{"OSC with ST", "\x1b]0;window title\x1b\\", ""},
		{"mixed content", "before\x1b[31mred\x1b[0m after", "beforered after"},
		{"nested sequences", "\x1b[1m\x1b[31mbold red\x1b[0m\x1b[22m", "bold red"},
		{"only escape sequences", "\x1b[31m\x1b[0m", ""},
		{"multiline with ANSI", "line1\n\x1b[32mline2\x1b[0m\nline3", "line1\nline2\nline3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ============ truncateString edge cases ============

func TestTruncateString_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty input", "", 10, ""},
		{"zero maxLen", "hello", 0, ""},
		{"negative maxLen", "hello", -5, ""},
		{"maxLen 1", "hello", 1, "."},
		{"maxLen 2", "hello", 2, ".."},
		{"maxLen 3", "hello", 3, "..."},
		{"maxLen 4", "hello", 4, "h..."},
		{"exact fit", "hello", 5, "hello"},
		{"one over", "hello!", 5, "he..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTruncateString_UTF8(t *testing.T) {
	t.Parallel()

	// Multi-byte chars: "世" is 3 bytes, "界" is 3 bytes
	input := "世界hello"
	got := truncateString(input, 8)
	// Should not split a multi-byte char
	if len(got) > 8 {
		t.Errorf("truncateString(%q, 8) = %q (len=%d), exceeds maxLen 8", input, got, len(got))
	}
}

// ============ ToConfigAlerts test ============

func TestToConfigAlerts(t *testing.T) {
	t.Parallel()

	cfg := ToConfigAlerts(true, 10, 2.5, 20, 48, 82.5, 120, "/tmp/projects")
	if !cfg.Enabled {
		t.Error("expected Enabled=true")
	}
	if cfg.AgentStuckMinutes != 10 {
		t.Errorf("AgentStuckMinutes = %d, want 10", cfg.AgentStuckMinutes)
	}
	if cfg.DiskLowThresholdGB != 2.5 {
		t.Errorf("DiskLowThresholdGB = %f, want 2.5", cfg.DiskLowThresholdGB)
	}
	if cfg.MailBacklogThreshold != 20 {
		t.Errorf("MailBacklogThreshold = %d, want 20", cfg.MailBacklogThreshold)
	}
	if cfg.ContextWarningThreshold != 82.5 {
		t.Errorf("ContextWarningThreshold = %f, want 82.5", cfg.ContextWarningThreshold)
	}
	if cfg.BeadStaleHours != 48 {
		t.Errorf("BeadStaleHours = %d, want 48", cfg.BeadStaleHours)
	}
	if cfg.ResolvedPruneMinutes != 120 {
		t.Errorf("ResolvedPruneMinutes = %d, want 120", cfg.ResolvedPruneMinutes)
	}
	if cfg.ProjectsDir != "/tmp/projects" {
		t.Errorf("ProjectsDir = %q, want /tmp/projects", cfg.ProjectsDir)
	}
}

// ============ Alert type/severity constants ============

func TestAlertTypeConstants(t *testing.T) {
	t.Parallel()

	// Verify key alert type strings
	types := map[AlertType]string{
		AlertAgentStuck:          "agent_stuck",
		AlertAgentCrashed:        "agent_crashed",
		AlertAgentError:          "agent_error",
		AlertDiskLow:             "disk_low",
		AlertBeadStale:           "bead_stale",
		AlertRateLimit:           "rate_limit",
		AlertDependencyCycle:     "dependency_cycle",
		AlertContextWarning:      "context_warning",
		AlertRotationStarted:     "rotation_started",
		AlertRotationComplete:    "rotation_complete",
		AlertRotationFailed:      "rotation_failed",
		AlertCompactionTriggered: "compaction_triggered",
		AlertCompactionComplete:  "compaction_complete",
		AlertCompactionFailed:    "compaction_failed",
	}

	for got, want := range types {
		if string(got) != want {
			t.Errorf("AlertType %q != expected %q", got, want)
		}
	}
}

func TestSeverityConstants(t *testing.T) {
	t.Parallel()

	if string(SeverityInfo) != "info" {
		t.Errorf("SeverityInfo = %q", SeverityInfo)
	}
	if string(SeverityWarning) != "warning" {
		t.Errorf("SeverityWarning = %q", SeverityWarning)
	}
	if string(SeverityError) != "error" {
		t.Errorf("SeverityError = %q", SeverityError)
	}
	if string(SeverityCritical) != "critical" {
		t.Errorf("SeverityCritical = %q", SeverityCritical)
	}
}

func TestGlobalTracker(t *testing.T) {
	// Get global tracker twice - should be same instance
	t1 := GetGlobalTracker()
	t2 := GetGlobalTracker()

	if t1 != t2 {
		t.Error("expected GetGlobalTracker to return same instance")
	}

	// Update config
	cfg := Config{
		Enabled:              true,
		AgentStuckMinutes:    10,
		DiskLowThresholdGB:   2.0,
		MailBacklogThreshold: 5,
		BeadStaleHours:       12,
		ResolvedPruneMinutes: 30,
	}
	SetGlobalTrackerConfig(cfg)

	// Verify config was updated (pruneAfter should be 30 minutes)
	tracker := GetGlobalTracker()
	if tracker.pruneAfter != 30*time.Minute {
		t.Errorf("expected pruneAfter 30m, got %v", tracker.pruneAfter)
	}
}

func TestGeneratorDetectErrorState_Last20Lines(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(DefaultConfig())
	pane := tmux.Pane{ID: "pane-1"}

	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "ok"
	}
	lines[0] = "fatal: outside window"
	lines[len(lines)-1] = "error: inside window"

	alert := gen.detectErrorState("sess", pane, lines)
	if alert == nil {
		t.Fatal("expected alert, got nil")
	}
	if alert.Type != AlertAgentError {
		t.Errorf("Type = %q, want %q", alert.Type, AlertAgentError)
	}
	if alert.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", alert.Severity, SeverityError)
	}
	if alert.Message != "Error detected in agent output" {
		t.Errorf("Message = %q, want %q", alert.Message, "Error detected in agent output")
	}
	if alert.Session != "sess" {
		t.Errorf("Session = %q, want %q", alert.Session, "sess")
	}
	if alert.Pane != "pane-1" {
		t.Errorf("Pane = %q, want %q", alert.Pane, "pane-1")
	}
	matched, ok := alert.Context["matched_line"].(string)
	if !ok || matched == "" {
		t.Fatalf("expected matched_line string in context, got %T (%v)", alert.Context["matched_line"], alert.Context["matched_line"])
	}
	if !strings.Contains(matched, "error:") {
		t.Errorf("matched_line = %q, want it to include %q", matched, "error:")
	}
}

func TestGeneratorDetectErrorState_SeverityClassification(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(DefaultConfig())
	pane := tmux.Pane{ID: "pane-1"}

	tests := []struct {
		name       string
		line       string
		wantSev    Severity
		wantMsg    string
		wantSubstr string
	}{
		{
			name:       "fatal",
			line:       "FATAL: something bad happened",
			wantSev:    SeverityCritical,
			wantMsg:    "Fatal error in agent",
			wantSubstr: "FATAL:",
		},
		{
			name:       "failed",
			line:       "failed: operation did not complete",
			wantSev:    SeverityWarning,
			wantMsg:    "Operation failed in agent",
			wantSubstr: "failed:",
		},
		{
			name:       "timeout",
			line:       "Timeout while waiting for response",
			wantSev:    SeverityWarning,
			wantMsg:    "Timeout detected",
			wantSubstr: "Timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			alert := gen.detectErrorState("sess", pane, []string{tt.line})
			if alert == nil {
				t.Fatalf("expected alert for line %q, got nil", tt.line)
			}
			if alert.Type != AlertAgentError {
				t.Errorf("Type = %q, want %q", alert.Type, AlertAgentError)
			}
			if alert.Severity != tt.wantSev {
				t.Errorf("Severity = %q, want %q", alert.Severity, tt.wantSev)
			}
			if alert.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", alert.Message, tt.wantMsg)
			}
			matched, ok := alert.Context["matched_line"].(string)
			if !ok || matched == "" {
				t.Fatalf("expected matched_line string in context, got %T (%v)", alert.Context["matched_line"], alert.Context["matched_line"])
			}
			if !strings.Contains(matched, tt.wantSubstr) {
				t.Errorf("matched_line = %q, want it to include %q", matched, tt.wantSubstr)
			}
		})
	}
}

func TestGeneratorDetectRateLimit_Last20Lines(t *testing.T) {
	t.Parallel()

	gen := NewGenerator(DefaultConfig())
	pane := tmux.Pane{ID: "pane-1"}

	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "ok"
	}
	lines[0] = "rate limit exceeded (outside window)"

	alert := gen.detectRateLimit("sess", pane, lines)
	if alert != nil {
		t.Fatalf("expected nil alert for rate limit outside last 20 lines, got %+v", alert)
	}

	lines[len(lines)-1] = "429 too many requests (inside window)"
	alert = gen.detectRateLimit("sess", pane, lines)
	if alert == nil {
		t.Fatal("expected rate limit alert, got nil")
	}
	if alert.Type != AlertRateLimit {
		t.Errorf("Type = %q, want %q", alert.Type, AlertRateLimit)
	}
	if alert.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q", alert.Severity, SeverityWarning)
	}
	if alert.Message != "Rate limiting detected" {
		t.Errorf("Message = %q, want %q", alert.Message, "Rate limiting detected")
	}
	matched, ok := alert.Context["matched_line"].(string)
	if !ok || matched == "" {
		t.Fatalf("expected matched_line string in context, got %T (%v)", alert.Context["matched_line"], alert.Context["matched_line"])
	}
	if !strings.Contains(matched, "429") {
		t.Errorf("matched_line = %q, want it to include %q", matched, "429")
	}
}

func TestGeneratorCheckDiskSpace_ThresholdAndFallbackPath(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" || runtime.GOOS == "js" || runtime.GOOS == "wasip1" {
		t.Skip("disk space checks are unix-only")
	}

	cfg := DefaultConfig()
	cfg.DiskLowThresholdGB = 0
	gen := NewGenerator(cfg)

	alert, err := gen.checkDiskSpace()
	if err != nil {
		t.Fatalf("checkDiskSpace error: %v", err)
	}
	if alert != nil {
		t.Fatalf("expected nil alert with threshold 0, got %+v", *alert)
	}

	cfg = DefaultConfig()
	cfg.ProjectsDir = "/__ntm_should_not_exist__bd37e3k"
	cfg.DiskLowThresholdGB = 1e9 // should always trigger on real systems
	gen = NewGenerator(cfg)

	alert, err = gen.checkDiskSpace()
	if err != nil {
		t.Fatalf("checkDiskSpace error: %v", err)
	}
	if alert == nil {
		t.Fatal("expected alert with huge threshold, got nil")
	}
	path, ok := alert.Context["path"].(string)
	if !ok {
		t.Fatalf("expected context path string, got %T (%v)", alert.Context["path"], alert.Context["path"])
	}
	if path != "/" {
		t.Errorf("context path = %q, want %q (should reflect fallback statfs path)", path, "/")
	}
	if !strings.Contains(alert.Message, " on /") {
		t.Errorf("Message = %q, want it to include %q", alert.Message, " on /")
	}
}

func TestFormatAlertString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		alert Alert
		want  string
	}{
		{
			name:  "message only",
			alert: Alert{Message: "hello"},
			want:  "hello",
		},
		{
			name:  "session prefix",
			alert: Alert{Session: "sess", Message: "hello"},
			want:  "sess: hello",
		},
		{
			name:  "pane suffix",
			alert: Alert{Pane: "3", Message: "hello"},
			want:  "hello (pane 3)",
		},
		{
			name:  "session and pane",
			alert: Alert{Session: "sess", Pane: "3", Message: "hello"},
			want:  "sess: hello (pane 3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := formatAlertString(tt.alert); got != tt.want {
				t.Errorf("formatAlertString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateAndTrack_DisabledResolvesExisting(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	tracker.AddAlert(Alert{
		ID:       "seed",
		Type:     AlertAgentError,
		Severity: SeverityWarning,
		Source:   "agents",
		Message:  "seed",
	})

	cfg := DefaultConfig()
	cfg.Enabled = false

	got := GenerateAndTrack(cfg)
	if got != tracker {
		t.Error("expected GenerateAndTrack to return the global tracker instance")
	}

	active, resolved := tracker.GetAll()
	if len(active) != 0 {
		t.Errorf("expected 0 active alerts, got %d", len(active))
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved alert, got %d", len(resolved))
	}
	if resolved[0].ID != "seed" {
		t.Errorf("resolved[0].ID = %q, want %q", resolved[0].ID, "seed")
	}
}

func TestGetAlertStrings_DisabledReturnsEmpty(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	cfg := DefaultConfig()
	cfg.Enabled = false

	msgs := GetAlertStrings(cfg)
	if len(msgs) != 0 {
		t.Errorf("expected 0 alert strings, got %d", len(msgs))
	}
}

func TestPrintAlerts_DisabledConfig(t *testing.T) {
	tracker := GetGlobalTracker()
	tracker.Clear()

	cfg := DefaultConfig()
	cfg.Enabled = false

	out, err := captureStdout(t, func() error {
		return PrintAlerts(cfg, false)
	})
	if err != nil {
		t.Fatalf("PrintAlerts error: %v", err)
	}

	var got AlertsOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json.Unmarshal error: %v\noutput:\n%s", err, string(out))
	}
	if got.Config.Enabled {
		t.Errorf("Config.Enabled = true, want false")
	}
	if len(got.Active) != 0 {
		t.Errorf("Active count = %d, want 0", len(got.Active))
	}
	if got.Summary.TotalActive != 0 {
		t.Errorf("Summary.TotalActive = %d, want 0", got.Summary.TotalActive)
	}
}

func captureStdout(t *testing.T, fn func() error) ([]byte, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe error: %v", err)
	}
	os.Stdout = w

	fnErr := fn()
	_ = w.Close()
	os.Stdout = oldStdout

	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("io.ReadAll error: %v", readErr)
	}
	return out, fnErr
}
