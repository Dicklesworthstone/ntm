package serve

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/util"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

const (
	maxServeSwarmOpsBodyBytes   = 64 << 10 // 64 KiB
	defaultStoredSummariesLimit = 50
	maxStoredSummariesLimit     = 500
)

// =============================================================================
// Swarm Scale, Rebalance, Review-Queue, Summary, Coordinator & Outbox Shapes
// =============================================================================

// SessionScaleRequest is the JSON request body for POST /api/v1/sessions/{id}/scale.
type SessionScaleRequest struct {
	Targets map[string]int `json:"targets"`
	DryRun  bool           `json:"dry_run,omitempty"`
	Force   bool           `json:"force,omitempty"`
}

// SessionScaleEnvelopeResponse is the REST JSON envelope for POST /api/v1/sessions/{id}/scale.
type SessionScaleEnvelopeResponse struct {
	APIResponse
	SchemaID      string              `json:"schema_id"`
	SchemaVersion string              `json:"schema_version"`
	Session       string              `json:"session"`
	Before        map[string]int      `json:"before"`
	After         map[string]int      `json:"after"`
	Actions       []robot.ScaleAction `json:"actions"`
	DryRun        bool                `json:"dry_run,omitempty"`
	Errors        []string            `json:"errors,omitempty"`
	AgentHints    *robot.AgentHints   `json:"_agent_hints,omitempty"`
}

// SessionRebalanceRequest is the optional JSON body for POST /api/v1/sessions/{id}/rebalance.
type SessionRebalanceRequest struct {
	DryRun            bool    `json:"dry_run,omitempty"`
	Apply             bool    `json:"apply,omitempty"`
	Filter            string  `json:"filter,omitempty"`
	Threshold         float64 `json:"threshold,omitempty"`
	CheckGeometry     bool    `json:"check_geometry,omitempty"`
	GeometryTolerance *int    `json:"geometry_tolerance,omitempty"`
}

// SessionRebalanceEnvelopeResponse is the REST JSON envelope for POST /api/v1/sessions/{id}/rebalance.
type SessionRebalanceEnvelopeResponse struct {
	APIResponse
	SchemaID       string                         `json:"schema_id"`
	SchemaVersion  string                         `json:"schema_version"`
	Session        string                         `json:"session"`
	ImbalanceScore float64                        `json:"imbalance_score"`
	Recommendation string                         `json:"recommendation"`
	Transfers      []robot.RebalanceTransferItem  `json:"transfers"`
	Workloads      []robot.RebalanceWorkload      `json:"workloads"`
	Before         map[string]int                 `json:"before"`
	After          map[string]int                 `json:"after"`
	Geometry       *robot.RebalanceGeometryReport `json:"geometry,omitempty"`
	Applied        bool                           `json:"applied,omitempty"`
	DryRun         bool                           `json:"dry_run,omitempty"`
	AgentHints     *robot.AgentHints              `json:"_agent_hints,omitempty"`
}

// SessionReviewQueueRequest is the optional JSON body for POST /api/v1/sessions/{id}/review-queue.
type SessionReviewQueueRequest struct {
	Filter        string `json:"filter,omitempty"`
	IdleThreshold string `json:"idle_threshold,omitempty"`
	Commits       *int   `json:"commits,omitempty"`
	Dispatch      bool   `json:"dispatch,omitempty"`
	ProjectDir    string `json:"project_dir,omitempty"`
}

// SessionReviewQueueEnvelopeResponse is the REST JSON envelope for POST /api/v1/sessions/{id}/review-queue.
type SessionReviewQueueEnvelopeResponse struct {
	APIResponse
	SchemaID      string                   `json:"schema_id"`
	SchemaVersion string                   `json:"schema_version"`
	Session       string                   `json:"session"`
	ProjectDir    string                   `json:"project_dir,omitempty"`
	IdleAgents    []robot.IdleAgent        `json:"idle_agents"`
	Suggestions   []robot.ReviewSuggestion `json:"suggestions"`
	Dispatched    bool                     `json:"dispatched,omitempty"`
	Sent          int                      `json:"sent,omitempty"`
	Skipped       int                      `json:"skipped,omitempty"`
	AgentHints    *robot.AgentHints        `json:"_agent_hints,omitempty"`
}

// SessionSummaryEnvelopeResponse is the REST JSON envelope for GET /api/v1/sessions/{id}/summary.
type SessionSummaryEnvelopeResponse struct {
	APIResponse
	Session string                `json:"session"`
	Summary *robot.SessionSummary `json:"summary,omitempty"`
}

