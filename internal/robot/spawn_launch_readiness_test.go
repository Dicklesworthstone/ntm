package robot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSpawnLaunchReadinessOrdersEveryLaunchAndPreservesOptions(t *testing.T) {
	var order []string
	var lastPaneID string
	ports := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return false },
		LaunchAgent: func(_ context.Context, pane tmux.Pane, session, kind string, number int, dir, command string) (SpawnedAgent, error) {
			if session != "gated" || dir != "/project" || command != "original command" || number != pane.Index {
				t.Fatal("launch arguments changed")
			}
			order = append(order, fmt.Sprintf("launch:%d", number))
			lastPaneID = pane.ID
			// A launch receipt is not itself proof of readiness.
			return SpawnedAgent{Title: "original title", Ready: true, StartupMs: 12}, nil
		},
		WaitForReady: func(ctx context.Context, out *SpawnOutput, timeout time.Duration) error {
			if _, ok := ctx.Deadline(); !ok || timeout != time.Minute || len(out.Agents) != 1 || out.Agents[0].Ready ||
				out.Session != "gated" || out.WorkingDir != "/project" || out.Agents[0].Type != "claude" || out.Agents[0].PaneID != lastPaneID {
				t.Fatalf("wrong readiness request: %+v", out)
			}
			order = append(order, "ready:"+out.Agents[0].Pane)
			out.Agents[0].Ready = true
			return nil
		},
	}
	original := SpawnOptions{Session: "gated", CCCount: 3, DryRun: true, Safety: true, LifecycleDeps: ports}
	if got, err := WithSpawnLaunchReadiness(original, 0); err != nil || got.LifecycleDeps != ports || got.WaitReady {
		t.Fatal("disabled gate changed original options")
	}
	if got, err := WithSpawnLaunchReadiness(original, -time.Second); err == nil || got.LifecycleDeps != ports {
		t.Fatal("negative timeout accepted or mutated options")
	}
	opts, err := WithSpawnLaunchReadiness(original, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if opts.LifecycleDeps == ports || !opts.WaitReady || original.WaitReady || !opts.DryRun || !opts.Safety || opts.CCCount != 3 || opts.LifecycleDeps.IsTMUXInstalled() {
		t.Fatal("gate lost options or mutated the caller")
	}
	for i := 1; i <= 3; i++ {
		agent, err := opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{ID: fmt.Sprintf("%%%d", i), WindowIndex: 2, Index: i}, "gated", "claude", i, "/project", "original command")
		if err != nil || !agent.Ready || agent.Title != "original title" || agent.StartupMs != 12 || agent.Error != "" || agent.PaneID != fmt.Sprintf("%%%d", i) {
			t.Fatalf("launch %d lost receipt: %+v, %v", i, agent, err)
		}
	}
	want := []string{"launch:1", "ready:2.1", "launch:2", "ready:2.2", "launch:3", "ready:2.3"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("launches were not gated, including the last: %v", order)
	}
}

func TestSpawnLaunchReadinessFailureStopsLaterSideEffects(t *testing.T) {
	for _, failure := range []string{"launch error", "receipt error", "wrong launch pane", "wrong launch ID", "readiness error", "not ready", "wrong ready pane", "wrong ready ID", "missing ready agent", "late success"} {
		t.Run(failure, func(t *testing.T) {
			cause := errors.New(failure)
			launches, waits := 0, 0
			opts, err := WithSpawnLaunchReadiness(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
				LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
					launches++
					a := SpawnedAgent{Pane: "0.1", Type: "claude", Title: "surviving process"}
					switch failure {
					case "launch error":
						return a, cause
					case "receipt error":
						a.Error = failure
					case "wrong launch pane":
						a.Pane = "0.9"
					case "wrong launch ID":
						a.PaneID = "%99"
					}
					return a, nil
				},
				WaitForReady: func(ctx context.Context, out *SpawnOutput, _ time.Duration) error {
					waits++
					switch failure {
					case "readiness error":
						return cause
					case "not ready":
						return nil
					case "wrong ready pane":
						out.Agents[0].Pane = "0.9"
					case "wrong ready ID":
						out.Agents[0].PaneID = "%99"
					case "missing ready agent":
						out.Agents = nil
						return nil
					case "late success":
						<-ctx.Done()
					}
					out.Agents[0].Ready = true
					return nil
				},
			}}, 10*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			launch := opts.LifecycleDeps.LaunchAgent
			agent, firstErr := launch(context.Background(), tmux.Pane{ID: "%1", Index: 1}, "gated", "claude", 1, "", "")
			if firstErr == nil || agent.Ready || agent.Error == "" || agent.Title != "surviving process" {
				t.Fatalf("failure lost partial output or granted readiness: %+v, %v", agent, firstErr)
			}
			if (failure == "launch error" || failure == "readiness error") && !errors.Is(firstErr, cause) {
				t.Fatalf("lost original cause: %v", firstErr)
			}
			if failure == "late success" && !errors.Is(firstErr, context.DeadlineExceeded) {
				t.Fatalf("late success masked timeout: %v", firstErr)
			}
			priorWaits := waits
			_, secondErr := launch(context.Background(), tmux.Pane{ID: "%2", Index: 2}, "gated", "codex", 1, "", "")
			if secondErr != firstErr || launches != 1 || waits != priorWaits {
				t.Fatalf("failure did not stop the fleet: launches=%d waits=%d err=%v", launches, waits, secondErr)
			}
		})
	}
}

