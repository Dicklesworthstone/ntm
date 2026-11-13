// Package serve provides an HTTP server for NTM with REST API and event streaming.
package serve

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	approvalpkg "github.com/Dicklesworthstone/ntm/internal/approval"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/robot"
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
	jobStore    *JobStore
	jobExecutor jobExecutor

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
	// paneInputTimeout bounds lookup, ownership waits, and canonical delivery.
	// Zero means defaultPaneInputTimeout; tests may shorten it.
	paneInputTimeout time.Duration
	// listLiveSessions lists the tmux sessions that are running right now.
	// Nil in production (tmux.ListSessionsContext); tests inject fixtures.
	listLiveSessions func(context.Context) ([]tmux.Session, error)
	// liveSessionsTimeout bounds each live tmux session or pane listing made
	// by the sessions and agents endpoints; zero means
	// defaultLiveSessionsTimeout. Tests shorten it.
	liveSessionsTimeout time.Duration
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
	// startupProjectDir is the project the server was started with (Config's
	// ProjectDir; empty when none was given). It never changes after New. A
	// configuration PATCH with an empty or null project_dir restores it.
	startupProjectDir string
	// pipelineScope is set only on an immutable per-operation execution view.
	// It pins cwd fallback separately from configured file-workflow access.
	pipelineScope *pipelineProjectScope
	// auditStore persists REST/WS audit records. Nil when the store could not be
	// opened, in which case the audit middleware is not installed.
	auditStore *AuditStore
	mu         sync.RWMutex

	// Redaction configuration for REST API
	redactionCfg *RedactionConfig

	// webUI is the embedded static dashboard; nil when not mounted.
	webUI fs.FS

	// wsTicketsMu guards single-use short-lived WebSocket upgrade tickets issued
	// via POST /api/v1/auth/ws-ticket.
	wsTicketsMu sync.Mutex
	wsTickets   map[string]wsTicketRecord

	// projectionState reports whether the runtime projection store is still
	// performing its initial asynchronous refresh ("warming_up"), up to date
	// ("ready"), or degraded ("degraded"). Guarded by mu.
	projectionState string
}

// Config holds server configuration.
type Config struct {
	Host string
	Port int
	// PublicBaseURL advertises the externally reachable base URL for clients.
	// Optional: leave empty to derive from host/port in documentation or clients.
	PublicBaseURL string
	// ProjectDir is the server-wide project used by mail, beads, and project-scoped
	// jobs. Nonempty paths must name an existing directory. Empty leaves the
	// server unscoped until a project is selected through the configuration API.
	// It is also the value a configuration PATCH with an empty or null
	// project_dir restores.
	ProjectDir string
	EventBus   *events.EventBus
	StateStore *state.Store
	Auth       AuthConfig
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
	// Redaction scrubs secrets/PII from REST bodies and WebSocket event frames.
	// Nil disables it, which is what every caller effectively did until now:
	// redactionMiddleware has been mounted since b5d45e20 but the field behind
	// it had no production writer, so the G1 dead-code gate deleted the
	// Set/GetRedactionConfig setters in 670f6380 rather than wiring them, and
	// the middleware has been a permanent no-op ever since. Same failure the
	// AuditMiddleware comment in buildRouter describes. Ownership of the
	// contents transfers to New, which deep-copies before storing.
	Redaction *RedactionConfig

	// Async job limits. Zero selects the default; negative values are invalid.
	// These limits are fixed for the server lifetime, including after Stop.
	JobConcurrency   int
	JobQueueCapacity int
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

// Every body admission and replay check must use the same limit. Hashing only
// a prefix of an accepted request can acknowledge a different mutation with a
// cached success, without ever executing the submitted operation.
const maxRequestBodyBytes = 10 << 20 // 10 MiB

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

// ResolveProjectDir validates a server project and returns its absolute,
// symlink-resolved path so every project-scoped route uses the same identity.
// An empty path preserves the unscoped server default.
func ResolveProjectDir(projectDir string) (string, error) {
	if projectDir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory %q: %w", projectDir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project directory %q: %w", projectDir, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project directory %q: %w", projectDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project directory %q is not a directory", projectDir)
	}
	return resolved, nil
}

