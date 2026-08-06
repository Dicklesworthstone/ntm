package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// NewClaudeAuthFlow
// =============================================================================

func TestNewClaudeAuthFlow(t *testing.T) {
	t.Parallel()

	t.Run("local mode", func(t *testing.T) {
		t.Parallel()
		flow := NewClaudeAuthFlow(false)
		if flow == nil {
			t.Fatal("NewClaudeAuthFlow returned nil")
		}
		if flow.isRemote {
			t.Error("isRemote should be false")
		}
	})

	t.Run("remote mode", func(t *testing.T) {
		t.Parallel()
		flow := NewClaudeAuthFlow(true)
		if flow == nil {
			t.Fatal("NewClaudeAuthFlow returned nil")
		}
		if !flow.isRemote {
			t.Error("isRemote should be true")
		}
	})
}

// =============================================================================
// InitiateAuth
// =============================================================================

func TestClaudeAuthFlow_InitiateAuth(t *testing.T) {
	t.Parallel()

	flow := NewClaudeAuthFlow(false)
	var gotPane string
	var gotKeys string
	var gotEnter bool

	flow.sendKeys = func(paneID, keys string, enter bool) error {
		gotPane = paneID
		gotKeys = keys
		gotEnter = enter
		return nil
	}

	if err := flow.InitiateAuth("pane-1"); err != nil {
		t.Fatalf("InitiateAuth error: %v", err)
	}
	if gotPane != "pane-1" {
		t.Errorf("paneID = %q, want %q", gotPane, "pane-1")
	}
	if gotKeys != "/login" {
		t.Errorf("keys = %q, want %q", gotKeys, "/login")
	}
	if !gotEnter {
		t.Error("expected enter=true")
	}
}

