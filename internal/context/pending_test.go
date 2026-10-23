package context

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func makeRecoveryPending(projectDir string) *PendingRotation {
	const token = "recovery-execution"
	source := tmux.Pane{ID: "%1", Index: 1, Title: "test__cc_1_architect", PID: 123, Type: tmux.AgentClaude, Command: "node", RotationToken: token}
	replacement := tmux.Pane{ID: "%99", Index: 0, Title: "test__cc_2", PID: 321, Type: tmux.AgentClaude, Command: "node", RotationToken: token}
	return &PendingRotation{
		AgentID: source.Title, SessionName: "test", PaneID: source.ID, PanePID: source.PID, PaneType: string(source.Type),
		CreatedAt: time.Now(), TimeoutAt: time.Now().Add(time.Hour), WorkDir: projectDir,
		SelectedAction: ConfirmRotate, ExecutionID: token, ExecutionState: RotationStateFailed,
		Continuation: &RotationContinuation{Token: token, Phase: rotationHandoffStarted, Source: source, Replacement: replacement, ProjectDir: projectDir, Method: RotationManual},
	}
}

func TestPendingResolutionKeepsExecutionAndImmutableDecision(t *testing.T) {
	root := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(root, "pending.jsonl"))
	pending := makeRecoveryPending(root)
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.storagePath)
	for _, request := range []struct {
		token  string
		action RotationResolutionAction
		reason string
	}{{"stale", RotationResolutionAbort, "stop duplicate"}, {pending.ExecutionID, RotationResolutionAbort, "  "}, {pending.ExecutionID, "rotate", "invalid decision"}} {
		if _, release, err := store.BeginResolution(t.Context(), pending.AgentID, request.token, request.action, request.reason); err == nil {
			release()
			t.Fatalf("invalid recovery was accepted: %+v", request)
		}
	}
	after, _ := os.ReadFile(store.storagePath)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid decisions changed the journal")
	}
	claimed, release, err := store.BeginResolution(t.Context(), pending.AgentID, pending.ExecutionID, RotationResolutionAbort, "  replacement is abandoned  ")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ExecutionID != pending.ExecutionID || claimed.ExecutionState != pending.ExecutionState || claimed.Continuation.Resolution.Reason != "replacement is abandoned" {
		t.Fatalf("recovery changed execution identity or lost the decision: %+v", claimed)
	}
	decision := *claimed.Continuation.Resolution
	if decision.RecordedAt.IsZero() {
		t.Fatal("operator decision has no recorded time")
	}
	if err := store.SaveContinuation(t.Context(), claimed, nil); err == nil {
		t.Fatal("old executor erased the operator's decision")
	}
	changed := cloneRotationContinuation(claimed.Continuation)
	changed.Resolution.Reason = "rewritten"
	if err := store.SaveContinuation(t.Context(), claimed, changed); err == nil {
		t.Fatal("old executor rewrote the operator's decision")
	}
	claimed.Continuation.Phase = rotationAborted
	if err := store.SaveContinuation(t.Context(), claimed, claimed.Continuation); err != nil {
		t.Fatal(err)
	}
	outcome := RotationResult{Success: true, OldAgentID: pending.AgentID, OldPaneID: pending.PaneID, State: RotationStateAborted, ResolutionAction: RotationResolutionAbort}
	if err := store.FinishConfirmation(t.Context(), claimed, outcome); err != nil {
		t.Fatal(err)
	}
	release()
	for _, request := range []struct {
		action RotationResolutionAction
		reason string
	}{{RotationResolutionAccept, decision.Reason}, {RotationResolutionAbort, "changed reason"}} {
		if _, release, err := store.BeginResolution(t.Context(), pending.AgentID, pending.ExecutionID, request.action, request.reason); err == nil {
			release()
			t.Fatal("completed decision could be reversed or rewritten")
		}
	}
	replayed, release, err := store.BeginResolution(t.Context(), pending.AgentID, pending.ExecutionID, RotationResolutionAbort, decision.Reason)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Result == nil || *replayed.Result != outcome || !replayed.Continuation.Resolution.RecordedAt.Equal(decision.RecordedAt) {
		t.Fatalf("completed decision was not replayed: %+v", replayed)
	}
	release()
	fresh := makePending(pending.AgentID, pending.SessionName, pending.PaneID, time.Now().Add(time.Hour))
	fresh.PanePID, fresh.PaneType = pending.PanePID, pending.PaneType
	if err := store.Add(fresh); err != nil {
		t.Fatalf("completed abandonment prevented a new rotation of its surviving source: %v", err)
	}
}

