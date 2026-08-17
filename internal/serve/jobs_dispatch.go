// jobs_dispatch.go is the real async job dispatcher behind POST /api/v1/jobs
// (D5, bd-ws3-contract-breadth-psvyu.5). The Jobs API accepts exactly three
// genuinely long operations — pipeline run, swarm spawn, checkpoint restore —
// and each dispatches to the same production code path the synchronous REST
// handlers use. A job's terminal state reflects the REAL operation's outcome:
// a failing operation reaches JobStatusFailed carrying the real error, never a
// simulated completion.
package serve

import (
	"context"
	"encoding/json"
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
	JobTypeSwarmSpawn        = "swarm_spawn"
	JobTypeCheckpointRestore = "checkpoint_restore"
)

// implementedJobTypes lists the allow-listed async operations in the order
// they are documented.
var implementedJobTypes = []string{JobTypePipelineRun, JobTypeSwarmSpawn, JobTypeCheckpointRestore}

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

	s.jobStore.Update(jobID, JobStatusRunning, 0, nil, "")

	var (
		result map[string]interface{}
		err    error
	)
	switch req.Type {
	case JobTypePipelineRun:
		result, err = s.jobPipelineRun(ctx, req.Params)
	case JobTypeSwarmSpawn:
		result, err = s.jobSwarmSpawn(ctx, req.Params)
	case JobTypeCheckpointRestore:
		result, err = s.jobCheckpointRestore(ctx, req.Params)
	default:
		// handleCreateJob rejects unknown types before a job exists; reaching
		// this arm means the allow-lists drifted apart.
		err = fmt.Errorf("job type %q accepted but has no dispatcher", req.Type)
	}

	if err != nil {
		s.jobStore.Update(jobID, JobStatusFailed, 0, nil, err.Error())
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

	result := s.runPipelineWithResult(ctx, pipeline.PipelineRunOptions{
		WorkflowFile: req.WorkflowFile,
		Session:      req.Session,
		ProjectDir:   s.projectDirSnapshot(),
		Variables:    req.Variables,
		DryRun:       req.DryRun,
		Background:   req.Background,
	})
	if !result.Success {
		return nil, fmt.Errorf("pipeline run failed [%s]: %s", result.ErrorCode, result.Error)
	}
	return map[string]interface{}{
		"run_id":      result.RunID,
		"workflow_id": result.WorkflowID,
		"session":     result.Session,
		"status":      result.Status,
		"dry_run":     result.DryRun,
		"progress":    result.Progress,
	}, nil
}

// jobSwarmSpawnParams mirrors AgentSpawnRequest plus the target session.
type jobSwarmSpawnParams struct {
	Session string `json:"session"`
	AgentSpawnRequest
}

// jobSwarmSpawn spawns agents through the same seam as
// POST /api/v1/sessions/{sessionId}/agents/spawn.
func (s *Server) jobSwarmSpawn(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
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
	if req.CCCount == 0 && req.CodCount == 0 && req.GmiCount == 0 && req.AgyCount == 0 && req.GrokCount == 0 && req.Preset == "" {
		return nil, fmt.Errorf("at least one agent count (cc_count, cod_count, gmi_count, agy_count, grok_count) or preset required")
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

// jobCheckpointRestoreParams mirrors RestoreCheckpointRequest plus the target
// session and checkpoint identity carried in the URL for the synchronous route.
type jobCheckpointRestoreParams struct {
	Session      string `json:"session"`
	CheckpointID string `json:"checkpoint_id"`
	RestoreCheckpointRequest
}

// jobCheckpointRestore restores a checkpoint through the same path as
// POST /api/v1/sessions/{sessionName}/checkpoints/{checkpointId}/restore.
func (s *Server) jobCheckpointRestore(_ context.Context, params map[string]interface{}) (map[string]interface{}, error) {
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
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}

	storage := checkpoint.NewStorage()
	cp, err := storage.Load(req.Session, req.CheckpointID)
	if err != nil {
		return nil, fmt.Errorf("load checkpoint %s/%s: %w", req.Session, req.CheckpointID, err)
	}

	restorer := checkpoint.NewRestorerWithStorage(storage)
	result, err := restorer.RestoreFromCheckpoint(cp, checkpoint.RestoreOptions{
		Force:           req.Force,
		SkipGitCheck:    req.SkipGitCheck,
		InjectContext:   req.InjectContext,
		DryRun:          req.DryRun,
		CustomDirectory: req.CustomDirectory,
		ScrollbackLines: req.ScrollbackLines,
	})
	if err != nil {
		return nil, fmt.Errorf("restore checkpoint: %w", err)
	}
	return map[string]interface{}{
		"session_name":     result.SessionName,
		"panes_restored":   result.PanesRestored,
		"context_injected": result.ContextInjected,
		"dry_run":          result.DryRun,
		"warnings":         result.Warnings,
	}, nil
}
