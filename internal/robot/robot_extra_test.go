package robot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPrintTerse(t *testing.T) {
	skipSlowRobotShortIntegrationTest(t, "PrintTerse walks live runtime collectors and is too expensive for go test -short")
	if !tmux.IsInstalled() {
		t.Skip("tmux not installed")
	}

	cfg := config.Default()
	output, err := captureStdout(t, func() error { return PrintTerse(cfg) })
	if err != nil {
		t.Fatalf("PrintTerse failed: %v", err)
	}

	// Output format: S:...|... (may be empty if no sessions exist and ListSessions returns empty)
	// When there are no sessions (but tmux is running), output may be just a newline
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		// No sessions - this is valid, skip further checks
		t.Log("No sessions found, output is empty (valid)")
		return
	}
	// Check for S: prefix (session) if there is output
	if !strings.HasPrefix(trimmed, "S:") {
		t.Errorf("Expected output to start with 'S:', got %q", trimmed)
	}
}

func TestPrintTerseNoTmux(t *testing.T) {
	skipSlowRobotShortIntegrationTest(t, "PrintTerseNoTmux still exercises live terse collection and is too expensive for go test -short")
	// Without mocking, we can only test the parsing logic helper if we extract it,
	// or rely on PrintTerse behavior in current env.

	cfg := config.Default()
	output, err := captureStdout(t, func() error { return PrintTerse(cfg) })
	if err != nil {
		t.Fatalf("PrintTerse failed: %v", err)
	}

	parts := parseTerseOutput(output)
	// If output is empty (e.g. no sessions and no alert config), parts might be nil or empty string
	if len(output) > 0 && len(parts) == 0 {
		// It might be just a newline?
		if strings.TrimSpace(output) != "" {
			t.Error("No terse parts found but output not empty")
		}
	}

	for _, part := range parts {
		state, err := ParseTerse(part)
		if err != nil {
			t.Errorf("Failed to parse terse part %q: %v", part, err)
		}
		if state.Session == "" {
			t.Error("Session is empty in parsed state")
		}
	}
}

func TestTerseKeyMapUnique(t *testing.T) {
	seen := make(map[string]string, len(TerseKeyMap))
	for longKey, shortKey := range TerseKeyMap {
		if shortKey == "" {
			t.Fatalf("short key is empty for %q", longKey)
		}
		if existing, ok := seen[shortKey]; ok {
			t.Fatalf("short key %q collision: %q and %q", shortKey, existing, longKey)
		}
		seen[shortKey] = longKey
	}
}

func TestTerseKeyMapRoundTrip(t *testing.T) {
	reverse := TerseKeyReverseMap()
	for longKey, shortKey := range TerseKeyMap {
		if got, ok := TerseKeyFor(longKey); !ok || got != shortKey {
			t.Fatalf("TerseKeyFor(%q) = %q, ok=%v; want %q", longKey, got, ok, shortKey)
		}
		if got, ok := reverse[shortKey]; !ok || got != longKey {
			t.Fatalf("reverse[%q] = %q, ok=%v; want %q", shortKey, got, ok, longKey)
		}
		if got, ok := ExpandTerseKey(shortKey); !ok || got != longKey {
			t.Fatalf("ExpandTerseKey(%q) = %q, ok=%v; want %q", shortKey, got, ok, longKey)
		}
	}
}

func TestGetACFSStatus_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "")

	output, err := GetACFSStatus()
	if err != nil {
		t.Fatalf("GetACFSStatus error: %v", err)
	}
	if output.Success {
		t.Fatalf("expected failure when acfs missing")
	}
	if output.ErrorCode != ErrCodeDependencyMissing {
		t.Fatalf("error_code=%q, want %q", output.ErrorCode, ErrCodeDependencyMissing)
	}
	if output.Tools == nil {
		t.Fatalf("expected tools map to be present")
	}
}

