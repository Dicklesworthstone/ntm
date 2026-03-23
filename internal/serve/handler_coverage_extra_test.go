package serve

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// IdempotencyStore tests
// ---------------------------------------------------------------------------

func TestIdempotencyStoreStop(t *testing.T) {
	t.Parallel()
	store := NewIdempotencyStore(time.Hour)
	store.Set("key1", []byte(`{"ok":true}`), 200)

	// Stop should be safe to call multiple times
	store.Stop()
	store.Stop()
	store.Stop()

	// After stop, Get should still work (just no cleanup goroutine)
	data, code, ok := store.Get("key1")
	if !ok {
		t.Fatal("expected key1 to still be available after Stop")
	}
	if code != 200 {
		t.Errorf("status = %d, want 200", code)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("data = %s", data)
	}
}

func TestIdempotencyStoreExpiry(t *testing.T) {
	t.Parallel()
	store := NewIdempotencyStore(10 * time.Millisecond)
	defer store.Stop()

	store.Set("ephemeral", []byte(`{}`), 200)
	time.Sleep(20 * time.Millisecond)

	_, _, ok := store.Get("ephemeral")
	if ok {
		t.Error("expected expired entry to not be found")
	}
}

func TestNewIdempotencyStore_DoesNotSpawnCleanupGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()

	stores := make([]*IdempotencyStore, 0, 64)
	for i := 0; i < cap(stores); i++ {
		stores = append(stores, NewIdempotencyStore(time.Hour))
	}
	for _, store := range stores {
		defer store.Stop()
	}

	time.Sleep(20 * time.Millisecond)

	after := runtime.NumGoroutine()
	if after > before+8 {
		t.Fatalf("NewIdempotencyStore spawned unexpected background goroutines: before=%d after=%d", before, after)
	}
}

func TestNewServer_DoesNotSpawnIdempotencyCleanupGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()

	servers := make([]*Server, 0, 32)
	for i := 0; i < cap(servers); i++ {
		servers = append(servers, New(Config{}))
	}
	for _, srv := range servers {
		defer srv.Stop()
	}

	time.Sleep(20 * time.Millisecond)

	after := runtime.NumGoroutine()
	if after > before+8 {
		t.Fatalf("New(Config{}) spawned unexpected background goroutines: before=%d after=%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// validate() tests
// ---------------------------------------------------------------------------

func TestServerValidate(t *testing.T) {
	t.Parallel()

	t.Run("defaults pass", func(t *testing.T) {
		t.Parallel()
		srv := New(Config{})
		if err := srv.validate(); err != nil {
			t.Fatalf("validate() = %v", err)
		}
	})

	t.Run("api key mode without key", func(t *testing.T) {
		t.Parallel()
		srv := New(Config{
			Auth: AuthConfig{Mode: AuthModeAPIKey},
		})
		err := srv.validate()
		if err == nil || !strings.Contains(err.Error(), "api_key requires") {
			t.Fatalf("validate() = %v, want api_key error", err)
		}
	})

	t.Run("api key mode with key", func(t *testing.T) {
		t.Parallel()
		srv := New(Config{
			Auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "test-key"},
		})
		if err := srv.validate(); err != nil {
			t.Fatalf("validate() = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// buildMTLSConfig() tests
// ---------------------------------------------------------------------------

func TestBuildMTLSConfigMissingFiles(t *testing.T) {
	t.Parallel()
	srv := New(Config{})
	srv.auth.MTLS.CertFile = ""
	srv.auth.MTLS.KeyFile = ""
	srv.auth.MTLS.ClientCAFile = ""
	_, err := srv.buildMTLSConfig()
	if err == nil {
		t.Fatal("expected error for missing mTLS files")
	}
}

func TestBuildMTLSConfigBadCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	os.WriteFile(caFile, []byte("not a certificate"), 0644)

	srv := New(Config{})
	srv.auth.MTLS.CertFile = "cert.pem"
	srv.auth.MTLS.KeyFile = "key.pem"
	srv.auth.MTLS.ClientCAFile = caFile
	_, err := srv.buildMTLSConfig()
	if err == nil || !strings.Contains(err.Error(), "no certs found") {
		t.Fatalf("expected 'no certs found' error, got %v", err)
	}
}

func TestBuildMTLSConfigValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Generate a self-signed CA certificate
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Test"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(dir, "ca.pem")
	os.WriteFile(caFile, caPEM, 0644)

	srv := New(Config{})
	srv.auth.MTLS.CertFile = "cert.pem"
	srv.auth.MTLS.KeyFile = "key.pem"
	srv.auth.MTLS.ClientCAFile = caFile
	cfg, err := srv.buildMTLSConfig()
	if err != nil {
		t.Fatalf("buildMTLSConfig() = %v", err)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("expected ClientCAs to be set")
	}
}

// ---------------------------------------------------------------------------
// requestIDMiddleware (deprecated package-level version) tests
// ---------------------------------------------------------------------------

func TestRequestIDMiddlewareDeprecated(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := requestIDFromContext(r.Context())
		w.Write([]byte(reqID))
	})
	handler := requestIDMiddleware(inner)

	t.Run("generates ID when missing", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Header().Get(requestIDHeader) == "" {
			t.Error("expected request ID in response header")
		}
		if rec.Body.Len() == 0 {
			t.Error("expected request ID in body")
		}
	})

	t.Run("preserves existing ID", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(requestIDHeader, "test-req-123")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get(requestIDHeader); got != "test-req-123" {
			t.Errorf("request ID = %q, want test-req-123", got)
		}
	})
}

