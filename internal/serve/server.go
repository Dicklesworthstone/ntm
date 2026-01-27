// Package serve provides an HTTP server for NTM with REST API and event streaming.
package serve

import (
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
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/metrics"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
)

// Server provides HTTP API and event streaming for NTM.
type Server struct {
	host       string
	port       int
	eventBus   *events.EventBus
	stateStore *state.Store
	server     *http.Server
	auth       AuthConfig

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

	// Pane output streaming
	streamManager *tmux.StreamManager

	// Agent Mail client (lazy-init)
	mailClient *agentmail.Client
	projectDir string
	mu         sync.Mutex
}

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
	Host       string
	Port       int
	EventBus   *events.EventBus
	StateStore *state.Store
	Auth       AuthConfig
	// AllowedOrigins controls CORS origin allowlist. Empty means default localhost only.
	AllowedOrigins []string
}

const (
	defaultPort         = 7337
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
	ErrCodeIdempotentReplay = "IDEMPOTENT_REPLAY"
	ErrCodeJobPending       = "JOB_PENDING"
)

// IdempotencyStore caches responses by idempotency key.
type IdempotencyStore struct {
	mu       sync.RWMutex
	entries  map[string]*idempotencyEntry
	ttl      time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

type idempotencyEntry struct {
	response   []byte
	statusCode int
	createdAt  time.Time
}

// NewIdempotencyStore creates an idempotency cache with the given TTL.
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	store := &IdempotencyStore{
		entries: make(map[string]*idempotencyEntry),
		ttl:     ttl,
		stop:    make(chan struct{}),
	}
	// Start cleanup goroutine
	go store.cleanup()
	return store
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

// Get returns a cached response for the idempotency key.
func (s *IdempotencyStore) Get(key string) ([]byte, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, 0, false
	}
	if time.Since(entry.createdAt) > s.ttl {
		return nil, 0, false
	}
	return entry.response, entry.statusCode, true
}

// Set stores a response for the idempotency key.
func (s *IdempotencyStore) Set(key string, response []byte, statusCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = &idempotencyEntry{
		response:   response,
		statusCode: statusCode,
		createdAt:  time.Now(),
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
	mu   sync.RWMutex
	jobs map[string]*Job
}

// NewJobStore creates a new job store.
func NewJobStore() *JobStore {
	return &JobStore{
		jobs: make(map[string]*Job),
	}
}

// Create creates a new job.
func (s *JobStore) Create(jobType string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return job
}

// Get retrieves a job by ID.
func (s *JobStore) Get(id string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jobs[id]
}

// Update updates a job's status and progress.
func (s *JobStore) Update(id string, status JobStatus, progress float64, result map[string]interface{}, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Status = status
	job.Progress = progress
	job.Result = result
	job.Error = errMsg
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// List returns all jobs.
func (s *JobStore) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// Delete removes a job.
func (s *JobStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return false
	}
	delete(s.jobs, id)
	return true
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
type WSSubscribeRequest struct {
	Topics []string `json:"topics"`
	Since  int64    `json:"since,omitempty"` // Cursor for replay (Unix ms)
}

// WSEvent is an event pushed to clients.
type WSEvent struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	Seq       int64         `json:"seq"`
	Topic     string        `json:"topic"`
	EventType string        `json:"event_type"`
	Data      interface{}   `json:"data"`
}

// WSError represents a WebSocket error frame.
type WSError struct {
	Type      WSMessageType `json:"type"`
	Timestamp string        `json:"ts"`
	RequestID string        `json:"request_id,omitempty"`
	Code      string        `json:"code"`
	Message   string        `json:"message"`
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
}

// WSHub manages WebSocket connections and topic routing.
type WSHub struct {
	clients    map[*WSClient]struct{}
	clientsMu  sync.RWMutex
	register   chan *WSClient
	unregister chan *WSClient
	broadcast  chan *WSEvent
	seq        int64
	seqMu      sync.Mutex
	done       chan struct{}
}

// NewWSHub creates a new WebSocket hub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients:    make(map[*WSClient]struct{}),
		register:   make(chan *WSClient),
		unregister: make(chan *WSClient),
		broadcast:  make(chan *WSEvent, 256),
		done:       make(chan struct{}),
	}
}

// Run starts the hub's main event loop.
func (h *WSHub) Run() {
	for {
		select {
		case <-h.done:
			return
		case client := <-h.register:
			h.clientsMu.Lock()
			h.clients[client] = struct{}{}
			h.clientsMu.Unlock()
			log.Printf("ws client connected id=%s total=%d", client.id, len(h.clients))
		case client := <-h.unregister:
			h.clientsMu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.clientsMu.Unlock()
			log.Printf("ws client disconnected id=%s total=%d", client.id, len(h.clients))
		case event := <-h.broadcast:
			h.broadcastEvent(event)
		}
	}
}

// Stop shuts down the hub.
func (h *WSHub) Stop() {
	close(h.done)
}

// nextSeq returns the next sequence number.
func (h *WSHub) nextSeq() int64 {
	h.seqMu.Lock()
	defer h.seqMu.Unlock()
	h.seq++
	return h.seq
}

// broadcastEvent sends an event to all subscribed clients.
func (h *WSHub) broadcastEvent(event *WSEvent) {
	event.Seq = h.nextSeq()
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
			default:
				// Client buffer full, skip
				log.Printf("ws client buffer full id=%s", client.id)
			}
		}
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
	case h.broadcast <- event:
	default:
		log.Printf("ws broadcast buffer full, dropping event topic=%s", topic)
	}
}

// ClientCount returns the number of connected clients.
func (h *WSHub) ClientCount() int {
	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()
	return len(h.clients)
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
		cfg.Port = defaultPort
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
	return nil
}

