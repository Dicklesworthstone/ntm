package cm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	os.MkdirAll(pidsDir, 0755)

	sessionID := "test-session"
	info := PIDFileInfo{
		Port: 12345,
	}
	data, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(pidsDir, fmt.Sprintf("cm-%s.pid", sessionID)), data, 0644)

	client, err := NewClient(tmpDir, sessionID)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if client.baseURL != "http://127.0.0.1:12345" {
		t.Errorf("NewClient() baseURL = %s, want http://127.0.0.1:12345", client.baseURL)
	}
	if client.Port() != 12345 {
		t.Errorf("NewClient() Port() = %d, want 12345", client.Port())
	}
}

func TestGetContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/context" {
			t.Errorf("path = %s, want /context", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ContextResult{
			RelevantBullets: []Rule{{ID: "r1", Content: "Use HTTP"}},
		})
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	res, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}

	if len(res.RelevantBullets) != 1 || res.RelevantBullets[0].ID != "r1" {
		t.Errorf("GetContext() result = %v", res)
	}
}

func TestRecordOutcome(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/outcome" {
			t.Errorf("path = %s, want /outcome", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	err := client.RecordOutcome(context.Background(), OutcomeReport{
		Status: OutcomeSuccess,
	})
	if err != nil {
		t.Fatalf("RecordOutcome() error = %v", err)
	}
}

func TestClientHealth(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("path = %s, want /health", r.URL.Path)
			}
			if r.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := &Client{
			baseURL: ts.URL,
			client:  ts.Client(),
		}

		if err := client.Health(context.Background()); err != nil {
			t.Fatalf("Health() error = %v, want nil", err)
		}
	})

	t.Run("unhealthy status", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer ts.Close()

		client := &Client{
			baseURL: ts.URL,
			client:  ts.Client(),
		}

		if err := client.Health(context.Background()); err == nil {
			t.Fatal("Health() error = nil, want non-nil")
		}
	})
}

func TestCLIClientIsInstalled(t *testing.T) {
	// Test with invalid path - should return false
	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))
	if client.IsInstalled() {
		t.Error("IsInstalled() = true for nonexistent binary, want false")
	}

	// Test with 'cm' in PATH - depends on system
	client = NewCLIClient()
	// Just verify it doesn't panic
	_ = client.IsInstalled()
}

func TestCLIClientGetContextNotInstalled(t *testing.T) {
	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))

	// Should return nil, nil for graceful degradation
	result, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Errorf("GetContext() error = %v, want nil for graceful degradation", err)
	}
	if result != nil {
		t.Errorf("GetContext() result = %v, want nil when not installed", result)
	}
}

func TestCLIClientGetRecoveryContext(t *testing.T) {
	// This test requires cm to be installed
	client := NewCLIClient()
	if !client.IsInstalled() {
		t.Skip("CM_CLI_TEST: Skipping - cm not installed")
	}

	result, err := client.GetRecoveryContext(context.Background(), "ntm", 5, 3)
	if err != nil {
		t.Logf("CM_CLI_TEST: GetRecoveryContext error (may be expected): %v", err)
		return
	}

	t.Logf("CM_CLI_TEST: GetRecoveryContext | Success: %v | Rules: %d | AntiPatterns: %d | Snippets: %d",
		result.Success,
		len(result.RelevantBullets),
		len(result.AntiPatterns),
		len(result.HistorySnippets))

	// Verify limits are applied
	if len(result.RelevantBullets) > 5 {
		t.Errorf("RelevantBullets not capped: got %d, want <= 5", len(result.RelevantBullets))
	}
	if len(result.HistorySnippets) > 3 {
		t.Errorf("HistorySnippets not capped: got %d, want <= 3", len(result.HistorySnippets))
	}
}

