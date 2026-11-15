// Package serve provides REST API endpoints for Agent Mail file reservations.
// mail_reservations.go implements the /api/v1/reservations endpoints.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	approvalpkg "github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// ReservePathsRequest is the request body for POST /api/v1/reservations
type ReservePathsRequest struct {
	AgentName  string   `json:"agent_name"`
	Paths      []string `json:"paths"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
	Exclusive  bool     `json:"exclusive,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// ReleaseReservationsRequest is the request body for DELETE /api/v1/reservations
type ReleaseReservationsRequest struct {
	AgentName string   `json:"agent_name"`
	Paths     []string `json:"paths,omitempty"`
	IDs       []int    `json:"ids,omitempty"`
}

// RenewReservationsRequest is the request body for POST /api/v1/reservations/{id}/renew
type RenewReservationsRequest struct {
	AgentName     string `json:"agent_name"`
	ExtendSeconds int    `json:"extend_seconds,omitempty"`
}

// ForceReleaseRequest is the request body for POST /api/v1/reservations/{id}/force-release
type ForceReleaseRequest struct {
	AgentName      string `json:"agent_name"`
	Note           string `json:"note,omitempty"`
	NotifyPrevious bool   `json:"notify_previous,omitempty"`
	NewApproval    string `json:"new_approval,omitempty"` // Explicit terminal predecessor; repeated requests reuse its successor.
}

// Reservation handlers

// handleListReservations handles GET /api/v1/reservations
func (s *Server) handleListReservations(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)

	agentName := r.URL.Query().Get("agent_name")
	slog.Info("list reservations", "request_id", reqID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	// If no agent specified, list all reservations
	allAgents := agentName == ""
	reservations, err := client.ListReservations(ctx, projectKey, agentName, allAgents)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "listing reservations is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservations": reservations,
		"count":        len(reservations),
	}, reqID)
}

// handleReservePaths handles POST /api/v1/reservations
func (s *Server) handleReservePaths(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)
	slog.Info("reserve paths", "request_id", reqID)

	var req ReservePathsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" || len(req.Paths) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name and paths are required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	ttl := req.TTLSeconds
	if ttl == 0 {
		ttl = 3600 // Default 1 hour
	}

	result, err := client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  req.AgentName,
		Paths:      req.Paths,
		TTLSeconds: ttl,
		Exclusive:  req.Exclusive,
		Reason:     req.Reason,
	})
	if err != nil && (!agentmail.IsReservationConflict(err) || result == nil) {
		writeAgentMailReservationError(w, reqID, err, "creating reservations is not supported by the configured Agent Mail server")
		return
	}
	if result == nil {
		result = &agentmail.ReservationResult{}
	}

	if len(result.Granted) > 0 {
		s.publishReservationEvent(projectKey, req.AgentName, "reservation.granted", map[string]interface{}{
			"agent_name":    req.AgentName,
			"exclusive":     req.Exclusive,
			"ttl_seconds":   ttl,
			"reason":        req.Reason,
			"granted":       result.Granted,
			"granted_count": len(result.Granted),
		})
	}
	if len(result.Conflicts) > 0 {
		s.publishReservationEvent(projectKey, req.AgentName, "reservation.conflict", map[string]interface{}{
			"agent_name":     req.AgentName,
			"paths":          req.Paths,
			"conflicts":      result.Conflicts,
			"conflict_count": len(result.Conflicts),
		})
	}

	if len(result.Conflicts) > 0 {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			"one or more requested paths are already reserved", map[string]interface{}{
				"granted":   result.Granted,
				"conflicts": result.Conflicts,
			}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusCreated, map[string]interface{}{
		"granted":   result.Granted,
		"conflicts": result.Conflicts,
	}, reqID)
}

