package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRole(t *testing.T) {
	tests := []struct {
		input    string
		expected Role
	}{
		{"admin", RoleAdmin},
		{"Admin", RoleAdmin},
		{"ADMIN", RoleAdmin},
		{"operator", RoleOperator},
		{"Operator", RoleOperator},
		{"viewer", RoleViewer},
		{"Viewer", RoleViewer},
		{"unknown", RoleViewer}, // Defaults to viewer
		{"", RoleViewer},        // Defaults to viewer
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := ParseRole(tc.input)
			if result != tc.expected {
				t.Errorf("ParseRole(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestRoleHasPermission(t *testing.T) {
	tests := []struct {
		role Role
		perm Permission
		want bool
	}{
		// Viewer permissions
		{RoleViewer, PermReadSessions, true},
		{RoleViewer, PermReadAgents, true},
		{RoleViewer, PermWriteSessions, false},
		{RoleViewer, PermDangerousOps, false},
		{RoleViewer, PermApproveRequests, false},

		// Operator permissions
		{RoleOperator, PermReadSessions, true},
		{RoleOperator, PermWriteSessions, true},
		{RoleOperator, PermWriteAgents, true},
		{RoleOperator, PermDangerousOps, false},
		{RoleOperator, PermApproveRequests, false},

		// Admin permissions
		{RoleAdmin, PermReadSessions, true},
		{RoleAdmin, PermWriteSessions, true},
		{RoleAdmin, PermDangerousOps, true},
		{RoleAdmin, PermApproveRequests, true},
		{RoleAdmin, PermForceRelease, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.role)+"_"+string(tc.perm), func(t *testing.T) {
			result := tc.role.HasPermission(tc.perm)
			if result != tc.want {
				t.Errorf("%s.HasPermission(%s) = %v, want %v", tc.role, tc.perm, result, tc.want)
			}
		})
	}
}

func TestRoleHierarchy(t *testing.T) {
	// Admin > Operator > Viewer
	if roleHierarchy(RoleAdmin) <= roleHierarchy(RoleOperator) {
		t.Error("Admin should have higher hierarchy than Operator")
	}
	if roleHierarchy(RoleOperator) <= roleHierarchy(RoleViewer) {
		t.Error("Operator should have higher hierarchy than Viewer")
	}
	if roleHierarchy(RoleViewer) <= 0 {
		t.Error("Viewer should have positive hierarchy")
	}
}

func TestRoleFromContext(t *testing.T) {
	// Test with no role context
	ctx := context.Background()
	if rc := RoleFromContext(ctx); rc != nil {
		t.Error("Expected nil for context without role")
	}

	// Test with role context
	rc := &RoleContext{
		Role:   RoleOperator,
		UserID: "test-user",
	}
	ctx = withRoleContext(ctx, rc)
	extracted := RoleFromContext(ctx)
	if extracted == nil {
		t.Fatal("Expected role context to be present")
	}
	if extracted.Role != RoleOperator {
		t.Errorf("Role = %q, want %q", extracted.Role, RoleOperator)
	}
	if extracted.UserID != "test-user" {
		t.Errorf("UserID = %q, want %q", extracted.UserID, "test-user")
	}
}

func TestExtractUserIDFromClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]interface{}
		want   string
	}{
		{
			name:   "sub claim",
			claims: map[string]interface{}{"sub": "user-123"},
			want:   "user-123",
		},
		{
			name:   "email claim",
			claims: map[string]interface{}{"email": "user@example.com"},
			want:   "user@example.com",
		},
		{
			name:   "preferred_username",
			claims: map[string]interface{}{"preferred_username": "jdoe"},
			want:   "jdoe",
		},
		{
			name:   "sub takes precedence",
			claims: map[string]interface{}{"sub": "user-123", "email": "other@example.com"},
			want:   "user-123",
		},
		{
			name:   "empty claims",
			claims: map[string]interface{}{},
			want:   "anonymous",
		},
		{
			name:   "nil claims",
			claims: nil,
			want:   "anonymous",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractUserIDFromClaims(tc.claims)
			if result != tc.want {
				t.Errorf("extractUserIDFromClaims() = %q, want %q", result, tc.want)
			}
		})
	}
}

