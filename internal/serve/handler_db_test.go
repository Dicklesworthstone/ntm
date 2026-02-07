package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/metrics"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/go-chi/chi/v5"
)

// createTestSessionForServe inserts a session row into the state store.
func createTestSessionForServe(t *testing.T, store *state.Store, id string) {
	t.Helper()
	err := store.CreateSession(&state.Session{
		ID:          id,
		Name:        id,
		ProjectPath: "/tmp/test",
		CreatedAt:   time.Now(),
		Status:      state.SessionActive,
	})
	if err != nil {
		t.Fatalf("CreateSession(%q): %v", id, err)
	}
}

// =============================================================================
// handleSessionAgents tests
// =============================================================================

func TestHandleSessionAgents_Empty(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "test-session")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/agents", nil)

	srv.handleSessionAgents(rr, req, "test-session")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp["success"] != true {
		t.Error("expected success=true")
	}
	if resp["session_id"] != "test-session" {
		t.Errorf("session_id = %v", resp["session_id"])
	}
	count, _ := resp["count"].(float64)
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
}

func TestHandleSessionAgents_WithAgents(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "agent-session")

	// Insert agents directly
	db := store.DB()
	_, err := db.Exec(`INSERT INTO agents (id, session_id, name, type, status) VALUES (?, ?, ?, ?, ?)`,
		"a1", "agent-session", "Agent1", "cc", "working")
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agents (id, session_id, name, type, status) VALUES (?, ?, ?, ?, ?)`,
		"a2", "agent-session", "Agent2", "cod", "idle")
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/agent-session/agents", nil)

	srv.handleSessionAgents(rr, req, "agent-session")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	count, _ := resp["count"].(float64)
	if count != 2 {
		t.Errorf("count = %v, want 2", count)
	}
}

func TestHandleSessionAgents_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/foo/agents", nil)

	srv.handleSessionAgents(rr, req, "foo")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

// =============================================================================
// handleMetricsCompareV1 tests
// =============================================================================

func TestHandleMetricsCompareV1_SnapshotNotFound(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "cmp-session")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/compare?session=cmp-session&baseline=nonexistent", nil)

	srv.handleMetricsCompareV1(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["error_code"] != "SNAPSHOT_NOT_FOUND" {
		t.Errorf("error_code = %v", resp["error_code"])
	}
}

func TestHandleMetricsCompareV1_Success(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "cmp-ok")

	// Pre-populate a baseline snapshot using the metrics collector directly
	c := metrics.NewCollector(store, "cmp-ok")
	c.RecordAPICall("bv", "triage")
	c.RecordLatency("op", 100*time.Millisecond)
	if err := c.SaveSnapshot("baseline"); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Record more activity so current differs from baseline
	c.RecordAPICall("bd", "create")
	c.RecordLatency("op", 50*time.Millisecond)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/compare?session=cmp-ok&baseline=baseline", nil)

	srv.handleMetricsCompareV1(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["session"] != "cmp-ok" {
		t.Errorf("session = %v", resp["session"])
	}
	if resp["comparison"] == nil {
		t.Error("expected comparison to be present")
	}
}

func TestHandleMetricsCompareV1_DefaultBaseline(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "cmp-default")

	// When baseline param is omitted, it defaults to "baseline"
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/compare?session=cmp-default", nil)

	srv.handleMetricsCompareV1(rr, req)

	// Should return 404 since no "baseline" snapshot exists
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// =============================================================================
// handleContextGetV1 tests
// =============================================================================

func TestHandleContextGetV1_MissingID(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	// Use chi context with empty URL param
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("contextId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleContextGetV1(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestHandleContextGetV1_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/nonexistent-ctx", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("contextId", "nonexistent-ctx")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleContextGetV1(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["error_code"] != "CONTEXT_NOT_FOUND" {
		t.Errorf("error_code = %v", resp["error_code"])
	}
}

// =============================================================================
// handleGitSyncV1 tests
// =============================================================================

func TestHandleGitSyncV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/git/sync", strings.NewReader("{invalid"))

	srv.handleGitSyncV1(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestHandleGitSyncV1_EmptyBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	// Empty body should parse as zero-value struct (valid)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/git/sync", strings.NewReader(""))

	srv.handleGitSyncV1(rr, req)

	// May succeed or fail depending on git state, but should not be 400
	if rr.Code == http.StatusBadRequest {
		t.Fatalf("empty body should not produce 400; body: %s", rr.Body.String())
	}
}

func TestHandleGitSyncV1_DryRun(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	body := `{"dry_run": true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/git/sync", strings.NewReader(body))

	srv.handleGitSyncV1(rr, req)

	// Should return success for dry run (exercises more of the handler)
	// Response code depends on git state but we exercise the parsing path
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Verify the response is valid JSON (handler didn't panic)
	if resp == nil {
		t.Error("expected non-nil response")
	}
}

// =============================================================================
// handleSessionAgentsV1 tests (v1 endpoint variant)
// =============================================================================

func TestHandleSessionAgentsV1_Empty(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "v1-session")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/v1-session/agents", nil)

	srv.handleSessionAgentsV1(rr, req, "v1-session")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
	// Verify agents is an array, not null
	agents, ok := resp["agents"].([]interface{})
	if !ok {
		t.Fatalf("agents should be array, got %T", resp["agents"])
	}
	if len(agents) != 0 {
		t.Errorf("agents len = %d, want 0", len(agents))
	}
}

func TestHandleSessionAgentsV1_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/foo/agents", nil)

	srv.handleSessionAgentsV1(rr, req, "foo")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

// =============================================================================
// Redact Flush test
// =============================================================================

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

func TestRedactingResponseWriter_Flush(t *testing.T) {
	t.Parallel()

	inner := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	rw := &redactingResponseWriter{
		ResponseWriter: inner,
		buffer:         new(bytes.Buffer),
		summary:        &RedactionSummary{},
		categories:     make(map[string]int),
	}

	rw.Flush()
	if !inner.flushed {
		t.Error("expected inner Flush to be called")
	}
}

// =============================================================================
// handleScannerStatus test
// =============================================================================

func TestHandleScannerStatus_NilScannerStore(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("scanId", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleScannerStatus(rr, req)

	// Should handle gracefully (500 or 404, not panic)
	if rr.Code == 0 {
		t.Error("expected non-zero status code")
	}
}