// StoredSummariesEnvelopeResponse is the REST JSON envelope for GET /api/v1/summaries.
type StoredSummariesEnvelopeResponse struct {
	APIResponse
	ProjectDir string                    `json:"project_dir,omitempty"`
	Summaries  []robot.StoredSummaryFile `json:"summaries"`
	Count      int                       `json:"count"`
}

// SessionCoordinatorDigestRequest is the optional JSON body for POST /api/v1/sessions/{id}/coordinator/digest.
type SessionCoordinatorDigestRequest struct {
	SendDigest bool `json:"send_digest,omitempty"`
}

// SessionCoordinatorEnvelopeResponse is the REST JSON envelope for /api/v1/sessions/{id}/coordinator*.
type SessionCoordinatorEnvelopeResponse struct {
	APIResponse
	SchemaID      string                                  `json:"schema_id"`
	SchemaVersion string                                  `json:"schema_version"`
	Session       string                                  `json:"session"`
	Action        string                                  `json:"action"`
	AgentCount    int                                     `json:"agent_count,omitempty"`
	IdleCount     int                                     `json:"idle_count,omitempty"`
	Agents        map[string]*robot.CoordinatorAgentState `json:"agents,omitempty"`
	Runtime       *robot.CoordinatorRuntimeView           `json:"runtime,omitempty"`
	Config        *robot.CoordinatorConfigView            `json:"config,omitempty"`
	Digest        *robot.CoordinatorDigestSummary         `json:"digest,omitempty"`
	DigestSent    bool                                    `json:"digest_sent,omitempty"`
	Conflicts     []robot.CoordinatorConflict             `json:"conflicts,omitempty"`
	ConflictCount int                                     `json:"conflict_count,omitempty"`
	AgentHints    *robot.AgentHints                       `json:"_agent_hints,omitempty"`
}

