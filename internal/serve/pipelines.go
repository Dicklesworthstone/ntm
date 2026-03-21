// Package serve provides REST API endpoints for pipeline management.
// pipelines.go implements the /api/v1/pipelines endpoints.
package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
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

// handleListPipelines handles GET /api/v1/pipelines
func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	slog.Info("pipelines list", "request_id", reqID)

	pipelines := pipeline.GetAllPipelines()

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

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pipelines": summaries,
		"count":     len(summaries),
	}, reqID)
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

	exec := pipeline.GetPipelineExecution(runID)
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

	exec := pipeline.GetPipelineExecution(runID)
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

	// Cancel the pipeline using the executor's Cancel method
	pipeline.CancelPipeline(runID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"run_id":  runID,
		"status":  "cancelled",
		"message": "pipeline cancellation requested",
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	slog.Info("pipeline resume", "request_id", reqID, "run_id", runID)

	// Try to load state from disk
	projectDir, _ := os.Getwd()
	state, err := pipeline.LoadState(projectDir, runID)
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

	result := s.resumePipelineWithResult(r.Context(), runID, session, req.Variables, state)

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
		// Load and validate from file
		wf, val, err := pipeline.LoadAndValidate(req.WorkflowFile)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Default to 24 hours
	hours := req.OlderThanHours
	if hours <= 0 {
		hours = 24
	}

	slog.Info("pipeline cleanup", "request_id", reqID, "older_than_hours", hours)

	projectDir, _ := os.Getwd()
	deleted, err := pipeline.CleanupStates(projectDir, time.Duration(hours)*time.Hour)
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

	workflowPath := opts.WorkflowFile
	if abs, err := filepath.Abs(opts.WorkflowFile); err == nil {
		workflowPath = abs
	}

	// Load and validate workflow
	workflow, validationResult, err := pipeline.LoadAndValidate(workflowPath)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "check workflow file syntax and path")
		return output
	}

	if !validationResult.Valid {
		errMsg := "workflow validation failed"
		if len(validationResult.Errors) > 0 {
			errMsg = validationResult.Errors[0].Message
		}
		output.RobotResponse = pipeline.NewErrorResponse(
			err,
			ErrCodeInvalidWorkflow,
			errMsg,
		)
		return output
	}

	// Create executor config
	config := pipeline.DefaultExecutorConfig(opts.Session)
	config.DryRun = opts.DryRun
	projectDir, _ := os.Getwd()
	config.ProjectDir = projectDir
	config.WorkflowFile = workflowPath
	config.RunID = pipeline.GenerateRunID()

	executor := pipeline.NewExecutor(config)

	if opts.DryRun {
		validation := executor.Validate(workflow)
		output.RobotResponse = pipeline.NewRobotResponse(validation.Valid)
		output.RunID = config.RunID
		output.WorkflowID = workflow.Name
		output.Status = "validated"
		output.DryRun = true
		return output
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	if opts.Background {
		// Detach from request lifecycle.
		runCtx = context.Background()
	}

	// Start execution
	output.RobotResponse = pipeline.NewRobotResponse(true)
	output.RunID = config.RunID
	output.WorkflowID = workflow.Name
	output.Session = opts.Session
	output.Status = "started"
	output.Progress = pipeline.PipelineProgress{
		Pending: len(workflow.Steps),
		Total:   len(workflow.Steps),
		Percent: 0,
	}

	progress := make(chan pipeline.ProgressEvent, 256)
	done := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		for {
			select {
			case ev := <-progress:
				eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
				if !ok {
					continue
				}
				payload := map[string]interface{}{
					"run_id":      config.RunID,
					"workflow_id": workflow.Name,
					"session":     opts.Session,
					"step_id":     ev.StepID,
					"message":     ev.Message,
					"progress":    ev.Progress,
					"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
				}
				if ev.Type == "workflow_error" {
					payload["success"] = false
				}
				if ev.Type == "workflow_complete" {
					payload["success"] = true
				}
				s.publishPipelineEvent(opts.Session, eventType, payload)
			case <-done:
				for {
					select {
					case ev := <-progress:
						eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
						if !ok {
							continue
						}
						payload := map[string]interface{}{
							"run_id":      config.RunID,
							"workflow_id": workflow.Name,
							"session":     opts.Session,
							"step_id":     ev.StepID,
							"message":     ev.Message,
							"progress":    ev.Progress,
							"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
						}
						if ev.Type == "workflow_error" {
							payload["success"] = false
						}
						if ev.Type == "workflow_complete" {
							payload["success"] = true
						}
						s.publishPipelineEvent(opts.Session, eventType, payload)
					default:
						return
					}
				}
			}
		}
	}()

	// For background mode, start async and return immediately
	if opts.Background {
		go func() {
			defer func() { _ = recover() }()
			defer close(done)
			_, _ = executor.Run(runCtx, workflow, opts.Variables, progress)
		}()
		output.Status = "running"
	} else {
		// Synchronous execution
		state, err := executor.Run(runCtx, workflow, opts.Variables, progress)
		close(done)
		if err != nil {
			output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "pipeline execution failed")
			return output
		}
		output.RunID = state.RunID
		output.Status = string(state.Status)
	}

	return output
}