func TestSpawnLaunchReadinessConcurrentWaitAndCancellation(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var launches atomic.Int32
	opts, err := WithSpawnLaunchReadiness(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			launches.Add(1)
			return SpawnedAgent{}, nil
		},
		WaitForReady: func(ctx context.Context, out *SpawnOutput, _ time.Duration) error {
			close(entered)
			select {
			case <-release:
				out.Agents[0].Ready = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{Index: 1}, "gated", "claude", 1, "", "")
		done <- err
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first agent never reached readiness")
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := opts.LifecycleDeps.LaunchAgent(waitCtx, tmux.Pane{Index: 2}, "gated", "codex", 1, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second launch ignored cancellation while the first initialized: %v", err)
	}
	if launches.Load() != 1 {
		t.Fatalf("launched %d agents before first was ready", launches.Load())
	}
	close(release)
}

func TestSpawnLaunchReadinessParentCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launches := 0
	opts, err := WithSpawnLaunchReadiness(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			launches++
			return SpawnedAgent{}, nil
		},
		WaitForReady: func(waitCtx context.Context, out *SpawnOutput, _ time.Duration) error {
			cancel()
			<-waitCtx.Done()
			out.Agents[0].Ready = true
			return nil
		},
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.LifecycleDeps.LaunchAgent(nil, tmux.Pane{}, "", "claude", 1, "", ""); err == nil || launches != 0 {
		t.Fatal("nil context reached launcher")
	}
	agent, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{Index: 1}, "gated", "claude", 1, "", "")
	if !errors.Is(err, context.Canceled) || agent.Ready || launches != 1 {
		t.Fatalf("readiness ignored parent cancellation: %+v %v", agent, err)
	}
	_, err = opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{Index: 2}, "gated", "claude", 2, "", "")
	if !errors.Is(err, context.Canceled) || launches != 1 {
		t.Fatal("new context revived the failed startup")
	}
}

func TestSpawnLaunchReadinessComposesWithProgressAndInterval(t *testing.T) {
	var events []SpawnProgress
	calls := 0
	base := SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			calls++
			return SpawnedAgent{}, nil
		},
		WaitForReady: func(_ context.Context, out *SpawnOutput, _ time.Duration) error {
			if len(events) < 3 || events[len(events)-2].Stage != "launch_agent" || events[len(events)-2].Phase != "finished" {
				t.Fatal("readiness began before the launched process was recorded")
			}
			out.Agents[0].Ready = true
			return nil
		},
	}}
	opts := WithSpawnProgress(base, func(event SpawnProgress) error { events = append(events, event); return nil })
	opts, err := WithSpawnLaunchInterval(opts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	opts, err = WithSpawnLaunchReadiness(opts, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%1", Index: 1}, "gated", "claude", 1, "", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(events) != 4 || events[3].Stage != "wait_ready" || !events[3].Agents[0].Ready {
		t.Fatalf("missing ready checkpoint: %+v", events)
	}
	cancel()
	if _, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%2", Index: 2}, "gated", "claude", 2, "", ""); !errors.Is(err, context.Canceled) || calls != 1 || len(events) != 4 {
		t.Fatalf("cancelled pacing emitted effects/checkpoints: %v", err)
	}
}

func TestSpawnLaunchReadinessFailureDoesNotWaitForAnotherInterval(t *testing.T) {
	cause := errors.New("agent requires operator attention")
	launches := 0
	opts, err := WithSpawnLaunchInterval(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			launches++
			return SpawnedAgent{}, nil
		},
		WaitForReady: func(context.Context, *SpawnOutput, time.Duration) error { return cause },
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	opts, err = WithSpawnLaunchReadiness(opts, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, firstErr := opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{Index: 1}, "gated", "claude", 1, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, secondErr := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{Index: 2}, "gated", "claude", 2, "", "")
	if !errors.Is(firstErr, cause) || secondErr != firstErr || launches != 1 {
		t.Fatalf("failure waited for pacing or lost its cause: first=%v second=%v launches=%d", firstErr, secondErr, launches)
	}
}
