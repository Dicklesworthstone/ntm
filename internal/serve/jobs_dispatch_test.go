package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// D5a (bd-ws3-contract-breadth-psvyu.5.1): hermetic E2E over the real async
// dispatcher. Every allow-listed op goes POST /api/v1/jobs → 202 → poll → a
// terminal state that reflects the REAL operation's outcome, with a
// real-effect assertion per op. A failing op must land in JobStatusFailed
// carrying the real error — success-for-failed-work is the simulator's lie.

type jobEnvelope struct {
	Job struct {
		ID       string                 `json:"id"`
		Type     string                 `json:"type"`
		Status   string                 `json:"status"`
		Progress float64                `json:"progress"`
		Result   map[string]interface{} `json:"result"`
		Error    string                 `json:"error"`
	} `json:"job"`
}

func postJob(t *testing.T, srv *Server, body string) jobEnvelope {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/jobs = %d, want 202 (body=%s)", rec.Code, rec.Body.String())
	}
	var env jobEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode job envelope: %v (body=%s)", err, rec.Body.String())
	}
	if env.Job.ID == "" {
		t.Fatalf("job id missing: %s", rec.Body.String())
	}
	return env
}

// pollJobTerminal polls GET /api/v1/jobs/{id} until the job reaches a
// terminal state, mirroring how a real client consumes the 202 contract.
func pollJobTerminal(t *testing.T, srv *Server, jobID string) jobEnvelope {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID, nil)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/jobs/%s = %d (body=%s)", jobID, rec.Code, rec.Body.String())
		}
		var env jobEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode job envelope: %v (body=%s)", err, rec.Body.String())
		}
		switch JobStatus(env.Job.Status) {
		case JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state (last=%s)", jobID, env.Job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestJobDispatchPipelineRun proves POST /api/v1/jobs{type:pipeline_run}
// executes the REAL pipeline: the workflow's command step writes a marker
// file on disk, and the run is queryable from the pipeline run state
// (GetPipelineSnapshot) afterwards.
func TestJobDispatchPipelineRun(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	projectDir := t.TempDir()
	srv.mu.Lock()
	srv.projectDir = projectDir
	srv.mu.Unlock()

	marker := filepath.Join(projectDir, "pipeline-ran.txt")
	workflow := fmt.Sprintf(`
schema_version: "2.0"
name: job-dispatch-e2e
steps:
  - id: mark
    command: 'echo really-ran > %s'
`, marker)
	if err := os.WriteFile(filepath.Join(projectDir, "job-e2e.yaml"), []byte(workflow), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	env := postJob(t, srv, `{"type":"pipeline_run","params":{"workflow_file":"job-e2e.yaml","session":"jobpipeline1"}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusCompleted {
		t.Fatalf("pipeline_run job status = %s (error=%q), want completed", final.Job.Status, final.Job.Error)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("pipeline marker file missing — the pipeline did not actually run: %v", err)
	}
	if !strings.Contains(string(data), "really-ran") {
		t.Fatalf("marker content = %q, want really-ran", string(data))
	}

	runID, _ := final.Job.Result["run_id"].(string)
	if runID == "" {
		t.Fatalf("job result missing run_id: %#v", final.Job.Result)
	}
	// State DB assertion: the executor persisted this run under
	// <projectDir>/.ntm/pipelines/<run-id>.json and it finished completed.
	state, err := pipeline.LoadState(projectDir, runID)
	if err != nil {
		t.Fatalf("pipeline run %s not present in persisted run state: %v", runID, err)
	}
	if string(state.Status) != "completed" {
		t.Fatalf("persisted run state status = %s, want completed", state.Status)
	}
}

// TestJobDispatchPipelineRunFailure is the FAILED-terminal-state proof: a
// pipeline_run naming a nonexistent workflow must land the job in
// JobStatusFailed carrying the real error — never a simulated completion.
func TestJobDispatchPipelineRunFailure(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	projectDir := t.TempDir()
	srv.mu.Lock()
	srv.projectDir = projectDir
	srv.mu.Unlock()

	env := postJob(t, srv, `{"type":"pipeline_run","params":{"workflow_file":"does-not-exist.yaml","session":"jobpipefail1"}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusFailed {
		t.Fatalf("failing pipeline_run job status = %s, want failed (result=%#v)", final.Job.Status, final.Job.Result)
	}
	if final.Job.Error == "" {
		t.Fatal("failed job carries no error message")
	}
	if !strings.Contains(final.Job.Error, "pipeline run failed") {
		t.Fatalf("job error %q does not carry the real dispatch failure", final.Job.Error)
	}
}

// TestJobDispatchSwarmSpawn proves swarm_spawn drives the same production
// spawn seam the synchronous spawn route uses, with the requested options,
// and that the job's terminal state carries the spawn result.
func TestJobDispatchSwarmSpawn(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	var calls int32
	var gotOpts robot.SpawnOptions
	srv.spawnAgents = func(_ context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
		atomic.AddInt32(&calls, 1)
		gotOpts = opts
		out := &robot.SpawnOutput{Session: opts.Session}
		out.Success = true
		return out, nil
	}

	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"jobswarm1","cc_count":2,"label":"lane"}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusCompleted {
		t.Fatalf("swarm_spawn job status = %s (error=%q), want completed", final.Job.Status, final.Job.Error)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("spawn seam executed %d times, want 1", calls)
	}
	if gotOpts.Session != "jobswarm1" || gotOpts.CCCount != 2 || gotOpts.Label != "lane" {
		t.Fatalf("spawn seam options = %+v, want session=jobswarm1 cc_count=2 label=lane", gotOpts)
	}
	if got, _ := final.Job.Result["session"].(string); got != "jobswarm1" {
		t.Fatalf("job result session = %q, want jobswarm1 (result=%#v)", got, final.Job.Result)
	}
}

// TestJobDispatchSwarmSpawnFailure: a spawn seam error must surface as a
// FAILED terminal state carrying the real error.
func TestJobDispatchSwarmSpawnFailure(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	srv.spawnAgents = func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error) {
		return nil, fmt.Errorf("tmux exploded for real")
	}

	env := postJob(t, srv, `{"type":"swarm_spawn","params":{"session":"jobswarmfail1","cc_count":1}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusFailed {
		t.Fatalf("failing swarm_spawn job status = %s, want failed", final.Job.Status)
	}
	if !strings.Contains(final.Job.Error, "tmux exploded for real") {
		t.Fatalf("job error %q does not carry the seam's real error", final.Job.Error)
	}
}

// installFakeTmux points NTM_TMUX_BINARY at a script that records every
// invocation and succeeds, except `has-session`, which reports "no such
// session" so restore takes the create path. Returns the invocation log path.
func installFakeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-calls.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1" in
  has-session) exit 1 ;;
  split-window|new-window) echo "%%9" ;;
  list-windows) echo "0" ;;
  list-panes) echo "0" ;;
  *) : ;;
esac
exit 0
`, logPath)
	binPath := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", binPath)
	return logPath
}

// TestJobDispatchCheckpointRestore proves checkpoint_restore loads a real
// checkpoint from storage and actually drives session recreation: the job
// completes with the restored pane count and tmux received new-session for
// the checkpoint's session.
func TestJobDispatchCheckpointRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmuxLog := installFakeTmux(t)

	workDir := t.TempDir()
	const session = "jobrestore1"
	const cpID = "cp-job-e2e-1"

	storage := checkpoint.NewStorage()
	cp := &checkpoint.Checkpoint{
		Version:     1,
		ID:          cpID,
		Name:        "job-e2e",
		SessionName: session,
		WorkingDir:  workDir,
		CreatedAt:   time.Now(),
		PaneCount:   1,
		Session: checkpoint.SessionState{
			Panes: []checkpoint.PaneState{{
				Index:       0,
				WindowIndex: 0,
				ID:          "%0",
				AgentType:   "user",
			}},
		},
	}
	if err := storage.Save(cp); err != nil {
		t.Fatalf("save checkpoint fixture: %v", err)
	}

	srv := NewHermeticServer("test")
	defer srv.Stop()

	env := postJob(t, srv, fmt.Sprintf(
		`{"type":"checkpoint_restore","params":{"session":%q,"checkpoint_id":%q,"skip_git_check":true}}`,
		session, cpID))
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusCompleted {
		t.Fatalf("checkpoint_restore job status = %s (error=%q), want completed", final.Job.Status, final.Job.Error)
	}
	if got, _ := final.Job.Result["panes_restored"].(float64); int(got) != 1 {
		t.Fatalf("panes_restored = %v, want 1 (result=%#v)", final.Job.Result["panes_restored"], final.Job.Result)
	}

	calls, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("fake tmux was never invoked — restore did not touch tmux: %v", err)
	}
	if !strings.Contains(string(calls), "new-session") || !strings.Contains(string(calls), session) {
		t.Fatalf("tmux invocations lack new-session for %s:\n%s", session, string(calls))
	}
}

