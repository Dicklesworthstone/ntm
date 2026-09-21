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
	flow.captureOutput = func(paneID string, lines int) (string, error) {
		if paneID != "pane-1" || lines != 30 {
			t.Fatalf("baseline capture = (%q, %d), want (pane-1, 30)", paneID, lines)
		}
		return "", nil
	}

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

// =============================================================================
// DetectAuthFailure
// =============================================================================

// =============================================================================
// Challenge prompt classification
// =============================================================================

func TestClaudeAuthFlow_ChallengePromptState(t *testing.T) {
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
			got := flow.latestAuthResult(tt.output) == AuthNeedsChallenge
			if got != tt.want {
				t.Errorf("challenge prompt in %q = %v, want %v", tt.output, got, tt.want)
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

func TestClaudeAuthFlow_MonitorAuthUsesLatestPendingSignal(t *testing.T) {
	t.Parallel()
	const url = "https://claude.ai/login?state=current"
	for _, tc := range []struct {
		name, output string
		want         AuthState
	}{
		{"old success then browser", "Login successful\n" + url, AuthNeedsBrowser},
		{"old success then challenge", "Successfully logged in\nEnter code:", AuthNeedsChallenge},
		{"old failure then browser", "Authentication failed\n" + url, AuthNeedsBrowser},
		{"old failure then challenge", "Login failed\nEnter the code from your browser", AuthNeedsChallenge},
		{"old challenge then new browser", "Enter code:\n" + url, AuthNeedsBrowser},
		{"browser then challenge", url + "\nEnter code:", AuthNeedsChallenge},
		{"browser then current success", url + "\nLogin successful", AuthSuccess},
		{"challenge then current failure", "Enter code:\nError logging in", AuthFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			flow := NewClaudeAuthFlow(false)
			flow.pollInterval = time.Millisecond
			flow.captureOutput = func(string, int) (string, error) { return tc.output, nil }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := flow.MonitorAuth(ctx, "%42")
			if err != nil || got == nil || got.State != tc.want {
				t.Fatalf("MonitorAuth() = %+v, %v; want %s", got, err, tc.want)
			}
			if tc.want == AuthNeedsBrowser && got.URL != url {
				t.Fatalf("current browser URL = %q, want %q", got.URL, url)
			}
		})
	}
}

func TestClaudeAuthFlow_DetectBrowserURLUsesNewestLink(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(true)
	got, ok := flow.DetectBrowserURL("https://claude.ai/login?state=expired\nOpen (https://claude.ai/login?state=current).")
	if !ok || got != "https://claude.ai/login?state=current" {
		t.Fatalf("returned an expired browser flow: %q, found=%v", got, ok)
	}
}

func TestClaudeAuthFlow_CancellationDuringCaptureIsNotSuccess(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flow.captureOutput = func(string, int) (string, error) {
		cancel()
		return "Login successful", nil
	}
	got, err := flow.MonitorAuth(ctx, "%42")
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled auth reported success: result=%+v err=%v", got, err)
	}
}

