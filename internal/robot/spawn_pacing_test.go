package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
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

// staggerTestClock is a fake prompt-stagger clock: Wait advances Now instead
// of sleeping and records every requested wait.
type staggerTestClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

var staggerTestStart = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func newStaggerTestClock() *staggerTestClock {
	return &staggerTestClock{now: staggerTestStart}
}

func (c *staggerTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *staggerTestClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, delay)
	c.now = c.now.Add(delay)
	return nil
}

func (c *staggerTestClock) recordedWaits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// staggerDelivery is one prompt that reached the fake tmux deliverer.
type staggerDelivery struct {
	PaneID  string
	Message string
	At      time.Time
}

// staggerSpawnWorld is a hermetic --robot-spawn fixture: fake tmux topology,
// readiness, observation, and a recording deliverer behind the real GetSpawn,
// stagger planner, and canonical dispatch port.
type staggerSpawnWorld struct {
	session    string
	clock      *staggerTestClock
	mu         sync.Mutex
	events     []string
	deliveries []staggerDelivery
	failPane   string
	lifecycle  *SpawnLifecycleDependencies
	assignment *SpawnAssignmentDependencies
}

func newStaggerSpawnWorld(t *testing.T, session string, agentTypes ...tmux.AgentType) *staggerSpawnWorld {
	t.Helper()
	world := &staggerSpawnWorld{session: session, clock: newStaggerTestClock()}
	panes := []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 0, Title: session + "__user", Type: tmux.AgentUser}}
	perType := map[tmux.AgentType]int{}
	for i, agentType := range agentTypes {
		perType[agentType]++
		panes = append(panes, tmux.Pane{
			ID: fmt.Sprintf("%%%d", i+2), WindowIndex: 0, Index: i + 1, Type: agentType,
			Title: fmt.Sprintf("%s__%s_%d", session, agentType, perType[agentType]),
		})
	}
	world.lifecycle = testSpawnLifecycleDependencies(panes)
	world.lifecycle.WaitForReady = func(context.Context, *SpawnOutput, time.Duration) error {
		world.record("ready")
		return nil
	}
	world.lifecycle.Now = world.clock.Now
	world.lifecycle.Wait = world.clock.Wait
	world.assignment = &SpawnAssignmentDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return append([]tmux.Pane(nil), panes...), nil
		},
		ObserveSession: bulkSafeObserver(panes),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			world.record("deliver:" + delivery.Target.Ref.ID)
			if delivery.Target.Ref.ID == world.failPane {
				return errors.New("tmux refused the keystrokes")
			}
			world.mu.Lock()
			world.deliveries = append(world.deliveries, staggerDelivery{
				PaneID: delivery.Target.Ref.ID, Message: delivery.Message, At: world.clock.Now(),
			})
			world.mu.Unlock()
			return nil
		}),
	}
	return world
}

func (w *staggerSpawnWorld) record(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *staggerSpawnWorld) options(t *testing.T) SpawnOptions {
	t.Helper()
	return SpawnOptions{
		Session: w.session, WorkingDir: t.TempDir(),
		LifecycleDeps: w.lifecycle, AssignmentDeps: w.assignment,
	}
}

func staggerAt(offset time.Duration) string {
	return staggerTestStart.Add(offset).UTC().Format(time.RFC3339Nano)
}

