// Package serve provides REST API endpoints for checkpoint management.
package serve

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/go-chi/chi/v5"
)

// Checkpoint API request/response types

// CreateCheckpointRequest is the payload for creating a new checkpoint.
type CreateCheckpointRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	CaptureGit      *bool  `json:"capture_git,omitempty"`
	ScrollbackLines *int   `json:"scrollback_lines,omitempty"`
}

// CheckpointResponse represents a checkpoint in API responses.
type CheckpointResponse struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Description string                    `json:"description,omitempty"`
	SessionName string                    `json:"session_name"`
	WorkingDir  string                    `json:"working_dir,omitempty"`
	CreatedAt   string                    `json:"created_at"`
	PaneCount   int                       `json:"pane_count"`
	Git         *CheckpointGitResponse    `json:"git,omitempty"`
	Session     *CheckpointSessionSummary `json:"session,omitempty"`
	Age         string                    `json:"age,omitempty"`
}

// CheckpointGitResponse represents git state in checkpoint responses.
type CheckpointGitResponse struct {
	Branch         string `json:"branch"`
	Commit         string `json:"commit"`
	IsDirty        bool   `json:"is_dirty"`
	StagedCount    int    `json:"staged_count,omitempty"`
	UnstagedCount  int    `json:"unstaged_count,omitempty"`
	UntrackedCount int    `json:"untracked_count,omitempty"`
	HasPatch       bool   `json:"has_patch,omitempty"`
}

// CheckpointSessionSummary summarizes session state in checkpoint responses.
type CheckpointSessionSummary struct {
	PaneCount       int      `json:"pane_count"`
	ActivePaneIndex int      `json:"active_pane_index"`
	Layout          string   `json:"layout,omitempty"`
	AgentTypes      []string `json:"agent_types,omitempty"`
}

// RestoreCheckpointRequest is the payload for restoring a checkpoint.
type RestoreCheckpointRequest struct {
	Force           bool   `json:"force,omitempty"`
	SkipGitCheck    bool   `json:"skip_git_check,omitempty"`
	InjectContext   bool   `json:"inject_context,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
	CustomDirectory string `json:"custom_directory,omitempty"`
	ScrollbackLines int    `json:"scrollback_lines,omitempty"`
}

// RestoreCheckpointResponse is the response after restoring a checkpoint.
type RestoreCheckpointResponse struct {
	SessionName     string   `json:"session_name"`
	PanesRestored   int      `json:"panes_restored"`
	ContextInjected bool     `json:"context_injected"`
	DryRun          bool     `json:"dry_run"`
	Warnings        []string `json:"warnings,omitempty"`
}

// VerifyCheckpointResponse is the response from checkpoint verification.
type VerifyCheckpointResponse struct {
	Valid            bool              `json:"valid"`
	SchemaValid      bool              `json:"schema_valid"`
	FilesPresent     bool              `json:"files_present"`
	ChecksumsValid   bool              `json:"checksums_valid"`
	ConsistencyValid bool              `json:"consistency_valid"`
	Errors           []string          `json:"errors,omitempty"`
	Warnings         []string          `json:"warnings,omitempty"`
	Details          map[string]string `json:"details,omitempty"`
}

// ExportCheckpointRequest is the payload for exporting a checkpoint.
type ExportCheckpointRequest struct {
	Format            string `json:"format,omitempty"` // "tar.gz" or "zip"
	RedactSecrets     bool   `json:"redact_secrets,omitempty"`
	RewritePaths      bool   `json:"rewrite_paths,omitempty"`
	IncludeScrollback *bool  `json:"include_scrollback,omitempty"`
	IncludeGitPatch   *bool  `json:"include_git_patch,omitempty"`
}

// ExportCheckpointResponse is the response after exporting a checkpoint.
type ExportCheckpointResponse struct {
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	Data        string `json:"data,omitempty"` // Base64 encoded if inline
	DownloadURL string `json:"download_url,omitempty"`
}

// ImportCheckpointRequest is the payload for importing a checkpoint.
type ImportCheckpointRequest struct {
	// Data is the base64-encoded archive content
	Data string `json:"data,omitempty"`
	// TargetSession overrides the session name on import
	TargetSession string `json:"target_session,omitempty"`
	// TargetDir overrides the working directory on import
	TargetDir string `json:"target_dir,omitempty"`
	// VerifyChecksums validates file integrity on import
	VerifyChecksums *bool `json:"verify_checksums,omitempty"`
	// AllowOverwrite permits overwriting existing checkpoints
	AllowOverwrite bool `json:"allow_overwrite,omitempty"`
}

// RollbackRequest is the payload for rolling back to a checkpoint.
type RollbackRequest struct {
	CheckpointRef string `json:"checkpoint_ref,omitempty"` // ID, name, or "~N" notation
	NoStash       bool   `json:"no_stash,omitempty"`
	NoGit         bool   `json:"no_git,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
}

