// Behavioral coverage for durable idempotent --robot-interrupt (--op-id /
// REST Idempotency-Key) through GetInterrupt, the function --robot-interrupt
// and POST /api/v1/sessions/{id}/agents/interrupt both call.
//
// Ground truth comes from testutil.InterruptFixture: a real tmux pane whose
// process traps SIGINT and logs one "INT" line per interrupt it receives and
// one "LINE:<text>" line per submitted input line. The envelope alone cannot
// prove the absence of a second Ctrl+C or a second delivery of the follow-up
// task; the fixture log can.
//
// Branch matrix:
//
//	fresh claim + identical retry replay -> TestGetInterruptIdempotency_IdenticalRetryReplaysWithoutSecondInterrupt
//	conflicting reuse (same + cross kind) -> TestGetInterruptIdempotency_ConflictingReuseRejected
//	live in-progress claimant           -> TestGetInterruptIdempotency_InProgressClaimNotInterrupted
//	no projection store / dry-run       -> TestGetInterruptIdempotency_FailsClosedWithoutStoreAndSkipsDryRun
package robot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func logInterruptEnvelope(t *testing.T, label string, output *InterruptOutput) {
	t.Helper()
	opSummary := "<nil>"
	if output.Operation != nil {
		opSummary = fmt.Sprintf("{kind=%s status=%s replayed=%v admissions=%+v}",
			output.Operation.Kind, output.Operation.Status, output.Operation.Replayed, output.Operation.Admissions)
	}
	t.Logf("%s: success=%v error_code=%q error=%q hint=%q interrupted=%v ready=%v message_sent=%v failed=%+v operation=%s",
		label, output.Success, output.ErrorCode, output.Error, output.Hint, output.Interrupted,
		output.ReadyForInput, output.MessageSent, output.Failed, opSummary)
}

// interruptFixtureOptions targets the fixture pane explicitly, forces the
// interrupt key regardless of observed state, and skips the readiness wait
// (the fixture is not an agent CLI with a recognizable idle prompt).
func interruptFixtureOptions(store *state.Store, session, paneID, message, opID string) InterruptOptions {
	return InterruptOptions{
		Session:        session,
		Panes:          []string{paneID},
		Force:          true,
		NoWait:         true,
		Message:        message,
		IdempotencyKey: opID,
		Store:          store,
		ExplicitStore:  true,
	}
}

