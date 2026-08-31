// Package robot provides machine-readable output for AI agents and automation.
// routing.go implements agent scoring and routing strategies for work distribution.
package robot

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// AgentMailConfig holds configuration for Agent Mail integration in routing.
type AgentMailConfig struct {
	Enabled             bool          `toml:"enabled"`              // Enable reservation-aware routing
	ReservationBonus    float64       `toml:"reservation_bonus"`    // Affinity bonus for reservation holders (default: 30)
	RespectReservations bool          `toml:"respect_reservations"` // If true, exclude non-holders; if false, just warn
	CacheTTL            time.Duration `toml:"cache_ttl"`            // Cache TTL for reservation queries (default: 30s)
	ProjectKey          string        `toml:"project_key"`          // Project key for Agent Mail queries
}

// DefaultAgentMailConfig returns sensible defaults for Agent Mail integration.
func DefaultAgentMailConfig() AgentMailConfig {
	return AgentMailConfig{
		Enabled:             false,
		ReservationBonus:    30.0,
		RespectReservations: false,
		CacheTTL:            30 * time.Second,
	}
}

// RoutingConfig holds configuration for agent routing and scoring.
type RoutingConfig struct {
	// Scoring weights (must sum to 1.0)
	ContextWeight float64 `toml:"context_weight"` // Default: 0.4
	StateWeight   float64 `toml:"state_weight"`   // Default: 0.4
	RecencyWeight float64 `toml:"recency_weight"` // Default: 0.2

	// Affinity settings
	AffinityEnabled bool    `toml:"affinity_enabled"` // Default: false
	AffinityBonus   float64 `toml:"affinity_bonus"`   // Default: 20

	// Exclusion thresholds
	ExcludeContextAbove  float64 `toml:"exclude_context_above"`   // Default: 85
	ExcludeIfGenerating  bool    `toml:"exclude_if_generating"`   // Default: true
	ExcludeIfRateLimited bool    `toml:"exclude_if_rate_limited"` // Default: true
	ExcludeIfErrorState  bool    `toml:"exclude_if_error"`        // Default: true

	// Agent Mail integration
	AgentMail AgentMailConfig `toml:"agent_mail"`
}

// DefaultRoutingConfig returns sensible default configuration.
func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		ContextWeight:        0.4,
		StateWeight:          0.4,
		RecencyWeight:        0.2,
		AffinityEnabled:      false,
		AffinityBonus:        20.0,
		ExcludeContextAbove:  85.0,
		ExcludeIfGenerating:  true,
		ExcludeIfRateLimited: true,
		ExcludeIfErrorState:  true,
		AgentMail:            DefaultAgentMailConfig(),
	}
}

// defaultRoutingContextLines aligns with --robot-context default (root.go uses 1000 when --lines is unset).
const defaultRoutingContextLines = 1000

// getContextUsageByPane returns a map of pane index -> context usage percent.
// Returns nil if context usage can't be computed.
func getContextUsageByPane(session string) map[int]float64 {
	output, err := GetContext(session, defaultRoutingContextLines)
	if err != nil || output == nil || !output.Success {
		return nil
	}

	usage := make(map[int]float64, len(output.Agents))
	for _, agent := range output.Agents {
		usage[agent.PaneIdx] = agent.UsagePercent
	}
	return usage
}

func contextUsageForPane(usage map[int]float64, paneIndex int) float64 {
	if usage == nil {
		return 0
	}
	if value, ok := usage[paneIndex]; ok {
		return value
	}
	return 0
}

// scoredAgentForRouting keeps every route surface aligned with the activity
// classifier's authoritative pane state, including live rate-limit evidence.
func scoredAgentForRouting(pane tmux.Pane, agentType string, activity *AgentActivity, contextUsage map[int]float64) ScoredAgent {
	return ScoredAgent{
		PaneID:       pane.ID,
		AgentType:    agentType,
		PaneIndex:    pane.Index,
		State:        activity.State,
		Confidence:   activity.Confidence,
		Velocity:     activity.Velocity,
		ContextUsage: contextUsageForPane(contextUsage, pane.Index),
		LastActivity: activity.LastOutput,
		HealthState:  deriveHealthState(activity.State),
		RateLimited:  activity.RateLimited,
	}
}

// ScoredAgent represents an agent with its computed routing score.
type ScoredAgent struct {
	// Identity
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"` // cc, cod, gmi
	PaneIndex int    `json:"pane_index"`

	// Current state
	State      AgentState `json:"state"`
	Confidence float64    `json:"confidence"`
	Velocity   float64    `json:"velocity"`

	// Context usage (from robot-context, 0-100)
	ContextUsage float64 `json:"context_usage"`

	// Last activity timestamp
	LastActivity time.Time `json:"last_activity"`

	// Health state
	HealthState HealthState `json:"health_state"`
	RateLimited bool        `json:"rate_limited"`

	// Scoring results
	Score         float64        `json:"score"`    // Final composite score (0-100)
	Excluded      bool           `json:"excluded"` // If true, agent should not receive work
	ExcludeReason string         `json:"exclude_reason,omitempty"`
	ScoreDetail   ScoreBreakdown `json:"score_detail,omitempty"`
}

// ScoreBreakdown shows how the score was calculated.
type ScoreBreakdown struct {
	ContextScore  float64 `json:"context_score"`  // 0-100
	StateScore    float64 `json:"state_score"`    // -100 to 100, normalized to 0-100
	RecencyScore  float64 `json:"recency_score"`  // 0-100
	AffinityBonus float64 `json:"affinity_bonus"` // 0-20 (if enabled)

	// Weighted contributions
	ContextContrib float64 `json:"context_contrib"`
	StateContrib   float64 `json:"state_contrib"`
	RecencyContrib float64 `json:"recency_contrib"`
}

// HealthState represents agent health status.
type HealthState string

const (
	HealthHealthy     HealthState = "healthy"
	HealthDegraded    HealthState = "degraded"
	HealthUnhealthy   HealthState = "unhealthy"
	HealthRateLimited HealthState = "rate_limited"
	// HealthBlocked: parked on an interactive gate screen (trust dialog,
	// login gate). Not dispatchable and not auto-restartable — a restart
	// cannot answer a dialog; a human keystroke can (bd-jf22c).
	HealthBlocked HealthState = "blocked"
)

