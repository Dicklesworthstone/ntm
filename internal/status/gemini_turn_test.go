package status

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the shared parser and the actual session-observation surface used
// by robot status/waits and dispatch. A fresh WINDOW activity timestamp must
// not keep an idle Gemini pane busy because a sibling is still responding.
func TestGeminiTurnSharedObservation(t *testing.T) {
	const prompt = "> Type your message or @path/to/file"
	const footer = "workspace (/directory) sandbox /model quota\n~/project no sandbox gemini-pro 15% used"
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		output string
		state  AgentState
	}{
		{"legacy", "Task completed.\ngemini>", StateIdle},
		{"current", prompt + "\n" + footer, StateIdle},
		{"ansi", "\x1b[32m" + prompt + "\x1b[0m\n" + footer, StateIdle},
		{"yolo", "* Type your message or @path/to/file\n" + footer, StateIdle},
		{"cancel hint", "Custom reasoning label (esc to cancel, 90s)\n" + prompt + "\n" + footer, StateWorking},
		{"spinner", "⠹ Examining implementation\n" + prompt + "\n" + footer, StateWorking},
		{"exhausted", "RESOURCE_EXHAUSTED\n" + prompt + "\n" + footer, StateError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := agent.NewParser().ParseWithHint(tc.output, agent.AgentTypeGemini)
			if err != nil {
				t.Fatal(err)
			}
			wantIdle := tc.state == StateIdle
			wantWorking := tc.state == StateWorking
			wantLimited := tc.state == StateError
			if parsed.IsIdle != wantIdle || parsed.IsWorking != wantWorking || parsed.IsRateLimited != wantLimited {
				t.Fatalf("parser disagrees with %s: %+v", tc.state, parsed)
			}
			if got := DetectIdleFromOutput(tc.output, "gemini"); got != wantIdle {
				t.Fatalf("idle=%v, want %v", got, wantIdle)
			}
			// Also protect a quiet, long-running response: a stale timestamp
			// must not let the input placeholder win over the live spinner.
			for _, activity := range []time.Time{now, now.Add(-time.Hour)} {
				got := NewDetector().AnalyzeAt("%7", "demo__gmi_1", "gmi", tc.output, activity, now)
				if got.State != tc.state {
					t.Fatalf("activity=%s: state=%s, want %s", activity, got.State, tc.state)
				}
			}
			observer := NewSessionObserverWithDependencies(NewDetector(), SessionObserverConfig{}, SessionObserverDependencies{
				Now: func() time.Time { return now },
				ListPanes: func(context.Context, string) ([]tmux.PaneActivity, error) {
					return []tmux.PaneActivity{{
						Pane:         tmux.Pane{ID: "%7", Title: "demo__gmi_1", Type: tmux.AgentGemini, Height: 40},
						LastActivity: now,
					}}, nil
				},
				CapturePane: func(context.Context, string, int) (string, error) { return tc.output, nil },
			})
			observed, err := observer.Observe(context.Background(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if !observed.Complete || len(observed.Panes) != 1 {
				t.Fatalf("incomplete observation: %+v", observed)
			}
			current := observed.Panes[0].Current
			if current.Status.State != tc.state || current.Freshness != FreshnessFresh || current.Confidence < 0.9 {
				t.Fatalf("unexpected canonical observation: %+v", current)
			}
		})
	}
}

func TestGeminiContextMetricsReachStatus(t *testing.T) {
	for _, used := range []float64{0, 14, 93.25, 100} {
		output := fmt.Sprintf("> Type your message or @path/to/file\nworkspace (/directory) sandbox /model context\n~/project no sandbox gemini-pro %g%% used", used)
		parsed, err := agent.NewParser().ParseWithHint(output, agent.AgentTypeGemini)
		if err != nil {
			t.Fatal(err)
		}
		remaining := 100 - used
		if parsed.ContextRemaining == nil || *parsed.ContextRemaining != remaining {
			t.Fatalf("used=%v: remaining=%v, want %v", used, parsed.ContextRemaining, remaining)
		}
		wantLow := remaining < agent.DefaultParserConfig().ContextLowThreshold
		if parsed.IsContextLow != wantLow {
			t.Fatalf("used=%v: low=%v, want %v", used, parsed.IsContextLow, wantLow)
		}
		now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
		got := NewDetector().AnalyzeAt("%7", "demo__gmi_1", "gmi", output, now, now)
		if got.ContextUsage != used || got.State != StateIdle {
			t.Fatalf("used=%v: canonical status=%+v", used, got)
		}
	}

	// Quota cannot supply a ContextRemaining pointer: rotating the agent
	// does not replenish provider quota and would discard useful context.
	quota := "> Type your message or @path/to/file\n/model quota\ngemini-pro 99% used"
	parsed, err := agent.NewParser().ParseWithHint(quota, agent.AgentTypeGemini)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ContextRemaining != nil || parsed.IsContextLow || parsed.IsRateLimited {
		t.Fatalf("quota footer was treated as context exhaustion: %+v", parsed)
	}
}
