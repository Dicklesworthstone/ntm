package serve

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/codeblock"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

const maxServeEcosystemBodyBytes = 1 << 20 // 1 MiB

// SessionGrepEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/grep and GET /api/v1/grep.
type SessionGrepEnvelopeResponse struct {
	APIResponse
	SchemaID        string            `json:"schema_id"`
	SchemaVersion   string            `json:"schema_version"`
	Pattern         string            `json:"pattern"`
	Session         string            `json:"session"`
	Matches         []robot.GrepMatch `json:"matches"`
	MatchingPanes   []string          `json:"matching_panes"`
	TotalLines      int               `json:"total_lines_searched"`
	MatchCount      int               `json:"match_count"`
	PaneCount       int               `json:"panes_searched"`
	CaseInsensitive bool              `json:"case_insensitive,omitempty"`
	InvertMatch     bool              `json:"invert_match,omitempty"`
	ListOnly        bool              `json:"list_only,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// SessionExtractEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/extract.
type SessionExtractEnvelopeResponse struct {
	APIResponse
	SchemaID      string                `json:"schema_id"`
	SchemaVersion string                `json:"schema_version"`
	Session       string                `json:"session"`
	Source        string                `json:"source"`
	Language      string                `json:"language,omitempty"`
	Blocks        []codeblock.CodeBlock `json:"blocks"`
	TotalFound    int                   `json:"total_found"`
	PanesScanned  int                   `json:"panes_scanned"`
}

// SessionErrorsEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/errors.
type SessionErrorsEnvelopeResponse struct {
	APIResponse
	Session string              `json:"session"`
	Errors  []robot.ErrorEntry  `json:"errors"`
	Summary robot.ErrorsSummary `json:"summary"`
}

// ConfigValidateRequest is the JSON request body for POST /api/v1/config/validate.
type ConfigValidateRequest struct {
	All        bool   `json:"all,omitempty"`
	Fix        bool   `json:"fix,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// ConfigValidateEnvelopeResponse is the REST JSON envelope for GET/POST /api/v1/config/validate.
type ConfigValidateEnvelopeResponse struct {
	APIResponse
	SchemaID        string                   `json:"schema_id"`
	SchemaVersion   string                   `json:"schema_version"`
	Valid           bool                     `json:"valid"`
	All             bool                     `json:"all,omitempty"`
	FixApplied      bool                     `json:"fix_applied,omitempty"`
	ValidationError string                   `json:"validation_error,omitempty"`
	Results         []robot.ValidationResult `json:"results"`
	Summary         robot.ValidationSummary  `json:"summary"`
}

// PluginsEnvelopeResponse is the REST JSON envelope for GET /api/v1/plugins.
type PluginsEnvelopeResponse struct {
	APIResponse
	SchemaID       string                     `json:"schema_id"`
	SchemaVersion  string                     `json:"schema_version"`
	ConfigDir      string                     `json:"config_dir"`
	AgentsDir      string                     `json:"agents_dir"`
	CommandsDir    string                     `json:"commands_dir"`
	AgentPlugins   []robot.AgentPluginEntry   `json:"agent_plugins"`
	CommandPlugins []robot.CommandPluginEntry `json:"command_plugins"`
	TotalCount     int                        `json:"total_count"`
}

// LocalModelsEnvelopeResponse is the REST JSON envelope for GET /api/v1/models.
type LocalModelsEnvelopeResponse struct {
	APIResponse
	SchemaID           string                  `json:"schema_id"`
	SchemaVersion      string                  `json:"schema_version"`
	Action             string                  `json:"action"`
	Host               string                  `json:"host,omitempty"`
	ModelCount         int                     `json:"model_count,omitempty"`
	TotalSizeBytes     int64                   `json:"total_size_bytes,omitempty"`
	DiskFreeBytes      int64                   `json:"disk_free_bytes,omitempty"`
	DiskLow            bool                    `json:"disk_low,omitempty"`
	CleanupSuggestions []string                `json:"cleanup_suggestions,omitempty"`
	Models             []robot.LocalModelEntry `json:"models"`
	DetectedVRAMGB     float64                 `json:"detected_vram_gb,omitempty"`
	VRAMSource         string                  `json:"vram_source,omitempty"`
	Recommendations    []string                `json:"recommendations"`
}

func (s *Server) registerOutputEcosystemRoutes(r chi.Router) {
	// Session & global pane scrollback regex grep, fenced code-block extract, and error signature scan
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/grep", s.handleSessionGrepV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/grep", s.handleGlobalGrepV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/extract", s.handleSessionExtractV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/errors", s.handleSessionErrorsV1)

	// Config, recipes, personas, policy & 11-harness executable validation
	r.With(s.RequirePermission(PermReadHealth)).Get("/config/validate", s.handleConfigValidateGetV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/config/validate", s.handleConfigValidatePostV1)

	// Installed agent/command plugins & local Ollama models / VRAM recommendations
	r.With(s.RequirePermission(PermReadAgents)).Get("/plugins", s.handlePluginsListV1)
	r.With(s.RequirePermission(PermReadAgents)).Get("/models", s.handleLocalModelsV1)
}

func parseOptionalBoolParam(r *http.Request, key string) (bool, bool, error) {
	raw := strings.TrimSpace(firstQueryParam(r, key))
	if raw == "" {
		return false, false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, true, err
	}
	return v, true, nil
}

func parseOptionalNonNegIntParam(r *http.Request, key string, defaultVal int) (int, error) {
	raw := strings.TrimSpace(firstQueryParam(r, key))
	if raw == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}

func parseGrepOptionsFromRequest(w http.ResponseWriter, r *http.Request, session string, defaultAll bool, reqID string) (robot.GrepOptions, bool) {
	pattern := firstQueryParam(r, "pattern")
	if strings.TrimSpace(pattern) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pattern query parameter is required", nil, reqID)
		return robot.GrepOptions{}, false
	}

	ignoreCase, _, err := parseOptionalBoolParam(r, "ignore_case")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "ignore_case must be a boolean", nil, reqID)
		return robot.GrepOptions{}, false
	}
	invert, _, err := parseOptionalBoolParam(r, "invert")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invert must be a boolean", nil, reqID)
		return robot.GrepOptions{}, false
	}
	listOnly, _, err := parseOptionalBoolParam(r, "list_only")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "list_only must be a boolean", nil, reqID)
		return robot.GrepOptions{}, false
	}
	allSessions := defaultAll
	if allVal, hasAll, err := parseOptionalBoolParam(r, "all"); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "all must be a boolean", nil, reqID)
		return robot.GrepOptions{}, false
	} else if hasAll {
		allSessions = allVal
	}

	ctxLines, err := parseOptionalNonNegIntParam(r, "context", 0)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "context must be a non-negative integer", nil, reqID)
		return robot.GrepOptions{}, false
	}
	beforeLines, err := parseOptionalNonNegIntParam(r, "before", 0)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "before must be a non-negative integer", nil, reqID)
		return robot.GrepOptions{}, false
	}
	afterLines, err := parseOptionalNonNegIntParam(r, "after", 0)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "after must be a non-negative integer", nil, reqID)
		return robot.GrepOptions{}, false
	}
	maxLines, err := parseOptionalNonNegIntParam(r, "lines", 1000)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be a non-negative integer", nil, reqID)
		return robot.GrepOptions{}, false
	}

	var agentTypes []string
	rawType := strings.TrimSpace(firstQueryParam(r, "type"))
	if rawType == "" {
		rawType = strings.TrimSpace(firstQueryParam(r, "agent_type"))
	}
	if rawType != "" {
		for _, part := range strings.Split(rawType, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				agentTypes = append(agentTypes, trimmed)
			}
		}
	}

	return robot.GrepOptions{
		Pattern:         pattern,
		Session:         session,
		AllSessions:     allSessions,
		AgentTypes:      agentTypes,
		CaseInsensitive: ignoreCase,
		InvertMatch:     invert,
		ContextLines:    ctxLines,
		BeforeLines:     beforeLines,
		AfterLines:      afterLines,
		MaxLines:        maxLines,
		ListOnly:        listOnly,
	}, true
}

