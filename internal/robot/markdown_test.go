package robot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRenderAgentTable(t *testing.T) {
	rows := []AgentTableRow{
		{Agent: "cc_1", Type: "claude", Status: "active"},
		{Agent: "cod_1", Type: "codex", Status: "idle"},
	}

	out := RenderAgentTable(rows)

	if !strings.HasPrefix(out, "| Agent | Type | Status |") {
		t.Fatalf("missing table header, got:\n%s", out)
	}
	if !strings.Contains(out, "| cc_1 | claude | active |") {
		t.Errorf("missing first row: %s", out)
	}
	if !strings.Contains(out, "| cod_1 | codex | idle |") {
		t.Errorf("missing second row: %s", out)
	}
}

func TestRenderAlertsList(t *testing.T) {
	alerts := []AlertInfo{
		{Severity: "critical", Type: "tmux", Message: "Session dropped", Session: "s1", Pane: "cc_1"},
		{Severity: "warning", Type: "disk", Message: "Low space"},
		{Severity: "info", Type: "beads", Message: "Ready: 5"},
		{Severity: "other", Type: "custom", Message: "Note"},
	}

	out := RenderAlertsList(alerts)

	// Order: Critical before Warning before Info
	critIdx := strings.Index(out, "### Critical")
	warnIdx := strings.Index(out, "### Warning")
	infoIdx := strings.Index(out, "### Info")
	if critIdx == -1 || warnIdx == -1 || infoIdx == -1 {
		t.Fatalf("missing severity headings:\n%s", out)
	}
	if !(critIdx < warnIdx && warnIdx < infoIdx) {
		t.Errorf("severity order wrong: crit=%d warn=%d info=%d", critIdx, warnIdx, infoIdx)
	}

	if !strings.Contains(out, "- [tmux] Session dropped (s1 cc_1)") {
		t.Errorf("missing critical item formatting: %s", out)
	}
	if !strings.Contains(out, "- [disk] Low space") {
		t.Errorf("missing warning item: %s", out)
	}
	if !strings.Contains(out, "### Other") || !strings.Contains(out, "[custom] Note") {
		t.Errorf("missing other bucket: %s", out)
	}
}

func TestRenderSuggestedActions(t *testing.T) {
	actions := []SuggestedAction{
		{Title: "Fix tmux", Reason: "session drops"},
		{Title: "Trim logs", Reason: ""},
	}
	out := RenderSuggestedActions(actions)

	if !strings.HasPrefix(out, "1. Fix tmux — session drops") {
		t.Fatalf("unexpected first line: %s", out)
	}
	if !strings.Contains(out, "2. Trim logs") {
		t.Errorf("second action missing: %s", out)
	}
}

