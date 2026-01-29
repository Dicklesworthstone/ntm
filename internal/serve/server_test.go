package serve

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func setupTestServer(t *testing.T) (*Server, *state.Store) {
	t.Helper()

	// Create temp database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
		os.Remove(dbPath)
	})

	eventBus := events.NewEventBus(100)

	srv := New(Config{
		Port:       0, // Will use default
		EventBus:   eventBus,
		StateStore: store,
	})

	return srv, store
}

func TestNew(t *testing.T) {
	srv := New(Config{})
	if srv == nil {
		t.Fatal("New returned nil")
	}
	if srv.Port() != 7337 {
		t.Errorf("Default port = %d, want 7337", srv.Port())
	}
}

func TestNewWithCustomPort(t *testing.T) {
	srv := New(Config{Port: 8080})
	if srv.Port() != 8080 {
		t.Errorf("Port = %d, want 8080", srv.Port())
	}
}

func TestValidateConfigDefaults(t *testing.T) {
	if err := ValidateConfig(Config{}); err != nil {
		t.Fatalf("ValidateConfig default should succeed, got %v", err)
	}
}

func TestValidateConfigRejectsExternalLocalAuth(t *testing.T) {
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{Mode: AuthModeLocal},
	}
	if err := ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "refusing to bind") {
		t.Fatalf("expected bind refusal error, got %v", err)
	}
}

func TestValidateConfigAllowsExternalWithAuth(t *testing.T) {
	cfg := Config{
		Host: "0.0.0.0",
		Auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "test-key"},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected external bind with auth to succeed, got %v", err)
	}
}

