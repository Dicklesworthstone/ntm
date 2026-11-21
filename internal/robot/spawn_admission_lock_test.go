package robot

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func isolateSpawnAdmissionTest(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("NTM_CONFIG", "")
}

func cappedSpawnConfig(limit int) *config.Config {
	cfg := config.Default()
	cfg.SpawnPacing.Enabled = true
	cfg.SpawnPacing.MaxConcurrentSpawns = 1000 // Isolate capacity from ambient CPU pressure.
	cfg.SpawnPacing.AgentCaps = config.AgentPacingConfig{ClaudeMaxConcurrent: limit}
	return cfg
}

func TestNeedsSpawnAdmissionFence(t *testing.T) {
	t.Parallel()
	cfg := cappedSpawnConfig(1)
	if needsSpawnAdmissionFence(SpawnOptions{DryRun: true}, cfg) || needsSpawnAdmissionFence(SpawnOptions{}, nil) {
		t.Fatal("preview or unconfigured spawn acquired capacity")
	}
	if !needsSpawnAdmissionFence(SpawnOptions{}, cfg) {
		t.Fatal("shared cap has no fence")
	}
	cfg.SpawnPacing.AgentCaps = config.AgentPacingConfig{}
	if needsSpawnAdmissionFence(SpawnOptions{}, cfg) {
		t.Fatal("uncapped spawn changed behavior")
	}
	cfg.SpawnPacing.AgentTypeLimits = map[string]int{"codex": 1}
	if !needsSpawnAdmissionFence(SpawnOptions{}, cfg) {
		t.Fatal("per-type-only cap has no fence")
	}
	cfg.SpawnPacing.Enabled = false
	if needsSpawnAdmissionFence(SpawnOptions{}, cfg) {
		t.Fatal("disabled policy acquired capacity")
	}
}

func TestGetSpawnAdmissionFenceCoversWholeBatchAndReleasesBeforeReadiness(t *testing.T) {
	isolateSpawnAdmissionTest(t)
	for _, decorated := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "progress_pacing_readiness"}[decorated], func(t *testing.T) {
			var order []string
			held := false
			releases := 0
			panes := []tmux.Pane{{ID: "%1", Index: 0}}
			deps := testSpawnLifecycleDependencies(nil)
			requireHeld := func(stage string) {
				t.Helper()
				if !held {
					t.Fatalf("%s ran outside admission ownership", stage)
				}
				order = append(order, stage)
			}
			deps.AcquireAdmission = func(context.Context) (func(), error) {
				held = true
				order = append(order, "lock")
				return func() { held = false; releases++; order = append(order, "release") }, nil
			}
			deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
				requireHeld("inventory")
				return map[string][]tmux.Pane{}, nil
			}
			deps.SessionExists = func(context.Context, string) (bool, error) { return false, nil }
			deps.CreateSession = func(context.Context, string, string, int) error { requireHeld("create"); return nil }
			deps.GetPanes = func(context.Context, string) ([]tmux.Pane, error) { return append([]tmux.Pane(nil), panes...), nil }
			deps.SplitWindow = func(context.Context, string, string) (string, error) {
				requireHeld("split")
				panes = append(panes, tmux.Pane{ID: "%2", Index: 1})
				return "%2", nil
			}
			deps.ApplyTiledLayout = func(context.Context, string) error { requireHeld("layout"); return nil }
			deps.LaunchAgent = func(_ context.Context, pane tmux.Pane, _, kind string, _ int, _, _ string) (SpawnedAgent, error) {
				requireHeld("launch")
				return SpawnedAgent{Pane: pane.Ref().Physical(), Type: kind}, nil
			}
			deps.StartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
				if held {
					t.Fatal("monitor startup retained admission ownership")
				}
				order = append(order, "monitor")
				return &resilience.SpawnMonitorResult{MonitorStarted: true}, nil
			}
			deps.WaitForReady = func(_ context.Context, out *SpawnOutput, _ time.Duration) error {
				if len(out.Agents) == 1 && decorated {
					requireHeld("individual_ready")
				} else {
					if held {
						t.Fatal("final readiness retained admission ownership")
					}
					order = append(order, "ready")
				}
				for i := range out.Agents {
					out.Agents[i].Ready = true
				}
				return nil
			}
			opts := SpawnOptions{Session: "guarded-batch", CCCount: 2, NoUserPane: true, WaitReady: true,
				WorkingDir: t.TempDir(), LifecycleDeps: deps}
			if decorated {
				opts = WithSpawnProgress(opts, func(SpawnProgress) error { return nil })
				var err error
				opts, err = WithSpawnLaunchInterval(opts, time.Nanosecond)
				if err != nil {
					t.Fatal(err)
				}
				opts, err = WithSpawnLaunchReadiness(opts, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := GetSpawn(context.Background(), opts, cappedSpawnConfig(2))
			if err != nil || !out.Success || held || releases != 1 || out.Admission == nil || !out.Admission.Serialized {
				t.Fatalf("spawn=%+v err=%v held=%t releases=%d", out, err, held, releases)
			}
			want := []string{"lock", "inventory", "create", "split", "layout", "launch", "launch", "release", "monitor", "ready"}
			if decorated {
				want = []string{"lock", "inventory", "create", "split", "layout", "launch", "individual_ready", "launch", "individual_ready", "release", "monitor", "ready"}
			}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("order=%v want=%v", order, want)
			}
		})
	}
}

