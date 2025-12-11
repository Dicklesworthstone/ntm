package robot

import (
	"strings"
	"testing"
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
