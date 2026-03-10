package agentmail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// SessionAgentInfo tracks the registered agent identity for a session.
type SessionAgentInfo struct {
	AgentName    string    `json:"agent_name"`
	ProjectKey   string    `json:"project_key"`
	RegisteredAt time.Time `json:"registered_at"`
	LastActiveAt time.Time `json:"last_active_at"`
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

// LoadSessionAgent loads the agent info for a session, if it exists.
func LoadSessionAgent(sessionName, projectKey string) (*SessionAgentInfo, error) {
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}

	base := filepath.Join(getSessionsBaseDir(), sessionName)
	paths := []string{}
	if projectKey != "" {
		for _, slug := range projectSlugCandidates(projectKey) {
			paths = append(paths, filepath.Join(base, slug, "agent.json"))
		}
	}
	paths = append(paths, filepath.Join(base, "agent.json"))

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

// DeleteSessionAgent removes the agent info file for a session.
func DeleteSessionAgent(sessionName, projectKey string) error {
	if err := validateSessionStorageName(sessionName); err != nil {
		return err
	}
	path := sessionAgentPath(sessionName, projectKey)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting session agent: %w", err)
	}
	return nil
}

// RegisterSessionAgent registers a session as an agent with Agent Mail.
// If Agent Mail is unavailable, registration silently fails without blocking.
// Returns the agent info on success, nil if unavailable, or an error on failure.
func (c *Client) RegisterSessionAgent(ctx context.Context, sessionName, workingDir string) (*SessionAgentInfo, error) {
	// Check if Agent Mail is available
	if !c.IsAvailable() {
		return nil, nil // Silently skip if unavailable
	}

	// Check if already registered
	existing, err := LoadSessionAgent(sessionName, workingDir)
	if err != nil {
		return nil, err
	}

	// If already registered with same project, just update activity
	if existing != nil && existing.ProjectKey == workingDir && existing.AgentName != "" {
		existing.LastActiveAt = time.Now()
		if err := SaveSessionAgent(sessionName, workingDir, existing); err != nil {
			return nil, err
		}
		// Update activity on server (re-register updates last_active_ts)
		_, serverErr := c.RegisterAgent(ctx, RegisterAgentOptions{
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
		return existing, nil
	}

	// Ensure project exists
	if _, err := c.EnsureProject(ctx, workingDir); err != nil {
		return nil, fmt.Errorf("ensuring project: %w", err)
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
		AgentName:    agent.Name,
		ProjectKey:   workingDir,
		RegisteredAt: time.Now(),
		LastActiveAt: time.Now(),
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

// IsNameTakenError checks if an error indicates the agent name is already taken.
func IsNameTakenError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "already in use") ||
		strings.Contains(errStr, "name taken") ||
		strings.Contains(errStr, "already registered")
}

// SessionAgentRegistry stores the mapping of pane titles/IDs to Agent Mail agent names
// for a session. This enables message routing and reservation management across
// session restarts.
type SessionAgentRegistry struct {
	SessionName  string            `json:"session_name"`
	ProjectKey   string            `json:"project_key"`
	Agents       map[string]string `json:"agents"`      // pane_title -> agent_name
	PaneIDMap    map[string]string `json:"pane_id_map"` // pane_id -> agent_name (backup)
	RegisteredAt time.Time         `json:"registered_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// NewSessionAgentRegistry creates a new empty registry.
func NewSessionAgentRegistry(sessionName, projectKey string) *SessionAgentRegistry {
	now := time.Now()
	return &SessionAgentRegistry{
		SessionName:  sessionName,
		ProjectKey:   projectKey,
		Agents:       make(map[string]string),
		PaneIDMap:    make(map[string]string),
		RegisteredAt: now,
		UpdatedAt:    now,
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
	if err := validateSessionStorageName(sessionName); err != nil {
		return nil, err
	}

	base := filepath.Join(getSessionsBaseDir(), sessionName)
	paths := []string{}
	if projectKey != "" {
		for _, slug := range projectSlugCandidates(projectKey) {
			paths = append(paths, filepath.Join(base, slug, "agent_registry.json"))
		}
	}
	paths = append(paths, filepath.Join(base, "agent_registry.json"))

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

// DeleteSessionAgentRegistry removes the agent registry for a session.
func DeleteSessionAgentRegistry(sessionName, projectKey string) error {
	if err := validateSessionStorageName(sessionName); err != nil {
		return err
	}
	path := registryPath(sessionName, projectKey)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting agent registry: %w", err)
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
			}
		}
		r.PaneIDMap[paneID] = agentName
	}
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
