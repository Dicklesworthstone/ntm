package serve

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

const defaultServeAnalyticsDays = 30

// AnalyticsEnvelopeResponse is the REST JSON envelope for GET /api/v1/analytics
// (reflected into OpenAPI 3.1 components.schemas).
type AnalyticsEnvelopeResponse struct {
	APIResponse
	SchemaID       string                               `json:"schema_id"`
	SchemaVersion  string                               `json:"schema_version"`
	Period         string                               `json:"period"`
	Since          string                               `json:"since"`
	Days           int                                  `json:"days,omitempty"`
	TotalSessions  int                                  `json:"total_sessions"`
	TotalAgents    int                                  `json:"total_agents"`
	TotalPrompts   int                                  `json:"total_prompts"`
	TotalCharsSent int                                  `json:"total_chars_sent"`
	TotalTokensEst int                                  `json:"total_tokens_estimated"`
	AgentBreakdown map[string]robot.AnalyticsAgentStats `json:"agent_breakdown"`
	Sessions       []robot.AnalyticsSessionSummary      `json:"sessions,omitempty"`
	ErrorCount     int                                  `json:"error_count"`
	ErrorTypes     map[string]int                       `json:"error_types"`
	AgentCrashes   int                                  `json:"agent_crashes"`
	AgentRestarts  int                                  `json:"agent_restarts"`
	AgentHints     *robot.AgentHints                    `json:"_agent_hints,omitempty"`
}

func (s *Server) registerAnalyticsRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadHealth)).Get("/analytics", s.handleGetAnalyticsV1)
}

func mapRobotAnalyticsError(out *robot.AnalyticsOutput) (int, string, string) {
	if out == nil {
		return http.StatusInternalServerError, ErrCodeInternalError, "analytics operation returned no result"
	}
	msg := strings.TrimSpace(out.Error)
	if msg == "" {
		msg = "analytics operation failed"
	}
	switch out.ErrorCode {
	case robot.ErrCodeInvalidFlag:
		return http.StatusBadRequest, ErrCodeBadRequest, msg
	case robot.ErrCodeTimeout:
		return http.StatusGatewayTimeout, ErrCodeTimeout, msg
	default:
		return http.StatusInternalServerError, ErrCodeInternalError, msg
	}
}

func parseAnalyticsSessionsParam(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0", "no":
		return false, true
	case "true", "1", "yes":
		return true, true
	default:
		return false, false
	}
}

func wantsPrometheusAnalytics(formatQuery string, headers http.Header) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(formatQuery)) {
	case "":
		for _, acceptVal := range headers.Values("Accept") {
			lower := strings.ToLower(acceptVal)
			if strings.Contains(lower, "text/plain") ||
				strings.Contains(lower, "application/openmetrics-text") {
				return true, true
			}
		}
		return false, true
	case "json":
		return false, true
	case "prometheus", "prom":
		return true, true
	default:
		return false, false
	}
}

// handleGetAnalyticsV1 handles GET /api/v1/analytics.
func (s *Server) handleGetAnalyticsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	days := defaultServeAnalyticsDays
	if rawDays := strings.TrimSpace(q.Get("days")); rawDays != "" {
		parsed, err := strconv.Atoi(rawDays)
		if err != nil || parsed < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "days must be a non-negative integer", nil, reqID)
			return
		}
		days = parsed
	}

	includeSessions, ok := parseAnalyticsSessionsParam(q.Get("sessions"))
	if !ok {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "sessions must be true or false", nil, reqID)
		return
	}

	promFormat, ok := wantsPrometheusAnalytics(q.Get("format"), r.Header)
	if !ok {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "format must be json or prometheus", nil, reqID)
		return
	}

	since := strings.TrimSpace(q.Get("since"))
	out, err := robot.GetAnalytics(r.Context(), robot.AnalyticsOptions{
		Days:     days,
		Since:    since,
		Sessions: includeSessions,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotAnalyticsError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	if promFormat {
		body := robot.FormatAnalyticsPrometheus(out)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if reqID != "" {
			w.Header().Set("X-Request-Id", reqID)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return
	}

	breakdown := out.AgentBreakdown
	if breakdown == nil {
		breakdown = map[string]robot.AnalyticsAgentStats{}
	}
	errorTypes := out.ErrorTypes
	if errorTypes == nil {
		errorTypes = map[string]int{}
	}

	resp := AnalyticsEnvelopeResponse{
		SchemaID:       out.SchemaID,
		SchemaVersion:  out.SchemaVersion,
		Period:         out.Period,
		Since:          out.Since,
		Days:           out.Days,
		TotalSessions:  out.TotalSessions,
		TotalAgents:    out.TotalAgents,
		TotalPrompts:   out.TotalPrompts,
		TotalCharsSent: out.TotalCharsSent,
		TotalTokensEst: out.TotalTokensEst,
		AgentBreakdown: breakdown,
		Sessions:       out.Sessions,
		ErrorCount:     out.ErrorCount,
		ErrorTypes:     errorTypes,
		AgentCrashes:   out.AgentCrashes,
		AgentRestarts:  out.AgentRestarts,
		AgentHints:     out.AgentHints,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if includeSessions && out.Sessions != nil && len(out.Sessions) == 0 {
		payload["sessions"] = []robot.AnalyticsSessionSummary{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
