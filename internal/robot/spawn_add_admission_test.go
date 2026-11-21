package robot

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func additionAdmissionConfig(shared int, limits map[string]int) *config.Config {
	cfg := config.Default()
	cfg.SpawnPacing.Enabled = true
	cfg.SpawnPacing.MaxConcurrentSpawns = int(^uint(0) >> 1)
	cfg.SpawnPacing.AgentCaps = config.AgentPacingConfig{ClaudeMaxConcurrent: shared}
	cfg.SpawnPacing.AgentTypeLimits = limits
	return cfg
}

func TestAdditionAdmissionCanonicalCountsAndPaneIncrement(t *testing.T) {
	cfg := additionAdmissionConfig(20, map[string]int{"claude": 3})
	counts := map[string]int{"CC": 1, "claude-code": 1, "custom-agent": 1, "ollama": 1}
	original := map[string]int{"CC": 1, "claude-code": 1, "custom-agent": 1, "ollama": 1}
	held, released := false, 0
	deps := SpawnLifecycleDependencies{
		AcquireAdmission: func(context.Context) (func(), error) {
			held = true
			return func() { held = false; released++ }, nil
		},
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			if !held {
				t.Fatal("addition counted the fleet without ownership")
			}
			return map[string][]tmux.Pane{
				"existing": {{ID: "%1", Type: tmux.AgentClaude}, {ID: "%2", Type: tmux.AgentClaude}},
				"target":   {{ID: "%3", Type: tmux.AgentUser}, {ID: "%4", Type: tmux.AgentType("custom-agent")}},
			}, nil
		},
	}
	release, admission, err := beginAgentAddition(context.Background(), "target", counts, cfg, deps)
	var denied *SpawnAdmissionError
	if !errors.As(err, &denied) || denied.ErrorCode != ErrCodeResourceBusy || admission == nil || admission.Reason != "agent_type_limit_exceeded" {
		t.Fatalf("addition bypassed its canonical cap: admission=%+v err=%v", admission, err)
	}
	if release != nil || held || released != 1 || !admission.Serialized {
		t.Fatalf("refusal leaked or lost ownership: released=%d admission=%+v", released, admission)
	}
	if admission.RequestedAgents != 4 || admission.RunningAgents != 3 || admission.RequestedPanes != 6 || admission.AdditionalPanes != 4 || admission.ProjectedPanes != 8 {
		t.Fatalf("addition was mistaken for reuse of existing panes: %+v", admission)
	}
	row := admission.AgentTypeLimits[0]
	if row.Requested != 2 || row.Running != 2 || row.Projected != 4 || !row.BlocksRequest {
		t.Fatalf("aliases or persona underlying types escaped cap: %+v", row)
	}
	if !reflect.DeepEqual(counts, original) {
		t.Fatal("admission changed caller count map")
	}
}

func TestAdditionAdmissionSharedBudgetIncludesOtherTypes(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"cursor", "windsurf", "aider", "ollama", "custom-agent"} {
		t.Run(kind, func(t *testing.T) {
			cfg := additionAdmissionConfig(2, map[string]int{"claude": 10})
			deps := SpawnLifecycleDependencies{
				AcquireAdmission: func(context.Context) (func(), error) { return func() {}, nil },
				GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
					return map[string][]tmux.Pane{"other": {{Type: tmux.AgentType(kind)}, {Type: tmux.AgentCodex}}}, nil
				},
			}
			release, admission, err := beginAgentAddition(context.Background(), "target", map[string]int{kind: 1}, cfg, deps)
			if err == nil || release != nil || admission == nil || admission.Reason != "agent_limit_exceeded" || admission.RunningAgents != 2 {
				t.Fatalf("shared budget dropped %s: admission=%+v err=%v", kind, admission, err)
			}
		})
	}
}

