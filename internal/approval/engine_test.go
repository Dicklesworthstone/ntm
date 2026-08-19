package approval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func setupTestStore(t *testing.T) *state.Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	// Run migrations to create tables
	if err := store.Migrate(); err != nil {
		t.Fatalf("Failed to migrate store: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
		os.Remove(dbPath)
	})

	return store
}

func TestNewEngine(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())
	if engine == nil {
		t.Fatal("New returned nil")
	}
}

func TestRequest(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	approval, err := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "internal/auth/**",
		Reason:      "Agent crashed",
		RequestedBy: "system",
	})

	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if approval.ID == "" {
		t.Error("Approval ID should be set")
	}
	if approval.Status != state.ApprovalPending {
		t.Errorf("Status should be pending, got %s", approval.Status)
	}
	if approval.Action != "force_release" {
		t.Errorf("Action should be force_release, got %s", approval.Action)
	}
}

func TestCheck(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	created, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "tester",
	})

	checked, err := engine.Check(ctx, created.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if checked.ID != created.ID {
		t.Error("Checked ID doesn't match created ID")
	}
	if checked.Status != state.ApprovalPending {
		t.Errorf("Status should be pending, got %s", checked.Status)
	}
}

func TestApprove(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "requester",
	})

	err := engine.Approve(ctx, approval.ID, "approver")
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}

	checked, _ := engine.Check(ctx, approval.ID)
	if checked.Status != state.ApprovalApproved {
		t.Errorf("Status should be approved, got %s", checked.Status)
	}
	if checked.ApprovedBy != "approver" {
		t.Errorf("ApprovedBy should be approver, got %s", checked.ApprovedBy)
	}
}

func TestDeny(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "requester",
	})

	err := engine.Deny(ctx, approval.ID, "denier", "Too risky")
	if err != nil {
		t.Fatalf("Deny failed: %v", err)
	}

	checked, _ := engine.Check(ctx, approval.ID)
	if checked.Status != state.ApprovalDenied {
		t.Errorf("Status should be denied, got %s", checked.Status)
	}
	if checked.DeniedReason != "Too risky" {
		t.Errorf("DeniedReason should be 'Too risky', got %s", checked.DeniedReason)
	}
}

func TestSLBEnforcement(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "sensitive_file",
		RequestedBy: "alice",
		RequiresSLB: true,
	})

	// Same person should not be able to approve
	err := engine.Approve(ctx, approval.ID, "alice")
	if err == nil {
		t.Error("SLB should prevent self-approval")
	}

	// Different person should be able to approve
	err = engine.Approve(ctx, approval.ID, "bob")
	if err != nil {
		t.Errorf("Different person should be able to approve: %v", err)
	}
}

func TestSLBApproverList(t *testing.T) {
	store := setupTestStore(t)
	cfg := DefaultConfig()
	cfg.ApproverList = []string{"admin", "manager"}
	engine := New(store, nil, nil, cfg)

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "force_release",
		Resource:    "sensitive_file",
		RequestedBy: "alice",
		RequiresSLB: true,
	})

	// Non-admin should not be able to approve
	err := engine.Approve(ctx, approval.ID, "bob")
	if err == nil {
		t.Error("Non-admin should not be able to approve when ApproverList is set")
	}

	// Admin should be able to approve
	err = engine.Approve(ctx, approval.ID, "admin")
	if err != nil {
		t.Errorf("Admin should be able to approve: %v", err)
	}
}

func TestApproverListAppliesToNonSLB(t *testing.T) {
	store := setupTestStore(t)
	cfg := DefaultConfig()
	cfg.ApproverList = []string{"admin"}
	engine := New(store, nil, nil, cfg)

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "requester",
	})

	err := engine.Approve(ctx, approval.ID, "bob")
	if err == nil {
		t.Fatal("non-admin should not be able to approve when ApproverList is set")
	}

	checked, _ := engine.Check(ctx, approval.ID)
	if checked.Status != state.ApprovalPending {
		t.Fatalf("approval should remain pending after rejected approver, got %s", checked.Status)
	}

	err = engine.Approve(ctx, approval.ID, "admin")
	if err != nil {
		t.Fatalf("admin should be able to approve non-SLB request: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "requester",
		ExpiresIn:   1 * time.Millisecond, // Very short expiry
	})

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Check should mark it as expired
	checked, err := engine.Check(ctx, approval.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if checked.Status != state.ApprovalExpired {
		t.Errorf("Status should be expired, got %s", checked.Status)
	}

	// Should not be able to approve expired request
	err = engine.Approve(ctx, approval.ID, "approver")
	if err == nil {
		t.Error("Should not be able to approve expired request")
	}
}

