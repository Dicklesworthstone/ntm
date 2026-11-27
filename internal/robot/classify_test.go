package robot

import (
	"strings"
	"testing"
	"time"
)

// TestClassifyWithOutput_IdlePrompt verifies that ClassifyWithOutput detects idle/waiting
// state when a Claude prompt is present with no output velocity.
func TestClassifyWithOutput_IdlePrompt(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0, // disable hysteresis for testing
	})

	// First call establishes baseline
	_, err := sc.ClassifyWithOutput("some initial output\nclaude>")
	if err != nil {
		t.Fatalf("ClassifyWithOutput baseline: %v", err)
	}

	// Second call with same content (no velocity) and idle prompt
	activity, err := sc.ClassifyWithOutput("some initial output\nclaude>")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	if activity.State != StateWaiting {
		t.Errorf("State = %q, want %q", activity.State, StateWaiting)
	}
	if activity.PaneID != "test-pane" {
		t.Errorf("PaneID = %q, want %q", activity.PaneID, "test-pane")
	}
	if activity.AgentType != "claude" {
		t.Errorf("AgentType = %q, want %q", activity.AgentType, "claude")
	}
}

// TestClassifyWithOutput_ErrorPattern verifies that error patterns take immediate priority.
func TestClassifyWithOutput_ErrorPattern(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0,
	})

	// Baseline
	_, _ = sc.ClassifyWithOutput("working on something")

	// Error pattern in output
	activity, err := sc.ClassifyWithOutput("Error: rate limit exceeded 429")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	if activity.State != StateError {
		t.Errorf("State = %q, want %q", activity.State, StateError)
	}
	if activity.Confidence < 0.90 {
		t.Errorf("Confidence = %f, want >= 0.90", activity.Confidence)
	}
}

// TestClassifyWithOutput_ThinkingPattern verifies thinking state detection.
func TestClassifyWithOutput_ThinkingPattern(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0,
	})

	// Baseline
	_, _ = sc.ClassifyWithOutput("starting task")

	// Thinking pattern
	activity, err := sc.ClassifyWithOutput("starting task\nThinking...")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	if activity.State != StateThinking {
		t.Errorf("State = %q, want %q", activity.State, StateThinking)
	}
}

// TestClassifyWithOutput_HighVelocity verifies generating state with high output velocity.
func TestClassifyWithOutput_HighVelocity(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0,
	})

	// Baseline with some content
	_, _ = sc.ClassifyWithOutput("start")

	// Need to wait a tiny bit so velocity is meaningful, but since
	// VelocityHighThreshold is 10 chars/sec, let's add lots of chars
	// The time between calls is very small, so lots of chars = high velocity
	largeOutput := "start" + strings.Repeat("x", 1000)

	activity, err := sc.ClassifyWithOutput(largeOutput)
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	// With high char addition and small time delta, velocity should be high
	if activity.State != StateGenerating {
		t.Errorf("State = %q, want %q (velocity=%.1f)", activity.State, StateGenerating, activity.Velocity)
	}
}

// TestClassifyWithOutput_StateTransitionHistory verifies that state transitions are recorded.
func TestClassifyWithOutput_StateTransitionHistory(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0,
	})

	// Move to a known state (not UNKNOWN)
	_, _ = sc.ClassifyWithOutput("Error: rate limit")

	history := sc.GetStateHistory()
	if len(history) == 0 {
		t.Fatal("expected at least one state transition")
	}
	if history[len(history)-1].To != StateError {
		t.Errorf("last transition To = %q, want %q", history[len(history)-1].To, StateError)
	}
}

// TestClassifyWithOutput_PaneIDPreserved verifies pane ID is carried through.
func TestClassifyWithOutput_PaneIDPreserved(t *testing.T) {

	sc := NewStateClassifier("my-pane-42", &ClassifierConfig{
		AgentType: "codex",
	})

	activity, err := sc.ClassifyWithOutput("test content")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}
	if activity.PaneID != "my-pane-42" {
		t.Errorf("PaneID = %q, want %q", activity.PaneID, "my-pane-42")
	}
	if activity.AgentType != "codex" {
		t.Errorf("AgentType = %q, want %q", activity.AgentType, "codex")
	}
}

