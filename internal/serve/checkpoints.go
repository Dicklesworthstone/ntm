// Package serve provides REST API endpoints for checkpoint management.
package serve

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Match checkpoint import's expanded-entry and aggregate limits. The HTTP
// disclosure boundary additionally bounds the exact compressed snapshot and
// entry count before inspection; it never admits a scanned prefix as an archive.
const (
	checkpointDisclosureArchiveBytes = 64 << 20
	checkpointDisclosureEntryBytes   = 100 << 20
	checkpointDisclosureTotalBytes   = 1 << 30
	checkpointDisclosureEntries      = 4096
	checkpointDisclosureTarPadding   = 1 << 20
)

var (
	errCheckpointDisclosureSensitive = errors.New("checkpoint archive contains sensitive content")
	errCheckpointDisclosureLimit     = errors.New("checkpoint archive exceeds inspection limits")
	errCheckpointDisclosureInvalid   = errors.New("checkpoint archive could not be safely inspected")
)

func (s *Server) checkpointDisclosureConfig() (redaction.Config, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.redactionCfg == nil || !s.redactionCfg.Enabled ||
		(s.redactionCfg.Config.Mode != redaction.ModeRedact && s.redactionCfg.Config.Mode != redaction.ModeBlock) {
		return redaction.Config{}, false
	}
	cfg := s.redactionCfg.Config.DeepCopy()
	cfg.Mode = redaction.ModeBlock
	return cfg, true
}

type checkpointDisclosureReader struct {
	ctx context.Context
	r   io.Reader
}