func TestAdditionAdmissionRetainsLeaseUntilCallerFinishes(t *testing.T) {
	held, releases := false, 0
	cfg := additionAdmissionConfig(20, map[string]int{"claude": 1, "codex": 4})
	deps := SpawnLifecycleDependencies{
		AcquireAdmission: func(context.Context) (func(), error) { held = true; return func() { held = false; releases++ }, nil },
		GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
			// Already over the Claude-specific cap, but this request is Codex-only.
			return map[string][]tmux.Pane{"target": {{Type: tmux.AgentClaude}, {Type: tmux.AgentClaude}}}, nil
		},
	}
	release, admission, err := beginAgentAddition(context.Background(), "target", map[string]int{"cod": 1}, cfg, deps)
	if err != nil || release == nil || admission == nil || !held || releases != 0 || !admission.Serialized || admission.Decision != pressure.SpawnAdmissionAdmit {
		t.Fatalf("addition lost its lease or applied an unrelated cap: %+v %v", admission, err)
	}
	release()
	release()
	if held || releases != 1 {
		t.Fatalf("release is not idempotent: held=%t releases=%d", held, releases)
	}
}

func TestAdditionAdmissionInvalidInputsDoNotReadOrLock(t *testing.T) {
	maxCount := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name, session string
		ctx           context.Context
		counts        map[string]int
		cfg           *config.Config
	}{
		{"nil context", "s", nil, map[string]int{"cc": 1}, additionAdmissionConfig(1, nil)},
		{"bad session", "s:1", context.Background(), map[string]int{"cc": 1}, additionAdmissionConfig(1, nil)},
		{"missing policy", "s", context.Background(), map[string]int{"cc": 1}, nil},
		{"no agents", "s", context.Background(), nil, additionAdmissionConfig(1, nil)},
		{"negative", "s", context.Background(), map[string]int{"cc": -1}, additionAdmissionConfig(1, nil)},
		{"overflow", "s", context.Background(), map[string]int{"cc": maxCount, "claude": 1}, additionAdmissionConfig(1, nil)},
		{"user pane", "s", context.Background(), map[string]int{"user": 1}, additionAdmissionConfig(1, nil)},
		{"unknown", "s", context.Background(), map[string]int{"unknown": 1}, additionAdmissionConfig(1, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := SpawnLifecycleDependencies{
				AcquireAdmission: func(context.Context) (func(), error) { t.Fatal("invalid request acquired ownership"); return nil, nil },
				GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
					t.Fatal("invalid request read fleet")
					return nil, nil
				},
			}
			release, _, err := beginAgentAddition(tc.ctx, tc.session, tc.counts, tc.cfg, deps)
			var refusal *SpawnAdmissionError
			if release != nil || !errors.As(err, &refusal) || refusal.ErrorCode != ErrCodeInvalidFlag {
				t.Fatalf("invalid input did not fail before I/O: %v", err)
			}
		})
	}
}

func TestAdditionAdmissionDisabledDoesNotObserveOrLock(t *testing.T) {
	cfg := additionAdmissionConfig(1, map[string]int{"claude": 1})
	cfg.SpawnPacing.Enabled = false
	release, admission, err := beginAgentAddition(context.Background(), "s", map[string]int{"cc": 10}, cfg, SpawnLifecycleDependencies{})
	if err != nil || release == nil || admission != nil {
		t.Fatalf("disabled policy changed the add: %+v %v", admission, err)
	}
	release()
	release()
}

