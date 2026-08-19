// Package serve provides an HTTP server for NTM with REST API and event streaming.
package serve

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	approvalpkg "github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/policy"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Server provides HTTP API and event streaming for NTM.
type Server struct {
	host          string
	port          int
	publicBaseURL string
	version       string
	eventBus      *events.EventBus
	stateStore    *state.Store
	server        *http.Server
	auth          AuthConfig

	// approvalEng is the durable approval engine over stateStore, shared with
	// the `ntm approve` / force-release gate workflow (bd-d2uxt). Built lazily
	// so a Server constructed without a state store stays side-effect-free;
	// nil when no state store is configured, in which case the approval
	// endpoints fail closed with 503 instead of falling back to process-local
	// memory.
	approvalEng     *approvalpkg.Engine
	approvalEngOnce sync.Once

	// SSE clients
	sseClients   map[chan events.BusEvent]struct{}
	sseClientsMu sync.RWMutex

	corsAllowedOrigins []string
	jwksCache          *jwksCache

	// Idempotency support
	idempotencyStore *IdempotencyStore

	// Job management
	jobStore *JobStore

	// Chi router for /api/v1
	router chi.Router

	// WebSocket hub for real-time subscriptions
	wsHub *WSHub
	// wsHubStartOnce ensures the hub loop starts exactly once, even when the
	// router is used directly in tests without going through Start().
	wsHubStartOnce sync.Once
	// wsEventStoreOnce lazily wires WS event persistence/replay exactly once.
	// Lazy (Start or first WS connection, not New) so that constructing a
	// Server stays side-effect-free: the store starts a retention goroutine.
	wsEventStoreOnce sync.Once

	// Pane output streaming
	streamManager   *tmux.StreamManager
	spawnAgents     func(context.Context, robot.SpawnOptions) (*robot.SpawnOutput, error)
	sendAgents      func(robot.SendOptions) (*robot.SendOutput, error)
	interruptAgents func(robot.InterruptOptions) (*robot.InterruptOutput, error)
	waitAgents      func(context.Context, robot.WaitOptions) (*robot.WaitResponse, int)
	resolvePane     func(context.Context, string, int) (tmux.Pane, error)
	sendPaneKeys    func(string, string, bool) error
	// policyWriteFile is optional test-only fault injection for policy updates.
	// Production servers leave it nil and use os.WriteFile directly.
	policyWriteFile func(string, []byte, os.FileMode) error
	// policyMkdirAll is optional test-only fault injection for policy directory creation.
	// Production servers leave it nil and use os.MkdirAll directly.
	policyMkdirAll func(string, os.FileMode) error

	// Agent Mail client (lazy-init)
	mailClient *agentmail.Client
	// mailUnavailableUntil caches a negative availability verdict so an
	// unreachable Agent Mail server does not make every mail request pay the
	// probe budget again. Guarded by mu.
	mailUnavailableUntil time.Time
	projectDir           string
	// auditStore persists REST/WS audit records. Nil when the store could not be
	// opened, in which case the audit middleware is not installed.
	auditStore *AuditStore
	mu         sync.RWMutex

	// Redaction configuration for REST API
	redactionCfg *RedactionConfig

	// webUI is the embedded static dashboard; nil when not mounted.
	webUI fs.FS
}

type attentionHeartbeatSourceSummary struct {
	healthy         int
	degraded        int
	unavailable     int
	degradedReasons []string
}

// Attention heartbeat intervals control how frequently the attention feed
// polls for updates in different states.
// These serve as defaults; override via [serve.attention] in config.toml.
const (
	// attentionHeartbeatIdleInterval is the poll interval when no events are occurring.
	attentionHeartbeatIdleInterval = 5 * time.Second
	// attentionHeartbeatHighActivityInterval is used during sustained high event rates
	// to reduce CPU overhead from frequent polling.
	attentionHeartbeatHighActivityInterval = 30 * time.Second
	// attentionHeartbeatRecoveryInterval is used when catching up after a gap in data,
	// polling more frequently to quickly reconstruct state.
	attentionHeartbeatRecoveryInterval = time.Second
)

// AuthMode configures authentication for the server.
type AuthMode string

const (
	AuthModeLocal  AuthMode = "local"
	AuthModeAPIKey AuthMode = "api_key"
	AuthModeOIDC   AuthMode = "oidc"
	AuthModeMTLS   AuthMode = "mtls"
)

// AuthConfig holds server authentication configuration.
type AuthConfig struct {
	Mode   AuthMode
	APIKey string
	OIDC   OIDCConfig
	MTLS   MTLSConfig
	// Role is granted to callers authenticated by a shared credential that
	// carries no role of its own — the api_key and mtls modes. Both mean "the
	// operator's own credential", so it defaults to RoleAdmin; set it lower to
	// restrict what a shared key or client certificate may do. It does not apply
	// to oidc, where the token's own claims decide.
	Role Role
}

// sharedCredentialRole resolves the role granted to api_key and mtls callers.
func (a AuthConfig) sharedCredentialRole() Role {
	if a.Role == "" {
		return RoleAdmin
	}
	return a.Role
}

// OIDCConfig configures OIDC/JWT verification for API access.
type OIDCConfig struct {
	Issuer   string
	Audience string
	JWKSURL  string
	CacheTTL time.Duration
}

// MTLSConfig configures mutual TLS for API access.
type MTLSConfig struct {
	CertFile     string
	KeyFile      string
	ClientCAFile string
}

// Config holds server configuration.
type Config struct {
	Host string
	Port int
	// PublicBaseURL advertises the externally reachable base URL for clients.
	// Optional: leave empty to derive from host/port in documentation or clients.
	PublicBaseURL string
	EventBus      *events.EventBus
	StateStore    *state.Store
	Auth          AuthConfig
	// AllowedOrigins controls CORS origin allowlist. Empty means default localhost only.
	AllowedOrigins []string
	// Version is the build version string (set via ldflags). Used by /api/v1/version.
	Version string
	// AuditStore records mutating REST/WS actions. Nil disables auditing.
	//
	// It is injected rather than opened here, like EventBus and StateStore, because
	// New must stay side-effect-free: opening the store creates files and starts a
	// retention goroutine, and callers construct many servers in tests. Ownership
	// stays with the caller, which is responsible for closing it.
	AuditStore *AuditStore
	// WebUI is the embedded static dashboard (internal/webui.FS()). Nil means
	// no UI is mounted and non-API paths 404 as before (`ntm serve` default;
	// `ntm web` and `ntm serve --web` set it).
	WebUI fs.FS
}

const (
	// DefaultPort is the default port for the NTM HTTP server.
	DefaultPort         = 7337
	defaultJWKSCacheTTL = 10 * time.Minute
)

const requestIDHeader = "X-Request-Id"

type ctxKey string

const requestIDKey ctxKey = "request_id"

// Response envelope types matching robot mode output format.
// Arrays are always initialized to [] (never null).

// APIResponse is the base envelope for all API responses.
type APIResponse struct {
	Success   bool   `json:"success"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"request_id,omitempty"`
}

// APIError represents a structured error response.
type APIError struct {
	APIResponse
	Error     string                 `json:"error"`
	ErrorCode string                 `json:"error_code,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Hint      string                 `json:"hint,omitempty"`
}

// Common error codes (matching robot mode conventions).
const (
	ErrCodeBadRequest       = "BAD_REQUEST"
	ErrCodeUnauthorized     = "UNAUTHORIZED"
	ErrCodeForbidden        = "FORBIDDEN"
	ErrCodeNotFound         = "NOT_FOUND"
	ErrCodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	ErrCodeConflict         = "CONFLICT"
	ErrCodeInternalError    = "INTERNAL_ERROR"
	ErrCodeServiceUnavail   = "SERVICE_UNAVAILABLE"
	ErrCodeNotImplemented   = "NOT_IMPLEMENTED"
	ErrCodeTimeout          = "TIMEOUT"
	ErrCodeIdempotentReplay = "IDEMPOTENT_REPLAY"
	ErrCodeJobPending       = "JOB_PENDING"
)

// Idempotency cache bounds (bd-vq37v). The 24h TTL alone left the cache
// unbounded: a remote-auth caller sending unique keys could grow the map (and
// full buffered response bodies) for a day. Entries are capped with
// least-recently-used eviction, and oversized responses are never cached.
const (
	// idempotencyMaxEntries caps the number of cached responses.
	idempotencyMaxEntries = 4096
	// idempotencyMaxBodyBytes caps a single cached (and buffered) response body.
	idempotencyMaxBodyBytes = 1 << 20 // 1 MiB
	// idempotencyFingerprintLimit bounds how much of a request body is hashed
	// for replay fingerprinting. Bodies larger than this are fingerprinted by
	// prefix, which is still deterministic for mismatch detection.
	idempotencyFingerprintLimit = 8 << 20 // 8 MiB
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

// BeginFlight registers the caller as the executor for key if no request with
// that key is currently in flight. It returns (nil, true) for the leader; a
// follower receives a channel that closes when the leader finishes, plus
// false. This closes the Get-miss..Set window in which two concurrent POSTs
// with the same Idempotency-Key both executed (bd-vq37v).
func (s *IdempotencyStore) BeginFlight(key string) (<-chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	defer s.mu.Unlock()
	s.cancels[id] = cancel
}

// ClearCancel drops the registered cancel func once dispatch has finished.
func (s *JobStore) ClearCancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, id)
}

// Cancel invokes the job's registered cancel func, if any.
func (s *JobStore) Cancel(id string) {
	s.mu.RLock()
	cancel := s.cancels[id]
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// maxRetainedJobs bounds the job store over the server's lifetime: once the
// map reaches this size, Create evicts the oldest terminal (completed /
// failed / cancelled) jobs. Pending and running jobs are never evicted.
const maxRetainedJobs = 1000

// Create creates a new job.
func (s *JobStore) Create(jobType string) *Job {
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

// ============================================================================
// WebSocket Hub + Subscription Protocol
// ============================================================================

// WSMessageType defines WebSocket message types.
type WSMessageType string

const (
	WSMsgSubscribe   WSMessageType = "subscribe"
	WSMsgUnsubscribe WSMessageType = "unsubscribe"
	WSMsgEvent       WSMessageType = "event"
	WSMsgError       WSMessageType = "error"
	WSMsgAck         WSMessageType = "ack"
	WSMsgPing        WSMessageType = "ping"
	WSMsgPong        WSMessageType = "pong"
)

// WSMessage is the base WebSocket message envelope.
type WSMessage struct {
	Type      WSMessageType          `json:"type"`
	Timestamp string                 `json:"ts"`
	RequestID string                 `json:"request_id,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// WSSubscribeRequest is sent by clients to subscribe to topics.
// Since is the replay cursor: the seq of the last event frame the client
// processed. When present, persisted events with seq > Since matching the
// requested topics are replayed before live delivery. See the replay contract
// in ws_events.go.
type WSSubscribeRequest struct {
	Topics []string `json:"topics"`
	Since  int64    `json:"since,omitempty"` // Cursor for replay (event seq)
}

// WSEvent is an event pushed to clients.
// Seq is the resume cursor: clients persist the highest seq they have
// processed and pass it back as `since` on re-subscribe. Replay is set only on
// frames re-delivered from the event store during cursor replay (additive
// field; absent on live frames).
type WSEvent struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	Seq       int64         `json:"seq"`
	Topic     string        `json:"topic"`
	EventType string        `json:"event_type"`
	Data      interface{}   `json:"data"`
	Replay    bool          `json:"replay,omitempty"`
}

// WSError represents a WebSocket error frame.
type WSError struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	RequestID string        `json:"request_id,omitempty"`
	Code      string        `json:"code"`
	Message   string        `json:"message"`
}

// WSAttentionSub tracks attention-specific subscription state for a WebSocket client.
// This enables durable attention semantics: cursor replay, cursor expiration,
// operator state filtering, and transport parity with SSE.
type WSAttentionSub struct {
	// Active indicates whether attention streaming is active for this client.
	Active bool `json:"active"`

	// Cursor is the last event cursor delivered to this client.
	// Used for replay deduplication and heartbeat reporting.
	Cursor int64 `json:"cursor"`

	// SinceCursor is the cursor from which streaming started.
	SinceCursor int64 `json:"since_cursor"`

	// Session filters events to a specific session (empty = all).
	Session string `json:"session,omitempty"`

	// Profile is the attention profile name (e.g., "operator", "minimal").
	Profile string `json:"profile,omitempty"`

	// CategoryFilter limits events to these categories.
	CategoryFilter []string `json:"category_filter,omitempty"`

	// ActionabilityFilter limits events to these actionability levels.
	ActionabilityFilter []string `json:"actionability_filter,omitempty"`

	// ExcludeMuted excludes items with muted operator state.
	ExcludeMuted bool `json:"exclude_muted"`

	// ExcludeSnoozed excludes items that are currently snoozed.
	ExcludeSnoozed bool `json:"exclude_snoozed"`

	// StartedAt is when this subscription started.
	StartedAt time.Time `json:"started_at"`

	// EventCount is the number of events delivered since subscription started.
	EventCount int64 `json:"event_count"`

	// DroppedCount is events dropped due to backpressure.
	DroppedCount uint64 `json:"dropped_count"`

	// unsubscribe is the function to stop the feed subscription.
	unsubscribe func()
	topics      []string
}

// WSClient represents a connected WebSocket client.
type WSClient struct {
	id         string
	conn       *websocket.Conn
	hub        *WSHub
	send       chan []byte
	topics     map[string]struct{}
	topicsMu   sync.RWMutex
	authClaims map[string]interface{}
	closeOnce  sync.Once

	// Attention subscription state for durable attention semantics.
	attentionSub   *WSAttentionSub
	attentionSubMu sync.Mutex
}

// WSHub manages WebSocket connections and topic routing.
type WSHub struct {
	clients      map[*WSClient]struct{}
	clientsMu    sync.RWMutex
	register     chan *WSClient
	unregister   chan *WSClient
	broadcast    chan *WSEvent
	dropped      atomic.Int64
	seq          int64
	seqMu        sync.Mutex
	done         chan struct{}
	stopOnce     sync.Once
	redactionCfg *RedactionConfig
	redactionMu  sync.RWMutex

	// store is the optional persistence/replay backend (see the replay
	// contract in ws_events.go). Nil pointer = live streaming only.
	store atomic.Pointer[WSEventStore]
	// replayCh carries subscribe-with-cursor requests into the Run loop so
	// replay and live delivery are serialized (gap-free, duplicate-free).
	replayCh chan *wsReplayRequest
	// pendingDrops coalesces per-client dropped-event ranges. Owned
	// exclusively by the Run goroutine; no locking.
	pendingDrops map[*WSClient]map[string]*wsDropRange
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients:      make(map[*WSClient]struct{}),
		register:     make(chan *WSClient),
		unregister:   make(chan *WSClient),
		broadcast:    make(chan *WSEvent, 256),
		done:         make(chan struct{}),
		replayCh:     make(chan *wsReplayRequest),
		pendingDrops: make(map[*WSClient]map[string]*wsDropRange),
	}
}

// Run starts the hub's main event loop.
func (h *WSHub) Run() {
	for {
		select {
		case <-h.done:
			// Persist any drop ranges still pending so slow-client gaps are
			// visible in ws_dropped_events across a restart. Best-effort.
			h.flushAllDrops()
			return
		case client := <-h.register:
			h.clientsMu.Lock()
			h.clients[client] = struct{}{}
			total := len(h.clients)
			h.clientsMu.Unlock()
			log.Printf("ws client connected id=%s total=%d", client.id, total)
		case client := <-h.unregister:
			h.flushClientDrops(client, false)
			h.clientsMu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			total := len(h.clients)
			h.clientsMu.Unlock()
			log.Printf("ws client disconnected id=%s total=%d", client.id, total)
		case req := <-h.replayCh:
			h.handleReplay(req)
		case event := <-h.broadcast:
			h.broadcastEvent(event)
		}
	}
}

// Stop shuts down the hub.
func (h *WSHub) Stop() {
	h.stopOnce.Do(func() {
		close(h.done)
		h.clientsMu.Lock()
		defer h.clientsMu.Unlock()
		for client := range h.clients {
			delete(h.clients, client)
			close(client.send)
		}
	})
}

// nextSeq returns the next sequence number.
func (h *WSHub) nextSeq() int64 {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	h.seq++
	return h.seq
}

// broadcastEvent sends an event to all subscribed clients.
// Runs on the hub's Run goroutine: the bounded broadcast channel (Publish
// drops on overflow instead of blocking) keeps the persistence insert below
// off the publication hot path.
func (h *WSHub) broadcastEvent(event *WSEvent) {
	// Apply redaction first so the persisted copy matches the wire form and
	// replay cannot leak content that live delivery would have redacted.
	h.redactionMu.RLock()
	cfg := h.redactionCfg
	h.redactionMu.RUnlock()

	if cfg != nil && cfg.Enabled && cfg.Config.Mode != redaction.ModeOff {
		event.Data = redactWSEventData(event.Data, cfg.Config)
	}

	// Assign the frame cursor. When an event store is attached the persisted
	// seq is authoritative: it survives restarts and is what clients pass back
	// as `since` to resume (see the replay contract in ws_events.go).
	if store := h.eventStore(); store != nil {
		if stored, err := store.Store(event.Topic, event.EventType, event.Data); err == nil {
			event.Seq = stored.Seq
		} else {
			event.Seq = h.nextSeq()
			log.Printf("ws_events: store failed, using ephemeral seq topic=%s: %v", event.Topic, err)
		}
	} else {
		event.Seq = h.nextSeq()
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("ws marshal error: %v", err)
		return
	}

	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()

	for client := range h.clients {
		if client.isSubscribed(event.Topic) {
			select {
			case client.send <- data:
				// Delivery succeeded again: report and persist any gap this
				// client accumulated while it was slow.
				h.flushClientDrops(client, true)
			default:
				dropped := h.dropped.Add(1)
				h.noteClientDrop(client, event.Topic, event.Seq)
				log.Printf("ws client buffer full id=%s surface=websocket session= pane= queue_depth=%d dropped_count=%d latency_ms=0 decision=coalesce reason_codes=queue_depth,dropped_output", client.id, len(client.send), dropped)
			}
		}
	}
}

