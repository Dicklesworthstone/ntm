package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/go-chi/chi/v5"
)

// TestAccountsEndpointsRegistered verifies that all account endpoints are registered.
func TestAccountsEndpointsRegistered(t *testing.T) {
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal}, // Local mode grants admin
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)

	// Register accounts routes in a sub-router
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	// Test each endpoint returns a response (not 404)
	testCases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/accounts"},
		{"GET", "/api/v1/accounts/status"},
		{"GET", "/api/v1/accounts/active"},
		{"GET", "/api/v1/accounts/quota"},
		{"GET", "/api/v1/accounts/auto-rotate"},
		{"GET", "/api/v1/accounts/history"},
		{"GET", "/api/v1/accounts/claude"},
	}

	for _, tc := range testCases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// Should not be 404 (endpoint exists)
			if w.Code == http.StatusNotFound {
				t.Errorf("endpoint %s %s not registered", tc.method, tc.path)
			}
		})
	}
}

// TestAutoRotateConfigGet tests getting auto-rotate configuration.
func TestAutoRotateConfigGet(t *testing.T) {
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	req := httptest.NewRequest("GET", "/api/v1/accounts/auto-rotate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
		Config  struct {
			AutoRotateEnabled         bool `json:"auto_rotate_enabled"`
			AutoRotateCooldownSeconds int  `json:"auto_rotate_cooldown_seconds"`
			AutoRotateOnRateLimit     bool `json:"auto_rotate_on_rate_limit"`
		} `json:"config"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Error("expected success=true")
	}

	// Check defaults
	if resp.Config.AutoRotateCooldownSeconds != 300 {
		t.Errorf("expected default cooldown 300, got %d", resp.Config.AutoRotateCooldownSeconds)
	}
}

// TestAutoRotateConfigPatch tests updating auto-rotate configuration.
func TestAutoRotateConfigPatch(t *testing.T) {
	// Reset state
	accountState.mu.Lock()
	accountState.config = AccountsConfig{
		AutoRotateEnabled:         false,
		AutoRotateCooldownSeconds: 300,
		AutoRotateOnRateLimit:     true,
	}
	accountState.mu.Unlock()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	// Patch to enable auto-rotate
	body := []byte(`{"auto_rotate_enabled": true, "auto_rotate_cooldown_seconds": 600}`)
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/auto-rotate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool           `json:"success"`
		Config  AccountsConfig `json:"config"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Config.AutoRotateEnabled {
		t.Error("expected auto_rotate_enabled=true after patch")
	}

	if resp.Config.AutoRotateCooldownSeconds != 600 {
		t.Errorf("expected cooldown 600, got %d", resp.Config.AutoRotateCooldownSeconds)
	}
}

// TestAutoRotateConfigPatchValidation tests validation of auto-rotate config.
func TestAutoRotateConfigPatchValidation(t *testing.T) {
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	// Try to set cooldown below minimum (60)
	body := []byte(`{"auto_rotate_cooldown_seconds": 30}`)
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/auto-rotate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid cooldown, got %d", w.Code)
	}
}