func TestListPending(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()

	// Create some approvals
	engine.Request(ctx, RequestParams{
		Action:      "action1",
		Resource:    "resource1",
		RequestedBy: "requester1",
	})
	engine.Request(ctx, RequestParams{
		Action:      "action2",
		Resource:    "resource2",
		RequestedBy: "requester2",
	})

	pending, err := engine.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}

	if len(pending) != 2 {
		t.Errorf("Expected 2 pending approvals, got %d", len(pending))
	}
}

// TestHistoryThreeStates is the H9 close condition
// (bd-ws7-docs-ux-truth-tqh3l.9): a fixture with one pending, one approved,
// and one denied approval — plus a consumed one for the full resolved set —
// against a REAL temp SQLite store. History must return every record with
// correct status, decider, and decision timestamp; the pre-fix code returned
// pending-only while self-labeling as history.
func TestHistoryThreeStates(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())
	ctx := context.Background()

	mkReq := func(action string) string {
		t.Helper()
		appr, err := engine.Request(ctx, RequestParams{
			Action:      action,
			Resource:    "res-" + action,
			RequestedBy: "requester",
		})
		if err != nil {
			t.Fatalf("Request(%s) failed: %v", action, err)
		}
		return appr.ID
	}

	pendingID := mkReq("stay_pending")
	approvedID := mkReq("get_approved")
	deniedID := mkReq("get_denied")
	consumedID := mkReq("get_consumed")

	if err := engine.Approve(ctx, approvedID, "alice"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if err := engine.Deny(ctx, deniedID, "bob", "not safe"); err != nil {
		t.Fatalf("Deny failed: %v", err)
	}
	if err := engine.Approve(ctx, consumedID, "carol"); err != nil {
		t.Fatalf("Approve (for consume) failed: %v", err)
	}
	if err := engine.Consume(ctx, consumedID, "gate"); err != nil {
		t.Fatalf("Consume failed: %v", err)
	}

	history, err := engine.History(ctx)
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("History returned %d records, want 4 (pending+approved+denied+consumed)", len(history))
	}

	byID := map[string]state.Approval{}
	for _, a := range history {
		byID[a.ID] = a
	}

	p, ok := byID[pendingID]
	if !ok {
		t.Fatalf("pending record %s missing from history", pendingID)
	}
	if p.Status != state.ApprovalPending {
		t.Errorf("pending record status = %s, want pending", p.Status)
	}

	a, ok := byID[approvedID]
	if !ok {
		t.Fatalf("approved record %s missing from history — resolved records must be included", approvedID)
	}
	if a.Status != state.ApprovalApproved {
		t.Errorf("approved record status = %s, want approved", a.Status)
	}
	if a.ApprovedBy != "alice" {
		t.Errorf("approved record decider = %q, want alice", a.ApprovedBy)
	}
	if a.ApprovedAt == nil {
		t.Error("approved record has no decision timestamp")
	}

	d, ok := byID[deniedID]
	if !ok {
		t.Fatalf("denied record %s missing from history — resolved records must be included", deniedID)
	}
	if d.Status != state.ApprovalDenied {
		t.Errorf("denied record status = %s, want denied", d.Status)
	}
	if d.ApprovedBy != "bob" {
		t.Errorf("denied record decider = %q, want bob", d.ApprovedBy)
	}
	if d.DeniedReason != "not safe" {
		t.Errorf("denied record reason = %q, want %q", d.DeniedReason, "not safe")
	}
	if d.ApprovedAt == nil {
		t.Error("denied record has no decision timestamp")
	}

	c, ok := byID[consumedID]
	if !ok {
		t.Fatalf("consumed record %s missing from history", consumedID)
	}
	if c.Status != state.ApprovalConsumed {
		t.Errorf("consumed record status = %s, want consumed", c.Status)
	}
}

func assertNoWaiterBucket(t *testing.T, engine *Engine, id string) {
	t.Helper()

	engine.waitersMu.Lock()
	defer engine.waitersMu.Unlock()
	if waiters, ok := engine.waiters[id]; ok {
		t.Fatalf("waiter bucket for %q still present with %d waiters", id, len(waiters))
	}
}

func TestEventEmission(t *testing.T) {
	store := setupTestStore(t)
	eventBus := events.NewEventBus(100)
	engine := New(store, nil, eventBus, DefaultConfig())

	// Subscribe to events
	received := make([]string, 0)
	var mu sync.Mutex
	eventBus.SubscribeAll(func(e events.BusEvent) {
		mu.Lock()
		received = append(received, e.EventType())
		mu.Unlock()
	})

	ctx := context.Background()
	approval, _ := engine.Request(ctx, RequestParams{
		Action:      "test_action",
		Resource:    "test_resource",
		RequestedBy: "requester",
	})

	engine.Approve(ctx, approval.ID, "approver")

	// Wait for events to be processed
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(received) < 2 {
		t.Errorf("Expected at least 2 events, got %d", len(received))
	}
}