func (r checkpointDisclosureReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// gzip.Reader's multistream mode exposes only the first member's header.
// Iterate explicitly so later headers cannot hide secrets, while presenting
// one decoded stream so credentials split between members remain detectable.
type checkpointDisclosureGzip struct {
	compressed *bytes.Reader
	current    *gzip.Reader
	scan       func([]byte) error
	remaining  int64
	members    int
}

func (r *checkpointDisclosureGzip) Read(p []byte) (int, error) {
	for {
		if r.current == nil {
			if r.compressed.Len() == 0 {
				if r.members == 0 {
					return 0, errCheckpointDisclosureInvalid
				}
				return 0, io.EOF
			}
			if r.members >= checkpointDisclosureEntries {
				return 0, errCheckpointDisclosureLimit
			}
			gz, err := gzip.NewReader(r.compressed)
			if err != nil {
				return 0, errCheckpointDisclosureInvalid
			}
			gz.Multistream(false)
			for _, metadata := range [][]byte{[]byte(gz.Name), []byte(gz.Comment), gz.Extra} {
				if err := r.scan(metadata); err != nil {
					gz.Close()
					return 0, err
				}
			}
			r.current = gz
			r.members++
		}
		n, err := r.current.Read(p)
		r.remaining -= int64(n)
		if r.remaining < 0 {
			return n, errCheckpointDisclosureLimit
		}
		if err == io.EOF {
			closeErr := r.current.Close()
			r.current = nil
			if closeErr != nil {
				return n, errCheckpointDisclosureInvalid
			}
			if n != 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, errCheckpointDisclosureInvalid
		}
		return n, nil
	}
}

func (r *checkpointDisclosureGzip) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

// inspectCheckpointArchive checks the exact encoded snapshot used for export or
// import. Recovery metadata and patches are executable state: rewriting them
// could produce a valid archive that restores a different command or credential.
// This boundary only refuses; explicit scrollback redaction remains the export
// caller's separate choice. No source bytes or entry names escape in errors.
func inspectCheckpointArchive(ctx context.Context, data []byte, format checkpoint.ExportFormat, cfg redaction.Config) error {
	if len(data) > checkpointDisclosureArchiveBytes {
		return errCheckpointDisclosureLimit
	}
	cfg.Mode = redaction.ModeBlock
	scan := func(data []byte) error {
		if ctx.Err() != nil {
			return errCheckpointDisclosureInvalid
		}
		if redaction.ScanAndRedact(string(data), cfg).Blocked {
			return errCheckpointDisclosureSensitive
		}
		return nil
	}
	var expanded int64
	readEntry := func(reader io.Reader) ([]byte, error) {
		limit := min(int64(checkpointDisclosureEntryBytes), int64(checkpointDisclosureTotalBytes)-expanded)
		if limit < 0 {
			return nil, errCheckpointDisclosureLimit
		}
		entry, err := io.ReadAll(io.LimitReader(checkpointDisclosureReader{ctx: ctx, r: reader}, limit+1))
		if int64(len(entry)) > limit {
			return nil, errCheckpointDisclosureLimit
		}
		if err != nil {
			if errors.Is(err, errCheckpointDisclosureSensitive) || errors.Is(err, errCheckpointDisclosureLimit) {
				return nil, err
			}
			return nil, errCheckpointDisclosureInvalid
		}
		expanded += int64(len(entry))
		return entry, nil
	}
	inspectEntry := func(name string, reader io.Reader) error {
		if err := scan([]byte(name)); err != nil {
			return err
		}
		entry, err := readEntry(reader)
		if err != nil {
			return err
		}
		// Scrollback is often itself gzip-compressed inside either archive
		// format. Inspect its decoded bytes, including the gzip checksum/end.
		if strings.EqualFold(filepath.Ext(name), ".gz") || bytes.HasPrefix(entry, []byte{0x1f, 0x8b}) {
			gz := &checkpointDisclosureGzip{compressed: bytes.NewReader(entry), scan: scan, remaining: checkpointDisclosureEntryBytes}
			decoded, readErr := readEntry(gz)
			closeErr := gz.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return errCheckpointDisclosureInvalid
			}
			return scan(decoded)
		}
		return scan(entry)
	}

	switch format {
	case checkpoint.FormatZip:
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return errCheckpointDisclosureInvalid
		}
		if len(archive.File) > checkpointDisclosureEntries {
			return errCheckpointDisclosureLimit
		}
		if err := scan([]byte(archive.Comment)); err != nil {
			return err
		}
		for _, file := range archive.File {
			if file.UncompressedSize64 > checkpointDisclosureEntryBytes ||
				file.UncompressedSize64 > uint64(int64(checkpointDisclosureTotalBytes)-expanded) {
				return errCheckpointDisclosureLimit
			}
			for _, metadata := range [][]byte{[]byte(file.Comment), file.Extra} {
				if err := scan(metadata); err != nil {
					return err
				}
			}
			reader, err := file.Open()
			if err != nil {
				return errCheckpointDisclosureInvalid
			}
			inspectErr := inspectEntry(file.Name, reader)
			closeErr := reader.Close()
			if inspectErr != nil {
				return inspectErr
			}
			if closeErr != nil {
				return errCheckpointDisclosureInvalid
			}
		}
	case checkpoint.FormatTarGz:
		// Count the complete expanded tar stream, including PAX/GNU headers
		// consumed internally by archive/tar rather than returned as entries.
		gz := &checkpointDisclosureGzip{compressed: bytes.NewReader(data), scan: scan, remaining: checkpointDisclosureTotalBytes}
		defer gz.Close()
		archive := tar.NewReader(checkpointDisclosureReader{ctx: ctx, r: gz})
		for count := 0; ; count++ {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				if errors.Is(err, errCheckpointDisclosureSensitive) || errors.Is(err, errCheckpointDisclosureLimit) {
					return err
				}
				return errCheckpointDisclosureInvalid
			}
			if count >= checkpointDisclosureEntries || header.Size > checkpointDisclosureEntryBytes ||
				header.Size > int64(checkpointDisclosureTotalBytes)-expanded {
				return errCheckpointDisclosureLimit
			}
			// Only regular files and directory markers are checkpoint data.
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
				return errCheckpointDisclosureInvalid
			}
			for _, metadata := range []string{header.Uname, header.Gname, header.Linkname} {
				if err := scan([]byte(metadata)); err != nil {
					return err
				}
			}
			for key, value := range header.PAXRecords {
				if err := scan([]byte(key + "=" + value)); err != nil {
					return err
				}
			}
			if err := inspectEntry(header.Name, archive); err != nil {
				return err
			}
		}
		// Tar EOF occurs before the gzip trailer. Finish it and refuse trailing
		// material instead of treating an unchecked suffix as inspected data.
		padding, err := io.ReadAll(io.LimitReader(checkpointDisclosureReader{ctx: ctx, r: gz}, checkpointDisclosureTarPadding+1))
		if len(padding) > checkpointDisclosureTarPadding {
			return errCheckpointDisclosureLimit
		}
		if errors.Is(err, errCheckpointDisclosureSensitive) || errors.Is(err, errCheckpointDisclosureLimit) {
			return err
		}
		if err != nil || len(bytes.Trim(padding, "\x00")) != 0 {
			return errCheckpointDisclosureInvalid
		}
	default:
		return errCheckpointDisclosureInvalid
	}
	return nil
}

