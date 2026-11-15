package serve

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

const (
	defaultHandoffsListLimit  = 20
	maxHandoffsListLimit      = 200
	maxCreateHandoffBodyBytes = 1 << 20 // 1 MiB
)

// =============================================================================
// Handoff REST Request & Response Shapes (Reflected into OpenAPI 3.1)
// =============================================================================

// CreateHandoffRequest is the JSON request body for POST /api/v1/handoffs.
type CreateHandoffRequest struct {
	Session     string            `json:"session"`
	Auto        bool              `json:"auto,omitempty"`
	Goal        string            `json:"goal,omitempty"`
	Now         string            `json:"now,omitempty"`
	Description string            `json:"description,omitempty"`
	IncludeGit  *bool             `json:"include_git,omitempty"`
	Next        []string          `json:"next,omitempty"`
	Blockers    []string          `json:"blockers,omitempty"`
	Decisions   map[string]string `json:"decisions,omitempty"`
	ProjectDir  string            `json:"project_dir,omitempty"`
}

// HandoffsEnvelopeResponse is the REST envelope for GET /api/v1/handoffs and POST /api/v1/handoffs.
type HandoffsEnvelopeResponse struct {
	APIResponse
	Action        string                      `json:"action"`
	Session       string                      `json:"session"`
	ProjectDir    string                      `json:"project_dir,omitempty"`
	CreatedPath   string                      `json:"created_path,omitempty"`
	LatestPath    string                      `json:"latest_path,omitempty"`
	Latest        *handoff.Handoff            `json:"latest,omitempty"`
	Quality       *handoff.QualityReport      `json:"quality,omitempty"`
	Sessions      []string                    `json:"sessions"`
	Handoffs      []robot.HandoffSummaryItem  `json:"handoffs"`
	TotalReturned int                         `json:"total_returned"`
	Ledger        *robot.HandoffLedgerSummary `json:"ledger,omitempty"`
	AgentHints    *robot.AgentHints           `json:"_agent_hints,omitempty"`
}

// HandoffLatestEnvelopeResponse is the REST envelope for GET /api/v1/handoffs/latest.
type HandoffLatestEnvelopeResponse struct {
	APIResponse
	Session    string                      `json:"session"`
	ProjectDir string                      `json:"project_dir,omitempty"`
	LatestPath string                      `json:"latest_path,omitempty"`
	Latest     *handoff.Handoff            `json:"latest,omitempty"`
	Quality    *handoff.QualityReport      `json:"quality,omitempty"`
	Ledger     *robot.HandoffLedgerSummary `json:"ledger,omitempty"`
	AgentHints *robot.AgentHints           `json:"_agent_hints,omitempty"`
}

func (s *Server) registerHandoffRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadSessions)).Get("/handoffs", s.handleListHandoffsV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/handoffs/latest", s.handleLatestHandoffV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/handoffs", s.handleCreateHandoffV1)
}

func mapRobotHandoffError(out *robot.HandoffOutput) (int, string, string) {
	if out == nil {
		return http.StatusInternalServerError, ErrCodeInternalError, "handoff operation returned no result"
	}
	msg := strings.TrimSpace(out.Error)
	if msg == "" {
		msg = "handoff operation failed"
	}
	switch out.ErrorCode {
	case robot.ErrCodeInvalidFlag:
		return http.StatusBadRequest, ErrCodeBadRequest, msg
	case robot.ErrCodeSessionNotFound:
		return http.StatusNotFound, ErrCodeNotFound, msg
	case robot.ErrCodeTimeout:
		return http.StatusGatewayTimeout, ErrCodeTimeout, msg
	default:
		return http.StatusInternalServerError, ErrCodeInternalError, msg
	}
}

func toServedHandoffsEnvelope(out *robot.HandoffOutput) HandoffsEnvelopeResponse {
	sessions := out.Sessions
	if sessions == nil {
		sessions = []string{}
	}
	handoffs := out.Handoffs
	if handoffs == nil {
		handoffs = []robot.HandoffSummaryItem{}
	}
	return HandoffsEnvelopeResponse{
		Action:        out.Action,
		Session:       out.Session,
		ProjectDir:    out.ProjectDir,
		CreatedPath:   out.CreatedPath,
		LatestPath:    out.LatestPath,
		Latest:        out.Latest,
		Quality:       out.Quality,
		Sessions:      sessions,
		Handoffs:      handoffs,
		TotalReturned: out.TotalReturned,
		Ledger:        out.Ledger,
		AgentHints:    out.AgentHints,
	}
}

// handleListHandoffsV1 handles GET /api/v1/handoffs.
func (s *Server) handleListHandoffsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	session := strings.TrimSpace(q.Get("session"))
	if session == "" {
		session = "all"
	}

	limit := defaultHandoffsListLimit
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxHandoffsListLimit {
			parsed = maxHandoffsListLimit
		}
		limit = parsed
	}

	rawDir := strings.TrimSpace(q.Get("project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(q.Get("project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	out, err := robot.GetHandoff(r.Context(), robot.HandoffOptions{
		Session:    session,
		ProjectDir: projectDir,
		Limit:      limit,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotHandoffError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	resp := toServedHandoffsEnvelope(out)
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleLatestHandoffV1 handles GET /api/v1/handoffs/latest.
func (s *Server) handleLatestHandoffV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	session := strings.TrimSpace(q.Get("session"))
	if session == "" {
		session = "all"
	}

	rawDir := strings.TrimSpace(q.Get("project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(q.Get("project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	out, err := robot.GetHandoff(r.Context(), robot.HandoffOptions{
		Session:    session,
		ProjectDir: projectDir,
		Limit:      1,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotHandoffError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	resp := HandoffLatestEnvelopeResponse{
		Session:    out.Session,
		ProjectDir: out.ProjectDir,
		LatestPath: out.LatestPath,
		Latest:     out.Latest,
		Quality:    out.Quality,
		Ledger:     out.Ledger,
		AgentHints: out.AgentHints,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleCreateHandoffV1 handles POST /api/v1/handoffs.
func (s *Server) handleCreateHandoffV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxCreateHandoffBodyBytes)
	var req CreateHandoffRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	session := strings.TrimSpace(req.Session)
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session is required", nil, reqID)
		return
	}

	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, req.ProjectDir, reqID)
	if !ok {
		return
	}

	includeGit := true
	if req.IncludeGit != nil {
		includeGit = *req.IncludeGit
	}

	out, err := robot.GetHandoff(r.Context(), robot.HandoffOptions{
		Session:     session,
		ProjectDir:  projectDir,
		Limit:       defaultHandoffsListLimit,
		Create:      true,
		Auto:        req.Auto,
		Goal:        req.Goal,
		Now:         req.Now,
		Description: req.Description,
		IncludeGit:  includeGit,
		Next:        req.Next,
		Blockers:    req.Blockers,
		Decisions:   req.Decisions,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotHandoffError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	SetAuditResource(r, "handoff", session)
	SetAuditAction(r, AuditActionCreate)
	SetAuditDetails(r, fmt.Sprintf("created handoff for session %s (%s)", session, out.CreatedPath))

	resp := toServedHandoffsEnvelope(out)
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}
