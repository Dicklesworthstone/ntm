package cli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initCLITestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for _, args := range [][]string{
		{"init", dir},
		{"-C", dir, "config", "user.email", "ntm-cli-test@example.com"},
		{"-C", dir, "config", "user.name", "NTM CLI Test"},
		{"-C", dir, "commit", "--allow-empty", "-m", "initial commit"},
		{"-C", dir, "branch", "-M", "main"},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	return dir
}

func captureCLIStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := fn()

	_ = w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out), runErr
}

func execHooksCLI(t *testing.T, asJSON bool, args ...string) (string, error) {
	t.Helper()
	prevJSON := jsonOutput
	jsonOutput = asJSON
	t.Cleanup(func() { jsonOutput = prevJSON })

	return captureCLIStdout(t, func() error {
		cmd := newHooksCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(args)
		return cmd.Execute()
	})
}

func TestHooksCLIInstallStatusUninstallAllFiveTypes(t *testing.T) {
	installHelp := newHooksInstallCmd().Long
	for _, hookName := range []string{"pre-commit", "pre-push", "commit-msg", "post-commit", "post-checkout"} {
		if !strings.Contains(installHelp, hookName) {
			t.Fatalf("newHooksInstallCmd Long help missing %q:\n%s", hookName, installHelp)
		}
	}

	repo := initCLITestGitRepo(t)
	t.Chdir(repo)

	binDir := t.TempDir()
	fakeNTM := filepath.Join(binDir, "ntm")
	if err := os.WriteFile(fakeNTM, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	allHooks := []string{"pre-commit", "pre-push", "commit-msg", "post-commit", "post-checkout"}
	for _, ht := range allHooks {
		out, err := execHooksCLI(t, true, "install", ht)
		if err != nil {
			t.Fatalf("hooks install %s: %v\nout: %s", ht, err, out)
		}
	}

	statusOut, err := execHooksCLI(t, true, "status")
	if err != nil {
		t.Fatalf("hooks status: %v\nout: %s", err, statusOut)
	}
	var statusPayload struct {
		Hooks []struct {
			Type      string `json:"type"`
			Installed bool   `json:"installed"`
			IsNTM     bool   `json:"is_ntm"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(statusOut), &statusPayload); err != nil {
		t.Fatalf("decode hooks status JSON: %v\nraw: %s", err, statusOut)
	}
	if len(statusPayload.Hooks) != 5 {
		t.Fatalf("expected 5 hooks in status, got %d", len(statusPayload.Hooks))
	}
	for _, h := range statusPayload.Hooks {
		if !h.Installed || !h.IsNTM {
			t.Fatalf("expected hook %s installed by ntm, got %+v", h.Type, h)
		}
	}

	for _, ht := range allHooks {
		if out, err := execHooksCLI(t, true, "uninstall", ht); err != nil {
			t.Fatalf("hooks uninstall %s: %v\nout: %s", ht, err, out)
		}
	}
}

func TestHooksCLIRunAllManagedHooksHumanAndJSON(t *testing.T) {
	repo := initCLITestGitRepo(t)
	t.Chdir(repo)

	// Stub br so pre-push cycle checks are deterministic.
	binDir := t.TempDir()
	fakeBR := filepath.Join(binDir, "br")
	if err := os.WriteFile(fakeBR, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// 1. pre-commit (no staged files) passes in JSON mode.
	out, err := execHooksCLI(t, true, "run", "pre-commit")
	if err != nil {
		t.Fatalf("hooks run pre-commit (JSON): %v\nout: %s", err, out)
	}
	if !strings.Contains(out, `"passed":true`) {
		t.Fatalf("expected passed:true for pre-commit, got: %s", out)
	}

	// 2. commit-msg with explicit positional $1 file passes in human & JSON modes, and fails on empty message.
	msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte("feat(hooks): test commit message hook\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	humanOut, err := execHooksCLI(t, false, "run", "commit-msg", msgFile)
	if err != nil || !strings.Contains(humanOut, "passed") {
		t.Fatalf("hooks run commit-msg (human): err=%v out=%s", err, humanOut)
	}
	jsonOut, err := execHooksCLI(t, true, "run", "commit-msg", msgFile)
	if err != nil || !strings.Contains(jsonOut, `"hook_type":"commit-msg"`) {
		t.Fatalf("hooks run commit-msg (JSON): err=%v out=%s", err, jsonOut)
	}

	emptyMsg := filepath.Join(t.TempDir(), "EMPTY_MSG")
	if err := os.WriteFile(emptyMsg, []byte("# only comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failOut, err := execHooksCLI(t, true, "run", "commit-msg", emptyMsg)
	if err == nil || !strings.Contains(failOut, `"passed":false`) {
		t.Fatalf("expected commit-msg empty failure, got err=%v out=%s", err, failOut)
	}

	// 3. Create dirty .beads and exercise pre-push, post-commit, and post-checkout (with git's 3 positional args).
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prePushOut, err := execHooksCLI(t, true, "run", "pre-push", "origin", "https://example.com/repo.git")
	if err != nil || !strings.Contains(prePushOut, `"hook_type":"pre-push"`) {
		t.Fatalf("hooks run pre-push (JSON): err=%v out=%s", err, prePushOut)
	}

	postCommitOut, err := execHooksCLI(t, true, "run", "post-commit")
	if err != nil || !strings.Contains(postCommitOut, "uncommitted .beads changes remain after commit") {
		t.Fatalf("hooks run post-commit (JSON): err=%v out=%s", err, postCommitOut)
	}

	postCheckoutOut, err := execHooksCLI(t, true, "run", "post-checkout", "0000", "1111", "1")
	if err != nil || !strings.Contains(postCheckoutOut, ".beads has uncommitted changes") {
		t.Fatalf("hooks run post-checkout (JSON): err=%v out=%s", err, postCheckoutOut)
	}

	// 4. Unknown hook type returns non-zero error.
	if _, err := execHooksCLI(t, true, "run", "not-a-hook"); err == nil {
		t.Fatal("expected error for unknown hook type")
	}
}
