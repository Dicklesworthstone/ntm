package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// =============================================================================
// Tests for ClassifyStuckPanes
// =============================================================================

func TestClassifyStuckPanes(t *testing.T) {

	tests := []struct {
		name      string
		agents    []SessionAgentHealth
		threshold time.Duration
		wantPanes []int
	}{
		{
			name:      "no agents returns nil",
			agents:    []SessionAgentHealth{},
			threshold: 5 * time.Minute,
			wantPanes: nil,
		},
		{
			name: "healthy agent below threshold not stuck",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 60},
			},
			threshold: 5 * time.Minute,
			wantPanes: nil,
		},
		{
			name: "healthy agent at exact threshold is stuck",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 300},
			},
			threshold: 5 * time.Minute,
			wantPanes: []int{1},
		},
		{
			name: "healthy agent above threshold is stuck",
			agents: []SessionAgentHealth{
				{Pane: 2, Health: "healthy", IdleSinceSeconds: 600},
			},
			threshold: 5 * time.Minute,
			wantPanes: []int{2},
		},
		{
			name: "unhealthy agent above threshold is stuck",
			agents: []SessionAgentHealth{
				{Pane: 3, Health: "unhealthy", IdleSinceSeconds: 400},
			},
			threshold: 5 * time.Minute,
			wantPanes: []int{3},
		},
		{
			name: "degraded agent above threshold is stuck",
			agents: []SessionAgentHealth{
				{Pane: 4, Health: "degraded", IdleSinceSeconds: 350},
			},
			threshold: 5 * time.Minute,
			wantPanes: []int{4},
		},
		{
			name: "unhealthy agent below threshold not stuck",
			agents: []SessionAgentHealth{
				{Pane: 3, Health: "unhealthy", IdleSinceSeconds: 100},
			},
			threshold: 5 * time.Minute,
			wantPanes: nil,
		},
		{
			name: "multiple agents mixed stuck states",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 60},
				{Pane: 2, Health: "healthy", IdleSinceSeconds: 600},
				{Pane: 3, Health: "unhealthy", IdleSinceSeconds: 400},
				{Pane: 4, Health: "degraded", IdleSinceSeconds: 100},
				{Pane: 5, Health: "rate_limited", IdleSinceSeconds: 500},
			},
			threshold: 5 * time.Minute,
			// Pane 5 (rate_limited over threshold) is a typed SKIP as of
			// bd-qz5wk: a restart does not lift a rate limit.
			wantPanes: []int{2, 3},
		},
		{
			name: "custom short threshold catches more",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 60},
				{Pane: 2, Health: "healthy", IdleSinceSeconds: 120},
			},
			threshold: 1 * time.Minute,
			wantPanes: []int{1, 2},
		},
		{
			name: "custom long threshold catches fewer",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 300},
				{Pane: 2, Health: "healthy", IdleSinceSeconds: 600},
				{Pane: 3, Health: "healthy", IdleSinceSeconds: 900},
			},
			threshold: 10 * time.Minute,
			wantPanes: []int{2, 3},
		},
		{
			name: "zero idle seconds not stuck",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 0},
			},
			threshold: 5 * time.Minute,
			wantPanes: nil,
		},
		{
			name: "agent with zero threshold always stuck if any idle",
			agents: []SessionAgentHealth{
				{Pane: 1, Health: "healthy", IdleSinceSeconds: 1},
			},
			threshold: 0,
			wantPanes: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ClassifyStuckPanes(tt.agents, tt.threshold)
			if !intSlicesEqual(got, tt.wantPanes) {
				t.Errorf("ClassifyStuckPanes() = %v, want %v", got, tt.wantPanes)
			}
		})
	}
}

// =============================================================================
// Tests for BuildAutoRestartStuckOutput
// =============================================================================

