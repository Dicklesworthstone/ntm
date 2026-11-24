package serve

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSpawnPolicyBindsSelectedConfigAndProject(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "operator.toml")
	projectA, projectB := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, tc := range []struct {
		name, workingDir, wantDir string
	}{
		{"selected project", "", projectA},
		{"relative override", "component", filepath.Join(projectA, "component")},
		{"absolute override", projectB, projectB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{projectDir: projectA}
			type ownerKey struct{}
			ctx := context.WithValue(context.Background(), ownerKey{}, "job-owner")
			ports := &robot.SpawnLifecycleDependencies{}
			opts := robot.SpawnOptions{Session: "workers", Preset: "full-stack", WorkingDir: tc.workingDir,
				DryRun: true, LifecycleDeps: ports, ConfigPath: "/untrusted/config", RequireConfig: false,
				AssignWork: true, AssignStrategy: "top-n", RequireReservation: true, ReservationPaths: []string{"internal/**"}}
			original := opts
			effective := config.Default()
			effective.Agents.Claude = "operator-command"
			calls := 0
			out, err := s.spawnWithPolicy(ctx, opts, selected, true,
				func(project, path string, required bool) (*config.Config, error) {
					calls++
					if project != tc.wantDir || path != selected || !required {
						t.Fatalf("policy source = (%q,%q,%t)", project, path, required)
					}
					return effective, nil
				},
				func(gotCtx context.Context, got robot.SpawnOptions, cfg *config.Config) (*robot.SpawnOutput, error) {
					calls++
					want := original
					want.WorkingDir, want.ConfigPath, want.RequireConfig = tc.wantDir, selected, true
					if gotCtx != ctx || cfg != effective || !reflect.DeepEqual(got, want) {
						t.Fatalf("spawn lost context, policy, or request controls: %+v", got)
					}
					return &robot.SpawnOutput{Session: got.Session, WorkingDir: got.WorkingDir}, nil
				})
			if err != nil || out == nil || calls != 2 || !reflect.DeepEqual(opts, original) {
				t.Fatalf("output=%+v err=%v calls=%d options mutated=%t", out, err, calls, !reflect.DeepEqual(opts, original))
			}
		})
	}
}

func TestSpawnPolicyRefusesBeforeEngineAndRetainsEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name                                  string
		cancelBefore, cancelDuring, nilConfig bool
		loadErr                               error
		wantCode                              string
		wantLoads                             int
	}{
		{name: "missing explicit file", loadErr: errors.New("selected config missing"), wantCode: robot.ErrCodeInvalidFlag, wantLoads: 1},
		{name: "invalid project overlay", loadErr: errors.New("project overlay malformed"), wantCode: robot.ErrCodeInvalidFlag, wantLoads: 1},
		{name: "nil configuration", nilConfig: true, wantCode: robot.ErrCodeInternalError, wantLoads: 1},
		{name: "already cancelled", cancelBefore: true, wantCode: robot.ErrCodeTimeout},
		{name: "cancelled while loading", cancelDuring: true, wantCode: robot.ErrCodeTimeout, wantLoads: 1},
		{name: "cancellation wins loader error", cancelDuring: true, loadErr: errors.New("read interrupted"), wantCode: robot.ErrCodeTimeout, wantLoads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{projectDir: t.TempDir()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			loads := 0
			out, err := s.spawnWithPolicy(ctx, robot.SpawnOptions{Session: "workers", Preset: "full-stack", DryRun: true}, "/operator/config.toml", true,
				func(string, string, bool) (*config.Config, error) {
					loads++
					if tc.cancelDuring {
						cancel()
					}
					if tc.nilConfig || tc.loadErr != nil {
						return nil, tc.loadErr
					}
					return config.Default(), nil
				},
				func(context.Context, robot.SpawnOptions, *config.Config) (*robot.SpawnOutput, error) {
					t.Fatal("policy refusal reached the spawn engine")
					return nil, nil
				})
			if err != nil || out == nil || out.Success || out.ErrorCode != tc.wantCode || out.Error == "" || loads != tc.wantLoads {
				t.Fatalf("output=%+v err=%v loads=%d", out, err, loads)
			}
			if !out.DryRun || out.PresetUsed != "full-stack" || out.Session != "workers" || out.CreatedAt == "" || out.Agents == nil {
				t.Fatalf("failure lost request identity or array contract: %+v", out)
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var envelope map[string]interface{}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if agents, ok := envelope["agents"].([]interface{}); !ok || len(agents) != 0 {
				t.Fatalf("failure agents must encode as [], got %s", raw)
			}
		})
	}
}