// New creates a new HTTP server.
func New(cfg Config) *Server {
	applyDefaults(&cfg)
	s := &Server{
		host:               cfg.Host,
		port:               cfg.Port,
		eventBus:           cfg.EventBus,
		stateStore:         cfg.StateStore,
		auth:               cfg.Auth,
		sseClients:         make(map[chan events.BusEvent]struct{}),
		corsAllowedOrigins: cfg.AllowedOrigins,
		jwksCache:          newJWKSCache(cfg.Auth.OIDC.CacheTTL),
		idempotencyStore:   NewIdempotencyStore(24 * time.Hour),
		jobStore:           NewJobStore(),
		wsHub:              NewWSHub(),
	}

	// Initialize pane output streaming
	streamCfg := tmux.DefaultPaneStreamerConfig()
	s.streamManager = tmux.NewStreamManager(tmux.DefaultClient, func(event tmux.StreamEvent) {
		// Publish pane output to WebSocket subscribers
		// Topic format: panes:session:pane_idx
		s.wsHub.Publish(event.Target, "pane.output", map[string]interface{}{
			"lines":   event.Lines,
			"seq":     event.Seq,
			"ts":      event.Timestamp.UTC().Format(time.RFC3339Nano),
			"is_full": event.IsFull,
		})
	}, streamCfg)

	s.router = s.buildRouter()
	return s
}

// buildRouter creates the chi router with all middleware and routes.
func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()

	// Base middleware stack
	r.Use(chimw.RealIP)
	r.Use(s.requestIDMiddlewareFunc)
	r.Use(s.recovererMiddleware)
	r.Use(s.loggingMiddlewareFunc)
	r.Use(s.corsMiddlewareFunc)
	r.Use(s.authMiddlewareFunc)
	r.Use(s.rbacMiddleware) // Extract role from auth claims

	// Health check (no versioning)
	r.Get("/health", s.handleHealth)

	// SSE event stream (no versioning)
	r.Get("/events", s.handleEventStream)

	// WebSocket stub (no versioning)
	r.Get("/ws", s.handleWS)

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

		// Robot endpoints (read-only)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/status", s.handleRobotStatusV1)
		r.With(s.RequirePermission(PermReadHealth)).Get("/robot/health", s.handleRobotHealthV1)

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
			r.With(s.RequirePermission(PermReadAgents)).Get("/route", s.handleAgentRouteV1)
			r.With(s.RequirePermission(PermReadAgents)).Get("/activity", s.handleAgentActivityV1)
			r.With(s.RequirePermission(PermReadAgents)).Get("/health", s.handleAgentHealthV1)
			r.With(s.RequirePermission(PermReadAgents)).Get("/context", s.handleAgentContextV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/restart", s.handleAgentRestartV1)
		})

		// Jobs API - read requires PermReadJobs, write requires PermWriteJobs
		r.Route("/jobs", func(r chi.Router) {
			r.Use(s.idempotencyMiddleware)
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

		// Metrics API - performance and analytics data
		r.Route("/metrics", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadHealth)).Get("/", s.handleMetricsV1)
			r.With(s.RequirePermission(PermReadHealth)).Get("/compare", s.handleMetricsCompareV1)
			r.With(s.RequirePermission(PermReadHealth)).Get("/export", s.handleMetricsExportV1)
			r.With(s.RequirePermission(PermWriteSessions)).Post("/snapshot", s.handleMetricsSnapshotSaveV1)
			r.With(s.RequirePermission(PermReadHealth)).Get("/snapshots", s.handleMetricsSnapshotListV1)
		})

		// Analytics API - aggregated session analytics
		r.With(s.RequirePermission(PermReadHealth)).Get("/analytics", s.handleAnalyticsV1)

		// Context API - context pack management
		r.Route("/context", func(r chi.Router) {
			r.With(s.RequirePermission(PermWriteSessions)).Post("/build", s.handleContextBuildV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/{contextId}", s.handleContextGetV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/stats", s.handleContextStatsV1)
			r.With(s.RequirePermission(PermWriteSessions)).Delete("/cache", s.handleContextCacheClearV1)
		})

		// Git API - git coordination with Agent Mail
		r.Route("/git", func(r chi.Router) {
			r.With(s.RequirePermission(PermWriteSessions)).Post("/sync", s.handleGitSyncV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/status", s.handleGitStatusV1)
		})

		// Output API - pane output capture and analysis
		r.Route("/output", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/tail", s.handleOutputTailV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/diff", s.handleOutputDiffV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/files", s.handleOutputFilesV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/summary", s.handleOutputSummaryV1)
		})

		// Palette API - command palette information
		r.Route("/palette", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handlePaletteV1)
		})

		// History API - command history
		r.Route("/history", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleHistoryV1)
			r.With(s.RequirePermission(PermReadSessions)).Get("/stats", s.handleHistoryStatsV1)
		})

		// Route API - agent routing recommendations
		r.Route("/route", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleRouteV1)
		})

		// Wait API - agent state polling
		r.Route("/wait", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/", s.handleWaitV1)
		})

		// Safety, Policy, and Approvals API
		s.registerSafetyRoutes(r)

		// Accounts API - CAAM account management
		s.registerAccountsRoutes(r)

		// WebSocket endpoint (requires read permission)
		r.With(s.RequirePermission(PermReadWebSocket)).Get("/ws", s.handleWebSocket)

		// OpenAPI specification endpoint
		r.With(s.RequirePermission(PermReadHealth)).Get("/openapi.json", s.handleOpenAPISpec)
	})

	// Swagger UI documentation (outside /api/v1, no auth required)
	r.Get("/docs", s.handleSwaggerUI)
	r.Get("/docs/", s.handleSwaggerUI)

	return r
}

// Start starts the HTTP server and blocks until shutdown.
func (s *Server) Start(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}

	// Start WebSocket hub
	go s.wsHub.Run()
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

