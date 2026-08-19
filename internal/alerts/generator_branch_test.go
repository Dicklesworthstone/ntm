package alerts

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// dummyPane returns a zero-value tmux.Pane for tests that need one.
func dummyPane() tmux.Pane {
	return tmux.Pane{ID: "%0", Title: "test"}
}

// =============================================================================
// truncateString — "all rune starts fit" fallthrough (line 350)
// =============================================================================

// TestTruncateString_AllRunesFit tests the fallthrough when all rune boundaries
// fit within targetLen but the string is still longer than maxLen.
// This covers the `return s[:prevI] + "..."` at the end of the loop (line 350).
func TestTruncateString_AllRunesFit(t *testing.T) {
	t.Parallel()

	// We need: len(s) > maxLen, but all rune start indices <= targetLen(=maxLen-3).
	// A string of length 5 with maxLen=4: targetLen=1.
	// Runes at i=0 (<=1, ok), i=1 (<=1, ok), i=2 (>1, returns s[:1]+"...").
	// That hits line 345 (inner return), not line 350.
	//
	// To hit line 350, the for-range loop must complete without the i > targetLen
	// check triggering. This happens when the LAST rune START is exactly at targetLen.
	// Example: "abcde" (len=5), maxLen=7: targetLen=4.
	// Rune starts: 0,1,2,3,4. Loop: i=0 (<=4), i=1 (<=4), ..., i=4 (<=4).
	// No i > 4, so loop completes. prevI=4. return s[:4]+"..." = "abcd..."
	// But len("abcde")=5 <= 7, so the len(s) <= maxLen guard returns early.
	//
	// We need: len(s) > maxLen AND last rune start <= targetLen.
	// Multi-byte chars: "aé" = 3 bytes. maxLen=5, targetLen=2.
	// Rune starts: 0 ('a'), 1 ('é' at byte 1, 2 bytes). i=0 (<=2), i=1 (<=2).
	// Loop completes. prevI=1. s[:1]+"..." = "a..." (4 bytes).
	// But len("aé")=3 <= 5. Guard returns early.
	//
	// Need: len(s) > maxLen, all rune starts fit.
	// "abc" (len=3) with multi-byte end: "abé" = 4 bytes. maxLen=6, targetLen=3.
	// Rune starts: 0,1,2. All <=3. Loop completes. prevI=2.
	// But len("abé")=4 <= 6. Guard returns early again.
	//
	// The key: string must have a multi-byte rune whose start <= targetLen
	// but whose end pushes len(s) > maxLen.
	// "é" at position 0: 2 bytes. String = "éb" (3 bytes).
	// maxLen=5, targetLen=2. Rune starts: 0,2. 0<=2, 2<=2. Loop completes.
	// prevI=2. But len("éb")=3 <= 5. Returns early.
	//
	// Actually, try: very long multibyte string.
	// "aaaa🌍" = 4 + 4 = 8 bytes. maxLen=7, targetLen=4.
	// Rune starts: 0,1,2,3,4. i=0..3 (<=4). i=4 (<=4).
	// Loop completes. prevI=4. return s[:4]+"..." = "aaaa..." (7 bytes). ✓
	// And len(s)=8 > 7. ✓ This should hit line 350!

	s := "aaaa\xf0\x9f\x8c\x8d" // "aaaa🌍" = 8 bytes
	got := truncateString(s, 7)
	want := "aaaa..."
	if got != want {
		t.Errorf("truncateString(%q, 7) = %q, want %q", s, got, want)
	}
	if len(got) > 7 {
		t.Errorf("result len = %d, want <= 7", len(got))
	}
}

// TestTruncateString_AllRunesFit2 is a second case: pure ASCII where last rune
// start == targetLen.
func TestTruncateString_AllRunesFit2(t *testing.T) {
	t.Parallel()

	// "abcdefgh" (8 bytes), maxLen=7, targetLen=4.
	// Rune starts: 0,1,2,3,4,5,6,7.
	// i=0..4 ok. i=5 > 4 → returns s[:4]+"..." = "abcd..." via line 345.
	// That's the inner return, not line 350.
	//
	// For line 350, need loop to exhaust. Pure ASCII won't do it because
	// there's always a rune start > targetLen when len(s) > maxLen.
	// Only multi-byte works. The test above covers it.
}

// =============================================================================
// AddAlert — update with non-nil Context on existing alert
// =============================================================================

func TestAddAlert_UpdateContext(t *testing.T) {
	t.Parallel()

	tracker := NewTracker(DefaultConfig())

	alert := Alert{
		ID:       "test-ctx-1",
		Type:     AlertAgentError,
		Severity: SeverityWarning,
		Source:   "test",
		Message:  "Test alert",
	}

	// Add initial alert without context
	tracker.AddAlert(alert)

	// Add same alert again with context — should update existing
	alert.Context = map[string]interface{}{"key": "value"}
	tracker.AddAlert(alert)

	active := tracker.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}
	if active[0].Context == nil {
		t.Error("Context should be updated to non-nil")
	}
	if active[0].Context["key"] != "value" {
		t.Errorf("Context[\"key\"] = %v, want \"value\"", active[0].Context["key"])
	}
	if active[0].Count != 2 {
		t.Errorf("Count = %d, want 2", active[0].Count)
	}
}