// =============================================================================
// Reservation Cache
// =============================================================================

// ReservationCache caches file reservations from Agent Mail with TTL.
type ReservationCache struct {
	mu           sync.RWMutex
	reservations []agentmail.FileReservation // All active reservations
	pathToAgents map[string][]string         // path_pattern -> agent names
	lastFetch    time.Time
	ttl          time.Duration
	client       *agentmail.Client
	projectKey   string
}

// NewReservationCache creates a new reservation cache.
func NewReservationCache(client *agentmail.Client, projectKey string, ttl time.Duration) *ReservationCache {
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	return &ReservationCache{
		pathToAgents: make(map[string][]string),
		ttl:          ttl,
		client:       client,
		projectKey:   projectKey,
	}
}

// NeedsRefresh returns true if the cache has expired.
func (rc *ReservationCache) NeedsRefresh() bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return time.Since(rc.lastFetch) > rc.ttl
}

// Refresh fetches fresh reservations from Agent Mail.
func (rc *ReservationCache) Refresh(ctx context.Context) error {
	if rc.client == nil {
		return nil
	}

	// Fetch all reservations for the project
	reservations, err := rc.client.ListReservations(ctx, rc.projectKey, "", true)
	if err != nil {
		return err
	}

	// Build index
	pathToAgents := make(map[string][]string)
	for _, r := range reservations {
		// Skip expired reservations (server should filter, but double-check)
		if r.ReleasedTS != nil || time.Now().After(r.ExpiresTS.Time) {
			continue
		}
		pathToAgents[r.PathPattern] = append(pathToAgents[r.PathPattern], r.AgentName)
	}

	rc.mu.Lock()
	rc.reservations = reservations
	rc.pathToAgents = pathToAgents
	rc.lastFetch = time.Now()
	rc.mu.Unlock()

	return nil
}

// EnsureFresh refreshes the cache if needed.
func (rc *ReservationCache) EnsureFresh(ctx context.Context) error {
	if !rc.NeedsRefresh() {
		return nil
	}
	return rc.Refresh(ctx)
}

// GetHoldersForPath returns agent names that have reservations matching the given path.
func (rc *ReservationCache) GetHoldersForPath(path string) []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	var holders []string
	seen := make(map[string]bool)

	for pattern, agents := range rc.pathToAgents {
		if matchesPattern(path, pattern) { // matchesPattern takes (filePath, pattern)
			for _, agent := range agents {
				if !seen[agent] {
					seen[agent] = true
					holders = append(holders, agent)
				}
			}
		}
	}

	return holders
}

// GetAllReservations returns all cached reservations.
func (rc *ReservationCache) GetAllReservations() []agentmail.FileReservation {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	result := make([]agentmail.FileReservation, len(rc.reservations))
	copy(result, rc.reservations)
	return result
}

// GetReservedPathsForAgent returns all paths reserved by a specific agent.
func (rc *ReservationCache) GetReservedPathsForAgent(agentName string) []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	var paths []string
	for _, r := range rc.reservations {
		if r.AgentName == agentName && r.ReleasedTS == nil && time.Now().Before(r.ExpiresTS.Time) {
			paths = append(paths, r.PathPattern)
		}
	}
	return paths
}

// =============================================================================
// File Path Extraction
// =============================================================================

// filePathRegex matches common file path patterns in prompts.
// Use lookahead-like logic by not consuming the trailing boundary to avoid overlap.
var filePathRegex = regexp.MustCompile(`(?:^|[\s"'(])([a-zA-Z0-9_./\-]+\.[a-zA-Z0-9]+)`)

// ExtractFilePaths extracts potential file paths from a prompt.
// It looks for patterns like:
// - internal/robot/routing.go
// - src/components/Button.tsx
// - ./config.yaml
func ExtractFilePaths(prompt string) []string {
	matches := filePathRegex.FindAllStringSubmatch(prompt, -1)

	pathSet := make(map[string]bool)
	var paths []string

	for _, match := range matches {
		if len(match) > 1 {
			path := match[1]
			// Filter out common non-file patterns
			if isLikelyCodePath(path) && !pathSet[path] {
				pathSet[path] = true
				paths = append(paths, path)
			}
		}
	}

	return paths
}

// isLikelyCodePath returns true if the string looks like a code file path.
func isLikelyCodePath(s string) bool {
	// Must contain at least one slash or start with ./
	if !strings.Contains(s, "/") && !strings.HasPrefix(s, "./") {
		// Could be just a filename like "config.go"
		ext := filepath.Ext(s)
		if ext == "" {
			return false
		}
		// Common code file extensions
		validExts := map[string]bool{
			".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true,
			".jsx": true, ".rs": true, ".java": true, ".c": true, ".h": true,
			".cpp": true, ".hpp": true, ".yaml": true, ".yml": true, ".json": true,
			".toml": true, ".md": true, ".txt": true, ".sh": true, ".bash": true,
		}
		return validExts[ext]
	}

	// Must have a file extension (not a directory)
	ext := filepath.Ext(s)
	if ext == "" {
		return false
	}

	// Filter out URLs
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return false
	}

	// Filter out version numbers like 1.0.0
	if matched, _ := regexp.MatchString(`^\d+\.\d+`, s); matched {
		return false
	}

	return true
}

// =============================================================================
// Agent Scorer
// =============================================================================

// AgentScorer scores agents for routing decisions.
type AgentScorer struct {
	config           RoutingConfig
	monitor          *ActivityMonitor
	reservationCache *ReservationCache
	agentMapping     map[string]string // pane_id -> agent_mail_name (optional)
}

// NewAgentScorer creates a new agent scorer with the given configuration.
func NewAgentScorer(cfg RoutingConfig) *AgentScorer {
	return &AgentScorer{
		config:       cfg,
		monitor:      NewActivityMonitor(nil),
		agentMapping: make(map[string]string),
	}
}