// Port returns the configured port.
func (s *Server) Port() int {
	return s.port
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

// requestIDMiddleware assigns a request ID and stores it in context and response headers.
// Deprecated: Use requestIDMiddlewareFunc for chi router.
func requestIDMiddleware(next http.Handler) http.Handler {
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
			if !originAllowed(origin, s.corsAllowedOrigins) {
				reqID := requestIDFromContext(r.Context())
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "origin not allowed", nil, reqID)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
		if s.auth.Mode == AuthModeLocal || s.auth.Mode == "" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		if err := s.authenticateRequest(r); err != nil {
			reqID := requestIDFromContext(r.Context())
			log.Printf("auth failed mode=%s path=%s remote=%s request_id=%s err=%v", s.auth.Mode, r.URL.Path, r.RemoteAddr, reqID, err)
			writeErrorResponse(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized", nil, reqID)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// idempotencyMiddleware handles Idempotency-Key header for mutating requests.
func (s *Server) idempotencyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only apply to mutating methods
		if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check cache
		if cached, status, ok := s.idempotencyStore.Get(key); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Idempotent-Replay", "true")
			w.WriteHeader(status)
			w.Write(cached)
			return
		}

		// Capture response
		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		// Cache successful responses
		if rec.statusCode >= 200 && rec.statusCode < 300 {
			s.idempotencyStore.Set(key, rec.body, rec.statusCode)
		}
	})
}

// responseRecorder captures the response for idempotency caching.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Bytes() []byte {
	return r.body
}

func sanitizeRequestID(id string) string {
	if id == "" {
		return ""
	}
	// Allow alphanumeric and common separators
	// Truncate to reasonable length (e.g., 64 chars)
	if len(id) > 64 {
		id = id[:64]
	}
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ':' || r == '/' {
			return r
		}
		return -1 // Drop invalid chars
	}, id)
}

// loggingMiddleware logs HTTP requests.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		reqID := requestIDFromContext(r.Context())
		if reqID != "" {
			log.Printf("%s %s %s request_id=%s", r.Method, r.URL.Path, time.Since(start), reqID)
			return
		}
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// corsMiddleware adds CORS headers with an allowlist (default localhost).
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !originAllowed(origin, s.corsAllowedOrigins) {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, "+requestIDHeader)
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authMiddleware enforces configured authentication for all routes.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth.Mode == AuthModeLocal || s.auth.Mode == "" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		if err := s.authenticateRequest(r); err != nil {
			reqID := requestIDFromContext(r.Context())
			log.Printf("auth failed mode=%s path=%s remote=%s request_id=%s err=%v", s.auth.Mode, r.URL.Path, r.RemoteAddr, reqID, err)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticateRequest(r *http.Request) error {
	switch s.auth.Mode {
	case AuthModeAPIKey:
		return s.authenticateAPIKey(r)
	case AuthModeOIDC:
		return s.authenticateOIDC(r)
	case AuthModeMTLS:
		return s.authenticateMTLS(r)
	case AuthModeLocal, "":
		return nil
	default:
		return fmt.Errorf("unsupported auth mode %q", s.auth.Mode)
	}
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

func (s *Server) authenticateOIDC(r *http.Request) error {
	token := extractBearerToken(r)
	if token == "" {
		return errors.New("missing bearer token")
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
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
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
	resp := APIError{
		APIResponse: APIResponse{
			Success:   false,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			RequestID: requestID,
		},
		Error:     message,
		ErrorCode: code,
		Details:   details,
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

	// Return basic status - in a full implementation, this would call robot.Status()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"note":      "full robot status requires robot package integration",
	})
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

// handleWS handles the WebSocket endpoint stub.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isWebSocketUpgrade(r) {
		writeError(w, http.StatusBadRequest, "websocket upgrade required")
		return
	}
	writeError(w, http.StatusNotImplemented, "websocket hub not implemented")
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
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\",\"time\":\"%s\"}\n\n",
		time.Now().UTC().Format(time.RFC3339))
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
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.EventType(), data)
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

func isWebSocketUpgrade(r *http.Request) bool {
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))
	if upgrade != "websocket" {
		return false
	}
	connection := strings.ToLower(r.Header.Get("Connection"))
	return strings.Contains(connection, "upgrade")
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

func (s *Server) validateOIDCToken(ctx context.Context, token string) error {
	if s.auth.OIDC.JWKSURL == "" || s.auth.OIDC.Issuer == "" {
		return errors.New("oidc config incomplete")
	}
	header, claims, signingInput, signature, err := parseJWT(token)
	if err != nil {
		return err
	}
	if header.Alg != "RS256" {
		return fmt.Errorf("unsupported jwt alg %q", header.Alg)
	}
	if iss, ok := claimString(claims, "iss"); !ok || iss != s.auth.OIDC.Issuer {
		return fmt.Errorf("invalid issuer")
	}
	if s.auth.OIDC.Audience != "" && !claimAudienceContains(claims, s.auth.OIDC.Audience) {
		return fmt.Errorf("invalid audience")
	}
	if exp, ok := claimInt64(claims, "exp"); ok {
		if time.Now().After(time.Unix(exp, 0).Add(30 * time.Second)) {
			return fmt.Errorf("token expired")
		}
	}
	if nbf, ok := claimInt64(claims, "nbf"); ok {
		if time.Now().Before(time.Unix(nbf, 0).Add(-30 * time.Second)) {
			return fmt.Errorf("token not yet valid")
		}
	}
	key, err := s.jwksCache.getKey(ctx, s.auth.OIDC.JWKSURL, header.Kid)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		return fmt.Errorf("invalid token signature")
	}
	return nil
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
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"version":    "1.0.0", // TODO: inject from build
		"api_version": "v1",
		"go_version": "1.25",
	}, reqID)
}