// An identical retry of a completed interrupt replays the recorded outcome:
// the fixture sees exactly one Ctrl+C and one delivery of the new task across
// both calls, and the durable receipt reports the interrupt outcome.
func TestGetInterruptIdempotency_IdenticalRetryReplaysWithoutSecondInterrupt(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	fx := testutil.StartInterruptFixture(t, "replay")
	session, paneID := fx.Session, fx.PaneID

	marker := fmt.Sprintf("ntm-int-replay-%d", time.Now().UnixNano())
	opID := "op-int-replay-" + marker
	opts := interruptFixtureOptions(store, session, paneID, "new task "+marker, opID)

	first, err := GetInterrupt(opts)
	if err != nil {
		t.Fatalf("first GetInterrupt error: %v", err)
	}
	logInterruptEnvelope(t, "first", first)
	if !first.Success {
		t.Fatalf("first interrupt failed: code=%q error=%q failed=%+v", first.ErrorCode, first.Error, first.Failed)
	}
	if len(first.Interrupted) != 1 || !first.MessageSent {
		t.Fatalf("first interrupt = interrupted %v message_sent %v, want one interrupted pane and the task delivered",
			first.Interrupted, first.MessageSent)
	}
	target := first.Interrupted[0]
	if first.Operation == nil || first.Operation.Kind != state.OperationKindInterrupt ||
		first.Operation.Status != state.SendOperationCompleted || first.Operation.Replayed {
		t.Fatalf("first Operation = %+v, want a fresh completed interrupt operation", first.Operation)
	}
	if len(first.Operation.Admissions) != 1 || first.Operation.Admissions[0].Target != target ||
		first.Operation.Admissions[0].State != AdmissionSubmitted {
		t.Fatalf("first Operation.Admissions = %+v, want one submitted admission for %s", first.Operation.Admissions, target)
	}
	if wantSHA, wantBytes := operationPayloadDigest(opts.Message); first.Operation.PayloadSHA256 != wantSHA ||
		first.Operation.PayloadBytes != wantBytes {
		t.Fatalf("first Operation payload = (%s, %d), want digest of the follow-up message (%s, %d)",
			first.Operation.PayloadSHA256, first.Operation.PayloadBytes, wantSHA, wantBytes)
	}
	fx.WaitForEvents(t, marker, 1, 1)

	// The retry an orchestrator issues after its own timeout: byte-identical
	// command, same operation ID. Selector order and wait behavior are not
	// part of the binding, so a longer timeout still replays.
	retry := interruptFixtureOptions(store, session, paneID, "new task "+marker, opID)
	retry.TimeoutMs = 30000
	second, err := GetInterrupt(retry)
	if err != nil {
		t.Fatalf("retry GetInterrupt error: %v", err)
	}
	logInterruptEnvelope(t, "retry", second)
	// Ground truth first: no second Ctrl+C and no second delivery of the task.
	fx.AssertQuiet(t, marker, 1, 1)
	if !second.Success {
		t.Fatalf("retry failed: code=%q error=%q", second.ErrorCode, second.Error)
	}
	if second.Operation == nil || !second.Operation.Replayed || second.Operation.Kind != state.OperationKindInterrupt {
		t.Fatalf("retry Operation = %+v, want a replayed interrupt operation", second.Operation)
	}
	if !second.InterruptedAt.Equal(first.InterruptedAt) || !second.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("retry timestamps = (%v, %v), want the recorded (%v, %v)",
			second.InterruptedAt, second.CompletedAt, first.InterruptedAt, first.CompletedAt)
	}
	if len(second.Interrupted) != 1 || second.Interrupted[0] != target || !second.MessageSent {
		t.Fatalf("retry = interrupted %v message_sent %v, want the recorded [%s] and true", second.Interrupted, second.MessageSent, target)
	}
	if prev, ok := second.PreviousStates[target]; !ok || prev.LastOutput != "" {
		t.Fatalf("retry previous_states[%s] = %+v (present=%v), want the recorded state without persisted pane output", target, prev, ok)
	}

	row, err := store.GetSendOperation(opID, session)
	if err != nil || row == nil {
		t.Fatalf("GetSendOperation = (%+v, %v), want the durable interrupt row", row, err)
	}
	if row.Kind != state.OperationKindInterrupt || row.Status != state.SendOperationCompleted ||
		row.BindingHash != interruptOperationBindingHash(opts) {
		t.Fatalf("durable row = %+v, want completed interrupt bound to the original command", row)
	}
	if strings.Contains(row.OutcomeJSON, testutil.InterruptFixtureReadyMarker) {
		t.Fatalf("durable outcome persisted pane output: %s", row.OutcomeJSON)
	}

	receipt, err := GetSendReceiptForStore(store, opID)
	if err != nil {
		t.Fatalf("GetSendReceipt error: %v", err)
	}
	if !receipt.Success || receipt.Session != session {
		t.Fatalf("receipt = success %v session %q (error=%q), want success for %s", receipt.Success, receipt.Session, receipt.Error, session)
	}
	if receipt.Operation == nil || receipt.Operation.Kind != state.OperationKindInterrupt ||
		receipt.Operation.Status != state.SendOperationCompleted {
		t.Fatalf("receipt.Operation = %+v, want completed interrupt operation", receipt.Operation)
	}
	if receipt.Outcome != nil {
		t.Fatalf("receipt.Outcome = %+v, want nil (send outcome) for an interrupt operation", receipt.Outcome)
	}
	got := receipt.InterruptOutcome
	if got == nil || !got.Success || !got.MessageSent || len(got.Interrupted) != 1 || got.Interrupted[0] != target ||
		len(got.Targets) != 1 || got.Targets[0] != target {
		t.Fatalf("receipt.InterruptOutcome = %+v, want the recorded interrupt of %s with the task delivered", got, target)
	}
}

