package health

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestParseWaitTime(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"try again in 60s", 60},
		{"wait 30 seconds", 30},
		{"retry after 120s", 120},
		{"Rate limit exceeded, 45s cooldown", 45},
		{"no wait time here", 0},
	}

	for _, tt := range tests {
		got := parseWaitTime(tt.input)
		if got != tt.want {
			t.Errorf("parseWaitTime(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestDetectErrors(t *testing.T) {
	tests := []struct {
		input    string
		wantType string
	}{
		{"Rate limit exceeded", "rate_limit"},
		{"HTTP 429 Too Many Requests", "rate_limit"},
		{"Authentication failed", "auth_error"},
		{"panic: runtime error", "crash"},
		{"connection refused", "network_error"},
		{"everything is fine", ""},
	}

	for _, tt := range tests {
		issues := detectErrors(tt.input)
		if tt.wantType == "" {
			if len(issues) > 0 {
				t.Errorf("detectErrors(%q) returned issues, want none", tt.input)
			}
		} else {
			found := false
			for _, issue := range issues {
				if issue.Type == tt.wantType {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("detectErrors(%q) did not return type %q", tt.input, tt.wantType)
			}
		}
	}
}

func TestDetectErrors_IgnoresStaleHistoryBeyondLookback(t *testing.T) {
	t.Parallel()

	output := "panic: old crash message\n" +
		strings.Repeat("working normally\n", errorLookbackLines+5) +
		"claude>\n"

	issues := detectErrors(output)
	if len(issues) != 0 {
		t.Fatalf("detectErrors returned %d issue(s) from stale history, want 0: %+v", len(issues), issues)
	}
}

func TestDetectErrorsForAgent_DetectsCrashBeyondShortLookbackWithoutPrompt(t *testing.T) {
	t.Parallel()

	output := "panic: unrecovered crash\n" +
		strings.Repeat("compilation output\n", errorLookbackLines+5)

	issues := detectErrorsForAgent(output, "cc")
	if !hasIssueType(issues, "crash") {
		t.Fatalf("detectErrorsForAgent missed unrecovered crash beyond short lookback: %+v", issues)
	}
}

func TestCheckAgentCopiesPaneShellPIDBeforeCapture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := checkAgent(ctx, tmux.PaneActivity{
		Pane: tmux.Pane{
			ID:    "%not-real",
			Index: 7,
			Type:  tmux.AgentCodex,
			PID:   4242,
		},
	})

	if got.ShellPID != 4242 {
		t.Fatalf("ShellPID = %d, want 4242", got.ShellPID)
	}
	if got.Pane != 7 {
		t.Fatalf("Pane = %d, want 7", got.Pane)
	}
}

func TestDetectErrorsForAgent_IgnoresRecoveredCrashAtPrompt(t *testing.T) {
	t.Parallel()

	output := "panic: recovered crash\n" +
		strings.Repeat("working normally\n", errorLookbackLines+5) +
		"claude>\n"

	issues := detectErrorsForAgent(output, "cc")
	if len(issues) != 0 {
		t.Fatalf("detectErrorsForAgent returned %d issue(s) from recovered history, want 0: %+v", len(issues), issues)
	}
}

func TestDetectErrorsForAgent_DetectsErrorAfterPrompt(t *testing.T) {
	t.Parallel()

	output := "panic: recovered crash\n" +
		strings.Repeat("working normally\n", errorLookbackLines+5) +
		"claude>\n" +
		"error: command failed\n"

	issues := detectErrorsForAgent(output, "cc")
	if !hasIssueType(issues, "error") {
		t.Fatalf("detectErrorsForAgent missed error after prompt: %+v", issues)
	}
	if hasIssueType(issues, "crash") {
		t.Fatalf("detectErrorsForAgent revived crash before prompt: %+v", issues)
	}
}

// bd-9b0et: agent panicked, recovered (prompt visible), then resumed
// productive work that pushed the prompt out of the trailing-3-line
// window status.DetectIdleFromOutput uses. The recovered crash must
// stay suppressed regardless of how far the prompt has scrolled,
// because a prompt anywhere in the buffer is proof of recovery.
func TestDetectErrorsForAgent_IgnoresRecoveredCrashWhenPromptScrolledPastTrailingWindow(t *testing.T) {
	t.Parallel()

	output := "panic: old crash\n" +
		strings.Repeat("compilation output\n", 30) +
		"claude>\n" + // recovery marker
		strings.Repeat("Working on file\n", 10) // pushes prompt past the trailing 3-line window

	issues := detectErrorsForAgent(output, "cc")
	if hasIssueType(issues, "crash") {
		t.Fatalf("detectErrorsForAgent re-fired recovered crash after prompt scrolled: %+v", issues)
	}
}

func TestDetectRateLimitWithAgentContext(t *testing.T) {
	t.Parallel()

	const output = "Error: insufficient_quota"

	detection := detectRateLimit(output, "openai-codex")
	if !detection.RateLimited {
		t.Fatalf("expected Codex alias to detect rate limit for %q", output)
	}

	nonCodex := detectRateLimit(output, "cc")
	if nonCodex.RateLimited {
		t.Fatalf("did not expect Claude agent to match Codex-specific rate limit pattern for %q", output)
	}
}

func TestDetectRateLimit_IgnoresStaleHistoryBeyondLookback(t *testing.T) {
	t.Parallel()

	output := "Rate limit exceeded, try again in 60s\n" +
		strings.Repeat("working normally\n", rateLimitLookbackLines+5)

	detection := detectRateLimit(output, "cc")
	if detection.RateLimited {
		t.Fatalf("detectRateLimit detected stale rate limit beyond lookback: %+v", detection)
	}
}

func TestDetectRateLimit_DetectsCurrentRateLimitFromFullInput(t *testing.T) {
	t.Parallel()

	output := strings.Repeat("working normally\n", rateLimitLookbackLines+5) +
		"Rate limit exceeded, try again in 60s\n"

	detection := detectRateLimit(output, "cc")
	if !detection.RateLimited {
		t.Fatalf("detectRateLimit missed current rate limit in tail: %+v", detection)
	}
	if detection.WaitSeconds != 60 {
		t.Fatalf("detectRateLimit WaitSeconds = %d, want 60", detection.WaitSeconds)
	}
}

func TestDetectRateLimit_ExtendsContextForRetryChatter(t *testing.T) {
	t.Parallel()

	output := "Rate limit exceeded, try again in 60s\n" +
		strings.Repeat("retrying request\n", rateLimitLookbackLines+5)

	detection := detectRateLimit(output, "cc")
	if !detection.RateLimited {
		t.Fatalf("detectRateLimit missed rate limit followed by retry chatter: %+v", detection)
	}
	if detection.WaitSeconds != 60 {
		t.Fatalf("detectRateLimit WaitSeconds = %d, want 60", detection.WaitSeconds)
	}
}

func TestDetectProgress(t *testing.T) {
	tests := []struct {
		output   string
		activity ActivityLevel
		want     ProgressStage
	}{
		{"Let me analyze this...", ActivityActive, StageStarting},
		{"Editing file main.go...", ActivityActive, StageWorking},
		{"All tests passed.", ActivityActive, StageFinishing},
		{"Error: unable to compile.", ActivityActive, StageStuck},
		{"", ActivityIdle, StageIdle},
	}

	for _, tt := range tests {
		p := detectProgress(tt.output, tt.activity, nil)
		if p.Stage != tt.want {
			t.Errorf("detectProgress(%q) = %v, want %v", tt.output, p.Stage, tt.want)
		}
	}
}

func TestDetectActivity(t *testing.T) {
	// With timestamp
	now := time.Now()
	active := detectActivity("output", now.Add(-10*time.Second), "user")
	if active != ActivityActive {
		t.Errorf("Expected Active for recent output, got %v", active)
	}

	stale := detectActivity("output", now.Add(-10*time.Minute), "user")
	if stale != ActivityStale {
		t.Errorf("Expected Stale for old output, got %v", stale)
	}

	// Without timestamp (rely on prompt)
	// Use ">" which is a generic prompt pattern
	idle := detectActivity("> ", time.Time{}, "user")
	if idle != ActivityIdle {
		t.Errorf("Expected Idle for prompt without timestamp, got %v", idle)
	}

	// Recent timestamp but prompt visible -> Idle (new behavior)
	// Use "cc" agent type for claude prompt
	idleWithTime := detectActivity("claude>", now.Add(-5*time.Second), "cc")
	if idleWithTime != ActivityIdle {
		t.Errorf("Expected Idle for prompt with recent timestamp, got %v", idleWithTime)
	}
}

func TestCalculateStatus(t *testing.T) {
	// Healthy
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
	}
	if s := calculateStatus(h); s != StatusOK {
		t.Errorf("Expected OK, got %v", s)
	}

	// Error
	h.ProcessStatus = ProcessExited
	if s := calculateStatus(h); s != StatusError {
		t.Errorf("Expected Error for exited process, got %v", s)
	}

	// Warning
	h.ProcessStatus = ProcessRunning
	h.Activity = ActivityStale
	if s := calculateStatus(h); s != StatusWarning {
		t.Errorf("Expected Warning for stale activity, got %v", s)
	}
}

func TestIsSessionMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cant find session", err: errors.New("can't find session: ntm"), want: true},
		{name: "no server running", err: errors.New("no server running on /tmp/tmux-1000/default"), want: true},
		{name: "no sessions", err: errors.New("no sessions"), want: true},
		{name: "transport failure", err: errors.New("error connecting to host"), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSessionMissing(tt.err); got != tt.want {
				t.Fatalf("isSessionMissing(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestNormalizeConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		score float64
		want  float64
	}{
		{"very high score", 1.5, 0.95},
		{"score exactly 1.0", 1.0, 0.95},
		{"score 0.8", 0.8, 0.85},
		{"score exactly 0.7", 0.7, 0.85},
		{"score 0.6", 0.6, 0.75},
		{"score exactly 0.5", 0.5, 0.75},
		{"score 0.4", 0.4, 0.60},
		{"score exactly 0.3", 0.3, 0.60},
		{"low score 0.2", 0.2, 0.50},
		{"very low score 0.1", 0.1, 0.50},
		{"zero score", 0.0, 0.50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeConfidence(tt.score)
			if got != tt.want {
				t.Errorf("normalizeConfidence(%v) = %v, want %v", tt.score, got, tt.want)
			}
		})
	}
}

func TestDedupeIndicators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  int // expected unique count
	}{
		{"empty slice", []string{}, 0},
		{"nil slice", nil, 0},
		{"no duplicates", []string{"a", "b", "c"}, 3},
		{"all duplicates", []string{"a", "a", "a"}, 1},
		{"mixed duplicates", []string{"a", "b", "a", "c", "b"}, 3},
		{"single element", []string{"x"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dedupeIndicators(tt.input)
			if len(got) != tt.want {
				t.Errorf("dedupeIndicators(%v) returned %d items, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestDedupeIndicators_PreservesOrder(t *testing.T) {
	t.Parallel()

	input := []string{"c", "a", "b", "a", "c"}
	got := dedupeIndicators(input)
	expected := []string{"c", "a", "b"}

	if len(got) != len(expected) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(expected))
	}
	for i, v := range got {
		if v != expected[i] {
			t.Errorf("dedupeIndicators[%d] = %q, want %q", i, v, expected[i])
		}
	}
}

func TestHasRateLimitIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issues []Issue
		want   bool
	}{
		{"empty issues", []Issue{}, false},
		{"nil issues", nil, false},
		{"rate limit present", []Issue{{Type: "rate_limit"}}, true},
		{"other issue only", []Issue{{Type: "crash"}}, false},
		{"mixed with rate limit", []Issue{{Type: "crash"}, {Type: "rate_limit"}}, true},
		{"multiple non-rate", []Issue{{Type: "auth_error"}, {Type: "network_error"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasRateLimitIssue(tt.issues)
			if got != tt.want {
				t.Errorf("hasRateLimitIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasErrorIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issues []Issue
		want   bool
	}{
		{"empty issues", []Issue{}, false},
		{"nil issues", nil, false},
		{"crash present", []Issue{{Type: "crash"}}, true},
		{"auth_error present", []Issue{{Type: "auth_error"}}, true},
		{"network_error present", []Issue{{Type: "network_error"}}, true},
		{"rate_limit only", []Issue{{Type: "rate_limit"}}, false},
		{"error type only", []Issue{{Type: "error"}}, false},
		{"mixed with crash", []Issue{{Type: "rate_limit"}, {Type: "crash"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasErrorIssue(tt.issues)
			if got != tt.want {
				t.Errorf("hasErrorIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status Status
		want   int
	}{
		{StatusOK, 0},
		{StatusUnknown, 1}, // between OK and Warning
		{StatusWarning, 2},
		{StatusError, 3},
		{Status("invalid"), 1}, // unknown defaults to Unknown severity
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			got := statusSeverity(tt.status)
			if got != tt.want {
				t.Errorf("statusSeverity(%v) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

func TestDetectProcessStatus(t *testing.T) {
	t.Parallel()

	// These tests use shellPID=0 to exercise the text-based fallback path.
	tests := []struct {
		name    string
		output  string
		command string
		want    ProcessStatus
	}{
		{"exit status in output", "exit status 1", "python", ProcessExited},
		{"exited with in output", "process exited with code 0", "node", ProcessExited},
		{"connection closed", "connection closed by remote host", "ssh", ProcessExited},
		{"session ended", "session ended", "tmux", ProcessExited},
		{"normal output with bash", "some output", "bash", ProcessRunning},
		{"normal output with zsh", "some output", "zsh", ProcessRunning},
		{"normal output with sh", "some output", "sh", ProcessRunning},
		{"empty command", "some output", "", ProcessRunning},
		{"normal output non-shell", "some output", "python", ProcessRunning},
		{"empty output non-shell", "", "node", ProcessRunning},
		{"case insensitive exit", "Exit Status 127", "python", ProcessExited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectProcessStatus(tt.output, tt.command, 0)
			if got != tt.want {
				t.Errorf("detectProcessStatus(%q, %q, 0) = %v, want %v", tt.output, tt.command, got, tt.want)
			}
		})
	}
}

func TestDetectProcessStatus_PIDBasedCurrentProcess(t *testing.T) {
	t.Parallel()

	// Use the current process PID as the "shell". The test runner is our
	// child, so HasChildAlive should return true for any PID that has
	// children. We use PID 1 (init/systemd) which always has children.
	// With a valid shell PID that has children, text patterns are ignored.
	got := detectProcessStatus("exit status 1", "python", 1)
	if got != ProcessRunning {
		t.Errorf("detectProcessStatus with PID 1 (has children) = %v, want ProcessRunning", got)
	}
}

func TestDetectProcessStatusForAgent_PromptOverridesExitText(t *testing.T) {
	t.Parallel()

	output := "connection closed by remote host\nclaude>\n"
	got := detectProcessStatusForAgent(output, "python", 0, "cc")
	if got != ProcessRunning {
		t.Fatalf("detectProcessStatusForAgent(prompt+exit-text) = %v, want %v", got, ProcessRunning)
	}
}

func TestDetectProcessStatus_IgnoresStaleExitTextBeyondLookback(t *testing.T) {
	t.Parallel()

	output := "session ended\n" + strings.Repeat("still running\n", processExitLookbackLines+2)
	got := detectProcessStatus(output, "python", 0)
	if got != ProcessRunning {
		t.Fatalf("detectProcessStatus(stale-exit-history) = %v, want %v", got, ProcessRunning)
	}
}

func TestCalculateStatus_DetailedCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		h    AgentHealth
		want Status
	}{
		{
			name: "crash issue returns error",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityActive,
				Issues:        []Issue{{Type: "crash", Message: "panic"}},
			},
			want: StatusError,
		},
		{
			name: "auth_error returns error",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityActive,
				Issues:        []Issue{{Type: "auth_error", Message: "unauthorized"}},
			},
			want: StatusError,
		},
		{
			name: "rate_limit returns warning",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityActive,
				Issues:        []Issue{{Type: "rate_limit", Message: "429"}},
			},
			want: StatusWarning,
		},
		{
			name: "network_error returns warning",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityActive,
				Issues:        []Issue{{Type: "network_error", Message: "refused"}},
			},
			want: StatusWarning,
		},
		{
			name: "idle process is ok",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityIdle,
				Issues:        []Issue{},
			},
			want: StatusOK,
		},
		{
			name: "unknown everything",
			h: AgentHealth{
				ProcessStatus: ProcessUnknown,
				Activity:      ActivityUnknown,
				Issues:        []Issue{},
			},
			want: StatusUnknown,
		},
		{
			name: "crash takes precedence over rate_limit",
			h: AgentHealth{
				ProcessStatus: ProcessRunning,
				Activity:      ActivityActive,
				Issues:        []Issue{{Type: "rate_limit"}, {Type: "crash"}},
			},
			want: StatusError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := calculateStatus(tt.h)
			if got != tt.want {
				t.Errorf("calculateStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionNotFoundError(t *testing.T) {
	t.Parallel()

	err := &SessionNotFoundError{Session: "my-session"}
	got := err.Error()
	want := "session 'my-session' not found"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDetectProgress_WithLargeOutput(t *testing.T) {
	t.Parallel()

	// Output larger than 2000 chars should be truncated to last 2000
	largePrefix := make([]byte, 3000)
	for i := range largePrefix {
		largePrefix[i] = 'x'
	}
	output := string(largePrefix) + "\nI have completed the task."
	p := detectProgress(output, ActivityActive, nil)
	if p.Stage != StageFinishing {
		t.Errorf("detectProgress with large output: got stage %v, want StageFinishing", p.Stage)
	}
}

func TestDetectProgress_ErrorIssuePreempts(t *testing.T) {
	t.Parallel()

	// Even with finishing output, error issues should preempt
	issues := []Issue{{Type: "crash", Message: "panic"}}
	p := detectProgress("I have completed the task.", ActivityActive, issues)
	if p.Stage != StageStuck {
		t.Errorf("detectProgress with error issues: got stage %v, want StageStuck", p.Stage)
	}
}

func TestDetectProgress_ConfidenceAndIndicators(t *testing.T) {
	t.Parallel()

	// Verify progress returns non-zero confidence and indicators for matched stage
	p := detectProgress("Editing file and running tests", ActivityActive, nil)
	if p.Stage == StageUnknown {
		t.Fatal("Expected a matched stage, got unknown")
	}
	if p.Confidence <= 0 {
		t.Errorf("Expected positive confidence, got %f", p.Confidence)
	}
	if len(p.Indicators) == 0 {
		t.Error("Expected non-empty indicators")
	}
}

// bd-v9sbz: hasRateLimitChatter must catch the phrasings of the
// major agent CLIs (claude/cc, codex/cod, gemini/gmi) so the
// 50-line second-chance scan in detectRateLimit doesn't miss
// rate-limit messages that are common in the wild.
func TestHasRateLimitChatter_CoversCommonAgentPhrasings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, input string
		want        bool
	}{
		{"empty", "", false},
		{"unrelated", "the build completed normally", false},

		{"throttle marker", "API throttled, slow down", true},
		{"retry word", "Please retry the request", true},
		{"try again", "rate-limited; try again later", true},
		{"cooldown", "in cooldown for 60s", true},

		// New agent-specific phrasings added by bd-v9sbz.
		{"claude rate limit", "Rate limit reached; please retry", true},
		{"openai too many", "429 Too Many Requests", true},
		{"gemini quota", "Quota exceeded for this model", true},
		{"google resource exhausted", "RESOURCE_EXHAUSTED: try again", true},
		{"codex rate exceeded", "rate exceeded; backing off", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasRateLimitChatter(c.input); got != c.want {
				t.Errorf("hasRateLimitChatter(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

// bd-brr6h: CheckSession's parallel checkAgent goroutines append to
// health.Agents in completion order. The function sorts the result by
// (Pane, PaneID) before returning so JSON output is byte-stable across
// calls. This test pins the expected post-sort ordering by exercising
// the same comparator on a hand-shuffled slice.
func TestCheckSession_AgentsAreSortedByPaneIndex(t *testing.T) {
	t.Parallel()

	// Hand-shuffled Agents — completion-order arrival from concurrent
	// goroutines.
	got := []AgentHealth{
		{Pane: 3, PaneID: "%30"},
		{Pane: 1, PaneID: "%10"},
		{Pane: 0, PaneID: "%0"},
		{Pane: 2, PaneID: "%20"},
		// Same Pane, different PaneID — secondary sort key.
		{Pane: 1, PaneID: "%11"},
	}

	// Mirror the inline comparator from CheckSession (bd-brr6h).
	sort.SliceStable(got, func(i, j int) bool {
		if got[i].Pane != got[j].Pane {
			return got[i].Pane < got[j].Pane
		}
		return got[i].PaneID < got[j].PaneID
	})

	want := []struct {
		pane   int
		paneID string
	}{
		{0, "%0"},
		{1, "%10"},
		{1, "%11"},
		{2, "%20"},
		{3, "%30"},
	}
	for i, w := range want {
		if got[i].Pane != w.pane || got[i].PaneID != w.paneID {
			t.Errorf("post-sort[%d] = (Pane=%d PaneID=%q), want (Pane=%d PaneID=%q)",
				i, got[i].Pane, got[i].PaneID, w.pane, w.paneID)
		}
	}
}