func TestPendingResolutionUsesConfirmationFenceAndCancellableStoreLock(t *testing.T) {
	for _, blockedBy := range []string{"confirmation", "store-and-writer-mutex"} {
		t.Run(blockedBy, func(t *testing.T) {
			root := t.TempDir()
			store := NewPendingRotationStoreWithPath(filepath.Join(root, "pending.jsonl"))
			pending := makeRecoveryPending(root)
			if blockedBy == "confirmation" {
				pending.SelectedAction, pending.ExecutionState, pending.ExecutionID, pending.Continuation = "", "", "", nil
			}
			if err := store.Add(pending); err != nil {
				t.Fatal(err)
			}
			var unlock func()
			var err error
			if blockedBy == "confirmation" {
				pending, unlock, err = store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false)
			} else {
				unlock, err = store.lock(t.Context())
				pendingMu.Lock()
				defer pendingMu.Unlock()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, release, err := store.BeginResolution(ctx, pending.AgentID, pending.ExecutionID, RotationResolutionAbort, "interrupted owner")
				if release != nil {
					release()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("recovery bypassed %s: %v", blockedBy, err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery ignored cancellation while waiting for ownership")
			}
		})
	}
}

func TestPendingResolutionRejectsAmbiguousOrCorruptJournal(t *testing.T) {
	for _, corruption := range []string{"duplicate-agent", "duplicate-execution", "mismatched-source", "mismatched-token", "missing-replacement", "malformed"} {
		t.Run(corruption, func(t *testing.T) {
			root := t.TempDir()
			store := NewPendingRotationStoreWithPath(filepath.Join(root, "pending.jsonl"))
			pending := makeRecoveryPending(root)
			entry := FromPendingRotation(pending)
			switch corruption {
			case "mismatched-source":
				entry.Continuation.Source.PID++
			case "mismatched-token":
				entry.Continuation.Token = "different"
			case "missing-replacement":
				entry.Continuation.Replacement = tmux.Pane{}
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			data := append(encoded, '\n')
			if strings.HasPrefix(corruption, "duplicate") {
				if corruption == "duplicate-execution" {
					entry.AgentID = "other-agent"
				}
				duplicate, _ := json.Marshal(entry)
				data = append(data, append(duplicate, '\n')...)
			}
			if corruption == "malformed" {
				data = append(data, []byte("{bad\n")...)
			}
			if err := os.WriteFile(store.storagePath, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, release, err := store.BeginResolution(t.Context(), pending.AgentID, pending.ExecutionID, RotationResolutionAbort, "operator stop"); err == nil {
				release()
				t.Fatal("recovery accepted ambiguous durable evidence")
			}
			after, _ := os.ReadFile(store.storagePath)
			if !bytes.Equal(data, after) {
				t.Fatal("recovery rewrote corrupt evidence")
			}
		})
	}
}

func TestPendingConfirmationRetainsChoiceAndReplaysReceipt(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))
	pending.PanePID = 123
	pending.PaneType = "cc"
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	claimed, release, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SelectedAction != ConfirmRotate || claimed.ExecutionState != RotationStateInProgress || claimed.PanePID != 123 {
		t.Fatalf("claim did not preserve request identity and choice: %+v", claimed)
	}
	if err := store.Remove(pending.AgentID); err == nil {
		t.Fatal("ordinary removal consumed owned execution")
	}
	if err := store.Add(pending); err == nil {
		t.Fatal("re-enqueue overwrote owned execution")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	failure := RotationResult{OldAgentID: pending.AgentID, OldPaneID: "%3", State: RotationStateFailed, Error: "handoff delivery canceled; original preserved"}
	if err := store.FinishConfirmation(canceled, claimed, failure); err != nil {
		t.Fatalf("cancellation lost outcome: %v", err)
	}
	release()
	stored, err := store.Get(pending.AgentID)
	if err != nil || stored == nil || stored.SelectedAction != ConfirmRotate || stored.Result == nil || stored.Result.Error != failure.Error {
		t.Fatalf("failure receipt = %+v, %v", stored, err)
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false); err == nil {
		t.Fatal("failed operation silently repeated")
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmCompact, true); err == nil {
		t.Fatal("retry changed the selected action")
	}
	retried, release, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, true)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ExecutionID == claimed.ExecutionID {
		t.Fatal("retry reused execution ownership")
	}
	success := RotationResult{Success: true, OldAgentID: pending.AgentID, OldPaneID: "%3", NewPaneID: "%9", State: RotationStateCompleted}
	if err := store.FinishConfirmation(t.Context(), retried, success); err != nil {
		t.Fatal(err)
	}
	release()
	if pending, err := store.Get(pending.AgentID); err != nil || pending != nil {
		t.Fatalf("completed receipt remained actionable: %+v, %v", pending, err)
	}
	replay, release, err := store.BeginConfirmation(t.Context(), retried.AgentID, ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if replay.Result == nil || !replay.Result.Success || replay.Result.NewPaneID != "%9" || replay.ExecutionID != retried.ExecutionID {
		t.Fatalf("duplicate confirmation did not replay durable outcome: %+v", replay)
	}
}

func TestPendingConfirmationInterruptedClaimSurvivesTimeout(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("demo__cc_1", "demo", "%3", time.Now().Add(-time.Hour))
	pending.SelectedAction = ConfirmCompact
	pending.ExecutionState = RotationStateInProgress
	pending.ExecutionID = "interrupted-owner"
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(makePending("other__cc_1", "other", "%4", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(pending.AgentID)
	if err != nil || stored == nil || stored.IsExpired() {
		t.Fatalf("interrupted choice expired: %+v, %v", stored, err)
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmCompact, false); err == nil || !strings.Contains(err.Error(), "--retry") {
		t.Fatalf("interrupted attempt was not diagnosed: %v", err)
	}
}

// TestPendingConfirmationLapsesWithoutAction: a pending rotation request that
// passed its timeout simply lapses (bd-fka4c). It is no longer visible, it
// cannot be confirmed without an explicit retry, and nothing ran on its own.
func TestPendingConfirmationLapsesWithoutAction(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	pending := makePending("lapsed__cc_1", "lapsed", "%3", time.Now().Add(-time.Minute))
	if err := store.Add(pending); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(pending.AgentID); err != nil || got != nil {
		t.Fatalf("lapsed request still visible: %+v (err %v)", got, err)
	}
	if _, _, err := store.BeginConfirmation(t.Context(), pending.AgentID, ConfirmRotate, false); err == nil || !strings.Contains(err.Error(), "has expired") {
		t.Fatalf("confirming a lapsed request = %v, want an expiry error", err)
	}
}

func TestPendingConfirmationProcessHelper(t *testing.T) {
	path := os.Getenv("NTM_PENDING_CLAIM_TEST")
	if path == "" {
		return
	}
	store := NewPendingRotationStoreWithPath(path)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, release, err := store.BeginConfirmation(ctx, "demo__cc_1", ConfirmRotate, false)
	if release != nil {
		release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("independent process acquired live confirmation: %v", err)
	}
}

func TestPendingConfirmationExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)
	if err := store.Add(makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	_, release, err := store.BeginConfirmation(t.Context(), "demo__cc_1", ConfirmRotate, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPendingConfirmationProcessHelper$")
	child.Env = append(os.Environ(), "NTM_PENDING_CLAIM_TEST="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("independent process claim check: %v\n%s", err, output)
	}
}

func TestPendingRotationStore_GetForSessionContextHonorsCancellation(t *testing.T) {
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	if err := store.Add(makePending("demo__cc_1", "demo", "%3", time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Model a writer waiting for another process while holding pendingMu.
	// The reader must honor its deadline without first acquiring that mutex.
	pendingMu.Lock()
	defer pendingMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := store.GetForSessionContext(ctx, "demo")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked snapshot read = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot read ignored cancellation behind the writer mutex")
	}
}

func makePending(agentID, session, pane string, timeout time.Time) *PendingRotation {
	return &PendingRotation{
		AgentID:        agentID,
		SessionName:    session,
		PaneID:         pane,
		ContextPercent: 85.5,
		CreatedAt:      time.Now(),
		TimeoutAt:      timeout,
		WorkDir:        "/tmp/test",
	}
}

func TestNewPendingRotationStoreWithPath(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath("/tmp/test.jsonl")
	if store.storagePath != "/tmp/test.jsonl" {
		t.Errorf("StoragePath() = %q, want /tmp/test.jsonl", store.storagePath)
	}
}

func TestPendingRotationStore_AddAndGet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	// Get on empty store returns nil
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty store, got %+v", got)
	}

	// Add a pending rotation
	timeout := time.Now().Add(10 * time.Minute)
	p := makePending("agent-1", "sess-1", "1.1", timeout)
	if err := store.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Get it back
	got, err = store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", got.AgentID)
	}
	if got.SessionName != "sess-1" {
		t.Errorf("SessionName = %q, want sess-1", got.SessionName)
	}
}

func TestPendingRotationStore_AddUpdatesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	p1 := makePending("agent-1", "sess-1", "1.1", timeout)
	p1.ContextPercent = 80.0
	if err := store.Add(p1); err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	// Add again with different context percent — should replace
	p2 := makePending("agent-1", "sess-1", "1.1", timeout)
	p2.ContextPercent = 95.0
	if err := store.Add(p2); err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	// Should only have one entry
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(all))
	}
	if all[0].ContextPercent != 95.0 {
		t.Errorf("ContextPercent = %f, want 95.0", all[0].ContextPercent)
	}
}

