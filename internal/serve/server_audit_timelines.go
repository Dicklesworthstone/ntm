package serve

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bundle"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	defaultServeTimelineListLimit  = 20
	maxServeTimelineListLimit      = 500
	defaultServeSupportBundleLines = 500
	maxServeSupportBundleLines     = 10000
	defaultServeSupportBundleMaxMB = 25
	maxServeSupportBundleMaxMB     = 50
	maxServeAuditVerifyBodyBytes   = 64 << 10 // 64 KiB
	maxServeSupportBundleBodyBytes = 64 << 10 // 64 KiB
	maxServeSupportBundleReadBytes = maxServeSupportBundleMaxMB << 20
)

// =============================================================================
// Audit, Timelines & Support Bundle REST Request & Response Shapes (OpenAPI 3.1)
// =============================================================================

// HTTPAuditEnvelopeResponse is the REST JSON envelope for GET /api/v1/audit.
type HTTPAuditEnvelopeResponse struct {
	APIResponse
	Records      []AuditRecord               `json:"records"`
	Count        int                         `json:"count"`
	TotalMatches int                         `json:"total_matches"`
	HasMore      bool                        `json:"has_more"`
	Limit        int                         `json:"limit"`
	Offset       int                         `json:"offset"`
	Pagination   *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints   *robot.PaginationAgentHints `json:"_agent_hints,omitempty"`
}

// SessionAuditEnvelopeResponse is the REST JSON envelope for GET /api/v1/audit/sessions.
type SessionAuditEnvelopeResponse struct {
	APIResponse
	SchemaID      string                      `json:"schema_id"`
	SchemaVersion string                      `json:"schema_version"`
	Action        string                      `json:"action"`
	Session       string                      `json:"session"`
	AuditDir      string                      `json:"audit_dir,omitempty"`
	Logs          []robot.AuditLogFileSummary `json:"logs"`
	Entries       []audit.AuditEntry          `json:"entries"`
	Count         int                         `json:"count"`
	TotalMatches  int                         `json:"total_matches"`
	Scanned       int                         `json:"scanned"`
	ScanCapped    bool                        `json:"scan_capped,omitempty"`
	DurationMs    int64                       `json:"duration_ms,omitempty"`
	HasMore       bool                        `json:"has_more"`
	Sources       []state.SourceHealth        `json:"sources,omitempty"`
	Pagination    *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints    *robot.AgentHints           `json:"_agent_hints,omitempty"`
}

// VerifySessionAuditRequest is the JSON request body for POST /api/v1/audit/verify.
type VerifySessionAuditRequest struct {
	Session string `json:"session"`
}

// SessionAuditVerifyEnvelopeResponse is the REST JSON envelope for POST /api/v1/audit/verify.
type SessionAuditVerifyEnvelopeResponse struct {
	APIResponse
	SchemaID      string                        `json:"schema_id"`
	SchemaVersion string                        `json:"schema_version"`
	Session       string                        `json:"session"`
	Verified      bool                          `json:"verified"`
	Files         []robot.AuditVerifyFileResult `json:"files"`
	Count         int                           `json:"count"`
	VerifyError   string                        `json:"verify_error,omitempty"`
	AgentHints    *robot.AgentHints             `json:"_agent_hints,omitempty"`
}

// TimelinesEnvelopeResponse is the REST JSON envelope for GET /api/v1/timelines,
// GET /api/v1/timelines/stats, and GET /api/v1/timelines/{sessionId}.
type TimelinesEnvelopeResponse struct {
	APIResponse
	SchemaID       string                        `json:"schema_id"`
	SchemaVersion  string                        `json:"schema_version"`
	Action         string                        `json:"action"`
	Session        string                        `json:"session"`
	Timelines      []state.TimelineInfo          `json:"timelines"`
	TotalCount     int                           `json:"total_count"`
	Showing        int                           `json:"showing"`
	Info           *state.TimelineInfo           `json:"info,omitempty"`
	Events         []state.AgentEvent            `json:"events"`
	SessionStats   *robot.TimelineEventStats     `json:"session_stats,omitempty"`
	AggregateStats *robot.TimelineAggregateStats `json:"aggregate_stats,omitempty"`
	AgentHints     *robot.AgentHints             `json:"_agent_hints,omitempty"`
}

// CreateSupportBundleRequest is the JSON request body for POST /api/v1/support-bundle.
type CreateSupportBundleRequest struct {
	Session    string `json:"session,omitempty"`
	Format     string `json:"format,omitempty"`
	Since      string `json:"since,omitempty"`
	Lines      int    `json:"lines,omitempty"`
	MaxSizeMB  int    `json:"max_size_mb,omitempty"`
	RedactMode string `json:"redact_mode,omitempty"`
}