func TestResolveSpawnStaggerModes(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker("")
	tracker.RecordRateLimit("anthropic", "spawn")
	learned := tracker.GetOptimalDelay("anthropic")
	claude := []string{"claude", "claude"}

	for _, tc := range []struct {
		name         string
		req          SpawnStaggerRequest
		tracker      *ratelimit.RateLimitTracker
		wantMode     string
		wantInterval time.Duration
		wantProvider string
	}{
		{name: "empty mode is none", req: SpawnStaggerRequest{Delay: time.Minute}, wantMode: "none"},
		{name: "explicit none ignores the fixed delay", req: SpawnStaggerRequest{Mode: "none", Delay: time.Minute}, wantMode: "none"},
		{name: "fixed uses the delay", req: SpawnStaggerRequest{Mode: "fixed", Delay: 20 * time.Second}, wantMode: "fixed", wantInterval: 20 * time.Second},
		{name: "fixed zero delay is unpaced", req: SpawnStaggerRequest{Mode: "fixed"}, wantMode: "fixed"},
		{name: "legacy interval applies under none", req: SpawnStaggerRequest{Mode: "none", Legacy: 90 * time.Second}, wantMode: "legacy", wantInterval: 90 * time.Second},
		{name: "explicit fixed wins over legacy", req: SpawnStaggerRequest{Mode: "fixed", Delay: 5 * time.Second, Legacy: 90 * time.Second}, wantMode: "fixed", wantInterval: 5 * time.Second},
		{name: "smart reads the learned delay", req: SpawnStaggerRequest{Mode: "smart", Legacy: 90 * time.Second}, tracker: tracker, wantMode: "smart", wantInterval: learned, wantProvider: "anthropic"},
		{name: "smart without history uses the built-in delay", req: SpawnStaggerRequest{Mode: "smart"}, wantMode: "smart", wantInterval: ratelimit.DefaultDelayAnthropic, wantProvider: "anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveSpawnStagger(tc.req, claude, tc.tracker)
			if got.Mode != tc.wantMode || got.Interval != tc.wantInterval || got.Provider != tc.wantProvider {
				t.Fatalf("ResolveSpawnStagger = %+v, want mode %q interval %v provider %q", got, tc.wantMode, tc.wantInterval, tc.wantProvider)
			}
			if got.Active() != (tc.wantInterval > 0) {
				t.Fatalf("Active() = %t for %+v", got.Active(), got)
			}
			for order, want := range []time.Duration{0, tc.wantInterval, 2 * tc.wantInterval, 3 * tc.wantInterval} {
				if delay := got.Delay(order); delay != want {
					t.Fatalf("Delay(%d) = %v, want %v", order, delay, want)
				}
			}
		})
	}
}

// TestResolveSpawnStaggerSmartProviderPriority pins smart mode's provider
// choice: the strictest provider present wins, and an omp-only batch uses
// omp's own bucket rather than Anthropic's learned backoff.
func TestResolveSpawnStaggerSmartProviderPriority(t *testing.T) {
	tracker := ratelimit.NewRateLimitTracker("")
	for i := 0; i < 3; i++ {
		tracker.RecordRateLimit("anthropic", "spawn")
	}
	if tracker.GetOptimalDelay("anthropic") == tracker.GetOptimalDelay("omp") {
		t.Fatal("control: the anthropic backoff must differ from the omp bucket")
	}
	for _, tc := range []struct {
		types []string
		want  string
	}{
		{types: []string{"omp", "omp"}, want: "omp"},
		{types: []string{"cc", "omp"}, want: "anthropic"},
		{types: []string{"codex", "gemini"}, want: "openai"},
		{types: []string{"gemini", "antigravity", "omp"}, want: "google"},
		{types: []string{"agy"}, want: "google"},
		{types: []string{"opencode"}, want: "anthropic"},
	} {
		got := ResolveSpawnStagger(SpawnStaggerRequest{Mode: "smart"}, tc.types, tracker)
		if got.Provider != tc.want || got.Interval != tracker.GetOptimalDelay(tc.want) {
			t.Fatalf("types %v: smart stagger = %+v, want provider %q at %v", tc.types, got, tc.want, tracker.GetOptimalDelay(tc.want))
		}
	}
}

