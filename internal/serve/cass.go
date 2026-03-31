// Package serve provides REST API endpoints for CASS and CM (Memory) integration.
// cass.go implements the /api/v1/cass and /api/v1/memory endpoints.
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/process"
)

// CASS/Memory-specific error codes
const (
	ErrCodeCASSUnavailable   = "CASS_UNAVAILABLE"
	ErrCodeMemoryUnavailable = "MEMORY_UNAVAILABLE"
	ErrCodeDaemonNotRunning  = "DAEMON_NOT_RUNNING"
	ErrCodeDaemonRunning     = "DAEMON_ALREADY_RUNNING"
	ErrCodeSearchFailed      = "SEARCH_FAILED"
	ErrCodeContextFailed     = "CONTEXT_FAILED"
	ErrCodeOutcomeFailed     = "OUTCOME_FAILED"
	ErrCodePrivacyFailed     = "PRIVACY_FAILED"
)

// MemoryDaemonState tracks the memory daemon status
type MemoryDaemonState string

const (
	DaemonStateStopped  MemoryDaemonState = "stopped"
	DaemonStateStarting MemoryDaemonState = "starting"
	DaemonStateRunning  MemoryDaemonState = "running"
	DaemonStateStopping MemoryDaemonState = "stopping"
)

// MemoryDaemonInfo holds information about the memory daemon
type MemoryDaemonInfo struct {
	State     MemoryDaemonState `json:"state"`
	PID       int               `json:"pid,omitempty"`
	Port      int               `json:"port,omitempty"`
	StartedAt *time.Time        `json:"started_at,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
}

// PrivacySettings represents cross-agent privacy settings
type PrivacySettings struct {
	Enabled       bool     `json:"enabled"`
	AllowedAgents []string `json:"allowed_agents,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// MemoryRule represents a rule from the CM playbook
type MemoryRule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
	Source   string `json:"source,omitempty"`
}

// MemoryStore provides in-memory caching for memory operations
type MemoryStore struct {
	mu         sync.RWMutex
	daemonInfo *MemoryDaemonInfo
	privacy    *PrivacySettings
	lastCheck  time.Time
}

type limitedLogBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedLogBuffer(limit int) *limitedLogBuffer {
	if limit <= 0 {
		limit = 8 * 1024
	}
	return &limitedLogBuffer{limit: limit}
}

func (b *limitedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	originalLen := len(p)
	if b.limit <= 0 {
		return originalLen, nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return originalLen, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return originalLen, nil
}

func (b *limitedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.truncated {
		return b.buf.String()
	}
	return b.buf.String() + "\n...[truncated]"
}

// NewMemoryStore creates a new memory store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		daemonInfo: &MemoryDaemonInfo{State: DaemonStateStopped},
	}
}

// GetDaemonInfo returns current daemon info
func (s *MemoryStore) GetDaemonInfo() *MemoryDaemonInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.daemonInfo
}

// SetDaemonInfo updates daemon info
func (s *MemoryStore) SetDaemonInfo(info *MemoryDaemonInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.daemonInfo = info
}

// Global memory store
var (
	memoryStore = NewMemoryStore()
	// A long-lived daemon must not inherit a request-scoped or function-scoped
	// context, otherwise it will be killed as soon as startup returns.
	memoryDaemonCommand = func(projectDir string, port int) *exec.Cmd {
		cmd := exec.Command("cm", "serve", "--port", fmt.Sprintf("%d", port))
		cmd.Dir = projectDir
		return cmd
	}
	memoryDaemonStartupDelay = 2 * time.Second
)

// Request/Response types