// ValidateConfig checks server configuration for security and completeness.
func ValidateConfig(cfg Config) error {
	applyDefaults(&cfg)
	if err := validateJobLimits(cfg.JobConcurrency, cfg.JobQueueCapacity); err != nil {
		return err
	}
	if _, err := ResolveProjectDir(cfg.ProjectDir); err != nil {
		return err
	}

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
	// New cannot return configuration errors; retain invalid paths so Start's
	// validation rejects them. Valid paths are canonical even before Start,
	// including for callers that embed Router directly.
	if projectDir, err := ResolveProjectDir(cfg.ProjectDir); err == nil {
		cfg.ProjectDir = projectDir
	}
	initialProjectionState := "ready"
	if cfg.StateStore != nil {
		initialProjectionState = "warming_up"
	}
	s := &Server{
		host:               cfg.Host,
		port:               cfg.Port,
		publicBaseURL:      cfg.PublicBaseURL,
		projectDir:         cfg.ProjectDir,
		startupProjectDir:  cfg.ProjectDir,
		version:            cfg.Version,
		eventBus:           cfg.EventBus,
		stateStore:         cfg.StateStore,
		auth:               cfg.Auth,
		auditStore:         cfg.AuditStore,
		webUI:              cfg.WebUI,
		projectionState:    initialProjectionState,
		sseClients:         make(map[chan events.BusEvent]struct{}),
		corsAllowedOrigins: cfg.AllowedOrigins,
		jwksCache:          newJWKSCache(cfg.Auth.OIDC.CacheTTL),
		idempotencyStore:   NewIdempotencyStore(24 * time.Hour),
		jobStore:           NewJobStore(),
		jobExecutor:        jobExecutor{maxConcurrent: cfg.JobConcurrency, maxQueued: cfg.JobQueueCapacity},
		wsHub:              NewWSHub(),
		spawnAgents: func(ctx context.Context, opts robot.SpawnOptions) (*robot.SpawnOutput, error) {
			return robot.GetSpawn(ctx, opts, nil)
		},
		waitAgents: robot.GetWaitContext,
	}

	// Wire redaction into both consumers: redactionMiddleware (REST bodies) and
	// WSHub.broadcastEvent (event frames). Each gets its own deep copy so a
	// caller mutating the config it passed in — or either consumer's reference
	// typed Allowlist/ExtraPatterns/DisabledCategories — cannot reach the other
	// (the hazard bd-oekc2 fixed in fa8045ea/0b0f0874). Assigning wsHub's field
	// directly is safe without redactionMu: the hub's Run goroutine has not been
	// started and s has not been published to any other goroutine yet.
	if cfg.Redaction != nil {
		restCfg := *cfg.Redaction
		restCfg.Config = restCfg.Config.DeepCopy()
		s.redactionCfg = &restCfg

		hubCfg := *cfg.Redaction
		hubCfg.Config = hubCfg.Config.DeepCopy()
		s.wsHub.redactionCfg = &hubCfg
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
	syncKernelCatalogFromRouter(s.router)
	return s
}

// ProjectionState returns the current runtime projection readiness state
// ("warming_up", "ready", or "degraded").
func (s *Server) ProjectionState() string {
	s.mu.RLock()
	state := s.projectionState
	s.mu.RUnlock()
	if state == "" {
		return "ready"
	}
	return state
}

// SetProjectionState updates the runtime projection readiness state reported on
// health endpoints.
func (s *Server) SetProjectionState(state string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	s.mu.Lock()
	s.projectionState = state
	s.mu.Unlock()
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

	// Base middleware stack.
	//
	// chimw.RealIP is deliberately NOT used. It rewrites r.RemoteAddr from
	// True-Client-IP / X-Real-IP / the leftmost X-Forwarded-For of whatever
	// client sent them, with no trusted-proxy allowlist — chi itself has
	// deprecated it for that reason (GHSA-3fxj-6jh8-hvhx and siblings). The
	// value lands in every AuditRecord.RemoteAddr and in the auth-failure log,
	// so trusting it let any caller stamp a forged source address onto the
	// audit trail of a dangerous or approval-gated action with one header.
	// ntm terminates connections directly (local mode is loopback-only), so
	// the socket address is the truthful answer. If NTM ever needs to run
	// behind a reverse proxy, that needs a configured trusted-proxy list
	// (chi's ClientIPFromXFFTrustedProxies), never blanket header trust.
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
	// Capture before any middleware reads the body: redaction and replay may
	// both wait for input while configuration changes in another request.
	// Validation still follows each pipeline route's permission check.
	r.Use(s.pipelineProjectScopeMiddleware)
	// Jobs select their effective destination from the eventual JSON body.
	// Pin configuration and cwd before redaction can wait on that body.
	r.Use(s.jobProjectSelectionMiddleware)
	// Reject sensitive JSON input before any handler or replay can act on it;
	// redact response content. Inert unless Config.Redaction was supplied (see
	// that field): no redaction config means untouched request/response bytes.
	r.Use(s.redactionMiddleware)
	// Freeze mail and reservation requests' projects before replay lookup.
	// Validation errors follow each route's permission check.
	r.Use(s.mailProjectScopeMiddleware)
	// Classify the unchanged, admitted job request before cache lookup.
	// Admission later uses the same captured selection and project resolver.
	r.Use(s.jobHTTPReplayScopeMiddleware)
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
		r.With(s.RequirePermission(PermReadHealth)).Get("/kernel/commands", s.handleKernelCommands)
		r.With(s.RequirePermission(PermReadWebSocket)).Post("/auth/ws-ticket", s.handleIssueWSTicketV1)

		// Sessions - read endpoints
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions", s.handleSessionsV1)
		r.With(s.RequirePermission(PermReadSessions)).Get("/sessions/{id}", s.handleSessionV1)
		// GET /sessions/{id}/agents is served by the Agents API group below.
		// Do not register it here as well: chi gives "{id}" and "{sessionId}"
		// the same tree node, and mounting that group replaces every method
		// on it, so a route registered here never runs.
		r.With(s.RequirePermission(PermReadEvents)).Get("/sessions/{id}/events", s.handleSessionEventsRouteV1)

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

		// Metrics export endpoint
		r.With(s.RequirePermission(PermReadHealth)).Get("/metrics/export", s.handleMetricsExportV1)

		// Agents API - manage AI agents within sessions
		r.Route("/sessions/{sessionId}/agents", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadAgents)).Get("/", s.handleListAgentsV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/spawn", s.handleAgentSpawnV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/send", s.handleAgentSendV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/interrupt", s.handleAgentInterruptV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/wait", s.handleAgentWaitV1)
		})

		// Jobs API - read requires PermReadJobs, write requires PermReadJobs.
		// Idempotency replay is provided by the router-level mount above.
		r.Route("/jobs", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadJobs)).Get("/", s.handleListJobs)
			r.With(s.RequirePermission(PermWriteJobs)).Post("/", s.handleCreateJob)
			r.With(s.RequirePermission(PermReadJobs)).Get("/{id}", s.handleGetJob)
			r.With(s.RequirePermission(PermWriteJobs)).Delete("/{id}", s.handleCancelJob)
		})

		// Durable Request Identity Lifecycle API (--op-id / Idempotency-Key / wait_id)
		r.Route("/requests", func(r chi.Router) {
			r.With(s.RequirePermission(PermReadSessions)).Get("/{id}", s.handleGetRequestStatusV1)
			r.With(s.RequirePermission(PermReadSessions)).Post("/{id}/wait", s.handleWaitRequestV1)
			r.With(s.RequirePermission(PermWriteAgents)).Post("/{id}/cancel", s.handleCancelRequestV1)
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

		// Ensemble API - multi-agent reasoning ensembles
		s.registerEnsembleRoutes(r)

		// Worktrees API - isolated git worktrees for agents
		s.registerWorktreeRoutes(r)

		// Backpressure, Synthetic Swarm Lab & Host-Capacity Calibration API
		s.registerBackpressureRoutes(r)

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

	var reconcileWG sync.WaitGroup
	defer reconcileWG.Wait()

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
	// Keep output consumers alive until accepted workers have checkpointed.
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.drainJobWorkers(drainCtx); err != nil {
			slog.Warn("job workers still shutting down", "error", err)
		}
	}()

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

	var tlsConfig *tls.Config
	if s.auth.Mode == AuthModeMTLS {
		var tlsErr error
		tlsConfig, tlsErr = s.buildMTLSConfig()
		if tlsErr != nil {
			return tlsErr
		}
		s.server.TLSConfig = tlsConfig
	}

	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		s.mu.Lock()
		if s.port == 0 {
			s.port = tcpAddr.Port
		}
		s.server.Addr = tcpAddr.String()
		s.mu.Unlock()
	}

	scheme := "http"
	if s.auth.Mode == AuthModeMTLS {
		scheme = "https"
	}
	log.Printf("Starting NTM server on %s://%s (auth=%s)", scheme, s.server.Addr, s.auth.Mode)

	// Start serving accepted connections in goroutine
	errCh := make(chan error, 1)
	go func() {
		var serveErr error
		if s.auth.Mode == AuthModeMTLS {
			serveErr = s.server.ServeTLS(ln, s.auth.MTLS.CertFile, s.auth.MTLS.KeyFile)
		} else {
			serveErr = s.server.Serve(ln)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	// Reconcile state store with tmux reality asynchronously after binding TCP so
	// slow tmux sockets never delay /health readiness.
	if s.stateStore != nil {
		reconcileWG.Add(1)
		go func() {
			defer reconcileWG.Done()
			if ctx.Err() != nil {
				return
			}
			if result, err := s.stateStore.ReconcileSessions(); err != nil {
				slog.Warn("state-tmux reconciliation failed on startup", "error", err)
			} else if len(result.Terminated) > 0 {
				slog.Info("state-tmux reconciliation completed",
					"terminated", result.Terminated,
					"checked", result.Checked)
			}
		}()
	}

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("Shutting down server...")
		s.jobExecutor.closeAdmission()
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
	limits := s.jobExecutor.snapshot()
	if err := validateJobLimits(limits.MaxConcurrent, limits.QueueCapacity); err != nil {
		return err
	}
	cfg := Config{
		Host:           s.host,
		Port:           s.port,
		ProjectDir:     s.projectDirSnapshot(),
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
	projectDir, err := ResolveProjectDir(cfg.ProjectDir)
	if err != nil {
		return err
	}
	s.host = cfg.Host
	s.port = cfg.Port
	s.auth = cfg.Auth
	s.corsAllowedOrigins = cfg.AllowedOrigins
	s.mu.Lock()
	s.projectDir = projectDir
	s.mu.Unlock()
	return nil
}

// maxBytesMiddleware limits the size of request bodies to prevent DoS via OOM.
func (s *Server) maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Limit to 10MB default
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// requestIDMiddlewareFunc is the chi middleware version of requestIDMiddleware.
func (s *Server) requestIDMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.redactionConfigSnapshot()
		originalID := r.Header.Get(requestIDHeader)
		reqID := ""
		// Screen the complete supplied value before sanitizing/truncating it:
		// a truncated token can still disclose credential material in headers,
		// early refusal bodies, and every downstream log or audit record.
		if !scanRequestMetadata(originalID, cfg).Blocked {
			reqID = sanitizeRequestID(originalID)
			if scanRequestMetadata(reqID, cfg).Blocked {
				reqID = ""
			}
		}
		if reqID == "" {
			reqID = generateRequestID()
		}
		w.Header().Set(requestIDHeader, reqID)
		ctx := context.WithValue(r.Context(), requestRedactionConfigKey{}, cfg)
		ctx = context.WithValue(ctx, requestIDKey, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recovererMiddleware catches panics and returns a proper JSON error response.
func (s *Server) recovererMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := requestIDFromContext(r.Context())
				cfg := s.requestRedactionConfig(r)
				stack := string(debug.Stack())
				log.Printf("PANIC recovered: %s request_id=%s\n%s",
					redactRequestDiagnostic(fmt.Sprint(rec), cfg), reqID, redactRequestDiagnostic(stack, cfg))
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
		cfg := s.requestRedactionConfig(r)
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		reqID := requestIDFromContext(r.Context())
		log.Printf("%s %s %d %s request_id=%s", redactRequestDiagnostic(r.Method, cfg),
			redactRequestDiagnostic(r.URL.Path, cfg), ww.Status(), time.Since(start), reqID)
	})
}

