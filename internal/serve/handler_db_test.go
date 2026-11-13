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

// A stored session tmux is not running has no agents to report.
func TestHandleSessionAgents_Empty(t *testing.T) {
	srv, store := setupTestServer(t)
	installFakeListPanesTmux(t, fakeTmuxNoServer)
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

// Without a state store nothing records the session, so a session tmux is not
// running is not found (the store is no longer required to list agents).
func TestHandleSessionAgents_NilStore(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.stateStore = nil
	installFakeListPanesTmux(t, fakeTmuxNoServer)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/foo/agents", nil)

	srv.handleSessionAgents(rr, req, "foo")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleSessionAgents_InvalidSessionName(t *testing.T) {
	srv, _ := setupTestServer(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/bad:name/agents", nil)

	srv.handleSessionAgents(rr, req, "bad:name")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// =============================================================================
// handleListAgentsV1 store-backed tests
// =============================================================================

// A stored session tmux is not running lists an empty agents array, never null.
func TestHandleListAgentsV1_StoredSessionNotRunning(t *testing.T) {
	srv, store := setupTestServer(t)
	installFakeListPanesTmux(t, fakeTmuxNoServer)
	createTestSessionForServe(t, store, "v1-session")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/v1-session/agents", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "v1-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListAgentsV1(rr, req)

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

func TestRedactingResponseWriter_FlushSSE(t *testing.T) {

	inner := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	rw := &redactingResponseWriter{
		ResponseWriter: inner,
		buffer:         new(bytes.Buffer),
		summary:        &RedactionSummary{},
		categories:     make(map[string]int),
	}

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Flush()
	if !inner.flushed {
		t.Error("expected inner Flush to be called")
	}
}

// =============================================================================
// handleScannerStatus test
// =============================================================================

func TestHandleScannerStatus_NilScannerStore(t *testing.T) {
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

func TestHandleMetricsExportV1_FormatsAndValidation(t *testing.T) {
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "rest-export-sess")

	doGet := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req = req.WithContext(withRoleContext(req.Context(), &RoleContext{
			Role:   RoleAdmin,
			UserID: "tester",
		}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	t.Run("json default and explicit", func(t *testing.T) {
		rec := doGet("/api/v1/metrics/export?session=rest-export-sess&format=json")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal json export: %v", err)
		}
		if resp["success"] != true || resp["format"] != "json" || resp["session_id"] != "rest-export-sess" {
			t.Fatalf("unexpected json payload: %v", resp)
		}
		if _, ok := resp["target_comparison"].([]interface{}); !ok {
			t.Fatalf("target_comparison = %T, want non-null JSON array", resp["target_comparison"])
		}
	})

	t.Run("csv format", func(t *testing.T) {
		rec := doGet("/api/v1/metrics/export?session=rest-export-sess&format=csv")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want text/csv; charset=utf-8", ct)
		}
		const wantHeader = "operation,count,min_ms,max_ms,avg_ms,p50_ms,p95_ms,p99_ms\n"
		if rec.Body.String() != wantHeader {
			t.Fatalf("csv body = %q, want %q", rec.Body.String(), wantHeader)
		}
	})

	t.Run("markdown format", func(t *testing.T) {
		rec := doGet("/api/v1/metrics/export?session=rest-export-sess&format=md")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want text/markdown; charset=utf-8", ct)
		}
		if body := rec.Body.String(); !bytes.Contains([]byte(body), []byte("# NTM Metrics Report (`rest-export-sess`)")) {
			t.Fatalf("markdown body missing header:\n%s", body)
		}
	})

	t.Run("invalid format returns 400", func(t *testing.T) {
		rec := doGet("/api/v1/metrics/export?session=rest-export-sess&format=xml")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
		}
	})
}
