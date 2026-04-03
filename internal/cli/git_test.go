package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestParseConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		output   string
		expected []string
	}{
		{
			name:     "no conflicts",
			output:   "Already up to date.\n",
			expected: nil,
		},
		{
			name:     "empty string",
			output:   "",
			expected: nil,
		},
		{
			name: "single conflict",
			output: `Auto-merging file.go
CONFLICT (content): Merge conflict in file.go
Automatic merge failed; fix conflicts and then commit the result.`,
			expected: []string{"CONFLICT (content): Merge conflict in file.go"},
		},
		{
			name: "multiple conflicts",
			output: `Auto-merging internal/cli/root.go
CONFLICT (content): Merge conflict in internal/cli/root.go
Auto-merging internal/cli/spawn.go
CONFLICT (content): Merge conflict in internal/cli/spawn.go
CONFLICT (modify/delete): internal/cli/old.go deleted in HEAD and modified in feature.
Automatic merge failed; fix conflicts and then commit the result.`,
			expected: []string{
				"CONFLICT (content): Merge conflict in internal/cli/root.go",
				"CONFLICT (content): Merge conflict in internal/cli/spawn.go",
				"CONFLICT (modify/delete): internal/cli/old.go deleted in HEAD and modified in feature.",
			},
		},
		{
			name: "add/add conflict",
			output: `CONFLICT (add/add): Merge conflict in newfile.go
Auto-merging newfile.go`,
			expected: []string{"CONFLICT (add/add): Merge conflict in newfile.go"},
		},
		{
			name:     "no CONFLICT prefix",
			output:   "conflict in the message but not at start\n",
			expected: nil,
		},
		{
			name: "mixed output with conflicts",
			output: `Updating abc1234..def5678
Fast-forward
 file1.go | 10 ++++++++++
 1 file changed, 10 insertions(+)
Already up to date.
Then some more text.
CONFLICT (content): Merge conflict in critical.go
More text after conflict.`,
			expected: []string{"CONFLICT (content): Merge conflict in critical.go"},
		},
		{
			name:     "whitespace only",
			output:   "   \n\t\n   ",
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := parseConflicts(tc.output)

			if len(result) != len(tc.expected) {
				t.Fatalf("parseConflicts() returned %d conflicts; want %d\nGot: %v\nWant: %v",
					len(result), len(tc.expected), result, tc.expected)
			}

			for i, conflict := range result {
				if conflict != tc.expected[i] {
					t.Errorf("parseConflicts()[%d] = %q; want %q", i, conflict, tc.expected[i])
				}
			}
		})
	}
}

func TestResolveGitAgentMailProjectKeyUsesSavedSessionAgentProjectKey(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	workDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	actualProject := filepath.Join(t.TempDir(), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	projectKey := resolveGitAgentMailProjectKey("mysession", workDir)
	if projectKey != actualProject {
		t.Fatalf("resolveGitAgentMailProjectKey() = %q, want saved session agent project %q", projectKey, actualProject)
	}
}

func TestResolveGitProjectDirUsesSavedSessionAgentProjectKey(t *testing.T) {
	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	configuredDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(configuredDir, 0o755); err != nil {
		t.Fatalf("mkdir configured dir: %v", err)
	}

	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	actualProject := filepath.Join(t.TempDir(), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	session, workDir, err := resolveGitProjectDir("mysession")
	if err != nil {
		t.Fatalf("resolveGitProjectDir() error = %v", err)
	}
	if session != "mysession" {
		t.Fatalf("resolveGitProjectDir() session = %q, want mysession", session)
	}
	if workDir != actualProject {
		t.Fatalf("resolveGitProjectDir() workDir = %q, want saved session agent project %q", workDir, actualProject)
	}
}

func TestResolveGitProjectDirRejectsWorkspaceFallbackForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir workspace git dir: %v", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	_, _, err := resolveGitProjectDir("mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}
