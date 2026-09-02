// Unit coverage for GetSend's durable idempotent operation claim branches
// (#245) — the claim block in robot.go starting at "Durable idempotent
// operation claim". The state-store methods and pure helpers are covered
// elsewhere; these tests exercise the BRANCH WIRING inside GetSend against a
// real projection store and a real (throwaway) tmux session, following the
// package convention established by TestPrintReplayRealTmuxDeliversOnce.
//
// Branch matrix:
//
//	fresh claim + dispatch     -> TestGetSendIdempotency_FreshClaimRecordsCompletedOutcome
//	replay completed success   -> TestGetSendIdempotency_ReplaysCompletedSuccessWithoutDispatch
//	replay completed failure   -> TestGetSendIdempotency_ReplaysCompletedFailureWithNewIDHint
//	conflicting binding        -> TestGetSendIdempotency_ConflictingBindingRejected
//	fresh in-progress claim    -> TestGetSendIdempotency_FreshInProgressClaimReportsInProgress
//	stale in-progress takeover -> TestGetSendIdempotency_StaleInProgressClaimTakenOver
//	preflight failure release  -> TestGetSendIdempotency_PreflightFailureReleasesClaim
//	no projection store        -> TestGetSendIdempotency_NoProjectionStoreNotImplemented
package robot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// installIdempotencyBranchStore opens a real migrated state store in a temp
// directory, installs it as the projection store, and restores the previous
// store on cleanup. Tests using it must not call t.Parallel(): the projection
// store is package-global state.
func installIdempotencyBranchStore(t *testing.T) *state.Store {
	t.Helper()

	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open state store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate state store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})
	return store
}

// installIdempotencyBranchFeed swaps in an isolated attention feed so the
// actuation events GetSend publishes do not leak into the global feed.
func installIdempotencyBranchFeed(t *testing.T) {
	t.Helper()

	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	t.Cleanup(func() { SetAttentionFeed(oldFeed) })
}

