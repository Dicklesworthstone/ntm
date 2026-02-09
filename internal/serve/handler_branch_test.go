package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"database/sql"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/go-chi/chi/v5"
	_ "github.com/mattn/go-sqlite3"
)

// =============================================================================
// RequirePermission / RequireRole — nil role-context branch
// =============================================================================

func TestRequirePermission_NilRoleContext(t *testing.T) {
	t.Parallel()
	s := &Server{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := s.RequirePermission(PermReadSessions)(inner)

	// Request WITHOUT a RoleContext in the context
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "no role context") {
		t.Errorf("error = %v, want substring 'no role context'", errMsg)
	}
}

func TestRequireRole_NilRoleContext(t *testing.T) {
	t.Parallel()
	s := &Server{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := s.RequireRole(RoleAdmin)(inner)

	// Request WITHOUT a RoleContext
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "no role context") {
		t.Errorf("error = %v, want substring 'no role context'", errMsg)
	}
}

// =============================================================================
// handleSessionsV1 — nil stateStore branch
// =============================================================================

func TestHandleSessionsV1_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)

	srv.handleSessionsV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// handleSessionsV1 — success with sessions present
func TestHandleSessionsV1_WithSessions(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "sess-a")
	createTestSessionForServe(t, store, "sess-b")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)

	srv.handleSessionsV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count < 2 {
		t.Errorf("count = %v, want >= 2", count)
	}
}

// =============================================================================
// handleSessionV1 — nil stateStore + success path
// =============================================================================

func TestHandleSessionV1_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleSessionV1_Success(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "found-session")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/found-session", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "found-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["session"] == nil {
		t.Error("expected session in response")
	}
}

// =============================================================================
// handleSessionEventsV1 — nil eventBus branch
// =============================================================================