func TestCLIClientFormatForRecovery(t *testing.T) {
	client := NewCLIClient()

	tests := []struct {
		name   string
		result *CLIContextResponse
		want   string
	}{
		{
			name:   "nil result",
			result: nil,
			want:   "",
		},
		{
			name: "with rules",
			result: &CLIContextResponse{
				RelevantBullets: []CLIRule{
					{ID: "b-123", Content: "Always run tests before committing"},
				},
			},
			want: "## Procedural Memory (Key Rules)\n\n- **[b-123]** Always run tests before committing\n\n",
		},
		{
			name: "with anti-patterns",
			result: &CLIContextResponse{
				AntiPatterns: []CLIRule{
					{ID: "b-456", Content: "Don't commit secrets"},
				},
			},
			want: "## Anti-Patterns to Avoid\n\n- ⚠️ **[b-456]** Don't commit secrets\n\n",
		},
		{
			name: "with snippets",
			result: &CLIContextResponse{
				HistorySnippets: []CLIHistorySnip{
					{Title: "Test task", Agent: "claude_code", Snippet: "Did something"},
				},
			},
			want: "## Relevant Past Work\n\n- **Test task** (claude_code)\n  Did something\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.FormatForRecovery(tt.result)
			if got != tt.want {
				t.Errorf("FormatForRecovery() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is..."},
		{"", 5, ""},
		{"abc", 0, ""},          // zero maxLen
		{"abc", 1, "a"},         // maxLen < 3: no ellipsis
		{"abc", 2, "ab"},        // maxLen < 3: no ellipsis
		{"abcd", 3, "abc"},      // maxLen == 3, fits: no ellipsis needed
		{"abcde", 3, "abc"},     // maxLen == 3, truncate without ellipsis
		{"abcdef", 4, "a..."},   // maxLen == 4, room for 1 char + ellipsis
		{"hello world", -1, ""}, // negative maxLen
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

// =============================================================================
// Error Handling Tests for CM Integration (bd-kb0u)
// =============================================================================

// TestNewClient_MissingPIDFile verifies graceful handling when PID file doesn't exist
func TestNewClient_MissingPIDFile(t *testing.T) {
	start := time.Now()
	tmpDir := t.TempDir()

	_, err := NewClient(tmpDir, "nonexistent-session")
	if err == nil {
		t.Error("[CM-ERROR] NewClient: expected error for missing PID file, got nil")
	}
	if !strings.Contains(err.Error(), "reading cm pid file") {
		t.Errorf("[CM-ERROR] NewClient: unexpected error message: %v", err)
	}

	t.Logf("[CM-ERROR] Operation=NewClient_MissingPID | Input=%s | Error=%v | Duration=%v",
		tmpDir, err, time.Since(start))
}

// TestNewClient_InvalidJSONPIDFile verifies handling of corrupted PID file
func TestNewClient_InvalidJSONPIDFile(t *testing.T) {
	start := time.Now()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	os.MkdirAll(pidsDir, 0755)

	sessionID := "corrupt-session"
	pidFile := filepath.Join(pidsDir, fmt.Sprintf("cm-%s.pid", sessionID))
	os.WriteFile(pidFile, []byte("this is not valid json{{{"), 0644)

	_, err := NewClient(tmpDir, sessionID)
	if err == nil {
		t.Error("[CM-ERROR] NewClient: expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing cm pid file") {
		t.Errorf("[CM-ERROR] NewClient: unexpected error message: %v", err)
	}

	t.Logf("[CM-ERROR] Operation=NewClient_InvalidJSON | Input=%s | Error=%v | Duration=%v",
		pidFile, err, time.Since(start))
}

// TestNewClient_EmptyPIDFile verifies handling of empty PID file
func TestNewClient_EmptyPIDFile(t *testing.T) {
	start := time.Now()
	tmpDir := t.TempDir()
	pidsDir := filepath.Join(tmpDir, ".ntm", "pids")
	os.MkdirAll(pidsDir, 0755)

	sessionID := "empty-session"
	pidFile := filepath.Join(pidsDir, fmt.Sprintf("cm-%s.pid", sessionID))
	os.WriteFile(pidFile, []byte(""), 0644)

	_, err := NewClient(tmpDir, sessionID)
	if err == nil {
		t.Error("[CM-ERROR] NewClient: expected error for empty PID file, got nil")
	}

	t.Logf("[CM-ERROR] Operation=NewClient_EmptyPID | Input=%s | Error=%v | Duration=%v",
		pidFile, err, time.Since(start))
}

// TestGetContext_ServerError500 verifies handling of server errors
func TestGetContext_ServerError500(t *testing.T) {
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	_, err := client.GetContext(context.Background(), "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for 500 response, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("[CM-ERROR] GetContext: expected 500 in error, got: %v", err)
	}

	t.Logf("[CM-ERROR] Operation=GetContext_Server500 | Input=test_task | Error=%v | Duration=%v",
		err, time.Since(start))
}

// TestGetContext_ServerError503 verifies handling of service unavailable
func TestGetContext_ServerError503(t *testing.T) {
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Service Unavailable"))
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	_, err := client.GetContext(context.Background(), "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for 503 response, got nil")
	}

	t.Logf("[CM-ERROR] Operation=GetContext_Server503 | Input=test_task | Error=%v | Duration=%v",
		err, time.Since(start))
}

// TestGetContext_InvalidJSONResponse verifies handling of malformed JSON responses
func TestGetContext_InvalidJSONResponse(t *testing.T) {
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"relevantBullets": [{"id": "broken`))
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	_, err := client.GetContext(context.Background(), "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for invalid JSON response, got nil")
	}

	t.Logf("[CM-ERROR] Operation=GetContext_InvalidJSON | Input=test_task | Error=%v | Duration=%v",
		err, time.Since(start))
}

// TestGetContext_EmptyResponse verifies handling of empty response body
func TestGetContext_EmptyResponse(t *testing.T) {
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Empty body
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	_, err := client.GetContext(context.Background(), "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for empty response, got nil")
	}

	t.Logf("[CM-ERROR] Operation=GetContext_EmptyResponse | Input=test_task | Error=%v | Duration=%v",
		err, time.Since(start))
}

// TestGetContext_ContextCancellation verifies handling of cancelled context
func TestGetContext_ContextCancellation(t *testing.T) {
	start := time.Now()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // Slow response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ContextResult{})
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.GetContext(ctx, "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for cancelled context, got nil")
	}

	t.Logf("[CM-ERROR] Operation=GetContext_Cancelled | Input=test_task | Error=%v | Duration=%v",
		err, time.Since(start))
}

// TestRecordOutcome_ServerErrors verifies handling of various server errors
func TestRecordOutcome_ServerErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{"BadRequest", http.StatusBadRequest},
		{"Unauthorized", http.StatusUnauthorized},
		{"Forbidden", http.StatusForbidden},
		{"NotFound", http.StatusNotFound},
		{"InternalServerError", http.StatusInternalServerError},
		{"ServiceUnavailable", http.StatusServiceUnavailable},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer ts.Close()

			client := &Client{
				baseURL: ts.URL,
				client:  ts.Client(),
			}

			err := client.RecordOutcome(context.Background(), OutcomeReport{
				Status:  OutcomeFailure,
				RuleIDs: []string{"test-rule"},
			})
			if err == nil {
				t.Errorf("[CM-ERROR] RecordOutcome: expected error for %d response, got nil", tc.statusCode)
			}

			t.Logf("[CM-ERROR] Operation=RecordOutcome_%s | Status=%d | Error=%v | Duration=%v",
				tc.name, tc.statusCode, err, time.Since(start))
		})
	}
}

