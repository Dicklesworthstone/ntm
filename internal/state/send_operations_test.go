package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

// Tests for the durable idempotent operation records (send #245, interrupt).

func TestCancelSendOperationUsesObservedSessionAndClaim(t *testing.T) {
	store := testStore(t)
	claim := func(session string) *SendOperation {
		t.Helper()
		op, claimed, err := store.ClaimSendOperation(&SendOperation{
			OperationID: "step-1", SessionName: session, BindingHash: "binding-" + session,
		})
		if err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%v err=%v", session, claimed, err)
		}
		return op
	}
	alpha := claim("alpha")
	beta := claim("beta")
	canceled, ok, err := store.CancelSendOperation(alpha, time.Now().UTC())
	if err != nil || !ok || canceled == nil || canceled.SessionName != "alpha" || canceled.Status != SendOperationCanceled {
		t.Fatalf("cancel alpha: %+v, %v, %v", canceled, ok, err)
	}
	other, err := store.GetSendOperation("step-1", "beta")
	if err != nil || other.Status != SendOperationInProgress {
		t.Fatalf("cancel alpha changed beta: %+v, %v", other, err)
	}
	successor, err := store.TakeOverStaleSendOperation("step-1", "beta", beta.BindingHash, beta.ClaimToken, time.Now().Add(time.Hour))
	if err != nil || successor == nil {
		t.Fatalf("takeover beta: %+v, %v", successor, err)
	}
	if _, ok, err := store.CancelSendOperation(beta, time.Now().UTC()); ok || !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("stale claimant canceled successor: ok=%v err=%v", ok, err)
	}
	current, err := store.GetSendOperation("step-1", "beta")
	if err != nil || current.Status != SendOperationInProgress || current.ClaimToken != successor.ClaimToken {
		t.Fatalf("stale cancellation changed successor: %+v, %v", current, err)
	}
}

func TestRobotRequestSnapshotAndWaitCancellationIdentity(t *testing.T) {
	store := testStore(t)
	const id = "same-id"
	for _, session := range []string{"alpha", "beta"} {
		if _, claimed, err := store.ClaimSendOperation(&SendOperation{OperationID: id, SessionName: session}); err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%v err=%v", session, claimed, err)
		}
	}
	handle := &RobotWaitHandle{ID: id, SessionName: "beta", CreatedAt: time.Now().UTC()}
	if err := store.CreateRobotWaitHandle(handle); err != nil {
		t.Fatal(err)
	}
	ops, wait, err := store.GetRobotRequestRecords(id, "")
	if err != nil || len(ops) != 2 || wait == nil || wait.SessionName != "beta" {
		t.Fatalf("request snapshot = %+v, %+v, %v", ops, wait, err)
	}
	ops, wait, err = store.GetRobotRequestRecords(id, "alpha")
	if err != nil || len(ops) != 1 || ops[0].SessionName != "alpha" || wait != nil {
		t.Fatalf("scoped request snapshot = %+v, %+v, %v", ops, wait, err)
	}
	ops, wait, err = store.GetRobotRequestRecords(id, "absent")
	if err != nil || len(ops) != 0 || wait != nil {
		t.Fatalf("missing session fell back: %+v, %+v, %v", ops, wait, err)
	}
	// Model an ID reused after the original handle was retired. Even the
	// same session and ID must not authorize canceling a later lifetime.
	if _, err := store.db.Exec(`UPDATE robot_wait_handles SET created_at = ? WHERE id = ?`, handle.CreatedAt.Add(time.Second), id); err != nil {
		t.Fatal(err)
	}
	if canceled, err := store.CancelRobotWaitHandle(handle, time.Now().UTC()); canceled || !errors.Is(err, ErrRobotWaitHandleChanged) {
		t.Fatalf("stale wait cancellation = %v, %v", canceled, err)
	}
	current, err := store.GetRobotWaitHandle(id)
	if err != nil || current == nil || current.CanceledAt != nil {
		t.Fatalf("stale cancellation changed current wait: %+v, %v", current, err)
	}
	if canceled, err := store.CancelRobotWaitHandle(current, time.Now().UTC()); err != nil || !canceled {
		t.Fatalf("current wait cancellation = %v, %v", canceled, err)
	}
}