func TestGetSpawnAdmissionFenceReleasesOnFailuresAndPanic(t *testing.T) {
	isolateSpawnAdmissionTest(t)
	for _, stage := range []string{"inventory", "capacity", "create", "layout", "launch", "cancel_launch", "panic"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps := testSpawnLifecycleDependencies([]tmux.Pane{{ID: "%1", Index: 0}})
			releases := 0
			deps.AcquireAdmission = func(context.Context) (func(), error) { return func() { releases++ }, nil }
			deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
				if stage == "inventory" {
					return nil, errors.New("unavailable")
				}
				if stage == "capacity" {
					return map[string][]tmux.Pane{"other": {{Type: tmux.AgentClaude}}}, nil
				}
				return map[string][]tmux.Pane{}, nil
			}
			deps.SessionExists = func(context.Context, string) (bool, error) { return false, nil }
			deps.CreateSession = func(context.Context, string, string, int) error {
				if stage == "create" {
					return errors.New("creation failed")
				}
				return nil
			}
			deps.ApplyTiledLayout = func(context.Context, string) error {
				if stage == "panic" {
					panic("fixture panic")
				}
				if stage == "layout" {
					return errors.New("layout failed")
				}
				return nil
			}
			deps.LaunchAgent = func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
				if stage == "cancel_launch" {
					cancel()
					return SpawnedAgent{}, ctx.Err()
				}
				return SpawnedAgent{}, errors.New("launch failed")
			}
			deps.StartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
				return &resilience.SpawnMonitorResult{}, nil
			}
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						if r != "fixture panic" {
							panic(r)
						}
						panicked = true
					}
				}()
				out, err := GetSpawn(ctx, SpawnOptions{Session: "guarded-failure", CCCount: 1, NoUserPane: true,
					WorkingDir: t.TempDir(), LifecycleDeps: deps}, cappedSpawnConfig(1))
				if err != nil || out.Success {
					t.Fatalf("expected structured failure: %+v %v", out, err)
				}
			}()
			if releases != 1 || panicked != (stage == "panic") {
				t.Fatalf("releases=%d panicked=%t", releases, panicked)
			}
		})
	}
}

func TestGetSpawnAdmissionOwnershipFailureDoesNotReadOrMutate(t *testing.T) {
	isolateSpawnAdmissionTest(t)
	for _, scenario := range []string{"busy", "unavailable", "missing_release", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps := testSpawnLifecycleDependencies(nil)
			deps.AcquireAdmission = func(context.Context) (func(), error) {
				switch scenario {
				case "busy":
					return nil, pressure.ErrSpawnAdmissionBusy
				case "unavailable":
					return nil, errors.New("permission denied")
				case "cancelled":
					cancel()
					return func() {}, nil
				default:
					return nil, nil
				}
			}
			deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
				t.Fatal("inventory read without owner")
				return nil, nil
			}
			deps.CreateSession = func(context.Context, string, string, int) error { t.Fatal("mutation without owner"); return nil }
			out, err := GetSpawn(ctx, SpawnOptions{Session: "no-owner", CCCount: 1, WorkingDir: t.TempDir(), LifecycleDeps: deps}, cappedSpawnConfig(1))
			if err != nil || out.Success || out.Agents == nil || len(out.Agents) != 0 {
				t.Fatalf("invalid failure: %+v %v", out, err)
			}
			if scenario == "cancelled" {
				if out.ErrorCode != ErrCodeTimeout {
					t.Fatalf("lost cancellation: %+v", out)
				}
			} else {
				if out.ErrorCode != ErrCodeResourceBusy || out.Admission == nil || out.Admission.Serialized || out.Admission.AgentInventoryAvailable {
					t.Fatalf("unowned inventory treated as authoritative: %+v", out)
				}
			}
		})
	}
}

