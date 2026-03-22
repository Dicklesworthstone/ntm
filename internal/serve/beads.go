// Package serve provides REST API endpoints for beads and bv robot integration.
// beads.go implements the /api/v1/beads endpoints.
package serve

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// Beads-specific error codes
const (
	ErrCodeBeadsUnavailable = "BEADS_UNAVAILABLE"
	ErrCodeBeadNotFound     = "BEAD_NOT_FOUND"
	ErrCodeBVUnavailable    = "BV_UNAVAILABLE"
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

		// Daemon control
		r.With(s.RequirePermission(PermReadBeads)).Get("/daemon/status", s.handleBeadsDaemonStatus)
		r.With(s.RequirePermission(PermWriteBeads)).Post("/daemon/start", s.handleBeadsDaemonStart)
		r.With(s.RequirePermission(PermWriteBeads)).Post("/daemon/stop", s.handleBeadsDaemonStop)

		// Sync
		r.With(s.RequirePermission(PermWriteBeads)).Post("/sync", s.handleBeadsSync)

		// Individual bead operations
		r.Route("/{id}", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadBeads)).Get("/", s.handleGetBead)
			r.With(s.RequirePermission(PermWriteBeads)).Patch("/", s.handleUpdateBead)
			r.With(s.RequirePermission(PermWriteBeads)).Post("/close", s.handleCloseBead)
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
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	// Get optional filters from query params
	status := r.URL.Query().Get("status") // open, closed, in_progress
	label := r.URL.Query().Get("label")
	assignee := r.URL.Query().Get("assignee")

	// Build bd command args
	args := []string{"list", "--json"}
	if status != "" {
		args = append(args, "--status", status)
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}

	output, err := bv.RunBd(s.projectDir, args...)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Parse JSON output
	var beads []interface{}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
		// Try wrapping in array if single object
		var singleBead interface{}
		if err2 := json.Unmarshal([]byte(output), &singleBead); err2 == nil {
			beads = []interface{}{singleBead}
		} else {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse beads output", nil, reqID)
			return
		}
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	var req CreateBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Title == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "title is required", nil, reqID)
		return
	}

	// Build bd new command
	args := []string{"new", req.Title, "--json"}
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
	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}
	for _, blocked := range req.BlockedBy {
		args = append(args, "--blocked-by", blocked)
	}

	output, err := bv.RunBd(s.projectDir, args...)
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "show", beadID, "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeBeadNotFound, err.Error(), nil, reqID)
		return
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to parse bead", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"bead": bead,
	}, reqID)
}

// handleUpdateBead handles PATCH /api/v1/beads/{id}
func (s *Server) handleUpdateBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("update bead", "request_id", reqID, "bead_id", beadID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	var req UpdateBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Build bd update command
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
	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}

	output, err := bv.RunBd(s.projectDir, args...)
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "close", beadID, "--json")
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

// handleClaimBead handles POST /api/v1/beads/{id}/claim
func (s *Server) handleClaimBead(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	beadID := chi.URLParam(r, "id")

	slog.Info("claim bead", "request_id", reqID, "bead_id", beadID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	var req ClaimBeadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Assignee == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "assignee is required", nil, reqID)
		return
	}

	// Use bd update to set assignee and status to in_progress
	output, err := bv.RunBd(s.projectDir, "update", beadID, "--assignee", req.Assignee, "--status", "in_progress", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var bead interface{}
	if err := json.Unmarshal([]byte(output), &bead); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"claimed":  true,
			"assignee": req.Assignee,
			"output":   output,
		}, reqID)
		return
	}

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "bead.claimed", map[string]interface{}{
		"id":       beadID,
		"assignee": req.Assignee,
		"bead":     bead,
	})

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
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "stats", "--json")
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
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "ready", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var beads []interface{}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
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
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "blocked", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var beads []interface{}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
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
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "list", "--status", "in_progress", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	var beads []interface{}
	if err := json.Unmarshal([]byte(output), &beads); err != nil {
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "deps", beadID, "--json")
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	var req AddDependencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.BlockedBy == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "blocked_by is required", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "link", beadID, req.BlockedBy, "--json")
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

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "unlink", beadID, depID, "--json")
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
		}
	}

	// Use the BVClient for triage
	client := bv.NewBVClientWithOptions(s.projectDir, 0, 0)
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

	insights, err := bv.GetInsights(s.projectDir)
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

	plan, err := bv.GetPlan(s.projectDir)
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

	priority, err := bv.GetPriority(s.projectDir)
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

	recipes, err := bv.GetRecipes(s.projectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"recipes": recipes,
	}, reqID)
}

// Daemon Control Endpoints

// handleBeadsDaemonStatus handles GET /api/v1/beads/daemon/status
func (s *Server) handleBeadsDaemonStatus(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("get beads daemon status", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "daemon", "status", "--json")
	if err != nil {
		// Daemon might not be running, return status anyway
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"running": false,
			"error":   err.Error(),
		}, reqID)
		return
	}

	var status interface{}
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"running": true,
			"output":  output,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"status": status,
	}, reqID)
}

// handleBeadsDaemonStart handles POST /api/v1/beads/daemon/start
func (s *Server) handleBeadsDaemonStart(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("start beads daemon", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "daemon", "start", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"started": true,
		"output":  output,
	}, reqID)
}

// handleBeadsDaemonStop handles POST /api/v1/beads/daemon/stop
func (s *Server) handleBeadsDaemonStop(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("stop beads daemon", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "daemon", "stop", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"stopped": true,
		"output":  output,
	}, reqID)
}

// handleBeadsSync handles POST /api/v1/beads/sync
func (s *Server) handleBeadsSync(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("sync beads", "request_id", reqID)

	if !bv.IsBdInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeBeadsUnavailable, "bd (beads_rust) is not installed", nil, reqID)
		return
	}

	output, err := bv.RunBd(s.projectDir, "sync", "--json")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Publish WebSocket event
	if s.wsHub != nil {
		s.wsHub.Publish("beads:*", "beads.synced", map[string]interface{}{
		"synced": true,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"synced": true,
		"output": output,
	}, reqID)
}
