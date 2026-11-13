package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/metrics"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

// Idempotency cache bounds (bd-vq37v). The 24h TTL alone left the cache
// unbounded: a remote-auth caller sending unique keys could grow the map (and
// full buffered response bodies) for a day. Entries are capped with
// least-recently-used eviction. Oversized terminal responses retain a small
// inspection receipt instead of permitting the original operation to run again.
const (
	// idempotencyMaxEntries caps the number of cached responses.
	idempotencyMaxEntries = 4096
	// idempotencyMaxBodyBytes caps a single cached (and buffered) response body.
	idempotencyMaxBodyBytes = 1 << 20 // 1 MiB
)

// IdempotencyStore caches responses by idempotency key.
type IdempotencyStore struct {
	mu         sync.RWMutex
	entries    map[string]*idempotencyEntry
	flights    map[string]chan struct{}
	ttl        time.Duration
	maxEntries int
	stop       chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
}

type idempotencyEntry struct {
	response     []byte
	statusCode   int
	createdAt    time.Time
	lastAccess   time.Time
	replayHeader http.Header
	// fingerprint is a hash of the originating request body. Replays with the
	// same key but a different body are rejected (422) instead of silently
	// echoing the first response. Empty means "unknown" (legacy Set callers)
	// and skips the check.
	fingerprint string
}

// NewIdempotencyStore creates an idempotency cache with the given TTL.
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &IdempotencyStore{
		entries:    make(map[string]*idempotencyEntry),
		flights:    make(map[string]chan struct{}),
		ttl:        ttl,
		maxEntries: idempotencyMaxEntries,
		stop:       make(chan struct{}),
	}
}

func (s *IdempotencyStore) startCleanup() {
	s.startOnce.Do(func() {
		go s.cleanup()
	})
}

// Stop terminates the cleanup goroutine. Call this when the store is no longer needed.
// Safe to call multiple times.
func (s *IdempotencyStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stop)
	})
}

