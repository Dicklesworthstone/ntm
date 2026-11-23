package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func seedTestAttentionStore(t *testing.T) (*state.Store, int64) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	store := openFastCLIStateStore(t, "")

	cursor, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            time.Now().UTC(),
		SessionName:   "proj-alpha",
		Pane:          "2",
		Category:      "alert",
		EventType:     "agent_stuck",
		Source:        "health_monitor",
		Actionability: state.ActionabilityActionRequired,
		Severity:      state.SeverityWarning,
		Summary:       "Agent cc_2 stuck for 120s",
		DedupKey:      "alert:agent_stuck:pane-def456",
	})
	if err != nil {
		store.Close()
		t.Fatalf("AppendAttentionEvent: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, cursor
}

func execAttentionCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newAttentionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestAttentionCLILifecycle_ListAckSnoozePinDismissRestore(t *testing.T) {
	store, cursor := seedTestAttentionStore(t)
	cursorRef := fmt.Sprintf("%d", cursor)

	// 1. Initial list surfaces the item in "new" state with non-null items slice.
	out, err := execAttentionCLI(t, "list", "--robot")
	if err != nil {
		t.Fatalf("attention list --robot error: %v\noutput: %s", err, out)
	}
	var listResp AttentionListResponse
	if err := json.Unmarshal([]byte(out), &listResp); err != nil {
		t.Fatalf("decode list JSON: %v\nraw: %s", err, out)
	}
	if !listResp.Success || listResp.Count != 1 || len(listResp.Items) != 1 {
		t.Fatalf("expected 1 surfaced item, got %+v", listResp)
	}
	if listResp.Items[0].State != string(state.AttentionStateNew) {
		t.Fatalf("expected initial state new, got %q", listResp.Items[0].State)
	}

	// 2. Acknowledge by item key (exercises docs/robot-contract-examples.md syntax).
	out, err = execAttentionCLI(t, "ack", "alert:agent_stuck:pane-def456", "--robot", "--reason", "investigating pane 2")
	if err != nil {
		t.Fatalf("attention ack --robot error: %v\noutput: %s", err, out)
	}
	var mutResp AttentionMutationResponse
	if err := json.Unmarshal([]byte(out), &mutResp); err != nil {
		t.Fatalf("decode ack JSON: %v\nraw: %s", err, out)
	}
	if !mutResp.Success || mutResp.CurrentState != string(state.AttentionStateAcknowledged) {
		t.Fatalf("expected acknowledged state, got %+v", mutResp)
	}

	// 3. Snooze by cursor with relative duration (+45m) hides it from default list.
	out, err = execAttentionCLI(t, "snooze", cursorRef, "--until", "+45m", "--robot")
	if err != nil {
		t.Fatalf("attention snooze error: %v\noutput: %s", err, out)
	}
	out, err = execAttentionCLI(t, "list", "--robot")
	if err != nil {
		t.Fatalf("attention list after snooze error: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &listResp); err != nil {
		t.Fatalf("decode list after snooze: %v", err)
	}
	if listResp.Items == nil {
		t.Fatal("items slice must serialize as [] and never null")
	}
	if listResp.Count != 0 {
		t.Fatalf("expected snoozed item hidden from default list, got %d", listResp.Count)
	}

	// 4. Pinning overrides snooze hiding so pinned items always surface.
	if _, err := execAttentionCLI(t, "pin", cursorRef, "--robot"); err != nil {
		t.Fatalf("attention pin error: %v", err)
	}
	out, _ = execAttentionCLI(t, "list", "--robot")
	_ = json.Unmarshal([]byte(out), &listResp)
	if listResp.Count != 1 || !listResp.Items[0].Pinned {
		t.Fatalf("expected pinned item to surface, got %+v", listResp)
	}

	// 5. Unpin + dismiss + restore round-trip verified in durable SQLite store.
	if _, err := execAttentionCLI(t, "unpin", cursorRef, "--robot"); err != nil {
		t.Fatalf("attention unpin error: %v", err)
	}
	if _, err := execAttentionCLI(t, "dismiss", cursorRef, "--robot"); err != nil {
		t.Fatalf("attention dismiss error: %v", err)
	}
	stored, err := store.GetAttentionItemStateByCursor(cursor)
	if err != nil || stored == nil || stored.State != state.AttentionStateDismissed {
		t.Fatalf("expected dismissed in SQLite, got state=%+v err=%v", stored, err)
	}

	if _, err := execAttentionCLI(t, "restore", cursorRef, "--robot"); err != nil {
		t.Fatalf("attention restore error: %v", err)
	}
	stored, err = store.GetAttentionItemStateByCursor(cursor)
	if err != nil || stored == nil || stored.State != state.AttentionStateNew {
		t.Fatalf("expected restored to new in SQLite, got state=%+v err=%v", stored, err)
	}

	// 6. Escalate sets priority override.
	if _, err := execAttentionCLI(t, "escalate", cursorRef, "--priority", "critical", "--expires", "+1h", "--robot"); err != nil {
		t.Fatalf("attention escalate error: %v", err)
	}
	stored, err = store.GetAttentionItemStateByCursor(cursor)
	if err != nil || stored == nil || stored.OverridePriority != "critical" || stored.OverrideExpiresAt == nil {
		t.Fatalf("expected critical escalation in SQLite, got %+v (err=%v)", stored, err)
	}
}

func TestAttentionCLIValidationErrors(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "cfg"))
	t.Setenv("HOME", xdg)

	// Snooze without --until fails with INVALID_FLAG JSON envelope.
	out, err := execAttentionCLI(t, "snooze", "alert:test", "--robot")
	if err == nil {
		t.Fatal("expected error when --until is missing")
	}
	var errEnv map[string]any
	if decErr := json.Unmarshal([]byte(out), &errEnv); decErr != nil {
		t.Fatalf("expected valid JSON error envelope, got %v\nraw: %s", decErr, out)
	}
	if errEnv["error_code"] != "INVALID_FLAG" {
		t.Fatalf("expected INVALID_FLAG, got %+v", errEnv)
	}

	// Unknown numeric cursor returns NOT_FOUND.
	out, err = execAttentionCLI(t, "ack", "999999", "--robot")
	if err == nil {
		t.Fatal("expected error for unknown numeric cursor")
	}
	errEnv = nil
	if decErr := json.Unmarshal([]byte(out), &errEnv); decErr != nil {
		t.Fatalf("expected valid JSON error envelope, got %v\nraw: %s", decErr, out)
	}
	if errEnv["error_code"] != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND, got %+v", errEnv)
	}
}