// TestGetSpawnDeliversInitialPromptInOrderWithFixedStagger drives
// --robot-spawn --spawn-prompt --spawn-stagger-mode=fixed through GetSpawn:
// the prompt waits for the readiness gate, reaches each agent in launch order
// through the canonical dispatch port, and consecutive deliveries are exactly
// one stagger interval apart on the injected clock.
func TestGetSpawnDeliversInitialPromptInOrderWithFixedStagger(t *testing.T) {
	world := newStaggerSpawnWorld(t, "stagger-prompts", tmux.AgentClaude, tmux.AgentClaude, tmux.AgentCodex)
	opts := world.options(t)
	opts.CCCount, opts.CodCount = 2, 1
	opts.Prompt = "Read AGENTS.md first"
	opts.StaggerMode, opts.StaggerDelay = config.SpawnStaggerFixed, 20*time.Second

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}

	if want := []string{"ready", "deliver:%2", "deliver:%3", "deliver:%4"}; !reflect.DeepEqual(world.events, want) {
		t.Fatalf("events = %v, want readiness before every delivery in launch order %v", world.events, want)
	}
	if want := []time.Duration{20 * time.Second, 20 * time.Second}; !reflect.DeepEqual(world.clock.recordedWaits(), want) {
		t.Fatalf("stagger waits = %v, want %v", world.clock.recordedWaits(), want)
	}
	for i, delivery := range world.deliveries {
		if want := fmt.Sprintf("%%%d", i+2); delivery.PaneID != want || delivery.Message != opts.Prompt ||
			!delivery.At.Equal(staggerTestStart.Add(time.Duration(i)*20*time.Second)) {
			t.Fatalf("delivery %d = %+v, want prompt on %s at +%ds", i, delivery, want, 20*i)
		}
	}

	plan := out.Stagger
	if plan == nil || plan.Mode != "fixed" || plan.IntervalMs != 20000 || plan.Provider != "" || plan.Warning != "" || len(plan.Schedule) != 3 {
		t.Fatalf("stagger plan = %+v", plan)
	}
	if len(out.PromptDeliveries) != 3 {
		t.Fatalf("prompt deliveries = %+v", out.PromptDeliveries)
	}
	for i := range plan.Schedule {
		offset := time.Duration(i) * 20 * time.Second
		slot, delivery := plan.Schedule[i], out.PromptDeliveries[i]
		wantPane, wantType := fmt.Sprintf("0.%d", i+1), []string{"claude", "claude", "codex"}[i]
		if slot.Pane != wantPane || slot.AgentType != wantType || slot.Order != i+1 ||
			slot.DelayMs != offset.Milliseconds() || slot.ScheduledAt != staggerAt(offset) {
			t.Fatalf("schedule[%d] = %+v, want %s/%s order %d at +%v", i, slot, wantPane, wantType, i+1, offset)
		}
		if delivery.Pane != wantPane || delivery.AgentType != wantType || delivery.Order != i+1 ||
			!delivery.PromptSent || delivery.DeliveredAt != staggerAt(offset) || delivery.Error != "" {
			t.Fatalf("prompt_deliveries[%d] = %+v", i, delivery)
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"stagger":{"mode":"fixed","interval_ms":20000,"schedule":[`, `"prompt_deliveries":[`, `"scheduled_at":"2026-10-07T12:00:40Z"`, `"delivered_at":"2026-10-07T12:00:20Z"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("envelope missing %s: %s", field, encoded)
		}
	}
}

func TestGetSpawnWithoutStaggerDeliversBackToBack(t *testing.T) {
	world := newStaggerSpawnWorld(t, "unpaced-prompts", tmux.AgentClaude, tmux.AgentClaude)
	opts := world.options(t)
	opts.CCCount = 2
	opts.Prompt = "start"

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	if len(world.clock.recordedWaits()) != 0 || len(world.deliveries) != 2 {
		t.Fatalf("unpaced spawn waited %v and delivered %+v", world.clock.recordedWaits(), world.deliveries)
	}
	if out.Stagger == nil || out.Stagger.Mode != "none" || out.Stagger.IntervalMs != 0 ||
		out.Stagger.Schedule[1].DelayMs != 0 || out.Stagger.Schedule[1].ScheduledAt != staggerAt(0) {
		t.Fatalf("unpaced plan = %+v", out.Stagger)
	}
}

// TestGetSpawnSmartStaggerUsesLearnedRateLimitDelay pins smart mode to the
// project's learned rate-limit history, and an unreadable history to the
// provider's built-in delay with a visible warning.
func TestGetSpawnSmartStaggerUsesLearnedRateLimitDelay(t *testing.T) {
	learned := ratelimit.NewRateLimitTracker("")
	learned.RecordRateLimit("anthropic", "spawn")
	learned.RecordRateLimit("anthropic", "spawn")
	for _, tc := range []struct {
		name         string
		load         func(string) (*ratelimit.RateLimitTracker, error)
		wantInterval time.Duration
		wantWarning  string
	}{
		{
			name:         "learned history",
			load:         func(string) (*ratelimit.RateLimitTracker, error) { return learned, nil },
			wantInterval: learned.GetOptimalDelay("anthropic"),
		},
		{
			name: "unreadable history",
			load: func(string) (*ratelimit.RateLimitTracker, error) {
				return nil, errors.New("parse rate limits file: bad json")
			},
			wantInterval: ratelimit.DefaultDelayAnthropic,
			wantWarning:  "rate-limit history unavailable (parse rate limits file: bad json)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := newStaggerSpawnWorld(t, "smart-prompts", tmux.AgentClaude, tmux.AgentCodex)
			opts := world.options(t)
			var loadedFrom string
			world.lifecycle.LoadRateLimits = func(dir string) (*ratelimit.RateLimitTracker, error) {
				loadedFrom = dir
				return tc.load(dir)
			}
			opts.CCCount, opts.CodCount = 1, 1
			opts.Prompt = "go"
			opts.StaggerMode = config.SpawnStaggerSmart

			out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
			if err != nil || !out.Success {
				t.Fatalf("GetSpawn output=%+v err=%v", out, err)
			}
			if loadedFrom != opts.WorkingDir {
				t.Fatalf("rate-limit history loaded from %q, want the project dir %q", loadedFrom, opts.WorkingDir)
			}
			plan := out.Stagger
			if plan.Mode != "smart" || plan.Provider != "anthropic" || plan.IntervalMs != tc.wantInterval.Milliseconds() ||
				!strings.HasPrefix(plan.Warning, tc.wantWarning) || (tc.wantWarning == "") != (plan.Warning == "") {
				t.Fatalf("smart plan = %+v, want interval %v warning %q", plan, tc.wantInterval, tc.wantWarning)
			}
			if want := []time.Duration{tc.wantInterval}; !reflect.DeepEqual(world.clock.recordedWaits(), want) {
				t.Fatalf("smart waits = %v, want %v", world.clock.recordedWaits(), want)
			}
		})
	}
}

// TestGetSpawnAssignWorkPacesWorkPromptsAndCarriesInitialPrompt drives
// --robot-spawn --spawn-assign-work with a fixed stagger and --spawn-prompt:
// each agent's atomic claim+dispatch waits for its slot, and the initial
// prompt prefixes the work prompt in that single delivery.
func TestGetSpawnAssignWorkPacesWorkPromptsAndCarriesInitialPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "stagger-assign"
	world := newStaggerSpawnWorld(t, session, tmux.AgentClaude, tmux.AgentClaude)
	store := assignment.NewStore(session)
	keys := 0
	world.assignment.LoadAssignmentPolicy = func(string, string, bool) (*config.Config, error) { return testSpawnConfig(), nil }
	world.assignment.FetchActionable = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{
			{ID: "bd-one", Title: "First task", Status: "open", Priority: 1},
			{ID: "bd-two", Title: "Second task", Status: "open", Priority: 2},
		}, nil
	}
	world.assignment.LoadStore = func(string) (*assignment.AssignmentStore, error) { return store, nil }
	world.assignment.ClaimBead = func(_ context.Context, _ string, beadID, actor string) (bv.BeadClaimResult, error) {
		world.record("claim:" + beadID)
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	world.assignment.GetBeadDetails = spawnOpenAssignmentDetails
	world.assignment.GetBeadStatus = func(context.Context, string, string) (string, error) { return "open", nil }
	world.assignment.NewIdempotencyKey = func() (string, error) {
		keys++
		return fmt.Sprintf("stagger-key-%d", keys), nil
	}

	opts := world.options(t)
	opts.CCCount = 2
	opts.AssignWork, opts.AssignStrategy = true, "top-n"
	opts.Prompt = "Read AGENTS.md first"
	opts.StaggerMode, opts.StaggerDelay = config.SpawnStaggerFixed, 15*time.Second

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil || !out.Success {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	// --spawn-prompt implies the readiness wait, and the second claim waits
	// for its slot instead of being held across the stagger interval.
	if want := []string{"ready", "claim:bd-one", "deliver:%2", "claim:bd-two", "deliver:%3"}; !reflect.DeepEqual(world.events, want) {
		t.Fatalf("events = %v, want %v", world.events, want)
	}
	if want := []time.Duration{15 * time.Second}; !reflect.DeepEqual(world.clock.recordedWaits(), want) {
		t.Fatalf("stagger waits = %v, want %v", world.clock.recordedWaits(), want)
	}
	for i, bead := range []string{"bd-one", "bd-two"} {
		delivery := world.deliveries[i]
		offset := time.Duration(i) * 15 * time.Second
		if !strings.HasPrefix(delivery.Message, "Read AGENTS.md first\n\nWork on bead "+bead+":") || !delivery.At.Equal(staggerTestStart.Add(offset)) {
			t.Fatalf("work delivery %d = %+v, want initial prompt + %s at +%v", i, delivery, bead, offset)
		}
		got := out.Assignments[i]
		if got.BeadID != bead || !got.Claimed || !got.PromptSent || got.DeliveredAt != staggerAt(offset) {
			t.Fatalf("assignments[%d] = %+v", i, got)
		}
		if slot := out.Stagger.Schedule[i]; slot.DelayMs != offset.Milliseconds() || slot.ScheduledAt != staggerAt(offset) {
			t.Fatalf("schedule[%d] = %+v", i, slot)
		}
	}
	if len(out.PromptDeliveries) != 0 {
		t.Fatalf("assign mode must report outcomes on assignments[], got prompt_deliveries %+v", out.PromptDeliveries)
	}
}

// TestGetSpawnAssignWorkEnrichesWorkPromptAndReplaysWithoutRequery drives
// --robot-spawn --spawn-assign-work with CASS enrichment: the work prompt
// carries the bead's history before the durable intent is recorded, the
// ledger keeps the template checksum, and re-running the same assignment
// replays the recorded prompt without delivering again or re-querying cass.
func TestGetSpawnAssignWorkEnrichesWorkPromptAndReplaysWithoutRequery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "spawn-assign-context"
	world := newStaggerSpawnWorld(t, session, tmux.AgentClaude)
	store := assignment.NewStore(session)
	var claimedBy string
	world.assignment.LoadAssignmentPolicy = func(string, string, bool) (*config.Config, error) { return testSpawnConfig(), nil }
	world.assignment.FetchActionable = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{ID: "bd-ctx", Title: "Rate limiting middleware", Status: "open", Priority: 1}}, nil
	}
	world.assignment.LoadStore = func(string) (*assignment.AssignmentStore, error) { return store, nil }
	world.assignment.ClaimBead = func(_ context.Context, _ string, beadID, actor string) (bv.BeadClaimResult, error) {
		claimedBy = actor
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	world.assignment.GetBeadDetails = func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
		details := &bv.BeadAssignmentDetails{
			ID: beadID, Status: "open", Title: "Rate limiting middleware",
			Labels: []string{"gateway"}, Description: "Use a token bucket per API key.",
		}
		if claimedBy != "" {
			details.Status, details.Assignee = "in_progress", claimedBy
		}
		return details, nil
	}
	world.assignment.GetBeadStatus = func(context.Context, string, string) (string, error) {
		if claimedBy != "" {
			return "in_progress", nil
		}
		return "open", nil
	}
	world.assignment.NewIdempotencyKey = func() (string, error) { return "spawn-context-key", nil }
	cassBin, cassLog := writeBulkContextStub(t, "cass", bulkContextCASSFixture)

	opts := world.options(t)
	opts.CCCount = 1
	opts.AssignWork, opts.AssignStrategy = true, "top-n"
	opts.PromptContext = bulkContextOptions(cassBin, "")

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil || !out.Success || len(out.Assignments) != 1 {
		t.Fatalf("GetSpawn output=%+v err=%v", out, err)
	}
	got := out.Assignments[0]
	if !got.Claimed || !got.PromptSent || got.CASSInjection == nil || got.CASSInjection.ItemsInjected == 0 {
		t.Fatalf("assignment = %+v (cass_injection %+v), want an enriched delivered prompt", got, got.CASSInjection)
	}
	if len(world.deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(world.deliveries))
	}
	prompt := world.deliveries[0].Message
	history, task := strings.Index(prompt, bulkContextCASSMarker), strings.Index(prompt, "Work on bead bd-ctx")
	if history < 0 || task < 0 || history > task {
		t.Fatalf("delivered prompt history=%d task=%d, want the history above the work prompt:\n%s", history, task, prompt)
	}
	cassCalls := bulkContextStubCalls(t, cassLog)
	if strings.Count(cassCalls, "search") != 2 || strings.Count(cassCalls, "--workspace") != 1 || !strings.Contains(cassCalls, "gateway") || !strings.Contains(cassCalls, "bucket") {
		t.Fatalf("cass calls = %q, want two-scope (global + --workspace) search over the bead's title, labels, and description", cassCalls)
	}
	row := store.Get("bd-ctx")
	if row == nil || row.PromptSent != prompt || row.BaseIntentSHA256 == "" || row.BaseIntentSHA256 == row.IntentSHA256 {
		t.Fatalf("ledger row = %+v, want the enriched prompt recorded with its template checksum", row)
	}

	// Re-running the assignment finds the recorded intent by its template
	// checksum and replays it: no second delivery, no second cass query.
	plan := mockTriage([]bv.TriageRecommendation{{ID: "bd-ctx", Title: "Rate limiting middleware", Status: "open", Priority: 1}}, nil)
	replayed, err := assignWorkToAgentsWithError(
		t.Context(), out, opts.WorkingDir, out.Session, "top-n", testSpawnConfig(),
		false, nil, world.assignment, plan, "", opts.PromptContext, nil,
	)
	if err != nil || len(replayed) != 1 {
		t.Fatalf("replay assignments=%+v err=%v", replayed, err)
	}
	if r := replayed[0]; !r.PromptSent || r.IdempotencyKey != got.IdempotencyKey || r.CASSInjection != nil {
		t.Fatalf("replay = %+v, want the recorded key %s replayed without enrichment", r, got.IdempotencyKey)
	}
	if len(world.deliveries) != 1 {
		t.Fatalf("replay delivered again: %d deliveries", len(world.deliveries))
	}
	if calls := bulkContextStubCalls(t, cassLog); calls != cassCalls {
		t.Fatalf("replay queried cass again: %q", calls)
	}
}

