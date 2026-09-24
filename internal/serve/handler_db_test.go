package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestWebSessionsAndAgentsIncludeLiveTmuxSession(t *testing.T) {
	srv, store := setupTestServer(t)
	bin := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
case "$1" in
  list-sessions)
    printf '%s\n' 'live-session_NTM_SEP_1_NTM_SEP_0_NTM_SEP_Wed Sep 23 15:30:42 2026'
    ;;
  list-panes)
    printf '%s\n' '%2_NTM_SEP_2_NTM_SEP_live-session__cod_1_NTM_SEP_codex_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_123_NTM_SEP_0_NTM_SEP_cod_NTM_SEP__NTM_SEP__NTM_SEP_0'
    ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := agentmail.NewSessionAgentRegistry("live-session", "/tmp/test")
	registry.AddAgent("live-session__cod_1", "%2", "BlueStone")
	registry.SetPanePID("%2", 123)
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })

	for _, endpoint := range []struct {
		path string
		key  string
	}{
		{"/api/v1/sessions", "sessions"},
		{"/api/v1/sessions/live-session", "session"},
		{"/api/v1/sessions/live-session/agents", "agents"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, endpoint.path, nil)
		srv.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", endpoint.path, rec.Code, rec.Body.String())
		}
		var response map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		switch value := response[endpoint.key].(type) {
		case []interface{}:
			if len(value) != 1 {
				t.Fatalf("%s: %s has %d entries, want 1", endpoint.path, endpoint.key, len(value))
			}
			if endpoint.key == "agents" {
				agent, ok := value[0].(map[string]interface{})
				if !ok || agent["type"] != "cod" || agent["session_id"] != "live-session" || agent["tmux_pane_id"] != "%2" || agent["agent_name"] != "BlueStone" {
					t.Fatalf("%s: unexpected agent payload: %v", endpoint.path, value[0])
				}
			}
		case map[string]interface{}:
			if value["name"] != "live-session" {
				t.Fatalf("%s: session name %v", endpoint.path, value["name"])
			}
		default:
			t.Fatalf("%s: unexpected %s type %T", endpoint.path, endpoint.key, value)
		}
	}

	createTestSessionForServe(t, store, "live-session")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions with stored row: status %d, body %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Sessions []state.Session `json:"sessions"`
		Count    int             `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Sessions) != 1 || response.Sessions[0].ProjectPath != "/tmp/test" {
		t.Fatalf("stored and live session should be deduplicated: %+v", response)
	}

	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions when tmux fails: status %d, body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Sessions) != 1 {
		t.Fatalf("stored session should remain available when tmux fails: %+v", response)
	}
}

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
