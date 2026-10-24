//go:build unix

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
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
	workflow, err := yaml.Marshal(&pipeline.Workflow{
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
	workflow, err := yaml.Marshal(&pipeline.Workflow{
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

func TestPipelineResumeHTTPRecoversConfirmedDeliveryWithoutResending(t *testing.T) {
	root := t.TempDir()
	workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "http-agent-recovery", Steps: []pipeline.Step{
		{ID: "agent", Pane: pipeline.PaneSpec{Index: 1}, Prompt: "Work on this HTTP request", Wait: pipeline.WaitTime, Timeout: pipeline.Duration{Duration: time.Second}},
		{ID: "consume", Command: "cat", Stdin: "${steps.agent.output}", DependsOn: []string{"agent"}},
	}}
	frozen, workflowPath, err := pipeline.SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	mock := pipeline.NewMockTmuxClient(tmux.Pane{ID: "%17", Index: 1, NTMIndex: 1, PID: 2718, Type: tmux.AgentClaude, Width: 120, Height: 40})
	t.Cleanup(mock.Reset)
	if err := mock.SetPaneOutput("%17", "OLD-ANSWER\n"); err != nil {
		t.Fatal(err)
	}
	cfg := pipeline.DefaultExecutorConfig("delivery-session")
	cfg.ProjectDir, cfg.WorkflowFile, cfg.RunID = root, workflowPath, pipeline.GenerateRunID()
	executor := pipeline.NewExecutor(cfg)
	executor.SetTmuxClient(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	watchDone := make(chan struct{})
	delivered := false
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := pipeline.LoadState(root, cfg.RunID)
				if err == nil && state.AgentDeliveries["agent"].Status == "delivered" {
					delivered = true
					cancel()
					return
				}
			}
		}
	}()
	state, runErr := executor.Run(ctx, frozen, nil, nil)
	cancel()
	<-watchDone
	if !delivered || runErr == nil || state == nil || state.Status != pipeline.StatusCancelled {
		t.Fatalf("initial dispatch did not stop after confirmed delivery: delivered=%t state=%+v err=%v", delivered, state, runErr)
	}
	history, err := mock.PasteHistory("%17")
	if err != nil || len(history) != 1 || len(mock.VerificationHistory()) != 1 {
		t.Fatalf("initial dispatch was not sent and verified exactly once: pastes=%+v verifications=%+v err=%v", history, mock.VerificationHistory(), err)
	}
	prior, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || prior.AgentDeliveries["agent"].Status != "delivered" {
		t.Fatalf("initial delivery was not durably resumable: %+v %v", prior, err)
	}
	if err := os.WriteFile(filepath.Join(root, "recovery-pane-output"), []byte(prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_ROOT", root)
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_HTTP_PIPELINE_RECOVERY_ROOT/recovery-tmux-calls"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  list-panes) echo '%17_NTM_SEP_1_NTM_SEP_delivery-session__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_2718_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0' ;;
  capture-pane) cat "$NTM_HTTP_PIPELINE_RECOVERY_ROOT/recovery-pane-output" ;;
  display-message) printf '%s\n' "$NTM_HTTP_PIPELINE_RECOVERY_ROOT" ;;
  load-buffer|paste-buffer|send-keys) exit 97 ;;
esac
`
	bin := filepath.Join(root, "recovery-tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	srv := &Server{projectDir: root}
	request := pipelineControlRequest(http.MethodPost, prior.RunID)
	resumeCtx, cancelResume := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancelResume()
	recorder := httptest.NewRecorder()
	srv.handleResumePipeline(recorder, request.WithContext(resumeCtx))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP recovery failed: %d %s", recorder.Code, recorder.Body.String())
	}
	final, err := pipeline.LoadState(root, prior.RunID)
	if err != nil || final.Status != pipeline.StatusCompleted {
		t.Fatalf("HTTP recovery did not persist completion: %+v %v", final, err)
	}
	for _, stepID := range []string{"agent", "consume"} {
		if got := strings.TrimSpace(final.Steps[stepID].Output); got != "FRESH-HTTP-ANSWER" {
			t.Fatalf("HTTP recovery %s used stale or missing response: %q", stepID, got)
		}
	}
	if receipt := final.AgentDeliveries["agent"]; receipt.Status != "completed" || receipt.DeliveredAt != prior.AgentDeliveries["agent"].DeliveredAt {
		t.Fatalf("HTTP resume did not complete the original delivery: %+v", receipt)
	}
	calls, err := os.ReadFile(filepath.Join(root, "recovery-tmux-calls"))
	if err != nil || !strings.Contains(string(calls), "capture-pane") {
		t.Fatalf("HTTP resume did not observe original pane: %s %v", calls, err)
	}
	for _, transport := range []string{"load-buffer", "paste-buffer", "send-keys"} {
		if strings.Contains(string(calls), transport) {
			t.Fatalf("HTTP resume repeated transport %s: %s", transport, calls)
		}
	}
}

type pipelineRecoveryHTTPResponse struct {
	Success    bool                                  `json:"success"`
	ErrorCode  string                                `json:"error_code"`
	RunID      string                                `json:"run_id"`
	WorkflowID string                                `json:"workflow_id"`
	Session    string                                `json:"session"`
	Deliveries []pipeline.AgentDeliveryRecoveryEntry `json:"deliveries"`
	Delivery   pipeline.AgentDeliveryRecoveryEntry   `json:"delivery"`
	Replayed   bool                                  `json:"replayed"`
}

type pipelineRecoveryHTTPBodyBarrier struct {
	*strings.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *pipelineRecoveryHTTPBodyBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.Reader.Read(p)
}

func pipelineRecoveryHTTPRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer pipeline-recovery-test-key")
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func pipelineRecoveryHTTPServe(t *testing.T, srv *Server, req *http.Request) (*httptest.ResponseRecorder, pipelineRecoveryHTTPResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out pipelineRecoveryHTTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid recovery response: %d %s: %v", rec.Code, rec.Body, err)
	}
	return rec, out
}

func pipelineRecoveryHTTPBody(t *testing.T, token, reason string) string {
	t.Helper()
	body, err := json.Marshal(PipelineRecoverRequest{StepID: "agent", Receipt: token, AcceptDelivery: true, Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func pipelineRecoveryHTTPJournal(t *testing.T, root, runID string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".ntm", "pipelines", runID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Produce a genuine uncertain dispatch: the transport pastes once but its
// submission acknowledgment fails. Tests recover the executor's durable journal,
// rather than inventing a receipt or bypassing production admission checks.
func pipelineRecoveryHTTPCheckpoint(t *testing.T, root, kind string) *pipeline.ExecutionState {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	step := pipeline.Step{ID: "agent", Pane: pipeline.PaneSpec{Index: 1}, Prompt: "Work on this HTTP request", Wait: pipeline.WaitTime, Timeout: pipeline.Duration{Duration: time.Second}}
	if kind == "template" {
		path := filepath.Join(root, "review.md")
		if err := os.WriteFile(path, []byte("Work on this HTTP template request"), 0600); err != nil {
			t.Fatal(err)
		}
		step.Prompt, step.Template = "", path
	}
	workflow := &pipeline.Workflow{SchemaVersion: pipeline.SchemaVersion, Name: "http-uncertain-recovery", Steps: []pipeline.Step{
		step, {ID: "consume", Command: "cat", Stdin: "${steps.agent.output}", DependsOn: []string{"agent"}},
	}}
	frozen, path, err := pipeline.SnapshotWorkflow(context.Background(), root, workflow)
	if err != nil {
		t.Fatal(err)
	}
	mock := pipeline.NewMockTmuxClient(tmux.Pane{ID: "%17", Index: 1, NTMIndex: 1, PID: 2718, Type: tmux.AgentClaude, Width: 120, Height: 40})
	t.Cleanup(mock.Reset)
	if err := mock.SetPaneOutput("%17", "OLD-ANSWER\n"); err != nil {
		t.Fatal(err)
	}
	mock.SetSubmissionVerifier(func(_, _, _ string) error { return errors.New("submission acknowledgment was lost") })
	cfg := pipeline.DefaultExecutorConfig("delivery-session")
	cfg.ProjectDir, cfg.WorkflowFile, cfg.RunID = root, path, pipeline.GenerateRunID()
	executor := pipeline.NewExecutor(cfg)
	executor.SetTmuxClient(mock)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, runErr := executor.Run(ctx, frozen, nil, nil)
	if runErr == nil || state == nil || state.Status != pipeline.StatusFailed {
		t.Fatalf("dispatch did not fail after uncertain submission: %+v %v", state, runErr)
	}
	history, err := mock.PasteHistory("%17")
	if err != nil || len(history) != 1 || len(mock.VerificationHistory()) != 1 {
		t.Fatalf("initial dispatch must paste and verify exactly once: %+v %v", history, err)
	}
	prior, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || prior.AgentDeliveries["agent"].Status != "sending" {
		t.Fatalf("uncertain delivery was not persisted: %+v %v", prior, err)
	}
	return prior
}

func pipelineRecoveryHTTPTmux(t *testing.T, root, transcript string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "recovery-pane-output"), []byte(transcript), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_ROOT", root)
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_PID", "2718")
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_HANG", "0")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_HTTP_PIPELINE_RECOVERY_ROOT/recovery-tmux-calls"
case "$1" in
  -V) echo 'tmux 3.4' ;;
  list-sessions) echo 'delivery-session_NTM_SEP_1_NTM_SEP_0_NTM_SEP_1700000000' ;;
  list-panes) printf '%%17_NTM_SEP_1_NTM_SEP_delivery-session__cc_1_NTM_SEP_claude_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$NTM_HTTP_PIPELINE_RECOVERY_PID" ;;
  capture-pane)
    if [ "$NTM_HTTP_PIPELINE_RECOVERY_HANG" = 1 ]; then exec sleep 30; fi
    cat "$NTM_HTTP_PIPELINE_RECOVERY_ROOT/recovery-pane-output" ;;
  display-message) printf '%s\n' "$NTM_HTTP_PIPELINE_RECOVERY_ROOT" ;;
  load-buffer|paste-buffer|send-keys) exit 97 ;;
esac
`
	bin := filepath.Join(root, "recovery-tmux")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	return filepath.Join(root, "recovery-tmux-calls")
}

