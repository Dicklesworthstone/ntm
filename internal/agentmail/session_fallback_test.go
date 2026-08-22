package agentmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pathWithin reports whether path is root itself or lies strictly inside
// root. Both sides are cleaned; a bare prefix match is NOT enough (/tmp/a must
// not contain /tmp/ab), so the comparison is done on a separator boundary.
//
// It exists because a recursive delete of a path a test did not create is
// never correct, whatever the environment says (gh#258).
func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "" || path == "" {
		return false
	}
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

// isolateUserConfig points every config-dir resolution NTM uses at a fresh
// temp dir and returns it. os.UserConfigDir() honours XDG_CONFIG_HOME *before*
// HOME on Linux and derives from HOME on macOS/Windows, so setting HOME alone
// redirects nothing on a session that exports XDG_CONFIG_HOME (systemd user
// sessions, uwsm, Hyprland, ...). That exact gap let this file's reset helper
// recursively delete a developer's real ~/.config (gh#258). Both variables
// are set so the redirect holds on every platform.
func isolateUserConfig(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	return tmpDir
}

// TestIsolateUserConfigRedirectsSessionsBaseDir is the regression guard for
// gh#258: with the isolation helper applied, the *production* resolver must
// land inside the temp dir, never in the real user's config directory.
func TestIsolateUserConfigRedirectsSessionsBaseDir(t *testing.T) {
	realConfigDir, realErr := os.UserConfigDir()

	tmpDir := isolateUserConfig(t)

	base := getSessionsBaseDir()
	if !pathWithin(tmpDir, base) {
		t.Fatalf("getSessionsBaseDir() = %q, want a path inside the test temp dir %q", base, tmpDir)
	}
	if realErr == nil && realConfigDir != "" && pathWithin(realConfigDir, base) {
		t.Fatalf("getSessionsBaseDir() = %q still resolves under the real config dir %q", base, realConfigDir)
	}
}

// TestPathWithinRefusesEscapes pins the containment predicate that gates the
// only recursive delete in this package's tests. The negative cases are the
// ones that matter: a real config dir, a sibling with a shared prefix, and a
// parent of the root must all be refused.
func TestPathWithinRefusesEscapes(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator), "tmp", "ntm-test-root")
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"root itself", root, true},
		{"direct child", filepath.Join(root, ".config"), true},
		{"nested child", filepath.Join(root, ".config", "ntm", "sessions", "s"), true},
		{"unclean child", root + string(os.PathSeparator) + "." + string(os.PathSeparator) + "x", true},
		{"shared prefix sibling", root + "-other", false},
		{"parent", filepath.Dir(root), false},
		{"unrelated absolute", filepath.Join(string(os.PathSeparator), "home", "user", ".config"), false},
		{"dot-dot escape", filepath.Join(root, "..", "escaped"), false},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathWithin(root, tc.path); got != tc.want {
				t.Fatalf("pathWithin(%q, %q) = %v, want %v", root, tc.path, got, tc.want)
			}
		})
	}
	if pathWithin("", root) {
		t.Fatal("pathWithin with an empty root must refuse")
	}
}

// TestLoadSessionAgent_Fallback tests the fallback logic in LoadSessionAgent.
func TestLoadSessionAgent_Fallback(t *testing.T) {
	tmpDir := isolateUserConfig(t)

	sessionName := "test-session"
	projectKey := "/path/to/project"
	projectSlug := ProjectSlugFromPath(projectKey)
	legacyProjectSlug := legacyProjectSlugFromPath(projectKey)

	// Resolve the session directory through the same function production
	// uses, so the test and the code under test can never disagree about
	// where files live.
	//   New:                 <base>/test-session/path-to-project/agent.json
	//   Legacy slug fallback: <base>/test-session/project/agent.json
	//   Legacy:              <base>/test-session/agent.json
	baseDir := filepath.Join(getSessionsBaseDir(), sessionName)
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

	// reset clears only this test's session directory between subtests.
	// The containment check is load-bearing: it refuses to delete anything
	// the isolation helper did not redirect into the temp dir (gh#258).
	reset := func() {
		t.Helper()
		if !pathWithin(tmpDir, baseDir) {
			t.Fatalf("refusing to remove %q: outside the test temp dir %q", baseDir, tmpDir)
		}
		if err := os.RemoveAll(baseDir); err != nil {
			t.Fatalf("reset %q: %v", baseDir, err)
		}
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