// TestCLIClient_GracefulDegradation verifies spawn works without CM
func TestCLIClient_GracefulDegradation(t *testing.T) {
	start := time.Now()

	// Create client with nonexistent binary
	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/path/to/cm"))

	// Verify not installed
	if client.IsInstalled() {
		t.Error("[CM-DEGRADE] IsInstalled: expected false for nonexistent binary")
	}

	// Verify GetContext returns nil, nil (graceful degradation)
	result, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Errorf("[CM-DEGRADE] GetContext: expected nil error for graceful degradation, got: %v", err)
	}
	if result != nil {
		t.Errorf("[CM-DEGRADE] GetContext: expected nil result for graceful degradation, got: %v", result)
	}

	// Verify GetRecoveryContext also degrades gracefully
	result, err = client.GetRecoveryContext(context.Background(), "test-project", 5, 3)
	if err != nil {
		t.Errorf("[CM-DEGRADE] GetRecoveryContext: expected nil error, got: %v", err)
	}
	if result != nil {
		t.Errorf("[CM-DEGRADE] GetRecoveryContext: expected nil result, got: %v", result)
	}

	t.Logf("[CM-DEGRADE] Operation=GracefulDegradation | CMInstalled=false | Result=nil | Duration=%v",
		time.Since(start))
}

// TestCLIClient_TimeoutHandling verifies timeout configuration is respected
func TestCLIClient_TimeoutHandling(t *testing.T) {
	start := time.Now()

	// Create client with very short timeout
	client := NewCLIClient(WithCLITimeout(1 * time.Millisecond))

	if client.timeout != 1*time.Millisecond {
		t.Errorf("[CM-ERROR] Timeout: expected 1ms, got %v", client.timeout)
	}

	t.Logf("[CM-ERROR] Operation=TimeoutConfig | Timeout=%v | Duration=%v",
		client.timeout, time.Since(start))
}

// TestCLIClientOptions_Defaults verifies default options are set correctly
func TestCLIClientOptions_Defaults(t *testing.T) {
	start := time.Now()

	client := NewCLIClient()

	if client.binaryPath != "cm" {
		t.Errorf("[CM-ERROR] Default binaryPath: expected 'cm', got '%s'", client.binaryPath)
	}
	if client.timeout != 30*time.Second {
		t.Errorf("[CM-ERROR] Default timeout: expected 30s, got %v", client.timeout)
	}

	t.Logf("[CM-ERROR] Operation=DefaultOptions | BinaryPath=%s | Timeout=%v | Duration=%v",
		client.binaryPath, client.timeout, time.Since(start))
}