// NewAgentScorerFromConfig creates a scorer using config file settings.
// Callers should pass a config loaded via config.Load() or config.Default(),
// which already include canonical routing defaults.
func NewAgentScorerFromConfig(cfg *config.Config) *AgentScorer {
	routingCfg := DefaultRoutingConfig()

	if cfg != nil {
		r := cfg.Routing
		if r != (config.RoutingConfig{}) {
			if r.ContextWeight > 0 {
				routingCfg.ContextWeight = r.ContextWeight
			}
			if r.StateWeight > 0 {
				routingCfg.StateWeight = r.StateWeight
			}
			if r.RecencyWeight > 0 {
				routingCfg.RecencyWeight = r.RecencyWeight
			}
			if r.AffinityBonus > 0 {
				routingCfg.AffinityBonus = r.AffinityBonus
			}
			if r.ExcludeContextAbove > 0 {
				routingCfg.ExcludeContextAbove = r.ExcludeContextAbove
			}
			routingCfg.AffinityEnabled = r.AffinityEnabled
			routingCfg.ExcludeIfGenerating = r.ExcludeIfGenerating
			routingCfg.ExcludeIfRateLimited = r.ExcludeIfRateLimited
			routingCfg.ExcludeIfErrorState = r.ExcludeIfErrorState
		}
	}

	return NewAgentScorer(routingCfg)
}

// SetReservationCache sets the reservation cache for Agent Mail integration.
func (s *AgentScorer) SetReservationCache(cache *ReservationCache) {
	s.reservationCache = cache
}

// resolveAffinityProjectKey resolves the Agent Mail project key for
// reservation-affinity wiring with the same SESSION-FIRST precedence the CLI
// uses (internal/cli resolveAgentMailProjectKey, bd-2rtl8): the session's
// persisted Agent Mail registry/agent-info project key wins, then the
// configured projects_base session directory, and only then the caller's
// working directory. An orchestrator invoking --robot-send from OUTSIDE the
// repo previously keyed ListReservations on its own cwd, silently degrading
// the bonus to 0 or matching another project's identically-named patterns.
func resolveAffinityProjectKey(cfg *config.Config, session string) string {
	cwdProject := util.ResolveProjectDir("")

	sessionProject := ""
	if cfg != nil && strings.TrimSpace(session) != "" {
		sessionProject = cfg.GetProjectDir(session)
	}

	savedProject := ""
	if strings.TrimSpace(session) != "" {
		if registry, err := agentmail.LoadBestSessionAgentRegistry(session, sessionProject, cwdProject); err == nil && registry != nil {
			savedProject = registry.ProjectKey
		}
		if info, err := agentmail.LoadBestSessionAgent(session, savedProject, sessionProject, cwdProject); err == nil && info != nil {
			savedProject = affinityProjectKeyPreference(savedProject, info.ProjectKey, "")
		}
	}

	return affinityProjectKeyPreference(savedProject, sessionProject, cwdProject)
}

// affinityProjectKeyPreference returns the first USABLE candidate in
// precedence order (session-saved, session-configured, cwd). Usability is
// scored the same way the CLI scores project dirs so a stand-in path like a
// nonexistent projects_base/<session> directory never beats a real checkout.
func affinityProjectKeyPreference(candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) == "" {
			continue
		}
		if util.ProjectDirScore(c) > 0 {
			return c
		}
	}
	return ""
}

// sharedReservationCaches holds one ReservationCache per project key for the
// lifetime of the process, so the configured CacheTTL actually amortizes
// Agent Mail round-trips: constructing a fresh cache per GetRoute /
// GetRouteRecommendation call left lastFetch at zero, making NeedsRefresh
// always true and every affinity-enabled send pay a blocking fetch (up to the
// 3s timeout against a wedged-but-accepting server) — the "30s TTL" was
// pretense (bd-2rtl8).
var (
	sharedReservationCachesMu sync.Mutex
	sharedReservationCaches   = make(map[string]*ReservationCache)
)

// sharedReservationCache returns the process-wide reservation cache for a
// project key, creating it on first use. An existing cache keeps its client
// and fetch history; only the TTL is updated if configuration changed.
func sharedReservationCache(client *agentmail.Client, projectKey string, ttl time.Duration) *ReservationCache {
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	sharedReservationCachesMu.Lock()
	defer sharedReservationCachesMu.Unlock()

	if rc, ok := sharedReservationCaches[projectKey]; ok {
		rc.mu.Lock()
		rc.ttl = ttl
		rc.mu.Unlock()
		return rc
	}
	rc := NewReservationCache(client, projectKey, ttl)
	sharedReservationCaches[projectKey] = rc
	return rc
}

// reservationAffinityRefreshTimeout bounds the best-effort Agent Mail
// reservation fetch at scorer setup. Affinity is enrichment, never a gate: an
// absent or wedged Agent Mail server must not stall routing or a send — the
// bonus simply degrades to 0 (bd-ws2-wire-or-delete-ykmcz.3).
const reservationAffinityRefreshTimeout = 3 * time.Second

