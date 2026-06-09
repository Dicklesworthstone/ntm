package agentmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestNewClient_LocalBaseURLUsesHTTPBearerToken(t *testing.T) {
	t.Setenv("AGENT_MAIL_TOKEN", "remote-token")
	t.Setenv("HTTP_BEARER_TOKEN", "local-token")

	c := NewClient(WithBaseURL(DefaultBaseURL))

	if c.bearerToken != "local-token" {
		t.Errorf("bearerToken = %q, want local-token", c.bearerToken)
	}
}

func TestNewClient_ExplicitTokenWinsForLoopbackURL(t *testing.T) {
	t.Setenv("HTTP_BEARER_TOKEN", "local-token")

	c := NewClient(WithBaseURL(DefaultBaseURL), WithToken("explicit-token"))

	if c.bearerToken != "explicit-token" {
		t.Errorf("bearerToken = %q, want explicit-token", c.bearerToken)
	}
}

func TestNewClient_LocalFallbackReadsConfigEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_MAIL_URL", "https://agentmail.example.test/mcp/")
	t.Setenv("AGENT_MAIL_TOKEN", "remote-token")
	t.Setenv("HTTP_BEARER_TOKEN", "")

	configDir := filepath.Join(home, ".config", "mcp-agent-mail")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.env"), []byte("HTTP_BEARER_TOKEN='local-config-token'\n"), 0o600); err != nil {
		t.Fatalf("write config.env: %v", err)
	}

	c := NewClient(WithBaseURL(DefaultBaseURL))

	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if c.bearerToken != "local-config-token" {
		t.Errorf("bearerToken = %q, want local-config-token", c.bearerToken)
	}
}

func TestNewClient_RemoteBaseURLKeepsAgentMailToken(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "https://agentmail.example.test/mcp/")
	t.Setenv("AGENT_MAIL_TOKEN", "remote-token")
	t.Setenv("HTTP_BEARER_TOKEN", "local-token")

	c := NewClient()

	if c.baseURL != "https://agentmail.example.test/mcp/" {
		t.Errorf("baseURL = %q, want remote URL", c.baseURL)
	}
	if c.bearerToken != "remote-token" {
		t.Errorf("bearerToken = %q, want remote-token", c.bearerToken)
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
		{ErrInvalidRequest, IsInvalidRequest, true},
		{ErrTimeout, IsTimeout, true},
		{ErrNotImplemented, IsNotImplemented, true},
		{ErrReservationConflict, IsReservationConflict, true},
		{ErrContactBlocked, IsContactBlocked, true},
		{ErrServerUnavailable, IsUnauthorized, false},
		{NewAPIError("test", 0, ErrNotFound), IsNotFound, true},
		{NewAPIError("test", 0, ErrNotImplemented), IsNotImplemented, true},
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

func TestFlexTime_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		want        time.Time
		wantErr     bool
		wantUTCOnly bool
	}{
		{
			name:        "rfc3339",
			input:       `"2026-01-31T01:24:27Z"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 0, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "rfc3339_nano",
			input:       `"2026-01-31T01:24:27.123456789Z"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 123456789, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "bare_seconds_assume_utc",
			input:       `"2026-01-31T01:24:27"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 0, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:        "bare_millis_assume_utc",
			input:       `"2026-01-31T01:24:27.123"`,
			want:        time.Date(2026, 1, 31, 1, 24, 27, 123000000, time.UTC),
			wantUTCOnly: true,
		},
		{
			name:  "empty_string_sets_zero",
			input: `""`,
			want:  time.Time{},
		},
		{
			name:        "numeric_microseconds",
			input:       `1767225600000000`,
			want:        time.UnixMicro(1767225600000000).UTC(),
			wantUTCOnly: true,
		},
		{
			name:  "null_sets_zero",
			input: `null`,
			want:  time.Time{},
		},
		{
			name:    "invalid_format",
			input:   `"not-a-timestamp"`,
			wantErr: true,
		},
		{
			name:    "invalid_object",
			input:   `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ft FlexTime
			err := json.Unmarshal([]byte(tt.input), &ft)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !ft.Time.Equal(tt.want) {
				t.Fatalf("expected %s, got %s", tt.want.Format(time.RFC3339Nano), ft.Time.Format(time.RFC3339Nano))
			}
			if tt.wantUTCOnly && ft.Time.Location() != time.UTC {
				t.Fatalf("expected UTC, got %s", ft.Time.Location())
			}
		})
	}
}

func TestFlexTime_MarshalJSON(t *testing.T) {
	t.Parallel()

	ft := FlexTime{Time: time.Date(2026, 1, 31, 1, 24, 27, 123456789, time.UTC)}
	got, err := json.Marshal(ft)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != `"2026-01-31T01:24:27.123456789Z"` {
		t.Fatalf("unexpected JSON: %s", string(got))
	}
}

