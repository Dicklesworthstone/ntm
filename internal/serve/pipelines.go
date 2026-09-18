// Package serve provides REST API endpoints for pipeline management.
// pipelines.go implements the /api/v1/pipelines endpoints.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Pipeline-specific error codes
const (
	ErrCodePipelineNotFound = "PIPELINE_NOT_FOUND"
	ErrCodePipelineRunning  = "PIPELINE_RUNNING"
	ErrCodePipelineFailed   = "PIPELINE_FAILED"
	ErrCodeInvalidWorkflow  = "INVALID_WORKFLOW"
	ErrCodeMissingWorkflow  = "MISSING_WORKFLOW"
	ErrCodeMissingSession   = "MISSING_SESSION"
	ErrCodeTemplateNotFound = "TEMPLATE_NOT_FOUND"
	ErrCodeNoResumableState = "NO_RESUMABLE_STATE"
)

// resolveWorkflowPath confines a caller-supplied workflow path to the server's
// project directory.
//
// Pipeline files are project assets, and the handlers pass the path straight to
// pipeline.ParseFile, which os.ReadFile's it BEFORE checking the extension. An
// unconfined path therefore turned a pipelines endpoint into an arbitrary-file
// reader: the returned load error distinguishes "permission denied" from "no such
// file" (an existence oracle), and for .yaml/.toml targets the parse error leaks
// the file's key names. /validate needs only pipelines:read, which RoleViewer
// holds, so this was reachable by the least-privileged role.
func (s *Server) resolveWorkflowPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("workflow_file is required")
	}

	projectDir := strings.TrimSpace(s.projectDirSnapshot())
	if projectDir == "" {
		return "", errors.New("server has no project directory configured")
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)

	// Resolve symlinks when the target exists so a link inside the project cannot
	// point outside it. A path that does not exist yet is checked lexically; the
	// load will fail on its own afterwards.
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}

	if !pathWithinRoot(root, candidate) {
		return "", errors.New("workflow_file must be inside the project directory")
	}
	return candidate, nil
}