func pipelineRecoveryHTTPAssertNoSend(t *testing.T, logPath string) {
	t.Helper()
	calls, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, transport := range []string{"load-buffer", "paste-buffer", "send-keys"} {
		if strings.Contains(string(calls), transport) {
			t.Fatalf("recovery repeated prompt transport %s: %s", transport, calls)
		}
	}
}

func TestPipelineRecoverHTTPAcceptsThenResumesWithoutResending(t *testing.T) {
	for _, kind := range []string{"prompt", "template"} {
		t.Run(kind, func(t *testing.T) {
			srv, _ := setupTestServer(t)
			srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
			root := srv.projectDir
			prior := pipelineRecoveryHTTPCheckpoint(t, root, kind)
			original := prior.AgentDeliveries["agent"]
			logPath := pipelineRecoveryHTTPTmux(t, root, original.BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
			path := "/api/v1/pipelines/" + prior.RunID + "/recover"
			before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			rec, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
			if rec.Code != http.StatusOK || !inspection.Success || len(inspection.Deliveries) != 1 {
				t.Fatalf("inspection failed: %d %s", rec.Code, rec.Body)
			}
			entry := inspection.Deliveries[0]
			if entry.StepID != "agent" || entry.Kind != kind || entry.Status != "sending" || !entry.CanAccept || entry.PaneID != "%17" || entry.PanePID != 2718 || len(entry.ReceiptToken) != 64 {
				t.Fatalf("wrong recovery identity: %+v", entry)
			}
			for _, hidden := range []string{"OLD-ANSWER", "FRESH-HTTP-ANSWER", "Work on this", "before_output", "prompt_hash", "workflow_file"} {
				if strings.Contains(rec.Body.String(), hidden) {
					t.Fatalf("inspection leaked %q: %s", hidden, rec.Body)
				}
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatal("read-only inspection changed the delivery journal")
			}
			if calls, err := os.ReadFile(logPath); err == nil && len(calls) > 0 {
				t.Fatalf("read-only inspection contacted tmux: %s", calls)
			}
			const reason = "Observed the original agent answer the submitted request"
			body := pipelineRecoveryHTTPBody(t, entry.ReceiptToken, reason)
			rec, accepted := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
			if rec.Code != http.StatusOK || !accepted.Success || accepted.Replayed || accepted.Delivery.Status != "delivered" || accepted.Delivery.ResolutionAction != pipeline.AgentDeliveryResolutionAccept || accepted.Delivery.ResolutionReason != reason || accepted.Delivery.ResolvedAt.IsZero() {
				t.Fatalf("acceptance failed: %d %s", rec.Code, rec.Body)
			}
			saved, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || saved.Status != prior.Status || saved.Steps["consume"].Status == pipeline.StatusCompleted {
				t.Fatalf("acceptance executed or completed pipeline work: %+v %v", saved, err)
			}
			delivery := saved.AgentDeliveries["agent"]
			if delivery.BeforeOutput != original.BeforeOutput || delivery.Output != original.Output || !delivery.StartedAt.Equal(original.StartedAt) {
				t.Fatalf("acceptance replaced original evidence: %+v", delivery)
			}
			acceptedBytes := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			rec, replay := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
			if rec.Code != http.StatusOK || !replay.Replayed || replay.Delivery != accepted.Delivery || !bytes.Equal(acceptedBytes, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatalf("durable acceptance replay changed its decision: %d %s", rec.Code, rec.Body)
			}
			resume := pipelineRecoveryHTTPRequest(http.MethodPost, "/api/v1/pipelines/"+prior.RunID+"/resume", "{}")
			ctx, cancel := context.WithTimeout(resume.Context(), 10*time.Second)
			defer cancel()
			rec, _ = pipelineRecoveryHTTPServe(t, srv, resume.WithContext(ctx))
			if rec.Code != http.StatusOK {
				t.Fatalf("ordinary HTTP resume failed: %d %s", rec.Code, rec.Body)
			}
			final, err := pipeline.LoadState(root, prior.RunID)
			if err != nil || final.Status != pipeline.StatusCompleted || final.AgentDeliveries["agent"].Status != "completed" {
				t.Fatalf("resume did not complete original delivery: %+v %v", final, err)
			}
			for _, stepID := range []string{"agent", "consume"} {
				if got := strings.TrimSpace(final.Steps[stepID].Output); got != "FRESH-HTTP-ANSWER" {
					t.Fatalf("%s consumed stale or missing output: %q", stepID, got)
				}
			}
			// An immutable decision remains replayable after completion, even
			// when the old physical agent process has since been replaced.
			t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_PID", "9999")
			completedBytes := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			rec, replay = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
			if rec.Code != http.StatusOK || !replay.Replayed || replay.Delivery.Status != "completed" || replay.Delivery.ResolvedAt != accepted.Delivery.ResolvedAt || !bytes.Equal(completedBytes, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatalf("completed receipt could not replay its saved decision: %d %s", rec.Code, rec.Body)
			}
			pipelineRecoveryHTTPAssertNoSend(t, logPath)
		})
	}
}

func TestPipelineRecoverHTTPRefusalsPreserveJournal(t *testing.T) {
	for _, scenario := range []string{"stale receipt", "changed pane lifetime", "lost output boundary", "live owner", "conflicting reason", "missing workflow"} {
		t.Run(scenario, func(t *testing.T) {
			srv, _ := setupTestServer(t)
			srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
			root := srv.projectDir
			prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
			logPath := pipelineRecoveryHTTPTmux(t, root, prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
			path := "/api/v1/pipelines/" + prior.RunID + "/recover"
			_, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
			if len(inspection.Deliveries) != 1 {
				t.Fatalf("missing receipt: %+v", inspection)
			}
			token, reason := inspection.Deliveries[0].ReceiptToken, "Observed the original response"
			wantStatus, wantCode := http.StatusConflict, ErrCodeRecoveryRefused
			switch scenario {
			case "stale receipt":
				if token[0] == '0' {
					token = "1" + token[1:]
				} else {
					token = "0" + token[1:]
				}
			case "changed pane lifetime":
				t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_PID", "9999")
			case "lost output boundary":
				if err := os.WriteFile(filepath.Join(root, "recovery-pane-output"), []byte("UNRELATED-REPLACEMENT-SCREEN"), 0600); err != nil {
					t.Fatal(err)
				}
			case "live owner":
				owner, err := pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(owner.Close)
				// Ownership must win even when reading the checkpoint would fail.
				if err := os.WriteFile(filepath.Join(root, ".ntm", "pipelines", prior.RunID+".json"), []byte("not a checkpoint"), 0600); err != nil {
					t.Fatal(err)
				}
				wantCode = ErrCodePipelineRunning
			case "conflicting reason":
				rec, accepted := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, pipelineRecoveryHTTPBody(t, token, reason)))
				if rec.Code != http.StatusOK || accepted.Replayed {
					t.Fatalf("initial acceptance failed: %d %s", rec.Code, rec.Body)
				}
				reason = "A different operator explanation"
			case "missing workflow":
				// Keep a valid delivery fingerprint; only the frozen asset is
				// unavailable. This must not become PIPELINE_NOT_FOUND.
				if err := os.Rename(prior.WorkflowFile, prior.WorkflowFile+".unavailable"); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantCode = http.StatusServiceUnavailable, ErrCodeServiceUnavail
			}
			before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			rec, out := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, pipelineRecoveryHTTPBody(t, token, reason)))
			if rec.Code != wantStatus || out.Success || out.ErrorCode != wantCode {
				t.Fatalf("unsafe recovery = %d %s, want %d %s", rec.Code, rec.Body, wantStatus, wantCode)
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatal("refused acceptance rewrote the checkpoint")
			}
			pipelineRecoveryHTTPAssertNoSend(t, logPath)
		})
	}
}

