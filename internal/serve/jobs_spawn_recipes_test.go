package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestSwarmJobRecipePreviewThroughSharedEngine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".ntm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".ntm", "recipes.toml"), []byte(`[[recipes]]
name = "minimal"
[[recipes.agents]]
type = "cc"
count = 3
model = "http-project-model"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("recipe-test")
	defer srv.Stop()
	srv.projectDir = project
	cfg := config.Default()
	cfg.SpawnPacing.Enabled = false
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		// Keep the production recipe/preview/admission path. Only the tmux
		// installation and fleet observation ports are replaced in this test.
		opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled: func() bool { return true },
			GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
				return map[string][]tmux.Pane{}, nil
			},
			CreateSession: func(context.Context, string, string, int) error {
				t.Error("recipe preview created a session")
				return nil
			},
		}
		return robot.GetSpawn(ctx, opts, cfg)
	}
	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"recipe-http","preset":"minimal","dry_run":true}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)
	if final.Job.Status != string(JobStatusCompleted) || final.Job.Result["preset_used"] != "minimal" || final.Job.Result["working_dir"] != project {
		t.Fatalf("preset-only HTTP job failed or lost the admitted project: %+v", final.Job)
	}
	agents, ok := final.Job.Result["would_create"].([]interface{})
	if !ok || len(agents) != 4 {
		t.Fatalf("HTTP preview did not expand the project's recipe: %+v", final.Job.Result)
	}
	if agents[1].(map[string]interface{})["variant"] != "http-project-model" {
		t.Fatalf("HTTP preview lost the recipe model: %+v", agents)
	}
}

func TestConfiguredSwarmJobUsesSelectedLaunchCommand(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The selected config leaves Agent Mail registration on (the default),
	// so the shared spawn engine provisions pane identities: keep it off any
	// real Agent Mail server.
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	selected := filepath.Join(t.TempDir(), "operator.toml")
	if err := os.WriteFile(selected, []byte("[spawn_pacing]\nenabled = false\n[agents]\nclaude = 'operator-claude --model {{shellQuote .Model}}'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("spawn-policy-command")
	defer srv.Stop()
	srv.projectDir = t.TempDir()
	if err := srv.ConfigureSpawnPolicy(selected, true); err != nil {
		t.Fatal(err)
	}
	// Changing environment selection must not redirect this server's policy.
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "not-selected.toml"))
	commands := make(chan string, 1)
	configured := srv.spawnAgents
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled: func() bool { return true },
			GetAllPanes:     func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil },
			SessionExists:   func(context.Context, string) (bool, error) { return true, nil },
			GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return []tmux.Pane{{ID: "%8", WindowIndex: 0, Index: 0}}, nil
			},
			ApplyTiledLayout: func(context.Context, string) error { return nil },
			LaunchAgent: func(_ context.Context, _ tmux.Pane, _ string, kind string, _ int, _ string, command string) (robot.SpawnedAgent, error) {
				commands <- command
				return robot.SpawnedAgent{Pane: "0.0", Type: kind}, nil
			},
			StartSessionMonitor: func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
				return &resilience.SpawnMonitorResult{}, nil
			},
		}
		return configured(ctx, opts)
	}
	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"policy-command","cc_count":1,"cc_model":"operator/model","no_user_pane":true}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)
	if final.Job.Status != string(JobStatusCompleted) {
		t.Fatalf("configured launch failed: %+v", final.Job)
	}
	select {
	case command := <-commands:
		// The configured wrapper is used and carries ntm's Claude hooks.
		if !strings.HasPrefix(command, "operator-claude --model 'operator/model' --settings ") || !strings.Contains(command, "safety claude-hook") {
			t.Fatalf("selected launch command ignored: %q", command)
		}
	default:
		t.Fatal("no agent was launched")
	}
}