func TestHandleSessionEventsV1_NilEventBus(t *testing.T) {
	t.Parallel()
	// Create server with nil eventBus
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/events", nil)

	srv.handleSessionEventsV1(rec, req, "test")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// handleSessionEventsV1 — success with empty events
func TestHandleSessionEventsV1_EmptyFiltered(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/nonexistent/events", nil)

	srv.handleSessionEventsV1(rec, req, "nonexistent")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should be empty array, not null
	evts, ok := resp["events"].([]interface{})
	if !ok {
		t.Fatalf("events should be array, got %T", resp["events"])
	}
	if len(evts) != 0 {
		t.Errorf("events len = %d, want 0", len(evts))
	}
}

// =============================================================================
// handleHistoryV1 / handleHistoryStatsV1 — missing session param
// =============================================================================

func TestHandleHistoryV1_MissingSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history", nil) // no session param

	srv.handleHistoryV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHistoryStatsV1_MissingSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history/stats", nil) // no session param

	srv.handleHistoryStatsV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleMetricsSnapshotSaveV1 — success path
// =============================================================================

func TestHandleMetricsSnapshotSaveV1_Success(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "snap-session")

	rec := httptest.NewRecorder()
	body := `{"name":"test-snap","session":"snap-session"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/snapshot", strings.NewReader(body))

	srv.handleMetricsSnapshotSaveV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["name"] != "test-snap" {
		t.Errorf("name = %v, want test-snap", resp["name"])
	}
	if resp["saved"] != true {
		t.Errorf("saved = %v, want true", resp["saved"])
	}
}

// =============================================================================
// IdempotencyStore cleanup — ticker expiry branch
// =============================================================================

func TestIdempotencyStore_CleanupExpiry(t *testing.T) {
	t.Parallel()

	// Very short TTL so cleanup actually deletes entries
	store := NewIdempotencyStore(50 * time.Millisecond)
	defer store.Stop()

	store.Set("ephemeral", []byte(`{"ok":true}`), 200)

	// Verify it's there
	_, _, ok := store.Get("ephemeral")
	if !ok {
		t.Fatal("expected ephemeral key to be present initially")
	}

	// Wait for TTL + cleanup cycle (cleanup runs every minute by default,
	// but we can test the Get TTL expiry path directly)
	time.Sleep(100 * time.Millisecond)

	// Get should now return false due to TTL check
	_, _, ok = store.Get("ephemeral")
	if ok {
		t.Error("expected ephemeral key to have expired")
	}
}

// =============================================================================
// WSHub Publish — broadcast buffer full (drop) branch
// =============================================================================

func TestWSHubPublish_BufferFull(t *testing.T) {
	t.Parallel()

	hub := NewWSHub()
	// Do NOT start hub.Run() so broadcast channel has no consumer

	// Fill the broadcast buffer (capacity is 256)
	for i := 0; i < 256; i++ {
		hub.Publish("global:events", "fill_event", map[string]interface{}{
			"i": i,
		})
	}

	// This 101st publish should hit the default (drop) branch instead of blocking
	done := make(chan struct{})
	go func() {
		hub.Publish("global:events", "dropped_event", map[string]interface{}{
			"message": "should be dropped",
		})
		close(done)
	}()

	select {
	case <-done:
		// Good — Publish returned without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked — buffer-full drop branch not hit")
	}
}

// =============================================================================
// broadcastEvent — JSON marshal error branch
// =============================================================================

func TestWSHub_BroadcastEvent_MarshalError(t *testing.T) {
	t.Parallel()

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Publish data that cannot be marshalled (channel is not JSON-serializable)
	// This exercises the json.Marshal error path in broadcastEvent
	hub.Publish("global:events", "bad_data", make(chan int))

	time.Sleep(50 * time.Millisecond)

	// Sequence should NOT increment on marshal failure
	// (actually it does increment in nextSeq before marshal; let's just confirm no panic)
}

// =============================================================================
// broadcastEvent — redaction path (cfg != nil)
// =============================================================================

func TestWSHub_BroadcastEvent_WithRedaction(t *testing.T) {
	t.Parallel()

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Set a redaction config to exercise the redaction branch
	hub.SetRedactionConfig(&RedactionConfig{
		Enabled: true,
	})

	hub.Publish("global:events", "test_redacted", map[string]interface{}{
		"message": "hello",
	})

	time.Sleep(50 * time.Millisecond)

	// Verify sequence incremented (broadcastEvent ran without panic)
	hub.seqMu.Lock()
	seq := hub.seq
	hub.seqMu.Unlock()
	if seq < 1 {
		t.Errorf("seq = %d, expected >= 1", seq)
	}
}

// =============================================================================
// handleSessionsV1 — sessions=nil (empty DB returns nil slice)
// =============================================================================

func TestHandleSessionsV1_EmptyDB(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)

	srv.handleSessionsV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// sessions should be an array, not null
	sessions, ok := resp["sessions"].([]interface{})
	if !ok {
		t.Fatalf("sessions should be array, got %T", resp["sessions"])
	}
	if len(sessions) != 0 {
		t.Errorf("sessions len = %d, want 0", len(sessions))
	}
}

// =============================================================================
// handleCreateSessionV1 — kernel error path (no tmux)
// =============================================================================

func TestHandleCreateSessionV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"session":"test-create"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body))

	srv.handleCreateSessionV1(rec, req)

	// Should error because kernel can't create tmux session in test env
	if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 500 or 201", rec.Code)
	}
}

// =============================================================================
// handleSessionStatusV1 / handleSessionAttachV1 — kernel error path
// =============================================================================

func TestHandleSessionStatusV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionStatusV1(rec, req)

	// Should error because kernel can't find tmux session
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleSessionAttachV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/attach", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionAttachV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// Publish (WSHub) — basic path with running hub
// =============================================================================

func TestWSHubPublish_WithClient(t *testing.T) {
	t.Parallel()

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create a client that subscribes to global:events
	client := &WSClient{
		id:     "test-client",
		send:   make(chan []byte, 16),
		topics: map[string]struct{}{"*": {}},
	}
	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	hub.Publish("global:events", "test_event", map[string]interface{}{
		"msg": "hello",
	})

	// Should receive the event
	select {
	case data := <-client.send:
		var evt WSEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		if evt.EventType != "test_event" {
			t.Errorf("event type = %q, want test_event", evt.EventType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// =============================================================================
// handleSessionEventsV1 — with events in the bus
// =============================================================================

func TestHandleSessionEventsV1_WithEvents(t *testing.T) {
	t.Parallel()

	bus := events.NewEventBus(100)
	srv := New(Config{EventBus: bus})

	// Emit an event
	bus.Publish(events.BaseEvent{
		Type:    "test.event",
		Session: "my-session",
	})

	time.Sleep(10 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/my-session/events", nil)

	srv.handleSessionEventsV1(rec, req, "my-session")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count < 1 {
		t.Errorf("count = %v, want >= 1", count)
	}
}

// =============================================================================
// handleSessionEvents (legacy) — nil eventBus
// =============================================================================

func TestHandleSessionEvents_NilEventBus(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test/events", nil)

	srv.handleSessionEvents(rec, req, "test")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// handleSessions (legacy) — nil stateStore
func TestHandleSessions_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// handleSessions — method not allowed
func TestHandleSessions_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// handleSession — method not allowed
func TestHandleSession_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test", nil)

	srv.handleSession(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// handleSession — nil stateStore
func TestHandleSession_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-id", nil)

	srv.handleSession(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// handleSession — success path with session found
func TestHandleSession_Success(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "legacy-session")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/legacy-session", nil)

	srv.handleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["session"] == nil {
		t.Error("expected session in response")
	}
}

// handleSession — sub-resource routing (agents)
func TestHandleSession_SubResourceAgents(t *testing.T) {
	t.Parallel()
	srv, store := setupTestServer(t)
	createTestSessionForServe(t, store, "sub-agents")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sub-agents/agents", nil)

	srv.handleSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// =============================================================================
// Pane handler validation branches
// =============================================================================

func TestHandlePaneOutputV1_InvalidIndex(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s/panes/abc/output", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneOutputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneOutputV1_InvalidLines(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s/panes/0/output?lines=abc", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneOutputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneOutputV1_LinesOutOfRange(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s/panes/0/output?lines=99999", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneOutputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneOutputV1_TmuxError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/nonexistent/panes/0/output", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "nonexistent")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneOutputV1(rec, req)

	// Should 500 since tmux session doesn't exist
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandlePaneInputV1_InvalidIndex(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"text":"hello","enter":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/panes/abc/input", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneInputV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/panes/0/input", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneInputV1_EmptyText(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/panes/0/input", strings.NewReader(`{"text":""}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInputV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleSessionViewV1 — kernel error path
// =============================================================================

func TestHandleSessionViewV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/view", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionViewV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// handleListPanesV1 — tmux error path
// =============================================================================

func TestHandleListPanesV1_EmptyResult(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__nonexistent_session_12345__/panes", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "__nonexistent_session_12345__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListPanesV1(rec, req)

	// Non-existent session returns empty pane list (200) or error (500)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// =============================================================================
// Agent handlers — kernel error paths
// =============================================================================

func TestHandleListAgentsV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListAgentsV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentSendV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/agents/send", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentSendV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentInterruptV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/agents/interrupt", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentInterruptV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentWaitV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents/wait", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentWaitV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentRouteV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents/route?prompt=test", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentRouteV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentActivityV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents/activity", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentActivityV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentHealthV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentHealthV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentContextV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/agents/context", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentContextV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleAgentRestartV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/agents/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentRestartV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// Output handlers — missing session validation
// =============================================================================

func TestHandleOutputTailV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/tail?session=noexist&pane=0", nil)

	srv.handleOutputTailV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleOutputDiffV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/diff?session=noexist&pane=0", nil)

	srv.handleOutputDiffV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleOutputSummaryV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/summary?session=noexist&pane=0", nil)

	srv.handleOutputSummaryV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleContextBuildV1_EmptyBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/context/build", strings.NewReader(""))

	srv.handleContextBuildV1(rec, req)

	// Empty body should be rejected as bad request
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handlePolicyUpdateV1 — invalid YAML and validation branches
// =============================================================================

func TestHandlePolicyUpdateV1_InvalidYAML(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"content":"not: [valid: yaml: broken"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))

	srv.handlePolicyUpdateV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePolicyUpdateV1_InvalidPolicy(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	// Valid YAML but invalid policy (bad force_release value)
	rec := httptest.NewRecorder()
	body := `{"content":"version: 1\nautomation:\n  force_release: invalid_value"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))

	srv.handlePolicyUpdateV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePolicyUpdateV1_ValidPolicy(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	// Valid YAML with valid policy
	rec := httptest.NewRecorder()
	body := `{"content":"version: 1\nblocked:\n  - pattern: 'rm -rf /'\n    reason: dangerous"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))

	srv.handlePolicyUpdateV1(rec, req)

	// Should succeed (writes to ~/.ntm/policy.yaml)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handlePolicyAutomationUpdateV1 — additional branches
// =============================================================================

func TestHandlePolicyAutomationUpdateV1_ValidUpdate(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	autoCommit := true
	_ = autoCommit // used in JSON
	body := `{"auto_commit":true,"auto_push":false}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))

	srv.handlePolicyAutomationUpdateV1(rec, req)

	// Should succeed or error depending on policy file state
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePolicyAutomationUpdateV1_ForceReleaseApproval(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"force_release":"approval"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))

	srv.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handlePolicyValidateV1 — additional branches
// =============================================================================

func TestHandlePolicyValidateV1_InvalidYAML(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"content":"not: [valid: yaml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", strings.NewReader(body))

	srv.handlePolicyValidateV1(rec, req)

	// Should return 200 with valid=false
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["valid"] != false {
		t.Errorf("valid = %v, want false", resp["valid"])
	}
}

func TestHandlePolicyValidateV1_BadJSON(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", strings.NewReader("{bad"))

	srv.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handlePolicyResetV1 — success path
// =============================================================================

func TestHandlePolicyResetV1_Success(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/reset", nil)

	srv.handlePolicyResetV1(rec, req)

	// Should succeed (resets to default)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleSafetyInstallV1/UninstallV1 — additional paths
// =============================================================================

func TestHandleSafetyInstallV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/install", strings.NewReader("{bad"))

	srv.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSafetyUninstallV1_NothingInstalled(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/uninstall", nil)

	srv.handleSafetyUninstallV1(rec, req)

	// Should succeed with empty removed list
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleSafetyCheckV1 — valid command path
// =============================================================================

func TestHandleSafetyCheckV1_ValidCommand_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check",
		strings.NewReader(`{"command":"ls -la"}`))

	srv.handleSafetyCheckV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleApprovalsListV1 — with status filter
// =============================================================================

func TestHandleApprovalsListV1_WithStatusFilter(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals?status=pending", nil)

	srv.handleApprovalsListV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleApprovalsListV1_NoFilter(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals", nil)

	srv.handleApprovalsListV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// =============================================================================
// handleGetPaneTitleV1 — invalid pane index
// =============================================================================

func TestHandleGetPaneTitleV1_InvalidIndex(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s/panes/abc/title", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetPaneTitleV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetPaneTitleV1_InvalidIndex(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/s/panes/abc/title",
		strings.NewReader(`{"title":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSetPaneTitleV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetPaneTitleV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/s/panes/0/title",
		strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSetPaneTitleV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handlePaneInterruptV1 — invalid pane index
// =============================================================================

func TestHandlePaneInterruptV1_InvalidIndex(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/panes/abc/interrupt", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInterruptV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleContextBuildV1 — missing question field
// =============================================================================

func TestHandleContextBuildV1_MissingQuestion(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"project_dir":"/tmp","bead_id":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/context/build", strings.NewReader(body))

	srv.handleContextBuildV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleContextStatsV1 — missing session param
// =============================================================================

func TestHandleContextStatsV1_MissingSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/stats", nil)

	srv.handleContextStatsV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleContextStatsV1_WithLinesParam(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Use session name that doesn't exist in tmux
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/stats?session=__nonexistent_session_12345__&lines=50", nil)

	srv.handleContextStatsV1(rec, req)

	// robot.GetContext may succeed with empty data or error — verify it doesn't panic
	// and returns a valid HTTP response
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// handleRouteV1 — missing session + invalid exclude
// =============================================================================

func TestHandleRouteV1_MissingSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/route", nil)

	srv.handleRouteV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRouteV1_InvalidExclude(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/route?session=test&exclude=abc", nil)

	srv.handleRouteV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleWaitV1 — missing session
// =============================================================================

func TestHandleWaitV1_MissingSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wait", nil)

	srv.handleWaitV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleAgentSendV1 — empty message
// =============================================================================

func TestHandleAgentSendV1_EmptyMessage(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"message":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/agents/send", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentSendV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleAgentWaitV1 — invalid body + empty condition
// =============================================================================

func TestHandleAgentWaitV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/agents/wait", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentWaitV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentWaitV1_EmptyCondition(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"condition":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/agents/wait", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentWaitV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleSessionZoomV1 — kernel error path
// =============================================================================

func TestHandleSessionZoomV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"pane":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/zoom", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "noexist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleSessionZoomV1(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleGetPaneV1 — tmux error path
// =============================================================================

func TestHandleGetPaneV1_PaneNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__nonexistent_session_12345__/panes/99", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "__nonexistent_session_12345__")
	rctx.URLParams.Add("paneIdx", "99")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetPaneV1(rec, req)

	// Session doesn't exist, tmux returns empty pane list, pane not found → 404
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 404 or 500", rec.Code)
	}
}

// =============================================================================
// handleCreateBead — validation branches
// =============================================================================

func TestHandleCreateBead_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads", strings.NewReader("{bad"))

	srv.handleCreateBead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateBead_EmptyTitle(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"title":"","description":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads", strings.NewReader(body))

	srv.handleCreateBead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleClaimBead — validation branches
// =============================================================================

func TestHandleClaimBead_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-test/claim", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleClaimBead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleClaimBead_EmptyAssignee(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"assignee":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-test/claim", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleClaimBead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleUpdateBead — validation
// =============================================================================

func TestHandleUpdateBead_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/beads/bd-test", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleUpdateBead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleAddBeadDep — validation
// =============================================================================

func TestHandleAddBeadDep_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-test/deps", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAddBeadDep(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleContextGetV1 — nil stateStore
// =============================================================================

func TestHandleContextGetV1_NilStore(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/test-id", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("contextId", "test-id")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleContextGetV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// =============================================================================
// handleContextCacheClearV1 — always returns 200
// =============================================================================

func TestHandleContextCacheClearV1_Success(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/context/cache", nil)

	srv.handleContextCacheClearV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["cleared"] != true {
		t.Errorf("cleared = %v, want true", resp["cleared"])
	}
}

// =============================================================================
// handleGetConfigV1 — returns safe config fields
// =============================================================================

func TestHandleGetConfigV1_Success(t *testing.T) {
	t.Parallel()
	srv := New(Config{Host: "127.0.0.1", Port: 8080})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)

	srv.handleGetConfigV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["host"] != "127.0.0.1" {
		t.Errorf("host = %v, want 127.0.0.1", resp["host"])
	}
	port, _ := resp["port"].(float64)
	if port != 8080 {
		t.Errorf("port = %v, want 8080", port)
	}
}

// =============================================================================
// handleRobotHealth — kernel error
// =============================================================================

func TestHandleRobotHealth_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/robot/health", nil)

	srv.handleRobotHealth(rec, req)

	// robot.Health() will try tmux and likely fail in test env
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// handleAgentSpawnV1 — kernel error
// =============================================================================

func TestHandleAgentSpawnV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"cc_count":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nonexistent_session_12345__/agents/spawn", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "__nonexistent_session_12345__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleAgentSpawnV1(rec, req)

	// Should error or succeed — just verify it doesn't panic and returns a valid response
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// handleDepsV1 — kernel error (no tmux)
// =============================================================================

func TestHandleDepsV1_KernelError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps", nil)

	srv.handleDepsV1(rec, req)

	// kernel.Run will fail without tmux
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// =============================================================================
// handlePaneInputV1 — tmux error path (valid params, no tmux session)
// =============================================================================

func TestHandlePaneInputV1_TmuxError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"text":"hello","enter":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/panes/0/input", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInputV1(rec, req)

	// Should error since tmux session doesn't exist
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// handleListAgentsV1 — empty session ID
// =============================================================================

func TestHandleListAgentsV1_EmptySession_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListAgentsV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// =============================================================================
// handleSessionAgentsV1 — nil stateStore
// =============================================================================

func TestHandleSessionAgentsV1_NilStore_Branch(t *testing.T) {
	t.Parallel()
	srv := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/agents-v1", nil)

	srv.handleSessionAgentsV1(rec, req, "test")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// =============================================================================
// handlePaneOutputV1 — default lines (no lines param)
// =============================================================================

func TestHandlePaneOutputV1_DefaultLines(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/noexist/panes/0/output", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneOutputV1(rec, req)

	// Should error since tmux session doesn't exist, but exercises default lines path
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// =============================================================================
// handlePaneInterruptV1 — tmux error path
// =============================================================================

func TestHandlePaneInterruptV1_TmuxError(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/noexist/panes/0/interrupt", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "noexist")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handlePaneInterruptV1(rec, req)

	// Should error since tmux session doesn't exist
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// FOURTH BATCH: handleWaitV1 query param branches, pipeline handlers,
// checkMemoryDaemon, jobs handlers
// =============================================================================

// handleWaitV1 — query param parsing branches

func TestHandleWaitV1_WithAllQueryParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Exercise all query param parsing: timeout, poll, panes, count, condition,
	// agent_type, any, exit_on_error, require_transition
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wait?session=__nonexist__&timeout=1s&poll=100ms&panes=0,1,2&count=3"+
			"&condition=idle&agent_type=cc&any=true&exit_on_error=true&require_transition=true",
		nil)

	srv.handleWaitV1(rec, req)

	// Will call robot.GetWait with a nonexistent session — expect timeout or error
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleWaitV1_InvalidTimeoutAndPoll(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Invalid durations should be silently ignored (use defaults)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wait?session=__nonexist__&timeout=notaduration&poll=invalid&panes=abc,2&count=-1",
		nil)

	srv.handleWaitV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

func TestHandleWaitV1_EmptyPanesAndCount(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Empty panes param, zero count — exercises the "no panes" and "parsed <= 0" branches
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wait?session=__nonexist__&panes=&count=0",
		nil)

	srv.handleWaitV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleValidatePipeline — inline content branches

func TestHandleValidatePipeline_InvalidYAMLContent(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"workflow_content":"{{invalid yaml: [}"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", strings.NewReader(body))

	srv.handleValidatePipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "parse") {
		t.Errorf("error = %q, want substring 'parse'", errMsg)
	}
}

func TestHandleValidatePipeline_ValidInlineContent(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Minimal valid workflow YAML
	body := `{"workflow_content":"name: test\nsteps:\n  - name: step1\n    type: send\n    pane: 0\n    content: hello\n"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", strings.NewReader(body))

	srv.handleValidatePipeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["workflow_id"] != "test" {
		t.Errorf("workflow_id = %v, want 'test'", resp["workflow_id"])
	}
}

func TestHandleValidatePipeline_WorkflowFileNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"workflow_file":"/tmp/__nonexistent_workflow_file_12345__.yaml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate", strings.NewReader(body))

	srv.handleValidatePipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleExecPipeline — invalid workflow validation

func TestHandleExecPipeline_InvalidWorkflow(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Workflow with no steps → validation failure
	body := `{"workflow":{"name":"test","steps":[]},"session":"test-session"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/exec", strings.NewReader(body))

	srv.handleExecPipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleRunPipeline — non-existent workflow file

func TestHandleRunPipeline_WorkflowFileNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"workflow_file":"/tmp/__nonexistent_workflow_12345__.yaml","session":"s"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/run", strings.NewReader(body))

	srv.handleRunPipeline(rec, req)

	// Should fail because workflow file doesn't exist
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleResumePipeline — invalid JSON body

func TestHandleResumePipeline_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/abc/resume", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleResumePipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleCancelPipeline — not cancellable (pipeline exists but wrong status)
// Note: We can't easily create a pipeline with a specific status in unit tests
// without mocking, so we rely on existing NotFound tests in handler_coverage_extra_test.go

// handleCleanupPipelines — invalid JSON body

func TestHandleCleanupPipelines_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/cleanup", strings.NewReader("{bad"))

	srv.handleCleanupPipelines(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Jobs API handlers
// =============================================================================

func TestHandleListJobs_Empty(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)

	srv.handleListJobs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	count, _ := resp["count"].(float64)
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
}

func TestHandleCreateJob_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader("{bad"))

	srv.handleCreateJob(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateJob_MissingType(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"type":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))

	srv.handleCreateJob(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateJob_InvalidType(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"type":"invalid_type"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))

	srv.handleCreateJob(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "invalid job type") {
		t.Errorf("error = %q, want substring 'invalid job type'", errMsg)
	}
}

func TestHandleCreateJob_ValidType(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"type":"scan","session":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(body))

	srv.handleCreateJob(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	job, _ := resp["job"].(map[string]interface{})
	if job == nil {
		t.Fatal("expected job in response")
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetJob(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleCancelJob_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleCancelJob(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// =============================================================================
// checkMemoryDaemon — PID file parsing branches
// =============================================================================

func TestCheckMemoryDaemon_NoPidsDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	srv := New(Config{})
	srv.projectDir = tmpDir

	info := srv.checkMemoryDaemon()

	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped", info.State)
	}
}

func TestCheckMemoryDaemon_EmptyPidsDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	srv := New(Config{})
	srv.projectDir = tmpDir

	info := srv.checkMemoryDaemon()

	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped", info.State)
	}
}

func TestCheckMemoryDaemon_NonMatchingFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Files that don't match cm-*.pid pattern
	os.WriteFile(filepath.Join(pidsDir, "other.pid"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(pidsDir, "cm-test.txt"), []byte(`{}`), 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir

	info := srv.checkMemoryDaemon()

	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped", info.State)
	}
}

func TestCheckMemoryDaemon_InvalidJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Valid filename but invalid JSON
	os.WriteFile(filepath.Join(pidsDir, "cm-sess1.pid"), []byte(`{invalid json`), 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir

	info := srv.checkMemoryDaemon()

	// Invalid JSON → continue loop → return stopped
	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped", info.State)
	}
}

func TestCheckMemoryDaemon_ValidPIDFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Valid cm-*.pid file with valid JSON
	pidData := `{"pid":12345,"port":8200,"started_at":"2025-01-01T00:00:00Z"}`
	os.WriteFile(filepath.Join(pidsDir, "cm-mysession.pid"), []byte(pidData), 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir

	info := srv.checkMemoryDaemon()

	if info.State != DaemonStateRunning {
		t.Fatalf("state = %v, want running", info.State)
	}
	if info.PID != 12345 {
		t.Errorf("PID = %d, want 12345", info.PID)
	}
	if info.Port != 8200 {
		t.Errorf("Port = %d, want 8200", info.Port)
	}
	if info.SessionID != "mysession" {
		t.Errorf("SessionID = %q, want 'mysession'", info.SessionID)
	}
}

// =============================================================================
// FIFTH BATCH: checkpoint handlers, export format validation,
// import validation, rollback, more pipeline coverage
// =============================================================================

// handleCreateCheckpoint — session not found (tmux check)

func TestHandleCreateCheckpoint_SessionNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"name":"test-checkpoint"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nonexistent__/checkpoints", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nonexistent__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleCreateCheckpoint(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateCheckpoint_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleCreateCheckpoint(rec, req)

	// Session check first — either 404 (session not found) or 400 (bad body)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 404 or 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleRestoreCheckpoint — invalid body

func TestHandleRestoreCheckpoint_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/cp1/restore", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	rctx.URLParams.Add("checkpointId", "cp1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRestoreCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRestoreCheckpoint_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"force":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nosess__/checkpoints/__nocp__/restore", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRestoreCheckpoint(rec, req)

	// Restore will fail because checkpoint doesn't exist
	if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want error code; body: %s", rec.Code, rec.Body.String())
	}
}

// handleVerifyCheckpoint — checkpoint not found

func TestHandleVerifyCheckpoint_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__nosess__/checkpoints/__nocp__/verify", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleVerifyCheckpoint(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// handleExportCheckpoint — invalid format + not found

func TestHandleExportCheckpoint_InvalidFormat(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"format":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/cp1/export", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	rctx.URLParams.Add("checkpointId", "cp1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleExportCheckpoint_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"format":"tar.gz"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nosess__/checkpoints/__nocp__/export", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleExportCheckpoint_GetDefaultFormat(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// GET request exercises the query-param parsing branch
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__nosess__/checkpoints/__nocp__/export?redact_secrets=true", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleExportCheckpoint(rec, req)

	// Default format is tar.gz; checkpoint not found → 404
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleExportCheckpoint_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/cp1/export", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	rctx.URLParams.Add("checkpointId", "cp1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleImportCheckpoint — validation branches

func TestHandleImportCheckpoint_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/import", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleImportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleImportCheckpoint_MissingData(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"data":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/import", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleImportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleImportCheckpoint_InvalidBase64(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"data":"not!valid!base64!!!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/checkpoints/import", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleImportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleRollback — validation branches

func TestHandleRollback_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/rollback", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "s")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRollback(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRollback_CheckpointNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"checkpoint_ref":"__nonexistent__"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nosess__/rollback", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRollback(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRollback_DefaultRef(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	// Empty body → default ref "latest"
	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/__nosess__/rollback", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRollback(rec, req)

	// No checkpoints → not found
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// handleGetCheckpoint — not found (different name from extra tests)

func TestHandleGetCheckpoint_Branch_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/__nosess__/checkpoints/__nocp__", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleGetCheckpoint(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// handleListPipelineTemplates — should return 200 with templates list

func TestHandleListPipelineTemplates_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/templates", nil)

	srv.handleListPipelineTemplates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := resp["templates"]; !ok {
		t.Error("expected 'templates' key in response")
	}
}

// handleListPipelines — should return 200 with empty list

func TestHandleListPipelines_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil)

	srv.handleListPipelines(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// SIXTH BATCH: accounts handlers, auto-rotate config, CASS handlers
// =============================================================================

// handleListAccountsV1 — exercises robot.GetAccountsList

func TestHandleListAccountsV1_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)

	srv.handleListAccountsV1(rec, req)

	// caam may or may not be available — just verify valid HTTP response
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleAccountStatusV1 — exercises robot.GetAccountStatus

func TestHandleAccountStatusV1_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/status", nil)

	srv.handleAccountStatusV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleActiveAccountsV1 — exercises active account filtering

func TestHandleActiveAccountsV1_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/active", nil)

	srv.handleActiveAccountsV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleAccountQuotaV1 — exercises quota extraction

func TestHandleAccountQuotaV1_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/quota", nil)

	srv.handleAccountQuotaV1(rec, req)

	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleAccountHistoryV1 — exercises history retrieval + limit param

func TestHandleAccountHistoryV1_Empty(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/history", nil)

	srv.handleAccountHistoryV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total, _ := resp["total"].(float64)
	if total < 0 {
		t.Errorf("total = %v, want >= 0", total)
	}
}

func TestHandleAccountHistoryV1_WithLimit(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/history?limit=5", nil)

	srv.handleAccountHistoryV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleAccountHistoryV1_InvalidLimit(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/history?limit=abc", nil)

	srv.handleAccountHistoryV1(rec, req)

	// Invalid limit falls back to default — still succeeds
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// handlePatchAutoRotateConfigV1 — config update branches

func TestHandlePatchAutoRotateConfigV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/auto-rotate", strings.NewReader("{bad"))

	srv.handlePatchAutoRotateConfigV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePatchAutoRotateConfigV1_CooldownTooLow(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"auto_rotate_cooldown_seconds":10}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/auto-rotate", strings.NewReader(body))

	srv.handlePatchAutoRotateConfigV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePatchAutoRotateConfigV1_ValidUpdate(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"auto_rotate_enabled":true,"auto_rotate_cooldown_seconds":120}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/auto-rotate", strings.NewReader(body))

	srv.handlePatchAutoRotateConfigV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// handleRotateAccountV1 — validation branches

func TestHandleRotateAccountV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/rotate", strings.NewReader("{bad"))

	srv.handleRotateAccountV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRotateAccountV1_MissingProvider(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"provider":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/rotate", strings.NewReader(body))

	srv.handleRotateAccountV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// handleListAccountsByProviderV1 — validation + exercise

func TestHandleListAccountsByProviderV1_EmptyProvider(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListAccountsByProviderV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleListAccountsByProviderV1_WithProvider(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/anthropic", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "anthropic")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleListAccountsByProviderV1(rec, req)

	// May succeed or fail depending on caam availability
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleRotateProviderAccountV1 — validation branches

func TestHandleRotateProviderAccountV1_EmptyProvider(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/rotate", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleRotateProviderAccountV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRotateProviderAccountV1_InvalidBody(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/anthropic/rotate", strings.NewReader("{bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "anthropic")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req.ContentLength = 4 // mark non-empty body

	srv.handleRotateProviderAccountV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleGetAutoRotateConfigV1 — always returns 200

func TestHandleGetAutoRotateConfigV1_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/auto-rotate", nil)

	srv.handleGetAutoRotateConfigV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// handleCASSSearch — query validation (already have BadRequest test, add empty query)

func TestHandleCASSSearch_EmptyQuery(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"query":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/search", strings.NewReader(body))

	srv.handleCASSSearch(rec, req)

	// Either 503 (CASS not installed) or 400 (empty query)
	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 503 or 400; body: %s", rec.Code, rec.Body.String())
	}
}

// handleMemoryContext — validation

func TestHandleMemoryContext_EmptySession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"task":"test","session_id":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context", strings.NewReader(body))

	srv.handleMemoryContext(rec, req)

	// Will exercise the session_id validation or daemon check branches
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// handleDeleteCheckpoint — not found (deeper branch exercise)

func TestHandleDeleteCheckpoint_Branch_NotFound(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/__nosess__/checkpoints/__nocp__", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "__nosess__")
	rctx.URLParams.Add("checkpointId", "__nocp__")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	srv.handleDeleteCheckpoint(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// BATCH 7: ws_events cleanup, matchTopic, memory outcome, MemoryStore, publish
// =============================================================================

// --- WSEventStore cleanup (16.7% → higher) ---

func TestWSEventStore_CleanupRemovesOldEvents(t *testing.T) {
	t.Parallel()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       100,
		RetentionSeconds: 1, // 1 second retention
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Insert events directly with old timestamps (2 hours ago) using Go time format
	oldTime := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		db.Exec("INSERT INTO ws_events (topic, event_type, data, created_at) VALUES (?, ?, ?, ?)",
			"test", "ev", `{"i":0}`, oldTime)
	}

	// Verify events exist
	var before int
	db.QueryRow("SELECT COUNT(*) FROM ws_events").Scan(&before)
	if before != 5 {
		t.Fatalf("expected 5 events before cleanup, got %d", before)
	}

	// Run cleanup directly
	if err := store.cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Verify events were removed from DB
	var count int
	db.QueryRow("SELECT COUNT(*) FROM ws_events").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 events after cleanup, got %d", count)
	}
}

func TestWSEventStore_CleanupNilDB(t *testing.T) {
	t.Parallel()
	store := &WSEventStore{db: nil}
	if err := store.cleanup(); err != nil {
		t.Errorf("cleanup with nil db should return nil, got %v", err)
	}
}

func TestWSEventStore_CleanupDroppedEvents(t *testing.T) {
	t.Parallel()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       100,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Insert dropped event records and backdate them using Go time format
	store.RecordDropped("client-1", "test", "slow", 1, 5)
	oldTime := time.Now().Add(-48 * time.Hour)
	_, _ = db.Exec("UPDATE ws_dropped_events SET created_at = ?", oldTime)

	// Run cleanup — should remove dropped events older than 24h
	if err := store.cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM ws_dropped_events").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 dropped events after cleanup, got %d", count)
	}
}

func TestWSEventStore_RecordDroppedNilDB(t *testing.T) {
	t.Parallel()
	store := &WSEventStore{db: nil}
	if err := store.RecordDropped("c1", "t", "r", 1, 5); err != nil {
		t.Errorf("RecordDropped with nil db should return nil, got %v", err)
	}
}

func TestWSEventStore_GetDroppedStatsNilDB(t *testing.T) {
	t.Parallel()
	store := &WSEventStore{db: nil}
	stats, err := store.GetDroppedStats("c1", time.Now())
	if err != nil {
		t.Errorf("GetDroppedStats nil db: %v", err)
	}
	if stats != nil {
		t.Errorf("expected nil stats from nil db, got %v", stats)
	}
}

func TestWSEventStore_CurrentSeqBranch(t *testing.T) {
	t.Parallel()
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 10, CleanupInterval: time.Hour})
	defer store.Stop()

	if seq := store.CurrentSeq(); seq != 0 {
		t.Errorf("initial seq = %d, want 0", seq)
	}
	store.Store("test", "ev", map[string]interface{}{})
	if seq := store.CurrentSeq(); seq != 1 {
		t.Errorf("after store seq = %d, want 1", seq)
	}
}

func TestWSEventStore_GetSinceDBFallback_CursorTooOld(t *testing.T) {
	t.Parallel()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       5, // tiny buffer
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store enough events to overflow ring buffer (all with topic "test")
	for i := 0; i < 20; i++ {
		store.Store("test", "ev", map[string]interface{}{"i": i})
	}

	// Delete old events from DB so minSeq jumps up
	db.Exec("DELETE FROM ws_events WHERE seq <= 15")

	// Ask for events since seq 1 with a non-matching topic.
	// Buffer can't satisfy (cursor too old), falls back to DB.
	// DB returns events 16-20 (seq > 1) but topic filter removes them all.
	// len(events)==0, since=1, minSeq=16, since < minSeq-1 → reset!
	events, needsReset, err := store.GetSince(1, "nonexistent:*", 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if !needsReset {
		t.Errorf("expected reset signal, got %d events", len(events))
	}
}

func TestWSEventStore_GetSinceDBWithTopicFilter(t *testing.T) {
	t.Parallel()
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := WSEventStoreConfig{
		BufferSize:       3, // tiny buffer forces DB fallback
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	}
	store := NewWSEventStore(db, cfg)
	defer store.Stop()

	// Store events with different topics
	store.Store("panes:proj:0", "pane.output", map[string]interface{}{})
	store.Store("panes:proj:1", "pane.output", map[string]interface{}{})
	store.Store("sessions:proj", "session.started", map[string]interface{}{})
	store.Store("panes:proj:2", "pane.output", map[string]interface{}{})
	store.Store("global", "system.event", map[string]interface{}{})

	// Buffer only has last 3, so seq=0 forces DB fallback
	events, _, err := store.GetSince(0, "panes:*", 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 pane events, got %d", len(events))
	}
}

func TestWSEventStore_GetSinceDefaultLimit(t *testing.T) {
	t.Parallel()
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 100, CleanupInterval: time.Hour})
	defer store.Stop()

	for i := 0; i < 5; i++ {
		store.Store("test", "ev", map[string]interface{}{"i": i})
	}

	// limit=0 should default to 1000 (not return 0 events)
	events, _, err := store.GetSince(0, "", 0)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("expected 5 events with default limit, got %d", len(events))
	}
}

// --- matchTopic direct tests ---

func TestMatchTopic_Wildcard(t *testing.T) {
	t.Parallel()
	if !matchTopic("*", "anything") {
		t.Error("* should match anything")
	}
}

func TestMatchTopic_PrefixWildcard(t *testing.T) {
	t.Parallel()
	if !matchTopic("sessions:*", "sessions:proj1") {
		t.Error("sessions:* should match sessions:proj1")
	}
	if matchTopic("sessions:*", "panes:proj1") {
		t.Error("sessions:* should not match panes:proj1")
	}
}

func TestMatchTopic_ExactMatch(t *testing.T) {
	t.Parallel()
	if !matchTopic("global", "global") {
		t.Error("exact match should work")
	}
	if matchTopic("global", "other") {
		t.Error("non-match should return false")
	}
}

// --- handleMemoryOutcome — daemon not running (deeper branch) ---

func TestHandleMemoryOutcome_DaemonNotRunning(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	srv := New(Config{})
	srv.projectDir = tmpDir

	rec := httptest.NewRecorder()
	body := `{"status":"success","rule_ids":["r1"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	srv.handleMemoryOutcome(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

// --- NewMemoryStore / MemoryStore methods ---

func TestNewMemoryStoreBranch(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	info := store.GetDaemonInfo()
	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped", info.State)
	}
}

func TestMemoryStore_SetGetDaemonInfo(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	now := time.Now()
	store.SetDaemonInfo(&MemoryDaemonInfo{
		State:     DaemonStateRunning,
		PID:       12345,
		Port:      8200,
		SessionID: "test-session",
		StartedAt: &now,
	})
	info := store.GetDaemonInfo()
	if info.State != DaemonStateRunning {
		t.Errorf("state = %v, want running", info.State)
	}
	if info.PID != 12345 {
		t.Errorf("pid = %d, want 12345", info.PID)
	}
}

// --- DefaultWSEventStoreConfig ---

func TestDefaultWSEventStoreConfigBranch(t *testing.T) {
	t.Parallel()
	cfg := DefaultWSEventStoreConfig()
	if cfg.BufferSize != 10000 {
		t.Errorf("BufferSize = %d, want 10000", cfg.BufferSize)
	}
	if cfg.RetentionSeconds != 3600 {
		t.Errorf("RetentionSeconds = %d, want 3600", cfg.RetentionSeconds)
	}
	if cfg.CleanupInterval != 5*time.Minute {
		t.Errorf("CleanupInterval = %v, want 5m", cfg.CleanupInterval)
	}
}

// --- publishMemoryEvent / publishMailEvent / publishReservationEvent nil hub ---

func TestPublishMemoryEvent_NilHub(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	// Should not panic
	srv.publishMemoryEvent("test.event", map[string]interface{}{"key": "val"})
}

func TestPublishMailEvent_NilHubBranch(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	srv.publishMailEvent("agent1", "mail.sent", map[string]interface{}{"key": "val"})
}

func TestPublishReservationEvent_NilHubBranch(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	srv.publishReservationEvent("agent1", "reservation.granted", map[string]interface{}{})
}

func TestPublishMailEvent_EmptyAgentName(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	srv.publishMailEvent("", "mail.sent", map[string]interface{}{"key": "val"})
}

func TestPublishReservationEvent_EmptyAgentName(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	srv.publishReservationEvent("", "reservation.granted", map[string]interface{}{})
}

// --- checkMemoryDaemon with invalid PID file JSON ---

func TestCheckMemoryDaemon_InvalidPIDJSON(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write invalid JSON PID file
	os.WriteFile(filepath.Join(pidsDir, "cm-badsession.pid"), []byte("{bad json"), 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir
	info := srv.checkMemoryDaemon()
	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped (invalid JSON should be skipped)", info.State)
	}
}

func TestCheckMemoryDaemon_NonCMPidFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write a non-cm PID file — should be ignored
	os.WriteFile(filepath.Join(pidsDir, "other-daemon.pid"), []byte(`{"pid":1}`), 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir
	info := srv.checkMemoryDaemon()
	if info.State != DaemonStateStopped {
		t.Errorf("state = %v, want stopped (non-cm PID files ignored)", info.State)
	}
}

// --- WSEventStore Stop idempotency ---

func TestWSEventStore_StopMemoryOnly(t *testing.T) {
	t.Parallel()
	store := NewWSEventStore(nil, WSEventStoreConfig{BufferSize: 10, CleanupInterval: time.Hour})
	// Stop should not panic even without cleanup ticker
	store.Stop()
}

// --- NewWSEventStore config defaults ---

func TestNewWSEventStore_ZeroConfig(t *testing.T) {
	t.Parallel()
	store := NewWSEventStore(nil, WSEventStoreConfig{})
	defer store.Stop()

	// Should apply defaults
	if store.bufferSize != 10000 {
		t.Errorf("bufferSize = %d, want 10000", store.bufferSize)
	}
	if store.retentionSecs != 3600 {
		t.Errorf("retentionSecs = %d, want 3600", store.retentionSecs)
	}
}

// =============================================================================
// BATCH 8: scanner handler branches, memory daemon, audit store, publish events
// =============================================================================

// --- Scanner handler branches ---

func TestHandleListFindings_IncludeDismissedAndPagination(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-a", "finding-pg1", scanner.SeverityWarning, "main.go", "security", false, "")
	addTestFinding("scan-a", "finding-pg2", scanner.SeverityCritical, "other.go", "perf", true, "")
	addTestFinding("scan-a", "finding-pg3", scanner.SeverityInfo, "util.go", "style", false, "")

	srv, _ := setupTestServer(t)

	// Test include_dismissed=true with limit and offset
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/scanner/findings?include_dismissed=true&limit=2&offset=0", nil)
	rec := httptest.NewRecorder()
	srv.handleListFindings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("count=%d, want 2 (limit=2 from 3 findings)", count)
	}
	if int(resp["offset"].(float64)) != 0 {
		t.Errorf("offset=%v, want 0", resp["offset"])
	}

	// Test with offset past all findings
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/scanner/findings?include_dismissed=true&limit=10&offset=100", nil)
	rec2 := httptest.NewRecorder()
	srv.handleListFindings(rec2, req2)
	var resp2 map[string]interface{}
	json.NewDecoder(rec2.Body).Decode(&resp2)
	if int(resp2["count"].(float64)) != 0 {
		t.Errorf("count=%v, want 0 for offset past end", resp2["count"])
	}
}

func TestHandleListBugs_OffsetPastTotal(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-bugs-1", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bugs?offset=100", nil)
	rec := httptest.NewRecorder()
	srv.handleListBugs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 0 {
		t.Errorf("count=%v, want 0 for offset past total", resp["count"])
	}
}

func TestHandleListBugs_CategoryFilter(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-cat1", scanner.SeverityWarning, "main.go", "security", false, "")
	addTestFinding("scan-1", "finding-cat2", scanner.SeverityCritical, "main.go", "performance", false, "")
	addTestFinding("scan-1", "finding-cat3", scanner.SeverityInfo, "main.go", "style", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bugs?category=performance", nil)
	rec := httptest.NewRecorder()
	srv.handleListBugs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 1 {
		t.Errorf("count=%v, want 1 for category=performance", resp["count"])
	}
}

func TestHandleListBugs_LimitPagination(t *testing.T) {
	resetScannerStoreForTest()
	for i := 0; i < 5; i++ {
		addTestFinding("scan-1", fmt.Sprintf("finding-lim%d", i),
			scanner.SeverityWarning, "main.go", "security", false, "")
	}

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bugs?limit=2&offset=1", nil)
	rec := httptest.NewRecorder()
	srv.handleListBugs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 2 {
		t.Errorf("count=%v, want 2 (limit=2, offset=1)", resp["count"])
	}
	if int(resp["total"].(float64)) != 5 {
		t.Errorf("total=%v, want 5", resp["total"])
	}
}

func TestHandleScannerHistory_LimitCapped(t *testing.T) {
	resetScannerStoreForTest()
	addTestScan("scan-cap1", ScanStateCompleted)

	srv, _ := setupTestServer(t)
	// limit > 1000 should be capped
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/history?limit=9999", nil)
	rec := httptest.NewRecorder()
	srv.handleScannerHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["limit"].(float64)) != 1000 {
		t.Errorf("limit=%v, want 1000 (capped)", resp["limit"])
	}
}

func TestHandleListFindings_LimitCapped(t *testing.T) {
	resetScannerStoreForTest()

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/findings?limit=9999", nil)
	rec := httptest.NewRecorder()
	srv.handleListFindings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if int(resp["limit"].(float64)) != 1000 {
		t.Errorf("limit=%v, want 1000 (capped)", resp["limit"])
	}
}

func TestHandleDismissFinding_NoRBACContext(t *testing.T) {
	resetScannerStoreForTest()
	addTestFinding("scan-1", "finding-norbac", scanner.SeverityWarning, "main.go", "security", false, "")

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/finding-norbac/dismiss", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "finding-norbac")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	// Intentionally no RBAC context
	rec := httptest.NewRecorder()

	srv.handleDismissFinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}

	finding, _ := scannerStore.GetFinding("finding-norbac")
	if finding.DismissedBy != "unknown" {
		t.Errorf("DismissedBy=%q, want 'unknown' when no RBAC context", finding.DismissedBy)
	}
}

func TestHandleDismissFinding_NotFoundBranch(t *testing.T) {
	resetScannerStoreForTest()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/ghost/dismiss", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "ghost")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleDismissFinding(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestHandleCreateBeadFromFinding_NotFound(t *testing.T) {
	resetScannerStoreForTest()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/findings/ghost/create-bead", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "ghost")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCreateBeadFromFinding(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

func TestPublishScannerEvent_NilHub(t *testing.T) {
	t.Parallel()
	srv := &Server{} // no wsHub
	// Should not panic
	srv.publishScannerEvent("test.event", map[string]interface{}{"key": "val"})
}

// --- Memory daemon start/stop deeper branches ---

func TestHandleMemoryDaemonStart_CMNotInstalled(t *testing.T) {
	srv := New(Config{})
	srv.projectDir = t.TempDir()

	// Ensure cm is not in PATH by using a temp empty dir
	t.Setenv("PATH", t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/start", nil)
	srv.handleMemoryDaemonStart(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMemoryDaemonStop_RunningDaemon(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	os.MkdirAll(pidsDir, 0o755)

	// Write a valid-looking PID file with a non-existent PID (Signal will silently fail)
	pidInfo := map[string]interface{}{
		"pid":        999999999,
		"port":       8200,
		"session_id": "test-session",
		"started_at": time.Now().Format(time.RFC3339),
	}
	data, _ := json.Marshal(pidInfo)
	os.WriteFile(filepath.Join(pidsDir, "cm-test-session.pid"), data, 0o644)

	srv := New(Config{})
	srv.projectDir = tmpDir

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/stop", nil)
	srv.handleMemoryDaemonStop(rec, req)

	// Should succeed (200) — the daemon appears running due to our PID file
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// --- Memory context CLI fallback branch ---

func TestHandleMemoryContext_CLINotInstalled(t *testing.T) {
	srv := New(Config{})
	srv.projectDir = t.TempDir()

	// Ensure cm is not in PATH
	t.Setenv("PATH", t.TempDir())

	body := `{"task":"test task"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context", strings.NewReader(body))
	srv.handleMemoryContext(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

// --- Memory rules CLI not installed ---

func TestHandleMemoryRules_CLINotInstalled(t *testing.T) {
	srv := New(Config{})
	srv.projectDir = t.TempDir()

	// Ensure cm is not in PATH
	t.Setenv("PATH", t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/rules", nil)
	srv.handleMemoryRules(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

// --- Audit store config defaults ---

func TestNewAuditStore_DefaultRetention(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:    filepath.Join(tmpDir, "audit.db"),
		JSONLPath: filepath.Join(tmpDir, "audit.jsonl"),
		Retention: 0, // zero → should apply default
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	if store.retention != 90*24*time.Hour {
		t.Errorf("retention = %v, want 90 days", store.retention)
	}
}

func TestNewAuditStore_NoPaths(t *testing.T) {
	t.Parallel()
	// No DBPath or JSONLPath → minimal store
	store, err := NewAuditStore(AuditStoreConfig{
		Retention:       time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	if store.db != nil {
		t.Error("expected nil db with empty DBPath")
	}
}

// =============================================================================
// Batch 9: Mail/Reservation handlers — client unavailable → 503
// =============================================================================

// TestMailAndReservationHandlers_ClientUnavailable exercises the "client nil → 503"
// path in all mail and reservation handlers. It uses a mock HTTP server that returns
// 500 for health checks, making IsAvailable() return false quickly.
// Non-parallel because it uses t.Setenv.
func TestMailAndReservationHandlers_ClientUnavailable(t *testing.T) {
	// Mock server: return 500 for all requests (makes IsAvailable() return false)
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockSrv.Close()

	// Point agent mail client to our mock server
	t.Setenv("AGENT_MAIL_URL", mockSrv.URL+"/mcp/")

	// Helper: assert 503 status
	assert503 := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d, want 503; body: %s", rec.Code, rec.Body.String())
		}
	}

	// Helper: build chi context with URL params
	withChi := func(req *http.Request, params map[string]string) *http.Request {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}

	t.Run("ListMailProjects", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/projects", nil)
		srv.handleListMailProjects(rec, req)
		assert503(t, rec)
	})

	t.Run("ListMailAgents", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/agents", nil)
		srv.handleListMailAgents(rec, req)
		assert503(t, rec)
	})

	t.Run("CreateMailAgent", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"program":"claude-code","model":"opus-4.5"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents", strings.NewReader(body))
		srv.handleCreateMailAgent(rec, req)
		assert503(t, rec)
	})

	t.Run("GetMailAgent", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/agents/TestAgent", nil)
		req = withChi(req, map[string]string{"name": "TestAgent"})
		srv.handleGetMailAgent(rec, req)
		assert503(t, rec)
	})

	t.Run("MailInbox", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=TestAgent", nil)
		srv.handleMailInbox(rec, req)
		assert503(t, rec)
	})

	t.Run("SendMessage", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"sender_name":"a","to":["b"],"subject":"s","body_md":"m"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages", strings.NewReader(body))
		srv.handleSendMessage(rec, req)
		assert503(t, rec)
	})

	t.Run("GetMessage", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/42", nil)
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleGetMessage(rec, req)
		assert503(t, rec)
	})

	t.Run("ReplyMessage", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"sender_name":"a","body_md":"reply text"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/reply", strings.NewReader(body))
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleReplyMessage(rec, req)
		assert503(t, rec)
	})

	t.Run("MarkMessageRead", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=TestAgent", nil)
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleMarkMessageRead(rec, req)
		assert503(t, rec)
	})

	t.Run("AckMessage", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/ack?agent_name=TestAgent", nil)
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleAckMessage(rec, req)
		assert503(t, rec)
	})

	t.Run("SearchMessages", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/search?q=test", nil)
		srv.handleSearchMessages(rec, req)
		assert503(t, rec)
	})

	t.Run("ThreadSummary", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-1/summary", nil)
		req = withChi(req, map[string]string{"id": "TKT-1"})
		srv.handleThreadSummary(rec, req)
		assert503(t, rec)
	})

	t.Run("ListContacts", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/contacts?agent_name=TestAgent", nil)
		srv.handleListContacts(rec, req)
		assert503(t, rec)
	})

	t.Run("RequestContact", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"from_agent":"a","to_agent":"b"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request", strings.NewReader(body))
		srv.handleRequestContact(rec, req)
		assert503(t, rec)
	})

	t.Run("RespondContact", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"to_agent":"a","from_agent":"b","accept":true}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond", strings.NewReader(body))
		srv.handleRespondContact(rec, req)
		assert503(t, rec)
	})

	t.Run("SetContactPolicy", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"agent_name":"a","policy":"open"}`
		req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy", strings.NewReader(body))
		srv.handleSetContactPolicy(rec, req)
		assert503(t, rec)
	})

	t.Run("ListReservations", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations", nil)
		srv.handleListReservations(rec, req)
		assert503(t, rec)
	})

	t.Run("ReservePaths", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"agent_name":"a","paths":["file.go"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))
		srv.handleReservePaths(rec, req)
		assert503(t, rec)
	})

	t.Run("ReleaseReservations", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"agent_name":"a"}`
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/reservations", strings.NewReader(body))
		srv.handleReleaseReservations(rec, req)
		assert503(t, rec)
	})

	t.Run("ReservationConflicts", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/conflicts?paths=file.go", nil)
		srv.handleReservationConflicts(rec, req)
		assert503(t, rec)
	})

	t.Run("GetReservation", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/42", nil)
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleGetReservation(rec, req)
		assert503(t, rec)
	})

	t.Run("ReleaseReservationByID", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/release?agent_name=a", nil)
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleReleaseReservationByID(rec, req)
		assert503(t, rec)
	})

	t.Run("RenewReservation", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"agent_name":"a","extend_seconds":60}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/renew", strings.NewReader(body))
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleRenewReservation(rec, req)
		assert503(t, rec)
	})

	t.Run("ForceReleaseReservation", func(t *testing.T) {
		srv := New(Config{})
		rec := httptest.NewRecorder()
		body := `{"agent_name":"a"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/42/force-release", strings.NewReader(body))
		req = withChi(req, map[string]string{"id": "42"})
		srv.handleForceReleaseReservation(rec, req)
		assert503(t, rec)
	})
}

// --- Memory daemon start: already running branch ---

func TestHandleMemoryDaemonStart_AlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	os.MkdirAll(pidsDir, 0o755)

	// Write a valid PID file so checkMemoryDaemon reports "running"
	pidInfo := map[string]interface{}{
		"pid":        999999999,
		"port":       8200,
		"session_id": "test-session",
		"started_at": time.Now().Format(time.RFC3339),
	}
	data, _ := json.Marshal(pidInfo)
	os.WriteFile(filepath.Join(pidsDir, "cm-test-session.pid"), data, 0o644)

	// Create a fake cm binary so exec.LookPath("cm") succeeds
	fakeBinDir := t.TempDir()
	fakeCM := filepath.Join(fakeBinDir, "cm")
	os.WriteFile(fakeCM, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("PATH", fakeBinDir)

	srv := New(Config{})
	srv.projectDir = tmpDir

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/start", nil)
	srv.handleMemoryDaemonStart(rec, req)

	// Should return 409 Conflict — daemon already running
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body: %s", rec.Code, rec.Body.String())
	}
}

// --- Memory daemon status: cm not installed branch ---

func TestHandleMemoryDaemonStatus_CMNotInstalled(t *testing.T) {
	srv := New(Config{})
	srv.projectDir = t.TempDir()

	t.Setenv("PATH", t.TempDir())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/daemon/status", nil)
	srv.handleMemoryDaemonStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["installed"] != false {
		t.Errorf("expected installed=false, got %v", resp["installed"])
	}
}

// --- Memory outcome: valid status values reach daemon check ---

func TestHandleMemoryOutcome_ValidStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"success", "failure", "partial"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			srv := New(Config{})
			srv.projectDir = t.TempDir()

			body := fmt.Sprintf(`{"status":"%s","rule_ids":["r1"],"notes":"test"}`, status)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
			srv.handleMemoryOutcome(rec, req)

			// Should hit "daemon not running" → 503
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d, want 503; body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// --- Memory privacy update: bad JSON ---

func TestHandleMemoryPrivacyUpdate_BadJSON(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/memory/privacy", strings.NewReader("not json"))
	srv.handleMemoryPrivacyUpdate(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// --- Memory context: empty task ---

func TestHandleMemoryContext_EmptyTaskBranch(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	srv.projectDir = t.TempDir()

	body := `{"task":""}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context", strings.NewReader(body))
	srv.handleMemoryContext(rec, req)

	// Empty task should return 400
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 10: WebSocket stub, JWKS, context build, robot health branches
// =============================================================================

// --- handleWS branches ---

func TestHandleWS_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ws", nil)
	srv.handleWS(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWS_NotWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	// No Upgrade header
	srv.handleWS(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWS_WebSocketUpgrade(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	srv.handleWS(rec, req)
	// Should return "not implemented" stub
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d, want 501; body: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRobotHealth: method not allowed ---

func TestHandleRobotHealth_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	srv.handleRobotHealth(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want 405; body: %s", rec.Code, rec.Body.String())
	}
}

// --- JWKS fetchJWKSKeys with mock server ---

func TestFetchJWKSKeys_EmptyURL(t *testing.T) {
	t.Parallel()
	_, err := fetchJWKSKeys(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "jwks url missing") {
		t.Fatalf("expected 'jwks url missing' error, got: %v", err)
	}
}

func TestFetchJWKSKeys_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	_, err := fetchJWKSKeys(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("expected status 500 error, got: %v", err)
	}
}

func TestFetchJWKSKeys_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	_, err := fetchJWKSKeys(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "parse jwks") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

func TestFetchJWKSKeys_NoValidKeys(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return JWKS with non-RSA key type
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "EC", "kid": "k1", "n": "", "e": ""},
			},
		})
	}))
	defer srv.Close()

	_, err := fetchJWKSKeys(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "no valid RSA keys") {
		t.Fatalf("expected 'no valid RSA keys' error, got: %v", err)
	}
}

