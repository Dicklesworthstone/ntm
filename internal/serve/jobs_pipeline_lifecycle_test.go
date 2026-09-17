package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestExecutePipelineJobOwnsBackgroundLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	defer close(release)
	go func() {
		_, err := executePipelineJob(ctx, pipeline.PipelineRunOptions{Background: true}, func(got context.Context, opts pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
			if opts.Background {
				return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewRobotResponse(true), Status: "running"}
			}
			if got != ctx {
				return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewErrorResponse(errors.New("lost owning context"), "TEST", "")}
			}
			close(started)
			select {
			case <-got.Done():
			case <-release:
			}
			// Even a badly behaved operation claiming success after cancellation
			// must not produce a completed job.
			return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewRobotResponse(true), Status: "completed", RunID: "owned-run"}
		})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("job detached or returned before work: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("job returned while pipeline still running: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not propagate cancellation")
	}
}

func TestExecutePipelineJobTerminalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status                      string
		dry, reportedDry, success, wantOK bool
	}{
		{"completed", "completed", false, false, true, true},
		{"validated dry run", "validated", true, true, true, true},
		{"launch is not completion", "running", false, false, true, false},
		{"started is not completion", "started", false, false, true, false},
		{"paused is not completion", "paused", false, false, true, false},
		{"failed status overrides success", "failed", false, false, true, false},
		{"failure flag overrides status", "completed", false, false, false, false},
		{"validation is not execution", "validated", false, true, true, false},
		{"dry run must not execute", "completed", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := executePipelineJob(context.Background(), pipeline.PipelineRunOptions{Session: "owned", DryRun: tc.dry}, func(context.Context, pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
				return pipeline.PipelineRunOutput{
					RobotResponse: pipeline.NewRobotResponse(tc.success), RunID: "run-123",
					WorkflowID: "workflow", Status: tc.status, DryRun: tc.reportedDry,
				}
			})
			if (err == nil) != tc.wantOK {
				t.Fatalf("error = %v, want success=%v", err, tc.wantOK)
			}
			if out["run_id"] != "run-123" || out["session"] != "owned" || out["status"] != tc.status {
				t.Fatalf("lost inspectable execution identity: %#v", out)
			}
		})
	}
}

func TestExecutePipelineJobPrecancelDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := executePipelineJob(ctx, pipeline.PipelineRunOptions{}, func(context.Context, pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
		t.Fatal("cancelled job entered executor")
		return pipeline.PipelineRunOutput{}
	})
	if !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("out=%v error=%v", out, err)
	}
}

func TestExecutePipelineJobCancellationRetainsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := executePipelineJob(ctx, pipeline.PipelineRunOptions{}, func(context.Context, pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
		cancel()
		return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewRobotResponse(true), RunID: "partial", Status: "completed"}
	})
	if !errors.Is(err, context.Canceled) || out["run_id"] != "partial" {
		t.Fatalf("cancelled execution lost run ID or error: %#v, %v", out, err)
	}
}

// These tests enter through the real jobs surface and run a command workflow.
// File rendezvous keeps the pipeline live until the test explicitly releases it.
func TestJobDispatchBackgroundPipelineLifetime(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			dir := t.TempDir()
			srv.mu.Lock()
			srv.projectDir = dir
			srv.mu.Unlock()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			// Release on any failure too, so this test cannot leave an orphan.
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			last := "exit 0"
			if fail {
				last = "exit 9"
			}
			workflow := fmt.Sprintf("schema_version: \"2.0\"\nname: owned-job\nsteps:\n  - id: gate\n    command: 'echo started > %q; while [ ! -f %q ]; do sleep 0.01; done; %s'\n", started, release, last)
			if err := os.WriteFile(filepath.Join(dir, "owned.yaml"), []byte(workflow), 0600); err != nil {
				t.Fatal(err)
			}
			env := postJob(t, srv, `{"type":"pipeline_run","params":{"workflow_file":"owned.yaml","session":"ownedjob","background":true}}`)
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("pipeline did not reach command rendezvous")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if got := srv.jobStore.Get(env.Job.ID); got == nil || got.Status != JobStatusRunning {
				t.Fatalf("job completed while its pipeline remained blocked: %+v", got)
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			final := pollJobTerminal(t, srv, env.Job.ID)
			want := JobStatusCompleted
			if fail {
				want = JobStatusFailed
			}
			runID, _ := final.Job.Result["run_id"].(string)
			if JobStatus(final.Job.Status) != want || runID == "" {
				t.Fatalf("incorrect terminal outcome: %+v", final.Job)
			}
		})
	}
}

func TestJobDispatchCancelledBeforeRegistration(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.jobStore.Update(job.ID, JobStatusCancelled, 0, nil, "cancelled by user")
	srv.jobStore.Cancel(job.ID) // No cancel func is registered yet.
	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		t.Fatal("cancelled job attempted to spawn agents")
		return nil, nil
	}
	srv.dispatchJob(job.ID, CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{"session": "cancelledjob", "cc_count": 1}})
	if got := srv.jobStore.Get(job.ID); got.Status != JobStatusCancelled || got.Error != "cancelled by user" {
		t.Fatalf("cancelled job was changed: %+v", got)
	}
}

func TestJobDispatchBackgroundPipelineCancellation(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()
	dir := t.TempDir()
	srv.mu.Lock()
	srv.projectDir = dir
	srv.mu.Unlock()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	workflow := fmt.Sprintf("schema_version: \"2.0\"\nname: cancelled-job\nsteps:\n  - id: gate\n    command: 'echo started > %q; while [ ! -f %q ]; do sleep 0.01; done'\n", started, release)
	if err := os.WriteFile(filepath.Join(dir, "cancel.yaml"), []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	env := postJob(t, srv, `{"type":"pipeline_run","params":{"workflow_file":"cancel.yaml","session":"canceljob","background":true}}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pipeline did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+env.Job.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel failed: %d %s", rec.Code, rec.Body.String())
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(dir, ".ntm", "pipelines"))
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			st, err := pipeline.LoadState(dir, strings.TrimSuffix(entry.Name(), ".json"))
			if err == nil && string(st.Status) == "cancelled" {
				return // The real executor stopped before the gate was released.
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("DELETE cancelled only the job row; the pipeline kept running")
}