// redactWSEventData recursively redacts sensitive content in event data.
func redactWSEventData(data interface{}, cfg redaction.Config) interface{} {
	if cfg.Mode == redaction.ModeOff {
		return data
	}

	switch v := data.(type) {
	case string:
		result := redaction.ScanAndRedact(v, cfg)
		return result.Output
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(v))
		for key, val := range v {
			redacted[key] = redactWSEventData(val, cfg)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(v))
		for i, val := range v {
			redacted[i] = redactWSEventData(val, cfg)
		}
		return redacted
	default:
		return data
	}
}

// Publish publishes an event to a topic.
func (h *WSHub) Publish(topic, eventType string, data interface{}) {
	event := &WSEvent{
		Type:      WSMsgEvent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Topic:     topic,
		EventType: eventType,
		Data:      data,
	}
	select {
	case <-h.done:
		return
	case h.broadcast <- event:
	default:
		dropped := h.dropped.Add(1)
		log.Printf("ws broadcast buffer full, dropping event topic=%s surface=websocket session= pane= queue_depth=%d dropped_count=%d latency_ms=0 decision=coalesce reason_codes=queue_depth,dropped_output", topic, len(h.broadcast), dropped)
	}
}

func (h *WSHub) RegisterClient(client *WSClient) bool {
	if client == nil {
		return false
	}
	select {
	case <-h.done:
		return false
	case h.register <- client:
		return true
	}
}

func (h *WSHub) UnregisterClient(client *WSClient) {
	if client == nil {
		return
	}
	select {
	case <-h.done:
		return
	case h.unregister <- client:
	}
}

// isSubscribed checks if a client is subscribed to a topic.
func (c *WSClient) isSubscribed(topic string) bool {
	c.topicsMu.RLock()
	defer c.topicsMu.RUnlock()

	// Check exact match
	if _, ok := c.topics[topic]; ok {
		return true
	}

	// Check wildcard patterns
	// "global" matches all global.* topics
	// "sessions:*" matches all session topics
	// "panes:*" matches all pane topics
	for pattern := range c.topics {
		if matchTopic(pattern, topic) {
			return true
		}
	}
	return false
}

// matchTopic checks if a pattern matches a topic.
// Supports:
//   - "*" matches everything
//   - "prefix:*" matches prefix:anything
//   - exact match
func matchTopic(pattern, topic string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(topic, prefix)
	}
	return pattern == topic
}

// Subscribe adds topics to the client's subscription.
func (c *WSClient) Subscribe(topics []string) {
	c.topicsMu.Lock()
	defer c.topicsMu.Unlock()
	for _, topic := range topics {
		c.topics[topic] = struct{}{}
	}
	log.Printf("ws client subscribed id=%s topics=%v", c.id, topics)
}

// Unsubscribe removes topics from the client's subscription.
func (c *WSClient) Unsubscribe(topics []string) {
	c.topicsMu.Lock()
	defer c.topicsMu.Unlock()
	for _, topic := range topics {
		delete(c.topics, topic)
	}
	log.Printf("ws client unsubscribed id=%s topics=%v", c.id, topics)
}

// Topics returns the client's subscribed topics.
func (c *WSClient) Topics() []string {
	c.topicsMu.RLock()
	defer c.topicsMu.RUnlock()
	topics := make([]string, 0, len(c.topics))
	for t := range c.topics {
		topics = append(topics, t)
	}
	return topics
}

// WebSocket upgrader configuration.
// Note: CheckOrigin always returns true here because origin validation
// is performed in handleWebSocket using Server.checkWSOrigin() which has
// access to the configured allowed origins. This is necessary because
// CORS middleware does NOT apply to WebSocket upgrade requests.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Origin validation is performed in handleWebSocket
		return true
	},
}

// WebSocket timeouts.
const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = (wsPongWait * 9) / 10
	wsMaxMessageSize = 4096
)

func ParseAuthMode(raw string) (AuthMode, error) {
	mode := AuthMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case "", AuthModeLocal:
		return AuthModeLocal, nil
	case AuthModeAPIKey, AuthModeOIDC, AuthModeMTLS:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid auth mode %q (valid: local, api_key, oidc, mtls)", raw)
	}
}

func defaultLocalOrigins() []string {
	return []string{
		"http://localhost",
		"http://127.0.0.1",
		"http://[::1]",
		"https://localhost",
		"https://127.0.0.1",
		"https://[::1]",
	}
}

func applyDefaults(cfg *Config) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = AuthModeLocal
	}
	if cfg.Auth.OIDC.CacheTTL == 0 {
		cfg.Auth.OIDC.CacheTTL = defaultJWKSCacheTTL
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = defaultLocalOrigins()
	}
}

// ValidateConfig checks server configuration for security and completeness.
func ValidateConfig(cfg Config) error {
	applyDefaults(&cfg)

	mode, err := ParseAuthMode(string(cfg.Auth.Mode))
	if err != nil {
		return err
	}
	cfg.Auth.Mode = mode

	if mode == AuthModeAPIKey && cfg.Auth.APIKey == "" {
		return fmt.Errorf("auth mode api_key requires --api-key")
	}
	if mode == AuthModeOIDC {
		if cfg.Auth.OIDC.Issuer == "" {
			return fmt.Errorf("auth mode oidc requires --oidc-issuer")
		}
		if cfg.Auth.OIDC.JWKSURL == "" {
			return fmt.Errorf("auth mode oidc requires --oidc-jwks-url")
		}
	}
	if mode == AuthModeMTLS {
		if cfg.Auth.MTLS.CertFile == "" || cfg.Auth.MTLS.KeyFile == "" || cfg.Auth.MTLS.ClientCAFile == "" {
			return fmt.Errorf("auth mode mtls requires --mtls-cert, --mtls-key, and --mtls-ca")
		}
	}

	if mode == AuthModeLocal && !isLoopbackHost(cfg.Host) {
		return fmt.Errorf("refusing to bind %s without auth; set --auth-mode and required credentials", cfg.Host)
	}
	if cfg.PublicBaseURL != "" {
		parsed, err := url.Parse(cfg.PublicBaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid public base URL %q", cfg.PublicBaseURL)
		}
	}
	return nil
}

// New creates a new HTTP server.
func New(cfg Config) *Server {
	applyDefaults(&cfg)
	s := &Server{
		host:               cfg.Host,
		port:               cfg.Port,
		publicBaseURL:      cfg.PublicBaseURL,
		version:            cfg.Version,
		eventBus:           cfg.EventBus,
		stateStore:         cfg.StateStore,
		auth:               cfg.Auth,
		auditStore:         cfg.AuditStore,
		webUI:              cfg.WebUI,
		sseClients:         make(map[chan events.BusEvent]struct{}),
		corsAllowedOrigins: cfg.AllowedOrigins,
		jwksCache:          newJWKSCache(cfg.Auth.OIDC.CacheTTL),
		idempotencyStore:   NewIdempotencyStore(24 * time.Hour),
		jobStore:           NewJobStore(),
		wsHub:              NewWSHub(),
		spawnAgents: func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
			return robot.GetSpawn(ctx, opts, nil)
		},
		waitAgents: robot.GetWaitContext,
	}

	// Initialize pane output streaming
	streamCfg := tmux.DefaultPaneStreamerConfig()
	s.streamManager = tmux.NewStreamManager(tmux.DefaultClient, func(event tmux.StreamEvent) {
		// Publish pane output to WebSocket subscribers
		// Topic format: panes:session:pane_idx
		s.wsHub.Publish(streamTopicForTarget(event.Target), "pane.output", map[string]interface{}{
			"lines":   event.Lines,
			"seq":     event.Seq,
			"ts":      event.Timestamp.UTC().Format(time.RFC3339Nano),
			"is_full": event.IsFull,
		})
	}, streamCfg)

	s.router = s.buildRouter()
	return s
}

// DefaultAuditDir is where REST/WS audit records live when a caller does not
// choose a location.
func DefaultAuditDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for audit store: %w", err)
	}
	return filepath.Join(home, ".ntm", "audit"), nil
}

// buildRouter creates the chi router with all middleware and routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Base middleware stack
	r.Use(chimw.RealIP)
	r.Use(s.maxBytesMiddleware)
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.recovererMiddleware)
	r.Use(s.loggingMiddlewareFunc)
	r.Use(s.corsMiddlewareFunc)
	r.Use(s.authMiddlewareFunc)
	r.Use(s.rbacMiddleware) // Extract role from auth claims
	// Record every mutating request. This must run after rbacMiddleware so the
	// record carries the authenticated principal. Without it, AuditMiddleware had
	// no callers outside tests, so no audit record was ever written even though
	// handlers dutifully call SetAuditResource/SetAuditSession/SetAuditAction.
	if s.auditStore != nil {
		r.Use(s.AuditMiddleware(s.auditStore))
	}
	r.Use(s.redactionMiddleware) // Redact sensitive content in requests/responses
	// Idempotency-Key replay for EVERY mutating route — the single mount point
	// (D4, bd-ws3-contract-breadth-psvyu.4). Deleting this one line disables the
	// feature (rollback lever). The middleware is inert for requests without an
	// Idempotency-Key header and for non-mutating methods.
	//
	// Replay ordering note: this sits ABOVE the per-route RequirePermission
	// chains, so a cache-hit replay returns without re-running the permission
	// check. That is sound because scopedIdempotencyKey embeds the caller's
	// role and user ID: a cached entry can only exist for the same principal
	// whose original request already passed RequirePermission (only 2xx
	// responses are cached), and the role→permission mapping is static.
	// TestIdempotencyReplayStillEnforcesPermission covers the cross-principal
	// case; TestIdempotencyRouterCoverage covers every mutating route.
	r.Use(s.idempotencyMiddleware)

	// Health check (no versioning)
	r.Get("/health", s.handleHealth)

	// SSE event stream (no versioning)
	r.Get("/events", s.handleEventStream)

	// Kernel registry introspection (legacy path used by the web dashboard palette)
	r.With(s.RequirePermission(PermReadHealth)).Get("/api/kernel/commands", s.handleKernelCommands)

	// Legacy /api/* routes (maintained for backward compatibility during migration)
	r.Route("/api", func(r chi.Router) {
		r.Get("/sessions", s.handleSessions)
		r.Get("/sessions/{id}", s.handleSession)
		r.Get("/sessions/{id}/agents", func(w http.ResponseWriter, req *http.Request) {
			s.handleSessionAgents(w, req, chi.URLParam(req, "id"))
		})
		r.Get("/sessions/{id}/events", func(w http.ResponseWriter, req *http.Request) {
			s.handleSessionEvents(w, req, chi.URLParam(req, "id"))
		})
		r.Get("/robot/status", s.handleRobotStatus)
		r.Get("/robot/health", s.handleRobotHealth)
	})

	// /api/v1 routes (canonical)
	r.Route("/api/v1", func(r chi.Router) {
		// System endpoints (read-only, require PermReadHealth)
		r.With(s.RequirePermission(PermReadHealth)).Get("/health", s.handleHealthV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/version", s.handleVersionV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/capabilities", s.handleCapabilitiesV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/deps", s.handleDepsV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/doctor", s.handleDoctorV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/config", s.handleGetConfigV1)
		r.With(s.RequirePermission(PermSystemConfig)).Patch("/config", s.handlePatchConfigV1)

		// Sessions - read endpoints
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions", s.handleSessionsV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}", s.handleSessionV1)
		r.With(s.RequirePermission(PermReadAgents)).Get("/sessions/{id}/agents", func(w http.ResponseWriter, req *http.Request) {
			s.handleSessionAgentsV1(w, req, chi.URLParam(req, "id"))
		})
		r.With(s.RequirePermission(PermReadEvents)).Get("/sessions/{id}/events", func(w http.ResponseWriter, req *http.Request) {
			s.handleSessionEventsV1(w, req, chi.URLParam(req, "id"))
		})

		// Sessions - write endpoints (call kernel commands)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions", s.handleCreateSessionV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}/status", s.handleSessionStatusV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/attach", s.handleSessionAttachV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/zoom", s.handleSessionZoomV1)
		r.With(s.RequirePermission(PermWriteSessions)).Post("/sessions/{id}/view", s.handleSessionViewV1)

		// Robot endpoints (read-only) - thin adapters over robot package (bd-j9jo3.8.1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/status", s.handleRobotStatusV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/health", s.handleRobotHealthV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/snapshot", s.handleRobotSnapshotV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/digest", s.handleRobotDigestV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/attention", s.handleRobotAttentionV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/dashboard", s.handleRobotDashboardV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/terse", s.handleRobotTerseV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/triage", s.handleRobotTriageV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/plan", s.handleRobotPlanV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/graph", s.handleRobotGraphV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/activity", s.handleRobotActivityV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/alerts", s.handleRobotAlertsV1)

		// Panes API - manage tmux panes within sessions
		r.Route("/sessions/{sessionId}/panes", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleListPanesV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/{paneIdx}", s.handleGetPaneV1)
			r.With(s.RequirePermission(PermWriteSessions)).Post("/{paneIdx}/input", s.handlePaneInputV1)
			r.With(s.RequirePermission(PermWriteSessions)).Post("/{paneIdx}/interrupt", s.handlePaneInterruptV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/{paneIdx}/output", s.handlePaneOutputV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/{paneIdx}/title", s.handleGetPaneTitleV1)
			r.With(s.RequirePermission(PermWriteSessions)).Patch("/{paneIdx}/title", s.handleSetPaneTitleV1)
			// Streaming endpoints
			r.With(s.RequirePermission(PermWriteSessions)).Post("/{paneIdx}/stream", s.handleStartPaneStreamV1)
			r.With(s.RequirePermission(PermWriteSessions)).Delete("/{paneIdx}/stream", s.handleStopPaneStreamV1)
		})

		// Streaming stats endpoint
		r.With(s.RequirePermission(PermReadHealth)).Get("/streaming/stats", s.handleStreamingStatsV1)

		// Agents API - manage AI agents within sessions
		r.Route("/sessions/{sessionId}/agents", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadAgents)).Get("/", s.handleListAgentsV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/spawn", s.handleAgentSpawnV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/send", s.handleAgentSendV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/interrupt", s.handleAgentInterruptV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/wait", s.handleAgentWaitV1)
		})

		// Jobs API - read requires PermReadJobs, write requires PermWriteJobs.
		// Idempotency replay is provided by the router-level mount above.
		r.Route("/jobs", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadJobs)).Get("/", s.handleListJobs)
			r.With(s.RequirePermission(PermWriteJobs)).Post("/", s.handleCreateJob)
			r.With(s.RequirePermission(PermReadJobs)).Get("/{id}", s.handleGetJob)
			r.With(s.RequirePermission(PermWriteJobs)).Delete("/{id}", s.handleCancelJob)
		})

		// Pipeline API
		s.registerPipelineRoutes(r)

		// Mail and Reservations API
		s.registerMailRoutes(r)

		// Beads and BV Robot API
		s.registerBeadsRoutes(r)

		// Scanner and Bug Reporting API
		s.registerScannerRoutes(r)

		// CASS and Memory API
		s.registerCASSRoutes(r)

		// Checkpoint and Rollback API
		s.registerCheckpointRoutes(r)

		// Safety, Policy, and Approvals API
		s.registerSafetyRoutes(r)

		// Accounts API - CAAM account management
		s.registerAccountsRoutes(r)

		// Attention Feed API - normalized event streaming for operator agents
		r.Route("/attention", func(r chi.Router) {
			// SSE stream with cursor-based replay
			r.With(s.RequirePermission(PermReadEvents)).Get("/stream", s.handleAttentionStreamV1)
			// HTTP replay for non-streaming clients
			r.With(s.RequirePermission(PermReadEvents)).Get("/events", s.handleAttentionEventsV1)
			// Digest endpoint for aggregated summary
			r.With(s.RequirePermission(PermReadEvents)).Get("/digest", s.handleAttentionDigestV1)
			// Durable operator state mutations for individual attention items
			r.With(s.RequirePermission(PermWriteSessions)).Post("/items/{cursor}/state", s.handleAttentionItemStateV1)
		})

		// WebSocket endpoint (requires read permission)
		r.With(s.RequirePermission(PermReadWebSocket)).Get("/ws", s.handleWebSocket)

		// OpenAPI specification endpoint
		r.With(s.RequirePermission(PermReadHealth)).Get("/openapi.json", s.handleOpenAPISpec)
	})

	// Swagger UI documentation (outside /api/v1, no auth required)
	r.Get("/docs", s.handleSwaggerUI)
	r.Get("/docs/", s.handleSwaggerUI)

	// Embedded web UI (F1b): every non-API path falls through to the static
	// export. Registered as the NotFound handler so it can never shadow an
	// API route, and it runs behind the full middleware chain above.
	if s.webUI != nil {
		r.NotFound(s.webUIHandler())
	}

	return r
}