// =============================================================================
// Pure function tests
// =============================================================================

func TestBuildSLBCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		action   string
		resource string
		want     string
	}{
		{"both present", "force_release", "internal/auth/**", "ntm approval: force_release internal/auth/**"},
		{"action only", "force_release", "", "ntm approval: force_release"},
		{"empty action", "", "resource", ""},
		{"both empty", "", "", ""},
		{"whitespace action", "  force_release  ", "  res  ", "ntm approval: force_release res"},
		{"whitespace only action", "   ", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildSLBCommand(tc.action, tc.resource)
			if got != tc.want {
				t.Errorf("buildSLBCommand(%q, %q) = %q, want %q", tc.action, tc.resource, got, tc.want)
			}
		})
	}
}

func TestParseSLBRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"empty", nil, ""},
		{"empty bytes", json.RawMessage{}, ""},
		{"valid id", json.RawMessage(`{"id":"req-123"}`), "req-123"},
		{"no id field", json.RawMessage(`{"status":"pending"}`), ""},
		{"id not string", json.RawMessage(`{"id":42}`), ""},
		{"invalid json", json.RawMessage(`{invalid}`), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseSLBRequestID(tc.raw)
			if got != tc.want {
				t.Errorf("parseSLBRequestID(%s) = %q, want %q", string(tc.raw), got, tc.want)
			}
		})
	}
}

func TestContains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slice []string
		s     string
		want  bool
	}{
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty slice", []string{}, "a", false},
		{"nil slice", nil, "a", false},
		{"empty string found", []string{"", "a"}, "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := contains(tc.slice, tc.s)
			if got != tc.want {
				t.Errorf("contains(%v, %q) = %v, want %v", tc.slice, tc.s, got, tc.want)
			}
		})
	}
}

func TestGenerateApprovalID(t *testing.T) {
	t.Parallel()

	id1 := generateApprovalID()
	id2 := generateApprovalID()

	if id1 == "" {
		t.Error("generateApprovalID() returned empty string")
	}
	if !strings.HasPrefix(id1, "appr-") {
		t.Errorf("ID should start with 'appr-', got %q", id1)
	}
	if id1 == id2 {
		t.Errorf("two calls should produce different IDs: %q == %q", id1, id2)
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	if cfg.DefaultExpiry != DefaultExpiry {
		t.Errorf("DefaultExpiry = %v, want %v", cfg.DefaultExpiry, DefaultExpiry)
	}
	if !cfg.NotifyOnRequest {
		t.Error("NotifyOnRequest should be true")
	}
	if !cfg.NotifyOnDecision {
		t.Error("NotifyOnDecision should be true")
	}
	if !cfg.EnableSLB {
		t.Error("EnableSLB should be true")
	}
}

func TestNewEngineDefaultExpiry(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, Config{})
	if engine.config.DefaultExpiry != DefaultExpiry {
		t.Errorf("expected default expiry %v, got %v", DefaultExpiry, engine.config.DefaultExpiry)
	}
}

func TestDenyAlreadyApproved(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	appr, _ := engine.Request(ctx, RequestParams{
		Action:      "test",
		Resource:    "res",
		RequestedBy: "alice",
	})

	engine.Approve(ctx, appr.ID, "bob")

	err := engine.Deny(ctx, appr.ID, "carol", "too late")
	if err == nil {
		t.Error("should not be able to deny an already-approved request")
	}
}

func TestDenyExpiredRequestMarksExpired(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	appr, _ := engine.Request(ctx, RequestParams{
		Action:      "test",
		Resource:    "res",
		RequestedBy: "alice",
		ExpiresIn:   1 * time.Millisecond,
	})

	time.Sleep(10 * time.Millisecond)

	err := engine.Deny(ctx, appr.ID, "bob", "too late")
	if err == nil {
		t.Fatal("expected denial of expired request to fail")
	}

	checked, err := engine.Check(ctx, appr.ID)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if checked.Status != state.ApprovalExpired {
		t.Fatalf("expected expired status after late denial, got %s", checked.Status)
	}
}

func TestApproveNonExistent(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	ctx := context.Background()
	err := engine.Approve(ctx, "nonexistent-id", "approver")
	if err == nil {
		t.Error("should error for non-existent approval ID")
	}
}

func TestRequestWithNilContext(t *testing.T) {
	store := setupTestStore(t)
	engine := New(store, nil, nil, DefaultConfig())

	appr, err := engine.Request(context.TODO(), RequestParams{
		Action:      "test",
		Resource:    "res",
		RequestedBy: "alice",
	})
	if err != nil {
		t.Fatalf("Request with nil context should work: %v", err)
	}
	if appr.ID == "" {
		t.Error("approval should have an ID")
	}
}