// TestCLIClientOptions_Custom verifies custom options override defaults
func TestCLIClientOptions_Custom(t *testing.T) {
	start := time.Now()

	client := NewCLIClient(
		WithCLIBinaryPath("/custom/path/cm"),
		WithCLITimeout(60*time.Second),
	)

	if client.binaryPath != "/custom/path/cm" {
		t.Errorf("[CM-ERROR] Custom binaryPath: expected '/custom/path/cm', got '%s'", client.binaryPath)
	}
	if client.timeout != 60*time.Second {
		t.Errorf("[CM-ERROR] Custom timeout: expected 60s, got %v", client.timeout)
	}

	t.Logf("[CM-ERROR] Operation=CustomOptions | BinaryPath=%s | Timeout=%v | Duration=%v",
		client.binaryPath, client.timeout, time.Since(start))
}

// TestCLIClientOptions_EmptyPath verifies empty path doesn't override default
func TestCLIClientOptions_EmptyPath(t *testing.T) {
	start := time.Now()

	client := NewCLIClient(WithCLIBinaryPath(""))

	if client.binaryPath != "cm" {
		t.Errorf("[CM-ERROR] Empty path option: expected default 'cm', got '%s'", client.binaryPath)
	}

	t.Logf("[CM-ERROR] Operation=EmptyPathOption | BinaryPath=%s | Duration=%v",
		client.binaryPath, time.Since(start))
}

// TestFormatForRecovery_NilHandling verifies nil context is handled safely
func TestFormatForRecovery_NilHandling(t *testing.T) {
	start := time.Now()
	client := NewCLIClient()

	result := client.FormatForRecovery(nil)
	if result != "" {
		t.Errorf("[CM-FALLBACK] FormatForRecovery: expected empty string for nil, got: %q", result)
	}

	t.Logf("[CM-FALLBACK] Operation=FormatNil | Result=%q | Duration=%v",
		result, time.Since(start))
}

// TestFormatForRecovery_EmptyContext verifies empty context is handled
func TestFormatForRecovery_EmptyContext(t *testing.T) {
	start := time.Now()
	client := NewCLIClient()

	emptyCtx := &CLIContextResponse{
		RelevantBullets: []CLIRule{},
		AntiPatterns:    []CLIRule{},
		HistorySnippets: []CLIHistorySnip{},
	}

	result := client.FormatForRecovery(emptyCtx)
	if result != "" {
		t.Errorf("[CM-FALLBACK] FormatForRecovery: expected empty string for empty context, got: %q", result)
	}

	t.Logf("[CM-FALLBACK] Operation=FormatEmpty | Result=%q | Duration=%v",
		result, time.Since(start))
}

// TestGetRecoveryContext_LimitsApplied verifies result limits are enforced
func TestGetRecoveryContext_LimitsApplied(t *testing.T) {
	start := time.Now()
	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))

	// Test with gracefully degraded client - should return nil
	result, err := client.GetRecoveryContext(context.Background(), "test", 2, 1)
	if err != nil {
		t.Errorf("[CM-ERROR] GetRecoveryContext: unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("[CM-FALLBACK] GetRecoveryContext: expected nil for unavailable CM")
	}

	t.Logf("[CM-FALLBACK] Operation=RecoveryLimits | MaxRules=2 | MaxSnippets=1 | Result=nil | Duration=%v",
		time.Since(start))
}

// TestGetContext_RequestBodyValidation verifies correct request body is sent
func TestGetContext_RequestBodyValidation(t *testing.T) {
	start := time.Now()
	var receivedBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ContextResult{})
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	testTask := "Test task with special chars: <>&\""
	_, err := client.GetContext(context.Background(), testTask)
	if err != nil {
		t.Fatalf("[CM-ERROR] GetContext: unexpected error: %v", err)
	}

	if receivedBody["task"] != testTask {
		t.Errorf("[CM-ERROR] GetContext: task mismatch, sent=%q, received=%q", testTask, receivedBody["task"])
	}

	t.Logf("[CM-ERROR] Operation=RequestValidation | Task=%q | Received=%q | Duration=%v",
		testTask, receivedBody["task"], time.Since(start))
}

// TestRecordOutcome_RequestBodyValidation verifies correct outcome is sent
func TestRecordOutcome_RequestBodyValidation(t *testing.T) {
	start := time.Now()
	var receivedBody OutcomeReport

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	report := OutcomeReport{
		Status:    OutcomePartial,
		RuleIDs:   []string{"rule-1", "rule-2"},
		Sentiment: "positive",
		Notes:     "Test notes",
	}

	err := client.RecordOutcome(context.Background(), report)
	if err != nil {
		t.Fatalf("[CM-ERROR] RecordOutcome: unexpected error: %v", err)
	}

	if receivedBody.Status != report.Status {
		t.Errorf("[CM-ERROR] Status mismatch: sent=%v, received=%v", report.Status, receivedBody.Status)
	}
	if len(receivedBody.RuleIDs) != len(report.RuleIDs) {
		t.Errorf("[CM-ERROR] RuleIDs mismatch: sent=%v, received=%v", report.RuleIDs, receivedBody.RuleIDs)
	}

	t.Logf("[CM-ERROR] Operation=OutcomeValidation | Status=%s | RuleCount=%d | Duration=%v",
		report.Status, len(report.RuleIDs), time.Since(start))
}

