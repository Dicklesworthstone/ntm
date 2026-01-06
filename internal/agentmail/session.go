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

// sessionAgentPath returns the path to the session's agent.json file.
// The path is namespaced by project slug to avoid collisions when
// the same tmux session name is reused across different projects.
// If projectKey is empty, we fall back to the legacy path (no slug)
// for backward compatibility.
func sessionAgentPath(sessionName, projectKey string) string {
	base := filepath.Join(getSessionsBaseDir(), sessionName)
	if projectKey != "" {
		slug := ProjectSlugFromPath(projectKey)
		if slug == "" {
			slug = sanitizeSessionName(projectKey)
		}
		base = filepath.Join(base, slug)
	}
	return filepath.Join(base, "agent.json")
}

// LoadSessionAgent loads the agent info for a session, if it exists.
func LoadSessionAgent(sessionName, projectKey string) (*SessionAgentInfo, error) {
	// Prefer project-scoped path to avoid cross-project collisions.
	path := sessionAgentPath(sessionName, projectKey)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Legacy fallback (pre-namespacing)
			legacyPath := sessionAgentPath(sessionName, "")
			if data, err = os.ReadFile(legacyPath); err != nil {
				if !os.IsNotExist(err) {
					return nil, fmt.Errorf("reading session agent: %w", err)
				}
				// Not found
				return nil, nil
			}
		} else {
			return nil, fmt.Errorf("reading session agent: %w", err)
		}
	}

	var info SessionAgentInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing session agent: %w", err)
	}

	// Strict validation: if we requested a specific project, ensure we got it.
	// This protects against legacy fallback returning an agent for a different project.
	if projectKey != "" && info.ProjectKey != projectKey {
		return nil, nil
	}

	return &info, nil
}

// SaveSessionAgent saves the agent info for a session.
func SaveSessionAgent(sessionName, projectKey string, info *SessionAgentInfo) error {
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