func TestGetSpawnPromptDeliveryFailureIsReportedPerAgent(t *testing.T) {
	world := newStaggerSpawnWorld(t, "partial-prompts", tmux.AgentClaude, tmux.AgentClaude, tmux.AgentClaude)
	world.failPane = "%3"
	opts := world.options(t)
	opts.CCCount = 3
	opts.Prompt = "go"
	opts.StaggerMode, opts.StaggerDelay = config.SpawnStaggerFixed, time.Second

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Success || out.ErrorCode != ErrCodePromptSendFailed || out.Error != "1 of 3 spawn prompt deliveries failed" {
		t.Fatalf("partial failure envelope = %+v", out.RobotResponse)
	}
	sent := []bool{out.PromptDeliveries[0].PromptSent, out.PromptDeliveries[1].PromptSent, out.PromptDeliveries[2].PromptSent}
	if !reflect.DeepEqual(sent, []bool{true, false, true}) || !strings.Contains(out.PromptDeliveries[1].Error, "tmux refused the keystrokes") ||
		out.PromptDeliveries[1].DeliveredAt != "" || len(out.Agents) != 4 {
		t.Fatalf("per-agent outcomes = %+v (agents %d)", out.PromptDeliveries, len(out.Agents))
	}
	if want := []time.Duration{time.Second, time.Second}; !reflect.DeepEqual(world.clock.recordedWaits(), want) {
		t.Fatalf("a failed delivery must keep later agents on schedule: waits %v", world.clock.recordedWaits())
	}
}

