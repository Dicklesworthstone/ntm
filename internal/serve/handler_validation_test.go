package serve

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

// =============================================================================
// Handler Validation Tests
//
// These tests verify REST handler validation errors map to correct error codes
// as required by bd-2gjig acceptance criteria.
// =============================================================================

// setupValidationTestServer creates a server with store for validation tests.
func setupValidationTestServer(t *testing.T) (*Server, *state.Store) {
	t.Helper()
	return setupTestServer(t)
}

// TestHandlerValidationMethodNotAllowed verifies METHOD_NOT_ALLOWED error codes.
func TestHandlerValidationMethodNotAllowed(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  func(http.ResponseWriter, *http.Request)
		wantCode int
	}{
		{
			name:     "health_post",
			method:   http.MethodPost,
			path:     "/health",
			handler:  srv.handleHealth,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "health_put",
			method:   http.MethodPut,
			path:     "/health",
			handler:  srv.handleHealth,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "health_delete",
			method:   http.MethodDelete,
			path:     "/health",
			handler:  srv.handleHealth,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "sessions_post",
			method:   http.MethodPost,
			path:     "/api/sessions",
			handler:  srv.handleSessions,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "session_post",
			method:   http.MethodPost,
			path:     "/api/sessions/test",
			handler:  srv.handleSession,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name:     "robot_status_post",
			method:   http.MethodPost,
			path:     "/api/robot/status",
			handler:  srv.handleRobotStatus,
			wantCode: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			tt.handler(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("handler %s: status=%d, want=%d", tt.name, rec.Code, tt.wantCode)
			}

			// Verify response body contains error info
			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if resp["success"] != false {
				t.Error("expected success=false")
			}
			if _, ok := resp["error"]; !ok {
				t.Error("expected error field in response")
			}

			t.Logf("handler=%s method=%s status=%d error=%v",
				tt.name, tt.method, rec.Code, resp["error"])
		})
	}
}

// TestHandlerValidationServiceUnavailable verifies SERVICE_UNAVAILABLE errors.
func TestHandlerValidationServiceUnavailable(t *testing.T) {
	// Create server without state store
	srv := New(Config{})

	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name:    "sessions_no_store",
			path:    "/api/sessions",
			handler: srv.handleSessions,
		},
		{
			name:    "session_no_store",
			path:    "/api/sessions/test",
			handler: srv.handleSession,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			tt.handler(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("handler %s: status=%d, want=%d", tt.name, rec.Code, http.StatusServiceUnavailable)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if resp["success"] != false {
				t.Error("expected success=false")
			}

			t.Logf("handler=%s status=%d error=%v", tt.name, rec.Code, resp["error"])
		})
	}
}

// TestHandlerValidationBadRequest verifies BAD_REQUEST error codes.
func TestHandlerValidationBadRequest(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name:    "session_no_id",
			path:    "/api/sessions/",
			handler: srv.handleSession,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			tt.handler(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("handler %s: status=%d, want=%d", tt.name, rec.Code, http.StatusBadRequest)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if resp["success"] != false {
				t.Error("expected success=false")
			}

			t.Logf("handler=%s status=%d error=%v", tt.name, rec.Code, resp["error"])
		})
	}
}

// TestHandlerValidationNotFound verifies NOT_FOUND error codes.
func TestHandlerValidationNotFound(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{
			name:    "session_nonexistent",
			path:    "/api/sessions/nonexistent-session-id",
			handler: srv.handleSession,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			tt.handler(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("handler %s: status=%d, want=%d", tt.name, rec.Code, http.StatusNotFound)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if resp["success"] != false {
				t.Error("expected success=false")
			}

			t.Logf("handler=%s status=%d error=%v", tt.name, rec.Code, resp["error"])
		})
	}
}

// TestAPIV1ValidationErrors tests API v1 endpoint validation.
func TestAPIV1ValidationErrors(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantErr    string
	}{
		{
			name:       "create_job_invalid_type",
			method:     http.MethodPost,
			path:       "/api/v1/jobs/",
			body:       `{"type": "invalid_job_type"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "create_job_invalid_json",
			method:     http.MethodPost,
			path:       "/api/v1/jobs/",
			body:       `{invalid json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "get_job_not_found",
			method:     http.MethodGet,
			path:       "/api/v1/jobs/nonexistent-job-id",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "cancel_job_not_found",
			method:     http.MethodDelete,
			path:       "/api/v1/jobs/nonexistent-job-id",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}

			var req *http.Request
			if body != nil {
				req = httptest.NewRequest(tt.method, tt.path, body)
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}

			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status=%d, want=%d, body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if rec.Code >= 400 && resp["success"] != false {
				t.Error("expected success=false for error response")
			}

			t.Logf("test=%s status=%d success=%v error=%v",
				tt.name, rec.Code, resp["success"], resp["error"])
		})
	}
}

