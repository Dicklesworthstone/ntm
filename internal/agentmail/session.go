package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// SessionAgentInfo tracks the registered agent identity for a session.
type SessionAgentInfo struct {
	AgentName  string `json:"agent_name"`
	ProjectKey string `json:"project_key"`
	// RegistrationToken is the server-issued token for this identity.
	// Persisting it lets a later ntm process re-claim the same agent name
	// instead of being rejected by a server that requires token auth for
	// re-registration.
	RegistrationToken string    `json:"registration_token,omitempty"`
	RegisteredAt      time.Time `json:"registered_at"`
	LastActiveAt      time.Time `json:"last_active_at"`
}

// sanitizeRegex is precompiled for performance (used by sanitizeSessionName)
var sanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// sanitizeSessionName converts a session name to a valid agent name component.
// Replaces non-alphanumeric chars with underscores, lowercases.
func sanitizeSessionName(name string) string {
	sanitized := sanitizeRegex.ReplaceAllString(name, "_")
	sanitized = strings.Trim(sanitized, "_")
	sanitized = strings.ToLower(sanitized)
	if sanitized == "" {
		// Fallback to hex encoding if sanitization stripped everything
		return fmt.Sprintf("hex_%x", []byte(name))
	}
	return sanitized
}

// getSessionsBaseDir returns the base directory for storing session data.
func getSessionsBaseDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
			if home == "" {
				home = os.TempDir()
			}
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "ntm", "sessions")
}

func validateSessionStorageName(sessionName string) error {
	if err := tmux.ValidateSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid session name: %w", err)
	}
	return nil
}

func primaryProjectSlug(projectKey string) string {
	slug := ProjectSlugFromPath(projectKey)
	if slug == "" {
		slug = sanitizeSessionName(projectKey)
	}
	return slug
}

func projectSlugCandidates(projectKey string) []string {
	if projectKey == "" {
		return nil
	}

	candidates := []string{}
	seen := make(map[string]struct{})
	for _, slug := range []string{
		primaryProjectSlug(projectKey),
		legacyProjectSlugFromPath(projectKey),
		sanitizeSessionName(projectKey),
	} {
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		candidates = append(candidates, slug)
	}
	return candidates
}

// sessionAgentPath returns the path to the session's agent.json file.
// The path is namespaced by project slug to avoid collisions when
// the same tmux session name is reused across different projects.
// If projectKey is empty, we fall back to the legacy path (no slug)
// for backward compatibility.
func sessionAgentPath(sessionName, projectKey string) string {
	base := filepath.Join(getSessionsBaseDir(), sessionName)
	if projectKey != "" {
		slug := primaryProjectSlug(projectKey)
		base = filepath.Join(base, slug)
	}
	return filepath.Join(base, "agent.json")
}

func sessionArtifactPaths(sessionName, projectKey, fileName string, includeSubdirs bool) ([]string, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}

	base := filepath.Join(getSessionsBaseDir(), sessionName)
	var paths []string
	seen := make(map[string]struct{})
	addPath := func(path string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	if projectKey != "" {
		for _, slug := range projectSlugCandidates(projectKey) {
			addPath(filepath.Join(base, slug, fileName))
		}
	}
	addPath(filepath.Join(base, fileName))
	if !includeSubdirs {
		return paths, nil
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return paths, nil
		}
		return nil, fmt.Errorf("reading session directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		addPath(filepath.Join(base, entry.Name(), fileName))
	}

	return paths, nil
}

// LoadSessionAgent loads the agent info for a session, if it exists.
func LoadSessionAgent(sessionName, projectKey string) (*SessionAgentInfo, error) {
	paths, err := sessionArtifactPaths(sessionName, projectKey, "agent.json", projectKey != "")
	if err != nil {
		return nil, err
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			var info SessionAgentInfo
			if err := json.Unmarshal(data, &info); err != nil {
				return nil, fmt.Errorf("parsing session agent: %w", err)
			}
			if projectKey != "" && filepath.Clean(info.ProjectKey) != filepath.Clean(projectKey) {
				continue
			}
			return &info, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading session agent: %w", err)
		}
	}
	return nil, nil
}