func (s *IdempotencyStore) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for key, entry := range s.entries {
				if now.Sub(entry.createdAt) > s.ttl {
					delete(s.entries, key)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GetWithFingerprint returns a cached response plus the fingerprint of the
// request body that produced it (empty when unknown).
func (s *IdempotencyStore) GetWithFingerprint(key string) ([]byte, int, http.Header, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, 0, nil, "", false
	}
	if time.Since(entry.createdAt) > s.ttl {
		return nil, 0, nil, "", false
	}
	entry.lastAccess = time.Now()
	return entry.response, entry.statusCode, cloneReplayHeaders(entry.replayHeader), entry.fingerprint, true
}

// SetWithFingerprint stores a response together with the request-body
// fingerprint. Oversized responses are not cached (bounded memory, bd-vq37v);
// when the cache is full the least-recently-used entry is evicted.
func (s *IdempotencyStore) SetWithFingerprint(key, fingerprint string, response []byte, statusCode int, replayHeader http.Header) {
	if len(response) > idempotencyMaxBodyBytes {
		return
	}
	s.startCleanup()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists && s.maxEntries > 0 && len(s.entries) >= s.maxEntries {
		s.evictLRULocked()
	}
	now := time.Now()
	s.entries[key] = &idempotencyEntry{
		response:     response,
		statusCode:   statusCode,
		createdAt:    now,
		lastAccess:   now,
		replayHeader: cloneReplayHeaders(replayHeader),
		fingerprint:  fingerprint,
	}
}

// evictLRULocked removes the least-recently-used entry. Caller holds s.mu.
func (s *IdempotencyStore) evictLRULocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range s.entries {
		if oldestKey == "" || entry.lastAccess.Before(oldest) {
			oldestKey = key
			oldest = entry.lastAccess
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

// BeginFlight registers the caller as the executor only if key has neither a
// completed result nor an active request. It returns true for the leader;
// a follower receives a channel that closes when it must recheck the cache.
// A completed result gets an already-closed channel: the previous leader may
// have stored it after this caller's cache miss but before flight acquisition.
func (s *IdempotencyStore) BeginFlight(key string) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok && time.Since(entry.createdAt) <= s.ttl {
		ready := make(chan struct{})
		close(ready)
		return ready, false
	}
	if ch, ok := s.flights[key]; ok {
		return ch, false
	}
	if s.flights == nil {
		s.flights = make(map[string]chan struct{})
	}
	ch := make(chan struct{})
	s.flights[key] = ch
	return ch, true
}

// EndFlight releases the in-flight reservation for key, waking any followers.
func (s *IdempotencyStore) EndFlight(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.flights[key]; ok {
		delete(s.flights, key)
		close(ch)
	}
}

// Job represents an asynchronous operation.
type Job struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Status    JobStatus              `json:"status"`
	Progress  float64                `json:"progress,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	CreatedAt string                 `json:"created_at"`
	UpdatedAt string                 `json:"updated_at"`

	// Admission-time namespace for queued pipeline execution and recovery.
	ProjectDir string `json:"project_dir,omitempty"`
}

// JobStatus represents the state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

// JobStore manages asynchronous jobs.
type JobStore struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	cancels map[string]context.CancelFunc
}

// NewJobStore creates a new job store.
func NewJobStore() *JobStore {
	return &JobStore{
		jobs:    make(map[string]*Job),
		cancels: make(map[string]context.CancelFunc),
	}
}

// SetCancel registers the dispatch goroutine's cancel func so a user-facing
// cancel actually stops the underlying work, not just the bookkeeping row.
func (s *JobStore) SetCancel(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancels[id] = cancel
	job := s.jobs[id]
	stopped := job == nil || job.Status == JobStatusCancelled || job.Status == JobStatusCompleted || job.Status == JobStatusFailed
	s.mu.Unlock()
	if stopped && cancel != nil {
		cancel()
	}
}

// ClearCancel drops the registered cancel func once dispatch has finished.
func (s *JobStore) ClearCancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, id)
}

// maxRetainedJobs bounds the job store over the server's lifetime: once the
// map reaches this size, Create evicts the oldest terminal (completed /
// failed / cancelled) jobs. Pending and running jobs are never evicted.
const maxRetainedJobs = 1000

// Create installs optional execution ownership atomically with the pending row.
// Terminal rows with live owners cannot be evicted before their final writes.
func (s *JobStore) Create(jobType string, ownership ...jobOwnership) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictTerminalLocked(maxRetainedJobs - 1)
	id := generateRequestID()
	now := time.Now().UTC().Format(time.RFC3339)
	job := &Job{
		ID:        id,
		Type:      jobType,
		Status:    JobStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if len(ownership) > 0 {
		job.ProjectDir = ownership[0].projectDir
		if ownership[0].cancel != nil {
			s.cancels[id] = ownership[0].cancel
		}
	}
	s.jobs[id] = job
	return s.cloneJob(job)
}

// Get retrieves a job by ID.
func (s *JobStore) Get(id string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job, ok := s.jobs[id]; ok {
		return s.cloneJob(job)
	}
	return nil
}

// Update updates a job's status and progress.
func (s *JobStore) Update(id string, status JobStatus, progress float64, result map[string]interface{}, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	// Terminal states are final: a cancelled job must not later flip to
	// completed/failed when its (now-cancelled) dispatch goroutine returns.
	switch job.Status {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
		return
	}
	job.Status = status
	job.Progress = progress
	job.Result = result
	job.Error = errMsg
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// cloneJob creates a deep copy of a job
func (s *JobStore) cloneJob(job *Job) *Job {
	if job == nil {
		return nil
	}
	copy := *job
	if job.Result != nil {
		copy.Result = make(map[string]interface{})
		for k, v := range job.Result {
			copy.Result[k] = v // shallow copy of values is usually enough for these JSON results
		}
	}
	return &copy
}

// List returns all jobs, newest first (CreatedAt descending, ID as a
// deterministic tie-break) rather than random map order.
func (s *JobStore) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, s.cloneJob(job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt != jobs[j].CreatedAt {
			return jobs[i].CreatedAt > jobs[j].CreatedAt
		}
		return jobs[i].ID < jobs[j].ID
	})
	return jobs
}

// evictTerminalLocked removes the oldest terminal jobs until at most limit
// jobs remain. Non-terminal jobs are never removed, so the store can exceed
// the limit only while more than `limit` jobs are actually in flight.
// Callers must hold s.mu.
func (s *JobStore) evictTerminalLocked(limit int) {
	if len(s.jobs) <= limit {
		return
	}
	type victim struct{ id, createdAt string }
	terminal := make([]victim, 0, len(s.jobs))
	for id, job := range s.jobs {
		// Cancellation is not proof of worker completion. Retain its row and
		// handle until the final checkpoint and worker fence are released.
		if _, owned := s.cancels[id]; owned {
			continue
		}
		switch job.Status {
		case JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
			terminal = append(terminal, victim{id: id, createdAt: job.CreatedAt})
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].createdAt != terminal[j].createdAt {
			return terminal[i].createdAt < terminal[j].createdAt
		}
		return terminal[i].id < terminal[j].id
	})
	for _, v := range terminal {
		if len(s.jobs) <= limit {
			return
		}
		delete(s.jobs, v.id)
		delete(s.cancels, v.id)
	}
}

// idempotencyHandledKey marks a request as already processed by
// idempotencyMiddleware. Some routes stack the middleware a second time on
// top of the global r.Use (e.g. beads /close); without this marker the inner
// instance would wait on the outer instance's in-flight reservation forever.
type idempotencyHandledKeyType struct{}

var idempotencyHandledKey idempotencyHandledKeyType

type idempotencyResultKey struct{}

type idempotencyResultState struct {
	retain atomic.Bool
}

// markIdempotencyResultRetained keeps a terminal outcome even when it is not a
// successful HTTP response. A handler that has published work, or cannot prove
// publication did not occur, must not let a same-key retry execute it again.
// Only server code can mark this request; no client header selects this policy.
func markIdempotencyResultRetained(r *http.Request) {
	if result, ok := r.Context().Value(idempotencyResultKey{}).(*idempotencyResultState); ok {
		result.retain.Store(true)
	}
}

// idempotencyMiddleware handles Idempotency-Key header for mutating requests.
func (s *Server) idempotencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only apply to mutating methods
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}

		// Re-entrancy guard: an outer instance already owns this request.
		if r.Context().Value(idempotencyHandledKey) != nil {
			next.ServeHTTP(w, r)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), idempotencyHandledKey, true))

		key := scopedIdempotencyKey(r)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Fingerprint the request body so a replay with the same key but a
		// DIFFERENT payload is rejected instead of silently echoing the first
		// response (bd-vq37v).
		fingerprint, err := idempotencyRequestFingerprint(r)
		if err != nil {
			reqID := requestIDFromContext(r.Context())
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				writeErrorResponse(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
					"request body exceeds the 10 MiB limit", nil, reqID)
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				writeErrorResponse(w, http.StatusRequestTimeout, ErrCodeTimeout,
					"request canceled before its body could be verified", nil, reqID)
			default:
				// Reader errors can contain source credentials. Neither a cached
				// response nor a handler may hide an incomplete read.
				writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
					"failed to read complete request body", nil, reqID)
			}
			return
		}

		// Single-flight per scoped key: concurrent duplicates wait for the
		// first request instead of double-executing (bd-vq37v). The loop
		// re-checks the cache after each leader finishes; if the leader failed
		// (nothing cached), the woken follower becomes the new leader.
		for {
			if cached, status, replayHeader, storedFP, ok := s.idempotencyStore.GetWithFingerprint(key); ok {
				if storedFP != "" && storedFP != fingerprint {
					reqID := requestIDFromContext(r.Context())
					writeErrorResponse(w, http.StatusUnprocessableEntity, ErrCodeIdempotentReplay,
						"Idempotency-Key reused with a different request body", nil, reqID)
					return
				}
				protectReplayMetadata(replayHeader, s.requestRedactionConfig(r))
				applyReplayHeaders(w.Header(), replayHeader)
				w.Header().Set("X-Idempotent-Replay", "true")
				w.WriteHeader(status)
				_, _ = w.Write(cached) // Best-effort: client may have disconnected
				return
			}
			wait, leader := s.idempotencyStore.BeginFlight(key)
			if leader {
				break
			}
			select {
			case <-wait:
				continue
			case <-r.Context().Done():
				reqID := requestIDFromContext(r.Context())
				writeErrorResponse(w, http.StatusRequestTimeout, ErrCodeTimeout,
					"client canceled while waiting for in-flight idempotent request", nil, reqID)
				return
			}
		}
		defer s.idempotencyStore.EndFlight(key)

		// Capture response
		result := &idempotencyResultState{}
		r = r.WithContext(context.WithValue(r.Context(), idempotencyResultKey{}, result))
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		// A failure after publication is a terminal result too: replay the
		// receipt instead of repeating work that may already have completed.
		// Ordinary failures remain retryable. When the original representation
		// exceeds the cache bound, retain a small receipt instead: discarding
		// the result would let a retry repeat an already completed mutation.
		if (rec.statusCode >= 200 && rec.statusCode < 300 || result.retain.Load()) && !rec.hijacked {
			body, status, header := rec.body, rec.statusCode, rec.Header()
			if rec.overflow {
				receipt := APIError{
					APIResponse: APIResponse{
						Success: false, Timestamp: time.Now().UTC().Format(time.RFC3339),
						RequestID: requestIDFromContext(r.Context()),
					},
					Error:     "The original request was handled, but its response is too large to replay",
					ErrorCode: "IDEMPOTENCY_RESULT_UNAVAILABLE",
					Details: map[string]interface{}{
						"original_status":      rec.statusCode,
						"response_unavailable": true,
						"inspection_required":  true,
						"retry_safe":           false,
					},
					Hint: "The handler has already run. Inspect the affected state before sending a new request; this retry did not repeat the operation.",
				}
				// Only fixed strings, booleans, and an HTTP status are encoded.
				body, _ = json.Marshal(receipt)
				body = append(body, '\n')
				status = http.StatusConflict
				header = cloneReplayHeaders(header)
				if header == nil {
					header = make(http.Header)
				}
				header.Set("Content-Type", "application/json")
			}
			s.idempotencyStore.SetWithFingerprint(key, fingerprint, body, status, header)
		}
	})
}

// idempotencyRequestFingerprint verifies and hashes the complete admitted body,
// then restores its exact bytes for the handler. A read failure must never turn
// into an empty fingerprint that authorizes an unrelated cached success.
func idempotencyRequestFingerprint(r *http.Request) (string, error) {
	if err := r.Context().Err(); err != nil {
		return "", err
	}
	if scope, ok := r.Context().Value(jobHTTPReplayScopeKey{}).(jobHTTPReplayScope); ok {
		// Jobs already verified the full body before binding its project. Keep
		// that captured identity and canonical decoder's error semantics.
		return scope.fingerprint, scope.requestErr
	}
	var data []byte
	if r.Body != nil && r.Body != http.NoBody {
		var err error
		data, err = io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
		_ = r.Body.Close()
		if len(data) > maxRequestBodyBytes {
			return "", &http.MaxBytesError{Limit: maxRequestBodyBytes}
		}
		if err != nil {
			return "", err
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	if err := r.Context().Err(); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func scopedIdempotencyKey(r *http.Request) string {
	rawKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if rawKey == "" {
		return ""
	}
	if r == nil || r.URL == nil {
		return r.Method + "\x00" + rawKey
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = r.URL.Path
	}
	// The authenticated principal is part of the key. Without it, one caller's
	// cached response was served to any other caller that reused the same
	// Idempotency-Key on the same method+path — including a caller who lacked the
	// permission the original request needed.
	principal := idempotencyPrincipal(r)
	scope, projectScoped := r.Context().Value(mailProjectScopeKey{}).(mailProjectScope)
	project, scopeErr := scope.project, scope.err
	projectMode := ""
	if pipelineScope, ok := r.Context().Value(pipelineProjectScopeKey{}).(pipelineProjectScope); ok {
		project, scopeErr, projectScoped = pipelineScope.projectDir, pipelineScope.err, true
		projectMode = "configured"
		if strings.TrimSpace(pipelineScope.configuredDir) == "" {
			projectMode = "cwd-fallback"
		}
	}
	if jobScope, ok := r.Context().Value(jobHTTPReplayScopeKey{}).(jobHTTPReplayScope); ok {
		if jobScope.err != nil {
			return "" // Preserve permission and canonical request errors.
		}
		project, projectScoped = jobScope.project, jobScope.project != ""
		projectMode = "job-execution"
	}
	if projectScoped && scopeErr != nil {
		return "" // Invalid scope must reach permission/validation, never replay.
	}

	var b strings.Builder
	b.Grow(len(r.Method) + len(path) + len(r.URL.RawQuery) + len(rawKey) + len(principal) + 4)
	b.WriteString(r.Method)
	b.WriteByte('\x00')
	b.WriteString(path)
	if r.URL.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(r.URL.RawQuery)
	}
	b.WriteByte('\x00')
	b.WriteString(principal)
	b.WriteByte('\x00')
	if projectScoped {
		b.WriteString(project)
		b.WriteByte('\x00')
		// A cleared configuration must not replay file-workflow access from a
		// previously configured cwd, even when the effective roots coincide.
		if projectMode != "" {
			b.WriteString(projectMode)
			b.WriteByte('\x00')
		}
	}
	b.WriteString(rawKey)
	return b.String()
}

// idempotencyPrincipal identifies the caller for idempotency scoping. It falls
// back to a fixed label rather than an empty string so keys stay well-formed
// when no RBAC context is attached (local mode with auth disabled).
func idempotencyPrincipal(r *http.Request) string {
	if rc := RoleFromContext(r.Context()); rc != nil {
		if rc.UserID != "" {
			return string(rc.Role) + ":" + rc.UserID
		}
		return string(rc.Role)
	}
	return "anonymous"
}

func cloneReplayHeaders(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	clone := make(http.Header, 3)
	// Location is part of an accepted job's polling contract. Preserve that
	// stable resource identity without replaying cookies, transport headers,
	// security policy, or other request-specific response metadata.
	for _, key := range []string{"Content-Type", "Location", requestIDHeader} {
		values := src.Values(key)
		if len(values) == 0 {
			continue
		}
		copied := append([]string(nil), values...)
		clone[key] = copied
	}
	if len(clone) == 0 {
		return nil
	}
	return clone
}

// idempotencyOriginalRequestIDHeader carries the request ID of the request
// that produced a cached response. The replay keeps the CURRENT request's
// X-Request-ID (bd-vq37v: overwriting it corrupted audit correlation) and
// exposes the original one here for cross-referencing.
const idempotencyOriginalRequestIDHeader = "X-Idempotency-Original-Request-Id"

func applyReplayHeaders(dst, src http.Header) {
	if len(src) == 0 {
		return
	}
	for key, values := range src {
		// Do not clobber the live request's X-Request-ID with the first
		// request's; surface the original under a dedicated header instead.
		if http.CanonicalHeaderKey(key) == http.CanonicalHeaderKey(requestIDHeader) {
			dst.Del(idempotencyOriginalRequestIDHeader)
			for _, value := range values {
				dst.Add(idempotencyOriginalRequestIDHeader, value)
			}
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// =============================================================================
// API v1 Handlers
// =============================================================================

// handleHealthV1 handles GET /api/v1/health.
func (s *Server) handleHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"status":           "healthy",
		"projection_state": s.ProjectionState(),
	}, reqID)
}

// handleVersionV1 handles GET /api/v1/version.
func (s *Server) handleVersionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	v := s.version
	if v == "" {
		v = "dev"
	}
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"version":     v,
		"api_version": "v1",
		"go_version":  runtime.Version(),
	}, reqID)
}

// handleCapabilitiesV1 handles GET /api/v1/capabilities.
func (s *Server) handleCapabilitiesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Phase 5 validation requires the REST capabilities surface to match the
	// canonical robot-mode contract rather than exposing a divergent ad hoc
	// server feature blob.
	caps, err := robot.GetCapabilities()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to collect capabilities", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}

	data, err := toJSONMap(caps)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode capabilities", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleDepsV1 handles GET /api/v1/deps.
// Returns dependency check status for tmux, agent CLIs, and optional tools.
func (s *Server) handleDepsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("deps check", "request_id", reqID)

	result, err := kernel.Run(r.Context(), "core.deps", nil)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Convert result to map[string]interface{} via JSON round-trip
	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize result: "+err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleKernelCommands handles GET /api/v1/kernel/commands.
// Returns the registered kernel command metadata used by the web command palette.
func (s *Server) handleKernelCommands(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("kernel commands list", "request_id", reqID)

	commands := kernel.List()
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"commands": commands,
		"count":    len(commands),
	}, reqID)
}

type waitRequestBodyV1 struct {
	TimeoutMs int    `json:"timeout_ms,omitempty"`
	PollMs    int    `json:"poll_ms,omitempty"`
	Timeout   string `json:"timeout,omitempty"`
	Poll      string `json:"poll,omitempty"`
}

func (s *Server) writeRobotRequestLifecycleOutput(w http.ResponseWriter, reqID string, resp robot.RobotResponse, output interface{}) {
	if !resp.Success {
		var details map[string]interface{}
		if m, err := toJSONMap(output); err == nil {
			delete(m, "success")
			delete(m, "timestamp")
			delete(m, "error")
			delete(m, "error_code")
			if len(m) > 0 {
				details = m
			}
		}
		writeErrorResponse(w, robotErrorHTTPStatus(resp.ErrorCode), resp.ErrorCode, resp.Error, details, reqID)
		return
	}
	payload, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize request lifecycle payload: "+err.Error(), nil, reqID)
		return
	}
	// Pass "" so writeSuccessResponse does not overwrite payload["request_id"]
	// (which identifies the durable operation/wait handle being inspected; the
	// HTTP correlation ID is already emitted on the X-Request-Id header).
	writeSuccessResponse(w, http.StatusOK, payload, "")
}

// handleGetRequestStatusV1 handles GET /api/v1/requests/{id}.
func (s *Server) handleGetRequestStatusV1(w http.ResponseWriter, r *http.Request) {
	httpReqID := requestIDFromContext(r.Context())
	targetID := strings.TrimSpace(chi.URLParam(r, "id"))
	slog.Info("request lifecycle status", "http_request_id", httpReqID, "target_request_id", targetID)

	out, err := robot.GetActionStatusWithStore(targetID, s.stateStore, true)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, httpReqID)
		return
	}
	s.writeRobotRequestLifecycleOutput(w, httpReqID, out.RobotResponse, out)
}

// handleWaitRequestV1 handles POST /api/v1/requests/{id}/wait.
func (s *Server) handleWaitRequestV1(w http.ResponseWriter, r *http.Request) {
	httpReqID := requestIDFromContext(r.Context())
	targetID := strings.TrimSpace(chi.URLParam(r, "id"))

	var body waitRequestBodyV1
	if err := decodeOptionalJSONBody(r, &body); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, httpReqID)
		return
	}

	timeout := 30 * time.Second
	if q := strings.TrimSpace(r.URL.Query().Get("timeout")); q != "" {
		d, err := time.ParseDuration(q)
		if err != nil || d <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid timeout query parameter", nil, httpReqID)
			return
		}
		timeout = d
	} else if strings.TrimSpace(body.Timeout) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(body.Timeout))
		if err != nil || d <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid timeout duration", nil, httpReqID)
			return
		}
		timeout = d
	} else if body.TimeoutMs > 0 {
		timeout = time.Duration(body.TimeoutMs) * time.Millisecond
	}

	poll := 100 * time.Millisecond
	if q := strings.TrimSpace(r.URL.Query().Get("poll")); q != "" {
		d, err := time.ParseDuration(q)
		if err != nil || d <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid poll query parameter", nil, httpReqID)
			return
		}
		poll = d
	} else if strings.TrimSpace(body.Poll) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(body.Poll))
		if err != nil || d <= 0 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid poll duration", nil, httpReqID)
			return
		}
		poll = d
	} else if body.PollMs > 0 {
		poll = time.Duration(body.PollMs) * time.Millisecond
	}

	slog.Info("request lifecycle wait", "http_request_id", httpReqID, "target_request_id", targetID, "timeout", timeout, "poll", poll)
	out, err := robot.GetWaitRequest(r.Context(), robot.WaitRequestOptions{
		RequestID:     targetID,
		Timeout:       timeout,
		PollInterval:  poll,
		Store:         s.stateStore,
		ExplicitStore: true,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, httpReqID)
		return
	}
	s.writeRobotRequestLifecycleOutput(w, httpReqID, out.RobotResponse, out)
}

// handleCancelRequestV1 handles POST /api/v1/requests/{id}/cancel.
func (s *Server) handleCancelRequestV1(w http.ResponseWriter, r *http.Request) {
	httpReqID := requestIDFromContext(r.Context())
	targetID := strings.TrimSpace(chi.URLParam(r, "id"))
	slog.Info("request lifecycle cancel", "http_request_id", httpReqID, "target_request_id", targetID)

	out, err := robot.GetCancelRequestWithStore(targetID, s.stateStore, true)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, httpReqID)
		return
	}
	s.writeRobotRequestLifecycleOutput(w, httpReqID, out.RobotResponse, out)
}

// handleDoctorV1 handles GET /api/v1/doctor.
// Returns comprehensive ecosystem health check including tools, daemons, and configuration.
func (s *Server) handleDoctorV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("doctor check", "request_id", reqID)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	report := performDoctorCheckAPI(ctx)
	writeSuccessResponse(w, http.StatusOK, report, reqID)
}

// handleGetConfigV1 handles GET /api/v1/config.
// Returns the current server configuration (safe fields only).
func (s *Server) handleGetConfigV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("config get", "request_id", reqID)

	// Snapshot mutable fields under the lock.
	s.mu.RLock()
	origins := s.corsAllowedOrigins
	projDir := s.projectDir
	s.mu.RUnlock()

	// Return safe configuration fields only (no secrets). startup_project_dir
	// is what a PATCH with an empty or null project_dir restores.
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"host":                s.host,
		"port":                s.port,
		"auth_mode":           string(s.auth.Mode),
		"allowed_origins":     origins,
		"public_base_url":     s.publicBaseURL,
		"project_dir":         projDir,
		"startup_project_dir": s.startupProjectDir,
	}, reqID)
}

// handlePatchConfigV1 handles PATCH /api/v1/config.
// Updates mutable configuration fields at runtime.
//
// project_dir selects the server-wide project. An omitted project_dir leaves
// it unchanged. A path selects that directory. An empty string or null resets
// it to the startup project (`--project-dir`), or to no project when the server
// was started without one, so a PATCH can always be undone without a restart
// (GH #360). A nonempty target, including the startup project on a reset, must
// still be an existing directory.
func (s *Server) handlePatchConfigV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("config patch", "request_id", reqID)

	var req struct {
		AllowedOrigins     []string        `json:"allowed_origins,omitempty"`
		CORSAllowedOrigins []string        `json:"cors_allowed_origins,omitempty"`
		Auth               json.RawMessage `json:"auth,omitempty"`
		// Raw so that an explicit "" or null (reset) is distinguishable from
		// an omitted field (unchanged).
		ProjectDir json.RawMessage `json:"project_dir,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	updateProject, resetProject := len(req.ProjectDir) > 0, false
	var projectDir string
	if updateProject {
		var requested *string
		if err := json.Unmarshal(req.ProjectDir, &requested); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "project_dir must be a string or null", nil, reqID)
			return
		}
		var err error
		if requested == nil || *requested == "" {
			resetProject = true
			// The startup value was validated when the server started; check
			// it again, since the directory may have been removed since.
			if projectDir, err = ResolveProjectDir(s.startupProjectDir); err != nil {
				writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "cannot reset project_dir to the startup project: "+err.Error(), nil, reqID)
				return
			}
		} else if projectDir, err = ResolveProjectDir(*requested); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
			return
		}
	}

	if len(req.Auth) > 0 {
		s.recordConfigPatchRefusal(r, "auth", "runtime auth reconfiguration is forbidden")
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			"auth configuration is immutable at runtime; restart ntm serve with explicit auth flags", nil, reqID)
		return
	}

	originsPatch := req.AllowedOrigins
	if originsPatch == nil && req.CORSAllowedOrigins != nil {
		originsPatch = req.CORSAllowedOrigins
	}
	if originsPatch != nil && (s.auth.Mode == AuthModeLocal || s.auth.Mode == "") {
		s.recordConfigPatchRefusal(r, "allowed_origins", "runtime CORS origin mutation forbidden in local auth mode")
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			"allowed_origins cannot be modified at runtime in local auth mode; restart with --cors-origins or enable authenticated mode", nil, reqID)
		return
	}

	// Apply updates and snapshot the result under the lock so the log
	// and response body see a consistent view.
	s.mu.Lock()
	if originsPatch != nil {
		s.corsAllowedOrigins = originsPatch
	}
	if updateProject {
		s.projectDir = projectDir
		// Reset mail client so it picks up new project dir
		s.mailClient = nil
		// A new project directory invalidates the cached unavailability verdict too.
		s.mailUnavailableUntil = time.Time{}
	}
	currentOrigins := s.corsAllowedOrigins
	currentProjectDir := s.projectDir
	s.mu.Unlock()

	slog.Info("config updated", "request_id", reqID, "allowed_origins", len(currentOrigins), "project_dir", currentProjectDir, "project_dir_reset", resetProject)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"config": map[string]interface{}{
			"allowed_origins": currentOrigins,
			"project_dir":     currentProjectDir,
		},
	}, reqID)
}

