package serve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

func inlineJobRequest(t *testing.T) PipelineExecRequest {
	t.Helper()
	var req PipelineExecRequest
	if err := json.Unmarshal([]byte(`{"session":"inlinejob","background":true,"variables":{"topic":"storage"},"workflow":{"schema_version":"2.0","name":"inline-job","steps":[{"id":"work","command":"true"}]}}`), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func completedResumeJob(_ context.Context, runID, session string, _ map[string]interface{}, prior *pipeline.ExecutionState) pipeline.PipelineRunOutput {
	return pipeline.PipelineRunOutput{
		RobotResponse: pipeline.NewRobotResponse(true),
		RunID:         runID, Session: session, WorkflowID: prior.WorkflowID, Status: "completed",
	}
}

func TestInlinePipelineJobOwnsExecutionAndCancellation(t *testing.T) {
	req := inlineJobRequest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := executeInlinePipelineJob(ctx, req, func(got context.Context, workflow *pipeline.Workflow, session string, vars map[string]interface{}, background bool) pipeline.PipelineRunOutput {
			if got != ctx || background || session != req.Session || workflow.Name != req.Workflow.Name || vars["topic"] != "storage" {
				return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewErrorResponse(errors.New("lost inline job ownership or inputs"), "TEST", "")}
			}
			close(started)
			<-got.Done()
			return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewRobotResponse(true), Status: "completed"}
		})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("inline job returned before entering execution: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("inline execution did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("inline job completed while its workflow remained running: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inline execution did not stop")
	}
}

func TestInlinePipelineJobRejectsInvalidInputsBeforeExecution(t *testing.T) {
	for _, name := range []string{"missing session", "invalid session", "invalid workflow", "canceled"} {
		t.Run(name, func(t *testing.T) {
			req := inlineJobRequest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "missing session":
				req.Session = ""
			case "invalid session":
				req.Session = "bad:session"
			case "invalid workflow":
				req.Workflow = pipeline.Workflow{}
			case "canceled":
				cancel()
			}
			_, err := executeInlinePipelineJob(ctx, req, func(context.Context, *pipeline.Workflow, string, map[string]interface{}, bool) pipeline.PipelineRunOutput {
				t.Fatal("invalid inline request entered executor")
				return pipeline.PipelineRunOutput{}
			})
			if err == nil {
				t.Fatal("invalid inline request succeeded")
			}
		})
	}
}

func TestResumePipelineJobUsesSavedIdentityAndOverrides(t *testing.T) {
	for _, override := range []string{"", "replacement"} {
		t.Run("session="+override, func(t *testing.T) {
			ctx := context.Background()
			prior := &pipeline.ExecutionState{RunID: "saved-run", WorkflowID: "saved-workflow", Session: "saved-session", Variables: map[string]interface{}{"existing": "keep"}}
			req := jobPipelineResumeParams{RunID: prior.RunID, PipelineResumeRequest: PipelineResumeRequest{Session: override, Variables: map[string]interface{}{"new": "override"}}}
			wantSession := prior.Session
			if override != "" {
				wantSession = override
			}
			called := false
			result, err := executeResumePipelineJob(ctx, "/project", req,
				func(dir, runID string) (*pipeline.ExecutionState, error) {
					if dir != "/project" || runID != prior.RunID {
						t.Fatalf("wrong saved-state lookup: %q %q", dir, runID)
					}
					return prior, nil
				},
				func(got context.Context, runID, session string, vars map[string]interface{}, state *pipeline.ExecutionState) pipeline.PipelineRunOutput {
					called = true
					if got != ctx || runID != prior.RunID || session != wantSession || state != prior || state.Variables["existing"] != "keep" || vars["new"] != "override" {
						t.Fatal("resume lost context, saved state, or requested overrides")
					}
					return completedResumeJob(got, runID, session, vars, state)
				})
			if err != nil || !called || result["run_id"] != prior.RunID || result["session"] != wantSession || result["status"] != "completed" {
				t.Fatalf("resume result=%#v error=%v called=%v", result, err, called)
			}
		})
	}
}

func TestResumePipelineJobPreflightFailuresNeverExecute(t *testing.T) {
	for _, name := range []string{"missing id", "load failure", "nil state", "wrong run", "missing session", "invalid session", "canceled before load", "canceled during load"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := jobPipelineResumeParams{RunID: "saved"}
			prior := &pipeline.ExecutionState{RunID: req.RunID, Session: "project"}
			switch name {
			case "missing id":
				req.RunID = " "
			case "wrong run":
				prior.RunID = "other"
			case "missing session":
				prior.Session = ""
			case "invalid session":
				prior.Session = "bad:session"
			case "canceled before load":
				cancel()
			}
			_, err := executeResumePipelineJob(ctx, "/project", req,
				func(string, string) (*pipeline.ExecutionState, error) {
					if name == "canceled before load" || name == "missing id" {
						t.Fatal("preflight failure reached disk")
					}
					if name == "load failure" {
						return nil, errors.New("corrupt saved state")
					}
					if name == "nil state" {
						return nil, nil
					}
					if name == "canceled during load" {
						cancel()
					}
					return prior, nil
				},
				func(context.Context, string, string, map[string]interface{}, *pipeline.ExecutionState) pipeline.PipelineRunOutput {
					t.Fatal("invalid resume entered executor")
					return pipeline.PipelineRunOutput{}
				})
			if err == nil {
				t.Fatal("invalid resume succeeded")
			}
			if strings.HasPrefix(name, "canceled") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestResumePipelineJobRetainsIdentityOnFailureAndCancellation(t *testing.T) {
	for _, outcome := range []string{"failed", "running", "paused", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prior := &pipeline.ExecutionState{RunID: "saved", WorkflowID: "workflow", Session: "project"}
			result, err := executeResumePipelineJob(ctx, "/project", jobPipelineResumeParams{RunID: prior.RunID},
				func(string, string) (*pipeline.ExecutionState, error) { return prior, nil },
				func(context.Context, string, string, map[string]interface{}, *pipeline.ExecutionState) pipeline.PipelineRunOutput {
					if outcome == "canceled" {
						cancel()
					}
					return pipeline.PipelineRunOutput{RobotResponse: pipeline.NewRobotResponse(outcome != "failed"), Status: outcome}
				})
			if err == nil || result["run_id"] != prior.RunID || result["workflow_id"] != prior.WorkflowID || result["session"] != prior.Session {
				t.Fatalf("failure lost identity or reported success: %#v %v", result, err)
			}
			if outcome == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestPipelineJobTypesRegistered(t *testing.T) {
	for _, jobType := range []string{JobTypePipelineExec, JobTypePipelineResume} {
		if !isImplementedJobType(jobType) {
			t.Fatalf("job type %q has no public jobs surface", jobType)
		}
	}
}
