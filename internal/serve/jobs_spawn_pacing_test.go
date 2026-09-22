package serve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the HTTP job surface, not just the duration parser. The worker must
// carry the actual pacing policy to the shared spawn service and keep previews
// and partial failures observable without waiting for the configured interval.
func TestSwarmJobLaunchPacingHTTP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval string
		dryRun   bool
		fail     bool
	}{
		{name: "paced preview", interval: "1h", dryRun: true},
		{name: "paced failure", interval: "2s", fail: true},
		{name: "explicit zero", interval: "0s"},
		{name: "omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			options := make(chan robot.SpawnOptions, 1)
			srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				options <- opts
				out := &robot.SpawnOutput{
					Session: opts.Session, DryRun: opts.DryRun,
					Agents: []robot.SpawnedAgent{{Pane: "%42", Type: "claude"}},
				}
				out.Success = !tc.fail
				if tc.fail {
					return out, errors.New("second launch failed")
				}
				return out, nil
			}
			extra := ""
			if tc.interval != "" {
				extra = fmt.Sprintf(`,"launch_interval":%q`, tc.interval)
			}
			env := postJob(t, srv, fmt.Sprintf(`{"type":"swarm_spawn","params":{"session":"recoverable","cc_count":2,"dry_run":%t%s}}`, tc.dryRun, extra))
			final := pollJobTerminal(t, srv, env.Job.ID)
			wantStatus := JobStatusCompleted
			if tc.fail {
				wantStatus = JobStatusFailed
				if !strings.Contains(final.Job.Error, "second launch failed") {
					t.Fatalf("lost launch error: %+v", final.Job)
				}
				assertSpawnJobRecovery(t, final)
			}
			if final.Job.Status != string(wantStatus) {
				t.Fatalf("paced job status = %s, want %s: %+v", final.Job.Status, wantStatus, final.Job)
			}
			if tc.interval == "" {
				if _, exists := final.Job.Result["launch_interval"]; exists {
					t.Fatal("unrequested pacing appeared in the result")
				}
			} else {
				wantInterval := tc.interval
				if wantInterval == "1h" {
					wantInterval = "1h0m0s"
				}
				if final.Job.Result["launch_interval"] != wantInterval {
					t.Fatalf("lost effective pacing: %+v", final.Job.Result)
				}
			}
			select {
			case opts := <-options:
				paced := tc.interval != "" && tc.interval != "0s"
				if (opts.LifecycleDeps != nil) != paced || opts.DryRun != tc.dryRun || opts.CCCount != 2 {
					t.Fatalf("pacing or preview controls did not reach the service: %+v", opts)
				}
				if paced {
					// Calling the installed policy with a cancelled context must
					// never reach its production tmux launcher.
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					_, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{}, opts.Session, "claude", 1, "", "")
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("installed pacing policy ignored cancellation: %v", err)
					}
				}
			default:
				t.Fatal("job finished without calling the spawn service")
			}
		})
	}
}

func TestSwarmJobLaunchPacingRejectsInvalidInputHTTP(t *testing.T) {
	for _, value := range []string{`"-1s"`, `"invalid"`, `"999999999999999999h"`, `10`, `true`, `{}`} {
		t.Run(value, func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			called := make(chan struct{}, 1)
			srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				called <- struct{}{}
				return nil, errors.New("unexpected mutation")
			}
			env := postJob(t, srv, fmt.Sprintf(`{"type":"swarm_spawn","params":{"session":"guarded","cc_count":1,"launch_interval":%s}}`, value))
			final := pollJobTerminal(t, srv, env.Job.ID)
			if final.Job.Status != string(JobStatusFailed) || !strings.Contains(final.Job.Error, "launch_interval") {
				t.Fatalf("invalid pacing was not diagnosed: %+v", final.Job)
			}
			select {
			case <-called:
				t.Fatal("invalid pacing reached the mutating spawn service")
			default:
			}
		})
	}
}