// RollbackResponse is the response after a rollback operation.
type RollbackResponse struct {
	CheckpointID   string   `json:"checkpoint_id"`
	CheckpointName string   `json:"checkpoint_name"`
	GitRestored    bool     `json:"git_restored"`
	StashCreated   bool     `json:"stash_created,omitempty"`
	StashRef       string   `json:"stash_ref,omitempty"`
	DryRun         bool     `json:"dry_run"`
	Warnings       []string `json:"warnings,omitempty"`
}

// registerCheckpointRoutes registers checkpoint-related API routes.
func (s *Server) registerCheckpointRoutes(r chi.Router) {
	r.Route("/sessions/{sessionName}/checkpoints", func(r chi.Router) {
		// List checkpoints for a session
		r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleListCheckpoints)
		// Create a new checkpoint
		r.With(s.RequirePermission(PermWriteSessions)).Post("/", s.handleCreateCheckpoint)
		// Import a checkpoint from archive
		r.With(s.RequirePermission(PermWriteSessions)).Post("/import", s.handleImportCheckpoint)

		// Single checkpoint operations
		r.Route("/{checkpointId}", func(r chi.Router) {
			// Get checkpoint details
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleGetCheckpoint)
			// Delete a checkpoint
			r.With(s.RequirePermission(PermWriteSessions)).Delete("/", s.handleDeleteCheckpoint)
			// Restore checkpoint
			r.With(s.RequirePermission(PermWriteSessions)).Post("/restore", s.handleRestoreCheckpoint)
			// Verify checkpoint integrity
			r.With(s.RequirePermission(PermReadSessions)).Get("/verify", s.handleVerifyCheckpoint)
			// Export checkpoint to archive
			r.With(s.RequirePermission(PermReadSessions)).Get("/export", s.handleExportCheckpoint)
			r.With(s.RequirePermission(PermReadSessions)).Post("/export", s.handleExportCheckpoint)
		})
	})

	// Rollback endpoint at session level
	r.Route("/sessions/{sessionName}/rollback", func(r chi.Router) {
		r.With(s.RequirePermission(PermWriteSessions)).Post("/", s.handleRollback)
	})
}

// handleListCheckpoints lists all checkpoints for a session.
func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	// Parse optional query params
	includeDetails := r.URL.Query().Get("details") == "true"

	storage := checkpoint.NewStorage()
	checkpoints, err := storage.List(sessionName)
	if err != nil {
		log.Printf("REST: checkpoint list failed session=%s error=%v request_id=%s", sessionName, err, reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to list checkpoints", nil, reqID)
		return
	}

	// Convert to response format
	items := make([]CheckpointResponse, 0, len(checkpoints))
	for _, cp := range checkpoints {
		items = append(items, checkpointToResponse(cp, includeDetails))
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_name": sessionName,
		"count":        len(items),
		"checkpoints":  items,
	}, reqID)
}