// wireReservationAffinity populates the reservation cache at route/send time
// so the affinity bonus is real instead of permanently 0
// (bd-ws2-wire-or-delete-ykmcz.3, WIRE-MINIMAL).
//
// When [routing] affinity_enabled=true AND [agent_mail] enabled=true, it
// resolves the project key SESSION-FIRST with the same precedence the CLI
// uses (persisted session registry, then configured session dir, then cwd —
// bd-2rtl8), constructs an Agent Mail client from the same config/env
// precedence the CLI uses (env AGENT_MAIL_URL/AGENT_MAIL_TOKEN or
// MCP_AGENT_MAIL_TOKEN override
// config), attaches the process-shared TTL ReservationCache with one
// best-effort TTL-bounded refresh, and loads the persisted pane→agent-name
// mapping from the session agent registry. Everything is best-effort: any
// failure leaves the scorer exactly as it was before wiring (bonus
// contributes 0), and affinity stays a SCORING BONUS under existing
// strategies — `--route=affinity` remains an invalid strategy.
func (s *AgentScorer) wireReservationAffinity(cfg *config.Config, session string) {
	if cfg == nil || !s.config.AffinityEnabled || !cfg.AgentMail.Enabled {
		return
	}
	projectKey := resolveAffinityProjectKey(cfg, session)
	if projectKey == "" {
		slog.Warn("[robot.route] affinity: cannot resolve project key; bonus degrades to 0", "session", session)
		return
	}

	opts := []agentmail.Option{agentmail.WithProjectKey(projectKey)}
	// Environment variables override config; agentmail.NewClient reads env
	// before applying options (same precedence as internal/cli's client).
	if cfg.AgentMail.URL != "" && os.Getenv("AGENT_MAIL_URL") == "" {
		opts = append(opts, agentmail.WithBaseURL(cfg.AgentMail.URL))
	}
	if cfg.AgentMail.Token != "" && os.Getenv("AGENT_MAIL_TOKEN") == "" && os.Getenv("MCP_AGENT_MAIL_TOKEN") == "" {
		opts = append(opts, agentmail.WithToken(cfg.AgentMail.Token))
	}
	client := agentmail.NewClient(opts...)
	agentmail.HydrateClientTokensForProject(client, projectKey)

	s.config.AgentMail.Enabled = true
	s.SetReservationCache(sharedReservationCache(client, projectKey, s.config.AgentMail.CacheTTL))
	if loaded := s.LoadAgentMappingFromRegistry(session, projectKey); loaded == 0 {
		slog.Debug("[robot.route] affinity: no persisted pane→agent mapping for session", "session", session)
	}

	ctx, cancel := context.WithTimeout(context.Background(), reservationAffinityRefreshTimeout)
	defer cancel()
	if err := s.reservationCache.EnsureFresh(ctx); err != nil {
		slog.Warn("[robot.route] affinity: reservation cache refresh failed; bonus degrades to 0",
			"session", session, "error", err)
	}
}

// SetAgentMapping sets the mapping from pane IDs to Agent Mail agent names.
func (s *AgentScorer) SetAgentMapping(mapping map[string]string) {
	s.agentMapping = mapping
}

// MapPaneToAgent adds a mapping from pane ID to Agent Mail agent name.
func (s *AgentScorer) MapPaneToAgent(paneID, agentName string) {
	if s.agentMapping == nil {
		s.agentMapping = make(map[string]string)
	}
	s.agentMapping[paneID] = agentName
}

// GetAgentNameForPane returns the Agent Mail agent name for a pane, if mapped.
func (s *AgentScorer) GetAgentNameForPane(paneID string) (string, bool) {
	if s.agentMapping == nil {
		return "", false
	}
	name, ok := s.agentMapping[paneID]
	return name, ok
}

// LoadAgentMappingFromRegistry loads the pane->agent name mapping from the
// persisted SessionAgentRegistry. This enables session restart recovery of
// agent identities. Returns the number of agents loaded, or 0 if no registry exists.
func (s *AgentScorer) LoadAgentMappingFromRegistry(sessionName, projectKey string) int {
	registry, err := agentmail.LoadSessionAgentRegistry(sessionName, projectKey)
	if err != nil || registry == nil {
		return 0
	}

	// Merge the registry mappings into the scorer's agent mapping
	// Both by pane title and by pane ID
	if registry.Agents != nil {
		for paneTitle, agentName := range registry.Agents {
			s.MapPaneToAgent(paneTitle, agentName)
		}
	}
	if registry.PaneIDMap != nil {
		for paneID, agentName := range registry.PaneIDMap {
			s.MapPaneToAgent(paneID, agentName)
		}
	}

	return registry.Count()
}

// CheckReservationWarning checks if any files in the prompt have reservations
// and returns a warning if the selected agent doesn't hold them.
func (s *AgentScorer) CheckReservationWarning(prompt string, selectedPaneID string) *ReservationWarning {
	if !s.config.AgentMail.Enabled || s.reservationCache == nil {
		return nil
	}

	// Extract file paths from prompt
	filePaths := ExtractFilePaths(prompt)
	if len(filePaths) == 0 {
		return nil
	}

	// Check which paths have reservations
	var reservedPaths []string
	holdersSet := make(map[string]bool)

	for _, path := range filePaths {
		holders := s.reservationCache.GetHoldersForPath(path)
		if len(holders) > 0 {
			reservedPaths = append(reservedPaths, path)
			for _, h := range holders {
				holdersSet[h] = true
			}
		}
	}

	if len(reservedPaths) == 0 {
		return nil
	}

	// Get all holder names
	var holders []string
	for h := range holdersSet {
		holders = append(holders, h)
	}

	// Check if selected agent holds any reservations
	selectedAgentName, hasMapping := s.GetAgentNameForPane(selectedPaneID)
	selectedHas := false
	if hasMapping {
		selectedHas = holdersSet[selectedAgentName]
	}

	// Build warning message
	var msg string
	if selectedHas {
		msg = "Selected agent holds reservations for some mentioned files"
	} else if hasMapping {
		msg = "Files mentioned in prompt are reserved by other agents"
	} else {
		msg = "Files mentioned in prompt have active reservations"
	}

	return &ReservationWarning{
		Message:     msg,
		Paths:       reservedPaths,
		Holders:     holders,
		SelectedHas: selectedHas,
	}
}

// ScoreAgentsWithContext calculates scores and refreshes reservation cache if needed.
func (s *AgentScorer) ScoreAgentsWithContext(ctx context.Context, session string, prompt string) ([]ScoredAgent, error) {
	// Refresh reservation cache if needed
	if s.reservationCache != nil {
		if err := s.reservationCache.EnsureFresh(ctx); err != nil {
			slog.Warn("reservation cache refresh failed", "error", err, "session", session)
		}
	}

	return s.ScoreAgents(session, prompt)
}

// ScoreAgents calculates scores for all agents in a session.
func (s *AgentScorer) ScoreAgents(session string, prompt string) ([]ScoredAgent, error) {
	// Get all panes
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, err
	}

	contextUsage := getContextUsageByPane(session)

	var scored []ScoredAgent

	for _, pane := range panes {
		agentType := routePaneAgentType(pane)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}

		// Get activity state
		classifier := s.monitor.GetOrCreate(pane.ID)
		classifier.SetAgentType(agentType)
		activity, err := classifier.Classify()
		if err != nil {
			// If we can't classify, skip this agent
			continue
		}

		// Build scored agent from the same classification used by route APIs.
		agent := scoredAgentForRouting(pane, agentType, activity, contextUsage)

		// Calculate score components
		agent.ScoreDetail = s.calculateScoreComponents(&agent, prompt)

		// Check exclusion rules first
		excluded, reason := s.checkExclusion(&agent)
		if excluded {
			agent.Excluded = true
			agent.ExcludeReason = reason
			agent.Score = 0
		} else {
			// Calculate final score
			agent.Score = s.calculateFinalScore(&agent)
		}

		scored = append(scored, agent)
	}

	return scored, nil
}

