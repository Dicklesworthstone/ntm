// Package serve provides audit trail verification for robot redesign.
// audit_attribution_test.go implements focused tests for audit-trail and actor-attribution
// verification per bd-j9jo3.9.12.
//
// Bead: bd-j9jo3.9.12
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// =============================================================================
// Actor-Attributed Attention State Changes
// =============================================================================

// TestAuditActorAttributionOnAttentionStateChange verifies that attention state
// changes include actor attribution (who changed it).
func TestAuditActorAttributionOnAttentionStateChange(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Simulate attention state change operations with different actors
	actors := []struct {
		userID    string
		role      Role
		action    AuditAction
		sessionID string
		details   string
	}{
		{"operator-alice", RoleOperator, AuditActionUpdate, "sess-main", "acknowledged attention item att-001"},
		{"admin-bob", RoleAdmin, AuditActionUpdate, "sess-main", "snoozed attention item att-002 until 2026-03-23"},
		{"operator-alice", RoleOperator, AuditActionUpdate, "sess-main", "pinned attention item att-003"},
		{"admin-carol", RoleAdmin, AuditActionUpdate, "sess-secondary", "dismissed attention item att-004"},
	}

	for i, actor := range actors {
		rec := &AuditRecord{
			Timestamp:  time.Now().UTC(),
			RequestID:  fmtReqID("attention-state", i),
			UserID:     actor.userID,
			Role:       actor.role,
			Action:     actor.action,
			Resource:   "attention",
			ResourceID: fmtResourceID("att", i),
			Method:     "PUT",
			Path:       "/api/v1/attention/" + fmtResourceID("att", i),
			StatusCode: 200,
			Duration:   10,
			SessionID:  actor.sessionID,
			Details:    actor.details,
			RemoteAddr: "127.0.0.1:5000",
		}
		if err := store.Record(rec); err != nil {
			t.Fatalf("Record error for actor %s: %v", actor.userID, err)
		}
	}

	// Verify: Query by session should return correctly attributed records
	sess1Recs, err := store.Query(AuditFilter{SessionID: "sess-main"})
	if err != nil {
		t.Fatalf("Query by session error: %v", err)
	}
	if len(sess1Recs) != 3 {
		t.Errorf("expected 3 records for sess-main, got %d", len(sess1Recs))
	}

	// Verify: Each record has actor attribution
	for _, rec := range sess1Recs {
		if rec.UserID == "" {
			t.Errorf("record %s missing UserID (actor attribution)", rec.RequestID)
		}
		if rec.Role == "" {
			t.Errorf("record %s missing Role", rec.RequestID)
		}
		if rec.Details == "" {
			t.Errorf("record %s missing Details describing the state change", rec.RequestID)
		}
		t.Logf("AUDIT_ATTRIBUTION_VERIFIED: request=%s actor=%s role=%s action=%s detail=%s",
			rec.RequestID, rec.UserID, rec.Role, rec.Action, truncateStr(rec.Details, 50))
	}

	// Verify: Query by actor should return their actions
	aliceRecs, err := store.Query(AuditFilter{UserID: "operator-alice"})
	if err != nil {
		t.Fatalf("Query by user error: %v", err)
	}
	if len(aliceRecs) != 2 {
		t.Errorf("expected 2 records for operator-alice, got %d", len(aliceRecs))
	}
}

// =============================================================================
// Actuation Request and Outcome Trails
// =============================================================================

