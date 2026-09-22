package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// Exercise the real admission/dispatch route, not a scheduler assembled only
// by the test. The spawn seam replaces tmux, but request binding, job receipts,
// queued cancellation, cross-operation resources, and HTTP inspection are real.
func TestJobResourceAdmissionHTTPSerializesActualSessionTargets(t *testing.T) {
	for _, tc := range []struct {
		name, target, queuedBody string
	}{
		{"same session", "project", `{"type":"swarm_spawn","session":"project","params":{"cc_count":1}}`},
		{"labelled spawn", "project--dev", `{"type":"swarm_spawn","params":{"session":"project","label":"dev","cc_count":1}}`},
		{"restore target", "project", `{"type":"checkpoint_restore","params":{"session":"archive-source","target_session":"project","checkpoint_id":"saved","force":true}}`},
		{"implicit restore target", "project", `{"type":"checkpoint_restore","params":{"session":"project","checkpoint_id":"saved","force":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewHermeticServer("resource-admission-test")
			t.Cleanup(func() { srv.Stop() })
			srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 3, 4
			gate, _ := resourceGate(t)
			events := make(chan string, 8)
			srv.spawnAgents = func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
				target := opts.Session
				if opts.Label != "" {
					target += "--" + opts.Label
				}
				events <- target
				out := &robot.SpawnOutput{Session: target}
				select {
				case <-ctx.Done():
					return out, ctx.Err()
				case <-gate:
					out.Success = true
					return out, nil
				}
			}
			body, err := json.Marshal(map[string]interface{}{
				"type": JobTypeSwarmSpawn, "params": map[string]interface{}{"session": tc.target, "cc_count": 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			admissionAcceptedJob(t, srv, string(body))
			resourceWait(t, events, tc.target)
			queued := admissionAcceptedJob(t, srv, tc.queuedBody)
			admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"independent","cc_count":1}}`)
			resourceWait(t, events, "independent")
			if job := srv.jobStore.Get(queued.ID); job == nil || job.Status != JobStatusPending {
				t.Fatalf("conflicting operation reached dispatch: %+v", job)
			}
			rec := admissionRequest(srv, http.MethodGet, "/api/v1/jobs", "")
			var listing struct {
				Execution JobExecutionStatus `json:"execution"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listing) != nil {
				t.Fatalf("list jobs: %d %s", rec.Code, rec.Body.String())
			}
			if listing.Execution.Running != 2 || listing.Execution.Queued != 1 || listing.Execution.WaitingForResources != 1 {
				t.Fatalf("resource wait hidden from HTTP clients: %+v", listing.Execution)
			}
			rec = admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+queued.ID, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("cancel resource waiter: %d %s", rec.Code, rec.Body.String())
			}
			if got := srv.jobStore.Get(queued.ID); got == nil || got.Status != JobStatusCancelled {
				t.Fatalf("resource waiter not cancelled: %+v", got)
			}
		})
	}
}

func TestJobResourceAdmissionHTTPRejectsConflictingSessionBinding(t *testing.T) {
	srv := NewHermeticServer("resource-admission-test")
	defer srv.Stop()
	rec := admissionRequest(srv, http.MethodPost, "/api/v1/jobs", `{"type":"swarm_spawn","session":"outer","params":{"session":"inner","cc_count":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous resource binding accepted: %d %s", rec.Code, rec.Body.String())
	}
	if len(srv.jobStore.List()) != 0 {
		t.Fatal("ambiguous resource binding created a receipt")
	}
}

func TestJobResumeResourceAdmissionHTTPBindsSavedRunAndSession(t *testing.T) {
	srv := NewHermeticServer("resume-resource-admission")
	t.Cleanup(func() { srv.Stop() })
	srv.projectDir = resourceResumeProject(t)
	srv.jobExecutor.maxConcurrent, srv.jobExecutor.maxQueued = 3, 5
	prior := &pipeline.ExecutionState{RunID: "saved-run", Session: "saved-session"}
	if err := pipeline.SaveState(srv.projectDir, prior); err != nil {
		t.Fatal(err)
	}
	started, release, calls := admissionBlockSpawn(srv)
	t.Cleanup(release)
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"saved-session","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	queued := admissionAcceptedJob(t, srv, `{"type":"pipeline_resume","params":{"run_id":"saved-run"}}`)
	// The same run into another session must wait for the older resume too.
	second := admissionAcceptedJob(t, srv, `{"type":"pipeline_resume","params":{"run_id":"saved-run","session":"other-session"}}`)
	srv.jobExecutor.mu.Lock()
	var bound context.Context
	for _, work := range srv.jobExecutor.queue {
		if work.id == queued.ID {
			bound = work.ctx
		}
	}
	srv.jobExecutor.mu.Unlock()
	if bound == nil {
		t.Fatal("implicit resume did not wait behind its saved session owner")
	}
	// A resume on another controller can update the saved Session field while
	// this request is queued. Dispatch must keep the originally admitted target.
	prior.Session = "later-session"
	if err := pipeline.SaveState(srv.projectDir, prior); err != nil {
		t.Fatal(err)
	}
	if session, err := applyJobResumeTarget(bound, srv.projectDir, prior.RunID, ""); err != nil || session != "saved-session" {
		t.Fatalf("queued resume followed mutable state: %q %v", session, err)
	}
	admissionAcceptedJob(t, srv, `{"type":"swarm_spawn","params":{"session":"independent","cc_count":1}}`)
	awaitExecutorSignal(t, started)
	if calls.Load() != 2 {
		t.Fatal("unrelated session did not proceed beside recovery waiters")
	}
	if got := srv.jobExecutor.snapshot(); got.Running != 2 || got.Queued != 2 || got.WaitingForResources != 2 {
		t.Fatalf("saved-run resource waits are not visible: %+v", got)
	}
	// Remove younger first so cancelling the first waiter cannot legitimately
	// dispatch the second resume during test teardown.
	for _, id := range []string{second.ID, queued.ID} {
		rec := admissionRequest(srv, http.MethodDelete, "/api/v1/jobs/"+id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel saved-run waiter: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestResumeJobUsesBoundTargetAndFreshExecutionState(t *testing.T) {
	project := resourceResumeProject(t)
	target := resolveJobResumeTarget(project, map[string]interface{}{"run_id": "saved-run"},
		func(string, string) (string, error) { return "admitted-session", nil })
	if target == nil {
		t.Fatal("target not resolved")
	}
	ctx := context.WithValue(context.Background(), jobResumeTargetKey{}, *target)
	prior := &pipeline.ExecutionState{
		RunID: "saved-run", WorkflowID: "workflow", Session: "later-session",
		Variables: map[string]interface{}{"fresh": "latest-checkpoint"},
	}
	called := false
	result, err := executeResumePipelineJob(ctx, project, jobPipelineResumeParams{RunID: "saved-run"},
		func(root, runID string) (*pipeline.ExecutionState, error) {
			if root != project || runID != prior.RunID {
				t.Fatalf("incorrect execution state namespace: %q %q", root, runID)
			}
			return prior, nil
		},
		func(_ context.Context, runID, session string, _ map[string]interface{}, state *pipeline.ExecutionState) pipeline.PipelineRunOutput {
			called = true
			if session != "admitted-session" || state != prior || state.Variables["fresh"] != "latest-checkpoint" {
				t.Fatalf("resume lost target binding or used an admission-time state copy: %q %+v", session, state)
			}
			out := pipeline.PipelineRunOutput{RunID: runID, Session: session, Status: "completed"}
			out.Success = true
			return out
		})
	if err != nil || !called || result["session"] != "admitted-session" {
		t.Fatalf("bound resume failed: %v %v called=%v", result, err, called)
	}
}

func TestResumeJobRejectsStorageDriftBeforeEngineOrStateLoad(t *testing.T) {
	project := resourceResumeProject(t)
	target := resolveJobResumeTarget(project, map[string]interface{}{"run_id": "saved-run", "session": "session"}, nil)
	if target == nil {
		t.Fatal("target not resolved")
	}
	ctx := context.WithValue(context.Background(), jobResumeTargetKey{}, *target)
	stateDir := filepath.Join(project, ".ntm", "pipelines")
	if err := os.Rename(stateDir, stateDir+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), stateDir); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := executeResumePipelineJob(ctx, project, jobPipelineResumeParams{RunID: "saved-run"},
		func(string, string) (*pipeline.ExecutionState, error) {
			called = true
			return &pipeline.ExecutionState{RunID: "saved-run", Session: "wrong-session"}, nil
		},
		func(context.Context, string, string, map[string]interface{}, *pipeline.ExecutionState) pipeline.PipelineRunOutput {
			called = true
			return pipeline.PipelineRunOutput{}
		})
	if err == nil || !strings.Contains(err.Error(), "namespace changed") || called || result != nil {
		t.Fatalf("changed storage reached resume: %v %v called=%v", result, err, called)
	}
}