// WebhookOutboxMutationRequest is the JSON request body for POST /api/v1/webhooks/outbox/{record}/{retry,archive}.
type WebhookOutboxMutationRequest struct {
	Revision   string `json:"revision"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// WebhookOutboxEnvelopeResponse is the REST JSON envelope for /api/v1/webhooks/outbox*.
type WebhookOutboxEnvelopeResponse struct {
	APIResponse
	SchemaID      string                  `json:"schema_id"`
	SchemaVersion string                  `json:"schema_version"`
	Action        string                  `json:"action"`
	ProjectDir    string                  `json:"project_dir"`
	Snapshot      *webhook.OutboxSnapshot `json:"snapshot,omitempty"`
	Authorized    bool                    `json:"authorized,omitempty"`
	Entry         *webhook.OutboxEntry    `json:"entry,omitempty"`
	Archive       *webhook.OutboxArchive  `json:"archive,omitempty"`
	AgentHints    *robot.AgentHints       `json:"_agent_hints,omitempty"`
}

func (s *Server) registerSwarmOpsRoutes(r chi.Router) {
	// Session-scoped swarm operations (mounted directly under /sessions/{id}/* to avoid chi subrouter shadowing).
	r.With(s.RequirePermission(PermWriteAgents)).Post("/sessions/{id}/scale", s.handleSessionScaleV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/rebalance", s.handleSessionRebalanceV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/review-queue", s.handleSessionReviewQueueV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/summary", s.handleSessionSummaryV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/coordinator", s.handleSessionCoordinatorV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/coordinator/digest", s.handleSessionCoordinatorDigestV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/coordinator/conflicts", s.handleSessionCoordinatorConflictsV1)

	// Stored session summaries index
	r.With(s.RequirePermission(PermReadSessions)).Get("/summaries", s.handleListSummariesV1)

	// Durable project webhook outbox inspection & recovery
	r.With(s.RequirePermission(PermReadHealth)).Get("/webhooks/outbox", s.handleWebhookOutboxListV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/webhooks/outbox/{record}/retry", s.handleWebhookOutboxRetryV1)
	r.With(s.RequirePermission(PermWriteSessions)).Post("/webhooks/outbox/{record}/archive", s.handleWebhookOutboxArchiveV1)
}

func parseSummarySinceQuery(raw string) (time.Duration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 30 * time.Minute, nil
	}
	if dur, err := util.ParseDuration(trimmed); err == nil {
		if dur <= 0 {
			return 0, fmt.Errorf("since duration must be positive")
		}
		return dur, nil
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		dur := time.Since(ts)
		if dur <= 0 {
			return 0, fmt.Errorf("since timestamp must be in the past")
		}
		return dur, nil
	}
	return 0, fmt.Errorf("invalid since parameter %q: expected duration (e.g. 30m, 2h) or RFC3339 timestamp", trimmed)
}

func validateOutboxRecordParam(w http.ResponseWriter, record, reqID string) (string, bool) {
	clean, err := validateSafeWorktreeIdentifier("record", record)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return "", false
	}
	return clean, true
}

// handleSessionScaleV1 handles POST /api/v1/sessions/{id}/scale via kernel command sessions.scale.
func (s *Server) handleSessionScaleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSwarmOpsBodyBytes)
	var req SessionScaleRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if len(req.Targets) == 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "targets map with at least one harness count is required", nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.scale", map[string]any{
		"session": sessionID,
		"targets": req.Targets,
		"dry_run": req.DryRun,
		"force":   req.Force,
	})
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize scale response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionRebalanceV1 handles POST /api/v1/sessions/{id}/rebalance via kernel command sessions.rebalance.
func (s *Server) handleSessionRebalanceV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSwarmOpsBodyBytes)
	var req SessionRebalanceRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if req.Threshold < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "threshold must be non-negative", nil, reqID)
		return
	}

	input := map[string]any{
		"session":        sessionID,
		"dry_run":        req.DryRun,
		"apply":          req.Apply,
		"filter":         strings.TrimSpace(req.Filter),
		"threshold":      req.Threshold,
		"check_geometry": req.CheckGeometry,
	}
	if req.GeometryTolerance != nil {
		if *req.GeometryTolerance < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "geometry_tolerance must be non-negative", nil, reqID)
			return
		}
		input["geometry_tolerance"] = *req.GeometryTolerance
		input["geometry_tolerance_set"] = true
	}

	result, err := kernel.Run(r.Context(), "sessions.rebalance", input)
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize rebalance response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionReviewQueueV1 handles POST /api/v1/sessions/{id}/review-queue via robot.GetReviewQueue.
func (s *Server) handleSessionReviewQueueV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSwarmOpsBodyBytes)
	var req SessionReviewQueueRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	rawDir := strings.TrimSpace(req.ProjectDir)
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project_dir"))
	}
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	opts := robot.ReviewQueueOptions{
		Session:    sessionID,
		ProjectDir: projectDir,
		Filter:     strings.TrimSpace(req.Filter),
		Dispatch:   req.Dispatch,
	}
	if trimmedDur := strings.TrimSpace(req.IdleThreshold); trimmedDur != "" {
		dur, err := util.ParseDuration(trimmedDur)
		if err != nil || dur < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid idle_threshold duration", nil, reqID)
			return
		}
		opts.IdleThreshold = dur
		opts.IdleThresholdSet = true
	}
	if req.Commits != nil {
		if *req.Commits < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "commits must be non-negative", nil, reqID)
			return
		}
		opts.CommitLimit = *req.Commits
		opts.CommitLimitSet = true
	}

	out, err := robot.GetReviewQueue(r.Context(), opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if errCode == "" {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize review-queue response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionSummaryV1 handles GET /api/v1/sessions/{id}/summary via robot.GetSummary.
func (s *Server) handleSessionSummaryV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	since, err := parseSummarySinceQuery(firstQueryParam(r, "since"))
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}

	out, err := robot.GetSummary(robot.SummaryOptions{
		Session: sessionID,
		Since:   since,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if errCode == "" {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]any{
		"session": sessionID,
		"summary": out.Summary,
	}, reqID)
}

// handleListSummariesV1 handles GET /api/v1/summaries via robot.ListStoredSummaries.
func (s *Server) handleListSummariesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	rawDir := strings.TrimSpace(firstQueryParam(r, "project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	limit := defaultStoredSummariesLimit
	if rawLimit := strings.TrimSpace(firstQueryParam(r, "limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxStoredSummariesLimit {
			parsed = maxStoredSummariesLimit
		}
		limit = parsed
	}
	sessionFilter := strings.TrimSpace(firstQueryParam(r, "session"))
	if sessionFilter != "" && sessionFilter != "all" {
		if !validateSessionParam(w, sessionFilter, reqID) {
			return
		}
	}

	files, err := robot.ListStoredSummaries(projectDir)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	filtered := make([]robot.StoredSummaryFile, 0, len(files))
	for _, f := range files {
		if sessionFilter != "" && sessionFilter != "all" && strings.Compare(f.Session, sessionFilter) != 0 {
			continue
		}
		filtered = append(filtered, f)
		if len(filtered) >= limit {
			break
		}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]any{
		"project_dir": projectDir,
		"summaries":   filtered,
		"count":       len(filtered),
	}, reqID)
}

// handleSessionCoordinatorV1 handles GET /api/v1/sessions/{id}/coordinator via kernel command sessions.coordinator.
func (s *Server) handleSessionCoordinatorV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.coordinator", map[string]any{
		"session": sessionID,
		"action":  "status",
	})
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize coordinator response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionCoordinatorDigestV1 handles POST /api/v1/sessions/{id}/coordinator/digest via kernel command sessions.coordinator_digest.
func (s *Server) handleSessionCoordinatorDigestV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSwarmOpsBodyBytes)
	var req SessionCoordinatorDigestRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.coordinator_digest", map[string]any{
		"session":     sessionID,
		"action":      "digest",
		"send_digest": req.SendDigest,
	})
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize coordinator digest response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionCoordinatorConflictsV1 handles GET /api/v1/sessions/{id}/coordinator/conflicts via kernel command sessions.coordinator_conflicts.
func (s *Server) handleSessionCoordinatorConflictsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")
	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.coordinator_conflicts", map[string]any{
		"session": sessionID,
		"action":  "conflicts",
	})
	if err != nil {
		statusCode, errCode := kernelSessionErrorHTTP(err)
		writeErrorResponse(w, statusCode, errCode, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize coordinator conflicts response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleWebhookOutboxListV1 handles GET /api/v1/webhooks/outbox via robot.GetWebhookOutbox.
func (s *Server) handleWebhookOutboxListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	rawDir := strings.TrimSpace(firstQueryParam(r, "project_dir"))
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	redactCfg := s.requestRedactionConfig(r)
	out, err := robot.GetWebhookOutbox(r.Context(), robot.WebhookOutboxOptions{
		Action:     "list",
		ProjectDir: projectDir,
		RedactCfg:  &redactCfg,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if errCode == "" {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize webhook outbox response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

func (s *Server) handleWebhookOutboxMutate(w http.ResponseWriter, r *http.Request, action string) {
	reqID := requestIDFromContext(r.Context())
	record, ok := validateOutboxRecordParam(w, chi.URLParam(r, "record"), reqID)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSwarmOpsBodyBytes)
	var req WebhookOutboxMutationRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	revision := strings.TrimSpace(req.Revision)
	if revision == "" {
		revision = strings.TrimSpace(firstQueryParam(r, "revision"))
	}
	if revision == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "revision is required", nil, reqID)
		return
	}

	rawDir := strings.TrimSpace(req.ProjectDir)
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project_dir"))
	}
	if rawDir == "" {
		rawDir = strings.TrimSpace(firstQueryParam(r, "project"))
	}
	projectDir, ok := s.resolveOptionalEnsembleAnalysisDir(w, rawDir, reqID)
	if !ok {
		return
	}

	redactCfg := s.requestRedactionConfig(r)
	out, err := robot.GetWebhookOutbox(r.Context(), robot.WebhookOutboxOptions{
		Action:     action,
		ProjectDir: projectDir,
		Record:     record,
		Revision:   revision,
		RedactCfg:  &redactCfg,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if !out.Success {
		statusCode := robotErrorHTTPStatus(out.ErrorCode)
		errCode := out.ErrorCode
		if errCode == "" {
			errCode = ErrCodeInternalError
		}
		writeErrorResponse(w, statusCode, errCode, out.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(out)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize webhook outbox mutation response", nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleWebhookOutboxRetryV1 handles POST /api/v1/webhooks/outbox/{record}/retry.
func (s *Server) handleWebhookOutboxRetryV1(w http.ResponseWriter, r *http.Request) {
	s.handleWebhookOutboxMutate(w, r, "retry")
}

// handleWebhookOutboxArchiveV1 handles POST /api/v1/webhooks/outbox/{record}/archive.
func (s *Server) handleWebhookOutboxArchiveV1(w http.ResponseWriter, r *http.Request) {
	s.handleWebhookOutboxMutate(w, r, "archive")
}
