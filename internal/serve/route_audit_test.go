package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/events"
)

// =============================================================================
// TestRouteAudit — router-level audit for empty/placeholder handlers
//
// Creates a test Server, walks the chi router tree, and verifies that:
//   - No registered routes have empty handler functions (handlers that return
//     empty bodies for GET requests)
//   - Key routes respond with non-empty response bodies
//
// Bead: bd-1aae9.8.3
// =============================================================================

// setupRouteAuditServer creates a minimal server for route auditing.
func setupRouteAuditServer(t *testing.T) *Server {
	t.Helper()

	eventBus := events.NewEventBus(100)

	srv := New(Config{
		Port:     0,
		EventBus: eventBus,
	})

	t.Cleanup(func() {
		srv.Stop()
	})

	return srv
}

// TestRouteAudit_NoEmptyGetHandlers walks all registered GET routes and
// verifies they return non-empty response bodies. Routes that require path
// parameters are tested with placeholder values.
func TestRouteAudit_NoEmptyGetHandlers(t *testing.T) {
	srv := setupRouteAuditServer(t)
	router := srv.Router()

	// Collect all GET routes from the chi router.
	type routeInfo struct {
		method  string
		pattern string
	}
	var routes []routeInfo

	err := chi.Walk(router, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == "GET" {
			routes = append(routes, routeInfo{method: method, pattern: route})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	if len(routes) == 0 {
		t.Fatal("no GET routes found in router — server may not have built routes")
	}

	// Routes that are expected to return empty bodies or require special
	// handling (e.g., SSE streams, WebSocket upgrades, routes needing DB,
	// or routes that shell out to external tools and may be slow).
	skipRoutes := map[string]string{
		"/events":                      "SSE stream, not a request/response endpoint",
		"/api/v1/ws":                   "WebSocket upgrade, not a request/response endpoint",
		"/api/v1/attention/stream":     "SSE stream",
		"/api/v1/openapi.json":         "generated OpenAPI spec may be empty without full config",
		"/docs":                        "serves external HTML, may redirect",
		"/docs/":                       "serves external HTML, may redirect",
		"/api/sessions/{id}":           "requires valid session ID in state store",
		"/api/sessions/{id}/agents":    "requires valid session ID",
		"/api/sessions/{id}/events":    "requires valid session ID, SSE stream",
		"/api/v1/sessions/{id}":        "requires valid session ID in state store",
		"/api/v1/sessions/{id}/status": "requires valid session ID",
		"/api/v1/sessions/{id}/agents": "requires valid session ID",
		"/api/v1/sessions/{id}/events": "requires valid session ID, SSE stream",
		"/api/v1/attention/events":     "requires attention feed data",
		"/api/v1/attention/digest":     "requires attention feed data",
		"/api/v1/streaming/stats":      "requires streaming manager data",
		"/api/v1/deps":                 "deps check shells out to verify installed tools",
		"/api/v1/doctor":               "doctor check shells out to external tools",
		"/api/kernel/commands":         "kernel command list, tested separately",
	}

	// Also skip routes with pane-specific or agent-specific path params,
	// and routes that call external tools (robot/*, memory/*, deps).
	skipPatterns := []string{
		"/panes/",
		"/agents/",
		"/jobs/",
		"/scans/",
		"/findings/",
		"/checkpoints/",
		"/pipelines/",
		"/reservations/",
		"/accounts/",
		"/mail/",
		"/cass/",
		"/beads/",
		"/safety/",
		"/robot/",  // robot endpoints shell out to tmux/bv and can be slow
		"/memory/", // memory endpoints may call external tools
		"/policy/", // policy endpoints may invoke external tools
		"/bugs/",   // bug reporting endpoints
	}

	t.Run("registered_routes_not_empty", func(t *testing.T) {
		for _, route := range routes {
			pattern := route.pattern

			// Check skip list
			if reason, ok := skipRoutes[pattern]; ok {
				t.Logf("SKIP %s %s — %s", route.method, pattern, reason)
				continue
			}

			// Check skip patterns
			skip := false
			for _, sp := range skipPatterns {
				if strings.Contains(pattern, sp) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}

			// Replace path parameters with test values
			testPath := pattern
			testPath = strings.ReplaceAll(testPath, "{id}", "test-id")
			testPath = strings.ReplaceAll(testPath, "{sessionId}", "test-session")
			testPath = strings.ReplaceAll(testPath, "{cursor}", "test-cursor")

			t.Run(route.method+"_"+sanitizeRouteName(pattern), func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, testPath, nil)
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				// We accept any status code that isn't a panic (500 with empty body).
				// A 401, 403, 404, or valid response all indicate a real handler.
				// The failure case we're looking for is a 200 with completely empty body
				// which would indicate a no-op handler.
				if rec.Code == http.StatusOK && rec.Body.Len() == 0 {
					t.Errorf("GET %s returned 200 with empty body — handler may be a placeholder",
						pattern)
				}
			})
		}
	})
}

// TestRouteAudit_KeyRoutesRespond verifies that critical routes respond with
// non-empty bodies. Only tests fast, self-contained routes that do not shell
// out to external tools.
func TestRouteAudit_KeyRoutesRespond(t *testing.T) {
	srv := setupRouteAuditServer(t)
	router := srv.Router()

	keyRoutes := []struct {
		name       string
		path       string
		wantStatus int // 0 means "any non-5xx"
	}{
		{"health", "/health", http.StatusOK},
		{"health_v1", "/api/v1/health", http.StatusOK},
		{"version_v1", "/api/v1/version", http.StatusOK},
		{"capabilities_v1", "/api/v1/capabilities", http.StatusOK},
		{"config_v1", "/api/v1/config", http.StatusOK},
	}

	for _, kr := range keyRoutes {
		t.Run(kr.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, kr.path, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if kr.wantStatus != 0 && rec.Code != kr.wantStatus {
				t.Errorf("GET %s: status = %d, want %d", kr.path, rec.Code, kr.wantStatus)
			}

			if rec.Body.Len() == 0 {
				t.Errorf("GET %s: response body is empty", kr.path)
			}
		})
	}
}

// TestRouteAudit_RouteCount ensures the route count doesn't accidentally drop.
// This acts as a snapshot — if routes are removed, this test will notice.
func TestRouteAudit_RouteCount(t *testing.T) {
	srv := setupRouteAuditServer(t)
	router := srv.Router()

	var count int
	err := chi.Walk(router, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	// This is a lower bound. If routes are added, the test still passes.
	// If routes are accidentally removed, it fails. Update this number when
	// intentionally removing routes.
	const minExpectedRoutes = 20
	if count < minExpectedRoutes {
		t.Errorf("router has only %d routes, expected at least %d — routes may have been accidentally removed",
			count, minExpectedRoutes)
	}

	t.Logf("total registered routes: %d", count)
}

// sanitizeRouteName converts a route pattern to a safe test name.
func sanitizeRouteName(pattern string) string {
	r := strings.NewReplacer(
		"/", "_",
		"{", "",
		"}", "",
		".", "_",
	)
	name := r.Replace(pattern)
	name = strings.Trim(name, "_")
	if name == "" {
		name = "root"
	}
	return name
}