func TestClaudeAuthFlow_InitiateIgnoresPriorScrollback(t *testing.T) {
	t.Parallel()
	const before = "banner\nLogin successful\n> \n \n\n"
	const url = "https://claude.ai/login?state=current"
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	sent, polls := false, 0
	flow.sendKeys = func(string, string, bool) error { sent = true; return nil }
	flow.captureOutput = func(string, int) (string, error) {
		if !sent {
			return before, nil
		}
		polls++
		if polls == 1 {
			return before, nil // /login has not redrawn the pane yet.
		}
		// The viewport has scrolled and the prompt line has changed.
		return "Login successful\n> /login\n" + url, nil
	}
	if err := flow.InitiateAuth("%42"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := flow.MonitorAuth(ctx, "%42")
	if err != nil || got == nil || got.State != AuthNeedsBrowser || got.URL != url || polls != 2 {
		t.Fatalf("accepted previous login as this attempt: result=%+v polls=%d err=%v", got, polls, err)
	}
}

func TestClaudeAuthFlow_InitiateFailsBeforeSendingWithoutBaseline(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	failure := errors.New("pane unavailable")
	flow.captureOutput = func(string, int) (string, error) { return "", failure }
	sent := false
	flow.sendKeys = func(string, string, bool) error { sent = true; return nil }
	if err := flow.InitiateAuth("%42"); !errors.Is(err, failure) || sent {
		t.Fatalf("started auth without knowing previous output: sent=%v err=%v", sent, err)
	}
}

func TestClaudeAuthFlow_WaitForAuthHandlesRepeatedPromptsAndTransitions(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	frames := []string{
		"Login successful\n> ", // Pre-/login baseline.
		"Login successful\n> ", // No new output yet.
		"Login successful\n> /login\nhttps://claude.ai/login?state=first",
		"Login successful\n> /login\nhttps://claude.ai/login?state=first",
		"https://claude.ai/login?state=first\nhttps://claude.ai/login?state=second",
		"https://claude.ai/login?state=second\nEnter code:",
		"https://claude.ai/login?state=second\nEnter code:",
		"Enter code:\nSuccessfully logged in as the new account",
	}
	captures := 0
	flow.captureOutput = func(string, int) (string, error) {
		if captures >= len(frames) {
			return "", errors.New("auth read beyond successful frame")
		}
		frame := frames[captures]
		captures++
		return frame, nil
	}
	flow.sendKeys = func(string, string, bool) error {
		if captures != 1 {
			t.Fatal("/login sent before baseline was captured")
		}
		return nil
	}
	flow.sleep = func(time.Duration) {}
	continued := false
	flow.pasteKeys = func(string, string, bool) error {
		if captures != len(frames) {
			t.Fatal("continuation reached pane before current authentication completed")
		}
		continued = true
		return nil
	}
	if err := flow.InitiateAuth("%42"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var prompts []AuthResult
	if err := flow.WaitForAuth(ctx, "%42", func(result AuthResult) { prompts = append(prompts, result) }); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 || prompts[0].URL != "https://claude.ai/login?state=first" ||
		prompts[1].URL != "https://claude.ai/login?state=second" || prompts[2].State != AuthNeedsChallenge {
		t.Fatalf("pending transitions lost or spammed: %+v", prompts)
	}
	if err := flow.SendContinuation("%42", "continue"); err != nil || !continued {
		t.Fatalf("successful auth did not continue: sent=%v err=%v", continued, err)
	}
}

func TestClaudeAuthFlow_WaitForAuthPropagatesFailure(t *testing.T) {
	t.Parallel()
	for _, captureFailure := range []bool{false, true} {
		flow := NewClaudeAuthFlow(false)
		flow.pollInterval = time.Millisecond
		captures := 0
		failure := errors.New("pane disappeared during login")
		flow.captureOutput = func(string, int) (string, error) {
			captures++
			if captures == 1 {
				return "Enter code:", nil
			}
			if captureFailure {
				return "", failure
			}
			return "Enter code:\nAuthentication failed", nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := flow.WaitForAuth(ctx, "%42", nil)
		cancel()
		if err == nil || captures != 2 {
			t.Fatalf("auth failure was not terminal: captures=%d err=%v", captures, err)
		}
		if captureFailure && !errors.Is(err, failure) {
			t.Fatalf("lost capture error identity: %v", err)
		}
	}
}

func TestClaudeAuthFlow_WaitForAuthKeepsPendingUntilDeadline(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(true)
	flow.pollInterval = time.Millisecond
	flow.captureOutput = func(string, int) (string, error) {
		return "https://claude.ai/login?state=pending", nil
	}
	prompts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := flow.WaitForAuth(ctx, "%42", func(AuthResult) { prompts++ })
	if !errors.Is(err, context.DeadlineExceeded) || prompts > 1 {
		t.Fatalf("pending prompt failed early or spammed: prompts=%d err=%v", prompts, err)
	}
}

func TestClaudeAuthFlow_WaitForAuthHonorsCancellationFromProgress(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	captures := 0
	flow.captureOutput = func(string, int) (string, error) {
		captures++
		return "Enter code:", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := flow.WaitForAuth(ctx, "%42", func(AuthResult) { cancel() })
	if !errors.Is(err, context.Canceled) || captures != 1 {
		t.Fatalf("cancelled wait kept polling: captures=%d err=%v", captures, err)
	}
}

func TestAuthOutputAfterBaseline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, before, after, want string }{
		{"unchanged", "Login successful\n> \n \n", "Login successful\n> \n \n", ""},
		{"appended", "banner\n> ", "banner\n> \nLogin successful", "> \nLogin successful"},
		{"scrolled", "banner\nLogin successful\n>", "Login successful\n> /login\nEnter code:", "> /login\nEnter code:"},
		{"cleared", "Login successful\n>", "Enter code:", "Enter code:"},
		{"matching first letter is not a line", "Error logging in", "Enter code:", "Enter code:"},
		{"empty baseline", "", "Login successful", "Login successful"},
		{"empty output", "Login successful", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authOutputAfterBaseline(tc.before, tc.after); got != tc.want {
				t.Fatalf("new auth output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeAuthFlow_InitiatePreservesSendError(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	flow.captureOutput = func(string, int) (string, error) { return "Login successful", nil }
	failure := errors.New("could not send /login")
	flow.sendKeys = func(string, string, bool) error { return failure }
	if err := flow.InitiateAuth("%42"); !errors.Is(err, failure) {
		t.Fatalf("lost send failure: %v", err)
	}
	if len(flow.baselines) != 0 {
		t.Fatal("failed login changed the active attempt baseline")
	}
}

func TestClaudeAuthFlow_RecognizesRepeatedSuccessAfterRedraw(t *testing.T) {
	t.Parallel()
	flow := NewClaudeAuthFlow(false)
	flow.pollInterval = time.Millisecond
	frames := []string{
		"Login successful",                  // Previous login, captured before /login.
		"https://claude.ai/login?state=new", // New attempt replaces the viewport.
		"Enter code:",
		"Login successful", // The new attempt legitimately uses the same text.
	}
	captures := 0
	flow.captureOutput = func(string, int) (string, error) {
		if captures == len(frames) {
			return "", errors.New("new success was ignored")
		}
		frame := frames[captures]
		captures++
		return frame, nil
	}
	flow.sendKeys = func(string, string, bool) error { return nil }
	if err := flow.InitiateAuth("%42"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := flow.WaitForAuth(ctx, "%42", nil); err != nil {
		t.Fatal(err)
	}
	if captures != len(frames) {
		t.Fatalf("authenticated before the fresh success frame: captures=%d", captures)
	}
}
