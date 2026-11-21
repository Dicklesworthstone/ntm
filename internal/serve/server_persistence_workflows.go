package serve

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// SavedSessionRestoreRequest is the JSON request body for POST /api/v1/saved-sessions/{name}/restore.
type SavedSessionRestoreRequest struct {
	TargetName string `json:"target_name,omitempty"`
	Force      bool   `json:"force,omitempty"`
	Mode       string `json:"mode,omitempty"` // topology, launch, or resume
	// Launch retains the legacy meaning: resume agents. New clients use Mode.
	Launch bool `json:"launch,omitempty"`
	Native bool `json:"native,omitempty"`
}

// SavedSessionRestoreEnvelopeResponse is the REST JSON envelope for POST /api/v1/saved-sessions/{name}/restore.
type SavedSessionRestoreEnvelopeResponse struct {
	APIResponse
	Action  string                `json:"action"`
	Name    string                `json:"name"`
	Session string                `json:"session"`
	Launch  bool                  `json:"launch"`
	Mode    string                `json:"mode"`
	State   *session.SessionState `json:"state,omitempty"`
	Resume  *session.ResumeResult `json:"resume,omitempty"`
}

// WorkflowRunRequest is the JSON request body for POST /api/v1/workflows/{name}/run.
type WorkflowRunRequest struct {
	Session    string            `json:"session"`
	ProjectDir string            `json:"project_dir,omitempty"`
	Variables  map[string]string `json:"variables,omitempty"`
	DryRun     bool              `json:"dry_run,omitempty"`
}