func TestPipelineRecoverHTTPRejectsMalformedDecisions(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	_, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if len(inspection.Deliveries) != 1 {
		t.Fatalf("missing receipt: %+v", inspection)
	}
	token := inspection.Deliveries[0].ReceiptToken
	valid := pipelineRecoveryHTTPBody(t, token, "Observed the response")
	cases := map[string]string{
		"empty": "", "null": "null", "array": "[]", "trailing document": valid + " {}", "trailing garbage": valid + " x",
		"missing step":       strings.Replace(valid, `"step_id":"agent",`, "", 1),
		"blank step":         strings.Replace(valid, `"step_id":"agent"`, `"step_id":" "`, 1),
		"inexact step":       strings.Replace(valid, `"step_id":"agent"`, `"step_id":" agent"`, 1),
		"missing receipt":    strings.Replace(valid, `"receipt":"`+token+`",`, "", 1),
		"short receipt":      strings.Replace(valid, token, "abcd", 1),
		"uppercase receipt":  strings.Replace(valid, token, strings.ToUpper(token), 1),
		"missing acceptance": strings.Replace(valid, `"accept_delivery":true,`, "", 1),
		"false acceptance":   strings.Replace(valid, `"accept_delivery":true`, `"accept_delivery":false`, 1),
		"null acceptance":    strings.Replace(valid, `"accept_delivery":true`, `"accept_delivery":null`, 1),
		"string acceptance":  strings.Replace(valid, `"accept_delivery":true`, `"accept_delivery":"true"`, 1),
		"missing reason":     strings.Replace(valid, `,"reason":"Observed the response"`, "", 1),
		"blank reason":       strings.Replace(valid, `"Observed the response"`, `" "`, 1),
		"null reason":        strings.Replace(valid, `"Observed the response"`, `null`, 1),
		"long reason":        pipelineRecoveryHTTPBody(t, token, strings.Repeat("r", 4097)),
		"project override":   strings.TrimSuffix(valid, "}") + `,"project_dir":"/another-project"}`,
		"session override":   strings.TrimSuffix(valid, "}") + `,"session":"another-session"}`,
		"dry run":            strings.TrimSuffix(valid, "}") + `,"dry_run":true}`,
		"oversized body":     strings.Repeat(" ", pipelineRecoveryMaxBodyBytes+1) + valid,
	}
	before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec, out := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
			if rec.Code != http.StatusBadRequest || out.Success || out.ErrorCode != ErrCodeBadRequest {
				t.Fatalf("malformed acceptance = %d %s", rec.Code, rec.Body)
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatal("malformed request changed the checkpoint")
			}
		})
	}
	for _, contentType := range []string{"", "text/plain", "application/jsonpatch"} {
		t.Run("content type "+contentType, func(t *testing.T) {
			req := pipelineRecoveryHTTPRequest(http.MethodPost, path, valid)
			req.Header.Set("Content-Type", contentType)
			rec, out := pipelineRecoveryHTTPServe(t, srv, req)
			if rec.Code != http.StatusBadRequest || out.ErrorCode != ErrCodeBadRequest {
				t.Fatalf("non-JSON content type bypassed validation: %d %s", rec.Code, rec.Body)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec, out := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(method, path+"?project=/another-project", valid))
		if rec.Code != http.StatusBadRequest || out.ErrorCode != ErrCodeBadRequest {
			t.Fatalf("request project override was accepted: %d %s", rec.Code, rec.Body)
		}
		rec, out = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(method, "/api/v1/pipelines/missing-run/recover", valid))
		if rec.Code != http.StatusNotFound || out.ErrorCode != ErrCodePipelineNotFound {
			t.Fatalf("missing run not distinguished: %d %s", rec.Code, rec.Body)
		}
	}
	if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
		t.Fatal("invalid request changed the original checkpoint")
	}
}

