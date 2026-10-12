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
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func postPipelineVariantJob(t *testing.T, srv *Server, jobType string, params map[string]interface{}) jobEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"type": jobType, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return postJob(t, srv, string(body))
}

func awaitPipelineJobFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pipeline did not reach filesystem rendezvous %s", path)
}

func TestJobDispatchPipelineExecOwnsInlineWorkflow(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			dir := t.TempDir()
			srv.mu.Lock()
			srv.projectDir = dir
			srv.mu.Unlock()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			exit := 0
			if fail {
				exit = 9
			}
			job := postPipelineVariantJob(t, srv, JobTypePipelineExec, map[string]interface{}{
				"session": "inlinejob", "background": true,
				"workflow": map[string]interface{}{
					"schema_version": "2.0", "name": "inline-job",
					"steps": []map[string]interface{}{{"id": "gate", "command": fmt.Sprintf("echo started > %q; while [ ! -f %q ]; do sleep 0.01; done; exit %d", started, release, exit)}},
				},
			})
			awaitPipelineJobFile(t, started)
			if got := srv.jobStore.Get(job.Job.ID); got == nil || got.Status != JobStatusRunning {
				t.Fatalf("inline job detached before its workflow finished: %+v", got)
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			final := pollJobTerminal(t, srv, job.Job.ID)
			want := JobStatusCompleted
			if fail {
				want = JobStatusFailed
			}
			if JobStatus(final.Job.Status) != want {
				t.Fatalf("inline job status=%s error=%s, want %s", final.Job.Status, final.Job.Error, want)
			}
			if runID, _ := final.Job.Result["run_id"].(string); runID == "" {
				t.Fatalf("inline execution lost run identity: %#v", final.Job.Result)
			}
		})
	}
}

func TestJobDispatchPipelineResumePreservesCompletedWork(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancel), func(t *testing.T) {
			srv := NewHermeticServer("test")
			defer srv.Stop()
			dir := t.TempDir()
			srv.mu.Lock()
			srv.projectDir = dir
			srv.mu.Unlock()
			first := filepath.Join(dir, "first-step")
			armed := filepath.Join(dir, "resume-allowed")
			started := filepath.Join(dir, "resumed-step-started")
			release := filepath.Join(dir, "release")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			workflow := fmt.Sprintf(`schema_version: "2.0"
name: resumable-job
steps:
  - id: first
    command: 'echo once >> %q'
  - id: gate
    depends_on: [first]
    command: 'test -f %q || exit 9; echo started > %q; while [ ! -f %q ]; do sleep 0.01; done'
`, first, armed, started, release)
			if err := os.WriteFile(filepath.Join(dir, "resume.yaml"), []byte(workflow), 0600); err != nil {
				t.Fatal(err)
			}
			initial := postPipelineVariantJob(t, srv, JobTypePipelineRun, map[string]interface{}{"workflow_file": "resume.yaml", "session": "resumejob"})
			failed := pollJobTerminal(t, srv, initial.Job.ID)
			runID, _ := failed.Job.Result["run_id"].(string)
			if JobStatus(failed.Job.Status) != JobStatusFailed || runID == "" {
				t.Fatalf("initial workflow must fail with inspectable saved state: %+v", failed.Job)
			}
			if err := os.WriteFile(armed, []byte("resume"), 0600); err != nil {
				t.Fatal(err)
			}
			// Session deliberately omitted: recovery must use the saved roster.
			resuming := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{"run_id": runID})
			awaitPipelineJobFile(t, started)
			if got := srv.jobStore.Get(resuming.Job.ID); got == nil || got.Status != JobStatusRunning {
				t.Fatalf("resume job detached before execution finished: %+v", got)
			}
			data, err := os.ReadFile(first)
			if err != nil || string(data) != "once\n" {
				t.Fatalf("resume re-executed completed work: %q %v", data, err)
			}
			if cancel {
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/jobs/"+resuming.Job.ID, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("cancel resume: %d %s", rec.Code, rec.Body.String())
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					state, err := pipeline.LoadState(dir, runID)
					if err == nil && string(state.Status) == "cancelled" {
						return // Real resumed execution stopped without releasing its gate.
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Fatal("resume cancellation changed only the job row")
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			final := pollJobTerminal(t, srv, resuming.Job.ID)
			if JobStatus(final.Job.Status) != JobStatusCompleted || final.Job.Result["run_id"] != runID || final.Job.Result["session"] != "resumejob" {
				t.Fatalf("resume failed or lost saved identity: %+v", final.Job)
			}
			state, err := pipeline.LoadState(dir, runID)
			if err != nil || string(state.Status) != "completed" {
				t.Fatalf("resume did not durably complete: %+v %v", state, err)
			}
		})
	}
}

