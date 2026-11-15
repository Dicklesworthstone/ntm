package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/state"
)

const (
	defaultIncidentsListLimit   = 50
	maxIncidentsListLimit       = 500
	defaultIncidentEventsLimit  = 50
	maxIncidentEventsLimit      = 500
	maxIncidentMutationBodySize = 1 << 20 // 1 MiB
)

// =============================================================================
// Incident REST Request & Response Shapes (Reflected into OpenAPI 3.1)
// =============================================================================

// IncidentsListEnvelopeResponse is the REST envelope for GET /api/v1/incidents.
type IncidentsListEnvelopeResponse struct {
	APIResponse
	Incidents []state.Incident `json:"incidents"`
	Count     int              `json:"count"`
}

// IncidentDetailEnvelopeResponse is the REST envelope for GET /api/v1/incidents/{id}.
type IncidentDetailEnvelopeResponse struct {
	APIResponse
	Incident     *state.Incident              `json:"incident"`
	Events       []state.StoredAttentionEvent `json:"events"`
	EventCount   int                          `json:"event_count"`
	ReplayWindow *state.AttentionReplayWindow `json:"replay_window,omitempty"`
}

// IncidentStatusUpdateRequest is the JSON request body for POST /api/v1/incidents/{id}/status.
type IncidentStatusUpdateRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// IncidentResolveRequest is the optional JSON request body for POST /api/v1/incidents/{id}/resolve.
type IncidentResolveRequest struct {
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

// IncidentStatusEnvelopeResponse is the REST envelope for POST /api/v1/incidents/{id}/status
// and POST /api/v1/incidents/{id}/resolve.
type IncidentStatusEnvelopeResponse struct {
	APIResponse
	IncidentID     string          `json:"incident_id"`
	Action         string          `json:"action"`
	PreviousStatus string          `json:"previous_status"`
	Status         string          `json:"status"`
	Idempotent     bool            `json:"idempotent"`
	Note           string          `json:"note,omitempty"`
	Incident       *state.Incident `json:"incident,omitempty"`
}

func (s *Server) registerIncidentRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadEvents)).Get("/incidents", s.handleListIncidentsV1)
	r.With(s.RequirePermission(PermReadEvents)).Get("/incidents/{id}", s.handleGetIncidentV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/incidents/{id}/status", s.handleUpdateIncidentStatusV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/incidents/{id}/resolve", s.handleResolveIncidentV1)
}

func parseBoolQueryParam(raw string) (bool, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("expected boolean true/false, got %q", raw)
	}
}

func normalizeTargetIncidentStatus(raw string) (state.IncidentStatus, string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(state.IncidentStatusOpen):
		return state.IncidentStatusOpen, "reopen", true
	case string(state.IncidentStatusInvestigating), "investigate":
		return state.IncidentStatusInvestigating, "investigate", true
	case string(state.IncidentStatusResolved), "resolve":
		return state.IncidentStatusResolved, "resolve", true
	case string(state.IncidentStatusMuted), "mute":
		return state.IncidentStatusMuted, "mute", true
	default:
		return "", "", false
	}
}

// handleListIncidentsV1 handles GET /api/v1/incidents.
func (s *Server) handleListIncidentsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	q := r.URL.Query()
	includeResolved, err := parseBoolQueryParam(q.Get("include_resolved"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid include_resolved: "+err.Error(), nil, reqID)
		return
	}

	statusFilter := strings.ToLower(strings.TrimSpace(q.Get("status")))
	if statusFilter != "" {
		switch statusFilter {
		case string(state.IncidentStatusOpen),
			string(state.IncidentStatusInvestigating),
			string(state.IncidentStatusResolved),
			string(state.IncidentStatusMuted):
		default:
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("invalid status %q (expected open, investigating, resolved, or muted)", statusFilter), nil, reqID)
			return
		}
	}

	limit := defaultIncidentsListLimit
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxIncidentsListLimit {
			parsed = maxIncidentsListLimit
		}
		limit = parsed
	}

	queryIncludeResolved := includeResolved ||
		statusFilter == string(state.IncidentStatusResolved) ||
		statusFilter == string(state.IncidentStatusMuted)

	queryLimit := limit
	if statusFilter != "" {
		queryLimit = 0
	}

	rows, err := s.stateStore.ListIncidents(queryIncludeResolved, queryLimit)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to list incidents: "+err.Error(), nil, reqID)
		return
	}

	incidents := make([]state.Incident, 0, len(rows))
	for _, row := range rows {
		if statusFilter != "" && string(row.Status) != statusFilter {
			continue
		}
		incidents = append(incidents, row)
		if len(incidents) >= limit {
			break
		}
	}

	resp := IncidentsListEnvelopeResponse{
		Incidents: incidents,
		Count:     len(incidents),
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleGetIncidentV1 handles GET /api/v1/incidents/{id}.
func (s *Server) handleGetIncidentV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "incident id is required", nil, reqID)
		return
	}

	eventsLimit := defaultIncidentEventsLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("events_limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "events_limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxIncidentEventsLimit {
			parsed = maxIncidentEventsLimit
		}
		eventsLimit = parsed
	}

	incident, err := s.stateStore.GetIncident(id)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load incident: "+err.Error(), nil, reqID)
		return
	}
	if incident == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("incident %q not found", id), nil, reqID)
		return
	}

	events, err := s.stateStore.GetEventsForIncident(id, eventsLimit)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load incident events: "+err.Error(), nil, reqID)
		return
	}
	if events == nil {
		events = []state.StoredAttentionEvent{}
	}

	var replayWindow *state.AttentionReplayWindow
	if rw, rwErr := s.stateStore.GetAttentionReplayWindow(); rwErr == nil {
		replayWindow = &rw
	}

	resp := IncidentDetailEnvelopeResponse{
		Incident:     incident,
		Events:       events,
		EventCount:   len(events),
		ReplayWindow: replayWindow,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleUpdateIncidentStatusV1 handles POST /api/v1/incidents/{id}/status.
func (s *Server) handleUpdateIncidentStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "incident id is required", nil, reqID)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIncidentMutationBodySize)
	var req IncidentStatusUpdateRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	targetStatus, action, ok := normalizeTargetIncidentStatus(req.Status)
	if !ok {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "valid status required (open, investigating, resolved, or muted)", nil, reqID)
		return
	}

	s.applyIncidentStatusTransition(w, r, reqID, id, targetStatus, action, strings.TrimSpace(req.Reason))
}