// handleReleaseReservations handles DELETE /api/v1/reservations
func (s *Server) handleReleaseReservations(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)
	slog.Info("release reservations", "request_id", reqID)

	var req ReleaseReservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	releaseResult, err := client.ReleaseReservations(ctx, projectKey, req.AgentName, req.Paths, req.IDs)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "releasing reservations is not supported by the configured Agent Mail server")
		return
	}
	if releaseResult == nil {
		releaseResult = &agentmail.ReleaseReservationsResult{}
	}

	if releaseResult.Released > 0 {
		eventPayload := map[string]interface{}{
			"agent_name": req.AgentName,
			"paths":      req.Paths,
			"ids":        req.IDs,
			"released":   releaseResult.Released,
		}
		if releaseResult.ReleasedAt != nil {
			eventPayload["released_at"] = releaseResult.ReleasedAt
		}
		s.publishReservationEvent(projectKey, req.AgentName, "reservation.released", eventPayload)
	}

	response := map[string]interface{}{
		"released": releaseResult.Released,
	}
	if releaseResult.ReleasedAt != nil {
		response["released_at"] = releaseResult.ReleasedAt
	}

	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleReservationConflicts handles GET /api/v1/reservations/conflicts
func (s *Server) handleReservationConflicts(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)

	paths := r.URL.Query()["paths"]
	if len(paths) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "paths query parameter required", nil, reqID)
		return
	}

	slog.Info("check reservation conflicts", "request_id", reqID, "paths", paths)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	conflicts, err := client.CheckConflicts(ctx, projectKey, paths)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "reservation conflict inspection is not supported by the configured Agent Mail server")
		return
	}

	if len(conflicts) > 0 {
		s.publishReservationEvent(projectKey, "", "reservation.conflict", map[string]interface{}{
			"paths":          paths,
			"conflicts":      conflicts,
			"conflict_count": len(conflicts),
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"conflicts":     conflicts,
		"has_conflicts": len(conflicts) > 0,
	}, reqID)
}

// handleGetReservation handles GET /api/v1/reservations/{id}
func (s *Server) handleGetReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)
	idStr := chi.URLParam(r, "id")

	slog.Info("get reservation", "request_id", reqID, "reservation_id", idStr)

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	reservation, err := client.GetReservation(ctx, projectKey, reservationID)
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "reservation lookup is not supported by the configured Agent Mail server")
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"reservation": reservation,
	}, reqID)
}

// handleReleaseReservationByID handles POST /api/v1/reservations/{id}/release
func (s *Server) handleReleaseReservationByID(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)
	idStr := chi.URLParam(r, "id")

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	agentName := r.URL.Query().Get("agent_name")
	if agentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name query parameter required", nil, reqID)
		return
	}

	slog.Info("release reservation by ID", "request_id", reqID, "reservation_id", reservationID, "agent", agentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	releaseResult, err := client.ReleaseReservations(ctx, projectKey, agentName, nil, []int{reservationID})
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "releasing reservations is not supported by the configured Agent Mail server")
		return
	}
	if releaseResult == nil || releaseResult.Released == 0 {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "reservation not found", nil, reqID)
		return
	}

	eventPayload := map[string]interface{}{
		"agent_name":     agentName,
		"reservation_id": reservationID,
	}
	if releaseResult.ReleasedAt != nil {
		eventPayload["released_at"] = releaseResult.ReleasedAt
	}
	s.publishReservationEvent(projectKey, agentName, "reservation.released", eventPayload)

	response := map[string]interface{}{
		"reservation_id": reservationID,
		"released":       true,
	}
	if releaseResult.ReleasedAt != nil {
		response["released_at"] = releaseResult.ReleasedAt
	}

	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleRenewReservation handles POST /api/v1/reservations/{id}/renew
func (s *Server) handleRenewReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectKey := s.mailProjectKey(r)
	idStr := chi.URLParam(r, "id")

	reservationID, err := strconv.Atoi(idStr)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid reservation ID", nil, reqID)
		return
	}

	var req RenewReservationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.AgentName == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}

	slog.Info("renew reservation", "request_id", reqID, "reservation_id", reservationID, "agent", req.AgentName)

	client, err := s.getMailClient()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to create mail client", nil, reqID)
		return
	}
	if client == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMailUnavailable, "Agent Mail server is not available", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()

	extendSeconds := req.ExtendSeconds
	if extendSeconds == 0 {
		extendSeconds = 1800 // Default 30 minutes
	}

	result, err := client.RenewReservationsWithOptions(ctx, agentmail.RenewReservationsOptions{
		ProjectKey:     projectKey,
		AgentName:      req.AgentName,
		ExtendSeconds:  extendSeconds,
		ReservationIDs: []int{reservationID},
	})
	if err != nil {
		writeAgentMailReservationError(w, reqID, err, "renewing reservations is not supported by the configured Agent Mail server")
		return
	}
	if result == nil {
		result = &agentmail.RenewReservationsResult{}
	}
	if result.Renewed == 0 {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "reservation not found", nil, reqID)
		return
	}

	s.publishReservationEvent(projectKey, req.AgentName, "reservation.renewed", map[string]interface{}{
		"agent_name":     req.AgentName,
		"reservation_id": reservationID,
		"extend_seconds": extendSeconds,
		"renewed":        result.Renewed,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"renewed":      result.Renewed,
		"reservations": result.Reservations,
	}, reqID)
}