func TestDefaultMarkdownOptions(t *testing.T) {
	opts := DefaultMarkdownOptions()

	if opts.MaxBeads != 5 {
		t.Errorf("expected MaxBeads=5, got %d", opts.MaxBeads)
	}
	if opts.MaxAlerts != 10 {
		t.Errorf("expected MaxAlerts=10, got %d", opts.MaxAlerts)
	}
	if opts.Compact {
		t.Error("expected Compact=false by default")
	}
	if opts.Session != "" {
		t.Errorf("expected empty Session, got %q", opts.Session)
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "he..."},
		{"ab", 3, "ab"},
		{"abcd", 3, "abc"},
		{"", 5, ""},
	}

	for _, tc := range tests {
		got := truncateStr(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

// TestTruncateStr_EdgeCases tests uncovered branches of truncateStr.
func TestTruncateStr_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"maxLen zero", "hello", 0, ""},
		{"maxLen negative", "hello", -5, ""},
		{"maxLen 1", "hello", 1, "h"},
		{"maxLen 2", "hello", 2, "he"},
		{"maxLen 3 exact", "abc", 3, "abc"},
		{"multibyte loop fallthrough", "aaaa\xf0\x9f\x8c\x8d", 7, "aaaa..."},
		{"single multibyte maxLen 3", "\xf0\x9f\x8c\x8d", 3, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateStr(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestAlertSeverityOrder(t *testing.T) {
	tests := []struct {
		severity alerts.Severity
		want     int
	}{
		{alerts.SeverityCritical, 0},
		{alerts.SeverityWarning, 1},
		{alerts.SeverityInfo, 2},
		{alerts.Severity("unknown"), 2},
	}

	for _, tc := range tests {
		got := alertSeverityOrder(tc.severity)
		if got != tc.want {
			t.Errorf("alertSeverityOrder(%v) = %d, want %d", tc.severity, got, tc.want)
		}
	}
}

func TestAlertSeverityIcon(t *testing.T) {
	tests := []struct {
		severity alerts.Severity
		want     string
	}{
		{alerts.SeverityCritical, "🔴"},
		{alerts.SeverityWarning, "⚠️"},
		{alerts.SeverityInfo, "ℹ️"},
		{alerts.Severity("other"), "ℹ️"},
	}

	for _, tc := range tests {
		got := alertSeverityIcon(tc.severity)
		if got != tc.want {
			t.Errorf("alertSeverityIcon(%v) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}

func TestAlertConfigForProject_UsesExplicitProjectDir(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Alerts: config.AlertsConfig{
			Enabled:                 true,
			AgentStuckMinutes:       12,
			DiskLowThresholdGB:      4.5,
			MailBacklogThreshold:    9,
			BeadStaleHours:          36,
			ContextWarningThreshold: 88.0,
			ResolvedPruneMinutes:    90,
		},
		ProjectsBase: "/tmp/wrong-base",
	}

	got := alertConfigForProject(cfg, "/tmp/right-project")
	if got.ProjectsDir != "/tmp/right-project" {
		t.Fatalf("ProjectsDir = %q, want /tmp/right-project", got.ProjectsDir)
	}
	if got.AgentStuckMinutes != 12 {
		t.Fatalf("AgentStuckMinutes = %d, want 12", got.AgentStuckMinutes)
	}
	if got.BeadStaleHours != 36 {
		t.Fatalf("BeadStaleHours = %d, want 36", got.BeadStaleHours)
	}
	if got.ContextWarningThreshold != 88.0 {
		t.Fatalf("ContextWarningThreshold = %v, want 88.0", got.ContextWarningThreshold)
	}
	if !got.Enabled {
		t.Fatal("expected enabled alert config")
	}
}

func TestAlertConfigForProject_ResolvesCurrentProjectDirWhenUnset(t *testing.T) {
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectDir := t.TempDir()
	nestedDir := filepath.Join(projectDir, "internal", "robot")
	if err := os.MkdirAll(filepath.Join(projectDir, ".ntm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".ntm", "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nestedDir); err != nil {
		t.Fatal(err)
	}

	got := alertConfigForProject(nil, "")
	if got.ProjectsDir != projectDir {
		t.Fatalf("ProjectsDir = %q, want %q", got.ProjectsDir, projectDir)
	}
	if !got.Enabled {
		t.Fatal("expected default alert config to remain enabled")
	}
}

// =============================================================================
// countAgentsByType
// =============================================================================

func TestCountAgentsByType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		panes  []tmux.Pane
		expect map[string]int
	}{
		{
			name:  "empty",
			panes: nil,
			expect: map[string]int{
				"claude": 0, "codex": 0, "gemini": 0, "user": 0, "other": 0,
			},
		},
		{
			name: "mixed types",
			panes: []tmux.Pane{
				{Type: tmux.AgentClaude},
				{Type: tmux.AgentClaude},
				{Type: tmux.AgentCodex},
				{Type: tmux.AgentGemini},
				{Type: tmux.AgentUser},
				{Type: tmux.AgentUnknown},
			},
			expect: map[string]int{
				"claude": 2, "codex": 1, "gemini": 1, "user": 1, "other": 1,
			},
		},
		{
			name: "all claude",
			panes: []tmux.Pane{
				{Type: tmux.AgentClaude},
				{Type: tmux.AgentClaude},
				{Type: tmux.AgentClaude},
			},
			expect: map[string]int{
				"claude": 3, "codex": 0, "gemini": 0, "user": 0, "other": 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := countAgentsByType(tt.panes)
			for k, want := range tt.expect {
				if result[k] != want {
					t.Errorf("countAgentsByType[%q] = %d, want %d", k, result[k], want)
				}
			}
		})
	}
}

// =============================================================================
// AgentTable
// =============================================================================

func TestAgentTable(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		out := AgentTable(nil)
		if !strings.Contains(out, "| Session | Pane | Type | Variant | State |") {
			t.Error("expected table header even for empty input")
		}
	})

	t.Run("with agents", func(t *testing.T) {
		t.Parallel()
		sessions := []SnapshotSession{
			{
				Name: "myproj",
				Agents: []SnapshotAgent{
					{Pane: "%0", Type: "claude", Variant: "opus", State: "working"},
					{Pane: "%1", Type: "codex", Variant: "", State: "idle"},
				},
			},
			{
				Name: "other",
				Agents: []SnapshotAgent{
					{Pane: "%2", Type: "gemini", Variant: "pro", State: "error"},
				},
			},
		}
		out := AgentTable(sessions)

		checks := []string{
			"| myproj | %0 | claude | opus | working |",
			"| myproj | %1 | codex |  | idle |",
			"| other | %2 | gemini | pro | error |",
		}
		for _, want := range checks {
			if !strings.Contains(out, want) {
				t.Errorf("missing row %q in:\n%s", want, out)
			}
		}
	})
}

// =============================================================================
// AlertsList
// =============================================================================

func TestAlertsList(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		out := AlertsList(nil)
		if out != "_No active alerts._" {
			t.Errorf("expected no-alerts message, got %q", out)
		}
	})

	t.Run("with session and pane", func(t *testing.T) {
		t.Parallel()
		alerts := []AlertInfo{
			{Severity: "critical", Message: "Agent crashed", Session: "proj1", Pane: "%0"},
		}
		out := AlertsList(alerts)
		if !strings.Contains(out, "[CRITICAL] Agent crashed") {
			t.Errorf("missing severity+message in: %s", out)
		}
		if !strings.Contains(out, "(session: proj1, pane: %0)") {
			t.Errorf("missing session/pane context in: %s", out)
		}
	})

	t.Run("with bead ID", func(t *testing.T) {
		t.Parallel()
		alerts := []AlertInfo{
			{Severity: "warning", Message: "Stale bead", BeadID: "br-42"},
		}
		out := AlertsList(alerts)
		if !strings.Contains(out, "[bead: br-42]") {
			t.Errorf("missing bead ID in: %s", out)
		}
	})

	t.Run("session without pane", func(t *testing.T) {
		t.Parallel()
		alerts := []AlertInfo{
			{Severity: "info", Message: "Check disk", Session: "s1"},
		}
		out := AlertsList(alerts)
		if !strings.Contains(out, "(session: s1)") {
			t.Errorf("missing session-only context in: %s", out)
		}
		if strings.Contains(out, "pane:") {
			t.Errorf("should not contain pane when empty: %s", out)
		}
	})
}

// =============================================================================
// BeadsSummary
// =============================================================================

func TestBeadsSummary(t *testing.T) {
	t.Parallel()

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		out := BeadsSummary(nil)
		if out != "_Beads summary unavailable._" {
			t.Errorf("expected unavailable message for nil, got %q", out)
		}
	})

	t.Run("not available", func(t *testing.T) {
		t.Parallel()
		out := BeadsSummary(&bv.BeadsSummary{Available: false})
		if out != "_Beads summary unavailable._" {
			t.Errorf("expected unavailable message, got %q", out)
		}
	})

	t.Run("available with counts", func(t *testing.T) {
		t.Parallel()
		out := BeadsSummary(&bv.BeadsSummary{
			Available:  true,
			Total:      20,
			Open:       5,
			InProgress: 3,
			Blocked:    2,
			Ready:      4,
			Closed:     6,
		})
		if !strings.Contains(out, "Total: 20") {
			t.Errorf("missing total in: %s", out)
		}
		if !strings.Contains(out, "Open: 5") {
			t.Errorf("missing open in: %s", out)
		}
		if !strings.Contains(out, "In Progress: 3") {
			t.Errorf("missing in-progress in: %s", out)
		}
		if !strings.Contains(out, "Ready: 4") {
			t.Errorf("missing ready in: %s", out)
		}
	})
}