// Reusing an operation ID with a different command — another follow-up
// task, another selector toggle, or another actuation kind — is rejected as
// IDEMPOTENCY_CONFLICT and never touches the pane.
func TestGetInterruptIdempotency_ConflictingReuseRejected(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	fx := testutil.StartInterruptFixture(t, "conflict")
	session, paneID := fx.Session, fx.PaneID

	marker := fmt.Sprintf("ntm-int-conflict-%d", time.Now().UnixNano())
	opID := "op-int-conflict-" + marker
	original := interruptFixtureOptions(store, session, paneID, "task A "+marker, opID)
	first, err := GetInterrupt(original)
	if err != nil {
		t.Fatalf("first GetInterrupt error: %v", err)
	}
	logInterruptEnvelope(t, "first", first)
	if !first.Success {
		t.Fatalf("first interrupt failed: code=%q error=%q", first.ErrorCode, first.Error)
	}
	fx.WaitForEvents(t, marker, 1, 1)
	recorded, err := store.GetSendOperation(opID, session)
	if err != nil || recorded == nil {
		t.Fatalf("GetSendOperation = (%+v, %v), want the recorded interrupt", recorded, err)
	}

	otherTask := interruptFixtureOptions(store, session, paneID, "task B "+marker, opID)
	notForced := interruptFixtureOptions(store, session, paneID, "task A "+marker, opID)
	notForced.Force = false
	for name, opts := range map[string]InterruptOptions{"different task": otherTask, "different force": notForced} {
		output, err := GetInterrupt(opts)
		if err != nil {
			t.Fatalf("%s: GetInterrupt error: %v", name, err)
		}
		logInterruptEnvelope(t, name, output)
		if output.Success || output.ErrorCode != ErrCodeIdempotencyConflict {
			t.Fatalf("%s: success=%v code=%q, want IDEMPOTENCY_CONFLICT", name, output.Success, output.ErrorCode)
		}
		if output.Operation == nil || output.Operation.Replayed || output.Operation.OperationID != opID ||
			output.Operation.Kind != state.OperationKindInterrupt {
			t.Fatalf("%s: Operation = %+v, want the stored interrupt record", name, output.Operation)
		}
		if len(output.Interrupted) != 0 || output.MessageSent {
			t.Fatalf("%s: interrupted=%v message_sent=%v, want nothing done", name, output.Interrupted, output.MessageSent)
		}
	}

	// Operation IDs share one namespace per session across actuation kinds:
	// a send reusing the interrupt's ID is a conflict, not a replay of the
	// interrupt's outcome and not a fresh delivery.
	send, err := GetSend(SendOptions{Session: session, Pane: paneID, Message: "send " + marker, IdempotencyKey: opID, Store: store, ExplicitStore: true})
	if err != nil {
		t.Fatalf("cross-kind GetSend error: %v", err)
	}
	if send.Success || send.ErrorCode != ErrCodeIdempotencyConflict || !strings.Contains(send.Error, "interrupt operation") {
		t.Fatalf("cross-kind send = success %v code %q error %q, want IDEMPOTENCY_CONFLICT naming the interrupt binding",
			send.Success, send.ErrorCode, send.Error)
	}
	fx.AssertQuiet(t, marker, 1, 1)

	row, err := store.GetSendOperation(opID, session)
	if err != nil || row == nil || row.OutcomeJSON != recorded.OutcomeJSON || row.BindingHash != recorded.BindingHash ||
		row.Kind != state.OperationKindInterrupt {
		t.Fatalf("durable row changed by conflicting reuse: %+v (recorded %+v, err %v)", row, recorded, err)
	}
}