// ---------------------------------------------------------------------------
// idempotencyMiddleware tests
// ---------------------------------------------------------------------------

func TestIdempotencyMiddlewareReplay(t *testing.T) {
	t.Parallel()

	srv, _ := setupTestServer(t)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"computed"}`))
	})
	handler := srv.idempotencyMiddleware(inner)

	t.Run("no key passes through", func(t *testing.T) {
		callCount = 0
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if callCount != 1 {
			t.Errorf("callCount = %d, want 1", callCount)
		}
	})

	t.Run("GET bypasses idempotency", func(t *testing.T) {
		callCount = 0
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Idempotency-Key", "test-key-get")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if callCount != 1 {
			t.Errorf("callCount = %d, want 1", callCount)
		}
	})

	t.Run("replay on second call", func(t *testing.T) {
		callCount = 0
		key := "test-key-replay"

		// First call
		req1 := httptest.NewRequest(http.MethodPost, "/", nil)
		req1.Header.Set("Idempotency-Key", key)
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)
		if callCount != 1 {
			t.Fatalf("first call: callCount = %d, want 1", callCount)
		}

		// Second call - should replay
		req2 := httptest.NewRequest(http.MethodPost, "/", nil)
		req2.Header.Set("Idempotency-Key", key)
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		if callCount != 1 {
			t.Errorf("second call: callCount = %d, want 1 (replayed)", callCount)
		}
		if rec2.Header().Get("X-Idempotent-Replay") != "true" {
			t.Error("expected X-Idempotent-Replay header")
		}
	})

	t.Run("PATCH replays on second call", func(t *testing.T) {
		callCount = 0
		key := "test-key-patch"

		req1 := httptest.NewRequest(http.MethodPatch, "/", nil)
		req1.Header.Set("Idempotency-Key", key)
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)
		if callCount != 1 {
			t.Fatalf("first PATCH call: callCount = %d, want 1", callCount)
		}

		req2 := httptest.NewRequest(http.MethodPatch, "/", nil)
		req2.Header.Set("Idempotency-Key", key)
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		if callCount != 1 {
			t.Errorf("second PATCH call: callCount = %d, want 1 (replayed)", callCount)
		}
		if rec2.Header().Get("X-Idempotent-Replay") != "true" {
			t.Error("expected X-Idempotent-Replay header for PATCH replay")
		}
	})
}

// ---------------------------------------------------------------------------
// responseRecorder tests
// ---------------------------------------------------------------------------

func TestResponseRecorder(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	rec := &responseRecorder{ResponseWriter: inner, statusCode: http.StatusOK}

	rec.WriteHeader(http.StatusCreated)
	if rec.statusCode != http.StatusCreated {
		t.Errorf("statusCode = %d, want 201", rec.statusCode)
	}

	n, err := rec.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 5 {
		t.Errorf("Write n = %d, want 5", n)
	}
	if string(rec.body) != "hello" {
		t.Errorf("body = %q, want hello", string(rec.body))
	}
}

// ---------------------------------------------------------------------------
// Simple handler tests (no external deps)
// ---------------------------------------------------------------------------

func TestHandleGetConfigV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.projectDir = "/test/project"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	rec := httptest.NewRecorder()
	srv.handleGetConfigV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["success"] != true {
		t.Fatalf("success = %v", resp["success"])
	}
	if resp["project_dir"] != "/test/project" {
		t.Errorf("project_dir = %v", resp["project_dir"])
	}
	if resp["auth_mode"] == nil {
		t.Error("auth_mode should be present")
	}
}

func TestHandlePatchConfigV1(t *testing.T) {
	srv, _ := setupTestServer(t)

	t.Run("update allowed origins", func(t *testing.T) {
		body := `{"allowed_origins":["http://new.example.com"]}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handlePatchConfigV1(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp map[string]interface{}
		json.NewDecoder(rec.Body).Decode(&resp)
		if resp["updated"] != true {
			t.Error("expected updated=true")
		}
	})

	t.Run("update project dir", func(t *testing.T) {
		body := `{"project_dir":"/new/dir"}`
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.handlePatchConfigV1(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if srv.projectDir != "/new/dir" {
			t.Errorf("projectDir = %q, want /new/dir", srv.projectDir)
		}
	})

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader("not json"))
		rec := httptest.NewRecorder()
		srv.handlePatchConfigV1(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func TestHandleRobotStatusV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/robot/status", nil)
	rec := httptest.NewRecorder()
	srv.handleRobotStatusV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleRobotHealthV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/robot/health", nil)
	rec := httptest.NewRecorder()
	srv.handleRobotHealthV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleDoctorV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/doctor", nil)
	rec := httptest.NewRecorder()
	srv.handleDoctorV1(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["success"] != true {
		t.Fatal("expected success=true")
	}
	if resp["timestamp"] == nil {
		t.Error("expected timestamp")
	}
}

// ---------------------------------------------------------------------------
// Session handler tests (using state store)
// ---------------------------------------------------------------------------

func TestHandleSessionV1NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionV1(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleSessionV1EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleSessionV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionAgentsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/agents", nil)
	rec := httptest.NewRecorder()

	srv.handleSessionAgentsV1(rec, req, "test-session")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["count"] == nil {
		t.Error("expected count field")
	}
}

func TestHandleSessionEventsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/events", nil)
	rec := httptest.NewRecorder()

	srv.handleSessionEventsV1(rec, req, "test-session")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["count"] == nil {
		t.Error("expected count field")
	}
}