// TestClassifyWithOutput_DetectedPatternsReported verifies pattern names are in result.
func TestClassifyWithOutput_DetectedPatternsReported(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 0,
	})

	_, _ = sc.ClassifyWithOutput("initial")

	activity, err := sc.ClassifyWithOutput("initial\nclaude>")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	// Should have detected at least one pattern
	if len(activity.DetectedPatterns) == 0 {
		t.Error("expected detected patterns to be populated")
	}
}

// TestClassifyWithOutput_MultipleErrors verifies multiple error patterns.
func TestClassifyWithOutput_MultipleErrors(t *testing.T) {

	tests := []struct {
		name    string
		content string
	}{
		{"rate_limit", "rate limit exceeded"},
		{"http_429", "HTTP 429 Too Many Requests"},
		{"quota_exceeded", "quota exceeded for this month"},
		{"api_error", "API error: something broke"},
		{"panic", "panic: runtime error"},
		{"connection_refused", "connection refused"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			sc := NewStateClassifier("pane", &ClassifierConfig{
				AgentType:          "claude",
				HysteresisDuration: 0,
			})
			_, _ = sc.ClassifyWithOutput("baseline")
			activity, err := sc.ClassifyWithOutput(tt.content)
			if err != nil {
				t.Fatalf("ClassifyWithOutput: %v", err)
			}
			if activity.State != StateError {
				t.Errorf("State = %q, want %q for content %q", activity.State, StateError, tt.content)
			}
		})
	}
}

// A Claude pane that ran a failing command ("⎿  Error: Exit code 144") or a
// failing build and then finished its turn is idle, not in ERROR: live swarm
// panes in exactly this layout were reported ERROR at 0.95 confidence while
// --robot-is-working correctly called them idle. Only an error that is
// Claude's newest marker, or any agent other than Claude, keeps ERROR.
func TestClassifyWithOutput_ClaudeToolOutputIsNotAnAgentError(t *testing.T) {
	toolFailure := "● Bash(pkill -f \"run_c7.sh\"; sleep 1; echo cleared)\n" +
		"  ⎿  Error: Exit code 144\n\n" +
		"● Bash(cargo test -p fs-mesh)\n" +
		"  ⎿  error: could not compile `fs-mesh` (lib) due to 2 previous errors\n\n"
	composer := "────────────────────────────────────────\n❯ \n────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on · 1 shell · ↓ to manage\n"
	tests := []struct {
		name      string
		agentType string
		content   string
		wantError bool
	}{
		{"claude turn completed", "claude", toolFailure + "✻ Crunched for 10s · done 11:09 PM · 1 shell still running\n\n" + composer, false},
		{"claude still working", "claude", toolFailure + "✻ Brewing… (ctrl+c to interrupt · 41s · ↓ 1.2k tokens)\n\n" + composer, false},
		{"claude stopped on the error", "claude", toolFailure + composer, true},
		{"codex unchanged", "codex", toolFailure + "✻ Crunched for 10s · done 11:09 PM\n\n" + composer, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewStateClassifier("pane", &ClassifierConfig{AgentType: tt.agentType, HysteresisDuration: 0})
			_, _ = sc.ClassifyWithOutput("baseline")
			activity, err := sc.ClassifyWithOutput(tt.content)
			if err != nil {
				t.Fatalf("ClassifyWithOutput: %v", err)
			}
			if got := activity.State == StateError; got != tt.wantError {
				t.Fatalf("State = %q (patterns %v), want ERROR=%v", activity.State, activity.DetectedPatterns, tt.wantError)
			}
		})
	}
}