// A live claimant (fresh in_progress row with the identical binding) means
// another interrupt is mid-flight: the retry reports OPERATION_IN_PROGRESS
// with unknown admissions and leaves the pane alone.
func TestGetInterruptIdempotency_InProgressClaimNotInterrupted(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	store := installIdempotencyBranchStore(t)
	fx := testutil.StartInterruptFixture(t, "inprog")
	session, paneID := fx.Session, fx.PaneID

	marker := fmt.Sprintf("ntm-int-inprog-%d", time.Now().UnixNano())
	opID := "op-int-inprog-" + marker
	opts := interruptFixtureOptions(store, session, paneID, "task "+marker, opID)
	payloadSHA, payloadBytes := operationPayloadDigest(opts.Message)
	if _, claimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID:   opID,
		SessionName:   session,
		Kind:          state.OperationKindInterrupt,
		BindingHash:   interruptOperationBindingHash(opts),
		PayloadSHA256: payloadSHA,
		PayloadBytes:  payloadBytes,
		Targets:       []string{expectedSendTargetKey(t, session, paneID)},
	}); err != nil || !claimed {
		t.Fatalf("seed live claim = (claimed=%v, err=%v)", claimed, err)
	}

	output, err := GetInterrupt(opts)
	if err != nil {
		t.Fatalf("GetInterrupt error: %v", err)
	}
	logInterruptEnvelope(t, "in-progress", output)
	if output.Success || output.ErrorCode != ErrCodeOperationInProgress {
		t.Fatalf("success=%v code=%q, want OPERATION_IN_PROGRESS", output.Success, output.ErrorCode)
	}
	if !strings.Contains(output.Hint, "--robot-send-receipt="+opID) {
		t.Fatalf("hint = %q, want the receipt query for %s", output.Hint, opID)
	}
	if output.Operation == nil || output.Operation.Status != state.SendOperationInProgress ||
		len(output.Operation.Admissions) != 1 || output.Operation.Admissions[0].State != AdmissionUnknown {
		t.Fatalf("Operation = %+v, want in-progress view with one unknown admission", output.Operation)
	}
	fx.AssertQuiet(t, marker, 0, 0)

	row, err := store.GetSendOperation(opID, session)
	if err != nil || row == nil || row.Status != state.SendOperationInProgress {
		t.Fatalf("live claim after retry = (%+v, %v), want untouched in_progress row", row, err)
	}
}

// Without a projection store an operation ID fails closed (NOT_IMPLEMENTED)
// before any interrupt key is sent, and a dry run never claims the ID.
func TestGetInterruptIdempotency_FailsClosedWithoutStoreAndSkipsDryRun(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	installIdempotencyBranchFeed(t)
	fx := testutil.StartInterruptFixture(t, "nostore")
	session, paneID := fx.Session, fx.PaneID
	marker := fmt.Sprintf("ntm-int-nostore-%d", time.Now().UnixNano())

	output, err := GetInterrupt(interruptFixtureOptions(nil, session, paneID, "task "+marker, "op-int-nostore-"+marker))
	if err != nil {
		t.Fatalf("GetInterrupt error: %v", err)
	}
	logInterruptEnvelope(t, "no store", output)
	if output.Success || output.ErrorCode != ErrCodeNotImplemented || !strings.Contains(output.Error, "projection store") {
		t.Fatalf("success=%v code=%q error=%q, want NOT_IMPLEMENTED naming the projection store", output.Success, output.ErrorCode, output.Error)
	}
	fx.AssertQuiet(t, marker, 0, 0)

	store := installIdempotencyBranchStore(t)
	dryOpID := "op-int-dry-" + marker
	dry := interruptFixtureOptions(store, session, paneID, "task "+marker, dryOpID)
	dry.DryRun = true
	preview, err := GetInterrupt(dry)
	if err != nil {
		t.Fatalf("dry-run GetInterrupt error: %v", err)
	}
	if !preview.Success || !preview.DryRun || preview.Operation != nil {
		t.Fatalf("dry run = success %v dry_run %v operation %+v, want a plain preview", preview.Success, preview.DryRun, preview.Operation)
	}
	if row, err := store.GetSendOperation(dryOpID, session); err != nil || row != nil {
		t.Fatalf("dry run claimed the operation ID: row=%+v err=%v", row, err)
	}
	fx.AssertQuiet(t, marker, 0, 0)
}