// LoadBestSessionAgent loads the strongest available session agent artifact for
// a session, preferring any provided project keys but falling back to the best
// locally stored candidate when the first lookup misses.
func LoadBestSessionAgent(sessionName string, projectKeys ...string) (*SessionAgentInfo, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}

	for _, projectKey := range projectKeys {
		projectKey = strings.TrimSpace(projectKey)
		if projectKey == "" {
			continue
		}
		info, err := LoadSessionAgent(sessionName, projectKey)
		if err != nil || info != nil {
			return info, err
		}
	}

	paths, err := sessionArtifactPaths(sessionName, "", "agent.json", true)
	if err != nil {
		return nil, err
	}

	var best *SessionAgentInfo
	bestScore := -1
	var firstErr error
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = fmt.Errorf("reading session agent: %w", err)
			}
			continue
		}

		var info SessionAgentInfo
		if err := json.Unmarshal(data, &info); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("parsing session agent: %w", err)
			}
			continue
		}

		score := util.ProjectDirScore(info.ProjectKey)
		if best == nil || score > bestScore ||
			(score == bestScore && info.LastActiveAt.After(best.LastActiveAt)) ||
			(score == bestScore && info.LastActiveAt.Equal(best.LastActiveAt) && info.RegisteredAt.After(best.RegisteredAt)) {
			copyInfo := info
			best = &copyInfo
			bestScore = score
		}
	}

	if best != nil {
		return best, nil
	}

	return nil, firstErr
}

// SaveSessionAgent saves the agent info for a session.
func SaveSessionAgent(sessionName, projectKey string, info *SessionAgentInfo) error {
	if err := validateSessionStorageName(sessionName); err != nil {
		return err
	}
	path := sessionAgentPath(sessionName, projectKey)
	dir := filepath.Dir(path)

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session agent: %w", err)
	}

	// Atomic write with restrictive permissions (0600) since it may contain sensitive info
	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing session agent: %w", err)
	}

	return nil
}

// RegisterSessionAgent registers a session as an agent with Agent Mail.
// The required registration calls double as the availability probe: the full
// health_check diagnostic is deliberately not used here because assembling
// its snapshot can be slower than ordinary identity operations on a loaded
// server, which would suppress registration while the server is healthy.
// Returns the agent info on success or an error on failure.
func (c *Client) RegisterSessionAgent(ctx context.Context, sessionName, workingDir string) (*SessionAgentInfo, error) {
	// Ensure the project first, even when local session metadata already
	// exists: Agent Mail may have rebuilt its database from the archive since
	// the last NTM run, and this lightweight required call is also the
	// reachability probe.
	if _, err := c.EnsureProject(ctx, workingDir); err != nil {
		return nil, fmt.Errorf("ensuring project: %w", err)
	}

	// Check if already registered
	existing, err := LoadSessionAgent(sessionName, workingDir)
	if err != nil {
		return nil, err
	}

	// If already registered with same project, just update activity
	if existing != nil && existing.ProjectKey == workingDir && existing.AgentName != "" {
		// Prime the client's token cache with the persisted token so the
		// re-register call below authenticates as the existing identity.
		if existing.RegistrationToken != "" {
			c.SetRegistrationToken(workingDir, existing.AgentName, existing.RegistrationToken)
		}
		existing.LastActiveAt = time.Now()
		if err := SaveSessionAgent(sessionName, workingDir, existing); err != nil {
			return nil, err
		}
		// Update activity on server (re-register updates last_active_ts)
		agent, serverErr := c.RegisterAgent(ctx, RegisterAgentOptions{
			ProjectKey:      workingDir,
			Program:         "ntm",
			Model:           "coordinator",
			Name:            existing.AgentName,
			TaskDescription: fmt.Sprintf("NTM session coordinator for %s", sessionName),
		})
		if serverErr != nil {
			// Return local state but pass error up so caller can warn
			return existing, fmt.Errorf("updating server activity: %w", serverErr)
		}
		// Persist a (re)issued token for the next process.
		if agent != nil && agent.RegistrationToken != "" && agent.RegistrationToken != existing.RegistrationToken {
			existing.RegistrationToken = agent.RegistrationToken
			if err := SaveSessionAgent(sessionName, workingDir, existing); err != nil {
				return nil, err
			}
		}
		return existing, nil
	}

	// Register the agent. Omit Name so the server auto-generates a valid
	// adjective+noun identity; persist it locally so we can reuse it.
	agent, err := c.RegisterAgent(ctx, RegisterAgentOptions{
		ProjectKey:      workingDir,
		Program:         "ntm",
		Model:           "coordinator",
		TaskDescription: fmt.Sprintf("NTM session coordinator for %s", sessionName),
	})
	if err != nil {
		return nil, fmt.Errorf("registering agent: %w", err)
	}

	// Save locally
	info := &SessionAgentInfo{
		AgentName:         agent.Name,
		ProjectKey:        workingDir,
		RegistrationToken: agent.RegistrationToken,
		RegisteredAt:      time.Now(),
		LastActiveAt:      time.Now(),
	}
	if err := SaveSessionAgent(sessionName, workingDir, info); err != nil {
		return nil, err
	}

	return info, nil
}