// createIdempotencyBranchSession creates a throwaway real tmux session with a
// single interactive bash pane and waits for the shell to render. It returns
// the session name and the pane's tmux ID (%N).
func createIdempotencyBranchSession(t *testing.T, tag string) (string, string) {
	t.Helper()

	session := fmt.Sprintf("ntm_idem_%s_%d", tag, time.Now().UnixNano())
	projectDir := t.TempDir()
	paneID, err := tmux.DefaultClient.Run(
		"new-session", "-d", "-s", session, "-c", projectDir,
		"-P", "-F", "#{pane_id}", "/bin/bash --noprofile --norc -i",
	)
	if err != nil {
		t.Fatalf("create idempotency test session: %v", err)
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		t.Fatal("create idempotency test session returned an empty pane ID")
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	shellDeadline := time.Now().Add(5 * time.Second)
	for {
		output, captureErr := tmux.CapturePaneOutput(paneID, 20)
		if captureErr == nil && strings.TrimSpace(output) != "" {
			break
		}
		if time.Now().After(shellDeadline) {
			t.Fatalf("timed out waiting for idempotency pane shell: output=%q err=%v", output, captureErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return session, paneID
}

// expectedSendTargetKey computes the canonical target address GetSend reports
// for the pane, matching dispatch's PaneRef.Canonical for the session's
// (single-window) topology.
func expectedSendTargetKey(t *testing.T, session, paneID string) string {
	t.Helper()

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("GetPanes(%s): %v", session, err)
	}
	for _, pane := range panes {
		if pane.ID == paneID {
			return strconv.Itoa(pane.Index)
		}
	}
	t.Fatalf("pane %s not found in session %s", paneID, session)
	return ""
}

func logSeededSendOperation(t *testing.T, label string, op *state.SendOperation) {
	t.Helper()
	if op == nil {
		t.Logf("%s: <nil>", label)
		return
	}
	t.Logf("%s: op_id=%s session=%s status=%s binding=%.12s created_at=%s outcome=%q",
		label, op.OperationID, op.SessionName, op.Status, op.BindingHash, op.CreatedAt.Format(time.RFC3339), op.OutcomeJSON)
}

func logSendEnvelopeSubset(t *testing.T, output *SendOutput) {
	t.Helper()
	opSummary := "<nil>"
	if output.Operation != nil {
		opSummary = fmt.Sprintf("{status=%s replayed=%v admissions=%d}",
			output.Operation.Status, output.Operation.Replayed, len(output.Operation.Admissions))
	}
	t.Logf("envelope: success=%v error_code=%q hint=%q targets=%v successful=%v operation=%s",
		output.Success, output.ErrorCode, output.Hint, output.Targets, output.Successful, opSummary)
}

// mustGetSendOperation reads the durable row for (opID, session); it may
// return nil when the row does not exist.
func mustGetSendOperation(t *testing.T, store *state.Store, opID, session string) *state.SendOperation {
	t.Helper()
	row, err := store.GetSendOperation(opID, session)
	if err != nil {
		t.Fatalf("GetSendOperation(%s, %s): %v", opID, session, err)
	}
	return row
}

// Branch (a): fresh claim + successful dispatch. The operation completes, the
// admissions are recorded as submitted, and the receipt is retrievable via
// GetSendReceipt.
func TestGetSendIdempotency_FreshClaimRecordsCompletedOutcome(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "fresh")
	targetKey := expectedSendTargetKey(t, session, paneID)

	opID := fmt.Sprintf("op-fresh-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-fresh-marker",
		Pane:           paneID,
		IdempotencyKey: opID,
	}
	t.Logf("seeded row: none (fresh claim); op_id=%s target=%s", opID, targetKey)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if !output.Success {
		t.Fatalf("Success = false, want true (error=%q code=%q)", output.Error, output.ErrorCode)
	}
	if len(output.Successful) != 1 || output.Successful[0] != targetKey {
		t.Fatalf("Successful = %v, want [%s]", output.Successful, targetKey)
	}
	if output.Operation == nil {
		t.Fatal("Operation is nil, want completed operation info")
	}
	if output.Operation.Status != state.SendOperationCompleted {
		t.Fatalf("Operation.Status = %q, want %q", output.Operation.Status, state.SendOperationCompleted)
	}
	if output.Operation.Replayed {
		t.Fatal("Operation.Replayed = true, want false for a fresh execution")
	}
	if len(output.Operation.Admissions) != 1 ||
		output.Operation.Admissions[0].Target != targetKey ||
		output.Operation.Admissions[0].State != AdmissionSubmitted {
		t.Fatalf("Operation.Admissions = %+v, want single submitted admission for %s",
			output.Operation.Admissions, targetKey)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil {
		t.Fatal("send operation row missing after completed dispatch")
	}
	if row.Status != state.SendOperationCompleted {
		t.Fatalf("row.Status = %q, want %q", row.Status, state.SendOperationCompleted)
	}
	if row.BindingHash != sendOperationBindingHash(opts) {
		t.Fatalf("row.BindingHash = %q, want binding hash of the sent command", row.BindingHash)
	}
	if row.CompletedAt == nil {
		t.Fatal("row.CompletedAt is nil, want a completion timestamp")
	}
	var outcome sendOperationOutcome
	if err := json.Unmarshal([]byte(row.OutcomeJSON), &outcome); err != nil {
		t.Fatalf("decode stored outcome: %v (json=%q)", err, row.OutcomeJSON)
	}
	if !outcome.Success || len(outcome.Successful) != 1 || outcome.Successful[0] != targetKey {
		t.Fatalf("stored outcome = %+v, want successful delivery to %s", outcome, targetKey)
	}

	receipt, err := GetSendReceipt(opID)
	if err != nil {
		t.Fatalf("GetSendReceipt returned error: %v", err)
	}
	if !receipt.Success {
		t.Fatalf("receipt.Success = false (error=%q code=%q)", receipt.Error, receipt.ErrorCode)
	}
	if receipt.Session != session {
		t.Fatalf("receipt.Session = %q, want %q", receipt.Session, session)
	}
	if receipt.Operation == nil || receipt.Operation.Status != state.SendOperationCompleted {
		t.Fatalf("receipt.Operation = %+v, want completed operation", receipt.Operation)
	}
	if receipt.Outcome == nil || !receipt.Outcome.Success ||
		len(receipt.Outcome.Successful) != 1 || receipt.Outcome.Successful[0] != targetKey {
		t.Fatalf("receipt.Outcome = %+v, want recorded successful delivery to %s", receipt.Outcome, targetKey)
	}
	t.Logf("receipt: session=%s operation.status=%s outcome.success=%v",
		receipt.Session, receipt.Operation.Status, receipt.Outcome.Success)
}

// seedCompletedSendOperation claims and completes a row so a later GetSend
// with the same (opID, session) observes a completed operation.
func seedCompletedSendOperation(t *testing.T, store *state.Store, opID, session, bindingHash string, outcome sendOperationOutcome) *state.SendOperation {
	t.Helper()

	payloadSHA, payloadBytes := sendPayloadDigest("seeded payload")
	_, claimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID:   opID,
		SessionName:   session,
		BindingHash:   bindingHash,
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
	})
	if err != nil {
		t.Fatalf("seed ClaimSendOperation: %v", err)
	}
	if !claimed {
		t.Fatalf("seed claim for %s was not fresh", opID)
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal seeded outcome: %v", err)
	}
	if err := store.CompleteSendOperation(opID, session, string(data), time.Now().UTC()); err != nil {
		t.Fatalf("seed CompleteSendOperation: %v", err)
	}
	row := mustGetSendOperation(t, store, opID, session)
	if row == nil || row.Status != state.SendOperationCompleted {
		t.Fatalf("seeded row = %+v, want completed", row)
	}
	return row
}

// Branch (b): a completed row with a MATCHING binding hash replays the stored
// outcome verbatim — Operation.Replayed=true and no dispatch is attempted.
func TestGetSendIdempotency_ReplaysCompletedSuccessWithoutDispatch(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "replay")

	const replayMarker = "ntm-idem-replay-must-not-appear"
	opID := fmt.Sprintf("op-replay-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo " + replayMarker,
		Pane:           paneID,
		IdempotencyKey: opID,
	}

	// Sentinel values no real dispatch in this session could produce: they
	// prove the output came from the stored record, not a fresh send.
	seededSentAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	seeded := seedCompletedSendOperation(t, store, opID, session, sendOperationBindingHash(opts), sendOperationOutcome{
		Success:    true,
		SentAt:     seededSentAt,
		Targets:    []string{"sentinel-target"},
		Successful: []string{"sentinel-target"},
		Failed:     []SendError{},
		Admissions: []SendAdmission{{Target: "sentinel-target", State: AdmissionSubmitted}},
	})
	logSeededSendOperation(t, "seeded row", seeded)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if !output.Success {
		t.Fatalf("Success = false, want replayed success (error=%q code=%q)", output.Error, output.ErrorCode)
	}
	if output.Operation == nil || !output.Operation.Replayed {
		t.Fatalf("Operation = %+v, want Replayed=true", output.Operation)
	}
	if output.Operation.Status != state.SendOperationCompleted {
		t.Fatalf("Operation.Status = %q, want %q", output.Operation.Status, state.SendOperationCompleted)
	}
	if len(output.Successful) != 1 || output.Successful[0] != "sentinel-target" {
		t.Fatalf("Successful = %v, want the stored [sentinel-target]", output.Successful)
	}
	if !output.SentAt.Equal(seededSentAt) {
		t.Fatalf("SentAt = %v, want stored %v", output.SentAt, seededSentAt)
	}
	if len(output.Operation.Admissions) != 1 || output.Operation.Admissions[0].Target != "sentinel-target" {
		t.Fatalf("Operation.Admissions = %+v, want stored sentinel admission", output.Operation.Admissions)
	}

	// No dispatch attempted: the echo marker must never reach the pane.
	capture, captureErr := tmux.CapturePaneOutput(paneID, 50)
	if captureErr != nil {
		t.Fatalf("CapturePaneOutput: %v", captureErr)
	}
	if strings.Contains(capture, replayMarker) {
		t.Fatalf("pane output contains %q — replay dispatched keystrokes:\n%s", replayMarker, capture)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil || row.Status != state.SendOperationCompleted || row.OutcomeJSON != seeded.OutcomeJSON {
		t.Fatalf("row changed by replay: %+v (seeded outcome=%q)", row, seeded.OutcomeJSON)
	}
}

// Branch (b, failure flavor): a replayed FAILURE is terminal for the
// operation ID and the envelope hints that a new operation ID is required.
func TestGetSendIdempotency_ReplaysCompletedFailureWithNewIDHint(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "replayfail")

	opID := fmt.Sprintf("op-replayfail-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-replay-failure",
		Pane:           paneID,
		IdempotencyKey: opID,
	}

	seeded := seedCompletedSendOperation(t, store, opID, session, sendOperationBindingHash(opts), sendOperationOutcome{
		Success:    false,
		SentAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Targets:    []string{"sentinel-target"},
		Successful: []string{},
		Failed:     []SendError{{Pane: "sentinel-target", Error: "seeded delivery failure"}},
		Admissions: []SendAdmission{{Target: "sentinel-target", State: AdmissionRejected, Error: "seeded delivery failure"}},
		Error:      "seeded delivery failure",
		ErrorCode:  ErrCodeInternalError,
	})
	logSeededSendOperation(t, "seeded row", seeded)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if output.Success {
		t.Fatal("Success = true, want replayed failure")
	}
	if output.ErrorCode != ErrCodeInternalError {
		t.Fatalf("ErrorCode = %q, want stored %q", output.ErrorCode, ErrCodeInternalError)
	}
	if output.Operation == nil || !output.Operation.Replayed {
		t.Fatalf("Operation = %+v, want Replayed=true", output.Operation)
	}
	if !strings.Contains(output.Hint, "new operation ID") {
		t.Fatalf("Hint = %q, want the replayed-failure new-operation-ID hint", output.Hint)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil || row.Status != state.SendOperationCompleted || row.OutcomeJSON != seeded.OutcomeJSON {
		t.Fatalf("row changed by failure replay: %+v", row)
	}
}

// Branch (c): a completed row bound to a DIFFERENT command spec rejects the
// reuse with IDEMPOTENCY_CONFLICT and leaves the row untouched.
func TestGetSendIdempotency_ConflictingBindingRejected(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "conflict")

	opID := fmt.Sprintf("op-conflict-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-conflict",
		Pane:           paneID,
		IdempotencyKey: opID,
	}

	// A binding hash that cannot match sendOperationBindingHash(opts).
	const conflictingBinding = "0000000000000000000000000000000000000000000000000000000000000000"
	if conflictingBinding == sendOperationBindingHash(opts) {
		t.Fatal("test bug: conflicting binding accidentally matches")
	}
	seeded := seedCompletedSendOperation(t, store, opID, session, conflictingBinding, sendOperationOutcome{
		Success:    true,
		SentAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Targets:    []string{"sentinel-target"},
		Successful: []string{"sentinel-target"},
	})
	logSeededSendOperation(t, "seeded row", seeded)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if output.Success {
		t.Fatal("Success = true, want conflict rejection")
	}
	if output.ErrorCode != ErrCodeIdempotencyConflict {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodeIdempotencyConflict)
	}
	if output.Operation == nil || output.Operation.Replayed {
		t.Fatalf("Operation = %+v, want non-replayed stored-record info", output.Operation)
	}
	if output.Operation.OperationID != opID {
		t.Fatalf("Operation.OperationID = %q, want %q", output.Operation.OperationID, opID)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil || row.Status != state.SendOperationCompleted ||
		row.BindingHash != conflictingBinding || row.OutcomeJSON != seeded.OutcomeJSON {
		t.Fatalf("row modified by conflicting reuse: %+v", row)
	}
}