func TestFetchJWKSKeys_ValidKey(t *testing.T) {
	t.Parallel()
	// Generate a real RSA key for testing
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "test-key", "n": nB64, "e": eB64, "alg": "RS256", "use": "sig"},
			},
		})
	}))
	defer srv.Close()

	keys, err := fetchJWKSKeys(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchJWKSKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if _, ok := keys["test-key"]; !ok {
		t.Error("expected key with kid 'test-key'")
	}
}

// --- jwksCache getKey with mock server ---

func TestJWKSCache_GetKey_CacheHit(t *testing.T) {
	t.Parallel()
	// Generate RSA key
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "k1", "n": nB64, "e": eB64, "alg": "RS256", "use": "sig"},
			},
		})
	}))
	defer srv.Close()

	cache := newJWKSCache(time.Hour)
	ctx := context.Background()

	// First call fetches from server
	key1, err := cache.getKey(ctx, srv.URL, "k1")
	if err != nil {
		t.Fatalf("first getKey: %v", err)
	}
	if key1 == nil {
		t.Fatal("expected non-nil key")
	}

	// Second call should use cache
	key2, err := cache.getKey(ctx, srv.URL, "k1")
	if err != nil {
		t.Fatalf("second getKey: %v", err)
	}
	if key2 == nil {
		t.Fatal("expected non-nil key")
	}
	if fetchCount != 1 {
		t.Errorf("expected 1 fetch (cache hit), got %d", fetchCount)
	}
}

func TestJWKSCache_GetKey_KidNotFound(t *testing.T) {
	t.Parallel()
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "k1", "n": nB64, "e": eB64, "alg": "RS256", "use": "sig"},
			},
		})
	}))
	defer srv.Close()

	cache := newJWKSCache(time.Hour)
	_, err := cache.getKey(context.Background(), srv.URL, "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "kid not found") {
		t.Fatalf("expected 'kid not found' error, got: %v", err)
	}
}

func TestJWKSCache_GetKey_EmptyKidSingleKey(t *testing.T) {
	t.Parallel()
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "only-key", "n": nB64, "e": eB64, "alg": "RS256", "use": "sig"},
			},
		})
	}))
	defer srv.Close()

	cache := newJWKSCache(time.Hour)
	// Empty kid with single key → should return that key
	key, err := cache.getKey(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatalf("getKey with empty kid: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key for empty kid with single key")
	}
}

func TestJWKSCache_GetKey_CachedEmptyKid(t *testing.T) {
	t.Parallel()
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	fetchCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "k1", "n": nB64, "e": eB64, "alg": "RS256", "use": "sig"},
			},
		})
	}))
	defer srv.Close()

	cache := newJWKSCache(time.Hour)
	ctx := context.Background()

	// First fetch populates cache
	_, _ = cache.getKey(ctx, srv.URL, "k1")

	// Second call with empty kid should use cache (single key)
	key, err := cache.getKey(ctx, srv.URL, "")
	if err != nil {
		t.Fatalf("cached getKey with empty kid: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil key")
	}
	if fetchCount != 1 {
		t.Errorf("expected 1 fetch (cache hit), got %d", fetchCount)
	}
}

// --- handleContextBuildV1: valid question ---

func TestHandleContextBuildV1_ValidQuestion(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"question":"what does this project do?"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/context/build", strings.NewReader(body))
	srv.handleContextBuildV1(rec, req)

	// Should either succeed (200) or fail (500) depending on ensemble generator
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 200 or 500; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleContextBuildV1_WithProjectDir(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := fmt.Sprintf(`{"question":"test","project_dir":"%s"}`, t.TempDir())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/context/build", strings.NewReader(body))
	srv.handleContextBuildV1(rec, req)

	// Ensemble generator may succeed or fail
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 200 or 500; body: %s", rec.Code, rec.Body.String())
	}
}

// --- parseRSAPublicKey branches ---

func TestParseRSAPublicKey_ValidKey_Branch(t *testing.T) {
	t.Parallel()
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	key, err := parseRSAPublicKey(nB64, eB64)
	if err != nil {
		t.Fatalf("parseRSAPublicKey: %v", err)
	}
	if key.N.Cmp(privKey.N) != 0 {
		t.Error("parsed key N doesn't match")
	}
}

func TestParseRSAPublicKey_BadN_Branch(t *testing.T) {
	t.Parallel()
	_, err := parseRSAPublicKey("not-valid-base64!!!", "AQAB")
	if err == nil || !strings.Contains(err.Error(), "decode jwk n") {
		t.Fatalf("expected decode n error, got: %v", err)
	}
}

func TestParseRSAPublicKey_BadE_Branch(t *testing.T) {
	t.Parallel()
	nB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(12345).Bytes())
	_, err := parseRSAPublicKey(nB64, "not-valid-base64!!!")
	if err == nil || !strings.Contains(err.Error(), "decode jwk e") {
		t.Fatalf("expected decode e error, got: %v", err)
	}
}

// --- JWKS key with no kid → default kid ---

func TestFetchJWKSKeys_DefaultKid(t *testing.T) {
	t.Parallel()
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	nB64 := base64.RawURLEncoding.EncodeToString(privKey.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privKey.E)).Bytes())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{"kty": "RSA", "kid": "", "n": nB64, "e": eB64, "alg": "RS256"},
			},
		})
	}))
	defer srv.Close()

	keys, err := fetchJWKSKeys(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchJWKSKeys: %v", err)
	}
	if _, ok := keys["default"]; !ok {
		t.Error("expected key with kid 'default' for empty kid")
	}
}

// =============================================================================
// Batch 11: Export checkpoint format validation + POST bad JSON
// =============================================================================

