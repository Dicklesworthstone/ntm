package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the registered command, not a second rendering/merge implementation.
func TestWorktreesMergeDryRunCommandPreservesPrimary(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "clean"
		if conflict {
			name = "conflicting"
		}
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git is required")
			}
			root := t.TempDir()
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "absent-config"))
			t.Setenv("GIT_CONFIG_COUNT", "0")
			t.Setenv("GIT_AUTHOR_NAME", "NTM Test")
			t.Setenv("GIT_AUTHOR_EMAIL", "ntm-test@example.invalid")
			t.Setenv("GIT_COMMITTER_NAME", "NTM Test")
			t.Setenv("GIT_COMMITTER_EMAIL", "ntm-test@example.invalid")
			savedJSON := jsonOutput
			jsonOutput = false
			t.Cleanup(func() { jsonOutput = savedJSON })
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(root); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chdir(cwd); err != nil {
					t.Error(err)
				}
			})
			run := func(dir string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir = dir
				cmd.WaitDelay = time.Second
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(dir, path, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			run(root, "init", "-b", "main")
			run(root, "config", "commit.gpgsign", "false")
			write(root, ".gitignore", ".ntm/\n")
			write(root, "shared.txt", "base\n")
			run(root, "add", ".gitignore", "shared.txt")
			run(root, "commit", "-m", "base")
			project := GetProjectRoot()
			session := defaultProjectScopedSession(project)
			source := filepath.Join(project, ".ntm", "worktrees", session, "agent")
			run(root, "worktree", "add", "-b", "ntm/"+session+"/agent", source)
			write(source, "shared.txt", "agent work\n")
			run(source, "add", "shared.txt")
			run(source, "commit", "-m", "agent")
			if conflict {
				write(root, "shared.txt", "human work\n")
				run(root, "add", "shared.txt")
				run(root, "commit", "-m", "human")
			}
			head := run(root, "rev-parse", "HEAD")
			var rendered bytes.Buffer
			cmd := newWorktreesMergeCmd()
			cmd.SetArgs([]string{"agent", "--dry-run"})
			cmd.SetOut(&rendered)
			cmd.SetErr(&rendered)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("dry-run command: %v: %s", err, rendered.String())
			}
			want := "Can merge: true"
			if conflict {
				want = "Can merge: false"
				if !strings.Contains(rendered.String(), `Conflict: "shared.txt"`) {
					t.Fatalf("missing conflict path: %s", rendered.String())
				}
			}
			if !strings.Contains(rendered.String(), want) || run(root, "rev-parse", "HEAD") != head || run(root, "status", "--porcelain") != "" {
				t.Fatalf("preview lied or changed the target: %s", rendered.String())
			}
		})
	}
}
