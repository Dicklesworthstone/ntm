package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditStore_Record(t *testing.T) {
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

	// Record an audit entry
	rec := &AuditRecord{
		RequestID:  "req-123",
		UserID:     "user-456",
		Role:       RoleOperator,
		Action:     AuditActionCreate,
		Resource:   "session",
		ResourceID: "sess-789",
		Method:     "POST",
		Path:       "/api/v1/sessions",
		StatusCode: 201,
		Duration:   42,
		SessionID:  "sess-789",
		RemoteAddr: "127.0.0.1:54321",
		UserAgent:  "TestAgent/1.0",
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	// Verify JSONL file was written
	data, err := os.ReadFile(cfg.JSONLPath)
	if err != nil {
		t.Fatalf("read jsonl error: %v", err)
	}
	if len(data) == 0 {
		t.Error("jsonl file is empty")
	}

	// Parse the JSONL record
	var parsed AuditRecord
	if err := json.Unmarshal(data[:len(data)-1], &parsed); err != nil {
		t.Fatalf("parse jsonl error: %v", err)
	}
	if parsed.RequestID != "req-123" {
		t.Errorf("parsed.RequestID = %q, want %q", parsed.RequestID, "req-123")
	}
	if parsed.Action != AuditActionCreate {
		t.Errorf("parsed.Action = %q, want %q", parsed.Action, AuditActionCreate)
	}
}

func TestAuditStore_Query(t *testing.T) {
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

	// Record multiple entries
	records := []AuditRecord{
		{
			RequestID:  "req-001",
			UserID:     "alice",
			Role:       RoleAdmin,
			Action:     AuditActionCreate,
			Resource:   "session",
			Method:     "POST",
			Path:       "/api/v1/sessions",
			StatusCode: 201,
			Duration:   10,
			SessionID:  "sess-1",
			RemoteAddr: "127.0.0.1:1111",
		},
		{
			RequestID:  "req-002",
			UserID:     "bob",
			Role:       RoleOperator,
			Action:     AuditActionUpdate,
			Resource:   "agent",
			Method:     "PUT",
			Path:       "/api/v1/agents/a1",
			StatusCode: 200,
			Duration:   20,
			SessionID:  "sess-1",
			AgentID:    "a1",
			RemoteAddr: "127.0.0.1:2222",
		},
		{
			RequestID:  "req-003",
			UserID:     "alice",
			Role:       RoleAdmin,
			Action:     AuditActionDelete,
			Resource:   "session",
			Method:     "DELETE",
			Path:       "/api/v1/sessions/sess-1",
			StatusCode: 204,
			Duration:   15,
			SessionID:  "sess-1",
			RemoteAddr: "127.0.0.1:3333",
		},
	}

	for _, rec := range records {
		r := rec // copy
		if err := store.Record(&r); err != nil {
			t.Fatalf("Record error: %v", err)
		}
	}

	// Query all records
	all, err := store.Query(AuditFilter{})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len(all) = %d, want 3", len(all))
	}

	// Query by user
	aliceRecs, err := store.Query(AuditFilter{UserID: "alice"})
	if err != nil {
		t.Fatalf("Query by user error: %v", err)
	}
	if len(aliceRecs) != 2 {
		t.Errorf("len(aliceRecs) = %d, want 2", len(aliceRecs))
	}

	// Query by action
	createRecs, err := store.Query(AuditFilter{Action: AuditActionCreate})
	if err != nil {
		t.Fatalf("Query by action error: %v", err)
	}
	if len(createRecs) != 1 {
		t.Errorf("len(createRecs) = %d, want 1", len(createRecs))
	}

	// Query by resource
	sessionRecs, err := store.Query(AuditFilter{Resource: "session"})
	if err != nil {
		t.Fatalf("Query by resource error: %v", err)
	}
	if len(sessionRecs) != 2 {
		t.Errorf("len(sessionRecs) = %d, want 2", len(sessionRecs))
	}

	// Query by session
	sess1Recs, err := store.Query(AuditFilter{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Query by session error: %v", err)
	}
	if len(sess1Recs) != 3 {
		t.Errorf("len(sess1Recs) = %d, want 3", len(sess1Recs))
	}

	// Query with limit
	limitRecs, err := store.Query(AuditFilter{Limit: 2})
	if err != nil {
		t.Fatalf("Query with limit error: %v", err)
	}
	if len(limitRecs) != 2 {
		t.Errorf("len(limitRecs) = %d, want 2", len(limitRecs))
	}
}

func TestAuditStore_Retention(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       1 * time.Millisecond, // Very short retention
		CleanupInterval: 24 * time.Hour,       // Don't run auto-cleanup
	}

	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// Record an entry
	rec := &AuditRecord{
		Timestamp:  time.Now().Add(-time.Hour), // Old record
		RequestID:  "old-req",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		Duration:   0,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	// Verify record exists
	before, _ := store.Query(AuditFilter{})
	if len(before) != 1 {
		t.Fatalf("expected 1 record before cleanup, got %d", len(before))
	}

	// Run cleanup manually
	store.cleanup()

	// Verify record was removed
	after, _ := store.Query(AuditFilter{})
	if len(after) != 0 {
		t.Errorf("expected 0 records after cleanup, got %d", len(after))
	}
}

func TestAuditMiddleware(t *testing.T) {
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

	// Create a test server with audit middleware
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}

	// Handler that sets audit context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetAuditResource(r, "test-resource", "res-123")
		SetAuditSession(r, "sess-test", "pane-1", "agent-1")
		SetAuditDetails(r, "test details")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"success":true}`))
	})

	// Wrap with RBAC middleware to set role context
	rbacHandler := s.rbacMiddleware(handler)

	// Wrap with audit middleware
	auditHandler := s.AuditMiddleware(store)(rbacHandler)

	// Test POST request (mutating)
	req := httptest.NewRequest("POST", "/api/v1/test", nil)
	req.Header.Set("X-Request-Id", "test-req-id")
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "test-req-id"))

	rr := httptest.NewRecorder()
	auditHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	// Give async writes time to complete
	time.Sleep(10 * time.Millisecond)

	// Query for the audit record
	records, err := store.Query(AuditFilter{RequestID: "test-req-id"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Resource != "test-resource" {
		t.Errorf("Resource = %q, want %q", rec.Resource, "test-resource")
	}
	if rec.ResourceID != "res-123" {
		t.Errorf("ResourceID = %q, want %q", rec.ResourceID, "res-123")
	}
	if rec.SessionID != "sess-test" {
		t.Errorf("SessionID = %q, want %q", rec.SessionID, "sess-test")
	}
	if rec.PaneID != "pane-1" {
		t.Errorf("PaneID = %q, want %q", rec.PaneID, "pane-1")
	}
	if rec.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", rec.AgentID, "agent-1")
	}
	if rec.Details != "test details" {
		t.Errorf("Details = %q, want %q", rec.Details, "test details")
	}
	if rec.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want %d", rec.StatusCode, http.StatusCreated)
	}
	if rec.Action != AuditActionCreate {
		t.Errorf("Action = %q, want %q", rec.Action, AuditActionCreate)
	}
}

func TestAuditMiddleware_SkipsGET(t *testing.T) {
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

	s := &Server{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	auditHandler := s.AuditMiddleware(store)(handler)

	// Test GET request (non-mutating) - should NOT be audited
	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	rr := httptest.NewRecorder()
	auditHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify no audit record was created
	records, _ := store.Query(AuditFilter{})
	if len(records) != 0 {
		t.Errorf("expected 0 records for GET, got %d", len(records))
	}
}

func TestInferAction(t *testing.T) {
	tests := []struct {
		method string
		want   AuditAction
	}{
		{"POST", AuditActionCreate},
		{"PUT", AuditActionUpdate},
		{"PATCH", AuditActionUpdate},
		{"DELETE", AuditActionDelete},
		{"GET", AuditActionExecute},
		{"OPTIONS", AuditActionExecute},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := inferAction(tt.method)
			if got != tt.want {
				t.Errorf("inferAction(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestInferResource(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/sessions", "sessions"},
		{"/api/v1/sessions/123", "sessions"},
		{"/api/v1/agents/a1/status", "agents"},
		{"/api/v1/jobs", "jobs"},
		{"/api/sessions", "sessions"},
		{"/api/health", "health"},
		{"/other", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := inferResource(tt.path)
			if got != tt.want {
				t.Errorf("inferResource(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsMutatingMethod(t *testing.T) {
	tests := []struct {
		method string
		want   bool
	}{
		{"GET", false},
		{"HEAD", false},
		{"OPTIONS", false},
		{"POST", true},
		{"PUT", true},
		{"PATCH", true},
		{"DELETE", true},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := isMutatingMethod(tt.method)
			if got != tt.want {
				t.Errorf("isMutatingMethod(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"/api/v1/sessions", []string{"api", "v1", "sessions"}},
		{"/api/v1/sessions/123/agents", []string{"api", "v1", "sessions", "123", "agents"}},
		{"/", nil},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := splitPath(tt.path)
			if len(got) != len(tt.want) {
				t.Errorf("splitPath(%q) = %v (len %d), want %v (len %d)", tt.path, got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRecordApprovalAction(t *testing.T) {
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

	ctx := context.WithValue(context.Background(), requestIDKey, "approval-req")
	err = store.RecordApprovalAction(ctx, AuditActionApprove, "apr-123", "admin-user", RoleAdmin, "approved dangerous op")
	if err != nil {
		t.Fatalf("RecordApprovalAction error: %v", err)
	}

	// Query for the approval audit record
	records, err := store.Query(AuditFilter{ApprovalID: "apr-123"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Action != AuditActionApprove {
		t.Errorf("Action = %q, want %q", rec.Action, AuditActionApprove)
	}
	if rec.Resource != "approval" {
		t.Errorf("Resource = %q, want %q", rec.Resource, "approval")
	}
	if rec.UserID != "admin-user" {
		t.Errorf("UserID = %q, want %q", rec.UserID, "admin-user")
	}
	if rec.Details != "approved dangerous op" {
		t.Errorf("Details = %q, want %q", rec.Details, "approved dangerous op")
	}
}

func TestRecordWebSocketAction(t *testing.T) {
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

	err = store.RecordWebSocketAction("client-456", AuditActionSubscribe, "ws-user", RoleOperator, []string{"sessions:*", "panes:sess1:*"}, "192.168.1.100:9999")
	if err != nil {
		t.Fatalf("RecordWebSocketAction error: %v", err)
	}

	// Query for the websocket audit record
	records, err := store.Query(AuditFilter{Resource: "websocket"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	rec := records[0]
	if rec.Action != AuditActionSubscribe {
		t.Errorf("Action = %q, want %q", rec.Action, AuditActionSubscribe)
	}
	if rec.ResourceID != "client-456" {
		t.Errorf("ResourceID = %q, want %q", rec.ResourceID, "client-456")
	}
	if rec.UserID != "ws-user" {
		t.Errorf("UserID = %q, want %q", rec.UserID, "ws-user")
	}

	// Verify topics are in details
	var topics []string
	if err := json.Unmarshal([]byte(rec.Details), &topics); err != nil {
		t.Errorf("failed to parse topics from details: %v", err)
	} else if len(topics) != 2 {
		t.Errorf("expected 2 topics, got %d", len(topics))
	}
}

func TestDefaultAuditStoreConfig(t *testing.T) {
	dataDir := "/tmp/test-data"
	cfg := DefaultAuditStoreConfig(dataDir)

	expectedDBPath := filepath.Join(dataDir, "audit.db")
	if cfg.DBPath != expectedDBPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, expectedDBPath)
	}

	expectedJSONLPath := filepath.Join(dataDir, "audit.jsonl")
	if cfg.JSONLPath != expectedJSONLPath {
		t.Errorf("JSONLPath = %q, want %q", cfg.JSONLPath, expectedJSONLPath)
	}

	expectedRetention := 90 * 24 * time.Hour
	if cfg.Retention != expectedRetention {
		t.Errorf("Retention = %v, want %v", cfg.Retention, expectedRetention)
	}

	expectedCleanupInterval := 24 * time.Hour
	if cfg.CleanupInterval != expectedCleanupInterval {
		t.Errorf("CleanupInterval = %v, want %v", cfg.CleanupInterval, expectedCleanupInterval)
	}
}

func TestSetAuditApproval(t *testing.T) {
	// Create a request with audit context
	req := httptest.NewRequest("POST", "/api/v1/test", nil)

	// Set audit context on the request
	ac := &AuditContext{}
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyAudit, ac))

	// Set approval ID
	SetAuditApproval(req, "approval-xyz")

	if ac.ApprovalID != "approval-xyz" {
		t.Errorf("ApprovalID = %q, want %q", ac.ApprovalID, "approval-xyz")
	}
}

func TestSetAuditApproval_NilContext(t *testing.T) {
	// Create a request without audit context
	req := httptest.NewRequest("POST", "/api/v1/test", nil)

	// Should not panic when context is nil
	SetAuditApproval(req, "approval-xyz")
	// No assertion needed - just verify no panic
}

func TestSetAuditAction(t *testing.T) {
	// Create a request with audit context
	req := httptest.NewRequest("POST", "/api/v1/test", nil)

	// Set audit context on the request
	ac := &AuditContext{}
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyAudit, ac))

	// Set action
	SetAuditAction(req, AuditActionApprove)

	if ac.Action != AuditActionApprove {
		t.Errorf("Action = %q, want %q", ac.Action, AuditActionApprove)
	}
}

func TestSetAuditAction_NilContext(t *testing.T) {
	// Create a request without audit context
	req := httptest.NewRequest("GET", "/api/v1/test", nil)

	// Should not panic when context is nil
	SetAuditAction(req, AuditActionExecute)
	// No assertion needed - just verify no panic
}

func TestNewAuditStore_DefaultRetentionValues(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       0,  // Should default to 90 days
		CleanupInterval: -1, // Should default to 24h
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.retention != 90*24*time.Hour {
		t.Errorf("retention = %v, want 90 days", store.retention)
	}
}

func TestNewAuditStore_DBOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		DBPath:          filepath.Join(tmpDir, "audit.db"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
		// No JSONLPath
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.db == nil {
		t.Error("db should not be nil")
	}
	if store.jsonlFile != nil {
		t.Error("jsonlFile should be nil when no JSONLPath")
	}

	// Recording should still work (just DB, no JSONL)
	rec := &AuditRecord{
		RequestID:  "req-db-only",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}
	records, _ := store.Query(AuditFilter{RequestID: "req-db-only"})
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

func TestNewAuditStore_JSONLOnly(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
		// No DBPath
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	if store.db != nil {
		t.Error("db should be nil when no DBPath")
	}
	if store.jsonlFile == nil {
		t.Error("jsonlFile should not be nil")
	}

	// Recording should still work (just JSONL, no DB)
	rec := &AuditRecord{
		RequestID:  "req-jsonl-only",
		UserID:     "user",
		Role:       RoleViewer,
		Action:     AuditActionExecute,
		Resource:   "test",
		Method:     "GET",
		Path:       "/test",
		StatusCode: 200,
		RemoteAddr: "127.0.0.1",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record error: %v", err)
	}

	data, _ := os.ReadFile(cfg.JSONLPath)
	if len(data) == 0 {
		t.Error("JSONL file should have content")
	}
}

func TestAuditStore_CleanupNilDB(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := AuditStoreConfig{
		JSONLPath:       filepath.Join(tmpDir, "audit.jsonl"),
		Retention:       time.Millisecond,
		CleanupInterval: time.Hour,
	}
	store, err := NewAuditStore(cfg)
	if err != nil {
		t.Fatalf("NewAuditStore error: %v", err)
	}
	defer store.Close()

	// cleanup with nil db should not panic
	store.cleanup()
}

func TestAuditStore_Close_DBOnly(t *testing.T) {
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

	// Close with only DB (no JSONL file)
	if err := store.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestAuditStore_Close_Idempotent(t *testing.T) {
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

	if err := store.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close error: %v", err)
	}
}

func TestAuditContextFromRequest_NilContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)

	// Should return nil when no audit context is set
	ac := AuditContextFromRequest(req)
	if ac != nil {
		t.Error("Expected nil audit context from request without context")
	}
}

func TestAuditContextFromRequest_WithContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	expected := &AuditContext{
		Resource:   "test",
		ResourceID: "123",
	}
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyAudit, expected))

	ac := AuditContextFromRequest(req)
	if ac != expected {
		t.Error("Expected audit context from request")
	}
	if ac.Resource != "test" {
		t.Errorf("Resource = %q, want %q", ac.Resource, "test")
	}
}
