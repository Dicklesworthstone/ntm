package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestSwarmJobFailureRecoveryHTTP(t *testing.T) {
	for _, failure := range []string{"returned error", "spawn error", "embedded error"} {
		t.Run(failure, func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				out := &robot.SpawnOutput{
					Session: opts.Session,
					Agents:  []robot.SpawnedAgent{{Pane: "%42", Type: "claude"}},
				}
				out.ErrorCode = "SPAWN_FAILED"
				switch failure {
				case "returned error":
					return out, errors.New("second agent failed")
				case "spawn error":
					out.Error = "second agent failed"
				case "embedded error":
					out.RobotResponse.Error = "second agent failed"
				}
				return out, nil
			}

			env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"recoverable","cc_count":2}}`)
			final := pollJobTerminal(t, srv, env.Job.ID)
			if final.Job.Status != string(JobStatusFailed) || !strings.Contains(final.Job.Error, "second agent failed") {
				t.Fatalf("lost spawn failure: %+v", final.Job)
			}
			assertSpawnJobRecovery(t, final)
		})
	}
}

func assertSpawnJobRecovery(t *testing.T, env jobEnvelope) {
	t.Helper()
	if env.Job.Result["session"] != "recoverable" {
		t.Fatalf("lost created session: %+v", env.Job)
	}
	agents, ok := env.Job.Result["agents"].([]interface{})
	if !ok || len(agents) != 1 {
		t.Fatalf("lost partially created agents: %+v", env.Job.Result)
	}
	agent, ok := agents[0].(map[string]interface{})
	if !ok || agent["pane"] != "%42" {
		t.Fatalf("lost pane identity: %+v", agents)
	}
}

func TestSwarmJobCancellationRecoveryHTTP(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-release:
			return nil, errors.New("test cleanup")
		}
		// Hold the worker after cancellation so the first GET deterministically
		// sees a terminal row without its still-in-flight recovery evidence.
		<-release
		return &robot.SpawnOutput{
			Session: opts.Session,
			Agents:  []robot.SpawnedAgent{{Pane: "%42", Type: "claude"}},
		}, ctx.Err()
	}
	defer finish()

	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"recoverable","cc_count":2}}`)
	defer srv.jobStore.Cancel(env.Job.ID)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("spawn worker did not start")
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+env.Job.ID, nil))
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("DELETE job = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("DELETE did not cancel the real worker")
	}
	initial := pollJobTerminal(t, srv, env.Job.ID)
	if initial.Job.Status != string(JobStatusCancelled) || initial.Job.Result != nil {
		t.Fatalf("unexpected cancellation state before worker returns: %+v", initial.Job)
	}
	finish()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		final := pollJobTerminal(t, srv, env.Job.ID)
		if final.Job.Result != nil {
			if final.Job.Status != initial.Job.Status || final.Job.Error != initial.Job.Error || final.Job.Progress != initial.Job.Progress {
				t.Fatalf("worker overwrote cancellation: before=%+v after=%+v", initial.Job, final.Job)
			}
			assertSpawnJobRecovery(t, final)
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("cancelled job never exposed the worker's recovery evidence")
		case <-ticker.C:
		}
	}
}

func TestCancelledJobRecoveryGuards(t *testing.T) {
	for _, status := range []JobStatus{JobStatusPending, JobStatusRunning, JobStatusCompleted, JobStatusFailed, JobStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store := NewJobStore()
			job := store.Create(JobTypeSwarmSpawn)
			store.Update(job.ID, status, 25, nil, "original reason")
			store.retainCancelledResult(job.ID, nil)
			store.retainCancelledResult(job.ID, map[string]interface{}{})
			if got := store.Get(job.ID); got.Result != nil {
				t.Fatalf("empty evidence changed result: %+v", got)
			}
			store.retainCancelledResult(job.ID, map[string]interface{}{"session": "first"})
			store.retainCancelledResult(job.ID, map[string]interface{}{"session": "replacement"})
			got := store.Get(job.ID)
			if got.Status != status || got.Error != "original reason" || got.Progress != 25 {
				t.Fatalf("recovery changed terminal metadata: %+v", got)
			}
			if status == JobStatusCancelled {
				if got.Result["session"] != "first" {
					t.Fatalf("recovery missing or replaced: %+v", got)
				}
			} else if got.Result != nil {
				t.Fatalf("recovery changed a non-cancelled job: %+v", got)
			}
			store.retainCancelledResult("missing", map[string]interface{}{"session": "ghost"})
			if store.Get("missing") != nil {
				t.Fatal("recovery resurrected an absent job")
			}
		})
	}
}

func TestCancelledJobRecoveryRace(t *testing.T) {
	for _, workerStatus := range []JobStatus{JobStatusCompleted, JobStatusFailed} {
		t.Run(string(workerStatus), func(t *testing.T) {
			for i := 0; i < 128; i++ {
				store := NewJobStore()
				job := store.Create(JobTypeSwarmSpawn)
				start := make(chan struct{})
				done := make(chan struct{}, 2)
				go func() {
					<-start
					store.Update(job.ID, JobStatusCancelled, 0, nil, "cancelled by user")
					done <- struct{}{}
				}()
				go func() {
					<-start
					result := map[string]interface{}{"session": "recoverable"}
					store.Update(job.ID, workerStatus, 100, result, "worker result")
					store.retainCancelledResult(job.ID, result)
					done <- struct{}{}
				}()
				close(start)
				<-done
				<-done
				got := store.Get(job.ID)
				if got.Result["session"] != "recoverable" {
					t.Fatalf("iteration %d lost evidence: %+v", i, got)
				}
				if got.Status == JobStatusCancelled {
					if got.Error != "cancelled by user" {
						t.Fatalf("worker overwrote cancellation reason: %+v", got)
					}
				} else if got.Status != workerStatus {
					t.Fatalf("unexpected terminal state: %+v", got)
				}
			}
		})
	}
}
