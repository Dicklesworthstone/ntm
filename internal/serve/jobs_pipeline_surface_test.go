package serve

import (
	"encoding/json"
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