// pathWithinRoot reports whether candidate is root or lies beneath it.
func pathWithinRoot(root, candidate string) bool {
	if candidate == root {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PipelineRunRequest is the request body for POST /api/v1/pipelines/run
type PipelineRunRequest struct {
	WorkflowFile string                 `json:"workflow_file"`
	Session      string                 `json:"session"`
	Variables    map[string]interface{} `json:"variables,omitempty"`
	DryRun       bool                   `json:"dry_run,omitempty"`
	Background   bool                   `json:"background,omitempty"`
}

// PipelineValidateRequest is the request body for POST /api/v1/pipelines/validate
type PipelineValidateRequest struct {
	WorkflowFile    string `json:"workflow_file,omitempty"`
	WorkflowContent string `json:"workflow_content,omitempty"`
}

// PipelineCleanupRequest is the request body for POST /api/v1/pipelines/cleanup
type PipelineCleanupRequest struct {
	OlderThanHours int `json:"older_than_hours,omitempty"`
}

// PipelineResumeRequest is the request body for POST /api/v1/pipelines/{id}/resume
type PipelineResumeRequest struct {
	Session   string                 `json:"session,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// PipelineExecRequest is the request body for POST /api/v1/pipelines/exec (inline workflow)
type PipelineExecRequest struct {
	Workflow   pipeline.Workflow      `json:"workflow"`
	Session    string                 `json:"session"`
	Variables  map[string]interface{} `json:"variables,omitempty"`
	Background bool                   `json:"background,omitempty"`
}

// registerPipelineRoutes registers pipeline-related REST endpoints
func (s *Server) registerPipelineRoutes(r chi.Router) {
	r.Route("/pipelines", func(r chi.Router) {
		// List all pipelines (read permission)
		r.With(s.RequirePermission(PermReadPipelines)).Get("/", s.handleListPipelines)

		// Run a new pipeline from a workflow file (write permission)
		r.With(s.RequirePermission(PermWritePipelines)).Post("/run", s.handleRunPipeline)

		// Execute a pipeline from inline workflow definition (write permission)
		r.With(s.RequirePermission(PermWritePipelines)).Post("/exec", s.handleExecPipeline)

		// Validate a workflow (read permission - non-destructive)
		r.With(s.RequirePermission(PermReadPipelines)).Post("/validate", s.handleValidatePipeline)

		// List available workflow templates (read permission)
		r.With(s.RequirePermission(PermReadPipelines)).Get("/templates", s.handleListPipelineTemplates)

		// Cleanup old pipeline state files (dangerous operation - admin only)
		r.With(s.RequirePermission(PermDangerousOps)).Post("/cleanup", s.handleCleanupPipelines)

		// Single pipeline operations
		r.Route("/{id}", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadPipelines)).Get("/", s.handleGetPipeline)
			r.With(s.RequirePermission(PermWritePipelines)).Delete("/", s.handleCancelPipeline)
			r.With(s.RequirePermission(PermWritePipelines)).Post("/cancel", s.handleCancelPipeline)
			r.With(s.RequirePermission(PermWritePipelines)).Post("/resume", s.handleResumePipeline)
		})
	})
}

// detachedRunContext is used by the best-effort worker progress observer, not
// by an in-server executor. Cancelling an HTTP request or an observer must not
// terminate an authorized detached worker; run control owns that operation.
func detachedRunContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// handleListPipelines handles GET /api/v1/pipelines
func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	slog.Info("pipelines list", "request_id", reqID)

	// Detached workers have no server-local registry entry. Inspect the
	// configured project's durable runs, not the server process's cwd.
	pipelines, warnings, err := s.projectPipelineSnapshots()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "read pipeline state", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	// Convert to summary format
	summaries := make([]pipeline.PipelineSummary, 0, len(pipelines))
	for _, p := range pipelines {
		summary := pipeline.PipelineSummary{
			RunID:      p.RunID,
			WorkflowID: p.WorkflowID,
			Session:    p.Session,
			Status:     p.Status,
			StartedAt:  p.StartedAt.Format(time.RFC3339),
			Progress:   p.Progress,
		}
		if p.FinishedAt != nil {
			summary.FinishedAt = p.FinishedAt.Format(time.RFC3339)
		}
		summaries = append(summaries, summary)
	}

	// Ensure never null
	if summaries == nil {
		summaries = []pipeline.PipelineSummary{}
	}

	response := map[string]interface{}{
		"pipelines": summaries,
		"count":     len(summaries),
	}
	if len(warnings) > 0 {
		response["warnings"] = warnings
	}
	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleRunPipeline handles POST /api/v1/pipelines/run
func (s *Server) handleRunPipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PipelineRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Validate required fields
	if req.WorkflowFile == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeMissingWorkflow, "workflow_file is required", nil, reqID)
		return
	}
	if req.Session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeMissingSession, "session is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %s", err.Error()), nil, reqID)
		return
	}

	slog.Info("pipeline run",
		"request_id", reqID,
		"workflow_file", req.WorkflowFile,
		"session", req.Session,
		"dry_run", req.DryRun,
		"background", req.Background,
	)

	opts := pipeline.PipelineRunOptions{
		WorkflowFile: req.WorkflowFile,
		Session:      req.Session,
		ProjectDir:   s.projectDirSnapshot(),
		Variables:    req.Variables,
		DryRun:       req.DryRun,
		Background:   req.Background,
	}

	// Use the pipeline robot API which handles everything
	// For REST, we capture the result instead of printing
	result := s.runPipelineWithResult(r.Context(), opts)

	if !result.Success {
		writeErrorResponse(w, http.StatusBadRequest, result.ErrorCode, result.Error, nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"run_id":      result.RunID,
		"workflow_id": result.WorkflowID,
		"session":     result.Session,
		"status":      result.Status,
		"dry_run":     result.DryRun,
		"progress":    result.Progress,
	}, reqID)
}

// handleExecPipeline handles POST /api/v1/pipelines/exec
func (s *Server) handleExecPipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PipelineExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeMissingSession, "session is required", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(req.Session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %s", err.Error()), nil, reqID)
		return
	}

	slog.Info("pipeline exec",
		"request_id", reqID,
		"workflow_id", req.Workflow.Name,
		"session", req.Session,
	)

	// Validate the inline workflow
	validation := pipeline.Validate(&req.Workflow)
	if !validation.Valid {
		errors := make([]string, 0, len(validation.Errors))
		for _, e := range validation.Errors {
			errors = append(errors, e.Message)
		}
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidWorkflow, "workflow validation failed", map[string]interface{}{
			"errors": errors,
		}, reqID)
		return
	}

	// Execute inline workflow
	result := s.execPipelineInline(r.Context(), &req.Workflow, req.Session, req.Variables, req.Background)

	if !result.Success {
		writeErrorResponse(w, http.StatusBadRequest, result.ErrorCode, result.Error, nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"run_id":      result.RunID,
		"workflow_id": result.WorkflowID,
		"session":     result.Session,
		"status":      result.Status,
		"progress":    result.Progress,
	}, reqID)
}

// handleGetPipeline handles GET /api/v1/pipelines/{id}
func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	runID := chi.URLParam(r, "id")

	if runID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pipeline ID required", nil, reqID)
		return
	}

	slog.Info("pipeline get", "request_id", reqID, "run_id", runID)

	exec := s.pipelineSnapshot(runID)
	if exec == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodePipelineNotFound, "pipeline not found", map[string]interface{}{
			"run_id": runID,
		}, reqID)
		return
	}

	resp := map[string]interface{}{
		"run_id":       exec.RunID,
		"workflow_id":  exec.WorkflowID,
		"session":      exec.Session,
		"status":       exec.Status,
		"started_at":   exec.StartedAt.Format(time.RFC3339),
		"current_step": exec.CurrentStep,
		"progress":     exec.Progress,
		"steps":        exec.Steps,
	}
	if exec.FinishedAt != nil {
		resp["finished_at"] = exec.FinishedAt.Format(time.RFC3339)
		resp["duration_ms"] = exec.FinishedAt.Sub(exec.StartedAt).Milliseconds()
	}
	if exec.Error != "" {
		resp["error"] = exec.Error
	}

	writeSuccessResponse(w, http.StatusOK, resp, reqID)
}

// handleCancelPipeline handles DELETE /api/v1/pipelines/{id} and POST /api/v1/pipelines/{id}/cancel
func (s *Server) handleCancelPipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	runID := chi.URLParam(r, "id")

	if runID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pipeline ID required", nil, reqID)
		return
	}

	slog.Info("pipeline cancel", "request_id", reqID, "run_id", runID)

	exec := s.pipelineSnapshot(runID)
	if exec == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodePipelineNotFound, "pipeline not found", map[string]interface{}{
			"run_id": runID,
		}, reqID)
		return
	}

	// Check if pipeline can be cancelled
	if exec.Status != "running" && exec.Status != "pending" {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, "pipeline cannot be cancelled", map[string]interface{}{
			"run_id": runID,
			"status": exec.Status,
		}, reqID)
		return
	}

	// Run IDs are scoped to a project by the ownership protocol. The global
	// in-process registry is keyed only by run ID: a colliding entry can belong
	// to another project and must never select the cancellation target. Ask the
	// configured project's owner even when it lives in this same process.
	if err := pipeline.RequestRunCancellation(r.Context(), s.pipelineProjectDir(), runID); err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			"pipeline is not cancellable from this server: no acknowledging execution owner", map[string]interface{}{
				"run_id": runID, "status": exec.Status, "error": err.Error(),
			}, reqID)
		return
	}

	// An acknowledged cancellation is not terminal completion. The executor
	// still owns cleanup and is solely responsible for persisting that outcome.
	writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
		"run_id": runID, "status": "cancellation_requested",
		"message": "owning process acknowledged cancellation; poll the pipeline for its terminal state",
	}, reqID)
}

// handleResumePipeline handles POST /api/v1/pipelines/{id}/resume
func (s *Server) handleResumePipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	runID := chi.URLParam(r, "id")

	if runID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pipeline ID required", nil, reqID)
		return
	}

	var req PipelineResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	slog.Info("pipeline resume", "request_id", reqID, "run_id", runID)

	// Try to load state from disk
	state, err := pipeline.LoadState(s.pipelineProjectDir(), runID)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNoResumableState, "no resumable state found", map[string]interface{}{
			"run_id": runID,
			"error":  err.Error(),
		}, reqID)
		return
	}

	// Use session from request or from saved state
	session := req.Session
	if session == "" {
		session = state.Session
	}
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeMissingSession, "session is required for resume", nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %s", err.Error()), nil, reqID)
		return
	}

	result := s.resumePipelineWithResult(r.Context(), runID, session, req.Variables, state)

	if !result.Success {
		statusCode := http.StatusBadRequest
		if result.ErrorCode == ErrCodePipelineRunning {
			statusCode = http.StatusConflict
		}
		writeErrorResponse(w, statusCode, result.ErrorCode, result.Error, nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"run_id":      result.RunID,
		"workflow_id": result.WorkflowID,
		"session":     result.Session,
		"status":      result.Status,
		"progress":    result.Progress,
		"resumed":     true,
	}, reqID)
}

// handleValidatePipeline handles POST /api/v1/pipelines/validate
func (s *Server) handleValidatePipeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PipelineValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	slog.Info("pipeline validate", "request_id", reqID, "workflow_file", req.WorkflowFile)

	var workflow *pipeline.Workflow
	var validation pipeline.ValidationResult

	if req.WorkflowContent != "" {
		// Parse inline content (assume YAML format for inline content)
		wf, err := pipeline.ParseString(req.WorkflowContent, "yaml")
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidWorkflow, "failed to parse workflow", map[string]interface{}{
				"parse_error": err.Error(),
			}, reqID)
			return
		}
		workflow = wf
		validation = pipeline.Validate(workflow)
	} else if req.WorkflowFile != "" {
		// Load and validate from file, confined to the project directory.
		workflowPath, pathErr := s.resolveWorkflowPath(req.WorkflowFile)
		if pathErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, pathErr.Error(), nil, reqID)
			return
		}
		wf, val, err := pipeline.LoadAndValidate(workflowPath)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidWorkflow, "failed to load workflow", map[string]interface{}{
				"load_error": err.Error(),
			}, reqID)
			return
		}
		workflow = wf
		validation = val
	} else {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeMissingWorkflow, "workflow_file or workflow_content required", nil, reqID)
		return
	}

	errors := make([]map[string]interface{}, 0, len(validation.Errors))
	for _, e := range validation.Errors {
		errors = append(errors, map[string]interface{}{
			"field":   e.Field,
			"message": e.Message,
			"hint":    e.Hint,
		})
	}

	warnings := make([]map[string]interface{}, 0, len(validation.Warnings))
	for _, w := range validation.Warnings {
		warnings = append(warnings, map[string]interface{}{
			"field":   w.Field,
			"message": w.Message,
			"hint":    w.Hint,
		})
	}

	resp := map[string]interface{}{
		"valid":       validation.Valid,
		"errors":      errors,
		"warnings":    warnings,
		"workflow_id": "",
		"step_count":  0,
	}
	if workflow != nil {
		resp["workflow_id"] = workflow.Name
		resp["step_count"] = len(workflow.Steps)
	}

	writeSuccessResponse(w, http.StatusOK, resp, reqID)
}

// handleListPipelineTemplates handles GET /api/v1/pipelines/templates
func (s *Server) handleListPipelineTemplates(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	slog.Info("pipeline templates list", "request_id", reqID)

	templates := discoverPipelineTemplates()

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"templates": templates,
		"count":     len(templates),
	}, reqID)
}

// handleCleanupPipelines handles POST /api/v1/pipelines/cleanup
func (s *Server) handleCleanupPipelines(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PipelineCleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Default to 24 hours
	hours := req.OlderThanHours
	if hours <= 0 {
		hours = 24
	}

	slog.Info("pipeline cleanup", "request_id", reqID, "older_than_hours", hours)

	deleted, err := pipeline.CleanupStates(s.pipelineProjectDir(), time.Duration(hours)*time.Hour)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "cleanup failed", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"deleted":          deleted,
		"older_than_hours": hours,
	}, reqID)
}

// Helper functions

func (s *Server) publishPipelineEvent(session, eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	topic := "pipelines:*"
	if session != "" {
		topic = "pipelines:" + session
	}
	s.wsHub.Publish(topic, eventType, payload)
}

func pipelineEventTypeFromProgressType(progressType string) (string, bool) {
	switch progressType {
	case "workflow_start":
		return "pipeline.started", true
	case "step_complete":
		return "pipeline.step_completed", true
	case "step_error":
		return "pipeline.step_failed", true
	case "workflow_complete":
		return "pipeline.complete", true
	case "workflow_error":
		return "pipeline.complete", true
	default:
		return "", false
	}
}

// runPipelineWithResult runs a pipeline and returns the result struct
func (s *Server) runPipelineWithResult(ctx context.Context, opts pipeline.PipelineRunOptions) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{}

	// Confine the workflow path to the project directory for the same reason
	// /validate does: the path reaches os.ReadFile before any extension check.
	workflowPath, pathErr := s.resolveWorkflowPath(opts.WorkflowFile)
	if pathErr != nil {
		output.RobotResponse = pipeline.NewErrorResponse(pathErr, ErrCodeBadRequest, "pass a workflow file inside the project directory")
		return output
	}

	// Load and validate workflow
	workflow, validationResult, err := pipeline.LoadAndValidate(workflowPath)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "check workflow file syntax and path")
		return output
	}

	if !validationResult.Valid {
		errMsg := "workflow validation failed"
		hint := "fix workflow validation errors"
		if len(validationResult.Errors) > 0 {
			errMsg = validationResult.Errors[0].Message
			if validationResult.Errors[0].Hint != "" {
				hint = validationResult.Errors[0].Hint
			}
		}
		output.RobotResponse = pipeline.NewErrorResponse(errors.New(errMsg), ErrCodeInvalidWorkflow, hint)
		return output
	}

	config := pipeline.DefaultExecutorConfig(opts.Session)
	config.DryRun = opts.DryRun
	config.ProjectDir = opts.ProjectDir
	if strings.TrimSpace(config.ProjectDir) == "" {
		config.ProjectDir = s.pipelineProjectDir()
	}
	config.WorkflowFile = workflowPath
	config.RunID = pipeline.GenerateRunID()

	if opts.DryRun {
		executor := pipeline.NewExecutor(config)
		validation := executor.Validate(workflow)
		output.RobotResponse = pipeline.NewRobotResponse(validation.Valid)
		output.RunID = config.RunID
		output.WorkflowID = workflow.Name
		output.Status = "validated"
		output.DryRun = true
		return output
	}
	if opts.Background {
		return s.startBackgroundPipeline(ctx, workflow, opts.Variables, config)
	}
	return s.executeOwnedPipeline(ctx, workflow, opts.Variables, config)
}

// execPipelineInline executes an inline workflow definition.
func (s *Server) execPipelineInline(ctx context.Context, workflow *pipeline.Workflow, session string, variables map[string]interface{}, background bool) pipeline.PipelineRunOutput {
	config := pipeline.DefaultExecutorConfig(session)
	config.ProjectDir = s.pipelineProjectDir()
	config.RunID = pipeline.GenerateRunID()
	if background {
		return s.startBackgroundPipeline(ctx, workflow, variables, config)
	}
	return s.executeOwnedPipeline(ctx, workflow, variables, config)
}

// executeOwnedPipeline is the synchronous REST/Jobs path. Background requests
// never enter this executor: the authorized worker runs the canonical engine in
// its own process. Jobs continue to own their cancellation and timeout contexts.
func (s *Server) executeOwnedPipeline(ctx context.Context, workflow *pipeline.Workflow, variables map[string]interface{}, config pipeline.ExecutorConfig) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{RunID: config.RunID, Session: config.Session}
	if workflow == nil {
		output.RobotResponse = pipeline.NewErrorResponse(errors.New("workflow is required"), ErrCodeInvalidWorkflow, "provide a workflow")
		return output
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	control, err := pipeline.AcquireRunControl(runCtx, config.ProjectDir, config.RunID)
	if err != nil {
		output.RobotResponse = pipelineControlError(err)
		return output
	}
	defer control.Close()
	runCtx = control.Context()
	frozen, snapshotPath, err := pipeline.SnapshotWorkflow(runCtx, config.ProjectDir, workflow, config.WorkflowFile)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "workflow was not started; repair snapshot storage or workflow serialization")
		return output
	}
	workflow = frozen
	config.WorkflowFile = snapshotPath
	executor := pipeline.NewExecutor(config)
	output.RobotResponse = pipeline.NewRobotResponse(true)
	output.WorkflowID = workflow.Name
	output.Status = "started"
	output.Progress = pipeline.PipelineProgress{Pending: len(workflow.Steps), Total: len(workflow.Steps)}
	progress, finishProgress := s.consumePipelineProgress(config.RunID, workflow.Name, config.Session)
	defer finishProgress()
	pipeline.RegisterPipeline(pipeline.NewTrackedExecution(config.RunID, workflow.Name, config.Session, len(workflow.Steps), executor, cancelRun))
	state, err := executor.Run(runCtx, workflow, variables, progress)
	finishPipelineExecution(config.RunID, state, err)
	if state == nil && err == nil {
		err = errors.New("pipeline returned no execution state")
	}
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "pipeline execution failed")
		return output
	}
	output.RunID = state.RunID
	output.Status = string(state.Status)
	return output
}

// resumePipelineWithResult resumes a pipeline from saved state
func (s *Server) resumePipelineWithResult(ctx context.Context, runID, session string, variables map[string]interface{}, state *pipeline.ExecutionState) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{RunID: runID, Session: session}
	control, err := pipeline.AcquireRunControl(ctx, s.pipelineProjectDir(), runID)
	if err != nil {
		output.RobotResponse = pipelineControlError(err)
		return output
	}
	defer control.Close()
	ctx = control.Context()
	// The caller's snapshot may predate a different resume completing. Reload
	// under the lock so completed steps are never replayed from stale state.
	state, err = pipeline.LoadState(s.pipelineProjectDir(), runID)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeNoResumableState, "inspect the saved pipeline state before retrying")
		return output
	}
	output.WorkflowID = state.WorkflowID

	// Load workflow from state
	if state.WorkflowFile == "" {
		output.RobotResponse = pipeline.NewErrorResponse(
			errors.New("workflow file not recorded in state"),
			ErrCodeNoResumableState,
			"re-run the pipeline from a workflow file before attempting resume",
		)
		return output
	}

	workflowPath := state.WorkflowFile
	if !filepath.IsAbs(workflowPath) {
		workflowPath = filepath.Join(s.pipelineProjectDir(), workflowPath)
	}
	// Check the resolved destination but preserve the original snapshot
	// locator: resolving a substituted symlink must not bypass hash checking.
	if _, err := s.resolveWorkflowPath(workflowPath); err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "saved workflow must remain inside the configured project")
		return output
	}
	workflow, validation, err := pipeline.LoadResumeWorkflow(workflowPath)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "failed to reload workflow")
		return output
	}
	if !validation.Valid {
		output.RobotResponse = pipeline.NewErrorResponse(fmt.Errorf("saved workflow is invalid: %v", validation.Errors), ErrCodeInvalidWorkflow, "repair the saved workflow before resuming")
		return output
	}
	if state.WorkflowID != "" && state.WorkflowID != workflow.Name {
		output.RobotResponse = pipeline.NewErrorResponse(errors.New("saved workflow identity does not match execution state"), ErrCodeInvalidWorkflow, "resume with the original workflow definition")
		return output
	}

	config := pipeline.DefaultExecutorConfig(session)
	config.ProjectDir = s.pipelineProjectDir()
	config.WorkflowFile = state.WorkflowFile
	config.RunID = runID

	executor := pipeline.NewExecutor(config)

	// Merge variables
	vars := state.Variables
	if vars == nil {
		vars = make(map[string]interface{})
	}
	for k, v := range variables {
		vars[k] = v
	}
	state.Variables = vars

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	pipeline.RegisterPipeline(pipeline.NewTrackedExecution(
		runID, workflow.Name, session, len(workflow.Steps), executor, cancelRun,
	))
	progress, finishProgress := s.consumePipelineProgress(config.RunID, workflow.Name, session)
	defer finishProgress()
	newState, err := executor.Resume(runCtx, workflow, state, progress)
	finishPipelineExecution(runID, newState, err)
	if newState == nil && err == nil {
		err = errors.New("pipeline returned no execution state")
	}
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "pipeline resume failed")
		return output
	}

	output.RobotResponse = pipeline.NewRobotResponse(true)
	output.RunID = newState.RunID
	output.WorkflowID = newState.WorkflowID
	output.Session = session
	output.Status = string(newState.Status)

	return output
}

func (s *Server) pipelineSnapshot(runID string) *pipeline.PipelineExecution {
	// Even when projectDir equals cwd, the process-global registry can contain
	// runs started for other project roots. No unscoped fallback is safe here.
	return pipeline.LoadPipelineSnapshot(s.pipelineProjectDir(), runID)
}

func pipelineControlError(err error) pipeline.RobotResponse {
	code := ErrCodeInternalError
	if errors.Is(err, pipeline.ErrRunAlreadyOwned) {
		code = ErrCodePipelineRunning
	}
	return pipeline.NewErrorResponse(err, code, "inspect the current run and its owner before retrying")
}

func finishPipelineExecution(runID string, state *pipeline.ExecutionState, err error) {
	if state == nil {
		message := "pipeline returned no execution state"
		if err != nil {
			message = err.Error()
		}
		// Validation can fail before the executor creates state. Retire only
		// the registry entry; never overwrite the durable resume checkpoint.
		state = &pipeline.ExecutionState{
			RunID: runID, Status: pipeline.StatusFailed, FinishedAt: time.Now(),
			Errors: []pipeline.ExecutionError{{Message: message, Fatal: true, Timestamp: time.Now()}},
		}
	}
	pipeline.UpdatePipelineFromState(runID, state)
}

func (s *Server) pipelineProjectDir() string {
	projectDir := s.projectDirSnapshot()
	if strings.TrimSpace(projectDir) != "" {
		return projectDir
	}
	projectDir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return projectDir
}

// PipelineTemplate represents an available workflow template
type PipelineTemplate struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// discoverPipelineTemplates finds workflow templates in common locations
func discoverPipelineTemplates() []PipelineTemplate {
	templates := []PipelineTemplate{}

	// Look in current dir and .ntm/workflows
	searchPaths := []string{
		".",
		".ntm/workflows",
		"workflows",
	}

	for _, dir := range searchPaths {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext == ".yaml" || ext == ".yml" || ext == ".toml" {
				path := filepath.Join(dir, name)
				templates = append(templates, PipelineTemplate{
					Name: strings.TrimSuffix(name, ext),
					Path: path,
				})
			}
		}
	}

	return templates
}