// TestRESTSpawnProvisionsAgentMailIdentity: the synchronous REST spawn
// endpoint reaches the shared robot spawn engine, so each launched pane gets
// its Agent Mail identity before the launch port runs and the response carries
// the same agent_mail status object as `ntm spawn --json`. Real strict config
// loading and the real router; only tmux ports and Agent Mail are stubbed.
func TestRESTSpawnProvisionsAgentMailIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".data"))

	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result interface{}
		switch req.Params.Name {
		case "ensure_project":
			result = map[string]interface{}{"id": 3, "slug": "proj", "human_key": req.Params.Arguments["project_key"]}
		case "create_agent_identity":
			result = map[string]interface{}{"id": 9, "name": "AmberHeron", "program": req.Params.Arguments["program"], "model": req.Params.Arguments["model"]}
		case "register_agent":
			// The session-level coordinator identity `ntm lock` acts as.
			result = map[string]interface{}{"id": 10, "name": "SilverFox", "program": req.Params.Arguments["program"], "model": req.Params.Arguments["model"]}
		default:
			t.Errorf("unexpected Agent Mail tool %q", req.Params.Name)
		}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(raw)})
	}))
	t.Cleanup(mail.Close)
	t.Setenv("AGENT_MAIL_URL", mail.URL+"/")

	selected := filepath.Join(t.TempDir(), "operator.toml")
	if err := os.WriteFile(selected, []byte("[spawn_pacing]\nenabled = false\n[agent_mail]\nenabled = true\nauto_register = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	srv := NewHermeticServer("spawn-identity")
	defer srv.Stop()
	srv.projectDir = project
	if err := srv.ConfigureSpawnPolicy(selected, true); err != nil {
		t.Fatal(err)
	}
	identityAtLaunch := ""
	configured := srv.spawnAgents
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled: func() bool { return true },
			GetAllPanes:     func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil },
			SessionExists:   func(context.Context, string) (bool, error) { return true, nil },
			GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return []tmux.Pane{{ID: "%4", Index: 0, PID: 40}, {ID: "%5", Index: 1, PID: 50}}, nil
			},
			ApplyTiledLayout: func(context.Context, string) error { return nil },
			LaunchAgent: func(_ context.Context, pane tmux.Pane, _ string, kind string, _ int, _ string, _ string) (robot.SpawnedAgent, error) {
				identityAtLaunch, _ = agentmail.ResolveIdentity(project, pane.ID)
				return robot.SpawnedAgent{Pane: "0.1", Type: kind}, nil
			},
			StartSessionMonitor: func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
				return &resilience.SpawnMonitorResult{}, nil
			},
		}
		return configured(ctx, opts)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/identity-workers/agents/spawn", strings.NewReader(`{"cc_count":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	var body struct {
		Success   bool `json:"success"`
		AgentMail *struct {
			Available        bool              `json:"available"`
			AgentsRegistered int               `json:"agents_registered"`
			AgentMap         map[string]string `json:"agent_map"`
			SessionAgent     string            `json:"session_agent"`
		} `json:"agent_mail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Success {
		t.Fatalf("REST spawn = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	if identityAtLaunch != "AmberHeron" {
		t.Fatalf("identity resolvable at launch = %q, want AmberHeron (published before the agent command)", identityAtLaunch)
	}
	if body.AgentMail == nil || !body.AgentMail.Available || body.AgentMail.AgentsRegistered != 1 || body.AgentMail.AgentMap["%5"] != "AmberHeron" {
		t.Fatalf("REST spawn agent_mail = %+v, want AmberHeron registered for %%5: %s", body.AgentMail, rec.Body.String())
	}
	// A REST-spawned session gets the session-level identity too, so
	// `ntm lock` works in it.
	if body.AgentMail.SessionAgent != "SilverFox" {
		t.Fatalf("REST spawn agent_mail.session_agent = %q, want SilverFox: %s", body.AgentMail.SessionAgent, rec.Body.String())
	}
	if saved, err := agentmail.LoadSessionAgent("identity-workers", project); err != nil || saved == nil || saved.AgentName != "SilverFox" {
		t.Fatalf("session agent.json = %+v (err %v), want SilverFox persisted", saved, err)
	}
}

// These tests use real strict config loading and the real served surfaces.
// Only tmux ports are replaced; no caller injects the effective configuration.
func TestConfiguredSpawnPolicyFailureThroughBothHTTPPaths(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, kind := range []string{"missing-global", "invalid-project"} {
		t.Run(kind, func(t *testing.T) {
			project := t.TempDir()
			selected := filepath.Join(t.TempDir(), "selected.toml")
			if kind == "invalid-project" {
				if err := os.WriteFile(selected, []byte("[spawn_pacing]\nenabled = false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(project, ".ntm"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(project, ".ntm", "config.toml"), []byte("[invalid TOML"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := NewHermeticServer("spawn-policy")
			defer srv.Stop()
			srv.projectDir = project
			if err := srv.ConfigureSpawnPolicy(selected, true); err != nil {
				t.Fatal(err)
			}
			configured := srv.spawnAgents
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
					IsTMUXInstalled: func() bool { t.Error("invalid policy reached tmux discovery"); return false },
				}
				return configured(ctx, opts)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/policy-workers/agents/spawn", strings.NewReader(`{"cc_count":1}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			var output struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &output); err != nil || output.Success || !strings.Contains(output.Error, "load spawn policy") {
				t.Fatalf("synchronous policy refusal = %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"policy-workers","cc_count":1}}`)
			final := pollJobTerminal(t, srv, env.Job.ID)
			if final.Job.Status != string(JobStatusFailed) || !strings.Contains(final.Job.Error, "load spawn policy") || final.Job.Result["working_dir"] != project {
				t.Fatalf("queued policy refusal lost failure or project: %+v", final.Job)
			}
		})
	}
}