// UpdateSessionActivity updates the last_active timestamp for a session's agent.
// If Agent Mail is unavailable, update silently fails without blocking.
func (c *Client) UpdateSessionActivity(ctx context.Context, sessionName, projectKey string) error {
	// Load existing agent info
	info, err := LoadSessionAgent(sessionName, projectKey)
	if err != nil {
		return err
	}
	if info == nil {
		return nil // No agent registered
	}

	// Verify project ownership if projectKey provided
	if projectKey != "" && info.ProjectKey != projectKey {
		return nil // Not our agent (silent skip)
	}

	// Update local timestamp
	info.LastActiveAt = time.Now()
	if err := SaveSessionAgent(sessionName, info.ProjectKey, info); err != nil {
		return err
	}

	// Check if Agent Mail is available
	if !c.IsAvailable() {
		return nil // Silently skip server update
	}

	// Re-register to update last_active_ts on server
	_, err = c.RegisterAgent(ctx, RegisterAgentOptions{
		ProjectKey:      info.ProjectKey,
		Program:         "ntm",
		Model:           "coordinator",
		Name:            info.AgentName,
		TaskDescription: fmt.Sprintf("NTM session coordinator for %s", sessionName),
	})
	if err != nil {
		return fmt.Errorf("updating server activity: %w", err)
	}
	return nil
}