func TestGetACFSStatus_WithFakeTools(t *testing.T) {
	cleanup := withFakeTools(t)
	defer cleanup()

	output, err := GetACFSStatus()
	if err != nil {
		t.Fatalf("GetACFSStatus error: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success, got error: %s", output.Error)
	}
	if !output.ACFSAvailable {
		t.Fatalf("expected acfs_available true")
	}
	if output.ACFSVersion == "" {
		t.Fatalf("expected acfs_version to be set")
	}
	if output.Tools == nil {
		t.Fatalf("expected tools map to be present")
	}
	// Ensure core keys exist (installed may vary by environment).
	for _, key := range []string{"tmux", "br", "bv", "cc", "cod", "gmi", "git"} {
		if _, ok := output.Tools[key]; !ok {
			t.Fatalf("missing tool entry for %q", key)
		}
	}
}

func parseTerseOutput(output string) []string {
	// Strip newline
	output = stripNewline(output)
	if output == "" {
		return nil
	}
	return strings.Split(output, ";")
}

func stripNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

func TestAttachSnapshotResourcePressureEmitsRobotPressure(t *testing.T) {
	oldCollect := collectSnapshotResourcePressure
	t.Cleanup(func() {
		collectSnapshotResourcePressure = oldCollect
	})
	collectSnapshotResourcePressure = func() *pressure.RobotPressure {
		return &pressure.RobotPressure{
			Success:           true,
			Timestamp:         "2026-05-09T18:30:00Z",
			Mode:              "observe",
			Overall:           "high",
			Limiting:          []string{"proc_count"},
			RecommendedAction: "defer_non_urgent_work",
			Sources: []pressure.RobotSource{{
				Source: "proc_count",
				Value:  0.86,
				Unit:   "ratio",
				Level:  "high",
			}},
		}
	}

	snapshot := newSnapshotOutput(config.Default())
	attachSnapshotResourcePressure(snapshot)

	if snapshot.ResourcePressure == nil {
		t.Fatal("ResourcePressure is nil")
	}
	if snapshot.ResourcePressure.Overall != "high" {
		t.Fatalf("Overall=%q, want high", snapshot.ResourcePressure.Overall)
	}
	if len(snapshot.ResourcePressure.Sources) != 1 || snapshot.ResourcePressure.Sources[0].Source != "proc_count" {
		t.Fatalf("sources=%+v, want proc_count source", snapshot.ResourcePressure.Sources)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	if !strings.Contains(string(raw), `"resource_pressure"`) || !strings.Contains(string(raw), `"proc_count"`) {
		t.Fatalf("snapshot JSON missing resource pressure projection: %s", raw)
	}
}

func TestBuildTerseOutputFromSnapshotUsesSharedProjection(t *testing.T) {
	snapshot := &SnapshotOutput{
		Summary: StatusSummary{
			MailUnread: 2,
			ReadyWork:  3,
			InProgress: 1,
		},
		Sessions: []SnapshotSession{
			{
				Name: "proj",
				Agents: []SnapshotAgent{
					{Type: "claude", State: "active"},
					{Type: "codex", State: "idle"},
					{Type: "gemini", State: "error"},
					{Type: "user", State: "active"},
				},
			},
		},
		BeadsSummary: &bv.BeadsSummary{
			Ready:      3,
			InProgress: 1,
			Blocked:    2,
		},
		AlertSummary: &AlertSummaryInfo{
			BySeverity: map[string]int{
				"critical": 1,
				"warning":  2,
			},
		},
		AttentionSummary: &SnapshotAttentionSummary{
			TotalEvents:         4,
			ActionRequiredCount: 2,
			InterestingCount:    1,
		},
		MailUnread: 2,
	}

	output := buildTerseOutputFromSnapshot(snapshot)
	if output.AttentionHint != "2!action 1?interest" {
		t.Fatalf("attention hint = %q, want compact summary", output.AttentionHint)
	}
	if len(output.States) != 1 {
		t.Fatalf("states len = %d, want 1", len(output.States))
	}

	state := output.States[0]
	if state.Session != "proj" {
		t.Fatalf("session = %q, want proj", state.Session)
	}
	if state.TotalAgents != 4 || state.ActiveAgents != 3 {
		t.Fatalf("agent counts = %+v, want total=4 active=3", state)
	}
	if state.WorkingAgents != 1 || state.IdleAgents != 1 || state.ErrorAgents != 1 {
		t.Fatalf("state counts = %+v, want 1 working/idle/error", state)
	}
	if state.ReadyBeads != 3 || state.InProgressBead != 1 || state.BlockedBeads != 2 {
		t.Fatalf("work counts = %+v, want ready=3 in_progress=1 blocked=2", state)
	}
	if state.UnreadMail != 2 || state.CriticalAlerts != 1 || state.WarningAlerts != 2 {
		t.Fatalf("coordination counts = %+v", state)
	}
	if got := output.TerseLines[0]; !strings.Contains(got, "S:proj|A:3/4|W:1|I:1|E:1") {
		t.Fatalf("terse line = %q, want shared session counts", got)
	}
}

func TestRenderMarkdownFromSnapshotUsesRegistrySections(t *testing.T) {
	snapshot := &SnapshotOutput{
		Timestamp: "2026-03-25T03:00:00Z",
		Summary: StatusSummary{
			TotalSessions: 1,
			TotalAgents:   2,
			ReadyWork:     2,
			InProgress:    1,
			AlertsActive:  1,
			MailUnread:    4,
			HealthStatus:  "healthy",
		},
		Sessions: []SnapshotSession{
			{
				Name:     "proj",
				Attached: true,
				Agents: []SnapshotAgent{
					{Type: "claude", State: "active"},
					{Type: "codex", State: "idle"},
				},
			},
		},
		Work: &adapters.WorkSection{
			Available: true,
			Summary: &adapters.WorkSummary{
				Total:      4,
				Ready:      2,
				InProgress: 1,
				Blocked:    1,
			},
			Ready: []adapters.WorkItem{
				{ID: "bd-1", Title: "Ready work"},
			},
			InProgress: []adapters.WorkItem{
				{ID: "bd-2", Title: "Active work", Assignee: "codex"},
			},
		},
		AlertsDetailed: []AlertInfo{
			{Type: "quota", Severity: "critical", Message: "quota exhausted"},
		},
		AlertSummary: &AlertSummaryInfo{
			TotalActive: 1,
			BySeverity: map[string]int{
				"critical": 1,
			},
		},
		AttentionSummary: &SnapshotAttentionSummary{
			TotalEvents:         1,
			ActionRequiredCount: 1,
			TopItems: []SnapshotAttentionItem{
				{Cursor: 9, Category: "quota", Severity: "critical", Summary: "Investigate quota"},
			},
		},
	}

	rendered, err := renderMarkdownFromSnapshot(snapshot, MarkdownOptions{
		IncludeSections: []string{"summary", "sessions", "work", "alerts", "attention"},
	})
	if err != nil {
		t.Fatalf("renderMarkdownFromSnapshot error: %v", err)
	}

	for _, want := range []string{
		"### Summary",
		"### Sessions (1)",
		"### Work (R:2 I:1 B:1 = 4)",
		"### Alerts (1, 1 critical)",
		"### Attention",
		"`bd-1`",
		"Investigate quota",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("markdown missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderMarkdownFromSnapshotRejectsUnknownSections(t *testing.T) {
	_, err := renderMarkdownFromSnapshot(&SnapshotOutput{}, MarkdownOptions{
		IncludeSections: []string{"mail"},
	})
	if err == nil {
		t.Fatal("expected invalid section error")
	}
	if !strings.Contains(err.Error(), "supported: summary, sessions, work, alerts, attention") {
		t.Fatalf("unexpected error: %v", err)
	}
}
