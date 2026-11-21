package robot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestCollectSpawnAdmissionCountsCanonicalTypesAcrossSessions(t *testing.T) {
	cfg := &config.Config{SpawnPacing: config.DefaultSpawnPacingConfig()}
	cfg.SpawnPacing.AgentTypeLimits = map[string]int{"claude": 2, "codex": 4}
	opts := SpawnOptions{Session: "target", CCCount: 1, CodCount: 1}
	panes := func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{
			"other":  {{Type: tmux.AgentType("cc")}, {Type: tmux.AgentType("claude")}, {Type: tmux.AgentUser}},
			"target": {{Type: tmux.AgentType("cod")}, {Type: tmux.AgentType("omp")}, {Type: ""}},
		}, nil
	}
	in := collectSpawnAdmissionInputWithPanes(context.Background(), opts, cfg, 2, 3, panes)
	if !reflect.DeepEqual(in.RunningByType, map[string]int{"claude": 2, "codex": 1, "omp": 1}) || in.RunningAgents != 4 || in.CurrentPanes != 6 || in.SessionPanes != 3 || in.RunningSessions != 2 {
		t.Fatalf("fleet/type counts disagree: %+v", in)
	}
	decision := pressure.EvaluateSpawnAdmission(in)
	if decision.Reason != "agent_type_limit_exceeded" || decision.Decision != pressure.SpawnAdmissionRefuse {
		t.Fatalf("existing Claude agents in another session did not consume capacity: %+v", decision)
	}
	cfg.SpawnPacing.AgentTypeLimits["claude"] = 100
	if in.MaxAgentsByType["claude"] != 2 {
		t.Fatal("admission aliases mutable configuration")
	}
	cfg.SpawnPacing.Enabled = false
	in = collectSpawnAdmissionInputWithPanes(context.Background(), opts, cfg, 2, 3, panes)
	if in.MaxAgents != 0 || len(in.MaxAgentsByType) != 0 || pressure.EvaluateSpawnAdmission(in).Decision != pressure.SpawnAdmissionAdmit {
		t.Fatal("disabled governor still enforces type limits")
	}
}

func TestGetSpawnAdmissionTypeLimitsAndInventoryFailureHaveNoLaunchEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, scenario := range []string{"type limit", "inventory failure", "checked empty"} {
		for _, preview := range []bool{false, true} {
			if scenario == "checked empty" && !preview {
				continue
			}
			t.Run(scenario+map[bool]string{false: "/execute", true: "/preview"}[preview], func(t *testing.T) {
				cfg := config.Default()
				cfg.SpawnPacing = config.DefaultSpawnPacingConfig()
				cfg.SpawnPacing.AgentTypeLimits = map[string]int{"claude": 2}
				effects := 0
				effect := func() error { effects++; return errors.New("unexpected mutating lifecycle call") }
				opts := SpawnOptions{Session: "fleet-cap-test", CCCount: 1, WorkingDir: t.TempDir(), DryRun: preview,
					LifecycleDeps: &SpawnLifecycleDependencies{
						IsTMUXInstalled: func() bool { return true },
						SessionExists:   func(context.Context, string) (bool, error) { return false, nil },
						GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
							if scenario == "checked empty" {
								return nil, nil
							}
							fleet := map[string][]tmux.Pane{"other-project": {{Type: tmux.AgentClaude}, {Type: tmux.AgentClaude}}}
							if scenario == "inventory failure" {
								return fleet, errors.New("cannot enumerate entire fleet")
							}
							return fleet, nil
						},
						CreateSession:    func(context.Context, string, string, int) error { return effect() },
						SplitWindow:      func(context.Context, string, string) (string, error) { return "", effect() },
						ApplyTiledLayout: func(context.Context, string) error { return effect() },
						LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (SpawnedAgent, error) {
							return SpawnedAgent{}, effect()
						},
					},
				}
				out, err := GetSpawn(context.Background(), opts, cfg)
				if err != nil || out == nil || out.Admission == nil {
					t.Fatalf("spawn did not reach admission: %+v %v", out, err)
				}
				wantReason := "agent_type_limit_exceeded"
				if scenario == "inventory failure" {
					wantReason = "agent_inventory_unavailable"
				}
				if scenario == "checked empty" {
					wantReason = "headroom_available"
				}
				if out.Admission.Reason != wantReason || effects != 0 {
					t.Fatalf("wrong admission or launch effects: %+v effects=%d", out, effects)
				}
				if preview {
					if !out.Success || !out.DryRun || len(out.WouldCreate) != 2 {
						t.Fatalf("preview contract lost: %+v", out)
					}
				} else if out.Success || out.ErrorCode != ErrCodeResourceBusy || len(out.Agents) != 0 {
					t.Fatalf("refused fleet reported successful startup: %+v", out)
				}
			})
		}
	}
}