// execPipelineInline executes an inline workflow definition
func (s *Server) execPipelineInline(ctx context.Context, workflow *pipeline.Workflow, session string, variables map[string]interface{}, background bool) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{}

	config := pipeline.DefaultExecutorConfig(session)
	projectDir, _ := os.Getwd()
	config.ProjectDir = projectDir
	config.RunID = pipeline.GenerateRunID()

	executor := pipeline.NewExecutor(config)

	output.RobotResponse = pipeline.NewRobotResponse(true)
	output.RunID = config.RunID
	output.WorkflowID = workflow.Name
	output.Session = session
	output.Status = "started"
	output.Progress = pipeline.PipelineProgress{
		Pending: len(workflow.Steps),
		Total:   len(workflow.Steps),
		Percent: 0,
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	if background {
		runCtx = context.Background()
	}

	progress := make(chan pipeline.ProgressEvent, 256)
	done := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		for {
			select {
			case ev := <-progress:
				eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
				if !ok {
					continue
				}
				payload := map[string]interface{}{
					"run_id":      config.RunID,
					"workflow_id": workflow.Name,
					"session":     session,
					"step_id":     ev.StepID,
					"message":     ev.Message,
					"progress":    ev.Progress,
					"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
				}
				if ev.Type == "workflow_error" {
					payload["success"] = false
				}
				if ev.Type == "workflow_complete" {
					payload["success"] = true
				}
				s.publishPipelineEvent(session, eventType, payload)
			case <-done:
				for {
					select {
					case ev := <-progress:
						eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
						if !ok {
							continue
						}
						payload := map[string]interface{}{
							"run_id":      config.RunID,
							"workflow_id": workflow.Name,
							"session":     session,
							"step_id":     ev.StepID,
							"message":     ev.Message,
							"progress":    ev.Progress,
							"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
						}
						if ev.Type == "workflow_error" {
							payload["success"] = false
						}
						if ev.Type == "workflow_complete" {
							payload["success"] = true
						}
						s.publishPipelineEvent(session, eventType, payload)
					default:
						return
					}
				}
			}
		}
	}()

	if background {
		go func() {
			defer func() { _ = recover() }()
			defer close(done)
			_, _ = executor.Run(runCtx, workflow, variables, progress)
		}()
		output.Status = "running"
	} else {
		state, err := executor.Run(runCtx, workflow, variables, progress)
		close(done)
		if err != nil {
			output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodePipelineFailed, "pipeline execution failed")
			return output
		}
		output.RunID = state.RunID
		output.Status = string(state.Status)
	}

	return output
}

// resumePipelineWithResult resumes a pipeline from saved state
func (s *Server) resumePipelineWithResult(ctx context.Context, runID, session string, variables map[string]interface{}, state *pipeline.ExecutionState) pipeline.PipelineRunOutput {
	output := pipeline.PipelineRunOutput{}

	// Load workflow from state
	if state.WorkflowFile == "" {
		output.RobotResponse = pipeline.NewErrorResponse(nil, ErrCodeNoResumableState, "workflow file not recorded in state")
		return output
	}

	workflow, _, err := pipeline.LoadAndValidate(state.WorkflowFile)
	if err != nil {
		output.RobotResponse = pipeline.NewErrorResponse(err, ErrCodeInvalidWorkflow, "failed to reload workflow")
		return output
	}

	config := pipeline.DefaultExecutorConfig(session)
	projectDir, _ := os.Getwd()
	config.ProjectDir = projectDir
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

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}

	progress := make(chan pipeline.ProgressEvent, 256)
	done := make(chan struct{})
	go func() {
		defer func() { _ = recover() }()
		for {
			select {
			case ev := <-progress:
				eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
				if !ok {
					continue
				}
				payload := map[string]interface{}{
					"run_id":      config.RunID,
					"workflow_id": workflow.Name,
					"session":     session,
					"step_id":     ev.StepID,
					"message":     ev.Message,
					"progress":    ev.Progress,
					"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
				}
				if ev.Type == "workflow_error" {
					payload["success"] = false
				}
				if ev.Type == "workflow_complete" {
					payload["success"] = true
				}
				s.publishPipelineEvent(session, eventType, payload)
			case <-done:
				for {
					select {
					case ev := <-progress:
						eventType, ok := pipelineEventTypeFromProgressType(ev.Type)
						if !ok {
							continue
						}
						payload := map[string]interface{}{
							"run_id":      config.RunID,
							"workflow_id": workflow.Name,
							"session":     session,
							"step_id":     ev.StepID,
							"message":     ev.Message,
							"progress":    ev.Progress,
							"timestamp":   ev.Timestamp.UTC().Format(time.RFC3339Nano),
						}
						if ev.Type == "workflow_error" {
							payload["success"] = false
						}
						if ev.Type == "workflow_complete" {
							payload["success"] = true
						}
						s.publishPipelineEvent(session, eventType, payload)
					default:
						return
					}
				}
			}
		}
	}()

	newState, err := executor.Resume(runCtx, workflow, state, progress)
	close(done)
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