// Start starts the HTTP server and blocks until shutdown.
func (s *Server) Start(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}

	// Set the robot projection store once for the server's lifetime.
	// The store never changes, so there is no need to set it per-handler.
	if s.stateStore != nil {
		robot.SetProjectionStore(s.stateStore)
	}

	// Reconcile state store with tmux reality on startup.
	// Marks sessions as "terminated" if their tmux session no longer exists.
	if s.stateStore != nil {
		if result, err := s.stateStore.ReconcileSessions(); err != nil {
			slog.Warn("state-tmux reconciliation failed on startup", "error", err)
		} else if len(result.Terminated) > 0 {
			slog.Info("state-tmux reconciliation completed",
				"terminated", result.Terminated,
				"checked", result.Checked)
		}
	}

	// Wire WS event persistence/replay before the hub starts broadcasting so
	// every published event is Store()d and carries a durable seq. Stopped
	// after the hub (LIFO defers) so late drop-range flushes still land.
	s.ensureWSEventStore()
	defer func() {
		if st := s.wsHub.eventStore(); st != nil {
			st.Stop()
		}
	}()

	// Start WebSocket hub
	s.ensureWSHubRunning()
	defer s.wsHub.Stop()

	// Cleanup pane streaming on shutdown
	defer s.streamManager.StopAll()

	// Subscribe to events for SSE and WebSocket broadcasting
	if s.eventBus != nil {
		unsubscribe := s.eventBus.SubscribeAll(func(e events.BusEvent) {
			s.broadcastEvent(e)
			// Also broadcast to WebSocket clients
			topic := "global:events"
			if session := e.EventSession(); session != "" {
				topic = "sessions:" + session
			}
			s.wsHub.Publish(topic, e.EventType(), e)
		})
		defer unsubscribe()
	}

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.host, s.port),
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // Disabled to support long-lived SSE streams at /events
		IdleTimeout:  60 * time.Second,
	}

	scheme := "http"
	if s.auth.Mode == AuthModeMTLS {
		scheme = "https"
	}
	log.Printf("Starting NTM server on %s://%s:%d (auth=%s)", scheme, s.host, s.port, s.auth.Mode)

	// Start server in goroutine
	errCh := make(chan error, 1)
	go func() {
		var err error
		if s.auth.Mode == AuthModeMTLS {
			tlsConfig, tlsErr := s.buildMTLSConfig()
			if tlsErr != nil {
				errCh <- tlsErr
				return
			}
			s.server.TLSConfig = tlsConfig
			err = s.server.ListenAndServeTLS(s.auth.MTLS.CertFile, s.auth.MTLS.KeyFile)
		} else {
			err = s.server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) ensureWSHubRunning() {
	if s == nil || s.wsHub == nil {
		return
	}
	s.wsHubStartOnce.Do(func() {
		go s.wsHub.Run()
	})
}

// ensureWSEventStore wires the WebSocket event persistence/replay store to the
// hub, backed by the same runtime state store the server already opened
// (ws_events tables from migration 006_ws_events.sql). Degrades gracefully:
// without a state store (or without the migrated schema) WebSocket streaming
// still works — replay falls back to the in-memory ring buffer or is reported
// unavailable — and the condition is logged exactly once.
func (s *Server) ensureWSEventStore() {
	if s == nil || s.wsHub == nil {
		return
	}
	s.wsEventStoreOnce.Do(func() {
		if s.stateStore == nil {
			log.Printf("ws_events: no state store configured; event persistence and cursor replay disabled")
			return
		}
		db := s.stateStore.DB()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM ws_events").Scan(&n); err != nil {
			log.Printf("ws_events: schema unavailable (%v); events buffered in memory only, no replay across restarts", err)
			s.wsHub.SetEventStore(NewWSEventStore(nil, DefaultWSEventStoreConfig()))
			return
		}
		store := NewWSEventStore(db, DefaultWSEventStoreConfig())
		s.wsHub.SetEventStore(store)
		log.Printf("ws_events: persistence enabled path=%s current_seq=%d", s.stateStore.Path(), store.CurrentSeq())
	})
}

func (s *Server) validate() error {
	cfg := Config{
		Host:           s.host,
		Port:           s.port,
		EventBus:       s.eventBus,
		StateStore:     s.stateStore,
		Auth:           s.auth,
		AllowedOrigins: s.corsAllowedOrigins,
	}
	applyDefaults(&cfg)
	mode, err := ParseAuthMode(string(cfg.Auth.Mode))
	if err != nil {
		return err
	}
	cfg.Auth.Mode = mode
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	s.host = cfg.Host
	s.port = cfg.Port
	s.auth = cfg.Auth
	s.corsAllowedOrigins = cfg.AllowedOrigins
	return nil
}

func (s *Server) buildMTLSConfig() (*tls.Config, error) {
	if s.auth.MTLS.CertFile == "" || s.auth.MTLS.KeyFile == "" || s.auth.MTLS.ClientCAFile == "" {
		return nil, fmt.Errorf("mtls requires cert, key, and client CA files")
	}
	caPEM, err := os.ReadFile(s.auth.MTLS.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read mtls CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse mtls CA: no certs found")
	}
	return &tls.Config{
		ClientCAs:  caPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// maxBytesMiddleware limits the size of request bodies to prevent DoS via OOM.
func (s *Server) maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit to 10MB default
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		next.ServeHTTP(w, r)
	})
}

// requestIDMiddlewareFunc is the chi middleware version of requestIDMiddleware.
func (s *Server) requestIDMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := sanitizeRequestID(r.Header.Get(requestIDHeader))
		if reqID == "" {
			reqID = generateRequestID()
		}
		w.Header().Set(requestIDHeader, reqID)
		ctx := context.WithValue(r.Context(), requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recovererMiddleware catches panics and returns a proper JSON error response.
func (s *Server) recovererMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := requestIDFromContext(r.Context())
				stack := string(debug.Stack())
				log.Printf("PANIC recovered: %v request_id=%s\n%s", rec, reqID, stack)
				writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "internal server error", nil, reqID)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// loggingMiddlewareFunc is the chi middleware version.
func (s *Server) loggingMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		reqID := requestIDFromContext(r.Context())
		log.Printf("%s %s %d %s request_id=%s", r.Method, r.URL.Path, ww.Status(), time.Since(start), reqID)
	})
}

// corsMiddlewareFunc is the chi middleware version.
func (s *Server) corsMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			s.mu.RLock()
			allowed := originAllowed(origin, s.corsAllowedOrigins)
			s.mu.RUnlock()
			if !allowed {
				reqID := requestIDFromContext(r.Context())
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "origin not allowed", nil, reqID)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Idempotency-Key, "+requestIDHeader)
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authMiddlewareFunc is the chi middleware version.
func (s *Server) authMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		claims, err := s.authenticateRequest(r)
		if err != nil {
			reqID := requestIDFromContext(r.Context())
			log.Printf("auth failed mode=%s path=%s remote=%s request_id=%s err=%v", s.auth.Mode, r.URL.Path, r.RemoteAddr, reqID, err)
			writeErrorResponse(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized", nil, reqID)
			return
		}

		if claims != nil {
			ctx := context.WithValue(r.Context(), authContextKey, claims)
			r = r.WithContext(ctx)
		}

		next.ServeHTTP(w, r)
	})
}

// idempotencyHandledKey marks a request as already processed by
// idempotencyMiddleware. Some routes stack the middleware a second time on
// top of the global r.Use (e.g. beads /close); without this marker the inner
// instance would wait on the outer instance's in-flight reservation forever.
type idempotencyHandledKeyType struct{}

var idempotencyHandledKey idempotencyHandledKeyType

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
		fingerprint := idempotencyRequestFingerprint(r)

		// Single-flight per scoped key: concurrent duplicates wait for the
		// first request instead of double-executing (bd-vq37v). The loop
		// re-checks the cache after each leader finishes; if the leader failed
		// (nothing cached), the woken follower becomes the new leader.
		for {
			if cached, status, replayHeader, storedFP, ok := s.idempotencyStore.GetWithFingerprint(key); ok {
				if storedFP != "" && fingerprint != "" && storedFP != fingerprint {
					reqID := requestIDFromContext(r.Context())
					writeErrorResponse(w, http.StatusUnprocessableEntity, ErrCodeIdempotentReplay,
						"Idempotency-Key reused with a different request body", nil, reqID)
					return
				}
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
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Cache successful, non-hijacked, bounded responses
		if rec.statusCode >= 200 && rec.statusCode < 300 && !rec.hijacked && !rec.overflow {
			s.idempotencyStore.SetWithFingerprint(key, fingerprint, rec.body, rec.statusCode, rec.Header())
		}
	})
}

// idempotencyRequestFingerprint hashes the request body (bounded by
// idempotencyFingerprintLimit) and restores r.Body for downstream handlers.
// Returns a non-empty hash even for empty bodies so mismatch detection always
// has something to compare.
func idempotencyRequestFingerprint(r *http.Request) string {
	h := sha256.New()
	if r.Body != nil && r.Body != http.NoBody {
		data, err := io.ReadAll(io.LimitReader(r.Body, idempotencyFingerprintLimit))
		if err != nil {
			// Body unreadable; hand the bytes read so far plus the original
			// reader back to the handler, which will surface the read error.
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), r.Body))
			return ""
		}
		h.Write(data)
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), r.Body))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// responseRecorder captures the response for idempotency caching.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
	// overflow is set when the response exceeds idempotencyMaxBodyBytes; the
	// buffer stops growing and the response is not cached (bd-vq37v).
	overflow bool
	// hijacked is set when the handler took over the connection; nothing
	// meaningful can be cached afterwards.
	hijacked bool
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.overflow {
		if len(r.body)+len(b) > idempotencyMaxBodyBytes {
			r.overflow = true
			r.body = nil
		} else {
			r.body = append(r.body, b...)
		}
	}
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Bytes() []byte {
	return r.body
}

// Flush forwards http.Flusher so streaming mutating handlers keep working
// when an Idempotency-Key is present (bd-vq37v).
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards http.Hijacker (e.g. websocket upgrades behind a mutating
// route). A hijacked response is never cached.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("serve: underlying ResponseWriter does not support hijacking")
	}
	r.hijacked = true
	return h.Hijack()
}

// Unwrap supports http.ResponseController pass-through.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
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
	clone := make(http.Header, 2)
	for _, key := range []string{"Content-Type", requestIDHeader} {
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

func sanitizeRequestID(id string) string {
	if id == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(id), 64))
	for _, r := range id {
		if b.Len() >= 64 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == '/':
			b.WriteRune(r)
		case r == ':':
			// Preserve namespace-like IDs while avoiding colon semantics in logs/headers.
			b.WriteByte('.')
		}
	}
	return b.String()
}

func (s *Server) authenticateRequest(r *http.Request) (map[string]interface{}, error) {
	// Every successful path must return claims carrying a role. Returning nil
	// claims left RBAC with an empty claim set, and extractRoleFromClaims only
	// short-circuits to admin for local mode — so api_key and mtls callers fell
	// through to RoleViewer and every mutating endpoint answered 403. Enabling
	// authentication must not disable the write API.
	switch s.auth.Mode {
	case AuthModeAPIKey:
		if err := s.authenticateAPIKey(r); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"role": string(s.auth.sharedCredentialRole()),
			"sub":  "api-key",
		}, nil
	case AuthModeOIDC:
		return s.authenticateOIDC(r)
	case AuthModeMTLS:
		if err := s.authenticateMTLS(r); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"role": string(s.auth.sharedCredentialRole()),
			"sub":  mtlsClientSubject(r),
		}, nil
	case AuthModeLocal, "":
		return map[string]interface{}{"role": string(RoleAdmin)}, nil
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", s.auth.Mode)
	}
}

// mtlsClientSubject names the authenticated client for audit and RBAC context.
func mtlsClientSubject(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "mtls-client"
	}
	if cn := strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName); cn != "" {
		return cn
	}
	return "mtls-client"
}

func (s *Server) authenticateAPIKey(r *http.Request) error {
	if s.auth.APIKey == "" {
		return errors.New("api key not configured")
	}
	key := extractAPIKey(r)
	if key == "" {
		return errors.New("missing api key")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.auth.APIKey)) != 1 {
		return errors.New("invalid api key")
	}
	return nil
}

func (s *Server) authenticateOIDC(r *http.Request) (map[string]interface{}, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, errors.New("missing bearer token")
	}
	return s.validateOIDCToken(r.Context(), token)
}

func (s *Server) authenticateMTLS(r *http.Request) error {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return errors.New("missing client certificate")
	}
	return nil
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON: %v", err)
	}
}

// toJSONMap converts any value to map[string]interface{} via JSON round-trip.
func toJSONMap(v any) (map[string]interface{}, error) {
	// Arrays-never-null holds on the REST surface too: the CLI encode paths
	// normalize through NormalizeArraysNeverNull, and every robot envelope
	// served over HTTP funnels through here (bd-ws3-contract-breadth-psvyu.2).
	// Normalize returns a fixed copy for unaddressable (non-pointer) payloads.
	v = robot.NormalizeArraysNeverNull(v)
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]interface{})
	}
	return m, nil
}

// writeError writes an error response.
// Deprecated: Use writeErrorResponse for better robot mode compatibility.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"success": false,
		"error":   message,
	})
}

// writeErrorResponse writes a structured error response matching robot mode format.
func writeErrorResponse(w http.ResponseWriter, status int, code, message string, details map[string]interface{}, requestID string) {
	var hint string
	if details != nil {
		if v, ok := details["hint"]; ok {
			if s, ok := v.(string); ok && s != "" {
				hint = s
			}
			delete(details, "hint")
			if len(details) == 0 {
				details = nil
			}
		}
	}

	resp := APIError{
		APIResponse: APIResponse{
			Success:   false,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			RequestID: requestID,
		},
		Error:     message,
		ErrorCode: code,
		Details:   details,
		Hint:      hint,
	}
	writeJSON(w, status, resp)
}

// writeSuccessResponse writes a success response with the given data.
func writeSuccessResponse(w http.ResponseWriter, status int, data map[string]interface{}, requestID string) {
	if data == nil {
		data = make(map[string]interface{})
	}
	data["success"] = true
	data["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	if requestID != "" {
		data["request_id"] = requestID
	}
	writeJSON(w, status, data)
}

func robotErrorHTTPStatus(code string) int {
	switch code {
	case robot.ErrCodeInvalidFlag:
		return http.StatusBadRequest
	case robot.ErrCodeSessionNotFound, robot.ErrCodePaneNotFound, robot.ErrCodeNotFound:
		return http.StatusNotFound
	case robot.ErrCodePermissionDenied:
		return http.StatusForbidden
	case robot.ErrCodeResourceBusy:
		return http.StatusConflict
	case robot.ErrCodeTimeout:
		return http.StatusGatewayTimeout
	case robot.ErrCodeNotImplemented:
		return http.StatusNotImplemented
	case robot.ErrCodeDependencyMissing:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// decodeOptionalJSONBody decodes an optional JSON request body into dst.
// An empty body is treated as "not provided", while malformed JSON is rejected.
func decodeOptionalJSONBody(r *http.Request, dst interface{}) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return nil
	}

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	return nil
}

// handleHealth handles health check requests.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"status":  "healthy",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// handleSessions handles /api/sessions - list all sessions.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.stateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not available")
		return
	}

	sessions, err := s.stateStore.ListSessions("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// handleSession handles /api/sessions/{id} - get session details.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "session ID required")
		return
	}
	sessionID := parts[0]

	if s.stateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not available")
		return
	}

	session, err := s.stateStore.GetSession(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if session == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// Check for sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "agents":
			s.handleSessionAgents(w, r, sessionID)
			return
		case "events":
			s.handleSessionEvents(w, r, sessionID)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"session": session,
	})
}

// handleSessionAgents handles /api/sessions/{id}/agents.
func (s *Server) handleSessionAgents(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.stateStore == nil {
		writeError(w, http.StatusServiceUnavailable, "state store not available")
		return
	}

	agents, err := s.stateStore.ListAgents(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"session_id": sessionID,
		"agents":     agents,
		"count":      len(agents),
	})
}

// handleSessionEvents handles /api/sessions/{id}/events.
func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request, sessionID string) {
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}

	// Get recent events from event bus history
	eventsData := s.eventBus.History(100)

	// Filter to session if specified
	var filtered []events.BusEvent
	for _, e := range eventsData {
		if sessionID == "" || e.EventSession() == sessionID {
			filtered = append(filtered, e)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"session_id": sessionID,
		"events":     filtered,
		"count":      len(filtered),
	})
}

// handleRobotStatus handles /api/robot/status - proxies to robot status.
func (s *Server) handleRobotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	output, err := robot.GetStatusWithOptions(robot.PaginationOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, output)
}

// handleRobotHealth handles /api/robot/health.
func (s *Server) handleRobotHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"note":      "full robot health requires robot package integration",
	})
}

// handleEventStream handles SSE event streaming at /events.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Create client channel
	clientCh := make(chan events.BusEvent, 100)
	s.addSSEClient(clientCh)
	defer s.removeSSEClient(clientCh)

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send initial connection event
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\",\"time\":\"%s\"}\n\n",
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return
	}
	flusher.Flush()

	// Stream events
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-clientCh:
			data, err := json.Marshal(map[string]interface{}{
				"type":      event.EventType(),
				"timestamp": event.EventTimestamp().Format(time.RFC3339),
				"session":   event.EventSession(),
			})
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.EventType(), data); err != nil {
				return // Client disconnected
			}
			flusher.Flush()
		}
	}
}

// addSSEClient adds a client to the SSE broadcast list.
func (s *Server) addSSEClient(ch chan events.BusEvent) {
	s.sseClientsMu.Lock()
	defer s.sseClientsMu.Unlock()
	s.sseClients[ch] = struct{}{}
}

// removeSSEClient removes a client from the SSE broadcast list.
func (s *Server) removeSSEClient(ch chan events.BusEvent) {
	s.sseClientsMu.Lock()
	defer s.sseClientsMu.Unlock()
	delete(s.sseClients, ch)
	close(ch)
}

// broadcastEvent sends an event to all SSE clients.
func (s *Server) broadcastEvent(event events.BusEvent) {
	s.sseClientsMu.RLock()
	defer s.sseClientsMu.RUnlock()

	for ch := range s.sseClients {
		select {
		case ch <- event:
		default:
			// Client buffer full, skip
		}
	}
}

func generateRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	val, ok := ctx.Value(requestIDKey).(string)
	if !ok {
		return ""
	}
	return val
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.Fields(auth)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func extractAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return extractBearerToken(r)
}