func TestPipelineRecoverHTTPAuthenticationAndProjectScope(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleViewer}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
	req := pipelineRecoveryHTTPRequest(http.MethodGet, path, "")
	req.Header.Del("Authorization")
	rec, out := pipelineRecoveryHTTPServe(t, srv, req)
	if rec.Code != http.StatusUnauthorized || out.Success {
		t.Fatalf("anonymous recovery inspection = %d %s", rec.Code, rec.Body)
	}
	rec, out = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if rec.Code != http.StatusOK || len(out.Deliveries) != 1 {
		t.Fatalf("viewer cannot inspect recovery: %d %s", rec.Code, rec.Body)
	}
	body := pipelineRecoveryHTTPBody(t, out.Deliveries[0].ReceiptToken, "Observed the response")
	for _, project := range []string{root, ""} {
		srv.projectDir = project
		rec, out = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
		if rec.Code != http.StatusForbidden || out.ErrorCode != ErrCodeForbidden {
			t.Fatalf("viewer acceptance bypassed permission check: %d %s", rec.Code, rec.Body)
		}
	}
	srv.auth.Role = RoleOperator
	// A valid run in cwd must not substitute for an unconfigured project.
	t.Chdir(root)
	file := filepath.Join(root, "not-a-project")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"", ".", filepath.Join(root, "absent-project"), file} {
		srv.projectDir = project
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec, out = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(method, path, body))
			if rec.Code != http.StatusServiceUnavailable || out.ErrorCode != ErrCodeServiceUnavail {
				t.Fatalf("invalid configured project %q fell back to cwd: %d %s", project, rec.Code, rec.Body)
			}
		}
	}
	if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
		t.Fatal("denied or unscoped recovery changed the checkpoint")
	}
}