// calculateScoreComponents computes individual score components.
func (s *AgentScorer) calculateScoreComponents(agent *ScoredAgent, prompt string) ScoreBreakdown {
	breakdown := ScoreBreakdown{}

	// 1. Context Score (0-100)
	// Higher is better - agents with more room for context
	breakdown.ContextScore = 100 - agent.ContextUsage
	if breakdown.ContextScore < 0 {
		breakdown.ContextScore = 0
	}

	// 2. State Score (-100 to 100, then normalized to 0-100)
	rawStateScore := s.stateToScore(agent.State)
	// Normalize -100 to 100 range to 0 to 100
	breakdown.StateScore = (rawStateScore + 100) / 2

	// 3. Recency Score (0-100)
	breakdown.RecencyScore = s.recencyToScore(agent.LastActivity)

	// 4. Affinity Bonus (0-20)
	if s.config.AffinityEnabled && prompt != "" {
		breakdown.AffinityBonus = s.calculateAffinity(agent, prompt)
	}

	// Calculate weighted contributions
	breakdown.ContextContrib = breakdown.ContextScore * s.config.ContextWeight
	breakdown.StateContrib = breakdown.StateScore * s.config.StateWeight
	breakdown.RecencyContrib = breakdown.RecencyScore * s.config.RecencyWeight

	return breakdown
}

// stateToScore converts agent state to a score (-100 to 100).
func (s *AgentScorer) stateToScore(state AgentState) float64 {
	switch state {
	case StateWaiting:
		return 100 // Ready for work
	case StateThinking:
		return 50 // May become available soon
	case StateGenerating:
		return 0 // Currently busy
	case StateStalled:
		return -50 // May need attention
	case StateError:
		return -100 // Excluded
	case StateUnknown:
		return 25 // Uncertain, slightly prefer known states
	default:
		return 0
	}
}

// recencyToScore converts last activity time to a score (0-100).
func (s *AgentScorer) recencyToScore(lastActivity time.Time) float64 {
	if lastActivity.IsZero() {
		return 50 // Unknown, neutral score
	}

	age := time.Since(lastActivity)

	// Recent activity (< 1 min): Lower score - agent is "hot" but busy
	if age < time.Minute {
		return 20
	}

	// Medium (1-5 min): Moderate score
	if age < 5*time.Minute {
		return 50
	}

	// Idle (> 5 min): Higher score - ready for work
	if age < 30*time.Minute {
		return 80
	}

	// Very idle (> 30 min): Might be stale, but still available
	return 70
}

// calculateAffinity calculates affinity bonus based on prompt matching.
func (s *AgentScorer) calculateAffinity(agent *ScoredAgent, prompt string) float64 {
	// If Agent Mail integration is not enabled or no cache, return 0
	if !s.config.AgentMail.Enabled || s.reservationCache == nil {
		return 0
	}

	// Get the Agent Mail name for this pane
	agentName, ok := s.GetAgentNameForPane(agent.PaneID)
	if !ok {
		return 0
	}

	// Extract file paths from the prompt
	filePaths := ExtractFilePaths(prompt)
	if len(filePaths) == 0 {
		return 0
	}

	// Check if this agent has reservations for any of the extracted paths
	reservedPaths := s.reservationCache.GetReservedPathsForAgent(agentName)
	if len(reservedPaths) == 0 {
		return 0
	}

	// Count matches
	matches := 0
	for _, filePath := range filePaths {
		for _, reserved := range reservedPaths {
			if matchesPattern(filePath, reserved) { // matchesPattern takes (filePath, pattern)
				matches++
				break // Count each file path only once
			}
		}
	}

	if matches == 0 {
		return 0
	}

	// Scale bonus based on match ratio (more matches = higher bonus, capped at config max).
	matchRatio := float64(matches) / float64(len(filePaths))
	bonus := s.config.AffinityBonus * matchRatio

	return bonus
}

// checkExclusion checks if an agent should be excluded from routing.
func (s *AgentScorer) checkExclusion(agent *ScoredAgent) (bool, string) {
	if s.config.ExcludeIfErrorState && agent.State == StateError {
		return true, "agent in ERROR state"
	}

	// Rate limited
	if s.config.ExcludeIfRateLimited && agent.RateLimited {
		return true, "agent is rate limited"
	}

	if s.config.ExcludeIfErrorState && agent.HealthState == HealthUnhealthy {
		return true, "agent is unhealthy"
	}

	// High context usage
	if agent.ContextUsage > s.config.ExcludeContextAbove {
		return true, "context usage above threshold"
	}

	// Currently generating
	if s.config.ExcludeIfGenerating && agent.State == StateGenerating {
		return true, "agent is currently generating"
	}

	return false, ""
}

// calculateFinalScore computes the final routing score.
func (s *AgentScorer) calculateFinalScore(agent *ScoredAgent) float64 {
	d := agent.ScoreDetail

	// Sum weighted components
	score := d.ContextContrib + d.StateContrib + d.RecencyContrib

	// Add affinity bonus
	score += d.AffinityBonus

	// A NaN or Inf must never escape: json.Encode rejects both, which would
	// replace the entire robot response with an empty stdout and a nonzero exit.
	// Neither clamp below catches NaN, since every comparison against it is
	// false, so test explicitly. An unscoreable agent sorts last.
	if math.IsNaN(score) || math.IsInf(score, -1) {
		return 0
	}
	if math.IsInf(score, 1) {
		return 100
	}

	// Clamp to 0-100 range
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}

	return math.Round(score*100) / 100 // Round to 2 decimal places
}