// handleCreateCheckpoint creates a new checkpoint for a session.
func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	// Check session exists
	if !tmux.SessionExists(sessionName) {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("session %q does not exist", sessionName), nil, reqID)
		return
	}

	var req CreateCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	// Use default name if not provided
	if req.Name == "" {
		req.Name = time.Now().Format("2006-01-02_15-04-05")
	}

	// Build options
	var opts []checkpoint.CheckpointOption
	if req.Description != "" {
		opts = append(opts, checkpoint.WithDescription(req.Description))
	}
	if req.CaptureGit != nil {
		opts = append(opts, checkpoint.WithGitCapture(*req.CaptureGit))
	}
	if req.ScrollbackLines != nil {
		opts = append(opts, checkpoint.WithScrollbackLines(*req.ScrollbackLines))
	}

	capturer := checkpoint.NewCapturer()
	cp, err := capturer.Create(sessionName, req.Name, opts...)
	if err != nil {
		log.Printf("REST: checkpoint create failed session=%s name=%s error=%v request_id=%s",
			sessionName, req.Name, err, reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create checkpoint", nil, reqID)
		return
	}

	log.Printf("REST: checkpoint created session=%s id=%s name=%s request_id=%s",
		sessionName, cp.ID, cp.Name, reqID)

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"checkpoint": checkpointToResponse(cp, true),
	}, reqID)
}

// handleGetCheckpoint returns details for a specific checkpoint.
func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	storage := checkpoint.NewStorage()
	capturer := checkpoint.NewCapturerWithStorage(storage)

	// Parse checkpoint reference (can be ID, name, or ~N notation)
	cp, err := capturer.ParseCheckpointRef(sessionName, checkpointID)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointID),
			nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"checkpoint": checkpointToResponse(cp, true),
	}, reqID)
}

// handleDeleteCheckpoint deletes a checkpoint.
func (s *Server) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	storage := checkpoint.NewStorage()

	// First verify checkpoint exists
	if !storage.Exists(sessionName, checkpointID) {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
		return
	}

	if err := storage.Delete(sessionName, checkpointID); err != nil {
		log.Printf("REST: checkpoint delete failed session=%s id=%s error=%v request_id=%s",
			sessionName, checkpointID, err, reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to delete checkpoint", nil, reqID)
		return
	}

	log.Printf("REST: checkpoint deleted session=%s id=%s request_id=%s",
		sessionName, checkpointID, reqID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"deleted":       true,
		"checkpoint_id": checkpointID,
	}, reqID)
}

// handleRestoreCheckpoint restores a session from a checkpoint.
func (s *Server) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	var req RestoreCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	storage := checkpoint.NewStorage()
	restorer := checkpoint.NewRestorerWithStorage(storage)

	opts := checkpoint.RestoreOptions{
		Force:           req.Force,
		SkipGitCheck:    req.SkipGitCheck,
		InjectContext:   req.InjectContext,
		DryRun:          req.DryRun,
		CustomDirectory: req.CustomDirectory,
		ScrollbackLines: req.ScrollbackLines,
	}

	result, err := restorer.Restore(sessionName, checkpointID, opts)
	if err != nil {
		log.Printf("REST: checkpoint restore failed session=%s id=%s error=%v request_id=%s",
			sessionName, checkpointID, err, reqID)

		statusCode := http.StatusInternalServerError
		errCode := ErrCodeInternalError
		if err == checkpoint.ErrSessionExists {
			statusCode = http.StatusConflict
			errCode = "SESSION_EXISTS"
		} else if err == checkpoint.ErrDirectoryNotFound {
			statusCode = http.StatusBadRequest
			errCode = ErrCodeBadRequest
		}

		writeErrorResponse(w, statusCode, errCode,
			fmt.Sprintf("failed to restore checkpoint: %v", err), nil, reqID)
		return
	}

	log.Printf("REST: checkpoint restored session=%s id=%s panes=%d dry_run=%v request_id=%s",
		sessionName, checkpointID, result.PanesRestored, result.DryRun, reqID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_name":     result.SessionName,
		"panes_restored":   result.PanesRestored,
		"context_injected": result.ContextInjected,
		"dry_run":          result.DryRun,
		"warnings":         result.Warnings,
	}, reqID)
}