// TestInterruptOperationBindingHashCanonicalizesCommand: the binding covers
// session, selectors (order-insensitive), --all, --force and the input
// message, but not wait behavior, and never collides with a send binding.
func TestInterruptOperationBindingHashCanonicalizesCommand(t *testing.T) {
	base := InterruptOptions{Session: "proj", Panes: []string{"1", "2"}, Message: "stop and fix"}
	want := interruptOperationBindingHash(base)

	same := base
	same.Panes = []string{"2", " 1"}
	same.AgentTypes = []string{" "}
	same.NoWait = true
	same.TimeoutMs = 30000
	same.PollMs = 50
	same.RequestID = "req-other"
	if got := interruptOperationBindingHash(same); got != want {
		t.Error("selector order, whitespace, or wait behavior changed the binding hash")
	}

	for name, mutate := range map[string]func(*InterruptOptions){
		"session": func(o *InterruptOptions) { o.Session = "other" },
		"panes":   func(o *InterruptOptions) { o.Panes = []string{"1"} },
		"all":     func(o *InterruptOptions) { o.All = true },
		"force":   func(o *InterruptOptions) { o.Force = true },
		"type":    func(o *InterruptOptions) { o.AgentTypes = []string{"codex"} },
		"message": func(o *InterruptOptions) { o.Message = "stop and fix it" },
		"no task": func(o *InterruptOptions) { o.Message = "" },
	} {
		changed := base
		changed.Panes = append([]string(nil), base.Panes...)
		mutate(&changed)
		if interruptOperationBindingHash(changed) == want {
			t.Errorf("changing %s did not change the binding hash", name)
		}
	}

	aliased := base
	aliased.AgentTypes = []string{"cod", "cc"}
	canonical := base
	canonical.AgentTypes = []string{"claude", "codex", "codex"}
	if interruptOperationBindingHash(aliased) != interruptOperationBindingHash(canonical) {
		t.Error("--type aliases, order, or duplicates changed the binding hash")
	}

	send := sendOperationBindingHash(SendOptions{Session: "proj", Panes: []string{"1", "2"}, Message: "stop and fix"})
	if send == want {
		t.Error("interrupt binding collides with the send binding of the same selector and message")
	}
}

func TestAdmissionsFromInterruptOutput(t *testing.T) {
	output := &InterruptOutput{
		Interrupted:   []string{"1", "2"},
		ReadyForInput: []string{"1", "3", "4"},
		MessageSent:   true,
		Failed: []InterruptError{
			{Pane: "2", Reason: "failed to send message: paste failed"},
			{Pane: "dispatch", Reason: "not a target"},
		},
	}
	targets := []string{"1", "2", "3", "5"}
	byTarget := map[string]OperationAdmission{}
	for _, adm := range admissionsFromInterruptOutput(targets, true, output) {
		byTarget[adm.Target] = adm
	}
	if len(byTarget) != len(targets) {
		t.Fatalf("admissions = %+v, want one per target", byTarget)
	}
	if byTarget["1"].State != AdmissionSubmitted {
		t.Errorf("interrupted target = %+v, want submitted", byTarget["1"])
	}
	if byTarget["2"].State != AdmissionRejected || !strings.Contains(byTarget["2"].Error, "paste failed") {
		t.Errorf("failed target = %+v, want rejected with the failure reason", byTarget["2"])
	}
	if byTarget["3"].State != AdmissionSubmitted {
		t.Errorf("already-ready target that received the task = %+v, want submitted", byTarget["3"])
	}
	if byTarget["5"].State != AdmissionNotAttempted {
		t.Errorf("untouched target = %+v, want not_attempted", byTarget["5"])
	}

	noTask := admissionsFromInterruptOutput([]string{"3"}, false, &InterruptOutput{ReadyForInput: []string{"3"}})
	if len(noTask) != 1 || noTask[0].State != AdmissionNotAttempted {
		t.Errorf("already-ready target with no follow-up task = %+v, want not_attempted", noTask)
	}
}

