package checkpoint

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func readyRestoreObservation(now time.Time) status.PaneObservation {
	pane := tmux.Pane{ID: "%7", Type: tmux.AgentClaude, Command: "claude", Width: 80, Height: 24}
	return status.PaneObservation{
		Pane: pane.Ref(), Metadata: pane, AgentType: string(tmux.AgentClaude), RawOutput: "Recovered agent\n❯ \n",
		Current: status.StateObservation{
			Status:    status.AgentStatus{PaneID: pane.ID, AgentType: string(tmux.AgentClaude), State: status.StateIdle},
			Freshness: status.FreshnessFresh, Confidence: 0.95, ObservedAt: now,
		},
	}
}

func TestRestoreContextReadinessRejectsUnsafeEvidence(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		change func(*status.PaneObservation)
		fatal  bool
	}{
		{"retagged", func(p *status.PaneObservation) { p.Metadata.Type = tmux.AgentCodex }, true},
		{"different physical pane", func(p *status.PaneObservation) { p.Metadata.ID = "%8" }, true},
		{"dead", func(p *status.PaneObservation) { p.Metadata.Dead = true }, true},
		{"service", func(p *status.PaneObservation) { p.Metadata.Service = "server" }, true},
		{"shell", func(p *status.PaneObservation) { p.Metadata.Command = "bash" }, true},
		{"no command", func(p *status.PaneObservation) { p.Metadata.Command = "" }, true},
		{"starting", func(p *status.PaneObservation) { p.Metadata.Command = "tmux" }, true},
		{"capture failure", func(p *status.PaneObservation) { p.Current.Error = "capture failed" }, true},
		{"unavailable", func(p *status.PaneObservation) { p.Current.Freshness = status.FreshnessUnavailable }, false},
		{"stale flag", func(p *status.PaneObservation) { p.Current.Freshness = status.FreshnessStale }, false},
		{"old timestamp", func(p *status.PaneObservation) {
			p.Current.ObservedAt = now.Add(-status.DispatchObservationMaxAge - time.Second)
		}, false},
		{"no timestamp", func(p *status.PaneObservation) { p.Current.ObservedAt = time.Time{} }, false},
		{"future timestamp", func(p *status.PaneObservation) { p.Current.ObservedAt = now.Add(time.Second) }, false},
		{"heuristic idle", func(p *status.PaneObservation) { p.Current.Confidence = 0.5 }, false},
		{"invalid confidence", func(p *status.PaneObservation) { p.Current.Confidence = math.NaN() }, false},
		{"working", func(p *status.PaneObservation) { p.Current.Status.State = status.StateWorking }, false},
		{"error", func(p *status.PaneObservation) { p.Current.Status.State = status.StateError }, false},
		{"unknown", func(p *status.PaneObservation) { p.Current.Status.State = status.StateUnknown }, false},
		{"empty screen", func(p *status.PaneObservation) { p.RawOutput = "\n  \n" }, false},
		{"missing composer", func(p *status.PaneObservation) { p.RawOutput = "Starting up..." }, false},
		{"draft", func(p *status.PaneObservation) { p.RawOutput = "❯ an existing draft\n" }, false},
		{"wrapped draft", func(p *status.PaneObservation) {
			p.RawOutput = "│ ❯    │\n│ continued draft │\n╰──────╯\n"
		}, false},
		{"queued", func(p *status.PaneObservation) { p.RawOutput = "❯ \nPress up to edit queued messages\n" }, false},
		{"trust", func(p *status.PaneObservation) {
			p.RawOutput = "Do you trust the contents of this project?\n❯ Yes, I trust this folder\n  No, exit\nEnter to confirm · Esc to cancel\n"
		}, false},
		{"numbered modal", func(p *status.PaneObservation) { p.RawOutput = "Usage limit reached\n❯ 1. Continue\n  2. Cancel\n" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := readyRestoreObservation(now)
			last := pane.Current
			pane.LastKnown = &last // Historical idle evidence must never rescue Current.
			tc.change(&pane)
			ready, reason, err := restoreContextReadiness(pane, "%7", tmux.AgentClaude, now)
			if ready || reason == "" || (err != nil) != tc.fatal {
				t.Fatalf("unsafe evidence accepted/misclassified: ready=%v reason=%q err=%v", ready, reason, err)
			}
		})
	}
}

func TestRestoreContextReadinessRecognizesEmptyComposers(t *testing.T) {
	for _, tc := range []struct {
		kind tmux.AgentType
		text string
	}{
		{tmux.AgentClaude, "❯ \n"},
		{tmux.AgentClaude, "❯ Try \"explain this project\"\n"},
		{tmux.AgentCodex, "› Ask Codex to do anything\n"},
		{tmux.AgentCodex, "» Ask Codex to do anything\n"},
		{tmux.AgentGrok, "│ ❯     │\n╰───────╯\n"},
	} {
		pane := readyRestoreObservation(time.Now())
		pane.Metadata.Type, pane.RawOutput = tc.kind, tc.text
		ready, reason, err := restoreContextReadiness(pane, "%7", tc.kind, time.Now())
		if !ready || err != nil || reason != "" {
			t.Fatalf("idle %s refused: %q, %v", tc.kind, reason, err)
		}
	}
}