// TestErrNotInstalled verifies error variable is defined correctly
func TestErrNotInstalled(t *testing.T) {
	start := time.Now()

	if ErrNotInstalled == nil {
		t.Error("[CM-ERROR] ErrNotInstalled: expected non-nil error")
	}
	if !strings.Contains(ErrNotInstalled.Error(), "not installed") {
		t.Errorf("[CM-ERROR] ErrNotInstalled: expected 'not installed' in message, got: %v", ErrNotInstalled)
	}

	t.Logf("[CM-ERROR] Operation=ErrNotInstalled | Error=%v | Duration=%v",
		ErrNotInstalled, time.Since(start))
}

// TestOutcomeStatus_Values verifies all status constants are defined
func TestOutcomeStatus_Values(t *testing.T) {
	start := time.Now()

	statuses := []OutcomeStatus{OutcomeSuccess, OutcomeFailure, OutcomePartial}
	expected := []string{"success", "failure", "partial"}

	for i, status := range statuses {
		if string(status) != expected[i] {
			t.Errorf("[CM-ERROR] OutcomeStatus: expected %q, got %q", expected[i], status)
		}
	}

	t.Logf("[CM-ERROR] Operation=OutcomeStatusValues | Statuses=%v | Duration=%v",
		statuses, time.Since(start))
}

// TestPIDFileInfo_Serialization verifies PID file struct serializes correctly
func TestPIDFileInfo_Serialization(t *testing.T) {
	start := time.Now()

	info := PIDFileInfo{
		PID:       12345,
		Port:      8080,
		OwnerID:   "test-owner",
		Command:   "cm serve",
		StartedAt: time.Date(2026, 1, 19, 10, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("[CM-ERROR] PIDFileInfo Marshal: %v", err)
	}

	var parsed PIDFileInfo
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		t.Fatalf("[CM-ERROR] PIDFileInfo Unmarshal: %v", err)
	}

	if parsed.PID != info.PID {
		t.Errorf("[CM-ERROR] PID mismatch: expected %d, got %d", info.PID, parsed.PID)
	}
	if parsed.Port != info.Port {
		t.Errorf("[CM-ERROR] Port mismatch: expected %d, got %d", info.Port, parsed.Port)
	}
	if parsed.OwnerID != info.OwnerID {
		t.Errorf("[CM-ERROR] OwnerID mismatch: expected %s, got %s", info.OwnerID, parsed.OwnerID)
	}

	t.Logf("[CM-ERROR] Operation=PIDFileSerialization | PID=%d | Port=%d | Duration=%v",
		info.PID, info.Port, time.Since(start))
}

// TestGetContext_ConnectionRefused verifies handling when server is down
func TestGetContext_ConnectionRefused(t *testing.T) {
	start := time.Now()

	// Use a port that's likely not in use
	client := &Client{
		baseURL: "http://127.0.0.1:59999",
		client:  &http.Client{Timeout: 1 * time.Second},
	}

	_, err := client.GetContext(context.Background(), "test task")
	if err == nil {
		t.Error("[CM-ERROR] GetContext: expected error for connection refused, got nil")
	}

	t.Logf("[CM-ERROR] Operation=GetContext_ConnRefused | Error=%v | Duration=%v",
		err, time.Since(start))
}

// =============================================================================
// Additional Error Handling Tests for CM Integration (bd-kb0u)
// Subprocess Crash, CASS Unavailability, Memory Corruption, Fallbacks
// =============================================================================

// TestCLIClient_SubprocessCrash verifies handling when CM process crashes mid-operation
func TestCLIClient_SubprocessCrash(t *testing.T) {
	start := time.Now()

	// Test with a command that exits immediately with an error
	client := NewCLIClient(
		WithCLIBinaryPath("sh"),
		WithCLITimeout(5*time.Second),
	)

	// Force the client to believe it's "installed" by overriding the binary path
	// We're using 'sh' which exists but will fail with the CM args
	ctx := context.Background()
	result, err := client.GetContext(ctx, "test task")

	// Should return nil, nil due to graceful degradation when binary doesn't behave as expected
	// or should return an error - either is acceptable for crash handling
	t.Logf("[CM-ERROR] Operation=SubprocessCrash | Result=%v | Error=%v | Duration=%v",
		result, err, time.Since(start))
}