func writeCheckpointDisclosureError(w http.ResponseWriter, err error, requestID string, importing bool) {
	status, code := http.StatusUnprocessableEntity, "CHECKPOINT_SENSITIVE_CONTENT"
	message := "checkpoint archive contains sensitive content; refused without changing checkpoint values"
	if errors.Is(err, errCheckpointDisclosureLimit) {
		code, message = "CHECKPOINT_INSPECTION_LIMIT", "checkpoint archive exceeds safe inspection limits; refused without changing checkpoint values"
	} else if !errors.Is(err, errCheckpointDisclosureSensitive) {
		status, code, message = http.StatusInternalServerError, ErrCodeInternalError, "failed to inspect checkpoint export"
		if importing {
			status, code, message = http.StatusBadRequest, ErrCodeBadRequest, "checkpoint archive could not be safely inspected"
		}
	}
	writeErrorResponse(w, status, code, message, nil, requestID)
}

// Checkpoint API request/response types

// CreateCheckpointRequest is the payload for creating a new checkpoint.
type CreateCheckpointRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	CaptureGit       *bool  `json:"capture_git,omitempty"`
	IncludeUntracked bool   `json:"include_untracked,omitempty"`
	ScrollbackLines  *int   `json:"scrollback_lines,omitempty"`
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
	Branch            string `json:"branch"`
	Commit            string `json:"commit"`
	IsDirty           bool   `json:"is_dirty"`
	StagedCount       int    `json:"staged_count,omitempty"`
	UnstagedCount     int    `json:"unstaged_count,omitempty"`
	UntrackedCount    int    `json:"untracked_count,omitempty"`
	UntrackedCaptured int    `json:"untracked_captured,omitempty"`
	HasPatch          bool   `json:"has_patch,omitempty"`
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
	TargetSession   string `json:"target_session,omitempty"`
	Force           bool   `json:"force,omitempty"`
	SkipGitCheck    bool   `json:"skip_git_check,omitempty"`
	InjectContext   bool   `json:"inject_context,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
	CustomDirectory string `json:"custom_directory,omitempty"`
	ScrollbackLines int    `json:"scrollback_lines,omitempty"`
}

// RestoreCheckpointResponse is the response after restoring a checkpoint.
type RestoreCheckpointResponse struct {
	SessionName          string   `json:"session_name"`
	SourceSession        string   `json:"source_session"`
	PanesRestored        int      `json:"panes_restored"`
	ContextInjected      bool     `json:"context_injected"`
	ContextPanesInjected int      `json:"context_panes_injected"`
	DryRun               bool     `json:"dry_run"`
	Warnings             []string `json:"warnings,omitempty"`
	Stage                string   `json:"stage"`
	Interrupted          bool     `json:"interrupted"`
}

// VerifyCheckpointResponse is the response from checkpoint verification.
type VerifyCheckpointResponse struct {
	Valid            bool              `json:"valid"`
	SchemaValid      bool              `json:"schema_valid"`
	FilesPresent     bool              `json:"files_present"`
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

func loadExactCheckpointOrWriteError(w http.ResponseWriter, storage *checkpoint.Storage, sessionName, checkpointID, reqID string) (*checkpoint.Checkpoint, bool) {
	cp, err := storage.Load(sessionName, checkpointID)
	if err == nil {
		return cp, true
	}

	exists, existsErr := storage.HasCheckpointPath(sessionName, checkpointID)
	if existsErr != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, existsErr.Error(), nil, reqID)
		return nil, false
	}
	if !exists {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
		return nil, false
	}

	writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
		fmt.Sprintf("failed to load checkpoint: %v", err), nil, reqID)
	return nil, false
}

func writeCheckpointRefLoadFailureIfExact(w http.ResponseWriter, storage *checkpoint.Storage, sessionName, checkpointRef string, resolveErr error, reqID string) bool {
	if !checkpoint.IsValidCheckpointID(checkpointRef) {
		return false
	}
	exists, existsErr := storage.HasCheckpointPath(sessionName, checkpointRef)
	if existsErr != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, existsErr.Error(), nil, reqID)
		return true
	}
	if !exists {
		return false
	}
	writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
		fmt.Sprintf("failed to load checkpoint: %v", resolveErr), nil, reqID)
	return true
}

// handleListCheckpoints lists all checkpoints for a session.
func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	if err := tmux.ValidateSessionName(sessionName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

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
	invalidIDs, err := storage.InvalidCheckpointIDs(sessionName)
	if err != nil {
		log.Printf("REST: invalid checkpoint list failed session=%s error=%v request_id=%s", sessionName, err, reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to inspect checkpoint state", nil, reqID)
		return
	}

	// Convert to response format
	items := make([]CheckpointResponse, 0, len(checkpoints))
	for _, cp := range checkpoints {
		items = append(items, checkpointToResponse(cp, includeDetails))
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_name":                sessionName,
		"count":                       len(items),
		"checkpoints":                 items,
		"invalid_checkpoints_present": len(invalidIDs) > 0,
		"invalid_checkpoint_ids":      invalidIDs,
	}, reqID)
}

// handleCreateCheckpoint creates a new checkpoint for a session.
func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())

	var req CreateCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}
	if req.IncludeUntracked && req.CaptureGit != nil && !*req.CaptureGit {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"include_untracked requires capture_git", nil, reqID)
		return
	}
	// Reject incompatible capture options before probing a live session.
	if !tmux.SessionExists(sessionName) {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("session %q does not exist", sessionName), nil, reqID)
		return
	}

	// Use default name if not provided
	if req.Name == "" {
		req.Name = time.Now().Format("2006-01-02_15-04-05")
	}

	// Build options
	opts := []checkpoint.CheckpointOption{checkpoint.WithUntrackedCapture(req.IncludeUntracked)}
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
		var details map[string]interface{}
		if cp != nil {
			details = map[string]interface{}{
				"checkpoint":      checkpointToResponse(cp, true),
				"git_unavailable": checkpoint.DescribeGitUnavailable(cp),
			}
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create checkpoint", details, reqID)
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
		if writeCheckpointRefLoadFailureIfExact(w, storage, sessionName, checkpointID, err, reqID) {
			return
		}
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

	deletingInvalid := false
	if _, err := storage.Load(sessionName, checkpointID); err != nil {
		exists, existsErr := storage.HasCheckpointPath(sessionName, checkpointID)
		if existsErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, existsErr.Error(), nil, reqID)
			return
		}
		if !exists {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
			return
		}
		deletingInvalid = true
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
		"deleted":            true,
		"checkpoint_id":      checkpointID,
		"invalid_checkpoint": deletingInvalid,
	}, reqID)
}

// handleRestoreCheckpoint restores a session from a checkpoint.
func (s *Server) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	var req RestoreCheckpointRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid request body", nil, reqID)
		return
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"request body must contain one JSON object", nil, reqID)
		return
	}
	if req.TargetSession != "" {
		if err := tmux.ValidateSessionName(req.TargetSession); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"invalid target_session: "+err.Error(), nil, reqID)
			return
		}
	}
	if req.ScrollbackLines < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"scrollback_lines must be non-negative", nil, reqID)
		return
	}

	storage := checkpoint.NewStorage()
	restorer := checkpoint.NewRestorerWithStorage(storage)
	cp, ok := loadExactCheckpointOrWriteError(w, storage, sessionName, checkpointID, reqID)
	if !ok {
		return
	}

	opts := checkpoint.RestoreOptions{
		TargetSession:   req.TargetSession,
		Force:           req.Force,
		SkipGitCheck:    req.SkipGitCheck,
		InjectContext:   req.InjectContext,
		DryRun:          req.DryRun,
		CustomDirectory: req.CustomDirectory,
		ScrollbackLines: req.ScrollbackLines,
	}

	result, err := restorer.RestoreFromCheckpointContext(r.Context(), cp, opts)
	payload := map[string]interface{}{
		"checkpoint_id":  cp.ID,
		"source_session": sessionName,
		"session_name":   sessionName,
	}
	if req.TargetSession != "" {
		payload["session_name"] = req.TargetSession
	}
	if result != nil {
		payload["session_name"] = result.SessionName
		payload["panes_restored"] = result.PanesRestored
		payload["context_injected"] = result.ContextInjected
		payload["context_panes_injected"] = result.ContextPanesInjected
		payload["dry_run"] = result.DryRun
		payload["warnings"] = result.Warnings
		payload["stage"] = result.Stage
		payload["interrupted"] = result.Interrupted
	}
	if err != nil {
		log.Printf("REST: checkpoint restore failed session=%s id=%s error=%v request_id=%s",
			sessionName, checkpointID, err, reqID)

		statusCode := http.StatusInternalServerError
		errCode := ErrCodeInternalError
		if errors.Is(err, context.DeadlineExceeded) {
			statusCode = http.StatusGatewayTimeout
			errCode = ErrCodeTimeout
		} else if errors.Is(err, context.Canceled) {
			statusCode = http.StatusRequestTimeout
			errCode = "CANCELLED"
		} else if errors.Is(err, checkpoint.ErrSessionExists) {
			statusCode = http.StatusConflict
			errCode = "SESSION_EXISTS"
		} else if errors.Is(err, checkpoint.ErrDirectoryNotFound) || errors.Is(err, checkpoint.ErrWorkingDirInvalid) {
			statusCode = http.StatusBadRequest
			errCode = ErrCodeBadRequest
		}

		writeErrorResponse(w, statusCode, errCode,
			fmt.Sprintf("failed to restore checkpoint: %v", err), payload, reqID)
		return
	}

	log.Printf("REST: checkpoint restored session=%s id=%s panes=%d dry_run=%v request_id=%s",
		sessionName, checkpointID, result.PanesRestored, result.DryRun, reqID)

	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleVerifyCheckpoint verifies checkpoint integrity.
func (s *Server) handleVerifyCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())

	storage := checkpoint.NewStorage()
	exists, err := storage.HasCheckpointPath(sessionName, checkpointID)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	if !exists {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
		return
	}

	result := checkpoint.VerifyStoredCheckpoint(storage, sessionName, checkpointID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"valid":             result.Valid,
		"schema_valid":      result.SchemaValid,
		"files_present":     result.FilesPresent,
		"consistency_valid": result.ConsistencyValid,
		"errors":            result.Errors,
		"warnings":          result.Warnings,
		"details":           result.Details,
	}, reqID)
}

// handleExportCheckpoint exports a checkpoint to an archive.
func (s *Server) handleExportCheckpoint(w http.ResponseWriter, r *http.Request) {
	sessionName := chi.URLParam(r, "sessionName")
	checkpointID := chi.URLParam(r, "checkpointId")
	reqID := requestIDFromContext(r.Context())
	disclosureCfg, protectArchive := s.checkpointDisclosureConfig()

	// Validate before the name reaches a filesystem path or the
	// Content-Disposition header. Sibling handlers validate here; export did
	// not, and it is the one that interpolates the name straight into a
	// response header. ValidateSessionName's allowlist (a-z, A-Z, 0-9, _, -)
	// is what excludes CR/LF, so the check is load-bearing, not cosmetic.
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %v", err), nil, reqID)
		return
	}

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

	// Check checkpoint exists and is loadable. Exact malformed checkpoint IDs
	// should report a load failure rather than "not found".
	if _, err := storage.Load(sessionName, checkpointID); err != nil {
		exists, existsErr := storage.HasCheckpointPath(sessionName, checkpointID)
		if existsErr != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, existsErr.Error(), nil, reqID)
			return
		}
		if !exists {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("checkpoint not found: %s", checkpointID), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("failed to load checkpoint: %v", err), nil, reqID)
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
	if err := tmpFile.Close(); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to prepare temp file", nil, reqID)
		return
	}
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("REST: remove temp export file failed path=%s error=%v request_id=%s", tmpPath, err, reqID)
		}
	}()

	_, err = storage.Export(sessionName, checkpointID, tmpPath, opts)
	if err != nil {
		if protectArchive {
			// Export errors can contain sensitive artifact names before a
			// completed archive is available for disclosure inspection.
			log.Printf("REST: checkpoint export failed request_id=%s", reqID)
		} else {
			log.Printf("REST: checkpoint export failed session=%s id=%s error=%v request_id=%s",
				sessionName, checkpointID, err, reqID)
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to export checkpoint", nil, reqID)
		return
	}

	// Inspect the completed archive, including metadata and patches that the
	// explicit scrollback-redaction option intentionally leaves unchanged.
	// Keep the admitted snapshot for both wire forms: do not reopen mutable
	// source artifacts after inspection or rewrite recovery values implicitly.
	var admitted []byte
	if protectArchive {
		file, openErr := os.Open(tmpPath)
		if openErr != nil {
			writeCheckpointDisclosureError(w, errCheckpointDisclosureInvalid, reqID, false)
			return
		}
		admitted, err = io.ReadAll(io.LimitReader(checkpointDisclosureReader{ctx: r.Context(), r: file}, checkpointDisclosureArchiveBytes+1))
		closeErr := file.Close()
		if len(admitted) > checkpointDisclosureArchiveBytes {
			writeCheckpointDisclosureError(w, errCheckpointDisclosureLimit, reqID, false)
			return
		}
		if err != nil || closeErr != nil {
			writeCheckpointDisclosureError(w, errCheckpointDisclosureInvalid, reqID, false)
			return
		}
		if err := inspectCheckpointArchive(r.Context(), admitted, exportFormat, disclosureCfg); err != nil {
			writeCheckpointDisclosureError(w, err, reqID, false)
			return
		}
	}

	// Check if client wants download or inline response
	if r.URL.Query().Get("download") == "true" || r.Header.Get("Accept") == "application/octet-stream" {
		if protectArchive {
			filename := fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext)
			if redaction.ScanAndRedact(filename, disclosureCfg).Blocked {
				writeCheckpointDisclosureError(w, errCheckpointDisclosureSensitive, reqID, false)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
			w.Header().Set("Content-Length", strconv.Itoa(len(admitted)))
			if _, err := w.Write(admitted); err != nil {
				log.Print("REST: failed to write inspected checkpoint export")
			}
			return
		}
		f, err := os.Open(tmpPath)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to open export file", nil, reqID)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to stat export file", nil, reqID)
			return
		}

		filename := fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("REST: failed to write checkpoint export data request_id=%s: %v", reqID, err)
		}
		return
	}

	// Return base64-encoded data in JSON response
	data := admitted
	if !protectArchive {
		data, err = os.ReadFile(tmpPath)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to read export file", nil, reqID)
			return
		}
	}

	contentType := "application/gzip"
	if exportFormat == checkpoint.FormatZip {
		contentType = "application/zip"
	}

	log.Printf("REST: checkpoint exported session=%s id=%s size=%d request_id=%s",
		sessionName, checkpointID, len(data), reqID)

	response := map[string]interface{}{
		"filename":     fmt.Sprintf("%s_%s%s", sessionName, checkpointID, ext),
		"size":         int64(len(data)),
		"content_type": contentType,
		"data":         base64.StdEncoding.EncodeToString(data),
	}
	if protectArchive {
		// Even an accidental/custom match in opaque base64 must not turn a
		// valid JSON response into a silently corrupted recovery archive.
		// Construct and inspect the exact final envelope before writing it.
		response["success"] = true
		response["timestamp"] = time.Now().UTC().Format(time.RFC3339)
		if reqID != "" {
			response["request_id"] = reqID
		}
		encoded, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			writeCheckpointDisclosureError(w, errCheckpointDisclosureInvalid, reqID, false)
			return
		}
		encoded = append(encoded, '\n')
		if redaction.ScanAndRedact(string(encoded), disclosureCfg).Blocked {
			writeCheckpointDisclosureError(w, errCheckpointDisclosureSensitive, reqID, false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(encoded); err != nil {
			log.Print("REST: failed to write inspected checkpoint export")
		}
		return
	}
	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleImportCheckpoint imports a checkpoint from an archive.
func (s *Server) handleImportCheckpoint(w http.ResponseWriter, r *http.Request) {
	s.handleImportCheckpointWithImporter(w, r, checkpoint.NewStorage().Import)
}

// Keep the import dependency local to the request so publication failure tests
// can exercise the HTTP contract without replacing process-wide filesystem ops.
func (s *Server) handleImportCheckpointWithImporter(w http.ResponseWriter, r *http.Request,
	importCheckpoint func(string, checkpoint.ImportOptions) (*checkpoint.Checkpoint, error),
) {
	sessionName := chi.URLParam(r, "sessionName")
	reqID := requestIDFromContext(r.Context())
	disclosureCfg, protectArchive := s.checkpointDisclosureConfig()

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

	// JSON request admission cannot see through base64 and compression.
	// Refuse sensitive recovery state before Import can publish or overwrite
	// a checkpoint; never replace values that a later restore will execute.
	if protectArchive {
		format := checkpoint.FormatTarGz
		if ext == ".zip" {
			format = checkpoint.FormatZip
		}
		if err := inspectCheckpointArchive(r.Context(), archiveData, format, disclosureCfg); err != nil {
			writeCheckpointDisclosureError(w, err, reqID, true)
			return
		}
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("ntm-import-*%s", ext))
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to create temp file", nil, reqID)
		return
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("REST: remove temp import file failed path=%s error=%v request_id=%s", tmpPath, err, reqID)
		}
	}()

	if _, err := tmpFile.Write(archiveData); err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			log.Printf("REST: close failed temp import file path=%s error=%v request_id=%s", tmpPath, closeErr, reqID)
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to write temp file", nil, reqID)
		return
	}
	if err := tmpFile.Close(); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			"failed to finalize temp file", nil, reqID)
		return
	}

	// A caller-supplied target_dir becomes the checkpoint's WorkingDir, which
	// rollback later runs git commands in. Reject an out-of-project directory
	// here as well as at rollback, so the bad value never reaches storage.
	if strings.TrimSpace(req.TargetDir) != "" {
		if _, err := s.resolveCheckpointWorkDir(req.TargetDir); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				fmt.Sprintf("target_dir is not usable: %v", err), nil, reqID)
			return
		}
	}

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

	cp, err := importCheckpoint(tmpPath, opts)
	if err != nil {
		var publicationErr *checkpoint.ImportPublicationError
		if errors.As(err, &publicationErr) {
			// Import may already have replaced the public recovery state. This
			// terminal result must survive a same-key retry, even though HTTP
			// correctly reports that publication/finalization did not complete.
			markIdempotencyResultRetained(r)
			s.writeCheckpointImportPublicationError(w, r, cp, publicationErr)
			return
		}
		if protectArchive {
			log.Printf("REST: checkpoint import failed request_id=%s", reqID)
		} else {
			log.Printf("REST: checkpoint import failed session=%s error=%v request_id=%s",
				sessionName, err, reqID)
		}
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

func (s *Server) writeCheckpointImportPublicationError(w http.ResponseWriter, r *http.Request,
	cp *checkpoint.Checkpoint, publicationErr *checkpoint.ImportPublicationError,
) {
	reqID := requestIDFromContext(r.Context())
	state := "unknown"
	code := "CHECKPOINT_IMPORT_PUBLICATION_UNCERTAIN"
	message := "checkpoint publication could not be confirmed; recovery state may have changed"
	hint := "Inspect the destination checkpoint and retained recovery evidence before importing again. Publication failure does not establish that the previous checkpoint is unchanged."
	if publicationErr.Published {
		state = "published"
		code = "CHECKPOINT_IMPORT_FINALIZATION_FAILED"
		message = "checkpoint was published but finalization failed"
		hint = "Inspect the published checkpoint and retained recovery evidence before importing again. Failed finalization does not undo publication."
	}
	details := map[string]interface{}{
		"publication_state":     state,
		"published":             publicationErr.Published,
		"finalization_complete": false,
		"inspection_required":   true,
		"retry_safe":            false,
		"hint":                  hint,
	}
	if cp != nil {
		// A recovery reference must be exact or explicitly withheld. A
		// placeholder ID could send a caller to the wrong checkpoint. Keep
		// diagnostics safe even when ordinary body redaction is disabled,
		// without exposing working directories, commands, or retained paths.
		cfg := redaction.DefaultConfig()
		s.mu.RLock()
		if s.redactionCfg != nil {
			cfg = s.redactionCfg.Config.DeepCopy()
		}
		s.mu.RUnlock()
		reference := map[string]string{"id": cp.ID, "session_name": cp.SessionName}
		encoded, _ := json.Marshal(reference) // A map of strings cannot fail.
		if len(redaction.Scan(string(encoded), cfg)) == 0 &&
			len(redaction.Scan(cp.ID+"\n"+cp.SessionName, cfg)) == 0 {
			details["checkpoint"] = reference
		} else {
			details["checkpoint_reference_withheld"] = true
		}
	}
	// ImportPublicationError.Error includes filesystem paths and the original
	// cause, which can contain credentials. Log only the classified outcome.
	log.Printf("REST: checkpoint import publication incomplete state=%s request_id=%s", state, reqID)
	writeErrorResponse(w, http.StatusInternalServerError, code, message, details, reqID)
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
		if writeCheckpointRefLoadFailureIfExact(w, storage, sessionName, checkpointRef, err, reqID) {
			return
		}
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
	// The commit reaches `git checkout` as a bare argv element and is sliced
	// for display; validate its shape before either happens.
	if !checkpointCommitPattern.MatchString(cp.Git.Commit) {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"checkpoint git commit is not a valid object id", nil, reqID)
		return
	}

	// Confine the working directory BEFORE any git command runs in it, and
	// before the dry-run path reports what it would do.
	workDir, err := s.resolveCheckpointWorkDir(cp.WorkingDir)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			err.Error(), nil, reqID)
		return
	}

	resp := RollbackResponse{
		CheckpointID:   cp.ID,
		CheckpointName: cp.Name,
		DryRun:         req.DryRun,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var plan *checkpoint.GitRollbackPlan
	if !req.NoGit {
		root, err := checkpoint.GitWorktreeRoot(ctx, workDir)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
			return
		}
		// Permission for a subdirectory does not authorize recovery in its
		// parent repository. Check the actual mutation root as well.
		root, err = s.resolveCheckpointWorkDir(root)
		if err != nil {
			writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, err.Error(), nil, reqID)
			return
		}
		plan, err = checkpoint.PrepareGitRollback(ctx, storage, cp, root)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				fmt.Sprintf("rollback preflight failed; repository unchanged: %v", err), rollbackResponseToMap(resp), reqID)
			return
		}
		workDir = plan.WorkDir
		resp.Warnings = append(resp.Warnings, plan.Warnings...)
	}

	if req.DryRun {
		// Just return what would happen
		if plan != nil {
			resp.Warnings = append(resp.Warnings,
				fmt.Sprintf("would rollback to commit %s on branch %s", plan.Commit[:8], cp.Git.Branch))
			if !req.NoStash {
				resp.Warnings = append(resp.Warnings, "would stash current changes if present, including untracked files")
			}
		} else {
			resp.Warnings = append(resp.Warnings, "git recovery disabled")
		}
		writeSuccessResponse(w, http.StatusOK, rollbackResponseToMap(resp), reqID)
		return
	}

	// Perform rollback
	if plan != nil {
		// Stash current changes if dirty and not skipped
		if !req.NoStash {
			stashRef, err := gitStashIfDirty(workDir)
			if err != nil {
				writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
					fmt.Sprintf("failed to stash current work; rollback stopped: %v", err), rollbackResponseToMap(resp), reqID)
				return
			} else if stashRef != "" {
				resp.StashCreated = true
				resp.StashRef = stashRef
			}
		}

		// Checkout the checkpoint commit
		if err := gitCheckout(workDir, plan.Commit); err != nil {
			log.Printf("REST: rollback checkout failed session=%s commit=%s error=%v request_id=%s",
				sessionName, cp.Git.Commit, err, reqID)
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				"failed to checkout commit; inspect before retrying (any stash was preserved)", rollbackResponseToMap(resp), reqID)
			return
		}

		if err := checkpoint.ApplyGitRollbackPatch(ctx, plan); err != nil {
			resp.Warnings = append(resp.Warnings, "HEAD moved but patch restoration did not finish; inspect before retrying. Any stash was preserved.")
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				fmt.Sprintf("rollback incomplete: %v", err), rollbackResponseToMap(resp), reqID)
			return
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
			Branch:            cp.Git.Branch,
			Commit:            cp.Git.Commit,
			IsDirty:           cp.Git.IsDirty,
			StagedCount:       cp.Git.StagedCount,
			UnstagedCount:     cp.Git.UnstagedCount,
			UntrackedCount:    cp.Git.UntrackedCount,
			UntrackedCaptured: cp.Git.UntrackedCaptured,
			HasPatch:          cp.HasGitPatch(),
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
			PaneCount:       cp.PaneCount,
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
	out, err := runGit(workDir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil // Not dirty
	}

	// Stash changes
	stashMsg := fmt.Sprintf("ntm-rollback-%s", time.Now().Format("20060102-150405"))
	if _, err := runGit(workDir, "stash", "push", "--include-untracked", "-m", stashMsg); err != nil {
		return "", err
	}

	ref, err := runGit(workDir, "rev-parse", "--verify", "refs/stash")
	if err != nil {
		return "", fmt.Errorf("stash command completed but its reference is unavailable; inspect before retrying: %w", err)
	}
	return strings.TrimSpace(ref), nil
}

// gitCheckout checks out a specific commit.
func gitCheckout(workDir, ref string) error {
	_, err := runGit(workDir, "checkout", "--no-overwrite-ignore", ref)
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

// checkpointCommitPattern bounds a checkpoint's recorded git commit to a real
// object id. The value reaches `git checkout` as a bare argv element, so an
// imported checkpoint carrying "--force" or "-B branch" would be
// argument-injected rather than treated as a revision; it is also sliced for
// display, which panicked on anything shorter than 8 characters — reachable
// through the DRY-RUN path operators choose precisely because they believe it
// cannot touch the repository.
var checkpointCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// resolveCheckpointWorkDir confines a checkpoint's working directory to the
// server's project directory before any git command runs in it.
//
// Both the directory and the commit come from the checkpoint archive, which is
// caller-supplied: POST /checkpoints/import accepts an arbitrary target_dir
// (and otherwise trusts the archive's own metadata.json), and rollback then
// runs `git stash push`, a detaching `git checkout`, and `git apply --3way` of
// the archive's patch inside it. Without this check, a caller holding only
// sessions:write could rewrite an unrelated repository on the host. The
// restore path already refuses an unusable working directory; rollback is the
// more destructive of the two and had no equivalent guard (bd-456fv).
func (s *Server) resolveCheckpointWorkDir(workDir string) (string, error) {
	trimmed := strings.TrimSpace(workDir)
	if trimmed == "" {
		return "", errors.New("checkpoint has no working directory")
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

	// The directory must already exist: resolving symlinks is what stops a
	// link inside the project from pointing at a repository outside it.
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("working directory is not usable: %w", err)
	}
	candidate = resolved

	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("working directory is not usable: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	if !pathWithinRoot(root, candidate) {
		return "", errors.New("working directory must be inside the project directory")
	}
	return candidate, nil
}