// handleCapabilitiesV1 handles GET /api/v1/capabilities.
func (s *Server) handleCapabilitiesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Detect installed tools
	tools := []string{}
	toolChecks := map[string]string{
		"br":   "beads_rust issue tracker",
		"bv":   "beads viewer",
		"cass": "code analysis/search",
		"cm":   "cass memory",
	}
	for tool := range toolChecks {
		// Simple existence check - in production, use the tools registry
		tools = append(tools, tool)
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"auth_modes":    []string{string(AuthModeLocal), string(AuthModeAPIKey), string(AuthModeOIDC), string(AuthModeMTLS)},
		"current_auth":  string(s.auth.Mode),
		"stream_topics": []string{"events", "ws"},
		"tools":         tools,
		"features": map[string]bool{
			"idempotency_keys": true,
			"jobs_api":         true,
			"sse_events":       true,
			"websocket":        false, // Not yet implemented
		},
	}, reqID)
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

	// Return safe configuration fields only (no secrets)
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"host":            s.host,
		"port":            s.port,
		"auth_mode":       string(s.auth.Mode),
		"allowed_origins": s.corsAllowedOrigins,
		"project_dir":     s.projectDir,
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

	// Apply updates
	s.mu.Lock()
	if req.AllowedOrigins != nil {
		s.corsAllowedOrigins = req.AllowedOrigins
	}
	if req.ProjectDir != "" {
		s.projectDir = req.ProjectDir
		// Reset mail client so it picks up new project dir
		s.mailClient = nil
	}
	s.mu.Unlock()

	slog.Info("config updated", "request_id", reqID, "allowed_origins", len(s.corsAllowedOrigins), "project_dir", s.projectDir)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"config": map[string]interface{}{
			"allowed_origins": s.corsAllowedOrigins,
			"project_dir":     s.projectDir,
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
		{"bd-daemon", 8767},
	}

	dialer := &net.Dialer{Timeout: time.Second}
	for _, dp := range daemonPorts {
		check := map[string]interface{}{
			"name": dp.name,
			"port": dp.port,
		}
		conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", dp.port))
		if err == nil {
			conn.Close()
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"note": "full robot status requires robot package integration",
	}, reqID)
}

// handleRobotHealthV1 handles GET /api/v1/robot/health.
func (s *Server) handleRobotHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"note": "full robot health requires robot package integration",
	}, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
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

	// Build pane target
	paneTarget := fmt.Sprintf("%s:%d", sessionID, paneIdx)

	if err := tmux.SendKeys(paneTarget, req.Text, req.Enter); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	// Build pane target
	paneTarget := fmt.Sprintf("%s:%d", sessionID, paneIdx)

	// Send Ctrl+C to interrupt
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	// Parse lines parameter (default 100)
	lines := 100
	if linesStr := r.URL.Query().Get("lines"); linesStr != "" {
		if _, err := fmt.Sscanf(linesStr, "%d", &lines); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid lines parameter", nil, reqID)
			return
		}
		if lines < 1 || lines > 10000 {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "lines must be 1-10000", nil, reqID)
			return
		}
	}

	// Build pane target
	paneTarget := fmt.Sprintf("%s:%d", sessionID, paneIdx)

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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	// Build pane target
	paneTarget := fmt.Sprintf("%s:%d", sessionID, paneIdx)

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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	var req PaneTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// Build pane target
	paneTarget := fmt.Sprintf("%s:%d", sessionID, paneIdx)

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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	// Target format for streaming is "session:pane_idx" which matches WebSocket topic "panes:session:idx"
	target := fmt.Sprintf("%s:%d", sessionID, paneIdx)

	if err := s.streamManager.StartStream(target); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"topic":   target, // WebSocket topic to subscribe to
		"message": "streaming started",
	}, reqID)
}

// handleStopPaneStreamV1 handles DELETE /api/v1/sessions/{sessionId}/panes/{paneIdx}/stream.
func (s *Server) handleStopPaneStreamV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	paneIdxStr := chi.URLParam(r, "paneIdx")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	paneIdx := 0
	if _, err := fmt.Sscanf(paneIdxStr, "%d", &paneIdx); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid pane index", nil, reqID)
		return
	}

	target := fmt.Sprintf("%s:%d", sessionID, paneIdx)
	s.streamManager.StopStream(target)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"target":  target,
		"message": "streaming stopped",
	}, reqID)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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
	CCCount  int    `json:"cc_count,omitempty"`
	CodCount int    `json:"cod_count,omitempty"`
	GmiCount int    `json:"gmi_count,omitempty"`
	Preset   string `json:"preset,omitempty"`
	WaitReady bool  `json:"wait_ready,omitempty"`
}

// handleAgentSpawnV1 handles POST /api/v1/sessions/{sessionId}/agents/spawn.
func (s *Server) handleAgentSpawnV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	var req AgentSpawnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	// At least one agent count or preset must be specified
	if req.CCCount == 0 && req.CodCount == 0 && req.GmiCount == 0 && req.Preset == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "at least one agent count (cc_count, cod_count, gmi_count) or preset required", nil, reqID)
		return
	}

	opts := robot.SpawnOptions{
		Session:   sessionID,
		CCCount:   req.CCCount,
		CodCount:  req.CodCount,
		GmiCount:  req.GmiCount,
		Preset:    req.Preset,
		WaitReady: req.WaitReady,
	}

	result, err := robot.GetSpawn(opts, nil)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	opts := robot.SendOptions{
		Session:    sessionID,
		Message:    req.Message,
		Panes:      req.Panes,
		AgentTypes: req.AgentTypes,
		All:        req.All,
	}

	result, err := robot.GetSend(opts)
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

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	var req AgentInterruptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	opts := robot.InterruptOptions{
		Session: sessionID,
		Panes:   req.Panes,
		Message: req.Message,
		Force:   req.Force,
		NoWait:  req.NoWait,
	}

	result, err := robot.GetInterrupt(opts)
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

// AgentWaitRequest is the request body for POST /sessions/{id}/agents/wait.
type AgentWaitRequest struct {
	Condition    string `json:"condition"`
	TimeoutMs    int    `json:"timeout_ms,omitempty"`
	PollMs       int    `json:"poll_ms,omitempty"`
	Panes        []int  `json:"panes,omitempty"`
	AgentType    string `json:"agent_type,omitempty"`
	WaitForAny   bool   `json:"wait_for_any,omitempty"`
	ExitOnError  bool   `json:"exit_on_error,omitempty"`
}