// SupportBundleEnvelopeResponse is the REST JSON envelope for POST /api/v1/support-bundle.
type SupportBundleEnvelopeResponse struct {
	APIResponse
	Filename          string                        `json:"filename"`
	Format            string                        `json:"format"`
	FileCount         int                           `json:"file_count"`
	Size              int64                         `json:"size"`
	ContentType       string                        `json:"content_type"`
	Data              string                        `json:"data"`
	PrivacyMode       bool                          `json:"privacy_mode"`
	PrivacySessions   []string                      `json:"privacy_sessions,omitempty"`
	ContentSuppressed bool                          `json:"content_suppressed"`
	Encrypted         bool                          `json:"encrypted"`
	RedactionSummary  *robot.BundleRedactionSummary `json:"redaction_summary,omitempty"`
	Warnings          []string                      `json:"warnings,omitempty"`
}

func (s *Server) registerAuditAndTimelineRoutes(r chi.Router) {
	r.With(s.RequirePermission(PermReadApprovals)).Get("/audit", s.handleListAuditRecordsV1)
	r.With(s.RequirePermission(PermReadApprovals)).Get("/audit/sessions", s.handleGetSessionAuditV1)
	r.With(s.RequirePermission(PermReadApprovals)).Post("/audit/verify", s.handleVerifySessionAuditV1)

	r.With(s.RequirePermission(PermReadSessions)).Get("/timelines", s.handleListTimelinesV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/timelines/stats", s.handleTimelineStatsV1)
	r.With(s.RequirePermission(PermReadSessions)).Get("/timelines/{sessionId}", s.handleGetSessionTimelineV1)

	r.With(s.RequirePermission(PermReadSessions)).Post("/support-bundle", s.handleCreateSupportBundleV1)
}

func mapRobotAuditError(out *robot.AuditOutput) (int, string, string) {
	if out == nil {
		return http.StatusInternalServerError, ErrCodeInternalError, "audit operation returned no result"
	}
	msg := strings.TrimSpace(out.Error)
	if msg == "" {
		msg = "audit operation failed"
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

func mapRobotTimelineError(out *robot.TimelineOutput) (int, string, string) {
	if out == nil {
		return http.StatusInternalServerError, ErrCodeInternalError, "timeline operation returned no result"
	}
	msg := strings.TrimSpace(out.Error)
	if msg == "" {
		msg = "timeline operation failed"
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

func toServedTimelinesEnvelope(out *robot.TimelineOutput) TimelinesEnvelopeResponse {
	timelines := out.Timelines
	if timelines == nil {
		timelines = []state.TimelineInfo{}
	}
	events := out.Events
	if events == nil {
		events = []state.AgentEvent{}
	}
	return TimelinesEnvelopeResponse{
		SchemaID:       out.SchemaID,
		SchemaVersion:  out.SchemaVersion,
		Action:         out.Action,
		Session:        out.Session,
		Timelines:      timelines,
		TotalCount:     out.TotalCount,
		Showing:        out.Showing,
		Info:           out.Info,
		Events:         events,
		SessionStats:   out.SessionStats,
		AggregateStats: out.AggregateStats,
		AgentHints:     out.AgentHints,
	}
}

// handleListAuditRecordsV1 handles GET /api/v1/audit.
func (s *Server) handleListAuditRecordsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	limit := defaultAuditQueryLimit
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		limit = parsed
	}

	offset := 0
	if rawOffset := strings.TrimSpace(q.Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "offset must be a non-negative integer", nil, reqID)
			return
		}
		offset = parsed
	}

	userID := strings.TrimSpace(q.Get("user_id"))
	if userID == "" {
		userID = strings.TrimSpace(q.Get("user"))
	}
	sessionID := strings.TrimSpace(q.Get("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(q.Get("session"))
	}

	filter := AuditFilter{
		UserID:     userID,
		Action:     AuditAction(strings.TrimSpace(q.Get("action"))),
		Resource:   strings.TrimSpace(q.Get("resource")),
		SessionID:  sessionID,
		RequestID:  strings.TrimSpace(q.Get("request_id")),
		ApprovalID: strings.TrimSpace(q.Get("approval_id")),
		Limit:      limit,
		Offset:     offset,
	}

	if rawSince := strings.TrimSpace(q.Get("since")); rawSince != "" {
		parsedSince, err := robot.ParseAuditTimeArg(rawSince)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid since filter: "+err.Error(), nil, reqID)
			return
		}
		filter.Since = parsedSince.UTC()
	}
	if rawUntil := strings.TrimSpace(q.Get("until")); rawUntil != "" {
		parsedUntil, err := robot.ParseAuditTimeArg(rawUntil)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid until filter: "+err.Error(), nil, reqID)
			return
		}
		filter.Until = parsedUntil.UTC()
	}

	var (
		records    []AuditRecord
		totalCount int
		err        error
	)
	if s.auditStore != nil {
		records, totalCount, err = s.auditStore.Query(r.Context(), filter)
	} else {
		auditDir, dirErr := DefaultAuditDir()
		if dirErr != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, dirErr.Error(), nil, reqID)
			return
		}
		records, totalCount, err = QueryAuditDBFile(r.Context(), filepath.Join(auditDir, "audit.db"), filter)
	}
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if records == nil {
		records = []AuditRecord{}
	}

	normLimit, normOffset := NormalizeAuditQueryPage(limit, offset)
	hasMore := normOffset+len(records) < totalCount
	var nextCursor *int
	if hasMore {
		next := normOffset + len(records)
		nextCursor = &next
	}
	pageInfo := &robot.PaginationInfo{
		Limit:      normLimit,
		Offset:     normOffset,
		Count:      len(records),
		Total:      totalCount,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}

	resp := HTTPAuditEnvelopeResponse{
		Records:      records,
		Count:        len(records),
		TotalMatches: totalCount,
		HasMore:      hasMore,
		Limit:        normLimit,
		Offset:       normOffset,
		Pagination:   pageInfo,
		AgentHints:   robot.PaginationHints(pageInfo),
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(records) == 0 {
		payload["records"] = []AuditRecord{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleGetSessionAuditV1 handles GET /api/v1/audit/sessions.
func (s *Server) handleGetSessionAuditV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	session := strings.TrimSpace(q.Get("session"))
	action := strings.TrimSpace(q.Get("action"))
	if action == "verify" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "use POST /api/v1/audit/verify to verify session hash chains", nil, reqID)
		return
	}

	limit := 50
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxAuditQueryLimit {
			parsed = maxAuditQueryLimit
		}
		limit = parsed
	}

	offset := 0
	if rawOffset := strings.TrimSpace(q.Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "offset must be a non-negative integer", nil, reqID)
			return
		}
		offset = parsed
	}

	days := 30
	if rawDays := strings.TrimSpace(q.Get("days")); rawDays != "" {
		parsed, err := strconv.Atoi(rawDays)
		if err != nil || parsed < 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "days must be a non-negative integer", nil, reqID)
			return
		}
		days = parsed
	}

	out, err := robot.GetAudit(r.Context(), robot.AuditOptions{
		Session:    session,
		Action:     action,
		Pattern:    strings.TrimSpace(q.Get("search")),
		EventTypes: strings.TrimSpace(q.Get("type")),
		Actors:     strings.TrimSpace(q.Get("actor")),
		Since:      strings.TrimSpace(q.Get("since")),
		Until:      strings.TrimSpace(q.Get("until")),
		Days:       days,
		Limit:      limit,
		Offset:     offset,
		AuditDir:   s.auditLogsDir,
		Store:      s.projectionStore(),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotAuditError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	logs := out.Logs
	if logs == nil {
		logs = []robot.AuditLogFileSummary{}
	}
	entries := out.Entries
	if entries == nil {
		entries = []audit.AuditEntry{}
	}

	resp := SessionAuditEnvelopeResponse{
		SchemaID:      out.SchemaID,
		SchemaVersion: out.SchemaVersion,
		Action:        out.Action,
		Session:       out.Session,
		AuditDir:      out.AuditDir,
		Logs:          logs,
		Entries:       entries,
		Count:         out.Count,
		TotalMatches:  out.TotalMatches,
		Scanned:       out.Scanned,
		ScanCapped:    out.ScanCapped,
		DurationMs:    out.DurationMs,
		HasMore:       out.HasMore,
		Sources:       out.Sources,
		Pagination:    out.Pagination,
		AgentHints:    out.AgentHints,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(logs) == 0 {
		payload["logs"] = []robot.AuditLogFileSummary{}
	}
	if len(entries) == 0 {
		payload["entries"] = []audit.AuditEntry{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleVerifySessionAuditV1 handles POST /api/v1/audit/verify.
func (s *Server) handleVerifySessionAuditV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxServeAuditVerifyBodyBytes)
	var req VerifySessionAuditRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	session := strings.TrimSpace(req.Session)
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session is required", nil, reqID)
		return
	}

	out, err := robot.GetAudit(r.Context(), robot.AuditOptions{
		Session:  session,
		Action:   "verify",
		Verify:   true,
		AuditDir: s.auditLogsDir,
		Store:    s.projectionStore(),
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	// When out.Verified != nil, verification ran to completion and inspected the
	// log files; return 200 OK with verified=true/false and per-file diagnostics
	// so operators and Web UI panels can inspect which file broke the hash chain.
	if out == nil || out.Verified == nil {
		status, code, msg := mapRobotAuditError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	files := out.Files
	if files == nil {
		files = []robot.AuditVerifyFileResult{}
	}

	SetAuditResource(r, "audit", out.Session)
	SetAuditAction(r, AuditActionExecute)
	SetAuditDetails(r, fmt.Sprintf("verified audit hash chain for session %s (verified=%t, files=%d)", out.Session, *out.Verified, len(files)))

	resp := SessionAuditVerifyEnvelopeResponse{
		SchemaID:      out.SchemaID,
		SchemaVersion: out.SchemaVersion,
		Session:       out.Session,
		Verified:      *out.Verified,
		Files:         files,
		Count:         len(files),
		VerifyError:   strings.TrimSpace(out.Error),
		AgentHints:    out.AgentHints,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(files) == 0 {
		payload["files"] = []robot.AuditVerifyFileResult{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleListTimelinesV1 handles GET /api/v1/timelines.
func (s *Server) handleListTimelinesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	q := r.URL.Query()

	limit := defaultServeTimelineListLimit
	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "limit must be a positive integer", nil, reqID)
			return
		}
		if parsed > maxServeTimelineListLimit {
			parsed = maxServeTimelineListLimit
		}
		limit = parsed
	}

	out, err := robot.GetTimeline(r.Context(), robot.TimelineOptions{
		Action:  "list",
		Limit:   limit,
		BaseDir: s.timelineBaseDir,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotTimelineError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	resp := toServedTimelinesEnvelope(out)
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(resp.Timelines) == 0 {
		payload["timelines"] = []state.TimelineInfo{}
	}
	if len(resp.Events) == 0 {
		payload["events"] = []state.AgentEvent{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleTimelineStatsV1 handles GET /api/v1/timelines/stats.
func (s *Server) handleTimelineStatsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	out, err := robot.GetTimeline(r.Context(), robot.TimelineOptions{
		Action:  "stats",
		Stats:   true,
		BaseDir: s.timelineBaseDir,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotTimelineError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	resp := toServedTimelinesEnvelope(out)
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(resp.Timelines) == 0 {
		payload["timelines"] = []state.TimelineInfo{}
	}
	if len(resp.Events) == 0 {
		payload["events"] = []state.AgentEvent{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleGetSessionTimelineV1 handles GET /api/v1/timelines/{sessionId}.
func (s *Server) handleGetSessionTimelineV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionId"))
	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "sessionId is required", nil, reqID)
		return
	}

	includeEvents, ok := parseAnalyticsSessionsParam(r.URL.Query().Get("events"))
	if !ok {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "events must be true or false", nil, reqID)
		return
	}

	out, err := robot.GetTimeline(r.Context(), robot.TimelineOptions{
		Session:       sessionID,
		Action:        "show",
		IncludeEvents: includeEvents,
		BaseDir:       s.timelineBaseDir,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if out == nil || !out.Success {
		status, code, msg := mapRobotTimelineError(out)
		writeErrorResponse(w, status, code, msg, nil, reqID)
		return
	}

	resp := toServedTimelinesEnvelope(out)
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if len(resp.Timelines) == 0 {
		payload["timelines"] = []state.TimelineInfo{}
	}
	if len(resp.Events) == 0 {
		payload["events"] = []state.AgentEvent{}
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

// handleCreateSupportBundleV1 handles POST /api/v1/support-bundle.
func (s *Server) handleCreateSupportBundleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxServeSupportBundleBodyBytes)
	var req CreateSupportBundleRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	session := strings.TrimSpace(req.Session)
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid session name: "+err.Error(), nil, reqID)
			return
		}
	}

	var (
		archiveFormat bundle.Format
		contentType   string
	)
	switch strings.ToLower(strings.TrimSpace(req.Format)) {
	case "", "zip":
		archiveFormat = bundle.FormatZip
		contentType = "application/zip"
	case "tar.gz", "tgz":
		archiveFormat = bundle.FormatTarGz
		contentType = "application/gzip"
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "format must be zip or tar.gz", nil, reqID)
		return
	}

	// Enforce disclosure-safe redaction over HTTP: never emit unredacted archives.
	redactMode := strings.ToLower(strings.TrimSpace(req.RedactMode))
	switch redactMode {
	case "":
		redactMode = "redact"
	case "redact", "block":
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "redact_mode over HTTP must be redact or block", nil, reqID)
		return
	}

	if req.Lines < 0 || req.Lines > maxServeSupportBundleLines {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("lines must be between 0 and %d", maxServeSupportBundleLines), nil, reqID)
		return
	}
	lines := req.Lines
	if lines == 0 {
		lines = defaultServeSupportBundleLines
	}

	if req.MaxSizeMB < 0 || req.MaxSizeMB > maxServeSupportBundleMaxMB {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("max_size_mb must be between 0 and %d", maxServeSupportBundleMaxMB), nil, reqID)
		return
	}
	maxSizeMB := req.MaxSizeMB
	if maxSizeMB == 0 {
		maxSizeMB = defaultServeSupportBundleMaxMB
	}

	version := strings.TrimSpace(s.version)
	if version == "" {
		version = "dev"
	}
	loadConfig := s.recoveryConfig
	if loadConfig == nil {
		loadConfig = func(project string) (*config.Config, error) {
			return config.LoadAssignmentPolicyStrict(project, config.DefaultPath(), false)
		}
	}
	effective, err := loadConfig(s.projectDirSnapshot())
	if err != nil || effective == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "cannot load support bundle encryption policy", nil, reqID)
		return
	}

	bundleOut, archiveBytes, err := robot.GenerateSupportBundleArchive(
		robot.SupportBundleOptions{
			Session:     session,
			Format:      string(archiveFormat),
			Since:       strings.TrimSpace(req.Since),
			Lines:       lines,
			MaxSizeMB:   maxSizeMB,
			RedactMode:  redactMode,
			AllSessions: session == "",
			NTMVersion:  version,
			Encryption:  &effective.Encryption,
		},
		maxServeSupportBundleReadBytes,
	)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if bundleOut == nil || !bundleOut.Success {
		errStatus, errCode, errMsg := mapRobotSupportBundleError(bundleOut)
		writeErrorResponse(w, errStatus, errCode, errMsg, nil, reqID)
		return
	}
	if int64(len(archiveBytes)) > maxServeSupportBundleReadBytes {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "generated support bundle exceeds HTTP response size limit", nil, reqID)
		return
	}

	displayName := filepath.Base(bundle.SuggestOutputPath(session, archiveFormat))
	if bundleOut.Encrypted {
		displayName += ".enc"
		contentType = "application/octet-stream"
	}
	targetResource := session
	if targetResource == "" {
		targetResource = "all"
	}
	SetAuditResource(r, "support-bundle", targetResource)
	SetAuditAction(r, AuditActionCreate)
	SetAuditDetails(r, fmt.Sprintf("generated redacted support bundle %s (%d files, %d bytes)", displayName, bundleOut.FileCount, len(archiveBytes)))

	resp := SupportBundleEnvelopeResponse{
		Filename:          displayName,
		Format:            bundleOut.Format,
		FileCount:         bundleOut.FileCount,
		Size:              int64(len(archiveBytes)),
		ContentType:       contentType,
		Data:              base64.StdEncoding.EncodeToString(archiveBytes),
		PrivacyMode:       bundleOut.PrivacyMode,
		PrivacySessions:   bundleOut.PrivacySessions,
		ContentSuppressed: bundleOut.ContentSuppressed,
		Encrypted:         bundleOut.Encrypted,
		RedactionSummary:  bundleOut.RedactionSummary,
		Warnings:          bundleOut.Warnings,
	}
	payload, err := toJSONMap(resp)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	writeSuccessResponse(w, http.StatusOK, payload, reqID)
}

func mapRobotSupportBundleError(bundleOut *robot.SupportBundleOutput) (int, string, string) {
	if bundleOut == nil {
		return http.StatusInternalServerError, ErrCodeInternalError, "failed to generate support bundle"
	}
	msg := strings.TrimSpace(bundleOut.Error)
	if msg == "" {
		msg = "failed to generate support bundle"
	}
	switch bundleOut.ErrorCode {
	case robot.ErrCodeInvalidFlag:
		return http.StatusBadRequest, ErrCodeBadRequest, msg
	default:
		return http.StatusInternalServerError, ErrCodeInternalError, msg
	}
}