// Branch (d): a FRESH in_progress claim held by someone else (created_at now,
// inside the staleness window) reports OPERATION_IN_PROGRESS with
// outcome-unknown admissions and leaves the claim alone.
func TestGetSendIdempotency_FreshInProgressClaimReportsInProgress(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "inprog")
	targetKey := expectedSendTargetKey(t, session, paneID)

	opID := fmt.Sprintf("op-inprog-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-in-progress",
		Pane:           paneID,
		IdempotencyKey: opID,
	}

	payloadSHA, payloadBytes := sendPayloadDigest(opts.Message)
	seeded, claimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID:   opID,
		SessionName:   session,
		BindingHash:   sendOperationBindingHash(opts),
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
	})
	if err != nil || !claimed {
		t.Fatalf("seed fresh claim: claimed=%v err=%v", claimed, err)
	}
	logSeededSendOperation(t, "seeded row", seeded)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if output.Success {
		t.Fatal("Success = true, want in-progress rejection")
	}
	if output.ErrorCode != ErrCodeOperationInProgress {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodeOperationInProgress)
	}
	if !strings.Contains(output.Hint, opID) {
		t.Fatalf("Hint = %q, want receipt-reconciliation hint mentioning %s", output.Hint, opID)
	}
	if output.Operation == nil || output.Operation.Status != state.SendOperationInProgress {
		t.Fatalf("Operation = %+v, want in_progress info", output.Operation)
	}
	if len(output.Operation.Admissions) != 1 ||
		output.Operation.Admissions[0].Target != targetKey ||
		output.Operation.Admissions[0].State != AdmissionUnknown {
		t.Fatalf("Operation.Admissions = %+v, want single unknown admission for %s",
			output.Operation.Admissions, targetKey)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil || row.Status != state.SendOperationInProgress {
		t.Fatalf("row = %+v, want claim left in_progress", row)
	}
	if delta := row.CreatedAt.Sub(seeded.CreatedAt); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("row.CreatedAt drifted by %v — the fresh claim must not be taken over", delta)
	}
}

