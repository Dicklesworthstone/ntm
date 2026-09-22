package robot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/resilience"
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

func TestSpawnProgressRecordsLifecycleBoundaries(t *testing.T) {
	var events []SpawnProgress
	var effects []string
	checkIntent := func(stage string) {
		t.Helper()
		if len(events) == 0 || events[len(events)-1].Stage != stage || events[len(events)-1].Phase != "started" {
			t.Fatalf("%s ran without its intent: %+v", stage, events)
		}
		effects = append(effects, stage)
	}
	ports := &SpawnLifecycleDependencies{
		CreateSession: func(_ context.Context, session, dir string, history int) error {
			checkIntent("create_session")
			if session != "progress" || dir != "/project" || history != 123 {
				t.Fatal("session arguments changed")
			}
			return nil
		},
		SplitWindow: func(context.Context, string, string) (string, error) {
			checkIntent("split_window")
			return "%7", nil
		},
		ApplyTiledLayout: func(context.Context, string) error { checkIntent("layout"); return nil },
		LaunchAgent: func(_ context.Context, pane tmux.Pane, session, kind string, number int, dir, command string) (SpawnedAgent, error) {
			checkIntent("launch_agent")
			if pane.ID != "%7" || session != "progress" || kind != "claude" || number != 2 || dir != "/project" || command != "PRIVATE-LAUNCH-COMMAND" {
				t.Fatal("launch arguments changed")
			}
			return SpawnedAgent{Title: "original"}, nil
		},
		WaitForReady: func(_ context.Context, output *SpawnOutput, _ time.Duration) error {
			checkIntent("wait_ready")
			output.Agents[0].Ready = true
			return nil
		},
		StartSessionMonitor: func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
			checkIntent("start_monitor")
			return &resilience.SpawnMonitorResult{MonitorPID: 42}, nil
		},
	}
	original := SpawnOptions{Session: "progress", CCCount: 2, LifecycleDeps: ports}
	if WithSpawnProgress(original, nil).LifecycleDeps != ports {
		t.Fatal("nil observer changed lifecycle ports")
	}
	opts := WithSpawnProgress(original, func(event SpawnProgress) error { events = append(events, event); return nil })
	if opts.LifecycleDeps == ports || opts.Session != original.Session || opts.CCCount != original.CCCount {
		t.Fatal("observer mutated original ports or controls")
	}
	ctx := context.Background()
	deps := opts.LifecycleDeps
	if err := deps.CreateSession(ctx, "progress", "/project", 123); err != nil {
		t.Fatal(err)
	}
	if id, err := deps.SplitWindow(ctx, "progress", "/project"); err != nil || id != "%7" {
		t.Fatalf("split receipt: %q %v", id, err)
	}
	if err := deps.ApplyTiledLayout(ctx, "progress"); err != nil {
		t.Fatal(err)
	}
	agent, err := deps.LaunchAgent(ctx, tmux.Pane{ID: "%7", WindowIndex: 2, Index: 3}, "progress", "claude", 2, "/project", "PRIVATE-LAUNCH-COMMAND")
	if err != nil || agent.Title != "original" || agent.Pane != "" {
		t.Fatalf("observer changed the original launch receipt: %+v %v", agent, err)
	}
	launchEvent := events[len(events)-1]
	if launchEvent.PaneID != "%7" || launchEvent.Agent.Pane != "2.3" || launchEvent.Agent.Type != "claude" {
		t.Fatalf("observation lost durable or physical identity: %+v", launchEvent)
	}
	launchEvent.Agent.Title = "observer mutation"
	if agent.Title != "original" {
		t.Fatal("observer aliases launch receipt")
	}
	output := &SpawnOutput{Session: "progress", WorkingDir: "/project", Agents: []SpawnedAgent{{Pane: "2.3"}}}
	if err := deps.WaitForReady(ctx, output, time.Second); err != nil || !output.Agents[0].Ready {
		t.Fatalf("readiness result lost: %+v %v", output, err)
	}
	events[len(events)-1].Agents[0].Ready = false
	if !output.Agents[0].Ready {
		t.Fatal("readiness observation aliases spawn output")
	}
	result, err := deps.StartSessionMonitor(ctx, resilience.SpawnMonitorRequest{Session: "progress", ProjectDir: "/project"})
	if err != nil || result.MonitorPID != 42 || events[len(events)-1].MonitorPID != 42 {
		t.Fatalf("monitor receipt lost: %+v %v", result, err)
	}
	if len(effects) != 6 || len(events) != 12 {
		t.Fatalf("unbalanced lifecycle observations: effects=%v events=%+v", effects, events)
	}
	for i := 0; i < len(events); i += 2 {
		if events[i].Phase != "started" || events[i+1].Phase != "finished" || events[i].Stage != events[i+1].Stage {
			t.Fatalf("invalid boundary order at %d: %+v", i, events)
		}
	}
	data, err := json.Marshal(events)
	if err != nil || strings.Contains(string(data), "PRIVATE-LAUNCH-COMMAND") {
		t.Fatalf("observations contain executable parameters: %s %v", data, err)
	}
}

