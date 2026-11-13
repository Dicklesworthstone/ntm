package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// handleSessions handles /api/sessions - list all sessions.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.stateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not available")
		return
	}

	sessions, err := s.stateStore.ListSessions("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// handleSession handles /api/sessions/{id} - get session details.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "session ID required")
		return
	}
	sessionID := parts[0]

	if s.stateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not available")
		return
	}

	session, err := s.stateStore.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Check for sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "agents":
			s.handleSessionAgents(w, r, sessionID)
			return
		case "events":
			s.handleSessionEvents(w, r, sessionID)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"session": session,
	})
}

// handleSessionAgents handles /api/sessions/{id}/agents. It reports the same
// agents as GET /api/v1/sessions/{id}/agents in the legacy envelope.
func (s *Server) handleSessionAgents(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := tmux.ValidateSessionName(sessionID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid session ID: "+err.Error())
		return
	}

	roster, err := s.listSessionAgents(r.Context(), sessionID)
	if err != nil {
		status, _, message := sessionAgentsFailure(err)
		writeError(w, status, message)
		return
	}

	body := roster.payload(sessionID)
	body["success"] = true
	writeJSON(w, http.StatusOK, body)
}

// handleSessionEvents handles /api/sessions/{id}/events.
func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}

	// Get recent events from event bus history
	eventsData := s.eventBus.History(100)

	// Filter to session if specified
	var filtered []events.BusEvent
	for _, e := range eventsData {
		if sessionID == "" || e.EventSession() == sessionID {
			filtered = append(filtered, e)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"session_id": sessionID,
		"events":     filtered,
		"count":      len(filtered),
	})
}

// handleRobotStatus handles /api/robot/status - proxies to robot status.
func (s *Server) robotPaginationOptions() robot.PaginationOptions {
	return robot.PaginationOptions{
		Store:         s.stateStore,
		ExplicitStore: true,
	}
}

func (s *Server) handleRobotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	output, err := robot.GetStatusWithOptions(s.robotPaginationOptions())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, output)
}