func (s *Server) recordConfigPatchRefusal(r *http.Request, field, reason string) {
	if s.stateStore == nil {
		return
	}
	actorID := "local"
	if rc := RoleFromContext(r.Context()); rc != nil && rc.UserID != "" {
		actorID = rc.UserID
	}
	_, _ = s.stateStore.RecordAuditDecision(&state.AuditDecision{
		DecisionType: "reject_config_patch",
		DecisionAt:   time.Now().UTC(),
		ActorType:    "http",
		ActorID:      actorID,
		EntityType:   "server_config",
		EntityID:     field,
		Reason:       reason,
	})
}

// performDoctorCheckAPI runs doctor checks for the REST API.
func performDoctorCheckAPI(ctx context.Context) map[string]interface{} {
	report := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"overall":   "healthy",
	}

	warnings := 0
	errors := 0

	// Check dependencies
	deps := []map[string]interface{}{}

	// Check tmux
	tmuxCheck := map[string]interface{}{
		"name":     "tmux",
		"required": true,
	}
	if _, err := exec.LookPath("tmux"); err == nil {
		tmuxCheck["installed"] = true
		tmuxCheck["status"] = "ok"
		// Get version
		if out, err := exec.CommandContext(ctx, "tmux", "-V").Output(); err == nil {
			tmuxCheck["version"] = strings.TrimSpace(string(out))
		}
	} else {
		tmuxCheck["installed"] = false
		tmuxCheck["status"] = "error"
		tmuxCheck["message"] = "tmux is required for NTM"
		errors++
	}
	deps = append(deps, tmuxCheck)

	// Check Go
	goCheck := map[string]interface{}{
		"name":     "go",
		"required": false,
	}
	if path, err := exec.LookPath("go"); err == nil {
		goCheck["installed"] = true
		goCheck["path"] = path
		goCheck["status"] = "ok"
		if out, err := exec.CommandContext(ctx, "go", "version").Output(); err == nil {
			goCheck["version"] = strings.TrimSpace(string(out))
		}
	} else {
		goCheck["installed"] = false
		goCheck["status"] = "warning"
		goCheck["message"] = "not found (needed for plugins)"
		warnings++
	}
	deps = append(deps, goCheck)

	report["dependencies"] = deps

	// Check daemons
	daemons := []map[string]interface{}{}
	daemonPorts := []struct {
		name string
		port int
	}{
		{"agent-mail", 8765},
		{"cm-server", 8766},
	}

	dialer := &net.Dialer{Timeout: time.Second}
	for _, dp := range daemonPorts {
		check := map[string]interface{}{
			"name": dp.name,
			"port": dp.port,
		}
		conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", dp.port))
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				log.Printf("health check: close daemon probe conn name=%s port=%d error=%v", dp.name, dp.port, closeErr)
			}
			check["running"] = true
			check["status"] = "ok"
			check["message"] = fmt.Sprintf("listening on port %d", dp.port)
		} else {
			check["running"] = false
			check["status"] = "ok"
			check["message"] = fmt.Sprintf("port %d available", dp.port)
		}
		daemons = append(daemons, check)
	}
	report["daemons"] = daemons

	// Pre-commit guard degraded runs (bd-ws1-truth-safety-l5ddi.1): the guard
	// hook fails open when Agent Mail is unreachable and records each such run
	// in the state DB; the dashboard surfaces the count as a warning line.
	guardCheck := map[string]interface{}{
		"name":          "guard-hook",
		"status":        "ok",
		"degraded_runs": 0,
	}
	if store, err := state.Open(""); err == nil {
		if migrateErr := store.Migrate(); migrateErr != nil {
			log.Printf("doctor: state store migrate error=%v", migrateErr)
		} else if stats, statsErr := store.GuardDegradedEventStats(time.Time{}); statsErr == nil && stats.Count > 0 {
			guardCheck["status"] = "warning"
			guardCheck["degraded_runs"] = stats.Count
			guardCheck["since"] = stats.FirstAt.UTC().Format(time.RFC3339)
			guardCheck["message"] = fmt.Sprintf("guard hook ran degraded %d time(s) since %s (commits allowed without a reservation check)",
				stats.Count, stats.FirstAt.UTC().Format(time.RFC3339))
			warnings++
		}
		if closeErr := store.Close(); closeErr != nil {
			log.Printf("doctor: close state store error=%v", closeErr)
		}
	}
	report["guard_hook"] = guardCheck

	// Set overall status
	if errors > 0 {
		report["overall"] = "unhealthy"
	} else if warnings > 0 {
		report["overall"] = "warning"
	}
	report["warnings"] = warnings
	report["errors"] = errors

	return report
}