func TestServerExtractRoleFromClaims(t *testing.T) {
	tests := []struct {
		name     string
		authMode AuthMode
		claims   map[string]interface{}
		want     Role
	}{
		{
			name:     "local mode gets admin",
			authMode: AuthModeLocal,
			claims:   nil,
			want:     RoleAdmin,
		},
		{
			name:     "role claim direct",
			authMode: AuthModeAPIKey,
			claims:   map[string]interface{}{"role": "operator"},
			want:     RoleOperator,
		},
		{
			name:     "roles array - highest wins",
			authMode: AuthModeAPIKey,
			claims:   map[string]interface{}{"roles": []interface{}{"viewer", "admin"}},
			want:     RoleAdmin,
		},
		{
			name:     "ntm_role custom claim",
			authMode: AuthModeAPIKey,
			claims:   map[string]interface{}{"ntm_role": "admin"},
			want:     RoleAdmin,
		},
		{
			name:     "keycloak realm_access format",
			authMode: AuthModeOIDC,
			claims: map[string]interface{}{
				"realm_access": map[string]interface{}{
					"roles": []interface{}{"operator"},
				},
			},
			want: RoleOperator,
		},
		{
			name:     "no role defaults to viewer",
			authMode: AuthModeAPIKey,
			claims:   map[string]interface{}{"sub": "user-123"},
			want:     RoleViewer,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{auth: AuthConfig{Mode: tc.authMode}}
			result := s.extractRoleFromClaims(tc.claims)
			if result != tc.want {
				t.Errorf("extractRoleFromClaims() = %q, want %q", result, tc.want)
			}
		})
	}
}