func TestApplyReplayedInterruptOutcomeRestoresRecordedFailure(t *testing.T) {
	interruptedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	outcome := interruptOperationOutcome{
		Success:        false,
		Error:          "interrupt timed out",
		ErrorCode:      ErrCodeTimeout,
		InterruptedAt:  interruptedAt,
		CompletedAt:    interruptedAt.Add(10 * time.Second),
		Method:         "ctrl_c_then_send",
		Targets:        []string{"1"},
		Interrupted:    []string{"1"},
		PreviousStates: map[string]PaneState{"1": {State: "active", AgentType: "claude"}},
		TimedOut:       true,
		Admissions:     []OperationAdmission{{Target: "1", State: AdmissionSubmitted}},
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	completed := time.Now().UTC()
	op := &state.SendOperation{
		OperationID: "op-int", SessionName: "proj", Kind: state.OperationKindInterrupt,
		Status: state.SendOperationCompleted, OutcomeJSON: string(data), CreatedAt: interruptedAt, CompletedAt: &completed,
	}

	output := &InterruptOutput{RobotResponse: NewRobotResponse(true), Method: "ctrl_c"}
	if err := applyReplayedInterruptOutcome(output, op); err != nil {
		t.Fatalf("applyReplayedInterruptOutcome error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeTimeout || !output.TimedOut || output.Method != "ctrl_c_then_send" {
		t.Fatalf("replayed output = %+v, want the recorded timeout", output)
	}
	if !output.InterruptedAt.Equal(interruptedAt) || output.PreviousStates["1"].State != "active" {
		t.Fatalf("replayed output lost recorded fields: %+v", output)
	}
	// Critical arrays stay non-nil even when the record carried none.
	if output.ReadyForInput == nil || output.Failed == nil {
		t.Fatalf("replayed arrays = ready %v failed %v, want empty non-nil arrays", output.ReadyForInput, output.Failed)
	}
	if output.Operation == nil || !output.Operation.Replayed || output.Operation.Kind != state.OperationKindInterrupt ||
		len(output.Operation.Admissions) != 1 {
		t.Fatalf("replayed Operation = %+v, want replayed interrupt view with recorded admissions", output.Operation)
	}
	if !strings.Contains(output.Hint, "new operation ID") {
		t.Fatalf("hint = %q, want the replayed-failure new-operation-ID hint", output.Hint)
	}
}

// The receipt surface decodes whichever kind claimed the ID. Runs without
// tmux so short-mode CI covers the interrupt receipt shape.
func TestGetSendReceiptReportsInterruptOperation(t *testing.T) {
	t.Parallel()
	store := installIdempotencyBranchStore(t)

	claim, claimed, err := store.ClaimSendOperation(&state.SendOperation{
		OperationID: "op-int-receipt", SessionName: "proj", Kind: state.OperationKindInterrupt, BindingHash: "bind",
	})
	if err != nil || !claimed {
		t.Fatalf("claim = (claimed=%v, err=%v)", claimed, err)
	}
	pending, err := GetSendReceiptForStore(store, "op-int-receipt")
	if err != nil || !pending.Success || pending.Operation == nil ||
		pending.Operation.Kind != state.OperationKindInterrupt || pending.Operation.Status != state.SendOperationInProgress ||
		pending.InterruptOutcome != nil {
		t.Fatalf("in-progress receipt = (%+v, %v), want in_progress interrupt view without outcome", pending, err)
	}

	data, err := json.Marshal(interruptOperationOutcome{
		Success: true, Method: "ctrl_c", Targets: []string{"1"}, Interrupted: []string{"1"},
		ReadyForInput: []string{"1"}, Failed: []InterruptError{},
		Admissions: []OperationAdmission{{Target: "1", State: AdmissionSubmitted}},
	})
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	if err := store.CompleteSendOperation("op-int-receipt", "proj", claim.ClaimToken, string(data), time.Now().UTC()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	receipt, err := GetSendReceiptForStore(store, "op-int-receipt")
	if err != nil || !receipt.Success {
		t.Fatalf("receipt = (%+v, %v), want success", receipt, err)
	}
	if receipt.Outcome != nil {
		t.Fatalf("receipt.Outcome = %+v, want nil for an interrupt operation", receipt.Outcome)
	}
	if receipt.InterruptOutcome == nil || !receipt.InterruptOutcome.Success ||
		len(receipt.InterruptOutcome.Interrupted) != 1 || receipt.Operation.Admissions[0].State != AdmissionSubmitted {
		t.Fatalf("receipt = outcome %+v operation %+v, want the recorded interrupt", receipt.InterruptOutcome, receipt.Operation)
	}

	missing, err := GetSendReceiptForStore(store, "op-never-claimed")
	if err != nil || missing.Success || missing.ErrorCode != ErrCodeNotFound || !strings.Contains(missing.Hint, "--robot-interrupt") {
		t.Fatalf("unknown receipt = (%+v, %v), want NOT_FOUND hinting at both actuations", missing, err)
	}
}