// handleMetricsExportV1 handles GET /api/v1/metrics/export?format=json|csv|md|prometheus&session=...
func (s *Server) handleMetricsExportV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json", "csv", "md", "markdown", "prometheus", "prom":
	default:
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid format %q: must be json, csv, md, or prometheus", format), nil, reqID)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		sessionID = "default"
	}

	collector := metrics.NewCollector(s.stateStore, sessionID)
	report, err := collector.GenerateReport()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
			fmt.Sprintf("generating metrics report: %v", err), nil, reqID)
		return
	}

	switch format {
	case "csv":
		if requestIDHeader != "" && reqID != "" {
			w.Header().Set(requestIDHeader, reqID)
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, report.ExportCSV())
	case "md", "markdown":
		if requestIDHeader != "" && reqID != "" {
			w.Header().Set(requestIDHeader, reqID)
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, report.ExportMarkdown())
	case "prometheus", "prom":
		if requestIDHeader != "" && reqID != "" {
			w.Header().Set(requestIDHeader, reqID)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, report.ExportPrometheus())
	default:
		rawJSON, err := report.ExportJSON()
		if err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				fmt.Sprintf("encoding metrics report: %v", err), nil, reqID)
			return
		}
		data := make(map[string]interface{})
		if err := json.Unmarshal(rawJSON, &data); err != nil {
			writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError,
				fmt.Sprintf("decoding metrics report: %v", err), nil, reqID)
			return
		}
		data["format"] = "json"
		writeSuccessResponse(w, http.StatusOK, data, reqID)
	}
}