func originAllowed(origin string, allowlist []string) bool {
	if origin == "" {
		return true
	}
	if len(allowlist) == 0 {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			return true
		}
		if strings.Contains(allowed, "://") {
			allowedURL, err := url.Parse(allowed)
			if err != nil {
				continue
			}
			if strings.EqualFold(allowedURL.Scheme, u.Scheme) && strings.EqualFold(allowedURL.Hostname(), host) {
				if allowedURL.Port() == "" || allowedURL.Port() == u.Port() {
					return true
				}
			}
			continue
		}
		if strings.Contains(allowed, ":") {
			if strings.EqualFold(allowed, u.Host) {
				return true
			}
			continue
		}
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return true
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	}
	if strings.Contains(h, ":") {
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func (s *Server) validateOIDCToken(ctx context.Context, token string) (map[string]interface{}, error) {
	if s.auth.OIDC.JWKSURL == "" || s.auth.OIDC.Issuer == "" {
		return nil, errors.New("oidc config incomplete")
	}
	header, claims, signingInput, signature, err := parseJWT(token)
	if err != nil {
		return nil, err
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported jwt alg %q", header.Alg)
	}
	if iss, ok := claimString(claims, "iss"); !ok || iss != s.auth.OIDC.Issuer {
		return nil, fmt.Errorf("invalid issuer")
	}
	if s.auth.OIDC.Audience != "" && !claimAudienceContains(claims, s.auth.OIDC.Audience) {
		return nil, fmt.Errorf("invalid audience")
	}
	// exp is mandatory and must be fail-CLOSED. Treating it as optional made a
	// token with no exp claim — or one whose exp an IdP serialized as a JSON
	// string rather than a number, which claimInt64 reports identically to
	// "absent" — authenticate forever. Only JWKS keys are re-fetched, so
	// revocation at the IdP never reaches such a token: it stays a permanent
	// credential for every write route on this server.
	exp, ok := claimInt64(claims, "exp")
	if !ok {
		return nil, fmt.Errorf("token has no usable exp claim")
	}
	if time.Now().After(time.Unix(exp, 0).Add(30 * time.Second)) {
		return nil, fmt.Errorf("token expired")
	}
	// nbf is optional, but a present-and-unparseable nbf is a malformed token,
	// not an absent claim; do not silently skip the check for it.
	if raw, present := claims["nbf"]; present {
		nbf, ok := claimInt64(claims, "nbf")
		if !ok {
			return nil, fmt.Errorf("token has an unusable nbf claim of type %T", raw)
		}
		if time.Now().Before(time.Unix(nbf, 0).Add(-30 * time.Second)) {
			return nil, fmt.Errorf("token not yet valid")
		}
	}
	key, err := s.jwksCache.getKey(ctx, s.auth.OIDC.JWKSURL, header.Kid)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		return nil, fmt.Errorf("invalid token signature")
	}
	return claims, nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

func parseJWT(token string) (jwtHeader, map[string]interface{}, string, []byte, error) {
	var header jwtHeader
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return header, nil, "", nil, fmt.Errorf("invalid jwt format")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt header: %w", err)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt signature: %w", err)
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return header, nil, "", nil, fmt.Errorf("parse jwt header: %w", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return header, nil, "", nil, fmt.Errorf("parse jwt payload: %w", err)
	}
	return header, claims, parts[0] + "." + parts[1], signature, nil
}

func claimString(claims map[string]interface{}, key string) (string, bool) {
	raw, ok := claims[key]
	if !ok {
		return "", false
	}
	str, ok := raw.(string)
	return str, ok
}

func claimInt64(claims map[string]interface{}, key string) (int64, bool) {
	raw, ok := claims[key]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case json.Number:
		val, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return val, true
	default:
		return 0, false
	}
}

func claimAudienceContains(claims map[string]interface{}, expected string) bool {
	raw, ok := claims["aud"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case string:
		return v == expected
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	}
	return false
}

type jwksCache struct {
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	ttl       time.Duration
}

func newJWKSCache(ttl time.Duration) *jwksCache {
	if ttl <= 0 {
		ttl = defaultJWKSCacheTTL
	}
	return &jwksCache{
		keys: make(map[string]*rsa.PublicKey),
		ttl:  ttl,
	}
}

func (c *jwksCache) getKey(ctx context.Context, jwksURL, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	if time.Since(c.fetchedAt) < c.ttl && len(c.keys) > 0 {
		if kid == "" && len(c.keys) == 1 {
			for _, key := range c.keys {
				c.mu.Unlock()
				return key, nil
			}
		}
		if key, ok := c.keys[kid]; ok {
			c.mu.Unlock()
			return key, nil
		}
	}
	c.mu.Unlock()

	keys, err := fetchJWKSKeys(ctx, jwksURL)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = time.Now()
	c.mu.Unlock()

	if kid == "" && len(keys) == 1 {
		for _, key := range keys {
			return key, nil
		}
	}
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwt kid not found in jwks")
	}
	return key, nil
}

type jwksPayload struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func fetchJWKSKeys(ctx context.Context, jwksURL string) (map[string]*rsa.PublicKey, error) {
	if jwksURL == "" {
		return nil, fmt.Errorf("jwks url missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build jwks request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read small error snippet
		return nil, fmt.Errorf("fetch jwks: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Limit JWKS to 1MB to prevent memory exhaustion DoS
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	var payload jwksPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, key := range payload.Keys {
		if key.Kty != "RSA" || key.N == "" || key.E == "" {
			continue
		}
		pub, err := parseRSAPublicKey(key.N, key.E)
		if err != nil {
			continue
		}
		kid := key.Kid
		if kid == "" {
			kid = "default"
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid RSA keys in jwks")
	}
	return keys, nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode jwk n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode jwk e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if e.Sign() <= 0 {
		return nil, fmt.Errorf("invalid jwk exponent")
	}
	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// =============================================================================
// API v1 Handlers
// =============================================================================

// handleHealthV1 handles GET /api/v1/health.
func (s *Server) handleHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"status": "healthy",
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

	// Return safe configuration fields only (no secrets)
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"host":            s.host,
		"port":            s.port,
		"auth_mode":       string(s.auth.Mode),
		"allowed_origins": origins,
		"public_base_url": s.publicBaseURL,
		"project_dir":     projDir,
	}, reqID)
}

// handlePatchConfigV1 handles PATCH /api/v1/config.
// Updates mutable configuration fields at runtime.
func (s *Server) handlePatchConfigV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	slog.Info("config patch", "request_id", reqID)

	var req struct {
		AllowedOrigins []string `json:"allowed_origins,omitempty"`
		ProjectDir     string   `json:"project_dir,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}

	// Apply updates and snapshot the result under the lock so the log
	// and response body see a consistent view.
	s.mu.Lock()
	if req.AllowedOrigins != nil {
		s.corsAllowedOrigins = req.AllowedOrigins
	}
	if req.ProjectDir != "" {
		s.projectDir = req.ProjectDir
		// Reset mail client so it picks up new project dir
		s.mailClient = nil
		// A new project directory invalidates the cached unavailability verdict too.
		s.mailUnavailableUntil = time.Time{}
	}
	currentOrigins := s.corsAllowedOrigins
	currentProjectDir := s.projectDir
	s.mu.Unlock()

	slog.Info("config updated", "request_id", reqID, "allowed_origins", len(currentOrigins), "project_dir", currentProjectDir)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"config": map[string]interface{}{
			"allowed_origins": currentOrigins,
			"project_dir":     currentProjectDir,
		},
	}, reqID)
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

// validateSessionParam checks that a session ID from a URL parameter is
// non-empty and satisfies tmux naming rules. It writes an error response and
// returns false when validation fails, so callers can return early.
func validateSessionParam(w http.ResponseWriter, sessionID, reqID string) bool {
	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return false
	}
	if err := tmux.ValidateSessionName(sessionID); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("invalid session ID: %s", err.Error()), nil, reqID)
		return false
	}
	return true
}

// validatePaneIdx parses a pane index string and validates it is non-negative.
// It writes an error response and returns -1 when validation fails.
func validatePaneIdx(w http.ResponseWriter, paneIdxStr, reqID string) int {
	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return -1
	}
	if paneIdx < 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "pane index must be non-negative", nil, reqID)
		return -1
	}
	return paneIdx
}

// handleSessionsV1 handles GET /api/v1/sessions.
func (s *Server) handleSessionsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	sessions, err := s.stateStore.ListSessions("")
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Ensure sessions is never null
	if sessions == nil {
		sessions = []state.Session{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
	}, reqID)
}

// handleSessionV1 handles GET /api/v1/sessions/{id}.
func (s *Server) handleSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	session, err := s.stateStore.GetSession(sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if session == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "session not found", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session": session,
	}, reqID)
}

// handleSessionAgentsV1 handles GET /api/v1/sessions/{id}/agents.
func (s *Server) handleSessionAgentsV1(w http.ResponseWriter, r *http.Request, sessionID string) {
	reqID := requestIDFromContext(r.Context())

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	agents, err := s.stateStore.ListAgents(sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Ensure agents is never null
	if agents == nil {
		agents = []state.Agent{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"agents":     agents,
		"count":      len(agents),
	}, reqID)
}

// handleSessionEventsV1 handles GET /api/v1/sessions/{id}/events.
func (s *Server) handleSessionEventsV1(w http.ResponseWriter, r *http.Request, sessionID string) {
	reqID := requestIDFromContext(r.Context())

	if s.eventBus == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "event bus not available", nil, reqID)
		return
	}

	eventsData := s.eventBus.History(100)

	var filtered []events.BusEvent
	for _, e := range eventsData {
		if sessionID == "" || e.EventSession() == sessionID {
			filtered = append(filtered, e)
		}
	}

	// Ensure events is never null
	if filtered == nil {
		filtered = []events.BusEvent{}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"events":     filtered,
		"count":      len(filtered),
	}, reqID)
}

// handleRobotStatusV1 handles GET /api/v1/robot/status.
func (s *Server) handleRobotStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetStatusWithOptions(robot.PaginationOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot status", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotHealthV1 handles GET /api/v1/robot/health.
func (s *Server) handleRobotHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetHealth()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot health", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotSnapshotV1 handles GET /api/v1/robot/snapshot.
func (s *Server) handleRobotSnapshotV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetSnapshotWithOptions(nil, robot.PaginationOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot snapshot", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotDigestV1 handles GET /api/v1/robot/digest.
func (s *Server) handleRobotDigestV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetDigest(robot.DigestOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot digest", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotAttentionV1 handles GET /api/v1/robot/attention.
// Returns attention digest with focus on action-required items (non-blocking).
func (s *Server) handleRobotAttentionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	// Use digest with attention-focused profile
	output, err := robot.GetDigest(robot.DigestOptions{
		Profile:             r.URL.Query().Get("profile"),
		ActionRequiredLimit: 10,
		InterestingLimit:    5,
		BackgroundLimit:     3,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot attention", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotDashboardV1 handles GET /api/v1/robot/dashboard.
func (s *Server) handleRobotDashboardV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetDashboard()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot dashboard", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotTerseV1 handles GET /api/v1/robot/terse.
func (s *Server) handleRobotTerseV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetTerse(nil)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot terse", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotTriageV1 handles GET /api/v1/robot/triage.
func (s *Server) handleRobotTriageV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetTriage(robot.TriageOptions{})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot triage", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotPlanV1 handles GET /api/v1/robot/plan.
func (s *Server) handleRobotPlanV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetPlan()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot plan", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotGraphV1 handles GET /api/v1/robot/graph.
func (s *Server) handleRobotGraphV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	output, err := robot.GetGraph()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot graph", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotActivityV1 handles GET /api/v1/robot/activity.
func (s *Server) handleRobotActivityV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	session := r.URL.Query().Get("session")
	output, err := robot.GetActivity(robot.ActivityOptions{Session: session})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot activity", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// handleRobotAlertsV1 handles GET /api/v1/robot/alerts.
func (s *Server) handleRobotAlertsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	includeResolved := r.URL.Query().Get("include_resolved") == "true"
	output, err := robot.GetAlertsDetailed(includeResolved)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	data, err := toJSONMap(output)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to encode robot alerts", map[string]interface{}{
			"error": err.Error(),
		}, reqID)
		return
	}
	if reqID != "" {
		data["request_id"] = reqID
	}
	writeJSON(w, http.StatusOK, data)
}

// =============================================================================
// Session Kernel Handlers (call kernel commands)
// =============================================================================

// CreateSessionRequest is the request body for POST /sessions.
type CreateSessionRequest struct {
	Session string `json:"session"`
	Panes   int    `json:"panes,omitempty"`
}

// handleCreateSessionV1 handles POST /api/v1/sessions.
func (s *Server) handleCreateSessionV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session name required", nil, reqID)
		return
	}

	if err := tmux.ValidateSessionName(req.Session); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest,
			fmt.Sprintf("invalid session name: %s", err.Error()), nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.create", map[string]interface{}{
		"session": req.Session,
		"panes":   req.Panes,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusCreated, data, reqID)
}

// handleSessionStatusV1 handles GET /api/v1/sessions/{id}/status.
func (s *Server) handleSessionStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.status", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionAttachV1 handles POST /api/v1/sessions/{id}/attach.
func (s *Server) handleSessionAttachV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.attach", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// SessionZoomRequest is the request body for POST /sessions/{id}/zoom.
type SessionZoomRequest struct {
	Pane int `json:"pane"`
}

// handleSessionZoomV1 handles POST /api/v1/sessions/{id}/zoom.
func (s *Server) handleSessionZoomV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req SessionZoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.zoom", map[string]interface{}{
		"session": sessionID,
		"pane":    req.Pane,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// handleSessionViewV1 handles POST /api/v1/sessions/{id}/view.
func (s *Server) handleSessionViewV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "id")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	result, err := kernel.Run(r.Context(), "sessions.view", map[string]interface{}{
		"session": sessionID,
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// =============================================================================
// Panes API Handlers
// =============================================================================

// handleListPanesV1 handles GET /api/v1/sessions/{sessionId}/panes.
func (s *Server) handleListPanesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	panes, err := tmux.GetPanesContext(r.Context(), sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Convert to serializable format
	paneList := make([]map[string]interface{}, len(panes))
	for i, p := range panes {
		paneList[i] = map[string]interface{}{
			"index":   p.Index,
			"id":      p.ID,
			"title":   p.Title,
			"type":    string(p.Type),
			"variant": p.Variant,
			"active":  p.Active,
			"width":   p.Width,
			"height":  p.Height,
			"command": p.Command,
		}
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"panes":      paneList,
		"count":      len(panes),
	}, reqID)
}

// handleGetPaneV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}.
func (s *Server) handleGetPaneV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	panes, err := tmux.GetPanesContext(r.Context(), sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Find the pane by index
	for _, p := range panes {
		if p.Index == paneIdx {
			writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
				"pane": map[string]interface{}{
					"index":   p.Index,
					"id":      p.ID,
					"title":   p.Title,
					"type":    string(p.Type),
					"variant": p.Variant,
					"active":  p.Active,
					"width":   p.Width,
					"height":  p.Height,
					"command": p.Command,
				},
			}, reqID)
			return
		}
	}

	writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "pane not found", nil, reqID)
}

// PaneInputRequest is the request body for POST /sessions/{id}/panes/{paneIdx}/input.
type PaneInputRequest struct {
	Text  string `json:"text"`
	Enter bool   `json:"enter,omitempty"`
}

// handlePaneInputV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/input.
func (s *Server) handlePaneInputV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	var req PaneInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Text == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "text required", nil, reqID)
		return
	}

	// Policy check: reject text that matches a blocked safety pattern.
	// Graceful degradation: if policy cannot be loaded, allow through but log.
	if p, policyErr := policy.LoadOrDefault(); policyErr != nil {
		slog.Warn("pane input policy check skipped: failed to load policy",
			"error", policyErr, "request_id", reqID)
	} else if match := p.Check(req.Text); match != nil && match.Action == policy.ActionBlock {
		slog.Warn("pane input blocked by policy",
			"session", sessionID, "pane", paneIdx,
			"pattern", match.Pattern, "request_id", reqID)
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			fmt.Sprintf("blocked by safety policy: %s", match.Reason), nil, reqID)
		return
	}

	// Build pane target. Resolve via the pane's tmux ID (the `%N` form) so
	// the target is base-index-independent — `<session>:<paneIdx>` looks
	// like a pane index but tmux interprets it as a window index, which
	// breaks on hosts with `base-index = 1` (see #141).
	pane, ok := s.resolvePaneForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	if err := pane.Type.ValidateAutomatedPromptDelivery(); err != nil {
		writeErrorResponse(w, http.StatusNotImplemented, ErrCodeNotImplemented, err.Error(), map[string]interface{}{
			"agent_type": pane.Type.Canonical().String(),
			"hint":       agent.GrokPromptDeliveryCapabilityHint,
		}, reqID)
		return
	}
	paneTarget := pane.ID

	sendKeys := tmux.SendKeys
	if s.sendPaneKeys != nil {
		sendKeys = s.sendPaneKeys
	}
	if err := sendKeys(paneTarget, req.Text, req.Enter); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	slog.Info("pane input sent via API",
		"session", sessionID, "pane", paneIdx,
		"text_len", len(req.Text), "request_id", reqID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"sent": true,
		"pane": paneTarget,
	}, reqID)
}

// handlePaneInterruptV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/interrupt.
func (s *Server) handlePaneInterruptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	// Send Ctrl+c to interrupt
	if err := tmux.SendKeys(paneTarget, "C-c", false); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"interrupted": true,
		"pane":        paneTarget,
	}, reqID)
}

// handlePaneOutputV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}/output.
func (s *Server) handlePaneOutputV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	// Parse lines parameter (default 100)
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if _, err := fmt.Sscanf(l, "%d", &lines); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid lines parameter", nil, reqID)
			return
		}
		if lines < 1 || lines > 10000 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be 1-10000", nil, reqID)
			return
		}
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	// Deliberately does NOT start a background streamer. This is a one-shot
	// read that captures the pane directly on the next line, so the streamer's
	// output went nowhere — but StreamManager only retires a streamer on an
	// explicit StopStream, and this handler never issued one. Every distinct
	// pane ever read this way leaked a goroutine that kept forking
	// `tmux capture-pane` forever, including long after its session was killed
	// (runPollingLoop treats a capture failure as `continue`). Starting a
	// server-side subscription was also a side effect on a GET. Callers that
	// want live output use POST/DELETE .../stream, which is the paired API.
	output, err := tmux.CapturePaneOutputContext(r.Context(), paneTarget, lines)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":   paneTarget,
		"output": output,
		"lines":  lines,
	}, reqID)
}

// handleGetPaneTitleV1 handles GET /api/v1/sessions/{sessionId}/panes/{paneIdx}/title.
func (s *Server) handleGetPaneTitleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	title, err := tmux.GetPaneTitle(paneTarget)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":  paneTarget,
		"title": title,
	}, reqID)
}

// PaneTitleRequest is the request body for PATCH /sessions/{id}/panes/{paneIdx}/title.
type PaneTitleRequest struct {
	Title string `json:"title"`
}

// handleSetPaneTitleV1 handles PATCH /api/v1/sessions/{sessionId}/panes/{paneIdx}/title.
func (s *Server) handleSetPaneTitleV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	var req PaneTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	paneTarget, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}

	if err := tmux.SetPaneTitle(paneTarget, req.Title); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"pane":  paneTarget,
		"title": req.Title,
	}, reqID)
}

// =============================================================================
// Streaming API Handlers
// =============================================================================

// handleStartPaneStreamV1 handles POST /api/v1/sessions/{sessionId}/panes/{paneIdx}/stream.
func (s *Server) handleStartPaneStreamV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	target, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	topic := streamTopicForTarget(target)

	if err := s.streamManager.StartStream(target); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"topic":   topic,
		"message": "streaming started",
	}, reqID)
}

// handleStopPaneStreamV1 handles DELETE /api/v1/sessions/{sessionId}/panes/{paneIdx}/stream.
func (s *Server) handleStopPaneStreamV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	paneIdx := validatePaneIdx(w, paneIdxStr, reqID)
	if paneIdx < 0 {
		return
	}

	target, ok := s.resolvePaneTargetForRequest(w, r, sessionID, paneIdx, reqID)
	if !ok {
		return
	}
	s.streamManager.StopStream(target)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"message": "streaming stopped",
	}, reqID)
}

func streamTopicForTarget(target string) string {
	if strings.HasPrefix(target, "panes:") {
		return target
	}
	return "panes:" + target
}

// handleStreamingStatsV1 handles GET /api/v1/streaming/stats.
func (s *Server) handleStreamingStatsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	stats := s.streamManager.Stats()
	stats["active_targets"] = s.streamManager.ListActive()

	writeSuccessResponse(w, http.StatusOK, stats, reqID)
}

// =============================================================================
// Agents API Handlers
// =============================================================================

// handleListAgentsV1 handles GET /api/v1/sessions/{sessionId}/agents.
func (s *Server) handleListAgentsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	panes, err := tmux.GetPanesContext(r.Context(), sessionID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Filter to only include recognized agent panes (not user/unknown)
	agents := make([]map[string]interface{}, 0, len(panes))
	for _, p := range panes {
		agentType := string(p.Type)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}
		agents = append(agents, map[string]interface{}{
			"pane_index": p.Index,
			"pane_id":    p.ID,
			"agent_type": agentType,
			"title":      p.Title,
			"variant":    p.Variant,
			"tags":       p.Tags,
			"active":     p.Active,
		})
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"agents":     agents,
		"count":      len(agents),
	}, reqID)
}

// AgentSpawnRequest is the request body for POST /sessions/{id}/agents/spawn.
type AgentSpawnRequest struct {
	CCCount   int    `json:"cc_count,omitempty"`
	CodCount  int    `json:"cod_count,omitempty"`
	GmiCount  int    `json:"gmi_count,omitempty"`
	AgyCount  int    `json:"agy_count,omitempty"`
	GrokCount int    `json:"grok_count,omitempty"`
	Preset    string `json:"preset,omitempty"`
	WaitReady bool   `json:"wait_ready,omitempty"`
	Label     string `json:"label,omitempty"` // Goal label for multi-session support
}

// handleAgentSpawnV1 handles POST /api/v1/sessions/{sessionId}/agents/spawn.
func (s *Server) handleAgentSpawnV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// At least one agent count or preset must be specified
	if req.CCCount == 0 && req.CodCount == 0 && req.GmiCount == 0 && req.AgyCount == 0 && req.GrokCount == 0 && req.Preset == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "at least one agent count (cc_count, cod_count, gmi_count, agy_count, grok_count) or preset required", nil, reqID)
		return
	}

	opts := robot.SpawnOptions{
		Session:   sessionID,
		Label:     req.Label,
		CCCount:   req.CCCount,
		CodCount:  req.CodCount,
		GmiCount:  req.GmiCount,
		AgyCount:  req.AgyCount,
		GrokCount: req.GrokCount,
		Preset:    req.Preset,
		WaitReady: req.WaitReady,
	}

	if s.spawnAgents == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "agent spawn service unavailable", nil, reqID)
		return
	}

	result, err := s.spawnAgents(r.Context(), opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent spawn returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentSendRequest is the request body for POST /sessions/{id}/agents/send.
type AgentSendRequest struct {
	Panes      []string `json:"panes,omitempty"`
	AgentTypes []string `json:"agent_types,omitempty"`
	Message    string   `json:"message"`
	All        bool     `json:"all,omitempty"`
}

// handleAgentSendV1 handles POST /api/v1/sessions/{sessionId}/agents/send.
func (s *Server) handleAgentSendV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Message == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "message required", nil, reqID)
		return
	}

	// Policy check: reject messages that match a blocked safety pattern.
	if p, policyErr := policy.LoadOrDefault(); policyErr != nil {
		slog.Warn("agent send policy check skipped: failed to load policy",
			"error", policyErr, "request_id", reqID)
	} else if match := p.Check(req.Message); match != nil && match.Action == policy.ActionBlock {
		slog.Warn("agent send blocked by policy",
			"session", sessionID, "pattern", match.Pattern, "request_id", reqID)
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
			fmt.Sprintf("blocked by safety policy: %s", match.Reason), nil, reqID)
		return
	}

	opts := robot.SendOptions{
		Session:        sessionID,
		Message:        req.Message,
		Panes:          req.Panes,
		AgentTypes:     req.AgentTypes,
		All:            req.All,
		RequestID:      reqID,
		CorrelationID:  reqID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}

	sendAgents := robot.GetSend
	if s.sendAgents != nil {
		sendAgents = s.sendAgents
	}
	result, err := sendAgents(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent send returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentInterruptRequest is the request body for POST /sessions/{id}/agents/interrupt.
type AgentInterruptRequest struct {
	Panes   []string `json:"panes,omitempty"`
	Message string   `json:"message,omitempty"`
	Force   bool     `json:"force,omitempty"`
	NoWait  bool     `json:"no_wait,omitempty"`
}

// handleAgentInterruptV1 handles POST /api/v1/sessions/{sessionId}/agents/interrupt.
func (s *Server) handleAgentInterruptV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentInterruptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Policy check on the follow-up message (if any).
	if req.Message != "" {
		if p, policyErr := policy.LoadOrDefault(); policyErr != nil {
			slog.Warn("agent interrupt policy check skipped: failed to load policy",
				"error", policyErr, "request_id", reqID)
		} else if match := p.Check(req.Message); match != nil && match.Action == policy.ActionBlock {
			slog.Warn("agent interrupt message blocked by policy",
				"session", sessionID, "pattern", match.Pattern, "request_id", reqID)
			writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
				fmt.Sprintf("blocked by safety policy: %s", match.Reason), nil, reqID)
			return
		}
	}

	opts := robot.InterruptOptions{
		Session:        sessionID,
		Panes:          req.Panes,
		Message:        req.Message,
		Force:          req.Force,
		NoWait:         req.NoWait,
		RequestID:      reqID,
		CorrelationID:  reqID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}

	interruptAgents := robot.GetInterrupt
	if s.interruptAgents != nil {
		interruptAgents = s.interruptAgents
	}
	result, err := interruptAgents(opts)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}
	if result == nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "agent interrupt returned no result", nil, reqID)
		return
	}
	if !result.Success {
		details := map[string]interface{}{}
		if result.Hint != "" {
			details["hint"] = result.Hint
		}
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, details, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
}

// AgentWaitRequest is the request body for POST /sessions/{id}/agents/wait.
type AgentWaitRequest struct {
	Condition   string `json:"condition"`
	TimeoutMs   int    `json:"timeout_ms,omitempty"`
	PollMs      int    `json:"poll_ms,omitempty"`
	Panes       []int  `json:"panes,omitempty"`
	AgentType   string `json:"agent_type,omitempty"`
	WaitForAny  bool   `json:"wait_for_any,omitempty"`
	ExitOnError bool   `json:"exit_on_error,omitempty"`
}

// handleAgentWaitV1 handles POST /api/v1/sessions/{sessionId}/agents/wait.
// maxAgentWaitTimeout caps the client-supplied wait so one request cannot pin
// a server goroutine indefinitely. Long orchestration waits belong in the
// caller's own polling loop, not in a single HTTP request.
const maxAgentWaitTimeout = 30 * time.Minute

func (s *Server) handleAgentWaitV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if !validateSessionParam(w, sessionID, reqID) {
		return
	}

	var req AgentWaitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Condition == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "condition required", nil, reqID)
		return
	}

	// Bound the client-supplied wait so an unbounded timeout_ms cannot pin
	// one server-side goroutine (and its tmux polling) indefinitely. Every
	// other client-supplied bound in this package is clamped; these two were
	// oversights.
	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	if timeout > maxAgentWaitTimeout {
		timeout = maxAgentWaitTimeout
	}
	pollInterval := 500 * time.Millisecond
	if req.PollMs > 0 {
		pollInterval = time.Duration(req.PollMs) * time.Millisecond
	}
	if pollInterval > timeout {
		pollInterval = timeout
	}

	opts := robot.WaitOptions{
		Session:           sessionID,
		Condition:         req.Condition,
		Timeout:           timeout,
		PollInterval:      pollInterval,
		PaneIndices:       req.Panes,
		AgentType:         req.AgentType,
		WaitForAny:        req.WaitForAny,
		ExitOnError:       req.ExitOnError,
		CountN:            1,
		RequireTransition: false,
	}

	waitAgents := s.waitAgents
	if waitAgents == nil {
		waitAgents = robot.GetWaitContext
	}
	result, exitCode := waitAgents(r.Context(), opts)
	if exitCode != 0 && !result.Success && result.ErrorCode != robot.ErrCodeTimeout {
		writeErrorResponse(w, robotErrorHTTPStatus(result.ErrorCode), result.ErrorCode, result.Error, nil, reqID)
		return
	}

	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	// A wait that times out is a successful observation of "the condition did
	// not become true in the window", and the payload already says so via
	// timed_out. It used to be returned as HTTP 408 through
	// writeSuccessResponse, which unconditionally injects "success": true —
	// so the two halves of one response contradicted each other, and 408
	// (which means the CLIENT was too slow, and which proxies and Go's
	// transport treat as connection-close-and-retry) was wrong on its own
	// terms. Only a genuine internal failure is an error status now.
	if exitCode >= 2 {
		writeErrorResponse(w, http.StatusInternalServerError, result.ErrorCode,
			result.Error, data, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, data, reqID)
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
		"jobs":  jobs,
		"count": len(jobs),
	}, reqID)
}

// CreateJobRequest is the request body for job creation.
type CreateJobRequest struct {
	Type    string                 `json:"type"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Session string                 `json:"session,omitempty"`
}

// handleCreateJob handles POST /api/v1/jobs.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
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

	job := s.jobStore.Create(req.Type)

	// Start real job execution in background
	go s.dispatchJob(job.ID, req)

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

	job := s.jobStore.Get(jobID)
	if job == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "job not found", nil, reqID)
		return
	}

	// Only allow cancelling pending or running jobs
	if job.Status != JobStatusPending && job.Status != JobStatusRunning {
		writeErrorResponse(w, http.StatusConflict, ErrCodeConflict, "job cannot be cancelled", map[string]interface{}{
			"status": job.Status,
		}, reqID)
		return
	}

	s.jobStore.Update(jobID, JobStatusCancelled, job.Progress, nil, "cancelled by user")
	// Stop the real work, not just the bookkeeping: cancel the dispatch
	// goroutine's context (no-op for jobs that already finished).
	s.jobStore.Cancel(jobID)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"job": s.jobStore.Get(jobID),
	}, reqID)
}

