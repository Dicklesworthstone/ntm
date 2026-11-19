package serve

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
)

const maxServePersistenceBodyBytes = 1 << 20 // 1 MiB

// SavedSessionSaveRequest is the JSON request body for POST /api/v1/saved-sessions.
type SavedSessionSaveRequest struct {
	Session   string `json:"session"`
	Name      string `json:"name,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

// SavedSessionsEnvelopeResponse is the REST JSON envelope for /api/v1/saved-sessions*.
type SavedSessionsEnvelopeResponse struct {
	APIResponse
	SchemaID       string                 `json:"schema_id"`
	SchemaVersion  string                 `json:"schema_version"`
	Action         string                 `json:"action"`
	Archived       bool                   `json:"archived,omitempty"`
	Sessions       []session.SavedSession `json:"sessions,omitempty"`
	Count          *int                   `json:"count,omitempty"`
	Session        string                 `json:"session,omitempty"`
	SavedAs        string                 `json:"saved_as,omitempty"`
	FilePath       string                 `json:"file_path,omitempty"`
	State          *session.SessionState  `json:"state,omitempty"`
	ResumablePanes *int                   `json:"resumable_panes,omitempty"`
	AgentHints     *robot.AgentHints      `json:"_agent_hints,omitempty"`
}

// SessionMappingEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/mapping.
type SessionMappingEnvelopeResponse struct {
	APIResponse
	SchemaID      string                      `json:"schema_id"`
	SchemaVersion string                      `json:"schema_version"`
	Session       string                      `json:"session"`
	Count         int                         `json:"count"`
	Entries       []robot.AgentMappingEntry   `json:"entries"`
	Discrepancies []agentmail.PaneBadgeRecord `json:"discrepancies"`
	Badges        *robot.AgentMappingBadges   `json:"badges,omitempty"`
	ReconcileErr  string                      `json:"reconcile_error,omitempty"`
	AgentHints    *robot.AgentHints           `json:"_agent_hints,omitempty"`
}

// WorkflowsEnvelopeResponse is the REST JSON envelope for GET /api/v1/workflows and GET /api/v1/workflows/{name}.
type WorkflowsEnvelopeResponse struct {
	APIResponse
	SchemaID      string                    `json:"schema_id"`
	SchemaVersion string                    `json:"schema_version"`
	Action        string                    `json:"action"`
	ProjectDir    string                    `json:"project_dir,omitempty"`
	FilterRole    string                    `json:"filter_role,omitempty"`
	Workflows     []robot.WorkflowInfo      `json:"workflows,omitempty"`
	Total         *int                      `json:"total,omitempty"`
	Workflow      *robot.WorkflowShowResult `json:"workflow,omitempty"`
	AgentHints    *robot.AgentHints         `json:"_agent_hints,omitempty"`
}

// SequenceCreateRequest is the JSON request body for POST /api/v1/sequences.
type SequenceCreateRequest struct {
	Name       string   `json:"name"`
	Steps      []string `json:"steps"`
	ProjectDir string   `json:"project_dir,omitempty"`
}

// SequenceAdvanceRequest is the JSON request body for POST /api/v1/sequences/{name}/advance.
type SequenceAdvanceRequest struct {
	Pane             string `json:"pane"`
	ExpectedPosition *int   `json:"expected_position,omitempty"`
	ProjectDir       string `json:"project_dir,omitempty"`
}

// SequenceEnvelopeResponse is the REST JSON envelope for /api/v1/sequences*.
type SequenceEnvelopeResponse struct {
	APIResponse
	Action   string                         `json:"action"`
	Sequence *workflow.PaneSequence         `json:"sequence,omitempty"`
	Pane     *workflow.PaneSequencePosition `json:"pane,omitempty"`
}

// SessionTemplatesEnvelopeResponse is the REST JSON envelope for GET /api/v1/session-templates and GET /api/v1/session-templates/{name}.
type SessionTemplatesEnvelopeResponse struct {
	APIResponse
	SchemaID      string                           `json:"schema_id"`
	SchemaVersion string                           `json:"schema_version"`
	Action        string                           `json:"action"`
	ProjectDir    string                           `json:"project_dir,omitempty"`
	Templates     []robot.SessionTemplateInfo      `json:"templates,omitempty"`
	Total         *int                             `json:"total,omitempty"`
	Template      *robot.SessionTemplateShowResult `json:"template,omitempty"`
	Suggestions   []string                         `json:"suggestions,omitempty"`
	AgentHints    *robot.AgentHints                `json:"_agent_hints,omitempty"`
}

func (s *Server) registerPersistenceWorkflowRoutes(r chi.Router) {
	// Saved sessions (non-destructive list, capture, inspect, archive, unarchive)
	r.With(s.RequirePermission(PermReadSessions)).Get("/saved-sessions", s.handleSavedSessionsListV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions", s.handleSavedSessionsSaveV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/saved-sessions/{name}", s.handleSavedSessionsShowV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions/{name}/archive", s.handleSavedSessionsArchiveV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions/{name}/unarchive", s.handleSavedSessionsUnarchiveV1)

	// Session Agent Mail <-> tmux pane mapping & border badge reconciliation
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/mapping", s.handleSessionMappingV1)

	// Multi-agent workflow coordination templates
	r.With(s.RequirePermission(PermReadSessions)).Get("/workflows", s.handleWorkflowsListV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/workflows/{name}", s.handleWorkflowsShowV1)

	// Durable per-pane prompt sequences
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sequences", s.handleSequencesCreateV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sequences/{name}/next", s.handleSequencesNextV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sequences/{name}/advance", s.handleSequencesAdvanceV1)

	// Multi-agent session templates
	r.With(s.RequirePermission(PermReadSessions)).Get("/session-templates", s.handleSessionTemplatesListV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/session-templates/{name}", s.handleSessionTemplatesShowV1)
}

func (s *Server) resolvePersistenceProjectDir(w http.ResponseWriter, r *http.Request, bodyDir, reqID string) (string, bool) {
	rawDir := strings.TrimSpace(bodyDir)
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project_dir"))
	}
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	return s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
}

func writeRobotEnvelopeOrError(w http.ResponseWriter, out any, resp robot.RobotResponse, label, reqID string) {
	if !resp.Success {
		statusCode := robotErrorHTTPStatus(resp.ErrorCode)
		errCode := resp.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, resp.Error, nil, reqID)
		return
	}
	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize "+label+" response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSavedSessionsListV1 handles GET /api/v1/saved-sessions.
func (s *Server) handleSavedSessionsListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	archived := false
	if raw := strings.TrimSpace(firstQueryParam(r, "archived")); strings.Compare(raw, "") != 0 {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "archived must be a boolean", nil, reqID)
			return
		}
		archived = parsed
	}
	out, err := robot.GetSavedSessions(robot.SavedSessionsOptions{
		Action:   "list",
		Archived: archived,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "saved-sessions list", reqID)
}

// handleSavedSessionsSaveV1 handles POST /api/v1/saved-sessions.
func (s *Server) handleSavedSessionsSaveV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req SavedSessionSaveRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	sessName, err := validateSafeWorktreeIdentifier("session", req.Session)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	customName := strings.TrimSpace(req.Name)
	if strings.Compare(customName, "") != 0 {
		validName, err := validateSafeWorktreeIdentifier("name", customName)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
			return
		}
		customName = validName
	}

	out, err := robot.GetSavedSessions(robot.SavedSessionsOptions{
		Action:    "save",
		Session:   sessName,
		Name:      customName,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "saved-sessions save", reqID)
}

// handleSavedSessionsShowV1 handles GET /api/v1/saved-sessions/{name}.
func (s *Server) handleSavedSessionsShowV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("name", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	out, err := robot.GetSavedSessions(robot.SavedSessionsOptions{
		Action: "show",
		Name:   name,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "saved-sessions show", reqID)
}

// handleSavedSessionsArchiveV1 handles POST /api/v1/saved-sessions/{name}/archive.
func (s *Server) handleSavedSessionsArchiveV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("name", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	out, err := robot.GetSavedSessions(robot.SavedSessionsOptions{
		Action: "archive",
		Name:   name,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "saved-sessions archive", reqID)
}

// handleSavedSessionsUnarchiveV1 handles POST /api/v1/saved-sessions/{name}/unarchive.
func (s *Server) handleSavedSessionsUnarchiveV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("name", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	out, err := robot.GetSavedSessions(robot.SavedSessionsOptions{
		Action: "unarchive",
		Name:   name,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "saved-sessions unarchive", reqID)
}

// handleSessionMappingV1 handles GET /api/v1/sessions/{id}/mapping.
func (s *Server) handleSessionMappingV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.GetAgentMapping(r.Context(), robot.AgentMappingOptions{
		Session:        sessionID,
		ProjectDir:     projectDir,
		Config:         s.loadEnsembleConfig(projectDir),
		RequireSession: true,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session mapping", reqID)
}

// handleWorkflowsListV1 handles GET /api/v1/workflows.
func (s *Server) handleWorkflowsListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.GetWorkflows(robot.WorkflowsOptions{
		ProjectDir: projectDir,
		Role:       strings.TrimSpace(firstQueryParam(r, "role")),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "workflows list", reqID)
}

// handleWorkflowsShowV1 handles GET /api/v1/workflows/{name}.
func (s *Server) handleWorkflowsShowV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("workflow", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.GetWorkflows(robot.WorkflowsOptions{
		ProjectDir: projectDir,
		Name:       name,
		Role:       strings.TrimSpace(firstQueryParam(r, "role")),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "workflow show", reqID)
}

func writeSequenceError(w http.ResponseWriter, err error, reqID string) {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not found"):
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, msg, nil, reqID)
	case strings.Contains(lower, "before expected position"):
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, msg, nil, reqID)
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, msg, nil, reqID)
	}
}

// handleSequencesCreateV1 handles POST /api/v1/sequences.
func (s *Server) handleSequencesCreateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req SequenceCreateRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	name, err := validateSafeWorktreeIdentifier("sequence", req.Name)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	if len(req.Steps) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "steps must contain at least one prompt", nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}
	out, err := robot.ExecuteSequence(robot.SequenceOptions{
		ProjectDir: projectDir,
		Name:       name,
		Action:     "create",
		Steps:      req.Steps,
	})
	if err != nil {
		writeSequenceError(w, err, reqID)
		return
	}
	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize sequence create response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSequencesNextV1 handles GET /api/v1/sequences/{name}/next.
func (s *Server) handleSequencesNextV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("sequence", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	pane := strings.TrimSpace(firstQueryParam(r, "pane"))
	if strings.Compare(pane, "") == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pane query parameter is required", nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.ExecuteSequence(robot.SequenceOptions{
		ProjectDir: projectDir,
		Name:       name,
		Action:     "next",
		Pane:       pane,
	})
	if err != nil {
		writeSequenceError(w, err, reqID)
		return
	}
	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize sequence next response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSequencesAdvanceV1 handles POST /api/v1/sequences/{name}/advance.
func (s *Server) handleSequencesAdvanceV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("sequence", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req SequenceAdvanceRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	pane := strings.TrimSpace(req.Pane)
	if strings.Compare(pane, "") == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pane is required", nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}
	out, err := robot.ExecuteSequence(robot.SequenceOptions{
		ProjectDir:       projectDir,
		Name:             name,
		Action:           "advance",
		Pane:             pane,
		ExpectedPosition: req.ExpectedPosition,
	})
	if err != nil {
		writeSequenceError(w, err, reqID)
		return
	}
	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize sequence advance response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionTemplatesListV1 handles GET /api/v1/session-templates.
func (s *Server) handleSessionTemplatesListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.GetSessionTemplates(robot.SessionTemplatesOptions{
		ProjectDir: projectDir,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session-templates list", reqID)
}

// handleSessionTemplatesShowV1 handles GET /api/v1/session-templates/{name}.
func (s *Server) handleSessionTemplatesShowV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("template", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	out, err := robot.GetSessionTemplates(robot.SessionTemplatesOptions{
		ProjectDir: projectDir,
		Name:       name,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session-template show", reqID)
}