func TestPipelineRecoverHTTPIdempotencyIsProjectScoped(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleAdmin}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	logPath := pipelineRecoveryHTTPTmux(t, root, prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
	other := t.TempDir()
	if err := pipeline.SaveState(other, &pipeline.ExecutionState{RunID: prior.RunID, WorkflowID: "other-project-workflow", Session: "other-session", Status: pipeline.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	otherBefore := pipelineRecoveryHTTPJournal(t, other, prior.RunID)
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	_, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if len(inspection.Deliveries) != 1 {
		t.Fatalf("missing receipt: %+v", inspection)
	}
	body := pipelineRecoveryHTTPBody(t, inspection.Deliveries[0].ReceiptToken, "Observed the response")
	post := func() (*httptest.ResponseRecorder, pipelineRecoveryHTTPResponse) {
		req := pipelineRecoveryHTTPRequest(http.MethodPost, path, body)
		req.Header.Set("Idempotency-Key", "same-recovery-key")
		return pipelineRecoveryHTTPServe(t, srv, req)
	}
	patch := func(project string) {
		t.Helper()
		data, err := json.Marshal(map[string]string{"project_dir": project})
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPatch, "/api/v1/config", string(data)))
		if rec.Code != http.StatusOK {
			t.Fatalf("configuration patch failed: %d %s", rec.Code, rec.Body)
		}
	}
	// Pause a real HTTP request as idempotency reads its body, after namespace
	// capture and before the handler runs. A concurrent configuration PATCH
	// must change neither this operation's target nor its cache namespace.
	barrier := &pipelineRecoveryHTTPBodyBarrier{Reader: strings.NewReader(body), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer unblock()
	req := pipelineRecoveryHTTPRequest(http.MethodPost, path, body)
	req.Body = io.NopCloser(barrier)
	req.Header.Set("Idempotency-Key", "same-recovery-key")
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.router.ServeHTTP(rec, req.WithContext(ctx))
	}()
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached body admission")
	}
	patch(other)
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after configuration changed")
	}
	var accepted pipelineRecoveryHTTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil || rec.Code != http.StatusOK || accepted.Replayed || accepted.WorkflowID != prior.WorkflowID || rec.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatalf("configuration patch redirected in-flight acceptance: %d %s: %v", rec.Code, rec.Body, err)
	}
	acceptedBytes := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
	rec, out := post()
	if rec.Code != http.StatusConflict || out.ErrorCode != ErrCodeRecoveryRefused || rec.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatalf("another project replayed the original acceptance: %d %s", rec.Code, rec.Body)
	}
	rec, inspection = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if rec.Code != http.StatusOK || inspection.WorkflowID != "other-project-workflow" || len(inspection.Deliveries) != 0 {
		t.Fatalf("inspection escaped the configured namespace: %d %s", rec.Code, rec.Body)
	}
	patch(root)
	rec, out = post()
	if rec.Code != http.StatusOK || rec.Header().Get("X-Idempotent-Replay") != "true" || out.Delivery != accepted.Delivery {
		t.Fatalf("original project lost its normal HTTP replay: %d %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(otherBefore, pipelineRecoveryHTTPJournal(t, other, prior.RunID)) || !bytes.Equal(acceptedBytes, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
		t.Fatal("project changes or cache replays mutated either journal")
	}
	pipelineRecoveryHTTPAssertNoSend(t, logPath)
}

