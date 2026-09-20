package serve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newJournalHTTPServer(t *testing.T, path string) (*Server, func()) {
	t.Helper()
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		t.Fatal(err)
	}
	srv := NewHermeticServer("job-journal-test")
	srv.stateStore = store
	closeJobs, err := srv.RestoreJobHistory()
	if err != nil {
		srv.Stop()
		store.Close()
		t.Fatal(err)
	}
	var once sync.Once
	closeServer := func() {
		once.Do(func() {
			srv.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := closeJobs(ctx); err != nil {
				t.Errorf("close persistent jobs: %v", err)
			}
			store.Close()
		})
	}
	t.Cleanup(closeServer)
	return srv, closeServer
}

func TestJobJournalHTTPCompletedAndFailedJobsSurviveRestart(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "completed"
		if fails {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			var calls atomic.Int32
			srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				out := &robot.SpawnOutput{Session: "durablejob", Agents: []robot.SpawnedAgent{{Pane: "%7", Type: "claude"}}}
				out.Success = !fails
				if fails {
					return out, errors.New("second pane failed")
				}
				return out, nil
			}
			env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"durablejob","cc_count":2}}`)
			pollJobTerminal(t, srv, env.Job.ID)
			closeServer() // Joins the worker's final checkpoint before reopening.
			restarted, _ := newJournalHTTPServer(t, path)
			restarted.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				return nil, errors.New("recovery must not dispatch")
			}
			got := pollJobTerminal(t, restarted, env.Job.ID)
			if got.Job.Status != name || got.Job.Result["session"] != "durablejob" || calls.Load() != 1 {
				t.Fatalf("HTTP recovery lost or replayed the outcome: %+v calls=%d", got.Job, calls.Load())
			}
			agents, ok := got.Job.Result["agents"].([]interface{})
			if !ok || len(agents) != 1 || agents[0].(map[string]interface{})["pane"] != "%7" {
				t.Fatalf("partial pane evidence lost: %+v", got.Job.Result)
			}
		})
	}
}

func TestJobJournalHTTPCancellationRetainsLateEvidenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	srv, closeServer := newJournalHTTPServer(t, path)
	started := make(chan struct{})
	srv.spawnAgents = func(ctx context.Context, _ robot.SpawnOptions) (*robot.SpawnOutput, error) {
		close(started)
		<-ctx.Done()
		return &robot.SpawnOutput{Session: "partially-created", Agents: []robot.SpawnedAgent{{Pane: "%9"}}}, ctx.Err()
	}
	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"durablecancel","cc_count":2}}`)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+env.Job.ID, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", rec.Code, rec.Body.String())
	}
	closeServer()
	restarted, _ := newJournalHTTPServer(t, path)
	got := pollJobTerminal(t, restarted, env.Job.ID)
	if got.Job.Status != string(JobStatusCancelled) || got.Job.Result["session"] != "partially-created" || !strings.Contains(got.Job.Error, "cancelled by user") {
		t.Fatalf("cancel reason or late result did not survive restart: %+v", got.Job)
	}
}

func TestJobJournalHTTPInterruptedWorkIsInspectableNotReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	journal, err := openJobJournal(path + ".jobs")
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.save(&Job{ID: "interrupted-run", Type: "pipeline_run", Status: JobStatusRunning, Result: map[string]interface{}{"run_id": "saved-run"}}); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	srv, _ := newJournalHTTPServer(t, path)
	got := pollJobTerminal(t, srv, "interrupted-run")
	if got.Job.Status != string(JobStatusFailed) || got.Job.Result["run_id"] != "saved-run" || got.Job.Result["outcome_unknown"] != true {
		t.Fatalf("stale work appeared healthy or lost its recovery identity: %+v", got.Job)
	}
}

func TestJobJournalFailurePreventsExecution(t *testing.T) {
	srv, _ := newJournalHTTPServer(t, filepath.Join(t.TempDir(), "state.db"))
	var calls atomic.Int32
	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		calls.Add(1)
		return nil, errors.New("must not execute")
	}
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	// Force a deterministic checkpoint encoding failure without depending on
	// chmod (which does not deny writes when tests run as root).
	srv.jobStore.Update(job.ID, JobStatusPending, 0, map[string]interface{}{"invalid": make(chan int)}, "")
	srv.dispatchJob(job.ID, CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{"session": "no-effects", "cc_count": 1}})
	got := srv.jobStore.Get(job.ID)
	if got.Status != JobStatusFailed || calls.Load() != 0 || !strings.Contains(got.Error, "checkpoint job before execution") {
		t.Fatalf("unrecordable execution was allowed: %+v calls=%d", got, calls.Load())
	}
}