// deriveHealthState derives health state from activity state.
func deriveHealthState(state AgentState) HealthState {
	switch state {
	case StateWaiting, StateThinking, StateGenerating:
		return HealthHealthy
	case StateStalled:
		return HealthDegraded
	case StateError:
		return HealthUnhealthy
	default:
		return HealthHealthy
	}
}

// GetBestAgent returns the agent with the highest score.
func (s *AgentScorer) GetBestAgent(scored []ScoredAgent) *ScoredAgent {
	var best *ScoredAgent

	for i := range scored {
		if scored[i].Excluded {
			continue
		}
		if best == nil || scored[i].Score > best.Score {
			best = &scored[i]
		}
	}

	return best
}

// GetAvailableAgents returns all non-excluded agents sorted by score.
func (s *AgentScorer) GetAvailableAgents(scored []ScoredAgent) []ScoredAgent {
	var available []ScoredAgent

	for _, agent := range scored {
		if !agent.Excluded {
			available = append(available, agent)
		}
	}

	// Sort by score descending
	for i := 0; i < len(available); i++ {
		for j := i + 1; j < len(available); j++ {
			if available[j].Score > available[i].Score {
				available[i], available[j] = available[j], available[i]
			}
		}
	}

	return available
}

// ExcludePanes excludes specific pane indices from the list.
func ExcludePanes(agents []ScoredAgent, excludeIndices []int) []ScoredAgent {
	if len(excludeIndices) == 0 {
		return agents
	}

	excludeSet := make(map[int]bool)
	for _, idx := range excludeIndices {
		excludeSet[idx] = true
	}

	var filtered []ScoredAgent
	for _, agent := range agents {
		if !excludeSet[agent.PaneIndex] {
			filtered = append(filtered, agent)
		}
	}
	return filtered
}

// =============================================================================
// Routing Strategies
// =============================================================================

// StrategyName represents a routing strategy identifier.
type StrategyName string

const (
	// StrategyLeastLoaded selects agent with highest score (default).
	StrategyLeastLoaded StrategyName = "least-loaded"

	// StrategyFirstAvailable selects first agent in WAITING state.
	StrategyFirstAvailable StrategyName = "first-available"

	// StrategyRoundRobin rotates through agents in order.
	StrategyRoundRobin StrategyName = "round-robin"

	// StrategyRoundRobinAvailable rotates but skips busy/unhealthy agents.
	StrategyRoundRobinAvailable StrategyName = "round-robin-available"

	// StrategyRandom randomly selects among available agents.
	StrategyRandom StrategyName = "random"

	// StrategySticky prefers same agent for related tasks.
	StrategySticky StrategyName = "sticky"

	// StrategyExplicit uses user-specified pane directly.
	StrategyExplicit StrategyName = "explicit"
)

// RoutingContext provides context for routing decisions.
type RoutingContext struct {
	Prompt       string // For affinity matching
	LastAgent    string // For sticky routing (pane ID of last used agent)
	ExcludePanes []int  // Pane indices to exclude
	ExplicitPane int    // For explicit routing (-1 = not set)
	// RotationCursor is the persisted round-robin cursor from session routing
	// state (bd-ws1-truth-safety-l5ddi.10): the index the PREVIOUS send's
	// selected pane held in its candidate list. It only counts as routing
	// history when HasRotationCursor is true (guarding the zero-value trap:
	// index 0 is a valid cursor). It anchors the rotation when LastAgent's
	// pane no longer resolves: the vanished pane's successor now sits AT the
	// cursor position, so the rotation starts there without advancing
	// (bd-88um4).
	RotationCursor    int
	HasRotationCursor bool
}

// RoutingStrategy defines the interface for routing strategies.
type RoutingStrategy interface {
	// Name returns the strategy identifier.
	Name() StrategyName

	// Select chooses an agent from the candidates.
	// Returns nil if no suitable agent found.
	Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent
}

// ReservationWarning contains information about file reservation conflicts.
type ReservationWarning struct {
	Message     string   `json:"message"`               // Human-readable warning message
	Paths       []string `json:"paths"`                 // File paths that are reserved
	Holders     []string `json:"holders"`               // Agent names that hold reservations
	SelectedHas bool     `json:"selected_has_reserved"` // True if selected agent holds reservations
}

// RoutingResult represents the outcome of a routing decision.
type RoutingResult struct {
	Selected           *ScoredAgent        `json:"selected,omitempty"`
	Strategy           StrategyName        `json:"strategy"`
	Candidates         []ScoredAgent       `json:"candidates"`
	Excluded           []ScoredAgent       `json:"excluded,omitempty"`
	FallbackUsed       bool                `json:"fallback_used"`
	Reason             string              `json:"reason,omitempty"`
	ReservationWarning *ReservationWarning `json:"reservation_warning,omitempty"` // Warning if files are reserved by other agents
}

// =============================================================================
// Strategy Implementations
// =============================================================================

// LeastLoadedStrategy selects the agent with the highest score.
type LeastLoadedStrategy struct{}

func (s *LeastLoadedStrategy) Name() StrategyName {
	return StrategyLeastLoaded
}

func (s *LeastLoadedStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	var best *ScoredAgent
	for i := range agents {
		if agents[i].Excluded {
			continue
		}
		if best == nil || agents[i].Score > best.Score {
			best = &agents[i]
		}
	}
	return best
}

// FirstAvailableStrategy selects the first agent in WAITING state.
type FirstAvailableStrategy struct{}

func (s *FirstAvailableStrategy) Name() StrategyName {
	return StrategyFirstAvailable
}

func (s *FirstAvailableStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	for i := range agents {
		if agents[i].Excluded {
			continue
		}
		if agents[i].State == StateWaiting {
			return &agents[i]
		}
	}
	return nil
}

