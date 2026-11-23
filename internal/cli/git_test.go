package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestParseConflicts(t *testing.T) {

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

func TestRunGitPullWithoutUpstreamIsFailure(t *testing.T) {
	repo := initGitSyncTestRepo(t)
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "pull", true: "dry-run"}[dryRun], func(t *testing.T) {
			result := runGitPull(context.Background(), repo, dryRun)
			if result.Success {
				t.Fatalf("runGitPull(dryRun=%v) success = true, want failure", dryRun)
			}
			if result.AlreadyUpToDate {
				t.Fatalf("runGitPull(dryRun=%v) reported already up to date without an upstream", dryRun)
			}
			if !strings.Contains(result.Error, "pull distance") {
				t.Fatalf("runGitPull(dryRun=%v) error = %q, want upstream distance failure", dryRun, result.Error)
			}
		})
	}
}

func TestRunGitPullFetchFailureIsFailure(t *testing.T) {
	result := runGitPull(context.Background(), filepath.Join(t.TempDir(), "missing-repository"), false)
	if result.Success || result.AlreadyUpToDate {
		t.Fatalf("runGitPull result = %+v, want fetch failure", result)
	}
	if !strings.Contains(result.Error, "fetch failed") {
		t.Fatalf("runGitPull error = %q, want fetch phase attribution", result.Error)
	}
}

func TestRunGitPushWithoutUpstreamIsFailure(t *testing.T) {
	result := runGitPush(context.Background(), initGitSyncTestRepo(t), false, false)
	if result.Success {
		t.Fatal("runGitPush success = true, want failure")
	}
	if result.NothingToPush {
		t.Fatal("runGitPush reported nothing to push without an upstream")
	}
	if !strings.Contains(result.Error, "upstream") {
		t.Fatalf("runGitPush error = %q, want upstream failure", result.Error)
	}
}

func TestRunGitPullAndPushHonorCanceledContext(t *testing.T) {
	repo := initGitSyncTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if res := runGitPull(ctx, repo, false); res.Success {
		t.Fatalf("runGitPull with canceled context succeeded: %+v", res)
	}
	if res := runGitPush(ctx, repo, false, false); res.Success {
		t.Fatalf("runGitPush with canceled context succeeded: %+v", res)
	}
}

func TestRunGitSyncNonConflictPullFailureIsTerminalJSON(t *testing.T) {
	repo := initGitSyncTestRepo(t)
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	originalJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = originalJSON })

	stdout, runErr := captureStdout(t, func() error {
		return runGitSync(t.Context(), "", true, false, false, false)
	})
	if !errors.Is(runErr, errJSONFailure) {
		t.Fatalf("runGitSync error = %v, want terminal JSON failure", runErr)
	}

	var result GitSyncResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode one git sync document: %v; output=%q", err, stdout)
	}
	if result.Success || result.PullResult == nil || result.PullResult.Success {
		t.Fatalf("git sync result = %+v, want failed pull and top-level failure", result)
	}
	if result.HasConflict {
		t.Fatalf("git sync result = %+v, non-conflict pull failure marked as conflict", result)
	}
	if !strings.Contains(result.Error, "pull failed") {
		t.Fatalf("git sync error = %q, want pull phase attribution", result.Error)
	}
}

func initGitSyncTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	commands := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "ntm-test@example.invalid"},
		{"config", "user.name", "NTM Test"},
	}
	for _, args := range commands {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func TestResolveGitAgentMailProjectKeyUsesSavedSessionAgentProjectKey(t *testing.T) {
	// HOME isolation so the saved session registry lands in a sandbox on
	// macOS (and does not leak into other tests).
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	cfg = &config.Config{ProjectsBase: projectsBase}

	workDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	cwdDir := canonicalTempDir(t)
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	actualProject := filepath.Join(canonicalTempDir(t), "actual-project")
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
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	cfg = &config.Config{ProjectsBase: projectsBase}

	configuredDir := filepath.Join(projectsBase, "mysession")
	if err := os.MkdirAll(configuredDir, 0o755); err != nil {
		t.Fatalf("mkdir configured dir: %v", err)
	}

	cwdDir := canonicalTempDir(t)
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir cwd: %v", err)
	}

	actualProject := filepath.Join(canonicalTempDir(t), "actual-project")
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "mysession", actualProject, "GreenCastle")

	session, workDir, err := resolveGitProjectDir(t.Context(), "mysession")
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

	_, _, err := resolveGitProjectDir(t.Context(), "mysession")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}