func TestRequirePermission(t *testing.T) {
	s := &Server{}

	// Create a test handler that just returns 200 OK
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		role       Role
		permission Permission
		wantCode   int
	}{
		{
			name:       "admin has dangerous ops",
			role:       RoleAdmin,
			permission: PermDangerousOps,
			wantCode:   http.StatusOK,
		},
		{
			name:       "operator lacks dangerous ops",
			role:       RoleOperator,
			permission: PermDangerousOps,
			wantCode:   http.StatusForbidden,
		},
		{
			name:       "viewer lacks write",
			role:       RoleViewer,
			permission: PermWriteSessions,
			wantCode:   http.StatusForbidden,
		},
		{
			name:       "operator has write",
			role:       RoleOperator,
			permission: PermWriteSessions,
			wantCode:   http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Wrap handler with permission middleware
			handler := s.RequirePermission(tc.permission)(testHandler)

			// Create request with role context
			req := httptest.NewRequest("GET", "/test", nil)
			rc := &RoleContext{Role: tc.role, UserID: "test-user"}
			ctx := withRoleContext(req.Context(), rc)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	s := &Server{}

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name     string
		userRole Role
		minRole  Role
		wantCode int
	}{
		{
			name:     "admin meets admin requirement",
			userRole: RoleAdmin,
			minRole:  RoleAdmin,
			wantCode: http.StatusOK,
		},
		{
			name:     "admin exceeds operator requirement",
			userRole: RoleAdmin,
			minRole:  RoleOperator,
			wantCode: http.StatusOK,
		},
		{
			name:     "operator meets operator requirement",
			userRole: RoleOperator,
			minRole:  RoleOperator,
			wantCode: http.StatusOK,
		},
		{
			name:     "viewer fails operator requirement",
			userRole: RoleViewer,
			minRole:  RoleOperator,
			wantCode: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := s.RequireRole(tc.minRole)(testHandler)

			req := httptest.NewRequest("GET", "/test", nil)
			rc := &RoleContext{Role: tc.userRole, UserID: "test-user"}
			ctx := withRoleContext(req.Context(), rc)
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestCheckPermission(t *testing.T) {
	tests := []struct {
		name     string
		role     *RoleContext
		perm     Permission
		wantOK   bool
		wantCode int
	}{
		{
			name:     "permission granted",
			role:     &RoleContext{Role: RoleAdmin, UserID: "admin"},
			perm:     PermDangerousOps,
			wantOK:   true,
			wantCode: 0, // No error written
		},
		{
			name:     "permission denied",
			role:     &RoleContext{Role: RoleViewer, UserID: "viewer"},
			perm:     PermWriteSessions,
			wantOK:   false,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "no role context",
			role:     nil,
			perm:     PermReadSessions,
			wantOK:   false,
			wantCode: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			if tc.role != nil {
				ctx := withRoleContext(req.Context(), tc.role)
				req = req.WithContext(ctx)
			}

			w := httptest.NewRecorder()
			result := CheckPermission(w, req, tc.perm)

			if result != tc.wantOK {
				t.Errorf("CheckPermission() = %v, want %v", result, tc.wantOK)
			}

			if !tc.wantOK && w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}

func TestDefaultRBACConfig(t *testing.T) {
	cfg := DefaultRBACConfig()

	if !cfg.Enabled {
		t.Error("Default RBAC should be enabled")
	}
	if cfg.DefaultRole != RoleViewer {
		t.Errorf("Default role = %q, want %q", cfg.DefaultRole, RoleViewer)
	}
	if cfg.RoleClaimKey != "role" {
		t.Errorf("RoleClaimKey = %q, want %q", cfg.RoleClaimKey, "role")
	}
	if cfg.AllowAnonymous {
		t.Error("Default should not allow anonymous")
	}
}

// api_key and mtls callers carry no role in the credential itself, so
// authenticateRequest must supply one. Returning nil claims left RBAC with an
// empty claim set that fell through to RoleViewer, which holds no write
// permission — so enabling authentication turned every mutating endpoint into a
// 403 and no test covered it.
func TestAuthenticateRequestGrantsRoleForSharedCredentials(t *testing.T) {
	t.Run("api key defaults to admin", func(t *testing.T) {
		s := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
		r.Header.Set("Authorization", "Bearer secret")

		claims, err := s.authenticateRequest(r)
		if err != nil {
			t.Fatalf("authenticateRequest: %v", err)
		}
		if claims == nil {
			t.Fatal("claims are nil; RBAC would fall through to viewer")
		}
		if got := s.extractRoleFromClaims(claims); got != RoleAdmin {
			t.Fatalf("role = %q, want %q", got, RoleAdmin)
		}
		if !RoleAdmin.HasPermission(PermWriteSessions) {
			t.Fatal("admin must hold sessions:write")
		}
	})

	t.Run("api key honors a configured lower role", func(t *testing.T) {
		s := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret", Role: RoleViewer}}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
		r.Header.Set("Authorization", "Bearer secret")

		claims, err := s.authenticateRequest(r)
		if err != nil {
			t.Fatalf("authenticateRequest: %v", err)
		}
		if got := s.extractRoleFromClaims(claims); got != RoleViewer {
			t.Fatalf("role = %q, want %q", got, RoleViewer)
		}
	})

	t.Run("bad api key still fails closed", func(t *testing.T) {
		s := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
		r.Header.Set("Authorization", "Bearer wrong")

		if _, err := s.authenticateRequest(r); err == nil {
			t.Fatal("an invalid api key must be rejected")
		}
	})
}

// The write API must actually work once authentication is enabled: a valid key
// reaches a permission-gated handler instead of being refused by RBAC.
func TestAuthenticatedAPIKeyReachesWriteGatedRoute(t *testing.T) {
	s := &Server{auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "secret"}}

	reached := false
	handler := s.authMiddlewareFunc(
		s.rbacMiddleware(
			s.RequirePermission(PermWriteSessions)(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					reached = true
					w.WriteHeader(http.StatusOK)
				}),
			),
		),
	)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("authenticated write got status=%d reached=%t body=%s", rec.Code, reached, rec.Body.String())
	}
}

// Chi applies a sub-router's Use middlewares before routing, hence before an
// inline With chain, and an idempotency replay returns without calling next — so
// with idempotencyMiddleware on the sub-router, a replayed request never reached
// RequirePermission. The key also carried no principal, so one caller's cached
// response was served to another who reused the key.
func TestIdempotencyReplayStillEnforcesPermission(t *testing.T) {
	srv, _ := setupTestServer(t)

	calls := 0
	target := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job":"created"}`))
	})
	// Production order: RequirePermission runs BEFORE idempotencyMiddleware, which
	// is what `With(RequirePermission(...), s.idempotencyMiddleware)` produces.
	// Nesting idempotency outside the permission check is the vulnerable shape.
	//
	// rbacMiddleware is deliberately not in this chain: it derives the role from
	// auth claims and would replace the injected RoleContext with admin in local
	// mode, so the per-caller distinction under test would vanish.
	handler := srv.RequirePermission(PermWriteJobs)(srv.idempotencyMiddleware(target))

	authorized := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
		r.Header.Set("Idempotency-Key", "shared-key")
		r = r.WithContext(withRoleContext(r.Context(), &RoleContext{Role: RoleAdmin, UserID: "alice"}))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}

	// First call runs the handler and caches the response.
	if rec := authorized(); rec.Code != http.StatusOK {
		t.Fatalf("first authorized call status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}

	// Same principal replaying the same key is served from cache.
	rec := authorized()
	if rec.Code != http.StatusOK || rec.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("replay status=%d replay-header=%q", rec.Code, rec.Header().Get("X-Idempotent-Replay"))
	}
	if calls != 1 {
		t.Fatalf("replay re-ran the handler: calls = %d", calls)
	}

	// A viewer reusing the same key must be refused, not handed the cached
	// response of an authorized caller.
	viewerReq := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	viewerReq.Header.Set("Idempotency-Key", "shared-key")
	viewerReq = viewerReq.WithContext(withRoleContext(viewerReq.Context(), &RoleContext{Role: RoleViewer, UserID: "bob"}))
	viewerRec := httptest.NewRecorder()
	handler.ServeHTTP(viewerRec, viewerReq)

	if viewerRec.Code != http.StatusForbidden {
		t.Fatalf("viewer replay status=%d body=%s, want 403", viewerRec.Code, viewerRec.Body.String())
	}
	if strings.Contains(viewerRec.Body.String(), "created") {
		t.Fatalf("viewer received the authorized caller's cached response: %s", viewerRec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("viewer reached the handler: calls = %d", calls)
	}
}

// The principal is part of the key, so the same key from different callers does
// not collide.
func TestScopedIdempotencyKeyIncludesPrincipal(t *testing.T) {
	newReq := func(rc *RoleContext) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
		r.Header.Set("Idempotency-Key", "same-key")
		if rc != nil {
			r = r.WithContext(withRoleContext(r.Context(), rc))
		}
		return r
	}

	alice := scopedIdempotencyKey(newReq(&RoleContext{Role: RoleAdmin, UserID: "alice"}))
	bob := scopedIdempotencyKey(newReq(&RoleContext{Role: RoleAdmin, UserID: "bob"}))
	anon := scopedIdempotencyKey(newReq(nil))

	if alice == bob {
		t.Fatal("different principals produced the same idempotency key")
	}
	for name, key := range map[string]string{"alice": alice, "bob": bob, "anonymous": anon} {
		if key == "" {
			t.Fatalf("%s produced an empty key", name)
		}
		if !strings.Contains(key, "same-key") {
			t.Fatalf("%s key lost the client key: %q", name, key)
		}
	}
	// No Idempotency-Key means no caching at all.
	if got := scopedIdempotencyKey(httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)); got != "" {
		t.Fatalf("missing Idempotency-Key produced %q, want empty", got)
	}
}