// rotationAnchorIndex resolves the index the next rotation step starts from,
// and reports whether the rotation should ADVANCE one step past that anchor
// (true = the anchor itself was already routed to; false = start AT the
// anchor).
//
// Precedence matters. An explicit LastAgent (pane ID) wins because it survives
// process boundaries AND is immune to positional drift: panes inserted or
// removed elsewhere in the candidate list cannot shift a pane-ID anchor.
//
// The persisted rotation cursor comes next, and it is reached only when the
// previously routed pane no longer resolves in the current candidate list —
// i.e. the pane vanished. The cursor was that pane's index at selection time,
// so after the removal the list shrank at/before that index and the VANISHED
// PANE'S SUCCESSOR now sits AT the cursor position. Advancing past it would
// skip the successor (A,B,C,D with cursor on B; kill B -> D, starving C —
// bd-88um4), so the cursor anchors WITHOUT advancing. A cursor past the end
// (the vanished pane was last) wraps to index 0, which is likewise the
// successor.
//
// The in-process cursor comes next: once this Router has actually routed, the
// cursor IS routing history. Only with none of the above does the rotation
// fall back to observed pane activity — and that fallback is not routing
// history at all, so it starts AT the idlest agent.
//
// The old order preferred activity over the cursor, and used "most recently
// ACTIVE" as a stand-in for "most recently ROUTED". Those are different facts:
// with agents A, B, C where A runs a chatty build that keeps refreshing its
// pane, A anchored every call, so every invocation returned B and C was never
// selected — the opposite of what RoundRobinStrategy documents (bd-sgs87).
func rotationAnchorIndex(agents []ScoredAgent, ctx RoutingContext, cursor int, routed bool) (int, bool) {
	if ctx.LastAgent != "" {
		for i := range agents {
			if agents[i].PaneID == ctx.LastAgent {
				return i, true
			}
		}
	}
	// Persisted rotation cursor from session routing state
	// (bd-ws1-truth-safety-l5ddi.10): the previously routed pane vanished, so
	// its successor sits AT the cursor position — anchor without advancing
	// (bd-88um4).
	if ctx.HasRotationCursor && ctx.RotationCursor >= 0 {
		idx := ctx.RotationCursor
		if idx >= len(agents) {
			idx = 0
		}
		return idx, false
	}
	if routed && cursor >= 0 && cursor < len(agents) {
		return cursor, true
	}
	return leastRecentlyActiveIndex(agents), false
}

// leastRecentlyActiveIndex picks the agent that has been quiet longest.
//
// It is the stateless fallback when nothing tells us who was routed to last.
// Selecting the idlest agent cannot starve anyone: routing work to an agent
// makes it active, which moves it to the back of the queue, so successive
// invocations spread across the fleet. Anchoring on the BUSIEST agent instead
// pinned the rotation to whichever pane was noisiest.
//
// An agent with no recorded activity has been quiet the longest of all, so it
// is selected first.
func leastRecentlyActiveIndex(agents []ScoredAgent) int {
	idlest := 0
	for i := range agents {
		if agents[i].LastActivity.IsZero() {
			return i
		}
		if agents[idlest].LastActivity.IsZero() {
			continue
		}
		if agents[i].LastActivity.Before(agents[idlest].LastActivity) {
			idlest = i
		}
	}
	return idlest
}

// RoundRobinStrategy rotates through agents in order.
type RoundRobinStrategy struct {
	lastIndex int
	// routed distinguishes "lastIndex is 0 because we have never routed" from
	// "lastIndex is 0 because index 0 was the last selection". Without it the
	// cursor cannot be trusted as routing history on the first call.
	routed bool
}

func (s *RoundRobinStrategy) Name() StrategyName {
	return StrategyRoundRobin
}

func (s *RoundRobinStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	if len(agents) == 0 {
		return nil
	}

	// With real routing history (an explicit --last-agent, or this Router's own
	// cursor) advance one step from it. Without any, the CLI builds a fresh
	// Router per invocation and there is nothing to advance FROM, so select the
	// idlest agent directly rather than stepping off an anchor derived from
	// pane chatter (bd-sgs87, bd-fresh-eyes-audit .8).
	anchor, anchored := rotationAnchorIndex(agents, ctx, s.lastIndex, s.routed)
	startIdx := anchor
	if anchored {
		startIdx = (anchor + 1) % len(agents)
	}

	// Round-robin ignores exclusion - use all agents
	selected := &agents[startIdx]
	s.lastIndex = startIdx
	s.routed = true
	return selected
}

// RoundRobinAvailableStrategy rotates but skips busy/unhealthy agents.
type RoundRobinAvailableStrategy struct {
	lastIndex int
	routed    bool
}

func (s *RoundRobinAvailableStrategy) Name() StrategyName {
	return StrategyRoundRobinAvailable
}

func (s *RoundRobinAvailableStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	if len(agents) == 0 {
		return nil
	}

	// Try to find next available agent starting from the anchor. With real
	// routing history we step past it; without any we start AT the idlest
	// agent, so a chatty pane cannot pin the rotation (bd-sgs87).
	anchor, anchored := rotationAnchorIndex(agents, ctx, s.lastIndex, s.routed)
	offset := 0
	if anchored {
		offset = 1
	}
	for i := 0; i < len(agents); i++ {
		idx := (anchor + offset + i) % len(agents)
		if !agents[idx].Excluded {
			s.lastIndex = idx
			s.routed = true
			return &agents[idx]
		}
	}

	return nil
}

// RandomStrategy randomly selects among available agents.
type RandomStrategy struct {
	randFunc func(int) int // Injected for testing
}

func (s *RandomStrategy) Name() StrategyName {
	return StrategyRandom
}

func (s *RandomStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	// Collect available agents
	var available []*ScoredAgent
	for i := range agents {
		if !agents[i].Excluded {
			available = append(available, &agents[i])
		}
	}

	if len(available) == 0 {
		return nil
	}

	// Use the injected random function (tests inject a seeded source), or
	// real randomness. The old nil-randFunc fallback was len/2 — a
	// deterministic "random" strategy that always picked the middle agent
	// (bd-ws1-truth-safety-l5ddi.10).
	idx := 0
	if s.randFunc != nil {
		idx = s.randFunc(len(available))
	} else {
		idx = rand.Intn(len(available))
	}

	return available[idx]
}

// StickyStrategy prefers the same agent for related tasks.
type StickyStrategy struct {
	fallback RoutingStrategy
}

func NewStickyStrategy() *StickyStrategy {
	return &StickyStrategy{
		fallback: &LeastLoadedStrategy{},
	}
}