func TestGetSpawnAdmissionPreviewNeverAcquires(t *testing.T) {
	isolateSpawnAdmissionTest(t)
	deps := testSpawnLifecycleDependencies(nil)
	deps.AcquireAdmission = func(context.Context) (func(), error) { t.Fatal("preview acquired capacity"); return nil, nil }
	out, err := GetSpawn(context.Background(), SpawnOptions{Session: "guard-preview", CCCount: 1, DryRun: true,
		WorkingDir: t.TempDir(), LifecycleDeps: deps}, cappedSpawnConfig(1))
	if err != nil || !out.Success || out.Admission == nil || out.Admission.Serialized || len(out.WouldCreate) != 2 {
		t.Fatalf("preview contract changed: %+v %v", out, err)
	}
}

func TestGetSpawnConcurrentAdmissionWaiterCannotSpendLastSlot(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("real cross-process admission is supported on Linux and macOS only")
	}
	isolateSpawnAdmissionTest(t)
	var running, reads atomic.Int32
	entered := make(chan struct{})
	finish := make(chan struct{})
	finished := make(chan *SpawnOutput, 1)
	allowFinish := sync.OnceFunc(func() { close(finish) })
	defer allowFinish()
	markEntered := sync.OnceFunc(func() { close(entered) })
	ownerCtx, stopOwner := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopOwner()
	deps := testSpawnLifecycleDependencies([]tmux.Pane{{ID: "%1", Index: 0}})
	deps.AcquireAdmission = pressure.AcquireSpawnAdmission
	deps.GetAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		reads.Add(1)
		if running.Load() > 0 {
			return map[string][]tmux.Pane{"first": {{ID: "%1", Type: tmux.AgentClaude}}}, nil
		}
		return map[string][]tmux.Pane{}, nil
	}
	deps.LaunchAgent = func(ctx context.Context, _ tmux.Pane, _, _ string, _ int, _, _ string) (SpawnedAgent, error) {
		markEntered()
		select {
		case <-finish:
		case <-ctx.Done():
			return SpawnedAgent{}, ctx.Err()
		}
		running.Add(1)
		return SpawnedAgent{Pane: "0.0", Type: "claude"}, nil
	}
	deps.StartSessionMonitor = func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
		return &resilience.SpawnMonitorResult{MonitorStarted: true}, nil
	}
	opts := SpawnOptions{Session: "first", CCCount: 1, NoUserPane: true, WorkingDir: t.TempDir(), LifecycleDeps: deps}
	go func() { out, _ := GetSpawn(ownerCtx, opts, cappedSpawnConfig(1)); finished <- out }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first spawn did not reach launch")
	}
	// A waiting request must cancel without even reading an obsolete inventory.
	other := opts
	other.Session = "second"
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	out, err := GetSpawn(ctx, other, cappedSpawnConfig(1))
	cancel()
	allowFinish()
	var first *SpawnOutput
	select {
	case first = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("first spawn did not finish after its launch was released")
	}
	if err != nil || out.Success || out.ErrorCode != ErrCodeTimeout || reads.Load() != 1 || !first.Success {
		t.Fatalf("waiting spawn read or spent the last slot: first=%+v second=%+v err=%v reads=%d", first, out, err, reads.Load())
	}
	// After ownership transfers, count the already launched agent and refuse.
	out, err = GetSpawn(context.Background(), other, cappedSpawnConfig(1))
	if err != nil || out.Success || out.Admission == nil || out.Admission.Reason != "agent_limit_exceeded" || reads.Load() != 2 || running.Load() != 1 {
		t.Fatalf("recount did not enforce last slot: %+v err=%v reads=%d running=%d", out, err, reads.Load(), running.Load())
	}
}
