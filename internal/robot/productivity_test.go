package robot

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestEvaluateProductivity(t *testing.T) {
	cases := []struct {
		name     string
		output   ProductivityOutput
		decision ProductivityDecision
	}{
		{
			name:     "incomplete evidence remains unknown",
			output:   ProductivityOutput{EvidenceComplete: false},
			decision: ProductivityUnknown,
		},
		{
			name:     "live build continues work",
			output:   ProductivityOutput{EvidenceComplete: true, Panes: []ProductivityPane{{Builds: []BuildProcess{{PID: 7, Command: "go test ./..."}}}}},
			decision: ProductivityContinue,
		},
		{
			name:     "recent attributed commit continues work",
			output:   ProductivityOutput{EvidenceComplete: true, Panes: []ProductivityPane{{Progress: &SemanticProgress{CommitsInWindow: 1}}}},
			decision: ProductivityContinue,
		},
		{
			name:     "ready work prevents a convergence claim",
			output:   ProductivityOutput{EvidenceComplete: true, ReadyBeadCount: 2},
			decision: ProductivityUnknown,
		},
		{
			name:     "no evidence and no ready work converges",
			output:   ProductivityOutput{EvidenceComplete: true},
			decision: ProductivityConverged,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := evaluateProductivity(&tc.output)
			if got != tc.decision {
				t.Fatalf("decision = %q, want %q (reason %q)", got, tc.decision, reason)
			}
			if reason == "" {
				t.Fatal("decision reason is empty")
			}
		})
	}
}

func TestGetProductivityAttributesBuildsAndProgressToMatchingPane(t *testing.T) {
	now := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	deps := productivityDependencies{
		sessionExists: func(string) bool { return true },
		getPanes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentCodex},
			}, nil
		},
		panePath: func(_ context.Context, paneID string) string {
			if paneID == "%1" {
				return "/workspace/a"
			}
			return "/workspace/b"
		},
		processes: func(context.Context) ([]productivityProcess, error) {
			return []productivityProcess{
				{pid: 11, cwd: "/workspace/a", command: "go test ./..."},
				{pid: 12, cwd: "/workspace/other", command: "cargo test"},
			}, nil
		},
		readyBeads: func(context.Context, string) (int, error) { return 0, nil },
		now:        func() time.Time { return now },
	}

	output, err := getProductivity(ProductivityOptions{Session: "swarm", Window: time.Minute}, deps)
	if err != nil {
		t.Fatalf("getProductivity() error = %v", err)
	}
	if output.Decision != ProductivityContinue {
		t.Fatalf("decision = %q, want continue (%s)", output.Decision, output.DecisionReason)
	}
	if len(output.Panes) != 2 {
		t.Fatalf("panes = %d, want 2", len(output.Panes))
	}
	if got := output.Panes[0].Builds; len(got) != 1 || got[0].PID != 11 {
		t.Fatalf("pane 0.1 builds = %+v, want pid 11 only", got)
	}
	if got := output.Panes[1].Builds; len(got) != 0 {
		t.Fatalf("pane 0.2 builds = %+v, want none", got)
	}
	if got := output.BuildProcesses; len(got) != 1 || got[0].PID != 11 {
		t.Fatalf("build_processes = %+v, want pane-associated pid 11 only", got)
	}
}

func TestGetProductivityDoesNotConvergeWhenEvidenceFails(t *testing.T) {
	deps := productivityDependencies{
		sessionExists: func(string) bool { return true },
		getPanes:      func(string) ([]tmux.Pane, error) { return nil, nil },
		panePath:      func(context.Context, string) string { return "" },
		processes:     func(context.Context) ([]productivityProcess, error) { return nil, context.DeadlineExceeded },
		readyBeads:    func(context.Context, string) (int, error) { return 0, context.DeadlineExceeded },
		now:           time.Now,
	}
	output, err := getProductivity(ProductivityOptions{Session: "swarm"}, deps)
	if err != nil {
		t.Fatalf("getProductivity() error = %v", err)
	}
	if output.Decision != ProductivityUnknown || output.EvidenceComplete {
		t.Fatalf("output = %+v, want unknown with incomplete evidence", output)
	}
}

func TestGetProductivityDoesNotConvergeWithoutRecognizedAgentPanes(t *testing.T) {
	deps := productivityDependencies{
		sessionExists: func(string) bool { return true },
		getPanes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%0", Index: 0, Type: tmux.AgentUser}, {ID: "%1", Index: 1, Type: tmux.AgentUnknown}}, nil
		},
		panePath:   func(context.Context, string) string { return "" },
		processes:  func(context.Context) ([]productivityProcess, error) { return nil, nil },
		readyBeads: func(context.Context, string) (int, error) { return 0, nil },
		now:        time.Now,
	}
	output, err := getProductivity(ProductivityOptions{Session: "swarm"}, deps)
	if err != nil {
		t.Fatalf("getProductivity() error = %v", err)
	}
	if output.Decision != ProductivityUnknown || output.EvidenceComplete {
		t.Fatalf("output = %+v, want unknown with incomplete agent evidence", output)
	}
}

func TestAdvanceConvergenceStateRequiresStableReadyCountAndStreak(t *testing.T) {
	state := ConvergenceState{}
	first := &ProductivityOutput{Decision: ProductivityConverged, ReadyBeadCount: 0}
	state, met := AdvanceConvergenceState(state, first, 2)
	if met || state.ConvergedStreak != 1 || first.ReadyBeadDelta != 0 {
		t.Fatalf("first observation = state %+v met=%v delta=%d, want streak 1 and not met", state, met, first.ReadyBeadDelta)
	}

	second := &ProductivityOutput{Decision: ProductivityConverged, ReadyBeadCount: 0}
	state, met = AdvanceConvergenceState(state, second, 2)
	if !met || state.ConvergedStreak != 2 || second.ReadyBeadDelta != 0 {
		t.Fatalf("second observation = state %+v met=%v delta=%d, want streak 2 and met", state, met, second.ReadyBeadDelta)
	}

	newWork := &ProductivityOutput{Decision: ProductivityConverged, ReadyBeadCount: 1}
	state, met = AdvanceConvergenceState(state, newWork, 2)
	if met || state.ConvergedStreak != 0 || newWork.ReadyBeadDelta != 1 {
		t.Fatalf("ready-work change = state %+v met=%v delta=%d, want reset", state, met, newWork.ReadyBeadDelta)
	}

	unknown := &ProductivityOutput{Decision: ProductivityUnknown, ReadyBeadCount: 1}
	state, met = AdvanceConvergenceState(state, unknown, 2)
	if met || state.ConvergedStreak != 0 {
		t.Fatalf("unknown observation = state %+v met=%v, want reset", state, met)
	}
}

func TestIsBuildOrTestCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"go test ./...", true},
		{"/usr/local/bin/cargo build", true},
		{"rustc main.rs", true},
		{"bun test", true},
		{"git status", false},
		{"", false},
	} {
		if got := isBuildOrTestCommand(tc.command); got != tc.want {
			t.Errorf("isBuildOrTestCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
