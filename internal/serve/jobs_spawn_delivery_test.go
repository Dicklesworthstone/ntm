package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/recovery"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestJobSwarmSpawnDeliveryOptions(t *testing.T) {
	const prompt = "  Review the design.\nThen implement it — preserve this text.\n"
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		mode   string
		delay  time.Duration
	}{
		{"prompt", nil, "", 0},
		{"assignment_prefix", map[string]interface{}{"assign_work": true}, "", 0},
		{"preview", map[string]interface{}{"dry_run": true}, "", 0},
		{"fixed_default", map[string]interface{}{"stagger_mode": "fixed"}, "fixed", config.DefaultSpawnStaggerDelay},
		{"fixed_explicit", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "1250ms"}, "fixed", 1250 * time.Millisecond},
		{"fixed_zero", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "0s"}, "fixed", 0},
		{"fixed_maximum", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "5m"}, "fixed", config.MaxSpawnStaggerDelay},
		{"smart", map[string]interface{}{"stagger_mode": "smart"}, "smart", 0},
		{"explicit_none", map[string]interface{}{"stagger_mode": "none"}, "none", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": prompt}
			for key, value := range tc.params {
				params[key] = value
			}
			calls := 0
			srv := &Server{spawnAgents: func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				if opts.Prompt != prompt || opts.StaggerMode != tc.mode || opts.StaggerDelay != tc.delay {
					t.Errorf("delivery = (%q, %q, %s), want (%q, %q, %s)", opts.Prompt, opts.StaggerMode, opts.StaggerDelay, prompt, tc.mode, tc.delay)
				}
				if opts.AssignWork != (params["assign_work"] == true) || opts.DryRun != (params["dry_run"] == true) {
					t.Error("delivery changed assignment or preview semantics")
				}
				return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}, Session: opts.Session}, nil
			}}
			if _, err := srv.jobSwarmSpawn(context.Background(), params); err != nil || calls != 1 {
				t.Fatalf("jobSwarmSpawn: calls=%d, err=%v", calls, err)
			}
		})
	}
}

func TestJobSwarmSpawnRejectsInvalidDeliveryBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		want   string
	}{
		{"unknown_mode", map[string]interface{}{"stagger_mode": "slow"}, "stagger_mode"},
		{"negative_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "-1s"}, "stagger_delay"},
		{"excessive_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "5m1ns"}, "stagger_delay"},
		{"overflow_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": "999999999999999999999h"}, "stagger_delay"},
		{"empty_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": ""}, "stagger_delay"},
		{"numeric_delay", map[string]interface{}{"stagger_mode": "fixed", "stagger_delay": 20}, "stagger_delay"},
		{"missing_mode", map[string]interface{}{"stagger_delay": "20s"}, "requires stagger_mode=fixed"},
		{"smart_fixed_delay", map[string]interface{}{"stagger_mode": "smart", "stagger_delay": "20s"}, "requires stagger_mode=fixed"},
		{"missing_work", map[string]interface{}{"stagger_mode": "fixed", "prompt": " \n"}, "requires a non-blank prompt"},
		{"numeric_prompt", map[string]interface{}{"prompt": 42}, "prompt"},
		{"unknown_prompt_option", map[string]interface{}{"prompt_file": "/tmp/private"}, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": "Do the work"}
			for key, value := range tc.params {
				params[key] = value
			}
			calls := 0
			srv := &Server{spawnAgents: func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls++
				return nil, errors.New("must not launch")
			}}
			_, err := srv.jobSwarmSpawn(context.Background(), params)
			if calls != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("calls=%d, err=%v, want preflight error containing %q", calls, err, tc.want)
			}
		})
	}
}