// Branch (e): an in_progress claim older than the staleness window is taken
// over (TakeOverStaleSendOperation) and the send executes fresh to completion.
func TestGetSendIdempotency_StaleInProgressClaimTakenOver(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "stale")
	targetKey := expectedSendTargetKey(t, session, paneID)

	opID := fmt.Sprintf("op-stale-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-stale-takeover",
		Pane:           paneID,
		IdempotencyKey: opID,
	}

	staleCreatedAt := time.Now().UTC().Add(-(sendOperationStaleClaimWindow + 10*time.Minute))
	payloadSHA, payloadBytes := sendPayloadDigest(opts.Message)
	seeded, claimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID:   opID,
		SessionName:   session,
		BindingHash:   sendOperationBindingHash(opts),
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
		CreatedAt:     staleCreatedAt,
	})
	if err != nil || !claimed {
		t.Fatalf("seed stale claim: claimed=%v err=%v", claimed, err)
	}
	logSeededSendOperation(t, "seeded row", seeded)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if !output.Success {
		t.Fatalf("Success = false, want stale takeover to execute the send (error=%q code=%q)",
			output.Error, output.ErrorCode)
	}
	if len(output.Successful) != 1 || output.Successful[0] != targetKey {
		t.Fatalf("Successful = %v, want [%s]", output.Successful, targetKey)
	}
	if output.Operation == nil || output.Operation.Replayed {
		t.Fatalf("Operation = %+v, want a fresh (non-replayed) execution", output.Operation)
	}
	if output.Operation.Status != state.SendOperationCompleted {
		t.Fatalf("Operation.Status = %q, want %q", output.Operation.Status, state.SendOperationCompleted)
	}

	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row == nil || row.Status != state.SendOperationCompleted {
		t.Fatalf("row = %+v, want completed after takeover", row)
	}
	// TakeOverStaleSendOperation refreshes created_at to now; a row still
	// carrying the stale timestamp means the takeover path never ran.
	if !row.CreatedAt.After(staleCreatedAt.Add(sendOperationStaleClaimWindow)) {
		t.Fatalf("row.CreatedAt = %v, want refreshed past the stale seed %v", row.CreatedAt, staleCreatedAt)
	}
	var outcome sendOperationOutcome
	if err := json.Unmarshal([]byte(row.OutcomeJSON), &outcome); err != nil {
		t.Fatalf("decode stored outcome: %v (json=%q)", err, row.OutcomeJSON)
	}
	if !outcome.Success || len(outcome.Successful) != 1 || outcome.Successful[0] != targetKey {
		t.Fatalf("stored outcome = %+v, want fresh successful delivery to %s", outcome, targetKey)
	}
}