// Router returns the chi router for testing.
func (s *Server) Router() chi.Router {
	return s.router
}

// projectDirSnapshot returns the current project directory under a read lock.
//
// PATCH /api/v1/config writes s.projectDir under the write lock, and in default
// local mode every caller is RoleAdmin and therefore holds PermSystemConfig — so
// a concurrent PATCH plus any handler that shells out to br/ubs/cm raced on the
// field. A string is a two-word value (pointer plus length), so a torn read means
// running an external tool with a garbage working directory.
//
// Handlers must use this instead of reading s.projectDir directly. Callers that
// already hold s.mu must NOT use it — RWMutex is not reentrant.
func (s *Server) projectDirSnapshot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectDir
}

// ============================================================================
// WebSocket Handler
// ============================================================================

// checkWSOrigin validates the Origin header for WebSocket connections.
//
// The allowlist applies in every auth mode, including local. WebSocket
// handshakes are exempt from CORS, and the REST CSRF defence is the CORS
// middleware's 403-on-bad-Origin, which never runs for an upgrade — so accepting
// any origin in local mode let any page the operator happened to visit open a
// socket to 127.0.0.1 and subscribe to the attention journal, pane output, and
// mail. Local mode is the default, so that was the default posture.
//
// An absent Origin header is still allowed: only browsers send Origin, and only
// browsers are the CSWSH vector, so requiring it would break agents and curl
// without adding protection.
func (s *Server) checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header - allow for non-browser clients
		return true
	}

	// Parse the origin URL to reject malformed values before matching.
	originURL, err := url.Parse(origin)
	if err != nil {
		log.Printf("ws: invalid origin URL %q: %v", origin, err)
		return false
	}

	// Reject malformed origins (e.g., "//example.com" or "https://")
	if originURL.Scheme == "" || originURL.Host == "" {
		log.Printf("ws: malformed origin %q (missing scheme or host)", origin)
		return false
	}

	// Match with the same predicate the CORS middleware uses. The two must not
	// diverge: this used to compare Host (port-sensitive) while CORS compares
	// Hostname, so the default localhost allowlist — which carries no ports —
	// would have rejected a browser at http://localhost:7337 here while allowing
	// it there.
	s.mu.RLock()
	origins := s.corsAllowedOrigins
	s.mu.RUnlock()
	if len(origins) == 0 {
		// Config documents an empty allowlist as "default localhost only".
		// applyDefaults normally materializes that, but honoring it here too
		// means a Server built without defaults enforces the documented policy
		// rather than rejecting every browser origin.
		origins = defaultLocalOrigins()
	}
	if originAllowed(origin, origins) {
		return true
	}

	log.Printf("ws: rejected origin %q (allowed: %v)", origin, origins)
	return false
}

// handleWebSocket handles WebSocket connections at /api/v1/ws.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Validate origin to prevent WebSocket CSRF attacks
	// Note: CORS middleware does NOT apply to WebSocket upgrades
	if !s.checkWSOrigin(r) {
		reqID := requestIDFromContext(r.Context())
		writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "origin not allowed", nil, reqID)
		return
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		return
	}

	// Generate client ID
	clientID := generateRequestID()

	// Create client
	client := &WSClient{
		id:         clientID,
		conn:       conn,
		hub:        s.wsHub,
		send:       make(chan []byte, 256),
		topics:     make(map[string]struct{}),
		authClaims: extractAuthClaims(r),
	}

	// Wire persistence before the hub loop starts so seq assignment is
	// durable from the very first broadcast (mirrors ensureWSHubRunning for
	// routers exercised without Start()).
	s.ensureWSEventStore()
	s.ensureWSHubRunning()

	// Register client with hub
	if !s.wsHub.RegisterClient(client) {
		if err := conn.Close(); err != nil {
			log.Printf("ws close after failed register id=%s: %v", clientID, err)
		}
		return
	}

	// Start read and write pumps
	go client.writePump()
	go client.readPump()
}

// extractAuthClaims extracts auth claims from the request context.
func extractAuthClaims(r *http.Request) map[string]interface{} {
	// If using OIDC, extract claims from verified token
	claims := make(map[string]interface{})
	if authCtx := r.Context().Value(authContextKey); authCtx != nil {
		if m, ok := authCtx.(map[string]interface{}); ok {
			claims = m
		}
	}
	return claims
}

// authContextKey is the context key for auth claims.
type ctxKeyAuth struct{}

var authContextKey = ctxKeyAuth{}

// readPump reads messages from the WebSocket connection.
func (c *WSClient) readPump() {
	defer func() {
		// Cancel attention subscription before unregistering
		c.cancelAttentionSubscription()
		c.hub.UnregisterClient(c)
		c.closeOnce.Do(func() {
			if err := c.conn.Close(); err != nil {
				log.Printf("ws close error id=%s: %v", c.id, err)
			}
		})
	}()

	c.conn.SetReadLimit(wsMaxMessageSize)
	if err := c.conn.SetReadDeadline(time.Now().Add(wsPongWait)); err != nil {
		log.Printf("ws set read deadline error id=%s: %v", c.id, err)
		return
	}
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws read error id=%s: %v", c.id, err)
			}
			break
		}

		c.handleMessage(message)
	}
}