func TestClaudeAuthFlow_MonitorAuthReturnsCaptureFailure(t *testing.T) {
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	flow.captureOutput = func(paneID string, lines int) (string, error) {
		if paneID != "%42" || lines != 30 {
			t.Fatalf("capture args = (%q, %d), want (%%42, 30)", paneID, lines)
		}
		return "", errors.New("pane unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := flow.MonitorAuth(ctx, "%42")
	if result != nil {
		t.Fatalf("MonitorAuth result = %+v, want nil", result)
	}
	if err == nil || !strings.Contains(err.Error(), "capture auth pane \"%42\"") || !strings.Contains(err.Error(), "pane unavailable") {
		t.Fatalf("MonitorAuth error = %v, want contextual capture failure", err)
	}
}

func TestClaudeAuthFlow_MonitorAuthUsesLatestTerminalResult(t *testing.T) {
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	flow.captureOutput = func(string, int) (string, error) {
		return "Successfully logged in as the previous account\nAuthentication failed", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := flow.MonitorAuth(ctx, "%42")
	if err != nil {
		t.Fatalf("MonitorAuth() error = %v", err)
	}
	if result == nil || result.State != AuthFailed {
		t.Fatalf("MonitorAuth() result = %+v, want current authentication failure", result)
	}
}

// =============================================================================
// SendContinuation
// =============================================================================

func TestClaudeAuthFlow_SendContinuation(t *testing.T) {
	t.Parallel()

	flow := NewClaudeAuthFlow(false)
	var slept time.Duration
	var gotPane string
	var gotPrompt string
	var gotEnter bool

	flow.sleep = func(d time.Duration) { slept = d }
	flow.pasteKeys = func(paneID, prompt string, enter bool) error {
		gotPane = paneID
		gotPrompt = prompt
		gotEnter = enter
		return nil
	}

	if err := flow.SendContinuation("pane-2", "continue now"); err != nil {
		t.Fatalf("SendContinuation error: %v", err)
	}
	if slept != 500*time.Millisecond {
		t.Errorf("sleep = %v, want %v", slept, 500*time.Millisecond)
	}
	if gotPane != "pane-2" {
		t.Errorf("paneID = %q, want %q", gotPane, "pane-2")
	}
	if gotPrompt != "continue now" {
		t.Errorf("prompt = %q, want %q", gotPrompt, "continue now")
	}
	if !gotEnter {
		t.Error("expected enter=true")
	}
}

// =============================================================================
// DetectBrowserURL
// =============================================================================

func TestClaudeAuthFlow_DetectBrowserURL(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)

	tests := []struct {
		name   string
		output string
		want   string
		found  bool
	}{
		{
			name:   "standard url",
			output: "Please visit https://claude.ai/login?code=123 to login",
			want:   "https://claude.ai/login?code=123",
			found:  true,
		},
		{
			name:   "bare login url",
			output: "Please visit https://claude.ai/login to login",
			want:   "https://claude.ai/login",
			found:  true,
		},
		{
			name:   "no url",
			output: "Just some random text",
			want:   "",
			found:  false,
		},
		{
			name:   "url at start of line",
			output: "https://claude.ai/login?token=abc",
			want:   "https://claude.ai/login?token=abc",
			found:  true,
		},
		{
			name:   "url at end of output",
			output: "Open this link:\nhttps://claude.ai/login?auth=xyz",
			want:   "https://claude.ai/login?auth=xyz",
			found:  true,
		},
		{
			name:   "url with multiple params",
			output: "https://claude.ai/login?code=123&redirect=home&org=test",
			want:   "https://claude.ai/login?code=123&redirect=home&org=test",
			found:  true,
		},
		{
			name:   "url followed by terminal punctuation",
			output: "Open (https://claude.ai/login?code=123).",
			want:   "https://claude.ai/login?code=123",
			found:  true,
		},
		{
			name:   "url followed by closing quote",
			output: "Open \"https://claude.ai/login?code=123\".",
			want:   "https://claude.ai/login?code=123",
			found:  true,
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
			found:  false,
		},
		{
			name:   "non-claude url",
			output: "Visit https://example.com/login?code=123",
			want:   "",
			found:  false,
		},
		{
			name:   "partial match - no path",
			output: "See https://claude.ai for more info",
			want:   "",
			found:  false,
		},
		{
			name:   "url surrounded by other text",
			output: "Step 1: go to https://claude.ai/login?code=abc then enter your code",
			want:   "https://claude.ai/login?code=abc",
			found:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, found := flow.DetectBrowserURL(tt.output)
			if found != tt.found {
				t.Errorf("DetectBrowserURL() found = %v, want %v", found, tt.found)
			}
			if got != tt.want {
				t.Errorf("DetectBrowserURL() got = %q, want %q", got, tt.want)
			}
		})
	}
}

// =============================================================================
// DetectAuthSuccess
// =============================================================================

func TestClaudeAuthFlow_DetectAuthSuccess(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"success logged in", "Successfully logged in as user", true},
		{"login successful", "Login successful", true},
		{"failure message", "Login failed", false},
		{"empty output", "", false},
		{"embedded success", "output\nSuccessfully logged in\nmore output", true},
		{"partial match - just success", "Success", false},
		{"no match - logging in", "Logging in...", false},
		{"error logging in (failure, not success)", "Error logging in", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := flow.DetectAuthSuccess(tt.output); got != tt.want {
				t.Errorf("DetectAuthSuccess(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// DetectAuthFailure
// =============================================================================

func TestClaudeAuthFlow_DetectAuthFailure(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"login failed", "Login failed due to error", true},
		{"auth failed", "Authentication failed", true},
		{"error logging in", "Error logging in: timeout", true},
		{"success message", "Login successful", false},
		{"empty output", "", false},
		{"embedded failure", "stdout\nLogin failed\nstderr", true},
		{"partial match - fail only", "Failed", false},
		{"no match - authenticating", "Authenticating...", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := flow.DetectAuthFailure(tt.output); got != tt.want {
				t.Errorf("DetectAuthFailure(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// DetectChallengeCode
// =============================================================================

func TestClaudeAuthFlow_DetectChallengeCode(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"enter code colon", "Enter code: ", true},
		{"enter the code", "Please Enter the code from your browser", true},
		{"no challenge", "Waiting for browser...", false},
		{"empty output", "", false},
		{"enter code embedded", "output\nEnter code: ABCD\nwaiting", true},
		{"enter the code at start", "Enter the code displayed in your browser", true},
		{"unrelated code mention", "Exit code: 0", false},
		{"partial match - just enter", "Enter your email:", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, got := flow.DetectChallengeCode(tt.output)
			if got != tt.want {
				t.Errorf("DetectChallengeCode(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

// =============================================================================
// AuthState constants
// =============================================================================

func TestAuthStateConstants(t *testing.T) {
	t.Parallel()

	// Verify distinct values
	states := []AuthState{AuthInProgress, AuthNeedsBrowser, AuthNeedsChallenge, AuthSuccess, AuthFailed}
	seen := make(map[AuthState]bool)
	for _, s := range states {
		if seen[s] {
			t.Errorf("duplicate AuthState value: %q", s)
		}
		seen[s] = true
		if s == "" {
			t.Error("AuthState constant should not be empty")
		}
	}
}