func TestJobDispatchPipelineResumeRejectsUnavailableState(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()
	dir := t.TempDir()
	srv.mu.Lock()
	srv.projectDir = dir
	srv.mu.Unlock()
	for _, runID := range []string{"missing", "../escape"} {
		job := postPipelineVariantJob(t, srv, JobTypePipelineResume, map[string]interface{}{"run_id": runID})
		final := pollJobTerminal(t, srv, job.Job.ID)
		if JobStatus(final.Job.Status) != JobStatusFailed || !strings.Contains(final.Job.Error, "load resume state") {
			t.Fatalf("unavailable resume state reported success: %+v", final.Job)
		}
	}
}

func TestPipelineJobIdentitySurvivesInterruptedJournal(t *testing.T) {
	for _, jobType := range []string{JobTypePipelineRun, JobTypePipelineExec, JobTypePipelineResume} {
		t.Run(jobType, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.db")
			srv, closeServer := newJournalHTTPServer(t, path)
			srv.mu.Lock()
			srv.projectDir = dir
			srv.mu.Unlock()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			workflow := &pipeline.Workflow{
				SchemaVersion: pipeline.SchemaVersion, Name: "durable-pipeline",
				Steps: []pipeline.Step{{ID: "gate", Command: fmt.Sprintf("echo once >> %q; while [ ! -f %q ]; do sleep 0.01; done", started, release)}},
			}
			_, locator, err := pipeline.SnapshotWorkflow(context.Background(), dir, workflow, "")
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]interface{}{"session": "durablepipeline", "operation_id": "pipeline-once"}
			phase := "before_run"
			switch jobType {
			case JobTypePipelineRun:
				params["workflow_file"] = locator
			case JobTypePipelineExec:
				params["workflow"] = workflow
			case JobTypePipelineResume:
				phase = "before_resume"
				params["run_id"] = "saved-pipeline"
				if err := pipeline.SaveState(dir, &pipeline.ExecutionState{
					RunID: "saved-pipeline", WorkflowID: workflow.Name, WorkflowFile: locator,
					Session: "durablepipeline", Status: pipeline.StatusFailed,
				}); err != nil {
					t.Fatal(err)
				}
			}
			job := postPipelineVariantJob(t, srv, jobType, params)
			awaitPipelineJobFile(t, started)
			response := admissionRequest(srv, http.MethodGet, "/api/v1/jobs/"+job.Job.ID, "")
			var live struct {
				Job *Job `json:"job"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &live) != nil || live.Job == nil || live.Job.Status != JobStatusRunning {
				t.Fatalf("live pipeline job unavailable: %d %s", response.Code, response.Body.String())
			}
			runID, _ := live.Job.Result["run_id"].(string)
			assertIdentity := func(result map[string]interface{}) {
				t.Helper()
				if runID == "" || result["run_id"] != runID || result["workflow_id"] != workflow.Name || result["session"] != "durablepipeline" || result["project_dir"] != dir || result["pipeline_checkpoint"] != phase {
					t.Fatalf("pipeline recovery identity lost: %#v", result)
				}
				file, _ := result["workflow_file"].(string)
				if file == "" {
					t.Fatal("frozen workflow locator missing")
				}
				if _, _, err := pipeline.LoadResumeWorkflow(file); err != nil {
					t.Fatalf("recorded workflow cannot be inspected: %v", err)
				}
			}
			assertIdentity(live.Job.Result)
			journalDir, err := srv.jobJournalDir()
			if err != nil {
				t.Fatal(err)
			}
			journal := &jobJournal{dir: journalDir}
			persisted, err := journal.load()
			if err != nil {
				t.Fatal(err)
			}
			var interrupted *Job
			for _, saved := range persisted {
				if saved.ID == job.Job.ID {
					interrupted = saved
				}
			}
			if interrupted == nil || interrupted.Result["_execution_in_progress"] != true {
				t.Fatal("pipeline acted without a durable live job checkpoint")
			}
			assertIdentity(interrupted.Result)
			operationPath := jobOperationPath(journalDir, "pipeline-once")
			operation, err := readJobOperation(operationPath)
			if err != nil || operation == nil || operation.Status != JobStatusRunning {
				t.Fatalf("operation checkpoint missing before pipeline effects: %+v %v", operation, err)
			}
			assertIdentity(operation.Result)
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			final := pollJobTerminal(t, srv, job.Job.ID)
			if final.Job.Status != string(JobStatusCompleted) {
				t.Fatalf("pipeline did not complete: %+v", final.Job)
			}
			assertIdentity(final.Job.Result)
			closeServer()
			// Restore the actual records captured while the command was blocked.
			// This models losing the worker before its final journal publication,
			// without leaving a real orphan process in the test runner.
			if err := journal.save(interrupted); err != nil {
				t.Fatal(err)
			}
			if err := operation.save(operationPath); err != nil {
				t.Fatal(err)
			}
			restarted, _ := newJournalHTTPServer(t, path)
			restarted.mu.Lock()
			restarted.projectDir = dir
			restarted.mu.Unlock()
			recovered := pollJobTerminal(t, restarted, job.Job.ID)
			if recovered.Job.Status != string(JobStatusFailed) || recovered.Job.Result["outcome_unknown"] != true || recovered.Job.Result["_execution_in_progress"] != nil {
				t.Fatalf("interruption was reported as a known outcome: %+v", recovered.Job)
			}
			assertIdentity(recovered.Job.Result)
			replayed := postPipelineVariantJob(t, restarted, jobType, params)
			replay := pollJobTerminal(t, restarted, replayed.Job.ID)
			if replay.Job.Status != string(JobStatusFailed) || !strings.Contains(replay.Job.Error, "outcome is unknown") {
				t.Fatalf("uncertain operation was re-executed: %+v", replay.Job)
			}
			assertIdentity(replay.Job.Result)
			calls, err := os.ReadFile(started)
			if err != nil || string(calls) != "once\n" {
				t.Fatalf("recovery repeated command effects: %q %v", calls, err)
			}
		})
	}
}

func TestPipelineJobCheckpointStopsBeforeExecution(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("resume=%v/cancelled=%v", resume, cancelled), func(t *testing.T) {
				dir := t.TempDir()
				srv := NewHermeticServer("test")
				defer srv.Stop()
				srv.mu.Lock()
				srv.projectDir = dir
				srv.mu.Unlock()
				marker := filepath.Join(dir, "must-not-run")
				workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "checkpoint-gate", Steps: []pipeline.Step{{ID: "effect", Command: fmt.Sprintf("echo acted > %q", marker)}}}
				config := pipeline.DefaultExecutorConfig("checkpointjob")
				config.ProjectDir, config.RunID = dir, "checkpoint-run"
				var before []byte
				statePath := filepath.Join(dir, ".ntm", "pipelines", config.RunID+".json")
				if resume {
					_, locator, err := pipeline.SnapshotWorkflow(context.Background(), dir, workflow, "")
					if err != nil {
						t.Fatal(err)
					}
					if err := pipeline.SaveState(dir, &pipeline.ExecutionState{RunID: config.RunID, WorkflowID: workflow.Name, WorkflowFile: locator, Session: config.Session, Status: pipeline.StatusFailed}); err != nil {
						t.Fatal(err)
					}
					before, err = os.ReadFile(statePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				ctx = context.WithValue(ctx, jobProgressContextKey{}, jobProgressReporter(func(result map[string]interface{}) error {
					calls++
					if result["run_id"] != config.RunID || result["workflow_id"] != workflow.Name || result["workflow_file"] == "" {
						t.Fatalf("checkpoint omitted execution identity: %#v", result)
					}
					if cancelled {
						cancel()
						return nil
					}
					return errors.New("recovery storage unavailable")
				}))
				var out pipeline.PipelineRunOutput
				if resume {
					out = srv.resumePipelineWithResult(ctx, config.RunID, config.Session, nil, nil)
				} else {
					out = srv.executeOwnedPipeline(ctx, workflow, nil, config)
				}
				if calls != 1 || out.Success || out.RunID != config.RunID || out.WorkflowID != workflow.Name {
					t.Fatalf("checkpoint refusal lost identity or entered execution: calls=%d out=%+v", calls, out)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("pipeline acted after its recovery checkpoint was refused: %v", err)
				}
				after, err := os.ReadFile(statePath)
				if resume {
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("refused resume changed durable execution state: %v", err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("refused run created execution state: %v", err)
				}
			})
		}
	}
}
