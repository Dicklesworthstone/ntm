package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// Mock MCP JSON-RPC server for health_check tool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		// Verify it's a health_check tool call
		params, ok := req.Params.(map[string]interface{})
		if !ok {
			t.Fatal("expected params to be a map")
		}
		if params["name"] != "health_check" {
			t.Errorf("expected health_check tool, got %v", params["name"])
		}

		// Return health status via JSON-RPC
		healthStatus := HealthStatus{
			Status:    "ok",
			Timestamp: time.Now().Format(time.RFC3339),
		}
		statusJSON, _ := json.Marshal(healthStatus)

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(statusJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	status, err := c.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", status.Status)
	}
}

func TestIsAvailable(t *testing.T) {
	// Mock MCP JSON-RPC server for health_check
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		// Return healthy status
		healthStatus := HealthStatus{Status: "ok"}
		statusJSON, _ := json.Marshal(healthStatus)

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(statusJSON),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	if !c.IsAvailable() {
		t.Error("expected IsAvailable to return true")
	}

	// Test unavailable server
	c = NewClient(WithBaseURL("http://localhost:1/"))
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

func TestExtractMCPContent(t *testing.T) {
	tests := []struct {
		name        string
		input       json.RawMessage
		wantErr     bool
		errContains string
		validate    func(t *testing.T, result json.RawMessage)
	}{
		{
			name:  "raw result (no envelope) - backward compatibility",
			input: json.RawMessage(`{"id": 1, "name": "TestAgent"}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 1 || data.Name != "TestAgent" {
					t.Errorf("unexpected data: %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with structuredContent (preferred)",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":99,\"name\":\"Ignored\"}"}],
				"structuredContent": {"id": 115, "name": "BrownOtter"},
				"isError": false
			}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 115 || data.Name != "BrownOtter" {
					t.Errorf("expected {115, BrownOtter}, got %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with content text (fallback)",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":42,\"name\":\"GreenLake\"}"}],
				"isError": false
			}`),
			validate: func(t *testing.T, result json.RawMessage) {
				var data struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &data); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if data.ID != 42 || data.Name != "GreenLake" {
					t.Errorf("expected {42, GreenLake}, got %+v", data)
				}
			},
		},
		{
			name: "MCP envelope with isError=true and message",
			input: json.RawMessage(`{
				"content": [{"type": "text", "text": "Agent name already in use"}],
				"isError": true
			}`),
			wantErr:     true,
			errContains: "Agent name already in use",
		},
		{
			name: "MCP envelope with isError=true no message",
			input: json.RawMessage(`{
				"content": [],
				"isError": true
			}`),
			wantErr:     true,
			errContains: "tool returned error",
		},
		{
			name:  "empty result",
			input: json.RawMessage(``),
			validate: func(t *testing.T, result json.RawMessage) {
				if len(result) != 0 {
					t.Errorf("expected empty result, got %s", string(result))
				}
			},
		},
		{
			name:  "null result",
			input: json.RawMessage(`null`),
			validate: func(t *testing.T, result json.RawMessage) {
				// null is valid JSON, should pass through
				if string(result) != "null" {
					t.Errorf("expected null, got %s", string(result))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractMCPContent(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}


func TestCallToolWithMCPEnvelope(t *testing.T) {
	// Mock server that returns MCP envelope format
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			// MCP envelope with structuredContent
			Result: json.RawMessage(`{
				"content": [{"type": "text", "text": "{\"id\":115,\"name\":\"BrownOtter\"}"}],
				"structuredContent": {"id": 115, "name": "BrownOtter", "program": "ntm", "model": "coordinator"},
				"isError": false
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(WithBaseURL(server.URL + "/"))
	result, err := c.callTool(context.Background(), "register_agent", map[string]interface{}{
		"project_key": "/test/project",
		"program":     "ntm",
		"model":       "coordinator",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify we got the extracted structuredContent, not the envelope
	var agent struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Program string `json:"program"`
		Model   string `json:"model"`
	}
	if err := json.Unmarshal(result, &agent); err != nil {
		t.Fatalf("failed to unmarshal agent: %v", err)
	}
	if agent.ID != 115 {
		t.Errorf("expected ID 115, got %d", agent.ID)
	}
	if agent.Name != "BrownOtter" {
		t.Errorf("expected name BrownOtter, got %s", agent.Name)
	}
	if agent.Program != "ntm" {
		t.Errorf("expected program ntm, got %s", agent.Program)
	}
}
