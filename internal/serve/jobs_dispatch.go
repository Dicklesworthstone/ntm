// jobs_dispatch.go is the real async job dispatcher behind POST /api/v1/jobs
// (D5, bd-ws3-contract-breadth-psvyu.5). The Jobs API accepts long operations —
// pipeline run/exec/resume, swarm spawn, and checkpoint restore —
// and each dispatches to the same production code path the synchronous REST
// handlers use. A job's terminal state reflects the REAL operation's outcome:
// a failing operation reaches JobStatusFailed carrying the real error, never a
// simulated completion.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Job types accepted by the async dispatcher. Everything else is honestly
// NOT_IMPLEMENTED at POST time — see handleCreateJob.
const (
	JobTypePipelineRun       = "pipeline_run"
	JobTypePipelineExec      = "pipeline_exec"
	JobTypePipelineResume    = "pipeline_resume"
	JobTypeSwarmSpawn        = "swarm_spawn"
	JobTypeCheckpointRestore = "checkpoint_restore"
)

// implementedJobTypes lists the allow-listed async operations in the order
// they are documented.
var implementedJobTypes = []string{JobTypePipelineRun, JobTypePipelineExec, JobTypePipelineResume, JobTypeSwarmSpawn, JobTypeCheckpointRestore}

func isImplementedJobType(jobType string) bool {
	for _, t := range implementedJobTypes {
		if t == jobType {
			return true
		}
	}
	return false
}

// jobExecutionTimeout bounds a single async job. Pipeline runs drive real
// agents and can legitimately take a long time; the bound exists so an
// orphaned job cannot run forever.
const jobExecutionTimeout = 2 * time.Hour

// dispatchJob runs one allow-listed job to its real terminal state. It is the
// production replacement for the deleted time.Sleep simulator.
func (s *Server) dispatchJob(jobID string, req CreateJobRequest) {
	defer func() {
		if r := recover(); r != nil {
			s.jobStore.Update(jobID, JobStatusFailed, 0, nil, fmt.Sprintf("panic: %v", r))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), jobExecutionTimeout)
	defer cancel()
	// Register the cancel func so DELETE /api/v1/jobs/{id} stops the real
	// work; the terminal-state guard in JobStore.Update keeps the cancelled
	// status from being overwritten when this goroutine unwinds.
	s.jobStore.SetCancel(jobID, cancel)
	defer s.jobStore.ClearCancel(jobID)

	// DELETE can mark the job cancelled before SetCancel registers its handle.
	// Publish the handle first, then check the row: an earlier cancellation is
	// observed here and a later one reaches the registered context.
	job := s.jobStore.Get(jobID)
	if job == nil || job.Status == JobStatusCancelled || job.Status == JobStatusCompleted || job.Status == JobStatusFailed {
		return
	}
	if err := ctx.Err(); err != nil {
		s.jobStore.Update(jobID, JobStatusCancelled, 0, nil, err.Error())
		return
	}
	s.jobStore.Update(jobID, JobStatusRunning, 0, nil, "")

	var (
		result map[string]interface{}
		err    error
	)
	switch req.Type {
	case JobTypePipelineRun:
		result, err = s.jobPipelineRun(ctx, req.Params)
	case JobTypePipelineExec:
		result, err = s.jobPipelineExec(ctx, req.Params)
	case JobTypePipelineResume:
		result, err = s.jobPipelineResume(ctx, req.Params)
	case JobTypeSwarmSpawn:
		result, err = s.jobSwarmSpawn(ctx, req.Params)
	case JobTypeCheckpointRestore:
		result, err = s.jobCheckpointRestore(ctx, req.Params)
	default:
		// handleCreateJob rejects unknown types before a job exists; reaching
		// this arm means the allow-lists drifted apart.
		err = fmt.Errorf("job type %q accepted but has no dispatcher", req.Type)
	}

	// Some operations can return a useful partial result on failure. Retain
	// its run/session identity so the caller can inspect effects before retrying.
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	if err != nil {
		status := JobStatusFailed
		if errors.Is(err, context.Canceled) {
			status = JobStatusCancelled
		}
		s.jobStore.Update(jobID, status, 0, result, err.Error())
		return
	}
	s.jobStore.Update(jobID, JobStatusCompleted, 100, result, "")
}

// decodeJobParams round-trips the untyped params map into a typed request
// struct so job params share field names with the synchronous REST handlers.
func decodeJobParams(params map[string]interface{}, into interface{}) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode job params: %w", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode job params: %w", err)
	}
	return nil
}

// jobPipelineRun executes a workflow through the same path as
// POST /api/v1/pipelines/run.
func (s *Server) jobPipelineRun(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req PipelineRunRequest
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	if req.WorkflowFile == "" {
		return nil, fmt.Errorf("workflow_file is required")
	}
	if req.Session == "" {
		return nil, fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}

	return executePipelineJob(ctx, pipeline.PipelineRunOptions{
		WorkflowFile: req.WorkflowFile,
		Session:      req.Session,
		ProjectDir:   s.projectDirSnapshot(),
		Variables:    req.Variables,
		DryRun:       req.DryRun,
		Background:   req.Background,
	}, s.runPipelineWithResult)
}