// handleRobotHealth handles /api/robot/health by delegating to real robot.GetHealth().
func (s *Server) handleRobotHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	output, err := robot.GetHealth()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/v1/robot/health>; rel="successor-version"`)
	writeJSON(w, http.StatusOK, output)
}

// validateSessionParam checks that a session ID from a URL parameter is
// non-empty and satisfies tmux naming rules. It writes an error response and
// returns false when validation fails, so callers can return early.
func validateSessionParam(w http.ResponseWriter, sessionID, reqID string) bool {
	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return false
	}
	if err := tmux.ValidateSessionName(sessionID); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("invalid session ID: %s", err.Error()), nil, reqID)
		return false
	}
	return true
}

// validatePaneIdx parses a pane index string and validates it is non-negative.
// It writes an error response and returns -1 when validation fails.
func validatePaneIdx(w http.ResponseWriter, paneIdxStr, reqID string) int {
	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return -1
	}
	if paneIdx < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pane index must be non-negative", nil, reqID)
		return -1
	}
	return paneIdx
}

// handleSessionsV1 handles GET /api/v1/sessions.
func (s *Server) handleSessionsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	sessions, err := s.stateStore.ListSessions("")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// The runtime store only knows sessions that were recorded through it, and
	// ordinary `ntm spawn` sessions never are. Merge in the sessions tmux is
	// running right now so the dashboard does not report "no sessions" while
	// agents are live. A tmux failure degrades to the stored rows.
	live, err := s.liveTmuxSessions(r.Context())
	if err != nil {
		slog.Warn("listing live tmux sessions failed", "request_id", reqID, "error", err)
		live = nil
	}

	visible := make([]interface{}, 0, len(sessions)+len(live))
	known := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		visible = append(visible, session)
		known[session.Name] = struct{}{}
	}
	for _, session := range live {
		if _, ok := known[session.Name]; ok {
			continue
		}
		known[session.Name] = struct{}{}
		visible = append(visible, liveSessionRecord(session))
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"sessions": visible,
		"count":    len(visible),
	}, reqID)
}

// defaultLiveSessionsTimeout bounds the tmux queries behind the sessions and
// agents endpoints. The web UI polls them every few seconds; without a bound, a
// wedged tmux server would hang every poll (and pile up tmux processes) instead
// of degrading to the stored rows.
const defaultLiveSessionsTimeout = 5 * time.Second

// liveTmuxTimeout is the bound applied to each live tmux query a read endpoint
// makes.
func (s *Server) liveTmuxTimeout() time.Duration {
	if s.liveSessionsTimeout > 0 {
		return s.liveSessionsTimeout
	}
	return defaultLiveSessionsTimeout
}

// liveTmuxSessions returns the running tmux sessions sorted by name.
func (s *Server) liveTmuxSessions(ctx context.Context) ([]tmux.Session, error) {
	list := tmux.ListSessionsContext
	if s.listLiveSessions != nil {
		list = s.listLiveSessions
	}
	ctx, cancel := context.WithTimeout(ctx, s.liveTmuxTimeout())
	defer cancel()
	live, err := list(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].Name < live[j].Name })
	return live, nil
}

// liveSessionRecord describes a running tmux session the runtime store has no
// row for, using the same field names as state.Session.
func liveSessionRecord(session tmux.Session) map[string]interface{} {
	return map[string]interface{}{
		"id":       session.Name,
		"name":     session.Name,
		"status":   state.SessionActive,
		"windows":  session.Windows,
		"attached": session.Attached,
		"source":   "tmux",
	}
}

// handleSessionV1 handles GET /api/v1/sessions/{id}.
func (s *Server) handleSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	session, err := s.stateStore.GetSession(sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if session == nil {
		// Same fallback as the list endpoint: a live tmux session without a
		// store row is still a session the dashboard can open.
		live, err := s.liveTmuxSessions(r.Context())
		if err != nil {
			// tmux, not the server, failed: a bounded listing that ran out of
			// time is a timeout, anything else (circuit open, tmux error) is
			// an unavailable dependency. Neither is an internal error.
			status, code := http.StatusServiceUnavailable, ErrCodeServiceUnavail
			if errors.Is(err, context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, ErrCodeTimeout
			}
			writeErrorResponse(w, status, code, "listing live tmux sessions failed: "+err.Error(), nil, reqID)
			return
		}
		for _, candidate := range live {
			if candidate.Name == sessionID {
				writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
					"session": liveSessionRecord(candidate),
				}, reqID)
				return
			}
		}
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "session not found", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session": session,
	}, reqID)
}

// handleSessionEventsRouteV1 mounts GET /api/v1/sessions/{id}/events with a named handler.
func (s *Server) handleSessionEventsRouteV1(w http.ResponseWriter, r *http.Request) {
	s.handleSessionEventsV1(w, r, chi.URLParam(r, "id"))
}

// handleSessionEventsV1 handles GET /api/v1/sessions/{id}/events.
func (s *Server) handleSessionEventsV1(w http.ResponseWriter, r *http.Request, sessionID string) {
	reqID := requestIDFromContext(r.Context())

	if s.eventBus == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "event bus not available", nil, reqID)
		return
	}

	eventsData := s.eventBus.History(100)

	var filtered []events.BusEvent
	for _, e := range eventsData {
		if sessionID == "" || e.EventSession() == sessionID {
			filtered = append(filtered, e)
		}
	}

	// Ensure events is never null
	if filtered == nil {
		filtered = []events.BusEvent{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"events":     filtered,
		"count":      len(filtered),
	}, reqID)
}

// handleRobotStatusV1 handles GET /api/v1/robot/status.
func (s *Server) handleRobotStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetStatusWithOptions(s.robotPaginationOptions())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot status", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotHealthV1 handles GET /api/v1/robot/health.
func (s *Server) handleRobotHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetHealth()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot health", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotSnapshotV1 handles GET /api/v1/robot/snapshot.
func (s *Server) handleRobotSnapshotV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetSnapshotWithOptions(nil, s.robotPaginationOptions())
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot snapshot", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotDigestV1 handles GET /api/v1/robot/digest.
func (s *Server) handleRobotDigestV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetDigest(robot.DigestOptions{
		Store:         s.stateStore,
		ExplicitStore: true,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot digest", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotAttentionV1 handles GET /api/v1/robot/attention.
// Returns cursor-replayable AttentionOutput (non-blocking).
func (s *Server) handleRobotAttentionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	var cursor int64
	rawCursor := strings.TrimSpace(q.Get("cursor"))
	if rawCursor == "" {
		rawCursor = strings.TrimSpace(q.Get("since_cursor"))
	}
	if rawCursor != "" {
		parsed, err := strconv.ParseInt(rawCursor, 10, 64)
		if err != nil || parsed < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid cursor parameter", nil, reqID)
			return
		}
		cursor = parsed
	}

	limit := 100
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid limit parameter", nil, reqID)
			return
		}
		limit = parsed
	}

	var actionRequiredOnly bool
	if rawAR := strings.TrimSpace(q.Get("action_required")); rawAR != "" {
		parsed, err := strconv.ParseBool(rawAR)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid action_required parameter", nil, reqID)
			return
		}
		actionRequiredOnly = parsed
	}

	output, err := robot.GetAttention(robot.AttentionQueryOptions{
		Cursor:             cursor,
		Limit:              limit,
		Session:            strings.TrimSpace(q.Get("session")),
		Profile:            strings.TrimSpace(q.Get("profile")),
		CategoryFilter:     parseCSVParam(q.Get("category")),
		ActionRequiredOnly: actionRequiredOnly,
	})
	if err != nil {
		var cursorErr *robot.CursorExpiredError
		if errors.As(err, &cursorErr) {
			newestCursor := int64(0)
			if feed := robot.GetAttentionFeed(); feed != nil {
				newestCursor = feed.CurrentCursor()
			}
			payload := attentionCursorExpiredPayload(cursorErr, newestCursor)
			if reqID != "" {
				payload["request_id"] = reqID
			}
			writeJSON(w, http.StatusConflict, payload)
			return
		}
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot attention", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotDashboardV1 handles GET /api/v1/robot/dashboard.
func (s *Server) handleRobotDashboardV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetDashboard()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot dashboard", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotTerseV1 handles GET /api/v1/robot/terse.
func (s *Server) handleRobotTerseV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetTerseForStore(s.stateStore, nil)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot terse", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotTriageV1 handles GET /api/v1/robot/triage.
func (s *Server) handleRobotTriageV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetTriage(robot.TriageOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot triage", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotPlanV1 handles GET /api/v1/robot/plan.
func (s *Server) handleRobotPlanV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetPlan()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot plan", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotGraphV1 handles GET /api/v1/robot/graph.
func (s *Server) handleRobotGraphV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetGraph()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot graph", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotActivityV1 handles GET /api/v1/robot/activity.
func (s *Server) handleRobotActivityV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := r.URL.Query().Get("session")
	output, err := robot.GetActivity(robot.ActivityOptions{
		Session:       session,
		Store:         s.stateStore,
		ExplicitStore: true,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot activity", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotAlertsV1 handles GET /api/v1/robot/alerts.
func (s *Server) handleRobotAlertsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	includeResolved := r.URL.Query().Get("include_resolved") == "true"
	output, err := robot.GetAlertsDetailed(includeResolved)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot alerts", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// =============================================================================
// Session Kernel Handlers (call kernel commands)
// =============================================================================

// CreateSessionRequest is the request body for POST /sessions.
type CreateSessionRequest struct {
	Session string `json:"session"`
	Panes   int    `json:"panes,omitempty"`
}

// handleCreateSessionV1 handles POST /api/v1/sessions.
func (s *Server) handleCreateSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session name required", nil, reqID)
		return
	}

	if err := tmux.ValidateSessionName(req.Session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %s", err.Error()), nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.create", map[string]interface{}{
		"session": req.Session,
		"panes":   req.Panes,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusCreated, data, reqID)
}

// handleSessionStatusV1 handles GET /api/v1/sessions/{id}/status.
func (s *Server) handleSessionStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.status", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionAttachV1 handles POST /api/v1/sessions/{id}/attach.
func (s *Server) handleSessionAttachV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.attach", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SessionZoomRequest is the request body for POST /sessions/{id}/zoom.
type SessionZoomRequest struct {
	Pane int `json:"pane"`
}

// handleSessionZoomV1 handles POST /api/v1/sessions/{id}/zoom.
func (s *Server) handleSessionZoomV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req SessionZoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.zoom", map[string]interface{}{
		"session": sessionID,
		"pane":    req.Pane,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionViewV1 handles POST /api/v1/sessions/{id}/view.
func (s *Server) handleSessionViewV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.view", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// =============================================================================
// Panes API Handlers
// =============================================================================

// handleListPanesV1 handles GET /api/v1/sessions/{sessionId}/panes.
func (s *Server) handleListPanesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	panes, err := tmux.GetPanesContext(r.Context(), sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Convert to serializable format
	paneList := make([]map[string]interface{}, len(panes))
	for i, p := range panes {
		paneList[i] = map[string]interface{}{
			"index":   p.Index,
			"id":      p.ID,
			"title":   p.Title,
			"type":    string(p.Type),
			"variant": p.Variant,
			"active":  p.Active,
			"width":   p.Width,
			"height":  p.Height,
			"command": p.Command,
		}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"panes":      paneList,
		"count":      len(panes),
	}, reqID)
}

// handleGetPaneV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}.
func (s *Server) handleGetPaneV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	panes, err := tmux.GetPanesContext(r.Context(), sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Find the pane by index
	for _, p := range panes {
		if p.Index == paneIdx {
			writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
				"pane": map[string]interface{}{
					"index":   p.Index,
					"id":      p.ID,
					"title":   p.Title,
					"type":    string(p.Type),
					"variant": p.Variant,
					"active":  p.Active,
					"width":   p.Width,
					"height":  p.Height,
					"command": p.Command,
				},
			}, reqID)
			return
		}
	}

	writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "pane not found", nil, reqID)
}

// PaneInputRequest is the request body for POST /sessions/{id}/panes/{paneIdx}/input.
type PaneInputRequest struct {
	Text  string `json:"text"`
	Enter bool   `json:"enter,omitempty"`
}

const defaultPaneInputTimeout = 30 * time.Second

// handlePaneInputV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/input.
func (s *Server) handlePaneInputV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	var req PaneInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Text == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "text required", nil, reqID)
		return
	}

	budget := s.paneInputTimeout
	if budget <= 0 {
		budget = defaultPaneInputTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	fail := func(status int, code string, err error) {
		if ctx.Err() != nil {
			status, code, err = http.StatusGatewayTimeout, ErrCodeTimeout, ctx.Err()
		}
		writeErrorResponse(w, status, code, err.Error(), nil, reqID)
	}
	if err := ctx.Err(); err != nil {
		fail(http.StatusGatewayTimeout, ErrCodeTimeout, err)
		return
	}
	// The server cannot interpret a remote pane's path as a local policy root.
	if tmux.DefaultClient.Remote != "" {
		fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, errors.New("cannot verify project safety policy for a remote tmux pane"))
		return
	}
	resolve := resolvePaneByIndex
	if s.resolvePane != nil {
		resolve = s.resolvePane
	}
	lookup := func() (tmux.Pane, bool) {
		pane, err := resolve(ctx, sessionID, paneIdx)
		if err != nil {
			status, code := http.StatusNotFound, ErrCodeNotFound
			var selectorErr *tmux.PaneSelectorError
			if errors.As(err, &selectorErr) && selectorErr.Kind == tmux.PaneSelectorAmbiguous {
				status, code = http.StatusConflict, ErrCodeConflict
			}
			fail(status, code, err)
			return tmux.Pane{}, false
		}
		selector, err := tmux.ParsePaneSelector(pane.ID)
		if err != nil || selector.Kind != tmux.PaneSelectorID || pane.PID <= 0 || pane.Dead || pane.IsServicePane() {
			fail(http.StatusConflict, ErrCodeConflict, errors.New("pane is not a live input target with a stable physical identity"))
			return tmux.Pane{}, false
		}
		return pane, true
	}
	pane, ok := lookup()
	if !ok {
		return
	}
	// Join assignment/rotation admission and pipeline ownership before checking
	// the physical pane again. Numeric indices can be reused while we wait.
	store, err := assignment.LoadStoreStrictReadOnly(sessionID)
	if err != nil {
		fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, fmt.Errorf("load pane admission state: %w", err))
		return
	}
	releaseTarget, err := store.AcquireTargetLock(ctx, pane.ID)
	if err != nil {
		fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, fmt.Errorf("acquire pane admission: %w", err))
		return
	}
	defer releaseTarget()
	releasePane, err := pipeline.AcquireSharedPaneLock(ctx, pane.ID, "")
	if err != nil {
		fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, fmt.Errorf("acquire pane delivery ownership: %w", err))
		return
	}
	defer releasePane()
	refresh := func() bool {
		current, ok := lookup()
		if !ok {
			return false
		}
		if current.ID != pane.ID || current.PID != pane.PID || current.Type.Canonical() != pane.Type.Canonical() {
			fail(http.StatusConflict, ErrCodeConflict, errors.New("pane identity changed while acquiring delivery ownership; refresh the session before sending"))
			return false
		}
		pane = current
		return true
	}
	if !refresh() {
		return
	}
	if err := pane.Type.ValidateAutomatedPromptDelivery(); err != nil {
		writeErrorResponse(w, http.StatusNotImplemented, ErrCodeNotImplemented, err.Error(), map[string]interface{}{
			"agent_type": pane.Type.Canonical().String(),
			"hint":       agent.GrokPromptDeliveryCapabilityHint,
		}, reqID)
		return
	}
	p, err := loadPaneProjectPolicy(ctx, pane)
	if err != nil {
		if errors.Is(err, errPaneProjectIdentityChanged) {
			fail(http.StatusConflict, ErrCodeConflict, err)
		} else {
			fail(http.StatusServiceUnavailable, ErrCodeServiceUnavail, err)
		}
		return
	}
	if match := p.Check(req.Text); match != nil && match.Action == policy.ActionBlock {
		slog.Warn("pane input blocked by policy",
			"session", sessionID, "pane", paneIdx,
			"pattern", match.Pattern, "request_id", reqID)
		fail(http.StatusForbidden, ErrCodeForbidden, fmt.Errorf("blocked by safety policy: %s", match.Reason))
		return
	}
	if !refresh() {
		return
	}
	// The REST redaction middleware already admitted this text without rewriting
	// it. Keep its bytes intact, including whitespace and stage-only input, while
	// sharing the live quarantine, trust/composer, transport, and submission checks.
	service, err := dispatch.NewService(dispatch.Ports{
		Redactor: dispatch.AllowAllRedactor{}, Deliverer: dispatch.TMUXDeliverer{},
	})
	if err != nil {
		fail(http.StatusInternalServerError, ErrCodeInternalError, err)
		return
	}
	_, err = service.Execute(ctx, dispatch.Request{
		Session: sessionID, Panes: []tmux.Pane{pane}, Selectors: []string{pane.ID},
		RequireSingleSelector: true, IncludeUser: true, AllowEmptyMessage: true,
		Message: req.Text, Submit: req.Enter, StopOnFailure: true,
	})
	if err != nil {
		status, code := http.StatusInternalServerError, ErrCodeInternalError
		var dispatchErr *dispatch.Error
		if dispatch.IsGuaranteedNoDeliveryActuation(err) ||
			(errors.As(err, &dispatchErr) && dispatchErr.Code == dispatch.ErrRotationPending) {
			status, code = http.StatusConflict, ErrCodeConflict
		}
		fail(status, code, err)
		return
	}

	slog.Info("pane input sent via API",
		"session", sessionID, "pane", paneIdx,
		"text_len", len(req.Text), "request_id", reqID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"sent": true,
		"pane": pane.ID,
	}, reqID)
}

// handlePaneInterruptV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/interrupt.
func (s *Server) handlePaneInterruptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	pane, ok := s.resolvePaneForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	paneTarget := pane.ID

	// Press the agent's interrupt KEY (Ctrl+C; Escape for omp). SendKeys is
	// literal (-l) and would have typed the characters "C-c" instead.
	if err := tmux.SendInterruptForAgent(paneTarget, pane.Type); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"interrupted": true,
		"pane":        paneTarget,
		"key":         tmux.InterruptKeyLabel(pane.Type),
	}, reqID)
}

// handlePaneOutputV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}/output.
func (s *Server) handlePaneOutputV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	// Parse lines parameter (default 100)
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if _, err := fmt.Sscanf(l, "%d", &lines); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid lines parameter", nil, reqID)
			return
		}
		if lines < 1 || lines > 10000 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be 1-10000", nil, reqID)
			return
		}
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	// Deliberately does NOT start a background streamer. This is a one-shot
	// read that captures the pane directly on the next line, so the streamer's
	// output went nowhere — but StreamManager only retires a streamer on an
	// explicit StopStream, and this handler never issued one. Every distinct
	// pane ever read this way leaked a goroutine that kept forking
	// `tmux capture-pane` forever, including long after its session was killed
	// (runPollingLoop treats a capture failure as `continue`). Starting a
	// server-side subscription was also a side effect on a GET. Callers that
	// want live output use POST/DELETE .../stream, which is the paired API.
	output, err := tmux.CapturePaneOutputContext(r.Context(), paneTarget, lines)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":   paneTarget,
		"output": output,
		"lines":  lines,
	}, reqID)
}

// handleGetPaneTitleV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}/title.
func (s *Server) handleGetPaneTitleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	title, err := tmux.GetPaneTitle(paneTarget)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":  paneTarget,
		"title": title,
	}, reqID)
}

// PaneTitleRequest is the request body for PATCH /sessions/{id}/panes/{paneIdx}/title.
type PaneTitleRequest struct {
	Title string `json:"title"`
}

// handleSetPaneTitleV1 handles PATCH /api/v1/sessions/{sessionId}/panes/{paneIdx}/title.
func (s *Server) handleSetPaneTitleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	var req PaneTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	if err := tmux.SetPaneTitle(paneTarget, req.Title); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":  paneTarget,
		"title": req.Title,
	}, reqID)
}

// resolvePaneByIndex returns the complete pane snapshot so callers can run
// agent-specific admission before using its base-index-independent tmux ID.
// The context is honored so the HTTP layer can cancel a slow ListPanes call.
func resolvePaneByIndex(ctx context.Context, session string, paneIdx int) (tmux.Pane, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("list panes: %w", err)
	}
	var matches []tmux.Pane
	for _, p := range panes {
		if p.Index == paneIdx {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for i, pane := range matches {
			ids[i] = pane.ID
		}
		return tmux.Pane{}, &tmux.PaneSelectorError{
			Kind: tmux.PaneSelectorAmbiguous, Selector: strconv.Itoa(paneIdx), Matches: len(matches), Available: ids,
		}
	}
	return tmux.Pane{}, fmt.Errorf("no pane with index %d in session %q", paneIdx, session)
}

func (s *Server) resolvePaneForRequest(w http.ResponseWriter, r *http.Request, session string, paneIdx int, reqID string) (tmux.Pane, bool) {
	resolvePane := resolvePaneByIndex
	if s.resolvePane != nil {
		resolvePane = s.resolvePane
	}
	pane, err := resolvePane(r.Context(), session, paneIdx)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("pane not found: %v", err), nil, reqID)
		return tmux.Pane{}, false
	}
	return pane, true
}

func (s *Server) resolvePaneTargetForRequest(w http.ResponseWriter, r *http.Request, session string, paneIdx int, reqID string) (string, bool) {
	pane, ok := s.resolvePaneForRequest(w, r, session, paneIdx, reqID)
	if !ok {
		return "", false
	}
	return pane.ID, true
}
