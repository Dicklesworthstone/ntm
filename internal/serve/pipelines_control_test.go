//go:build unix

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func pipelineControlRequest(method, runID string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/pipelines/"+runID, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", runID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
}

func TestPipelineResumeRejectsLiveOwnerBeforeCheckpointMutation(t *testing.T) {
	dir := t.TempDir()
	runID := pipeline.GenerateRunID()
	workflowPath := filepath.Join(dir, "owned.yaml")
	workflow, err := json.Marshal(&pipeline.Workflow{
		SchemaVersion: "2.0", Name: "owned",
		Steps: []pipeline.Step{{ID: "work", Command: "true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, workflow, 0600); err != nil {
		t.Fatal(err)
	}
	state := &pipeline.ExecutionState{RunID: runID, WorkflowID: "owned", WorkflowFile: workflowPath, Session: "owned", Status: pipeline.StatusRunning}
	if err := pipeline.SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".ntm", "pipelines", runID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), dir, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	srv := &Server{projectDir: dir}
	rec := httptest.NewRecorder()
	srv.handleResumePipeline(rec, pipelineControlRequest(http.MethodPost, runID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("concurrent resume = %d %s, want conflict", rec.Code, rec.Body.String())
	}
	var response struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ErrorCode != ErrCodePipelineRunning {
		t.Fatalf("missing live-owner error: %s %v", rec.Body.String(), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || owner.Context().Err() != nil {
		t.Fatalf("rejected resume mutated checkpoint or canceled owner: %v", err)
	}
}

func TestPipelineResumeReloadsCheckpointInsideOwnership(t *testing.T) {
	dir := t.TempDir()
	runID := pipeline.GenerateRunID()
	marker := filepath.Join(dir, "must-not-repeat")
	workflowPath := filepath.Join(dir, "resume.yaml")
	workflow, err := json.Marshal(&pipeline.Workflow{
		SchemaVersion: "2.0", Name: "completed-work",
		Steps: []pipeline.Step{{ID: "completed", Command: fmt.Sprintf("echo duplicate > %q", marker)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, workflow, 0600); err != nil {
		t.Fatal(err)
	}
	// This is the caller's old view. Another attempt has since completed the
	// step and checkpointed it; passing this snapshot must not replay work.
	stale := &pipeline.ExecutionState{
		RunID: runID, WorkflowID: "completed-work", WorkflowFile: workflowPath,
		Session: "resumecontrol", Status: pipeline.StatusFailed,
		Variables: map[string]interface{}{}, Steps: map[string]pipeline.StepResult{},
	}
	latest := *stale
	latest.Steps = map[string]pipeline.StepResult{"completed": {Status: pipeline.StatusCompleted}}
	if err := pipeline.SaveState(dir, &latest); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectDir: dir}
	out := srv.resumePipelineWithResult(context.Background(), runID, stale.Session, nil, stale)
	if !out.Success || out.Status != "completed" || out.RunID != runID {
		t.Fatalf("resume from fresh checkpoint failed: %+v", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("resume replayed already completed work: %v", err)
	}
}

func TestPipelineCancelRequiresAcknowledgingOwner(t *testing.T) {
	dir := t.TempDir()
	runID := pipeline.GenerateRunID()
	state := &pipeline.ExecutionState{RunID: runID, WorkflowID: "orphan", Session: "orphan", Status: pipeline.StatusRunning}
	if err := pipeline.SaveState(dir, state); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectDir: dir}
	rec := httptest.NewRecorder()
	srv.handleCancelPipeline(rec, pipelineControlRequest(http.MethodDelete, runID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("orphan status was mistaken for live cancellation: %d %s", rec.Code, rec.Body.String())
	}
	stillRunning, err := pipeline.LoadState(dir, runID)
	if err != nil || stillRunning.Status != pipeline.StatusRunning {
		t.Fatalf("controller forged a terminal state for an unacknowledged request: %+v %v", stillRunning, err)
	}
}

func TestPipelineCancelReachesSeparateServerProcess(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "command-running")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPipelineRemoteControlProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_PIPELINE_CONTROL_HELPER=1", "NTM_PIPELINE_CONTROL_ROOT="+dir, "NTM_PIPELINE_CONTROL_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { defer close(done); done <- cmd.Wait() }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote workflow did not start actual command work")
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".ntm", "pipelines"))
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			runID = entry.Name()[:len(entry.Name())-5]
			break
		}
	}
	if runID == "" {
		t.Fatal("remote execution did not persist an inspectable run")
	}
	// No local registration and cwd is not dir: the configured project must
	// supply the snapshot, and only the other process can acknowledge cancel.
	srv := &Server{projectDir: dir}
	inspect := httptest.NewRecorder()
	srv.handleGetPipeline(inspect, pipelineControlRequest(http.MethodGet, runID))
	if inspect.Code != http.StatusOK {
		t.Fatalf("remote inspect: %d %s", inspect.Code, inspect.Body.String())
	}
	rec := httptest.NewRecorder()
	srv.handleCancelPipeline(rec, pipelineControlRequest(http.MethodDelete, runID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("remote cancellation: %d %s, want acknowledged 202", rec.Code, rec.Body.String())
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Status != "cancellation_requested" {
		t.Fatalf("acknowledgment overclaimed termination: %s %v", rec.Body.String(), err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("owning server did not stop real work: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("controller changed bookkeeping but the remote execution kept running")
	}
	final, err := pipeline.LoadState(dir, runID)
	if err != nil || final.Status != pipeline.StatusCancelled {
		t.Fatalf("owning executor did not checkpoint cancellation: %+v %v", final, err)
	}
}

func TestPipelineRemoteControlProcessHelper(t *testing.T) {
	if os.Getenv("NTM_PIPELINE_CONTROL_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	srv := &Server{projectDir: os.Getenv("NTM_PIPELINE_CONTROL_ROOT")}
	workflow := &pipeline.Workflow{
		SchemaVersion: "2.0", Name: "remote-control",
		Steps: []pipeline.Step{{ID: "gate", Command: fmt.Sprintf("echo started > %q; exec sleep 30", os.Getenv("NTM_PIPELINE_CONTROL_READY"))}},
	}
	out := srv.execPipelineInline(context.Background(), workflow, "remotecontrol", nil, false)
	if out.Success || out.RunID == "" {
		t.Fatalf("canceled remote execution reported success: %+v", out)
	}
	state, err := pipeline.LoadState(srv.projectDir, out.RunID)
	if err != nil || state.Status != pipeline.StatusCancelled {
		t.Fatalf("cancellation did not reach executor: %+v %v", state, err)
	}
}