// handleVerifyCheckpoint verifies checkpoint integrity.
func (s *Server) handleVerifyCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	storage := checkpoint.NewStorage()
	cp, err := storage.Load(sessionName, checkpointID)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointID),
			nil, reqID)
		return
	}

	result := cp.Verify(storage)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"valid":            result.Valid,
		"schema_valid":     result.SchemaValid,
		"files_present":    result.FilesPresent,
		"checksums_valid":  result.ChecksumsValid,
		"consistency_valid": result.ConsistencyValid,
		"errors":           result.Errors,
		"warnings":         result.Warnings,
		"details":          result.Details,
	}, reqID)
}

// handleExportCheckpoint exports a checkpoint to an archive.
func (s *Server) handleExportCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	// Parse options from query params or body
	var req ExportCheckpointRequest
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"invalid request body", nil, reqID)
			return
		}
	} else {
		// Parse from query params for GET
		req.Format = r.URL.Query().Get("format")
		req.RedactSecrets = r.URL.Query().Get("redact_secrets") == "true"
		req.RewritePaths = r.URL.Query().Get("rewrite_paths") != "false" // default true
	}

	// Default format
	if req.Format == "" {
		req.Format = "tar.gz"
	}

	// Validate format
	var exportFormat checkpoint.ExportFormat
	switch strings.ToLower(req.Format) {
	case "tar.gz", "tgz":
		exportFormat = checkpoint.FormatTarGz
	case "zip":
		exportFormat = checkpoint.FormatZip
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("unsupported format: %s (use tar.gz or zip)", req.Format), nil, reqID)
		return
	}

	storage := checkpoint.NewStorage()

	// Check checkpoint exists
	if !storage.Exists(sessionName, checkpointID) {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
		return
	}

	opts := checkpoint.ExportOptions{
		Format:            exportFormat,
		RedactSecrets:     req.RedactSecrets,
		RewritePaths:      req.RewritePaths,
		IncludeScrollback: true,
		IncludeGitPatch:   true,
	}
	if req.IncludeScrollback != nil {
		opts.IncludeScrollback = *req.IncludeScrollback
	}
	if req.IncludeGitPatch != nil {
		opts.IncludeGitPatch = *req.IncludeGitPatch
	}

	// Create temp file for export
	ext := ".tar.gz"
	if exportFormat == checkpoint.FormatZip {
		ext = ".zip"
	}
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("ntm-export-*%s", ext))
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create temp file", nil, reqID)
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	_, err = storage.Export(sessionName, checkpointID, tmpPath, opts)
	if err != nil {
		log.Printf("REST: checkpoint export failed session=%s id=%s error=%v request_id=%s",
			sessionName, checkpointID, err, reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to export checkpoint", nil, reqID)
		return
	}

	// Check if client wants download or inline response
	if r.URL.Query().Get("download") == "true" || r.Header.Get("Accept") == "application/octet-stream" {
		// Stream file directly
		data, err := os.ReadFile(tmpPath)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to read export file", nil, reqID)
			return
		}

		filename := fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
		return
	}

	// Return base64-encoded data in JSON response
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to read export file", nil, reqID)
		return
	}

	contentType := "application/gzip"
	if exportFormat == checkpoint.FormatZip {
		contentType = "application/zip"
	}

	log.Printf("REST: checkpoint exported session=%s id=%s size=%d request_id=%s",
		sessionName, checkpointID, len(data), reqID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"filename":     fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext),
		"size":         int64(len(data)),
		"content_type": contentType,
		"data":         base64.StdEncoding.EncodeToString(data),
	}, reqID)
}