func TestAttentionIncidentsCLILifecycle(t *testing.T) {
	store, cursor := seedTestAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	// 1. Empty store returns [] (never null) in JSON mode.
	out, err := execAttentionCLI(t, "incidents", "--robot")
	if err != nil {
		t.Fatalf("attention incidents --robot (empty) error: %v\nout: %s", err, out)
	}
	var emptyList AttentionIncidentsListResponse
	if err := json.Unmarshal([]byte(out), &emptyList); err != nil {
		t.Fatalf("decode empty incidents list: %v\nraw: %s", err, out)
	}
	if !emptyList.Success || emptyList.Count != 0 || emptyList.Incidents == nil {
		t.Fatalf("expected non-nil empty incidents slice, got %+v", emptyList)
	}

	// Seed two incidents and link the seeded attention event to inc-1.
	if err := store.CreateIncident(&state.Incident{
		ID:          "inc-1",
		Title:       "Agent cc_2 stuck loop",
		Fingerprint: "alert:agent_stuck:pane-2",
		Family:      "agent_stuck",
		Category:    "alert",
		Status:      state.IncidentStatusOpen,
		Severity:    state.SeverityError,
		AlertCount:  2,
		StartedAt:   now.Add(-2 * time.Minute),
		LastEventAt: now.Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateIncident(inc-1): %v", err)
	}
	if err := store.LinkEventToIncident("inc-1", cursor); err != nil {
		t.Fatalf("LinkEventToIncident: %v", err)
	}

	if err := store.CreateIncident(&state.Incident{
		ID:          "inc-2",
		Title:       "Mail bridge flap",
		Fingerprint: "source.outage:mail",
		Family:      "source.outage",
		Category:    "availability",
		Status:      state.IncidentStatusOpen,
		Severity:    state.SeverityWarning,
		AlertCount:  1,
		StartedAt:   now.Add(-1 * time.Minute),
		LastEventAt: now,
	}); err != nil {
		t.Fatalf("CreateIncident(inc-2): %v", err)
	}

	// 2. Human list + JSON list return both open incidents ordered newest-first.
	humanOut, err := execAttentionCLI(t, "incidents", "list")
	if err != nil {
		t.Fatalf("attention incidents list (human): %v\nout: %s", err, humanOut)
	}
	if !strings.Contains(humanOut, "inc-1") || !strings.Contains(humanOut, "inc-2") {
		t.Fatalf("expected human table to include inc-1 and inc-2, got:\n%s", humanOut)
	}

	out, err = execAttentionCLI(t, "incidents", "list", "--robot")
	if err != nil {
		t.Fatalf("attention incidents list --robot: %v", err)
	}
	var listResp AttentionIncidentsListResponse
	if err := json.Unmarshal([]byte(out), &listResp); err != nil {
		t.Fatalf("decode incidents list: %v", err)
	}
	if listResp.Count != 2 || listResp.Incidents[0].ID != "inc-2" || listResp.Incidents[1].ID != "inc-1" {
		t.Fatalf("unexpected incidents list: %+v", listResp)
	}

	// 3. Show returns incident metadata + linked event in JSON and human modes.
	out, err = execAttentionCLI(t, "incidents", "show", "inc-1", "--events-limit", "10", "--robot")
	if err != nil {
		t.Fatalf("attention incidents show --robot: %v\nout: %s", err, out)
	}
	var showResp AttentionIncidentShowResponse
	if err := json.Unmarshal([]byte(out), &showResp); err != nil {
		t.Fatalf("decode incident show: %v\nraw: %s", err, out)
	}
	if !showResp.Success || showResp.Incident == nil || showResp.Incident.ID != "inc-1" || len(showResp.Events) != 1 || showResp.Events[0].Cursor != cursor {
		t.Fatalf("unexpected incident show response: %+v", showResp)
	}
	humanShow, err := execAttentionCLI(t, "incidents", "show", "inc-1")
	if err != nil || !strings.Contains(humanShow, "Linked Events (1):") {
		t.Fatalf("unexpected human show: err=%v out=%s", err, humanShow)
	}

	// 4. Investigate transitions inc-1 to investigating with actor attribution.
	out, err = execAttentionCLI(t, "incidents", "investigate", "inc-1", "--by", "alice", "--reason", "checking logs", "--robot")
	if err != nil {
		t.Fatalf("incidents investigate: %v\nout: %s", err, out)
	}
	var mutResp AttentionIncidentMutationResponse
	if err := json.Unmarshal([]byte(out), &mutResp); err != nil {
		t.Fatalf("decode investigate JSON: %v", err)
	}
	if mutResp.PreviousStatus != "open" || mutResp.Status != string(state.IncidentStatusInvestigating) || mutResp.Incident.AcknowledgedBy != "alice" {
		t.Fatalf("unexpected investigate response: %+v", mutResp)
	}

	// 5. Resolve transitions inc-1 to resolved, hides it from default list, and repeat resolve is idempotent.
	out, err = execAttentionCLI(t, "incidents", "resolve", "inc-1", "--by", "alice", "--reason", "restarted pane", "--robot")
	if err != nil {
		t.Fatalf("incidents resolve: %v\nout: %s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &mutResp); err != nil {
		t.Fatalf("decode resolve JSON: %v", err)
	}
	if mutResp.Idempotent || mutResp.Status != string(state.IncidentStatusResolved) || mutResp.Incident.Resolution != "restarted pane" {
		t.Fatalf("unexpected first resolve response: %+v", mutResp)
	}

	// Repeat resolve is idempotent and preserves original resolution note & actor.
	out, err = execAttentionCLI(t, "incidents", "resolve", "inc-1", "--by", "bob", "--robot")
	if err != nil {
		t.Fatalf("incidents repeat resolve: %v\nout: %s", err, out)
	}
	var repeatResp AttentionIncidentMutationResponse
	if err := json.Unmarshal([]byte(out), &repeatResp); err != nil {
		t.Fatalf("decode repeat resolve JSON: %v", err)
	}
	if !repeatResp.Idempotent || repeatResp.Incident.ResolvedBy != "alice" || repeatResp.Incident.Resolution != "restarted pane" {
		t.Fatalf("expected idempotent repeat resolve preserving alice/restarted pane, got %+v", repeatResp)
	}

	// 6. Mute transitions inc-2 to muted; default list is now empty, while --all and --status surface resolved/muted.
	if _, err := execAttentionCLI(t, "incidents", "mute", "inc-2", "--reason", "expected maintenance", "--robot"); err != nil {
		t.Fatalf("incidents mute: %v", err)
	}

	out, _ = execAttentionCLI(t, "incidents", "list", "--robot")
	_ = json.Unmarshal([]byte(out), &listResp)
	if listResp.Count != 0 {
		t.Fatalf("expected 0 active incidents after resolve+mute, got %d", listResp.Count)
	}

	out, _ = execAttentionCLI(t, "incidents", "list", "--status", "resolved", "--robot")
	_ = json.Unmarshal([]byte(out), &listResp)
	if listResp.Count != 1 || listResp.Incidents[0].ID != "inc-1" {
		t.Fatalf("expected --status resolved to return inc-1, got %+v", listResp)
	}

	out, _ = execAttentionCLI(t, "incidents", "list", "--all", "--limit", "1", "--robot")
	_ = json.Unmarshal([]byte(out), &listResp)
	if listResp.Count != 1 || listResp.Incidents[0].ID != "inc-2" {
		t.Fatalf("expected --all --limit 1 to return newest incident inc-2, got %+v", listResp)
	}
}

func TestAttentionIncidentsCLIValidationAndNotFound(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "cfg"))
	t.Setenv("HOME", xdg)

	// Invalid --status returns INVALID_FLAG.
	out, err := execAttentionCLI(t, "incidents", "list", "--status", "bogus", "--robot")
	if err == nil {
		t.Fatal("expected error for invalid --status")
	}
	var errEnv map[string]any
	if decErr := json.Unmarshal([]byte(out), &errEnv); decErr != nil {
		t.Fatalf("expected JSON error envelope, got %v\nraw: %s", decErr, out)
	}
	if errEnv["error_code"] != "INVALID_FLAG" {
		t.Fatalf("expected INVALID_FLAG, got %+v", errEnv)
	}

	// Missing incident ID on show / resolve returns NOT_FOUND.
	for _, action := range [][]string{
		{"incidents", "show", "inc-missing", "--robot"},
		{"incidents", "resolve", "inc-missing", "--robot"},
	} {
		out, err = execAttentionCLI(t, action...)
		if err == nil {
			t.Fatalf("expected NOT_FOUND error for %v", action)
		}
		if !errors.Is(err, errJSONFailure) {
			t.Fatalf("expected errJSONFailure sentinel for %v so root.Execute does not emit duplicate JSON, got %v", action, err)
		}
		errEnv = nil
		if decErr := json.Unmarshal([]byte(out), &errEnv); decErr != nil {
			t.Fatalf("decode error envelope for %v: %v\nraw: %s", action, decErr, out)
		}
		if errEnv["error_code"] != "NOT_FOUND" {
			t.Fatalf("expected NOT_FOUND for %v, got %+v", action, errEnv)
		}
	}
}
