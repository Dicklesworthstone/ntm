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
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