// writePump writes messages to the WebSocket connection.
func (c *WSClient) writePump() {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		c.closeOnce.Do(func() {
			if err := c.conn.Close(); err != nil {
				log.Printf("ws close error id=%s: %v", c.id, err)
			}
		})
	}()

	for {
		select {
		case message, ok := <-c.send:
			if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
				log.Printf("ws set write deadline error id=%s: %v", c.id, err)
				return
			}
			if !ok {
				// Hub closed the channel
				if err := c.conn.WriteMessage(websocket.CloseMessage, []byte{}); err != nil {
					log.Printf("ws close frame error id=%s: %v", c.id, err)
				}
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				return
			}

			// Drain queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				if _, err := w.Write([]byte{'\n'}); err != nil {
					return
				}
				if _, err := w.Write(<-c.send); err != nil {
					return
				}
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait)); err != nil {
				log.Printf("ws ping deadline error id=%s: %v", c.id, err)
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes an incoming WebSocket message.
func (c *WSClient) handleMessage(data []byte) {
	var msg WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendError("", "parse_error", "invalid JSON message")
		return
	}

	switch msg.Type {
	case WSMsgSubscribe:
		c.handleSubscribe(msg)
	case WSMsgUnsubscribe:
		c.handleUnsubscribe(msg)
	case WSMsgPing:
		c.sendPong(msg.RequestID)
	default:
		c.sendError(msg.RequestID, "unknown_type", fmt.Sprintf("unknown message type: %s", msg.Type))
	}
}

// handleSubscribe processes a subscribe request.
func (c *WSClient) handleSubscribe(msg WSMessage) {
	// Extract topics from data
	topicsRaw, ok := msg.Data["topics"]
	if !ok {
		c.sendError(msg.RequestID, "missing_topics", "subscribe requires topics array")
		return
	}

	topicsSlice, ok := topicsRaw.([]interface{})
	if !ok {
		c.sendError(msg.RequestID, "invalid_topics", "topics must be an array")
		return
	}

	topics := make([]string, 0, len(topicsSlice))
	for _, t := range topicsSlice {
		if str, ok := t.(string); ok {
			// Validate topic format
			if !isValidTopic(str) {
				c.sendError(msg.RequestID, "invalid_topic", fmt.Sprintf("invalid topic: %s", str))
				return
			}
			topics = append(topics, str)
		}
	}

	if len(topics) == 0 {
		c.sendError(msg.RequestID, "empty_topics", "at least one topic required")
		return
	}

	// Check RBAC for topics
	for _, topic := range topics {
		if !c.canSubscribe(topic) {
			c.sendError(msg.RequestID, "unauthorized", fmt.Sprintf("not authorized for topic: %s", topic))
			return
		}
	}

	// Optional replay cursor (see the replay contract in ws_events.go).
	// Presence of "since" requests replay of persisted events with seq > since
	// for the regular (non-attention) topics before live delivery. Attention
	// topics use their own durable cursor field, since_cursor.
	since := int64(-1)
	if sinceRaw, hasSince := msg.Data["since"]; hasSince {
		f, ok := sinceRaw.(float64)
		if !ok || f < 0 {
			c.sendError(msg.RequestID, "invalid_since", "since must be a non-negative integer event seq")
			return
		}
		since = int64(f)
	}

	// Check for attention topic subscriptions with durable semantics
	attentionTopics, regularTopics := partitionAttentionTopics(topics)

	// Subscribe attention topics first. A cursor-expired or unavailable feed
	// cannot satisfy the requested subscription, so do not acknowledge the
	// complete request (or partially subscribe its regular topics) as though it
	// had succeeded.
	var attentionResult map[string]interface{}
	if len(attentionTopics) > 0 {
		attentionResult = c.handleAttentionSubscribe(msg, attentionTopics)
		if errorCode, failed := attentionResult["error_code"].(string); failed {
			message, _ := attentionResult["error"].(string)
			if message == "" {
				message = "attention subscription failed"
			}
			c.sendError(msg.RequestID, errorCode, message)
			return
		}
	}

	// Regular topics are only committed after any durable attention request has
	// been accepted, keeping a single subscribe message all-or-nothing.
	var replayInfo map[string]interface{}
	if len(regularTopics) > 0 {
		if since >= 0 {
			// Replay-then-live is serialized through the hub loop; the replay
			// frames are queued to this client's send channel before the ack
			// below, so the ack marks the end of replay.
			res := c.hub.SubscribeWithReplay(c, regularTopics, since)
			replayInfo = map[string]interface{}{
				"since":    since,
				"replayed": res.replayed,
				"reset":    res.reset,
				"cursor":   res.cursor,
			}
		} else {
			c.Subscribe(regularTopics)
		}
	}

	// Build response
	response := map[string]interface{}{
		"subscribed": topics,
		"total":      len(c.Topics()),
	}
	if attentionResult != nil {
		response["attention"] = attentionResult
	}
	if replayInfo != nil {
		response["replay"] = replayInfo
	}
	c.sendAck(msg.RequestID, response)
}

// handleUnsubscribe processes an unsubscribe request.
func (c *WSClient) handleUnsubscribe(msg WSMessage) {
	topicsRaw, ok := msg.Data["topics"]
	if !ok {
		c.sendError(msg.RequestID, "missing_topics", "unsubscribe requires topics array")
		return
	}

	topicsSlice, ok := topicsRaw.([]interface{})
	if !ok {
		c.sendError(msg.RequestID, "invalid_topics", "topics must be an array")
		return
	}

	topics := make([]string, 0, len(topicsSlice))
	for _, t := range topicsSlice {
		if str, ok := t.(string); ok {
			topics = append(topics, str)
		}
	}

	// Check if any attention topics are being unsubscribed
	attentionTopics, _ := partitionAttentionTopics(topics)
	if len(attentionTopics) > 0 {
		c.removeAttentionTopics(attentionTopics)
	}

	c.Unsubscribe(topics)
	c.sendAck(msg.RequestID, map[string]interface{}{
		"unsubscribed": topics,
		"total":        len(c.Topics()),
	})
}

// isValidTopic checks if a topic string is valid.
//
// Note: This is intentionally permissive for topic *values* and primarily
// validates known topic namespaces, not the full shape of each topic string.
func isValidTopic(topic string) bool {
	if topic == "" {
		return false
	}
	if topic == "*" || topic == "global" || topic == "global:*" || topic == "scanner" || topic == "memory" {
		return true
	}
	// sessions:* or sessions:{name}
	if strings.HasPrefix(topic, "sessions:") {
		return true
	}
	// panes:* or panes:{session}:{idx}
	if strings.HasPrefix(topic, "panes:") {
		return true
	}
	// agent:{type}
	if strings.HasPrefix(topic, "agent:") {
		return true
	}
	// tool systems
	if strings.HasPrefix(topic, "beads:") ||
		strings.HasPrefix(topic, "mail:") ||
		strings.HasPrefix(topic, "reservations:") ||
		strings.HasPrefix(topic, "pipelines:") ||
		strings.HasPrefix(topic, "approvals:") ||
		strings.HasPrefix(topic, "accounts:") ||
		strings.HasPrefix(topic, "attention:") {
		return true
	}
	// attention topic without prefix (for main feed)
	if topic == "attention" {
		return true
	}
	return false
}

// canSubscribe checks if the client is authorized to subscribe to a topic.
func (c *WSClient) canSubscribe(topic string) bool {
	// Deliberate: every authenticated client may subscribe to any topic.
	// ntm serve is a single-operator surface (auth gates the connection, not
	// individual topics); per-topic RBAC is not part of the current design.
	return true
}

// partitionAttentionTopics separates attention topics from regular topics.
// Attention topics ("attention", "attention:*") get special durable semantics.
func partitionAttentionTopics(topics []string) (attention, regular []string) {
	for _, topic := range topics {
		if isAttentionTopic(topic) {
			attention = append(attention, topic)
		} else {
			regular = append(regular, topic)
		}
	}
	return attention, regular
}

// isAttentionTopic checks if a topic is an attention feed topic.
func isAttentionTopic(topic string) bool {
	return topic == "attention" || strings.HasPrefix(topic, "attention:")
}

// handleAttentionSubscribe processes attention topic subscriptions with durable semantics.
// Supports cursor-based replay, filtering, and operator attention state.
//
// Subscription options in msg.Data:
//   - since_cursor: int64 - Replay from cursor (0 = beginning, -1 = now)
//   - session: string - Filter to session
//   - profile: string - Attention profile (operator, minimal, debug, alerts)
//   - category: []string - Filter by event categories
//   - actionability: []string - Filter by actionability levels
//   - exclude_muted: bool - Skip muted items
//   - exclude_snoozed: bool - Skip snoozed items
func (c *WSClient) handleAttentionSubscribe(msg WSMessage, topics []string) map[string]interface{} {
	return c.handleAttentionSubscribeWithFeed(robot.GetAttentionFeed(), msg, topics)
}

// handleAttentionSubscribeWithFeed establishes a durable attention subscription.
// The feed parameter keeps the cursor-validation and replay path testable at the
// point where subscription state is committed.
func (c *WSClient) handleAttentionSubscribeWithFeed(feed attentionStreamFeed, msg WSMessage, topics []string) map[string]interface{} {
	// Extract subscription options
	sinceCursor := int64(0)
	if sc, ok := msg.Data["since_cursor"]; ok {
		switch v := sc.(type) {
		case float64:
			sinceCursor = int64(v)
		case int64:
			sinceCursor = v
		case int:
			sinceCursor = int64(v)
		}
	}

	session := ""
	if s, ok := msg.Data["session"].(string); ok {
		session = s
	}

	profile := ""
	if p, ok := msg.Data["profile"].(string); ok {
		profile = p
	}

	categoryFilter := extractStringSlice(msg.Data["category"])
	actionabilityFilter := extractStringSlice(msg.Data["actionability"])

	excludeMuted := false
	if em, ok := msg.Data["exclude_muted"].(bool); ok {
		excludeMuted = em
	}

	excludeSnoozed := false
	if es, ok := msg.Data["exclude_snoozed"].(bool); ok {
		excludeSnoozed = es
	}

	if feed == nil {
		return map[string]interface{}{
			"error":      "attention_feed_unavailable",
			"error_code": "ATTENTION_UNAVAILABLE",
		}
	}

	// Check for cursor expiration before subscribing
	stats := feed.Stats()
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		return map[string]interface{}{
			"error":          "cursor_expired",
			"error_code":     robot.ErrCodeCursorExpired,
			"oldest_cursor":  earliest,
			"newest_cursor":  stats.NewestCursor,
			"resync_hint":    "Resync via --robot-snapshot, then resubscribe with since_cursor=-1",
			"resync_command": "ntm --robot-snapshot",
		}
	}

	// Replace the previous durable subscription and its hub memberships.
	c.cancelAttentionSubscription()

	// Create new attention subscription
	sub := &WSAttentionSub{
		Active:              true,
		Cursor:              sinceCursor,
		SinceCursor:         sinceCursor,
		Session:             session,
		Profile:             profile,
		CategoryFilter:      categoryFilter,
		ActionabilityFilter: actionabilityFilter,
		ExcludeMuted:        excludeMuted,
		ExcludeSnoozed:      excludeSnoozed,
		StartedAt:           time.Now(),
		topics:              append([]string(nil), topics...),
	}

	// Subscribe to the feed with filtering
	sub.unsubscribe = feed.Subscribe(func(event robot.AttentionEvent) {
		c.deliverAttentionEvent(event, sub)
	})

	// Store subscription
	c.attentionSubMu.Lock()
	c.attentionSub = sub
	c.attentionSubMu.Unlock()

	// Retain the requested topics for replacement and explicit unsubscribe.
	// Live attention events use the durable feed callback above, so Start does
	// not bridge attention events back through wsHub.
	c.Subscribe(topics)

	// Perform replay if cursor is not "start from now"
	var replayCount int
	if sinceCursor >= 0 {
		events, _, err := feed.Replay(sinceCursor, stats.Count)
		if err != nil {
			// The cursor can fall out of the bounded replay window after the
			// preflight above and before Replay acquires the feed's journal lock.
			// Do not acknowledge a subscription which missed its requested
			// history: remove the live callback before
			// reporting the failure to the caller.
			c.cancelAttentionSubscription()
			c.Unsubscribe(topics)

			var cursorErr *robot.CursorExpiredError
			if errors.As(err, &cursorErr) {
				currentStats := feed.Stats()
				return map[string]interface{}{
					"error":          "cursor_expired",
					"error_code":     robot.ErrCodeCursorExpired,
					"oldest_cursor":  cursorErr.EarliestCursor,
					"newest_cursor":  currentStats.NewestCursor,
					"resync_hint":    "Resync via --robot-snapshot, then resubscribe with since_cursor=-1",
					"resync_command": "ntm --robot-snapshot",
				}
			}
			return map[string]interface{}{
				"error":      fmt.Sprintf("replay attention events: %v", err),
				"error_code": "ATTENTION_REPLAY_FAILED",
			}
		} else {
			// Deliver replayed events
			replayCount = c.deliverAttentionReplay(events, topics, sub)
		}
	}

	// Build response
	result := map[string]interface{}{
		"subscribed":    true,
		"topics":        topics,
		"since_cursor":  sinceCursor,
		"oldest_cursor": attentionReplayEarliestCursor(stats),
		"newest_cursor": stats.NewestCursor,
		"replay_count":  replayCount,
	}
	if session != "" {
		result["session_filter"] = session
	}
	if profile != "" {
		result["profile"] = profile
	}
	return result
}

// cancelAttentionSubscription stops any existing attention subscription.
func (c *WSClient) cancelAttentionSubscription() {
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	c.attentionSub = nil
	c.attentionSubMu.Unlock()

	if sub != nil && sub.unsubscribe != nil {
		sub.unsubscribe()
	}
	if sub != nil && len(sub.topics) > 0 {
		c.Unsubscribe(sub.topics)
	}
}

func (c *WSClient) removeAttentionTopics(topics []string) {
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	if sub == nil {
		c.attentionSubMu.Unlock()
		return
	}
	remaining := make([]string, 0, len(sub.topics))
	for _, subscribed := range sub.topics {
		remove := false
		for _, requested := range topics {
			if subscribed == requested {
				remove = true
				break
			}
		}
		if !remove {
			remaining = append(remaining, subscribed)
		}
	}
	sub.topics = remaining
	if len(remaining) != 0 {
		c.attentionSubMu.Unlock()
		return
	}
	c.attentionSub = nil
	c.attentionSubMu.Unlock()
	if sub.unsubscribe != nil {
		sub.unsubscribe()
	}
}

// deliverAttentionEvent delivers a single attention event to the client.
// Applies subscription filters and tracks cursor position.
func (c *WSClient) deliverAttentionEvent(event robot.AttentionEvent, expected *WSAttentionSub) {
	// Snapshot the filter criteria AND the cursor under the lock. Copying only
	// the pointer and then reading sub.Cursor/sub.Active raced the locked
	// cursor writes below and in deliverAttentionReplay: AttentionFeed invokes
	// subscriber handlers synchronously on the publishing goroutine, so any
	// event published during the subscribe→replay window runs both at once,
	// which either re-delivered an event or rolled the cursor backwards.
	c.attentionSubMu.Lock()
	sub := c.attentionSub
	if sub == nil || sub != expected || !sub.Active {
		c.attentionSubMu.Unlock()
		return
	}
	criteria := *sub
	c.attentionSubMu.Unlock()

	// Skip events already sent during replay
	if event.Cursor <= criteria.Cursor {
		return
	}

	// Skip transport heartbeat events (handled separately)
	if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
		return
	}

	// Apply session filter
	if criteria.Session != "" && event.Session != criteria.Session {
		return
	}

	// Apply category/actionability filters (session already checked above)
	if !matchesAttentionFilters(event, criteria.CategoryFilter, "", criteria.ActionabilityFilter) {
		return
	}

	// Determine target topic
	topic := "attention"
	if event.Session != "" {
		sessionTopic := "attention:" + event.Session
		for _, t := range criteria.topics {
			if t == sessionTopic {
				topic = sessionTopic
				break
			}
		}
	}

	// Build event envelope
	wsEvent := &WSEvent{
		Type:      WSMsgEvent,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Seq:       event.Cursor, // Use attention cursor as seq for correlation
		Topic:     topic,
		EventType: string(event.Type),
		Data:      event,
	}

	data, err := json.Marshal(wsEvent)
	if err != nil {
		return
	}

	// Send with backpressure tracking
	sent := c.trySend(data, func() {
		c.attentionSubMu.Lock()
		if c.attentionSub != nil {
			c.attentionSub.DroppedCount++
		}
		c.attentionSubMu.Unlock()
		log.Printf("ws attention: dropped event cursor=%d client=%s", event.Cursor, c.id)
	})

	if sent {
		c.attentionSubMu.Lock()
		if c.attentionSub != nil {
			// Monotonic, like the replay path: a live event and a replayed one
			// can commit in either order, and the cursor is what "resume from
			// here" means to the client. Never move it backwards.
			if event.Cursor > c.attentionSub.Cursor {
				c.attentionSub.Cursor = event.Cursor
			}
			c.attentionSub.EventCount++
		}
		c.attentionSubMu.Unlock()
	}
}

// deliverAttentionReplay delivers replayed attention events to the client.
// Returns the count of events delivered.
func (c *WSClient) deliverAttentionReplay(events []robot.AttentionEvent, topics []string, sub *WSAttentionSub) int {
	if sub == nil {
		return 0
	}

	delivered := 0
	for _, event := range events {
		// Skip events before cursor
		if event.Cursor <= sub.SinceCursor {
			continue
		}

		// Skip transport heartbeats
		if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
			continue
		}

		// Apply filters
		if sub.Session != "" && event.Session != sub.Session {
			continue
		}
		// Session already checked above
		if !matchesAttentionFilters(event, sub.CategoryFilter, "", sub.ActionabilityFilter) {
			continue
		}

		// Determine topic
		topic := "attention"
		if event.Session != "" {
			sessionTopic := "attention:" + event.Session
			for _, t := range topics {
				if t == sessionTopic {
					topic = sessionTopic
					break
				}
			}
		}

		// Build replay event
		wsEvent := &WSEvent{
			Type:      WSMsgEvent,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Seq:       event.Cursor,
			Topic:     topic,
			EventType: string(event.Type),
			Data:      event,
		}

		data, err := json.Marshal(wsEvent)
		if err != nil {
			continue
		}

		if c.trySend(data, nil) {
			delivered++
			c.attentionSubMu.Lock()
			if c.attentionSub != nil && event.Cursor > c.attentionSub.Cursor {
				c.attentionSub.Cursor = event.Cursor
			}
			c.attentionSubMu.Unlock()
		}
	}

	return delivered
}

