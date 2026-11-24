package serve

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

const (
	maxServeSessionLifecycleBodyBytes = 64 << 10 // 64 KiB
	maxServePromptPreflightBodyBytes  = 1 << 20  // 1 MiB
	maxServeOrchestrationBodyBytes    = 64 << 10 // 64 KiB
)

// =============================================================================
// Session Lifecycle & Orchestration REST Request / Response Shapes (OpenAPI 3.1)
// =============================================================================

// DeleteSessionRequest is the optional JSON body for DELETE /api/v1/sessions/{id}.
type DeleteSessionRequest struct {
	Force     bool     `json:"force,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	NoHooks   bool     `json:"no_hooks,omitempty"`
	Summarize bool     `json:"summarize,omitempty"`
}

// SessionInterruptRequest is the optional JSON body for POST /api/v1/sessions/{id}/interrupt.
type SessionInterruptRequest struct {
	Tags []string `json:"tags,omitempty"`
}

// SessionControllerRequest is the optional JSON body for POST /api/v1/sessions/{id}/controller.
type SessionControllerRequest struct {
	AgentType  string `json:"agent_type,omitempty"`
	PromptFile string `json:"prompt_file,omitempty"`
	NoPrompt   bool   `json:"no_prompt,omitempty"`
}

// SessionControllerEnvelopeResponse is the REST JSON envelope for POST /api/v1/sessions/{id}/controller.
type SessionControllerEnvelopeResponse struct {
	APIResponse
	Session    string `json:"session"`
	PaneID     string `json:"pane_id"`
	PaneIndex  int    `json:"pane_index"`
	AgentType  string `json:"agent_type"`
	PromptUsed string `json:"prompt_used,omitempty"`
	AgentCount int    `json:"agent_count"`
	AgentList  string `json:"agent_list,omitempty"`
}

// SessionHealthEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/health.
type SessionHealthEnvelopeResponse struct {
	APIResponse
	Session             string                      `json:"session"`
	CheckedAt           string                      `json:"checked_at"`
	Agents              []health.AgentHealth        `json:"agents"`
	Summary             health.HealthSummary        `json:"summary"`
	OverallStatus       health.Status               `json:"overall_status"`
	ObservationComplete bool                        `json:"observation_complete"`
	ObservationFailures []status.ObservationFailure `json:"observation_failures"`
}

// PromptPreflightRequest is the JSON request body for POST /api/v1/prompt/preflight.
type PromptPreflightRequest struct {
	Prompt string `json:"prompt"`
	Strict bool   `json:"strict,omitempty"`
}

// PromptPreflightEnvelopeResponse is the REST JSON envelope for POST /api/v1/prompt/preflight.
type PromptPreflightEnvelopeResponse struct {
	APIResponse
	SchemaID        string                   `json:"schema_id"`
	SchemaVersion   string                   `json:"schema_version"`
	Allowed         bool                     `json:"allowed"`
	PreviewHash     string                   `json:"preview_hash"`
	PreviewLen      int                      `json:"preview_len"`
	EstimatedTokens int                      `json:"estimated_tokens"`
	Findings        []robot.PreflightFinding `json:"findings"`
	ErrorCount      int                      `json:"error_count"`
	WarningCount    int                      `json:"warning_count"`
	InfoCount       int                      `json:"info_count"`
	Preview         string                   `json:"preview,omitempty"`
	DCGAvailable    bool                     `json:"dcg_available"`
	Strict          bool                     `json:"strict"`
	AgentHints      *robot.AgentHints        `json:"_agent_hints,omitempty"`
}

// RecommendAssignRequest is the JSON request body for POST /api/v1/assign.
type RecommendAssignRequest struct {
	Session    string   `json:"session"`
	ProjectDir string   `json:"project_dir,omitempty"`
	Beads      []string `json:"beads,omitempty"`
	Strategy   string   `json:"strategy,omitempty"`
	Relax      bool     `json:"relax,omitempty"`
}

// AssignPlanEnvelopeResponse is the REST JSON envelope for POST /api/v1/assign.
type AssignPlanEnvelopeResponse struct {
	APIResponse
	Session            string                   `json:"session"`
	Strategy           string                   `json:"strategy"`
	GeneratedAt        string                   `json:"generated_at"`
	Relaxed            bool                     `json:"relaxed"`
	RelaxedConstraints []string                 `json:"relaxed_constraints"`
	OriginalScore      float64                  `json:"original_score"`
	RelaxedScore       float64                  `json:"relaxed_score"`
	Recommendations    []robot.AssignRecommend  `json:"recommendations"`
	BlockedBeads       []robot.BlockedBead      `json:"blocked_beads"`
	IdleAgents         []string                 `json:"idle_agents"`
	UnassignableBeads  []robot.UnassignableBead `json:"unassignable_beads,omitempty"`
	Summary            robot.AssignSummary      `json:"summary"`
	AgentHints         *robot.AssignAgentHints  `json:"_agent_hints,omitempty"`
	WorkSourceMismatch *worksource.StaleError   `json:"work_source_mismatch,omitempty"`
}

// RecommendRouteRequest is the JSON request body for POST /api/v1/route.
type RecommendRouteRequest struct {
	Session        string   `json:"session"`
	Strategy       string   `json:"strategy,omitempty"`
	AgentType      string   `json:"agent_type,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	ExcludePanes   []int    `json:"exclude_panes,omitempty"`
	ExcludePaneIDs []string `json:"exclude_pane_ids,omitempty"`
	LastAgent      string   `json:"last_agent,omitempty"`
	Relax          bool     `json:"relax,omitempty"`
}