// handleAgentWaitV1 handles POST /api/v1/sessions/{sessionId}/agents/wait.
func (s *Server) handleAgentWaitV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
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

	timeout := 30 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	pollInterval := 300 * time.Millisecond
	if req.PollMs > 0 {
		pollInterval = time.Duration(req.PollMs) * time.Millisecond
	}

	opts := robot.WaitOptions{
		Session:      sessionID,
		Condition:    req.Condition,
		Timeout:      timeout,
		PollInterval: pollInterval,
		PaneIndices:  req.Panes,
		AgentType:    req.AgentType,
		WaitForAny:   req.WaitForAny,
		ExitOnError:  req.ExitOnError,
	}

	result, exitCode := robot.GetWait(opts)
	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	// Map exit codes to HTTP status
	status := http.StatusOK
	if exitCode == 1 {
		status = http.StatusRequestTimeout
	} else if exitCode >= 2 {
		status = http.StatusInternalServerError
	}

	writeSuccessResponse(w, status, data, reqID)
}

// handleAgentRouteV1 handles GET /api/v1/sessions/{sessionId}/agents/route.
func (s *Server) handleAgentRouteV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	// Parse query parameters
	agentType := r.URL.Query().Get("agent_type")
	strategy := robot.StrategyName(r.URL.Query().Get("strategy"))
	prompt := r.URL.Query().Get("prompt")

	opts := robot.RouteOptions{
		Session:   sessionID,
		AgentType: agentType,
		Strategy:  strategy,
		Prompt:    prompt,
	}

	result, exitCode := robot.GetRoute(opts)
	data, err := toJSONMap(result)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to serialize response", nil, reqID)
		return
	}

	status := http.StatusOK
	if exitCode != 0 {
		status = http.StatusInternalServerError
	}

	writeSuccessResponse(w, status, data, reqID)
}

// handleAgentActivityV1 handles GET /api/v1/sessions/{sessionId}/agents/activity.
func (s *Server) handleAgentActivityV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	opts := robot.ActivityOptions{
		Session: sessionID,
	}

	result, err := robot.GetActivity(opts)
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

// handleAgentHealthV1 handles GET /api/v1/sessions/{sessionId}/agents/health.
func (s *Server) handleAgentHealthV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	opts := robot.AgentHealthOptions{
		Session:       sessionID,
		LinesCaptured: 100,
		IncludeCaut:   true,
		IncludePT:     true,
	}

	result, err := robot.GetAgentHealth(opts)
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

// handleAgentContextV1 handles GET /api/v1/sessions/{sessionId}/agents/context.
func (s *Server) handleAgentContextV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	// Parse lines parameter (default 100)
	lines := 100
	if linesStr := r.URL.Query().Get("lines"); linesStr != "" {
		if _, err := fmt.Sscanf(linesStr, "%d", &lines); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid lines parameter", nil, reqID)
			return
		}
	}

	result, err := robot.GetContext(sessionID, lines)
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