func TestClaimSendOperationLifecycle(t *testing.T) {
	store := testStore(t)

	op := &SendOperation{
		OperationID:   "op-1",
		SessionName:   "proj",
		BindingHash:   "bind-a",
		PayloadSHA256: "sha-a",
		PayloadBytes:  42,
		Targets:       []string{"proj:1", "proj:2"},
		ClaimToken:    "caller-supplied-token",
	}

	stored, claimed, err := store.ClaimSendOperation(op)
	if err != nil {
		t.Fatalf("first claim error = %v", err)
	}
	if !claimed {
		t.Fatal("first claim not claimed; want claimed=true")
	}
	if stored.Status != SendOperationInProgress {
		t.Errorf("claimed status = %q, want in_progress", stored.Status)
	}
	if stored.BindingHash != "bind-a" || stored.PayloadSHA256 != "sha-a" || stored.PayloadBytes != 42 {
		t.Errorf("claimed record lost binding fields: %+v", stored)
	}
	if len(stored.ClaimToken) != 64 || stored.ClaimToken == op.ClaimToken || stored.DispatchStartedAt != nil {
		t.Fatalf("fresh claim must have its own token and no dispatch: %+v", stored)
	}
	if !slices.Equal(stored.Targets, op.Targets) {
		t.Fatalf("claimed targets = %v, want %v", stored.Targets, op.Targets)
	}
	public, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal operation: %v", err)
	}
	if strings.Contains(string(public), stored.ClaimToken) || strings.Contains(string(public), "claim_token") {
		t.Fatalf("operation JSON exposes its private claim token: %s", public)
	}

	// A second claim with the same ID observes the existing row (race-safe
	// duplicate detection) and does not overwrite its binding.
	dup := &SendOperation{
		OperationID:   "op-1",
		SessionName:   "proj",
		BindingHash:   "bind-DIFFERENT",
		PayloadSHA256: "sha-DIFFERENT",
		PayloadBytes:  7,
	}
	existing, claimedAgain, err := store.ClaimSendOperation(dup)
	if err != nil {
		t.Fatalf("duplicate claim error = %v", err)
	}
	if claimedAgain {
		t.Fatal("duplicate claim reported claimed=true; want false")
	}
	if existing.BindingHash != "bind-a" {
		t.Errorf("duplicate claim overwrote binding: %+v", existing)
	}
	if existing.ClaimToken != stored.ClaimToken || !slices.Equal(existing.Targets, stored.Targets) {
		t.Fatalf("duplicate claim changed execution ownership or targets: %+v", existing)
	}

	// The dispatch boundary freezes the prepared payload after enrichment
	// and final redaction, even when it differs from the original claim.
	prepared := *stored
	prepared.PayloadSHA256 = "sha-prepared"
	prepared.PayloadBytes = 84
	prepared.Targets = []string{"proj:3", "proj:4"}
	started, err := store.StartSendOperation(&prepared)
	if err != nil {
		t.Fatalf("start error = %v", err)
	}
	if started.DispatchStartedAt == nil || started.ClaimToken != stored.ClaimToken || started.BindingHash != "bind-a" {
		t.Fatalf("start lost dispatch or binding evidence: %+v", started)
	}
	if started.PayloadSHA256 != prepared.PayloadSHA256 || started.PayloadBytes != prepared.PayloadBytes || !slices.Equal(started.Targets, prepared.Targets) {
		t.Fatalf("start did not freeze prepared delivery metadata: %+v", started)
	}
	prepared.PayloadSHA256 = "should-not-replace-frozen-payload"
	if _, err := store.StartSendOperation(&prepared); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("repeated start error = %v, want claim-lost", err)
	}

	// Completion records the outcome durably.
	if err := store.CompleteSendOperation("op-1", "proj", stored.ClaimToken, `{"success":true}`, time.Now()); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	got, err := store.GetSendOperation("op-1", "proj")
	if err != nil {
		t.Fatalf("get error = %v", err)
	}
	if got.Status != SendOperationCompleted || got.OutcomeJSON != `{"success":true}` {
		t.Errorf("completed record = %+v, want completed with stored outcome", got)
	}
	if got.CompletedAt == nil {
		t.Error("completed record missing completed_at")
	}
	if got.PayloadSHA256 != started.PayloadSHA256 || got.PayloadBytes != started.PayloadBytes || !slices.Equal(got.Targets, started.Targets) || got.DispatchStartedAt == nil || !got.DispatchStartedAt.Equal(*started.DispatchStartedAt) {
		t.Fatalf("completed receipt changed frozen delivery metadata: %+v", got)
	}
	byID, _, err := store.GetRobotRequestRecords("op-1", "")
	if err != nil || len(byID) != 1 || !slices.Equal(byID[0].Targets, started.Targets) || byID[0].DispatchStartedAt == nil {
		t.Fatalf("receipt listing lost delivery metadata: %+v, %v", byID, err)
	}

	// A completed operation is no longer an owned in-progress attempt.
	// Refuse another completion honestly while preserving the first receipt.
	if err := store.CompleteSendOperation("op-1", "proj", stored.ClaimToken, `{"success":false}`, time.Now()); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("re-complete error = %v, want claim-lost", err)
	}
	got2, _ := store.GetSendOperation("op-1", "proj")
	if got2.OutcomeJSON != `{"success":true}` {
		t.Errorf("re-complete overwrote outcome: %q", got2.OutcomeJSON)
	}
}