func TestJobSwarmSpawnAssignmentCanStaggerWithoutInitialPrompt(t *testing.T) {
	srv := &Server{spawnAgents: func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		if opts.Prompt != "" || !opts.AssignWork || opts.StaggerMode != "smart" {
			t.Errorf("assignment delivery options were lost: %+v", opts)
		}
		return &robot.SpawnOutput{RobotResponse: robot.RobotResponse{Success: true}}, nil
	}}
	_, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
		"session": "delivery", "cc_count": 1, "assign_work": true, "stagger_mode": "smart",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobSwarmSpawnDeliveryRetainsPartialFailure(t *testing.T) {
	failure := errors.New("second pane refused prompt delivery")
	srv := &Server{spawnAgents: func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return &robot.SpawnOutput{
			RobotResponse: robot.RobotResponse{Success: false}, Session: opts.Session,
			Agents: []robot.SpawnedAgent{{Pane: "0.1", PaneID: "%17", Type: "cc"}},
		}, failure
	}}
	result, err := srv.jobSwarmSpawn(context.Background(), map[string]interface{}{
		"session": "delivery", "cc_count": 2, "prompt": "Do the work", "stagger_mode": "fixed",
	})
	if !errors.Is(err, failure) || result["session"] != "delivery" || result["success"] != false {
		t.Fatalf("lost partial failure: result=%v, err=%v", result, err)
	}
	agents, ok := result["agents"].([]interface{})
	if !ok || len(agents) != 1 || agents[0].(map[string]interface{})["pane_id"] != "%17" {
		t.Fatalf("lost created pane identity: %v", result)
	}
}

func TestJobSwarmSpawnDeliveryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := &Server{spawnAgents: func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		t.Error("cancelled job launched agents")
		return nil, nil
	}}
	_, err := srv.jobSwarmSpawn(ctx, map[string]interface{}{"session": "delivery", "cc_count": 1, "prompt": "Do the work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context cancellation", err)
	}
}

type spawnHandoffHTTPAttempt struct {
	pane    string
	message string
}

type spawnHandoffHTTPCapture struct {
	mu       sync.Mutex
	events   []string
	attempts []spawnHandoffHTTPAttempt
}

func (c *spawnHandoffHTTPCapture) record(event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *spawnHandoffHTTPCapture) snapshot() ([]string, []spawnHandoffHTTPAttempt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...), append([]spawnHandoffHTTPAttempt(nil), c.attempts...)
}

