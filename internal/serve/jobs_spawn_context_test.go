package serve

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestJobSwarmSpawnContextOptIn(t *testing.T) {
	for _, cass := range []bool{false, true} {
		for _, memory := range []bool{false, true} {
			calls := 0
			srv := &Server{spawnAgents: func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				want := robot.PromptContextOptions{WithCASS: cass, WithMemory: memory}
				if !reflect.DeepEqual(opts.PromptContext, want) {
					t.Errorf("context request = %+v, want %+v", opts.PromptContext, want)
				}
				return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
			}}
			_, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
				"session": "context", "cc_count": 1, "assign_work": true,
				"with_cass": cass, "with_memory": memory,
			})
			if err != nil || calls != 1 {
				t.Fatalf("cass=%t memory=%t calls=%d err=%v", cass, memory, calls, err)
			}
		}
	}
}

func TestJobSwarmSpawnRejectsIneffectiveOrUnsafeContextControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		want   string
	}{
		{"cass_without_assignment", map[string]interface{}{"with_cass": true}, "require assign_work=true"},
		{"memory_without_assignment", map[string]interface{}{"with_memory": true}, "require assign_work=true"},
		{"string_boolean", map[string]interface{}{"assign_work": true, "with_cass": "false"}, "with_cass"},
		{"client_binary", map[string]interface{}{"assign_work": true, "with_cass": true, "cass_binary": "/tmp/program"}, "unknown field"},
		{"client_workspace", map[string]interface{}{"assign_work": true, "with_memory": true, "memory_workspace": "/private"}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"session": "context", "cc_count": 1, "prompt": "Initial instructions"}
			for key, value := range tc.params {
				params[key] = value
			}
			calls := 0
			srv := &Server{spawnAgents: func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				return nil, nil
			}}
			_, err := srv.jobSwarmSpawn(context.Background(), params)
			if calls != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("calls=%d err=%v, want %q before launch", calls, err, tc.want)
			}
		})
	}
}

func TestJobSwarmSpawnContextUsesLaunchProjectPolicy(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "admitted")
	selected := filepath.Join(root, "operator.toml")
	srv := &Server{projectDir: filepath.Join(root, "different-current-project")}
	cfg := config.Default()
	cfg.CASS.Enabled = true
	cfg.CASS.BinaryPath = "/operator/bin/cass"
	cfg.CASS.Timeout = 7
	cfg.CASS.Context.MaxSessions = 3
	cfg.CASS.Context.LookbackDays = 14
	cfg.CASS.Context.MinRelevance = 0.8
	cfg.CASS.Context.PreferSameProject = true
	cfg.CASS.Context.MaxTokens = 1200
	cfg.CASS.Context.SkipIfContextAbove = 75
	cfg.Memory.Enabled = true
	cfg.Memory.SendMaxRules = 4
	cfg.Memory.SendBudgetTokens = 900
	cfg.Memory.QueryTimeoutSeconds = 6
	calls := 0
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return srv.spawnWithPolicy(ctx, opts, selected, true,
			func(dir, path string, required bool) (*config.Config, error) {
				if dir != project || path != selected || !required {
					t.Errorf("wrong policy source: %q %q %t", dir, path, required)
				}
				return cfg, nil
			},
			func(_ context.Context, opts robot.SpawnOptions, _ *config.Config) (*robot.SpawnOutput, error) {
				calls++
				pc := opts.PromptContext
				if !pc.WithCASS || !pc.WithMemory || pc.CASSConfig == nil || pc.FilterConfig == nil || pc.InjectConfig == nil || pc.MemoryInject == nil {
					t.Fatalf("missing effective context configuration: %+v", pc)
				}
				q, f, i, m := pc.CASSConfig, pc.FilterConfig, pc.InjectConfig, pc.MemoryInject
				if !q.Enabled || q.BinaryPath != cfg.CASS.BinaryPath || q.Timeout != 7*time.Second || q.MaxResults != 3 || q.MaxAgeDays != 14 || q.MinRelevance != 0.8 || !q.PreferSameProject {
					t.Errorf("CASS policy was lost: %+v", q)
				}
				if f.MaxItems != 3 || f.MaxAgeDays != 14 || f.MinRelevance != 0.8 || !f.PreferSameProject || i.MaxTokens != 1200 || i.SkipThreshold != 75 {
					t.Errorf("filter/injection policy was lost: %+v %+v", f, i)
				}
				if !m.Enabled || m.MaxRules != 4 || m.BudgetTokens != 900 || m.QueryTimeout != 6*time.Second {
					t.Errorf("memory policy was lost: %+v", m)
				}
				if opts.WorkingDir != project || f.CurrentWorkspace != "" || m.ProjectDir != "" || m.Workspace != "" || m.SessionID != "" {
					t.Error("context must use the engine's authoritative assignment scope, not another project")
				}
				return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
			})
	}
	_, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
		"session": "context", "cc_count": 1, "assign_work": true, "working_dir": project,
		"with_cass": true, "with_memory": true, "prompt": "Follow project conventions", "stagger_mode": "smart",
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestSpawnPromptContextPolicyCannotBeBypassedOrEnableUnrequestedTools(t *testing.T) {
	cfg := config.Default()
	cfg.CASS.Enabled, cfg.Memory.Enabled = false, false
	request := robot.PromptContextOptions{WithCASS: true, WithMemory: true}
	got := spawnPromptContextOptions(request, cfg)
	if !got.WithCASS || !got.WithMemory || got.CASSConfig.Enabled || got.MemoryInject.Enabled {
		t.Fatalf("disabled integrations must reach the engine as recorded skips: %+v", got)
	}
	if request.CASSConfig != nil || request.MemoryInject != nil {
		t.Fatal("caller-owned context request mutated")
	}
	cfg.CASS.Enabled, cfg.Memory.Enabled = true, true
	cfg.CASS.Context.Enabled, cfg.Memory.SendInjection = true, true
	for _, want := range []robot.PromptContextOptions{{}, {WithCASS: true}, {WithMemory: true}} {
		got := spawnPromptContextOptions(want, cfg)
		if got.WithCASS != want.WithCASS || got.WithMemory != want.WithMemory || (got.CASSConfig != nil) != want.WithCASS || (got.MemoryInject != nil) != want.WithMemory {
			t.Errorf("API enrichment must be explicit: want %+v, got %+v", want, got)
		}
	}
}

func TestSpawnPromptContextIndependentConcurrentPolicies(t *testing.T) {
	var wg sync.WaitGroup
	for _, limit := range []int{2, 9} {
		cfg := config.Default()
		cfg.CASS.Context.MaxSessions, cfg.Memory.SendMaxRules = limit, limit
		wg.Add(1)
		go func(cfg *config.Config, limit int) {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				got := spawnPromptContextOptions(robot.PromptContextOptions{WithCASS: true, WithMemory: true}, cfg)
				if got.CASSConfig.MaxResults != limit || got.MemoryInject.MaxRules != limit {
					t.Error("project context policy leaked across jobs")
				}
				got.CASSConfig.MaxResults, got.MemoryInject.MaxRules = 999, 999
				if cfg.CASS.Context.MaxSessions != limit || cfg.Memory.SendMaxRules != limit {
					t.Error("context options mutated loaded policy")
				}
			}
		}(cfg, limit)
	}
	wg.Wait()
}