func TestPendingRotationStore_Remove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-1", "1.2", timeout))

	// Remove agent-1
	if err := store.Remove("agent-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// agent-1 should be gone
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get after remove: %v", err)
	}
	if got != nil {
		t.Error("expected nil after remove")
	}

	// agent-2 should still be there
	got, err = store.Get("agent-2")
	if err != nil {
		t.Fatalf("Get agent-2: %v", err)
	}
	if got == nil {
		t.Error("expected agent-2 to still exist")
	}
}

func TestPendingRotationStore_GetAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-2", "2.1", timeout))
	_ = store.Add(makePending("agent-3", "sess-1", "1.3", timeout))

	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
}

func TestPendingRotationStore_GetAll_SkipsExpired(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	future := time.Now().Add(10 * time.Minute)
	past := time.Now().Add(-1 * time.Minute) // already expired

	_ = store.Add(makePending("agent-1", "sess-1", "1.1", future))
	_ = store.Add(makePending("agent-2", "sess-1", "1.2", past))

	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	// Only future entry should remain
	if len(all) != 1 {
		t.Fatalf("expected 1 non-expired entry, got %d", len(all))
	}
	if all[0].AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %q", all[0].AgentID)
	}
}

func TestPendingRotationStore_GetForSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPendingRotationStoreWithPath(filepath.Join(dir, "pending.jsonl"))

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store.Add(makePending("agent-2", "sess-2", "2.1", timeout))
	_ = store.Add(makePending("agent-3", "sess-1", "1.3", timeout))

	result, err := store.GetForSession("sess-1")
	if err != nil {
		t.Fatalf("GetForSession: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries for sess-1, got %d", len(result))
	}

	// None for non-existent session
	result, err = store.GetForSession("sess-99")
	if err != nil {
		t.Fatalf("GetForSession empty: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 entries for sess-99, got %d", len(result))
	}
}