func TestValidateConfigPublicBaseURL(t *testing.T) {
	cfg := Config{
		PublicBaseURL: "https://ntm.example.com",
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("expected valid public base URL, got %v", err)
	}

	cfg.PublicBaseURL = "not-a-url"
	if err := ValidateConfig(cfg); err == nil {
		t.Fatalf("expected invalid public base URL error")
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["status"] != "healthy" {
		t.Error("Expected status=healthy")
	}
}

func TestHealthEndpointMethodNotAllowed(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()

	srv.handleHealth(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSessionsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if _, ok := resp["sessions"]; !ok {
		t.Error("Expected sessions field")
	}
}

func TestSessionsEndpointNoStore(t *testing.T) {
	srv := New(Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	srv.handleSessions(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestSessionEndpointNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/nonexistent", nil)
	rec := httptest.NewRecorder()

	srv.handleSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestSessionEndpointMissingID(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/", nil)
	rec := httptest.NewRecorder()

	srv.handleSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRobotStatusEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/robot/status", nil)
	rec := httptest.NewRecorder()

	srv.handleRobotStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestRobotHealthEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/robot/health", nil)
	rec := httptest.NewRecorder()

	srv.handleRobotHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestSessionEventsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Publish an event
	srv.eventBus.Publish(events.BaseEvent{
		Type:      "test_event",
		Timestamp: time.Now().UTC(),
		Session:   "test-session",
	})

	// Give event time to be recorded
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/test-session/events", nil)
	rec := httptest.NewRecorder()

	srv.handleSessionEvents(rec, req, "test-session")

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestSSEClientManagement(t *testing.T) {
	srv, _ := setupTestServer(t)

	ch := make(chan events.BusEvent, 10)
	srv.addSSEClient(ch)

	srv.sseClientsMu.RLock()
	clientCount := len(srv.sseClients)
	srv.sseClientsMu.RUnlock()

	if clientCount != 1 {
		t.Errorf("Client count = %d, want 1", clientCount)
	}

	srv.removeSSEClient(ch)

	srv.sseClientsMu.RLock()
	clientCount = len(srv.sseClients)
	srv.sseClientsMu.RUnlock()

	if clientCount != 0 {
		t.Errorf("Client count = %d, want 0 after removal", clientCount)
	}
}

func TestBroadcastEvent(t *testing.T) {
	srv, _ := setupTestServer(t)

	ch := make(chan events.BusEvent, 10)
	srv.addSSEClient(ch)
	defer srv.removeSSEClient(ch)

	testEvent := events.BaseEvent{
		Type:      "broadcast_test",
		Timestamp: time.Now().UTC(),
		Session:   "test",
	}

	srv.broadcastEvent(testEvent)

	select {
	case e := <-ch:
		if e.EventType() != "broadcast_test" {
			t.Errorf("Event type = %s, want broadcast_test", e.EventType())
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for broadcast")
	}
}

func TestEventStreamSSE(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a request with a cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	// Start the handler in a goroutine
	done := make(chan struct{})
	go func() {
		srv.handleEventStream(rec, req)
		close(done)
	}()

	// Give time for connection setup
	time.Sleep(50 * time.Millisecond)

	// Cancel to end the request
	cancel()

	// Wait for handler to complete
	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Error("Handler did not complete after context cancel")
	}

	// Check headers
	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Content-Type = %s, want text/event-stream", contentType)
	}

	// Check for connected event
	body, _ := io.ReadAll(rec.Body)
	if len(body) == 0 {
		t.Error("Expected some output from SSE stream")
	}
}

func TestCORSMiddleware(t *testing.T) {
	srv := New(Config{})
	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test preflight OPTIONS request
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Error("Expected CORS allowlist header")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("OPTIONS Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddlewareRejectsOrigin(t *testing.T) {
	srv := New(Config{})
	handler := srv.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestLoggingMiddleware(t *testing.T) {
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareAPIKey(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing api key status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid api key status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareOIDC(t *testing.T) {
	issuer := "https://issuer.example.com"
	audience := "ntm"
	key := mustGenerateKey(t)
	jwksURL := startJWKS(t, key, "kid1")
	token := signJWT(t, key, "kid1", issuer, audience, time.Now().Add(1*time.Hour))

	srv := New(Config{
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			OIDC: OIDCConfig{
				Issuer:   issuer,
				Audience: audience,
				JWKSURL:  jwksURL,
			},
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing oidc token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid oidc token status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAuthMiddlewareMTLS(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode: AuthModeMTLS,
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing mtls cert status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid mtls cert status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestWebSocketRejectedWithoutToken(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
	})
	handler := srv.authMiddleware(http.HandlerFunc(srv.handleWS))

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("ws unauth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCheckWSOriginAllowsConfiguredOrigin(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
		AllowedOrigins: []string{"https://ui.example.com"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	req.Header.Set("Origin", "https://ui.example.com")

	if !srv.checkWSOrigin(req) {
		t.Fatalf("expected origin to be allowed")
	}
}

func TestCheckWSOriginRejectsUnlistedOrigin(t *testing.T) {
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret",
		},
		AllowedOrigins: []string{"https://ui.example.com"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	if srv.checkWSOrigin(req) {
		t.Fatalf("expected origin to be rejected")
	}
}

func mustGenerateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func startJWKS(t *testing.T, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	payload := map[string]interface{}{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid, issuer, audience string, exp time.Time) string {
	t.Helper()
	header := map[string]string{
		"alg": "RS256",
		"kid": kid,
		"typ": "JWT",
	}
	claims := map[string]interface{}{
		"iss": issuer,
		"aud": audience,
		"exp": exp.Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	headerEnc := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsEnc := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerEnc + "." + claimsEnc
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	sigEnc := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigEnc
}

// =============================================================================
// API v1 Tests
// =============================================================================

func TestHealthV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["status"] != "healthy" {
		t.Error("Expected status=healthy")
	}
	if _, ok := resp["timestamp"]; !ok {
		t.Error("Expected timestamp field")
	}
}

func TestVersionV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if resp["api_version"] != "v1" {
		t.Error("Expected api_version=v1")
	}
}

func TestCapabilitiesV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	if _, ok := resp["auth_modes"]; !ok {
		t.Error("Expected auth_modes field")
	}
	if _, ok := resp["features"]; !ok {
		t.Error("Expected features field")
	}
}

func TestSessionsV1Endpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	// Verify sessions is an array (never null)
	sessions, ok := resp["sessions"].([]interface{})
	if !ok {
		t.Error("Expected sessions to be an array")
	}
	if sessions == nil {
		t.Error("Expected sessions to be non-nil array")
	}
}

// =============================================================================
// Jobs API Tests
// =============================================================================

func TestListJobsEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
	// Verify jobs is an array (never null)
	jobs, ok := resp["jobs"].([]interface{})
	if !ok {
		t.Error("Expected jobs to be an array")
	}
	if jobs == nil {
		t.Error("Expected jobs to be non-nil array")
	}
}

func TestCreateJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"type": "spawn", "session": "test-session"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}

	job, ok := resp["job"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected job object in response")
	}

	if job["type"] != "spawn" {
		t.Errorf("Job type = %v, want spawn", job["type"])
	}
	// Status can be "pending" or "running" due to the goroutine race
	status, _ := job["status"].(string)
	if status != "pending" && status != "running" {
		t.Errorf("Job status = %v, want pending or running", job["status"])
	}
}

func TestCreateJobInvalidType(t *testing.T) {
	srv, _ := setupTestServer(t)

	body := `{"type": "invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a job first
	job := srv.jobStore.Create("spawn")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestGetJobNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/nonexistent", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCancelJobEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Create a job first
	job := srv.jobStore.Create("spawn")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Verify job is cancelled
	updatedJob := srv.jobStore.Get(job.ID)
	if updatedJob.Status != JobStatusCancelled {
		t.Errorf("Job status = %v, want cancelled", updatedJob.Status)
	}
}

// =============================================================================
// Idempotency Tests
// =============================================================================

func TestIdempotencyStore(t *testing.T) {
	store := NewIdempotencyStore(time.Hour)

	// Test set and get
	store.Set("key1", []byte(`{"test": true}`), 200)

	resp, status, ok := store.Get("key1")
	if !ok {
		t.Fatal("Expected to find key1")
	}
	if status != 200 {
		t.Errorf("Status = %d, want 200", status)
	}
	if string(resp) != `{"test": true}` {
		t.Errorf("Response = %s, want {\"test\": true}", string(resp))
	}

	// Test non-existent key
	_, _, ok = store.Get("nonexistent")
	if ok {
		t.Error("Expected not to find nonexistent key")
	}
}

func TestIdempotencyMiddleware(t *testing.T) {
	srv, _ := setupTestServer(t)

	// First request
	body := `{"type": "spawn"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-123")
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("First request status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	// Capture the job ID from first response
	var firstResp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&firstResp); err != nil {
		t.Fatalf("Failed to decode first response: %v", err)
	}
	firstJob := firstResp["job"].(map[string]interface{})
	firstJobID := firstJob["id"].(string)

	// Second request with same idempotency key
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", "test-key-123")
	rec2 := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec2, req2)

	// Should get same response (replay)
	if rec2.Header().Get("X-Idempotent-Replay") != "true" {
		t.Error("Expected X-Idempotent-Replay header")
	}

	var secondResp map[string]interface{}
	if err := json.NewDecoder(rec2.Body).Decode(&secondResp); err != nil {
		t.Fatalf("Failed to decode second response: %v", err)
	}
	secondJob := secondResp["job"].(map[string]interface{})
	secondJobID := secondJob["id"].(string)

	// Should return same job ID
	if firstJobID != secondJobID {
		t.Errorf("Idempotent replay returned different job ID: %s vs %s", firstJobID, secondJobID)
	}
}

// =============================================================================
// Panic Recovery Tests
// =============================================================================

func TestRecovererMiddleware(t *testing.T) {
	srv := New(Config{})

	// Create a handler that panics
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Wrap with recoverer
	handler := srv.recovererMiddleware(panicHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	// Should not panic
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp["success"] != false {
		t.Error("Expected success=false")
	}
	if resp["error_code"] != "INTERNAL_ERROR" {
		t.Errorf("Error code = %v, want INTERNAL_ERROR", resp["error_code"])
	}
}

// =============================================================================
// WebSocket Hub Tests
// =============================================================================

func TestWSHub(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	// Check initial state
	if hub.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0", hub.ClientCount())
	}

	// Test nextSeq
	seq1 := hub.nextSeq()
	seq2 := hub.nextSeq()
	if seq2 != seq1+1 {
		t.Errorf("nextSeq not incrementing: got %d, want %d", seq2, seq1+1)
	}
}

func TestTopicMatching(t *testing.T) {
	tests := []struct {
		pattern string
		topic   string
		want    bool
	}{
		{"*", "anything", true},
		{"*", "sessions:test", true},
		{"global", "global", true},
		{"global", "sessions:test", false},
		{"sessions:*", "sessions:test", true},
		{"sessions:*", "sessions:my-session", true},
		{"sessions:*", "panes:test:0", false},
		{"panes:*", "panes:test:0", true},
		{"panes:*", "panes:other:5", true},
		{"panes:test:*", "panes:test:0", true},
		{"panes:test:*", "panes:other:0", false},
		{"sessions:test", "sessions:test", true},
		{"sessions:test", "sessions:other", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.topic, func(t *testing.T) {
			got := matchTopic(tt.pattern, tt.topic)
			if got != tt.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
			}
		})
	}
}

func TestWSClientSubscription(t *testing.T) {
	client := &WSClient{
		id:     "test-client",
		topics: make(map[string]struct{}),
	}

	// Initially no subscriptions
	if client.isSubscribed("global") {
		t.Error("Expected not subscribed to global")
	}

	// Subscribe to global
	client.Subscribe([]string{"global", "sessions:test"})
	if !client.isSubscribed("global") {
		t.Error("Expected subscribed to global")
	}
	if !client.isSubscribed("sessions:test") {
		t.Error("Expected subscribed to sessions:test")
	}

	// Unsubscribe
	client.Unsubscribe([]string{"global"})
	if client.isSubscribed("global") {
		t.Error("Expected not subscribed to global after unsubscribe")
	}
	if !client.isSubscribed("sessions:test") {
		t.Error("Expected still subscribed to sessions:test")
	}

	// Test wildcard subscription
	client.Subscribe([]string{"panes:*"})
	if !client.isSubscribed("panes:test:0") {
		t.Error("Expected wildcard to match panes:test:0")
	}
	if !client.isSubscribed("panes:other:5") {
		t.Error("Expected wildcard to match panes:other:5")
	}
}

func TestIsValidTopic(t *testing.T) {
	valid := []string{
		"*",
		"global",
		"global:*",
		"sessions:*",
		"sessions:my-session",
		"panes:*",
		"panes:test:0",
		"agent:claude",
	}

	invalid := []string{
		"",
		"invalid",
		"random:topic",
		"foo:bar:baz",
	}

	for _, topic := range valid {
		if !isValidTopic(topic) {
			t.Errorf("isValidTopic(%q) = false, want true", topic)
		}
	}

	for _, topic := range invalid {
		if isValidTopic(topic) {
			t.Errorf("isValidTopic(%q) = true, want false", topic)
		}
	}
}

func TestWSHubPublish(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	// Give hub time to start
	time.Sleep(10 * time.Millisecond)

	// Publish should not block even with no clients
	hub.Publish("global:events", "test_event", map[string]interface{}{
		"message": "hello",
	})

	// Give time for event to be processed
	time.Sleep(20 * time.Millisecond)

	// Verify sequence incremented (happens in broadcastEvent)
	hub.seqMu.Lock()
	seq := hub.seq
	hub.seqMu.Unlock()
	if seq != 1 {
		t.Errorf("seq = %d, want 1", seq)
	}
}

// =============================================================================
// WebSocket Streaming Tests (bd-3ef1l)
// Tests for streaming event delivery and client subscription behavior
// =============================================================================

func TestWSHub_PaneOutputSequence(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Publish multiple pane output events
	for i := 0; i < 10; i++ {
		hub.Publish("panes:test:0", "pane.output", map[string]interface{}{
			"lines": []string{"output line " + string(rune('A'+i))},
			"seq":   i,
		})
	}

	time.Sleep(50 * time.Millisecond)

	// Verify sequence numbers
	hub.seqMu.Lock()
	finalSeq := hub.seq
	hub.seqMu.Unlock()

	if finalSeq != 10 {
		t.Errorf("expected final seq 10, got %d", finalSeq)
	}

	t.Logf("WS_STREAMING_TEST: hub assigned sequences 1-%d for 10 pane output events", finalSeq)
}

func TestWSHub_MultiClientFanOut(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create simulated clients
	clients := make([]*WSClient, 3)
	for i := 0; i < 3; i++ {
		clients[i] = &WSClient{
			id:     "client-" + string(rune('A'+i)),
			hub:    hub,
			send:   make(chan []byte, 100),
			topics: make(map[string]struct{}),
		}
		// Subscribe to pane output
		clients[i].Subscribe([]string{"panes:*"})

		// Register with hub
		hub.register <- clients[i]
	}

	time.Sleep(20 * time.Millisecond)

	// Verify client count
	if hub.ClientCount() != 3 {
		t.Errorf("expected 3 clients, got %d", hub.ClientCount())
	}

	// Publish an event
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{
		"lines": []string{"test output"},
	})

	time.Sleep(50 * time.Millisecond)

	// Each client should receive the message
	for i, client := range clients {
		select {
		case msg := <-client.send:
			if len(msg) == 0 {
				t.Errorf("client %d received empty message", i)
			}
			t.Logf("WS_STREAMING_TEST: client %s received %d bytes", client.id, len(msg))
		default:
			t.Errorf("client %d did not receive message", i)
		}
	}

	// Cleanup
	for _, client := range clients {
		hub.unregister <- client
	}
}

func TestWSHub_TopicFiltering(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create two clients with different subscriptions
	paneClient := &WSClient{
		id:     "pane-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	paneClient.Subscribe([]string{"panes:*"})
	hub.register <- paneClient

	sessionClient := &WSClient{
		id:     "session-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	sessionClient.Subscribe([]string{"sessions:*"})
	hub.register <- sessionClient

	time.Sleep(20 * time.Millisecond)

	// Publish a pane event
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{"data": "pane"})

	// Publish a session event
	hub.Publish("sessions:test", "session.started", map[string]interface{}{"data": "session"})

	time.Sleep(50 * time.Millisecond)

	// Pane client should only receive pane event
	paneCount := 0
	for {
		select {
		case <-paneClient.send:
			paneCount++
		default:
			goto donePane
		}
	}
donePane:

	// Session client should only receive session event
	sessionCount := 0
	for {
		select {
		case <-sessionClient.send:
			sessionCount++
		default:
			goto doneSession
		}
	}
doneSession:

	if paneCount != 1 {
		t.Errorf("pane client expected 1 message, got %d", paneCount)
	}
	if sessionCount != 1 {
		t.Errorf("session client expected 1 message, got %d", sessionCount)
	}

	t.Logf("WS_STREAMING_TEST: topic filtering verified, pane_client=%d session_client=%d", paneCount, sessionCount)

	// Cleanup
	hub.unregister <- paneClient
	hub.unregister <- sessionClient
}

func TestWSHub_ClientBufferFull(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Create client with tiny buffer (1)
	slowClient := &WSClient{
		id:     "slow-client",
		hub:    hub,
		send:   make(chan []byte, 1), // Very small buffer
		topics: make(map[string]struct{}),
	}
	slowClient.Subscribe([]string{"panes:*"})
	hub.register <- slowClient

	time.Sleep(20 * time.Millisecond)

	// Publish many events quickly - some should be dropped due to full buffer
	for i := 0; i < 50; i++ {
		hub.Publish("panes:test:0", "pane.output", map[string]interface{}{"idx": i})
	}

	time.Sleep(100 * time.Millisecond)

	// Count received messages
	received := 0
	for {
		select {
		case <-slowClient.send:
			received++
		default:
			goto done
		}
	}
done:

	// Should receive some but not all (buffer was full)
	if received >= 50 {
		t.Errorf("expected some messages to be dropped, but received all %d", received)
	}
	if received == 0 {
		t.Error("expected at least some messages to be received")
	}

	t.Logf("WS_STREAMING_TEST: backpressure test - sent 50, received %d (dropped %d)", received, 50-received)

	hub.unregister <- slowClient
}

func TestWSHub_GlobalWildcard(t *testing.T) {
	hub := NewWSHub()
	go hub.Run()
	defer hub.Stop()

	time.Sleep(10 * time.Millisecond)

	// Client subscribed to everything
	globalClient := &WSClient{
		id:     "global-watcher",
		hub:    hub,
		send:   make(chan []byte, 100),
		topics: make(map[string]struct{}),
	}
	globalClient.Subscribe([]string{"*"})
	hub.register <- globalClient

	time.Sleep(20 * time.Millisecond)

	// Publish to different topics
	hub.Publish("panes:test:0", "pane.output", map[string]interface{}{})
	hub.Publish("sessions:test", "session.started", map[string]interface{}{})
	hub.Publish("global:events", "system.event", map[string]interface{}{})

	time.Sleep(50 * time.Millisecond)

	// Global client should receive all 3
	received := 0
	for {
		select {
		case <-globalClient.send:
			received++
		default:
			goto done
		}
	}
done:

	if received != 3 {
		t.Errorf("global wildcard client expected 3 messages, got %d", received)
	}

	t.Logf("WS_STREAMING_TEST: global wildcard subscription received all %d events", received)

	hub.unregister <- globalClient
}

func TestMatchTopic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		topic   string
		want    bool
	}{
		{"wildcard matches anything", "*", "sessions:foo", true},
		{"wildcard matches empty-like", "*", "x", true},
		{"exact match", "global", "global", true},
		{"exact mismatch", "global", "sessions:foo", false},
		{"prefix wildcard match", "sessions:*", "sessions:my-session", true},
		{"prefix wildcard mismatch", "sessions:*", "panes:foo", false},
		{"prefix wildcard exact prefix", "panes:*", "panes:test:0", true},
		{"empty pattern no match", "", "global", false},
		{"empty topic no match", "global", "", false},
		{"both empty", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchTopic(tc.pattern, tc.topic)
			if got != tc.want {
				t.Errorf("matchTopic(%q, %q) = %v, want %v", tc.pattern, tc.topic, got, tc.want)
			}
		})
	}
}

func TestSanitizeRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"alphanumeric", "abc123", "abc123"},
		{"with dashes", "req-123-abc", "req-123-abc"},
		{"with underscores", "req_123_abc", "req_123_abc"},
		{"with dots", "req.123.abc", "req.123.abc"},
		{"with colons", "req:123", "req:123"},
		{"with slashes", "req/path", "req/path"},
		{"strips special chars", "req<script>alert", "reqscriptalert"},
		{"strips spaces", "req 123", "req123"},
		{"truncates long input", strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeRequestID(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"empty", "", true},
		{"localhost", "localhost", true},
		{"LOCALHOST", "LOCALHOST", true},
		{"127.0.0.1", "127.0.0.1", true},
		{"::1", "::1", true},
		{"bracketed ::1", "[::1]", true},
		{"external IP", "192.168.1.1", false},
		{"domain", "example.com", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isLoopbackHost(tc.host)
			if got != tc.want {
				t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestOriginAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    string
		allowlist []string
		want      bool
	}{
		{"empty origin always allowed", "", []string{"example.com"}, true},
		{"empty allowlist rejects", "http://evil.com", []string{}, false},
		{"wildcard allows all", "http://evil.com", []string{"*"}, true},
		{"hostname match", "http://example.com", []string{"example.com"}, true},
		{"hostname mismatch", "http://evil.com", []string{"example.com"}, false},
		{"full URL match", "http://localhost:3000", []string{"http://localhost:3000"}, true},
		{"port mismatch in full URL", "http://localhost:3001", []string{"http://localhost:3000"}, false},
		{"host:port match", "http://localhost:8080", []string{"localhost:8080"}, true},
		{"case insensitive", "http://Example.Com", []string{"example.com"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := originAllowed(tc.origin, tc.allowlist)
			if got != tc.want {
				t.Errorf("originAllowed(%q, %v) = %v, want %v", tc.origin, tc.allowlist, got, tc.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "just now"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3h ago"},
		{"days", 48 * time.Hour, "2d ago"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := formatAge(tc.d)
			if got != tc.want {
				t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