// ---------------------------------------------------------------------------
// Beads handlers - unavailable path tests
// ---------------------------------------------------------------------------

func TestHandleBeadsTriage_BVUnavailable(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/triage?limit=5", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsTriage(rec, req)

	// 200 (bv installed + working), 500 (installed but errored), or 503 (unavailable)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 500, or 503", rec.Code)
	}
}

func TestHandleBeadsInsights_BVUnavailable(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/insights", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsInsights(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 500, or 503", rec.Code)
	}
}

func TestHandleBeadsPlan(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/plan", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsPlan(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 500, or 503", rec.Code)
	}
}

func TestHandleBeadsPriority(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/priority", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsPriority(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 500, or 503", rec.Code)
	}
}

func TestHandleBeadsRecipes(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/recipes", nil)
	rec := httptest.NewRecorder()
	srv.handleBeadsRecipes(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200, 500, or 503", rec.Code)
	}
}

func TestHandleListBeadDeps(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/beads/bd-123/deps", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleListBeadDeps(rec, req)
	// Either 200 (bd installed) or 503 (unavailable)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200/500/503", rec.Code)
	}
}

func TestHandleAddBeadDep_BdUnavailable(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"blocked_by":"bd-456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-123/deps", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAddBeadDep(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleAddBeadDep_MissingBlockedBy(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"blocked_by":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/beads/bd-123/deps", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleAddBeadDep(rec, req)
	// Either 400 (validation error) or 503 (bd unavailable)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 400 or 503", rec.Code)
	}
}

func TestHandleRemoveBeadDep(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/beads/bd-123/deps/bd-456", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bd-123")
	rctx.URLParams.Add("depId", "bd-456")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleRemoveBeadDep(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// CASS handler tests
// ---------------------------------------------------------------------------

func TestHandleCASSStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/status", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["success"] != true {
		t.Fatal("expected success=true")
	}
	// "installed" field should be present regardless
	if _, ok := resp["installed"]; !ok {
		t.Error("expected 'installed' field")
	}
}