// Keep the real router, configured policy loader, GetSpawn, and final dispatch
// pipeline. Only tmux lifecycle/observation/actuation ports are substituted.
func configuredSpawnHandoffHTTPFixture(t *testing.T, recoveryPolicy string, failSecond bool) (*Server, *spawnHandoffHTTPCapture, string) {
	t.Helper()
	const session = "handoff-workers"
	project := t.TempDir()
	h := handoff.New(session).WithGoalAndNow("TARGET PROJECT migration", "Resume the interrupted migration")
	h.Findings = map[string]string{"details": strings.Repeat("界", 2000)}
	handoffPath, err := handoff.NewWriter(project).Write(h, "continuity")
	if err != nil {
		t.Fatal(err)
	}
	handoffPath, err = filepath.EvalSymlinks(handoffPath)
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "operator.toml")
	selectedConfig := "[spawn_pacing]\nenabled = false\n[agent_mail]\nenabled = false\nauto_register = false\n" + recoveryPolicy
	if err := os.WriteFile(selected, []byte(selectedConfig), 0600); err != nil {
		t.Fatal(err)
	}
	srv := NewHermeticServer("spawn-handoff")
	t.Cleanup(func() { srv.Stop() })
	srv.projectDir = project
	if err := srv.ConfigureSpawnPolicy(selected, true); err != nil {
		t.Fatal(err)
	}
	panes := []tmux.Pane{
		{ID: "%41", WindowIndex: 0, Index: 0, Title: session + "__user", Type: tmux.AgentUser},
		{ID: "%42", WindowIndex: 0, Index: 1, Title: session + "__cc_1", Type: tmux.AgentClaude},
		{ID: "%43", WindowIndex: 0, Index: 2, Title: session + "__cc_2", Type: tmux.AgentClaude},
	}
	capture := &spawnHandoffHTTPCapture{}
	configured := srv.spawnAgents
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		opts.LifecycleDeps = &robot.SpawnLifecycleDependencies{
			IsTMUXInstalled: func() bool { return true },
			GetAllPanes: func(context.Context) (map[string][]tmux.Pane, error) {
				return map[string][]tmux.Pane{}, nil
			},
			SessionExists: func(context.Context, string) (bool, error) { return true, nil },
			GetPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return append([]tmux.Pane(nil), panes...), nil
			},
			ApplyTiledLayout: func(context.Context, string) error { return nil },
			LaunchAgent: func(_ context.Context, pane tmux.Pane, _ string, kind string, _ int, _ string, _ string) (robot.SpawnedAgent, error) {
				capture.record("launch:" + pane.ID)
				return robot.SpawnedAgent{Pane: pane.Ref().Physical(), PaneID: pane.ID, Type: kind, Title: pane.Title}, nil
			},
			WaitForReady: func(_ context.Context, out *robot.SpawnOutput, _ time.Duration) error {
				capture.record("ready")
				for i := range out.Agents {
					out.Agents[i].Ready = true
				}
				return nil
			},
			StartSessionMonitor: func(context.Context, resilience.SpawnMonitorRequest) (*resilience.SpawnMonitorResult, error) {
				return &resilience.SpawnMonitorResult{}, nil
			},
			CollectPressure: deterministicTestPressure,
		}
		opts.AssignmentDeps = &robot.SpawnAssignmentDependencies{
			ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
				return append([]tmux.Pane(nil), panes...), nil
			},
			ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
				now := time.Now().UTC()
				observed := statuspkg.SessionObservation{Session: session, ObservedAt: now, Complete: true}
				for _, pane := range panes {
					observed.Panes = append(observed.Panes, statuspkg.PaneObservation{
						Pane: pane.Ref(), Metadata: pane,
						Current: statuspkg.StateObservation{
							Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, ObservedAt: now,
							Freshness: statuspkg.FreshnessFresh, Confidence: 1,
						},
					})
				}
				return observed, nil
			},
			DispatchDeliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				capture.mu.Lock()
				capture.events = append(capture.events, "deliver:"+delivery.Target.Ref.ID)
				capture.attempts = append(capture.attempts, spawnHandoffHTTPAttempt{pane: delivery.Target.Ref.ID, message: delivery.Message})
				capture.mu.Unlock()
				if failSecond && delivery.Target.Ref.ID == "%43" {
					return errors.New("second pane refused recovery delivery")
				}
				return nil
			}),
		}
		return configured(ctx, opts)
	}
	return srv, capture, handoffPath
}