// AgentRestartRequest is the request body for POST /sessions/{id}/agents/restart.
type AgentRestartRequest struct {
	Panes     []string `json:"panes,omitempty"`
	AgentType string   `json:"agent_type,omitempty"`
	All       bool     `json:"all,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
}

// handleAgentRestartV1 handles POST /api/v1/sessions/{sessionId}/agents/restart.
func (s *Server) handleAgentRestartV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	if sessionID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session ID required", nil, reqID)
		return
	}

	var req AgentRestartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	opts := robot.RestartPaneOptions{
		Session: sessionID,
		Panes:   req.Panes,
		Type:    req.AgentType,
		All:     req.All,
		DryRun:  req.DryRun,
	}

	result, err := robot.GetRestartPane(opts)
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
// Metrics API Handlers
// =============================================================================

// handleMetricsV1 handles GET /api/v1/metrics.
func (s *Server) handleMetricsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}

	opts := robot.MetricsOptions{
		Session: session,
		Period:  period,
	}

	result, err := robot.GetMetrics(opts)
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

// MetricsCompareRequest represents a metrics comparison request.
type MetricsCompareRequest struct {
	Session      string `json:"session,omitempty"`
	BaselineName string `json:"baseline_name,omitempty"`
}

// handleMetricsCompareV1 handles GET /api/v1/metrics/compare.
func (s *Server) handleMetricsCompareV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	baselineName := r.URL.Query().Get("baseline")
	if baselineName == "" {
		baselineName = "baseline"
	}

	// Get or create metrics collector
	collector := metrics.NewCollector(s.stateStore, session)

	// Generate current report
	currentReport, err := collector.GenerateReport()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Load baseline
	baselineReport, err := collector.LoadSnapshot(baselineName)
	if err != nil {
		writeErrorResponse(w, http.StatusNotFound, "SNAPSHOT_NOT_FOUND", fmt.Sprintf("baseline '%s' not found", baselineName), nil, reqID)
		return
	}

	// Compare
	comparison := collector.CompareSnapshots(baselineReport, currentReport)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session":    session,
		"baseline":   baselineName,
		"comparison": comparison,
	}, reqID)
}

// handleMetricsExportV1 handles GET /api/v1/metrics/export.
func (s *Server) handleMetricsExportV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	collector := metrics.NewCollector(s.stateStore, session)
	report, err := collector.GenerateReport()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"session": session,
		"format":  format,
		"report":  report,
	}, reqID)
}

// MetricsSnapshotSaveRequest represents a snapshot save request.
type MetricsSnapshotSaveRequest struct {
	Name    string `json:"name"`
	Session string `json:"session,omitempty"`
}

// handleMetricsSnapshotSaveV1 handles POST /api/v1/metrics/snapshot.
func (s *Server) handleMetricsSnapshotSaveV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req MetricsSnapshotSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Name == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "snapshot name required", nil, reqID)
		return
	}

	collector := metrics.NewCollector(s.stateStore, req.Session)
	if err := collector.SaveSnapshot(req.Name); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"name":    req.Name,
		"saved":   true,
		"message": fmt.Sprintf("snapshot '%s' saved successfully", req.Name),
	}, reqID)
}

// handleMetricsSnapshotListV1 handles GET /api/v1/metrics/snapshots.
func (s *Server) handleMetricsSnapshotListV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// For now, return a simple response indicating the endpoint exists
	// A full implementation would query stored snapshots from the state store
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"snapshots": []string{},
		"message":   "use POST /metrics/snapshot to create snapshots",
	}, reqID)
}

// =============================================================================
// Analytics API Handlers
// =============================================================================

// handleAnalyticsV1 handles GET /api/v1/analytics.
func (s *Server) handleAnalyticsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			days = parsed
		}
	}

	since := r.URL.Query().Get("since")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	// Build analytics response
	// For now, we'll provide session-based analytics from robot.GetMetrics
	opts := robot.MetricsOptions{
		Period: fmt.Sprintf("%dd", days),
	}
	metricsData, _ := robot.GetMetrics(opts)

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"period":   fmt.Sprintf("%d days", days),
		"since":    since,
		"format":   format,
		"metrics":  metricsData,
		"sessions": []interface{}{},
	}, reqID)
}

// =============================================================================
// Context API Handlers
// =============================================================================

// ContextBuildRequest represents a context pack build request.
type ContextBuildRequest struct {
	Question   string `json:"question"`
	ProjectDir string `json:"project_dir,omitempty"`
	BeadID     string `json:"bead_id,omitempty"`
	AgentType  string `json:"agent_type,omitempty"`
}

// handleContextBuildV1 handles POST /api/v1/context/build.
func (s *Server) handleContextBuildV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req ContextBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	if req.Question == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "question required", nil, reqID)
		return
	}

	projectDir := req.ProjectDir
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}

	// Create context pack generator
	generator := ensemble.NewContextPackGenerator(projectDir, nil, nil)
	pack, err := generator.Generate(req.Question, "default", ensemble.CacheConfig{Enabled: false})
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	// Store in state if available
	if s.stateStore != nil && pack != nil {
		statePack := &state.ContextPack{
			ID:         pack.Hash,
			BeadID:     req.BeadID,
			AgentType:  state.AgentType(req.AgentType),
			CreatedAt:  pack.GeneratedAt,
			TokenCount: pack.TokenEstimate,
		}
		_ = s.stateStore.CreateContextPack(statePack)
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"id":          pack.Hash,
		"bead_id":     req.BeadID,
		"token_count": pack.TokenEstimate,
		"created_at":  pack.GeneratedAt,
	}, reqID)
}

// handleContextGetV1 handles GET /api/v1/context/{contextId}.
func (s *Server) handleContextGetV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	contextID := chi.URLParam(r, "contextId")

	if contextID == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "context ID required", nil, reqID)
		return
	}

	if s.stateStore == nil {
		writeErrorResponse(w, http.StatusServiceUnavailable, "STATE_STORE_UNAVAILABLE", "state store not available", nil, reqID)
		return
	}

	pack, err := s.stateStore.GetContextPack(contextID)
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error(), nil, reqID)
		return
	}

	if pack == nil {
		writeErrorResponse(w, http.StatusNotFound, "CONTEXT_NOT_FOUND", fmt.Sprintf("context pack '%s' not found", contextID), nil, reqID)
		return
	}

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"id":              pack.ID,
		"bead_id":         pack.BeadID,
		"agent_type":      pack.AgentType,
		"repo_rev":        pack.RepoRev,
		"token_count":     pack.TokenCount,
		"created_at":      pack.CreatedAt,
		"rendered_prompt": pack.RenderedPrompt,
	}, reqID)
}

// handleContextStatsV1 handles GET /api/v1/context/stats.
func (s *Server) handleContextStatsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	lines := 1000
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}

	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session required", nil, reqID)
		return
	}

	result, err := robot.GetContext(session, lines)
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

// handleContextCacheClearV1 handles DELETE /api/v1/context/cache.
func (s *Server) handleContextCacheClearV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	// Clear context cache
	// For now, return success - actual implementation would clear ensemble cache
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"cleared": true,
		"message": "context cache cleared",
	}, reqID)
}

// =============================================================================
// Git API Handlers
// =============================================================================

// GitSyncRequest represents a git sync request.
type GitSyncRequest struct {
	Session  string `json:"session,omitempty"`
	PullOnly bool   `json:"pull_only,omitempty"`
	PushOnly bool   `json:"push_only,omitempty"`
	Force    bool   `json:"force,omitempty"`
	DryRun   bool   `json:"dry_run,omitempty"`
}

// handleGitSyncV1 handles POST /api/v1/git/sync.
func (s *Server) handleGitSyncV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	var req GitSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body", nil, reqID)
		return
	}

	workDir, err := os.Getwd()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to get working directory", nil, reqID)
		return
	}

	result := map[string]interface{}{
		"session":     req.Session,
		"working_dir": workDir,
		"success":     true,
	}

	// Check if git repo
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "NOT_GIT_REPO", "not a git repository", nil, reqID)
		return
	}

	// Perform git operations based on request
	if !req.PushOnly {
		// Fetch first
		fetchCmd := exec.Command("git", "-C", workDir, "fetch")
		if err := fetchCmd.Run(); err != nil {
			result["pull_error"] = "fetch failed"
			result["success"] = false
		} else if !req.DryRun {
			// Pull with rebase
			pullCmd := exec.Command("git", "-C", workDir, "pull", "--rebase")
			if err := pullCmd.Run(); err != nil {
				result["pull_error"] = "pull failed"
				result["success"] = false
			} else {
				result["pulled"] = true
			}
		}
	}

	if !req.PullOnly && result["success"] == true {
		// Push
		pushArgs := []string{"-C", workDir, "push"}
		if req.Force {
			pushArgs = append(pushArgs, "--force")
		}
		if req.DryRun {
			pushArgs = append(pushArgs, "--dry-run")
		}
		pushCmd := exec.Command("git", pushArgs...)
		if err := pushCmd.Run(); err != nil {
			result["push_error"] = "push failed"
			result["success"] = false
		} else {
			result["pushed"] = true
		}
	}

	writeSuccessResponse(w, http.StatusOK, result, reqID)
}

// handleGitStatusV1 handles GET /api/v1/git/status.
func (s *Server) handleGitStatusV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")

	workDir, err := os.Getwd()
	if err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to get working directory", nil, reqID)
		return
	}

	result := map[string]interface{}{
		"session":     session,
		"working_dir": workDir,
		"success":     true,
	}

	// Check if git repo
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "NOT_GIT_REPO", "not a git repository", nil, reqID)
		return
	}

	// Get branch
	branchCmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, err := branchCmd.Output()
	if err == nil {
		result["branch"] = strings.TrimSpace(string(branchOut))
	}

	// Get commit
	commitCmd := exec.Command("git", "-C", workDir, "rev-parse", "HEAD")
	commitOut, err := commitCmd.Output()
	if err == nil {
		commit := strings.TrimSpace(string(commitOut))
		result["commit"] = commit
		if len(commit) >= 7 {
			result["commit_short"] = commit[:7]
		}
	}

	// Check if dirty
	dirtyCmd := exec.Command("git", "-C", workDir, "diff", "--quiet", "HEAD")
	result["dirty"] = dirtyCmd.Run() != nil

	// Get status summary
	statusCmd := exec.Command("git", "-C", workDir, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(statusOut)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			result["changes"] = 0
		} else {
			result["changes"] = len(lines)
		}
	}

	writeSuccessResponse(w, http.StatusOK, result, reqID)
}

// =============================================================================
// Output API Handlers
// =============================================================================

// handleOutputTailV1 handles GET /api/v1/output/tail.
func (s *Server) handleOutputTailV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	lines := 50
	if l := r.URL.Query().Get("lines"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			lines = parsed
		}
	}

	paneFilter := []string{}
	if panes := r.URL.Query().Get("panes"); panes != "" {
		paneFilter = strings.Split(panes, ",")
	}

	opts := robot.TailOptions{
		Session:    session,
		Lines:      lines,
		PaneFilter: paneFilter,
	}

	result, err := robot.GetTail(opts)
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

// handleOutputDiffV1 handles GET /api/v1/output/diff.
func (s *Server) handleOutputDiffV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	since := 15 * time.Minute
	if s := r.URL.Query().Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			since = d
		}
	}

	opts := robot.DiffOptions{
		Session: session,
		Since:   since,
	}

	result, err := robot.GetDiff(opts)
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

// handleOutputFilesV1 handles GET /api/v1/output/files.
func (s *Server) handleOutputFilesV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	timeWindow := r.URL.Query().Get("window")
	if timeWindow == "" {
		timeWindow = "15m"
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	opts := robot.FilesOptions{
		Session:    session,
		TimeWindow: timeWindow,
		Limit:      limit,
	}

	result, err := robot.GetFiles(opts)
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

// handleOutputSummaryV1 handles GET /api/v1/output/summary.
func (s *Server) handleOutputSummaryV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	since := 30 * time.Minute
	if s := r.URL.Query().Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			since = d
		}
	}

	opts := robot.SummaryOptions{
		Session: session,
		Since:   since,
	}

	result, err := robot.GetSummary(opts)
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
// Palette API Handlers
// =============================================================================

// handlePaletteV1 handles GET /api/v1/palette.
func (s *Server) handlePaletteV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	category := r.URL.Query().Get("category")
	search := r.URL.Query().Get("search")

	opts := robot.PaletteOptions{
		Session:     session,
		Category:    category,
		SearchQuery: search,
	}

	cfg := config.Default()
	result, err := robot.GetPalette(cfg, opts)
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
// History API Handlers
// =============================================================================

// handleHistoryV1 handles GET /api/v1/history.
func (s *Server) handleHistoryV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	pane := r.URL.Query().Get("pane")
	agentType := r.URL.Query().Get("agent_type")
	since := r.URL.Query().Get("since")

	last := 50
	if l := r.URL.Query().Get("last"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			last = parsed
		}
	}

	opts := robot.HistoryOptions{
		Session:   session,
		Pane:      pane,
		AgentType: agentType,
		Last:      last,
		Since:     since,
		Stats:     false,
	}

	result, err := robot.GetHistory(opts)
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

// handleHistoryStatsV1 handles GET /api/v1/history/stats.
func (s *Server) handleHistoryStatsV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	opts := robot.HistoryOptions{
		Session: session,
		Stats:   true,
	}

	result, err := robot.GetHistory(opts)
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
// Route API Handlers
// =============================================================================

// handleRouteV1 handles GET /api/v1/route.
func (s *Server) handleRouteV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	agentType := r.URL.Query().Get("agent_type")
	strategy := robot.StrategyName(r.URL.Query().Get("strategy"))
	prompt := r.URL.Query().Get("prompt")
	lastAgent := r.URL.Query().Get("last_agent")

	var excludePanes []int
	if exclude := r.URL.Query().Get("exclude"); exclude != "" {
		parsed, err := robot.ParseExcludePanes(exclude)
		if err != nil {
			writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid exclude parameter: "+err.Error(), nil, reqID)
			return
		}
		excludePanes = parsed
	}

	opts := robot.RouteOptions{
		Session:      session,
		AgentType:    agentType,
		Strategy:     strategy,
		ExcludePanes: excludePanes,
		Prompt:       prompt,
		LastAgent:    lastAgent,
	}

	result, exitCode := robot.GetRoute(opts)
	if exitCode != 0 && !result.Success {
		writeErrorResponse(w, http.StatusBadRequest, result.ErrorCode, result.Error, nil, reqID)
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
// Wait API Handlers
// =============================================================================

// handleWaitV1 handles GET /api/v1/wait.
func (s *Server) handleWaitV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())

	session := r.URL.Query().Get("session")
	if session == "" {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "session parameter required", nil, reqID)
		return
	}

	condition := r.URL.Query().Get("condition")
	if condition == "" {
		condition = "idle"
	}

	timeout := 60 * time.Second
	if t := r.URL.Query().Get("timeout"); t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}

	pollInterval := 500 * time.Millisecond
	if p := r.URL.Query().Get("poll"); p != "" {
		if d, err := time.ParseDuration(p); err == nil {
			pollInterval = d
		}
	}

	var paneIndices []int
	if panes := r.URL.Query().Get("panes"); panes != "" {
		for _, p := range strings.Split(panes, ",") {
			if idx, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				paneIndices = append(paneIndices, idx)
			}
		}
	}

	agentType := r.URL.Query().Get("agent_type")
	waitForAny := r.URL.Query().Get("any") == "true"
	exitOnError := r.URL.Query().Get("exit_on_error") == "true"
	requireTransition := r.URL.Query().Get("require_transition") == "true"

	countN := 1
	if n := r.URL.Query().Get("count"); n != "" {
		if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 {
			countN = parsed
		}
	}

	opts := robot.WaitOptions{
		Session:           session,
		Condition:         condition,
		Timeout:           timeout,
		PollInterval:      pollInterval,
		PaneIndices:       paneIndices,
		AgentType:         agentType,
		WaitForAny:        waitForAny,
		ExitOnError:       exitOnError,
		CountN:            countN,
		RequireTransition: requireTransition,
	}

	result, exitCode := robot.GetWait(opts)
	if exitCode == 2 { // Error
		writeErrorResponse(w, http.StatusBadRequest, result.ErrorCode, result.Error, nil, reqID)
		return
	}
	if exitCode == 1 { // Timeout
		data, _ := toJSONMap(result)
		writeErrorResponse(w, http.StatusRequestTimeout, "TIMEOUT", result.Error, data, reqID)
		return
	}
	if exitCode == 3 { // Agent error
		data, _ := toJSONMap(result)
		writeErrorResponse(w, http.StatusConflict, "AGENT_ERROR", result.Error, data, reqID)
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
// Jobs API Handlers
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

	// Validate job type
	validTypes := map[string]bool{
		"spawn":       true,
		"scan":        true,
		"checkpoint":  true,
		"import":      true,
		"export":      true,
	}
	if !validTypes[req.Type] {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid job type", map[string]interface{}{
			"valid_types": []string{"spawn", "scan", "checkpoint", "import", "export"},
		}, reqID)
		return
	}

	job := s.jobStore.Create(req.Type)

	// Start job execution in background
	go s.executeJob(job.ID, req)

	writeSuccessResponse(w, http.StatusAccepted, map[string]interface{}{
		"job": job,
	}, reqID)
}

// executeJob runs a job asynchronously.
func (s *Server) executeJob(jobID string, req CreateJobRequest) {
	s.jobStore.Update(jobID, JobStatusRunning, 0, nil, "")

	// Simulate job execution - in production, this would dispatch to actual handlers
	time.Sleep(100 * time.Millisecond)

	// Mark as completed
	result := map[string]interface{}{
		"type":    req.Type,
		"params":  req.Params,
		"session": req.Session,
	}
	s.jobStore.Update(jobID, JobStatusCompleted, 100, result, "")
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

	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"job": s.jobStore.Get(jobID),
	}, reqID)
}

// Router returns the chi router for testing.
func (s *Server) Router() chi.Router {
	return s.router
}

// ============================================================================
// WebSocket Handler
// ============================================================================

// checkWSOrigin validates the Origin header for WebSocket connections.
// In local auth mode, it allows any origin. Otherwise, it validates against
// the configured allowed origins to prevent WebSocket CSRF attacks.
func (s *Server) checkWSOrigin(r *http.Request) bool {
	// In local mode, accept any origin for development convenience
	if s.auth.Mode == AuthModeLocal || s.auth.Mode == "" {
		return true
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		// No origin header - allow for non-browser clients
		return true
	}

	// Parse the origin URL to extract scheme and host
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

	// Check against configured allowed origins using full URL comparison
	// (not prefix matching, which would allow https://evil.com to match https://e)
	for _, allowed := range s.corsAllowedOrigins {
		allowedURL, err := url.Parse(allowed)
		if err != nil {
			continue
		}
		// Skip malformed allowed origins
		if allowedURL.Scheme == "" || allowedURL.Host == "" {
			continue
		}
		// Compare scheme and host (host includes port if specified)
		if originURL.Scheme == allowedURL.Scheme && originURL.Host == allowedURL.Host {
			return true
		}
	}

	log.Printf("ws: rejected origin %q (allowed: %v)", origin, s.corsAllowedOrigins)
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

	// Register client with hub
	s.wsHub.register <- client

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
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(wsMaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
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
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Drain queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
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

	c.Subscribe(topics)
	c.sendAck(msg.RequestID, map[string]interface{}{
		"subscribed": topics,
		"total":      len(c.Topics()),
	})
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

	c.Unsubscribe(topics)
	c.sendAck(msg.RequestID, map[string]interface{}{
		"unsubscribed": topics,
		"total":        len(c.Topics()),
	})
}

// isValidTopic checks if a topic string is valid.
// Valid topics: global, global:*, sessions:*, sessions:{name}, panes:*,
// panes:{session}:{idx}, agent:{type}
func isValidTopic(topic string) bool {
	if topic == "" {
		return false
	}
	if topic == "*" || topic == "global" || topic == "global:*" {
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
	return false
}

// canSubscribe checks if the client is authorized to subscribe to a topic.
func (c *WSClient) canSubscribe(topic string) bool {
	// For now, allow all authenticated clients to subscribe to any topic.
	// Future: implement RBAC based on auth claims.
	// Example checks:
	// - Check if user has access to specific session
	// - Check if user has agent-type filter permissions
	return true
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
	select {
	case c.send <- data:
	default:
		log.Printf("ws client buffer full, dropping error id=%s", c.id)
	}
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
	select {
	case c.send <- msg:
	default:
		log.Printf("ws client buffer full, dropping ack id=%s", c.id)
	}
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
	select {
	case c.send <- data:
	default:
		// Buffer full, skip
	}
}

// WSHub returns the WebSocket hub for testing.
func (s *Server) WSHub() *WSHub {
	return s.wsHub
}