// extractStringSlice extracts a string slice from an interface{}.
func extractStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []string:
		return arr
	case []interface{}:
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func (c *WSClient) trySend(data []byte, onDrop func()) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case c.send <- data:
		return true
	default:
		if onDrop != nil {
			onDrop()
		}
		return false
	}
}

// sendError sends a WebSocket error frame.
func (c *WSClient) sendError(requestID, code, message string) {
	errMsg := WSError{
		Type:      WSMsgError,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}
	data, err := json.Marshal(errMsg)
	if err != nil {
		return
	}
	c.trySend(data, func() {
		log.Printf("ws client buffer full, dropping error id=%s", c.id)
	})
}

// sendAck sends a WebSocket acknowledgment frame.
func (c *WSClient) sendAck(requestID string, data map[string]interface{}) {
	ack := WSMessage{
		Type:      WSMsgAck,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
		Data:      data,
	}
	msg, err := json.Marshal(ack)
	if err != nil {
		return
	}
	c.trySend(msg, func() {
		log.Printf("ws client buffer full, dropping ack id=%s", c.id)
	})
}

// sendPong sends a WebSocket pong response.
func (c *WSClient) sendPong(requestID string) {
	pong := WSMessage{
		Type:      WSMsgPong,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		RequestID: requestID,
	}
	data, err := json.Marshal(pong)
	if err != nil {
		return
	}
	c.trySend(data, func() {
		// Buffer full, skip
	})
}

// =============================================================================
// Attention Feed Handlers
// =============================================================================

type attentionStreamFeed interface {
	Stats() robot.JournalStats
	Replay(sinceCursor int64, limit int) ([]robot.AttentionEvent, int64, error)
	Subscribe(robot.AttentionHandler) func()
}

type preparedAttentionStream struct {
	stats          robot.JournalStats
	replayBoundary int64
	replayEvents   []robot.AttentionEvent
	eventCh        chan robot.AttentionEvent
	unsubscribe    func()
	droppedCount   atomic.Uint64
}

func (p *preparedAttentionStream) takeDroppedCount() uint64 {
	return p.droppedCount.Swap(0)
}

func prepareAttentionStream(feed attentionStreamFeed, sinceCursor int64, bufferSize int) (*preparedAttentionStream, error) {
	if feed == nil {
		return nil, errors.New("attention feed unavailable")
	}
	if bufferSize <= 0 {
		bufferSize = 100
	}

	eventCh := make(chan robot.AttentionEvent, bufferSize)
	prepared := &preparedAttentionStream{
		eventCh: eventCh,
	}
	unsubscribe := feed.Subscribe(func(event robot.AttentionEvent) {
		select {
		case eventCh <- event:
		default:
			prepared.droppedCount.Add(1)
		}
	})

	stats := feed.Stats()
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		unsubscribe()
		return nil, &robot.CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  earliest,
			RetentionPeriod: stats.RetentionPeriod,
		}
	}

	prepared.stats = stats
	prepared.replayBoundary = stats.NewestCursor
	prepared.replayEvents = []robot.AttentionEvent{}
	prepared.unsubscribe = unsubscribe
	if sinceCursor < 0 {
		return prepared, nil
	}

	replayLimit := stats.Count
	if replayLimit <= 0 {
		replayLimit = 1
	}
	events, _, err := feed.Replay(sinceCursor, replayLimit)
	if err != nil {
		unsubscribe()
		return nil, err
	}

	prepared.replayEvents = filterAttentionReplayBoundary(events, prepared.replayBoundary)
	return prepared, nil
}

func attentionCursorExpiredPayload(cursorErr *robot.CursorExpiredError, newestCursor int64) map[string]interface{} {
	details := cursorErr.ToDetails()
	return map[string]interface{}{
		"error_code":     robot.ErrCodeCursorExpired,
		"message":        "cursor has expired, resync required",
		"oldest_cursor":  details.EarliestCursor,
		"newest_cursor":  newestCursor,
		"resync_command": details.ResyncCommand,
	}
}

func filterAttentionReplayBoundary(events []robot.AttentionEvent, maxCursor int64) []robot.AttentionEvent {
	if len(events) == 0 || maxCursor <= 0 {
		return []robot.AttentionEvent{}
	}

	filtered := make([]robot.AttentionEvent, 0, len(events))
	for _, event := range events {
		if event.Cursor > maxCursor {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func writeAttentionReplay(
	w io.Writer,
	flusher http.Flusher,
	events []robot.AttentionEvent,
	categoryFilter []string,
	sessionFilter string,
	actionabilityFilter []string,
	replayCursor int64,
) (int64, int, error) {
	delivered := 0
	for _, event := range events {
		if event.Cursor > replayCursor {
			replayCursor = event.Cursor
		}
		if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
			// Match the live stream path: transport heartbeat events are not emitted as attention items.
			continue
		}
		if !matchesAttentionFilters(event, categoryFilter, sessionFilter, actionabilityFilter) {
			continue
		}
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "event: attention\ndata: %s\n\n", data); err != nil {
			return replayCursor, delivered, err
		}
		delivered++
	}
	if flusher != nil {
		flusher.Flush()
	}
	return replayCursor, delivered, nil
}

// handleAttentionStreamV1 handles SSE streaming at /api/v1/attention/stream.
// Query params:
//   - since_cursor: replay from this cursor (0 = start from beginning, -1 = from now)
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - actionability: filter by actionability level (comma-separated)
//   - heartbeat: heartbeat interval in seconds (default adaptive, 0 to disable)
func (s *Server) handleAttentionStreamV1(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	sinceCursor := int64(0)
	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		sinceCursor = parsed
	}

	categoryFilter := parseCSVParam(r.URL.Query().Get("category"))
	sessionFilter := r.URL.Query().Get("session")
	actionabilityFilter := parseCSVParam(r.URL.Query().Get("actionability"))

	heartbeatInterval := attentionHeartbeatIdleInterval
	heartbeatOverride := false
	if hb := r.URL.Query().Get("heartbeat"); hb != "" {
		parsed, err := strconv.Atoi(hb)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid heartbeat: "+err.Error())
			return
		}
		heartbeatInterval = time.Duration(parsed) * time.Second
		heartbeatOverride = true
	}

	feed := robot.GetAttentionFeed()
	prepared, err := prepareAttentionStream(feed, sinceCursor, 100)
	if err != nil {
		var cursorErr *robot.CursorExpiredError
		if !errors.As(err, &cursorErr) {
			writeError(w, http.StatusInternalServerError, "attention stream setup failed: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		cursorExpiredEvent := attentionCursorExpiredPayload(cursorErr, feed.Stats().NewestCursor)
		data, marshalErr := json.Marshal(cursorExpiredEvent)
		if marshalErr != nil {
			log.Printf("SSE: failed to marshal cursor expired event: %v", marshalErr)
			return
		}
		if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
		return
	}
	defer prepared.unsubscribe()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send connection event
	connEvent := map[string]interface{}{
		"status":        "connected",
		"time":          time.Now().UTC().Format(time.RFC3339),
		"since_cursor":  sinceCursor,
		"oldest_cursor": attentionReplayEarliestCursor(prepared.stats),
		"newest_cursor": prepared.stats.NewestCursor,
	}
	connData, marshalErr := json.Marshal(connEvent)
	if marshalErr != nil {
		log.Printf("SSE: failed to marshal connection event: %v", marshalErr)
		return
	}
	if _, err := fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connData); err != nil {
		return
	}
	flusher.Flush()

	// Stream bookkeeping spans replayed and live-delivered events.
	streamStart := time.Now()
	streamID := fmt.Sprintf("watch_%s", streamStart.UTC().Format("20060102T150405Z"))

	// Replay events from cursor if requested
	replayCursor := prepared.replayBoundary
	replayCursor, eventsSinceStart, err := writeAttentionReplay(
		w,
		flusher,
		prepared.replayEvents,
		categoryFilter,
		sessionFilter,
		actionabilityFilter,
		replayCursor,
	)
	if err != nil {
		return
	}

	// Stream events
	ctx := r.Context()
	recoveryMode := sinceCursor > 0 || len(prepared.replayEvents) > 0
	deliveredSinceHeartbeat := 0
	filteredSinceHeartbeat := 0
	var heartbeatTimer *time.Timer
	var heartbeatCh <-chan time.Time
	nextHeartbeatInterval := attentionHeartbeatInterval(
		time.Since(streamStart),
		deliveredSinceHeartbeat,
		recoveryMode,
		attentionHeartbeatSourceSummary{},
		heartbeatInterval,
		heartbeatOverride,
	)
	if nextHeartbeatInterval > 0 {
		heartbeatTimer = time.NewTimer(nextHeartbeatInterval)
		heartbeatCh = heartbeatTimer.C
		defer heartbeatTimer.Stop()
	}
	resetHeartbeatTimer := func(nextInterval time.Duration) {
		if heartbeatTimer == nil {
			return
		}
		if !heartbeatTimer.Stop() {
			select {
			case <-heartbeatTimer.C:
			default:
			}
		}
		heartbeatTimer.Reset(nextInterval)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-prepared.eventCh:
			if event.Cursor <= replayCursor {
				// Already sent during replay
				continue
			}
			if event.Type == robot.EventType(robot.DefaultTransportLiveness.HeartbeatType) {
				// SSE uses its own transport heartbeat framing instead of replay/feed events.
				continue
			}
			if !matchesAttentionFilters(event, categoryFilter, sessionFilter, actionabilityFilter) {
				filteredSinceHeartbeat++
				continue
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: attention\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			replayCursor = event.Cursor
			eventsSinceStart++
			deliveredSinceHeartbeat++
			if heartbeatTimer != nil {
				sourceSummary := s.attentionHeartbeatSourceSummary()
				nextHeartbeatInterval = attentionHeartbeatInterval(
					time.Since(streamStart),
					deliveredSinceHeartbeat,
					recoveryMode,
					sourceSummary,
					heartbeatInterval,
					heartbeatOverride,
				)
				resetHeartbeatTimer(nextHeartbeatInterval)
			}
		case <-heartbeatCh:
			currentStats := feed.Stats()
			sourceSummary := s.attentionHeartbeatSourceSummary()
			nextHeartbeatInterval = attentionHeartbeatInterval(
				time.Since(streamStart),
				deliveredSinceHeartbeat,
				recoveryMode,
				sourceSummary,
				heartbeatInterval,
				heartbeatOverride,
			)
			heartbeat := map[string]interface{}{
				"type":                        "heartbeat",
				"stream_id":                   streamID,
				"time":                        time.Now().UTC().Format(time.RFC3339),
				"oldest_cursor":               attentionReplayEarliestCursor(currentStats),
				"newest_cursor":               currentStats.NewestCursor,
				"cursor_position":             replayCursor,
				"event_count":                 currentStats.Count,
				"subscriber_count":            feed.SubscriberCount(),
				"uptime_ms":                   time.Since(streamStart).Milliseconds(),
				"events_since_start":          eventsSinceStart,
				"next_heartbeat_ms":           nextHeartbeatInterval.Milliseconds(),
				"events_since_last_heartbeat": deliveredSinceHeartbeat,
				"filtered_since_last":         filteredSinceHeartbeat,
				"dropped_since_last":          prepared.takeDroppedCount(),
				"sources_healthy":             sourceSummary.healthy,
				"sources_degraded":            sourceSummary.degraded,
				"sources_unavailable":         sourceSummary.unavailable,
				"degraded_reasons":            sourceSummary.degradedReasons,
				"subscription_active":         true,
			}
			if currentStats.LastEventTime != nil && !currentStats.LastEventTime.IsZero() {
				lastEventTime := currentStats.LastEventTime.UTC()
				heartbeat["last_event_time"] = lastEventTime.Format(time.RFC3339Nano)
				heartbeat["idle_ms"] = time.Since(lastEventTime).Milliseconds()
			}
			data, marshalErr := json.Marshal(heartbeat)
			if marshalErr != nil {
				log.Printf("SSE: failed to marshal heartbeat: %v", marshalErr)
				return
			}
			if _, err := fmt.Fprintf(w, "event: heartbeat\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			deliveredSinceHeartbeat = 0
			filteredSinceHeartbeat = 0
			recoveryMode = false
			if heartbeatTimer != nil && nextHeartbeatInterval > 0 {
				resetHeartbeatTimer(nextHeartbeatInterval)
			}
		}
	}
}

func attentionHeartbeatInterval(
	streamAge time.Duration,
	deliveredSinceHeartbeat int,
	recoveryMode bool,
	sourceSummary attentionHeartbeatSourceSummary,
	baseInterval time.Duration,
	override bool,
) time.Duration {
	if baseInterval <= 0 {
		return 0
	}
	if override {
		return baseInterval
	}
	if recoveryMode && deliveredSinceHeartbeat == 0 && streamAge < attentionHeartbeatIdleInterval {
		return attentionHeartbeatRecoveryInterval
	}
	if sourceSummary.degraded > 0 {
		return attentionHeartbeatIdleInterval
	}
	if deliveredSinceHeartbeat > 0 {
		return attentionHeartbeatHighActivityInterval
	}
	return attentionHeartbeatIdleInterval
}

func (s *Server) attentionHeartbeatSourceSummary() attentionHeartbeatSourceSummary {
	summary := attentionHeartbeatSourceSummary{
		degradedReasons: []string{},
	}
	if s == nil || s.stateStore == nil {
		return summary
	}
	rows, err := s.stateStore.GetAllSourceHealth()
	if err != nil {
		return summary
	}
	return summarizeAttentionHeartbeatSources(robot.SourceHealthSectionFromRows(rows))
}

func summarizeAttentionHeartbeatSources(section *adapters.SourceHealthSection) attentionHeartbeatSourceSummary {
	summary := attentionHeartbeatSourceSummary{
		degradedReasons: []string{},
	}
	if section == nil {
		return summary
	}
	reasonSet := make(map[string]struct{}, len(section.Degraded))
	for _, source := range section.Sources {
		if source.Fresh {
			summary.healthy++
		}
		if source.Degraded {
			summary.degraded++
		}
		if !source.Available {
			summary.unavailable++
		}
		if !source.Degraded {
			continue
		}
		reason := strings.TrimSpace(string(source.ReasonCode))
		if reason == "" {
			reason = strings.TrimSpace(source.DegradedReason)
		}
		if reason == "" {
			reason = strings.TrimSpace(source.LastError)
		}
		if reason == "" {
			continue
		}
		if _, exists := reasonSet[reason]; exists {
			continue
		}
		reasonSet[reason] = struct{}{}
		summary.degradedReasons = append(summary.degradedReasons, reason)
	}
	sort.Strings(summary.degradedReasons)
	return summary
}

// handleAttentionEventsV1 handles HTTP replay at /api/v1/attention/events.
// Query params:
//   - since_cursor: replay from this cursor (required)
//   - incident_id: bounded replay around a durable incident
//   - as_of: reconstruct bounded attention context at or before an RFC3339 timestamp
//   - window_before_ms/window_after_ms: incident replay context bounds
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - actionability: filter by actionability level (comma-separated)
//   - limit: max events to return (default 100)
func (s *Server) handleAttentionEventsV1(w http.ResponseWriter, r *http.Request) {
	opts := robot.EventsOptions{
		Limit:               100,
		Session:             r.URL.Query().Get("session"),
		CategoryFilter:      parseCSVParam(r.URL.Query().Get("category")),
		ActionabilityFilter: parseCSVParam(r.URL.Query().Get("actionability")),
		IncidentID:          strings.TrimSpace(r.URL.Query().Get("incident_id")),
	}

	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		opts.SinceCursor = parsed
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.Atoi(l)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit: "+err.Error())
			return
		}
		if parsed > 0 && parsed < 10000 {
			limit = parsed
		}
	}
	opts.Limit = limit

	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		asOf, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid as_of: "+err.Error())
			return
		}
		opts.AsOf = asOf
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("window_before_ms")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid window_before_ms")
			return
		}
		opts.WindowBefore = time.Duration(parsed) * time.Millisecond
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("window_after_ms")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid window_after_ms")
			return
		}
		opts.WindowAfter = time.Duration(parsed) * time.Millisecond
	}

	output, status := robot.BuildEventsOutput(opts)
	if !output.Success && output.ErrorCode == robot.ErrCodeCursorExpired {
		oldestCursor := int64(0)
		retention := time.Duration(0)
		if output.ReplayWindow != nil {
			oldestCursor = output.ReplayWindow.OldestCursor
			if parsed, err := time.ParseDuration(output.ReplayWindow.RetentionPeriod); err == nil {
				retention = parsed
			}
		}
		writeJSON(w, http.StatusConflict, attentionCursorExpiredPayload(&robot.CursorExpiredError{
			RequestedCursor: opts.SinceCursor,
			EarliestCursor:  oldestCursor,
			RetentionPeriod: retention,
		}, output.NextCursor))
		return
	}
	payload := map[string]interface{}{
		"success":       output.Success,
		"timestamp":     output.Timestamp,
		"version":       output.Version,
		"output_format": output.OutputFormat,
		"events":        output.Events,
		"since_cursor":  opts.SinceCursor,
		"next_cursor":   output.NextCursor,
		"newest_cursor": output.NextCursor,
		"oldest_cursor": int64(0),
		"event_count":   len(output.Events),
		"truncated":     output.HasMore,
	}
	if output.ReplayWindow != nil {
		payload["newest_cursor"] = output.ReplayWindow.LatestCursor
		payload["oldest_cursor"] = output.ReplayWindow.OldestCursor
	}
	if output.ReplayTarget != nil {
		payload["replay_target"] = output.ReplayTarget
	}
	if output.Reconstruction != nil {
		payload["reconstruction"] = output.Reconstruction
	}
	if output.Boundedness != nil {
		payload["boundedness"] = output.Boundedness
	}
	if output.Incident != nil {
		payload["incident"] = output.Incident
	}
	if output.Error != "" {
		payload["error"] = output.Error
	}
	if output.ErrorCode != "" {
		payload["error_code"] = output.ErrorCode
	}
	if output.Hint != "" {
		payload["hint"] = output.Hint
	}
	writeJSON(w, status, payload)
}