// =============================================================================
// GetAlertStrings — additional coverage
// =============================================================================

// =============================================================================
// detectErrorState — truncation of long matched line
// =============================================================================

func TestDetectErrorState_LongLine(t *testing.T) {
	t.Parallel()

	g := &Generator{config: DefaultConfig()}

	// Create lines where one has an error pattern in a very long line
	longLine := "error: " + strings.Repeat("x", 300)
	lines := []string{longLine}

	// Need a tmux.Pane — use zero value
	// detectErrorState takes lines directly, doesn't call tmux
	alert := g.detectErrorState("sess", dummyPane(), lines)
	if alert == nil {
		t.Fatal("expected alert for error pattern")
	}

	// The matched_line in context should be truncated
	matched, ok := alert.Context["matched_line"].(string)
	if !ok {
		t.Fatal("expected matched_line in context")
	}
	if len(matched) > 200 {
		t.Errorf("matched_line should be truncated to 200, got %d", len(matched))
	}
}

// TestDetectErrorState_MoreThan20Lines tests the len(checkLines) > 20 branch.
func TestDetectErrorState_MoreThan20Lines(t *testing.T) {
	t.Parallel()

	g := &Generator{config: DefaultConfig()}

	// Create 25 clean lines + error in last line
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "clean output line"
	}
	lines[24] = "fatal: something went wrong"

	alert := g.detectErrorState("sess", dummyPane(), lines)
	if alert == nil {
		t.Fatal("expected alert for fatal pattern in last 20 lines")
	}
}

// TestDetectErrorState_NoMatch tests no pattern match returns nil.
func TestDetectErrorState_NoMatch(t *testing.T) {
	t.Parallel()

	g := &Generator{config: DefaultConfig()}
	lines := []string{"all good here", "nothing wrong", "just working"}

	alert := g.detectErrorState("sess", dummyPane(), lines)
	if alert != nil {
		t.Errorf("expected nil alert for clean output, got %v", alert)
	}
}

func TestScanAgentSessions_PartialSessionFailurePreservesHealthyAlerts(t *testing.T) {
	t.Parallel()

	g := &Generator{config: DefaultConfig()}
	sessions := []tmux.Session{
		{Name: "healthy"},
		{Name: "broken"},
	}

	alerts, failedSources := g.scanAgentSessions(
		sessions,
		func(session string) ([]tmux.Pane, error) {
			if session == "broken" {
				return nil, assertErr("pane lookup failed")
			}
			return []tmux.Pane{{ID: "%1", Title: "healthy__cod_1", Type: tmux.AgentCodex}}, nil
		},
		func(paneID string, lines int) (string, error) {
			return "fatal: crashed badly", nil
		},
	)

	if len(failedSources) != 1 || failedSources[0] != agentAlertSource("broken") {
		t.Fatalf("failedSources = %#v, want [%q]", failedSources, agentAlertSource("broken"))
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert from healthy session, got %d", len(alerts))
	}
	if alerts[0].Session != "healthy" {
		t.Fatalf("expected alert from healthy session, got %q", alerts[0].Session)
	}
	if alerts[0].Source != agentAlertSource("healthy") {
		t.Fatalf("expected source %q, got %q", agentAlertSource("healthy"), alerts[0].Source)
	}
	if alerts[0].Type != AlertAgentError {
		t.Fatalf("expected AlertAgentError, got %s", alerts[0].Type)
	}
}

func TestScanAgentSessions_RespectsSessionFilterBeforePaneLookup(t *testing.T) {
	t.Parallel()

	g := &Generator{config: Config{Enabled: true, SessionFilter: "target"}}
	sessions := []tmux.Session{
		{Name: "target"},
		{Name: "ignored"},
	}

	called := make(map[string]int)
	alerts, failedSources := g.scanAgentSessions(
		sessions,
		func(session string) ([]tmux.Pane, error) {
			called[session]++
			return []tmux.Pane{{ID: "%1", Title: session + "__cc_1", Type: tmux.AgentClaude}}, nil
		},
		func(paneID string, lines int) (string, error) {
			return "all good", nil
		},
	)

	if len(failedSources) != 0 {
		t.Fatalf("did not expect failed sources, got %#v", failedSources)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts, got %d", len(alerts))
	}
	if called["target"] != 1 {
		t.Fatalf("expected target session to be scanned once, got %d", called["target"])
	}
	if called["ignored"] != 0 {
		t.Fatalf("expected ignored session to be skipped, got %d calls", called["ignored"])
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
