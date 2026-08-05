// Package serve provides REST API endpoints for beads and bv robot integration.
// beads.go implements the /api/v1/beads endpoints.
package serve

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func normalizeSingularBeadPayload(payload interface{}) interface{} {
	beads, ok := payload.([]interface{})
	if !ok || len(beads) != 1 {
		return payload
	}
	return beads[0]
}

// beadIDPattern matches a beads issue ID such as "bd-2euwg" or "ntm-y9cd".
var beadIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[A-Za-z0-9]+$`)

// validateBeadIDParam checks that a bead ID from a URL parameter is a real ID
// before it becomes an argv element for br. br is executed without a shell, but
// argv is still parsed for flags: an ID like "--db=/etc/shadow" or "--file=..."
// would be consumed as an option rather than a positional, turning a read
// endpoint into an arbitrary-path probe. It writes an error response and returns
// false when validation fails, so callers can return early.
func validateBeadIDParam(w http.ResponseWriter, beadID, reqID string) bool {
	if strings.TrimSpace(beadID) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "bead ID required", nil, reqID)
		return false
	}
	if !beadIDPattern.MatchString(beadID) {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"invalid bead ID: expected a prefixed identifier such as bd-2euwg", nil, reqID)
		return false
	}
	return true
}

// validateBeadArgument rejects a caller-supplied value that br would parse as an
// option instead of data. Applies to free-form positionals and flag values alike,
// since a value beginning with "-" can terminate the intended flag and introduce
// another one.
func validateBeadArgument(w http.ResponseWriter, value, field, reqID string) bool {
	if strings.HasPrefix(strings.TrimSpace(value), "-") {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			field+" must not begin with '-'", nil, reqID)
		return false
	}
	return true
}

// Beads-specific error codes
const (
	ErrCodeBeadsUnavailable   = "BEADS_UNAVAILABLE"
	ErrCodeBeadNotFound       = "BEAD_NOT_FOUND"
	ErrCodeBeadAlreadyClaimed = "BEAD_ALREADY_CLAIMED"
	ErrCodeBVUnavailable      = "BV_UNAVAILABLE"
	beadsUnavailableMessage   = "br (beads_rust) is not installed"
)

// Beads request/response types

// CreateBeadRequest is the request body for POST /api/v1/beads
type CreateBeadRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`     // task, bug, epic, etc.
	Priority    string   `json:"priority,omitempty"` // P0, P1, P2, P3
	Labels      []string `json:"labels,omitempty"`
	Parent      string   `json:"parent,omitempty"`     // Parent bead ID for sub-tasks
	BlockedBy   []string `json:"blocked_by,omitempty"` // IDs this bead is blocked by
}

