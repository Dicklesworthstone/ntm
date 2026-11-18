package serve

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

const maxServeSessionIntelBodyBytes = 1 << 20 // 1 MiB

// SessionAdoptRequest is the JSON body for POST /api/v1/sessions/{id}/adopt.
type SessionAdoptRequest struct {
	Panes        map[string]string `json:"panes,omitempty"`
	ByWindow     bool              `json:"by_window,omitempty"`
	ByWindowType string            `json:"by_window_type,omitempty"`
	AutoName     *bool             `json:"auto_name,omitempty"`
	DryRun       bool              `json:"dry_run,omitempty"`
}

// SessionAdoptEnvelopeResponse is the REST JSON envelope for POST /api/v1/sessions/{id}/adopt.
type SessionAdoptEnvelopeResponse struct {
	APIResponse
	SchemaID      string                   `json:"schema_id"`
	SchemaVersion string                   `json:"schema_version"`
	Session       string                   `json:"session"`
	AdoptedPanes  []robot.AdoptedPaneInfo  `json:"adopted_panes"`
	TotalPanes    int                      `json:"total_panes"`
	Agents        robot.AdoptedAgentCounts `json:"agents"`
	AutoName      bool                     `json:"auto_name"`
	ByWindow      bool                     `json:"by_window,omitempty"`
	DryRun        bool                     `json:"dry_run,omitempty"`
	AgentHints    *robot.AgentHints        `json:"_agent_hints,omitempty"`
}

// SessionContextEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/context.
type SessionContextEnvelopeResponse struct {
	APIResponse
	Session          string                             `json:"session"`
	CapturedAt       time.Time                          `json:"captured_at"`
	Agents           []robot.AgentContextInfo           `json:"agents"`
	Summary          robot.ContextSummary               `json:"summary"`
	PendingRotations []robot.ContextPendingRotationInfo `json:"pending_rotations,omitempty"`
	AgentHints       *robot.ContextAgentHints           `json:"_agent_hints,omitempty"`
}

// SessionDiffEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/diff.
type SessionDiffEnvelopeResponse struct {
	APIResponse
	Session       string                `json:"session"`
	Timeframe     robot.DiffTimeframe   `json:"timeframe"`
	Files         robot.DiffFiles       `json:"files"`
	AgentActivity []robot.DiffAgentInfo `json:"agent_activity"`
	AgentHints    *robot.DiffAgentHints `json:"_agent_hints,omitempty"`
}

// SessionFilesEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/files.
type SessionFilesEnvelopeResponse struct {
	APIResponse
	Session    string                   `json:"session,omitempty"`
	TimeWindow string                   `json:"time_window"`
	Count      int                      `json:"count"`
	Changes    []robot.FileChangeRecord `json:"changes"`
	Summary    robot.FileChangesSummary `json:"summary"`
	AgentHints *robot.AgentHints        `json:"_agent_hints,omitempty"`
}

// PersonasEnvelopeResponse is the REST JSON envelope for GET /api/v1/personas.
type PersonasEnvelopeResponse struct {
	APIResponse
	SchemaID      string                `json:"schema_id"`
	SchemaVersion string                `json:"schema_version"`
	ProjectDir    string                `json:"project_dir,omitempty"`
	FilterAgent   string                `json:"filter_agent,omitempty"`
	FilterTag     string                `json:"filter_tag,omitempty"`
	Personas      []*persona.Persona    `json:"personas"`
	ProfileSets   []*persona.PersonaSet `json:"profile_sets"`
	Count         int                   `json:"count"`
	BuiltinCount  int                   `json:"builtin_count"`
	AgentHints    *robot.AgentHints     `json:"_agent_hints,omitempty"`
}

// RecipesEnvelopeResponse is the REST JSON envelope for GET /api/v1/recipes.
type RecipesEnvelopeResponse struct {
	APIResponse
	GeneratedAt time.Time          `json:"generated_at"`
	Count       int                `json:"count"`
	Recipes     []robot.RecipeInfo `json:"recipes"`
}

// HooksRunRequest is the JSON request body for POST /api/v1/hooks/run.
type HooksRunRequest struct {
	HookType      string `json:"hook_type,omitempty"`
	ProjectDir    string `json:"project_dir,omitempty"`
	Verbose       bool   `json:"verbose,omitempty"`
	FailOnWarning *bool  `json:"fail_on_warning,omitempty"`
	TimeoutSec    int    `json:"timeout_sec,omitempty"`
}