func TestBuildAutoRestartStuckOutput(t *testing.T) {

	t.Run("basic output with stuck and restarted panes", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("test-session", []int{1, 3}, []int{1, 3}, nil, 5*time.Minute, false)

		if out.Session != "test-session" {
			t.Errorf("Session = %q, want %q", out.Session, "test-session")
		}
		if !intSlicesEqual(out.StuckPanes, []int{1, 3}) {
			t.Errorf("StuckPanes = %v, want [1, 3]", out.StuckPanes)
		}
		if !intSlicesEqual(out.Restarted, []int{1, 3}) {
			t.Errorf("Restarted = %v, want [1, 3]", out.Restarted)
		}
		if out.Threshold != "5m0s" {
			t.Errorf("Threshold = %q, want %q", out.Threshold, "5m0s")
		}
		if out.DryRun {
			t.Error("DryRun should be false")
		}
		if !out.Success {
			t.Error("Success should be true")
		}
	})

	t.Run("dry run mode", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("proj", []int{2}, nil, nil, 10*time.Minute, true)

		if !out.DryRun {
			t.Error("DryRun should be true")
		}
		if !intSlicesEqual(out.StuckPanes, []int{2}) {
			t.Errorf("StuckPanes = %v, want [2]", out.StuckPanes)
		}
		if len(out.Restarted) != 0 {
			t.Errorf("Restarted should be empty, got %v", out.Restarted)
		}
	})

	t.Run("nil stuck panes becomes empty slice", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("s", nil, nil, nil, 5*time.Minute, false)

		if out.StuckPanes == nil {
			t.Error("StuckPanes should be non-nil empty slice")
		}
		if len(out.StuckPanes) != 0 {
			t.Errorf("StuckPanes length = %d, want 0", len(out.StuckPanes))
		}
	})

	t.Run("nil restarted becomes empty slice", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("s", []int{1}, nil, nil, 5*time.Minute, false)

		if out.Restarted == nil {
			t.Error("Restarted should be non-nil empty slice")
		}
		if len(out.Restarted) != 0 {
			t.Errorf("Restarted length = %d, want 0", len(out.Restarted))
		}
	})

	t.Run("with failed panes", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("s", []int{1, 2, 3}, []int{1}, []int{2, 3}, 5*time.Minute, false)

		if !intSlicesEqual(out.Failed, []int{2, 3}) {
			t.Errorf("Failed = %v, want [2, 3]", out.Failed)
		}
		if !intSlicesEqual(out.Restarted, []int{1}) {
			t.Errorf("Restarted = %v, want [1]", out.Restarted)
		}
		if out.Success || out.ErrorCode != ErrCodeInternalError || out.Error == "" {
			t.Fatalf("failed restart response = %+v, want typed terminal failure", out.RobotResponse)
		}
	})

	t.Run("checked_at is populated", func(t *testing.T) {
		out := BuildAutoRestartStuckOutput("s", nil, nil, nil, 5*time.Minute, false)

		if out.CheckedAt == "" {
			t.Error("CheckedAt should be non-empty")
		}
		_, err := time.Parse(time.RFC3339, out.CheckedAt)
		if err != nil {
			t.Errorf("CheckedAt %q is not valid RFC3339: %v", out.CheckedAt, err)
		}
	})

	t.Run("various thresholds", func(t *testing.T) {
		cases := []struct {
			dur  time.Duration
			want string
		}{
			{30 * time.Second, "30s"},
			{5 * time.Minute, "5m0s"},
			{1 * time.Hour, "1h0m0s"},
			{10 * time.Minute, "10m0s"},
		}
		for _, c := range cases {
			out := BuildAutoRestartStuckOutput("s", nil, nil, nil, c.dur, false)
			if out.Threshold != c.want {
				t.Errorf("Threshold for %v = %q, want %q", c.dur, out.Threshold, c.want)
			}
		}
	})
}

// GH#251 phase 2: grok supports automated relaunch, so mixed batches that
// target a grok pane pass the stuck-agent preflight.
func TestValidateAutoRestartStuckAgentsAcceptsGrokInMixedBatch(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, AgentType: "claude", Health: "unhealthy", IdleSinceSeconds: 600},
		{Pane: 2, AgentType: "grok-build", Health: "unhealthy", IdleSinceSeconds: 600},
	}
	if err := validateAutoRestartStuckAgents(agents, []int{1, 2}); err != nil {
		t.Fatalf("validateAutoRestartStuckAgents() error = %v, want nil (grok relaunch is supported)", err)
	}

	if err := validateAutoRestartStuckAgents(agents, []int{1}); err != nil {
		t.Fatalf("single-pane target rejected: %v", err)
	}
}

func TestAutoRestartStuckPreflightRejectsZAI(t *testing.T) {
	err := validateAutoRestartStuckAgents([]SessionAgentHealth{{Pane: 7, AgentType: "zai"}}, []int{7})
	if !errors.Is(err, agent.ErrZAIProfileRelaunchRequired) {
		t.Fatalf("validateAutoRestartStuckAgents(zai) error = %v, want profile-required error", err)
	}
}

