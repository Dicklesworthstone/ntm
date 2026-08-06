package resilience

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// sanitizeSessionName ensures a session name is safe for use in file paths.
// It rejects names containing path separators or traversal components.
func sanitizeSessionName(session string) (string, error) {
	if session == "" {
		return "", fmt.Errorf("empty session name")
	}
	// Reject any path separator characters
	if strings.ContainsAny(session, "/\\") {
		return "", fmt.Errorf("session name %q contains path separators", session)
	}
	// Reject path traversal components
	if session == "." || session == ".." || strings.Contains(session, "..") {
		return "", fmt.Errorf("session name %q contains path traversal", session)
	}
	return session, nil
}

// SpawnManifest represents the configuration of a spawned session for monitoring
type SpawnManifest struct {
	Session     string        `json:"session"`
	ProjectDir  string        `json:"project_dir"`
	Agents      []AgentConfig `json:"agents"`
	AutoRestart bool          `json:"auto_restart"`
}

// AgentConfig represents the configuration for a single agent
type AgentConfig struct {
	PaneID    string `json:"pane_id"`
	PaneIndex int    `json:"pane_index"`
	Type      string `json:"type"`
	Model     string `json:"model"`
	Command   string `json:"command"`
}

// ManifestDir returns the directory for storing session manifests
func ManifestDir() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "ntm", "manifests")
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", "manifests")
}

// LogDir returns the directory for storing session monitor logs
func LogDir() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "ntm", "logs")
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", "logs")
}

// SaveManifest saves the spawn manifest for a session
func SaveManifest(manifest *SpawnManifest) error {
	safe, err := sanitizeSessionName(manifest.Session)
	if err != nil {
		return fmt.Errorf("invalid session for manifest: %w", err)
	}

	dir := ManifestDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating manifest directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("restricting manifest directory permissions: %w", err)
	}

	path := filepath.Join(dir, safe+".json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	return util.AtomicWriteFile(path, data, 0600)
}

// LoadManifest loads the spawn manifest for a session
func LoadManifest(session string) (*SpawnManifest, error) {
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return nil, fmt.Errorf("invalid session for manifest: %w", err)
	}

	path := filepath.Join(ManifestDir(), safe+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	var manifest SpawnManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("unmarshaling manifest: %w", err)
	}

	return &manifest, nil
}

// DeleteManifest removes the manifest for a session
func DeleteManifest(session string) error {
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return fmt.Errorf("invalid session for manifest: %w", err)
	}

	path := filepath.Join(ManifestDir(), safe+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