func TestGetSpawnStaggerWaitCancellationStopsLaterDeliveries(t *testing.T) {
	world := newStaggerSpawnWorld(t, "cancel-prompts", tmux.AgentClaude, tmux.AgentClaude, tmux.AgentClaude)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	world.lifecycle.Wait = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	opts := world.options(t)
	opts.CCCount = 3
	opts.Prompt = "go"
	opts.StaggerMode, opts.StaggerDelay = config.SpawnStaggerFixed, time.Minute

	out, err := GetSpawn(ctx, opts, testSpawnConfig())
	if err != nil {
		t.Fatal(err)
	}
	if out.Success || out.ErrorCode != ErrCodeTimeout || len(world.deliveries) != 1 {
		t.Fatalf("canceled stagger = %+v with deliveries %+v", out.RobotResponse, world.deliveries)
	}
	if !out.PromptDeliveries[0].PromptSent || out.PromptDeliveries[1].PromptSent || out.PromptDeliveries[2].PromptSent ||
		!strings.Contains(out.PromptDeliveries[2].Error, "canceled") {
		t.Fatalf("canceled deliveries = %+v", out.PromptDeliveries)
	}
}

// TestGetSpawnRejectsInvalidStaggerBeforeLifecycle pins INVALID_FLAG for an
// unsupported mode or an out-of-range delay before any tmux side effect.
func TestGetSpawnRejectsInvalidStaggerBeforeLifecycle(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		delay   time.Duration
		wantErr string
	}{
		{mode: "adaptive", wantErr: `--spawn-stagger-mode must be one of none, fixed, or smart; got "adaptive"`},
		{mode: "fixed", delay: -time.Second, wantErr: "--spawn-stagger-delay must be between 0 and 5m0s"},
		{mode: "none", delay: config.MaxSpawnStaggerDelay + time.Second, wantErr: "--spawn-stagger-delay must be between 0 and 5m0s"},
	} {
		t.Run(tc.mode+"/"+tc.delay.String(), func(t *testing.T) {
			var tmuxCalls atomic.Int32
			lifecycle := &SpawnLifecycleDependencies{IsTMUXInstalled: func() bool { tmuxCalls.Add(1); return true }}
			out, err := GetSpawn(t.Context(), SpawnOptions{
				Session: "invalid-stagger", CCCount: 1, Prompt: "go", WorkingDir: t.TempDir(),
				StaggerMode: tc.mode, StaggerDelay: tc.delay, LifecycleDeps: lifecycle,
			}, testSpawnConfig())
			if err != nil {
				t.Fatal(err)
			}
			if out.Success || out.ErrorCode != ErrCodeInvalidFlag || out.Error != tc.wantErr || out.Hint == "" {
				t.Fatalf("invalid stagger envelope = %+v, want INVALID_FLAG %q", out.RobotResponse, tc.wantErr)
			}
			if tmuxCalls.Load() != 0 || out.Agents == nil {
				t.Fatalf("invalid stagger reached the tmux lifecycle (%d calls)", tmuxCalls.Load())
			}
		})
	}
}