// GH#251 phase 2: the whole-batch preflight passes for mixed claude+grok
// batches, and the grok pane is restarted like any other agent pane.
func TestRestartAutoRestartStuckPanesRestartsMixedGrokBatch(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, AgentType: "claude"},
		{Pane: 2, AgentType: "grok"},
	}
	var restartedPanes []string
	restarted, failed, err := restartAutoRestartStuckPanes(
		t.Context(),
		AutoRestartStuckOptions{Session: "mixed"},
		agents,
		[]int{1, 2},
		func(_ context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
			restartedPanes = append(restartedPanes, opts.Panes...)
			return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
		},
	)
	if err != nil {
		t.Fatalf("restartAutoRestartStuckPanes() error = %v, want nil (grok relaunch is supported)", err)
	}
	if !intSlicesEqual(restarted, []int{1, 2}) || failed != nil {
		t.Fatalf("results = restarted %v failed %v, want [1 2] and nil", restarted, failed)
	}
	if len(restartedPanes) != 2 {
		t.Fatalf("restart callback saw panes %v, want both panes including grok", restartedPanes)
	}
}

func TestRestartAutoRestartStuckPanesTreatsTerminalResponseAsFailure(t *testing.T) {
	agents := []SessionAgentHealth{{Pane: 1, AgentType: "claude"}, {Pane: 2, AgentType: "codex"}}
	restarted, failed, err := restartAutoRestartStuckPanes(
		t.Context(),
		AutoRestartStuckOptions{Session: "supported"},
		agents,
		[]int{1, 2},
		func(_ context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
			if opts.Panes[0] == "1" {
				return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
			}
			return &RestartPaneOutput{
				RobotResponse: NewErrorResponse(errors.New("unavailable"), ErrCodeNotImplemented, "hint"),
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("restartAutoRestartStuckPanes() error = %v", err)
	}
	if !intSlicesEqual(restarted, []int{1}) || !intSlicesEqual(failed, []int{2}) {
		t.Fatalf("results = restarted %v failed %v, want [1] and [2]", restarted, failed)
	}
}

func TestRestartAutoRestartStuckPanesRejectsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	restarted, failed, err := restartAutoRestartStuckPanes(
		ctx,
		AutoRestartStuckOptions{Session: "canceled"},
		[]SessionAgentHealth{{Pane: 1, AgentType: "claude"}},
		[]int{1},
		func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error) {
			calls++
			return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
		},
	)
	if !errors.Is(err, context.Canceled) || calls != 0 || restarted != nil || failed != nil {
		t.Fatalf("pre-canceled restart result restarted=%v failed=%v calls=%d err=%v", restarted, failed, calls, err)
	}
}

func TestRestartAutoRestartStuckPanesStopsAfterCallbackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	restarted, failed, err := restartAutoRestartStuckPanes(
		ctx,
		AutoRestartStuckOptions{Session: "cancel-in-flight"},
		[]SessionAgentHealth{{Pane: 1, AgentType: "claude"}, {Pane: 2, AgentType: "codex"}},
		[]int{1, 2},
		func(gotCtx context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
			calls++
			if gotCtx != ctx {
				t.Fatal("auto-restart-stuck replaced the caller context")
			}
			cancel()
			return GetRestartPaneContext(gotCtx, opts)
		},
	)
	if !errors.Is(err, context.Canceled) || calls != 1 || len(restarted) != 0 || !intSlicesEqual(failed, []int{1}) {
		t.Fatalf("callback-canceled restart result restarted=%v failed=%v calls=%d err=%v", restarted, failed, calls, err)
	}
}

func TestRestartAutoRestartStuckPanesStopsOnCallbackCancellationError(t *testing.T) {
	calls := 0
	restarted, failed, err := restartAutoRestartStuckPanes(
		t.Context(),
		AutoRestartStuckOptions{Session: "callback-canceled"},
		[]SessionAgentHealth{{Pane: 1, AgentType: "claude"}, {Pane: 2, AgentType: "codex"}},
		[]int{1, 2},
		func(context.Context, RestartPaneOptions) (*RestartPaneOutput, error) {
			calls++
			return nil, fmt.Errorf("restart transport: %w", context.Canceled)
		},
	)
	if !errors.Is(err, context.Canceled) || calls != 1 || len(restarted) != 0 || !intSlicesEqual(failed, []int{1}) {
		t.Fatalf("callback error cancellation result restarted=%v failed=%v calls=%d err=%v", restarted, failed, calls, err)
	}
}