// Operation kinds share one per-session operation-ID namespace: the kind is
// recorded on claim, an empty kind claims as a send, and a later claim of the
// same ID under a different kind observes (never overwrites) the original
// row so the caller can reject it as a conflict.
func TestClaimSendOperationRecordsKind(t *testing.T) {
	store := testStore(t)

	stored, claimed, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-int", SessionName: "proj", Kind: OperationKindInterrupt, BindingHash: "bind-int",
	})
	if err != nil || !claimed {
		t.Fatalf("interrupt claim = (claimed=%v, err=%v), want fresh claim", claimed, err)
	}
	if stored.Kind != OperationKindInterrupt {
		t.Fatalf("claimed kind = %q, want %q", stored.Kind, OperationKindInterrupt)
	}

	defaulted, claimed, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-legacy", SessionName: "proj", BindingHash: "bind-send",
	})
	if err != nil || !claimed {
		t.Fatalf("kindless claim = (claimed=%v, err=%v), want fresh claim", claimed, err)
	}
	if defaulted.Kind != OperationKindSend {
		t.Fatalf("kindless claim kind = %q, want %q", defaulted.Kind, OperationKindSend)
	}

	existing, claimedAgain, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-int", SessionName: "proj", Kind: OperationKindSend, BindingHash: "bind-send",
	})
	if err != nil {
		t.Fatalf("cross-kind claim error = %v", err)
	}
	if claimedAgain {
		t.Fatal("cross-kind claim reported claimed=true; operation IDs must share one namespace per session")
	}
	if existing.Kind != OperationKindInterrupt || existing.BindingHash != "bind-int" {
		t.Fatalf("cross-kind claim overwrote the original row: %+v", existing)
	}

	ops, _, err := store.GetRobotRequestRecords("op-int", "")
	if err != nil || len(ops) != 1 || ops[0].Kind != OperationKindInterrupt {
		t.Fatalf("GetRobotRequestRecords(op-int) = (%+v, %v), want one interrupt row", ops, err)
	}
}

