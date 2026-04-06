package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/state"
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
