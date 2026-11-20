package serve

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestExtractPathParams(t *testing.T) {
	tests := []struct {
		path     string
		expected []string
	}{
		{"/sessions", nil},
		{"/sessions/{sessionId}", []string{"sessionId"}},
		{"/sessions/{sessionId}/panes/{paneIdx}", []string{"sessionId", "paneIdx"}},
	}

	for _, tt := range tests {
		params := extractPathParams(tt.path)
		if len(params) != len(tt.expected) {
			t.Errorf("extractPathParams(%q) returned %d params, want %d", tt.path, len(params), len(tt.expected))
			continue
		}
		for i, p := range params {
			if p.Name != tt.expected[i] {
				t.Errorf("extractPathParams(%q)[%d].Name = %q, want %q", tt.path, i, p.Name, tt.expected[i])
			}
			if p.In != "path" {
				t.Errorf("extractPathParams(%q)[%d].In = %q, want %q", tt.path, i, p.In, "path")
			}
			if !p.Required {
				t.Errorf("extractPathParams(%q)[%d].Required = false, want true", tt.path, i)
			}
		}
	}
}

func TestHandleOpenAPISpec_Handler(t *testing.T) {
	srv := New(Config{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)

	srv.handleOpenAPISpec(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cors := rr.Header().Get("Access-Control-Allow-Origin"); cors != "" {
		t.Errorf("handler must leave CORS to configured middleware, got %q", cors)
	}

	// Verify valid JSON
	var spec map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", spec["openapi"])
	}
}

func TestOpenAPICORSConfiguredOrigins(t *testing.T) {
	srv := New(Config{AllowedOrigins: []string{"https://cockpit.example.test"}})
	t.Cleanup(srv.Stop)
	for _, tc := range []struct {
		name, origin, allow string
		status              int
	}{
		{"same origin", "", "", http.StatusOK},
		{"allowed cockpit", "https://cockpit.example.test", "https://cockpit.example.test", http.StatusOK},
		{"foreign origin", "https://attacker.example.test", "", http.StatusForbidden},
		{"opaque origin", "null", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Origin", tc.origin)
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, req)
			if rr.Code != tc.status || rr.Header().Get("Access-Control-Allow-Origin") != tc.allow {
				t.Fatalf("origin=%q status=%d allow=%q body=%s", tc.origin, rr.Code, rr.Header().Get("Access-Control-Allow-Origin"), rr.Body.String())
			}
			if tc.allow != "" && rr.Header().Get("Vary") != "Origin" {
				t.Errorf("configured CORS response must vary by Origin: %v", rr.Header())
			}
			if tc.allow != "" {
				for _, header := range []string{"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
					if !strings.Contains(rr.Header().Get("Access-Control-Expose-Headers"), header) {
						t.Errorf("cross-origin clients cannot inspect %s: %v", header, rr.Header())
					}
				}
			}
		})
	}
}

func TestSwaggerEmbeddedAssets(t *testing.T) {
	srv := New(Config{Auth: AuthConfig{Mode: AuthModeAPIKey, APIKey: "docs-test-key"}})
	t.Cleanup(srv.Stop)
	for _, tc := range []struct {
		asset, contentType, marker string
	}{
		{"swagger-ui.css", "text/css", ".swagger-ui"},
		{"swagger-ui-bundle.js", "text/javascript", "SwaggerUIBundle"},
		{"swagger-ui-standalone-preset.js", "text/javascript", "SwaggerUIStandalonePreset"},
	} {
		t.Run(tc.asset, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/docs?asset="+tc.asset, nil)
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(rr.Body.String(), tc.marker) {
				t.Fatalf("asset=%s status=%d type=%s bytes=%d", tc.asset, rr.Code, rr.Header().Get("Content-Type"), rr.Body.Len())
			}
			if rr.Header().Get("X-Content-Type-Options") != "nosniff" || rr.Header().Get("ETag") == "" {
				t.Fatalf("missing embedded-asset protections: %v", rr.Header())
			}
			req.Header.Set("If-None-Match", rr.Header().Get("ETag"))
			cached := httptest.NewRecorder()
			srv.Router().ServeHTTP(cached, req)
			if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
				t.Fatalf("conditional asset request: status=%d bytes=%d", cached.Code, cached.Body.Len())
			}
		})
	}
	for _, asset := range []string{"../server_auth.go", "LICENSE", "swagger-ui.css/../server.go", "not-a-file.js"} {
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/docs?asset="+asset, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("unlisted asset %q: status=%d, want 404", asset, rr.Code)
		}
	}
}