// sendOperationStoreAtMigration recreates a prior schema for upgrade tests.
func sendOperationStoreAtMigration(t *testing.T, lastVersion int) *Store {
	t.Helper()
	db, err := sql.Open(sqliteutil.DriverName, sqliteutil.MemoryDSN("foreign_keys(1)"))
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	files, err := GetMigrationFiles()
	if err != nil {
		t.Fatalf("GetMigrationFiles: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE _migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create _migrations: %v", err)
	}
	// Bring the schema to the requested version exactly as an older ntm
	// would have left it, before any newer schema is applied.
	for _, filename := range files {
		var version int
		if _, err := fmt.Sscanf(filename, "%03d_", &version); err != nil {
			t.Fatalf("parse migration version %s: %v", filename, err)
		}
		if version > lastVersion {
			break
		}
		content, err := ReadMigration(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		if _, err := db.Exec(content); err != nil {
			t.Fatalf("apply %s: %v", filename, err)
		}
		if _, err := db.Exec(`INSERT INTO _migrations (version, name) VALUES (?, ?)`, version, filename); err != nil {
			t.Fatalf("record %s: %v", filename, err)
		}
	}
	return &Store{db: db, path: ":memory:"}
}

// A database migrated before kinds existed keeps its send rows readable and
// replayable: migration 025 backfills kind='send' and leaves the
// (operation_id, session_name) key the idempotency invariant depends on.
func TestSendOperationsKindMigrationBackfillsLegacyRows(t *testing.T) {
	store := sendOperationStoreAtMigration(t, 24)
	db := store.db
	if _, err := db.Exec(`
		INSERT INTO send_operations (
			operation_id, session_name, binding_hash, payload_sha256,
			payload_bytes, status, outcome_json, created_at, completed_at
		) VALUES ('op-pre', 'proj', 'bind-pre', 'sha-pre', 3, ?, '{"success":true}', ?, ?)`,
		SendOperationCompleted, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("seed legacy send row: %v", err)
	}

	if err := ApplyMigrations(db); err != nil {
		t.Fatalf("ApplyMigrations upgrade: %v", err)
	}
	got, err := store.GetSendOperation("op-pre", "proj")
	if err != nil {
		t.Fatalf("GetSendOperation after upgrade: %v", err)
	}
	if got == nil || got.Kind != OperationKindSend || got.BindingHash != "bind-pre" || got.Status != SendOperationCompleted {
		t.Fatalf("legacy row after upgrade = %+v, want completed send row with original binding", got)
	}

	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'send_operations'`).Scan(&tableSQL); err != nil {
		t.Fatalf("read send_operations schema: %v", err)
	}
	if !strings.Contains(tableSQL, "PRIMARY KEY (operation_id, session_name)") {
		t.Fatalf("send_operations lost its (operation_id, session_name) key after upgrade:\n%s", tableSQL)
	}
}

func TestSendOperationsClaimFenceMigrationPreservesUnknownDelivery(t *testing.T) {
	store := sendOperationStoreAtMigration(t, 25)
	createdAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	completedAt := createdAt.Add(time.Minute)
	const outcome = `{"success":true,"sent_to":["proj:1"]}`
	for _, kind := range []string{OperationKindSend, OperationKindInterrupt} {
		if _, err := store.db.Exec(`
			INSERT INTO send_operations (
				operation_id, session_name, kind, binding_hash, payload_sha256,
				payload_bytes, status, created_at
			) VALUES (?, 'proj', ?, 'legacy-binding', 'legacy-digest', 77, ?, ?)`,
			"legacy-"+kind, kind, SendOperationInProgress, createdAt); err != nil {
			t.Fatalf("seed legacy %s: %v", kind, err)
		}
	}
	if _, err := store.db.Exec(`
		INSERT INTO send_operations (
			operation_id, session_name, kind, binding_hash, payload_sha256,
			payload_bytes, status, outcome_json, created_at, completed_at
		) VALUES ('legacy-completed', 'proj', 'send', 'completed-binding', 'completed-digest', 78, ?, ?, ?, ?)`,
		SendOperationCompleted, outcome, createdAt, completedAt); err != nil {
		t.Fatalf("seed completed receipt: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("upgrade claim fence: %v", err)
	}

	tokens := make(map[string]bool)
	for _, kind := range []string{OperationKindSend, OperationKindInterrupt} {
		id := "legacy-" + kind
		op, err := store.GetSendOperation(id, "proj")
		if err != nil || op == nil {
			t.Fatalf("read migrated %s: %+v, %v", kind, op, err)
		}
		if op.Kind != kind || op.Status != SendOperationInProgress || op.BindingHash != "legacy-binding" || op.PayloadSHA256 != "legacy-digest" || op.PayloadBytes != 77 || !op.CreatedAt.Equal(createdAt) {
			t.Fatalf("migration changed legacy operation: %+v", op)
		}
		if op.DispatchStartedAt == nil || !op.DispatchStartedAt.Equal(createdAt) || op.CompletedAt != nil || op.Targets == nil || len(op.Targets) != 0 {
			t.Fatalf("legacy delivery uncertainty was not preserved: %+v", op)
		}
		if len(op.ClaimToken) != 64 || tokens[op.ClaimToken] {
			t.Fatalf("migration produced empty or reused ownership token for %s", id)
		}
		tokens[op.ClaimToken] = true
		taken, err := store.TakeOverStaleSendOperation(id, "proj", op.BindingHash, op.ClaimToken, time.Now().UTC())
		if err != nil || taken != nil {
			t.Fatalf("unknown legacy delivery was taken over: %+v, %v", taken, err)
		}
		if _, err := store.StartSendOperation(op); !errors.Is(err, ErrSendOperationClaimLost) {
			t.Fatalf("legacy unknown operation could start again: %v", err)
		}
		if err := store.ReleaseSendOperation(id, "proj", op.ClaimToken); !errors.Is(err, ErrSendOperationClaimLost) {
			t.Fatalf("legacy unknown operation could be discarded: %v", err)
		}
	}
	completed, err := store.GetSendOperation("legacy-completed", "proj")
	if err != nil || completed == nil {
		t.Fatalf("read migrated completed receipt: %+v, %v", completed, err)
	}
	if completed.Status != SendOperationCompleted || completed.OutcomeJSON != outcome || completed.BindingHash != "completed-binding" || completed.PayloadSHA256 != "completed-digest" || completed.PayloadBytes != 78 || !completed.CreatedAt.Equal(createdAt) || completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) || completed.DispatchStartedAt != nil {
		t.Fatalf("migration changed a completed receipt: %+v", completed)
	}
	if len(completed.ClaimToken) != 64 || tokens[completed.ClaimToken] {
		t.Fatal("completed legacy receipt has no distinct claim token")
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	again, err := store.GetSendOperation("legacy-completed", "proj")
	if err != nil || again == nil || again.ClaimToken != completed.ClaimToken || again.OutcomeJSON != outcome {
		t.Fatalf("repeat migration changed receipt: %+v, %v", again, err)
	}
	fresh, claimed, err := store.ClaimSendOperation(&SendOperation{OperationID: "fresh-after-upgrade", SessionName: "proj"})
	if err != nil || !claimed || fresh.DispatchStartedAt != nil || fresh.ClaimToken == "" {
		t.Fatalf("new claim inherited legacy uncertainty: %+v, %v", fresh, err)
	}
}

func TestGetSendOperationUnknownID(t *testing.T) {
	store := testStore(t)
	got, err := store.GetSendOperation("missing", "proj")
	if err != nil {
		t.Fatalf("get unknown error = %v", err)
	}
	if got != nil {
		t.Errorf("get unknown = %+v, want nil", got)
	}
}

func TestClaimSendOperationRequiresID(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.ClaimSendOperation(&SendOperation{SessionName: "proj"}); err == nil {
		t.Fatal("claim without ID succeeded; want error")
	}
	if _, _, err := store.ClaimSendOperation(&SendOperation{OperationID: "op"}); err == nil {
		t.Fatal("claim without session succeeded; want error")
	}
}

// Sessions form independent operation-ID namespaces: the same ID claimed in
// two sessions creates two rows rather than colliding.
func TestClaimSendOperationSessionScoped(t *testing.T) {
	store := testStore(t)

	_, claimedA, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "step-1", SessionName: "projA", BindingHash: "bind-a",
	})
	if err != nil || !claimedA {
		t.Fatalf("session A claim = (claimed=%v, err=%v), want fresh claim", claimedA, err)
	}
	_, claimedB, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "step-1", SessionName: "projB", BindingHash: "bind-b",
	})
	if err != nil || !claimedB {
		t.Fatalf("session B claim = (claimed=%v, err=%v), want independent fresh claim", claimedB, err)
	}

	ops, _, err := store.GetRobotRequestRecords("step-1", "")
	if err != nil {
		t.Fatalf("get by ID error = %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("rows for step-1 = %d, want 2", len(ops))
	}
}

func TestReleaseSendOperation(t *testing.T) {
	store := testStore(t)
	op := &SendOperation{OperationID: "op-r", SessionName: "proj", BindingHash: "b"}
	stored, _, err := store.ClaimSendOperation(op)
	if err != nil {
		t.Fatalf("claim error = %v", err)
	}

	// Release frees the ID for a fresh claim.
	if err := store.ReleaseSendOperation("op-r", "proj", stored.ClaimToken); err != nil {
		t.Fatalf("release error = %v", err)
	}
	reclaimed, claimed, err := store.ClaimSendOperation(op)
	if err != nil || !claimed {
		t.Fatalf("re-claim after release = (claimed=%v, err=%v), want fresh claim", claimed, err)
	}
	if reclaimed.ClaimToken == stored.ClaimToken {
		t.Fatal("re-claim reused the released attempt's ownership token")
	}
	if err := store.ReleaseSendOperation("op-r", "proj", stored.ClaimToken); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("released claimant could release a new row: %v", err)
	}

	// A completed row is never released.
	if err := store.CompleteSendOperation("op-r", "proj", reclaimed.ClaimToken, `{"success":true}`, time.Now()); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	if err := store.ReleaseSendOperation("op-r", "proj", reclaimed.ClaimToken); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("release completed error = %v, want claim-lost", err)
	}
	got, _ := store.GetSendOperation("op-r", "proj")
	if got == nil || got.Status != SendOperationCompleted {
		t.Fatalf("completed row after release attempt = %+v, want intact", got)
	}
}

func TestTakeOverStaleSendOperation(t *testing.T) {
	store := testStore(t)
	stale := time.Now().UTC().Add(-time.Hour)
	stored, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-t", SessionName: "proj", BindingHash: "b", CreatedAt: stale,
	})
	if err != nil {
		t.Fatalf("claim error = %v", err)
	}

	// A fresh claim (created now) must not be usurped.
	taken, err := store.TakeOverStaleSendOperation("op-t", "proj", "b", stored.ClaimToken, time.Now().UTC().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("takeover error = %v", err)
	}
	if taken != nil {
		t.Fatal("takeover won against a claim inside the staleness window")
	}

	// A stale claim with matching binding is taken over.
	taken, err = store.TakeOverStaleSendOperation("op-t", "proj", "b", stored.ClaimToken, time.Now().UTC().Add(-time.Minute))
	if err != nil || taken == nil {
		t.Fatalf("stale takeover = (%+v, err=%v), want success", taken, err)
	}
	if taken.ClaimToken == stored.ClaimToken || !taken.CreatedAt.After(stale) || taken.DispatchStartedAt != nil {
		t.Fatalf("takeover did not fence and refresh its claim: %+v", taken)
	}

	// Mismatched binding never takes over.
	wrongBinding, err := store.TakeOverStaleSendOperation("op-t", "proj", "OTHER", taken.ClaimToken, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("mismatched takeover error = %v", err)
	}
	if wrongBinding != nil {
		t.Fatal("takeover won with a mismatched binding hash")
	}
}

func TestSendOperationStaleOwnerCannotMutateSuccessorAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	original, err := Open(path)
	if err != nil {
		t.Fatalf("open original store: %v", err)
	}
	t.Cleanup(func() { _ = original.Close() })
	if err := original.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	successor, err := Open(path)
	if err != nil {
		t.Fatalf("open successor store: %v", err)
	}
	t.Cleanup(func() { _ = successor.Close() })

	old, claimed, err := original.ClaimSendOperation(&SendOperation{
		OperationID: "op-fenced", SessionName: "proj", Kind: OperationKindInterrupt,
		BindingHash: "binding", PayloadSHA256: "before", PayloadBytes: 10,
		Targets: []string{"proj:1"}, CreatedAt: time.Now().UTC().Add(-time.Hour),
	})
	if err != nil || !claimed {
		t.Fatalf("initial claim: %+v, %v", old, err)
	}
	owned, err := successor.TakeOverStaleSendOperation(old.OperationID, old.SessionName, old.BindingHash, old.ClaimToken, time.Now().UTC().Add(-time.Minute))
	if err != nil || owned == nil {
		t.Fatalf("takeover: %+v, %v", owned, err)
	}
	if owned.ClaimToken == old.ClaimToken || owned.Kind != old.Kind || owned.BindingHash != old.BindingHash || !slices.Equal(owned.Targets, old.Targets) {
		t.Fatalf("takeover failed to retain the operation while fencing its owner: %+v", owned)
	}
	// Even a cutoff that would consider the successor stale cannot reuse the
	// old observation's token. A delayed contender must reread and retry.
	if taken, err := original.TakeOverStaleSendOperation(old.OperationID, old.SessionName, old.BindingHash, old.ClaimToken, time.Now().UTC().Add(time.Hour)); err != nil || taken != nil {
		t.Fatalf("old observation stole the successor: %+v, %v", taken, err)
	}
	old.PayloadSHA256 = "stale-writer-payload"
	old.Targets = []string{"proj:99"}
	if _, err := original.StartSendOperation(old); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("stale owner start = %v, want claim-lost", err)
	}
	if err := original.ReleaseSendOperation(old.OperationID, old.SessionName, old.ClaimToken); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("stale owner release = %v, want claim-lost", err)
	}
	if err := original.CompleteSendOperation(old.OperationID, old.SessionName, old.ClaimToken, `{"owner":"stale"}`, time.Now()); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("stale owner completion = %v, want claim-lost", err)
	}
	current, err := successor.GetSendOperation(old.OperationID, old.SessionName)
	if err != nil || current == nil || current.ClaimToken != owned.ClaimToken || current.DispatchStartedAt != nil || current.PayloadSHA256 != "before" || current.Status != SendOperationInProgress || current.OutcomeJSON != "" {
		t.Fatalf("stale writer changed the successor: %+v, %v", current, err)
	}

	// The successor resolves new targets and freezes its own prepared bytes
	// at the boundary. Even its current token cannot restart or discard it.
	owned.PayloadSHA256, owned.PayloadBytes = "after", 20
	owned.Targets = []string{"proj:2", "proj:3"}
	started, err := successor.StartSendOperation(owned)
	if err != nil {
		t.Fatalf("successor start: %v", err)
	}
	if taken, err := original.TakeOverStaleSendOperation(started.OperationID, started.SessionName, started.BindingHash, started.ClaimToken, time.Now().UTC().Add(time.Hour)); err != nil || taken != nil {
		t.Fatalf("started operation was taken over: %+v, %v", taken, err)
	}
	if err := successor.ReleaseSendOperation(started.OperationID, started.SessionName, started.ClaimToken); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("started operation was released: %v", err)
	}
	if _, err := successor.StartSendOperation(owned); !errors.Is(err, ErrSendOperationClaimLost) {
		t.Fatalf("started operation started twice: %v", err)
	}
	const outcome = `{"owner":"successor","success":true}`
	if err := successor.CompleteSendOperation(started.OperationID, started.SessionName, started.ClaimToken, outcome, time.Now()); err != nil {
		t.Fatalf("successor completion: %v", err)
	}
	completed, err := original.GetSendOperation(started.OperationID, started.SessionName)
	if err != nil || completed == nil || completed.Status != SendOperationCompleted || completed.OutcomeJSON != outcome || completed.PayloadSHA256 != "after" || completed.PayloadBytes != 20 || !slices.Equal(completed.Targets, owned.Targets) || completed.DispatchStartedAt == nil {
		t.Fatalf("receipt did not retain successor's delivery: %+v, %v", completed, err)
	}
}

func TestSendOperationStartAndTakeoverHaveOneWinnerAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	owner, err := Open(path)
	if err != nil {
		t.Fatalf("open owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := owner.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	contender, err := Open(path)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	t.Cleanup(func() { _ = contender.Close() })

	type attemptResult struct {
		op  *SendOperation
		err error
	}
	for i := 0; i < 20; i++ {
		claimed, fresh, err := owner.ClaimSendOperation(&SendOperation{
			OperationID: fmt.Sprintf("op-race-%d", i), SessionName: "proj",
			BindingHash: "binding", CreatedAt: time.Now().UTC().Add(-time.Hour),
		})
		if err != nil || !fresh {
			t.Fatalf("claim %d: %+v, %v", i, claimed, err)
		}
		ready := make(chan struct{})
		started := make(chan attemptResult, 1)
		taken := make(chan attemptResult, 1)
		go func() {
			<-ready
			op, err := owner.StartSendOperation(claimed)
			started <- attemptResult{op, err}
		}()
		go func() {
			<-ready
			op, err := contender.TakeOverStaleSendOperation(claimed.OperationID, claimed.SessionName, claimed.BindingHash, claimed.ClaimToken, time.Now().UTC().Add(-time.Minute))
			taken <- attemptResult{op, err}
		}()
		close(ready)
		startResult, takeoverResult := <-started, <-taken
		if takeoverResult.err != nil {
			t.Fatalf("race %d takeover: %v", i, takeoverResult.err)
		}
		if startResult.err != nil && !errors.Is(startResult.err, ErrSendOperationClaimLost) {
			t.Fatalf("race %d start: %v", i, startResult.err)
		}
		startWon, takeoverWon := startResult.op != nil, takeoverResult.op != nil
		if startWon == takeoverWon {
			t.Fatalf("race %d has start winner=%v, takeover winner=%v; exactly one must win", i, startWon, takeoverWon)
		}
		if takeoverWon {
			if _, err := contender.StartSendOperation(takeoverResult.op); err != nil {
				t.Fatalf("race %d winning takeover could not start: %v", i, err)
			}
		}
		persisted, err := owner.GetSendOperation(claimed.OperationID, claimed.SessionName)
		if err != nil || persisted == nil || persisted.DispatchStartedAt == nil {
			t.Fatalf("race %d winner has no durable boundary: %+v, %v", i, persisted, err)
		}
		wantToken := claimed.ClaimToken
		if takeoverWon {
			wantToken = takeoverResult.op.ClaimToken
		}
		if persisted.ClaimToken != wantToken {
			t.Fatalf("race %d persisted a non-winning token", i)
		}
	}
}

func TestGCCompletedSendOperations(t *testing.T) {
	store := testStore(t)
	stored, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-old", SessionName: "proj", BindingHash: "b",
	})
	if err != nil {
		t.Fatalf("claim error = %v", err)
	}
	if err := store.CompleteSendOperation("op-old", "proj", stored.ClaimToken, `{}`, time.Now().UTC().Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("complete error = %v", err)
	}
	// An in_progress row is never GC'd regardless of age.
	if _, _, err := store.ClaimSendOperation(&SendOperation{
		OperationID: "op-live", SessionName: "proj", BindingHash: "b",
		CreatedAt: time.Now().UTC().Add(-60 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("claim error = %v", err)
	}

	pruned, err := store.GCCompletedSendOperations(0)
	if err != nil {
		t.Fatalf("gc error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if got, _ := store.GetSendOperation("op-old", "proj"); got != nil {
		t.Error("old completed row survived GC")
	}
	if got, _ := store.GetSendOperation("op-live", "proj"); got == nil {
		t.Error("in_progress row was GC'd")
	}
}

func TestGCStaleOutputSeqWatermarks(t *testing.T) {
	store := testStore(t)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	fresh := time.Now().UTC()

	for _, wm := range []*OutputWatermark{
		{WatermarkType: "output_seq", Scope: "dead|%1", Consumer: "e1", CreatedAt: old, UpdatedAt: old},
		{WatermarkType: "output_seq", Scope: "live|%2", Consumer: "e2", CreatedAt: old, UpdatedAt: fresh},
		// Velocity baselines keep their pre-existing lifecycle: never GC'd here.
		{WatermarkType: "velocity", Scope: "dead|%1", CreatedAt: old, UpdatedAt: old},
	} {
		if err := store.SetWatermark(wm); err != nil {
			t.Fatalf("seed watermark %s/%s: %v", wm.WatermarkType, wm.Scope, err)
		}
	}

	pruned, err := store.GCStaleOutputSeqWatermarks(0)
	if err != nil {
		t.Fatalf("gc error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if wm, _ := store.GetWatermark("output_seq", "dead|%1"); wm != nil {
		t.Error("stale output_seq row survived GC")
	}
	if wm, _ := store.GetWatermark("output_seq", "live|%2"); wm == nil {
		t.Error("recently-observed output_seq row was pruned")
	}
	if wm, _ := store.GetWatermark("velocity", "dead|%1"); wm == nil {
		t.Error("velocity watermark was pruned by output_seq GC")
	}
}
