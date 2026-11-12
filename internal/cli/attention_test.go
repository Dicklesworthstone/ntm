package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func seedTestAttentionStore(t *testing.T) (*state.Store, int64) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", xdg)

	store, err := state.Open("")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}

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
