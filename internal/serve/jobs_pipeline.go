package serve

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// jobPipelineExec gives inline workflows the same pollable, cancellable
// lifetime as file-backed pipeline_run jobs. Execution stays in the existing
// REST pipeline engine; the job dispatcher owns its context until it finishes.
func (s *Server) jobPipelineExec(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req PipelineExecRequest
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	return executeInlinePipelineJob(ctx, req, s.execPipelineInline)
}

func executeInlinePipelineJob(ctx context.Context, req PipelineExecRequest, run func(context.Context, *pipeline.Workflow, string, map[string]interface{}, bool) pipeline.PipelineRunOutput) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Session) == "" {
		return nil, fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	validation := pipeline.Validate(&req.Workflow)
	if !validation.Valid {
		message := "workflow validation failed"
		if len(validation.Errors) > 0 {
			message += ": " + validation.Errors[0].Message
		}
		return nil, fmt.Errorf("%s", message)
	}

	return executePipelineJob(ctx, pipeline.PipelineRunOptions{
		Session: req.Session, Variables: req.Variables, Background: req.Background,
	}, func(runCtx context.Context, opts pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
		// executePipelineJob always clears Background. The HTTP request may end,
		// but the job must not finish while a detached executor is still running.
		return run(runCtx, &req.Workflow, opts.Session, opts.Variables, opts.Background)
	})
}

// jobPipelineResumeParams adds the saved run identity to the ordinary REST
// resume request. Omitted session and variables retain the saved values;
// Executor.Resume remains responsible for checkpoint and roster policy.
type jobPipelineResumeParams struct {
	RunID string `json:"run_id"`
	PipelineResumeRequest
}

func (s *Server) jobPipelineResume(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req jobPipelineResumeParams
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	return executeResumePipelineJob(ctx, s.pipelineProjectDir(), req, pipeline.LoadState, s.resumePipelineWithResult)
}

func executeResumePipelineJob(ctx context.Context, projectDir string, req jobPipelineResumeParams,
	load func(string, string) (*pipeline.ExecutionState, error),
	resume func(context.Context, string, string, map[string]interface{}, *pipeline.ExecutionState) pipeline.PipelineRunOutput,
) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.RunID) == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	// LoadState confines run IDs to the project's pipeline state directory.
	// Do not accept an arbitrary state path or search another project's cwd.
	prior, err := load(projectDir, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("load resume state %q: %w", req.RunID, err)
	}
	if prior == nil {
		return nil, fmt.Errorf("no resumable state for run %q", req.RunID)
	}
	if prior.RunID != req.RunID {
		return nil, fmt.Errorf("resume state run_id %q does not match requested run %q", prior.RunID, req.RunID)
	}
	session := req.Session
	if session == "" {
		session = prior.Session
	}
	if strings.TrimSpace(session) == "" {
		return nil, fmt.Errorf("session is required for resume")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, fmt.Errorf("invalid resume session: %w", err)
	}

	return executePipelineJob(ctx, pipeline.PipelineRunOptions{
		Session: session, Variables: req.Variables,
	}, func(runCtx context.Context, opts pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
		out := resume(runCtx, req.RunID, opts.Session, opts.Variables, prior)
		// The synchronous resume helper can return an error-only envelope.
		// Keep the saved identity even on failure so callers can inspect its
		// checkpoint and partial effects before deciding whether to retry.
		if out.RunID == "" {
			out.RunID = req.RunID
		}
		if out.WorkflowID == "" {
			out.WorkflowID = prior.WorkflowID
		}
		return out
	})
}
