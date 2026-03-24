package resilience

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestDir_WithXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir := ManifestDir()
	want := filepath.Join("/custom/data", "ntm", "manifests")
	if dir != want {
		t.Errorf("ManifestDir() = %q, want %q", dir, want)
	}
}

func TestManifestDir_WithoutXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	dir := ManifestDir()
	// Should use ~/.local/share/ntm/manifests (or temp fallback)
	if !strings.HasSuffix(dir, filepath.Join("ntm", "manifests")) {
		t.Errorf("ManifestDir() = %q, want suffix ntm/manifests", dir)
	}
}

func TestLogDir_WithXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir := LogDir()
	want := filepath.Join("/custom/data", "ntm", "logs")
	if dir != want {
		t.Errorf("LogDir() = %q, want %q", dir, want)
	}
}

func TestLogDir_WithoutXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	dir := LogDir()
	if !strings.HasSuffix(dir, filepath.Join("ntm", "logs")) {
		t.Errorf("LogDir() = %q, want suffix ntm/logs", dir)
	}
}

func TestSaveLoadDeleteManifest_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifest := &SpawnManifest{
		Session:     "test-session",
		ProjectDir:  "/home/user/project",
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "%0", PaneIndex: 0, Type: "cc", Model: "opus-4", Command: "claude"},
			{PaneID: "%1", PaneIndex: 1, Type: "cod", Model: "gpt-5", Command: "codex"},
		},
	}

	// Save
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Verify file exists
	expectedPath := filepath.Join(tmpDir, "ntm", "manifests", "test-session.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("manifest file not created at %s: %v", expectedPath, err)
	}

	// Load
	loaded, err := LoadManifest("test-session")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if loaded.Session != "test-session" {
		t.Errorf("Session = %q, want test-session", loaded.Session)
	}
	if loaded.ProjectDir != "/home/user/project" {
		t.Errorf("ProjectDir = %q, want /home/user/project", loaded.ProjectDir)
	}
	if !loaded.AutoRestart {
		t.Error("AutoRestart should be true")
	}
	if len(loaded.Agents) != 2 {
		t.Fatalf("Agents count = %d, want 2", len(loaded.Agents))
	}
	if loaded.Agents[0].Type != "cc" {
		t.Errorf("Agents[0].Type = %q, want cc", loaded.Agents[0].Type)
	}
	if loaded.Agents[1].Model != "gpt-5" {
		t.Errorf("Agents[1].Model = %q, want gpt-5", loaded.Agents[1].Model)
	}

	// Delete
	if err := DeleteManifest("test-session"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	// Verify deleted
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Errorf("manifest file still exists after delete: %v", err)
	}
}

func TestLoadManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	_, err := LoadManifest("nonexistent")
	if err == nil {
		t.Error("expected error for missing manifest")
	}
	if !strings.Contains(err.Error(), "reading manifest") {
		t.Errorf("error = %q, want 'reading manifest'", err.Error())
	}
}

func TestDeleteManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	err := DeleteManifest("nonexistent")
	if err != nil {
		t.Errorf("DeleteManifest on non-existent manifest should be idempotent, got error: %v", err)
	}
}

func TestSaveManifest_EmptyAgents(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifest := &SpawnManifest{
		Session:    "empty-agents",
		ProjectDir: "/tmp/test",
		Agents:     []AgentConfig{},
	}

	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	loaded, err := LoadManifest("empty-agents")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(loaded.Agents) != 0 {
		t.Errorf("Agents count = %d, want 0", len(loaded.Agents))
	}
}