// TestHandleSwaggerUI exercises the Swagger UI HTML handler.
func TestHandleSwaggerUI_Handler(t *testing.T) {
	srv := New(Config{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/docs", nil)

	srv.handleSwaggerUI(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "NTM API Documentation") {
		t.Error("expected title in HTML")
	}
	if !strings.Contains(body, "swagger-ui") {
		t.Error("expected swagger-ui reference in HTML")
	}
	if strings.Contains(body, "unpkg.com") || strings.Contains(body, "validator.swagger.io") || !strings.Contains(body, "validatorUrl: null") {
		t.Error("documentation must not depend on a CDN or contact the external Swagger validator")
	}
	if !strings.Contains(body, "?asset=swagger-ui-bundle.js") || !strings.Contains(body, "?asset=swagger-ui.css") {
		t.Error("documentation must reference embedded scripts and styles")
	}
	if !strings.Contains(body, "redirect: 'error'") || !strings.Contains(body, "request.redirect = 'error'") {
		t.Error("specification and Try it out requests must refuse credential-bearing redirects")
	}
	// Every executable script carries the unpredictable nonce used by CSP.
	policy := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "default-src 'self'") || !strings.Contains(policy, "script-src 'nonce-") {
		t.Fatalf("unexpected script policy: %q", policy)
	}
	for _, directive := range strings.Split(policy, ";") {
		if strings.HasPrefix(strings.TrimSpace(directive), "script-src ") && (strings.Contains(directive, "unsafe-inline") || strings.Contains(directive, "unsafe-eval")) {
			t.Fatalf("executable scripts must require a nonce: %q", directive)
		}
	}
	scripts := regexp.MustCompile(`<script([^>]*)>`).FindAllStringSubmatch(body, -1)
	if len(scripts) != 3 {
		t.Fatalf("expected two local scripts and one bootstrap, got %d", len(scripts))
	}
	for _, script := range scripts {
		nonce := regexp.MustCompile(`nonce="([^"]+)"`).FindStringSubmatch(script[1])
		if len(nonce) != 2 || !strings.Contains(policy, "'nonce-"+nonce[1]+"'") {
			t.Errorf("script missing matching CSP nonce: %s", script[0])
		}
	}
}

func TestSwaggerPublicURLIsEscaped(t *testing.T) {
	srv := New(Config{PublicBaseURL: "https://ntm.example.test/</script><script>alert(1)</script>"})
	t.Cleanup(srv.Stop)
	rr := httptest.NewRecorder()
	srv.handleSwaggerUI(rr, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "</script><script>alert(1)</script>") {
		t.Fatal("configured public URL broke out of its template context")
	}
}

func TestSwaggerCSPRejectsConfiguredSourceInjection(t *testing.T) {
	srv := New(Config{PublicBaseURL: "https://ntm.example.test;connect-src*/control"})
	t.Cleanup(srv.Stop)
	rr := httptest.NewRecorder()
	srv.handleSwaggerUI(rr, httptest.NewRequest(http.MethodGet, "/docs", nil))
	policy := rr.Header().Get("Content-Security-Policy")
	if strings.Contains(policy, "ntm.example.test") || strings.Contains(policy, "*") || !strings.Contains(policy, "connect-src 'self';") {
		t.Fatalf("configured URL injected CSP source syntax: %q", policy)
	}
}

func TestDocumentationURLsUseBrowserOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, host, browserOrigin string
		port                      int
	}{
		{"IPv4 wildcard", "0.0.0.0", "http://192.0.2.44:8877", 7337},
		{"IPv6 wildcard", "::", "http://[2001:db8::1]:9444", 7337},
		{"localhost alias", "127.0.0.1", "http://localhost:7337", 7337},
		{"TLS reverse proxy", "127.0.0.1", "https://ntm.example.test", 7337},
		{"ephemeral browser port", "127.0.0.1", "http://127.0.0.1:53112", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(Config{Host: tc.host, Port: tc.port})
			t.Cleanup(srv.Stop)
			pageURL, err := url.Parse(tc.browserOrigin + "/docs")
			if err != nil {
				t.Fatal(err)
			}

			docs := httptest.NewRecorder()
			srv.handleSwaggerUI(docs, httptest.NewRequest(http.MethodGet, pageURL.String(), nil))
			if docs.Code != http.StatusOK {
				t.Fatalf("documentation status=%d body=%s", docs.Code, docs.Body.String())
			}
			body := docs.Body.String()
			assets := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(body, -1)
			if len(assets) != 3 {
				t.Fatalf("expected three local assets, got %d", len(assets))
			}
			for _, asset := range assets {
				reference, err := url.Parse(html.UnescapeString(asset[1]))
				if err != nil {
					t.Fatal(err)
				}
				resolved := pageURL.ResolveReference(reference)
				if reference.IsAbs() || reference.Host != "" || reference.Path != "/docs" || reference.Query().Get("asset") == "" {
					t.Errorf("asset must use the origin-relative docs route: %q", asset[1])
				}
				if resolved.Scheme != pageURL.Scheme || resolved.Host != pageURL.Host {
					t.Errorf("asset escaped browser origin: %s", resolved)
				}
			}
			specMatch := regexp.MustCompile(`const specURL = new URL\(([^,\n]+), location\.href\);`).FindStringSubmatch(body)
			if len(specMatch) != 2 {
				t.Fatal("documentation does not resolve its specification against the page URL")
			}
			var specReference string
			if err := json.Unmarshal([]byte(specMatch[1]), &specReference); err != nil {
				t.Fatalf("decode specification URL: %v", err)
			}
			if specReference != "/api/v1/openapi.json" {
				t.Fatalf("specification URL=%q, want origin-relative API path", specReference)
			}
			specURL := pageURL.ResolveReference(&url.URL{Path: specReference})
			response := httptest.NewRecorder()
			srv.handleOpenAPISpec(response, httptest.NewRequest(http.MethodGet, specURL.String(), nil))
			var spec OpenAPISpec
			if response.Code != http.StatusOK {
				t.Fatalf("specification status=%d body=%s", response.Code, response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &spec); err != nil {
				t.Fatal(err)
			}
			if len(spec.Servers) != 1 || spec.Servers[0].URL != "/" {
				t.Fatalf("servers=%#v, want origin-relative root", spec.Servers)
			}
			apiURL := specURL.ResolveReference(&url.URL{Path: spec.Servers[0].URL + "api/v1/health"})
			if apiURL.String() != tc.browserOrigin+"/api/v1/health" {
				t.Errorf("Try it out API URL=%s, want browser origin %s", apiURL, tc.browserOrigin)
			}
		})
	}
}