// handleImportCheckpoint imports a checkpoint from an archive.
func (s *Server) handleImportCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	var req ImportCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	if req.Data == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"data field is required (base64-encoded archive)", nil, reqID)
		return
	}

	// Decode base64 data
	archiveData, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid base64 data", nil, reqID)
		return
	}

	// Detect format from magic bytes
	ext := ".tar.gz"
	if len(archiveData) >= 4 && archiveData[0] == 'P' && archiveData[1] == 'K' {
		ext = ".zip"
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("ntm-import-*%s", ext))
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create temp file", nil, reqID)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(archiveData); err != nil {
		tmpFile.Close()
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to write temp file", nil, reqID)
		return
	}
	tmpFile.Close()

	storage := checkpoint.NewStorage()
	opts := checkpoint.ImportOptions{
		TargetSession:   sessionName,
		TargetDir:       req.TargetDir,
		VerifyChecksums: true,
		AllowOverwrite:  req.AllowOverwrite,
	}
	if req.VerifyChecksums != nil {
		opts.VerifyChecksums = *req.VerifyChecksums
	}
	if req.TargetSession != "" {
		opts.TargetSession = req.TargetSession
	}

	cp, err := storage.Import(tmpPath, opts)
	if err != nil {
		log.Printf("REST: checkpoint import failed session=%s error=%v request_id=%s",
			sessionName, err, reqID)
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"failed to import checkpoint", nil, reqID)
		return
	}

	log.Printf("REST: checkpoint imported session=%s id=%s name=%s request_id=%s",
		cp.SessionName, cp.ID, cp.Name, reqID)

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"imported":   true,
		"checkpoint": checkpointToResponse(cp, true),
	}, reqID)
}

// handleRollback performs a rollback to a checkpoint's git state.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	var req RollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}

	// Default to latest checkpoint
	checkpointRef := req.CheckpointRef
	if checkpointRef == "" {
		checkpointRef = "latest"
	}

	storage := checkpoint.NewStorage()
	capturer := checkpoint.NewCapturerWithStorage(storage)

	// Parse checkpoint reference
	cp, err := capturer.ParseCheckpointRef(sessionName, checkpointRef)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointRef),
			nil, reqID)
		return
	}

	// Verify checkpoint has git state
	if cp.Git.Commit == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"checkpoint has no git state to rollback to", nil, reqID)
		return
	}

	// Get working directory
	workDir := cp.WorkingDir
	if workDir == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"checkpoint has no working directory", nil, reqID)
		return
	}

	resp := RollbackResponse{
		CheckpointID:   cp.ID,
		CheckpointName: cp.Name,
		DryRun:         req.DryRun,
	}

	if req.DryRun {
		// Just return what would happen
		resp.Warnings = append(resp.Warnings,
			fmt.Sprintf("would rollback to commit %s on branch %s", cp.Git.Commit[:8], cp.Git.Branch))
		if !req.NoGit && cp.Git.IsDirty && !req.NoStash {
			resp.Warnings = append(resp.Warnings, "would stash current changes")
		}
		writeSuccessResponse(w, http.StatusOK, rollbackResponseToMap(resp), reqID)
		return
	}

	// Perform rollback
	if !req.NoGit {
		// Stash current changes if dirty and not skipped
		if !req.NoStash {
			stashRef, err := gitStashIfDirty(workDir)
			if err != nil {
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("stash failed: %v", err))
			} else if stashRef != "" {
				resp.StashCreated = true
				resp.StashRef = stashRef
			}
		}

		// Checkout the checkpoint commit
		if err := gitCheckout(workDir, cp.Git.Commit); err != nil {
			log.Printf("REST: rollback checkout failed session=%s commit=%s error=%v request_id=%s",
				sessionName, cp.Git.Commit, err, reqID)
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to checkout commit", nil, reqID)
			return
		}

		// Apply git patch if available
		if cp.HasGitPatch() {
			patchPath := filepath.Join(storage.CheckpointDir(sessionName, cp.ID), cp.Git.PatchFile)
			if err := gitApplyPatch(workDir, patchPath); err != nil {
				resp.Warnings = append(resp.Warnings, fmt.Sprintf("patch apply failed: %v", err))
			}
		}

		resp.GitRestored = true
	}

	log.Printf("REST: rollback completed session=%s checkpoint=%s git=%v request_id=%s",
		sessionName, cp.ID, resp.GitRestored, reqID)

	writeSuccessResponse(w, http.StatusOK, rollbackResponseToMap(resp), reqID)
}

