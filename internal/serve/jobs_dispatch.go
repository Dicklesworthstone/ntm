// jobs_dispatch.go is the real async job dispatcher behind POST /api/v1/jobs
// (D5, bd-ws3-contract-breadth-psvyu.5). The Jobs API accepts long operations —
// pipeline run/exec/resume, swarm spawn, and checkpoint restore —
// and each dispatches to the same production code path the synchronous REST
// handlers use. A job's terminal state reflects the REAL operation's outcome:
// a failing operation reaches JobStatusFailed carrying the real error, never a
// simulated completion.
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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
	// Admission already registered ownership. Every exit, including failure
	// to acquire a worker fence, must release it AFTER all deferred writes.
	defer s.jobStore.ClearCancel(jobID)
	// Take the worker fence before looking at the row. Shutdown cancels even
	// not-yet-started rows; a late dispatcher then exits without any writes.
	release, err := s.fenceJobWorker(jobID)
	if err != nil {
		s.jobStore.Update(jobID, JobStatusFailed, 0, nil, fmt.Sprintf("job journal unavailable: %v", err))
		return
	}
	defer release()

	parent := req.executionContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, jobExecutionTimeout)
	defer cancel()
	// Register the cancel func so DELETE /api/v1/jobs/{id} stops the real
	// work; the terminal-state guard in JobStore.Update keeps the cancelled
	// status from being overwritten when this goroutine unwinds.
	s.jobStore.SetCancel(jobID, cancel)

	// DELETE can mark the job cancelled before SetCancel registers its handle.
	// Publish the handle first, then check the row: an earlier cancellation is
	// observed here and a later one reaches the registered context.
	job := s.jobStore.Get(jobID)
	if job == nil || job.Status == JobStatusCancelled || job.Status == JobStatusCompleted || job.Status == JobStatusFailed {
		return
	}
	var progressMu sync.Mutex
	var lastProgress map[string]interface{}
	finishProgress := func(result map[string]interface{}) map[string]interface{} {
		progressMu.Lock()
		defer progressMu.Unlock()
		return finishJobProgress(lastProgress, result)
	}
	// This defer runs before ClearCancel and before releasing the worker
	// fence, so shutdown/recovery cannot race the final checkpoint.
	defer func() {
		if r := recover(); r != nil {
			result := finishProgress(nil)
			s.jobStore.Update(jobID, JobStatusFailed, 0, result, fmt.Sprintf("panic: %v", r))
			s.jobStore.retainCancelledResult(jobID, result)
		}
		if err := s.persistJobHistory(jobID); err != nil {
			s.recordJobJournalError(jobID, err)
		}
	}()
	// No pipeline, agent spawn, or checkpoint restore may act without a
	// recoverable pre-dispatch record when a durable state store is supplied.
	if err := s.persistJobHistory(jobID); err != nil {
		s.jobStore.Update(jobID, JobStatusFailed, 0, nil, fmt.Sprintf("checkpoint job before execution: %v", err))
		return
	}
	if err := ctx.Err(); err != nil {
		s.jobStore.Update(jobID, JobStatusCancelled, 0, nil, err.Error())
		return
	}
	s.jobStore.Update(jobID, JobStatusRunning, 0, nil, "")
	// Persistent servers expose live recovery evidence through the existing
	// job GET/list surfaces. In-memory callers retain their original ports.
	dir, err := s.jobJournalDir()
	if err != nil {
		s.jobStore.Update(jobID, JobStatusFailed, 0, nil, err.Error())
		return
	}
	if dir != "" {
		ctx = context.WithValue(ctx, jobProgressContextKey{}, jobProgressReporter(func(result map[string]interface{}) error {
			progressMu.Lock()
			defer progressMu.Unlock()
			snapshot, err := s.checkpointJobProgress(jobID, result)
			if snapshot != nil {
				lastProgress = snapshot
			}
			return err
		}))
	}

	result, err := s.executeJobOperation(ctx, jobID, req)
	result = finishProgress(result)

	// Some operations can return a useful partial result on failure. Retain
	// its run/session identity so the caller can inspect effects before retrying.
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	status := JobStatusCompleted
	progress := float64(100)
	message := ""
	if err != nil {
		status = JobStatusFailed
		progress = 0
		message = err.Error()
		if errors.Is(err, context.Canceled) && !errors.Is(err, errJobProgressCheckpoint) {
			status = JobStatusCancelled
		}
	}
	s.jobStore.Update(jobID, status, progress, result, message)
	// Cancellation may have won the terminal-state race, making Update a
	// no-op. The worker's recovery evidence is still valuable: cancelling a
	// request does not undo sessions, panes, or pipeline runs already created.
	// Run this AFTER Update so cancellation between the two cannot lose it.
	s.jobStore.retainCancelledResult(jobID, result)
}