// Branch (f): a preflight failure AFTER the claim (dispatch Prepare rejects
// the request before any keystroke) releases the claim so the operation ID is
// immediately reusable. Grok is now a supported delivery target, so use an
// invalid inter-send delay: dispatch Prepare evaluates it after the claim has
// been recorded.
func TestGetSendIdempotency_PreflightFailureReleasesClaim(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	session, paneID := createIdempotencyBranchSession(t, "preflight")

	opID := fmt.Sprintf("op-preflight-%d", time.Now().UnixNano())
	opts := SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-preflight",
		Pane:           paneID,
		DelayMs:        -1,
		IdempotencyKey: opID,
	}
	t.Logf("seeded row: none (fresh claim before invalid-delay preflight for pane %s)", paneID)

	output, err := GetSend(opts)
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if output.Success {
		t.Fatal("Success = true, want invalid-delay preflight failure")
	}
	if output.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("ErrorCode = %q, want %q (invalid dispatch delay)", output.ErrorCode, ErrCodeInvalidFlag)
	}

	// The claim must have been RELEASED: no row remains.
	row := mustGetSendOperation(t, store, opID, session)
	logSeededSendOperation(t, "final row", row)
	if row != nil {
		t.Fatalf("row = %+v, want claim released (no row) after preflight failure", row)
	}

	// And the operation ID is immediately reusable: a fresh claim wins.
	payloadSHA, payloadBytes := sendPayloadDigest(opts.Message)
	_, reclaimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID:   opID,
		SessionName:   session,
		BindingHash:   sendOperationBindingHash(opts),
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
	})
	if err != nil {
		t.Fatalf("re-claim after release: %v", err)
	}
	if !reclaimed {
		t.Fatal("re-claim after release = false, want the operation ID to be immediately reusable")
	}
	t.Logf("op-id %s re-claimed fresh after release", opID)
}