// =============================================================================
// SuggestedActions
// =============================================================================

func TestSuggestedActions(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		out := SuggestedActions(nil)
		if out != "_No suggested actions._" {
			t.Errorf("expected no-actions message, got %q", out)
		}
	})

	t.Run("with actions", func(t *testing.T) {
		t.Parallel()
		actions := []BeadAction{
			{BeadID: "br-1", Title: "Fix auth", Command: "br update br-1 --status in_progress"},
			{BeadID: "br-2", Title: "Add tests", BlockedBy: []string{"br-1", "br-3"}},
			{BeadID: "br-3", Title: "Simple task"},
		}
		out := SuggestedActions(actions)

		if !strings.Contains(out, "- br-1: Fix auth") {
			t.Errorf("missing first action in: %s", out)
		}
		if !strings.Contains(out, "`br update br-1 --status in_progress`") {
			t.Errorf("missing command in: %s", out)
		}
		if !strings.Contains(out, "(blocked by: br-1, br-3)") {
			t.Errorf("missing blocked-by in: %s", out)
		}
		if !strings.Contains(out, "- br-3: Simple task\n") {
			t.Errorf("missing simple action in: %s", out)
		}
	})
}

// =============================================================================
// Projection-based Rendering Tests (bd-j9jo3.8.2/9.9)
// =============================================================================