func TestPipelineRecoverHTTPCancellationPreservesReceipt(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	logPath := pipelineRecoveryHTTPTmux(t, root, prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	_, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if len(inspection.Deliveries) != 1 {
		t.Fatalf("missing receipt: %+v", inspection)
	}
	body := pipelineRecoveryHTTPBody(t, inspection.Deliveries[0].ReceiptToken, "Observed the response")
	before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_HANG", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := pipelineRecoveryHTTPRequest(http.MethodPost, path, body).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.router.ServeHTTP(rec, req)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		calls, _ := os.ReadFile(logPath)
		if strings.Contains(string(calls), "capture-pane") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("recovery never reached the cancellable pane capture: %d %s", rec.Code, rec.Body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not stop the original pane capture")
	}
	var out pipelineRecoveryHTTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusGatewayTimeout || out.ErrorCode != ErrCodeTimeout {
		t.Fatalf("canceled recovery = %d %s: %v", rec.Code, rec.Body, err)
	}
	if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
		t.Fatal("canceled inspection persisted an acceptance")
	}
	// The same receipt can be accepted after cancellation: both the durable
	// run owner and the shared physical-pane lock must have been released.
	t.Setenv("NTM_HTTP_PIPELINE_RECOVERY_HANG", "0")
	rec, out = pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
	if rec.Code != http.StatusOK || out.Replayed {
		t.Fatalf("canceled request retained ownership or accepted late: %d %s", rec.Code, rec.Body)
	}
	pipelineRecoveryHTTPAssertNoSend(t, logPath)
}

