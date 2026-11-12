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
)

func TestWorktreeCommandORIUnification(t *testing.T) {
	singularCmd, _, err := rootCmd.Find([]string{"worktree"})
	if err != nil {
		t.Fatalf("rootCmd.Find(worktree): %v", err)
	}
	pluralCmd, _, err := rootCmd.Find([]string{"worktrees"})
	if err != nil {
		t.Fatalf("rootCmd.Find(worktrees): %v", err)
	}
	if singularCmd != pluralCmd {
		t.Fatalf("ORI violation: 'ntm worktree' (%p) and 'ntm worktrees' (%p) resolved to different commands", singularCmd, pluralCmd)
	}
	if pluralCmd.Name() != "worktrees" {
		t.Fatalf("canonical command name = %q, want %q", pluralCmd.Name(), "worktrees")
	}

	worktreeRegistrations := 0
	for _, sub := range rootCmd.Commands() {
		if sub.Name() == "worktree" || sub.Name() == "worktrees" {
			worktreeRegistrations++
		}
	}
	if worktreeRegistrations != 1 {
		t.Fatalf("rootCmd registered %d worktree(s) commands, want 1", worktreeRegistrations)
	}

	for _, subName := range []string{"list", "status", "create", "provision", "merge", "merge-status", "merge-recover", "clean", "cleanup", "remove", "rm", "prune"} {
		resolved, _, findErr := pluralCmd.Find([]string{subName})
		if findErr != nil || resolved == nil || resolved == pluralCmd {
			t.Errorf("worktrees subcommand/alias %q failed to resolve: %v", subName, findErr)
		}
	}
}

func TestWorktreeAliasLifecycleAndParity(t *testing.T) {
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
		_ = os.Chdir(cwd)
	})

	runGit := func(dir string, args ...string) string {
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

	runGit(root, "init", "-b", "main")
	runGit(root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".ntm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(root, "add", ".gitignore", "README.md")
	runGit(root, "commit", "-m", "initial")

	execWorktrees := func(args ...string) (string, error) {
		t.Helper()
		cmd := newWorktreesCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(t.Context())
		return buf.String(), err
	}

	// Create via provision alias
	createOut, err := execWorktrees("provision", "cc_1", "cod_1")
	if err != nil {
		t.Fatalf("worktrees provision failed: %v\n%s", err, createOut)
	}
	if !strings.Contains(createOut, "ntm/") || !strings.Contains(createOut, "cc_1") || !strings.Contains(createOut, "cod_1") {
		t.Fatalf("unexpected create output:\n%s", createOut)
	}

	// List via status alias
	statusOut, err := execWorktrees("status")
	if err != nil {
		t.Fatalf("worktrees status failed: %v\n%s", err, statusOut)
	}
	if !strings.Contains(statusOut, "Agent: cc_1") || !strings.Contains(statusOut, "Agent: cod_1") {
		t.Fatalf("status output missing created agents:\n%s", statusOut)
	}

	// Prune dry-run
	pruneOut, err := execWorktrees("prune", "--dry-run")
	if err != nil {
		t.Fatalf("worktrees prune --dry-run failed: %v\n%s", err, pruneOut)
	}

	// Remove without --force in -o json mode must refuse safely
	if _, err := execWorktrees("remove", "cod_1", "-o", "json"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("remove in JSON mode without --force err = %v, want --force refusal", err)
	}

	// Remove with --force via rm alias
	rmOut, err := execWorktrees("rm", "cod_1", "--force")
	if err != nil {
		t.Fatalf("worktrees rm --force failed: %v\n%s", err, rmOut)
	}
	if !strings.Contains(rmOut, "cod_1") {
		t.Fatalf("unexpected rm output:\n%s", rmOut)
	}

	// Clean dry-run previews remaining cc_1 worktree without deleting it
	dryCleanOut, err := execWorktrees("clean", "--dry-run")
	if err != nil {
		t.Fatalf("worktrees clean --dry-run failed: %v\n%s", err, dryCleanOut)
	}
	if !strings.Contains(dryCleanOut, "cc_1") {
		t.Fatalf("clean --dry-run did not list cc_1:\n%s", dryCleanOut)
	}

	// Clean --force removes remaining worktrees
	cleanOut, err := execWorktrees("cleanup", "--force")
	if err != nil {
		t.Fatalf("worktrees cleanup --force failed: %v\n%s", err, cleanOut)
	}
	if !strings.Contains(cleanOut, "Successfully cleaned up 1 worktrees") {
		t.Fatalf("unexpected cleanup output:\n%s", cleanOut)
	}
}

func setupCLIWorktreeGitRepo(t *testing.T) string {
	t.Helper()
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

	runGit := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.WaitDelay = time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")
	return root
}

func TestBeadsCommandRedirectsToWorkAndBr(t *testing.T) {
	cmd := newBeadsCmd()
	if len(cmd.Commands()) != 0 {
		t.Fatalf("beads command has subcommands %v, want none", cmd.Commands())
	}
	if !strings.Contains(cmd.Long, "ntm work") || !strings.Contains(cmd.Long, "br") {
		t.Fatalf("beads Long help should mention both 'ntm work' and 'br':\n%s", cmd.Long)
	}

	savedJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = savedJSON })

	jsonOutput = true
	stdout, err := captureStdout(t, func() error {
		cmd.SetArgs([]string{})
		return cmd.ExecuteContext(t.Context())
	})
	if err != nil {
		t.Fatalf("beads --json failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("unmarshal beads JSON %q: %v", stdout, err)
	}
	if payload["replacement"] != "ntm work" || payload["cli"] != "br" {
		t.Fatalf("unexpected beads JSON payload: %+v", payload)
	}
}