// HooksEnvelopeResponse is the REST JSON envelope for GET /api/v1/hooks and POST /api/v1/hooks/run.
type HooksEnvelopeResponse struct {
	APIResponse
	SchemaID        string                 `json:"schema_id"`
	SchemaVersion   string                 `json:"schema_version"`
	Action          string                 `json:"action"`
	RepoRoot        string                 `json:"repo_root,omitempty"`
	HooksDir        string                 `json:"hooks_dir,omitempty"`
	Hooks           []*hooks.HookInfo      `json:"hooks,omitempty"`
	InstalledCount  *int                   `json:"installed_count,omitempty"`
	HookType        string                 `json:"hook_type,omitempty"`
	Passed          *bool                  `json:"passed,omitempty"`
	PreCommitResult *hooks.PreCommitResult `json:"pre_commit_result,omitempty"`
	CheckResult     *hooks.HookCheckResult `json:"check_result,omitempty"`
	AgentHints      *robot.AgentHints      `json:"_agent_hints,omitempty"`
}

// RedactPreviewRequest is the JSON request body for POST /api/v1/redact/preview.
type RedactPreviewRequest struct {
	Text string `json:"text"`
}

// RedactPreviewEnvelopeResponse is the REST JSON envelope for POST /api/v1/redact/preview.
type RedactPreviewEnvelopeResponse struct {
	APIResponse
	SchemaID      string                       `json:"schema_id"`
	SchemaVersion string                       `json:"schema_version"`
	Source        string                       `json:"source,omitempty"`
	InputLen      int                          `json:"input_len"`
	FindingCount  int                          `json:"finding_count"`
	Findings      []robot.RedactPreviewFinding `json:"findings"`
	Output        string                       `json:"output"`
	AgentHints    *robot.AgentHints            `json:"_agent_hints,omitempty"`
}

func (s *Server) registerSessionIntelRoutes(r chi.Router) {
	// Session-scoped adopt & inspection routes (mounted directly to avoid chi subrouter shadowing).
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/adopt", s.handleSessionAdoptV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/context", s.handleSessionContextV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/diff", s.handleSessionDiffV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/files", s.handleSessionFilesV1)

	// Catalog routes: personas & spawn recipes
	r.With(s.RequirePermission(PermReadSessions)).Get("/personas", s.handlePersonasListV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/recipes", s.handleRecipesListV1)

	// Repository git hooks & safe secret redaction preview
	r.With(s.RequirePermission(PermReadHealth)).Get("/hooks", s.handleHooksStatusV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/hooks/run", s.handleHooksRunV1)
	r.With(s.RequirePermission(PermReadHealth)).Post("/redact/preview", s.handleRedactPreviewV1)
}

// handleSessionAdoptV1 handles POST /api/v1/sessions/{id}/adopt via kernel command sessions.adopt.
func (s *Server) handleSessionAdoptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionIntelBodyBytes)
	var req SessionAdoptRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	byWindowType := strings.TrimSpace(req.ByWindowType)
	if len(req.Panes) == 0 && !req.ByWindow && strings.Compare(byWindowType, "") == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "panes map or by_window_type is required", nil, reqID)
		return
	}

	payload := map[string]any{
		"session":        sessionID,
		"panes":          req.Panes,
		"by_window":      req.ByWindow || strings.Compare(byWindowType, "") != 0,
		"by_window_type": byWindowType,
		"dry_run":        req.DryRun,
	}
	if req.AutoName != nil {
		payload["auto_name"] = *req.AutoName
	}

	result, err := kernel.Run(r.Context(), "sessions.adopt", payload)
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize adopt response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionContextV1 handles GET /api/v1/sessions/{id}/context via robot.GetContext.
func (s *Server) handleSessionContextV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	lines := 200
	if rawLines := strings.TrimSpace(firstQueryParam(r, "lines")); strings.Compare(rawLines, "") != 0 {
		parsed, err := strconv.Atoi(rawLines)
		if err != nil || parsed < 1 || parsed > 10000 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be an integer between 1 and 10000", nil, reqID)
			return
		}
		lines = parsed
	}

	out, err := robot.GetContext(sessionID, lines)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize context response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionDiffV1 handles GET /api/v1/sessions/{id}/diff via robot.GetDiff.
