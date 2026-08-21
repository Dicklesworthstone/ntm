package agentmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadSessionAgent_Fallback tests the fallback logic in LoadSessionAgent.
func TestLoadSessionAgent_Fallback(t *testing.T) {
	// Setup temporary config directory
	tmpDir := t.TempDir()

	// Mock user config dir via environment variable or override function if possible.
	// Since sessionAgentPath uses os.UserConfigDir(), we can't easily mock it without
	// changing the implementation or using a different approach.
	// Instead, we can create a local version of sessionAgentPath for testing logic,
	// OR we can rely on the fact that LoadSessionAgent calls sessionAgentPath.

	// However, sessionAgentPath is not exported and uses hardcoded os.UserConfigDir().
	// To test this effectively without mocking os.UserConfigDir (which is hard),
	// let's create the directory structure manually in a location and see if we can
	// point the function to it. But we can't redirect it.

	// sessionAgentPath resolves through os.UserConfigDir(), which on Linux reads
	// XDG_CONFIG_HOME FIRST and only falls back to $HOME/.config when that is
	// unset. Setting HOME alone therefore redirects nothing on any desktop that
	// exports XDG_CONFIG_HOME -- os.UserConfigDir() keeps returning the real
	// ~/.config, and the reset helper below used to delete it. Both must be set.
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	sessionName := "test-session"
	projectKey := "/path/to/project"
	projectSlug := ProjectSlugFromPath(projectKey)
	legacyProjectSlug := legacyProjectSlugFromPath(projectKey)

	// Expected paths based on implementation:
	// New: $HOME/.config/ntm/sessions/test-session/path-to-project/agent.json
	// Legacy slug fallback: $HOME/.config/ntm/sessions/test-session/project/agent.json
	// Legacy: $HOME/.config/ntm/sessions/test-session/agent.json

	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(tmpDir, ".config")
	}
	baseDir := filepath.Join(configDir, "ntm", "sessions", sessionName)
	newPath := filepath.Join(baseDir, projectSlug, "agent.json")
	legacySlugPath := filepath.Join(baseDir, legacyProjectSlug, "agent.json")
	legacyPath := filepath.Join(baseDir, "agent.json")

	info := SessionAgentInfo{
		AgentName:    "test-agent",
		ProjectKey:   projectKey,
		RegisteredAt: time.Now(),
		LastActiveAt: time.Now(),
	}
	data, _ := json.Marshal(info)

	// Helper to clear directories.
	//
	// The containment check is not defensive programming for its own sake: this
	// helper deleted the real ~/.config twice on 2026-08-21, taking the desktop's
	// entire configuration with it, because configDir resolved outside tmpDir. A
	// recursive delete of a path this test did not create is never correct, so it
	// fails the test instead of running.
	reset := func() {
		t.Helper()
		if !strings.HasPrefix(configDir, tmpDir+string(os.PathSeparator)) {
			t.Fatalf("refusing to remove %q: outside the test temp dir %q", configDir, tmpDir)
		}
		os.RemoveAll(configDir)
	}

	// 1. Test finding in new path
	t.Run("Finds in new path", func(t *testing.T) {
		reset()
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		loaded, err := LoadSessionAgent(sessionName, projectKey)
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}
		if loaded == nil {
			t.Fatal("Expected loaded agent, got nil")
		}
		if loaded.AgentName != info.AgentName {
			t.Errorf("Expected agent %s, got %s", info.AgentName, loaded.AgentName)
		}
	})

	// 2. Test fallback to legacy basename slug when project key is provided but the
	// modern full-path slug directory does not exist yet.
	t.Run("Fallback to legacy slug path", func(t *testing.T) {
		reset()
		if err := os.MkdirAll(filepath.Dir(legacySlugPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacySlugPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		loaded, err := LoadSessionAgent(sessionName, projectKey)
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}
		if loaded == nil {
			t.Fatal("Expected loaded agent, got nil")
		}
		if loaded.AgentName != info.AgentName {
			t.Errorf("Expected agent %s, got %s", info.AgentName, loaded.AgentName)
		}
	})

	// 3. Test fallback to legacy no-slug path when project key is provided but
	// both modern and legacy-slug paths are missing.
	t.Run("Fallback to legacy no-slug path", func(t *testing.T) {
		reset()
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		loaded, err := LoadSessionAgent(sessionName, projectKey)
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}
		if loaded == nil {
			t.Fatal("Expected loaded agent, got nil")
		}
		if loaded.AgentName != info.AgentName {
			t.Errorf("Expected agent %s, got %s", info.AgentName, loaded.AgentName)
		}
	})

	// 4. Test fallback to searching subdirectories when project key is unknown/empty
	// (Note: LoadSessionAgent calls sessionAgentPath(name, "") which returns legacyPath.
	// If legacyPath doesn't exist, it used to search subdirectories but that unsafe behavior was removed.)
	t.Run("Fallback search subdirectories", func(t *testing.T) {
		reset()
		// Create a random slug dir
		randomSlugDir := filepath.Join(baseDir, "some-random-slug")
		randomPath := filepath.Join(randomSlugDir, "agent.json")

		if err := os.MkdirAll(randomSlugDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(randomPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		// Pass empty project key to trigger legacy lookup -> fail -> STOP (no search)
		loaded, err := LoadSessionAgent(sessionName, "")
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}
		if loaded != nil {
			t.Fatal("Expected nil (strict loading), got agent")
		}
	})

	t.Run("Continue past mismatched candidate", func(t *testing.T) {
		reset()

		wrongInfo := info
		wrongInfo.ProjectKey = "/path/to/other-project"
		wrongData, _ := json.Marshal(wrongInfo)

		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, wrongData, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyPath, data, 0644); err != nil {
			t.Fatal(err)
		}

		loaded, err := LoadSessionAgent(sessionName, projectKey)
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}
		if loaded == nil {
			t.Fatal("Expected loaded agent, got nil")
		}
		if loaded.ProjectKey != projectKey {
			t.Fatalf("Expected project %s, got %s", projectKey, loaded.ProjectKey)
		}
	})
}
