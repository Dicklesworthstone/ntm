package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/backpressure"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// defaultSSEClientChannelCapacity matches the buffered channel capacity used by
// handleEventStream when subscribing SSE clients.
const defaultSSEClientChannelCapacity = 64

// HealthBackpressureEnvelopeResponse is the REST envelope for
// GET /api/v1/health/backpressure.
type HealthBackpressureEnvelopeResponse struct {
	robot.BackpressureOutput
	RequestID string `json:"request_id,omitempty"`
}

// SwarmLabEnvelopeResponse is the REST envelope for
// GET /api/v1/swarm/lab/scenarios and POST /api/v1/swarm/lab/run.
type SwarmLabEnvelopeResponse struct {
	robot.SwarmLabOutput
	RequestID string `json:"request_id,omitempty"`
}

// SwarmLabRunRequest is the JSON request body for POST /api/v1/swarm/lab/run.
// All executions run strictly in-memory so network callers cannot read or write
// arbitrary host filesystem paths.
type SwarmLabRunRequest struct {
	Action        string  `json:"action,omitempty"`
	Scenario      string  `json:"scenario,omitempty"`
	Gate          string  `json:"gate,omitempty"`
	PaneCount     int     `json:"pane_count,omitempty"`
	CommandCount  int     `json:"command_count,omitempty"`
	Calibrate     bool    `json:"calibrate,omitempty"`
	ProfileID     string  `json:"profile_id,omitempty"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
	IncludeLive   bool    `json:"include_live,omitempty"`
	IncludeOptIn  bool    `json:"include_opt_in,omitempty"`
}

// registerBackpressureRoutes mounts live backpressure inspection and synthetic
// swarm lab / host-capacity calibration endpoints under /api/v1.
func (s *Server) registerBackpressureRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadHealth)).Get("/health/backpressure", s.handleHealthBackpressureV1)
	r.With(s.RequirePermission(PermReadHealth)).Get("/swarm/lab/scenarios", s.handleSwarmLabScenariosV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/swarm/lab/run", s.handleSwarmLabRunV1)
}

func (s *Server) collectServerBackpressureInputs(session string) []backpressure.SurfaceInput {
	inputs := make([]backpressure.SurfaceInput, 0, 3)

	// 1. REST job executor queue utilization.
	jobSnap := s.jobExecutor.snapshot()
	inputs = append(inputs, backpressure.SurfaceInput{
		Surface:       backpressure.SurfaceREST,
		Session:       session,
		Command:       "jobs-executor",
		QueueDepth:    jobSnap.Queued,
		QueueCapacity: jobSnap.QueueCapacity,
		SourceLoaded:  true,
	})

	// 2. SSE subscriber channel utilization.
	sseMaxDepth := 0
	sseCapacity := defaultSSEClientChannelCapacity
	s.sseClientsMu.RLock()
	for ch := range s.sseClients {
		if depth := len(ch); depth > sseMaxDepth {
			sseMaxDepth = depth
		}
		if c := cap(ch); c > sseCapacity {
			sseCapacity = c
		}
	}
	s.sseClientsMu.RUnlock()
	inputs = append(inputs, backpressure.SurfaceInput{
		Surface:       backpressure.SurfaceSSE,
		Session:       session,
		Command:       "events-stream",
		QueueDepth:    sseMaxDepth,
		QueueCapacity: sseCapacity,
		SourceLoaded:  true,
	})

	// 3. WebSocket hub broadcast + per-client send buffer utilization & drops.
	if s.wsHub == nil {
		inputs = append(inputs, backpressure.SurfaceInput{
			Surface:        backpressure.SurfaceWebSocket,
			Session:        session,
			Command:        "ws-hub",
			SourceLoaded:   false,
			MissingWarning: "websocket hub is not initialized.",
		})
	} else {
		wsDepth := len(s.wsHub.broadcast)
		wsCap := cap(s.wsHub.broadcast)
		s.wsHub.clientsMu.RLock()
		for client := range s.wsHub.clients {
			if d := len(client.send); d > wsDepth {
				wsDepth = d
			}
			if c := cap(client.send); c > wsCap {
				wsCap = c
			}
		}
		s.wsHub.clientsMu.RUnlock()
		inputs = append(inputs, backpressure.SurfaceInput{
			Surface:       backpressure.SurfaceWebSocket,
			Session:       session,
			Command:       "ws-hub",
			QueueDepth:    wsDepth,
			QueueCapacity: wsCap,
			DroppedCount:  s.wsHub.dropped.Load(),
			SourceLoaded:  true,
		})
	}

	return inputs
}

func writeBackpressureRobotFailure(w http.ResponseWriter, resp robot.RobotResponse, fallbackMsg, reqID string) {
	code := strings.TrimSpace(resp.ErrorCode)
	if code == "" {
		code = ErrCodeInternalError
	}
	msg := strings.TrimSpace(resp.Error)
	if msg == "" {
		msg = fallbackMsg
	}
	var details map[string]interface{}
	if strings.TrimSpace(resp.Hint) != "" {
		details = map[string]interface{}{"hint": resp.Hint}
	}
	status := http.StatusBadRequest
	if code == robot.ErrCodeInternalError || code == ErrCodeInternalError {
		status = http.StatusInternalServerError
	}
	writeErrorResponse(w, status, code, msg, details, reqID)
}

// handleHealthBackpressureV1 handles GET /api/v1/health/backpressure.
func (s *Server) handleHealthBackpressureV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := strings.TrimSpace(r.URL.Query().Get("session"))

	out, err := robot.GetBackpressure(robot.BackpressureOptions{
		Session:     session,
		ExtraInputs: s.collectServerBackpressureInputs(session),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeBackpressureRobotFailure(w, out.RobotResponse, "backpressure inspection failed", reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleSwarmLabScenariosV1 handles GET /api/v1/swarm/lab/scenarios.
func (s *Server) handleSwarmLabScenariosV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	opts := robot.SwarmLabOptions{
		Action:   "list",
		Scenario: strings.TrimSpace(r.URL.Query().Get("scenario")),
		Gate:     strings.TrimSpace(r.URL.Query().Get("gate")),
	}

	out, err := robot.GetSwarmLab(r.Context(), opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeBackpressureRobotFailure(w, out.RobotResponse, "swarm lab scenario listing failed", reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleSwarmLabRunV1 handles POST /api/v1/swarm/lab/run.
func (s *Server) handleSwarmLabRunV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req SwarmLabRunRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "invalid JSON request body", nil, reqID)
			return
		}
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "run"
	}
	switch action {
	case "run", "corpus", "calibrate":
	default:
		writeErrorResponse(
			w,
			http.StatusBadRequest,
			ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported swarm lab action %q (want run, corpus, or calibrate)", req.Action),
			nil,
			reqID,
		)
		return
	}

	if req.PaneCount < 0 || req.CommandCount < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeInvalidRequest, "pane_count and command_count must be non-negative", nil, reqID)
		return
	}

	gate := strings.TrimSpace(req.Gate)
	if action == "run" && gate == "" && req.PaneCount == 0 && req.CommandCount == 0 {
		gate = "short"
	}

	SetAuditAction(r, AuditAction("swarm.lab."+action))
	if scenario := strings.TrimSpace(req.Scenario); scenario != "" {
		SetAuditResource(r, "swarm_lab_scenario", scenario)
	}

	out, err := robot.GetSwarmLab(r.Context(), robot.SwarmLabOptions{
		Action:        action,
		Scenario:      strings.TrimSpace(req.Scenario),
		Gate:          gate,
		PaneCount:     req.PaneCount,
		CommandCount:  req.CommandCount,
		Calibrate:     req.Calibrate,
		ProfileID:     strings.TrimSpace(req.ProfileID),
		MinConfidence: req.MinConfidence,
		IncludeLive:   req.IncludeLive,
		IncludeOptIn:  req.IncludeOptIn,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeBackpressureRobotFailure(w, out.RobotResponse, "swarm lab execution failed", reqID)
		return
	}

	payload, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