// handleResolveIncidentV1 handles POST /api/v1/incidents/{id}/resolve.
func (s *Server) handleResolveIncidentV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "incident id is required", nil, reqID)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIncidentMutationBodySize)
	var req IncidentResolveRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = strings.TrimSpace(req.Note)
	}
	s.applyIncidentStatusTransition(w, r, reqID, id, state.IncidentStatusResolved, "resolve", reason)
}

func (s *Server) applyIncidentStatusTransition(
	w http.ResponseWriter,
	r *http.Request,
	reqID string,
	id string,
	targetStatus state.IncidentStatus,
	action string,
	reason string,
) {
	prior, err := s.stateStore.GetIncident(id)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load incident: "+err.Error(), nil, reqID)
		return
	}
	if prior == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("incident %q not found", id), nil, reqID)
		return
	}

	actorType, actorID, actorOrigin, actorLabel := attentionActorFromRequest(r)
	if strings.TrimSpace(actorLabel) == "" {
		actorLabel = "operator"
	}

	prevStatus := string(prior.Status)
	updated := prior
	idempotent := false
	note := reason

	if prior.Status == targetStatus {
		idempotent = true
		if note == "" {
			note = fmt.Sprintf("already %s (idempotent no-op)", targetStatus)
		}
	} else {
		effectiveReason := reason
		if targetStatus == state.IncidentStatusResolved && effectiveReason == "" {
			effectiveReason = "resolved via REST API"
		}
		if err := s.stateStore.UpdateIncidentStatus(id, targetStatus, actorLabel, effectiveReason); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to update incident status: "+err.Error(), nil, reqID)
			return
		}
		note = effectiveReason
		if reloaded, reloadErr := s.stateStore.GetIncident(id); reloadErr == nil && reloaded != nil {
			updated = reloaded
		}

		prevJSON, _ := json.Marshal(prior)
		nextJSON, _ := json.Marshal(updated)
		_, _ = s.stateStore.RecordAuditEvent(&state.AuditEvent{
			Ts:              time.Now().UTC(),
			ActorType:       actorType,
			ActorID:         actorID,
			ActorOrigin:     actorOrigin,
			RequestID:       reqID,
			CorrelationID:   r.Header.Get("Idempotency-Key"),
			Category:        "incident",
			EventType:       action,
			Severity:        updated.Severity,
			EntityType:      "incident",
			EntityID:        id,
			PreviousState:   string(prevJSON),
			NewState:        string(nextJSON),
			ChangeSummary:   fmt.Sprintf("%s incident %s (%s -> %s)", action, id, prevStatus, updated.Status),
			Reason:          effectiveReason,
			DisclosureState: "visible",
			RetentionClass:  state.RetentionClassStandard,
		})
	}

	SetAuditResource(r, "incident", id)
	SetAuditAction(r, AuditActionUpdate)
	SetAuditDetails(r, fmt.Sprintf("%s incident %s (%s -> %s)", action, id, prevStatus, updated.Status))

	resp := IncidentStatusEnvelopeResponse{
		IncidentID:     id,
		Action:         action,
		PreviousStatus: prevStatus,
		Status:         string(updated.Status),
		Idempotent:     idempotent,
		Note:           note,
		Incident:       updated,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