// responseRecorder captures the response for idempotency caching.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
	// overflow is set when the response exceeds idempotencyMaxBodyBytes. The
	// full body is discarded; terminal results retain a small inspection receipt.
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
	case robot.ErrCodeSessionNotFound, robot.ErrCodePaneNotFound, robot.ErrCodeNotFound, robot.ErrCodeEnsembleNotFound:
		return http.StatusNotFound
	case robot.ErrCodeSynthesisNotReady:
		return http.StatusConflict
	case robot.ErrCodeOutputSchemaInvalid:
		return http.StatusUnprocessableEntity
	case robot.ErrCodePermissionDenied:
		return http.StatusForbidden
	case robot.ErrCodeDestructiveCommandBlocked, "SENSITIVE_DATA_BLOCKED":
		// A safety refusal of the caller's own message: not a server fault,
		// and retrying the same body cannot succeed.
		return http.StatusForbidden
	case robot.ErrCodeResourceBusy, robot.ErrCodeStaleWorkCoordination:
		return http.StatusConflict
	case robot.ErrCodeIdempotencyConflict, robot.ErrCodeOperationInProgress, robot.ErrCodeOperationOutcomeUnknown,
		"REQUEST_ALREADY_DISPATCHED", "REQUEST_NOT_CANCELLABLE":
		// The operation ID (Idempotency-Key) is bound to a different command,
		// another request holding it is still running, or a cancel request arrived
		// after dispatch started / after terminal completion: a client-side
		// conflict to reconcile via the receipt, never a server fault.
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
		"success":          true,
		"status":           "healthy",
		"projection_state": s.ProjectionState(),
		"time":             time.Now().UTC().Format(time.RFC3339),
	})
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
	if s.jobStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.drainJobWorkers(ctx); err != nil {
			slog.Warn("job workers still shutting down", "error", err)
		}
		cancel()
	}
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