func TestHandleCASSCapabilities(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/capabilities", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSCapabilities(rec, req)

	// Either 200 (cass installed) or 503 (unavailable)
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 200 or 503", rec.Code)
	}
}

func TestHandleCASSSearch_BadRequest(t *testing.T) {
	srv, _ := setupTestServer(t)

	t.Run("invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/search", strings.NewReader("not json"))
		rec := httptest.NewRecorder()
		srv.handleCASSSearch(rec, req)
		// 400 (bad request) or 503 (cass not installed)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 400 or 503", rec.Code)
		}
	})

	t.Run("empty query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/search", strings.NewReader(`{"query":""}`))
		rec := httptest.NewRecorder()
		srv.handleCASSSearch(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 400 or 503", rec.Code)
		}
	})
}

func TestHandleCASSInsights(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/insights", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSInsights(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleCASSTimeline(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cass/timeline", nil)
	rec := httptest.NewRecorder()
	srv.handleCASSTimeline(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleCASSPreview(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cass/preview", strings.NewReader(`{"query":"test"}`))
	rec := httptest.NewRecorder()
	srv.handleCASSPreview(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Memory daemon handler tests
// ---------------------------------------------------------------------------

func TestHandleMemoryDaemonStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/daemon/status", nil)
	rec := httptest.NewRecorder()
	srv.handleMemoryDaemonStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["success"] != true {
		t.Fatal("expected success=true")
	}
}

func TestHandleMemoryDaemonStop_NotRunning(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/daemon/stop", nil)
	rec := httptest.NewRecorder()
	srv.handleMemoryDaemonStop(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleMemoryPrivacyGet(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/privacy", nil)
	rec := httptest.NewRecorder()
	srv.handleMemoryPrivacyGet(rec, req)

	// Either 200 (cm installed) or some other status
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleMemoryPrivacyUpdate(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"enabled":true,"anonymize_paths":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/memory/privacy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleMemoryPrivacyUpdate(rec, req)

	// Depends on cm availability
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleMemoryRules(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memory/rules", nil)
	rec := httptest.NewRecorder()
	srv.handleMemoryRules(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Checkpoint handler tests
// ---------------------------------------------------------------------------

func TestHandleListCheckpoints(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test-session/checkpoints/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleListCheckpoints(rec, req)
	// Checkpoints use filesystem storage - may succeed or fail based on environment
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 200 or 500", rec.Code)
	}
}

func TestHandleGetCheckpoint_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/checkpoints/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent-id")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleGetCheckpoint(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 404 or 500", rec.Code)
	}
}

func TestHandleDeleteCheckpoint(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/test/checkpoints/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionName", "test-session")
	rctx.URLParams.Add("checkpointId", "nonexistent-id")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	srv.handleDeleteCheckpoint(rec, req)
	// Should get 404 or 500 for nonexistent checkpoint
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError && rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// performDoctorCheckAPI test
// ---------------------------------------------------------------------------

func TestPerformDoctorCheckAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	report := performDoctorCheckAPI(ctx)

	if report["timestamp"] == nil {
		t.Error("expected timestamp in report")
	}
	if report["overall"] == nil {
		t.Error("expected overall status in report")
	}
}

// ---------------------------------------------------------------------------
// Session kernel handler validation tests
// ---------------------------------------------------------------------------

func TestHandleCreateSessionV1_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	srv.handleCreateSessionV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateSessionV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"session":""}`))
	rec := httptest.NewRecorder()
	srv.handleCreateSessionV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionStatusV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//status", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionStatusV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionAttachV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//attach", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionAttachV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionZoomV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//zoom", strings.NewReader(`{"pane":0}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionZoomV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionZoomV1_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test/zoom", strings.NewReader("bad json"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionZoomV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSessionViewV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//view", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSessionViewV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Pane handler validation tests
// ---------------------------------------------------------------------------

func TestHandleListPanesV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//panes/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleListPanesV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetPaneV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//panes/0", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetPaneV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetPaneV1_InvalidIndex(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/test/panes/abc", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test")
	rctx.URLParams.Add("paneIdx", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetPaneV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneInputV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//panes/0/input", strings.NewReader(`{"text":"hello"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handlePaneInputV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePaneInterruptV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//panes/0/interrupt", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handlePaneInterruptV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetPaneTitleV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//panes/0/title", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetPaneTitleV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetPaneTitleV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions//panes/0/title", strings.NewReader(`{"title":"new"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	rctx.URLParams.Add("paneIdx", "0")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleSetPaneTitleV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Agent handler validation tests
// ---------------------------------------------------------------------------

func TestHandleListAgentsV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleListAgentsV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentSpawnV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//agents/spawn", strings.NewReader(`{"cc_count":1}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentSpawnV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentSpawnV1_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test/agents/spawn", strings.NewReader("bad"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentSpawnV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentSpawnV1_NoAgents(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/test/agents/spawn", strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "test")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentSpawnV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentSendV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//agents/send", strings.NewReader(`{"message":"hello"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentSendV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentInterruptV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//agents/interrupt", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentInterruptV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAgentWaitV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//agents/wait", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentWaitV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleAgentRouteV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents/route", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentRouteV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleAgentActivityV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents/activity", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentActivityV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleAgentHealthV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents/health", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentHealthV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleAgentContextV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions//agents/context", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentContextV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleAgentRestartV1_EmptySession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions//agents/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sessionId", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAgentRestartV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Metrics handler tests
// ---------------------------------------------------------------------------

func skip_TestHandleMetricsSnapshotListV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/snapshots", nil)
	rec := httptest.NewRecorder()
	srv.handleMetricsSnapshotListV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func skip_TestHandleMetricsSnapshotSaveV1_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/snapshot", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.handleMetricsSnapshotSaveV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleMetricsSnapshotSaveV1_EmptyName(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/snapshot", strings.NewReader(`{"name":""}`))
	rec := httptest.NewRecorder()
	srv.handleMetricsSnapshotSaveV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleMetricsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics?session=test&period=1h", nil)
	rec := httptest.NewRecorder()
	srv.handleMetricsV1(rec, req)
	// robot.GetMetrics may fail without tmux, accept 200 or 500
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func skip_TestHandleAnalyticsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/analytics?days=7", nil)
	rec := httptest.NewRecorder()
	srv.handleAnalyticsV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func skip_TestHandleMetricsExportV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/export?format=json", nil)
	rec := httptest.NewRecorder()
	srv.handleMetricsExportV1(rec, req)
	// May succeed or fail depending on state store content
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Output handler validation tests
// ---------------------------------------------------------------------------

func skip_TestHandleOutputTailV1_MissingSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/tail", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputTailV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleOutputDiffV1_MissingSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/diff", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputDiffV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleOutputSummaryV1_MissingSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/summary", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputSummaryV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleOutputFilesV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/output/files?session=test", nil)
	rec := httptest.NewRecorder()
	srv.handleOutputFilesV1(rec, req)
	// robot.GetFiles may fail without tmux
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Context handler validation tests
// ---------------------------------------------------------------------------

func skip_TestHandleContextBuildV1_InvalidBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/context/build", strings.NewReader("bad"))
	rec := httptest.NewRecorder()
	srv.handleContextBuildV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func skip_TestHandleContextCacheClearV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/context/cache", nil)
	rec := httptest.NewRecorder()
	srv.handleContextCacheClearV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Git handler tests
// ---------------------------------------------------------------------------

func skip_TestHandleGitStatusV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.projectDir = t.TempDir()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/git/status", nil)
	rec := httptest.NewRecorder()
	srv.handleGitStatusV1(rec, req)
	// May succeed or fail based on git availability
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Palette and History handler tests
// ---------------------------------------------------------------------------

func skip_TestHandlePaletteV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/palette?category=agent&search=claude", nil)
	rec := httptest.NewRecorder()
	srv.handlePaletteV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func skip_TestHandleHistoryV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?session=test&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.handleHistoryV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func skip_TestHandleHistoryStatsV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history/stats?session=test", nil)
	rec := httptest.NewRecorder()
	srv.handleHistoryStatsV1(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ===========================================================================
// Safety handler tests
// ===========================================================================

func TestHandleSafetyStatusV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/safety/status", nil)
	rec := httptest.NewRecorder()
	srv.handleSafetyStatusV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleSafetyBlockedV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/safety/blocked?hours=1&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.handleSafetyBlockedV1(rec, req)
	// May fail if blocked log directory doesn't exist
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleSafetyCheckV1_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check",
		strings.NewReader(`{bad json`))
	rec := httptest.NewRecorder()
	srv.handleSafetyCheckV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSafetyCheckV1_EmptyCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check",
		strings.NewReader(`{"command":""}`))
	rec := httptest.NewRecorder()
	srv.handleSafetyCheckV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSafetyCheckV1_ValidCommand(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/check",
		strings.NewReader(`{"command":"ls -la"}`))
	rec := httptest.NewRecorder()
	srv.handleSafetyCheckV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyGetV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy?rules=true", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyGetV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyGetV1_WithContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy?rules=true&content=true", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyGetV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	content, _ := resp["content"].(string)
	if content == "" {
		t.Fatalf("content missing from response body: %s", rec.Body.String())
	}
	if !strings.Contains(content, "version:") {
		t.Fatalf("content = %q, want generated YAML", content)
	}
}