// Grok Build phase 2 (GH#251) implemented readiness and composer-gated
// delivery, so a spawn prompt for Grok agents is accepted like any other.
func TestValidateSpawnRequestAcceptsGrokSpawnPrompt(t *testing.T) {
	if _, err := validateSpawnRequest(SpawnOptions{Session: "grok-prompt", GrokCount: 1, Prompt: "go"}); err != nil {
		t.Fatalf("validateSpawnRequest refused a Grok spawn prompt: %v", err)
	}
}

// TestGetSpawnDryRunPreviewsStaggerSchedule pins that a dry run reports the
// per-agent delays it would use without waiting, delivering, or stamping
// scheduled times.
func TestGetSpawnDryRunPreviewsStaggerSchedule(t *testing.T) {
	world := newStaggerSpawnWorld(t, "dry-stagger", tmux.AgentClaude, tmux.AgentCodex)
	opts := world.options(t)
	opts.CCCount, opts.CodCount = 1, 1
	opts.Prompt = "go"
	opts.StaggerMode, opts.StaggerDelay = config.SpawnStaggerFixed, 25*time.Second
	opts.DryRun = true

	out, err := GetSpawn(t.Context(), opts, testSpawnConfig())
	if err != nil || !out.Success || !out.DryRun {
		t.Fatalf("dry run output=%+v err=%v", out, err)
	}
	if out.Stagger == nil || len(out.Stagger.Schedule) != 2 || out.Stagger.Schedule[1].DelayMs != 25000 ||
		out.Stagger.Schedule[0].AgentType != "claude" || out.Stagger.Schedule[1].ScheduledAt != "" {
		t.Fatalf("dry-run plan = %+v", out.Stagger)
	}
	if len(world.events) != 0 || len(world.clock.recordedWaits()) != 0 || out.PromptDeliveries != nil {
		t.Fatalf("dry run actuated: events %v waits %v deliveries %+v", world.events, world.clock.recordedWaits(), out.PromptDeliveries)
	}
}