func TestRestoreReadyWaitRequiresConsecutiveNewObservations(t *testing.T) {
	for _, scenario := range []string{"startup", "ready-busy-ready", "same-timestamp", "command-change", "draft-cleared"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			base := time.Now()
			observe := func(context.Context, string) (status.SessionObservation, error) {
				calls++
				p := readyRestoreObservation(base.Add(time.Duration(calls) * time.Millisecond))
				switch scenario {
				case "startup":
					if calls == 1 {
						p.RawOutput = ""
					}
				case "ready-busy-ready":
					if calls == 2 {
						p.Current.Status.State = status.StateWorking
					}
				case "same-timestamp":
					if calls == 2 {
						p.Current.ObservedAt = base.Add(time.Millisecond)
					}
				case "command-change":
					if calls > 1 {
						p.Metadata.Command = "node"
					}
				case "draft-cleared":
					if calls < 3 {
						p.RawOutput = "❯ operator draft\n"
					}
				}
				return status.SessionObservation{Session: "restored", Panes: []status.PaneObservation{p}}, nil
			}
			err := waitForRestoreReadyObservation(context.Background(), "restored", "%7", tmux.AgentClaude,
				time.Second, time.Millisecond, observe, func() time.Time { return base.Add(time.Second) })
			want := 3
			if scenario == "ready-busy-ready" || scenario == "draft-cleared" {
				want = 4
			}
			if err != nil || calls != want {
				t.Fatalf("readiness streak: calls=%d want=%d err=%v", calls, want, err)
			}
		})
	}
}

func TestRestoreReadyWaitPreservesCancellationAndDoesNotObserveAgain(t *testing.T) {
	cause := errors.New("restore cancelled by operator")
	ctx, cancel := context.WithCancelCause(context.Background())
	calls := 0
	observe := func(ctx context.Context, session string) (status.SessionObservation, error) {
		calls++
		cancel(cause)
		return status.SessionObservation{Session: session, Panes: []status.PaneObservation{readyRestoreObservation(time.Now())}}, nil
	}
	err := waitForRestoreReadyObservation(ctx, "restored", "%7", tmux.AgentClaude, time.Hour, time.Hour, observe, time.Now)
	if !errors.Is(err, cause) || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRestoreContextNotReady) || calls != 1 {
		t.Fatalf("cancellation lost or readiness continued: calls=%d err=%v", calls, err)
	}
}

func TestRestoreReadyWaitBoundsNeverReadyPane(t *testing.T) {
	observe := func(ctx context.Context, session string) (status.SessionObservation, error) {
		p := readyRestoreObservation(time.Now())
		p.RawOutput = "❯ private unsent draft\n"
		return status.SessionObservation{Session: session, Panes: []status.PaneObservation{p}}, nil
	}
	err := waitForRestoreReadyObservation(context.Background(), "restored", "%7", tmux.AgentClaude,
		10*time.Millisecond, time.Millisecond, observe, time.Now)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrRestoreContextNotReady) || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("unready pane did not time out with reason: %v", err)
	}
	if strings.Contains(err.Error(), "private unsent") {
		t.Fatalf("diagnostic leaked draft: %v", err)
	}
}

func TestRestoreReadyWaitRejectsMissingAmbiguousOrChangedSession(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "session", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			cause := errors.New("tmux unavailable")
			observe := func(context.Context, string) (status.SessionObservation, error) {
				calls++
				p := readyRestoreObservation(time.Now())
				out := status.SessionObservation{Session: "restored", Panes: []status.PaneObservation{p}}
				switch scenario {
				case "missing":
					out.Panes = nil
				case "duplicate":
					out.Panes = append(out.Panes, p)
				case "session":
					out.Session = "different"
				case "read-error":
					return out, cause
				}
				return out, nil
			}
			err := waitForRestoreReadyObservation(context.Background(), "restored", "%7", tmux.AgentClaude, time.Second, time.Millisecond, observe, time.Now)
			if !errors.Is(err, ErrRestoreContextNotReady) || calls != 1 || (scenario == "read-error" && !errors.Is(err, cause)) {
				t.Fatalf("unsafe observation retried/accepted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestRestoreReadyWaitDeadlineReachesInFlightCapture(t *testing.T) {
	observe := func(ctx context.Context, session string) (status.SessionObservation, error) {
		<-ctx.Done()
		return status.SessionObservation{}, ctx.Err()
	}
	err := waitForRestoreReadyObservation(context.Background(), "restored", "%7", tmux.AgentClaude,
		5*time.Millisecond, time.Millisecond, observe, time.Now)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture outlived readiness deadline: %v", err)
	}
}
