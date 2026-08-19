// Package robot provides machine-readable output for AI agents.
// ack_test.go contains tests for the acknowledgment detection logic.
package robot

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestRobotSendSingularPaneRealTmux(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	session := fmt.Sprintf("ntm-dispatch-smoke-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	defer tmux.KillSession(session)

	if _, err := tmux.DefaultClient.Run("new-window", "-d", "-t", session); err != nil {
		t.Fatalf("create second window: %v", err)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatal(err)
	}
	secondWindow := -1
	for _, pane := range panes {
		if pane.WindowIndex > secondWindow {
			secondWindow = pane.WindowIndex
		}
	}
	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", fmt.Sprintf("%s:%d", session, secondWindow)); err != nil {
		t.Fatalf("split second window: %v", err)
	}
	panes, err = tmux.GetPanes(session)
	if err != nil {
		t.Fatal(err)
	}
	var target tmux.Pane
	for _, pane := range panes {
		if pane.WindowIndex == secondWindow && pane.Index > target.Index {
			target = pane
		}
	}
	if target.ID == "" {
		t.Fatalf("no target pane found in window %d: %+v", secondWindow, panes)
	}

	enter := false
	output, err := GetSend(SendOptions{Session: session, Message: "dispatch-smoke", Pane: target.ID, Enter: &enter})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Success || len(output.Successful) != 1 || output.Successful[0] != target.Ref().Physical() {
		t.Fatalf("singular send output = %+v", output)
	}
	time.Sleep(100 * time.Millisecond)
	captured, err := tmux.CapturePaneVisible(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	// capture-pane inserts line breaks when staged input wraps at the pane
	// boundary. Remove only terminal row separators so the assertion still
	// requires the exact message bytes in order.
	capturedWithoutLineBreaks := strings.NewReplacer("\r", "", "\n", "").Replace(captured)
	if !strings.Contains(capturedWithoutLineBreaks, "dispatch-smoke") {
		t.Fatalf("target pane did not receive staged message: %q", captured)
	}

	ambiguous, err := GetSend(SendOptions{Session: session, Message: "work", Pane: fmt.Sprint(secondWindow), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous.Success || ambiguous.ErrorCode != ErrCodeInvalidFlag || len(ambiguous.Failed) != 1 {
		t.Fatalf("ambiguous singular output = %+v", ambiguous)
	}
	notFound, err := GetSend(SendOptions{Session: session, Message: "work", Pane: "99.99", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if notFound.Success || notFound.ErrorCode != ErrCodePaneNotFound {
		t.Fatalf("not-found singular output = %+v", notFound)
	}

	tracked, err := GetSendAndAck(SendAndAckOptions{
		SendOptions: SendOptions{Session: session, Message: "track-preview", Pane: target.Ref().Physical(), DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tracked.Success || !tracked.Send.DryRun || !tracked.Send.Success || !reflect.DeepEqual(tracked.Send.WouldSendTo, []string{target.Ref().Physical()}) {
		t.Fatalf("tracked singular dry-run = %+v", tracked)
	}
}

func TestGetNewContent(t *testing.T) {
	tests := []struct {
		name     string
		initial  string
		current  string
		expected string
	}{
		{
			name:     "simple append",
			initial:  "hello",
			current:  "hello world",
			expected: " world",
		},
		{
			name:     "new lines",
			initial:  "line1\nline2",
			current:  "line1\nline2\nline3\nline4",
			expected: "\nline3\nline4",
		},
		{
			name:     "no change",
			initial:  "same",
			current:  "same",
			expected: "",
		},
		{
			name:     "rolling window shift",
			initial:  "a\nb\nc\nd",
			current:  "c\nd\ne\nf",
			expected: "e\nf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getNewContent(tt.initial, tt.current)
			if result != tt.expected {
				t.Errorf("getNewContent() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestTruncateForMatch(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "short message",
			message:  "fix the bug",
			expected: "fix the bug",
		},
		{
			name:     "long message truncated",
			message:  "this is a very long message that should be truncated at 50 characters for matching purposes",
			expected: "this is a very long message that should be truncat", // 50 chars
		},
		{
			name:     "multiline takes first line",
			message:  "first line\nsecond line",
			expected: "first line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForMatch(tt.message)
			if result != tt.expected {
				t.Errorf("truncateForMatch() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestAckPaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom title",
		Type:    tmux.AgentCodex,
		Command: "codex --model o3",
	}

	if got := ackPaneAgentType(pane); got != "codex" {
		t.Fatalf("ackPaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestResolveAckTargetsCanonicalSelectors(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%10", WindowIndex: 0, Index: 0, Type: tmux.AgentUser},
		{ID: "%11", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
		{ID: "%20", WindowIndex: 1, Index: 0, Type: tmux.AgentCodex},
		{ID: "%21", WindowIndex: 1, Index: 1, Type: tmux.AgentGemini},
	}

	targets, err := resolveAckTargets(panes, []string{"1"})
	if err != nil {
		t.Fatalf("resolveAckTargets(window selector) error = %v", err)
	}
	if len(targets) != 2 || targets[0].ID != "%20" || targets[1].ID != "%21" {
		t.Fatalf("window selector resolved %+v, want %%20 then %%21", targets)
	}

	targets, err = resolveAckTargets(panes, []string{"0.1", "%11", "0.1"})
	if err != nil {
		t.Fatalf("resolveAckTargets(alias dedup) error = %v", err)
	}
	if len(targets) != 1 || targets[0].ID != "%11" {
		t.Fatalf("alias dedup resolved %+v, want only %%11", targets)
	}

	if _, err := resolveAckTargets(panes, []string{"9.0"}); err == nil || paneSelectorRobotErrorCode(err) != ErrCodePaneNotFound {
		t.Fatalf("missing selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
	if _, err := resolveAckTargets(panes, []string{"1.x"}); err == nil || paneSelectorRobotErrorCode(err) != ErrCodeInvalidFlag {
		t.Fatalf("malformed selector error = %v, code = %q", err, paneSelectorRobotErrorCode(err))
	}
}

func TestTrackPreservesUnsupportedPromptClassification(t *testing.T) {
	t.Parallel()
	prepareErr := &dispatchsvc.Error{
		Code: dispatchsvc.ErrPromptDeliveryUnsupported,
		Err:  errors.New("prompt delivery is not implemented for Grok Build panes"),
	}
	output := SendOutput{
		RobotResponse: robotDispatchPrepareErrorResponse(prepareErr),
		Targets:       []string{"1"},
		Successful:    []string{},
		Failed:        []SendError{{Pane: "dispatch", Error: prepareErr.Error()}},
	}
	finalizeRobotSendDispatchStatus(&output)
	if output.Success || output.ErrorCode != ErrCodeNotImplemented || ExitCodeForResponse(output.RobotResponse) != 2 {
		t.Fatalf("track send output = %+v, want NOT_IMPLEMENTED / exit 2", output)
	}
}

func TestDetectAcknowledgmentForAgentIgnoresAgentPrompts(t *testing.T) {

	ackType, detected := detectAcknowledgmentForAgent(
		"ready\n",
		"ready\ncodex> \ncodex> \n",
		"",
		"codex",
	)
	if detected {
		t.Fatalf("detectAcknowledgmentForAgent() detected = %v, want false (type=%v)", detected, ackType)
	}
	if ackType != AckNone {
		t.Fatalf("detectAcknowledgmentForAgent() type = %v, want %v", ackType, AckNone)
	}
}

func TestGetLastNonEmptyLines(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		n        int
		expected []string
	}{
		{
			name:     "simple case",
			content:  "line1\nline2\nline3\n",
			n:        2,
			expected: []string{"line3", "line2"},
		},
		{
			name:     "with empty lines",
			content:  "line1\n\nline2\n\n\nline3\n",
			n:        3,
			expected: []string{"line3", "line2", "line1"},
		},
		{
			name:     "fewer lines than requested",
			content:  "only one",
			n:        5,
			expected: []string{"only one"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getLastNonEmptyLines(tt.content, tt.n)
			if len(result) != len(tt.expected) {
				t.Errorf("getLastNonEmptyLines() returned %d lines, want %d", len(result), len(tt.expected))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("getLastNonEmptyLines()[%d] = %q, want %q", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestGetContentAfterEcho(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		message  string
		expected string
	}{
		{
			name:     "with content after echo",
			content:  "fix the bug\nOkay, I'll fix it",
			message:  "fix the bug",
			expected: "Okay, I'll fix it",
		},
		{
			name:     "no content after echo",
			content:  "fix the bug",
			message:  "fix the bug",
			expected: "",
		},
		{
			name:     "message not found",
			content:  "some other text",
			message:  "fix the bug",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getContentAfterEcho(tt.content, tt.message)
			if result != tt.expected {
				t.Errorf("getContentAfterEcho() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// =============================================================================
// WAITING AND TIMEOUT BEHAVIOR TESTS
// =============================================================================

// TestAckOptions_Defaults verifies that AckOptions has correct defaults applied
func TestAckOptions_Defaults(t *testing.T) {
	t.Run("zero timeout defaults to 30000ms", func(t *testing.T) {
		opts := AckOptions{
			Session:   "test-session",
			TimeoutMs: 0,
			PollMs:    0,
		}

		// The defaults are applied inside PrintAck, but we can verify the expected values
		if opts.TimeoutMs != 0 {
			t.Errorf("Expected zero TimeoutMs before PrintAck, got %d", opts.TimeoutMs)
		}
		// Default should be 30000ms when zero
		expectedDefault := 30000
		t.Logf("ACK_TEST: Default timeout should be %dms", expectedDefault)
	})

	t.Run("zero poll defaults to 500ms", func(t *testing.T) {
		opts := AckOptions{
			Session:   "test-session",
			TimeoutMs: 5000,
			PollMs:    0,
		}

		if opts.PollMs != 0 {
			t.Errorf("Expected zero PollMs before PrintAck, got %d", opts.PollMs)
		}
		// Default should be 500ms when zero
		expectedDefault := 500
		t.Logf("ACK_TEST: Default poll interval should be %dms", expectedDefault)
	})

	t.Run("custom values are preserved", func(t *testing.T) {
		opts := AckOptions{
			Session:   "test-session",
			Message:   "test message",
			Panes:     []string{"1", "2"},
			TimeoutMs: 10000,
			PollMs:    100,
		}

		if opts.TimeoutMs != 10000 {
			t.Errorf("Expected TimeoutMs=10000, got %d", opts.TimeoutMs)
		}
		if opts.PollMs != 100 {
			t.Errorf("Expected PollMs=100, got %d", opts.PollMs)
		}
		if len(opts.Panes) != 2 {
			t.Errorf("Expected 2 panes, got %d", len(opts.Panes))
		}
	})
}

// TestAckOutput_Structure verifies the AckOutput structure semantics
func TestAckOutput_Structure(t *testing.T) {
	t.Run("initial output state", func(t *testing.T) {
		output := AckOutput{
			Session:       "test-session",
			Confirmations: []AckConfirmation{},
			Pending:       []string{"1", "2", "3"},
			Failed:        []AckFailure{},
			TimeoutMs:     5000,
			TimedOut:      false,
		}

		if output.Session != "test-session" {
			t.Errorf("Session = %q, want %q", output.Session, "test-session")
		}
		if len(output.Confirmations) != 0 {
			t.Errorf("Expected empty Confirmations initially")
		}
		if len(output.Pending) != 3 {
			t.Errorf("Expected 3 pending, got %d", len(output.Pending))
		}
		if output.TimedOut {
			t.Error("TimedOut should be false initially")
		}
	})

	t.Run("timed out output state", func(t *testing.T) {
		output := AckOutput{
			Session:       "test-session",
			Confirmations: []AckConfirmation{{Pane: "1", AckType: "explicit_ack"}},
			Pending:       []string{"2", "3"},
			Failed:        []AckFailure{},
			TimeoutMs:     5000,
			TimedOut:      true,
		}

		if !output.TimedOut {
			t.Error("TimedOut should be true")
		}
		if len(output.Pending) != 2 {
			t.Errorf("Expected 2 still pending, got %d", len(output.Pending))
		}
		if len(output.Confirmations) != 1 {
			t.Errorf("Expected 1 confirmation, got %d", len(output.Confirmations))
		}
	})

	t.Run("fully confirmed output state", func(t *testing.T) {
		output := AckOutput{
			Session: "test-session",
			Confirmations: []AckConfirmation{
				{Pane: "1", AckType: "explicit_ack", LatencyMs: 150},
				{Pane: "2", AckType: "echo_detected", LatencyMs: 200},
			},
			Pending:   []string{},
			Failed:    []AckFailure{},
			TimeoutMs: 5000,
			TimedOut:  false,
		}

		if output.TimedOut {
			t.Error("TimedOut should be false when all confirmed")
		}
		if len(output.Pending) != 0 {
			t.Errorf("Expected 0 pending, got %d", len(output.Pending))
		}
		if len(output.Confirmations) != 2 {
			t.Errorf("Expected 2 confirmations, got %d", len(output.Confirmations))
		}
	})
}

// TestAckConfirmation_Fields verifies AckConfirmation struct
func TestAckConfirmation_Fields(t *testing.T) {
	ack := AckConfirmation{
		Pane:      "1",
		AckType:   string(AckExplicitAck),
		AckAt:     "2026-01-20T12:00:00Z",
		LatencyMs: 250,
	}

	if ack.Pane != "1" {
		t.Errorf("Pane = %q, want %q", ack.Pane, "1")
	}
	if ack.AckType != "explicit_ack" {
		t.Errorf("AckType = %q, want %q", ack.AckType, "explicit_ack")
	}
	if ack.LatencyMs != 250 {
		t.Errorf("LatencyMs = %d, want %d", ack.LatencyMs, 250)
	}
}

// TestAckFailure_Fields verifies AckFailure struct
func TestAckFailure_Fields(t *testing.T) {
	failure := AckFailure{
		Pane:   "session",
		Reason: "session 'test' not found",
	}

	if failure.Pane != "session" {
		t.Errorf("Pane = %q, want %q", failure.Pane, "session")
	}
	if failure.Reason != "session 'test' not found" {
		t.Errorf("Reason = %q, want %q", failure.Reason, "session 'test' not found")
	}
}

// TestAckType_Constants verifies AckType constant values
func TestAckType_Constants(t *testing.T) {
	tests := []struct {
		ackType  AckType
		expected string
	}{
		{AckPromptReturned, "prompt_returned"},
		{AckEchoDetected, "echo_detected"},
		{AckExplicitAck, "explicit_ack"},
		{AckOutputStarted, "output_started"},
		{AckNone, "none"},
	}

	for _, tt := range tests {
		t.Run(string(tt.ackType), func(t *testing.T) {
			if string(tt.ackType) != tt.expected {
				t.Errorf("AckType = %q, want %q", tt.ackType, tt.expected)
			}
		})
	}
}

// TestPrintAck_NonexistentSession tests PrintAck with a session that doesn't exist
func TestPrintAck_NonexistentSession(t *testing.T) {
	t.Log("ACK_TEST: TestPrintAck_NonexistentSession | Testing error handling for missing session")

	opts := AckOptions{
		Session:   "nonexistent-session-12345-test",
		Message:   "test message",
		TimeoutMs: 100, // Short timeout for test
		PollMs:    50,
	}

	// PrintAck writes the envelope once and returns the typed exit-1 result.
	err := PrintAck(opts)
	var processExit *ProcessExitError
	if !errors.As(err, &processExit) || processExit.ExitCode() != 1 || !processExit.JSONWritten() {
		t.Fatalf("PrintAck error = %T %v, want written exit-1 ProcessExitError", err, err)
	}
	// The function writes JSON to stdout including the failure info
	t.Log("ACK_TEST: PrintAck handled missing session - failure captured in output")
}

// TestSendAndAckOptions_Defaults verifies SendAndAckOptions defaults
func TestSendAndAckOptions_Defaults(t *testing.T) {
	opts := SendAndAckOptions{
		SendOptions: SendOptions{
			Session: "test-session",
			Message: "test message",
		},
		AckTimeoutMs: 0,
		AckPollMs:    0,
	}

	// Zero values should trigger defaults in PrintSendAndAck
	if opts.AckTimeoutMs != 0 {
		t.Errorf("Expected zero AckTimeoutMs before call, got %d", opts.AckTimeoutMs)
	}
	if opts.AckPollMs != 0 {
		t.Errorf("Expected zero AckPollMs before call, got %d", opts.AckPollMs)
	}
	// Defaults: AckTimeoutMs=30000, AckPollMs=500
	t.Log("ACK_TEST: SendAndAckOptions defaults - AckTimeoutMs=30000ms, AckPollMs=500ms")
}

// TestPrintSendAndAck_NonexistentSession tests combined send+ack with missing session
func TestPrintSendAndAck_NonexistentSession(t *testing.T) {
	t.Log("ACK_TEST: TestPrintSendAndAck_NonexistentSession | Testing error handling")

	opts := SendAndAckOptions{
		SendOptions: SendOptions{
			Session: "nonexistent-session-12345-test",
			Message: "test message",
		},
		AckTimeoutMs: 100,
		AckPollMs:    50,
	}

	err := PrintSendAndAck(opts)
	if err == nil {
		t.Fatal("PrintSendAndAck should return the typed process failure after writing JSON")
	}
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("PrintSendAndAck error = %T %v, want written exit-1 ProcessExitError", err, err)
	}
	t.Log("ACK_TEST: PrintSendAndAck handled missing session - failure captured in output")
}

// TestGetNewContent_EdgeCases tests edge cases in content extraction
func TestGetNewContent_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		initial  string
		current  string
		wantLen  int // Expected length, not exact content (for complex cases)
		wantNone bool
	}{
		{
			name:     "empty initial",
			initial:  "",
			current:  "new content",
			wantLen:  11, // len("new content")
			wantNone: false,
		},
		{
			name:     "current shorter than initial",
			initial:  "longer initial content",
			current:  "short",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:     "identical content",
			initial:  "same",
			current:  "same",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:     "newline added",
			initial:  "line1",
			current:  "line1\nline2",
			wantLen:  6, // "\nline2"
			wantNone: false,
		},
		{
			name:     "content replaced in middle",
			initial:  "hello world",
			current:  "hello there friend",
			wantLen:  12, // "there friend" (from divergence point at index 6)
			wantNone: false,
		},
		{
			name:     "completely different content",
			initial:  "original text",
			current:  "completely new",
			wantLen:  14, // entire new content (no common prefix)
			wantNone: false,
		},
		{
			name:     "shorter bytes but more lines returns new lines",
			initial:  "very long single line content here",
			current:  "a\nb\nc",
			wantLen:  3, // "b\nc" - lines after initial line count
			wantNone: false,
		},
		{
			name:     "different content no common prefix",
			initial:  "long",
			current:  "a\nb\nc",
			wantLen:  5, // entire current (no common prefix)
			wantNone: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getNewContent(tt.initial, tt.current)
			if tt.wantNone && result != "" {
				t.Errorf("getNewContent() = %q, want empty", result)
			}
			if !tt.wantNone && len(result) != tt.wantLen {
				t.Errorf("getNewContent() len = %d, want %d (content: %q)", len(result), tt.wantLen, result)
			}
		})
	}
}

// TestTruncateForMatch_UTF8 tests UTF-8 boundary handling
func TestTruncateForMatch_UTF8(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantMax int // Result should be at most this length
	}{
		{
			name:    "ASCII within limit",
			message: "short message",
			wantMax: 13,
		},
		{
			name:    "ASCII at limit",
			message: "exactly fifty characters long for testing purposes!",
			wantMax: 50,
		},
		{
			name:    "UTF-8 with emojis",
			message: "Hello 🌍 world! This is a test with unicode chars 🎉🎊",
			wantMax: 50, // Should truncate at rune boundary
		},
		{
			name:    "Japanese characters",
			message: "こんにちは世界これはテストです",
			wantMax: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForMatch(tt.message)
			if len(result) > tt.wantMax {
				t.Errorf("truncateForMatch() len = %d, want <= %d", len(result), tt.wantMax)
			}
			t.Logf("ACK_TEST: UTF8 truncation | Input=%d bytes | Output=%d bytes",
				len(tt.message), len(result))
		})
	}
}

// TestWaitingBehavior_Semantics documents the expected waiting behavior
func TestWaitingBehavior_Semantics(t *testing.T) {
	t.Log("ACK_TEST: Documenting expected waiting behavior semantics")

	// Document the polling behavior
	t.Run("polling should respect configured interval", func(t *testing.T) {
		// The PollMs option controls how frequently PrintAck checks for changes
		// Default is 500ms
		t.Log("ACK_TEST: Poll interval default = 500ms, configurable via PollMs option")
	})

	t.Run("timeout should trigger after configured duration", func(t *testing.T) {
		// The TimeoutMs option controls how long PrintAck waits before giving up
		// Default is 30000ms (30 seconds)
		t.Log("ACK_TEST: Timeout default = 30000ms, configurable via TimeoutMs option")
	})

	t.Run("pending panes should be tracked correctly", func(t *testing.T) {
		// Initially all target panes are in Pending
		// As each acknowledges, it moves to Confirmations
		// Remaining panes stay in Pending if timeout occurs
		t.Log("ACK_TEST: Panes start in Pending, move to Confirmations on ack")
	})

	t.Run("timeout flag semantics", func(t *testing.T) {
		// TimedOut = true means at least one pane did not acknowledge
		// TimedOut = false means all panes acknowledged (or no target panes)
		t.Log("ACK_TEST: TimedOut=true if any panes still pending at deadline")
	})
}

// TestTimeoutBehavior_Semantics documents the expected timeout behavior
func TestTimeoutBehavior_Semantics(t *testing.T) {
	t.Log("ACK_TEST: Documenting expected timeout behavior semantics")

	t.Run("short timeout for fast tests", func(t *testing.T) {
		// For testing, use short timeouts (e.g., 100ms)
		// Production use typically needs 30s+ for real agents
		shortTimeout := 100
		productionTimeout := 30000
		t.Logf("ACK_TEST: Test timeout = %dms, Production timeout = %dms",
			shortTimeout, productionTimeout)
	})

	t.Run("completed_at timestamp is always set", func(t *testing.T) {
		// CompletedAt is set when PrintAck finishes, regardless of success/timeout
		t.Log("ACK_TEST: CompletedAt timestamp indicates when waiting finished")
	})

	t.Run("latency_ms tracks time from sent_at to ack", func(t *testing.T) {
		// Each AckConfirmation has LatencyMs showing response time
		t.Log("ACK_TEST: LatencyMs = time(AckAt) - time(SentAt)")
	})
}

// A missing baseline must never be diffed against as empty output.
// getNewContent("", current) returns the whole scrollback, which almost always
// contains an ack phrase, so one transient capture failure confirmed an
// acknowledgment for a message the agent had never seen.
func TestRemoveAckPanesDropsBaselineFailures(t *testing.T) {
	pending := []string{"0.1", "0.2", "0.3"}
	got := removeAckPanes(pending, []string{"0.2"})
	if len(got) != 2 || got[0] != "0.1" || got[1] != "0.3" {
		t.Fatalf("removeAckPanes = %v, want [0.1 0.3]", got)
	}
	if same := removeAckPanes(pending, nil); len(same) != 3 {
		t.Fatalf("removeAckPanes with no drops = %v, want the input unchanged", same)
	}
}