// handleSessionGrepV1 handles GET /api/v1/sessions/{id}/grep.
func (s *Server) handleSessionGrepV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "id"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	opts, ok := parseGrepOptionsFromRequest(w, r, sessName, false, reqID)
	if !ok {
		return
	}
	opts.AllSessions = false
	out, err := robot.GetGrep(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session grep", reqID)
}

// handleGlobalGrepV1 handles GET /api/v1/grep.
func (s *Server) handleGlobalGrepV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessParam := strings.TrimSpace(firstQueryParam(r, "session"))
	if sessParam != "" {
		validSess, err := validateSafeWorktreeIdentifier("session", sessParam)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
			return
		}
		sessParam = validSess
	}
	defaultAll := sessParam == ""
	opts, ok := parseGrepOptionsFromRequest(w, r, sessParam, defaultAll, reqID)
	if !ok {
		return
	}
	out, err := robot.GetGrep(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "global grep", reqID)
}

// handleSessionExtractV1 handles GET /api/v1/sessions/{id}/extract.
func (s *Server) handleSessionExtractV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "id"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	lastPane, _, err := parseOptionalBoolParam(r, "last")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "last must be a boolean", nil, reqID)
		return
	}
	lines, err := parseOptionalNonNegIntParam(r, "lines", 500)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be a non-negative integer", nil, reqID)
		return
	}
	selectBlock, err := parseOptionalNonNegIntParam(r, "select", 0)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "select must be a non-negative integer", nil, reqID)
		return
	}

	out, err := robot.GetExtract(robot.ExtractOptions{
		Session:     sessName,
		Pane:        strings.TrimSpace(firstQueryParam(r, "pane")),
		Language:    strings.TrimSpace(firstQueryParam(r, "lang")),
		LastPane:    lastPane,
		Lines:       lines,
		SelectBlock: selectBlock,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session extract", reqID)
}

// handleSessionErrorsV1 handles GET /api/v1/sessions/{id}/errors.
func (s *Server) handleSessionErrorsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessName, err := validateSafeWorktreeIdentifier("session", chi.URLParam(r, "id"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	lines, err := parseOptionalNonNegIntParam(r, "lines", 1000)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be a non-negative integer", nil, reqID)
		return
	}
	ctxLines, err := parseOptionalNonNegIntParam(r, "context", 2)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "context must be a non-negative integer", nil, reqID)
		return
	}

	var paneSelectors []string
	if rawPanes := strings.TrimSpace(firstQueryParam(r, "panes")); rawPanes != "" {
		for _, p := range strings.Split(rawPanes, ",") {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				paneSelectors = append(paneSelectors, trimmed)
			}
		}
	}
	agentType := strings.TrimSpace(firstQueryParam(r, "agent_type"))
	if agentType == "" {
		agentType = strings.TrimSpace(firstQueryParam(r, "type"))
	}

	out, err := robot.GetErrors(robot.ErrorsOptions{
		Session:   sessName,
		Panes:     paneSelectors,
		Lines:     lines,
		AgentType: agentType,
		Context:   ctxLines,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "session errors", reqID)
}