func TestPendingRotationStore_Clear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	timeout := time.Now().Add(10 * time.Minute)
	_ = store.Add(makePending("agent-1", "sess-1", "1.1", timeout))

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected file to exist before clear")
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	// File should be gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to be removed after clear")
	}

	// Clear on already-cleared store should be no-op
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear on empty: %v", err)
	}
}

func TestPendingRotationStore_GetOnNonExistentFile(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath("/tmp/nonexistent-test-pending.jsonl")

	// Get should return nil, nil
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("Get nonexistent file: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent file")
	}

	// GetAll should return empty
	all, err := store.GetAll()
	if err != nil {
		t.Fatalf("GetAll nonexistent file: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 entries, got %d", len(all))
	}
}

func TestPendingRotationStore_MalformedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")

	// Write a mix of valid and malformed JSONL
	timeout := time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)
	content := `not-json
{"agent_id":"agent-1","session_name":"sess-1","pane_id":"1.1","context_percent":85.5,"created_at":"2026-01-01T00:00:00Z","timeout_at":"` + timeout + `","default_action":"rotate"}
{broken json
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store := NewPendingRotationStoreWithPath(path)
	if _, err := store.GetAll(); err == nil {
		t.Fatal("malformed durable state was accepted as an incomplete pending list")
	}
	if _, _, err := store.BeginConfirmation(t.Context(), "agent-1", ConfirmRotate, false); err == nil {
		t.Fatal("confirmation proceeded despite unobservable durable ownership")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != content {
		t.Fatalf("malformed durable state changed: %q, %v", data, err)
	}
}

func TestPendingRotationStore_AddFailsClosedOnReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	timeout := time.Now().Add(10 * time.Minute)
	if err := store.Add(makePending("agent-1", "sess-1", "1.1", timeout)); err != nil {
		t.Fatalf("Add initial entry: %v", err)
	}
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile initial: %v", err)
	}

	corrupted := append(append([]byte{}, initial...), []byte(strings.Repeat("x", 1024*1024+1))...)
	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatalf("WriteFile corrupted: %v", err)
	}

	err = store.Add(makePending("agent-2", "sess-1", "1.2", timeout))
	if err == nil {
		t.Fatal("expected Add to fail on scanner read error")
	}
	if !strings.Contains(err.Error(), "reading pending rotations") {
		t.Fatalf("expected read error context, got %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after failed Add: %v", err)
	}
	if !bytes.Equal(after, corrupted) {
		t.Fatal("Add rewrote pending rotations after a read error")
	}
}

func TestPendingRotationStore_AddReturnsMarshalError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	store := NewPendingRotationStoreWithPath(path)

	pending := makePending("agent-1", "sess-1", "1.1", time.Now().Add(10*time.Minute))
	pending.ContextPercent = math.NaN()

	err := store.Add(pending)
	if err == nil {
		t.Fatal("expected Add to fail when pending rotation cannot be marshaled")
	}
	if !strings.Contains(err.Error(), "marshaling pending rotation") {
		t.Fatalf("expected marshal error context, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected no committed pending file after marshal error, stat err=%v", statErr)
	}
}

func TestPendingRotationStore_AddRejectsNil(t *testing.T) {
	t.Parallel()
	store := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	if err := store.Add(nil); err == nil {
		t.Fatal("expected Add(nil) to return an error")
	}
}

func TestPendingRotationStore_Persistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")

	timeout := time.Now().Add(10 * time.Minute)

	// Write with one store instance
	store1 := NewPendingRotationStoreWithPath(path)
	_ = store1.Add(makePending("agent-1", "sess-1", "1.1", timeout))
	_ = store1.Add(makePending("agent-2", "sess-2", "2.1", timeout))

	// Read with a new store instance
	store2 := NewPendingRotationStoreWithPath(path)
	all, err := store2.GetAll()
	if err != nil {
		t.Fatalf("GetAll from new store: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries from persisted store, got %d", len(all))
	}
}

func TestStoredPendingRotation_ToPendingRotation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	stored := &StoredPendingRotation{
		AgentID:        "agent-1",
		SessionName:    "sess-1",
		PaneID:         "1.1",
		ContextPercent: 90.0,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		WorkDir:        "/projects/test",
	}

	p := stored.ToPendingRotation()
	if p.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", p.AgentID)
	}
	if !p.TimeoutAt.Equal(timeout) {
		t.Errorf("TimeoutAt = %v, want %v", p.TimeoutAt, timeout)
	}
	if p.WorkDir != "/projects/test" {
		t.Errorf("WorkDir = %q, want /projects/test", p.WorkDir)
	}
}

func TestFromPendingRotation_Fields(t *testing.T) {
	t.Parallel()
	now := time.Now()
	timeout := now.Add(5 * time.Minute)

	p := &PendingRotation{
		AgentID:        "agent-2",
		SessionName:    "sess-2",
		PaneID:         "2.2",
		ContextPercent: 75.5,
		CreatedAt:      now,
		TimeoutAt:      timeout,
		WorkDir:        "/projects/other",
	}

	stored := FromPendingRotation(p)
	if stored.AgentID != "agent-2" {
		t.Errorf("AgentID = %q, want agent-2", stored.AgentID)
	}
	if !stored.TimeoutAt.Equal(timeout) {
		t.Errorf("TimeoutAt = %v, want %v", stored.TimeoutAt, timeout)
	}
	if stored.WorkDir != "/projects/other" {
		t.Errorf("WorkDir = %q, want /projects/other", stored.WorkDir)
	}
}

// =============================================================================
// Global wrapper function tests
// =============================================================================

func TestAddPendingRotation_Global(t *testing.T) {
	// Not parallel: modifies package-level DefaultPendingRotationStore
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr := makePending("agent-global-1", "sess-g", "1.0", time.Now().Add(5*time.Minute))
	if err := AddPendingRotation(pr); err != nil {
		t.Fatalf("AddPendingRotation: %v", err)
	}

	got, err := GetPendingRotationByID("agent-global-1")
	if err != nil {
		t.Fatalf("GetPendingRotationByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil pending rotation")
	}
	if got.SessionName != "sess-g" {
		t.Errorf("SessionName = %q, want sess-g", got.SessionName)
	}
}

func TestRemovePendingRotation_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr := makePending("agent-rm-1", "sess-rm", "2.0", time.Now().Add(5*time.Minute))
	if err := AddPendingRotation(pr); err != nil {
		t.Fatalf("AddPendingRotation: %v", err)
	}

	if err := RemovePendingRotation("agent-rm-1"); err != nil {
		t.Fatalf("RemovePendingRotation: %v", err)
	}

	got, err := GetPendingRotationByID("agent-rm-1")
	if err != nil {
		t.Fatalf("GetPendingRotationByID after remove: %v", err)
	}
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestGetAllPendingRotations_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr1 := makePending("agent-all-1", "sess-1", "1.0", time.Now().Add(5*time.Minute))
	pr2 := makePending("agent-all-2", "sess-2", "2.0", time.Now().Add(5*time.Minute))
	_ = AddPendingRotation(pr1)
	_ = AddPendingRotation(pr2)

	all, err := GetAllPendingRotations()
	if err != nil {
		t.Fatalf("GetAllPendingRotations: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 pending rotations, got %d", len(all))
	}
}

func TestGetPendingRotationsForSession_Global(t *testing.T) {
	origStore := DefaultPendingRotationStore
	tmpStore := NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	DefaultPendingRotationStore = tmpStore
	t.Cleanup(func() { DefaultPendingRotationStore = origStore })

	pr1 := makePending("agent-sess-1", "target-sess", "1.0", time.Now().Add(5*time.Minute))
	pr2 := makePending("agent-sess-2", "other-sess", "2.0", time.Now().Add(5*time.Minute))
	pr3 := makePending("agent-sess-3", "target-sess", "3.0", time.Now().Add(5*time.Minute))
	_ = AddPendingRotation(pr1)
	_ = AddPendingRotation(pr2)
	_ = AddPendingRotation(pr3)

	forSession, err := GetPendingRotationsForSession("target-sess")
	if err != nil {
		t.Fatalf("GetPendingRotationsForSession: %v", err)
	}
	if len(forSession) != 2 {
		t.Errorf("expected 2 rotations for target-sess, got %d", len(forSession))
	}
}
