package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/workflow"
	"github.com/Dicklesworthstone/ntm/internal/workflowrun"
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
	Session        string            `json:"session"`
	ProjectDir     string            `json:"project_dir,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	DryRun         bool              `json:"dry_run,omitempty"`
	Resume         bool              `json:"resume,omitempty"`
	Restart        bool              `json:"restart,omitempty"`
	FireManual     bool              `json:"fire_manual,omitempty"`
	MaxTransitions int               `json:"max_transitions,omitempty"`
	Interval       string            `json:"interval,omitempty"`        // Positive Go duration, e.g. "2s".
	Timeout        string            `json:"timeout,omitempty"`         // Positive Go duration, e.g. "15m".
	TriggerTimeout string            `json:"trigger_timeout,omitempty"` // Positive Go duration, e.g. "30s".
}

type jobWorkflowRunParams struct {
	Workflow string `json:"workflow"`
	WorkflowRunRequest
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

// WorkflowControlEnvelopeResponse is the REST JSON envelope for /api/v1/workflows/{name}/(run|status|trigger|stop).
type WorkflowControlEnvelopeResponse struct {
	APIResponse
	Action                string                     `json:"action"`
	Workflow              string                     `json:"workflow,omitempty"`
	Session               string                     `json:"session,omitempty"`
	ProjectDir            string                     `json:"project_dir,omitempty"`
	JobID                 string                     `json:"job_id,omitempty"`
	JobStatus             JobStatus                  `json:"job_status,omitempty"`
	Running               bool                       `json:"running,omitempty"`
	StoppedJob            bool                       `json:"stopped_job,omitempty"`
	CancellationRequested bool                       `json:"cancellation_requested,omitempty"`
	Job                   *Job                       `json:"job,omitempty"`
	DryRun                bool                       `json:"dry_run,omitempty"`
	Stage                 string                     `json:"stage,omitempty"`
	Paused                bool                       `json:"paused,omitempty"`
	Completed             bool                       `json:"completed,omitempty"`
	TmuxEndpoint          string                     `json:"tmux_endpoint,omitempty"`
	Dispatches            []workflow.StageDispatch   `json:"dispatches,omitempty"`
	Agents                []workflowrun.RunAgent     `json:"agents,omitempty"`
	State                 *workflow.WorkflowState    `json:"state,omitempty"`
	CheckpointToken       string                     `json:"checkpoint_token,omitempty"`
	ManualGate            *workflow.ManualGateStatus `json:"manual_gate,omitempty"`
	Decision              *workflow.ManualDecision   `json:"decision,omitempty"`
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
	r.With(s.RequirePermission(PermReadSessions)).Get("/workflows/{name}/status", s.handleWorkflowsStatusV1)
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

type workflowProjectScopeKey struct{}

type workflowProjectScope struct {
	configuredDir string
	projectDir    string
	err           error
}

// workflowProjectScopeMiddleware pins the default project directory for
// /api/v1/workflows/* requests before Idempotency-Key lookup so PATCH /config
// cannot cross-replay a cached response against a different project.
func (s *Server) workflowProjectScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflows" && !strings.HasPrefix(r.URL.Path, "/api/v1/workflows/") {
			next.ServeHTTP(w, r)
			return
		}
		scope := workflowProjectScope{configuredDir: s.projectDirSnapshot()}
		root := strings.TrimSpace(scope.configuredDir)
		if root == "" {
			root, scope.err = os.Getwd()
		}
		if scope.err == nil && strings.TrimSpace(root) != "" {
			scope.projectDir, scope.err = ResolveProjectDir(root)
		}
		ctx := context.WithValue(r.Context(), workflowProjectScopeKey{}, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) resolvePersistenceProjectDir(w http.ResponseWriter, r *http.Request, bodyDir, reqID string) (string, bool) {
	rawDir := strings.TrimSpace(bodyDir)
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project_dir"))
	}
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	if rawDir == "" {
		if scope, ok := r.Context().Value(workflowProjectScopeKey{}).(workflowProjectScope); ok {
			if scope.err != nil {
				writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail,
					"workflow project directory is unavailable: "+scope.err.Error(), nil, reqID)
				return "", false
			}
			if scope.projectDir != "" {
				return scope.projectDir, true
			}
		}
		if snap := strings.TrimSpace(s.projectDirSnapshot()); snap != "" {
			resolved, err := ResolveProjectDir(snap)
			if err != nil {
				writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail,
					"configured project directory is unavailable: "+err.Error(), nil, reqID)
				return "", false
			}
			return resolved, true
		}
	}
	dir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return "", false
	}
	if resolved, err := ResolveProjectDir(dir); err == nil && resolved != "" {
		return resolved, true
	}
	return dir, true
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

type workflowRunKey struct {
	projectDir string
	session    string
}

type activeWorkflowRunKey struct{}

type activeWorkflowRun struct {
	jobID      string
	workflow   string
	projectDir string
	session    string
	done       chan struct{}
	doneOnce   sync.Once
	stopMu     sync.Mutex
	stopReason string
}

func (a *activeWorkflowRun) setStopReason(reason string) {
	if a == nil {
		return
	}
	a.stopMu.Lock()
	if a.stopReason == "" {
		a.stopReason = reason
	}
	a.stopMu.Unlock()
}

func (a *activeWorkflowRun) getStopReason() string {
	if a == nil {
		return ""
	}
	a.stopMu.Lock()
	defer a.stopMu.Unlock()
	return a.stopReason
}

func (a *activeWorkflowRun) closeDone() {
	if a == nil {
		return
	}
	a.doneOnce.Do(func() {
		close(a.done)
	})
}

type workflowAlreadyRunningError struct {
	jobID    string
	workflow string
	session  string
}

func (e *workflowAlreadyRunningError) Error() string {
	if e.workflow != "" {
		return fmt.Sprintf("workflow %q is already active for session %q (job %s)", e.workflow, e.session, e.jobID)
	}
	return fmt.Sprintf("a workflow is already active for session %q (job %s)", e.session, e.jobID)
}

func (s *Server) reserveWorkflowRun(workflowName, projectDir, session string, rejectDuplicate bool) (*activeWorkflowRun, error) {
	key := workflowRunKey{projectDir: projectDir, session: session}
	s.workflowRunsMu.Lock()
	defer s.workflowRunsMu.Unlock()
	if rejectDuplicate && len(s.workflowRuns[key]) > 0 {
		existing := s.workflowRuns[key][0]
		return nil, &workflowAlreadyRunningError{
			jobID:    existing.jobID,
			workflow: existing.workflow,
			session:  session,
		}
	}
	if s.workflowRuns == nil {
		s.workflowRuns = make(map[workflowRunKey][]*activeWorkflowRun)
	}
	active := &activeWorkflowRun{
		workflow:   workflowName,
		projectDir: projectDir,
		session:    session,
		done:       make(chan struct{}),
	}
	s.workflowRuns[key] = append(s.workflowRuns[key], active)
	return active, nil
}

func (s *Server) bindWorkflowRunJob(active *activeWorkflowRun, jobID string) {
	if active == nil || jobID == "" {
		return
	}
	key := workflowRunKey{projectDir: active.projectDir, session: active.session}
	s.workflowRunsMu.Lock()
	active.jobID = jobID
	if s.lastWorkflowJob == nil {
		s.lastWorkflowJob = make(map[workflowRunKey]string)
	}
	if len(s.lastWorkflowJob) >= maxRetainedJobs {
		for oldKey := range s.lastWorkflowJob {
			if len(s.workflowRuns[oldKey]) == 0 {
				delete(s.lastWorkflowJob, oldKey)
				break
			}
		}
	}
	s.lastWorkflowJob[key] = jobID
	s.workflowRunsMu.Unlock()
}

func (s *Server) cancelWorkflowReservation(active *activeWorkflowRun) {
	if active == nil {
		return
	}
	key := workflowRunKey{projectDir: active.projectDir, session: active.session}
	s.workflowRunsMu.Lock()
	if list := s.workflowRuns[key]; len(list) > 0 {
		kept := list[:0]
		for _, item := range list {
			if item == active {
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(s.workflowRuns, key)
		} else {
			s.workflowRuns[key] = kept
		}
	}
	s.workflowRunsMu.Unlock()
	active.closeDone()
}

func (s *Server) finishWorkflowRunByJobID(jobID string) {
	if jobID == "" {
		return
	}
	var finished []*activeWorkflowRun
	s.workflowRunsMu.Lock()
	for key, list := range s.workflowRuns {
		kept := list[:0]
		for _, item := range list {
			if item.jobID == jobID {
				finished = append(finished, item)
				if s.lastWorkflowJob == nil {
					s.lastWorkflowJob = make(map[workflowRunKey]string)
				}
				s.lastWorkflowJob[key] = jobID
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(s.workflowRuns, key)
		} else {
			s.workflowRuns[key] = kept
		}
	}
	s.workflowRunsMu.Unlock()
	for _, item := range finished {
		item.closeDone()
	}
}

func (s *Server) activeWorkflowRuns(projectDir, session string) []*activeWorkflowRun {
	key := workflowRunKey{projectDir: projectDir, session: session}
	s.workflowRunsMu.Lock()
	defer s.workflowRunsMu.Unlock()
	list := s.workflowRuns[key]
	if len(list) == 0 {
		return nil
	}
	return append([]*activeWorkflowRun(nil), list...)
}

func (s *Server) workflowRunStatusSnapshot(projectDir, session string) (*activeWorkflowRun, *Job) {
	key := workflowRunKey{projectDir: projectDir, session: session}
	s.workflowRunsMu.Lock()
	var active *activeWorkflowRun
	if list := s.workflowRuns[key]; len(list) > 0 {
		active = list[0]
	}
	jobID := ""
	if active != nil {
		jobID = active.jobID
	} else {
		jobID = s.lastWorkflowJob[key]
	}
	s.workflowRunsMu.Unlock()
	var job *Job
	if jobID != "" && s.jobStore != nil {
		job = s.jobStore.Get(jobID)
	}
	return active, job
}

func workflowStateStoreForProject(projectDir string) *workflow.StateStore {
	return &workflow.StateStore{Dir: filepath.Join(projectDir, ".ntm", "workflows", "state")}
}

func (s *Server) loadWorkflowConfig(projectDir string) (*config.Config, error) {
	if s.recoveryConfig != nil {
		cfg, err := s.recoveryConfig(projectDir)
		if err != nil {
			return nil, fmt.Errorf("load workflow policy: %w", err)
		}
		if cfg == nil {
			return nil, errors.New("workflow policy loader returned no configuration")
		}
		return cfg, nil
	}
	return s.loadEnsembleConfig(projectDir), nil
}

type parsedWorkflowDurations struct {
	interval       time.Duration
	timeout        time.Duration
	triggerTimeout time.Duration
}

func parseWorkflowRunControls(req WorkflowRunRequest) (parsedWorkflowDurations, error) {
	if req.Resume && req.Restart {
		return parsedWorkflowDurations{}, errors.New("resume and restart are mutually exclusive")
	}
	if req.MaxTransitions < 0 {
		return parsedWorkflowDurations{}, errors.New("max_transitions must be zero or positive")
	}
	var out parsedWorkflowDurations
	if raw := strings.TrimSpace(req.Interval); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return parsedWorkflowDurations{}, errors.New("interval must be a positive duration such as 2s")
		}
		out.interval = d
	}
	if raw := strings.TrimSpace(req.Timeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return parsedWorkflowDurations{}, errors.New("timeout must be a positive duration such as 15m")
		}
		out.timeout = d
	}
	if raw := strings.TrimSpace(req.TriggerTimeout); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return parsedWorkflowDurations{}, errors.New("trigger_timeout must be a positive duration such as 30s")
		}
		out.triggerTimeout = d
	}
	return out, nil
}

func workflowInitialStage(tmpl *workflow.WorkflowTemplate) string {
	if tmpl == nil {
		return ""
	}
	if tmpl.Flow != nil {
		if strings.TrimSpace(tmpl.Flow.Initial) != "" {
			return tmpl.Flow.Initial
		}
		if len(tmpl.Flow.Stages) > 0 {
			return tmpl.Flow.Stages[0]
		}
	}
	if tmpl.Coordination == workflow.CoordParallel {
		return workflowrun.StageLabel("")
	}
	return "initial"
}

func workflowAgentsFromCoord(agents []workflow.CoordinatorAgent) []workflowrun.RunAgent {
	out := make([]workflowrun.RunAgent, 0, len(agents))
	for _, agent := range agents {
		out = append(out, workflowrun.RunAgent{Role: agent.Role, Pane: agent.ID})
	}
	return out
}

func workflowAgentsFromState(state *workflow.WorkflowState) []workflowrun.RunAgent {
	if state == nil || len(state.Agents) == 0 {
		return nil
	}
	panes := make([]string, 0, len(state.Agents))
	for pane := range state.Agents {
		panes = append(panes, pane)
	}
	sort.Strings(panes)
	out := make([]workflowrun.RunAgent, 0, len(panes))
	for _, pane := range panes {
		out = append(out, workflowrun.RunAgent{Role: state.Agents[pane], Pane: pane})
	}
	return out
}

func populateWorkflowCheckpointFields(resp *WorkflowControlEnvelopeResponse, store *workflow.StateStore, state *workflow.WorkflowState) {
	if resp == nil || state == nil {
		return
	}
	resp.Workflow = state.WorkflowName
	resp.Stage = state.CurrentStage
	resp.Paused = state.Paused
	resp.Completed = state.Completed
	resp.TmuxEndpoint = state.TmuxEndpoint
	if len(state.Dispatches) > 0 {
		resp.Dispatches = append([]workflow.StageDispatch(nil), state.Dispatches...)
	}
	if len(resp.Agents) == 0 {
		resp.Agents = workflowAgentsFromState(state)
	}
	resp.State = state
	if tok, err := state.CheckpointToken(); err == nil {
		resp.CheckpointToken = tok
	}
	if store != nil {
		if gate, err := store.ManualStatus(state); err == nil {
			resp.ManualGate = gate
		}
	}
}

func isMissingTmuxSessionErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "can't find session") ||
		strings.Contains(lower, "session not found") ||
		strings.Contains(lower, "no such session") ||
		strings.Contains(lower, "no current target") ||
		strings.Contains(lower, "no server running") ||
		(strings.Contains(lower, "error connecting to") && strings.Contains(lower, "no such file or directory"))
}

// jobWorkflowRun executes a managed workflow job through the shared production
// coordinator runner (`internal/workflowrun`), honoring the admitted canonical
// project directory, exact session identity, and true context cancellation.
func (s *Server) jobWorkflowRun(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var req jobWorkflowRunParams
	if err := decodeJobParams(params, &req); err != nil {
		return nil, err
	}
	wfName, err := validateSafeWorktreeIdentifier("workflow", req.Workflow)
	if err != nil {
		return nil, err
	}
	sessName, err := validateSafeWorktreeIdentifier("session", req.Session)
	if err != nil {
		return nil, err
	}
	if err := tmux.ValidateSessionName(sessName); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	durations, err := parseWorkflowRunControls(req.WorkflowRunRequest)
	if err != nil {
		return nil, err
	}
	projectDir := s.projectDirSnapshot()
	if strings.TrimSpace(projectDir) == "" {
		return nil, errors.New("workflow job requires an admitted project directory")
	}
	if rawDir := strings.TrimSpace(req.ProjectDir); rawDir != "" {
		resolved, err := ResolveProjectDir(rawDir)
		if err != nil {
			return nil, err
		}
		if resolved != projectDir {
			return nil, fmt.Errorf("workflow project_dir %q differs from admitted project %q", resolved, projectDir)
		}
	}

	endpoint := workflowrun.TmuxEndpoint()
	tmpl, err := workflowrun.ResolveTemplateByName(projectDir, wfName)
	if err != nil {
		return nil, err
	}
	if err := workflowrun.ValidateEndpoint(tmpl, endpoint); err != nil {
		return nil, err
	}
	vars, err := workflowrun.ResolveVarsMap(tmpl, req.Variables, req.Resume)
	if err != nil {
		return nil, err
	}
	if req.DryRun {
		return map[string]interface{}{
			"workflow":      tmpl.Name,
			"session":       sessName,
			"project_dir":   projectDir,
			"tmux_endpoint": endpoint,
			"dry_run":       true,
			"status":        "validated",
			"stage":         workflowInitialStage(tmpl),
		}, nil
	}

	wfCfg, err := s.loadWorkflowConfig(projectDir)
	if err != nil {
		return nil, err
	}
	agents, panePIDs, paneTypes, err := workflowrun.BindSessionPanes(ctx, tmpl, sessName)
	if err != nil {
		return nil, err
	}
	runAgents := workflowAgentsFromCoord(agents)
	store := workflowStateStoreForProject(projectDir)

	if err := reportJobProgress(ctx, map[string]interface{}{
		"workflow":      tmpl.Name,
		"session":       sessName,
		"project_dir":   projectDir,
		"tmux_endpoint": endpoint,
		"agents":        runAgents,
		"stage":         workflowInitialStage(tmpl),
	}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resilienceCfg := config.Default().Resilience
	if wfCfg != nil {
		resilienceCfg = wfCfg.Resilience
	}
	opts := workflowrun.Options{
		Session:          sessName,
		ProjectRoot:      projectDir,
		TmuxEndpoint:     endpoint,
		Vars:             vars,
		PanePIDs:         panePIDs,
		PaneTypes:        paneTypes,
		MaxAgentRestarts: resilienceCfg.MaxRestarts,
		AgentFaultPolls:  resilienceCfg.CrashThreshold,
		MaxTransitions:   req.MaxTransitions,
		Interval:         durations.interval,
		FireManual:       req.FireManual,
		TriggerTimeout:   durations.triggerTimeout,
		StateDir:         store.Dir,
		Resume:           req.Resume,
		Restart:          req.Restart,
	}

	runner, err := workflowrun.NewProductionRunner(tmpl, agents, opts, workflowrun.ProductionConfig{
		Config: wfCfg,
		Notify: func(format string, args ...any) {
			slog.Info("workflow run", "workflow", tmpl.Name, "session", sessName, "message", fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		return nil, err
	}

	runCtx := ctx
	var cancelTimeout context.CancelFunc
	if durations.timeout > 0 {
		runCtx, cancelTimeout = context.WithTimeoutCause(ctx, durations.timeout, fmt.Errorf("workflow run timeout %s elapsed", durations.timeout))
		defer cancelTimeout()
	}

	result, runErr := runner.Run(runCtx)

	runner.Mu.Lock()
	ownedCheckpoint := runner.State != nil
	runner.Mu.Unlock()

	// When the managed job's parent context was cancelled (via /workflows/{session}/stop,
	// DELETE /api/v1/jobs/{id}, or server shutdown) or timed out, durably pause the
	// checkpoint only if this runner actually owned it, so a rejected/unowned job can
	// never mutate or leak another run's checkpoint.
	if ctx.Err() != nil {
		if ownedCheckpoint {
			pauseReason := "cancelled via jobs API"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				pauseReason = "job execution timeout elapsed"
			}
			if active, ok := ctx.Value(activeWorkflowRunKey{}).(*activeWorkflowRun); ok {
				if custom := strings.TrimSpace(active.getStopReason()); custom != "" {
					pauseReason = custom
				}
			}
			pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if unlock, lockErr := store.Acquire(pauseCtx, sessName); lockErr == nil {
				if st, loadErr := store.Load(sessName); loadErr == nil && st != nil && !st.Completed && !st.Paused {
					if pauseErr := store.Pause(st, pauseReason, time.Now().UTC()); pauseErr != nil {
						slog.Warn("workflow stop pause checkpoint failed", "session", sessName, "error", pauseErr)
					}
				}
				unlock()
			}
			pauseCancel()
		}
		if runErr == nil || !errors.Is(runErr, ctx.Err()) {
			runErr = errors.Join(runErr, ctx.Err())
		}
	}

	payload := map[string]interface{}{
		"workflow":      tmpl.Name,
		"session":       sessName,
		"project_dir":   projectDir,
		"tmux_endpoint": endpoint,
		"coordination":  result.Coordination,
		"agents":        result.Agents,
		"stages":        result.Stages,
		"transitions":   result.Transitions,
		"completed":     result.Completed,
		"resumed":       result.Resumed,
		"restarts":      result.Restarts,
		"reason":        result.Reason,
	}
	if len(result.Faults) > 0 {
		payload["faults"] = result.Faults
	}
	if ownedCheckpoint {
		if st, loadErr := store.Load(sessName); loadErr == nil && st != nil {
			payload["stage"] = st.CurrentStage
			payload["paused"] = st.Paused
			payload["completed"] = st.Completed
			if tok, tokErr := st.CheckpointToken(); tokErr == nil && tok != "" {
				payload["checkpoint_token"] = tok
			}
			if gate, gateErr := store.ManualStatus(st); gateErr == nil && gate != nil {
				payload["manual_gate"] = gate
			}
		}
	}
	if s.wsHub != nil {
		s.wsHub.Publish("workflows:"+sessName, "workflow.finished", map[string]interface{}{
			"workflow":    tmpl.Name,
			"session":     sessName,
			"project_dir": projectDir,
			"reason":      result.Reason,
			"completed":   result.Completed,
			"error":       result.Error,
		})
	}
	return payload, runErr
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
	if err := tmux.ValidateSessionName(sessName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session name: "+err.Error(), nil, reqID)
		return
	}
	if _, err := parseWorkflowRunControls(req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}

	endpoint := workflowrun.TmuxEndpoint()
	tmpl, err := workflowrun.ResolveTemplateByName(projectDir, name)
	if err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	if err := workflowrun.ValidateEndpoint(tmpl, endpoint); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	vars, err := workflowrun.ResolveVarsMap(tmpl, req.Variables, req.Resume)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}

	initialStage := workflowInitialStage(tmpl)
	if req.DryRun {
		templateHash, err := tmpl.Fingerprint()
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
			return
		}
		now := time.Now().UTC()
		preview := &workflow.WorkflowState{
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
			TmuxEndpoint:   endpoint,
			Turn:           0,
			Dispatches:     []workflow.StageDispatch{},
		}
		var previewAgents []workflowrun.RunAgent
		if agents, _, _, bindErr := workflowrun.BindSessionPanes(r.Context(), tmpl, sessName); bindErr == nil {
			previewAgents = workflowAgentsFromCoord(agents)
			for _, a := range agents {
				preview.Agents[a.ID] = a.Role
			}
		}
		resp := WorkflowControlEnvelopeResponse{
			Action:       "run",
			Workflow:     tmpl.Name,
			Session:      sessName,
			ProjectDir:   projectDir,
			DryRun:       true,
			Stage:        initialStage,
			TmuxEndpoint: endpoint,
			Agents:       previewAgents,
			State:        preview,
		}
		data, err := toJSONMap(resp)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow run response", nil, reqID)
			return
		}
		writeSuccessResponse(w, http.StatusOK, data, reqID)
		return
	}

	// Non-dry-run: validate config, live session/pane assignment, and durable
	// checkpoint preconditions before accepting a managed workflow job.
	if _, err := s.loadWorkflowConfig(projectDir); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	agents, _, _, err := workflowrun.BindSessionPanes(r.Context(), tmpl, sessName)
	if err != nil {
		if isMissingTmuxSessionErr(err) {
			writeErrorResponse(w, http.StatusNotFound, robot.ErrCodeSessionNotFound, fmt.Sprintf("session %q not found: %v", sessName, err), nil, reqID)
			return
		}
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}

	store := workflowStateStoreForProject(projectDir)
	prior, err := store.Load(sessName)
	if err != nil {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		return
	}
	if req.Resume && prior == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("no workflow checkpoint to resume for session %q in %s", sessName, projectDir), nil, reqID)
		return
	}
	if !req.Resume && !req.Restart && prior != nil && !prior.Completed {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			fmt.Sprintf("workflow %s has an unfinished checkpoint at stage %q; set resume=true to continue or restart=true to deliberately restart", prior.WorkflowName, prior.CurrentStage),
			map[string]interface{}{"workflow": prior.WorkflowName, "stage": prior.CurrentStage, "paused": prior.Paused}, reqID)
		return
	}

	jobParams := map[string]interface{}{
		"workflow":    tmpl.Name,
		"session":     sessName,
		"project_dir": projectDir,
	}
	if len(req.Variables) > 0 {
		jobParams["variables"] = req.Variables
	}
	if req.Resume {
		jobParams["resume"] = true
	}
	if req.Restart {
		jobParams["restart"] = true
	}
	if req.FireManual {
		jobParams["fire_manual"] = true
	}
	if req.MaxTransitions > 0 {
		jobParams["max_transitions"] = req.MaxTransitions
	}
	if strings.TrimSpace(req.Interval) != "" {
		jobParams["interval"] = strings.TrimSpace(req.Interval)
	}
	if strings.TrimSpace(req.Timeout) != "" {
		jobParams["timeout"] = strings.TrimSpace(req.Timeout)
	}
	if strings.TrimSpace(req.TriggerTimeout) != "" {
		jobParams["trigger_timeout"] = strings.TrimSpace(req.TriggerTimeout)
	}

	job, err := s.submitJob(r.Context(), CreateJobRequest{
		Type:                    JobTypeWorkflowRun,
		Session:                 sessName,
		Params:                  jobParams,
		rejectDuplicateWorkflow: true,
	})
	if err != nil {
		var runningErr *workflowAlreadyRunningError
		if errors.As(err, &runningErr) {
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, runningErr.Error(), map[string]interface{}{
				"job_id":   runningErr.jobID,
				"workflow": runningErr.workflow,
				"session":  runningErr.session,
			}, reqID)
			return
		}
		code, status := ErrCodeServiceUnavail, http.StatusServiceUnavailable
		if errors.Is(err, errInvalidJobRequest) {
			code, status = ErrCodeBadRequest, http.StatusBadRequest
		}
		if errors.Is(err, errJobQueueFull) {
			w.Header().Set("Retry-After", "1")
		}
		details := map[string]interface{}{"execution": s.jobExecutor.snapshot()}
		if job != nil {
			details["job"] = job
		}
		writeErrorResponse(w, status, code, err.Error(), details, reqID)
		return
	}

	stage := initialStage
	if req.Resume && prior != nil {
		stage = prior.CurrentStage
	}
	resp := WorkflowControlEnvelopeResponse{
		Action:       "run",
		Workflow:     tmpl.Name,
		Session:      sessName,
		ProjectDir:   projectDir,
		JobID:        job.ID,
		JobStatus:    job.Status,
		Running:      true,
		Job:          job,
		Stage:        stage,
		TmuxEndpoint: endpoint,
		Agents:       workflowAgentsFromCoord(agents),
	}
	if req.Resume && prior != nil {
		populateWorkflowCheckpointFields(&resp, store, prior)
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow run response", nil, reqID)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	writeSuccessResponse(w, http.StatusAccepted, data, reqID)
}

// handleWorkflowsStatusV1 handles GET /api/v1/workflows/{name}/status where {name} is the workflow session.
func (s *Server) handleWorkflowsStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "name"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	if err := tmux.ValidateSessionName(sessName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session name: "+err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}

	// Snapshot live job tracking BEFORE reading disk checkpoint so a worker
	// finishing between the two reads cannot pair a terminal job status with a
	// stale pre-completion/pre-pause checkpoint.
	active, job := s.workflowRunStatusSnapshot(projectDir, sessName)
	store := workflowStateStoreForProject(projectDir)
	state, err := store.Load(sessName)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if state == nil && active == nil && job == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("no workflow run or checkpoint found for session %q in %s", sessName, projectDir), nil, reqID)
		return
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:     "status",
		Session:    sessName,
		ProjectDir: projectDir,
	}
	if active != nil {
		resp.Running = true
		resp.JobID = active.jobID
		resp.Workflow = active.workflow
	}
	if job != nil {
		resp.JobID = job.ID
		resp.JobStatus = job.Status
		resp.Job = job
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			resp.Running = true
		}
		if resp.Workflow == "" {
			if wf, ok := job.Result["workflow"].(string); ok {
				resp.Workflow = wf
			}
		}
	}
	if state != nil {
		// While a newly queued/restarted workflow is active, do not let a
		// superseded checkpoint from a completed or different workflow clobber
		// the active run's identity or completion flag.
		stalePrior := active != nil && (state.Completed || (active.workflow != "" && state.WorkflowName != "" && state.WorkflowName != active.workflow))
		if !stalePrior {
			populateWorkflowCheckpointFields(&resp, store, state)
		}
	}

	data, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow status response", nil, reqID)
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
	if err := tmux.ValidateSessionName(sessName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session name: "+err.Error(), nil, reqID)
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
		case strings.Contains(lower, "conflict") || strings.Contains(lower, "already recorded") || strings.Contains(lower, "different immutable") || strings.Contains(lower, "token") || strings.Contains(lower, "manual gate"):
			writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, err.Error(), nil, reqID)
		default:
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		}
		return
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:     "trigger",
		Session:    sessName,
		ProjectDir: projectDir,
		Stage:      decision.Stage,
		Decision:   decision,
	}
	if active, job := s.workflowRunStatusSnapshot(projectDir, sessName); active != nil || job != nil {
		if active != nil {
			resp.Running = true
			resp.JobID = active.jobID
			resp.Workflow = active.workflow
		}
		if job != nil {
			resp.JobID = job.ID
			resp.JobStatus = job.Status
			resp.Job = job
		}
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
	if err := tmux.ValidateSessionName(sessName); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session name: "+err.Error(), nil, reqID)
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

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "stopped via REST API"
	}
	store := workflowStateStoreForProject(projectDir)

	// If one or more managed workflow jobs are active for this exact
	// (projectDir, session), cancel the actual running job(s) first and wait
	// boundedly for coordinator unwind + durable pause persistence.
	activeRuns := s.activeWorkflowRuns(projectDir, sessName)
	if len(activeRuns) > 0 {
		primary := activeRuns[0]
		stoppedAny := false
		for _, run := range activeRuns {
			run.setStopReason(reason)
			if _, cancelErr := s.cancelJob(run.jobID); cancelErr == nil {
				stoppedAny = true
			} else if !errors.Is(cancelErr, errJobNotCancellable) && !errors.Is(cancelErr, errJobNotFound) {
				slog.Warn("workflow stop cancel persistence warning", "job_id", run.jobID, "session", sessName, "error", cancelErr)
			}
		}

		waitCtx, waitCancel := context.WithTimeout(r.Context(), 5*time.Second)
		allStopped := true
		for _, run := range activeRuns {
			select {
			case <-run.done:
			case <-waitCtx.Done():
				allStopped = false
			}
		}
		waitCancel()

		var state *workflow.WorkflowState
		if allStopped {
			pauseCtx, pauseCancel := context.WithTimeout(r.Context(), 2*time.Second)
			if unlock, lockErr := store.Acquire(pauseCtx, sessName); lockErr == nil {
				if loaded, loadErr := store.Load(sessName); loadErr == nil && loaded != nil {
					if !loaded.Completed && !loaded.Paused {
						_ = store.Pause(loaded, reason, time.Now().UTC())
					}
					state = loaded
				}
				unlock()
			}
			pauseCancel()
		}
		if state == nil {
			state, _ = store.Load(sessName)
		}

		job := s.jobStore.Get(primary.jobID)
		resp := WorkflowControlEnvelopeResponse{
			Action:                "stop",
			Workflow:              primary.workflow,
			Session:               sessName,
			ProjectDir:            projectDir,
			JobID:                 primary.jobID,
			StoppedJob:            stoppedAny,
			Running:               !allStopped,
			CancellationRequested: !allStopped,
			Job:                   job,
		}
		if job != nil {
			resp.JobStatus = job.Status
		} else {
			resp.JobStatus = JobStatusCancelled
		}
		if state != nil {
			stalePrior := !allStopped && (state.Completed || (primary.workflow != "" && state.WorkflowName != "" && state.WorkflowName != primary.workflow))
			if !stalePrior {
				populateWorkflowCheckpointFields(&resp, store, state)
			}
		}

		statusCode := http.StatusOK
		if !allStopped {
			statusCode = http.StatusAccepted
		}
		data, err := toJSONMap(resp)
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize workflow stop response", nil, reqID)
			return
		}
		writeSuccessResponse(w, statusCode, data, reqID)
		return
	}

	// No active managed job for (projectDir, session): pause an offline stored
	// checkpoint under its session lock.
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
	if state.Completed {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict,
			fmt.Sprintf("workflow checkpoint for session %q is already completed and cannot be paused", sessName), nil, reqID)
		return
	}
	if !state.Paused {
		if err := store.Pause(state, reason, time.Now().UTC()); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
			return
		}
	}

	resp := WorkflowControlEnvelopeResponse{
		Action:     "stop",
		Session:    sessName,
		ProjectDir: projectDir,
	}
	if _, job := s.workflowRunStatusSnapshot(projectDir, sessName); job != nil {
		resp.JobID = job.ID
		resp.JobStatus = job.Status
		resp.Job = job
	}
	populateWorkflowCheckpointFields(&resp, store, state)
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
