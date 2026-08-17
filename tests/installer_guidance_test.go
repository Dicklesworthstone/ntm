// installer_guidance_test.go — WS7-H7 installer guidance matrix
// (bd-ws7-docs-ux-truth-tqh3l.7).
//
// The 2026-08 reality audit found install.sh (1) never even CHECKS for tmux,
// NTM's one hard runtime dependency, and (2) silently no-ops easy-mode PATH
// setup when no writable rc file exists. The fix adds check_tmux (print the
// platform install command, warn-and-continue — never auto-install) and makes
// easy_mode_path_setup print the exact export line when no rc file exists.
//
// This harness sources install.sh under NTM_INSTALL_SH_NO_MAIN=1 (which
// suppresses the main install run) and exercises the two guidance paths as
// real shell functions — no mocks of the script itself.
package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runInstallShFunc sources install.sh with main suppressed and runs the given
// shell snippet with a controlled HOME and PATH prefix dir.
func runInstallShFunc(t *testing.T, home string, binDir string, snippet string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a bash script; not exercised on windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	root := docsRepoRoot(t)
	script := "set -u\n" +
		"export NTM_INSTALL_SH_NO_MAIN=1\n" +
		"source " + filepath.Join(root, "install.sh") + "\n" +
		snippet + "\n"
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + binDir,
		"NTM_INSTALL_SH_NO_MAIN=1",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell snippet failed: %v\noutput:\n%s", err, out)
	}
	return string(out)
}

// minimalBinDir builds a PATH dir containing only the external tools the
// exercised functions themselves need (uname, grep) — critically WITHOUT
// tmux, so check_tmux sees it missing.
func minimalBinDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"uname", "grep"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found on host", tool)
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	return dir
}

// TestInstallShTmuxMissingGuidance: with tmux absent from PATH, check_tmux
// must warn, print the platform install command, and still return 0
// (warn-and-continue — the binary install itself does not need tmux).
func TestInstallShTmuxMissingGuidance(t *testing.T) {
	home := t.TempDir()
	binDir := minimalBinDir(t)

	out := runInstallShFunc(t, home, binDir, "check_tmux; echo RC=$?")

	if !strings.Contains(out, "tmux not found") {
		t.Errorf("check_tmux with tmux missing printed no warning; output:\n%s", out)
	}
	if !strings.Contains(out, "install tmux") && !strings.Contains(out, "brew install tmux") &&
		!strings.Contains(out, "apt install tmux") && !strings.Contains(out, "dnf install tmux") &&
		!strings.Contains(out, "pacman -S tmux") {
		t.Errorf("check_tmux printed no install command guidance; output:\n%s", out)
	}
	if !strings.Contains(out, "RC=0") {
		t.Errorf("check_tmux must warn-and-continue (exit 0); output:\n%s", out)
	}
}

// TestInstallShTmuxPresentQuiet: with a tmux stub on PATH, check_tmux reports
// it found tmux and emits no warning.
func TestInstallShTmuxPresentQuiet(t *testing.T) {
	home := t.TempDir()
	binDir := minimalBinDir(t)
	stub := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing tmux stub: %v", err)
	}

	out := runInstallShFunc(t, home, binDir, "check_tmux; echo RC=$?")

	if strings.Contains(out, "tmux not found") {
		t.Errorf("check_tmux warned although tmux is on PATH; output:\n%s", out)
	}
	if !strings.Contains(out, "Found tmux") || !strings.Contains(out, "RC=0") {
		t.Errorf("check_tmux did not report found tmux cleanly; output:\n%s", out)
	}
}

// TestInstallShPathSetupNoRcFile: easy-mode PATH setup with NO rc file in
// $HOME must NOT silently no-op — it prints the exact export line.
func TestInstallShPathSetupNoRcFile(t *testing.T) {
	home := t.TempDir() // deliberately empty: no .zshrc, no .bashrc
	binDir := minimalBinDir(t)

	out := runInstallShFunc(t, home, binDir, "easy_mode_path_setup /opt/ntm-test-bin")

	if strings.TrimSpace(out) == "" {
		t.Fatal("easy_mode_path_setup with no rc file produced NO output — the silent no-op the H7 audit flagged")
	}
	if !strings.Contains(out, "Could not update PATH automatically") {
		t.Errorf("expected loud warning about missing rc files; output:\n%s", out)
	}
	if !strings.Contains(out, `export PATH="$PATH:/opt/ntm-test-bin"`) {
		t.Errorf("expected the exact export line for manual setup; output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err == nil {
		t.Error("easy_mode_path_setup created ~/.zshrc; it must only print guidance when no rc file exists")
	}
}

// TestInstallShPathSetupAppendsExistingRc: with a writable ~/.bashrc present,
// easy-mode appends the export line to it and says so.
func TestInstallShPathSetupAppendsExistingRc(t *testing.T) {
	home := t.TempDir()
	binDir := minimalBinDir(t)
	rc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(rc, []byte("# existing rc\n"), 0o644); err != nil {
		t.Fatalf("writing .bashrc: %v", err)
	}

	out := runInstallShFunc(t, home, binDir, "easy_mode_path_setup /opt/ntm-test-bin")

	if !strings.Contains(out, "PATH updated in shell rc files") {
		t.Errorf("expected update confirmation; output:\n%s", out)
	}
	content, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("reading .bashrc back: %v", err)
	}
	if !strings.Contains(string(content), `export PATH="$PATH:/opt/ntm-test-bin"`) {
		t.Errorf(".bashrc was not updated with the export line; content:\n%s", content)
	}
}