// Branch (g): an operation ID without a projection store fails closed with
// NOT_IMPLEMENTED before any claim or dispatch.
func TestGetSendIdempotency_NoProjectionStoreNotImplemented(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	session, paneID := createIdempotencyBranchSession(t, "nostore")

	oldStore := currentProjectionStore()
	SetProjectionStore(nil)
	t.Cleanup(func() { SetProjectionStore(oldStore) })

	opID := fmt.Sprintf("op-nostore-%d", time.Now().UnixNano())
	t.Logf("seeded row: none (projection store nil); op_id=%s", opID)

	output, err := GetSend(SendOptions{
		Session:        session,
		Message:        "echo ntm-idem-no-store",
		Pane:           paneID,
		IdempotencyKey: opID,
	})
	if err != nil {
		t.Fatalf("GetSend returned error: %v", err)
	}
	logSendEnvelopeSubset(t, output)

	if output.Success {
		t.Fatal("Success = true, want NOT_IMPLEMENTED without a projection store")
	}
	if output.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodeNotImplemented)
	}
	if !strings.Contains(output.Error, "projection store") {
		t.Fatalf("Error = %q, want mention of the runtime projection store", output.Error)
	}
	if output.Operation != nil {
		t.Fatalf("Operation = %+v, want nil when no store exists", output.Operation)
	}
	t.Logf("final row state: no store installed, nothing persisted")
}