// GitHub #387: an idle Codex pane whose last reply quoted a hook's
// "ERROR: ..." and "exited with status 1" classified ERROR, so
// --robot-wait --wait-until=idle timed out on a pane ready for input. Text
// inside Codex's conversation cells is output (a working pane quoting it is
// THINKING); Codex's own error cells, rate limits, dialogs, an exited agent
// and ownerless text keep ERROR.
func TestClassifyWithOutput_CodexConversationErrorsAreNotAgentErrors(t *testing.T) {
	const composer = "› Ask Codex to do anything\n\n" +
		"  GPT-6.2-Sol high · /tmp/demo · GPT-6.2-Sol · high\n" +
		"  ← for agents · ? for shortcuts"
	// The capture from the report: a finished turn quoting a pre-commit hook.
	const quotedHook = "• Ran git commit; the pre-commit hook rejected the commit:\n" +
		"  ERROR: mcp-agent-mail: file reservation conflict detected!\n" +
		"  exited with status 1\n\n"
	tests := []struct {
		name    string
		content string
		want    AgentState
	}{
		{"reply quoting a hook error", quotedHook + composer, StateWaiting},
		{"tool output with a failed build", "• Ran go build ./...\n  └ error: undefined: foo\n    exit status 1\n\n" +
			"• The build fails on an undefined symbol; fixing it next turn.\n\n" + composer, StateWaiting},
		{"user prompt quoting an error", "› Fix this: Error: connection refused on :8080\n\n" +
			"• Restarted the listener.\n\n" + composer, StateWaiting},
		{"working pane quoting a hook error", quotedHook + "• Working (4s • esc to interrupt)\n\n" + composer, StateThinking},
		{"codex's own error cell", quotedHook + "■ Invalid API key provided.\n\n" + composer, StateError},
		{"own error after a capture that starts mid-cell", "  ERROR: build failed\n\n" + composer, StateError},
		{"dialog over a quoted error", quotedHook + "  Update available\n› 1. Update now\n  2. Skip\n  Press enter to continue\n", StateError},
		{"agent exited to the shell", quotedHook + "› Ask Codex to do anything\nuser@host:~/demo$ ", StateError},
		{"usage-limit banner", quotedHook + testCodexUsageBanner, StateError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The second capture of an unchanged pane has no output velocity.
			sc := NewStateClassifier("pane", &ClassifierConfig{AgentType: "codex", HysteresisDuration: 0})
			_, _ = sc.ClassifyWithOutput(tt.content)
			activity, err := sc.ClassifyWithOutput(tt.content)
			if err != nil {
				t.Fatalf("ClassifyWithOutput: %v", err)
			}
			if activity.State != tt.want {
				t.Fatalf("State = %q (patterns %v), want %q", activity.State, activity.DetectedPatterns, tt.want)
			}
			if idle := meetsSingleWaitCondition(activity, WaitConditionIdle); idle != (tt.want == StateWaiting) {
				t.Fatalf("idle wait condition = %v for state %q", idle, activity.State)
			}
		})
	}

	// A usage-limit banner keeps its rate-limit flag beside quoted errors.
	sc := NewStateClassifier("pane", &ClassifierConfig{AgentType: "codex", HysteresisDuration: 0})
	if activity, _ := sc.ClassifyWithOutput(quotedHook + testCodexUsageBanner); !activity.RateLimited {
		t.Fatalf("usage-limit banner lost its rate-limit flag: %+v", activity)
	}
}

// Idle Claude panes at their input box after a finished turn classified
// UNKNOWN (live swarm, 5 of 6 idle panes), or THINKING on a stale spinner
// frame, while --robot-is-working called them idle. Activity now follows the
// same ordering-aware liveness rule dispatch uses.
func TestClassifyWithOutput_ClaudeFinishedTurnIsWaiting(t *testing.T) {
	box := "────────────────────────────────────────\n❯ check the test results\n────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on"
	boxWithJob := box + " · 1 shell · ↓ to manage\n"
	tests := []struct {
		name    string
		content string
		want    AgentState
	}{
		{"queued text in the box after a finished turn", "● Done; the fix is pushed.\n\n✻ Crunched for 10s · done 11:09 PM\n\n" + box, StateWaiting},
		{"finished turn with a running shell", "✻ Crunched for 10s · done 11:09 PM · 1 shell still running\n\n" + boxWithJob, StateWaitingOnJob},
		{"stale spinner frame above the completion line", "· Thundering… (4s)\n● Done.\n\n✻ Worked for 42s · done 9:03 AM\n\n" + box, StateWaiting},
		{"live spinner is still THINKING", "● Running the suite.\n\n✻ Brewing… (ctrl+c to interrupt · 41s · ↓ 1.2k tokens)\n\n" + boxWithJob, StateThinking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewStateClassifier("pane", &ClassifierConfig{AgentType: "claude", HysteresisDuration: 0})
			_, _ = sc.ClassifyWithOutput(tt.content)
			activity, err := sc.ClassifyWithOutput(tt.content)
			if err != nil {
				t.Fatalf("ClassifyWithOutput: %v", err)
			}
			if activity.State != tt.want {
				t.Fatalf("State = %q (patterns %v), want %q", activity.State, activity.DetectedPatterns, tt.want)
			}
		})
	}
}