// CASSSearchRequest is the request body for POST /api/v1/cass/search
type CASSSearchRequest struct {
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Fields    string `json:"fields,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Aggregate string `json:"aggregate,omitempty"`
	Explain   bool   `json:"explain,omitempty"`
	Highlight bool   `json:"highlight,omitempty"`
}

// CASSStatusResponse is the response for GET /api/v1/cass/status
type CASSStatusResponse struct {
	Installed     bool   `json:"installed"`
	Healthy       bool   `json:"healthy"`
	Version       string `json:"version,omitempty"`
	IndexSize     int64  `json:"index_size,omitempty"`
	DocCount      int64  `json:"doc_count,omitempty"`
	LastIndexed   string `json:"last_indexed,omitempty"`
	NeedsReindex  bool   `json:"needs_reindex,omitempty"`
	ReindexReason string `json:"reindex_reason,omitempty"`
}

// MemoryContextRequest is the request body for POST /api/v1/memory/context
type MemoryContextRequest struct {
	Task        string `json:"task"`
	MaxRules    int    `json:"max_rules,omitempty"`
	MaxSnippets int    `json:"max_snippets,omitempty"`
}

// MemoryOutcomeRequest is the request body for POST /api/v1/memory/outcome
type MemoryOutcomeRequest struct {
	Status    string   `json:"status"` // success, failure, partial
	RuleIDs   []string `json:"rule_ids,omitempty"`
	Sentiment string   `json:"sentiment,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

// MemoryDaemonRequest is the request body for POST /api/v1/memory/daemon/start
type MemoryDaemonRequest struct {
	Port      int    `json:"port,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// PrivacyUpdateRequest is the request body for PUT /api/v1/memory/privacy
type PrivacyUpdateRequest struct {
	Enabled bool     `json:"enabled"`
	Agents  []string `json:"agents,omitempty"`
}

// registerCASSRoutes registers all CASS and memory-related routes
func (s *Server) registerCASSRoutes(r chi.Router) {
	r.Route("/cass", func(r chi.Router) {
		// Read operations
		r.With(s.RequirePermission(PermReadMemory)).Get("/status", s.handleCASSStatus)
		r.With(s.RequirePermission(PermReadMemory)).Get("/capabilities", s.handleCASSCapabilities)
		r.With(s.RequirePermission(PermReadMemory)).Post("/search", s.handleCASSSearch)
		r.With(s.RequirePermission(PermReadMemory)).Get("/insights", s.handleCASSInsights)
		r.With(s.RequirePermission(PermReadMemory)).Get("/timeline", s.handleCASSTimeline)
		r.With(s.RequirePermission(PermReadMemory)).Get("/preview", s.handleCASSPreview)
	})

	r.Route("/memory", func(r chi.Router) {
		// Daemon management
		r.Route("/daemon", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadMemory)).Get("/status", s.handleMemoryDaemonStatus)
			r.With(s.RequirePermission(PermWriteMemory)).Post("/start", s.handleMemoryDaemonStart)
			r.With(s.RequirePermission(PermWriteMemory)).Post("/stop", s.handleMemoryDaemonStop)
		})

		// Context and outcome
		r.With(s.RequirePermission(PermReadMemory)).Post("/context", s.handleMemoryContext)
		r.With(s.RequirePermission(PermWriteMemory)).Post("/outcome", s.handleMemoryOutcome)

		// Privacy settings
		r.With(s.RequirePermission(PermReadMemory)).Get("/privacy", s.handleMemoryPrivacyGet)
		r.With(s.RequirePermission(PermWriteMemory)).Put("/privacy", s.handleMemoryPrivacyUpdate)

		// Rules
		r.With(s.RequirePermission(PermReadMemory)).Get("/rules", s.handleMemoryRules)
	})
}

// CASS Handlers

// handleCASSStatus returns the current CASS status
func (s *Server) handleCASSStatus(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("cass status request", "request_id", reqID)

	client := cass.NewClient()
	installed := client.IsInstalled()

	status := CASSStatusResponse{
		Installed: installed,
	}

	if installed {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Get full status
		if statusResp, err := client.Status(ctx); err == nil {
			status.Healthy = statusResp.IsHealthy()
			status.Version = statusResp.Version
			status.IndexSize = statusResp.Index.SizeBytes
			status.DocCount = statusResp.Index.DocCount
			if !statusResp.Index.LastUpdated.IsZero() {
				status.LastIndexed = statusResp.Index.LastUpdated.Format(time.RFC3339)
			}
		}

		// Check if reindex needed
		needsReindex, reason := client.NeedsReindex(ctx)
		status.NeedsReindex = needsReindex
		status.ReindexReason = reason
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"installed":      status.Installed,
		"healthy":        status.Healthy,
		"version":        status.Version,
		"index_size":     status.IndexSize,
		"doc_count":      status.DocCount,
		"last_indexed":   status.LastIndexed,
		"needs_reindex":  status.NeedsReindex,
		"reindex_reason": status.ReindexReason,
	}, reqID)
}

// handleCASSCapabilities returns CASS capabilities
func (s *Server) handleCASSCapabilities(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	client := cass.NewClient()
	if !client.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeCASSUnavailable,
			"CASS is not installed", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	caps, err := client.Capabilities(ctx)
	if err != nil {
		slog.Warn("failed to get cass capabilities", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeCASSUnavailable,
			"Failed to get CASS capabilities", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"crate_version":    caps.CrateVersion,
		"api_version":      caps.APIVersion,
		"contract_version": caps.ContractVersion,
		"features":         caps.Features,
		"connectors":       caps.Connectors,
		"limits": map[string]interface{}{
			"max_query_length":       caps.Limits.MaxQueryLength,
			"max_results":            caps.Limits.MaxResults,
			"max_concurrent_queries": caps.Limits.MaxConcurrentQueries,
			"rate_limit_per_minute":  caps.Limits.RateLimitPerMinute,
		},
	}, reqID)
}

// handleCASSSearch performs a search query
func (s *Server) handleCASSSearch(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	client := cass.NewClient()
	if !client.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeCASSUnavailable,
			"CASS is not installed", nil, reqID)
		return
	}

	var req CASSSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	if req.Query == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Query is required", nil, reqID)
		return
	}

	slog.Info("cass search request", "request_id", reqID, "query", req.Query)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Cap user-provided limits to prevent resource exhaustion
	searchLimit := req.Limit
	if searchLimit <= 0 || searchLimit > 500 {
		searchLimit = 500
	}
	searchMaxTokens := req.MaxTokens
	if searchMaxTokens > 50000 {
		searchMaxTokens = 50000
	}
	opts := cass.SearchOptions{
		Query:     req.Query,
		Limit:     searchLimit,
		Offset:    req.Offset,
		Agent:     req.Agent,
		Workspace: req.Workspace,
		Since:     req.Since,
		Until:     req.Until,
		Cursor:    req.Cursor,
		Fields:    req.Fields,
		MaxTokens: searchMaxTokens,
		Aggregate: req.Aggregate,
		Explain:   req.Explain,
		Highlight: req.Highlight,
	}

	result, err := client.Search(ctx, opts)
	if err != nil {
		slog.Warn("cass search failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeSearchFailed,
			"Search failed", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	// Convert hits to maps
	hits := make([]map[string]interface{}, 0, len(result.Hits))
	for _, hit := range result.Hits {
		h := map[string]interface{}{
			"source_path": hit.SourcePath,
			"agent":       hit.Agent,
			"workspace":   hit.Workspace,
			"title":       hit.Title,
			"score":       hit.Score,
			"snippet":     hit.Snippet,
			"match_type":  hit.MatchType,
		}
		if hit.LineNumber != nil {
			h["line_number"] = *hit.LineNumber
		}
		if hit.Content != "" {
			h["content"] = hit.Content
		}
		if hit.SessionID != "" {
			h["session_id"] = hit.SessionID
		}
		if hit.CreatedAt != nil {
			h["created_at"] = hit.CreatedAt.Format(time.RFC3339)
		}
		hits = append(hits, h)
	}

	response := map[string]interface{}{
		"query":         result.Query,
		"limit":         result.Limit,
		"offset":        result.Offset,
		"count":         result.Count,
		"total_matches": result.TotalMatches,
		"hits":          hits,
		"has_more":      result.HasMore(),
	}

	if result.Meta != nil {
		response["meta"] = map[string]interface{}{
			"took_ms":           result.Meta.TookMs,
			"index_size":        result.Meta.IndexSize,
			"version":           result.Meta.Version,
			"wildcard_fallback": result.Meta.WildcardFallback,
			"next_cursor":       result.Meta.NextCursor,
		}
	}

	if result.Aggregations != nil {
		response["aggregations"] = map[string]interface{}{
			"agents":     result.Aggregations.Agents,
			"workspaces": result.Aggregations.Workspaces,
			"tags":       result.Aggregations.Tags,
		}
	}

	// Publish search event
	s.publishMemoryEvent("cass.search", map[string]interface{}{
		"query":         req.Query,
		"total_matches": result.TotalMatches,
		"count":         result.Count,
	})

	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleCASSInsights returns aggregated insights
func (s *Server) handleCASSInsights(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	client := cass.NewClient()
	if !client.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeCASSUnavailable,
			"CASS is not installed", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Get aggregations for agents and workspaces
	result, err := client.Search(ctx, cass.SearchOptions{
		Query:     "*",
		Limit:     0,
		Aggregate: "agent,workspace",
	})
	if err != nil {
		slog.Warn("cass insights failed", "error", err, "request_id", reqID)
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"total_documents": 0,
			"agents":          map[string]int{},
			"workspaces":      map[string]int{},
			"tags":            map[string]int{},
			"warnings":        []string{"cass insights unavailable; returning empty aggregates"},
		}, reqID)
		return
	}

	response := map[string]interface{}{
		"total_documents": result.TotalMatches,
	}

	if result.Aggregations != nil {
		response["agents"] = result.Aggregations.Agents
		response["workspaces"] = result.Aggregations.Workspaces
		response["tags"] = result.Aggregations.Tags
	}

	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// handleCASSTimeline returns a timeline of recent activity
func (s *Server) handleCASSTimeline(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	client := cass.NewClient()
	if !client.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeCASSUnavailable,
			"CASS is not installed", nil, reqID)
		return
	}

	// Parse query params
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}

	since := r.URL.Query().Get("since")
	if since == "" {
		since = "7d"
	}

	agent := r.URL.Query().Get("agent")
	workspace := r.URL.Query().Get("workspace")

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := client.Search(ctx, cass.SearchOptions{
		Query:     "*",
		Limit:     limit,
		Agent:     agent,
		Workspace: workspace,
		Since:     since,
		Fields:    "summary",
	})
	if err != nil {
		slog.Warn("cass timeline failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeCASSUnavailable,
			"Failed to get timeline", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	// Build timeline entries
	entries := make([]map[string]interface{}, 0, len(result.Hits))
	for _, hit := range result.Hits {
		entry := map[string]interface{}{
			"type":      hit.MatchType,
			"agent":     hit.Agent,
			"workspace": hit.Workspace,
			"title":     hit.Title,
			"snippet":   hit.Snippet,
			"path":      hit.SourcePath,
		}
		if hit.CreatedAt != nil {
			entry["timestamp"] = hit.CreatedAt.Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
		"since":   since,
	}, reqID)
}

// handleCASSPreview returns a preview of a specific document
func (s *Server) handleCASSPreview(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	client := cass.NewClient()
	if !client.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeCASSUnavailable,
			"CASS is not installed", nil, reqID)
		return
	}

	// Get required params
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Path parameter is required", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Search for the specific document
	result, err := client.Search(ctx, cass.SearchOptions{
		Query:     fmt.Sprintf("source_path:%q", path),
		Limit:     1,
		Highlight: true,
	})
	if err != nil {
		slog.Warn("cass preview failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeCASSUnavailable,
			"Failed to get preview", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	if len(result.Hits) == 0 {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			"Document not found", nil, reqID)
		return
	}

	hit := result.Hits[0]
	response := map[string]interface{}{
		"source_path": hit.SourcePath,
		"agent":       hit.Agent,
		"workspace":   hit.Workspace,
		"title":       hit.Title,
		"content":     hit.Content,
		"snippet":     hit.Snippet,
		"match_type":  hit.MatchType,
	}
	if hit.LineNumber != nil {
		response["line_number"] = *hit.LineNumber
	}
	if hit.CreatedAt != nil {
		response["created_at"] = hit.CreatedAt.Format(time.RFC3339)
	}

	writeSuccessResponse(w, http.StatusOK, response, reqID)
}

// Memory Handlers

// handleMemoryDaemonStatus returns the memory daemon status
func (s *Server) handleMemoryDaemonStatus(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Check if cm is installed
	if _, err := exec.LookPath("cm"); err != nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"installed": false,
			"state":     DaemonStateStopped,
			"message":   "cm (CASS Memory) is not installed",
		}, reqID)
		return
	}

	// Check for running daemon by looking for PID file
	daemonInfo := s.checkMemoryDaemon()

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"installed":  true,
		"state":      daemonInfo.State,
		"pid":        daemonInfo.PID,
		"port":       daemonInfo.Port,
		"started_at": daemonInfo.StartedAt,
		"session_id": daemonInfo.SessionID,
	}, reqID)
}

// checkMemoryDaemon checks if the memory daemon is running
func (s *Server) checkMemoryDaemon() *MemoryDaemonInfo {
	info := &MemoryDaemonInfo{State: DaemonStateStopped}

	// Look for PID files in .ntm/pids
	pidsDir := filepath.Join(s.projectDir, ".ntm", "pids")
	entries, err := os.ReadDir(pidsDir)
	if err != nil {
		return info
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "cm-") && strings.HasSuffix(name, ".pid") {
			pidPath := filepath.Join(pidsDir, name)
			data, err := os.ReadFile(pidPath)
			if err != nil {
				continue
			}

			var pidInfo cm.PIDFileInfo
			if err := json.Unmarshal(data, &pidInfo); err != nil {
				continue
			}

			// Verify the process is actually alive
			if !process.IsAlive(pidInfo.PID) {
				// Clean up stale pid file
				_ = os.Remove(pidPath)
				continue
			}

			// Extract session ID from filename
			sessionID := strings.TrimPrefix(name, "cm-")
			sessionID = strings.TrimSuffix(sessionID, ".pid")

			info.State = DaemonStateRunning
			info.PID = pidInfo.PID
			info.Port = pidInfo.Port
			info.SessionID = sessionID
			info.StartedAt = &pidInfo.StartedAt
			return info
		}
	}

	return info
}

// handleMemoryDaemonStart starts the memory daemon
func (s *Server) handleMemoryDaemonStart(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Check if cm is installed
	if _, err := exec.LookPath("cm"); err != nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMemoryUnavailable,
			"cm (CASS Memory) is not installed", nil, reqID)
		return
	}

	// Check if daemon is already running
	daemonInfo := s.checkMemoryDaemon()
	if daemonInfo.State == DaemonStateRunning {
		writeErrorResponse(w, http.StatusConflict, ErrCodeDaemonRunning,
			"Memory daemon is already running", map[string]interface{}{
				"pid":        daemonInfo.PID,
				"port":       daemonInfo.Port,
				"session_id": daemonInfo.SessionID,
			}, reqID)
		return
	}

	var req MemoryDaemonRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
				"Invalid request body", nil, reqID)
			return
		}
	}

	// Default port
	port := req.Port
	if port == 0 {
		port = 8200
	}

	// Generate session ID if not provided
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("rest-%d", time.Now().Unix())
	}

	slog.Info("starting memory daemon", "request_id", reqID, "port", port, "session_id", sessionID)

	// Start daemon in background
	go s.startMemoryDaemonAsync(port, sessionID)

	// Update store
	memoryStore.SetDaemonInfo(&MemoryDaemonInfo{
		State:     DaemonStateStarting,
		Port:      port,
		SessionID: sessionID,
	})

	// Publish event
	s.publishMemoryEvent("memory.daemon.starting", map[string]interface{}{
		"port":       port,
		"session_id": sessionID,
	})

	writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
		"state":      DaemonStateStarting,
		"port":       port,
		"session_id": sessionID,
		"message":    "Memory daemon is starting",
	}, reqID)
}

// startMemoryDaemonAsync starts the memory daemon in the background
func (s *Server) startMemoryDaemonAsync(port int, sessionID string) {
	// The daemon must outlive this startup helper, so use a plain exec.Cmd.
	cmd := memoryDaemonCommand(s.projectDir, port)
	if cmd == nil {
		slog.Error("failed to build memory daemon command")
		memoryStore.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped})
		s.publishMemoryEvent("memory.daemon.failed", map[string]interface{}{
			"error": "memory daemon command was nil",
		})
		return
	}
	if cmd.Dir == "" {
		cmd.Dir = s.projectDir
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 2 * time.Second
	}

	stderr := newLimitedLogBuffer(8 * 1024)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		slog.Error("failed to start memory daemon", "error", err)
		memoryStore.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped})
		s.publishMemoryEvent("memory.daemon.failed", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	go s.watchMemoryDaemonExit(cmd, sessionID)

	// Wait a moment for the daemon to start
	time.Sleep(memoryDaemonStartupDelay)

	// Check if it's running
	daemonInfo := s.checkMemoryDaemon()
	if daemonInfo.State == DaemonStateRunning {
		memoryStore.SetDaemonInfo(daemonInfo)
		s.publishMemoryEvent("memory.daemon.started", map[string]interface{}{
			"pid":        daemonInfo.PID,
			"port":       daemonInfo.Port,
			"session_id": daemonInfo.SessionID,
		})
	} else {
		slog.Warn("memory daemon may have failed to start", "stderr", stderr.String())
		memoryStore.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped})
		s.publishMemoryEvent("memory.daemon.failed", map[string]interface{}{
			"error": "daemon did not start successfully",
		})
	}
}

// handleMemoryDaemonStop stops the memory daemon
func (s *Server) handleMemoryDaemonStop(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Check if daemon is running
	daemonInfo := s.checkMemoryDaemon()
	if daemonInfo.State != DaemonStateRunning {
		writeErrorResponse(w, http.StatusConflict, ErrCodeDaemonNotRunning,
			"Memory daemon is not running", nil, reqID)
		return
	}

	slog.Info("stopping memory daemon", "request_id", reqID, "pid", daemonInfo.PID)
	memoryStore.SetDaemonInfo(&MemoryDaemonInfo{
		State:     DaemonStateStopping,
		PID:       daemonInfo.PID,
		Port:      daemonInfo.Port,
		StartedAt: daemonInfo.StartedAt,
		SessionID: daemonInfo.SessionID,
	})

	if err := stopMemoryDaemonProcess(daemonInfo.PID, 2*time.Second); err != nil {
		memoryStore.SetDaemonInfo(daemonInfo)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("failed to stop memory daemon: %v", err), map[string]interface{}{
				"pid":        daemonInfo.PID,
				"session_id": daemonInfo.SessionID,
			}, reqID)
		return
	}

	s.removeMemoryDaemonPIDFileIfMatches(daemonInfo.SessionID, daemonInfo.PID)

	memoryStore.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped})

	s.publishMemoryEvent("memory.daemon.stopped", map[string]interface{}{
		"pid":        daemonInfo.PID,
		"session_id": daemonInfo.SessionID,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"state":   DaemonStateStopped,
		"message": "Memory daemon stopped",
	}, reqID)
}

func (s *Server) watchMemoryDaemonExit(cmd *exec.Cmd, sessionID string) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	err := cmd.Wait()
	pidFileRemoved := s.removeMemoryDaemonPIDFileIfMatches(sessionID, pid)

	current := memoryStore.GetDaemonInfo()
	if current == nil || current.PID != pid || current.SessionID != sessionID {
		return
	}
	if current.State == DaemonStateStopping {
		return
	}

	memoryStore.SetDaemonInfo(&MemoryDaemonInfo{State: DaemonStateStopped})
	if err != nil {
		slog.Warn("memory daemon exited", "pid", pid, "session_id", sessionID, "pid_file_removed", pidFileRemoved, "error", err)
		s.publishMemoryEvent("memory.daemon.failed", map[string]interface{}{
			"pid":        pid,
			"session_id": sessionID,
			"error":      err.Error(),
		})
		return
	}

	slog.Info("memory daemon exited", "pid", pid, "session_id", sessionID, "pid_file_removed", pidFileRemoved)
	s.publishMemoryEvent("memory.daemon.stopped", map[string]interface{}{
		"pid":        pid,
		"session_id": sessionID,
	})
}

func (s *Server) removeMemoryDaemonPIDFileIfMatches(sessionID string, pid int) bool {
	if sessionID == "" || pid <= 0 {
		return false
	}

	pidPath := filepath.Join(s.projectDir, ".ntm", "pids", fmt.Sprintf("cm-%s.pid", sessionID))
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}

	var pidInfo cm.PIDFileInfo
	if err := json.Unmarshal(data, &pidInfo); err != nil {
		return false
	}
	if pidInfo.PID != pid {
		return false
	}

	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove memory daemon pid file", "path", pidPath, "pid", pid, "error", err)
		return false
	}
	return true
}

func stopMemoryDaemonProcess(pid int, timeout time.Duration) error {
	if pid <= 0 || !process.IsAlive(pid) {
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := proc.Signal(os.Interrupt); err != nil && process.IsAlive(pid) {
		return fmt.Errorf("interrupt pid %d: %w", pid, err)
	}
	if waitForProcessExit(pid, timeout) {
		return nil
	}

	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && process.IsAlive(pid) {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	if waitForProcessExit(pid, 500*time.Millisecond) {
		return nil
	}

	return fmt.Errorf("pid %d is still running after interrupt/kill", pid)
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !process.IsAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !process.IsAlive(pid)
}

// handleMemoryContext retrieves context for a task
func (s *Server) handleMemoryContext(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req MemoryContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	if req.Task == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Task is required", nil, reqID)
		return
	}

	slog.Info("memory context request", "request_id", reqID, "task", req.Task)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Try HTTP client first (if daemon is running)
	daemonInfo := s.checkMemoryDaemon()
	if daemonInfo.State == DaemonStateRunning && daemonInfo.SessionID != "" {
		client, err := cm.NewClient(s.projectDir, daemonInfo.SessionID)
		if err == nil {
			result, err := client.GetContext(ctx, req.Task)
			if err == nil {
				// Convert to response format
				rules := make([]map[string]interface{}, 0, len(result.RelevantBullets))
				for _, r := range result.RelevantBullets {
					rules = append(rules, map[string]interface{}{
						"id":       r.ID,
						"content":  r.Content,
						"category": r.Category,
					})
				}

				antiPatterns := make([]map[string]interface{}, 0, len(result.AntiPatterns))
				for _, p := range result.AntiPatterns {
					antiPatterns = append(antiPatterns, map[string]interface{}{
						"id":       p.ID,
						"content":  p.Content,
						"category": p.Category,
					})
				}

				snippets := make([]map[string]interface{}, 0, len(result.HistorySnippets))
				for _, sn := range result.HistorySnippets {
					snippets = append(snippets, map[string]interface{}{
						"id":      sn.ID,
						"content": sn.Content,
					})
				}

				writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
					"task":              req.Task,
					"relevant_rules":    rules,
					"anti_patterns":     antiPatterns,
					"history_snippets":  snippets,
					"suggested_queries": result.SuggestedQueries,
					"source":            "daemon",
				}, reqID)
				return
			}
		}
	}

	// Fall back to CLI client
	cliClient := cm.NewCLIClient()
	if !cliClient.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMemoryUnavailable,
			"cm (CASS Memory) is not installed", nil, reqID)
		return
	}

	maxRules := req.MaxRules
	if maxRules == 0 {
		maxRules = 10
	}
	maxSnippets := req.MaxSnippets
	if maxSnippets == 0 {
		maxSnippets = 5
	}

	result, err := cliClient.GetRecoveryContext(ctx, req.Task, maxRules, maxSnippets)
	if err != nil {
		slog.Warn("memory context failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeContextFailed,
			"Failed to get context", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	if result == nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"task":              req.Task,
			"relevant_rules":    []interface{}{},
			"anti_patterns":     []interface{}{},
			"history_snippets":  []interface{}{},
			"suggested_queries": []interface{}{},
			"source":            "cli",
		}, reqID)
		return
	}

	// Convert CLI result to response format
	rules := make([]map[string]interface{}, 0, len(result.RelevantBullets))
	for _, r := range result.RelevantBullets {
		rules = append(rules, map[string]interface{}{
			"id":       r.ID,
			"content":  r.Content,
			"category": r.Category,
		})
	}

	antiPatterns := make([]map[string]interface{}, 0, len(result.AntiPatterns))
	for _, p := range result.AntiPatterns {
		antiPatterns = append(antiPatterns, map[string]interface{}{
			"id":       p.ID,
			"content":  p.Content,
			"category": p.Category,
		})
	}

	snippets := make([]map[string]interface{}, 0, len(result.HistorySnippets))
	for _, sn := range result.HistorySnippets {
		snippets = append(snippets, map[string]interface{}{
			"source_path": sn.SourcePath,
			"line_number": sn.LineNumber,
			"agent":       sn.Agent,
			"workspace":   sn.Workspace,
			"title":       sn.Title,
			"snippet":     sn.Snippet,
			"score":       sn.Score,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"task":              req.Task,
		"relevant_rules":    rules,
		"anti_patterns":     antiPatterns,
		"history_snippets":  snippets,
		"suggested_queries": result.SuggestedQueries,
		"source":            "cli",
	}, reqID)
}

// handleMemoryOutcome records task outcome feedback
func (s *Server) handleMemoryOutcome(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req MemoryOutcomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	// Validate status
	var status cm.OutcomeStatus
	switch req.Status {
	case "success":
		status = cm.OutcomeSuccess
	case "failure":
		status = cm.OutcomeFailure
	case "partial":
		status = cm.OutcomePartial
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid status: must be success, failure, or partial", nil, reqID)
		return
	}

	slog.Info("memory outcome request", "request_id", reqID, "status", req.Status)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Try to find a running daemon
	daemonInfo := s.checkMemoryDaemon()
	if daemonInfo.State != DaemonStateRunning || daemonInfo.SessionID == "" {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeDaemonNotRunning,
			"Memory daemon is not running", nil, reqID)
		return
	}

	client, err := cm.NewClient(s.projectDir, daemonInfo.SessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMemoryUnavailable,
			"Failed to connect to memory daemon", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	report := cm.OutcomeReport{
		Status:    status,
		RuleIDs:   req.RuleIDs,
		Sentiment: req.Sentiment,
		Notes:     req.Notes,
	}

	if err := client.RecordOutcome(ctx, report); err != nil {
		slog.Warn("memory outcome failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeOutcomeFailed,
			"Failed to record outcome", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	s.publishMemoryEvent("memory.outcome", map[string]interface{}{
		"status":   req.Status,
		"rule_ids": req.RuleIDs,
	})

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"recorded": true,
		"status":   req.Status,
	}, reqID)
}

// handleMemoryPrivacyGet returns privacy settings
func (s *Server) handleMemoryPrivacyGet(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Run cm privacy status --json
	cmd := exec.CommandContext(ctx, "cm", "privacy", "status", "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = s.projectDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Warn("privacy status failed", "error", err, "stderr", stderr.String(), "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodePrivacyFailed,
			"Failed to get privacy settings", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	// Parse and return the JSON
	var settings map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &settings); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodePrivacyFailed,
			"Failed to parse privacy settings", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, settings, reqID)
}

// handleMemoryPrivacyUpdate updates privacy settings
func (s *Server) handleMemoryPrivacyUpdate(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req PrivacyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			"Invalid request body", nil, reqID)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Enable or disable cross-agent enrichment
	var cmd *exec.Cmd
	if req.Enabled {
		args := []string{"privacy", "enable", "--json"}
		args = append(args, req.Agents...)
		cmd = exec.CommandContext(ctx, "cm", args...)
		cmd.WaitDelay = 2 * time.Second
	} else {
		cmd = exec.CommandContext(ctx, "cm", "privacy", "disable", "--json")
		cmd.WaitDelay = 2 * time.Second
	}

	cmd.Dir = s.projectDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		slog.Warn("privacy update failed", "error", err, "stderr", stderr.String(), "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodePrivacyFailed,
			"Failed to update privacy settings", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	s.publishMemoryEvent("memory.privacy.updated", map[string]interface{}{
		"enabled": req.Enabled,
		"agents":  req.Agents,
	})

	// Parse and return updated settings
	var settings map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &settings); err != nil {
		// Return success even if parsing fails
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"enabled": req.Enabled,
			"agents":  req.Agents,
			"updated": true,
		}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, settings, reqID)
}

// handleMemoryRules returns available memory rules
func (s *Server) handleMemoryRules(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get rules via cm context with a generic task
	cliClient := cm.NewCLIClient()
	if !cliClient.IsInstalled() {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeMemoryUnavailable,
			"cm (CASS Memory) is not installed", nil, reqID)
		return
	}

	result, err := cliClient.GetContext(ctx, "list all available rules")
	if err != nil {
		slog.Warn("memory rules failed", "error", err, "request_id", reqID)
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeContextFailed,
			"Failed to get rules", map[string]interface{}{"error": err.Error()}, reqID)
		return
	}

	if result == nil {
		writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
			"rules":         []interface{}{},
			"anti_patterns": []interface{}{},
		}, reqID)
		return
	}

	rules := make([]map[string]interface{}, 0, len(result.RelevantBullets))
	for _, r := range result.RelevantBullets {
		rules = append(rules, map[string]interface{}{
			"id":       r.ID,
			"content":  r.Content,
			"category": r.Category,
		})
	}

	antiPatterns := make([]map[string]interface{}, 0, len(result.AntiPatterns))
	for _, p := range result.AntiPatterns {
		antiPatterns = append(antiPatterns, map[string]interface{}{
			"id":       p.ID,
			"content":  p.Content,
			"category": p.Category,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"rules":         rules,
		"anti_patterns": antiPatterns,
	}, reqID)
}

// publishMemoryEvent publishes a memory-related WebSocket event
func (s *Server) publishMemoryEvent(eventType string, payload map[string]interface{}) {
	if s.wsHub == nil {
		return
	}
	s.wsHub.Publish("memory", eventType, payload)
}