// RoutePlanEnvelopeResponse is the REST JSON envelope for POST /api/v1/route.
type RoutePlanEnvelopeResponse struct {
	APIResponse
	Session            string                         `json:"session"`
	Strategy           robot.StrategyName             `json:"strategy"`
	Recommendation     *robot.RouteRecommendation     `json:"recommendation,omitempty"`
	Candidates         []robot.RouteCandidate         `json:"candidates"`
	Excluded           []robot.RouteExcluded          `json:"excluded,omitempty"`
	FallbackUsed       bool                           `json:"fallback_used,omitempty"`
	Relaxed            bool                           `json:"relaxed"`
	RelaxedConstraints []string                       `json:"relaxed_constraints"`
	OriginalScore      float64                        `json:"original_score"`
	RelaxedScore       float64                        `json:"relaxed_score"`
	ReservationSummary *robot.RouteReservationSummary `json:"reservation_summary,omitempty"`
	AgentHints         *robot.RouteAgentHints         `json:"_agent_hints,omitempty"`
}

// QuotaStatusEnvelopeResponse is the REST JSON envelope for GET /api/v1/quota.
type QuotaStatusEnvelopeResponse struct {
	APIResponse
	Provider string               `json:"provider,omitempty"`
	Quota    robot.QuotaInfo      `json:"quota"`
	Detail   *robot.ProviderQuota `json:"detail,omitempty"`
}

func (s *Server) registerOrchestrationRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadSessions)).Post("/prompt/preflight", s.handlePromptPreflightV1)
	r.With(s.RequirePermission(PermReadBeads)).Post("/assign", s.handleRecommendAssignV1)
	r.With(s.RequirePermission(PermReadSessions)).Post("/route", s.handleRecommendRouteV1)
	r.With(s.RequirePermission(PermReadAccounts)).Get("/quota", s.handleGetQuotaStatusV1)
}

