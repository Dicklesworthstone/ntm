package robot

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSpawnLaunchIntervalPreservesOptionsAndPorts(t *testing.T) {
	launchErr := errors.New("original launcher failure")
	wantAgent := SpawnedAgent{Pane: "2.3", Type: "claude", Error: launchErr.Error()}
	var calls int
	ports := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return false },
		LaunchAgent: func(ctx context.Context, pane tmux.Pane, session, agentType string, number int, dir, command string) (SpawnedAgent, error) {
			calls++
			if ctx == nil || pane.ID != "%7" || session != "paced" || agentType != "claude" || number != 3 || dir != "/project" || command != "agent-command" {
				t.Fatalf("launch arguments changed: %v %+v %q %q %d %q %q", ctx, pane, session, agentType, number, dir, command)
			}
			return wantAgent, launchErr
		},
	}
	original := SpawnOptions{Session: "paced", CCCount: 3, Safety: true, DryRun: true, LifecycleDeps: ports}
	for _, interval := range []time.Duration{-time.Second, 0} {
		got, err := WithSpawnLaunchInterval(original, interval)
		if (err != nil) != (interval < 0) || got.LifecycleDeps != ports {
			t.Fatalf("interval %v changed options or validation: %+v, %v", interval, got, err)
		}
	}
	paced, err := WithSpawnLaunchInterval(original, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if paced.LifecycleDeps == ports || paced.LifecycleDeps.IsTMUXInstalled() {
		t.Fatal("pacing mutated or discarded the original lifecycle ports")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := paced.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%7"}, "paced", "claude", 3, "/project", "agent-command")
	if !errors.Is(err, launchErr) || !reflect.DeepEqual(got, wantAgent) || calls != 1 {
		t.Fatalf("first launch did not preserve its receipt: %+v, %v, calls=%d", got, err, calls)
	}
	// The caller's launcher remains unpaced, even after the returned copy has
	// consumed its first slot. A deadline bounds this regression if aliased.
	_, err = ports.LaunchAgent(ctx, tmux.Pane{ID: "%7"}, "paced", "claude", 3, "/project", "agent-command")
	if !errors.Is(err, launchErr) || calls != 2 || !paced.Safety || !paced.DryRun || paced.CCCount != 3 {
		t.Fatalf("caller options or launcher changed: %+v, %v, calls=%d", paced, err, calls)
	}
}

func TestSpawnLaunchIntervalSpacesAttemptsIncludingFailures(t *testing.T) {
	const interval = 15 * time.Millisecond
	var starts []time.Time
	opts, err := WithSpawnLaunchInterval(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			starts = append(starts, time.Now())
			return SpawnedAgent{}, errors.New("launch failed")
		},
	}}, interval)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		_, _ = opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{}, "paced", "claude", i+1, "", "")
	}
	if len(starts) != 3 {
		t.Fatalf("got %d launch attempts, want 3", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		// The callback records just after the start boundary, so allow only
		// that tiny instrumentation gap, not an omitted inter-launch wait.
		if gap := starts[i].Sub(starts[i-1]); gap < interval-time.Millisecond {
			t.Fatalf("attempts %d and %d were only %v apart", i, i+1, gap)
		}
	}
}

func TestSpawnLaunchIntervalCancellationStopsFurtherLaunches(t *testing.T) {
	var calls atomic.Int32
	opts, err := WithSpawnLaunchInterval(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			calls.Add(1)
			return SpawnedAgent{Pane: "0.1"}, nil
		},
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	launch := opts.LifecycleDeps.LaunchAgent
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := launch(ctx, tmux.Pane{}, "paced", "claude", 1, "", ""); err != nil {
		t.Fatalf("first launch was delayed: %v", err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, err := launch(waitCtx, tmux.Pane{}, "paced", "claude", 2, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting launch ignored deadline: %v", err)
	}
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := launch(cancelled, tmux.Pane{}, "paced", "claude", 3, "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled launch returned %v", err)
	}
	if _, err := launch(nil, tmux.Pane{}, "paced", "claude", 4, "", ""); err == nil {
		t.Fatal("nil context accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("cancelled requests launched %d agents, want only the first", calls.Load())
	}
}

func TestSpawnLaunchIntervalConcurrentWaitIsCancellable(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	opts, err := WithSpawnLaunchInterval(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			calls.Add(1)
			close(entered)
			<-release
			return SpawnedAgent{}, nil
		},
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{}, "paced", "claude", 1, "", "")
	}()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first launcher did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{}, "paced", "codex", 1, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent waiter ignored cancellation: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent waiter bypassed the launch gate: calls=%d", calls.Load())
	}
}