func TestSpawnProgressCheckpointFailureStopsLaterEffects(t *testing.T) {
	for _, phase := range []string{"started", "finished"} {
		t.Run(phase, func(t *testing.T) {
			checkpointErr, launchErr := errors.New("disk full"), errors.New("launch partially failed")
			calls := 0
			opts := WithSpawnProgress(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
				LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
					calls++
					return SpawnedAgent{Pane: "0.1"}, launchErr
				},
				ApplyTiledLayout: func(context.Context, string) error { calls++; return nil },
			}}, func(event SpawnProgress) error {
				if event.Phase == phase {
					return checkpointErr
				}
				return nil
			})
			agent, err := opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{ID: "%7"}, "progress", "claude", 1, "", "")
			if !errors.Is(err, checkpointErr) {
				t.Fatalf("checkpoint cause lost: %v", err)
			}
			if phase == "started" && calls != 0 {
				t.Fatal("unrecorded launch ran")
			}
			if phase == "finished" && (calls != 1 || agent.Pane != "0.1" || !errors.Is(err, launchErr)) {
				t.Fatalf("partial receipt or original error lost: %+v %v", agent, err)
			}
			before := calls
			if err := opts.LifecycleDeps.ApplyTiledLayout(context.Background(), "progress"); !errors.Is(err, checkpointErr) || calls != before {
				t.Fatalf("later effect bypassed failed observer: %v calls=%d", err, calls)
			}
		})
	}
}

func TestSpawnProgressCancellationPreservesFinishedEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []SpawnProgress
	opts := WithSpawnProgress(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			cancel()
			return SpawnedAgent{Pane: "0.1"}, context.Canceled
		},
	}}, func(event SpawnProgress) error { events = append(events, event); return nil })
	agent, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%9"}, "progress", "claude", 1, "", "")
	if !errors.Is(err, context.Canceled) || agent.Pane != "0.1" || len(events) != 2 || events[1].Phase != "finished" || events[1].Agent.Pane != "0.1" || events[1].Error == "" {
		t.Fatalf("cancellation swallowed late effects: %+v %+v %v", agent, events, err)
	}
	_, _ = opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{}, "progress", "claude", 2, "", "")
	if len(events) != 2 {
		t.Fatal("cancelled next launch emitted new intent")
	}
}

func TestSpawnProgressDoesNotAnnounceLaunchDuringPacingWait(t *testing.T) {
	var events []SpawnProgress
	opts := WithSpawnProgress(SpawnOptions{LifecycleDeps: &SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
			return SpawnedAgent{}, nil
		},
	}}, func(event SpawnProgress) error { events = append(events, event); return nil })
	opts, err := WithSpawnLaunchInterval(opts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.LifecycleDeps.LaunchAgent(context.Background(), tmux.Pane{}, "progress", "claude", 1, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{}, "progress", "claude", 2, "", ""); !errors.Is(err, context.DeadlineExceeded) || len(events) != 2 {
		t.Fatalf("pacing wait generated false launch evidence: %+v %v", events, err)
	}
}