func (s *StickyStrategy) Name() StrategyName {
	return StrategySticky
}

func (s *StickyStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	// If we have a last agent, prefer it if still available
	if ctx.LastAgent != "" {
		for i := range agents {
			if agents[i].PaneID == ctx.LastAgent && !agents[i].Excluded {
				return &agents[i]
			}
		}
	}

	// Fall back to least-loaded
	return s.fallback.Select(agents, ctx)
}

// ExplicitStrategy uses user-specified pane directly.
type ExplicitStrategy struct{}

func (s *ExplicitStrategy) Name() StrategyName {
	return StrategyExplicit
}

func (s *ExplicitStrategy) Select(agents []ScoredAgent, ctx RoutingContext) *ScoredAgent {
	if ctx.ExplicitPane < 0 {
		return nil
	}

	for i := range agents {
		if agents[i].PaneIndex == ctx.ExplicitPane {
			return &agents[i]
		}
	}

	return nil
}

// =============================================================================
// Router
// =============================================================================

// Router applies routing strategies to select agents.
type Router struct {
	strategies    map[StrategyName]RoutingStrategy
	defaultStrat  RoutingStrategy
	fallbackOrder []RoutingStrategy
}

// NewRouter creates a new router with all strategies registered.
func NewRouter() *Router {
	r := &Router{
		strategies:   make(map[StrategyName]RoutingStrategy),
		defaultStrat: &LeastLoadedStrategy{},
	}

	// Register all strategies
	r.RegisterStrategy(&LeastLoadedStrategy{})
	r.RegisterStrategy(&FirstAvailableStrategy{})
	r.RegisterStrategy(&RoundRobinStrategy{})
	r.RegisterStrategy(&RoundRobinAvailableStrategy{})
	r.RegisterStrategy(&RandomStrategy{})
	r.RegisterStrategy(NewStickyStrategy())
	r.RegisterStrategy(&ExplicitStrategy{})

	// Default fallback order
	r.fallbackOrder = []RoutingStrategy{
		&LeastLoadedStrategy{},    // Try best score first
		&FirstAvailableStrategy{}, // Then any waiting agent
	}

	return r
}

// RegisterStrategy registers a routing strategy.
func (r *Router) RegisterStrategy(s RoutingStrategy) {
	r.strategies[s.Name()] = s
}

// GetStrategy returns a strategy by name, or the default if not found.
func (r *Router) GetStrategy(name StrategyName) RoutingStrategy {
	if s, ok := r.strategies[name]; ok {
		return s
	}
	return r.defaultStrat
}

// Route selects an agent using the specified strategy.
func (r *Router) Route(agents []ScoredAgent, strategy StrategyName, ctx RoutingContext) RoutingResult {
	result := RoutingResult{
		Strategy:   strategy,
		Candidates: filterExcluded(agents, false),
		Excluded:   filterExcluded(agents, true),
	}

	// Apply exclusion from context
	if len(ctx.ExcludePanes) > 0 {
		agents = ExcludePanes(agents, ctx.ExcludePanes)
	}

	// Get the strategy
	strat := r.GetStrategy(strategy)

	// Try primary strategy. A strategy may return an excluded agent — plain
	// round-robin documents that it "ignores exclusion" — but routing must
	// never RECOMMEND a pane it simultaneously reports as excluded: callers
	// act on the recommendation and would send work into a dead or
	// rate-limited pane (bd-fresh-eyes-audit .8). Treat that as no selection
	// and fall through to the fallback chain.
	selected := strat.Select(agents, ctx)
	if selected != nil && selected.Excluded {
		selected = nil
	}
	if selected != nil {
		result.Selected = selected
		result.Reason = "primary strategy succeeded"
		return result
	}

	// Try fallback chain
	for _, fb := range r.fallbackOrder {
		if fb.Name() == strategy {
			continue // Skip if same as primary
		}
		selected = fb.Select(agents, ctx)
		if selected != nil && selected.Excluded {
			continue
		}
		if selected != nil {
			result.Selected = selected
			result.FallbackUsed = true
			result.Reason = "fallback to " + string(fb.Name())
			return result
		}
	}

	result.Reason = "no suitable agent found"
	return result
}

// RouteWithRelaxation tries routing with progressively relaxed constraints.
func (r *Router) RouteWithRelaxation(agents []ScoredAgent, strategy StrategyName, ctx RoutingContext) RoutingResult {
	// First try with normal constraints
	result := r.Route(agents, strategy, ctx)
	if result.Selected != nil {
		return result
	}

	// Relax constraint: include THINKING agents (which are close to WAITING)
	// These agents might have been excluded but are nearly ready for work.
	relaxedAgents := make([]ScoredAgent, len(agents))
	copy(relaxedAgents, agents)
	for i := range relaxedAgents {
		if relaxedAgents[i].State == StateThinking && relaxedAgents[i].Excluded {
			relaxedAgents[i].Excluded = false
			relaxedAgents[i].ExcludeReason = ""
		}
	}

	result = r.Route(relaxedAgents, strategy, ctx)
	if result.Selected != nil {
		result.Reason = "relaxed constraints (included THINKING)"
		return result
	}

	return result
}

// filterExcluded returns agents filtered by exclusion status.
func filterExcluded(agents []ScoredAgent, excluded bool) []ScoredAgent {
	var result []ScoredAgent
	for _, a := range agents {
		if a.Excluded == excluded {
			result = append(result, a)
		}
	}
	return result
}

// GetStrategyNames returns all available strategy names.
func GetStrategyNames() []StrategyName {
	return []StrategyName{
		StrategyLeastLoaded,
		StrategyFirstAvailable,
		StrategyRoundRobin,
		StrategyRoundRobinAvailable,
		StrategyRandom,
		StrategySticky,
		StrategyExplicit,
	}
}

// IsValidStrategy checks if a strategy name is valid.
func IsValidStrategy(name StrategyName) bool {
	switch name {
	case StrategyLeastLoaded, StrategyFirstAvailable, StrategyRoundRobin,
		StrategyRoundRobinAvailable, StrategyRandom, StrategySticky, StrategyExplicit:
		return true
	default:
		return false
	}
}