// rollbackResponseToMap converts a RollbackResponse to a map for JSON response.
func rollbackResponseToMap(r RollbackResponse) map[string]interface{} {
	m := map[string]interface{}{
		"checkpoint_id":   r.CheckpointID,
		"checkpoint_name": r.CheckpointName,
		"git_restored":    r.GitRestored,
		"dry_run":         r.DryRun,
	}
	if r.StashCreated {
		m["stash_created"] = r.StashCreated
		m["stash_ref"] = r.StashRef
	}
	if len(r.Warnings) > 0 {
		m["warnings"] = r.Warnings
	}
	return m
}

// checkpointToResponse converts a checkpoint to its API response format.
func checkpointToResponse(cp *checkpoint.Checkpoint, includeDetails bool) CheckpointResponse {
	resp := CheckpointResponse{
		ID:          cp.ID,
		Name:        cp.Name,
		Description: cp.Description,
		SessionName: cp.SessionName,
		WorkingDir:  cp.WorkingDir,
		CreatedAt:   cp.CreatedAt.Format(time.RFC3339),
		PaneCount:   cp.PaneCount,
		Age:         formatAge(cp.Age()),
	}

	// Add git info if present
	if cp.Git.Branch != "" {
		resp.Git = &CheckpointGitResponse{
			Branch:         cp.Git.Branch,
			Commit:         cp.Git.Commit,
			IsDirty:        cp.Git.IsDirty,
			StagedCount:    cp.Git.StagedCount,
			UnstagedCount:  cp.Git.UnstagedCount,
			UntrackedCount: cp.Git.UntrackedCount,
			HasPatch:       cp.HasGitPatch(),
		}
	}

	// Add session details if requested
	if includeDetails {
		agentTypes := make([]string, 0, len(cp.Session.Panes))
		for _, pane := range cp.Session.Panes {
			if pane.AgentType != "" {
				agentTypes = append(agentTypes, pane.AgentType)
			}
		}
		resp.Session = &CheckpointSessionSummary{
			PaneCount:       len(cp.Session.Panes),
			ActivePaneIndex: cp.Session.ActivePaneIndex,
			Layout:          cp.Session.Layout,
			AgentTypes:      agentTypes,
		}
	}

	return resp
}

// formatAge returns a human-readable age string.
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// gitStashIfDirty stashes changes if the working tree is dirty.
func gitStashIfDirty(workDir string) (string, error) {
	// Check if dirty
	out, err := runGit(workDir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil // Not dirty
	}

	// Stash changes
	stashMsg := fmt.Sprintf("ntm-rollback-%s", time.Now().Format("20060102-150405"))
	if _, err := runGit(workDir, "stash", "push", "-m", stashMsg); err != nil {
		return "", err
	}

	return stashMsg, nil
}

// gitCheckout checks out a specific commit.
func gitCheckout(workDir, ref string) error {
	_, err := runGit(workDir, "checkout", ref)
	return err
}

// gitApplyPatch applies a git patch file.
func gitApplyPatch(workDir, patchPath string) error {
	_, err := runGit(workDir, "apply", patchPath)
	return err
}

// runGit runs a git command and returns the output.
func runGit(workDir string, args ...string) (string, error) {
	allArgs := append([]string{"-C", workDir}, args...)
	cmd := exec.Command("git", allArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