func TestGetAutoRestartStuckRejectsMissingAndCanceledContexts(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		output, err := GetAutoRestartStuck(nil, AutoRestartStuckOptions{Session: "nil-context"})
		if err != nil || output == nil || output.Success || output.ErrorCode != ErrCodeInternalError ||
			!strings.Contains(output.Error, "context is required") {
			t.Fatalf("nil-context output=%+v err=%v", output, err)
		}
	})

	t.Run("pre-canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		output, err := GetAutoRestartStuck(ctx, AutoRestartStuckOptions{Session: "pre-canceled"})
		if err != nil || output == nil || output.Success || output.ErrorCode != ErrCodeTimeout ||
			!strings.Contains(strings.ToLower(output.Error), "canceled") {
			t.Fatalf("pre-canceled output=%+v err=%v", output, err)
		}
	})
}

// =============================================================================
// Tests for ParseStuckThreshold
// =============================================================================

func TestParseStuckThreshold(t *testing.T) {

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:  "empty returns default",
			input: "",
			want:  DefaultStuckThreshold,
		},
		{
			name:  "5 minutes",
			input: "5m",
			want:  5 * time.Minute,
		},
		{
			name:  "10 minutes",
			input: "10m",
			want:  10 * time.Minute,
		},
		{
			name:  "300 seconds",
			input: "300s",
			want:  300 * time.Second,
		},
		{
			name:  "1 hour",
			input: "1h",
			want:  1 * time.Hour,
		},
		{
			name:  "30 seconds minimum",
			input: "30s",
			want:  30 * time.Second,
		},
		{
			name:  "mixed duration",
			input: "1h30m",
			want:  90 * time.Minute,
		},
		{
			name:    "too short rejected",
			input:   "10s",
			wantErr: true,
		},
		{
			name:    "1 second rejected",
			input:   "1s",
			wantErr: true,
		},
		{
			name:    "invalid format rejected",
			input:   "five_minutes",
			wantErr: true,
		},
		{
			name:    "negative rejected",
			input:   "-5m",
			wantErr: true,
		},
		{
			name:    "zero rejected",
			input:   "0s",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStuckThreshold(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseStuckThreshold(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseStuckThreshold(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("ParseStuckThreshold(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Tests for DefaultStuckThreshold constant
// =============================================================================

func TestDefaultStuckThreshold(t *testing.T) {
	if DefaultStuckThreshold != 5*time.Minute {
		t.Errorf("DefaultStuckThreshold = %v, want 5m", DefaultStuckThreshold)
	}
}

func TestAutoRestartStuckPaneOptionsForwardsEffectiveConfig(t *testing.T) {
	effectiveConfig := config.Default()
	opts := autoRestartStuckPaneOptions(AutoRestartStuckOptions{
		Session: "project",
		Config:  effectiveConfig,
	}, 7, "")

	if opts.Session != "project" || len(opts.Panes) != 1 || opts.Panes[0] != "7" {
		t.Fatalf("restart options target = session %q panes %v", opts.Session, opts.Panes)
	}
	if opts.Config != effectiveConfig {
		t.Fatal("auto-restart discarded the caller's effective config")
	}
}

// A bare pane index resolves to *window* N on a multi-window session, so
// auto-restart must address the pane by its recorded unambiguous target.
// Restarting by index there killed every pane in an unrelated window.
func TestAutoRestartStuckUsesUnambiguousPaneTarget(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, PaneTarget: "0.1", AgentType: "claude"},
		{Pane: 1, PaneTarget: "1.1", AgentType: "codex"},
		{Pane: 2, PaneTarget: "", AgentType: "claude"},
	}

	// The first recorded target for a given index wins, and it is never the
	// bare index when a target exists.
	if got := autoRestartStuckPaneTarget(agents, 1); got != "0.1" {
		t.Fatalf("autoRestartStuckPaneTarget(1) = %q, want %q", got, "0.1")
	}
	// No recorded target (single-window session) falls back to the index.
	if got := autoRestartStuckPaneTarget(agents, 2); got != "2" {
		t.Fatalf("autoRestartStuckPaneTarget(2) = %q, want %q", got, "2")
	}

	opts := autoRestartStuckPaneOptions(AutoRestartStuckOptions{Session: "project"}, 1, "0.1")
	if len(opts.Panes) != 1 || opts.Panes[0] != "0.1" {
		t.Fatalf("restart selector = %v, want [0.1]; a bare index would target window 1", opts.Panes)
	}
}

// The restart loop must consume the recorded target, not the index it iterates.
func TestRestartAutoRestartStuckPanesAddressesRecordedTarget(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, PaneTarget: "1.1", AgentType: "claude", Health: "degraded"},
	}

	var seen []string
	restart := func(_ context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
		seen = append(seen, opts.Panes...)
		return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
	}

	restarted, failed, err := restartAutoRestartStuckPanes(
		context.Background(),
		AutoRestartStuckOptions{Session: "project"},
		agents,
		[]int{1},
		restart,
	)
	if err != nil {
		t.Fatalf("restartAutoRestartStuckPanes: %v", err)
	}
	if len(failed) != 0 || len(restarted) != 1 || restarted[0] != 1 {
		t.Fatalf("restarted=%v failed=%v, want restarted=[1]", restarted, failed)
	}
	if len(seen) != 1 || seen[0] != "1.1" {
		t.Fatalf("restart addressed %v, want [1.1]", seen)
	}
}

// =============================================================================
// Tests for ClassifyStuckPanes edge cases
// =============================================================================

func TestClassifyStuckPanes_AllHealthStates(t *testing.T) {

	threshold := 5 * time.Minute
	thresholdSec := int(threshold.Seconds())

	// rate_limited and blocked above threshold become typed SKIPS as of
	// bd-qz5wk: restarting neither lifts a rate limit nor answers a gate.
	healthStates := []string{"healthy", "degraded", "unhealthy"}
	skipStates := []string{"rate_limited", "blocked"}

	for _, health := range skipStates {
		t.Run("above_threshold_"+health+"_is_skipped", func(t *testing.T) {
			agents := []SessionAgentHealth{
				{Pane: 1, Health: health, IdleSinceSeconds: thresholdSec + 1},
			}
			got, skipped := ClassifyStuckPanes(agents, threshold)
			if len(got) != 0 {
				t.Errorf("health=%q above threshold: got %v as stuck, want skip", health, got)
			}
			if len(skipped) != 1 || skipped[0].Pane != 1 || skipped[0].Reason == "" {
				t.Errorf("health=%q above threshold: skipped=%+v, want one reasoned skip", health, skipped)
			}
		})
	}

	for _, health := range healthStates {
		t.Run("above_threshold_"+health, func(t *testing.T) {
			agents := []SessionAgentHealth{
				{Pane: 1, Health: health, IdleSinceSeconds: thresholdSec + 1},
			}
			got, _ := ClassifyStuckPanes(agents, threshold)
			if len(got) != 1 || got[0] != 1 {
				t.Errorf("health=%q above threshold: got %v, want [1]", health, got)
			}
		})

		t.Run("below_threshold_"+health, func(t *testing.T) {
			agents := []SessionAgentHealth{
				{Pane: 1, Health: health, IdleSinceSeconds: thresholdSec - 1},
			}
			got, _ := ClassifyStuckPanes(agents, threshold)
			if len(got) != 0 {
				t.Errorf("health=%q below threshold: got %v, want []", health, got)
			}
		})
	}
}

func TestClassifyStuckPanes_PreservesPaneOrder(t *testing.T) {

	agents := []SessionAgentHealth{
		{Pane: 5, Health: "healthy", IdleSinceSeconds: 600},
		{Pane: 2, Health: "healthy", IdleSinceSeconds: 600},
		{Pane: 8, Health: "healthy", IdleSinceSeconds: 600},
	}
	got, _ := ClassifyStuckPanes(agents, 5*time.Minute)
	want := []int{5, 2, 8}
	if !intSlicesEqual(got, want) {
		t.Errorf("pane order not preserved: got %v, want %v", got, want)
	}
}

func TestClassifyStuckPanes_LargePaneCount(t *testing.T) {

	agents := make([]SessionAgentHealth, 20)
	for i := range agents {
		agents[i] = SessionAgentHealth{
			Pane:             i,
			Health:           "healthy",
			IdleSinceSeconds: 600,
		}
	}
	got, _ := ClassifyStuckPanes(agents, 5*time.Minute)
	if len(got) != 20 {
		t.Errorf("expected 20 stuck panes, got %d", len(got))
	}
}

// =============================================================================
// Tests for AutoRestartStuckOutput JSON structure
// =============================================================================

func TestAutoRestartStuckOutput_JSONFields(t *testing.T) {

	out := BuildAutoRestartStuckOutput("myproject", []int{1, 3}, []int{1}, []int{3}, 5*time.Minute, false)

	// Verify all fields are set
	if out.Session == "" {
		t.Error("Session should be set")
	}
	if out.Threshold == "" {
		t.Error("Threshold should be set")
	}
	if out.CheckedAt == "" {
		t.Error("CheckedAt should be set")
	}
}

// =============================================================================
// Helpers
// =============================================================================

func intSlicesEqual(a, b []int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stuckPanes carries WINDOW-LOCAL pane indices, so on a window-per-agent
// layout every stuck agent reports index 0. Resolving each entry independently
// by first match returned window 0's target for all of them: one agent was
// killed and relaunched three times while the other two stayed stuck and were
// reported as restarted.
func TestRestartAutoRestartStuckPanes_WindowPerAgentTargetsDistinctPanes(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 0, PaneTarget: "proj:0.0"},
		{Pane: 0, PaneTarget: "proj:1.0"},
		{Pane: 0, PaneTarget: "proj:2.0"},
	}
	stuck := []int{0, 0, 0}

	var got []string
	restart := func(_ context.Context, opts RestartPaneOptions) (*RestartPaneOutput, error) {
		got = append(got, strings.Join(opts.Panes, ","))
		return &RestartPaneOutput{RobotResponse: NewRobotResponse(true)}, nil
	}

	restarted, failed, err := restartAutoRestartStuckPanes(context.Background(),
		AutoRestartStuckOptions{Session: "proj"}, agents, stuck, restart)
	if err != nil {
		t.Fatalf("restartAutoRestartStuckPanes: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("unexpected failures: %v", failed)
	}
	if len(restarted) != 3 {
		t.Fatalf("restarted %d panes, want 3", len(restarted))
	}
	if len(got) != 3 {
		t.Fatalf("issued %d restarts, want 3", len(got))
	}
	seen := map[string]int{}
	for _, target := range got {
		seen[target]++
	}
	if len(seen) != 3 {
		t.Fatalf("restarts hit %d distinct target(s): %v — each window's stuck agent must be restarted once", len(seen), seen)
	}
}

// bd-qz5wk: threshold-exceeding panes whose health forbids a restart become
// typed skips — a restart cannot answer a gate screen or lift a rate limit.
func TestClassifyStuckPanesSkipsBlockedAndRateLimited(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, PaneTarget: "%1", Health: "blocked", IdleSinceSeconds: 900},
		{Pane: 2, PaneTarget: "%2", Health: "rate_limited", IdleSinceSeconds: 900, BackoffRemaining: 120},
		{Pane: 3, PaneTarget: "%3", Health: "unhealthy", IdleSinceSeconds: 900},
		{Pane: 4, PaneTarget: "%4", Health: "blocked", IdleSinceSeconds: 10}, // under threshold: neither list
	}
	stuck, skipped := ClassifyStuckPanes(agents, 5*time.Minute)
	t.Logf("stuck=%v skipped=%+v", stuck, skipped)

	if len(stuck) != 1 || stuck[0] != 3 {
		t.Errorf("stuck = %v, want only pane 3", stuck)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %+v, want 2 entries", skipped)
	}
	byPane := map[int]AutoRestartSkip{}
	for _, sk := range skipped {
		byPane[sk.Pane] = sk
	}
	if sk := byPane[1]; sk.Health != "blocked" ||
		!strings.Contains(sk.Reason, "restart cannot answer it") ||
		!strings.Contains(sk.Reason, "keystroke") {
		t.Errorf("blocked skip = %+v, want gate-refusal reason", sk)
	}
	if sk := byPane[2]; sk.Health != "rate_limited" ||
		!strings.Contains(sk.Reason, "backoff 120s") {
		t.Errorf("rate-limited skip = %+v, want backoff reason", sk)
	}
}

// The skips must survive into the robot envelope on every exit path.
func TestBuildAutoRestartStuckOutputCarriesSkips(t *testing.T) {
	agents := []SessionAgentHealth{
		{Pane: 1, PaneTarget: "%1", Health: "blocked", IdleSinceSeconds: 900},
	}
	stuck, skipped := ClassifyStuckPanes(agents, 5*time.Minute)
	output := BuildAutoRestartStuckOutput("sess", stuck, nil, nil, 5*time.Minute, true)
	output.Skipped = skipped

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"skipped"`) ||
		!strings.Contains(string(data), "restart cannot answer it") {
		t.Errorf("envelope missing typed skip: %s", data)
	}
	t.Logf("envelope: %s", data)
}