// handleForceReleaseReservation handles POST /api/v1/reservations/{id}/force-release.
func (s *Server) handleForceReleaseReservation(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	reservationID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || reservationID < 1 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "reservation ID must be a positive number", nil, reqID)
		return
	}
	var req ForceReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}
	if strings.TrimSpace(req.AgentName) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "agent_name is required", nil, reqID)
		return
	}
	// The lease's project also owns its policy. A server may serve projects
	// whose force-release settings differ from its working directory; loading
	// the cwd policy here could silently authorize a release the target forbids.
	projectKey := s.mailProjectKey(r)
	var pol *policy.Policy
	if projectKey == "" {
		pol, err = policy.LoadOrDefault()
	} else {
		pol, err = policy.LoadForProject(projectKey)
	}
	if err != nil {
		slog.Error("force-release policy load failed", "request_id", reqID, "error", err)
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "cannot load policy for force-release gate; see server logs", nil, reqID)
		return
	}

	// Snapshot the canonical project and trusted principal once. Neither a
	// changed server selection nor an agent_name body field can redirect an
	// approval to a different project, requester, or Agent Mail actor.
	operation := approvalpkg.ForceReleaseOperation{
		ProjectKey:    agentmail.CanonicalProjectKey(projectKey),
		ReservationID: reservationID, RequestedBy: approvalIdentity(r),
		AgentName: req.AgentName, Note: req.Note, NotifyPrevious: req.NotifyPrevious,
	}
	var engine *approvalpkg.Engine
	if pol.ForceReleasePolicy() != "never" && pol.ForceReleasePolicy() != "auto" {
		engine = s.requireApprovalEngine(w, reqID)
		if engine == nil {
			return
		}
	}

	ctx, cancel := context.WithTimeout(approvalpkg.WithActorProvenance(r.Context(), s.httpActorProvenance(r)), 10*time.Second)
	defer cancel()
	var client *agentmail.Client
	loadClient := func() error {
		if client != nil {
			return nil
		}
		var err error
		client, err = s.getMailClient()
		if err != nil {
			return err
		}
		if client == nil {
			return agentmail.ErrServerUnavailable
		}
		return nil
	}
	decision, gateErr := engine.EvaluateForceRelease(ctx, pol.ForceReleasePolicy(), operation, req.NewApproval, func(ctx context.Context) (approvalpkg.ForceReleaseLease, error) {
		if err := loadClient(); err != nil {
			return approvalpkg.ForceReleaseLease{}, err
		}
		reservation, err := client.GetReservation(ctx, operation.ProjectKey, reservationID)
		if err != nil {
			return approvalpkg.ForceReleaseLease{}, err
		}
		if reservation == nil {
			return approvalpkg.ForceReleaseLease{}, fmt.Errorf("reservation inspection returned no result")
		}
		return approvalpkg.ForceReleaseLease{
			Holder: reservation.AgentName, Path: reservation.PathPattern,
			CreatedAt: reservation.CreatedTS.Time.UTC().Format(time.RFC3339Nano),
			ExpiresAt: reservation.ExpiresTS.Time.UTC().Format(time.RFC3339Nano),
			ProjectID: reservation.ProjectID, Exclusive: reservation.Exclusive,
		}, nil
	})
	details := map[string]interface{}{
		"approval_status":         decision.ApprovalStatus,
		"operation_key":           decision.OperationKey,
		"force_release_attempted": false,
	}
	if decision.ApprovalID != "" {
		details["approval_id"] = decision.ApprovalID
	}
	if decision.Created {
		if record, err := engine.Check(ctx, decision.ApprovalID); err == nil {
			snapshot := approvalFromState(record)
			s.publishApprovalEvent("approval.requested", map[string]interface{}{
				"project_key": operation.ProjectKey,
				"approval_id": snapshot.ID, "action": snapshot.Action, "resource": snapshot.Resource,
				"requestor": snapshot.Requestor, "reason": snapshot.Reason,
				"slb_required": snapshot.SLBRequired, "status": snapshot.Status,
				"expires_at":   snapshot.ExpiresAt.Format(time.RFC3339Nano),
				"requested_at": snapshot.CreatedAt.Format(time.RFC3339Nano),
			})
		}
	}
	if gateErr != nil {
		switch {
		case errors.Is(gateErr, approvalpkg.ErrNotFound):
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "previous approval not found", details, reqID)
		case errors.Is(gateErr, approvalpkg.ErrForceReleaseScope):
			writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, gateErr.Error(), details, reqID)
		default:
			writeAgentMailReservationError(w, reqID, gateErr, "reservation inspection is not supported by the configured Agent Mail server", details)
		}
		return
	}
	if !decision.Allowed {
		status, code := http.StatusForbidden, ErrCodeForbidden
		switch decision.ApprovalStatus {
		case "consumed", "expired", "target_changed", "approved":
			status, code = http.StatusConflict, ErrCodeConflict
		}
		writeErrorResponse(w, status, code, decision.Message, details, reqID)
		return
	}

	if err := loadClient(); err != nil {
		writeAgentMailReservationError(w, reqID, err, "force releasing reservations is not supported by the configured Agent Mail server", details)
		return
	}
	details["force_release_attempted"] = true
	details["execution_status"] = "unconfirmed"
	result, err := client.ForceReleaseReservation(ctx, agentmail.ForceReleaseOptions{
		ProjectKey: operation.ProjectKey, AgentName: operation.AgentName, ReservationID: reservationID,
		Note: operation.Note, NotifyPrevious: operation.NotifyPrevious,
	})
	if err != nil {
		if decision.ApprovalID != "" {
			details["hint"] = "Approval remains consumed. Inspect the reservation before requesting a new authorization with new_approval set to this approval_id."
		}
		writeAgentMailReservationError(w, reqID, err, "force releasing reservations is not supported by the configured Agent Mail server", details)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "force-release returned no result; inspect reservation state before retrying", details, reqID)
		return
	}
	if !result.Success {
		details["execution_status"] = "refused"
		details["notified"] = result.Notified
		if result.PreviousHolder != "" {
			details["previous_holder"] = result.PreviousHolder
		}
		if result.PathPattern != "" {
			details["path_pattern"] = result.PathPattern
		}
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, "force-release denied: reservation may not be stale or agent may still be active", details, reqID)
		return
	}

	response := map[string]interface{}{
		"reservation_id": reservationID, "force_released": true,
		"notified": result.Notified, "approval_status": decision.ApprovalStatus,
		"execution_status": "confirmed",
	}
	if decision.ApprovalID != "" {
		response["approval_id"] = decision.ApprovalID
	}
	if result.ReleasedAt != nil {
		response["released_at"] = result.ReleasedAt
	}
	if result.PreviousHolder != "" {
		response["previous_holder"] = result.PreviousHolder
	}
	if result.PathPattern != "" {
		response["path_pattern"] = result.PathPattern
	}
	eventPayload := map[string]interface{}{
		"agent_name": req.AgentName, "reservation_id": reservationID, "force_released": true,
		"notified": result.Notified, "approval_id": decision.ApprovalID, "requested_by": operation.RequestedBy,
	}
	if result.PreviousHolder != "" {
		eventPayload["previous_holder"] = result.PreviousHolder
	}
	if result.PathPattern != "" {
		eventPayload["path_pattern"] = result.PathPattern
	}
	if result.ReleasedAt != nil {
		eventPayload["released_at"] = result.ReleasedAt
	}
	s.publishReservationEvent(operation.ProjectKey, req.AgentName, "reservation.released", eventPayload)
	writeSuccessResponse(w, http.StatusOK, response, reqID)
}
