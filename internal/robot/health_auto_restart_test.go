package robot

import (
	"testing"
	"time"
)

// =============================================================================
// Tests for ClassifyStuckPanes
// =============================================================================

func TestClassifyStuckPanes(t *testing.T) {
	t.Parallel()

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
			wantPanes: []int{2, 3, 5},
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
			t.Parallel()
			got := ClassifyStuckPanes(tt.agents, tt.threshold)
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
	t.Parallel()

	t.Run("basic output with stuck and restarted panes", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
		out := BuildAutoRestartStuckOutput("s", nil, nil, nil, 5*time.Minute, false)

		if out.StuckPanes == nil {
			t.Error("StuckPanes should be non-nil empty slice")
		}
		if len(out.StuckPanes) != 0 {
			t.Errorf("StuckPanes length = %d, want 0", len(out.StuckPanes))
		}
	})

	t.Run("nil restarted becomes empty slice", func(t *testing.T) {
		t.Parallel()
		out := BuildAutoRestartStuckOutput("s", []int{1}, nil, nil, 5*time.Minute, false)

		if out.Restarted == nil {
			t.Error("Restarted should be non-nil empty slice")
		}
		if len(out.Restarted) != 0 {
			t.Errorf("Restarted length = %d, want 0", len(out.Restarted))
		}
	})

	t.Run("with failed panes", func(t *testing.T) {
		t.Parallel()
		out := BuildAutoRestartStuckOutput("s", []int{1, 2, 3}, []int{1}, []int{2, 3}, 5*time.Minute, false)

		if !intSlicesEqual(out.Failed, []int{2, 3}) {
			t.Errorf("Failed = %v, want [2, 3]", out.Failed)
		}
		if !intSlicesEqual(out.Restarted, []int{1}) {
			t.Errorf("Restarted = %v, want [1]", out.Restarted)
		}
	})

	t.Run("checked_at is populated", func(t *testing.T) {
		t.Parallel()
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
		t.Parallel()
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

// =============================================================================
// Tests for ParseStuckThreshold
// =============================================================================

func TestParseStuckThreshold(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
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
	t.Parallel()
	if DefaultStuckThreshold != 5*time.Minute {
		t.Errorf("DefaultStuckThreshold = %v, want 5m", DefaultStuckThreshold)
	}
}

// =============================================================================
// Tests for ClassifyStuckPanes edge cases
// =============================================================================

func TestClassifyStuckPanes_AllHealthStates(t *testing.T) {
	t.Parallel()

	threshold := 5 * time.Minute
	thresholdSec := int(threshold.Seconds())

	healthStates := []string{"healthy", "degraded", "unhealthy", "rate_limited"}

	for _, health := range healthStates {
		t.Run("above_threshold_"+health, func(t *testing.T) {
			t.Parallel()
			agents := []SessionAgentHealth{
				{Pane: 1, Health: health, IdleSinceSeconds: thresholdSec + 1},
			}
			got := ClassifyStuckPanes(agents, threshold)
			if len(got) != 1 || got[0] != 1 {
				t.Errorf("health=%q above threshold: got %v, want [1]", health, got)
			}
		})

		t.Run("below_threshold_"+health, func(t *testing.T) {
			t.Parallel()
			agents := []SessionAgentHealth{
				{Pane: 1, Health: health, IdleSinceSeconds: thresholdSec - 1},
			}
			got := ClassifyStuckPanes(agents, threshold)
			if len(got) != 0 {
				t.Errorf("health=%q below threshold: got %v, want []", health, got)
			}
		})
	}
}

func TestClassifyStuckPanes_PreservesPaneOrder(t *testing.T) {
	t.Parallel()

	agents := []SessionAgentHealth{
		{Pane: 5, Health: "healthy", IdleSinceSeconds: 600},
		{Pane: 2, Health: "healthy", IdleSinceSeconds: 600},
		{Pane: 8, Health: "healthy", IdleSinceSeconds: 600},
	}
	got := ClassifyStuckPanes(agents, 5*time.Minute)
	want := []int{5, 2, 8}
	if !intSlicesEqual(got, want) {
		t.Errorf("pane order not preserved: got %v, want %v", got, want)
	}
}

func TestClassifyStuckPanes_LargePaneCount(t *testing.T) {
	t.Parallel()

	agents := make([]SessionAgentHealth, 20)
	for i := range agents {
		agents[i] = SessionAgentHealth{
			Pane:             i,
			Health:           "healthy",
			IdleSinceSeconds: 600,
		}
	}
	got := ClassifyStuckPanes(agents, 5*time.Minute)
	if len(got) != 20 {
		t.Errorf("expected 20 stuck panes, got %d", len(got))
	}
}

// =============================================================================
// Tests for AutoRestartStuckOutput JSON structure
// =============================================================================

func TestAutoRestartStuckOutput_JSONFields(t *testing.T) {
	t.Parallel()

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

func TestShouldAutoRestartHealthState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       HealthState
		wantRestart bool
		wantReason  string
	}{
		{
			name:        "unhealthy restarts",
			state:       HealthUnhealthy,
			wantRestart: true,
		},
		{
			name:        "rate limited waits",
			state:       HealthRateLimited,
			wantRestart: false,
			wantReason:  "agent is rate_limited, waiting for cooldown",
		},
		{
			name:        "healthy does not restart",
			state:       HealthHealthy,
			wantRestart: false,
			wantReason:  "agent is healthy, no restart needed",
		},
		{
			name:        "degraded does not restart",
			state:       HealthDegraded,
			wantRestart: false,
			wantReason:  "agent is degraded, no restart needed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotRestart, gotReason := shouldAutoRestartHealthState(tt.state)
			if gotRestart != tt.wantRestart {
				t.Fatalf("shouldAutoRestartHealthState(%q) restart = %v, want %v", tt.state, gotRestart, tt.wantRestart)
			}
			if gotReason != tt.wantReason {
				t.Fatalf("shouldAutoRestartHealthState(%q) reason = %q, want %q", tt.state, gotReason, tt.wantReason)
			}
		})
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