func TestReservationConflict_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantPath    string
		wantHolders []string
		wantErr     bool
	}{
		{
			name:        "null_holders_becomes_empty_slice",
			input:       `{"path":"internal/agentmail/*","holders":null}`,
			wantPath:    "internal/agentmail/*",
			wantHolders: []string{},
		},
		{
			name:        "legacy_list_of_names",
			input:       `{"path":"x","holders":["BlueLake","RedStone"]}`,
			wantPath:    "x",
			wantHolders: []string{"BlueLake", "RedStone"},
		},
		{
			name:        "current_list_of_objects",
			input:       `{"path":"x","holders":[{"agent":"BlueLake"},{"agent_name":"RedStone"},{"agent":"","agent_name":"GreenCastle"}]}`,
			wantPath:    "x",
			wantHolders: []string{"BlueLake", "RedStone", "GreenCastle"},
		},
		{
			name:    "unsupported_format_errors",
			input:   `{"path":"x","holders":123}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c ReservationConflict
			err := json.Unmarshal([]byte(tt.input), &c)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if c.Path != tt.wantPath {
				t.Fatalf("expected path %q, got %q", tt.wantPath, c.Path)
			}
			if !reflect.DeepEqual(c.Holders, tt.wantHolders) {
				t.Fatalf("expected holders %#v, got %#v", tt.wantHolders, c.Holders)
			}
		})
	}
}

// =============================================================================
// Client option and method tests for coverage
// =============================================================================

func TestInvalidateCache(t *testing.T) {
	t.Parallel()

	c := NewClient()
	// Manually set cache time to simulate a cached result
	c.availableCacheTime.Store(time.Now().Unix())
	c.availableCache.Store(true)

	// Verify cache was set
	if c.availableCacheTime.Load() == 0 {
		t.Fatal("expected cache time to be set")
	}

	// Invalidate
	c.InvalidateCache()

	// Verify cache was cleared
	if c.availableCacheTime.Load() != 0 {
		t.Error("expected cache time to be cleared after InvalidateCache")
	}
}

func TestBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inputURL string
		wantURL  string
	}{
		{"default URL", "", DefaultBaseURL},
		{"custom URL with trailing slash", "http://custom:8080/mcp/", "http://custom:8080/mcp/"},
		{"custom URL without trailing slash", "http://custom:8080/mcp", "http://custom:8080/mcp/"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c *Client
			if tc.inputURL == "" {
				c = NewClient()
			} else {
				c = NewClient(WithBaseURL(tc.inputURL))
			}
			got := c.BaseURL()
			if got != tc.wantURL {
				t.Errorf("BaseURL() = %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestWithHTTPClient(t *testing.T) {
	t.Parallel()

	customClient := &http.Client{Timeout: 5 * time.Minute}
	c := NewClient(WithHTTPClient(customClient))

	if c.httpClient != customClient {
		t.Error("expected custom HTTP client to be set")
	}
}

func TestWithTimeout(t *testing.T) {
	t.Parallel()

	// WithTimeout modifies an existing httpClient's timeout
	customClient := &http.Client{Timeout: 10 * time.Second}
	c := NewClient(WithHTTPClient(customClient), WithTimeout(60*time.Second))

	if c.httpClient.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", c.httpClient.Timeout)
	}
}

func TestHttpBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"MCP URL with trailing slash", "http://127.0.0.1:8765/mcp/", "http://127.0.0.1:8765"},
		{"MCP URL without trailing slash", "http://127.0.0.1:8765/mcp", "http://127.0.0.1:8765"},
		{"non-MCP URL with trailing slash", "http://example.com/api/", "http://example.com/api"},
		{"non-MCP URL without trailing slash", "http://example.com/api", "http://example.com/api"},
		{"root URL with trailing slash", "http://localhost:8000/", "http://localhost:8000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(WithBaseURL(tc.baseURL))
			got := c.httpBaseURL()
			if got != tc.want {
				t.Errorf("httpBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchInbox_UnmarshalFormats(t *testing.T) {
	t.Parallel()

	makeServer := func(t *testing.T, rawResult json.RawMessage) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Method != "tools/call" {
				t.Fatalf("expected tools/call, got %s", req.Method)
			}

			params, ok := req.Params.(map[string]interface{})
			if !ok {
				t.Fatal("expected params to be a map")
			}
			if params["name"] != "fetch_inbox" {
				t.Fatalf("expected tool fetch_inbox, got %v", params["name"])
			}

			args, _ := params["arguments"].(map[string]interface{})
			if args["project_key"] != "/test/project" {
				t.Fatalf("expected project_key /test/project, got %v", args["project_key"])
			}
			if args["agent_name"] != "BlueLake" {
				t.Fatalf("expected agent_name BlueLake, got %v", args["agent_name"])
			}
			if args["include_bodies"] != true {
				t.Fatalf("expected include_bodies true, got %v", args["include_bodies"])
			}

			resp := JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  rawResult,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
	}

	t.Run("wrapper_object", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`{"result":[{"id":1,"subject":"Hello","from":"alice","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"hi"}]}`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		msgs, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != 1 || msgs[0].From != "alice" {
			t.Fatalf("unexpected messages: %+v", msgs)
		}
	})

	t.Run("direct_array", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`[{"id":2,"subject":"Yo","from":"bob","created_ts":"2026-01-01T00:00:00Z","importance":"normal","ack_required":false,"kind":"to","body_md":"hey"}]`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		msgs, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != 2 || msgs[0].From != "bob" {
			t.Fatalf("unexpected messages: %+v", msgs)
		}
	})

	t.Run("schema_drift_is_error", func(t *testing.T) {
		server := makeServer(t, json.RawMessage(`{"messages":[]}`))
		defer server.Close()

		c := NewClient(WithBaseURL(server.URL + "/"))
		if _, err := c.FetchInbox(context.Background(), FetchInboxOptions{
			ProjectKey:    "/test/project",
			AgentName:     "BlueLake",
			IncludeBodies: true,
		}); err == nil {
			t.Fatal("expected schema drift to return an error")
		}
	})
}