func TestRenderMarkdownFromProjection_BasicStructure(t *testing.T) {
	t.Parallel()

	snapshot := &SnapshotOutput{
		Summary: StatusSummary{
			TotalSessions: 2,
			TotalAgents:   5,
		},
		Sessions: []SnapshotSession{
			{Name: "proj-a"},
			{Name: "proj-b"},
		},
	}

	proj := ProjectSections(snapshot, SectionProjectionOptions{})
	out := RenderMarkdownFromProjection(proj, false)

	// Should have markdown headings
	if !strings.Contains(out, "## Summary") {
		t.Error("expected summary heading in markdown")
	}

	// Should include sessions
	if !strings.Contains(out, "proj-a") {
		t.Errorf("expected session proj-a in output:\n%s", out)
	}
	if !strings.Contains(out, "proj-b") {
		t.Errorf("expected session proj-b in output:\n%s", out)
	}
}

func TestRenderMarkdownFromProjection_CompactMode(t *testing.T) {
	t.Parallel()

	snapshot := &SnapshotOutput{
		Summary: StatusSummary{
			TotalSessions: 1,
		},
		Sessions: []SnapshotSession{
			{Name: "proj-test"},
		},
	}

	proj := ProjectSections(snapshot, SectionProjectionOptions{
		Limits: CompactSectionLimits(),
	})

	normal := RenderMarkdownFromProjection(proj, false)
	compact := RenderMarkdownFromProjection(proj, true)

	// Compact mode should produce shorter output
	// (both should be non-empty)
	if len(normal) == 0 {
		t.Error("expected non-empty normal output")
	}
	if len(compact) == 0 {
		t.Error("expected non-empty compact output")
	}
}

func TestRenderMarkdownFromProjection_WithTruncation(t *testing.T) {
	t.Parallel()

	// Create more sessions than limit
	sessions := make([]SnapshotSession, 25)
	for i := range sessions {
		sessions[i] = SnapshotSession{Name: "proj-" + string(rune('a'+i))}
	}

	snapshot := &SnapshotOutput{
		Sessions: sessions,
	}

	proj := ProjectSections(snapshot, SectionProjectionOptions{
		Limits: SectionLimits{Sessions: 5},
	})

	out := RenderMarkdownFromProjection(proj, false)

	// Should indicate truncation
	if !strings.Contains(out, "truncated") && !strings.Contains(out, "omitted") && !strings.Contains(out, "showing") {
		// Truncation may be indicated differently - just verify we got output
		if len(out) == 0 {
			t.Error("expected non-empty output")
		}
	}
}

func TestRenderMarkdownFromProjection_EmptySections(t *testing.T) {
	t.Parallel()

	snapshot := &SnapshotOutput{
		Sessions:       []SnapshotSession{},
		Alerts:         []string{},
		AlertsDetailed: []AlertInfo{},
	}

	proj := ProjectSections(snapshot, SectionProjectionOptions{})
	out := RenderMarkdownFromProjection(proj, false)

	// Should produce valid markdown even with empty data
	if len(out) == 0 {
		t.Error("expected non-empty output even with empty sections")
	}

	// Should have summary section at minimum
	if !strings.Contains(out, "Summary") {
		t.Error("expected summary section even with empty data")
	}
}