// executeJobRequest is the one execution switch, shared by ordinary dispatch
// and first-time durable operations. A replay never reaches this function.
func (s *Server) executeJobRequest(ctx context.Context, req CreateJobRequest) (map[string]interface{}, error) {
	s = s.jobExecutionServer(req)
	params, err := jobExecutionParams(req)
	if err != nil {
		return nil, err
	}
	switch req.Type {
	case JobTypePipelineRun:
		return s.jobPipelineRun(ctx, params)
	case JobTypePipelineExec:
		return s.jobPipelineExec(ctx, params)
	case JobTypePipelineResume:
		return s.jobPipelineResume(ctx, params)
	case JobTypeSwarmSpawn:
		return s.jobSwarmSpawn(ctx, params)
	case JobTypeCheckpointRestore:
		return s.jobCheckpointRestore(ctx, params)
	default:
		return nil, fmt.Errorf("job type %q accepted but has no dispatcher", req.Type)
	}
}

// retainCancelledResult fills in recovery evidence when a worker finishes
// after DELETE has already made its job terminal. It never changes the
// cancellation status/reason, replaces a final result, or revives an
// evicted job. Callers can keep polling GET /jobs/{id} for these late results;
// a cancelled status by itself does not mean the operation rolled back.
func (s *JobStore) retainCancelledResult(id string, result map[string]interface{}) {
	if len(result) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil || job.Status != JobStatusCancelled {
		return
	}
	if job.Result != nil && job.Result["_execution_in_progress"] != true {
		return
	}
	job.Result = result
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// decodeJobParams round-trips the untyped params map into a typed request
// struct so job params share field names with the synchronous REST handlers.
// Unknown fields must fail closed: silently dropping a misspelled dry_run or
// reservation flag can turn an intended preview into an unguarded mutation.
func decodeJobParams(params map[string]interface{}, into interface{}) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode job params: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
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

// jobSwarmSpawnParams extends the basic REST spawn request with the robot
// engine's launch, preview, and work-assignment controls. All behavior stays
// in the shared spawn service, including model and reservation policy checks.
type jobSwarmSpawnParams struct {
	Session string `json:"session"`
	AgentSpawnRequest
	WorkingDir          string   `json:"working_dir,omitempty"`
	DryRun              bool     `json:"dry_run,omitempty"`
	Safety              bool     `json:"safety,omitempty"`
	NoUserPane          bool     `json:"no_user_pane,omitempty"`
	ReadyTimeout        string   `json:"ready_timeout,omitempty"`   // Positive Go duration, e.g. "45s".
	LaunchInterval      string   `json:"launch_interval,omitempty"` // Minimum launch start interval, e.g. "2s"; zero disables pacing.
	StartupTimeout      string   `json:"startup_timeout,omitempty"` // Whole spawn budget, including pacing and assignment.
	CCModel             string   `json:"cc_model,omitempty"`
	CCReasoningEffort   string   `json:"cc_reasoning_effort,omitempty"`
	CodModel            string   `json:"cod_model,omitempty"`
	CodReasoningEffort  string   `json:"cod_reasoning_effort,omitempty"`
	GmiModel            string   `json:"gmi_model,omitempty"`
	GrokModel           string   `json:"grok_model,omitempty"`
	GrokReasoningEffort string   `json:"grok_reasoning_effort,omitempty"`
	OmpModel            string   `json:"omp_model,omitempty"`
	OmpReasoningEffort  string   `json:"omp_reasoning_effort,omitempty"`
	AssignWork          bool     `json:"assign_work,omitempty"`
	AssignStrategy      string   `json:"assign_strategy,omitempty"`
	CustomNames         []string `json:"custom_names,omitempty"`
	RequireReservation  bool     `json:"require_reservation,omitempty"`
	ReservationPaths    []string `json:"reservation_paths,omitempty"`
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
	var readyTimeout time.Duration
	if req.ReadyTimeout != "" {
		var err error
		readyTimeout, err = time.ParseDuration(req.ReadyTimeout)
		if err != nil || readyTimeout <= 0 {
			return nil, fmt.Errorf("ready_timeout must be a positive duration such as 45s")
		}
	}
	if !req.AssignWork && (req.AssignStrategy != "" || req.RequireReservation || len(req.ReservationPaths) > 0) {
		return nil, fmt.Errorf("assign_strategy, require_reservation, and reservation_paths require assign_work=true")
	}
	var launchInterval time.Duration
	if req.LaunchInterval != "" {
		var err error
		launchInterval, err = time.ParseDuration(req.LaunchInterval)
		if err != nil || launchInterval < 0 {
			return nil, fmt.Errorf("launch_interval must be a non-negative duration such as 2s")
		}
	}
	var startupTimeout time.Duration
	if req.StartupTimeout != "" {
		var err error
		startupTimeout, err = time.ParseDuration(req.StartupTimeout)
		if err != nil || startupTimeout <= 0 {
			return nil, fmt.Errorf("startup_timeout must be a positive duration such as 2m")
		}
	}
	if s.spawnAgents == nil {
		return nil, fmt.Errorf("agent spawn service unavailable")
	}

	opts := robot.SpawnOptions{
		Session:             req.Session,
		Label:               req.Label,
		CCCount:             req.CCCount,
		CodCount:            req.CodCount,
		GmiCount:            req.GmiCount,
		AgyCount:            req.AgyCount,
		GrokCount:           req.GrokCount,
		OmpCount:            req.OmpCount,
		Preset:              req.Preset,
		WaitReady:           req.WaitReady,
		WorkingDir:          req.WorkingDir,
		DryRun:              req.DryRun,
		Safety:              req.Safety,
		NoUserPane:          req.NoUserPane,
		ReadyTimeout:        readyTimeout,
		CCModel:             req.CCModel,
		CCReasoningEffort:   req.CCReasoningEffort,
		CodModel:            req.CodModel,
		CodReasoningEffort:  req.CodReasoningEffort,
		GmiModel:            req.GmiModel,
		GrokModel:           req.GrokModel,
		GrokReasoningEffort: req.GrokReasoningEffort,
		OmpModel:            req.OmpModel,
		OmpReasoningEffort:  req.OmpReasoningEffort,
		AssignWork:          req.AssignWork,
		AssignStrategy:      req.AssignStrategy,
		CustomNames:         req.CustomNames,
		RequireReservation:  req.RequireReservation,
		ReservationPaths:    req.ReservationPaths,
	}
	spawnCtx := ctx
	if startupTimeout > 0 {
		var cancel context.CancelFunc
		spawnCtx, cancel = context.WithTimeout(ctx, startupTimeout)
		defer cancel()
	}
	if _, ok := spawnCtx.Value(jobProgressContextKey{}).(jobProgressReporter); ok && !req.DryRun {
		var cancel context.CancelCauseFunc
		spawnCtx, cancel = context.WithCancelCause(spawnCtx)
		defer cancel(nil)
		opts = robot.WithSpawnProgress(opts, spawnJobProgressObserver(spawnCtx, cancel))
	}
	// The progress observer runs inside pacing, after a wait has completed.
	opts, err := robot.WithSpawnLaunchInterval(opts, launchInterval)
	if err != nil {
		return nil, err
	}
	result, err := s.spawnAgents(spawnCtx, opts)
	// A backend may return a structured failure (or even success) rather than
	// its context error. The caller's startup budget remains authoritative.
	// Keep the partial output below, but never report a timed-out spawn as done.
	if ctxErr := spawnCtx.Err(); ctxErr != nil {
		err = errors.Join(err, fmt.Errorf("swarm startup stopped: %w", context.Cause(spawnCtx)))
	}
	// A spawn can create its session and some agents before failing or being
	// cancelled. Serialize that output BEFORE inspecting either error channel
	// so operators retain pane identities and recovery instructions on failure.
	var payload map[string]interface{}
	if result != nil {
		var encodeErr error
		payload, encodeErr = toJSONMap(result)
		if encodeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("encode agent spawn result: %w", encodeErr))
		}
		if req.LaunchInterval != "" {
			payload["launch_interval"] = launchInterval.String()
		}
		if req.StartupTimeout != "" {
			payload["startup_timeout"] = startupTimeout.String()
		}
	}
	if err != nil {
		return payload, err
	}
	if result == nil {
		return nil, fmt.Errorf("agent spawn returned no result")
	}
	if !result.Success {
		message := result.Error
		if message == "" {
			message = result.RobotResponse.Error
		}
		return payload, fmt.Errorf("agent spawn failed [%s]: %s", result.ErrorCode, message)
	}
	return payload, nil
}