func TestSpawnAdmissionSharedBudgetCannotOverflow(t *testing.T) {
	maxValue := int(^uint(0) >> 1)
	cfg := &config.Config{SpawnPacing: config.SpawnPacingConfig{AgentCaps: config.AgentPacingConfig{
		ClaudeMaxConcurrent: maxValue, CodexMaxConcurrent: maxValue,
	}}}
	if got := spawnAdmissionAgentLimit(cfg); got != maxValue {
		t.Fatalf("shared cap wrapped: %d", got)
	}
}

func TestGetSpawnRejectsCountOverflowBeforeAnyDependency(t *testing.T) {
	maxValue := int(^uint(0) >> 1)
	for _, counts := range []SpawnOptions{
		{CCCount: maxValue, CodCount: 1, NoUserPane: true},
		{CCCount: maxValue, CodCount: maxValue, GmiCount: 3}, // Wraps to +1 without checked addition.
		{CCCount: maxValue}, // User-pane addition would overflow.
	} {
		for _, preview := range []bool{false, true} {
			opts := counts
			opts.Session, opts.DryRun = "invalid-counts", preview
			calls := 0
			opts.LifecycleDeps = &SpawnLifecycleDependencies{IsTMUXInstalled: func() bool { calls++; return false }}
			out, err := GetSpawn(context.Background(), opts, nil)
			if err != nil || out == nil || out.Success || out.ErrorCode != ErrCodeInvalidFlag || calls != 0 || out.Admission != nil {
				t.Fatalf("oversized request reached dependencies or preview allocation: %+v %v calls=%d", out, err, calls)
			}
		}
	}
}

func TestSpawnAdmissionAgentTypeAliasesConsumeConfiguredLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind    string
		aliases []tmux.AgentType
		opts    SpawnOptions
	}{
		{"claude", []tmux.AgentType{tmux.AgentClaude, "claude", "claude-code"}, SpawnOptions{CCCount: 1}},
		{"codex", []tmux.AgentType{tmux.AgentCodex, "codex", "openai-codex"}, SpawnOptions{CodCount: 1}},
		{"gemini", []tmux.AgentType{tmux.AgentGemini, "gemini", "google-gemini"}, SpawnOptions{GmiCount: 1}},
		{"antigravity", []tmux.AgentType{tmux.AgentAntigravity, "antigravity", "google-antigravity"}, SpawnOptions{AgyCount: 1}},
		{"grok", []tmux.AgentType{tmux.AgentGrok, "grok-build"}, SpawnOptions{GrokCount: 1}},
		{"omp", []tmux.AgentType{tmux.AgentOmp, "oh-my-pi"}, SpawnOptions{OmpCount: 1}},
		{"opencode", []tmux.AgentType{tmux.AgentOpencode, "opencode"}, SpawnOptions{OcCount: 1}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			cfg := &config.Config{SpawnPacing: config.DefaultSpawnPacingConfig()}
			cfg.SpawnPacing.AgentTypeLimits = map[string]int{tc.kind: len(tc.aliases)}
			panes := func(context.Context) (map[string][]tmux.Pane, error) {
				fleet := make([]tmux.Pane, len(tc.aliases))
				for i, alias := range tc.aliases {
					fleet[i].Type = alias
				}
				return map[string][]tmux.Pane{"existing": fleet}, nil
			}
			in := collectSpawnAdmissionInputWithPanes(context.Background(), tc.opts, cfg, 1, 2, panes)
			if !reflect.DeepEqual(in.RunningByType, map[string]int{tc.kind: len(tc.aliases)}) || in.RequestedByType[tc.kind] != 1 {
				t.Fatalf("aliases split fleet/request identities: %+v", in)
			}
			out := pressure.EvaluateSpawnAdmission(in)
			if out.Decision != pressure.SpawnAdmissionRefuse || out.Reason != "agent_type_limit_exceeded" || len(out.AgentTypeLimits) != 1 || !out.AgentTypeLimits[0].BlocksRequest {
				t.Fatalf("aliases bypassed the configured %s limit: %+v", tc.kind, out)
			}
		})
	}
}