// =============================================================================
// Metrics API Handlers
// =============================================================================

// handleListJobs handles GET /api/v1/jobs.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	jobs := s.jobStore.List()

	// Ensure jobs is never null
	if jobs == nil {
		jobs = []*Job{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"jobs":      jobs,
		"count":     len(jobs),
		"execution": s.jobExecutor.snapshot(),
	}, reqID)
}

// CreateJobRequest is the request body for job creation.
type CreateJobRequest struct {
	Type    string                 `json:"type"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Session string                 `json:"session,omitempty"`

	// Internal service ownership; never accepted from JSON or fingerprinted.
	executionContext    context.Context
	executionProjectDir string
}

// handleCreateJob handles POST /api/v1/jobs.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req CreateJobRequest
	var err error
	if scope, captured := r.Context().Value(jobHTTPReplayScopeKey{}).(jobHTTPReplayScope); captured {
		req, err = scope.request, scope.requestErr
	} else {
		req, err = decodeCreateJobRequest(r.Body)
	}
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	if req.Type == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "job type required", nil, reqID)
		return
	}

	// Only the three allow-listed async operations are implemented (D5,
	// bd-ws3-contract-breadth-psvyu.5). Everything else is an honest
	// NOT_IMPLEMENTED — never a fake acceptance for work that will not run.
	if !isImplementedJobType(req.Type) {
		writeErrorResponse(w, http.StatusNotImplemented, ErrCodeNotImplemented,
			fmt.Sprintf("job type %q is not implemented", req.Type), map[string]interface{}{
				"implemented_types": implementedJobTypes,
			}, reqID)
		return
	}

	job, err := s.submitJob(r.Context(), req)
	if err != nil {
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
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)

	writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
		"job": job,
	}, reqID)
}

// handleGetJob handles GET /api/v1/jobs/{id}.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")

	job := s.jobStore.Get(jobID)
	if job == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "job not found", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"job": job,
	}, reqID)
}

// handleCancelJob handles DELETE /api/v1/jobs/{id}.
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	jobID := chi.URLParam(r, "id")

	job, err := s.cancelJob(jobID)
	if errors.Is(err, errJobNotFound) {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "job not found", nil, reqID)
		return
	}

	if errors.Is(err, errJobNotCancellable) {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, "job cannot be cancelled", map[string]interface{}{
			"status": job.Status,
		}, reqID)
		return
	}

	if err != nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail,
			"cancellation signalled but checkpoint failed: "+err.Error(), map[string]interface{}{
				"job": job, "cancellation_requested": true,
			}, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"job": job,
	}, reqID)
}