// handleAttentionDigestV1 handles digest at /api/v1/attention/digest.
// Query params:
//   - since_cursor: aggregate events from this cursor
//   - category: filter by event category (comma-separated)
//   - session: filter by session name
//   - action_required_limit: max action_required items (default 5)
//   - interesting_limit: max interesting items (default 10)
//   - background_limit: max background items (default 5)
//   - trace: include decision trace (default false)
func (s *Server) handleAttentionDigestV1(w http.ResponseWriter, r *http.Request) {
	sinceCursor := int64(0)
	if sc := r.URL.Query().Get("since_cursor"); sc != "" {
		parsed, err := strconv.ParseInt(sc, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since_cursor: "+err.Error())
			return
		}
		sinceCursor = parsed
	}

	opts := robot.DefaultAttentionDigestOptions()

	if arl := r.URL.Query().Get("action_required_limit"); arl != "" {
		if parsed, err := strconv.Atoi(arl); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.ActionRequiredLimit = parsed
		}
	}
	if il := r.URL.Query().Get("interesting_limit"); il != "" {
		if parsed, err := strconv.Atoi(il); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.InterestingLimit = parsed
		}
	}
	if bl := r.URL.Query().Get("background_limit"); bl != "" {
		if parsed, err := strconv.Atoi(bl); err == nil && parsed >= 0 && parsed <= 1000 {
			opts.BackgroundLimit = parsed
		}
	}
	if trace := r.URL.Query().Get("trace"); trace == "true" || trace == "1" {
		opts.IncludeTrace = true
	}

	categoryFilter := parseCSVParam(r.URL.Query().Get("category"))
	if len(categoryFilter) > 0 {
		categories := make([]robot.EventCategory, 0, len(categoryFilter))
		for _, cat := range categoryFilter {
			categories = append(categories, robot.EventCategory(cat))
		}
		opts.Categories = categories
	}

	sessionFilter := r.URL.Query().Get("session")
	if sessionFilter != "" {
		opts.Session = sessionFilter
	}

	feed := robot.GetAttentionFeed()
	stats := feed.Stats()

	// Check for cursor expiration using the same boundary as the underlying journal.
	if expired, earliest := attentionCursorExpired(sinceCursor, stats); expired {
		writeJSON(w, http.StatusConflict, attentionCursorExpiredPayload(&robot.CursorExpiredError{
			RequestedCursor: sinceCursor,
			EarliestCursor:  earliest,
			RetentionPeriod: stats.RetentionPeriod,
		}, stats.NewestCursor))
		return
	}

	digest, err := feed.Digest(sinceCursor, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "digest failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, digest)
}

type AttentionItemStateRequest struct {
	Action            string `json:"action"`
	Reason            string `json:"reason,omitempty"`
	Until             string `json:"until,omitempty"`
	OverridePriority  string `json:"override_priority,omitempty"`
	OverrideExpiresAt string `json:"override_expires_at,omitempty"`
	ResurfacingPolicy string `json:"resurfacing_policy,omitempty"`
}

func attentionMaterialDetailsForServe(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	filtered := make(map[string]any, len(details))
	for key, value := range details {
		switch {
		case strings.HasPrefix(key, "attention_"):
			continue
		case strings.HasPrefix(key, "digest_"):
			continue
		case key == "resurfaced",
			key == "resurface_reason",
			key == "cooldown_window_ms",
			key == "cooldown_suppressed_count",
			key == "cooldown_suppressed_since",
			key == "cooldown_last_suppressed":
			continue
		default:
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func attentionEventFingerprintForServe(event state.StoredAttentionEvent) string {
	pane := 0
	if event.Pane != "" {
		if parsed, err := strconv.Atoi(event.Pane); err == nil {
			pane = parsed
		}
	}
	payload := map[string]any{
		"session":       strings.TrimSpace(event.SessionName),
		"pane":          pane,
		"category":      strings.TrimSpace(event.Category),
		"type":          strings.TrimSpace(event.EventType),
		"source":        strings.TrimSpace(event.Source),
		"actionability": strings.TrimSpace(string(event.Actionability)),
		"severity":      strings.TrimSpace(string(event.Severity)),
		"reason_code":   strings.TrimSpace(event.ReasonCode),
		"summary":       strings.TrimSpace(event.Summary),
	}
	if event.Details != "" {
		var details map[string]any
		if err := json.Unmarshal([]byte(event.Details), &details); err == nil {
			if filtered := attentionMaterialDetailsForServe(details); len(filtered) > 0 {
				payload["details"] = filtered
			}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("fp-%x", sum[:8])
}

func (s *Server) attentionStoredEventByCursor(cursor int64) (*state.StoredAttentionEvent, error) {
	if s.stateStore == nil {
		return nil, nil
	}
	events, err := s.stateStore.GetAttentionEventsSince(cursor-1, 1)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[0].Cursor != cursor {
		return nil, nil
	}
	event := events[0]
	return &event, nil
}

func attentionActorFromRequest(r *http.Request) (actorType, actorID, actorOrigin, actorLabel string) {
	rc := RoleFromContext(r.Context())
	if rc == nil {
		return "api", "anonymous", "api", "anonymous"
	}
	actorType = "user"
	actorOrigin = string(rc.Role)
	actorID = strings.TrimSpace(rc.UserID)
	if actorID == "" {
		actorID = actorOrigin
	}
	if actorID == "" {
		actorID = "anonymous"
	}
	return actorType, actorID, actorOrigin, actorID
}

func attentionCanonicalState(itemKey, dedupKey, fingerprint string, prior *state.AttentionItemState) state.AttentionItemState {
	if prior != nil {
		next := *prior
		if itemKey != "" {
			next.ItemKey = itemKey
		}
		if dedupKey != "" {
			next.DedupKey = dedupKey
		}
		if fingerprint != "" {
			next.Fingerprint = fingerprint
		}
		if next.State == "" {
			next.State = state.AttentionStateNew
		}
		if next.ResurfacingPolicy == "" {
			next.ResurfacingPolicy = "on_change"
		}
		return next
	}
	return state.AttentionItemState{
		ItemKey:           itemKey,
		DedupKey:          dedupKey,
		State:             state.AttentionStateNew,
		Fingerprint:       fingerprint,
		ResurfacingPolicy: "on_change",
	}
}

func attentionStateJSON(item state.AttentionItemState) (string, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func attentionStateLabel(item state.AttentionItemState) string {
	if strings.TrimSpace(string(item.State)) == "" {
		return string(state.AttentionStateNew)
	}
	return string(item.State)
}

func applyAttentionItemAction(item *state.AttentionItemState, action string, req AttentionItemStateRequest, actorLabel string, now time.Time) error {
	if item == nil {
		return fmt.Errorf("attention item state is nil")
	}

	action = strings.ToLower(strings.TrimSpace(action))
	if policy := strings.TrimSpace(req.ResurfacingPolicy); policy != "" {
		item.ResurfacingPolicy = policy
	}

	switch action {
	case "ack", "acknowledge":
		item.State = state.AttentionStateAcknowledged
		item.AcknowledgedAt = &now
		item.AcknowledgedBy = actorLabel
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "snooze":
		until := strings.TrimSpace(req.Until)
		if until == "" {
			return fmt.Errorf("until is required for snooze")
		}
		parsed, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return fmt.Errorf("invalid until: %w", err)
		}
		parsed = parsed.UTC()
		if !parsed.After(now) {
			return fmt.Errorf("until must be in the future")
		}
		item.State = state.AttentionStateSnoozed
		item.SnoozedUntil = &parsed
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "dismiss":
		item.State = state.AttentionStateDismissed
		item.DismissedAt = &now
		item.DismissedBy = actorLabel
		item.SnoozedUntil = nil
	case "restore":
		item.State = state.AttentionStateNew
		item.AcknowledgedAt = nil
		item.AcknowledgedBy = ""
		item.SnoozedUntil = nil
		item.DismissedAt = nil
		item.DismissedBy = ""
	case "pin":
		item.Pinned = true
		item.PinnedAt = &now
		item.PinnedBy = actorLabel
	case "unpin":
		item.Pinned = false
		item.PinnedAt = nil
		item.PinnedBy = ""
	case "mute":
		item.Muted = true
		item.MutedAt = &now
		item.MutedBy = actorLabel
	case "unmute":
		item.Muted = false
		item.MutedAt = nil
		item.MutedBy = ""
	case "escalate":
		override := strings.TrimSpace(req.OverridePriority)
		if override == "" {
			override = "critical"
		}
		item.OverridePriority = override
		item.OverrideReason = strings.TrimSpace(req.Reason)
		if expires := strings.TrimSpace(req.OverrideExpiresAt); expires != "" {
			parsed, err := time.Parse(time.RFC3339, expires)
			if err != nil {
				return fmt.Errorf("invalid override_expires_at: %w", err)
			}
			parsed = parsed.UTC()
			item.OverrideExpiresAt = &parsed
		} else {
			item.OverrideExpiresAt = nil
		}
	default:
		return fmt.Errorf("unsupported action %q", req.Action)
	}

	return nil
}

// handleAttentionItemStateV1 handles POST /api/v1/attention/items/{cursor}/state.
func (s *Server) handleAttentionItemStateV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, "state store not available", nil, reqID)
		return
	}

	cursor, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "cursor")), 10, 64)
	if err != nil || cursor <= 0 {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "valid attention cursor required", nil, reqID)
		return
	}

	var req AttentionItemStateRequest
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body: "+err.Error(), nil, reqID)
		return
	}
	if strings.TrimSpace(req.Action) == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "action required", nil, reqID)
		return
	}

	storedEvent, err := s.attentionStoredEventByCursor(cursor)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load attention event: "+err.Error(), nil, reqID)
		return
	}
	if storedEvent == nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "attention item not found", nil, reqID)
		return
	}

	itemKey, err := s.stateStore.ResolveAttentionItemKey(cursor)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to resolve attention item key: "+err.Error(), nil, reqID)
		return
	}
	if itemKey == "" {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound, "attention item key not found", nil, reqID)
		return
	}

	priorState, err := s.stateStore.GetAttentionItemState(itemKey)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load attention item state: "+err.Error(), nil, reqID)
		return
	}

	actorType, actorID, actorOrigin, actorLabel := attentionActorFromRequest(r)
	fingerprint := attentionEventFingerprintForServe(*storedEvent)
	now := time.Now().UTC()
	nextState := attentionCanonicalState(itemKey, storedEvent.DedupKey, fingerprint, priorState)
	if err := applyAttentionItemAction(&nextState, req.Action, req, actorLabel, now); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error(), nil, reqID)
		return
	}
	nextState.Fingerprint = fingerprint

	previous := attentionCanonicalState(itemKey, storedEvent.DedupKey, fingerprint, priorState)
	previousJSON, err := attentionStateJSON(previous)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize previous state", nil, reqID)
		return
	}
	newJSON, err := attentionStateJSON(nextState)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize new state", nil, reqID)
		return
	}

	var (
		auditEventID    int64
		auditDecisionID int64
	)

	err = s.stateStore.Transaction(func(tx *state.Tx) error {
		if err := tx.UpsertAttentionItemState(&nextState); err != nil {
			return err
		}

		evidenceJSON, marshalErr := json.Marshal(map[string]any{
			"cursor":      cursor,
			"dedup_key":   storedEvent.DedupKey,
			"session":     storedEvent.SessionName,
			"pane":        storedEvent.Pane,
			"summary":     storedEvent.Summary,
			"fingerprint": fingerprint,
		})
		if marshalErr != nil {
			return fmt.Errorf("marshal attention evidence: %w", marshalErr)
		}

		eventID, err := tx.RecordAuditEvent(&state.AuditEvent{
			Ts:              now,
			ActorType:       actorType,
			ActorID:         actorID,
			ActorOrigin:     actorOrigin,
			RequestID:       reqID,
			CorrelationID:   r.Header.Get("Idempotency-Key"),
			Category:        "attention",
			EventType:       strings.ToLower(strings.TrimSpace(req.Action)),
			Severity:        storedEvent.Severity,
			EntityType:      "attention_item",
			EntityID:        itemKey,
			PreviousState:   previousJSON,
			NewState:        newJSON,
			ChangeSummary:   fmt.Sprintf("%s attention item %s", strings.ToLower(strings.TrimSpace(req.Action)), itemKey),
			Reason:          strings.TrimSpace(req.Reason),
			Evidence:        string(evidenceJSON),
			DisclosureState: "visible",
			RetentionClass:  state.RetentionClassStandard,
		})
		if err != nil {
			return err
		}
		auditEventID = eventID

		decisionID, err := tx.RecordAuditDecision(&state.AuditDecision{
			DecisionType: strings.ToLower(strings.TrimSpace(req.Action)),
			DecisionAt:   now,
			ActorType:    actorType,
			ActorID:      actorID,
			EntityType:   "attention_item",
			EntityID:     itemKey,
			Reason:       strings.TrimSpace(req.Reason),
			ExpiresAt:    nextState.OverrideExpiresAt,
			AuditEventID: &auditEventID,
		})
		if err != nil {
			return err
		}
		auditDecisionID = decisionID
		return nil
	})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to apply attention action: "+err.Error(), nil, reqID)
		return
	}

	SetAuditResource(r, "attention", itemKey)
	SetAuditSession(r, storedEvent.SessionName, storedEvent.Pane, "")
	SetAuditAction(r, AuditActionUpdate)
	SetAuditDetails(r, fmt.Sprintf("%s attention item %s", strings.ToLower(strings.TrimSpace(req.Action)), itemKey))

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target": map[string]interface{}{
			"cursor":    cursor,
			"item_key":  itemKey,
			"dedup_key": storedEvent.DedupKey,
			"session":   storedEvent.SessionName,
			"pane":      storedEvent.Pane,
		},
		"result": map[string]interface{}{
			"action":          strings.ToLower(strings.TrimSpace(req.Action)),
			"previous_state":  attentionStateLabel(previous),
			"new_state":       attentionStateLabel(nextState),
			"attention_state": nextState,
		},
		"audit": map[string]interface{}{
			"event_id":    auditEventID,
			"decision_id": auditDecisionID,
			"actor_type":  actorType,
			"actor_id":    actorID,
		},
	}, reqID)
}

func attentionReplayEarliestCursor(stats robot.JournalStats) int64 {
	if stats.Count == 0 {
		return stats.NewestCursor
	}
	return stats.OldestCursor
}

func attentionCursorExpired(sinceCursor int64, stats robot.JournalStats) (bool, int64) {
	earliest := attentionReplayEarliestCursor(stats)
	if sinceCursor <= 0 || stats.NewestCursor == 0 {
		return false, earliest
	}
	if stats.Count == 0 {
		return sinceCursor < stats.NewestCursor, earliest
	}
	return sinceCursor < earliest-1, earliest
}

// matchesAttentionFilters checks if an event matches the specified filters.
func matchesAttentionFilters(event robot.AttentionEvent, categoryFilter []string, sessionFilter string, actionabilityFilter []string) bool {
	// Category filter
	if len(categoryFilter) > 0 {
		matched := false
		for _, cat := range categoryFilter {
			if string(event.Category) == cat {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Session filter
	if sessionFilter != "" && event.Session != sessionFilter {
		return false
	}

	// Actionability filter
	if len(actionabilityFilter) > 0 {
		matched := false
		for _, act := range actionabilityFilter {
			if string(event.Actionability) == act {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// resolvePaneByIndex returns the complete pane snapshot so callers can run
// agent-specific admission before using its base-index-independent tmux ID.
// The context is honored so the HTTP layer can cancel a slow ListPanes call.
func resolvePaneByIndex(ctx context.Context, session string, paneIdx int) (tmux.Pane, error) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return tmux.Pane{}, fmt.Errorf("list panes: %w", err)
	}
	for _, p := range panes {
		if p.Index == paneIdx {
			return p, nil
		}
	}
	return tmux.Pane{}, fmt.Errorf("no pane with index %d in session %q", paneIdx, session)
}

func (s *Server) resolvePaneForRequest(w http.ResponseWriter, r *http.Request, session string, paneIdx int, reqID string) (tmux.Pane, bool) {
	resolvePane := resolvePaneByIndex
	if s.resolvePane != nil {
		resolvePane = s.resolvePane
	}
	pane, err := resolvePane(r.Context(), session, paneIdx)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("pane not found: %v", err), nil, reqID)
		return tmux.Pane{}, false
	}
	return pane, true
}

func (s *Server) resolvePaneTargetForRequest(w http.ResponseWriter, r *http.Request, session string, paneIdx int, reqID string) (string, bool) {
	pane, ok := s.resolvePaneForRequest(w, r, session, paneIdx, reqID)
	if !ok {
		return "", false
	}
	return pane.ID, true
}

// parseCSVParam parses a comma-separated query parameter into a slice.
func parseCSVParam(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Stop cleans up resources used by the Server.
func (s *Server) Stop() {
	if s.idempotencyStore != nil {
		s.idempotencyStore.Stop()
	}
	if s.wsHub != nil {
		s.wsHub.Stop()
		if st := s.wsHub.eventStore(); st != nil {
			st.Stop()
		}
	}
	if s.streamManager != nil {
		s.streamManager.StopAll()
	}
}
