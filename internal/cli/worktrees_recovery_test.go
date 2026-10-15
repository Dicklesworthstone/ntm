package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/worktrees"
)

func TestWorktreesRegisteredRecoveryCommands(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Error(err)
		}
	})
	git := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run := func(args ...string) (string, error) {
		cmd := newWorktreesCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), err
	}
	git(root, "init", "-b", "main")
	git(root, "config", "user.name", "NTM Test")
	git(root, "config", "user.email", "ntm@example.invalid")
	git(root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".ntm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".gitignore")
	git(root, "commit", "-m", "base")
	project := GetProjectRoot()
	session := defaultProjectScopedSession(project)
	source := filepath.Join(project, ".ntm", "worktrees", session, "agent")
	git(root, "worktree", "add", "-b", "ntm/"+session+"/agent", source)
	if err := os.WriteFile(filepath.Join(source, "delivered.txt"), []byte("agent work"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "delivered.txt")
	git(source, "commit", "-m", "agent")
	if text, err := run("merge-status"); err != nil || !strings.Contains(text, "No recorded merge") {
		t.Fatalf("status without a receipt: %s %v", text, err)
	}
	if text, err := run("merge", "agent"); err != nil {
		t.Fatalf("registered merge failed: %s %v", text, err)
	}
	head := git(root, "rev-parse", "HEAD")
	manager := worktrees.NewManager(project, session)
	record, err := manager.InspectMerge(context.Background())
	if err != nil || record == nil {
		t.Fatalf("missing receipt: %+v %v", record, err)
	}
	// Preserve a complete Git commit but simulate loss of its final journal save.
	record.State, record.Commit = "prepared", ""
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".git", "ntm-merge.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if text, err := run("merge-status"); err != nil || !strings.Contains(text, record.AttemptID) || !strings.Contains(text, "Outcome unknown") {
		t.Fatalf("uncertain receipt missing from status: %s %v", text, err)
	}
	if _, err := run("merge-recover", strings.Repeat("0", 32)); err == nil {
		t.Fatal("wrong attempt ID accepted")
	}
	if text, err := run("merge-recover", record.AttemptID); err != nil || !strings.Contains(text, "State: recovered") || !strings.Contains(text, head) {
		t.Fatalf("registered recovery did not reconcile Git evidence: %s %v", text, err)
	}
	if git(root, "rev-parse", "HEAD") != head {
		t.Fatal("recovery reran the merge")
	}
	for _, args := range [][]string{{"merge-status", "extra"}, {"merge-recover"}, {"merge-recover", record.AttemptID, "--force"}} {
		if _, err := run(args...); err == nil {
			t.Fatalf("invalid recovery controls accepted: %v", args)
		}
	}
}