// TestJobDispatchCheckpointRestoreFailure: restoring a checkpoint that does
// not exist must land in FAILED with the real load error.
func TestJobDispatchCheckpointRestoreFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := NewHermeticServer("test")
	defer srv.Stop()

	env := postJob(t, srv, `{"type":"checkpoint_restore","params":{"session":"jobrestoregone","checkpoint_id":"cp-missing-1"}}`)
	final := pollJobTerminal(t, srv, env.Job.ID)

	if JobStatus(final.Job.Status) != JobStatusFailed {
		t.Fatalf("missing-checkpoint job status = %s, want failed", final.Job.Status)
	}
	if !strings.Contains(final.Job.Error, "load checkpoint") {
		t.Fatalf("job error %q does not carry the real load failure", final.Job.Error)
	}
}

// TestJobsNotImplementedTypes (D5b, bd-ws3-contract-breadth-psvyu.5.2): every
// job type outside the three-op allow-list gets an honest NOT_IMPLEMENTED
// envelope at POST time — no job is created, nothing is faked. The legacy
// simulator types are covered explicitly: they used to be "accepted" and then
// simulated with a time.Sleep.
func TestJobsNotImplementedTypes(t *testing.T) {
	srv := NewHermeticServer("test")
	defer srv.Stop()

	for _, jobType := range []string{"spawn", "scan", "checkpoint", "import", "export", "made-up-type"} {
		body := fmt.Sprintf(`{"type":%q}`, jobType)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("POST jobs type=%q status = %d, want 501 (body=%s)", jobType, rec.Code, rec.Body.String())
		}
		var resp struct {
			Success bool   `json:"success"`
			Code    string `json:"error_code"`
			Details struct {
				ImplementedTypes []string `json:"implemented_types"`
			} `json:"details"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode envelope for %q: %v (body=%s)", jobType, err, rec.Body.String())
		}
		if resp.Success {
			t.Fatalf("type=%q: success=true on a NOT_IMPLEMENTED response", jobType)
		}
		if resp.Code != ErrCodeNotImplemented {
			t.Fatalf("type=%q: code = %q, want %q (body=%s)", jobType, resp.Code, ErrCodeNotImplemented, rec.Body.String())
		}
		if len(resp.Details.ImplementedTypes) != len(implementedJobTypes) {
			t.Fatalf("type=%q: implemented_types = %v, want %v", jobType, resp.Details.ImplementedTypes, implementedJobTypes)
		}
	}

	// No jobs may exist after the rejected POSTs.
	if jobs := srv.jobStore.List(); len(jobs) != 0 {
		t.Fatalf("rejected job types still created %d job(s): %#v", len(jobs), jobs)
	}
}

// TestNoSimulatorInProduction is the in-package guard for the G5 grep-gate:
// the executeJob time.Sleep simulator must not exist outside _test.go files.
// (The broader `in production,? this would` regex is enforced by
// scripts/guards/placebo_lint.sh against ci/allowlists/placebo.txt — the D5
// waiver line is gone, so the simulator comment can never come back waived.)
func TestNoSimulatorInProduction(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	re := regexp.MustCompile(`Simulate job execution|func \(s \*Server\) executeJob`)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if loc := re.Find(data); loc != nil {
			t.Errorf("%s: simulator artifact %q present in non-test code", name, string(loc))
		}
	}
}
