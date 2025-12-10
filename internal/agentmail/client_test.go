package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	// Test default configuration
	c := NewClient()
	if c.baseURL != DefaultBaseURL {
		t.Errorf("expected base URL %s, got %s", DefaultBaseURL, c.baseURL)
	}
	if c.httpClient == nil {
		t.Error("expected HTTP client to be initialized")
	}

	// Test with options
	customURL := "http://custom:8080/mcp/"
	c = NewClient(WithBaseURL(customURL), WithToken("test-token"))
	if c.baseURL != customURL {
		t.Errorf("expected base URL %s, got %s", customURL, c.baseURL)
	}
	if c.bearerToken != "test-token" {
		t.Errorf("expected token 'test-token', got %s", c.bearerToken)
	}
}

func TestHealthCheck(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthStatus{
			Status:    "ok",
			Timestamp: time.Now().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	status, err := c.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", status.Status)
	}
}

func TestIsAvailable(t *testing.T) {
	// Server that returns 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HealthStatus{Status: "ok"})
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/mcp/"))
	if !c.IsAvailable() {
		t.Error("expected IsAvailable to return true")
	}

	// Test unavailable server
	c = NewClient(WithBaseURL("http://localhost:1/mcp/"))
	if c.IsAvailable() {
		t.Error("expected IsAvailable to return false for unreachable server")
	}
}

func TestCallTool(t *testing.T) {
	// Mock JSON-RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.JSONRPC != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %s", req.JSONRPC)
		}
		if req.Method != "tools/call" {
			t.Errorf("expected method tools/call, got %s", req.Method)
		}

		// Return success response
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"id": 1, "name": "TestAgent"}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.callTool(context.Background(), "test_tool", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var data struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if data.ID != 1 || data.Name != "TestAgent" {
		t.Errorf("unexpected result: %+v", data)
	}
}

func TestCallToolError(t *testing.T) {
	// Mock server that returns JSON-RPC error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Error: &JSONRPCError{
				Code:    -32600,
				Message: "Invalid Request",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	_, err := c.callTool(context.Background(), "test_tool", nil)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !IsUnauthorized(err) {
		t.Errorf("expected unauthorized error, got: %v", err)
	}
}

func TestProjectKey(t *testing.T) {
	c := NewClient(WithProjectKey("/test/project"))
	if c.ProjectKey() != "/test/project" {
		t.Errorf("expected /test/project, got %s", c.ProjectKey())
	}

	c.SetProjectKey("/new/project")
	if c.ProjectKey() != "/new/project" {
		t.Errorf("expected /new/project, got %s", c.ProjectKey())
	}
}

func TestJSONRPCError(t *testing.T) {
	err := &JSONRPCError{
		Code:    -32600,
		Message: "Invalid Request",
	}

	expected := "JSON-RPC error -32600: Invalid Request"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}

	// With data
	err.Data = map[string]string{"field": "value"}
	if err.Error() == expected {
		t.Error("expected error message to include data")
	}
}

func TestAPIError(t *testing.T) {
	innerErr := ErrServerUnavailable
	err := NewAPIError("test_op", 503, innerErr)

	if err.Operation != "test_op" {
		t.Errorf("expected operation 'test_op', got %s", err.Operation)
	}
	if err.StatusCode != 503 {
		t.Errorf("expected status 503, got %d", err.StatusCode)
	}
	if err.Unwrap() != innerErr {
		t.Error("Unwrap should return the inner error")
	}
	if !IsServerUnavailable(err) {
		t.Error("expected IsServerUnavailable to return true")
	}
}

func TestErrorHelpers(t *testing.T) {
	tests := []struct {
		err    error
		check  func(error) bool
		expect bool
	}{
		{ErrServerUnavailable, IsServerUnavailable, true},
		{ErrUnauthorized, IsUnauthorized, true},
		{ErrNotFound, IsNotFound, true},
		{ErrTimeout, IsTimeout, true},
		{ErrReservationConflict, IsReservationConflict, true},
		{ErrServerUnavailable, IsUnauthorized, false},
		{NewAPIError("test", 0, ErrNotFound), IsNotFound, true},
	}

	for _, tt := range tests {
		result := tt.check(tt.err)
		if result != tt.expect {
			t.Errorf("for %v, expected %v, got %v", tt.err, tt.expect, result)
		}
	}
}