// UpdateBeadRequest is the request body for PATCH /api/v1/beads/{id}
type UpdateBeadRequest struct {
	Title       *string  `json:"title,omitempty"`
	Description *string  `json:"description,omitempty"`
	Priority    *string  `json:"priority,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Assignee    *string  `json:"assignee,omitempty"`
}

// ClaimBeadRequest is the request body for POST /api/v1/beads/{id}/claim
type ClaimBeadRequest struct {
	Assignee string `json:"assignee"`
}

// AddDependencyRequest is the request body for POST /api/v1/beads/{id}/deps
type AddDependencyRequest struct {
	BlockedBy string `json:"blocked_by"` // ID of the bead that blocks this one
}

// registerBeadsRoutes registers beads and bv REST endpoints
func (s *Server) registerBeadsRoutes(r chi.Router) {
	r.Route("/beads", func(r chi.Router) {
		// List/Create beads
		r.With(s.RequirePermission(PermReadBeads)).Get("/", s.handleListBeads)
		r.With(s.RequirePermission(PermWriteBeads)).Post("/", s.handleCreateBead)

		// Stats and filtered lists
		r.With(s.RequirePermission(PermReadBeads)).Get("/stats", s.handleBeadsStats)
		r.With(s.RequirePermission(PermReadBeads)).Get("/ready", s.handleBeadsReady)
		r.With(s.RequirePermission(PermReadBeads)).Get("/blocked", s.handleBeadsBlocked)
		r.With(s.RequirePermission(PermReadBeads)).Get("/in-progress", s.handleBeadsInProgress)

		// BV robot mode passthrough
		r.With(s.RequirePermission(PermReadBeads)).Get("/triage", s.handleBeadsTriage)
		r.With(s.RequirePermission(PermReadBeads)).Get("/insights", s.handleBeadsInsights)
		r.With(s.RequirePermission(PermReadBeads)).Get("/plan", s.handleBeadsPlan)
		r.With(s.RequirePermission(PermReadBeads)).Get("/priority", s.handleBeadsPriority)
		r.With(s.RequirePermission(PermReadBeads)).Get("/recipes", s.handleBeadsRecipes)

		// Sync
		r.With(s.RequirePermission(PermWriteBeads)).Post("/sync", s.handleBeadsSync)

		// Individual bead operations
		r.Route("/{id}", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadBeads)).Get("/", s.handleGetBead)
			r.With(s.RequirePermission(PermWriteBeads)).Patch("/", s.handleUpdateBead)
			r.With(s.RequirePermission(PermWriteBeads), s.idempotencyMiddleware).Post("/close", s.handleCloseBead)
			r.With(s.RequirePermission(PermWriteBeads)).Post("/claim", s.handleClaimBead)

			// Dependencies
			r.With(s.RequirePermission(PermReadBeads)).Get("/deps", s.handleListBeadDeps)
			r.With(s.RequirePermission(PermWriteBeads)).Post("/deps", s.handleAddBeadDep)
			r.With(s.RequirePermission(PermWriteBeads)).Delete("/deps/{depId}", s.handleRemoveBeadDep)
		})
	})
}

// handleListBeads handles GET /api/v1/beads
func (s *Server) handleListBeads(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("list beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	// Get optional filters from query params
	status := r.URL.Query().Get("status") // open, closed, in_progress
	label := r.URL.Query().Get("label")
	assignee := r.URL.Query().Get("assignee")

	// A filter value beginning with "-" would terminate its own flag and become
	// a new option to br, e.g. ?status=--db=/etc/shadow.
	if !validateBeadArgument(w, status, "status", reqID) ||
		!validateBeadArgument(w, label, "label", reqID) ||
		!validateBeadArgument(w, assignee, "assignee", reqID) {
		return
	}

	// Build br command args
	args := []string{"list", "--json"}
	if status != "" {
		args = append(args, "--status", status)
	}
	if label != "" {
		args = append(args, "--labels", label)
	}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), args...)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Parse JSON output from br list --json.
	beads, err := bv.UnmarshalBdList[json.RawMessage](output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse beads output", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"beads": beads,
		"count": len(beads),
	}, reqID)
}

// handleCreateBead handles POST /api/v1/beads
func (s *Server) handleCreateBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("create bead", "request_id", reqID)

	// Validate the request body before checking external tool availability so
	// malformed requests get a 400 regardless of whether bd is installed.
	var req CreateBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Title == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "title is required", nil, reqID)
		return
	}
	// The title is a positional argument to br, so a leading dash would be read
	// as an option — notably `br create --file <FILE>`, which would ingest an
	// arbitrary file as beads readable back through GET /api/v1/beads.
	if !validateBeadArgument(w, req.Title, "title", reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	// Build br create command using the live CLI contract.
	args := []string{"create", req.Title, "--json"}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	if req.Type != "" {
		args = append(args, "--type", req.Type)
	}
	if req.Priority != "" {
		args = append(args, "--priority", req.Priority)
	}
	if req.Parent != "" {
		args = append(args, "--parent", req.Parent)
	}
	if len(req.Labels) > 0 {
		args = append(args, "--labels", strings.Join(req.Labels, ","))
	}
	if len(req.BlockedBy) > 0 {
		args = append(args, "--deps", strings.Join(req.BlockedBy, ","))
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), args...)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Parse created bead
	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		// Return raw output if not JSON
		writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
			"created": true,
			"output":  output,
		}, reqID)
		return
	}
	bead = normalizeSingularBeadPayload(bead)

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.created", bead)
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"bead": bead,
	}, reqID)
}

// handleGetBead handles GET /api/v1/beads/{id}
func (s *Server) handleGetBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("get bead", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "show", beadID, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeBeadNotFound, err.Error(), nil, reqID)
		return
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse bead", nil, reqID)
		return
	}
	bead = normalizeSingularBeadPayload(bead)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead": bead,
	}, reqID)
}

// handleUpdateBead handles PATCH /api/v1/beads/{id}
func (s *Server) handleUpdateBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("update bead", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	// Validate request body before checking external tool availability.
	var req UpdateBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}
	if req.Title != nil && !validateBeadArgument(w, *req.Title, "title", reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	// Build br update command
	args := []string{"update", beadID, "--json"}
	if req.Title != nil {
		args = append(args, "--title", *req.Title)
	}
	if req.Description != nil {
		args = append(args, "--description", *req.Description)
	}
	if req.Priority != nil {
		args = append(args, "--priority", *req.Priority)
	}
	if req.Assignee != nil {
		args = append(args, "--assignee", *req.Assignee)
	}
	if req.Labels != nil {
		args = append(args, "--set-labels", strings.Join(req.Labels, ","))
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), args...)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"updated": true,
			"output":  output,
		}, reqID)
		return
	}
	bead = normalizeSingularBeadPayload(bead)

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.updated", bead)
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead": bead,
	}, reqID)
}

// handleCloseBead handles POST /api/v1/beads/{id}/close
func (s *Server) handleCloseBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("close bead", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	async := false
	if rawAsync := r.URL.Query().Get("async"); rawAsync != "" {
		var err error
		async, err = strconv.ParseBool(rawAsync)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"async must be a boolean", nil, reqID)
			return
		}
	}
	if async {
		job := s.jobStore.Create("bead_close")
		go s.executeBeadCloseJob(job.ID, beadID)
		writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
			"job":     job,
			"bead_id": beadID,
		}, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "close", beadID, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"closed": true,
			"output": output,
		}, reqID)
		return
	}
	bead = normalizeSingularBeadPayload(bead)

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.closed", map[string]interface{}{
			"id":   beadID,
			"bead": bead,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead":   bead,
		"closed": true,
	}, reqID)
}

// executeBeadCloseJob closes a bead outside the request lifecycle. RunBd already
// serializes access to a workspace and retries transient Beads database errors,
// so an overloaded tracker cannot strand a caller in a shell retry loop.
func (s *Server) executeBeadCloseJob(jobID, beadID string) {
	s.jobStore.Update(jobID, JobStatusRunning, 0, nil, "")

	bead, alreadyClosed, err := s.closeBeadForAsyncJob(beadID)
	if err != nil {
		s.jobStore.Update(jobID, JobStatusFailed, 0, map[string]interface{}{
			"bead_id": beadID,
		}, err.Error())
		s.publishAsyncBeadCloseAttention(jobID, beadID, false, err)
		return
	}

	result := map[string]interface{}{
		"bead_id": beadID,
		"closed":  true,
	}
	if bead != nil {
		result["bead"] = bead
	}
	if alreadyClosed {
		result["already_closed"] = true
	} else if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.closed", map[string]interface{}{
			"id":   beadID,
			"bead": bead,
		})
	}
	s.jobStore.Update(jobID, JobStatusCompleted, 100, result, "")
	s.publishAsyncBeadCloseAttention(jobID, beadID, alreadyClosed, nil)
}

func (s *Server) publishAsyncBeadCloseAttention(jobID, beadID string, alreadyClosed bool, closeErr error) {
	details := map[string]any{
		"job_id":  jobID,
		"bead_id": beadID,
	}
	if closeErr != nil {
		details["error"] = closeErr.Error()
		robot.GetAttentionFeed().Append(robot.AttentionEvent{
			Category:      robot.EventCategoryAlert,
			Type:          robot.EventTypeAlertWarning,
			Source:        "serve.async_bead_close",
			Actionability: robot.ActionabilityInteresting,
			Severity:      robot.SeverityWarning,
			ReasonCode:    "async_bead_close_failed",
			Summary:       "Async bead close failed: " + beadID,
			Details:       details,
		})
		return
	}
	if alreadyClosed {
		details["already_closed"] = true
	}
	robot.GetAttentionFeed().Append(robot.NewBeadEvent(
		robot.EventTypeBeadClosed,
		beadID,
		"closed asynchronously",
		details,
	))
}

// closeBeadForAsyncJob treats an already-closed bead as the successful terminal
// result of the same logical close request. This makes a retry after a completed
// close safe even when the client did not receive the original response.
func (s *Server) closeBeadForAsyncJob(beadID string) (interface{}, bool, error) {
	dir := s.projectDirSnapshot()
	if bead, closed, err := asyncJobBeadStatus(dir, beadID); err == nil && closed {
		return bead, true, nil
	}

	output, err := bv.RunBd(dir, "close", beadID, "--json")
	if err != nil {
		if bead, closed, statusErr := asyncJobBeadStatus(dir, beadID); statusErr == nil && closed {
			return bead, true, nil
		}
		return nil, false, err
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		return map[string]interface{}{
			"id":     beadID,
			"output": output,
		}, false, nil
	}
	return normalizeSingularBeadPayload(bead), false, nil
}

func asyncJobBeadStatus(dir, beadID string) (interface{}, bool, error) {
	output, err := bv.RunBd(dir, "show", beadID, "--json")
	if err != nil {
		return nil, false, err
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		return nil, false, err
	}
	bead = normalizeSingularBeadPayload(bead)
	values, ok := bead.(map[string]interface{})
	if !ok {
		return bead, false, nil
	}
	status, _ := values["status"].(string)
	return bead, strings.EqualFold(strings.TrimSpace(status), "closed"), nil
}

// handleClaimBead handles POST /api/v1/beads/{id}/claim
func (s *Server) handleClaimBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("claim bead", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	// Validate request body before checking external tool availability.
	var req ClaimBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Assignee == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "assignee is required", nil, reqID)
		return
	}
	if !validateBeadArgument(w, req.Assignee, "assignee", reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	// Claiming must be a compare-and-set inside one SQLite transaction. Writing
	// --assignee plus --status is a blind overwrite that silently steals a bead
	// another agent already holds, and it also permits br's --no-db fallback,
	// which cannot provide the transaction at all.
	claim, err := bv.ClaimBead(r.Context(), s.projectDirSnapshot(), beadID, req.Assignee)
	if err != nil {
		if errors.Is(err, bv.ErrBeadAlreadyClaimed) {
			writeErrorResponse(w, http.StatusConflict, ErrCodeBeadAlreadyClaimed, err.Error(), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// ClaimBead returns the claimed bead's authoritative post-claim state, so it
	// is the response payload directly rather than re-parsed br output.
	bead := claim

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.claimed", map[string]interface{}{
			"id":       beadID,
			"assignee": req.Assignee,
			"bead":     bead,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead":     bead,
		"claimed":  true,
		"assignee": req.Assignee,
	}, reqID)
}

// handleBeadsStats handles GET /api/v1/beads/stats
func (s *Server) handleBeadsStats(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads stats", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "stats", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var stats interface{}
	if err := json.Unmarshal([]byte(output), &stats); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse stats", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"stats": stats,
	}, reqID)
}

// handleBeadsReady handles GET /api/v1/beads/ready
func (s *Server) handleBeadsReady(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get ready beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "ready", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	beads, err := bv.UnmarshalBdList[map[string]interface{}](output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse ready beads", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"beads": beads,
		"count": len(beads),
	}, reqID)
}

// handleBeadsBlocked handles GET /api/v1/beads/blocked
func (s *Server) handleBeadsBlocked(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get blocked beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "blocked", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	beads, err := bv.UnmarshalBdList[map[string]interface{}](output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse blocked beads", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"beads": beads,
		"count": len(beads),
	}, reqID)
}

// handleBeadsInProgress handles GET /api/v1/beads/in-progress
func (s *Server) handleBeadsInProgress(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get in-progress beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "list", "--status", "in_progress", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	beads, err := bv.UnmarshalBdList[map[string]interface{}](output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse in-progress beads", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"beads": beads,
		"count": len(beads),
	}, reqID)
}

// handleListBeadDeps handles GET /api/v1/beads/{id}/deps
func (s *Server) handleListBeadDeps(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("list bead dependencies", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "dep", "list", beadID, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var deps interface{}
	if err := json.Unmarshal([]byte(output), &deps); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"bead_id": beadID,
			"output":  output,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead_id":      beadID,
		"dependencies": deps,
	}, reqID)
}

// handleAddBeadDep handles POST /api/v1/beads/{id}/deps
func (s *Server) handleAddBeadDep(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("add bead dependency", "request_id", reqID, "bead_id", beadID)

	if !validateBeadIDParam(w, beadID, reqID) {
		return
	}

	// Validate request body before checking external tool availability.
	var req AddDependencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.BlockedBy == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "blocked_by is required", nil, reqID)
		return
	}
	if !validateBeadIDParam(w, req.BlockedBy, reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "dep", "add", beadID, req.BlockedBy, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.dependency_added", map[string]interface{}{
			"bead_id":    beadID,
			"blocked_by": req.BlockedBy,
		})
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"bead_id":    beadID,
		"blocked_by": req.BlockedBy,
		"linked":     true,
		"output":     output,
	}, reqID)
}

// handleRemoveBeadDep handles DELETE /api/v1/beads/{id}/deps/{depId}
func (s *Server) handleRemoveBeadDep(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")
	depID := chi.URLParam(r, "depId")

	slog.Info("remove bead dependency", "request_id", reqID, "bead_id", beadID, "dep_id", depID)

	if !validateBeadIDParam(w, beadID, reqID) || !validateBeadIDParam(w, depID, reqID) {
		return
	}

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDirSnapshot(), "dep", "remove", beadID, depID, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.dependency_removed", map[string]interface{}{
			"bead_id": beadID,
			"dep_id":  depID,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead_id":  beadID,
		"dep_id":   depID,
		"unlinked": true,
		"output":   output,
	}, reqID)
}

// BV Robot Mode Endpoints

// handleBeadsTriage handles GET /api/v1/beads/triage
func (s *Server) handleBeadsTriage(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads triage", "request_id", reqID)

	if !bv.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBVUnavailable, "bv (beads_viewer) is not installed", nil, reqID)
		return
	}

	// Get optional limit param
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > 200 {
				limit = 200
			}
		}
	}

	// Use the BVClient for triage
	client := bv.NewBVClientWithOptions(s.projectDirSnapshot(), 0, 0)
	recs, err := client.GetRecommendations(bv.RecommendationOpts{Limit: limit})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"recommendations": recs,
		"count":           len(recs),
	}, reqID)
}

// handleBeadsInsights handles GET /api/v1/beads/insights
func (s *Server) handleBeadsInsights(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads insights", "request_id", reqID)

	if !bv.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBVUnavailable, "bv (beads_viewer) is not installed", nil, reqID)
		return
	}

	insights, err := bv.GetInsights(s.projectDirSnapshot())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"insights": insights,
	}, reqID)
}

// handleBeadsPlan handles GET /api/v1/beads/plan
func (s *Server) handleBeadsPlan(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads plan", "request_id", reqID)

	if !bv.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBVUnavailable, "bv (beads_viewer) is not installed", nil, reqID)
		return
	}

	plan, err := bv.GetPlan(s.projectDirSnapshot())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"plan": plan,
	}, reqID)
}

// handleBeadsPriority handles GET /api/v1/beads/priority
func (s *Server) handleBeadsPriority(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads priority", "request_id", reqID)

	if !bv.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBVUnavailable, "bv (beads_viewer) is not installed", nil, reqID)
		return
	}

	priority, err := bv.GetPriority(s.projectDirSnapshot())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"priority": priority,
	}, reqID)
}

// handleBeadsRecipes handles GET /api/v1/beads/recipes
func (s *Server) handleBeadsRecipes(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads recipes", "request_id", reqID)

	if !bv.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBVUnavailable, "bv (beads_viewer) is not installed", nil, reqID)
		return
	}

	recipes, err := bv.GetRecipes(s.projectDirSnapshot())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"recipes": recipes,
	}, reqID)
}

// handleBeadsSync handles POST /api/v1/beads/sync
func (s *Server) handleBeadsSync(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("sync beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, beadsUnavailableMessage, nil, reqID)
		return
	}

	// The direction must be pinned. A bare `br sync` lets br choose, and choosing
	// import can overwrite .beads/issues.jsonl from a stale SQLite database,
	// regressing issue status and destroying close reasons. Every other sync call
	// site in the tree pins the same flags (internal/bv/bv.go, internal/hooks).
	output, err := bv.RunBd(s.projectDirSnapshot(), "sync", "--flush-only", "--json", "--no-auto-import")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "beads.synced", map[string]interface{}{
			"synced": true,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"synced": true,
		"output": output,
	}, reqID)
}