func TestHandlePolicyUpdateV1_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handlePolicyUpdateV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePolicyUpdateV1_EmptyContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy",
		strings.NewReader(`{"content":""}`))
	rec := httptest.NewRecorder()
	srv.handlePolicyUpdateV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePolicyValidateV1_WithContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	content := `{"content":"version: 1\nblocked:\n  - pattern: 'rm -rf /'\n    reason: dangerous"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate",
		strings.NewReader(content))
	req.Header.Set("Content-Length", "100")
	rec := httptest.NewRecorder()
	srv.handlePolicyValidateV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyValidateV1_NoContent(t *testing.T) {
	srv, _ := setupTestServer(t)
	// No body - should validate file-based policy
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/validate", nil)
	req.ContentLength = 0
	rec := httptest.NewRecorder()
	srv.handlePolicyValidateV1(rec, req)
	// Returns 200 with valid/invalid status (never errors with HTTP error)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyResetV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policy/reset", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyResetV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyAutomationGetV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/policy/automation", nil)
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationGetV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlePolicyAutomationUpdateV1_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation",
		strings.NewReader(`{bad json`))
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationUpdateV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePolicyAutomationUpdateV1_InvalidForceRelease(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policy/automation",
		strings.NewReader(`{"force_release":"bogus"}`))
	rec := httptest.NewRecorder()
	srv.handlePolicyAutomationUpdateV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Approval handler tests
// ---------------------------------------------------------------------------

func resetApprovalsForTest() {
	approvalsLock.Lock()
	defer approvalsLock.Unlock()
	for k := range approvals {
		delete(approvals, k)
	}
	approvalIDSeq = 0
}

func TestHandleApprovalsListV1(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals?status=pending", nil)
	rec := httptest.NewRecorder()
	srv.handleApprovalsListV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleApprovalsHistoryV1(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/history?limit=5", nil)
	rec := httptest.NewRecorder()
	srv.handleApprovalsHistoryV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleApprovalGetV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalGetV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleApprovalGetV1_NotFound(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/apr-999", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalGetV1(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleApprovalApproveV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals//approve", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalApproveV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleApprovalApproveV1_NotFound(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/apr-999/approve", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalApproveV1(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleApprovalDenyV1_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals//deny", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalDenyV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleApprovalDenyV1_NotFound(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/apr-999/deny", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "apr-999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleApprovalDenyV1(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleApprovalRequestV1_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/request",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleApprovalRequestV1_EmptyAction(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/request",
		strings.NewReader(`{"action":""}`))
	rec := httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleApprovalRequestV1_Valid(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/request",
		strings.NewReader(`{"action":"rm -rf /tmp/test","resource":"/tmp/test","reason":"cleanup","ttl_seconds":60}`))
	rec := httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ===========================================================================
// Pipeline handler tests
// ===========================================================================

func TestHandleListPipelines(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil)
	rec := httptest.NewRecorder()
	srv.handleListPipelines(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleRunPipeline_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/run",
		strings.NewReader(`{bad json`))
	rec := httptest.NewRecorder()
	srv.handleRunPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRunPipeline_MissingWorkflow(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/run",
		strings.NewReader(`{"session":"test"}`))
	rec := httptest.NewRecorder()
	srv.handleRunPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRunPipeline_MissingSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/run",
		strings.NewReader(`{"workflow_file":"test.yaml"}`))
	rec := httptest.NewRecorder()
	srv.handleRunPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleExecPipeline_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/exec",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleExecPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleExecPipeline_MissingSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/exec",
		strings.NewReader(`{"workflow":{"name":"test","steps":[]},"session":""}`))
	rec := httptest.NewRecorder()
	srv.handleExecPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetPipeline_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetPipeline_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetPipeline(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleCancelPipeline_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pipelines/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleCancelPipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCancelPipeline_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pipelines/nonexistent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleCancelPipeline(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleResumePipeline_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines//resume",
		strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleResumePipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleResumePipeline_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/nonexistent/resume",
		strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleResumePipeline(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleValidatePipeline_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleValidatePipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleValidatePipeline_MissingBothFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/validate",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.handleValidatePipeline(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleListPipelineTemplates(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/templates", nil)
	rec := httptest.NewRecorder()
	srv.handleListPipelineTemplates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandleCleanupPipelines(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/cleanup",
		strings.NewReader(`{"older_than_hours":48}`))
	rec := httptest.NewRecorder()
	srv.handleCleanupPipelines(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleCleanupPipelines_EmptyBody(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/cleanup",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.handleCleanupPipelines(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ===========================================================================
// Mail handler tests
// ===========================================================================

func TestHandleMailHealth(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/health", nil)
	rec := httptest.NewRecorder()
	srv.handleMailHealth(rec, req)
	// Returns 200 with available:true or available:false
	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleCreateMailAgent_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleCreateMailAgent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateMailAgent_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/agents",
		strings.NewReader(`{"program":"","model":""}`))
	rec := httptest.NewRecorder()
	srv.handleCreateMailAgent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetMailAgent_EmptyName(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/agents/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetMailAgent(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMailInbox_MissingAgentName(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox", nil)
	rec := httptest.NewRecorder()
	srv.handleMailInbox(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSendMessage_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleSendMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSendMessage_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages",
		strings.NewReader(`{"sender_name":"","to":[],"subject":"","body_md":""}`))
	rec := httptest.NewRecorder()
	srv.handleSendMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetMessage_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/abc", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReplyMessage_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/abc/reply",
		strings.NewReader(`{"sender_name":"test","body_md":"hello"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleReplyMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReplyMessage_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/123/reply",
		strings.NewReader(`{bad`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleReplyMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReplyMessage_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/123/reply",
		strings.NewReader(`{"sender_name":"","body_md":""}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleReplyMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMarkMessageRead_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/abc/read", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleMarkMessageRead(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMarkMessageRead_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/123/read", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleMarkMessageRead(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAckMessage_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/abc/ack", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAckMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAckMessage_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/messages/123/ack", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleAckMessage(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSearchMessages_MissingQuery(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/search", nil)
	rec := httptest.NewRecorder()
	srv.handleSearchMessages(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleThreadSummary_EmptyID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/threads//summary", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleThreadSummary(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleListContacts_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/contacts", nil)
	rec := httptest.NewRecorder()
	srv.handleListContacts(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRequestContact_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleRequestContact(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRequestContact_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/request",
		strings.NewReader(`{"from_agent":"","to_agent":""}`))
	rec := httptest.NewRecorder()
	srv.handleRequestContact(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRespondContact_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleRespondContact(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRespondContact_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/contacts/respond",
		strings.NewReader(`{"to_agent":"","from_agent":""}`))
	rec := httptest.NewRecorder()
	srv.handleRespondContact(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetContactPolicy_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleSetContactPolicy(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetContactPolicy_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy",
		strings.NewReader(`{"agent_name":"","policy":""}`))
	rec := httptest.NewRecorder()
	srv.handleSetContactPolicy(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleSetContactPolicy_InvalidPolicy(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/mail/contacts/policy",
		strings.NewReader(`{"agent_name":"TestAgent","policy":"bogus"}`))
	rec := httptest.NewRecorder()
	srv.handleSetContactPolicy(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ===========================================================================
// Reservation handler tests
// ===========================================================================

func TestHandleReservePaths_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleReservePaths(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReservePaths_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations",
		strings.NewReader(`{"agent_name":"","paths":[]}`))
	rec := httptest.NewRecorder()
	srv.handleReservePaths(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReleaseReservations_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/reservations",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleReleaseReservations(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReleaseReservations_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/reservations",
		strings.NewReader(`{"agent_name":""}`))
	rec := httptest.NewRecorder()
	srv.handleReleaseReservations(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReservationConflicts_MissingPaths(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/conflicts", nil)
	rec := httptest.NewRecorder()
	srv.handleReservationConflicts(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGetReservation_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reservations/abc", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleGetReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ===========================================================================
// Additional reservation handler validation tests
// ===========================================================================

func TestHandleReleaseReservationByID_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/abc/release", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleReleaseReservationByID(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleReleaseReservationByID_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/123/release", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleReleaseReservationByID(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRenewReservation_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/abc/renew",
		strings.NewReader(`{"agent_name":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleRenewReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRenewReservation_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/123/renew",
		strings.NewReader(`{bad`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleRenewReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRenewReservation_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/123/renew",
		strings.NewReader(`{"agent_name":""}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleRenewReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleForceReleaseReservation_InvalidID(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/abc/force-release",
		strings.NewReader(`{"agent_name":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "abc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleForceReleaseReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleForceReleaseReservation_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/123/force-release",
		strings.NewReader(`{bad`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleForceReleaseReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleForceReleaseReservation_MissingAgent(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reservations/123/force-release",
		strings.NewReader(`{"agent_name":""}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	srv.handleForceReleaseReservation(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ===========================================================================
// OpenAPI and Swagger UI handler tests
// ===========================================================================

func TestHandleOpenAPISpec(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()
	srv.handleOpenAPISpec(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandleSwaggerUI(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	rec := httptest.NewRecorder()
	srv.handleSwaggerUI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ===========================================================================
// Memory (CASS) handler tests for 0% coverage functions
// ===========================================================================

func TestHandleMemoryContext_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleMemoryContext(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMemoryContext_EmptyTask(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/context",
		strings.NewReader(`{"task":""}`))
	rec := httptest.NewRecorder()
	srv.handleMemoryContext(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMemoryOutcome_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.handleMemoryOutcome(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleMemoryOutcome_InvalidStatus(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memory/outcome",
		strings.NewReader(`{"status":"bogus"}`))
	rec := httptest.NewRecorder()
	srv.handleMemoryOutcome(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// ===========================================================================
// Approval end-to-end: create, get, approve, deny
// ===========================================================================

func TestApprovalEndToEnd(t *testing.T) {
	resetApprovalsForTest()
	srv, _ := setupTestServer(t)

	// 1. Create an approval request
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/request",
		strings.NewReader(`{"action":"rm -rf /tmp/test","resource":"/tmp/test"}`))
	rec := httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200", rec.Code)
	}

	// Parse the response to get the approval ID
	var createResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	approvalID, ok := createResp["id"].(string)
	if !ok || approvalID == "" {
		t.Fatalf("expected approval ID in response, got: %v", createResp)
	}

	// 2. Get the approval
	req = httptest.NewRequest(http.MethodGet, "/api/v1/approvals/"+approvalID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	srv.handleApprovalGetV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", rec.Code)
	}

	// 3. Approve it
	req = httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+approvalID+"/approve", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	srv.handleApprovalApproveV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: status = %d, want 200", rec.Code)
	}

	// 4. Try to approve again (should conflict - not pending anymore)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+approvalID+"/approve", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	srv.handleApprovalApproveV1(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-approve: status = %d, want 409", rec.Code)
	}

	// 5. Create another approval and deny it
	req = httptest.NewRequest(http.MethodPost, "/api/v1/approvals/request",
		strings.NewReader(`{"action":"delete-db","resource":"main-db"}`))
	rec = httptest.NewRecorder()
	srv.handleApprovalRequestV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create2: status = %d, want 200", rec.Code)
	}
	var createResp2 map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &createResp2)
	approvalID2 := createResp2["id"].(string)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+approvalID2+"/deny", nil)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", approvalID2)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec = httptest.NewRecorder()
	srv.handleApprovalDenyV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deny: status = %d, want 200", rec.Code)
	}

	// 6. Verify history now has entries
	req = httptest.NewRequest(http.MethodGet, "/api/v1/approvals/history", nil)
	rec = httptest.NewRecorder()
	srv.handleApprovalsHistoryV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: status = %d, want 200", rec.Code)
	}
}

// ===========================================================================
// Safety install/uninstall
// ===========================================================================

func TestHandleSafetyInstallV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/install",
		strings.NewReader(`{"force":true}`))
	req.Header.Set("Content-Length", "15")
	rec := httptest.NewRecorder()
	srv.handleSafetyInstallV1(rec, req)
	// May succeed or conflict
	if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleSafetyUninstallV1(t *testing.T) {
	srv, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/safety/uninstall", nil)
	rec := httptest.NewRecorder()
	srv.handleSafetyUninstallV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