func TestDocumentationHandlersUseConfiguredPublicBaseURL(t *testing.T) {
	srv := New(Config{
		Host:          "0.0.0.0",
		Port:          7337,
		PublicBaseURL: "https://ntm.example.test/control/",
		Version:       "1.2.3",
	})
	t.Cleanup(srv.Stop)

	specRecorder := httptest.NewRecorder()
	srv.handleOpenAPISpec(specRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if specRecorder.Code != http.StatusOK {
		t.Fatalf("OpenAPI status = %d, want %d", specRecorder.Code, http.StatusOK)
	}

	var spec OpenAPISpec
	if err := json.Unmarshal(specRecorder.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode OpenAPI response: %v", err)
	}
	if spec.Info.Version != "1.2.3" {
		t.Errorf("OpenAPI version = %q, want configured version", spec.Info.Version)
	}
	if len(spec.Servers) != 1 || spec.Servers[0].URL != "https://ntm.example.test/control" {
		t.Errorf("OpenAPI servers = %#v, want configured public base URL", spec.Servers)
	}

	docsRecorder := httptest.NewRecorder()
	srv.handleSwaggerUI(docsRecorder, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if docsRecorder.Code != http.StatusOK {
		t.Fatalf("docs status = %d, want %d", docsRecorder.Code, http.StatusOK)
	}
	if !strings.Contains(docsRecorder.Body.String(), "https://ntm.example.test/control/api/v1/openapi.json") {
		t.Errorf("Swagger UI does not reference configured public OpenAPI URL: %s", docsRecorder.Body.String())
	}
	for _, asset := range []string{"swagger-ui.css", "swagger-ui-bundle.js", "swagger-ui-standalone-preset.js"} {
		if !strings.Contains(docsRecorder.Body.String(), "https://ntm.example.test/control/docs?asset="+asset) {
			t.Errorf("Swagger UI asset %q does not preserve the configured public path prefix", asset)
		}
	}
	policy := docsRecorder.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"connect-src 'self' https://ntm.example.test;", "style-src 'self' https://ntm.example.test 'unsafe-inline';"} {
		if !strings.Contains(policy, directive) {
			t.Errorf("CSP must permit configured public origin: missing %q in %q", directive, policy)
		}
	}
	if strings.Contains(policy, "/control") {
		t.Errorf("CSP source must be an origin, not the public URL path: %q", policy)
	}
}