// WorkflowTriggerRequest is the JSON request body for POST /api/v1/workflows/{name}/trigger.
type WorkflowTriggerRequest struct {
	Token      string `json:"token"`
	Transition int    `json:"transition"`
	Reason     string `json:"reason"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// WorkflowStopRequest is the JSON request body for POST /api/v1/workflows/{name}/stop.
type WorkflowStopRequest struct {
	Reason     string `json:"reason,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// WorkflowControlEnvelopeResponse is the REST JSON envelope for POST /api/v1/workflows/{name}/(run|trigger|stop).
type WorkflowControlEnvelopeResponse struct {
	APIResponse
	Action          string                     `json:"action"`
	Workflow        string                     `json:"workflow,omitempty"`
	Session         string                     `json:"session,omitempty"`
	DryRun          bool                       `json:"dry_run,omitempty"`
	Stage           string                     `json:"stage,omitempty"`
	State           *workflow.WorkflowState    `json:"state,omitempty"`
	CheckpointToken string                     `json:"checkpoint_token,omitempty"`
	ManualGate      *workflow.ManualGateStatus `json:"manual_gate,omitempty"`
	Decision        *workflow.ManualDecision   `json:"decision,omitempty"`
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
	// Saved sessions (list, capture, inspect, archive, unarchive, restore)
	r.With(s.RequirePermission(PermReadSessions)).Get("/saved-sessions", s.handleSavedSessionsListV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions", s.handleSavedSessionsSaveV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/saved-sessions/{name}", s.handleSavedSessionsShowV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions/{name}/archive", s.handleSavedSessionsArchiveV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions/{name}/unarchive", s.handleSavedSessionsUnarchiveV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/saved-sessions/{name}/restore", s.handleSavedSessionsRestoreV1)

	// Session Agent Mail <-> tmux pane mapping & border badge reconciliation
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/mapping", s.handleSessionMappingV1)

	// Multi-agent workflow coordination templates & runtime controls
	r.With(s.RequirePermission(PermReadSessions)).Get("/workflows", s.handleWorkflowsListV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/workflows/{name}", s.handleWorkflowsShowV1)
	r.With(s.RequirePermission(PermWritePipelines)).Post("/workflows/{name}/run", s.handleWorkflowsRunV1)
	r.With(s.RequirePermission(PermWritePipelines)).Post("/workflows/{name}/trigger", s.handleWorkflowsTriggerV1)
	r.With(s.RequirePermission(PermWritePipelines)).Post("/workflows/{name}/stop", s.handleWorkflowsStopV1)

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

func workflowStateStoreForProject(projectDir string) *workflow.StateStore {
	return &workflow.StateStore{Dir: filepath.Join(projectDir, ".ntm", "workflows", "state")}
}

// handleWorkflowsRunV1 handles POST /api/v1/workflows/{name}/run.
func (s *Server) handleWorkflowsRunV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	name, err := validateSafeWorktreeIdentifier("workflow", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req WorkflowRunRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	sessName, err := validateSafeWorktreeIdentifier("session", req.Session)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}

	loader := workflow.NewLoader()
	loader.ProjectDir = projectDir
	tmpl, err := loader.Get(name)
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not found") {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}

	vars := make(map[string]string, len(req.Variables))
	for k, v := range req.Variables {
		vars[k] = v
	}
	for _, prompt := range tmpl.Prompts {
		if prompt.Required && strings.TrimSpace(vars[prompt.Key]) == "" {
			if strings.TrimSpace(prompt.Default) != "" {
				vars[prompt.Key] = prompt.Default
			} else {
				writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "missing required workflow variable "+prompt.Key, nil, reqID)
				return
			}
		}
	}

	initialStage := "initial"
	if tmpl.Flow != nil {
		if strings.TrimSpace(tmpl.Flow.Initial) != "" {
			initialStage = tmpl.Flow.Initial
		} else if len(tmpl.Flow.Stages) > 0 {
			initialStage = tmpl.Flow.Stages[0]
		}
	}

	templateHash, err := tmpl.Fingerprint()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	now := time.Now().UTC()
	state := &workflow.WorkflowState{
		WorkflowName:   tmpl.Name,
		SessionName:    sessName,
		CurrentStage:   initialStage,
		StageStartedAt: now,
		Agents:         map[string]string{},
		Variables:      vars,
		StageHistory:   []workflow.StageRecord{},
		Errors:         []workflow.WorkflowError{},
		ResumeVersion:  1,
		TemplateHash:   templateHash,
		ProjectRoot:    projectDir,
		Turn:           1,
		Dispatches:     []workflow.StageDispatch{},
	}
	if err := state.RefreshManualGate(tmpl); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:   "run",
		Workflow: tmpl.Name,
		Session:  sessName,
		DryRun:   req.DryRun,
		Stage:    initialStage,
		State:    state,
	}

	if !req.DryRun {
		store := workflowStateStoreForProject(projectDir)
		unlock, err := store.Acquire(r.Context(), sessName)
		if err != nil {
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
			return
		}
		defer unlock()
		if err := store.Save(state); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
			return
		}
		if tok, err := state.CheckpointToken(); err == nil {
			resp.CheckpointToken = tok
		}
		if gate, err := store.ManualStatus(state); err == nil {
			resp.ManualGate = gate
		}
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow run response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleWorkflowsTriggerV1 handles POST /api/v1/workflows/{name}/trigger where {name} is the workflow session.
func (s *Server) handleWorkflowsTriggerV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req WorkflowTriggerRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "token is required", nil, reqID)
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "reason is required", nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}

	store := workflowStateStoreForProject(projectDir)
	decision, err := store.RequestManualTransition(r.Context(), sessName, req.Token, req.Transition, req.Reason)
	if err != nil {
		lower := strings.ToLower(err.Error())
		switch {
		case strings.Contains(lower, "no workflow checkpoint") || strings.Contains(lower, "not found"):
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil, reqID)
		case strings.Contains(lower, "conflict") || strings.Contains(lower, "already recorded") || strings.Contains(lower, "token") || strings.Contains(lower, "manual gate"):
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		default:
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		}
		return
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:   "trigger",
		Session:  sessName,
		Stage:    decision.Stage,
		Decision: decision,
	}
	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow trigger response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleWorkflowsStopV1 handles POST /api/v1/workflows/{name}/stop where {name} is the workflow session.
func (s *Server) handleWorkflowsStopV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServePersistenceBodyBytes)
	var req WorkflowStopRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}

	store := workflowStateStoreForProject(projectDir)
	unlock, err := store.Acquire(r.Context(), sessName)
	if err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}
	defer unlock()

	state, err := store.Load(sessName)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if state == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "no workflow checkpoint found for session "+sessName, nil, reqID)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "stopped via REST API"
	}
	if err := store.Pause(state, reason, time.Now().UTC()); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:   "stop",
		Workflow: state.WorkflowName,
		Session:  sessName,
		Stage:    state.CurrentStage,
		State:    state,
	}
	if tok, err := state.CheckpointToken(); err == nil {
		resp.CheckpointToken = tok
	}
	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow stop response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
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