// executePipelineJob owns the entire pipeline lifetime, not just its launch.
// The jobs endpoint is already asynchronous: detaching a second time would
// discard its timeout/cancel handle and report success before the work ends.
func executePipelineJob(ctx context.Context, opts pipeline.PipelineRunOptions, run func(context.Context, pipeline.PipelineRunOptions) pipeline.PipelineRunOutput) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts.Background = false
	out := run(ctx, opts)
	result := map[string]interface{}{
		"run_id": out.RunID, "workflow_id": out.WorkflowID, "session": out.Session,
		"status": out.Status, "dry_run": out.DryRun, "progress": out.Progress,
	}
	if out.Session == "" {
		result["session"] = opts.Session
	}
	if len(out.Warnings) > 0 {
		result["warnings"] = out.Warnings
	}
	if out.SideEffects != nil {
		result["side_effect_manifest"] = out.SideEffects
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !out.Success {
		return result, fmt.Errorf("pipeline run failed [%s]: %s", out.ErrorCode, out.Error)
	}
	if opts.DryRun && out.DryRun && out.Status == "validated" {
		return result, nil
	}
	if !opts.DryRun && !out.DryRun && out.Status == "completed" {
		return result, nil
	}
	return result, fmt.Errorf("pipeline run returned status %q instead of a terminal successful outcome", out.Status)
}

// jobSwarmSpawnParams mirrors AgentSpawnRequest plus the target session.
type jobSwarmSpawnParams struct {
	Session string `json:"session"`
	AgentSpawnRequest
}

// jobSwarmSpawn spawns agents through the same seam as
// POST /api/v1/sessions/{sessionId}/agents/spawn.
func (s *Server) jobSwarmSpawn(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req jobSwarmSpawnParams
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	if req.Session == "" {
		return nil, fmt.Errorf("session is required")
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	if !req.hasAgentCountOrPreset() {
		return nil, errors.New(agentSpawnCountRequiredMessage)
	}
	if s.spawnAgents == nil {
		return nil, fmt.Errorf("agent spawn service unavailable")
	}

	result, err := s.spawnAgents(ctx, robot.SpawnOptions{
		Session:   req.Session,
		Label:     req.Label,
		CCCount:   req.CCCount,
		CodCount:  req.CodCount,
		GmiCount:  req.GmiCount,
		AgyCount:  req.AgyCount,
		GrokCount: req.GrokCount,
		OmpCount:  req.OmpCount,
		Preset:    req.Preset,
		WaitReady: req.WaitReady,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("agent spawn returned no result")
	}
	if !result.Success {
		return nil, fmt.Errorf("agent spawn failed [%s]: %s", result.ErrorCode, result.Error)
	}
	return toJSONMap(result)
}

// jobCheckpointRestoreParams identifies the source artifact separately from
// the optional destination. The source remains the storage namespace.
type jobCheckpointRestoreParams struct {
	Session       string `json:"session"`
	CheckpointID  string `json:"checkpoint_id"`
	TargetSession string `json:"target_session,omitempty"`
	RestoreCheckpointRequest
}

// jobCheckpointRestore restores a checkpoint through the same path as
// POST /api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/restore.
func (s *Server) jobCheckpointRestore(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req jobCheckpointRestoreParams
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	if req.Session == "" {
		return nil, fmt.Errorf("session is required")
	}
	if req.CheckpointID == "" {
		return nil, fmt.Errorf("checkpoint_id is required")
	}
	if req.TargetSession != "" {
		if err := tmux.ValidateSessionName(req.TargetSession); err != nil {
			return nil, fmt.Errorf("invalid restore target session: %w", err)
		}
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}

	storage := checkpoint.NewStorage()
	cp, err := storage.Load(req.Session, req.CheckpointID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint %s/%s: %w", req.Session, req.CheckpointID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	restorer := checkpoint.NewRestorerWithStorage(storage)
	result, err := restorer.RestoreFromCheckpointContext(ctx, cp, checkpoint.RestoreOptions{
		TargetSession:   req.TargetSession,
		Force:           req.Force,
		SkipGitCheck:    req.SkipGitCheck,
		InjectContext:   req.InjectContext,
		DryRun:          req.DryRun,
		CustomDirectory: req.CustomDirectory,
		ScrollbackLines: req.ScrollbackLines,
	})
	var payload map[string]interface{}
	if result != nil {
		payload = map[string]interface{}{
			"session_name":     result.SessionName,
			"source_session":   result.SourceSession,
			"panes_restored":   result.PanesRestored,
			"context_injected": result.ContextInjected,
			"dry_run":          result.DryRun,
			"warnings":         result.Warnings,
			"stage":            result.Stage,
			"interrupted":      result.Interrupted,
		}
	}
	if err != nil {
		return payload, fmt.Errorf("restore checkpoint: %w", err)
	}
	if result == nil {
		return nil, errors.New("restore checkpoint returned no result")
	}
	return payload, nil
}
