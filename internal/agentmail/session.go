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
)

// SessionAgentInfo tracks the registered agent identity for a session.
type SessionAgentInfo struct {
	AgentName    string    `json:"agent_name"`
	ProjectKey   string    `json:"project_key"`
	RegisteredAt time.Time `json:"registered_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

// sanitizeSessionName converts a session name to a valid agent name component.
// Replaces non-alphanumeric chars with underscores, lowercases.
func sanitizeSessionName(name string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9]+`)
	sanitized := re.ReplaceAllString(name, "_")
	sanitized = strings.Trim(sanitized, "_")
	return strings.ToLower(sanitized)
}

// sessionAgentPath returns the path to the session's agent.json file.
// The path is namespaced by project slug to avoid collisions when
// the same tmux session name is reused across different projects.
// If projectKey is empty, we fall back to the legacy path (no slug)
// for backward compatibility.
func sessionAgentPath(sessionName, projectKey string) string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	base := filepath.Join(configDir, "ntm", "sessions", sessionName)
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
			data, err = os.ReadFile(legacyPath)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, nil // No agent registered yet
				}
				return nil, fmt.Errorf("reading session agent: %w", err)
			}
			path = legacyPath
		} else {
			return nil, fmt.Errorf("reading session agent: %w", err)
		}
	}

	var info SessionAgentInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing session agent: %w", err)
	}

	return &info, nil
}

// SaveSessionAgent saves the agent info for a session.
func SaveSessionAgent(sessionName, projectKey string, info *SessionAgentInfo) error {
	path := sessionAgentPath(sessionName, projectKey)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session agent: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
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
	if existing != nil && existing.ProjectKey == workingDir {
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
			// Log but don't fail - local state is already updated
			return existing, nil
		}
		return existing, nil
	}

	// Ensure project exists
	if _, err := c.EnsureProject(ctx, workingDir); err != nil {
		return nil, fmt.Errorf("ensuring project: %w", err)
	}

	// Generate agent name: ntm_{sanitized_session}
	agentName := fmt.Sprintf("ntm_%s", sanitizeSessionName(sessionName))

	// Register the agent
	agent, err := c.RegisterAgent(ctx, RegisterAgentOptions{
		ProjectKey:      workingDir,
		Program:         "ntm",
		Model:           "coordinator",
		Name:            agentName,
		TaskDescription: fmt.Sprintf("NTM session coordinator for %s", sessionName),
	})
	if err != nil {
		// If name is taken (already exists), try with timestamp suffix
		if IsNameTakenError(err) {
			agentName = fmt.Sprintf("ntm_%s_%d", sanitizeSessionName(sessionName), time.Now().Unix()%10000)
			agent, err = c.RegisterAgent(ctx, RegisterAgentOptions{
				ProjectKey:      workingDir,
				Program:         "ntm",
				Model:           "coordinator",
				Name:            agentName,
				TaskDescription: fmt.Sprintf("NTM session coordinator for %s", sessionName),
			})
		}
		if err != nil {
			return nil, fmt.Errorf("registering agent: %w", err)
		}
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
func (c *Client) UpdateSessionActivity(ctx context.Context, sessionName string) error {
	// Load existing agent info
	info, err := LoadSessionAgent(sessionName, "")
	if err != nil {
		return err
	}
	if info == nil {
		return nil // No agent registered
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
	// Ignore server errors - local state is already updated
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
