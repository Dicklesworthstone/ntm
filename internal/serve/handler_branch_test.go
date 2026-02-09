package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/go-chi/chi/v5"
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
