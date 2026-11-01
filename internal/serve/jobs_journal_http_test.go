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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newJournalHTTPServer(t *testing.T, path string, configure ...func(*Server)) (*Server, func()) {
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
	for _, setup := range configure {
		setup(srv)
	}
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

func TestJobJournalHTTPRecoveryBoundsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := openJobJournal(path + ".jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	base := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	lastID := fmt.Sprintf("history-%04d", maxRetainedJobs+1)
	for i := 0; i < maxRetainedJobs+2; i++ {
		job := &Job{ID: fmt.Sprintf("history-%04d", i), Type: JobTypeSwarmSpawn,
			Status:    []JobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled}[i%3],
			CreatedAt: base.Format(time.RFC3339), UpdatedAt: base.Format(time.RFC3339),
			Result: map[string]interface{}{"session": "history-session", "pane_id": fmt.Sprintf("%%%d", i)},
		}
		if job.Status == JobStatusCancelled {
			job.Error = "cancelled by user"
		}
		if i < 2 {
			job.CreatedAt = base.Add(-time.Hour).Format(time.RFC3339)
			job.Status = JobStatusRunning
			if i == 1 {
				job.Status, job.Error = JobStatusCancelled, "cancelled by user"
				job.Result["_execution_in_progress"] = true
			}
		}
		if job.ID == lastID {
			job.CreatedAt = base.Add(time.Hour).Format(time.RFC3339)
			job.Status = JobStatusRunning
		}
		if err := journal.save(job); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	configure := func(srv *Server) {
		// Install before RestoreJobHistory, so even an early replay is visible.
		srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
			calls.Add(1)
			return nil, errors.New("history must never dispatch an engine")
		}
	}
	srv, closeServer := newJournalHTTPServer(t, path, configure)
	rec := admissionRequest(srv, http.MethodGet, "/api/v1/jobs", "")
	type historyResponse struct {
		Jobs      []*Job             `json:"jobs"`
		Count     int                `json:"count"`
		Execution JobExecutionStatus `json:"execution"`
	}
	var listing historyResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listing) != nil || listing.Count != maxRetainedJobs || len(listing.Jobs) != maxRetainedJobs {
		t.Fatalf("HTTP startup history is not bounded: status=%d count=%d jobs=%d", rec.Code, listing.Count, len(listing.Jobs))
	}
	for i, job := range listing.Jobs {
		index := i + 1
		wantStatus := []JobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled}[index%3]
		if i == 0 {
			index, wantStatus = maxRetainedJobs+1, JobStatusFailed
		}
		if job.ID != fmt.Sprintf("history-%04d", index) || job.Status != wantStatus || job.Result["pane_id"] != fmt.Sprintf("%%%d", index) {
			t.Fatalf("HTTP startup changed retained ordering or evidence at index %d: %+v", i, job)
		}
	}
	got := pollJobTerminal(t, srv, lastID)
	if got.Job.Status != string(JobStatusFailed) || got.Job.Result["outcome_unknown"] != true || !strings.Contains(got.Job.Error, "not automatically replayed") {
		t.Fatalf("HTTP hid an interrupted execution: %+v", got.Job)
	}
	oldEvidence := make(map[string][]byte)
	for _, id := range []string{"history-0000", "history-0001"} {
		data, err := os.ReadFile(filepath.Join(journal.dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		oldEvidence[id] = data
		var record jobJournalRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Job.Result["outcome_unknown"] != true || record.Job.Result["_execution_in_progress"] != nil ||
			(id == "history-0000" && record.Job.Status != JobStatusFailed) ||
			(id == "history-0001" && (record.Job.Status != JobStatusCancelled || record.Job.Error != "cancelled by user")) {
			t.Fatalf("startup discarded interrupted evidence outside the retained window: %+v", record.Job)
		}
		if rec := admissionRequest(srv, http.MethodGet, "/api/v1/jobs/"+id, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("old receipt bypassed the in-memory history bound: %s status=%d", id, rec.Code)
		}
	}
	if calls.Load() != 0 || listing.Execution.Running != 0 || listing.Execution.Queued != 0 || listing.Execution.Owned != 0 {
		t.Fatalf("startup replayed history: calls=%d execution=%+v", calls.Load(), listing.Execution)
	}
	closeServer()
	restarted, closeRestarted := newJournalHTTPServer(t, path, configure)
	rec = admissionRequest(restarted, http.MethodGet, "/api/v1/jobs", "")
	var repeated historyResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &repeated) != nil {
		t.Fatalf("second HTTP startup failed: status=%d", rec.Code)
	}
	beforeView, err := json.Marshal(listing.Jobs)
	if err != nil {
		t.Fatal(err)
	}
	afterView, err := json.Marshal(repeated.Jobs)
	if err != nil || !bytes.Equal(beforeView, afterView) || repeated.Count != listing.Count ||
		repeated.Execution.Running != 0 || repeated.Execution.Queued != 0 || repeated.Execution.Owned != 0 {
		t.Fatalf("repeated HTTP startup changed recovered history or dispatched work: %v", err)
	}
	closeRestarted()
	for id, before := range oldEvidence {
		after, err := os.ReadFile(filepath.Join(journal.dir, id+".json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("HTTP startup changed or removed old recovery evidence %s: %v", id, err)
		}
	}
	entries, err := os.ReadDir(journal.dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	if count != maxRetainedJobs+2 || calls.Load() != 0 {
		t.Fatalf("history was pruned or replayed: receipts=%d calls=%d", count, calls.Load())
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

func TestJobJournalHTTPLiveProgressSurvivesNilResultAndPanic(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil-result", true: "panic"}[panics], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				if opts.LifecycleDeps == nil || opts.LifecycleDeps.LaunchAgent == nil {
					return nil, errors.New("durable spawn did not install lifecycle checkpoints")
				}
				if err := reportJobProgress(ctx, map[string]interface{}{
					"session": opts.Session, "agents": []robot.SpawnedAgent{{Pane: "0.1", Type: "claude"}},
					"spawn_progress": map[string]interface{}{"agent_pane_ids": map[string]string{"0.1": "%7"}},
				}); err != nil {
					return nil, err
				}
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if panics {
					panic("backend stopped after first agent")
				}
				return nil, errors.New("backend stopped after first agent")
			}
			env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"liveprogress","cc_count":2}}`)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("job did not publish progress")
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+env.Job.ID, nil))
			var live jobEnvelope
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &live) != nil || live.Job.Status != string(JobStatusRunning) || live.Job.Result["_execution_in_progress"] != true {
				t.Fatalf("running job hides progress: %d %s", rec.Code, rec.Body.String())
			}
			dir, err := srv.jobJournalDir()
			if err != nil {
				t.Fatal(err)
			}
			checkpoints, err := (&jobJournal{dir: dir}).load()
			if err != nil || len(checkpoints) != 1 || checkpoints[0].Result["session"] != "liveprogress" {
				t.Fatalf("GET exposed uncheckpointed progress: %+v %v", checkpoints, err)
			}
			finish()
			final := pollJobTerminal(t, srv, env.Job.ID)
			if final.Job.Status != string(JobStatusFailed) || !strings.Contains(final.Job.Error, "backend stopped") || final.Job.Result["spawn_progress"] == nil || final.Job.Result["_execution_in_progress"] != nil {
				t.Fatalf("terminal failure lost progress: %+v", final.Job)
			}
			closeServer()
			restarted, _ := newJournalHTTPServer(t, path)
			got := pollJobTerminal(t, restarted, env.Job.ID)
			if got.Job.Result["spawn_progress"] == nil || got.Job.Result["session"] != "liveprogress" || got.Job.Status != string(JobStatusFailed) {
				t.Fatalf("restart lost live checkpoint: %+v", got.Job)
			}
		})
	}
}

func TestJobOperationHTTPProgressSurvivesOriginalJobEviction(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "interrupted"}[panics], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			var calls atomic.Int32
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				if opts.LifecycleDeps == nil {
					return nil, errors.New("operation did not install lifecycle reporting")
				}
				if err := reportJobProgress(ctx, map[string]interface{}{"session": opts.Session, "pane_id": "%17"}); err != nil {
					return nil, err
				}
				if panics {
					panic("lost backend")
				}
				return nil, errors.New("lost backend")
			}
			body := `{"type":"swarm_spawn","params":{"operation_id":"progress-once","session":"opprogress","cc_count":2}}`
			first := postJob(t, srv, body)
			pollJobTerminal(t, srv, first.Job.ID)
			closeServer()
			restarted, _ := newJournalHTTPServer(t, path)
			// Model the bounded job list evicting the original transport row.
			restarted.jobStore.mu.Lock()
			delete(restarted.jobStore.jobs, first.Job.ID)
			restarted.jobStore.mu.Unlock()
			restarted.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
				calls.Add(1)
				return nil, errors.New("must not execute twice")
			}
			retry := postJob(t, restarted, body)
			got := pollJobTerminal(t, restarted, retry.Job.ID)
			if got.Job.Status != string(JobStatusFailed) || got.Job.Result["pane_id"] != "%17" || got.Job.Result["_execution_in_progress"] != nil || calls.Load() != 1 {
				t.Fatalf("operation retry lost or repeated effects: %+v calls=%d", got.Job, calls.Load())
			}
			meta := got.Job.Result["_operation"].(map[string]interface{})
			wantStatus := "failed"
			if panics {
				wantStatus = "outcome_unknown"
			}
			if meta["original_job_id"] != first.Job.ID || meta["status"] != wantStatus || meta["replayed"] != !panics {
				t.Fatalf("retry did not preserve operation semantics: %+v", meta)
			}
		})
	}
}

// Block the actual atomic-rename destination after the preceding receipt is
// durable. Preserve that original file byte-for-byte for restart recovery.
func blockJobOutcomePublication(t *testing.T, srv *Server, id string) (string, []byte, func()) {
	t.Helper()
	dir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	retained := path + ".before-terminal"
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if err := os.Rename(path, path+".blocked-terminal"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(retained, path); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Cleanup(restore)
	return retained, before, restore
}

func awaitJobOutcomePublication(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !srv.jobExecutor.idle() {
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish its final checkpoint")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestJobJournalHTTPFinalPublicationFailureRetainsExecutionEvidence(t *testing.T) {
	for _, operationID := range []string{"", "terminal-once"} {
		t.Run("operation="+operationID, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			srv.mu.Lock()
			srv.projectDir = root
			srv.mu.Unlock()
			effects, ready, release := filepath.Join(root, "effects"), filepath.Join(root, "ready"), filepath.Join(root, "release")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0o600) })
			params := map[string]interface{}{
				"session": "terminal-journal",
				"workflow": pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "terminal-journal", Steps: []pipeline.Step{{
					ID: "effect", Command: fmt.Sprintf("printf 'once\\n' >> %q; touch %q; while [ ! -f %q ]; do sleep 0.01; done", effects, ready, release),
				}}},
			}
			if operationID != "" {
				params["operation_id"] = operationID
			}
			initial := postPipelineVariantJob(t, srv, JobTypePipelineExec, params)
			awaitPipelineJobFile(t, ready)
			retained, before, restore := blockJobOutcomePublication(t, srv, initial.Job.ID)
			if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			awaitJobOutcomePublication(t, srv)
			// Inspect through HTTP before shutdown: shutdown must not be what
			// makes a misleading completed row eventually truthful.
			final := pollJobTerminal(t, srv, initial.Job.ID)
			runID, _ := final.Job.Result["run_id"].(string)
			if final.Job.Status != string(JobStatusFailed) || final.Job.Result["observed_status"] != string(JobStatusCompleted) ||
				final.Job.Result["outcome_unknown"] != true || final.Job.Result["_journal_error"] == nil || runID == "" ||
				final.Job.Result["status"] != "completed" || !strings.Contains(final.Job.Error, "final job receipt") {
				t.Fatalf("failed final publication advertised completion or lost evidence: %+v", final.Job)
			}
			state, err := pipeline.LoadState(root, runID)
			if err != nil || state.Status != pipeline.StatusCompleted {
				t.Fatalf("storage failure downgraded the completed pipeline: %+v %v", state, err)
			}
			if data, err := os.ReadFile(retained); err != nil || !bytes.Equal(data, before) {
				t.Fatalf("failed publication replaced the preceding receipt: %v", err)
			}
			if operationID != "" {
				dir, _ := srv.jobJournalDir()
				receipt, err := readJobOperation(jobOperationPath(dir, operationID))
				if err != nil || receipt == nil || receipt.Status != JobStatusCompleted || receipt.Result["run_id"] != runID {
					t.Fatalf("job publication damaged authoritative operation outcome: %+v %v", receipt, err)
				}
			}
			restore()
			closeServer()
			restarted, _ := newJournalHTTPServer(t, path)
			restarted.mu.Lock()
			restarted.projectDir = root
			restarted.mu.Unlock()
			recovered := pollJobTerminal(t, restarted, initial.Job.ID)
			if recovered.Job.Status != string(JobStatusFailed) || recovered.Job.Result["outcome_unknown"] != true || recovered.Job.Result["run_id"] != runID {
				t.Fatalf("restart guessed completion or lost the original run: %+v", recovered.Job)
			}
			if operationID != "" {
				retry := postPipelineVariantJob(t, restarted, JobTypePipelineExec, params)
				replayed := pollJobTerminal(t, restarted, retry.Job.ID)
				meta := operationMetadata(t, replayed.Job.Result)
				if replayed.Job.Status != string(JobStatusCompleted) || replayed.Job.Result["run_id"] != runID || meta["replayed"] != true || meta["original_job_id"] != initial.Job.ID {
					t.Fatalf("retry ignored the durable operation outcome: %+v", replayed.Job)
				}
				// A completed polling response now implies the final receipt
				// already exists, without first joining shutdown.
				dir, _ := restarted.jobJournalDir()
				data, err := os.ReadFile(filepath.Join(dir, retry.Job.ID+".json"))
				var record jobJournalRecord
				if err != nil || json.Unmarshal(data, &record) != nil || record.Job == nil || record.Job.Status != JobStatusCompleted {
					t.Fatalf("HTTP completed response preceded its receipt: %s %v", data, err)
				}
			}
			if data, err := os.ReadFile(effects); err != nil || string(data) != "once\n" {
				t.Fatalf("publication failure or recovery repeated pipeline effects: %q %v", data, err)
			}
		})
	}
}

func TestJobJournalHTTPFinalPublicationFailurePreservesCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	srv, closeServer := newJournalHTTPServer(t, path)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	var workerCtx context.Context
	srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		workerCtx = ctx
		if err := reportJobProgress(ctx, map[string]interface{}{"session": opts.Session, "pane_id": "%17"}); err != nil {
			return nil, err
		}
		close(started)
		<-ctx.Done()
		<-release // Keep the worker alive until DELETE's receipt is durable.
		return &robot.SpawnOutput{Session: opts.Session, Agents: []robot.SpawnedAgent{{Pane: "%17"}}}, ctx.Err()
	}
	initial := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"terminalcancel","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+initial.Job.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	retained, before, restore := blockJobOutcomePublication(t, srv, initial.Job.ID)
	finish()
	awaitJobOutcomePublication(t, srv)
	final := pollJobTerminal(t, srv, initial.Job.ID)
	if final.Job.Status != string(JobStatusCancelled) || final.Job.Error != "cancelled by user" || final.Job.Result["observed_status"] != string(JobStatusCancelled) ||
		final.Job.Result["agents"] == nil || final.Job.Result["pane_id"] != "%17" || final.Job.Result["_journal_error"] == nil || final.Job.Result["_execution_in_progress"] != nil {
		t.Fatalf("failed final write changed cancellation or discarded effects: %+v", final.Job)
	}
	if err := reportJobProgress(workerCtx, map[string]interface{}{"late": true}); !errors.Is(err, errJobProgressCheckpoint) || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("finished worker retained permission to write progress: %v", err)
	}
	if data, err := os.ReadFile(retained); err != nil || !bytes.Equal(data, before) {
		t.Fatalf("failed final write replaced durable cancellation: %v", err)
	}
	restore()
	closeServer()
	restarted, _ := newJournalHTTPServer(t, path)
	recovered := pollJobTerminal(t, restarted, initial.Job.ID)
	if recovered.Job.Status != string(JobStatusCancelled) || recovered.Job.Error != "cancelled by user" || recovered.Job.Result["pane_id"] != "%17" ||
		recovered.Job.Result["outcome_unknown"] != true || recovered.Job.Result["_execution_in_progress"] != nil {
		t.Fatalf("restart lost cancellation or invented a settled outcome: %+v", recovered.Job)
	}
}