func kernelSessionErrorHTTP(err error) (int, string) {
	var selectorErr *tmux.PaneSelectorError
	if errors.As(err, &selectorErr) {
		switch selectorErr.Kind {
		case tmux.PaneSelectorAmbiguous:
			return http.StatusConflict, ErrCodeConflict
		case tmux.PaneSelectorInvalid, tmux.PaneSelectorMissing:
			return http.StatusBadRequest, ErrCodeBadRequest
		case tmux.PaneSelectorNotFound:
			return http.StatusNotFound, ErrCodeNotFound
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"), strings.Contains(msg, "does not exist"), strings.Contains(msg, "no session"), strings.Contains(msg, "no panes match"):
		return http.StatusNotFound, ErrCodeNotFound
	case strings.Contains(msg, "not registered"):
		return http.StatusNotImplemented, ErrCodeNotImplemented
	case strings.Contains(msg, "invalid"), strings.Contains(msg, "required"):
		return http.StatusBadRequest, ErrCodeBadRequest
	default:
		return http.StatusInternalServerError, ErrCodeInternalError
	}
}

func firstQueryParam(r *http.Request, key string) string {
	vals := r.URL.Query()[key]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// handleDeleteSessionV1 handles DELETE /api/v1/sessions/{id}.
func (s *Server) handleDeleteSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionLifecycleBodyBytes)
	var req DeleteSessionRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	force := req.Force
	if rawForce := firstQueryParam(r, "force"); rawForce != "" {
		parsed, err := parseBoolQueryParam(rawForce)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid force query parameter", nil, reqID)
			return
		}
		force = parsed
	}
	noHooks := req.NoHooks
	if rawNoHooks := firstQueryParam(r, "no_hooks"); rawNoHooks != "" {
		parsed, err := parseBoolQueryParam(rawNoHooks)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid no_hooks query parameter", nil, reqID)
			return
		}
		noHooks = parsed
	}
	summarize := req.Summarize
	if rawSummarize := firstQueryParam(r, "summarize"); rawSummarize != "" {
		parsed, err := parseBoolQueryParam(rawSummarize)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid summarize query parameter", nil, reqID)
			return
		}
		summarize = parsed
	}

	result, err := kernel.Run(r.Context(), "sessions.kill", map[string]interface{}{
		"session":   sessionID,
		"force":     force,
		"tags":      req.Tags,
		"no_hooks":  noHooks,
		"summarize": summarize,
	})
	if err != nil {
		codeStatus, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, codeStatus, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionInterruptV1 handles POST /api/v1/sessions/{id}/interrupt.
func (s *Server) handleSessionInterruptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionLifecycleBodyBytes)
	var req SessionInterruptRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.interrupt", map[string]interface{}{
		"session": sessionID,
		"tags":    req.Tags,
	})
	if err != nil {
		codeStatus, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, codeStatus, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionHealthV1 handles GET /api/v1/sessions/{id}/health.
func (s *Server) handleSessionHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	verbose, err := parseBoolQueryParam(firstQueryParam(r, "verbose"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid verbose query parameter", nil, reqID)
		return
	}

	input := map[string]interface{}{
		"session": sessionID,
		"verbose": verbose,
	}
	if statusFilter := strings.TrimSpace(firstQueryParam(r, "status")); statusFilter != "" {
		input["status"] = statusFilter
	}
	if rawPane := strings.TrimSpace(firstQueryParam(r, "pane")); rawPane != "" {
		if idx, parseErr := strconv.Atoi(rawPane); parseErr == nil {
			if idx < 0 {
				writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pane index must be non-negative", nil, reqID)
				return
			}
			input["pane"] = idx
		} else {
			input["pane_selector"] = rawPane
		}
	}

	result, err := kernel.Run(r.Context(), "sessions.health", input)
	if err != nil {
		codeStatus, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, codeStatus, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func resolveBoundedProjectFile(projectDir, rawFile string) (string, error) {
	trimmed := strings.TrimSpace(rawFile)
	if trimmed == "" {
		return "", nil
	}
	baseDir := strings.TrimSpace(projectDir)
	if baseDir == "" {
		return "", fmt.Errorf("prompt_file requires a configured server project_dir")
	}
	rawBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve project_dir: %w", err)
	}
	cleanBase := rawBase
	if resolvedBase, err := filepath.EvalSymlinks(rawBase); err == nil {
		cleanBase = resolvedBase
	}
	var candidate string
	if filepath.IsAbs(trimmed) {
		candidate = filepath.Clean(trimmed)
	} else {
		candidate = filepath.Clean(filepath.Join(cleanBase, trimmed))
	}
	if !pathWithinRoot(cleanBase, candidate) && !pathWithinRoot(rawBase, candidate) {
		return "", fmt.Errorf("prompt_file must reside within active project_dir")
	}
	// Resolve symlinks on the target so a link inside project_dir cannot read
	// arbitrary host files outside the project root.
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("prompt_file is not accessible within active project_dir: %w", err)
	}
	if !pathWithinRoot(cleanBase, resolvedCandidate) {
		return "", fmt.Errorf("prompt_file must reside within active project_dir")
	}
	return resolvedCandidate, nil
}

// handleSessionControllerV1 handles POST /api/v1/sessions/{id}/controller.
func (s *Server) handleSessionControllerV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSessionLifecycleBodyBytes)
	var req SessionControllerRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	resolvedPromptFile, err := resolveBoundedProjectFile(s.projectDirSnapshot(), req.PromptFile)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.controller", map[string]interface{}{
		"session":     sessionID,
		"agent_type":  strings.TrimSpace(req.AgentType),
		"prompt_file": resolvedPromptFile,
		"no_prompt":   req.NoPrompt,
	})
	if err != nil {
		codeStatus, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, codeStatus, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handlePromptPreflightV1 handles POST /api/v1/prompt/preflight.
// Delegates under ADR ORI to robot.GetPreflight.
func (s *Server) handlePromptPreflightV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServePromptPreflightBodyBytes)

	var req PromptPreflightRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "prompt is required", nil, reqID)
		return
	}

	cfg := s.loadEnsembleConfig(s.projectDirSnapshot())
	out, err := robot.GetPreflight(r.Context(), robot.PreflightOptions{
		Prompt: req.Prompt,
		Strict: req.Strict,
		Config: cfg,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	findings := out.Findings
	if findings == nil {
		findings = []robot.PreflightFinding{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"schema_id":        out.SchemaID,
		"schema_version":   out.SchemaVersion,
		"allowed":          out.Success,
		"preview_hash":     out.PreviewHash,
		"preview_len":      out.PreviewLen,
		"estimated_tokens": out.EstimatedTokens,
		"findings":         findings,
		"error_count":      out.ErrorCount,
		"warning_count":    out.WarningCount,
		"info_count":       out.InfoCount,
		"preview":          out.Preview,
		"dcg_available":    out.DCGAvailable,
		"strict":           out.Strict,
		"_agent_hints":     out.AgentHints,
	}, reqID)
}

// handleRecommendAssignV1 handles POST /api/v1/assign.
// Delegates under ADR ORI to robot.GetAssign.
func (s *Server) handleRecommendAssignV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServeOrchestrationBodyBytes)

	var req RecommendAssignRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	session := strings.TrimSpace(req.Session)
	if !validateSessionParam(w, session, reqID) {
		return
	}

	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, req.ProjectDir, reqID)
	if !ok {
		return
	}

	out, err := robot.GetAssign(r.Context(), robot.AssignOptions{
		Session:    session,
		ProjectDir: projectDir,
		Beads:      req.Beads,
		Strategy:   strings.TrimSpace(req.Strategy),
		Relax:      req.Relax,
		Store:      s.projectionStore(),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeErrorResponse(w, robotErrorHTTPStatus(out.ErrorCode), out.ErrorCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize assign plan", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleRecommendRouteV1 handles POST /api/v1/route.
// Delegates under ADR ORI to robot.GetRoute (advisory / NoPersist).
func (s *Server) handleRecommendRouteV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxServeOrchestrationBodyBytes)

	var req RecommendRouteRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	session := strings.TrimSpace(req.Session)
	if !validateSessionParam(w, session, reqID) {
		return
	}

	cfg := s.loadEnsembleConfig(s.projectDirSnapshot())
	out, _ := robot.GetRoute(robot.RouteOptions{
		Session:        session,
		Strategy:       robot.StrategyName(strings.TrimSpace(req.Strategy)),
		AgentType:      strings.TrimSpace(req.AgentType),
		Prompt:         req.Prompt,
		ExcludePanes:   req.ExcludePanes,
		ExcludePaneIDs: req.ExcludePaneIDs,
		LastAgent:      strings.TrimSpace(req.LastAgent),
		Config:         cfg,
		NoPersist:      true,
		Relax:          req.Relax,
		Store:          s.projectionStore(),
	})
	if !out.Success {
		writeErrorResponse(w, robotErrorHTTPStatus(out.ErrorCode), out.ErrorCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize route recommendation", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleGetQuotaStatusV1 handles GET /api/v1/quota.
// Delegates under ADR ORI to robot.GetQuotaStatus / robot.GetQuotaCheck.
func (s *Server) handleGetQuotaStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	provider := strings.TrimSpace(firstQueryParam(r, "provider"))
	projectDir := s.projectDirSnapshot()
	if provider != "" {
		checkOut, checkErr := robot.GetQuotaCheckForProject(provider, projectDir)
		if checkErr != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, checkErr.Error(), nil, reqID)
			return
		}
		if !checkOut.Success {
			writeErrorResponse(w, robotErrorHTTPStatus(checkOut.ErrorCode), checkOut.ErrorCode, checkOut.Error, nil, reqID)
			return
		}
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"provider": checkOut.Provider,
			"detail":   checkOut.Quota,
			"quota": robot.QuotaInfo{
				Providers: map[string]robot.ProviderQuota{
					checkOut.Provider: checkOut.Quota,
				},
			},
		}, reqID)
		return
	}

	out, err := robot.GetQuotaStatusForProject(projectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		writeErrorResponse(w, robotErrorHTTPStatus(out.ErrorCode), out.ErrorCode, out.Error, nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"quota": out.Quota,
	}, reqID)
}