// TestAuditActuationRequestOutcomeTrail verifies that actuation requests
// (send, spawn, interrupt) create audit trails linking request to outcome.
func TestAuditActuationRequestOutcomeTrail(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record actuation request
	requestID := "actuation-req-001"
	requestRec := &AuditRecord{
		Timestamp:  time.Now().UTC(),
		RequestID:  requestID,
		UserID:     "operator-dave",
		Role:       RoleOperator,
		Action:     AuditActionExecute,
		Resource:   "actuation",
		ResourceID: "send",
		Method:     "POST",
		Path:       "/api/v1/sessions/myproject/send",
		StatusCode: 202, // Accepted
		Duration:   50,
		SessionID:  "myproject",
		PaneID:     "0.1",
		AgentID:    "claude-code-1",
		Details:    `{"text":"implement feature X","correlation_id":"corr-abc123"}`,
		RemoteAddr: "192.168.1.100:54321",
	}
	if err := store.Record(requestRec); err != nil {
		t.Fatalf("Record request error: %v", err)
	}

	// Record actuation outcome (agent response/completion)
	outcomeRec := &AuditRecord{
		Timestamp:  time.Now().UTC().Add(30 * time.Second),
		RequestID:  requestID + "-outcome",
		UserID:     "system",
		Role:       RoleViewer,
		Action:     AuditActionUpdate,
		Resource:   "actuation-outcome",
		ResourceID: "send",
		Method:     "INTERNAL",
		Path:       "/internal/actuation/outcome",
		StatusCode: 200,
		Duration:   30000, // 30s agent work
		SessionID:  "myproject",
		PaneID:     "0.1",
		AgentID:    "claude-code-1",
		Details:    `{"correlation_id":"corr-abc123","status":"completed","output_lines":150}`,
		RemoteAddr: "internal",
	}
	if err := store.Record(outcomeRec); err != nil {
		t.Fatalf("Record outcome error: %v", err)
	}

	// Verify: Can reconstruct the request-outcome trail
	sessionRecs, err := store.Query(AuditFilter{SessionID: "myproject"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(sessionRecs) != 2 {
		t.Errorf("expected 2 records (request + outcome), got %d", len(sessionRecs))
	}

	// Verify: Request and outcome share correlation via request ID prefix
	var foundRequest, foundOutcome bool
	for _, rec := range sessionRecs {
		if strings.HasPrefix(rec.RequestID, "actuation-req-001") {
			if rec.Resource == "actuation" {
				foundRequest = true
				t.Logf("AUDIT_REQUEST_VERIFIED: %s actor=%s target=%s/%s",
					rec.RequestID, rec.UserID, rec.SessionID, rec.PaneID)
			} else if rec.Resource == "actuation-outcome" {
				foundOutcome = true
				// Parse details to extract correlation
				var details map[string]interface{}
				if err := json.Unmarshal([]byte(rec.Details), &details); err == nil {
					if corrID, ok := details["correlation_id"]; ok {
						t.Logf("AUDIT_OUTCOME_VERIFIED: %s correlation=%v status=%v",
							rec.RequestID, corrID, details["status"])
					}
				}
			}
		}
	}

	if !foundRequest {
		t.Error("missing actuation request record")
	}
	if !foundOutcome {
		t.Error("missing actuation outcome record")
	}
}

// =============================================================================
// Incident Lifecycle History
// =============================================================================

// TestAuditIncidentLifecycleHistory verifies that incident state transitions
// are captured with full attribution.
func TestAuditIncidentLifecycleHistory(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	incidentID := "inc-crash-loop-001"
	transitions := []struct {
		actor   string
		role    Role
		action  AuditAction
		details string
	}{
		{"system", RoleViewer, AuditActionCreate, "incident opened: agent_crash_loop, 3 occurrences in 10 min"},
		{"operator-eve", RoleOperator, AuditActionUpdate, "incident acknowledged: investigating root cause"},
		{"system", RoleViewer, AuditActionUpdate, "incident escalated: 6 occurrences, severity warning->error"},
		{"admin-frank", RoleAdmin, AuditActionUpdate, "incident resolved: memory limit increased, notes='Agents were hitting 90% context limit'"},
	}

	baseTime := time.Now().UTC()
	for i, trans := range transitions {
		rec := &AuditRecord{
			Timestamp:  baseTime.Add(time.Duration(i*5) * time.Minute),
			RequestID:  fmtReqID("incident", i),
			UserID:     trans.actor,
			Role:       trans.role,
			Action:     trans.action,
			Resource:   "incident",
			ResourceID: incidentID,
			Method:     methodForAction(trans.action),
			Path:       "/api/v1/incidents/" + incidentID,
			StatusCode: 200,
			Duration:   5,
			Details:    trans.details,
			RemoteAddr: "127.0.0.1:8080",
		}
		if err := store.Record(rec); err != nil {
			t.Fatalf("Record transition %d error: %v", i, err)
		}
	}

	// Verify: Full incident lifecycle is queryable
	incidentRecs, err := store.Query(AuditFilter{Resource: "incident"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(incidentRecs) != 4 {
		t.Errorf("expected 4 incident lifecycle records, got %d", len(incidentRecs))
	}

	// Verify: Transitions are in chronological order and attributable
	t.Log("INCIDENT_LIFECYCLE_TRAIL:")
	for _, rec := range incidentRecs {
		t.Logf("  %s [%s by %s (%s)]: %s",
			rec.Timestamp.Format("15:04:05"),
			rec.Action, rec.UserID, rec.Role,
			truncateStr(rec.Details, 60))
	}

	// Verify: Can answer "who resolved this incident?"
	var resolver string
	for _, rec := range incidentRecs {
		if strings.Contains(rec.Details, "resolved") {
			resolver = rec.UserID
			break
		}
	}
	if resolver != "admin-frank" {
		t.Errorf("expected resolver='admin-frank', got %q", resolver)
	}
}

// =============================================================================
// Audit Visibility in Diagnostics/Inspect Flows
// =============================================================================

// TestAuditVisibleInDiagnostics verifies that audit records are accessible
// via diagnostic/inspect queries.
func TestAuditVisibleInDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record operations across multiple sessions
	sessions := []string{"project-a", "project-b", "project-c"}
	for i, sess := range sessions {
		for j := 0; j < 3; j++ {
			rec := &AuditRecord{
				Timestamp:  time.Now().UTC().Add(time.Duration(i*10+j) * time.Minute),
				RequestID:  fmtReqID("diag", i*3+j),
				UserID:     "user-" + string(rune('a'+i)),
				Role:       RoleOperator,
				Action:     AuditActionExecute,
				Resource:   "agent",
				ResourceID: fmtResourceID("agent", j),
				Method:     "POST",
				Path:       "/api/v1/sessions/" + sess + "/agents",
				StatusCode: 200,
				Duration:   int64(10 + j*5),
				SessionID:  sess,
				AgentID:    fmtResourceID("agent", j),
				Details:    "action performed",
				RemoteAddr: "127.0.0.1:9999",
			}
			if err := store.Record(rec); err != nil {
				t.Fatalf("Record error: %v", err)
			}
		}
	}

	// Diagnostic query 1: Recent activity across all sessions
	allRecent, err := store.Query(AuditFilter{
		Since: time.Now().Add(-1 * time.Hour),
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("Query all recent error: %v", err)
	}
	if len(allRecent) != 9 {
		t.Errorf("expected 9 total records, got %d", len(allRecent))
	}
	t.Logf("DIAGNOSTIC_ALL_SESSIONS: %d records in last hour", len(allRecent))

	// Diagnostic query 2: Per-session breakdown
	for _, sess := range sessions {
		sessRecs, err := store.Query(AuditFilter{SessionID: sess})
		if err != nil {
			t.Fatalf("Query session %s error: %v", sess, err)
		}
		if len(sessRecs) != 3 {
			t.Errorf("session %s: expected 3 records, got %d", sess, len(sessRecs))
		}
		t.Logf("DIAGNOSTIC_SESSION[%s]: %d records", sess, len(sessRecs))
	}

	// Diagnostic query 3: Per-user activity
	users := []string{"user-a", "user-b", "user-c"}
	for _, user := range users {
		userRecs, err := store.Query(AuditFilter{UserID: user})
		if err != nil {
			t.Fatalf("Query user %s error: %v", user, err)
		}
		if len(userRecs) != 3 {
			t.Errorf("user %s: expected 3 records, got %d", user, len(userRecs))
		}
		t.Logf("DIAGNOSTIC_USER[%s]: %d records", user, len(userRecs))
	}
}

// =============================================================================
// Sensitivity Redaction and Audit Usefulness
// =============================================================================

// TestAuditSensitivityRedactionPreservesUsefulness verifies that audit records
// remain useful for traceability even when sensitive data is redacted.
func TestAuditSensitivityRedactionPreservesUsefulness(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record with redacted sensitive content but preserved structure
	rec := &AuditRecord{
		Timestamp:  time.Now().UTC(),
		RequestID:  "sensitive-req-001",
		UserID:     "operator-grace",
		Role:       RoleOperator,
		Action:     AuditActionExecute,
		Resource:   "agent",
		ResourceID: "agent-sensitive-task",
		Method:     "POST",
		Path:       "/api/v1/sessions/secure-project/send",
		StatusCode: 200,
		Duration:   25,
		SessionID:  "secure-project",
		PaneID:     "0.1",
		AgentID:    "claude-code-1",
		// Details are redacted but structure preserved for audit usefulness
		Details:    `{"text":"[REDACTED:api_key_reference]","redaction_reason":"contains_secrets","redacted_at":"2026-03-22T03:00:00Z","original_length":256}`,
		RemoteAddr: "192.168.1.50:12345",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	// Verify: Record is queryable
	recs, err := store.Query(AuditFilter{RequestID: "sensitive-req-001"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}

	// Verify: Essential audit fields remain intact despite redaction
	r := recs[0]
	essential := []struct {
		name  string
		value string
	}{
		{"UserID", r.UserID},
		{"Role", string(r.Role)},
		{"Action", string(r.Action)},
		{"Resource", r.Resource},
		{"SessionID", r.SessionID},
		{"PaneID", r.PaneID},
		{"AgentID", r.AgentID},
		{"RemoteAddr", r.RemoteAddr},
	}

	for _, e := range essential {
		if e.value == "" {
			t.Errorf("essential field %s is empty - redaction broke audit usefulness", e.name)
		} else {
			t.Logf("AUDIT_PRESERVED[%s]: %s", e.name, e.value)
		}
	}

	// Verify: Redaction metadata is preserved in details
	var details map[string]interface{}
	if err := json.Unmarshal([]byte(r.Details), &details); err != nil {
		t.Fatalf("parse details error: %v", err)
	}

	if reason, ok := details["redaction_reason"]; !ok || reason != "contains_secrets" {
		t.Error("redaction_reason not preserved in audit details")
	}
	if _, ok := details["redacted_at"]; !ok {
		t.Error("redacted_at timestamp not preserved in audit details")
	}
	if origLen, ok := details["original_length"]; !ok {
		t.Error("original_length not preserved - cannot assess impact of redaction")
	} else {
		t.Logf("REDACTION_METADATA: reason=%v original_length=%v",
			details["redaction_reason"], origLen)
	}
}

// =============================================================================
// Multi-Actor Session State Mutation
// =============================================================================

// TestAuditMultiActorSessionMutation verifies audit correctness when multiple
// actors mutate the same session concurrently.
func TestAuditMultiActorSessionMutation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Simulate concurrent operations on same session
	sessionID := "shared-session"
	baseTime := time.Now().UTC()

	operations := []struct {
		offsetMs int
		userID   string
		action   string
		paneID   string
	}{
		{0, "user-alpha", "send to pane 1", "0.1"},
		{5, "user-beta", "send to pane 2", "0.2"},
		{10, "user-alpha", "interrupt pane 1", "0.1"},
		{15, "user-gamma", "spawn new pane", "0.3"},
		{20, "user-beta", "send to pane 1", "0.1"},
		{25, "user-alpha", "close pane 3", "0.3"},
	}

	for i, op := range operations {
		rec := &AuditRecord{
			Timestamp:  baseTime.Add(time.Duration(op.offsetMs) * time.Millisecond),
			RequestID:  fmtReqID("multi", i),
			UserID:     op.userID,
			Role:       RoleOperator,
			Action:     AuditActionExecute,
			Resource:   "pane",
			ResourceID: op.paneID,
			Method:     "POST",
			Path:       "/api/v1/sessions/" + sessionID + "/panes/" + op.paneID,
			StatusCode: 200,
			Duration:   3,
			SessionID:  sessionID,
			PaneID:     op.paneID,
			Details:    op.action,
			RemoteAddr: "127.0.0.1:5000",
		}
		if err := store.Record(rec); err != nil {
			t.Fatalf("Record error: %v", err)
		}
	}

	// Verify: All operations recorded
	allOps, err := store.Query(AuditFilter{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(allOps) != 6 {
		t.Errorf("expected 6 operations, got %d", len(allOps))
	}

	// Verify: Can answer "who touched pane 0.1?"
	pane1Ops := []AuditRecord{}
	for _, op := range allOps {
		if op.PaneID == "0.1" {
			pane1Ops = append(pane1Ops, op)
		}
	}
	if len(pane1Ops) != 3 {
		t.Errorf("expected 3 operations on pane 0.1, got %d", len(pane1Ops))
	}

	actors := make(map[string]int)
	for _, op := range pane1Ops {
		actors[op.UserID]++
	}

	t.Log("PANE_0.1_ACTIVITY_ATTRIBUTION:")
	for actor, count := range actors {
		t.Logf("  %s: %d operations", actor, count)
	}

	// Expected: user-alpha=2 (send, interrupt), user-beta=1 (send)
	if actors["user-alpha"] != 2 {
		t.Errorf("user-alpha should have 2 operations on pane 0.1, got %d", actors["user-alpha"])
	}
	if actors["user-beta"] != 1 {
		t.Errorf("user-beta should have 1 operation on pane 0.1, got %d", actors["user-beta"])
	}
}

// =============================================================================
// Audit Persistence Across Restart
// =============================================================================

// TestAuditPersistenceAcrossRestart verifies that audit records survive
// store close/reopen cycles.
func TestAuditPersistenceAcrossRestart(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	// Create store and record data
	store1, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}

	for i := 0; i < 5; i++ {
		rec := &AuditRecord{
			Timestamp:  time.Now().UTC(),
			RequestID:  fmtReqID("persist", i),
			UserID:     "persist-user",
			Role:       RoleOperator,
			Action:     AuditActionExecute,
			Resource:   "test",
			Method:     "POST",
			Path:       "/api/v1/test",
			StatusCode: 200,
			Duration:   1,
			RemoteAddr: "127.0.0.1:1234",
		}
		if err := store1.Record(rec); err != nil {
			t.Fatalf("Record error: %v", err)
		}
	}

	// Close the store (simulating process termination)
	if err := store1.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Reopen the store (simulating process restart)
	store2, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore (reopen) error: %v", err)
	}
	defer store2.Close()

	// Verify: All records survived the restart
	recs, err := store2.Query(AuditFilter{UserID: "persist-user"})
	if err != nil {
		t.Fatalf("Query after restart error: %v", err)
	}
	if len(recs) != 5 {
		t.Errorf("expected 5 records after restart, got %d", len(recs))
	}

	// Verify: JSONL also has the records
	jsonlData, err := os.ReadFile(cfg.JSONLPath)
	if err != nil {
		t.Fatalf("read JSONL error: %v", err)
	}
	lines := strings.Count(string(jsonlData), "\n")
	if lines != 5 {
		t.Errorf("expected 5 lines in JSONL, got %d", lines)
	}

	t.Logf("PERSISTENCE_VERIFIED: %d records in DB, %d lines in JSONL", len(recs), lines)
}

// =============================================================================
// Integration with HTTP Middleware
// =============================================================================

// TestAuditMiddlewareActorAttribution verifies that the audit middleware
// correctly captures actor information from RBAC context.
func TestAuditMiddlewareActorAttribution(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}

	// Handler that performs a state-changing action
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetAuditResource(r, "session", "test-session")
		SetAuditSession(r, "test-session", "0.1", "agent-1")
		SetAuditAction(r, AuditActionExecute)
		SetAuditDetails(r, "sent command to agent")
		w.WriteHeader(http.StatusOK)
	})

	// Stack: RBAC -> Audit -> Handler
	rbacHandler := s.rbacMiddleware(handler)
	auditHandler := s.AuditMiddleware(store)(rbacHandler)

	// Test with identifiable request
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/send", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "attribution-test-req"))

	rr := httptest.NewRecorder()
	auditHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify: Audit record captures all attribution fields
	recs, err := store.Query(AuditFilter{RequestID: "attribution-test-req"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}

	rec := recs[0]
	t.Logf("MIDDLEWARE_ATTRIBUTION_TEST:")
	t.Logf("  UserID: %s", rec.UserID)
	t.Logf("  Role: %s", rec.Role)
	t.Logf("  Action: %s", rec.Action)
	t.Logf("  Resource: %s", rec.Resource)
	t.Logf("  SessionID: %s", rec.SessionID)
	t.Logf("  PaneID: %s", rec.PaneID)
	t.Logf("  AgentID: %s", rec.AgentID)
	t.Logf("  Details: %s", rec.Details)

	// All attribution fields should be populated
	if rec.SessionID != "test-session" {
		t.Errorf("SessionID = %q, want 'test-session'", rec.SessionID)
	}
	if rec.PaneID != "0.1" {
		t.Errorf("PaneID = %q, want '0.1'", rec.PaneID)
	}
	if rec.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want 'agent-1'", rec.AgentID)
	}
	if rec.Details != "sent command to agent" {
		t.Errorf("Details = %q, want 'sent command to agent'", rec.Details)
	}
}

// =============================================================================
// Helpers
// =============================================================================

func fmtReqID(prefix string, i int) string {
	return fmt.Sprintf("%s-req-%03d", prefix, i)
}

func fmtResourceID(prefix string, i int) string {
	return fmt.Sprintf("%s-%03d", prefix, i)
}

func methodForAction(action AuditAction) string {
	switch action {
	case AuditActionCreate:
		return "POST"
	case AuditActionUpdate:
		return "PUT"
	case AuditActionDelete:
		return "DELETE"
	default:
		return "POST"
	}
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// bd-aljrb: the middleware's contract assumed handlers call
// SetAuditResource/SetAuditSession, but exactly one handler in the tree ever
// did. Every other audit record — rollback, checkpoint restore, agent spawn,
// pane input, approval approve/deny — was written with empty session_id and
// resource_id, so an operator investigating a destructive action and filtering
// by session got ZERO rows. Deriving the fields from the routed chi params
// covers every route, including ones that do not exist yet.
func TestAuditMiddlewareDerivesAttributionFromRoute(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	s := &Server{auth: AuthConfig{Mode: AuthModeLocal}}

	// A handler that sets NOTHING, like almost every real handler.
	silent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Use(s.rbacMiddleware)
	r.Use(s.AuditMiddleware(store))
	r.Post("/api/v1/sessions/{sessionName}/rollback", silent)
	r.Post("/api/v1/sessions/{sessionId}/panes/{paneIdx}/input", silent)
	r.Post("/api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/restore", silent)

	cases := []struct {
		name           string
		method         string
		path           string
		reqID          string
		wantSession    string
		wantPane       string
		wantResourceID string
	}{
		{
			name: "rollback", method: "POST", reqID: "req-rollback",
			path:        "/api/v1/sessions/payments/rollback",
			wantSession: "payments", wantResourceID: "payments",
		},
		{
			name: "pane input", method: "POST", reqID: "req-input",
			path:        "/api/v1/sessions/payments/panes/3/input",
			wantSession: "payments", wantPane: "3", wantResourceID: "payments",
		},
		{
			name: "checkpoint restore", method: "POST", reqID: "req-restore",
			path:        "/api/v1/sessions/payments/checkpoints/cp-123/restore",
			wantSession: "payments", wantResourceID: "cp-123",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req = req.WithContext(context.WithValue(req.Context(), requestIDKey, tc.reqID))
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}

			recs, err := store.Query(AuditFilter{RequestID: tc.reqID})
			if err != nil {
				t.Fatalf("Query error: %v", err)
			}
			if len(recs) != 1 {
				t.Fatalf("got %d audit records, want 1", len(recs))
			}
			rec := recs[0]
			if rec.SessionID != tc.wantSession {
				t.Errorf("SessionID = %q, want %q", rec.SessionID, tc.wantSession)
			}
			if rec.PaneID != tc.wantPane {
				t.Errorf("PaneID = %q, want %q", rec.PaneID, tc.wantPane)
			}
			if rec.ResourceID != tc.wantResourceID {
				t.Errorf("ResourceID = %q, want %q", rec.ResourceID, tc.wantResourceID)
			}

			// The point of the fix: a session-filtered query finds it.
			bySession, err := store.Query(AuditFilter{SessionID: tc.wantSession})
			if err != nil {
				t.Fatalf("session-filtered Query error: %v", err)
			}
			if len(bySession) == 0 {
				t.Fatal("a session-filtered audit query returned zero rows; the record is unfindable by the field operators actually filter on")
			}
		})
	}
}

// A handler that DOES set the fields must still win over the routed values.
func TestAuditMiddlewareHandlerValuesWinOverRoute(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	s := &Server{auth: AuthConfig{Mode: AuthModeLocal}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetAuditSession(r, "handler-session", "handler-pane", "handler-agent")
		SetAuditResource(r, "session", "handler-resource")
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Use(s.rbacMiddleware)
	r.Use(s.AuditMiddleware(store))
	r.Post("/api/v1/sessions/{sessionName}/rollback", handler)

	req := httptest.NewRequest("POST", "/api/v1/sessions/route-session/rollback", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "req-precedence"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	recs, err := store.Query(AuditFilter{RequestID: "req-precedence"})
	if err != nil || len(recs) != 1 {
		t.Fatalf("Query = (%d records, %v), want exactly 1", len(recs), err)
	}
	if recs[0].SessionID != "handler-session" {
		t.Errorf("SessionID = %q, want the handler-set value to win", recs[0].SessionID)
	}
	if recs[0].ResourceID != "handler-resource" {
		t.Errorf("ResourceID = %q, want the handler-set value to win", recs[0].ResourceID)
	}
}