func TestAdditionAdmissionOwnershipAndInventoryFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"busy", "unavailable", "nil-release", "owned-error", "inventory-error", "inventory-cancel", "nil-inventory", "cancel-after-read", "panic"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			released, reads := 0, 0
			deps := SpawnLifecycleDependencies{
				AcquireAdmission: func(context.Context) (func(), error) {
					switch mode {
					case "busy":
						return nil, pressure.ErrSpawnAdmissionBusy
					case "unavailable":
						return nil, errors.New("lock denied")
					case "nil-release":
						return nil, nil
					case "owned-error":
						return func() { released++ }, errors.New("lock validation failed")
					default:
						return func() { released++ }, nil
					}
				},
				GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
					reads++
					switch mode {
					case "inventory-cancel":
						return nil, context.Canceled
					case "cancel-after-read":
						cancel()
						return map[string][]tmux.Pane{}, nil
					case "panic":
						panic("injected capture panic")
					default:
						return nil, errors.New("capture unavailable")
					}
				},
			}
			if mode == "nil-inventory" {
				deps.GetAllPanes = nil
			}
			if mode == "panic" {
				func() {
					defer func() {
						if recover() == nil {
							t.Error("expected capture panic")
						}
					}()
					_, _, _ = beginAgentAddition(ctx, "s", map[string]int{"cc": 1}, additionAdmissionConfig(1, nil), deps)
				}()
				if released != 1 {
					t.Fatalf("panic leaked ownership: %d", released)
				}
				return
			}
			release, admission, err := beginAgentAddition(ctx, "s", map[string]int{"cc": 1}, additionAdmissionConfig(1, nil), deps)
			var refusal *SpawnAdmissionError
			if release != nil || !errors.As(err, &refusal) {
				t.Fatalf("failed port authorized addition: %+v %v", admission, err)
			}
			if mode == "inventory-cancel" || mode == "cancel-after-read" {
				if refusal.ErrorCode != ErrCodeTimeout || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else if refusal.ErrorCode != ErrCodeResourceBusy || admission == nil || admission.AgentInventoryAvailable {
				t.Fatalf("failed evidence became authorization: %+v %v", admission, err)
			}
			switch mode {
			case "busy", "unavailable", "nil-release":
				if released != 0 || reads != 0 {
					t.Fatal("failed ownership touched fleet")
				}
			case "owned-error":
				if released != 1 || reads != 0 {
					t.Fatal("owned failure leaked or read fleet")
				}
			default:
				if released != 1 {
					t.Fatal("capture failure leaked ownership")
				}
			}
			if mode == "busy" && (!errors.Is(err, pressure.ErrSpawnAdmissionBusy) || admission.Reason != "spawn_admission_busy") {
				t.Fatal("busy classification lost")
			}
		})
	}
}

// Real OS ownership is shared between an add's admission and GetSpawn. Only
// tmux observations are fixtures; this does not simulate an in-memory lock.
func TestAdditionAndRobotSpawnShareAdmissionFence(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("OS admission locking is Linux/macOS-only")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := additionAdmissionConfig(1, nil)
	var mu sync.Mutex
	var inventory map[string][]tmux.Pane
	read := func(context.Context) (map[string][]tmux.Pane, error) {
		mu.Lock()
		defer mu.Unlock()
		return inventory, nil
	}
	release, admission, err := beginAgentAddition(context.Background(), "first", map[string]int{"cc": 1}, cfg, SpawnLifecycleDependencies{AcquireAdmission: pressure.AcquireSpawnAdmission, GetAllPanes: read})
	if err != nil || admission == nil || !admission.Serialized {
		t.Fatalf("real add fence failed: %+v %v", admission, err)
	}
	defer release()
	started := make(chan struct{}, 1)
	deps := &SpawnLifecycleDependencies{
		IsTMUXInstalled: func() bool { return true },
		AcquireAdmission: func(ctx context.Context) (func(), error) {
			started <- struct{}{}
			return pressure.AcquireSpawnAdmission(ctx)
		},
		GetAllPanes: read,
		SessionExists: func(context.Context, string) (bool, error) {
			t.Error("over-cap competitor reached lifecycle")
			return false, errors.New("unexpected lifecycle")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan *SpawnOutput, 1)
	finished := make(chan struct{})
	project := t.TempDir()
	go func() {
		defer close(finished)
		out, callErr := GetSpawn(ctx, SpawnOptions{Session: "second", CCCount: 1, WorkingDir: project, LifecycleDeps: deps}, cfg)
		if callErr != nil {
			t.Errorf("spawn transport error: %v", callErr)
		}
		done <- out
	}()
	defer func() {
		cancel()
		release()
		<-finished
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("competitor did not attempt admission")
	}
	mu.Lock()
	inventory = map[string][]tmux.Pane{"first": {{ID: "%7", Type: tmux.AgentClaude}}}
	mu.Unlock()
	release()
	select {
	case out := <-done:
		if out == nil || out.Success || out.Admission == nil || out.Admission.Reason != "agent_limit_exceeded" || out.Admission.RunningAgents != 1 || !out.Admission.Serialized {
			t.Fatalf("contender failed to recount the added agent: %+v", out)
		}
	case <-ctx.Done():
		t.Fatal("contender did not finish")
	}
}