func TestSpawnPolicyPreservesPartialEngineResult(t *testing.T) {
	s := &Server{projectDir: t.TempDir()}
	partial := &robot.SpawnOutput{Session: "workers", Agents: []robot.SpawnedAgent{{Pane: "2.4", Type: "claude"}}}
	cause := errors.New("second launch failed")
	out, err := s.spawnWithPolicy(context.Background(), robot.SpawnOptions{Session: "workers"}, "/operator/config.toml", true,
		func(string, string, bool) (*config.Config, error) { return config.Default(), nil },
		func(context.Context, robot.SpawnOptions, *config.Config) (*robot.SpawnOutput, error) {
			return partial, cause
		})
	if out != partial || err != cause {
		t.Fatalf("partial effects discarded or rewritten: %+v %v", out, err)
	}
}

func TestSpawnPolicyIndependentConcurrentProjectLoads(t *testing.T) {
	s := &Server{projectDir: t.TempDir()}
	root := t.TempDir()
	var wg sync.WaitGroup
	for _, project := range []string{filepath.Join(root, "one"), filepath.Join(root, "two")} {
		wg.Add(1)
		go func(project string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, err := s.spawnWithPolicy(context.Background(), robot.SpawnOptions{Session: "workers", WorkingDir: project}, "/operator/config.toml", true,
					func(dir, _ string, _ bool) (*config.Config, error) {
						cfg := config.Default()
						cfg.Agents.Claude = dir
						return cfg, nil
					},
					func(_ context.Context, opts robot.SpawnOptions, cfg *config.Config) (*robot.SpawnOutput, error) {
						if opts.WorkingDir != project || cfg.Agents.Claude != project {
							t.Error("project policy leaked across requests")
						}
						cfg.Agents.Claude = "mutated by this execution"
						return nil, nil
					})
				if err != nil {
					t.Error(err)
				}
			}
		}(project)
	}
	wg.Wait()
}

func TestConfigureSpawnPolicyRejectsMissingSelection(t *testing.T) {
	var nilServer *Server
	if err := nilServer.ConfigureSpawnPolicy("config.toml", true); err == nil {
		t.Fatal("nil server accepted")
	}
	s := &Server{}
	for _, path := range []string{"", "  "} {
		if err := s.ConfigureSpawnPolicy(path, true); err == nil || s.spawnAgents != nil {
			t.Fatal("invalid selection installed a spawn backend")
		}
	}
}

func TestSpawnPolicyReachesSerializedAdmission(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(map[bool]string{true: "preview", false: "execution"}[dryRun], func(t *testing.T) {
			s := &Server{projectDir: t.TempDir()}
			effective := config.Default()
			effective.SpawnPacing.AgentTypeLimits = map[string]int{"claude": 1}
			held, acquisitions, releases, inventories := false, 0, 0, 0
			ports := &robot.SpawnLifecycleDependencies{
				IsTMUXInstalled: func() bool { return true },
				AcquireAdmission: func(context.Context) (func(), error) {
					acquisitions++
					held = true
					return func() { held = false; releases++ }, nil
				},
				GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
					inventories++
					if held == dryRun {
						t.Errorf("inventory ownership=%t for dry_run=%t", held, dryRun)
					}
					return map[string][]tmux.Pane{"existing": {{ID: "%9", Type: tmux.AgentClaude}}}, nil
				},
				SessionExists: func(context.Context, string) (bool, error) {
					t.Error("over-cap spawn crossed the admission boundary")
					return false, errors.New("unexpected lifecycle access")
				},
				CollectPressure: deterministicTestPressure,
			}
			opts := robot.SpawnOptions{Session: "policy-budget", CCCount: 1, NoUserPane: true, DryRun: dryRun, LifecycleDeps: ports}
			out, err := s.spawnWithPolicy(context.Background(), opts, "/operator/config.toml", true,
				func(string, string, bool) (*config.Config, error) { return effective, nil }, robot.GetSpawn)
			if err != nil || out == nil || out.Admission == nil || out.Admission.Decision != pressure.SpawnAdmissionRefuse || out.Admission.Reason != "agent_type_limit_exceeded" {
				t.Fatalf("configured cap was bypassed: output=%+v err=%v", out, err)
			}
			if out.Success != dryRun || out.Admission.Serialized == dryRun || held || inventories != 1 {
				t.Fatalf("incorrect preview/ownership result: %+v held=%t inventories=%d", out, held, inventories)
			}
			wantLocks := 1
			if dryRun {
				wantLocks = 0
			}
			if acquisitions != wantLocks || releases != wantLocks {
				t.Fatalf("ownership %d/%d, want %d", acquisitions, releases, wantLocks)
			}
		})
	}
}