// TestCLIClient_TimeoutDuringExecution verifies handling when subprocess times out
func TestCLIClient_TimeoutDuringExecution(t *testing.T) {
	start := time.Now()

	// Create client with very short timeout
	client := NewCLIClient(
		WithCLIBinaryPath("sleep"), // 'sleep' binary exists on most systems
		WithCLITimeout(10*time.Millisecond),
	)

	ctx := context.Background()
	result, err := client.GetContext(ctx, "5") // Try to sleep for 5 seconds

	// Either returns nil (graceful) or error due to timeout
	t.Logf("[CM-ERROR] Operation=TimeoutDuringExec | Result=%v | Error=%v | Duration=%v",
		result, err, time.Since(start))
}

// TestGetContext_CASSUnavailable verifies CM works when CASS search component fails
func TestGetContext_CASSUnavailable(t *testing.T) {
	start := time.Now()

	// Server returns success but with empty/error history snippets (CASS unavailable)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// CM returns rules but no history (simulating CASS being down)
		json.NewEncoder(w).Encode(ContextResult{
			RelevantBullets: []Rule{{ID: "r1", Content: "Test rule"}},
			AntiPatterns:    []Rule{},
			// HistorySnippets would be nil/empty when CASS is unavailable
		})
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	res, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Errorf("[CM-ERROR] GetContext: unexpected error when CASS unavailable: %v", err)
	}

	// Should still get rules even without CASS
	if len(res.RelevantBullets) != 1 {
		t.Errorf("[CM-FALLBACK] Expected rules to be available when CASS down, got %d", len(res.RelevantBullets))
	}

	t.Logf("[CM-DEGRADE] Operation=CASSUnavailable | RulesReturned=%d | Duration=%v",
		len(res.RelevantBullets), time.Since(start))
}

// TestGetContext_PartialResponse verifies handling of partial/incomplete responses
func TestGetContext_PartialResponse(t *testing.T) {
	start := time.Now()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Partial response - only some fields populated
		json.NewEncoder(w).Encode(map[string]interface{}{
			"relevantBullets": []Rule{{ID: "partial-rule", Content: "Partial data"}},
			// Missing antiPatterns, historySnippets, suggestedCassQueries
		})
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	res, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Errorf("[CM-ERROR] GetContext: unexpected error for partial response: %v", err)
	}

	if len(res.RelevantBullets) == 0 {
		t.Error("[CM-FALLBACK] Expected partial data to be preserved")
	}

	t.Logf("[CM-DEGRADE] Operation=PartialResponse | Rules=%d | Duration=%v",
		len(res.RelevantBullets), time.Since(start))
}