// SessionAgentRegistry stores the mapping of pane titles/IDs to Agent Mail agent names
// for a session. This enables message routing and reservation management across
// session restarts.
type SessionAgentRegistry struct {
	SessionName string            `json:"session_name"`
	ProjectKey  string            `json:"project_key"`
	Agents      map[string]string `json:"agents"`      // pane_title -> agent_name
	PaneIDMap   map[string]string `json:"pane_id_map"` // pane_id -> agent_name (backup)
	// RegistrationTokens maps agent_name -> registration_token returned
	// by create_agent_identity / register_agent on mcp-agent-mail
	// >=2.13. The token must be supplied to identity-scoped MCP calls
	// across process restarts, so we persist it next to the agent_name
	// mapping (ntm#146). Keyed by agent_name (not pane title) so it
	// survives pane renames. `omitempty` + map-pointer treats old
	// registries that predate this field as if no tokens were known.
	RegistrationTokens map[string]string `json:"registration_tokens,omitempty"`
	// PanePIDs records the tmux #{pane_pid} observed for each pane id when its
	// binding was written (pane_id -> pid). tmux reuses pane ids across server
	// restarts, so the pid distinguishes "the pane that registered this name"
	// from "an unrelated pane that later got the same %N". Together with
	// PaneIDMap it lets a later process decide whether a recorded binding is
	// still LIVE instead of trusting a recycled title (ntm#256). Optional:
	// registries written before this field carry no pids (treated as unknown).
	PanePIDs     map[string]int `json:"pane_pids,omitempty"`
	RegisteredAt time.Time      `json:"registered_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// PaneLiveness reports whether a pane id recorded in the registry still refers
// to the pane that registered it. recordedPID is the #{pane_pid} stored at
// registration time, or 0 when unknown. Implementations typically require the
// pane to exist in the session and, when a pid was recorded, to still carry
// that pid.
type PaneLiveness func(paneID string, recordedPID int) bool

// NewSessionAgentRegistry creates a new empty registry.
func NewSessionAgentRegistry(sessionName, projectKey string) *SessionAgentRegistry {
	now := time.Now()
	return &SessionAgentRegistry{
		SessionName:        sessionName,
		ProjectKey:         projectKey,
		Agents:             make(map[string]string),
		PaneIDMap:          make(map[string]string),
		RegistrationTokens: make(map[string]string),
		RegisteredAt:       now,
		UpdatedAt:          now,
	}
}

// HydrateClientTokensForProject scans every session under the ntm
// sessions base dir for an agent_registry.json that targets the given
// projectKey and pushes each (agent_name, registration_token) pair
// into the Client's per-agent token cache. This lets `ntm mail inbox`,
// `ntm mail send`, and other commands that don't carry session context
// still authenticate as their existing agents on mcp-agent-mail >=2.13
// (ntm#146). Errors enumerating sessions are non-fatal — the function
// just hydrates whatever it can find.
func HydrateClientTokensForProject(c *Client, projectKey string) {
	if c == nil || projectKey == "" {
		return
	}
	base := getSessionsBaseDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	target := filepath.Clean(projectKey)
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		registry, err := LoadSessionAgentRegistry(ent.Name(), projectKey)
		if err != nil || registry == nil {
			continue
		}
		if filepath.Clean(registry.ProjectKey) != target {
			continue
		}
		registry.HydrateClientTokens(c)
	}
}

// SetRegistrationToken stores the token for an agent on the registry
// (in-memory). Callers must Save() afterwards to persist.
func (r *SessionAgentRegistry) SetRegistrationToken(agentName, token string) {
	if r == nil || agentName == "" {
		return
	}
	if r.RegistrationTokens == nil {
		r.RegistrationTokens = make(map[string]string)
	}
	if token == "" {
		delete(r.RegistrationTokens, agentName)
		return
	}
	r.RegistrationTokens[agentName] = token
}

// RegistrationToken returns the cached token for agent_name, or "".
// Nil-receiver safe.
func (r *SessionAgentRegistry) RegistrationToken(agentName string) string {
	if r == nil {
		return ""
	}
	return r.RegistrationTokens[agentName]
}

// HydrateClientTokens pushes every (agent_name, token) pair in this
// registry into the given Client's per-agent token cache so later
// identity-scoped MCP calls can attach the token automatically. Use
// at startup / after LoadSessionAgentRegistry.
func (r *SessionAgentRegistry) HydrateClientTokens(c *Client) {
	if r == nil || c == nil || r.ProjectKey == "" {
		return
	}
	for agentName, token := range r.RegistrationTokens {
		if agentName == "" || token == "" {
			continue
		}
		c.SetRegistrationToken(r.ProjectKey, agentName, token)
	}
}

// registryPath returns the path to the session's agent registry file.
func registryPath(sessionName, projectKey string) string {
	base := filepath.Join(getSessionsBaseDir(), sessionName)
	if projectKey != "" {
		slug := primaryProjectSlug(projectKey)
		base = filepath.Join(base, slug)
	}
	return filepath.Join(base, "agent_registry.json")
}

// LoadSessionAgentRegistry loads the agent registry for a session, if it exists.
// Returns nil without error if no registry exists.
func LoadSessionAgentRegistry(sessionName, projectKey string) (*SessionAgentRegistry, error) {
	paths, err := sessionArtifactPaths(sessionName, projectKey, "agent_registry.json", projectKey != "")
	if err != nil {
		return nil, err
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			var registry SessionAgentRegistry
			if err := json.Unmarshal(data, &registry); err != nil {
				return nil, fmt.Errorf("parsing agent registry: %w", err)
			}
			if projectKey != "" && filepath.Clean(registry.ProjectKey) != filepath.Clean(projectKey) {
				continue
			}
			return &registry, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading agent registry: %w", err)
		}
	}
	return nil, nil
}

// LoadBestSessionAgentRegistry loads the strongest available registry for a
// session, preferring any provided project keys but falling back to the best
// locally stored candidate when the first lookup misses.
func LoadBestSessionAgentRegistry(sessionName string, projectKeys ...string) (*SessionAgentRegistry, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}

	for _, projectKey := range projectKeys {
		projectKey = strings.TrimSpace(projectKey)
		if projectKey == "" {
			continue
		}
		registry, err := LoadSessionAgentRegistry(sessionName, projectKey)
		if err != nil || registry != nil {
			return registry, err
		}
	}

	paths, err := sessionArtifactPaths(sessionName, "", "agent_registry.json", true)
	if err != nil {
		return nil, err
	}

	var best *SessionAgentRegistry
	bestScore := -1
	var firstErr error
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) && firstErr == nil {
				firstErr = fmt.Errorf("reading agent registry: %w", err)
			}
			continue
		}

		var registry SessionAgentRegistry
		if err := json.Unmarshal(data, &registry); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("parsing agent registry: %w", err)
			}
			continue
		}

		score := util.ProjectDirScore(registry.ProjectKey)
		if best == nil || score > bestScore ||
			(score == bestScore && registry.UpdatedAt.After(best.UpdatedAt)) ||
			(score == bestScore && registry.UpdatedAt.Equal(best.UpdatedAt) && registry.RegisteredAt.After(best.RegisteredAt)) {
			copyRegistry := registry
			best = &copyRegistry
			bestScore = score
		}
	}

	if best != nil {
		return best, nil
	}

	return nil, firstErr
}

// SaveSessionAgentRegistry saves the agent registry for a session.
func SaveSessionAgentRegistry(registry *SessionAgentRegistry) error {
	if registry == nil {
		return fmt.Errorf("cannot save nil registry")
	}
	if err := validateSessionStorageName(registry.SessionName); err != nil {
		return err
	}

	path := registryPath(registry.SessionName, registry.ProjectKey)
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating registry directory: %w", err)
	}

	registry.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent registry: %w", err)
	}

	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing agent registry: %w", err)
	}

	return nil
}

// AddAgent adds a pane -> agent name mapping to the registry.
func (r *SessionAgentRegistry) AddAgent(paneTitle, paneID, agentName string) {
	if r.Agents == nil {
		r.Agents = make(map[string]string)
	}
	if r.PaneIDMap == nil {
		r.PaneIDMap = make(map[string]string)
	}

	if paneTitle != "" {
		for existingTitle, existingAgent := range r.Agents {
			if existingAgent == agentName && existingTitle != paneTitle {
				delete(r.Agents, existingTitle)
			}
		}
		r.Agents[paneTitle] = agentName
	}
	if paneID != "" {
		for existingPaneID, existingAgent := range r.PaneIDMap {
			if existingAgent == agentName && existingPaneID != paneID {
				delete(r.PaneIDMap, existingPaneID)
				// The pid belonged to the binding we just retired.
				delete(r.PanePIDs, existingPaneID)
			}
		}
		r.PaneIDMap[paneID] = agentName
	}
}

// SetPanePID records the #{pane_pid} observed for paneID when its binding was
// written. A pid <= 0 clears the record (liveness falls back to pane
// existence only).
func (r *SessionAgentRegistry) SetPanePID(paneID string, pid int) {
	if r == nil || paneID == "" {
		return
	}
	if pid <= 0 {
		delete(r.PanePIDs, paneID)
		return
	}
	if r.PanePIDs == nil {
		r.PanePIDs = make(map[string]int)
	}
	r.PanePIDs[paneID] = pid
}

// PanePID returns the pid recorded for paneID, or 0 when none was recorded.
func (r *SessionAgentRegistry) PanePID(paneID string) int {
	if r == nil || r.PanePIDs == nil {
		return 0
	}
	return r.PanePIDs[paneID]
}

// PaneIDForAgent returns the pane id currently bound to agentName, if any.
func (r *SessionAgentRegistry) PaneIDForAgent(agentName string) (string, bool) {
	if r == nil || r.PaneIDMap == nil || agentName == "" {
		return "", false
	}
	for paneID, name := range r.PaneIDMap {
		if name == agentName {
			return paneID, true
		}
	}
	return "", false
}

// paneBindingLive reports whether the binding recorded for paneID is still
// held by a live pane. A nil isLive means the caller cannot observe liveness;
// the binding is then treated as LIVE (occupied) so an unobservable slot is
// never handed to a second pane — the conservative failure is a fresh
// identity, never a shared one.
func (r *SessionAgentRegistry) paneBindingLive(paneID string, isLive PaneLiveness) bool {
	if paneID == "" {
		return false
	}
	if isLive == nil {
		return true
	}
	return isLive(paneID, r.PanePID(paneID))
}

// ResolveForPane decides which registered identity, if any, the pane
// (paneTitle, paneID) should reuse. It returns the agent name, the pane id the
// identity is being recovered from (empty when the name was bound to this very
// pane or to no pane at all), and whether a reuse is allowed.
//
// Resolution order (ntm#256):
//  1. paneID itself is bound -> that name. The stable physical pane id is the
//     primary key; a title is only a hint.
//  2. paneTitle is bound -> that name, but ONLY when the pane currently
//     holding it is dead per isLive. A live holder makes the slot OCCUPIED and
//     the caller must mint a fresh identity instead of re-issuing a running
//     agent's name to a second pane. Dead-slot reuse (same-session respawn
//     after a kill) keeps working exactly as before.
//
// Callers that cannot observe liveness may pass a nil isLive; every recorded
// holder is then treated as live, i.e. titles never trigger reuse.
func (r *SessionAgentRegistry) ResolveForPane(paneTitle, paneID string, isLive PaneLiveness) (name string, recoveredFrom string, ok bool) {
	if r == nil {
		return "", "", false
	}
	if paneID != "" {
		if name, found := r.GetAgentByID(paneID); found && name != "" {
			return name, "", true
		}
	}
	if paneTitle == "" {
		return "", "", false
	}
	name, found := r.GetAgentByTitle(paneTitle)
	if !found || name == "" {
		return "", "", false
	}
	holder, bound := r.PaneIDForAgent(name)
	if !bound || holder == "" || holder == paneID {
		// Title known but no (other) pane on record: nothing can be live.
		return name, "", true
	}
	if r.paneBindingLive(holder, isLive) {
		return "", "", false
	}
	return name, holder, true
}

// OccupiedTitles returns the registry titles whose bound pane is still live
// per isLive. `ntm add` folds these into its next-index derivation so a live
// pane whose title was overwritten by the agent (or the user) still counts as
// occupying its slot (ntm#256). Order is deterministic (sorted).
func (r *SessionAgentRegistry) OccupiedTitles(isLive PaneLiveness) []string {
	if r == nil || len(r.Agents) == 0 {
		return nil
	}
	titles := make([]string, 0, len(r.Agents))
	for title, name := range r.Agents {
		holder, bound := r.PaneIDForAgent(name)
		if !bound {
			continue
		}
		if r.paneBindingLive(holder, isLive) {
			titles = append(titles, title)
		}
	}
	sort.Strings(titles)
	return titles
}

// GetAgentByTitle returns the agent name for a pane title.
func (r *SessionAgentRegistry) GetAgentByTitle(paneTitle string) (string, bool) {
	if r == nil || r.Agents == nil {
		return "", false
	}
	name, ok := r.Agents[paneTitle]
	return name, ok
}

// GetAgentByID returns the agent name for a pane ID.
func (r *SessionAgentRegistry) GetAgentByID(paneID string) (string, bool) {
	if r == nil || r.PaneIDMap == nil {
		return "", false
	}
	name, ok := r.PaneIDMap[paneID]
	return name, ok
}

// GetAgent tries to find an agent by title first, then by ID.
func (r *SessionAgentRegistry) GetAgent(paneTitle, paneID string) (string, bool) {
	if name, ok := r.GetAgentByTitle(paneTitle); ok {
		return name, true
	}
	return r.GetAgentByID(paneID)
}

// Count returns the number of registered agents.
func (r *SessionAgentRegistry) Count() int {
	if r == nil || r.Agents == nil {
		return 0
	}
	return len(r.Agents)
}
