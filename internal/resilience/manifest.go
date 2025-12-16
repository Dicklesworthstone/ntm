package resilience

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SpawnManifest represents the configuration of a spawned session for monitoring
type SpawnManifest struct {
	Session    string        `json:"session"`
	ProjectDir string        `json:"project_dir"`
	Agents     []AgentConfig `json:"agents"`
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
			return "manifests"
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", "manifests")
}

// SaveManifest saves the spawn manifest for a session
func SaveManifest(manifest *SpawnManifest) error {
	dir := ManifestDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating manifest directory: %w", err)
	}

	path := filepath.Join(dir, manifest.Session+".json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// LoadManifest loads the spawn manifest for a session
func LoadManifest(session string) (*SpawnManifest, error) {
	path := filepath.Join(ManifestDir(), session+".json")
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
	path := filepath.Join(ManifestDir(), session+".json")
	return os.Remove(path)
}