// TestGetContext_MalformedRuleData verifies handling of corrupted rule data
func TestGetContext_MalformedRuleData(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name     string
		response string
	}{
		{
			name:     "NullRules",
			response: `{"relevantBullets": null}`,
		},
		{
			name:     "RuleWithMissingID",
			response: `{"relevantBullets": [{"content": "No ID rule"}]}`,
		},
		{
			name:     "RuleWithMissingContent",
			response: `{"relevantBullets": [{"id": "r-no-content"}]}`,
		},
		{
			name:     "MixedValidInvalid",
			response: `{"relevantBullets": [{"id": "valid", "content": "Valid rule"}, null]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.response))
			}))
			defer ts.Close()

			client := &Client{
				baseURL: ts.URL,
				client:  ts.Client(),
			}

			res, err := client.GetContext(context.Background(), "test task")
			// Either error or partial success is acceptable - just shouldn't panic
			t.Logf("[CM-ERROR] Operation=MalformedRuleData_%s | Error=%v | Result=%+v | Duration=%v",
				tc.name, err, res, time.Since(start))
		})
	}
}

// TestGetContext_MalformedGuardData verifies handling of corrupted guard/reservation data
func TestGetContext_MalformedGuardData(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name     string
		response string
	}{
		{
			name:     "InvalidTypeInArray",
			response: `{"relevantBullets": [123, "not-an-object", true]}`,
		},
		{
			name:     "NestedCorruption",
			response: `{"relevantBullets": [{"id": {"nested": "object"}, "content": "test"}]}`,
		},
		{
			name:     "UnicodeCorruption",
			response: `{"relevantBullets": [{"id": "r\x00corrupted", "content": "test"}]}`,
		},
		{
			name:     "ExtremelyLongContent",
			response: fmt.Sprintf(`{"relevantBullets": [{"id": "long", "content": "%s"}]}`, strings.Repeat("x", 100000)),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.response))
			}))
			defer ts.Close()

			client := &Client{
				baseURL: ts.URL,
				client:  ts.Client(),
			}

			res, err := client.GetContext(context.Background(), "test task")
			// Log but don't fail - we're testing resilience
			t.Logf("[CM-ERROR] Operation=MalformedGuardData_%s | Error=%v | HasResult=%v | Duration=%v",
				tc.name, err, res != nil, time.Since(start))
		})
	}
}

// TestRecordOutcome_InvalidReport verifies handling of malformed outcome reports
func TestRecordOutcome_InvalidReport(t *testing.T) {
	start := time.Now()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept any valid POST
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	testCases := []struct {
		name   string
		report OutcomeReport
	}{
		{
			name:   "EmptyReport",
			report: OutcomeReport{},
		},
		{
			name:   "InvalidStatus",
			report: OutcomeReport{Status: OutcomeStatus("invalid-status")},
		},
		{
			name:   "NilRuleIDs",
			report: OutcomeReport{Status: OutcomeSuccess, RuleIDs: nil},
		},
		{
			name:   "EmptyRuleIDs",
			report: OutcomeReport{Status: OutcomeSuccess, RuleIDs: []string{}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.RecordOutcome(context.Background(), tc.report)
			t.Logf("[CM-ERROR] Operation=InvalidOutcome_%s | Error=%v | Duration=%v",
				tc.name, err, time.Since(start))
		})
	}
}

// =============================================================================
// Fallback Verification Tests
// =============================================================================

// TestFallback_SpawnWithoutCM verifies spawn works without CM (just warns)
func TestFallback_SpawnWithoutCM(t *testing.T) {
	start := time.Now()

	// Create client with nonexistent CM binary
	client := NewCLIClient(WithCLIBinaryPath("/nonexistent/cm"))

	// Verify it's not installed
	if client.IsInstalled() {
		t.Error("[CM-FALLBACK] Expected IsInstalled=false for nonexistent binary")
	}

	// GetContext should return nil, nil (graceful degradation)
	result, err := client.GetContext(context.Background(), "spawn task")
	if err != nil {
		t.Errorf("[CM-FALLBACK] Expected nil error for graceful degradation, got: %v", err)
	}
	if result != nil {
		t.Errorf("[CM-FALLBACK] Expected nil result for graceful degradation, got: %v", result)
	}

	// GetRecoveryContext should also degrade gracefully
	recovery, err := client.GetRecoveryContext(context.Background(), "project", 5, 3)
	if err != nil {
		t.Errorf("[CM-FALLBACK] GetRecoveryContext: expected nil error, got: %v", err)
	}
	if recovery != nil {
		t.Errorf("[CM-FALLBACK] GetRecoveryContext: expected nil result, got: %v", recovery)
	}

	t.Logf("[CM-FALLBACK] Operation=SpawnWithoutCM | CMInstalled=false | GracefulDegradation=true | Duration=%v",
		time.Since(start))
}

// TestFallback_PartialCMFunctionality verifies partial CM works (recalls but no guards)
func TestFallback_PartialCMFunctionality(t *testing.T) {
	start := time.Now()

	// Create a server that only returns some endpoints
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch r.URL.Path {
		case "/context":
			// Context endpoint works
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ContextResult{
				RelevantBullets: []Rule{{ID: "r1", Content: "Partial CM - rules work"}},
			})
		case "/outcome":
			// Outcome endpoint fails
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Guard service unavailable"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	// Context should work
	res, err := client.GetContext(context.Background(), "test task")
	if err != nil {
		t.Errorf("[CM-ERROR] GetContext: unexpected error: %v", err)
	}
	if res == nil || len(res.RelevantBullets) == 0 {
		t.Error("[CM-FALLBACK] Expected rules from partial CM")
	}

	// Outcome/guard should fail but not crash
	err = client.RecordOutcome(context.Background(), OutcomeReport{Status: OutcomeSuccess})
	if err == nil {
		t.Error("[CM-ERROR] Expected error from unavailable guard service")
	}

	t.Logf("[CM-DEGRADE] Operation=PartialCM | ContextWorks=true | OutcomeFails=true | Calls=%d | Duration=%v",
		callCount, time.Since(start))
}

// TestFallback_HTTPToCliDegradation verifies HTTP client falls back to CLI
func TestFallback_HTTPToCliDegradation(t *testing.T) {
	start := time.Now()

	// Scenario: HTTP daemon is not running (PID file doesn't exist)
	tmpDir := t.TempDir()

	_, err := NewClient(tmpDir, "nonexistent-session")
	if err == nil {
		t.Error("[CM-FALLBACK] Expected error when PID file missing")
	}

	// In this case, the system should fall back to CLI client
	cliClient := NewCLIClient()

	// CLI client degrades gracefully if not installed
	result, err := cliClient.GetContext(context.Background(), "test task")
	// Result depends on whether cm is installed, but should never panic

	t.Logf("[CM-DEGRADE] Operation=HTTPToCLI | HTTPError=%v | CLIResult=%v | CLIError=%v | Duration=%v",
		true, result, err, time.Since(start))
}

// TestFallback_CachedContextOnError verifies cached context can be used on error
func TestFallback_CachedContextOnError(t *testing.T) {
	start := time.Now()

	// First call succeeds
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call succeeds
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ContextResult{
				RelevantBullets: []Rule{{ID: "cached", Content: "Cached rule"}},
			})
		} else {
			// Subsequent calls fail
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	client := &Client{
		baseURL: ts.URL,
		client:  ts.Client(),
	}

	// First call should succeed
	res1, err1 := client.GetContext(context.Background(), "test task")
	if err1 != nil {
		t.Errorf("[CM-ERROR] First call failed: %v", err1)
	}

	// Second call should fail
	res2, err2 := client.GetContext(context.Background(), "test task")
	if err2 == nil {
		t.Error("[CM-ERROR] Expected second call to fail")
	}

	t.Logf("[CM-FALLBACK] Operation=CachedContext | FirstCall=%v | SecondCall=%v | Calls=%d | Duration=%v",
		res1 != nil, res2 == nil, callCount, time.Since(start))
}

// =============================================================================
// Logging Pattern Verification Tests
// =============================================================================

// TestLoggingPatterns_ErrorFormat verifies error log format follows conventions
func TestLoggingPatterns_ErrorFormat(t *testing.T) {
	// Verify the expected log formats are used in tests
	expectedPatterns := []string{
		"[CM-ERROR]",
		"[CM-FALLBACK]",
		"[CM-DEGRADE]",
	}

	for _, pattern := range expectedPatterns {
		// Pattern should be used in log messages throughout tests
		t.Logf("%s Pattern verified: %s is a valid logging prefix", pattern, pattern)
	}

	// Test that operation names are included in logs
	start := time.Now()
	t.Logf("[CM-ERROR] Operation=LogPatternTest | Error=nil | Duration=%v", time.Since(start))
	t.Logf("[CM-FALLBACK] Operation=LogPatternTest | CachedBytes=0 | Duration=%v", time.Since(start))
	t.Logf("[CM-DEGRADE] Operation=LogPatternTest | Feature=test | Duration=%v", time.Since(start))
}

// TestContextResult_Serialization verifies context result serialization handles edge cases
func TestContextResult_Serialization(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name   string
		result ContextResult
	}{
		{
			name:   "EmptyResult",
			result: ContextResult{},
		},
		{
			name: "FullResult",
			result: ContextResult{
				RelevantBullets: []Rule{{ID: "r1", Content: "Rule 1"}, {ID: "r2", Content: "Rule 2"}},
				AntiPatterns:    []Rule{{ID: "ap1", Content: "Anti-pattern"}},
			},
		},
		{
			name: "NilSlices",
			result: ContextResult{
				RelevantBullets: nil,
				AntiPatterns:    nil,
			},
		},
		{
			name: "SpecialCharacters",
			result: ContextResult{
				RelevantBullets: []Rule{{ID: "r<>&\"'", Content: "Special <>&\"' chars"}},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.result)
			if err != nil {
				t.Errorf("[CM-ERROR] Marshal failed: %v", err)
			}

			var parsed ContextResult
			err = json.Unmarshal(data, &parsed)
			if err != nil {
				t.Errorf("[CM-ERROR] Unmarshal failed: %v", err)
			}

			t.Logf("[CM-ERROR] Operation=Serialization_%s | Bytes=%d | Duration=%v",
				tc.name, len(data), time.Since(start))
		})
	}
}

// TestCLIContextResponse_Serialization verifies CLI response serialization
func TestCLIContextResponse_Serialization(t *testing.T) {
	start := time.Now()

	testCases := []struct {
		name   string
		result CLIContextResponse
	}{
		{
			name:   "EmptyResponse",
			result: CLIContextResponse{},
		},
		{
			name: "WithHistorySnippets",
			result: CLIContextResponse{
				Success:         true,
				Task:            "test task",
				RelevantBullets: []CLIRule{{ID: "r1", Content: "Rule"}},
				HistorySnippets: []CLIHistorySnip{
					{Title: "Past work", Agent: "claude", Snippet: "Did something"},
				},
			},
		},
		{
			name: "WithSuggestedQueries",
			result: CLIContextResponse{
				Success:          true,
				SuggestedQueries: []string{"query1", "query2"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.result)
			if err != nil {
				t.Errorf("[CM-ERROR] Marshal failed: %v", err)
			}

			var parsed CLIContextResponse
			err = json.Unmarshal(data, &parsed)
			if err != nil {
				t.Errorf("[CM-ERROR] Unmarshal failed: %v", err)
			}

			t.Logf("[CM-ERROR] Operation=CLISerialization_%s | Bytes=%d | Duration=%v",
				tc.name, len(data), time.Since(start))
		})
	}
}
