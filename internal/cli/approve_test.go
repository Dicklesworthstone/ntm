package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

func withTestApprovalStore(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	orig := openApprovalStateStoreFunc
	openApprovalStateStoreFunc = func() (*state.Store, error) {
		st, err := state.Open(dbPath)
		if err != nil {
			return nil, err
		}
		if err := st.Migrate(); err != nil {
			_ = st.Close()
			return nil, err
		}
		return st, nil
	}
	t.Cleanup(func() { openApprovalStateStoreFunc = orig })
	return dbPath
}

func TestApproveListExpiredAndExpireCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NTM_USER", "ops-reviewer")
	dbPath := withTestApprovalStore(t)

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateApproval(&state.Approval{
		ID:            "appr-live",
		Action:        "force_release",
		Resource:      "internal/cli/**",
		Reason:        "active request",
		RequestedBy:   "agent-a",
		CorrelationID: "corr-live",
		CreatedAt:     now.Add(-5 * time.Minute),
		ExpiresAt:     now.Add(25 * time.Minute),
		Status:        state.ApprovalPending,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("CreateApproval(live): %v", err)
	}
	if err := store.CreateApproval(&state.Approval{
		ID:            "appr-expired",
		Action:        "git_push_force",
		Resource:      "main",
		Reason:        "timed out request",
		RequestedBy:   "agent-b",
		CorrelationID: "corr-expired",
		CreatedAt:     now.Add(-45 * time.Minute),
		ExpiresAt:     now.Add(-15 * time.Minute),
		Status:        state.ApprovalPending,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("CreateApproval(expired): %v", err)
	}
	_ = store.Close()

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	// 1. Default `approve list` returns only active pending requests + expired_count=1.
	listRaw, err := captureStdout(t, func() error {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"list"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("approve list failed: %v", err)
	}
	var listOut ApprovalsListOutput
	if err := json.Unmarshal([]byte(listRaw), &listOut); err != nil {
		t.Fatalf("decode approve list: %v (raw=%q)", err, listRaw)
	}
	if listOut.Count != 1 || listOut.Pending[0].ID != "appr-live" {
		t.Fatalf("expected [appr-live], got %+v", listOut.Pending)
	}
	if listOut.ExpiredCount != 1 {
		t.Fatalf("ExpiredCount = %d, want 1", listOut.ExpiredCount)
	}

	// 2. `approve list --expired` returns the expired pending request.
	expiredListRaw, err := captureStdout(t, func() error {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"list", "--expired"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("approve list --expired failed: %v", err)
	}
	var expiredListOut ApprovalsListOutput
	if err := json.Unmarshal([]byte(expiredListRaw), &expiredListOut); err != nil {
		t.Fatalf("decode approve list --expired: %v (raw=%q)", err, expiredListRaw)
	}
	if !expiredListOut.ExpiredOnly || expiredListOut.Count != 1 || expiredListOut.Pending[0].ID != "appr-expired" {
		t.Fatalf("unexpected expired list output: %+v", expiredListOut)
	}

	// 3. `approve expire` transitions expired pending requests to status=expired and records state audit.
	expireRaw, err := captureStdout(t, func() error {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"expire"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("approve expire failed: %v", err)
	}
	var expireOut ApprovalExpireOutput
	if err := json.Unmarshal([]byte(expireRaw), &expireOut); err != nil {
		t.Fatalf("decode approve expire: %v (raw=%q)", err, expireRaw)
	}
	if !expireOut.Success || expireOut.ExpiredCount != 1 || expireOut.Expired[0].ID != "appr-expired" || expireOut.Expired[0].Status != state.ApprovalExpired {
		t.Fatalf("unexpected approve expire output: %+v", expireOut)
	}

	verifyStore, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen state.db: %v", err)
	}
	defer verifyStore.Close()

	gotAppr, err := verifyStore.GetApproval("appr-expired")
	if err != nil {
		t.Fatalf("GetApproval(appr-expired): %v", err)
	}
	if gotAppr == nil || gotAppr.Status != state.ApprovalExpired {
		t.Fatalf("stored approval = %+v, want status=expired", gotAppr)
	}

	auditEvents, err := verifyStore.GetAuditHistory("approval", "appr-expired", 10)
	if err != nil {
		t.Fatalf("GetAuditHistory: %v", err)
	}
	if len(auditEvents) != 1 || auditEvents[0].EventType != "approval.expired" || auditEvents[0].NewState != string(state.ApprovalExpired) {
		t.Fatalf("unexpected state audit events for expired approval: %+v", auditEvents)
	}
}

func TestApproveAndDenyPersistDurableStateAudit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NTM_USER", "alice-approver")
	dbPath := withTestApprovalStore(t)

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"appr-ok", "appr-no"} {
		if err := store.CreateApproval(&state.Approval{
			ID:            id,
			Action:        "force_release",
			Resource:      "internal/state/**",
			Reason:        "needs lock override",
			RequestedBy:   "agent-requester",
			CorrelationID: "corr-" + id,
			CreatedAt:     now.Add(-1 * time.Minute),
			ExpiresAt:     now.Add(20 * time.Minute),
			Status:        state.ApprovalPending,
		}); err != nil {
			_ = store.Close()
			t.Fatalf("CreateApproval(%s): %v", id, err)
		}
	}
	_ = store.Close()

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	if _, err := captureStdout(t, func() error {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"appr-ok"})
		return cmd.Execute()
	}); err != nil {
		t.Fatalf("approve appr-ok failed: %v", err)
	}

	if _, err := captureStdout(t, func() error {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"deny", "appr-no", "--reason", "lock still active"})
		return cmd.Execute()
	}); err != nil {
		t.Fatalf("approve deny appr-no failed: %v", err)
	}

	verifyStore, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen state.db: %v", err)
	}
	defer verifyStore.Close()

	actor, err := verifyStore.GetAuditActor("user", "alice-approver")
	if err != nil {
		t.Fatalf("GetAuditActor: %v", err)
	}
	if actor == nil || actor.DisplayName != "alice-approver" {
		t.Fatalf("expected persisted AuditActor for alice-approver, got %+v", actor)
	}

	okHistory, err := verifyStore.GetAuditHistory("approval", "appr-ok", 10)
	if err != nil {
		t.Fatalf("GetAuditHistory(appr-ok): %v", err)
	}
	if len(okHistory) != 1 || okHistory[0].EventType != "approval.approved" || okHistory[0].NewState != string(state.ApprovalApproved) {
		t.Fatalf("unexpected audit history for appr-ok: %+v", okHistory)
	}

	denyHistory, err := verifyStore.GetAuditHistory("approval", "appr-no", 10)
	if err != nil {
		t.Fatalf("GetAuditHistory(appr-no): %v", err)
	}
	if len(denyHistory) != 1 || denyHistory[0].EventType != "approval.denied" || denyHistory[0].Reason != "lock still active" {
		t.Fatalf("unexpected audit history for appr-no: %+v", denyHistory)
	}
}