func (s *Server) handleSessionDiffV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	sinceDur := 15 * time.Minute
	if rawSince := strings.TrimSpace(firstQueryParam(r, "since")); strings.Compare(rawSince, "") != 0 {
		parsed, err := parseSummarySinceQuery(rawSince)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
			return
		}
		sinceDur = parsed
	}

	out, err := robot.GetDiff(robot.DiffOptions{
		Session: sessionID,
		Since:   sinceDur,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize diff response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionFilesV1 handles GET /api/v1/sessions/{id}/files via robot.GetFiles.
func (s *Server) handleSessionFilesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	timeWindow := strings.TrimSpace(firstQueryParam(r, "window"))
	if strings.Compare(timeWindow, "") == 0 {
		timeWindow = "15m"
	} else if !strings.EqualFold(timeWindow, "5m") && !strings.EqualFold(timeWindow, "15m") && !strings.EqualFold(timeWindow, "1h") && !strings.EqualFold(timeWindow, "all") {
		dur, err := time.ParseDuration(timeWindow)
		if err != nil || dur <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "window must be 5m, 15m, 1h, all, or a positive Go duration", nil, reqID)
			return
		}
	}

	limit := 100
	if rawLimit := strings.TrimSpace(firstQueryParam(r, "limit")); strings.Compare(rawLimit, "") != 0 {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be an integer between 1 and 1000", nil, reqID)
			return
		}
		limit = parsed
	}

	out, err := robot.GetFiles(robot.FilesOptions{
		Session:    sessionID,
		TimeWindow: timeWindow,
		Limit:      limit,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize files response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handlePersonasListV1 handles GET /api/v1/personas via robot.GetPersonas.
func (s *Server) handlePersonasListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	rawDir := strings.TrimSpace(firstQueryParam(r, "project_dir"))
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	out, err := robot.GetPersonas(robot.PersonasOptions{
		ProjectDir: projectDir,
		Agent:      strings.TrimSpace(firstQueryParam(r, "agent")),
		Tag:        strings.TrimSpace(firstQueryParam(r, "tag")),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize personas response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleRecipesListV1 handles GET /api/v1/recipes via robot.GetRecipes.
func (s *Server) handleRecipesListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	out, err := robot.GetRecipes()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize recipes response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleHooksStatusV1 handles GET /api/v1/hooks via robot.GetHooks.
func (s *Server) handleHooksStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	rawDir := strings.TrimSpace(firstQueryParam(r, "project_dir"))
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	out, err := robot.GetHooks(r.Context(), robot.HooksOptions{
		Action:     "status",
		ProjectDir: projectDir,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize hooks status response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleHooksRunV1 handles POST /api/v1/hooks/run via robot.GetHooks.
func (s *Server) handleHooksRunV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionIntelBodyBytes)
	var req HooksRunRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	rawDir := strings.TrimSpace(req.ProjectDir)
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project_dir"))
	}
	if strings.Compare(rawDir, "") == 0 {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	if req.TimeoutSec < 0 || req.TimeoutSec > 600 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "timeout_sec must be between 0 and 600", nil, reqID)
		return
	}

	out, err := robot.GetHooks(r.Context(), robot.HooksOptions{
		Action:        "run",
		HookType:      strings.TrimSpace(req.HookType),
		ProjectDir:    projectDir,
		Verbose:       req.Verbose,
		FailOnWarning: req.FailOnWarning,
		TimeoutSec:    req.TimeoutSec,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success && !strings.EqualFold(out.ErrorCode, robot.ErrCodeHookCheckFailed) {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize hooks run response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleRedactPreviewV1 handles POST /api/v1/redact/preview via robot.GetRedactPreview.
// Over HTTP only inline JSON text is accepted (never host file paths) and raw secret matches are never returned.
func (s *Server) handleRedactPreviewV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionIntelBodyBytes)
	var req RedactPreviewRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if strings.Compare(req.Text, "") == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "text is required", nil, reqID)
		return
	}

	redactCfg := s.requestRedactionConfig(r)
	out, err := robot.GetRedactPreview(robot.RedactOptions{
		Text:      req.Text,
		RedactCfg: &redactCfg,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if strings.Compare(errCode, "") == 0 {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize redact preview response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}