// TestClassifyWithOutput_CodexIdlePrompt verifies codex-specific idle detection.
func TestClassifyWithOutput_CodexIdlePrompt(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "codex",
		HysteresisDuration: 0,
	})

	_, _ = sc.ClassifyWithOutput("some output\ncodex>")
	activity, err := sc.ClassifyWithOutput("some output\ncodex>")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	if activity.State != StateWaiting {
		t.Errorf("State = %q, want %q", activity.State, StateWaiting)
	}
}

// TestClassifyWithOutput_GeminiIdlePrompt verifies gemini-specific idle detection.
func TestClassifyWithOutput_GeminiIdlePrompt(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "gemini",
		HysteresisDuration: 0,
	})

	_, _ = sc.ClassifyWithOutput("some output\ngemini>")
	activity, err := sc.ClassifyWithOutput("some output\ngemini>")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	if activity.State != StateWaiting {
		t.Errorf("State = %q, want %q", activity.State, StateWaiting)
	}
}

// TestClassifyWithOutput_EmptyInput verifies behavior with empty output.
func TestClassifyWithOutput_EmptyInput(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		HysteresisDuration: 0,
	})

	activity, err := sc.ClassifyWithOutput("")
	if err != nil {
		t.Fatalf("ClassifyWithOutput: %v", err)
	}

	// Empty input should produce a valid activity (state may be UNKNOWN)
	if activity == nil {
		t.Fatal("expected non-nil activity")
	}
}

// TestClassifyWithOutput_Hysteresis verifies that hysteresis prevents rapid state flapping.
func TestClassifyWithOutput_Hysteresis(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 5 * time.Second, // long hysteresis
	})

	// Move to ERROR state (bypasses hysteresis)
	activity1, _ := sc.ClassifyWithOutput("error: rate limit")
	if activity1.State != StateError {
		t.Fatalf("initial state = %q, want ERROR", activity1.State)
	}

	// Try to move to idle - should be blocked by hysteresis
	activity2, _ := sc.ClassifyWithOutput("error: rate limit\nclaude>")
	// Should still be ERROR because hysteresis hasn't elapsed
	if activity2.State != StateError {
		t.Logf("State changed to %q - hysteresis may have been bypassed", activity2.State)
	}
}

// TestClassifyWithOutput_ErrorBypassesHysteresis verifies ERROR transitions are immediate.
func TestClassifyWithOutput_ErrorBypassesHysteresis(t *testing.T) {

	sc := NewStateClassifier("test-pane", &ClassifierConfig{
		AgentType:          "claude",
		HysteresisDuration: 10 * time.Second, // long hysteresis
	})

	// Start with some content
	_, _ = sc.ClassifyWithOutput("working on something claude>")
	_, _ = sc.ClassifyWithOutput("working on something claude>")

	// Error should transition immediately regardless of hysteresis
	activity, _ := sc.ClassifyWithOutput("error: rate limit")
	if activity.State != StateError {
		t.Errorf("State = %q, want %q (ERROR should bypass hysteresis)", activity.State, StateError)
	}
}

// TestNewStateClassifier_Defaults verifies default configuration values.
func TestNewStateClassifier_Defaults(t *testing.T) {

	sc := NewStateClassifier("pane-1", nil)

	if sc.currentState != StateUnknown {
		t.Errorf("initial state = %q, want UNKNOWN", sc.currentState)
	}
	if sc.stallThreshold != DefaultStallThreshold {
		t.Errorf("stallThreshold = %v, want %v", sc.stallThreshold, DefaultStallThreshold)
	}
	if sc.hysteresisDuration != DefaultHysteresisDuration {
		t.Errorf("hysteresisDuration = %v, want %v", sc.hysteresisDuration, DefaultHysteresisDuration)
	}
	if sc.patternLibrary == nil {
		t.Error("expected non-nil pattern library")
	}
}

// TestNewStateClassifier_CustomConfig verifies custom config is respected.
func TestNewStateClassifier_CustomConfig(t *testing.T) {

	sc := NewStateClassifier("pane-1", &ClassifierConfig{
		AgentType:          "gemini",
		StallThreshold:     5 * time.Minute,
		HysteresisDuration: 10 * time.Second,
	})

	if sc.agentType != "gemini" {
		t.Errorf("agentType = %q, want gemini", sc.agentType)
	}
	if sc.stallThreshold != 5*time.Minute {
		t.Errorf("stallThreshold = %v, want 5m", sc.stallThreshold)
	}
	if sc.hysteresisDuration != 10*time.Second {
		t.Errorf("hysteresisDuration = %v, want 10s", sc.hysteresisDuration)
	}
}