// spawnJobProgressObserver accumulates lifecycle evidence, not a second spawn
// model. The final SpawnOutput remains authoritative for completion/readiness.
func spawnJobProgressObserver(ctx context.Context, cancel context.CancelCauseFunc) func(robot.SpawnProgress) error {
	var mu sync.Mutex
	var sequence int
	var session, workingDir string
	sessionCreated := false
	createdPanes := []string{}
	agents := []robot.SpawnedAgent{}
	agentPositions := make(map[string]int)
	paneIDs := make(map[string]string)
	return func(event robot.SpawnProgress) error {
		mu.Lock()
		defer mu.Unlock()
		sequence++
		if event.Session != "" {
			session = event.Session
		}
		if event.WorkingDir != "" {
			workingDir = event.WorkingDir
		}
		if event.Phase == "finished" {
			if event.Stage == "create_session" && event.Error == "" {
				sessionCreated = true
			}
			if event.Stage == "split_window" && event.PaneID != "" {
				createdPanes = append(createdPanes, event.PaneID)
			}
			if event.Agent != nil {
				agent := *event.Agent
				if event.Error != "" {
					agent.Error = event.Error
				}
				if index, exists := agentPositions[agent.Pane]; exists {
					agents[index] = agent
				} else {
					agentPositions[agent.Pane] = len(agents)
					agents = append(agents, agent)
				}
				paneIDs[agent.Pane] = event.PaneID
			}
			if event.Stage == "wait_ready" && event.Agents != nil {
				agents = append([]robot.SpawnedAgent{}, event.Agents...)
				agentPositions = make(map[string]int, len(agents))
				for i, agent := range agents {
					agentPositions[agent.Pane] = i
				}
			}
		}
		// Clone through JSON before publishing so later events cannot change a
		// previously handed-off snapshot's slices, maps, or agent pointers.
		snapshot, err := toJSONMap(map[string]interface{}{
			"session": session, "working_dir": workingDir, "agents": agents,
			"spawn_progress": map[string]interface{}{
				"sequence": sequence, "observed_at": time.Now().UTC().Format(time.RFC3339Nano),
				"last_event": event, "session_created": sessionCreated,
				"created_pane_ids": createdPanes, "agent_pane_ids": paneIDs,
				"observed_agents": agents,
			},
		})
		if err == nil {
			err = reportJobProgress(ctx, snapshot)
		}
		if err != nil {
			// Stop GetSpawn's assignment path as well as its lifecycle ports.
			cancel(err)
		}
		return err
	}
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