func TestPipelineRecoverHTTPPreservesConfiguredRedaction(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
	srv.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	logPath := pipelineRecoveryHTTPTmux(t, root, prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	_, inspection := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if len(inspection.Deliveries) != 1 {
		t.Fatalf("redaction lost inspection receipt: %+v", inspection)
	}
	token := inspection.Deliveries[0].ReceiptToken
	canonical, err := pipeline.InspectAgentDeliveryRecovery(context.Background(), root, prior.RunID)
	if err != nil || token != canonical.Deliveries[0].ReceiptToken {
		t.Fatalf("redaction changed the exact receipt token: %q %v", token, err)
	}
	const secret = "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	body := pipelineRecoveryHTTPBody(t, token, "Observed original response using "+secret)
	for attempt := 0; attempt < 2; attempt++ {
		rec, out := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
		if rec.Code != http.StatusOK || out.Replayed != (attempt == 1) || out.Delivery.ReceiptToken != token || strings.Contains(rec.Body.String(), secret) || !strings.Contains(out.Delivery.ResolutionReason, "[REDACTED:") {
			t.Fatalf("redacted acceptance/replay failed: %d %s", rec.Code, rec.Body)
		}
		persisted, err := pipeline.LoadState(root, prior.RunID)
		if err != nil || persisted.AgentDeliveries["agent"].ResolutionReason != out.Delivery.ResolutionReason {
			t.Fatalf("durable decision and wire reason differ: %+v %v", persisted, err)
		}
		if bytes.Contains(pipelineRecoveryHTTPJournal(t, root, prior.RunID), []byte(secret)) {
			t.Fatal("operator reason bypassed configured request redaction")
		}
	}
	pipelineRecoveryHTTPAssertNoSend(t, logPath)
}