func TestConfiguredSwarmJobEnforcesFleetLimitWithoutAssignment(t *testing.T) {
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	project := t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected.toml")
	if err := os.WriteFile(selected, []byte("[spawn_pacing]\nenabled = true\n[spawn_pacing.agent_type_limits]\nclaude = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("spawn-policy-budget")
	defer srv.Stop()
	srv.projectDir = project
	if err := srv.ConfigureSpawnPolicy(selected, true); err != nil {
		t.Fatal(err)
	}
	configured := srv.spawnAgents
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled:  func() bool { return true },
			AcquireAdmission: func(context.Context) (func(), error) { return func() {}, nil },
			GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
				return map[string][]tmux.Pane{"existing": {{ID: "%9", Type: tmux.AgentClaude}}}, nil
			},
			SessionExists: func(context.Context, string) (bool, error) {
				t.Error("over-cap API spawn reached session creation preflight")
				return false, errors.New("unexpected lifecycle access")
			},
		}
		return configured(ctx, opts)
	}
	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"policy-budget","cc_count":1}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)
	admission, ok := final.Job.Result["admission"].(map[string]interface{})
	if final.Job.Status != string(JobStatusFailed) || !ok || admission["reason"] != "agent_type_limit_exceeded" || admission["serialized"] != true {
		t.Fatalf("configured fleet cap was bypassed by ordinary HTTP spawn: %+v", final.Job)
	}
}

func TestAgentSpawnRESTAndSwarmJobForwardORIOptions(t *testing.T) {
	srv := NewHermeticServer("spawn-ori-parity")
	defer srv.Stop()

	var seen []robot.SpawnOptions
	srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		seen = append(seen, opts)
		return &robot.SpawnOutput{
			RobotResponse: robot.NewRobotResponse(true),
			Session:       opts.Session,
			ProfileSet:    opts.ProfileSet,
			Agents:        []robot.SpawnedAgent{},
		}, nil
	}

	// 1. Synchronous REST POST /api/v1/sessions/{id}/agents/spawn accepts pure personas + worktrees + no_hooks.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/ori-rest/agents/spawn",
		strings.NewReader(`{"personas":["reviewer:2"],"worktrees":true,"worktree_name":"wt-custom","no_hooks":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("REST spawn status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 2. Async swarm_spawn job accepts pure profile_set + worktrees + no_hooks.
	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"ori-job","profile_set":"review-team","worktrees":true,"no_hooks":true}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)
	if final.Job.Status != string(JobStatusCompleted) {
		t.Fatalf("swarm_spawn job failed: %+v", final.Job)
	}

	if len(seen) != 2 {
		t.Fatalf("expected 2 spawn calls, got %d", len(seen))
	}
	if len(seen[0].Personas) != 1 || seen[0].Personas[0] != "reviewer:2" || !seen[0].UseWorktrees || seen[0].WorktreeName != "wt-custom" || !seen[0].NoHooks {
		t.Fatalf("REST spawn did not forward ORI options: %+v", seen[0])
	}
	if seen[1].ProfileSet != "review-team" || !seen[1].UseWorktrees || !seen[1].NoHooks {
		t.Fatalf("swarm_spawn job did not forward ORI options: %+v", seen[1])
	}
}