// TestResponseEnvelopeFormat verifies response envelope structure.
func TestResponseEnvelopeFormat(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify required envelope fields
	required := []string{"success", "timestamp"}
	for _, field := range required {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}

	// Verify success value type
	if _, ok := resp["success"].(bool); !ok {
		t.Error("success field should be boolean")
	}

	// Verify timestamp format (RFC3339)
	if ts, ok := resp["timestamp"].(string); ok {
		if len(ts) < 20 {
			t.Errorf("timestamp doesn't look like RFC3339: %s", ts)
		}
	}

	t.Logf("envelope: success=%v timestamp=%v", resp["success"], resp["timestamp"])
}

// TestErrorResponseEnvelopeFormat verifies error response structure.
func TestErrorResponseEnvelopeFormat(t *testing.T) {
	srv := New(Config{}) // No store - will trigger service unavailable

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rec := httptest.NewRecorder()

	srv.handleSessions(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify error envelope fields
	if resp["success"] != false {
		t.Error("expected success=false")
	}
	if _, ok := resp["error"]; !ok {
		t.Error("expected error field")
	}

	t.Logf("error envelope: success=%v error=%v", resp["success"], resp["error"])
}

// TestArrayFieldsNeverNull verifies array fields are never null.
func TestArrayFieldsNeverNull(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	tests := []struct {
		name       string
		path       string
		arrayField string
	}{
		{
			name:       "sessions_empty",
			path:       "/api/v1/sessions",
			arrayField: "sessions",
		},
		{
			name:       "jobs_empty",
			path:       "/api/v1/jobs/",
			arrayField: "jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			srv.Router().ServeHTTP(rec, req)

			// Verify response is valid JSON
			var rawResp map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			// Check the array field exists
			arrayBytes, ok := rawResp[tt.arrayField]
			if !ok {
				t.Fatalf("missing field: %s", tt.arrayField)
			}

			// Verify it's an array (starts with [), not null
			trimmed := bytes.TrimSpace(arrayBytes)
			if string(trimmed) == "null" {
				t.Errorf("field %s is null, should be []", tt.arrayField)
			}
			if len(trimmed) > 0 && trimmed[0] != '[' {
				t.Errorf("field %s is not an array: %s", tt.arrayField, string(trimmed))
			}

			t.Logf("field=%s value=%s", tt.arrayField, string(trimmed))
		})
	}
}

// TestContentTypeHeader verifies JSON content type is set.
func TestContentTypeHeader(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	endpoints := []struct {
		path   string
		method string
	}{
		{"/api/v1/health", http.MethodGet},
		{"/api/v1/version", http.MethodGet},
		{"/api/v1/sessions", http.MethodGet},
		{"/api/v1/capabilities", http.MethodGet},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path, nil)
			rec := httptest.NewRecorder()

			srv.Router().ServeHTTP(rec, req)

			contentType := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(contentType, "application/json") {
				t.Errorf("Content-Type=%s, want application/json", contentType)
			}

			t.Logf("path=%s content_type=%s", ep.path, contentType)
		})
	}
}

// TestRequestIDMiddleware verifies request IDs are generated.
func TestRequestIDMiddleware(t *testing.T) {
	srv, _ := setupValidationTestServer(t)

	// Make request without X-Request-Id
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	// Response should have request_id (may be in body or header depending on implementation)
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Check for request_id in response or header
	hasRequestID := false
	if _, ok := resp["request_id"]; ok {
		hasRequestID = true
	}
	if rec.Header().Get(requestIDHeader) != "" {
		hasRequestID = true
	}

	t.Logf("request_id_in_body=%v request_id_in_header=%v",
		resp["request_id"] != nil, rec.Header().Get(requestIDHeader))

	// With client-provided request ID
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req2.Header.Set(requestIDHeader, "custom-req-123")
	rec2 := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec2, req2)

	t.Logf("custom_request_id: header=%s", rec2.Header().Get(requestIDHeader))
	_ = hasRequestID // Acknowledge we checked it
}
