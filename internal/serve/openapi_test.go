package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if cors := rr.Header().Get("Access-Control-Allow-Origin"); cors != "*" {
		t.Errorf("CORS header = %q, want *", cors)
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
}

func TestDocumentationHandlersUseConfiguredPublicBaseURL(t *testing.T) {
	srv := New(Config{
		Host:          "0.0.0.0",
		Port:          7337,
		PublicBaseURL: "https://ntm.example.test/control/",
		Version:       "1.2.3",
	})

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
}