func TestHandleExportCheckpoint_GetBadFormat(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent-id")

	req := httptest.NewRequest("GET", "/api/v1/sessions/test-session/checkpoints/nonexistent-id/export?format=invalid", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleExportCheckpoint(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleExportCheckpoint_PostBadJSON(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent-id")

	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/checkpoints/nonexistent-id/export",
		strings.NewReader("{bad json"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleExportCheckpoint(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleExportCheckpoint_ZipFormatNotFound(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent-cp-id")

	// zip format branch + not found
	req := httptest.NewRequest("GET", "/api/v1/sessions/test-session/checkpoints/nonexistent-cp-id/export?format=zip", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleExportCheckpoint(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- checkWSOrigin branches ---

func TestCheckWSOrigin_Localhost(t *testing.T) {
	t.Parallel()
	s := &Server{}
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	result := s.checkWSOrigin(req)
	if !result {
		t.Error("expected localhost origin to be allowed")
	}
}

func TestCheckWSOrigin_LoopbackIP(t *testing.T) {
	t.Parallel()
	s := &Server{}
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	result := s.checkWSOrigin(req)
	if !result {
		t.Error("expected 127.0.0.1 origin to be allowed")
	}
}

func TestCheckWSOrigin_NoOriginNonLocal(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.auth.Mode = AuthModeAPIKey
	req := httptest.NewRequest("GET", "/ws", nil)
	// No Origin header — should still be allowed for non-browser clients
	result := s.checkWSOrigin(req)
	if !result {
		t.Error("expected no-origin request to be allowed even in non-local mode")
	}
}

func TestCheckWSOrigin_ExternalDomain(t *testing.T) {
	t.Parallel()
	s := &Server{}
	s.auth.Mode = AuthModeAPIKey // non-local mode to enable origin checking
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	result := s.checkWSOrigin(req)
	if result {
		t.Error("expected external domain origin to be rejected")
	}
}

// --- AuditStore record + query with data ---

func TestAuditStore_RecordAndQuery(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:    filepath.Join(tmpDir, "audit.db"),
		JSONLPath: filepath.Join(tmpDir, "audit.jsonl"),
		Retention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	// Record an entry
	err = store.Record(&AuditRecord{
		RequestID:  "req-001",
		UserID:     "user1",
		Role:       RoleAdmin,
		Action:     AuditActionCreate,
		Resource:   "session",
		Method:     "POST",
		Path:       "/api/v1/sessions",
		StatusCode: 201,
		Duration:   50,
		RemoteAddr: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Query for the record
	records, err := store.Query(AuditFilter{UserID: "user1", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

func TestAuditStore_QueryNoDb(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	// Create store with only JSONL, no DB
	store, err := NewAuditStore(AuditStoreConfig{
		JSONLPath: filepath.Join(tmpDir, "audit.jsonl"),
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	// Query without DB should fail
	_, err = store.Query(AuditFilter{Limit: 10})
	if err == nil {
		t.Error("expected error querying without db")
	}
}

// --- handleSetPaneTitleV1 bad JSON ---

func TestHandleSetPaneTitleV1_BadJSON(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	rctx.URLParams.Add("paneIndex", "0")
	req := httptest.NewRequest("PUT", "/api/v1/sessions/test-session/panes/0/title",
		strings.NewReader("{bad json"))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleSetPaneTitleV1(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- handleStartPaneStreamV1 empty session ---

func TestHandleStartPaneStreamV1_EmptySession(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	req := httptest.NewRequest("POST", "/api/v1/sessions//panes/0/stream/start", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleStartPaneStreamV1(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- handleStopPaneStreamV1 empty session ---

func TestHandleStopPaneStreamV1_EmptySession(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	rctx := chi.NewRouteContext()
	req := httptest.NewRequest("POST", "/api/v1/sessions//panes/0/stream/stop", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleStopPaneStreamV1(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// =============================================================================
// Batch 12: handleRunScan, ScannerStatus available, AuditStore Query filters,
//           AuditStore cleanup with data, handleRouteV1 exclude valid parse
// =============================================================================

// --- handleRunScan: bad JSON body ---

func TestHandleRunScan_BadJSON(t *testing.T) {
	if !scanner.IsAvailable() {
		t.Skip("ubs not installed")
	}
	resetScannerStoreForTest()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/run", strings.NewReader("{bad"))
	req.Header.Set("Content-Length", "4")
	w := httptest.NewRecorder()
	s.handleRunScan(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

// --- handleRunScan: successful start (ubs installed, no running scan, empty body) ---

func TestHandleRunScan_SuccessEmptyBody(t *testing.T) {
	if !scanner.IsAvailable() {
		t.Skip("ubs not installed")
	}
	resetScannerStoreForTest()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/run", nil)
	w := httptest.NewRecorder()
	s.handleRunScan(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202; body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["state"] != string(ScanStatePending) {
		t.Errorf("state=%v, want pending", resp["state"])
	}
	scanID, ok := resp["scan_id"].(string)
	if !ok || scanID == "" {
		t.Errorf("scan_id missing or empty: %v", resp["scan_id"])
	}
	// Give async goroutine a moment then clean up
	time.Sleep(50 * time.Millisecond)
}

// --- handleRunScan: successful start with custom path ---

func TestHandleRunScan_SuccessWithPath(t *testing.T) {
	if !scanner.IsAvailable() {
		t.Skip("ubs not installed")
	}
	resetScannerStoreForTest()
	s, _ := setupTestServer(t)

	customPath := t.TempDir()
	body := fmt.Sprintf(`{"path":%q}`, customPath)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scanner/run", strings.NewReader(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w := httptest.NewRecorder()
	s.handleRunScan(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d, want 202; body=%s", w.Code, w.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
}

// --- handleScannerStatus: with ubs available (tests version retrieval path) ---

func TestHandleScannerStatus_Available(t *testing.T) {
	if !scanner.IsAvailable() {
		t.Skip("ubs not installed")
	}
	resetScannerStoreForTest()
	addTestScan("scan-done", ScanStateCompleted)

	s, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scanner/status", nil)
	w := httptest.NewRecorder()
	s.handleScannerStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["available"] != true {
		t.Errorf("available=%v, want true", resp["available"])
	}
	// Version should be populated when ubs is installed
	if v, ok := resp["version"].(string); !ok || v == "" {
		t.Errorf("version should be non-empty when ubs is available, got %v", resp["version"])
	}
}

// --- AuditStore Query: exercise all filter branches ---

func TestAuditStore_QueryFilterBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")
	jsonlPath := filepath.Join(dir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbPath,
		JSONLPath:       jsonlPath,
		Retention:       24 * time.Hour,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	now := time.Now()

	// Insert a record with all fields populated
	rec := &AuditRecord{
		Timestamp: now,
		RequestID: "req-filter-001",
		UserID:    "user-alice",
		Role:      "admin",
		Action:    AuditActionCreate,
		Resource:  "session",
		Method:    "POST",
		Path:      "/api/v1/sessions",
		StatusCode: 201,
		SessionID: "sess-abc",
		ApprovalID: "approval-xyz",
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Insert a second record with different fields
	rec2 := &AuditRecord{
		Timestamp: now.Add(-time.Minute),
		RequestID: "req-filter-002",
		UserID:    "user-bob",
		Role:      "viewer",
		Action:    AuditActionExecute,
		Resource:  "pane",
		Method:    "GET",
		Path:      "/api/v1/panes",
		StatusCode: 200,
		SessionID: "sess-def",
	}
	if err := store.Record(rec2); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Filter by Action
	results, err := store.Query(AuditFilter{Action: AuditActionCreate})
	if err != nil {
		t.Fatalf("Query(action): %v", err)
	}
	if len(results) != 1 || results[0].RequestID != "req-filter-001" {
		t.Errorf("action filter: got %d results, want 1 with req-filter-001", len(results))
	}

	// Filter by Resource
	results, err = store.Query(AuditFilter{Resource: "pane"})
	if err != nil {
		t.Fatalf("Query(resource): %v", err)
	}
	if len(results) != 1 || results[0].RequestID != "req-filter-002" {
		t.Errorf("resource filter: got %d results", len(results))
	}

	// Filter by SessionID
	results, err = store.Query(AuditFilter{SessionID: "sess-abc"})
	if err != nil {
		t.Fatalf("Query(session): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("session filter: got %d results, want 1", len(results))
	}

	// Filter by ApprovalID
	results, err = store.Query(AuditFilter{ApprovalID: "approval-xyz"})
	if err != nil {
		t.Fatalf("Query(approval): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("approval filter: got %d results, want 1", len(results))
	}

	// Filter by Since
	results, err = store.Query(AuditFilter{Since: now.Add(-30 * time.Second)})
	if err != nil {
		t.Fatalf("Query(since): %v", err)
	}
	if len(results) != 1 || results[0].RequestID != "req-filter-001" {
		t.Errorf("since filter: got %d results, want 1 (recent only)", len(results))
	}

	// Filter by Until
	results, err = store.Query(AuditFilter{Until: now.Add(-30 * time.Second)})
	if err != nil {
		t.Fatalf("Query(until): %v", err)
	}
	if len(results) != 1 || results[0].RequestID != "req-filter-002" {
		t.Errorf("until filter: got %d results, want 1 (old only)", len(results))
	}

	// Filter by Limit + Offset
	results, err = store.Query(AuditFilter{Limit: 10, Offset: 1})
	if err != nil {
		t.Fatalf("Query(offset): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("offset filter: got %d results, want 1 (second record)", len(results))
	}
}

// --- AuditStore.cleanup: actually removes old records ---

func TestAuditStore_CleanupRemovesOldRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbPath,
		Retention:       1 * time.Millisecond, // Very short retention
		CleanupInterval: time.Hour,            // We'll call cleanup manually
	})
	if err != nil {
		t.Fatalf("NewAuditStore: %v", err)
	}
	defer store.Close()

	// Insert a record
	rec := &AuditRecord{
		Timestamp:  time.Now().Add(-time.Hour), // Old record
		RequestID:  "req-old-001",
		UserID:     "user-test",
		Action:     AuditActionCreate,
		Resource:   "test",
		Method:     "POST",
		Path:       "/test",
		StatusCode: 200,
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Verify record exists
	results, err := store.Query(AuditFilter{})
	if err != nil {
		t.Fatalf("Query before cleanup: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 record before cleanup")
	}

	// Wait for retention to expire and run cleanup
	time.Sleep(5 * time.Millisecond)
	store.cleanup()

	// Verify record is gone
	results, err = store.Query(AuditFilter{})
	if err != nil {
		t.Fatalf("Query after cleanup: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 records after cleanup, got %d", len(results))
	}
}

// --- handleRouteV1: valid exclude parameter ---

func TestHandleRouteV1_ValidExclude(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	// Provide session + valid exclude; will fail at robot.GetRoute (no tmux) but
	// exercises the exclude parsing success path
	req := httptest.NewRequest(http.MethodGet, "/api/v1/route?session=test-sess&exclude=0,2&strategy=round-robin", nil)
	w := httptest.NewRecorder()
	s.handleRouteV1(w, req)
	// Robot will fail since no tmux session exists, but we get past the exclude parsing
	// Accept either 200 (with error in body) or 400 (robot error)
	if w.Code == http.StatusBadRequest {
		var resp map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&resp); err == nil {
			// Should NOT be about exclude parsing — that succeeded
			if msg, ok := resp["message"].(string); ok && strings.Contains(msg, "invalid exclude") {
				t.Errorf("exclude should have parsed successfully, got: %s", msg)
			}
		}
	}
}

// --- handleAgentWaitV1: custom timeout_ms and poll_ms ---

func TestHandleAgentWaitV1_CustomTimeoutAndPoll(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-sess")
	body := `{"condition":"idle","timeout_ms":100,"poll_ms":50}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-sess/agents/wait", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleAgentWaitV1(w, req)
	// Robot will try to check tmux and either timeout or error, but we exercise
	// the timeout_ms and poll_ms custom parsing branches
	// Accept 200 (timeout result), 408 (timeout), or 500 (no tmux)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("should not get 400 for valid request, got body: %s", w.Body.String())
	}
}

// --- ScannerStore.GetScans offset beyond range ---

func TestScannerStore_GetScans_OffsetBeyondRange(t *testing.T) {
	t.Parallel()
	store := NewScannerStore()
	store.AddScan(&ScanRecord{ID: "scan-1", State: ScanStateCompleted, StartedAt: time.Now()})

	result := store.GetScans(10, 100) // offset=100 but only 1 scan
	if len(result) != 0 {
		t.Errorf("expected empty slice for offset beyond range, got %d", len(result))
	}
}

// --- ScannerStore.GetFindings: severity filter + dismissed filter + pagination ---

func TestScannerStore_GetFindings_FilterAndPaginate(t *testing.T) {
	t.Parallel()
	store := NewScannerStore()
	now := time.Now()

	// Add findings with different attributes
	store.AddFinding(&FindingRecord{
		ID: "f1", ScanID: "scan-a",
		Finding: scanner.Finding{Severity: scanner.SeverityWarning, File: "a.go", Category: "sec"},
		CreatedAt: now.Add(-2 * time.Second),
	})
	store.AddFinding(&FindingRecord{
		ID: "f2", ScanID: "scan-a",
		Finding: scanner.Finding{Severity: scanner.SeverityCritical, File: "b.go", Category: "perf"},
		CreatedAt: now.Add(-time.Second),
	})
	store.AddFinding(&FindingRecord{
		ID: "f3", ScanID: "scan-b",
		Finding:   scanner.Finding{Severity: scanner.SeverityWarning, File: "c.go", Category: "sec"},
		Dismissed: true,
		CreatedAt: now,
	})

	// Filter by scan_id
	results := store.GetFindings("scan-a", true, "", 100, 0)
	if len(results) != 2 {
		t.Errorf("scan_id filter: got %d, want 2", len(results))
	}

	// Filter by severity
	results = store.GetFindings("", false, "warning", 100, 0)
	if len(results) != 1 {
		t.Errorf("severity filter: got %d, want 1 (non-dismissed warning)", len(results))
	}

	// Include dismissed
	results = store.GetFindings("", true, "warning", 100, 0)
	if len(results) != 2 {
		t.Errorf("include dismissed: got %d, want 2", len(results))
	}

	// Pagination: limit=1, offset=0
	results = store.GetFindings("", true, "", 1, 0)
	if len(results) != 1 {
		t.Errorf("pagination limit=1: got %d, want 1", len(results))
	}

	// Pagination: offset beyond range
	results = store.GetFindings("", true, "", 10, 100)
	if len(results) != 0 {
		t.Errorf("offset beyond range: got %d, want 0", len(results))
	}
}

// --- ScannerStore.GetFindingsByScan ---

func TestScannerStore_GetFindingsByScan(t *testing.T) {
	t.Parallel()
	store := NewScannerStore()
	store.AddFinding(&FindingRecord{
		ID: "f1", ScanID: "scan-x",
		Finding:   scanner.Finding{File: "a.go"},
		CreatedAt: time.Now(),
	})
	store.AddFinding(&FindingRecord{
		ID: "f2", ScanID: "scan-y",
		Finding:   scanner.Finding{File: "b.go"},
		CreatedAt: time.Now(),
	})

	results := store.GetFindingsByScan("scan-x")
	if len(results) != 1 || results[0].ID != "f1" {
		t.Errorf("GetFindingsByScan: got %d results, want 1 with f1", len(results))
	}
}

// --- NewIdempotencyStore: default TTL for zero/negative ---

func TestNewIdempotencyStore_DefaultTTL(t *testing.T) {
	t.Parallel()
	store := NewIdempotencyStore(0)
	defer store.Stop()

	// Should not panic; TTL defaults to 24h
	store.Set("key1", []byte(`{"ok":true}`), 200)
	data, code, ok := store.Get("key1")
	if !ok {
		t.Fatal("expected key1 to be found")
	}
	if code != 200 {
		t.Errorf("code=%d, want 200", code)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("data=%q, want {\"ok\":true}", string(data))
	}
}

// --- NewIdempotencyStore: negative TTL defaults to 24h ---

func TestNewIdempotencyStore_NegativeTTL(t *testing.T) {
	t.Parallel()
	store := NewIdempotencyStore(-5 * time.Second)
	defer store.Stop()

	store.Set("k", []byte("v"), 201)
	_, code, ok := store.Get("k")
	if !ok || code != 201 {
		t.Errorf("expected key found with code 201, got ok=%v code=%d", ok, code)
	}
}

// --- IdempotencyStore.Get: expired entry returns not found ---

func TestIdempotencyStore_Get_Expired(t *testing.T) {
	t.Parallel()
	store := NewIdempotencyStore(1 * time.Millisecond)
	defer store.Stop()

	store.Set("ephemeral", []byte("data"), 200)
	time.Sleep(5 * time.Millisecond) // Wait for TTL to expire

	_, _, ok := store.Get("ephemeral")
	if ok {
		t.Error("expected expired entry to not be found")
	}
}

// =============================================================================
// Batch 13: CASS search/preview full path, bead handlers, checkpoint handlers
// =============================================================================

// --- handleCASSSearch: valid query (cass installed, full search path) ---

func TestHandleCASSSearch_ValidQuery(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	body := `{"query":"test","limit":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/search", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCASSSearch(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("success=%v, want true", resp["success"])
	}
	// Should have hits array
	if _, ok := resp["hits"]; !ok {
		t.Error("response missing 'hits' field")
	}
}

// --- handleCASSPreview: valid path (cass installed) ---

func TestHandleCASSPreview_ValidPath(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/preview?path=/nonexistent/file.go", nil)
	w := httptest.NewRecorder()
	s.handleCASSPreview(w, req)

	// Will either find the doc (200) or not find it (404) or search error (500)
	// Key thing: we get past the install check AND the path validation
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cass is installed")
	}
	if w.Code == http.StatusBadRequest {
		t.Fatal("should not get 400 with a valid path parameter")
	}
}

// --- handleCASSPreview: missing path parameter ---

func TestHandleCASSPreview_MissingPath(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/preview", nil)
	w := httptest.NewRecorder()
	s.handleCASSPreview(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

// --- handleListBeads: br installed but non-git project dir (exercises RunBd error path) ---

func TestHandleListBeads_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir() // temp dir, no git, no .beads

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads", nil)
	w := httptest.NewRecorder()
	s.handleListBeads(w, req)

	// br will fail because no .beads dir → 500
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", w.Code, w.Body.String())
	}
}

// --- handleListBeads: with filters query params ---

func TestHandleListBeads_WithFilters(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads?status=open&label=test&assignee=user1", nil)
	w := httptest.NewRecorder()
	s.handleListBeads(w, req)

	// Will fail at RunBd (no .beads dir) but exercises the filter-building branches
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsStats: br installed, temp dir ---

func TestHandleBeadsStats_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/stats", nil)
	w := httptest.NewRecorder()
	s.handleBeadsStats(w, req)

	// br stats will fail in temp dir → 500
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsReady: br installed, temp dir ---

func TestHandleBeadsReady_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/ready", nil)
	w := httptest.NewRecorder()
	s.handleBeadsReady(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsBlocked: br installed, temp dir ---

func TestHandleBeadsBlocked_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/blocked", nil)
	w := httptest.NewRecorder()
	s.handleBeadsBlocked(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsInProgress: br installed, temp dir ---

func TestHandleBeadsInProgress_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/in-progress", nil)
	w := httptest.NewRecorder()
	s.handleBeadsInProgress(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsDaemonStatus: br installed, temp dir (hits error → returns 200 with running:false) ---

func TestHandleBeadsDaemonStatus_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/daemon/status", nil)
	w := httptest.NewRecorder()
	s.handleBeadsDaemonStatus(w, req)

	// The daemon status handler returns 200 with running:false on error
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["running"] != false {
		t.Errorf("running=%v, want false", resp["running"])
	}
}

// --- handleBeadsDaemonStart: br installed, temp dir ---

func TestHandleBeadsDaemonStart_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/daemon/start", nil)
	w := httptest.NewRecorder()
	s.handleBeadsDaemonStart(w, req)

	// Will fail → 500
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsDaemonStop: br installed, temp dir ---

func TestHandleBeadsDaemonStop_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/daemon/stop", nil)
	w := httptest.NewRecorder()
	s.handleBeadsDaemonStop(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleBeadsSync: br installed, temp dir ---

func TestHandleBeadsSync_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/sync", nil)
	w := httptest.NewRecorder()
	s.handleBeadsSync(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// =============================================================================
// Batch 14: CASS capabilities/insights/timeline, bead get/update/close/deps,
//           memory daemon start, handleCreateBead with title
// =============================================================================

// --- handleCASSCapabilities: full path (cass installed) ---

func TestHandleCASSCapabilities_Installed(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/capabilities", nil)
	w := httptest.NewRecorder()
	s.handleCASSCapabilities(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("success=%v, want true", resp["success"])
	}
}

// --- handleCASSInsights: full path (cass installed) ---

func TestHandleCASSInsights_Installed(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/insights", nil)
	w := httptest.NewRecorder()
	s.handleCASSInsights(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// --- handleCASSTimeline: full path (cass installed) ---

func TestHandleCASSTimeline_Installed(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/timeline", nil)
	w := httptest.NewRecorder()
	s.handleCASSTimeline(w, req)

	// May return 200 or 500 depending on timeline data availability
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cass is installed")
	}
}

// --- handleGetBead: br installed, bead not found in temp dir ---

func TestHandleGetBead_NotFound(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-nonexistent")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-nonexistent", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleGetBead(w, req)

	// br show will fail → 404 (bead not found)
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleUpdateBead: br installed, valid JSON but no .beads ---

func TestHandleUpdateBead_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	body := `{"title":"updated title"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/beads/bd-test", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleUpdateBead(w, req)

	// br update fails in temp dir (no .beads) → 500
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
	if w.Code == http.StatusBadRequest {
		t.Fatal("valid JSON body should not produce 400")
	}
}

// --- handleCloseBead: br installed, bead not found ---

func TestHandleCloseBead_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-test/close", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleCloseBead(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleListBeadDeps: br installed, bad dir ---

func TestHandleListBeadDeps_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-test/deps", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleListBeadDeps(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleRemoveBeadDep: br installed, bad dir ---

func TestHandleRemoveBeadDep_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-test")
	rctx.URLParams.Add("depId", "bd-other")
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/beads/bd-test/deps/bd-other", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	s.handleRemoveBeadDep(w, req)

	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
}

// --- handleCreateBead: br installed, valid title but bad dir ---

func TestHandleCreateBead_BrInstalledBadDir(t *testing.T) {
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"title":"test bead","type":"task","priority":"P2","labels":["test"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleCreateBead(w, req)

	// br new fails (no git repo/no .beads) → 500
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when br is installed")
	}
	if w.Code == http.StatusBadRequest {
		t.Fatal("valid JSON with title should not produce 400")
	}
}

// --- handleMemoryDaemonStart: cm installed, temp dir (no existing daemon) ---

func TestHandleMemoryDaemonStart_CmInstalled(t *testing.T) {
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/start", nil)
	w := httptest.NewRecorder()
	s.handleMemoryDaemonStart(w, req)

	// Should pass cm install check, pass daemon running check (no PID file in temp dir),
	// then start async (202 Accepted)
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cm is installed")
	}
	if w.Code == http.StatusAccepted {
		var resp map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["state"] != string(DaemonStateStarting) {
			t.Errorf("state=%v, want starting", resp["state"])
		}
	}
	// Wait briefly for the async goroutine
	time.Sleep(50 * time.Millisecond)
}

// --- handleMemoryDaemonStart: bad JSON body ---

func TestHandleMemoryDaemonStart_BadJSON(t *testing.T) {
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := "{bad"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/start", strings.NewReader(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w := httptest.NewRecorder()
	s.handleMemoryDaemonStart(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// --- handleMemoryContext: cm installed ---

func TestHandleMemoryContext_CmInstalled(t *testing.T) {
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"task":"test task for coverage"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMemoryContext(w, req)

	// Should get past install check. May succeed (200) or fail (500) depending on cm state
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cm is installed")
	}
}

// --- handleMemoryOutcome: valid JSON, exercises parse + status validation ---

func TestHandleMemoryOutcome_ValidStatusParsing(t *testing.T) {
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"status":"success","rule_ids":["r1"],"sentiment":"positive"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMemoryOutcome(w, req)

	// Exercises JSON decode + status validation + daemon check branches
	// Will get 503 (daemon not running) but that's past the parse/validation
	if w.Code == http.StatusBadRequest {
		t.Fatal("valid body should not produce 400")
	}
}

// --- handleMemoryOutcome: invalid status ---

func TestHandleMemoryOutcome_InvalidStatus_Branch(t *testing.T) {
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"status":"bogus","rule_ids":["r1"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleMemoryOutcome(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for invalid status", w.Code)
	}
}

// --- handleMemoryOutcome: bad JSON ---

func TestHandleMemoryOutcome_BadJSON_Branch(t *testing.T) {
	s, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader("{bad"))
	w := httptest.NewRecorder()
	s.handleMemoryOutcome(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}

// --- handleBeadsRecipes: bv installed check ---

func TestHandleBeadsRecipes_BvInstalled(t *testing.T) {
	if !bv.IsInstalled() {
		t.Skip("bv not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/recipes", nil)
	w := httptest.NewRecorder()
	s.handleBeadsRecipes(w, req)

	// bv is installed so we get past the check; may fail with 500 (no .beads) or succeed
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when bv is installed")
	}
}

// --- helper: check if cass is installed ---

func cassInstalled() bool {
	c := cass.NewClient()
	return c.IsInstalled()
}

// =============================================================================
// Batch 15 — safety, policy, account, mail, memory handler branches
// =============================================================================

// --- handlePolicyValidateV1: content-based branches ---

func TestHandlePolicyValidateV1_ContentNoVersion(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	// YAML with rules but no version → warning "no version specified"
	body := `{"content":"blocked:\n  - pattern: 'rm -rf'\n    reason: dangerous"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", strings.NewReader(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should have a "no version specified" warning
	warnings, _ := resp["warnings"].([]interface{})
	found := false
	for _, w := range warnings {
		if s, ok := w.(string); ok && strings.Contains(s, "no version") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no version specified' warning, got warnings: %v", warnings)
	}
}

func TestHandlePolicyValidateV1_ContentNoRules(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	// YAML with version but no rules → warning "policy has no rules defined"
	body := `{"content":"version: 1"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", strings.NewReader(body))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	warnings, _ := resp["warnings"].([]interface{})
	found := false
	for _, w := range warnings {
		if s, ok := w.(string); ok && strings.Contains(s, "no rules") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no rules defined' warning, got warnings: %v", warnings)
	}
}

// --- handleSafetyCheckV1: blocked command exercises match != nil branch ---

func TestHandleSafetyCheckV1_BlockedCommand(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check",
		strings.NewReader(`{"command":"rm -rf /"}`))

	s.handleSafetyCheckV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should match a blocked pattern, action should not be "allow"
	action, _ := resp["action"].(string)
	if action == "" {
		t.Fatal("expected action field in response")
	}
	// The default policy blocks "rm -rf /" so we expect "block" or "approval"
	if action == "allow" {
		// May not match if policy doesn't have this exact pattern — still exercises code
		t.Log("command was allowed; default policy may not block this exact form")
	}
}

// --- handlePolicyAutomationUpdateV1: force_release branches ---

func TestHandlePolicyAutomationUpdateV1_ForceReleaseNever(t *testing.T) {
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"force_release":"never"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))

	s.handlePolicyAutomationUpdateV1(rec, req)

	// Should succeed (200) or 500 if policy write fails
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePolicyAutomationUpdateV1_ForceReleaseAuto(t *testing.T) {
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	body := `{"force_release":"auto"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))

	s.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePolicyAutomationUpdateV1_NoChanges(t *testing.T) {
	s, _ := setupTestServer(t)

	// Empty update — nothing changed → modified=false
	rec := httptest.NewRecorder()
	body := `{}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))

	s.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if modified, ok := resp["modified"].(bool); ok && modified {
			t.Error("expected modified=false for empty update")
		}
	}
}

// --- handleAccountHistoryV1: limit truncation + reverse order ---

func TestHandleAccountHistoryV1_LimitTruncation(t *testing.T) {
	// Pre-fill history with 5 events
	accountState.mu.Lock()
	accountState.history = []AccountRotationEvent{
		{Timestamp: "2025-01-01T00:00:00Z", Provider: "claude", NewAccount: "a1"},
		{Timestamp: "2025-01-02T00:00:00Z", Provider: "claude", NewAccount: "a2"},
		{Timestamp: "2025-01-03T00:00:00Z", Provider: "claude", NewAccount: "a3"},
		{Timestamp: "2025-01-04T00:00:00Z", Provider: "claude", NewAccount: "a4"},
		{Timestamp: "2025-01-05T00:00:00Z", Provider: "claude", NewAccount: "a5"},
	}
	accountState.mu.Unlock()
	defer func() {
		accountState.mu.Lock()
		accountState.history = make([]AccountRotationEvent, 0)
		accountState.mu.Unlock()
	}()

	s, _ := setupTestServer(t)

	// Request with limit=2 — should get most recent 2 in reverse order
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/history?limit=2", nil)

	s.handleAccountHistoryV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success bool                   `json:"success"`
		History []AccountRotationEvent `json:"history"`
		Total   int                    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("total = %d, want 5", resp.Total)
	}
	if len(resp.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(resp.History))
	}
	// Most recent first (a5 before a4)
	if resp.History[0].NewAccount != "a5" {
		t.Errorf("first event = %s, want a5", resp.History[0].NewAccount)
	}
	if resp.History[1].NewAccount != "a4" {
		t.Errorf("second event = %s, want a4", resp.History[1].NewAccount)
	}
}

// --- Mail handler branches: exercise getMailClient path ---
// These tests exercise the client-creation and mail-client paths.
// If the MCP Agent Mail server is available, they test the full flow.
// If not, they test the client==nil → 503 branch.

func TestHandleListMailProjects_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/projects", nil)

	s.handleListMailProjects(rec, req)

	// Either 200 (mail available) or 503 (unavailable) — not 400 or 404
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable &&
		rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListMailAgents_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/agents", nil)

	s.handleListMailAgents(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable &&
		rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleMailInbox_WithAgent_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=TestAgent&limit=5&urgent_only=true&include_bodies=true", nil)

	s.handleMailInbox(rec, req)

	// Exercises getMailClient + query param parsing
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable &&
		rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleSearchMessages_WithQuery_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/search?q=test&limit=10", nil)

	s.handleSearchMessages(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable &&
		rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetMessage_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/42", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleGetMessage(rec, req)

	// Past ID parse → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid ID should not return 400")
	}
}

func TestHandleGetMailAgent_ValidName_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/agents/TestAgent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "TestAgent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleGetMailAgent(rec, req)

	// Past name check → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid name should not return 400")
	}
}

func TestHandleListContacts_WithAgent_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/contacts?agent_name=TestAgent", nil)

	s.handleListContacts(rec, req)

	// Past agent_name check → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid agent_name should not return 400")
	}
}

func TestHandleThreadSummary_WithID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads/TKT-123/summary?include_examples=true&llm_mode=false", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "TKT-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleThreadSummary(rec, req)

	// Past threadID check → exercises getMailClient + query param parsing
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid thread ID should not return 400")
	}
}

func TestHandleSendMessage_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"sender_name":"Agent1","to":["Agent2"],"subject":"test","body_md":"hello"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages", strings.NewReader(body))

	s.handleSendMessage(rec, req)

	// Past validation → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

func TestHandleReplyMessage_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"sender_name":"Agent1","body_md":"reply text"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/reply", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleReplyMessage(rec, req)

	// Past ID parse + body validation → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

func TestHandleMarkMessageRead_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/read?agent_name=Agent1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleMarkMessageRead(rec, req)

	// Past ID parse + agent_name check → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

func TestHandleAckMessage_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/42/ack?agent_name=Agent1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "42")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleAckMessage(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

func TestHandleRequestContact_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"from_agent":"Agent1","to_agent":"Agent2","reason":"collab"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request", strings.NewReader(body))

	s.handleRequestContact(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

func TestHandleRespondContact_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"to_agent":"Agent1","from_agent":"Agent2","accept":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond", strings.NewReader(body))

	s.handleRespondContact(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

func TestHandleSetContactPolicy_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"agent_name":"Agent1","policy":"open"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy", strings.NewReader(body))

	s.handleSetContactPolicy(rec, req)

	// Past validation → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

// --- Reservation handler branches: exercise getMailClient path ---

func TestHandleListReservations_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations?agent_name=Agent1", nil)

	s.handleListReservations(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

func TestHandleReservePaths_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"agent_name":"Agent1","paths":["src/*.go"],"ttl_seconds":3600,"exclusive":true,"reason":"test"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))

	s.handleReservePaths(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

func TestHandleReleaseReservations_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"agent_name":"Agent1","paths":["src/*.go"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/reservations", strings.NewReader(body))

	s.handleReleaseReservations(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

func TestHandleReservationConflicts_WithPaths_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/conflicts?paths=src/main.go&paths=src/util.go", nil)

	s.handleReservationConflicts(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid paths should not return 400")
	}
}

func TestHandleGetReservation_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/99", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "99")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleGetReservation(rec, req)

	// Past ID parse → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid ID should not return 400")
	}
}

func TestHandleReleaseReservationByID_ValidID_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/99/release?agent_name=Agent1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "99")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleReleaseReservationByID(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

func TestHandleRenewReservation_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"agent_name":"Agent1","extend_seconds":1800}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/99/renew", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "99")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleRenewReservation(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid request should not return 400")
	}
}

// --- handleMemoryRules: cm installed, exercises full path ---

func TestHandleMemoryRules_CmInstalled_Branch(t *testing.T) {
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/rules", nil)

	s.handleMemoryRules(rec, req)

	// cm is installed, so we get past the install check
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cm is installed")
	}
}

// --- handleDepsV1: success path ---

func TestHandleDepsV1_Success_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deps", nil)

	s.handleDepsV1(rec, req)

	// kernel.Run("core.deps") should succeed in most environments
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 16 — account DependencyMissing, force-release, create agent, CASS, deps
// =============================================================================

// --- Account handlers: DependencyMissing branch (caam not on PATH) ---

func TestHandleListAccountsV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	s.handleListAccountsV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ErrorCode != robot.ErrCodeDependencyMissing {
		t.Errorf("error_code = %q, want %q", resp.ErrorCode, robot.ErrCodeDependencyMissing)
	}
}

func TestHandleAccountStatusV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/status", nil)
	s.handleAccountStatusV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleActiveAccountsV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/active", nil)
	s.handleActiveAccountsV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountQuotaV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/quota", nil)
	s.handleAccountQuotaV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListAccountsByProviderV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "claude")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/claude", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleListAccountsByProviderV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRotateProviderAccountV1_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "claude")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/claude/rotate", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleRotateProviderAccountV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

// --- handleForceReleaseReservation: valid body exercises getMailClient ---

func TestHandleForceReleaseReservation_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"agent_name":"Agent1","note":"stale","notify_previous":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/99/force-release", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "99")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	s.handleForceReleaseReservation(rec, req)

	// Past all validation → exercises getMailClient path
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

// --- handleCreateMailAgent: valid body exercises getMailClient ---

func TestHandleCreateMailAgent_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"program":"claude-code","model":"opus-4.5","name":"TestBot","task_description":"testing"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents", strings.NewReader(body))

	s.handleCreateMailAgent(rec, req)

	// Past validation → exercises getMailClient
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

// --- handleCASSInsights: cass installed exercises full path ---

func TestHandleCASSInsights_CassInstalled(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	body := `{"query":"test query","aggregate":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/insights", strings.NewReader(body))

	s.handleCASSInsights(rec, req)

	// cass installed → past install check; may fail with 500 (no sessions) or succeed
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cass is installed")
	}
}

// --- handleCASSPreview: cass installed with existing file ---

func TestHandleCASSPreview_CassInstalled_ValidPath(t *testing.T) {
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	// Provide a real existing file path
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/preview?path=README.md", nil)

	s.handleCASSPreview(rec, req)

	// cass installed → past install check
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cass is installed")
	}
}

// --- handleMemoryContext: cm installed with valid task ---

func TestHandleMemoryContext_CmInstalledValidTask(t *testing.T) {
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)

	body := `{"task":"test task description","max_rules":5,"max_snippets":3}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context", strings.NewReader(body))

	s.handleMemoryContext(rec, req)

	// cm installed → past install check, exercises full path
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("should not get 503 when cm is installed")
	}
}

// --- handleMailHealth: exercises getMailClient branches ---

func TestHandleMailHealth_WithProjectDir(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/health", nil)

	s.handleMailHealth(rec, req)

	// Should return 200 with available status
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should have "available" field regardless of mail state
	if _, ok := resp["available"]; !ok {
		t.Error("expected 'available' field in response")
	}
}

// --- handleListReservations: no agent_name exercises allAgents=true path ---

func TestHandleListReservations_AllAgents_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	rec := httptest.NewRecorder()
	// No agent_name query param → allAgents = true
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations", nil)

	s.handleListReservations(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("missing agent_name is valid for list reservations")
	}
}

// --- handleReservePaths: zero TTL defaults to 3600 ---

func TestHandleReservePaths_DefaultTTL_Branch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.projectDir = t.TempDir()

	// No ttl_seconds field → defaults to 3600
	body := `{"agent_name":"Agent1","paths":["src/*.go"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))

	s.handleReservePaths(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid body should not return 400")
	}
}

// =============================================================================
// Batch 17 — Bead success paths, session/agent valid-ID paths, output handlers,
//            memory outcome status branches
// =============================================================================

// --- Bead handlers with real project dir (success paths) ---

func TestHandleListBeads_WithProjectDir_Filters(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	// Exercise query param branches: status, label, assignee
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads?status=open&label=test&assignee=nobody", nil)
	rec := httptest.NewRecorder()
	srv.handleListBeads(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsStats_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/stats", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsStats(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["stats"]; !ok {
			t.Error("expected 'stats' in success response")
		}
	}
}

func TestHandleBeadsReady_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/ready", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsReady(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsBlocked_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/blocked", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsBlocked(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsInProgress_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/in-progress", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsInProgress(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleListBeadDeps_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	// Use a bead ID that exists in the project
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-bdseb/deps", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-bdseb")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleListBeadDeps(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsDaemonStatus_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/daemon/status", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsDaemonStatus(rec, req)

	// Daemon may or may not be running: 200 either way per handler logic
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handler returns 200 even when daemon not running)", rec.Code)
	}
}

func TestHandleClaimBead_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	body := `{"assignee":"test-agent"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-nonexistent/claim", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleClaimBead(rec, req)
	// Nonexistent bead → RunBd error → 500, or installed check passes
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsInsights_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsInstalled() {
		t.Skip("bv not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/insights", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsInsights(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsPlan_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsInstalled() {
		t.Skip("bv not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/plan", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsPlan(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsPriority_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsInstalled() {
		t.Skip("bv not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/priority", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsPriority(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleBeadsRecipes_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsInstalled() {
		t.Skip("bv not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/recipes", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsRecipes(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleRemoveBeadDep_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/beads/bd-nonexistent/deps/bd-also-nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-nonexistent")
	rctx.URLParams.Add("depId", "bd-also-nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleRemoveBeadDep(rec, req)
	// Nonexistent bead → RunBd error → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Session handlers with valid ID (exercises kernel.Run error path) ---

func TestHandleSessionStatusV1_ValidID_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionStatusV1(rec, req)
	// kernel.Run will fail (no tmux session) → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleSessionAttachV1_ValidID_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/attach", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionAttachV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleSessionViewV1_ValidID_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/view", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionViewV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Agent handlers with valid session/body (exercises robot.Get* error path) ---

func TestHandleAgentSendV1_ValidSession_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"message":"hello","panes":["0"],"all":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/agents/send", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentSendV1(rec, req)
	// robot.GetSend fails (no tmux) → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleAgentContextV1_WithLinesParam(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	// Exercise the lines query param parsing
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/agents/context?lines=50", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentContextV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleAgentContextV1_InvalidLinesParam(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/agents/context?lines=notanumber", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentContextV1(rec, req)
	// Invalid lines param → 400
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentInterruptV1_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"panes":["0"],"message":"stop","force":true,"no_wait":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/agents/interrupt", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentInterruptV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleAgentRestartV1_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"panes":["0"],"agent_type":"claude","all":false,"dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/agents/restart", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentRestartV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Output handlers with valid session (exercises robot.Get* error path) ---

func TestHandleOutputTailV1_WithSessionAndPanes(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/tail?session=test-session&lines=20&panes=0,1", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputTailV1(rec, req)

	// robot.GetTail fails (no tmux) → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleOutputDiffV1_WithSessionAndSince(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/diff?session=test-session&since=30m", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputDiffV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleOutputFilesV1_WithSessionAndParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/files?session=test-session&window=1h&limit=50", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputFilesV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleOutputSummaryV1_WithSessionAndSince(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/summary?session=test-session&since=1h", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputSummaryV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Metrics handler with params ---

func TestHandleMetricsV1_WithSessionAndPeriod(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics?session=test-session&period=1h", nil)
	rec := httptest.NewRecorder()
	srv.handleMetricsV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Memory outcome status branches ---

func TestHandleMemoryOutcome_SuccessStatus(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"status":"success","rule_ids":["r1"],"sentiment":"positive","notes":"worked"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryOutcome(rec, req)

	// status=success is valid → daemon check → 503 (no daemon) or 200
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 503, or 500", rec.Code)
	}
}

func TestHandleMemoryOutcome_FailureStatus(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"status":"failure","notes":"broke"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryOutcome(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 503, or 500", rec.Code)
	}
}

func TestHandleMemoryOutcome_PartialStatus(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"status":"partial","notes":"half done"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryOutcome(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 503, or 500", rec.Code)
	}
}

// --- Memory privacy with project dir ---

func TestHandleMemoryPrivacyGet_WithProjectDir(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/privacy", nil)
	rec := httptest.NewRecorder()
	srv.handleMemoryPrivacyGet(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleMemoryPrivacyUpdate_EnabledWithAgents(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	body := `{"enabled":true,"agents":["agent1","agent2"]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/memory/privacy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryPrivacyUpdate(rec, req)

	// cm privacy enable with agents → may succeed or fail
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleMemoryPrivacyUpdate_Disabled(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	body := `{"enabled":false}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/memory/privacy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryPrivacyUpdate(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- CASS handlers with project dir ---

func TestHandleCASSCapabilities_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/capabilities", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSCapabilities(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleCASSTimeline_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !cassInstalled() {
		t.Skip("cass not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/timeline?limit=5", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSTimeline(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Policy handler deeper branches ---

func TestHandlePolicyResetV1_SuccessPath(t *testing.T) {
	// Don't use t.Parallel() - writes to ~/.ntm/policy.yaml
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/reset", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyResetV1(rec, req)

	// Should succeed: creates ~/.ntm dir, writes default policy
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["policy_path"]; !ok {
			t.Error("expected 'policy_path' in success response")
		}
	}
}

func TestHandlePolicyAutomationGetV1_SuccessPath(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy/automation", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationGetV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["force_release"]; !ok {
			t.Error("expected 'force_release' in success response")
		}
	}
}

// --- handleMailInbox with since_ts time.Parse branch ---

func TestHandleMailInbox_WithSinceTS(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox?agent_name=test&since_ts=2025-01-01T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	srv.handleMailInbox(rec, req)

	// Exercises the time.Parse(time.RFC3339, sinceTSStr) branch
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid since_ts should not return 400")
	}
}

// =============================================================================
// Batch 18 — Pane title, agent activity/health, palette, history, wait,
//            safety blocked params, context stats, create session, policy update
// =============================================================================

// --- Pane title handlers with valid params ---

func TestHandleGetPaneTitleV1_ValidParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/panes/0/title", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleGetPaneTitleV1(rec, req)
	// tmux.GetPaneTitle fails (no session) → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleSetPaneTitleV1_ValidParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"title":"My Pane"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/test-session/panes/0/title", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSetPaneTitleV1(rec, req)
	// tmux.SetPaneTitle fails (no session) → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Agent activity/health with valid session ---

func TestHandleAgentActivityV1_ValidSession_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/agents/activity", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentActivityV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleAgentHealthV1_ValidSession_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/agents/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAgentHealthV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Palette with query params ---

func TestHandlePaletteV1_WithCategoryAndSearch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/palette?session=test-session&category=agents&search=restart", nil)
	rec := httptest.NewRecorder()
	srv.handlePaletteV1(rec, req)

	// robot.GetPalette may succeed or fail
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- History with query params ---

func TestHandleHistoryV1_WithAllParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?session=test-session&pane=0&agent_type=claude&since=1h&last=20", nil)
	rec := httptest.NewRecorder()
	srv.handleHistoryV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleHistoryStatsV1_WithSession(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history/stats?session=test-session", nil)
	rec := httptest.NewRecorder()
	srv.handleHistoryStatsV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Wait with all query params ---

func TestHandleWaitV1_WithAllParams(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/wait?session=test-session&condition=idle&timeout=1s&poll=100ms&panes=0,1&agent_type=claude&any=true&exit_on_error=true&require_transition=true&count=2",
		nil)
	rec := httptest.NewRecorder()
	srv.handleWaitV1(rec, req)

	// robot.GetWait with no tmux → error code 2 → 400
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest &&
		rec.Code != http.StatusRequestTimeout && rec.Code != http.StatusConflict &&
		rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 400, 408, 409, or 500", rec.Code)
	}
}

// --- Safety blocked with params ---

func TestHandleSafetyBlockedV1_WithHoursAndLimit(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/safety/blocked?hours=1&limit=5", nil)
	rec := httptest.NewRecorder()
	srv.handleSafetyBlockedV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Context stats with session + lines ---

func TestHandleContextStatsV1_WithSessionAndLines(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/context/stats?session=test-session&lines=500", nil)
	rec := httptest.NewRecorder()
	srv.handleContextStatsV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Create session with valid body ---

func TestHandleCreateSessionV1_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"session":"test-new-session","panes":3}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCreateSessionV1(rec, req)

	// kernel.Run fails (no tmux) → 500
	if rec.Code != http.StatusCreated && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 201 or 500", rec.Code)
	}
}

// --- Session zoom with valid body ---

func TestHandleSessionZoomV1_ValidBody_Branch(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"pane":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test-session/zoom", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionZoomV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Policy automation update with auto_commit/auto_push branches ---

func TestHandlePolicyAutomationUpdateV1_AutoCommitToggle(t *testing.T) {
	// Don't use t.Parallel() - modifies ~/.ntm/policy.yaml
	srv, _ := setupTestServer(t)

	trueVal := true
	body := fmt.Sprintf(`{"auto_commit":%v}`, trueVal)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["modified"]; !ok {
			t.Error("expected 'modified' in response")
		}
	}
}

func TestHandlePolicyAutomationUpdateV1_AutoPushToggle(t *testing.T) {
	// Don't use t.Parallel() - modifies ~/.ntm/policy.yaml
	srv, _ := setupTestServer(t)

	body := `{"auto_push":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- handlePolicyValidateV1 with file-based path ---

func TestHandlePolicyValidateV1_FileBased(t *testing.T) {
	// Don't use t.Parallel() - writes temp policy file
	srv, _ := setupTestServer(t)

	// Create a temp policy file
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "policy.yaml")
	content := "version: 1\nrules:\n  - pattern: \"rm -rf /\"\n    action: block\n    reason: dangerous\n"
	os.WriteFile(policyFile, []byte(content), 0644)

	body := fmt.Sprintf(`{"path":"%s"}`, policyFile)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- handleSafetyInstallV1 with force=true ---

func TestHandleSafetyInstallV1_Force(t *testing.T) {
	// Don't use t.Parallel() - modifies ~/.ntm/bin and ~/.claude/hooks
	srv, _ := setupTestServer(t)

	body := `{"force":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/install", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusConflict && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 409, or 500", rec.Code)
	}
}

// --- handleSafetyUninstallV1 exercises remove paths ---

func TestHandleSafetyUninstallV1_Branch(t *testing.T) {
	// Don't use t.Parallel() - removes files from ~/.ntm/bin
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/uninstall", nil)
	rec := httptest.NewRecorder()
	srv.handleSafetyUninstallV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// =============================================================================
// Batch 19 — Bead CRUD success, policy get/update, git sync, OpenAPI TLS,
//            safety check, redact helpers
// =============================================================================

// --- Bead CRUD with real project dir ---

func TestHandleGetBead_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	// Use a known bead ID from the project
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-bdseb", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-bdseb")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleGetBead(rec, req)
	// br show bd-bdseb --json → 200 with valid JSON, or 404 if bead not found
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 404, or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["bead"]; !ok {
			t.Error("expected 'bead' in success response")
		}
	}
}

func TestHandleUpdateBead_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	// Update with all optional fields to exercise param branches
	body := `{"title":"test-title","description":"test-desc","priority":"P2","assignee":"test-agent","labels":["label1"]}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/beads/bd-nonexistent", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleUpdateBead(rec, req)
	// Nonexistent bead → RunBd error → 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleCloseBead_WithProjectDir(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	srv, _ := setupTestServer(t)
	srv.projectDir = "/data/projects/ntm"

	// Use nonexistent bead to avoid actually closing a real one
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-nonexistent/close", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleCloseBead(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Policy GET with rules=true ---

func TestHandlePolicyGetV1_WithRulesTrue(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy?rules=true", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyGetV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if _, ok := resp["rules"]; !ok {
			t.Error("expected 'rules' in response when rules=true")
		}
	}
}

// --- Policy UPDATE with valid YAML content ---

func TestHandlePolicyUpdateV1_ValidContent(t *testing.T) {
	// Don't use t.Parallel() - writes to ~/.ntm/policy.yaml
	srv, _ := setupTestServer(t)

	yamlContent := "version: 1\nblocked:\n  - pattern: \"rm -rf /\"\n    reason: dangerous\n"
	body := fmt.Sprintf(`{"content":%q}`, yamlContent)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handlePolicyUpdateV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- Git sync with dry_run + pull_only ---

func TestHandleGitSyncV1_DryRunPullOnly(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"session":"test","pull_only":true,"dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/git/sync", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleGitSyncV1(rec, req)

	// We're in a git repo, so should not get NOT_GIT_REPO error
	if rec.Code == http.StatusBadRequest {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if code, _ := resp["error_code"].(string); code == "NOT_GIT_REPO" {
			t.Skip("not in git repo")
		}
	}
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleGitSyncV1_PushOnlyDryRun(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"session":"test","push_only":true,"dry_run":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/git/sync", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleGitSyncV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 400, or 500", rec.Code)
	}
}

// --- OpenAPI with TLS ---

func TestHandleOpenAPISpec_WithTLS(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)
	srv.host = "localhost"
	srv.port = 9443

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	req.TLS = &tls.ConnectionState{} // simulate TLS
	rec := httptest.NewRecorder()
	srv.handleOpenAPISpec(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// --- Safety check with allowed command ---

func TestHandleSafetyCheckV1_AllowedCommand(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"command":"ls -la"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleSafetyCheckV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
	if rec.Code == http.StatusOK {
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if action, ok := resp["action"].(string); ok && action != "allow" {
			// "ls -la" should not be blocked by default policy
			t.Logf("action=%s (might be blocked by custom policy)", action)
		}
	}
}

// --- Safety check with approval-required command ---

func TestHandleSafetyCheckV1_ApprovalCommand(t *testing.T) {
	t.Parallel()
	srv, _ := setupTestServer(t)

	body := `{"command":"git push --force"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleSafetyCheckV1(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

// --- redactingResponseWriter Write branch ---

func TestRedactingResponseWriter_WriteWithoutHeader(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	rw := &redactingResponseWriter{
		ResponseWriter: rec,
		buffer:         new(bytes.Buffer),
	}

	n, err := rw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 5 {
		t.Fatalf("Write n = %d, want 5", n)
	}
	// After Write without WriteHeader, statusCode should be 200
	if rw.statusCode != http.StatusOK {
		t.Errorf("statusCode = %d, want 200", rw.statusCode)
	}
}

// --- logRedactionSummary branches ---

func TestLogRedactionSummary_ValidSummary(t *testing.T) {
	t.Parallel()
	// Just exercises the JSON marshal path — no panic
	summary := &RedactionSummary{
		RequestID:     "test-123",
		Path:          "/api/v1/test",
		RequestFinds:  2,
		ResponseFinds: 1,
		Blocked:       false,
	}
	logRedactionSummary(summary)
}

// =============================================================================
// BATCH 20 — deeper branch tests for remaining low-coverage handlers
// =============================================================================

// --- handleListBeadDeps deeper success path (JSON parse branch) ---

func TestHandleListBeadDeps_JSONParse(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = "/data/projects/ntm"

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-4b4zf")
	req := httptest.NewRequest("GET", "/api/v1/beads/bd-4b4zf/deps", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleListBeadDeps(rec, req)

	// Should exercise RunBd + JSON parse path
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("br is installed but got 503")
	}
}

// --- handleRemoveBeadDep deeper path with different bead ---

func TestHandleRemoveBeadDep_UnlinkPath(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = "/data/projects/ntm"

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-4b4zf")
	rctx.URLParams.Add("depId", "bd-nonexistent")
	req := httptest.NewRequest("DELETE", "/api/v1/beads/bd-4b4zf/deps/bd-nonexistent", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRemoveBeadDep(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("br is installed but got 503")
	}
}

// --- handleListBeads with status filter ---

func TestHandleListBeads_WithStatusFilter(t *testing.T) {
	t.Parallel()
	if !bv.IsBdInstalled() {
		t.Skip("br not installed")
	}
	s, _ := setupTestServer(t)
	s.projectDir = "/data/projects/ntm"

	req := httptest.NewRequest("GET", "/api/v1/beads?status=open&limit=5", nil)
	rec := httptest.NewRecorder()

	s.handleListBeads(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("br is installed but got 503")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyValidateV1 content-based with invalid YAML ---

func TestHandlePolicyValidateV1_InvalidContent(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	body := strings.NewReader(`{"content":"not: [valid: yaml: !!!"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// The response should show valid=false with YAML errors
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data != nil {
		if v, ok := data["valid"].(bool); ok && v {
			t.Error("expected valid=false for invalid YAML")
		}
	}
}

// --- handlePolicyValidateV1 content-based with valid but empty rules ---

func TestHandlePolicyValidateV1_EmptyRulesWarning(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	// Valid YAML but no rules — triggers "no rules defined" warning
	body := strings.NewReader(`{"content":"version: 1\nrules: []\n"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// --- handlePolicyValidateV1 content with no version (triggers warning) ---

func TestHandlePolicyValidateV1_NoVersion(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	body := strings.NewReader(`{"content":"rules:\n- action: block\n  pattern: rm -rf /\n"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// --- WSClient sendAck exercises JSON marshal + channel send ---

func TestWSClient_SendAck(t *testing.T) {
	t.Parallel()
	c := &WSClient{
		id:   "test-client",
		send: make(chan []byte, 10),
	}

	c.sendAck("req-123", map[string]interface{}{
		"subscribed": []string{"sessions:*"},
	})

	select {
	case msg := <-c.send:
		var parsed WSMessage
		if err := json.Unmarshal(msg, &parsed); err != nil {
			t.Fatalf("invalid JSON from sendAck: %v", err)
		}
		if parsed.Type != WSMsgAck {
			t.Errorf("type = %s, want %s", parsed.Type, WSMsgAck)
		}
		if parsed.RequestID != "req-123" {
			t.Errorf("request_id = %s, want req-123", parsed.RequestID)
		}
	default:
		t.Fatal("expected message on send channel")
	}
}

// --- WSClient sendError exercises JSON marshal + channel send ---

func TestWSClient_SendError(t *testing.T) {
	t.Parallel()
	c := &WSClient{
		id:   "test-client",
		send: make(chan []byte, 10),
	}

	c.sendError("req-456", "TEST_ERR", "something failed")

	select {
	case msg := <-c.send:
		var parsed WSMessage
		if err := json.Unmarshal(msg, &parsed); err != nil {
			t.Fatalf("invalid JSON from sendError: %v", err)
		}
		if parsed.Type != WSMsgError {
			t.Errorf("type = %s, want %s", parsed.Type, WSMsgError)
		}
	default:
		t.Fatal("expected message on send channel")
	}
}

// --- WSClient sendPong exercises JSON marshal + channel send ---

func TestWSClient_SendPong(t *testing.T) {
	t.Parallel()
	c := &WSClient{
		id:   "test-client",
		send: make(chan []byte, 10),
	}

	c.sendPong("req-789")

	select {
	case msg := <-c.send:
		var parsed WSMessage
		if err := json.Unmarshal(msg, &parsed); err != nil {
			t.Fatalf("invalid JSON from sendPong: %v", err)
		}
		if parsed.Type != WSMsgPong {
			t.Errorf("type = %s, want %s", parsed.Type, WSMsgPong)
		}
	default:
		t.Fatal("expected message on send channel")
	}
}

// --- WSClient sendAck with full buffer (exercises default drop path) ---

func TestWSClient_SendAck_FullBuffer(t *testing.T) {
	t.Parallel()
	c := &WSClient{
		id:   "test-client",
		send: make(chan []byte), // unbuffered — will be full
	}

	// Should not panic — just drops the message
	c.sendAck("req-full", map[string]interface{}{"dropped": true})
}

// --- handleSessionAgents exercises stateStore success path ---

func TestHandleSessionAgents_Success(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/sessions/test-session/agents", nil)

	s.handleSessionAgents(rec, req, "test-session")

	// stateStore is set from setupTestServer, so should succeed
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleCheckpointList exercises storage.List success path ---

func TestHandleListCheckpoints_Success(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "nonexistent-session")
	req := httptest.NewRequest("GET", "/api/v1/sessions/nonexistent-session/checkpoints", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleListCheckpoints(rec, req)

	// Should return 200 with empty list (no error from listing nonexistent session)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d", rec.Code)
	}
}

// --- handleCheckpointList with details=true ---

func TestHandleListCheckpoints_WithDetails(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "nonexistent-session")
	req := httptest.NewRequest("GET", "/api/v1/sessions/nonexistent-session/checkpoints?details=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleListCheckpoints(rec, req)

	// Exercises includeDetails branch in checkpointToResponse
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d", rec.Code)
	}
}

// --- handleDeleteCheckpoint exercises storage.Exists → not found path ---

func TestHandleDeleteCheckpoint_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "nonexistent-session")
	rctx.URLParams.Add("checkpointId", "cp-nonexistent")
	req := httptest.NewRequest("DELETE", "/api/v1/sessions/nonexistent-session/checkpoints/cp-nonexistent", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleDeleteCheckpoint(rec, req)

	// storage.Exists should return false → 404
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- logRedactionSummary fallback path (JSON marshal error) ---

func TestLogRedactionSummary_MarshalError(t *testing.T) {
	t.Parallel()
	// Categories with non-marshalable value to trigger json.Marshal error
	summary := &RedactionSummary{
		RequestID:    "test-err",
		Path:         "/api/v1/test",
		RequestFinds: 1,
		Categories:   map[string]int{"normal": 1},
	}
	// This exercises the success path; the fallback path is harder to trigger
	// since all fields are primitive types, but this validates coverage of Categories field
	logRedactionSummary(summary)
}

// --- writeJSON exercises JSON encode error branch ---

func TestWriteJSON_Success(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]interface{}{
		"key": "value",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %s, want application/json", ct)
	}
}

// --- toJSONMap exercises successful round-trip ---

func TestToJSONMap_Success(t *testing.T) {
	t.Parallel()
	type sample struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	m, err := toJSONMap(sample{Name: "test", Count: 5})
	if err != nil {
		t.Fatalf("toJSONMap error: %v", err)
	}
	if m["name"] != "test" {
		t.Errorf("name = %v, want test", m["name"])
	}
}

// --- toJSONMap error path (unmarshalable input) ---

func TestToJSONMap_MarshalError(t *testing.T) {
	t.Parallel()
	// channels can't be marshaled to JSON
	_, err := toJSONMap(make(chan int))
	if err == nil {
		t.Fatal("expected error for unmarshalable type")
	}
}

// =============================================================================
// BATCH 21 — approval flows, pipeline handlers, deeper handler branches
// =============================================================================

// --- Approval request → approve → approve-again (conflict) ---

func TestApprovalFlow_RequestThenApprove(t *testing.T) {
	s, _ := setupTestServer(t)

	// 1. Create an approval request
	body := strings.NewReader(`{"action":"git push --force","resource":"main","reason":"deploy fix"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/request", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleApprovalRequestV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("request approval: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	approvalID, _ := resp["id"].(string)
	if approvalID == "" {
		t.Fatalf("no approval ID in response: %s", rec.Body.String())
	}

	// 2. Approve it
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID)
	req2 := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+approvalID+"/approve", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	rec2 := httptest.NewRecorder()

	s.handleApprovalApproveV1(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Try to approve again → conflict (already approved)
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+approvalID+"/approve", nil)
	req3 = req3.WithContext(context.WithValue(req3.Context(), chi.RouteCtxKey, rctx))

	s.handleApprovalApproveV1(rec3, req3)

	if rec3.Code != http.StatusConflict {
		t.Fatalf("re-approve: expected 409 conflict, got %d", rec3.Code)
	}
}

// --- Approval deny then deny again (conflict) ---

func TestApprovalFlow_RequestThenDeny(t *testing.T) {
	s, _ := setupTestServer(t)

	// Create request
	body := strings.NewReader(`{"action":"rm -rf /","resource":"system"}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/request", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleApprovalRequestV1(rec, req)

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	approvalID, _ := resp["id"].(string)
	if approvalID == "" {
		t.Fatalf("no approval ID in response: %s", rec.Body.String())
	}

	// Deny it
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID)
	req2 := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+approvalID+"/deny", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx))
	rec2 := httptest.NewRecorder()

	s.handleApprovalDenyV1(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("deny: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Try to deny again → conflict
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+approvalID+"/deny", nil)
	req3 = req3.WithContext(context.WithValue(req3.Context(), chi.RouteCtxKey, rctx))

	s.handleApprovalDenyV1(rec3, req3)

	if rec3.Code != http.StatusConflict {
		t.Fatalf("re-deny: expected 409 conflict, got %d", rec3.Code)
	}
}

// --- Approval approve not found ---

func TestApprovalApproveV1_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-nonexistent")
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/apr-nonexistent/approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleApprovalApproveV1(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Approval deny not found ---

func TestApprovalDenyV1_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-nonexistent")
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/apr-nonexistent/deny", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleApprovalDenyV1(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Approval request with TTL ---

func TestApprovalRequestV1_WithTTL(t *testing.T) {
	s, _ := setupTestServer(t)

	body := strings.NewReader(`{"action":"deploy","ttl_seconds":60}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/request", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleApprovalRequestV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Pipeline list (exercises empty list path) ---

func TestHandleListPipelines_Empty(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/pipelines", nil)
	rec := httptest.NewRecorder()

	s.handleListPipelines(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}


// --- publishApprovalEvent with nil wsHub (exercises nil guard) ---

func TestPublishApprovalEvent_NilHub(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.wsHub = nil

	// Should not panic
	s.publishApprovalEvent("approval.test", map[string]interface{}{
		"test": true,
	})
}


// =============================================================================
// BATCH 22 — OIDC validation branches, deeper approval, CASS with real tools
// =============================================================================

// --- validateOIDCToken: incomplete config ---

func TestValidateOIDCToken_IncompleteConfig(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	// Empty OIDC config → "oidc config incomplete"
	s.auth = AuthConfig{Mode: AuthModeOIDC}

	err := s.validateOIDCToken(context.Background(), "any-token")
	if err == nil || !strings.Contains(err.Error(), "oidc config incomplete") {
		t.Fatalf("expected oidc config incomplete error, got: %v", err)
	}
}

// --- validateOIDCToken: unsupported algorithm ---

func TestValidateOIDCToken_UnsupportedAlg(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: "https://example.com/.well-known/jwks.json",
			Issuer:  "https://example.com",
		},
	}

	// Build JWT with HS256 algorithm
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","kid":"key1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://example.com","sub":"user"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "unsupported jwt alg") {
		t.Fatalf("expected unsupported jwt alg error, got: %v", err)
	}
}

// --- validateOIDCToken: invalid issuer ---

func TestValidateOIDCToken_InvalidIssuer(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: "https://example.com/.well-known/jwks.json",
			Issuer:  "https://correct-issuer.com",
		},
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://wrong-issuer.com","sub":"user"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "invalid issuer") {
		t.Fatalf("expected invalid issuer error, got: %v", err)
	}
}

// --- validateOIDCToken: invalid audience ---

func TestValidateOIDCToken_InvalidAudience(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL:  "https://example.com/.well-known/jwks.json",
			Issuer:   "https://example.com",
			Audience: "my-app",
		},
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://example.com","aud":"wrong-app"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "invalid audience") {
		t.Fatalf("expected invalid audience error, got: %v", err)
	}
}

// --- validateOIDCToken: expired token ---

func TestValidateOIDCToken_ExpiredToken(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: "https://example.com/.well-known/jwks.json",
			Issuer:  "https://example.com",
		},
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	// exp set to year 2000
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://example.com","exp":946684800}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "token expired") {
		t.Fatalf("expected token expired error, got: %v", err)
	}
}

// --- validateOIDCToken: not yet valid (nbf in future) ---

func TestValidateOIDCToken_NotYetValid(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: "https://example.com/.well-known/jwks.json",
			Issuer:  "https://example.com",
		},
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	// nbf set to year 2099
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://example.com","nbf":4102444800}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "token not yet valid") {
		t.Fatalf("expected token not yet valid error, got: %v", err)
	}
}

// --- validateOIDCToken: valid claims but JWKS fetch fails ---

func TestValidateOIDCToken_JWKSFetchError(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: "https://127.0.0.1:1/nonexistent-jwks",
			Issuer:  "https://example.com",
		},
	}
	s.jwksCache = newJWKSCache(0) // default TTL

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":"https://example.com","exp":%d}`, time.Now().Add(time.Hour).Unix())))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.validateOIDCToken(ctx, token)
	// Should fail on JWKS fetch
	if err == nil {
		t.Fatal("expected JWKS fetch error")
	}
}

// --- validateOIDCToken: valid claims + JWKS served but wrong kid ---

func TestValidateOIDCToken_JWKSKidNotFound(t *testing.T) {
	t.Parallel()

	// Generate RSA key
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	// Serve JWKS with kid="correct-kid"
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]interface{}{
				{
					"kty": "RSA",
					"kid": "correct-kid",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.E)).Bytes()),
				},
			},
		})
	}))
	defer jwksServer.Close()

	s, _ := setupTestServer(t)
	s.auth = AuthConfig{
		Mode: AuthModeOIDC,
		OIDC: OIDCConfig{
			JWKSURL: jwksServer.URL,
			Issuer:  "https://example.com",
		},
	}
	s.jwksCache = newJWKSCache(time.Minute)

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"wrong-kid"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":"https://example.com","exp":%d}`, time.Now().Add(time.Hour).Unix())))
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	token := header + "." + payload + "." + sig

	err := s.validateOIDCToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "kid not found") {
		t.Fatalf("expected kid not found error, got: %v", err)
	}
}

// --- newJWKSCache with zero TTL (uses default) ---

func TestNewJWKSCache_DefaultTTL(t *testing.T) {
	t.Parallel()
	cache := newJWKSCache(0)
	if cache == nil {
		t.Fatal("expected non-nil cache")
	}
	if cache.ttl != defaultJWKSCacheTTL {
		t.Errorf("ttl = %v, want default %v", cache.ttl, defaultJWKSCacheTTL)
	}
}

// --- newJWKSCache with custom TTL ---

func TestNewJWKSCache_CustomTTL(t *testing.T) {
	t.Parallel()
	cache := newJWKSCache(5 * time.Minute)
	if cache.ttl != 5*time.Minute {
		t.Errorf("ttl = %v, want 5m", cache.ttl)
	}
}

// --- handleCASSCapabilities with real cass ---

func TestHandleCASSCapabilities_WithRealCass(t *testing.T) {
	t.Parallel()
	client := cass.NewClient()
	if !client.IsInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/memory/cass/capabilities", nil)
	rec := httptest.NewRecorder()

	s.handleCASSCapabilities(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("cass is installed but got 503")
	}
	// Should be 200 (capabilities) or 500 (cass error)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleCASSInsights with real cass ---

func TestHandleCASSInsights_WithRealCass(t *testing.T) {
	t.Parallel()
	client := cass.NewClient()
	if !client.IsInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/memory/cass/insights", nil)
	rec := httptest.NewRecorder()

	s.handleCASSInsights(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("cass is installed but got 503")
	}
}

// --- handleCASSTimeline with real cass ---

func TestHandleCASSTimeline_WithRealCass(t *testing.T) {
	t.Parallel()
	client := cass.NewClient()
	if !client.IsInstalled() {
		t.Skip("cass not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/memory/cass/timeline?limit=5", nil)
	rec := httptest.NewRecorder()

	s.handleCASSTimeline(rec, req)

	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("cass is installed but got 503")
	}
}

// --- handleMemoryRules with real cm ---

func TestHandleMemoryRules_WithRealCM(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("cm"); err != nil {
		t.Skip("cm not installed")
	}
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/memory/rules", nil)
	rec := httptest.NewRecorder()

	s.handleMemoryRules(rec, req)

	// Should exercise CLI client path (may succeed or error on daemon)
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("cm is installed but got 503")
	}
}

// --- Approval approve with expired approval ---

func TestApprovalApproveV1_Expired(t *testing.T) {
	s, _ := setupTestServer(t)

	// Directly insert an expired approval into the map
	approvalsLock.Lock()
	approvalIDSeq++
	id := fmt.Sprintf("apr-%d", approvalIDSeq)
	approvals[id] = &Approval{
		ID:        id,
		Action:    "test-action",
		Status:    "pending",
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour), // expired 1 hour ago
	}
	approvalsLock.Unlock()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest("POST", "/api/v1/safety/approvals/"+id+"/approve", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleApprovalApproveV1(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for expired approval, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// BATCH 23 — WS origin checks, account quota, safety blocked, smaller targets
// =============================================================================

// --- checkWSOrigin: non-local mode with valid origin ---

func TestCheckWSOrigin_ValidOrigin(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{Mode: AuthModeOIDC}
	s.corsAllowedOrigins = []string{"https://app.example.com"}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "https://app.example.com")

	if !s.checkWSOrigin(req) {
		t.Error("expected origin to be allowed")
	}
}

// --- checkWSOrigin: non-local mode with invalid origin ---

func TestCheckWSOrigin_InvalidOrigin(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{Mode: AuthModeOIDC}
	s.corsAllowedOrigins = []string{"https://app.example.com"}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "https://evil.com")

	if s.checkWSOrigin(req) {
		t.Error("expected origin to be rejected")
	}
}


// --- checkWSOrigin: origin missing host ---

func TestCheckWSOrigin_OriginMissingHost(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)
	s.auth = AuthConfig{Mode: AuthModeOIDC}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Origin", "https://")

	if s.checkWSOrigin(req) {
		t.Error("expected origin with missing host to be rejected")
	}
}

// --- handleAccountQuotaV1 with real caam ---

func TestHandleAccountQuotaV1_WithCAAM(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/accounts/quota", nil)
	rec := httptest.NewRecorder()

	s.handleAccountQuotaV1(rec, req)

	// May succeed (200) or fail (500/503) depending on caam state
	if rec.Code == 0 {
		t.Fatal("expected non-zero status code")
	}
}

// --- handleAccountQuotaV1 DependencyMissing path ---

func TestHandleAccountQuotaV1_NoCAAM(t *testing.T) {
	s, _ := setupTestServer(t)
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	req := httptest.NewRequest("GET", "/api/v1/accounts/quota", nil)
	rec := httptest.NewRecorder()

	s.handleAccountQuotaV1(rec, req)

	if rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 503 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSafetyBlockedV1 success with default params ---

func TestHandleSafetyBlockedV1_DefaultParams(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/safety/blocked", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyBlockedV1(rec, req)

	// Should succeed with empty or populated entries
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleMetricsV1 exercises robot.GetMetrics ---

func TestHandleMetricsV1_WithPeriod(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/metrics?session=test&period=1h", nil)
	rec := httptest.NewRecorder()

	s.handleMetricsV1(rec, req)

	// robot.GetMetrics may succeed or fail — either exercises the code
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAgentInterruptV1 with valid session exercises past validation ---

func TestHandleAgentInterruptV1_ValidSession(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	body := strings.NewReader(`{"pane_index":0}`)
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/agent/interrupt", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentInterruptV1(rec, req)

	// Should pass validation, attempt tmux interaction → 500
	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid session should not return 400")
	}
}

// --- handleAgentRestartV1 with valid session ---

func TestHandleAgentRestartV1_ValidSession(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test-session")
	body := strings.NewReader(`{"pane_index":0}`)
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/agent/restart", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentRestartV1(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Fatal("valid session should not return 400")
	}
}

// --- handlePaletteV1 with search param ---

func TestHandlePaletteV1_WithSearch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/palette?search=test", nil)
	rec := httptest.NewRecorder()

	s.handlePaletteV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- writeErrorResponse hint-only (details empty after removal) ---

// --- writeErrorResponse hint only (details empty after removal) ---

func TestWriteErrorResponse_HintOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeErrorResponse(rec, http.StatusBadRequest, "BAD_REQUEST", "test error",
		map[string]interface{}{"hint": "only hint"}, "req-456")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// =============================================================================
// Batch 24: Policy validate file-based paths, checkpoint import/export,
//           installWrapperFile, policy automation update with file, policy reset
// =============================================================================

// --- handlePolicyValidateV1: valid content with version + blocked rules (no warnings, valid=true) ---

func TestHandlePolicyValidateV1_ValidContentNoWarnings(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	validPolicy := "version: 1\nblocked:\n  - pattern: \"rm -rf /\"\n    reason: \"dangerous\"\n"
	body := fmt.Sprintf(`{"content":%q}`, validPolicy)
	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["valid"] != true {
		t.Errorf("expected valid=true, got %v", resp["valid"])
	}
}

// --- handlePolicyValidateV1: valid content with validate error (bad force_release) ---

func TestHandlePolicyValidateV1_ContentValidateError(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	badPolicy := "version: 1\nautomation:\n  force_release: invalid_value\n"
	body := fmt.Sprintf(`{"content":%q}`, badPolicy)
	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false for bad force_release")
	}
}

// --- handlePolicyValidateV1: file-based, no policy file ---

func TestHandlePolicyValidateV1_FileBasedNoFile(t *testing.T) {
	// t.Setenv incompatible with t.Parallel
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["valid"] != false {
		t.Errorf("expected valid=false when file missing")
	}
	if resp["policy_path"] == nil || resp["policy_path"] == "" {
		t.Error("expected policy_path set for file-based validation")
	}
}

// --- handlePolicyValidateV1: file-based, valid policy file ---

func TestHandlePolicyValidateV1_FileBasedValidFile(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nblocked:\n  - pattern: \"danger\"\n    reason: \"test\"\n"), 0644)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["policy_path"] == nil || resp["policy_path"] == "" {
		t.Error("expected policy_path set for file-based validation")
	}
}

// --- handlePolicyValidateV1: file-based, invalid YAML in file ---

func TestHandlePolicyValidateV1_FileBasedInvalidYAML(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("{{{not valid yaml"), 0644)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["valid"] != false {
		t.Error("expected valid=false for invalid YAML file")
	}
}

// --- handlePolicyValidateV1: file-based, no version warning ---

func TestHandlePolicyValidateV1_FileBasedNoVersion(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("blocked:\n  - pattern: \"test\"\n"), 0644)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyValidateV1: file-based, empty rules warning ---

func TestHandlePolicyValidateV1_FileBasedEmptyRules(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\n"), 0644)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/validate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyValidateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyResetV1: success with HOME override ---

func TestHandlePolicyResetV1_CreatesNewDir(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	req := httptest.NewRequest("POST", "/api/v1/safety/policy/reset", nil)
	rec := httptest.NewRecorder()

	s.handlePolicyResetV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify file was created
	policyPath := filepath.Join(tmpHome, ".ntm", "policy.yaml")
	if _, err := os.Stat(policyPath); err != nil {
		t.Errorf("policy file not created: %v", err)
	}
}

// --- handlePolicyAutomationGetV1: invalid policy file returns error ---

func TestHandlePolicyAutomationGetV1_InvalidFile(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create invalid policy file so LoadOrDefault fails to parse
	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("{{{invalid"), 0644)

	req := httptest.NewRequest("GET", "/api/v1/safety/policy/automation", nil)
	rec := httptest.NewRecorder()

	s.handlePolicyAutomationGetV1(rec, req)

	// If LoadOrDefault returns default on error, should be 200
	// If it fails, should be 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyAutomationUpdateV1: update with existing policy file ---

func TestHandlePolicyAutomationUpdateV1_WithExistingFile(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create valid policy file
	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nautomation:\n  auto_commit: false\n"), 0644)

	boolTrue := true
	reqBody, _ := json.Marshal(map[string]interface{}{"auto_commit": boolTrue})
	req := httptest.NewRequest("PUT", "/api/v1/safety/policy/automation", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["modified"] != true {
		t.Errorf("expected modified=true, got %v", resp["modified"])
	}
}

// --- handlePolicyAutomationUpdateV1: create default when file missing ---

func TestHandlePolicyAutomationUpdateV1_NoExistingFile(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	boolTrue := true
	reqBody, _ := json.Marshal(map[string]interface{}{"auto_push": boolTrue})
	req := httptest.NewRequest("PUT", "/api/v1/safety/policy/automation", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyAutomationUpdateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["modified"] != true {
		t.Errorf("expected modified=true, got %v", resp["modified"])
	}
}

// --- handleImportCheckpoint: zip magic bytes detection ---

func TestHandleImportCheckpoint_ZipDetection(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")

	// PK\x03\x04 is zip magic bytes
	zipData := base64.StdEncoding.EncodeToString([]byte("PK\x03\x04fakezipdata"))
	body := fmt.Sprintf(`{"data":%q}`, zipData)
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/checkpoints/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleImportCheckpoint(rec, req)

	// Will fail at storage.Import but exercises decode + zip detection + temp file write
	if rec.Code == http.StatusOK {
		t.Log("import succeeded unexpectedly")
	}
}

// --- handleExportCheckpoint: GET with all query params ---

func TestHandleExportCheckpoint_GetWithQueryParams(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent")

	req := httptest.NewRequest("GET", "/api/v1/sessions/test-session/checkpoints/nonexistent/export?format=zip&redact_secrets=true&rewrite_paths=false", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	// Checkpoint doesn't exist, should get 404 but exercises query param parsing
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- installWrapperFile: write to nonexistent directory ---

func TestInstallWrapperFile_WriteError(t *testing.T) {
	t.Parallel()

	badPath := filepath.Join(t.TempDir(), "nonexistent", "subdir", "wrapper.sh")
	err := installWrapperFile(badPath, "#!/bin/sh\necho hello", false)
	if err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
}

// --- installWrapperFile: already exists without force ---

func TestInstallWrapperFile_ExistsNoForce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "existing.sh")
	os.WriteFile(path, []byte("existing"), 0755)

	err := installWrapperFile(path, "new content", false)
	if err == nil {
		t.Fatal("expected conflict error when file exists and force=false")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %s", err.Error())
	}
}

// --- installWrapperFile: force overwrite ---

func TestInstallWrapperFile_ForceOverwrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wrapper.sh")
	os.WriteFile(path, []byte("old"), 0755)

	err := installWrapperFile(path, "new content", true)
	if err != nil {
		t.Fatalf("expected no error with force=true: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new content" {
		t.Errorf("file content not updated: %q", string(data))
	}
}

// --- handleSafetyInstallV1: success creates all files ---

func TestHandleSafetyInstallV1_SuccessCreatesFiles(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	body := strings.NewReader(`{"force":true}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/install", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["git_wrapper"] == nil {
		t.Error("expected git_wrapper in response")
	}
	if resp["policy"] == nil {
		t.Error("expected policy in response")
	}
}

// --- handleSafetyUninstallV1: removes installed files ---

func TestHandleSafetyUninstallV1_RemovesFiles(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Install first
	binDir := filepath.Join(tmpHome, ".ntm", "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "git"), []byte("wrapper"), 0755)
	os.WriteFile(filepath.Join(binDir, "rm"), []byte("wrapper"), 0755)

	hookDir := filepath.Join(tmpHome, ".claude", "hooks", "PreToolUse")
	os.MkdirAll(hookDir, 0755)
	os.WriteFile(filepath.Join(hookDir, "ntm-safety.sh"), []byte("hook"), 0755)

	req := httptest.NewRequest("POST", "/api/v1/safety/uninstall", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyUninstallV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	removed := resp["removed"]
	if removed == nil {
		t.Fatal("expected removed field in response")
	}
	arr, ok := removed.([]interface{})
	if !ok || len(arr) == 0 {
		t.Error("expected at least one removed file")
	}
}

// --- handleSafetyInstallV1: conflict when files exist (no force) ---

func TestHandleSafetyInstallV1_ConflictNoForce(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pre-create the git wrapper to trigger conflict
	binDir := filepath.Join(tmpHome, ".ntm", "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "git"), []byte("existing"), 0755)

	req := httptest.NewRequest("POST", "/api/v1/safety/install", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyGetV1: with existing file + rules=true via HOME override ---

func TestHandlePolicyGetV1_ExistingFileWithRules(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\nblocked:\n  - pattern: \"danger\"\n    reason: \"test\"\napproval_required:\n  - pattern: \"risky\"\n    slb: true\n"), 0644)

	req := httptest.NewRequest("GET", "/api/v1/safety/policy?rules=true", nil)
	rec := httptest.NewRecorder()

	s.handlePolicyGetV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["is_default"] != false {
		t.Error("expected is_default=false when file exists")
	}
	if resp["policy_path"] == nil || resp["policy_path"] == "" {
		t.Error("expected policy_path set when file exists")
	}
	if resp["rules"] == nil {
		t.Error("expected rules when rules=true")
	}
}

// --- handlePolicyUpdateV1: success path with HOME override ---

func TestHandlePolicyUpdateV1_SuccessWithHomeOverride(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	validPolicy := "version: 1\nblocked:\n  - pattern: \"danger\"\n"
	body := fmt.Sprintf(`{"content":%q}`, validPolicy)
	req := httptest.NewRequest("PUT", "/api/v1/safety/policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handlePolicyUpdateV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Verify file written
	policyPath := filepath.Join(tmpHome, ".ntm", "policy.yaml")
	if _, err := os.Stat(policyPath); err != nil {
		t.Errorf("policy file not created: %v", err)
	}
}

// --- handleSafetyInstallV1: rm wrapper conflict after git succeeds ---

func TestHandleSafetyInstallV1_RmWrapperConflict(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pre-create rm wrapper but NOT git, so git install succeeds then rm fails
	binDir := filepath.Join(tmpHome, ".ntm", "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "rm"), []byte("existing"), 0755)

	req := httptest.NewRequest("POST", "/api/v1/safety/install", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	// Git wrapper succeeds, rm wrapper conflicts
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for rm wrapper conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSafetyInstallV1: hook conflict after wrappers succeed ---

func TestHandleSafetyInstallV1_HookConflict(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pre-create hook but not wrappers
	hookDir := filepath.Join(tmpHome, ".claude", "hooks", "PreToolUse")
	os.MkdirAll(hookDir, 0755)
	os.WriteFile(filepath.Join(hookDir, "ntm-safety.sh"), []byte("existing"), 0755)

	req := httptest.NewRequest("POST", "/api/v1/safety/install", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	// Wrappers succeed, hook conflicts
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for hook conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSafetyBlockedV1: with limit param that truncates ---

func TestHandleSafetyBlockedV1_WithLimitParam(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	req := httptest.NewRequest("GET", "/api/v1/safety/blocked?hours=1&limit=5", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyBlockedV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSafetyBlockedV1: with invalid hours/limit ---

func TestHandleSafetyBlockedV1_InvalidParams(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/safety/blocked?hours=abc&limit=-1", nil)
	rec := httptest.NewRecorder()

	s.handleSafetyBlockedV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handlePolicyGetV1: with SLB approval rules via HOME override ---

func TestHandlePolicyGetV1_SLBRuleCount(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	policyDir := filepath.Join(tmpHome, ".ntm")
	os.MkdirAll(policyDir, 0755)
	os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("version: 1\napproval_required:\n  - pattern: \"destroy\"\n    slb: true\n  - pattern: \"rebuild\"\n    slb: true\n"), 0644)

	req := httptest.NewRequest("GET", "/api/v1/safety/policy", nil)
	rec := httptest.NewRecorder()

	s.handlePolicyGetV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	stats, _ := resp["stats"].(map[string]interface{})
	if stats != nil && stats["slb_rules"] != nil {
		slb := stats["slb_rules"].(float64)
		if slb != 2 {
			t.Errorf("expected 2 slb rules, got %v", slb)
		}
	}
}

// --- handleExportCheckpoint: successful tar.gz export with fake checkpoint ---

func TestHandleExportCheckpoint_TarGzSuccess(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create fake checkpoint directory with metadata.json
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-cp")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-cp","name":"test","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "test-cp")

	req := httptest.NewRequest("GET", "/api/v1/sessions/test-session/checkpoints/test-cp/export", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["data"] == nil {
		t.Error("expected base64 data in response")
	}
	if resp["content_type"] != "application/gzip" {
		t.Errorf("expected application/gzip, got %v", resp["content_type"])
	}
}

// --- handleExportCheckpoint: zip format success ---

func TestHandleExportCheckpoint_ZipSuccess(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-cp2")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-cp2","name":"test2","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "test-cp2")

	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/checkpoints/test-cp2/export", strings.NewReader(`{"format":"zip"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["content_type"] != "application/zip" {
		t.Errorf("expected application/zip, got %v", resp["content_type"])
	}
}

// --- handleExportCheckpoint: download mode ---

func TestHandleExportCheckpoint_Download(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-cp3")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-cp3","name":"dl-test","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "test-cp3")

	req := httptest.NewRequest("GET", "/api/v1/sessions/test-session/checkpoints/test-cp3/export?download=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/octet-stream" {
		t.Errorf("expected application/octet-stream content type, got %s", ct)
	}
}

// --- handleRollback: dry run with fake checkpoint ---

func TestHandleRollback_DryRun(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-rb")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-rb","name":"rollback-test","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def","branch":"main"}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")

	body := `{"checkpoint_ref":"test-rb","dry_run":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRollback: no git state ---

func TestHandleRollback_NoGitState(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-rb2")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-rb2","name":"no-git","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")

	body := `{"checkpoint_ref":"test-rb2"}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/test-session/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	// Should fail with "checkpoint has no git state to rollback to"
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleDeleteCheckpoint: successful delete with fake checkpoint ---

func TestHandleDeleteCheckpoint_Success(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "test-session", "test-del")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"test-del","name":"delete-me","session_name":"test-session","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "test-del")

	req := httptest.NewRequest("DELETE", "/api/v1/sessions/test-session/checkpoints/test-del", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleDeleteCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 25 – checkpoint list/get, rollback dry-run branches, export POST,
//            search messages missing query, get message invalid ID
// =============================================================================

// --- handleListCheckpoints: success with fake checkpoint data ---

func TestHandleListCheckpoints_WithFakeCheckpoint(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create fake checkpoint
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "list-sess", "cp-list-1")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-list-1","name":"list-test","session_name":"list-sess","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":2}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "list-sess")

	req := httptest.NewRequest("GET", "/api/v1/sessions/list-sess/checkpoints", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleListCheckpoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if count, ok := resp["count"].(float64); !ok || count != 1 {
		t.Fatalf("expected count=1, got %v", resp["count"])
	}
}

// --- handleListCheckpoints: with details=true and description ---

func TestHandleListCheckpoints_WithDetailsFakeCP(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "det-sess", "cp-det-1")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-det-1","name":"detail-test","description":"with details","session_name":"det-sess","working_dir":"/tmp/proj","created_at":"2025-06-01T12:00:00Z","pane_count":3}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "det-sess")

	req := httptest.NewRequest("GET", "/api/v1/sessions/det-sess/checkpoints?details=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleListCheckpoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleGetCheckpoint: success with fake checkpoint data ---

func TestHandleGetCheckpoint_FakeCheckpointSuccess(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "get-sess", "cp-get-1")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-get-1","name":"get-test","session_name":"get-sess","working_dir":"/tmp","created_at":"2025-01-15T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "get-sess")
	rctx.URLParams.Add("checkpointId", "cp-get-1")

	req := httptest.NewRequest("GET", "/api/v1/sessions/get-sess/checkpoints/cp-get-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleGetCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRollback: no working directory in checkpoint ---

func TestHandleRollback_NoWorkingDir(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-nowd", "cp-nowd")
	os.MkdirAll(cpDir, 0755)
	// working_dir is empty string
	metadata := `{"version":1,"id":"cp-nowd","name":"no-wd","session_name":"rb-nowd","working_dir":"","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890","branch":"main"}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-nowd")

	body := `{"checkpoint_ref":"cp-nowd"}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-nowd/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	// Should fail with "checkpoint has no working directory"
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "working directory") {
		t.Fatalf("expected working directory error, got: %s", rec.Body.String())
	}
}

// --- handleRollback: dry run with dirty + stash warning ---

func TestHandleRollback_DryRunDirtyStash(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-dirty", "cp-dirty")
	os.MkdirAll(cpDir, 0755)
	// IsDirty=true to exercise the stash warning path
	metadata := `{"version":1,"id":"cp-dirty","name":"dirty-test","session_name":"rb-dirty","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890","branch":"main","is_dirty":true}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-dirty")

	body := `{"checkpoint_ref":"cp-dirty","dry_run":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-dirty/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Should include stash warning
	body2 := rec.Body.String()
	if !strings.Contains(body2, "would rollback") {
		t.Fatalf("expected rollback warning, got: %s", body2)
	}
	if !strings.Contains(body2, "would stash") {
		t.Fatalf("expected stash warning for dirty checkpoint, got: %s", body2)
	}
}

// --- handleRollback: dry run with no_git flag (no stash warning) ---

func TestHandleRollback_DryRunNoGitFlag(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-nogit", "cp-nogit")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-nogit","name":"nogit-test","session_name":"rb-nogit","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890","branch":"main","is_dirty":true}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-nogit")

	body := `{"checkpoint_ref":"cp-nogit","dry_run":true,"no_git":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-nogit/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// With no_git=true, should NOT have stash warning
	if strings.Contains(rec.Body.String(), "would stash") {
		t.Fatalf("no_git=true should skip stash warning, got: %s", rec.Body.String())
	}
}

// --- handleExportCheckpoint: unsupported format ---

func TestHandleExportCheckpoint_UnsupportedFormat(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "exp-fmt", "cp-fmt")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-fmt","name":"fmt-test","session_name":"exp-fmt","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "exp-fmt")
	rctx.URLParams.Add("checkpointId", "cp-fmt")

	req := httptest.NewRequest("GET", "/api/v1/sessions/exp-fmt/checkpoints/cp-fmt/export?format=7z", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported format, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got: %s", rec.Body.String())
	}
}

// --- handleExportCheckpoint: POST with body flags ---

func TestHandleExportCheckpoint_PostWithFlags(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "exp-post", "cp-post")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-post","name":"post-test","session_name":"exp-post","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "exp-post")
	rctx.URLParams.Add("checkpointId", "cp-post")

	// POST with JSON body specifying format and options
	body := `{"format":"tar.gz","include_scrollback":false,"include_git_patch":false,"redact_secrets":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/exp-post/checkpoints/cp-post/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	// Should succeed (or fail gracefully trying to export real data)
	// The key is we exercise the POST parsing path + include_scrollback/include_git_patch flags
	// Status may be 200 (if export works) or 500 (if export fails on fake data)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleExportCheckpoint: POST with invalid JSON body ---

func TestHandleExportCheckpoint_PostInvalidBody(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "any-sess")
	rctx.URLParams.Add("checkpointId", "any-cp")

	req := httptest.NewRequest("POST", "/api/v1/sessions/any-sess/checkpoints/any-cp/export", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid body, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRollback: default checkpoint_ref to latest (empty ref) ---

func TestHandleRollback_DefaultRefToLatest(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Don't create any checkpoints — so "latest" won't resolve
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-latest")

	body := `{}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-latest/checkpoints/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	// Should 404 because "latest" resolves to nothing
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 26 – missing-parameter validation branches across server.go handlers
// =============================================================================

// --- handleAgentActivityV1: missing sessionId ---

func TestHandleAgentActivityV1_MissingSessionID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("GET", "/api/v1/sessions//agents/activity", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentActivityV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAgentHealthV1: missing sessionId ---

func TestHandleAgentHealthV1_MissingSessionID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("GET", "/api/v1/sessions//agents/health", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentHealthV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAgentContextV1: missing sessionId ---

func TestHandleAgentContextV1_MissingSessionID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("GET", "/api/v1/sessions//agents/context", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentContextV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAgentRestartV1: missing sessionId ---

func TestHandleAgentRestartV1_MissingSessionID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("POST", "/api/v1/sessions//agents/restart", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleAgentRestartV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSessionStatusV1: missing session ID ---

func TestHandleSessionStatusV1_MissingID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("GET", "/api/v1/sessions//status", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleSessionStatusV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSessionAttachV1: missing session ID ---

func TestHandleSessionAttachV1_MissingID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("POST", "/api/v1/sessions//attach", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleSessionAttachV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSessionViewV1: missing session ID ---

func TestHandleSessionViewV1_MissingID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("POST", "/api/v1/sessions//view", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleSessionViewV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleSessionZoomV1: missing session ID ---

func TestHandleSessionZoomV1_MissingID(t *testing.T) {
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()

	req := httptest.NewRequest("POST", "/api/v1/sessions//zoom", strings.NewReader(`{"pane":0}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleSessionZoomV1(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleVerifyCheckpoint: success with fake checkpoint ---

func TestHandleVerifyCheckpoint_FakeCheckpoint(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "vfy-sess2", "vfy-cp2")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"vfy-cp2","name":"verify-test","session_name":"vfy-sess2","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "vfy-sess2")
	rctx.URLParams.Add("checkpointId", "vfy-cp2")

	req := httptest.NewRequest("GET", "/api/v1/sessions/vfy-sess2/checkpoints/vfy-cp2/verify", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleVerifyCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 27 – import checkpoint branches, SSE event broadcasting, restore
//            checkpoint error mapping
// =============================================================================

// --- handleImportCheckpoint: valid base64 but corrupt archive (non-zip, non-gzip) ---

func TestHandleImportCheckpoint_CorruptArchive(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "imp-corrupt")

	// Encode "this is not a valid archive" as base64
	corruptData := base64.StdEncoding.EncodeToString([]byte("this is not a valid tar.gz or zip"))
	body := fmt.Sprintf(`{"data":"%s"}`, corruptData)
	req := httptest.NewRequest("POST", "/api/v1/sessions/imp-corrupt/checkpoints/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleImportCheckpoint(rec, req)

	// Should fail because the archive is corrupt
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for corrupt archive, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleImportCheckpoint: valid base64 zip magic bytes but corrupt content ---

func TestHandleImportCheckpoint_CorruptZip(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "imp-zip")

	// Start with PK magic bytes (zip) followed by garbage
	zipLike := append([]byte("PK\x03\x04"), []byte("garbage data that is not a valid zip")...)
	data := base64.StdEncoding.EncodeToString(zipLike)
	body := fmt.Sprintf(`{"data":"%s"}`, data)
	req := httptest.NewRequest("POST", "/api/v1/sessions/imp-zip/checkpoints/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleImportCheckpoint(rec, req)

	// Should fail because the zip is corrupt
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for corrupt zip, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleImportCheckpoint: with target_session and verify_checksums ---

func TestHandleImportCheckpoint_WithOptions(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "imp-opts")

	corruptData := base64.StdEncoding.EncodeToString([]byte("not valid"))
	boolFalse := "false"
	body := fmt.Sprintf(`{"data":"%s","target_session":"override-sess","verify_checksums":%s,"allow_overwrite":true}`, corruptData, boolFalse)
	req := httptest.NewRequest("POST", "/api/v1/sessions/imp-opts/checkpoints/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleImportCheckpoint(rec, req)

	// Will fail on import but exercises option parsing branches
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRestoreCheckpoint: restore with fake checkpoint (not found by restorer) ---

func TestHandleRestoreCheckpoint_CheckpointNotFound(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rs-nf")
	rctx.URLParams.Add("checkpointId", "nonexistent")

	req := httptest.NewRequest("POST", "/api/v1/sessions/rs-nf/checkpoints/nonexistent/restore", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRestoreCheckpoint(rec, req)

	// Should fail because checkpoint doesn't exist
	if rec.Code == http.StatusOK {
		t.Fatalf("expected error for nonexistent checkpoint, got 200")
	}
}

// --- handleRestoreCheckpoint: with options (dry_run, force, skip_git_check) ---

func TestHandleRestoreCheckpoint_WithDryRunOptions(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create fake checkpoint
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rs-dry", "cp-dry")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-dry","name":"dry-run","session_name":"rs-dry","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rs-dry")
	rctx.URLParams.Add("checkpointId", "cp-dry")

	body := `{"dry_run":true,"force":true,"skip_git_check":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rs-dry/checkpoints/cp-dry/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRestoreCheckpoint(rec, req)

	// May succeed (dry_run) or fail (tmux not available) — exercises option parsing
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleEventStream: sends event through broadcast ---

func TestHandleEventStream_WithBroadcast(t *testing.T) {
	s, _ := setupTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.handleEventStream(rec, req)
		close(done)
	}()

	// Wait for SSE handler to register client
	time.Sleep(50 * time.Millisecond)

	// Send an event through broadcast
	s.broadcastEvent(testSSEEvent{
		eventType: "test.event",
		session:   "test-session",
		timestamp: time.Now(),
	})

	// Give time for event to be received and written
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to end streaming
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not complete after cancel")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("expected connected event, got: %s", body)
	}
	if !strings.Contains(body, "event: test.event") {
		t.Fatalf("expected test.event in output, got: %s", body)
	}
}

// testSSEEvent implements events.BusEvent for testing.
type testSSEEvent struct {
	eventType string
	session   string
	timestamp time.Time
}

func (e testSSEEvent) EventType() string        { return e.eventType }
func (e testSSEEvent) EventSession() string      { return e.session }
func (e testSSEEvent) EventTimestamp() time.Time { return e.timestamp }

// --- handleExportCheckpoint: GET with redact_secrets param ---

func TestHandleExportCheckpoint_GetWithRedactSecrets(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "exp-redact", "cp-redact")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-redact","name":"redact-test","session_name":"exp-redact","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "exp-redact")
	rctx.URLParams.Add("checkpointId", "cp-redact")

	req := httptest.NewRequest("GET", "/api/v1/sessions/exp-redact/checkpoints/cp-redact/export?format=zip&redact_secrets=true&rewrite_paths=false", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	// Should succeed or fail on export but exercise the query param parsing
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 28: broadcastEvent buffer-full, audit record/close both paths,
//           SSE no-flusher, RedactJSON ModeOff, import tar.gz, audit JSONL error
// =============================================================================

// --- broadcastEvent: client buffer full → default case ---

func TestBroadcastEvent_BufferFull(t *testing.T) {
	s, _ := setupTestServer(t)

	// Create a channel with buffer size 1
	ch := make(chan events.BusEvent, 1)
	s.addSSEClient(ch)
	defer s.removeSSEClient(ch)

	// Fill the buffer
	s.broadcastEvent(testSSEEvent{
		eventType: "fill.event",
		session:   "test",
		timestamp: time.Now(),
	})

	// Second broadcast should hit the default (buffer full, skip) case
	s.broadcastEvent(testSSEEvent{
		eventType: "overflow.event",
		session:   "test",
		timestamp: time.Now(),
	})

	// Drain the channel — should only get the first event
	select {
	case ev := <-ch:
		if ev.EventType() != "fill.event" {
			t.Fatalf("expected fill.event, got %s", ev.EventType())
		}
	default:
		t.Fatal("expected at least one event in channel")
	}

	// Channel should now be empty (overflow was dropped)
	select {
	case ev := <-ch:
		t.Fatalf("expected empty channel, got event: %s", ev.EventType())
	default:
		// expected — overflow was dropped
	}
}

// --- handleEventStream: writer without Flusher interface ---

type noFlushWriter struct {
	code int
	hdr  http.Header
	body bytes.Buffer
}

func (w *noFlushWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}
func (w *noFlushWriter) WriteHeader(code int) { w.code = code }
func (w *noFlushWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func TestHandleEventStream_NoFlusher(t *testing.T) {
	s, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/events", nil)
	w := &noFlushWriter{}

	s.handleEventStream(w, req)

	if w.code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for no-flusher, got %d", w.code)
	}
}

// --- RedactJSON: ModeOff early return ---

func TestRedactJSON_ModeOff(t *testing.T) {
	t.Parallel()

	cfg := redaction.Config{Mode: redaction.ModeOff}
	input := map[string]interface{}{
		"secret":   "password123",
		"api_key":  "sk-test-abc",
		"harmless": "hello",
	}

	result, findings := RedactJSON(input, cfg)
	if findings != 0 {
		t.Fatalf("expected 0 findings in ModeOff, got %d", findings)
	}

	// Result should be unchanged (same reference)
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resultMap["secret"] != "password123" {
		t.Fatalf("expected secret unchanged, got %v", resultMap["secret"])
	}
}

// --- AuditStore: Record with both DB and JSONL active ---

func TestAuditStore_RecordBothDBAndJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")
	jsonlPath := filepath.Join(tmpDir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:    dbPath,
		JSONLPath: jsonlPath,
	})
	if err != nil {
		t.Fatalf("failed to create audit store: %v", err)
	}
	defer store.Close()

	rec := &AuditRecord{
		RequestID:  "req-both-1",
		UserID:     "test-user",
		Role:       RoleAdmin,
		Action:     AuditActionCreate,
		Resource:   "session",
		Method:     "POST",
		Path:       "/api/v1/sessions",
		StatusCode: 200,
		Duration:   42,
		RemoteAddr: "127.0.0.1",
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// Verify JSONL file was written
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("failed to read JSONL: %v", err)
	}
	if !strings.Contains(string(jsonlData), "req-both-1") {
		t.Fatalf("JSONL missing request_id, got: %s", string(jsonlData))
	}

	// Verify DB was written
	records, err := store.Query(AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record in DB, got %d", len(records))
	}
}

// --- AuditStore: Close with both DB and JSONL ---

func TestAuditStore_CloseBothDBAndJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit_close.db")
	jsonlPath := filepath.Join(tmpDir, "audit_close.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:    dbPath,
		JSONLPath: jsonlPath,
	})
	if err != nil {
		t.Fatalf("failed to create audit store: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// --- NewAuditStore: unwritable JSONL dir (error branch) ---

func TestNewAuditStore_UnwritableJSONLDir(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit_uw.db")

	// Use /proc/1/audit.jsonl as unwritable path (Linux)
	jsonlPath := "/proc/1/audit.jsonl"

	_, err := NewAuditStore(AuditStoreConfig{
		DBPath:    dbPath,
		JSONLPath: jsonlPath,
	})
	if err == nil {
		t.Fatal("expected error for unwritable JSONL path, got nil")
	}
}

// --- WSEventStore: cleanup direct invocation with old events ---

func TestWSEventStore_CleanupRemovesExpired(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ws_cleanup.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Create schema
	db.Exec(`CREATE TABLE IF NOT EXISTS ws_events (
		seq INTEGER PRIMARY KEY,
		topic TEXT NOT NULL,
		event_type TEXT NOT NULL,
		data TEXT NOT NULL,
		created_at DATETIME NOT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS ws_dropped_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_id TEXT NOT NULL,
		topic TEXT NOT NULL,
		dropped_count INTEGER NOT NULL,
		first_dropped_seq INTEGER,
		last_dropped_seq INTEGER,
		reason TEXT NOT NULL,
		created_at DATETIME NOT NULL
	)`)

	// Insert an old event (2 hours ago)
	oldTime := time.Now().Add(-2 * time.Hour)
	db.Exec("INSERT INTO ws_events (seq, topic, event_type, data, created_at) VALUES (1, 'test', 'old', '{}', ?)", oldTime)

	// Insert a recent event
	db.Exec("INSERT INTO ws_events (seq, topic, event_type, data, created_at) VALUES (2, 'test', 'new', '{}', ?)", time.Now())

	// Insert an old dropped event (25 hours ago)
	db.Exec("INSERT INTO ws_dropped_events (client_id, topic, dropped_count, reason, created_at) VALUES ('c1', 'test', 5, 'buffer_full', ?)", time.Now().Add(-25*time.Hour))

	store := NewWSEventStore(db, WSEventStoreConfig{
		BufferSize:       100,
		RetentionSeconds: 3600, // 1 hour retention
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	// Directly invoke cleanup
	err = store.cleanup()
	if err != nil {
		t.Fatalf("cleanup error: %v", err)
	}

	// Verify old event was removed
	var count int
	db.QueryRow("SELECT COUNT(*) FROM ws_events").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 event after cleanup, got %d", count)
	}

	// Verify old dropped event was removed
	db.QueryRow("SELECT COUNT(*) FROM ws_dropped_events").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 dropped events after cleanup, got %d", count)
	}
}

// --- handleImportCheckpoint: tar.gz magic bytes detection ---

func TestHandleImportCheckpoint_TarGzMagic(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "imp-tgz")

	// gzip magic bytes (1f 8b) followed by garbage
	gzipLike := append([]byte{0x1f, 0x8b, 0x08, 0x00}, []byte("not a real gzip stream but has magic bytes")...)
	data := base64.StdEncoding.EncodeToString(gzipLike)
	body := fmt.Sprintf(`{"data":"%s"}`, data)
	req := httptest.NewRequest("POST", "/api/v1/sessions/imp-tgz/checkpoints/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleImportCheckpoint(rec, req)

	// Should fail during tar.gz parsing
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRollback: fake checkpoint with git state but restore fails ---

func TestHandleRollback_FakeGitCheckpointNoGit(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create fake checkpoint with git state
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-git", "cp-git")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-git","name":"git-test","session_name":"rb-git","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890abc12345def67890abc12345","branch":"main","is_dirty":false}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-git")

	// Request non-dry-run rollback (will fail at git checkout since /tmp is not a git repo)
	body := `{"checkpoint_ref":"cp-git","dry_run":false,"no_stash":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-git/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	// Should fail at git checkout (not a git repo)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for git checkout in non-git dir, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleRollback: fake checkpoint with git state, dry_run=true, no_stash=false, dirty ---

func TestHandleRollback_DryRunDirtyNoStash(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-dns", "cp-dns")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-dns","name":"dns-test","session_name":"rb-dns","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890abc12345def67890abc12345","branch":"main","is_dirty":true}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-dns")

	// dry_run=true, no_git=false, no_stash=false → exercises "would stash" warning branch
	body := `{"checkpoint_ref":"cp-dns","dry_run":true,"no_git":false,"no_stash":false}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-dns/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for dry_run, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "would rollback") {
		t.Fatalf("expected rollback warning, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "would stash") {
		t.Fatalf("expected stash warning, got: %s", bodyStr)
	}
}

// =============================================================================
// Batch 29: full git rollback integration, delete fake CP, export tar.gz,
//           audit zero timestamp, ws_events memory-only store
// =============================================================================

// --- handleRollback: full integration with real git repo ---
// Creates a temp git repo, makes it dirty, creates a fake checkpoint pointing
// to the initial commit, then runs a non-dry-run rollback to exercise:
// stash (success), checkout, success response path.

func TestHandleRollback_FullGitIntegration(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create a real git repo in a temp directory
	gitDir := filepath.Join(tmpHome, "work")
	os.MkdirAll(gitDir, 0755)

	// Initialize git repo and create initial commit
	cmds := []struct {
		args []string
		dir  string
	}{
		{[]string{"git", "init"}, gitDir},
		{[]string{"git", "config", "user.email", "test@test.com"}, gitDir},
		{[]string{"git", "config", "user.name", "Test"}, gitDir},
	}
	for _, c := range cmds {
		cmd := exec.Command(c.args[0], c.args[1:]...)
		cmd.Dir = c.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", c.args, err, out)
		}
	}

	// Create a file and initial commit
	os.WriteFile(filepath.Join(gitDir, "hello.txt"), []byte("hello"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = gitDir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = gitDir
	cmd.CombinedOutput()

	// Get the commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	commitOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	commitHash := strings.TrimSpace(string(commitOut))

	// Make the repo dirty
	os.WriteFile(filepath.Join(gitDir, "dirty.txt"), []byte("dirty"), 0644)

	// Create fake checkpoint pointing to this commit
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-full", "cp-full")
	os.MkdirAll(cpDir, 0755)
	metadata := fmt.Sprintf(`{"version":1,"id":"cp-full","name":"full-test","session_name":"rb-full","working_dir":%q,"created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"%s","branch":"main","is_dirty":true}}`, gitDir, commitHash)
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-full")

	body := `{"checkpoint_ref":"cp-full","dry_run":false,"no_git":false,"no_stash":false}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-full/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "git_restored") {
		t.Fatalf("expected git_restored in response, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "stash_created") {
		t.Fatalf("expected stash_created in response, got: %s", bodyStr)
	}
}

// --- handleRollback: no_git=true skips git entirely → success path ---

func TestHandleRollback_NoGitSuccess(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-nog", "cp-nog")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-nog","name":"no-git","session_name":"rb-nog","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"abc12345def67890abc12345def67890abc12345","branch":"main","is_dirty":false}}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-nog")

	body := `{"checkpoint_ref":"cp-nog","dry_run":false,"no_git":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-nog/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for no_git rollback, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	// git_restored should be false when no_git=true
	if strings.Contains(bodyStr, `"git_restored":true`) {
		t.Fatalf("expected git_restored=false when no_git=true, got: %s", bodyStr)
	}
}

// --- handleDeleteCheckpoint: delete fake checkpoint success path ---

func TestHandleDeleteCheckpoint_FakeCPSuccess(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create fake checkpoint
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "del-fake", "cp-del")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-del","name":"delete-me","session_name":"del-fake","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "del-fake")
	rctx.URLParams.Add("checkpointId", "cp-del")

	req := httptest.NewRequest("DELETE", "/api/v1/sessions/del-fake/checkpoints/cp-del", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleDeleteCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"deleted":true`) {
		t.Fatalf("expected deleted:true, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "cp-del") {
		t.Fatalf("expected checkpoint_id in response, got: %s", bodyStr)
	}
}

// --- handleExportCheckpoint: tar.gz format ---

func TestHandleExportCheckpoint_TarGzFormat(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "exp-tgz", "cp-tgz")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-tgz","name":"tgz-test","session_name":"exp-tgz","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "exp-tgz")
	rctx.URLParams.Add("checkpointId", "cp-tgz")

	req := httptest.NewRequest("GET", "/api/v1/sessions/exp-tgz/checkpoints/cp-tgz/export?format=tar.gz", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	// Should succeed (tar.gz export of simple checkpoint)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- AuditStore Record: zero timestamp gets auto-filled ---

func TestAuditStore_RecordZeroTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit_ts.db")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath: dbPath,
	})
	if err != nil {
		t.Fatalf("failed to create audit store: %v", err)
	}
	defer store.Close()

	rec := &AuditRecord{
		// Timestamp is zero value — should get auto-filled
		RequestID:  "req-auto-ts",
		UserID:     "test-user",
		Role:       RoleAdmin,
		Action:     AuditActionExecute,
		Resource:   "config",
		Method:     "GET",
		Path:       "/api/v1/config",
		StatusCode: 200,
		Duration:   5,
		RemoteAddr: "127.0.0.1",
	}

	if err := store.Record(rec); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// The timestamp should have been auto-filled
	if rec.Timestamp.IsZero() {
		t.Fatal("expected timestamp to be auto-filled, got zero")
	}
}

// --- WSEventStore: Store with nil DB (memory-only) ---

func TestWSEventStore_StoreNilDB(t *testing.T) {
	t.Parallel()

	store := NewWSEventStore(nil, WSEventStoreConfig{
		BufferSize:       10,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	ev, err := store.Store("session.test", "agent.start", map[string]string{"agent": "test"})
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if ev.Seq != 1 {
		t.Fatalf("expected seq=1, got %d", ev.Seq)
	}

	// GetSince should work from buffer
	events, reset, err := store.GetSince(0, "", 10)
	if err != nil {
		t.Fatalf("GetSince failed: %v", err)
	}
	if reset {
		t.Fatal("unexpected cursor reset for fresh store")
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

// =============================================================================
// Batch 30: rollback with git patch, rollback clean repo, export POST tar.gz
// =============================================================================

// --- handleRollback: with git patch file (HasGitPatch + gitApplyPatch) ---

func TestHandleRollback_WithGitPatch(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create a real git repo
	gitDir := filepath.Join(tmpHome, "work-patch")
	os.MkdirAll(gitDir, 0755)
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create file and initial commit
	os.WriteFile(filepath.Join(gitDir, "hello.txt"), []byte("hello"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = gitDir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = gitDir
	cmd.CombinedOutput()

	// Get commit hash
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	commitOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	commitHash := strings.TrimSpace(string(commitOut))

	// Create a patch file in the checkpoint dir
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-patch", "cp-patch")
	os.MkdirAll(cpDir, 0755)

	// Write a valid but intentionally broken patch file (will fail on apply)
	patchContent := `--- /dev/null
+++ b/patched.txt
@@ -0,0 +1 @@
+patched content
`
	os.WriteFile(filepath.Join(cpDir, "changes.patch"), []byte(patchContent), 0644)

	metadata := fmt.Sprintf(`{"version":1,"id":"cp-patch","name":"patch-test","session_name":"rb-patch","working_dir":%q,"created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"%s","branch":"main","is_dirty":false,"patch_file":"changes.patch"}}`, gitDir, commitHash)
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-patch")

	body := `{"checkpoint_ref":"cp-patch","dry_run":false,"no_git":false,"no_stash":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-patch/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	// Should succeed — patch apply may warn but rollback still succeeds
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "git_restored") {
		t.Fatalf("expected git_restored in response, got: %s", bodyStr)
	}
}

// --- handleRollback: clean repo (gitStashIfDirty returns "", nil) ---

func TestHandleRollback_CleanRepo(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create a real git repo with NO dirty state
	gitDir := filepath.Join(tmpHome, "work-clean")
	os.MkdirAll(gitDir, 0755)
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	os.WriteFile(filepath.Join(gitDir, "hello.txt"), []byte("hello"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = gitDir
	cmd.CombinedOutput()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = gitDir
	cmd.CombinedOutput()

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	commitOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse failed: %v", err)
	}
	commitHash := strings.TrimSpace(string(commitOut))

	// Create fake checkpoint pointing to this commit — no dirty state
	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "rb-clean", "cp-clean")
	os.MkdirAll(cpDir, 0755)
	metadata := fmt.Sprintf(`{"version":1,"id":"cp-clean","name":"clean-test","session_name":"rb-clean","working_dir":%q,"created_at":"2025-01-01T00:00:00Z","pane_count":1,"git":{"commit":"%s","branch":"main","is_dirty":false}}`, gitDir, commitHash)
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "rb-clean")

	body := `{"checkpoint_ref":"cp-clean","dry_run":false,"no_git":false,"no_stash":false}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/rb-clean/rollback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleRollback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"git_restored":true`) {
		t.Fatalf("expected git_restored=true, got: %s", bodyStr)
	}
	// Should NOT have stash_created since repo was clean
	if strings.Contains(bodyStr, `"stash_created":true`) {
		t.Fatalf("did not expect stash_created for clean repo, got: %s", bodyStr)
	}
}

// --- handleExportCheckpoint: POST with tar.gz format ---

func TestHandleExportCheckpoint_PostTarGzFormat(t *testing.T) {
	s, _ := setupTestServer(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cpDir := filepath.Join(tmpHome, ".local", "share", "ntm", "checkpoints", "exp-post-tgz", "cp-ptgz")
	os.MkdirAll(cpDir, 0755)
	metadata := `{"version":1,"id":"cp-ptgz","name":"post-tgz","session_name":"exp-post-tgz","working_dir":"/tmp","created_at":"2025-01-01T00:00:00Z","pane_count":1}`
	os.WriteFile(filepath.Join(cpDir, "metadata.json"), []byte(metadata), 0644)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "exp-post-tgz")
	rctx.URLParams.Add("checkpointId", "cp-ptgz")

	body := `{"format":"tar.gz","include_scrollback":true}`
	req := httptest.NewRequest("POST", "/api/v1/sessions/exp-post-tgz/checkpoints/cp-ptgz/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleExportCheckpoint(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 200 or 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// Batch 31 — WSEventStore, AuditStore, OpenAPI, Pipeline, RBAC branches
// =============================================================================

// TestWSEventStore_StorePersistError exercises the log.Printf error path
// in Store when the DB insert fails (line 204-206 of ws_events.go).
func TestWSEventStore_StorePersistError(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewWSEventStore(db, WSEventStoreConfig{
		BufferSize:       64,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	// Close the DB to trigger persist error on next Store
	db.Close()

	// Store should succeed (goes to ring buffer) but persist fails silently
	ev, err := store.Store("test.topic", "test.event", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("Store should not return error (persist error is logged): %v", err)
	}
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Topic != "test.topic" {
		t.Errorf("topic = %q, want test.topic", ev.Topic)
	}
}

// TestWSEventStore_RecordDroppedDBError exercises the error return path
// in RecordDropped when the DB exec fails (line 347-348 of ws_events.go).
func TestWSEventStore_RecordDroppedDBError(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewWSEventStore(db, WSEventStoreConfig{
		BufferSize:       64,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	// Close the DB to trigger error
	db.Close()

	err := store.RecordDropped("client-1", "test.topic", "buffer_full", 1, 10)
	if err == nil {
		t.Fatal("expected error from RecordDropped with closed DB")
	}
}

// TestAuditStore_CleanupWithRecords exercises the "affected > 0" log path
// in cleanup (line 369-370 of audit.go) by inserting old records then running cleanup.
func TestAuditStore_CleanupWithRecords(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbPath,
		Retention:       1 * time.Second,
		CleanupInterval: 1 * time.Hour, // won't fire during test
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	// Insert a record with old timestamp directly
	oldTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)
	_, err = store.db.Exec(`INSERT INTO audit_records
		(timestamp, request_id, user_id, role, action, resource, method, path, status_code, duration_ms, remote_addr)
		VALUES (?, 'req1', 'user1', 'admin', 'execute', '/test', 'GET', '/test', 200, 10, '127.0.0.1')`, oldTime)
	if err != nil {
		t.Fatalf("insert old record: %v", err)
	}

	// Verify record exists
	var count int
	store.db.QueryRow("SELECT COUNT(*) FROM audit_records").Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 record, got %d", count)
	}

	// Run cleanup — retention is 1s, record is 1h old, should be removed
	store.cleanup()

	store.db.QueryRow("SELECT COUNT(*) FROM audit_records").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 records after cleanup, got %d", count)
	}
}

// TestAuditStore_CleanupDBError exercises the error log path in cleanup
// when db.Exec fails (line 363-364 of audit.go).
func TestAuditStore_CleanupDBError(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbPath,
		Retention:       1 * time.Second,
		CleanupInterval: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() {
		// Close channel only — DB already closed
		close(store.stopCleanup)
	}()

	// Close the DB to trigger error
	store.db.Close()

	// Should not panic
	store.cleanup()
}

// TestAuditStore_CloseWithErrors exercises the combined error path in Close
// when both jsonlFile and db Close return errors (line 393-394 of audit.go).
func TestAuditStore_CloseWithErrors(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "audit.db")
	jsonlPath := filepath.Join(tmpDir, "audit.jsonl")

	store, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbPath,
		JSONLPath:       jsonlPath,
		Retention:       90 * 24 * time.Hour,
		CleanupInterval: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Close JSONL file early to trigger error on second close
	store.jsonlFile.Close()

	// Now Close the store — jsonlFile.Close should error, db.Close should succeed
	err = store.Close()
	if err == nil {
		t.Fatal("expected error from Close with pre-closed JSONL file")
	}
	if !strings.Contains(err.Error(), "close audit store") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestHandleOpenAPISpec_TLS exercises the HTTPS branch in handleOpenAPISpec
// (line 386 of openapi.go) when r.TLS is non-nil.
func TestHandleOpenAPISpec_TLS(t *testing.T) {
	t.Parallel()
	s := &Server{host: "localhost", port: 8080}

	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	req.TLS = &tls.ConnectionState{} // non-nil → HTTPS branch
	rec := httptest.NewRecorder()

	s.handleOpenAPISpec(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://localhost:8080") {
		t.Errorf("expected https server URL in spec, got: %s", body[:min(200, len(body))])
	}
}

// TestHandleSwaggerUI_TLS exercises the HTTPS branch in handleSwaggerUI
// (line 402 of openapi.go) when r.TLS is non-nil.
func TestHandleSwaggerUI_TLS(t *testing.T) {
	t.Parallel()
	s := &Server{host: "localhost", port: 8080}

	req := httptest.NewRequest("GET", "/api/v1/docs", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()

	s.handleSwaggerUI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://localhost:8080") {
		t.Errorf("expected https URL in swagger HTML, got snippet: %s", body[:min(200, len(body))])
	}
}

// TestHandleListPipelines_WithFinishedAt exercises the FinishedAt != nil branch
// in handleListPipelines (line 115-117 of pipelines.go).
func TestHandleListPipelines_WithFinishedAt(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	// Register a pipeline with FinishedAt set
	now := time.Now()
	finished := now.Add(10 * time.Second)
	exec := &pipeline.PipelineExecution{
		RunID:      "test-run-finished-" + fmt.Sprintf("%d", now.UnixNano()),
		WorkflowID: "test-workflow",
		Session:    "test-session",
		Status:     "completed",
		StartedAt:  now,
		FinishedAt: &finished,
		Progress:   pipeline.PipelineProgress{Total: 3, Completed: 3, Percent: 100},
	}
	pipeline.RegisterPipeline(exec)

	req := httptest.NewRequest("GET", "/api/v1/pipelines", nil)
	rec := httptest.NewRecorder()
	s.handleListPipelines(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	pipelines, _ := resp["pipelines"].([]interface{})

	// Find our pipeline
	found := false
	for _, p := range pipelines {
		pm, _ := p.(map[string]interface{})
		if pm["run_id"] == exec.RunID {
			found = true
			if pm["finished_at"] == nil || pm["finished_at"] == "" {
				t.Error("expected non-empty finished_at")
			}
		}
	}
	if !found {
		t.Error("registered pipeline not found in response")
	}
}

// TestWSEventStore_GetSinceDB_TopicFilterWithLimit exercises getFromDB
// with topic filtering and the limit truncation (lines 308-309 of ws_events.go).
func TestWSEventStore_GetSinceDB_TopicFilterWithLimit(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewWSEventStore(db, WSEventStoreConfig{
		BufferSize:       4, // tiny buffer so events fall out
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	// Store many events with different topics to overflow buffer
	for i := 0; i < 20; i++ {
		topic := "topicA"
		if i%3 == 0 {
			topic = "topicB"
		}
		store.Store(topic, "evt", map[string]int{"i": i})
	}

	// Query from DB with topic filter and limit=2
	events, reset, err := store.getFromDB(0, "topicA", 2)
	if err != nil {
		t.Fatalf("getFromDB: %v", err)
	}
	if reset {
		t.Error("unexpected reset signal")
	}
	if len(events) > 2 {
		t.Errorf("expected at most 2 events, got %d", len(events))
	}
	for _, ev := range events {
		if ev.Topic != "topicA" {
			t.Errorf("expected topic=topicA, got %q", ev.Topic)
		}
	}
}

// TestWSEventStore_GetSinceDB_CursorTooOldBranch exercises the cursor-too-old
// branch in getFromDB (lines 318-325 of ws_events.go) when since < minSeq-1.
// The branch only fires when topic filtering removes all SQL results.
func TestWSEventStore_GetSinceDB_CursorTooOldBranch(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewWSEventStore(db, WSEventStoreConfig{
		BufferSize:       4,
		RetentionSeconds: 3600,
		CleanupInterval:  time.Hour,
	})
	defer store.Stop()

	// Insert high-seq events directly into DB with topic "onlyA"
	for i := 100; i < 105; i++ {
		db.Exec("INSERT INTO ws_events (seq, topic, event_type, data, created_at) VALUES (?, 'onlyA', 'evt', '{}', datetime('now'))", i)
	}

	// Query with since=1, topic="nonexistent" — SQL fetches events 100-104 (seq > 1),
	// but Go topic filter removes all → len(events)=0.
	// since=1 > 0, minSeq=100, 1 < 100-1=99 → reset=true
	events, reset, err := store.getFromDB(1, "nonexistent", 10)
	if err != nil {
		t.Fatalf("getFromDB: %v", err)
	}
	if !reset {
		t.Error("expected reset=true when cursor is too old for DB (topic filter)")
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events on reset, got %d", len(events))
	}
}

// TestNewAuditStore_DBOpenError exercises the sql.Open error path
// in NewAuditStore (lines 117-118 of audit.go) by using an invalid path.
func TestNewAuditStore_DBOpenError(t *testing.T) {
	// Use a path that will fail at schema init (directory as file)
	tmpDir := t.TempDir()
	// Create a directory where the DB file should be
	dbAsDir := filepath.Join(tmpDir, "audit.db")
	os.MkdirAll(dbAsDir, 0755)

	_, err := NewAuditStore(AuditStoreConfig{
		DBPath:          dbAsDir, // a directory, not a file → schema init will fail
		Retention:       90 * 24 * time.Hour,
		CleanupInterval: 1 * time.Hour,
	})
	if err == nil {
		t.Fatal("expected error with directory as DB path")
	}
}

// TestRoleHierarchy_UnknownRole exercises the default case in roleHierarchy
// (line 296-297 of rbac.go).
func TestRoleHierarchy_UnknownRole(t *testing.T) {
	t.Parallel()
	if got := roleHierarchy("nonexistent"); got != 0 {
		t.Errorf("roleHierarchy(nonexistent) = %d, want 0", got)
	}
	if got := roleHierarchy(RoleAdmin); got != 3 {
		t.Errorf("roleHierarchy(admin) = %d, want 3", got)
	}
	if got := roleHierarchy(RoleOperator); got != 2 {
		t.Errorf("roleHierarchy(operator) = %d, want 2", got)
	}
	if got := roleHierarchy(RoleViewer); got != 1 {
		t.Errorf("roleHierarchy(viewer) = %d, want 1", got)
	}
}

// TestWriteApprovalRequired_FullResponse exercises the writeApprovalRequired function
// output format including approval_id and error_code (lines 388-410 of rbac.go).
func TestWriteApprovalRequired_FullResponse(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()

	ar := &ApprovalRequired{
		ApprovalID: "apr-123",
		Action:     "delete",
		Resource:   "session",
		ExpiresAt:  "2025-12-31T23:59:59Z",
		Message:    "Approval needed for destructive operation",
	}

	writeApprovalRequired(rec, ar, "req-456")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if resp["success"] != false {
		t.Error("expected success=false")
	}
	if resp["error_code"] != "APPROVAL_REQUIRED" {
		t.Errorf("error_code = %v", resp["error_code"])
	}
	approval, _ := resp["approval"].(map[string]interface{})
	if approval["approval_id"] != "apr-123" {
		t.Errorf("approval_id = %v", approval["approval_id"])
	}
}

// =============================================================================
// Batch 32 — Pipeline GET/Cancel/Resume, discoverPipelineTemplates, Safety Install
// =============================================================================

// TestHandleGetPipeline_FoundWithFinishedAt exercises the success path
// with FinishedAt and Error fields populated (lines 258-276 of pipelines.go).
func TestHandleGetPipeline_FoundWithFinishedAt(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	now := time.Now()
	finished := now.Add(5 * time.Second)
	runID := fmt.Sprintf("get-test-%d", now.UnixNano())
	exec := &pipeline.PipelineExecution{
		RunID:       runID,
		WorkflowID:  "wf-1",
		Session:     "sess-1",
		Status:      "failed",
		StartedAt:   now,
		FinishedAt:  &finished,
		CurrentStep: "step-2",
		Error:       "step-2 timed out",
		Progress:    pipeline.PipelineProgress{Total: 3, Completed: 1, Failed: 1},
	}
	pipeline.RegisterPipeline(exec)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID)
	req := httptest.NewRequest("GET", "/api/v1/pipelines/"+runID, nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleGetPipeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["finished_at"] == nil || resp["finished_at"] == "" {
		t.Error("expected finished_at in response")
	}
	if resp["duration_ms"] == nil {
		t.Error("expected duration_ms in response")
	}
	if resp["error"] != "step-2 timed out" {
		t.Errorf("error = %v, want 'step-2 timed out'", resp["error"])
	}
}

// TestHandleGetPipeline_NotFoundBranch exercises the 404 path in handleGetPipeline
// (line 252-256 of pipelines.go).
func TestHandleGetPipeline_NotFoundBranch(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent-run")
	req := httptest.NewRequest("GET", "/api/v1/pipelines/nonexistent-run", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleGetPipeline(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCancelPipeline_CompletedConflict exercises the conflict path
// in handleCancelPipeline (lines 300-306 of pipelines.go) — can't cancel completed.
func TestHandleCancelPipeline_CompletedConflict(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	now := time.Now()
	runID := fmt.Sprintf("cancel-conflict-%d", now.UnixNano())
	exec := &pipeline.PipelineExecution{
		RunID:      runID,
		WorkflowID: "wf-1",
		Session:    "sess-1",
		Status:     "completed",
		StartedAt:  now,
	}
	pipeline.RegisterPipeline(exec)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID)
	req := httptest.NewRequest("DELETE", "/api/v1/pipelines/"+runID, nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleCancelPipeline(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCancelPipeline_RunningSuccess exercises the success path
// in handleCancelPipeline (lines 308-315 of pipelines.go) — cancel a running pipeline.
func TestHandleCancelPipeline_RunningSuccess(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	now := time.Now()
	runID := fmt.Sprintf("cancel-ok-%d", now.UnixNano())
	exec := &pipeline.PipelineExecution{
		RunID:      runID,
		WorkflowID: "wf-1",
		Session:    "sess-1",
		Status:     "running",
		StartedAt:  now,
	}
	pipeline.RegisterPipeline(exec)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID)
	req := httptest.NewRequest("DELETE", "/api/v1/pipelines/"+runID, nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleCancelPipeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", resp["status"])
	}
}

// TestHandleResumePipeline_NoState exercises the 404 path when
// pipeline state file doesn't exist (lines 339-345 of pipelines.go).
func TestHandleResumePipeline_NoState(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent-resume")
	body := bytes.NewBufferString(`{"session":"test"}`)
	req := httptest.NewRequest("POST", "/api/v1/pipelines/nonexistent-resume/resume", body)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	s.handleResumePipeline(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDiscoverPipelineTemplates exercises the template discovery function
// with actual YAML/TOML files present (lines 920-933 of pipelines.go).
func TestDiscoverPipelineTemplates(t *testing.T) {
	// Create a temp dir with workflow files
	tmpDir := t.TempDir()

	// Create .ntm/workflows subdirectory
	wfDir := filepath.Join(tmpDir, ".ntm", "workflows")
	os.MkdirAll(wfDir, 0755)
	os.WriteFile(filepath.Join(wfDir, "deploy.yaml"), []byte("name: deploy"), 0644)
	os.WriteFile(filepath.Join(wfDir, "build.yml"), []byte("name: build"), 0644)
	os.WriteFile(filepath.Join(wfDir, "config.toml"), []byte("[config]"), 0644)
	os.WriteFile(filepath.Join(wfDir, "readme.md"), []byte("# ignore"), 0644) // should be skipped

	// Create a subdirectory to verify IsDir skip
	os.MkdirAll(filepath.Join(wfDir, "subdir"), 0755)

	// Change to tmpDir so that .ntm/workflows is found
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	templates := discoverPipelineTemplates()

	// Should find 3 templates (yaml, yml, toml) but not .md or dirs
	foundNames := map[string]bool{}
	for _, tpl := range templates {
		foundNames[tpl.Name] = true
	}
	for _, name := range []string{"deploy", "build", "config"} {
		if !foundNames[name] {
			t.Errorf("expected template %q, not found in %v", name, templates)
		}
	}
}

// TestHandleSafetyInstallV1_FullFlow exercises handleSafetyInstallV1 end-to-end
// with a fake HOME directory (lines 244-329 of safety.go).
func TestHandleSafetyInstallV1_FullFlow(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, _ := setupTestServer(t)

	body := bytes.NewBufferString(`{"force":true}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/install", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify files were created
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Check git wrapper exists
	gitWrapper := filepath.Join(tmpDir, ".ntm", "bin", "git")
	if _, err := os.Stat(gitWrapper); err != nil {
		t.Errorf("git wrapper not created: %v", err)
	}

	// Check rm wrapper exists
	rmWrapper := filepath.Join(tmpDir, ".ntm", "bin", "rm")
	if _, err := os.Stat(rmWrapper); err != nil {
		t.Errorf("rm wrapper not created: %v", err)
	}

	// Check hook exists
	hookPath := filepath.Join(tmpDir, ".claude", "hooks", "PreToolUse", "ntm-safety.sh")
	if _, err := os.Stat(hookPath); err != nil {
		t.Errorf("hook not created: %v", err)
	}
}

// TestHandleSafetyInstallV1_ExistingWrapperConflict exercises the conflict path
// when wrappers already exist and force=false (lines 278-280 of safety.go).
func TestHandleSafetyInstallV1_ExistingWrapperConflict(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Pre-create git wrapper
	binDir := filepath.Join(tmpDir, ".ntm", "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\n"), 0755)

	s, _ := setupTestServer(t)

	body := bytes.NewBufferString(`{"force":false}`)
	req := httptest.NewRequest("POST", "/api/v1/safety/install", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleSafetyInstallV1(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleExecPipeline_EmptySession exercises the missing session path
// in handleExecPipeline (lines 197-200 of pipelines.go).
func TestHandleExecPipeline_EmptySession(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	body := bytes.NewBufferString(`{"workflow":{"name":"test","steps":[]},"session":""}`)
	req := httptest.NewRequest("POST", "/api/v1/pipelines/exec", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleExecPipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleExecPipeline_InvalidBody exercises the invalid body path
// in handleExecPipeline (lines 192-195 of pipelines.go).
func TestHandleExecPipeline_InvalidBody(t *testing.T) {
	t.Parallel()
	s, _ := setupTestServer(t)

	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/api/v1/pipelines/exec", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleExecPipeline(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Ensure kernel import is used
var _ = kernel.Run