func (s *Server) executeAndWriteConfigValidation(w http.ResponseWriter, opts robot.ValidationOptions, reqID string) {
	out, err := robot.GetValidation(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	respPayload := map[string]any{
		"schema_id":      out.SchemaID,
		"schema_version": out.SchemaVersion,
		"valid":          out.Valid,
		"results":        out.Results,
		"summary":        out.Summary,
	}
	if out.All {
		respPayload["all"] = true
	}
	if out.FixApplied {
		respPayload["fix_applied"] = true
	}
	if !out.Valid && out.Error != "" {
		respPayload["validation_error"] = out.Error
	}
	writeSuccessResponse(w, http.StatusOK, respPayload, reqID)
}

// handleConfigValidateGetV1 handles GET /api/v1/config/validate.
func (s *Server) handleConfigValidateGetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	allFlag, _, err := parseOptionalBoolParam(r, "all")
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "all must be a boolean", nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, "", reqID)
	if !ok {
		return
	}
	s.executeAndWriteConfigValidation(w, robot.ValidationOptions{
		ProjectDir: projectDir,
		All:        allFlag,
		Fix:        false,
	}, reqID)
}

// handleConfigValidatePostV1 handles POST /api/v1/config/validate.
func (s *Server) handleConfigValidatePostV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServeEcosystemBodyBytes)
	var req ConfigValidateRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	projectDir, ok := s.resolvePersistenceProjectDir(w, r, req.ProjectDir, reqID)
	if !ok {
		return
	}
	s.executeAndWriteConfigValidation(w, robot.ValidationOptions{
		ProjectDir: projectDir,
		All:        req.All,
		Fix:        req.Fix,
	}, reqID)
}

// handlePluginsListV1 handles GET /api/v1/plugins.
func (s *Server) handlePluginsListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	out, err := robot.GetPlugins(robot.PluginsOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "plugins list", reqID)
}

// handleLocalModelsV1 handles GET /api/v1/models.
func (s *Server) handleLocalModelsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	action := strings.TrimSpace(firstQueryParam(r, "action"))
	if action == "" {
		action = "recommend"
	}
	host := strings.TrimSpace(firstQueryParam(r, "host"))

	out, err := robot.GetLocalModels(r.Context(), robot.LocalModelsOptions{
		Action:       action,
		Host:         host,
		SafeHostOnly: true,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeRobotEnvelopeOrError(w, out, out.RobotResponse, "local models", reqID)
}