func TestSpawnHandoffRecoveryThroughRESTAndJobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NTM_CONFIG", "")
	t.Setenv("NTM_RECOVERY_ENABLED", "")
	t.Setenv("NTM_RECOVERY_AUTO_INJECT", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".data"))
	const explicitPrompt = "Finish the newly requested task after recovery."
	for _, asJob := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			policy     string
			noRecovery bool
			partial    bool
			want       bool
		}{
			{name: "enabled", want: true},
			{name: "explicit suppression", noRecovery: true},
			{name: "global disabled", policy: "[recovery]\nenabled = false\n"},
			{name: "automatic injection disabled", policy: "[recovery]\nauto_inject_on_spawn = false\n"},
			{name: "partial delivery", partial: true, want: true},
		} {
			t.Run(fmt.Sprintf("%s/job=%t", tc.name, asJob), func(t *testing.T) {
				srv, capture, handoffPath := configuredSpawnHandoffHTTPFixture(t, tc.policy, tc.partial)
				params := map[string]interface{}{"cc_count": 2, "no_hooks": true, "no_recovery": tc.noRecovery}
				var raw []byte
				if asJob {
					params["session"] = "handoff-workers"
					params["prompt"] = explicitPrompt
					request, err := json.Marshal(map[string]interface{}{"type": "swarm_spawn", "params": params})
					if err != nil {
						t.Fatal(err)
					}
					queued := postJob(t, srv, string(request))
					final := pollJobTerminal(t, srv, queued.Job.ID)
					wantStatus := string(JobStatusCompleted)
					if tc.partial {
						wantStatus = string(JobStatusFailed)
					}
					if final.Job.Status != wantStatus {
						t.Fatalf("spawn job status=%s, want %s: %+v", final.Job.Status, wantStatus, final.Job)
					}
					raw, err = json.Marshal(final.Job.Result)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					request, err := json.Marshal(params)
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/handoff-workers/agents/spawn", bytes.NewReader(request))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					srv.Router().ServeHTTP(rec, req)
					raw = rec.Body.Bytes()
					if tc.partial {
						var failure struct {
							Success bool `json:"success"`
							Details struct {
								Spawn json.RawMessage `json:"spawn"`
							} `json:"details"`
						}
						if rec.Code != http.StatusInternalServerError || json.Unmarshal(raw, &failure) != nil || failure.Success || len(failure.Details.Spawn) == 0 {
							t.Fatalf("REST failure discarded its partial spawn: %d %s", rec.Code, raw)
						}
						raw = failure.Details.Spawn
					} else if rec.Code != http.StatusOK {
						t.Fatalf("REST spawn failed: %d %s", rec.Code, raw)
					}
				}
				var out robot.SpawnOutput
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatalf("decode spawn result: %v (%s)", err, raw)
				}
				t.Logf("spawn handoff result: %s", raw)
				if out.Success == tc.partial || out.Session != "handoff-workers" || out.WorkingDir != srv.projectDir || len(out.Agents) != 3 {
					t.Fatalf("spawn lost outcome or created identities: %+v", out)
				}
				events, attempts := capture.snapshot()
				wantEvents := []string{"launch:%42", "launch:%43"}
				if tc.want || asJob {
					wantEvents = append(wantEvents, "ready", "deliver:%42", "deliver:%43")
				}
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatalf("recovery escaped readiness or repeated delivery: events=%v, want %v", events, wantEvents)
				}
				if tc.want {
					wantDelivered := []string{"0.1", "0.2"}
					if tc.partial {
						wantDelivered = []string{"0.1"}
					}
					if out.Recovery == nil || out.Recovery.HandoffPath != handoffPath || !reflect.DeepEqual(out.Recovery.DeliveredTo, wantDelivered) ||
						out.Recovery.Warning != "" || len(out.Recovery.InjectedText) > recovery.MaxHandoffTokens*4 {
						t.Fatalf("recovery source/budget/delivery evidence lost: %+v", out.Recovery)
					}
					if len(attempts) != 2 || len(out.PromptDeliveries) != 2 || !out.PromptDeliveries[0].PromptSent || out.PromptDeliveries[1].PromptSent == tc.partial {
						t.Fatalf("partial prompt outcomes changed: attempts=%v receipts=%+v", attempts, out.PromptDeliveries)
					}
				} else if out.Recovery != nil {
					t.Fatalf("suppressed recovery was still prepared or delivered: %+v", out.Recovery)
				}
				for _, attempt := range attempts {
					if tc.want {
						if !strings.Contains(attempt.message, "TARGET PROJECT migration") || !strings.Contains(attempt.message, "Resume the interrupted migration") {
							t.Fatalf("%s received no handoff: %q", attempt.pane, attempt.message)
						}
						if asJob && (!strings.Contains(attempt.message, explicitPrompt) || strings.Index(attempt.message, "Resume the interrupted migration") > strings.Index(attempt.message, explicitPrompt)) {
							t.Fatalf("handoff did not precede the requested task in one delivery: %q", attempt.message)
						}
					} else if attempt.message != explicitPrompt {
						t.Fatalf("suppression changed the explicit prompt: %q", attempt.message)
					}
				}
			})
		}
	}
}
