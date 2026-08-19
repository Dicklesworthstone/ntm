package robot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestPrintMarkdownSnapshotPropagatesTerminalFailure(t *testing.T) {
	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	snapshot := &SnapshotOutput{
		RobotResponse: NewErrorResponse(errors.New("snapshot unavailable"), ErrCodeInternalError, "retry snapshot"),
		Sessions:      []SnapshotSession{},
	}
	stdout, err := captureStdout(t, func() error {
		return printMarkdownSnapshot(snapshot, DefaultMarkdownOptions())
	})
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("markdown error = %T %v, want written exit-1 ProcessExitError", err, err)
	}

	var response SnapshotOutput
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("markdown failure is not JSON: %v\noutput=%q", err, stdout)
	}
	if response.Success || response.ErrorCode != ErrCodeInternalError || response.OutputFormat != string(FormatJSON) {
		t.Fatalf("markdown failure response = %+v", response.RobotResponse)
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
			got := truncateStr(tc.input, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestAlertConfigForProject_UsesExplicitProjectDir(t *testing.T) {

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

	projectDir := tempDirCanonical(t)
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

func TestSnapshotSessionCountsCanonicalizesAndKeepsNewerTypes(t *testing.T) {

	counts, states := snapshotSessionCounts([]SnapshotAgent{
		{Type: "openai-codex", State: "idle"},
		{Type: "google-gemini", State: "error"},
		{Type: "grok-build", State: "working"},
		{Type: "cursor", State: "working"},
		{Type: "ws", State: "busy"},
		{Type: "aider", State: "active"},
		{Type: "ollama", State: "idle"},
		{Type: "user", State: "idle"},
		{Type: "mystery", State: "error"},
	})

	if counts["codex"] != 1 || counts["gemini"] != 1 || counts["grok"] != 1 || counts["cursor"] != 1 || counts["windsurf"] != 1 || counts["aider"] != 1 || counts["ollama"] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if counts["user"] != 1 || counts["other"] != 1 {
		t.Fatalf("counts = %+v, want user=1 other=1", counts)
	}
	if states["idle"] != 2 || states["error"] != 2 || states["active"] != 4 {
		t.Fatalf("states = %+v, want idle=2 error=2 active=4", states)
	}
}

func TestFormatMarkdownAgentTypeCounts(t *testing.T) {

	got := formatMarkdownAgentTypeCounts(map[string]int{
		"claude":   1,
		"codex":    2,
		"grok":     1,
		"cursor":   1,
		"ollama":   1,
		"user":     1,
		"other":    1,
		"gemini":   0,
		"windsurf": 0,
		"aider":    0,
	})
	want := "cc:1 cod:2 grok:1 cur:1 oll:1 usr:1 oth:1"
	if got != want {
		t.Fatalf("formatMarkdownAgentTypeCounts() = %q, want %q", got, want)
	}
}

// =============================================================================
// AgentTable
// =============================================================================

// =============================================================================
// AlertsList
// =============================================================================

// =============================================================================
// BeadsSummary
// =============================================================================

// =============================================================================
// SuggestedActions
// =============================================================================

// =============================================================================
// Projection-based Rendering Tests (bd-j9jo3.8.2/9.9)
// =============================================================================

func TestRenderMarkdownFromSnapshotRendersLegacyAlerts(t *testing.T) {
	snapshot := &SnapshotOutput{
		Alerts:         []string{"failed to get panes for proj: tmux unavailable", "failed to list active incidents: unavailable"},
		AlertsDetailed: []AlertInfo{},
	}

	rendered, err := renderMarkdownFromSnapshot(snapshot, MarkdownOptions{IncludeSections: []string{"alerts"}, MaxAlerts: 1})
	if err != nil {
		t.Fatalf("renderMarkdownFromSnapshot error: %v", err)
	}
	if !strings.Contains(rendered, "### Alerts (2)") {
		t.Fatalf("markdown missing legacy alert count:\n%s", rendered)
	}
	if strings.Contains(rendered, "No active alerts") {
		t.Fatalf("markdown incorrectly reports all-clear:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- failed to get panes for proj: tmux unavailable") {
		t.Fatalf("markdown missing legacy alert message:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Truncated: showing 1 of 2 alerts") {
		t.Fatalf("markdown missing legacy alert truncation:\n%s", rendered)
	}
	if strings.Contains(rendered, "failed to list active incidents") {
		t.Fatalf("markdown rendered legacy alert beyond MaxAlerts:\n%s", rendered)
	}
}