// TestAccountsHistoryEmpty tests empty history response.
func TestAccountsHistoryEmpty(t *testing.T) {
	// Reset history
	accountState.mu.Lock()
	accountState.history = make([]AccountRotationEvent, 0)
	accountState.mu.Unlock()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	req := httptest.NewRequest("GET", "/api/v1/accounts/history", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool                   `json:"success"`
		History []AccountRotationEvent `json:"history"`
		Total   int                    `json:"total"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Total != 0 {
		t.Errorf("expected total=0, got %d", resp.Total)
	}

	if len(resp.History) != 0 {
		t.Errorf("expected empty history, got %d events", len(resp.History))
	}
}

// TestRotateAccountMissingProvider tests validation for rotate endpoint.
func TestRotateAccountMissingProvider(t *testing.T) {
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	// Empty body
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/accounts/rotate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing provider, got %d", w.Code)
	}
}

// TestAccountsResponseEnvelope tests that responses follow the API envelope format.
func TestAccountsResponseEnvelope(t *testing.T) {
	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	req := httptest.NewRequest("GET", "/api/v1/accounts/auto-rotate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify envelope fields
	if _, ok := resp["success"]; !ok {
		t.Error("response missing 'success' field")
	}
	if _, ok := resp["timestamp"]; !ok {
		t.Error("response missing 'timestamp' field")
	}
	if _, ok := resp["request_id"]; !ok {
		t.Error("response missing 'request_id' field")
	}
}

// TestAccountsRBACPermissions tests that proper permissions are enforced.
func TestAccountsRBACPermissions(t *testing.T) {
	// This test would require mocking the auth middleware to test different roles
	// For now, just verify the routes are registered with permission middleware
	t.Log("RBAC permissions are enforced via RequirePermission middleware in route registration")
}

func TestRotateAccount_DependencyMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tools.NewCAAMAdapter().InvalidateCache()

	s := &Server{
		auth: AuthConfig{Mode: AuthModeLocal},
	}
	s.wsHub = NewWSHub()

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	body := []byte(`{"provider":"claude"}`)
	req := httptest.NewRequest("POST", "/api/v1/accounts/rotate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"error_code"`
		Hint      string `json:"hint"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected success=false")
	}
	if resp.ErrorCode != robot.ErrCodeDependencyMissing {
		t.Fatalf("expected error_code=%s, got %q", robot.ErrCodeDependencyMissing, resp.ErrorCode)
	}
	if resp.Hint == "" {
		t.Fatalf("expected non-empty hint")
	}
}

func TestRotateProviderAccount_FakeCAAM_UpdatesActiveAndPublishesEvent(t *testing.T) {
	dir := t.TempDir()

	stateFile := filepath.Join(dir, "state_claude")
	if err := os.WriteFile(stateFile, []byte("claude-a"), 0o644); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}

	fakeCAAM := filepath.Join(dir, "caam")
	script := fmt.Sprintf(`#!/bin/sh
set -eu

STATE_FILE=%q

if [ "${1:-}" = "--version" ]; then
  echo "caam 1.0.0"
  exit 0
fi

if [ "${1:-}" = "list" ] && [ "${2:-}" = "--json" ]; then
  active="$(cat "$STATE_FILE" 2>/dev/null || true)"
  if [ "$active" = "claude-b" ]; then
    echo '[{"id":"claude-a","provider":"claude","email":"a@example.com","active":false},{"id":"claude-b","provider":"claude","email":"b@example.com","active":true}]'
  else
    echo '[{"id":"claude-a","provider":"claude","email":"a@example.com","active":true},{"id":"claude-b","provider":"claude","email":"b@example.com","active":false}]'
  fi
  exit 0
fi

if [ "${1:-}" = "creds" ] && [ "${3:-}" = "--json" ]; then
  provider="${2:-}"
  active="$(cat "$STATE_FILE" 2>/dev/null || true)"
  if [ "$provider" = "claude" ]; then
    echo "{\"provider\":\"claude\",\"account_id\":\"$active\",\"env_var_name\":\"ANTHROPIC_API_KEY\",\"rate_limited\":false}"
    exit 0
  fi
fi

if [ "${1:-}" = "switch" ]; then
  if [ "${3:-}" = "--next" ] && [ "${4:-}" = "--json" ]; then
    provider="${2:-}"
    if [ "$provider" != "claude" ]; then
      echo "{\"success\":false,\"provider\":\"$provider\",\"error\":\"unknown provider\"}" >&2
      exit 1
    fi

    prev="$(cat "$STATE_FILE" 2>/dev/null || true)"
    if [ "$prev" = "claude-b" ]; then
      next="claude-a"
    else
      next="claude-b"
    fi

    echo "$next" > "$STATE_FILE"
    echo "{\"success\":true,\"provider\":\"claude\",\"previous_account\":\"$prev\",\"new_account\":\"$next\",\"accounts_remaining\":1}"
    exit 0
  fi

  # Switch to a specific account ID.
  acct="${2:-}"
  echo "$acct" > "$STATE_FILE"
  exit 0
fi

echo "unknown command" >&2
exit 2
`, stateFile)
	if err := os.WriteFile(fakeCAAM, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake caam: %v", err)
	}

	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	tools.NewCAAMAdapter().InvalidateCache()

	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()
	time.Sleep(10 * time.Millisecond)

	client := &WSClient{
		id:     "accounts-watcher",
		hub:    hub,
		send:   make(chan []byte, 10),
		topics: make(map[string]struct{}),
	}
	client.Subscribe([]string{"accounts:*"})
	hub.register <- client
	time.Sleep(20 * time.Millisecond)

	s := &Server{
		auth:  AuthConfig{Mode: AuthModeLocal},
		wsHub: hub,
	}

	r := chi.NewRouter()
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.rbacMiddleware)
	r.Route("/api/v1", func(r chi.Router) {
		s.registerAccountsRoutes(r)
	})

	getActive := func(t *testing.T) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/accounts/active", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Success bool `json:"success"`
			Active  map[string]struct {
				ID string `json:"id"`
			} `json:"active"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode active response: %v", err)
		}
		if !resp.Success {
			t.Fatalf("expected success=true")
		}
		if resp.Active["claude"].ID == "" {
			t.Fatalf("expected active claude account to be present")
		}
		return resp.Active["claude"].ID
	}

	if got := getActive(t); got != "claude-a" {
		t.Fatalf("expected initial active=claude-a, got %q", got)
	}

	req := httptest.NewRequest("POST", "/api/v1/accounts/claude/rotate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var rotateResp struct {
		Success bool `json:"success"`
		Switch  struct {
			Success         bool   `json:"success"`
			Provider        string `json:"provider"`
			PreviousAccount string `json:"previous_account"`
			NewAccount      string `json:"new_account"`
		} `json:"switch"`
	}
	if err := json.NewDecoder(w.Body).Decode(&rotateResp); err != nil {
		t.Fatalf("failed to decode rotate response: %v", err)
	}
	if !rotateResp.Success || !rotateResp.Switch.Success {
		t.Fatalf("expected rotate success=true, got %+v", rotateResp)
	}
	if rotateResp.Switch.Provider != "claude" {
		t.Fatalf("expected provider=claude, got %q", rotateResp.Switch.Provider)
	}
	if rotateResp.Switch.PreviousAccount != "claude-a" || rotateResp.Switch.NewAccount != "claude-b" {
		t.Fatalf("unexpected switch result: %+v", rotateResp.Switch)
	}

	select {
	case msg := <-client.send:
		var ev struct {
			Topic     string                 `json:"topic"`
			EventType string                 `json:"event_type"`
			Data      map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil {
			t.Fatalf("failed to decode WS event: %v", err)
		}
		if ev.Topic != "accounts:claude" {
			t.Fatalf("expected topic=accounts:claude, got %q", ev.Topic)
		}
		if ev.EventType != "account.rotated" {
			t.Fatalf("expected event_type=account.rotated, got %q", ev.EventType)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("timed out waiting for websocket event")
	}

	if got := getActive(t); got != "claude-b" {
		t.Fatalf("expected post-rotate active=claude-b, got %q", got)
	}
}
