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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestInstallShReleaseResolution runs the real installer entry point with
// deterministic transport responses. Archives, SHA256 verification, extraction,
// and installation are real; this is an offline regression, not native release
// qualification through DSR.
func TestInstallShReleaseResolution(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("installer fixtures exercise supported Unix platforms")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	binDir := t.TempDir()
	for _, tool := range []string{"awk", "cat", "chmod", "cp", "grep", "gzip", "head", "mkdir", "mktemp", "mv", "rm", "sed", "tar", "tr", "uname"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(src, filepath.Join(binDir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	checksumTool := "sha256sum"
	checksumExecutable, err := exec.LookPath(checksumTool)
	if err != nil {
		checksumTool = "shasum"
		checksumExecutable, err = exec.LookPath(checksumTool)
		if err != nil {
			t.Skip("SHA256 command not available")
		}
	}
	if err := os.Symlink(checksumExecutable, filepath.Join(binDir, checksumTool)); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit jq, Python, and other JSON runtimes from PATH.
	curl := `#!/bin/sh
set -eu
out=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        -w) shift 2 ;;
        -*) shift ;;
        *) url="$1"; shift ;;
    esac
done
printf '%s\n' "$url" >> "$NTM_TEST_INSTALL_LOG"
repo=https://github.com/Dicklesworthstone/ntm
api=https://api.github.com/repos/Dicklesworthstone/ntm
case "$url" in
    "$repo/releases/latest")
        [ "$NTM_TEST_INSTALL_REDIRECT" = 1 ] || exit 22
        printf '%s/releases/tag/%s' "$repo" "$NTM_TEST_INSTALL_TAG"
        ;;
    "$api/releases/latest"|"$api/releases/tags/$NTM_TEST_INSTALL_TAG")
        cat "$NTM_TEST_INSTALL_JSON"
        ;;
    "$repo/releases/download/$NTM_TEST_INSTALL_TAG/$NTM_TEST_INSTALL_ASSET")
        [ "$out" = /dev/null ] || cp "$NTM_TEST_INSTALL_ARCHIVE" "$out"
        ;;
    "$repo/releases/download/$NTM_TEST_INSTALL_TAG/SHA256SUMS")
        [ "$NTM_TEST_INSTALL_CHECKSUM" != missing ] || exit 22
        cp "$NTM_TEST_INSTALL_CHECKSUM" "$out"
        ;;
    *) exit 22 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	const tag = "v1.2.3"
	asset := "ntm_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	binary := []byte("#!/bin/sh\n[ \"$1\" = version ] || exit 1\nprintf '%s\\n' 'ntm version v1.2.3'\n")
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "ntm", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), asset)
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
	compact := fmt.Sprintf(`{"author":{"tag_name":"v9.9.9"},"tag_name":"%s","assets":[{"name":"%s"}],"name":"later release title","body":"later prose with \"tag_name\":\"v8.8.8\" and braces {}[]"}`, tag, asset)
	cases := []struct {
		name     string
		json     string
		redirect bool
		pinned   string
		checksum string
		wantErr  string
	}{
		{name: "compact API fallback", json: compact},
		{name: "pretty API fallback", json: fmt.Sprintf("{\n  \"tag_name\" \n : \n \"%s\",\n  \"assets\": [{\"name\":\"%s\"}],\n  \"body\": \"a later string\"\n}\n", tag, asset)},
		{name: "large compact response drains input", json: fmt.Sprintf(`{"tag_name":"%s","assets":[{"name":"%s"}],"body":"%s"}`, tag, asset, strings.Repeat("release notes ", 16384))},
		{name: "large prose before tag", json: fmt.Sprintf(`{"body":"%s","tag_name":"%s","assets":[{"name":"%s"}]}`, strings.Repeat(`release notes with \"quotes\" and {}[] `, 4096), tag, asset)},
		{name: "redirect succeeds without latest API", json: compact, redirect: true},
		{name: "pinned version skips latest lookup", json: compact, pinned: "1.2.3"},
		{name: "missing tag cannot use prose", json: `{"name":"v1.2.3","body":"\"tag_name\":\"v1.2.3\""}`, wantErr: "Could not determine latest version"},
		{name: "nested tag cannot supply release version", json: `{"metadata":[{"tag_name":"v1.2.3"}],"name":"v1.2.3"}`, wantErr: "Could not determine latest version"},
		{name: "null tag", json: `{"tag_name":null,"name":"v1.2.3"}`, wantErr: "Could not determine latest version"},
		{name: "invalid tag cannot use later version", json: `{"tag_name":"main","body":"v1.2.3"}`, wantErr: "Invalid version: main"},
		{name: "escaped tag remains invalid", json: `{"tag_name":"v1.\u0032.3","body":"v1.2.3"}`, wantErr: "Invalid version:"},
		{name: "mismatched checksum blocks install", json: compact, checksum: "mismatch", wantErr: "Checksum mismatch"},
		{name: "missing checksum entry blocks install", json: compact, checksum: "wrong asset", wantErr: "No SHA256 checksum found"},
		{name: "missing checksums block install", json: compact, checksum: "missing", wantErr: "Could not download release checksums"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			installDir := filepath.Join(dir, "installed")
			if err := os.Mkdir(installDir, 0o755); err != nil {
				t.Fatal(err)
			}
			const previous = "previous installed binary\n"
			dest := filepath.Join(installDir, "ntm")
			if err := os.WriteFile(dest, []byte(previous), 0o755); err != nil {
				t.Fatal(err)
			}
			jsonPath := filepath.Join(dir, "release.json")
			if err := os.WriteFile(jsonPath, []byte(tc.json), 0o644); err != nil {
				t.Fatal(err)
			}
			checksumPath := filepath.Join(dir, "SHA256SUMS")
			checksum := digest + "  " + asset + "\n"
			switch tc.checksum {
			case "mismatch":
				checksum = strings.Repeat("0", 64) + "  " + asset + "\n"
			case "wrong asset":
				checksum = digest + "  another-asset.tar.gz\n"
			case "missing":
				checksumPath = "missing"
			}
			if checksumPath != "missing" {
				if err := os.WriteFile(checksumPath, []byte(checksum), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			logPath := filepath.Join(dir, "requests.log")
			args := []string{filepath.Join(docsRepoRoot(t), "install.sh"), "--dir=" + installDir, "--no-shell"}
			if tc.pinned != "" {
				args = append(args, "--version="+tc.pinned)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, args...)
			redirect := "0"
			if tc.redirect {
				redirect = "1"
			}
			cmd.Env = []string{
				"HOME=" + dir, "PATH=" + binDir, "TMPDIR=" + dir,
				"NTM_TEST_INSTALL_LOG=" + logPath,
				"NTM_TEST_INSTALL_REDIRECT=" + redirect,
				"NTM_TEST_INSTALL_TAG=" + tag,
				"NTM_TEST_INSTALL_ASSET=" + asset,
				"NTM_TEST_INSTALL_ARCHIVE=" + archivePath,
				"NTM_TEST_INSTALL_JSON=" + jsonPath,
				"NTM_TEST_INSTALL_CHECKSUM=" + checksumPath,
			}
			out, err := cmd.CombinedOutput()
			if tc.wantErr == "" {
				if err != nil || !strings.Contains(string(out), "Installation complete!") {
					t.Fatalf("installer failed: %v\n%s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), tc.wantErr) {
				t.Fatalf("installer error = %v, want %q\n%s", err, tc.wantErr, out)
			}
			installed, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr == "" {
				if !bytes.Equal(installed, binary) {
					t.Fatal("installer did not place the checksum-verified payload at the destination")
				}
			} else if string(installed) != previous {
				t.Fatal("failed installation replaced the existing binary")
			}
			requests, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(requests)
			const latestAPI = "https://api.github.com/repos/Dicklesworthstone/ntm/releases/latest"
			if got := strings.Contains(log, latestAPI); got != (!tc.redirect && tc.pinned == "") {
				t.Errorf("latest API fallback = %v\n%s", got, log)
			}
			if tc.pinned == "" {
				if !strings.HasPrefix(log, "https://github.com/Dicklesworthstone/ntm/releases/latest\n") {
					t.Errorf("latest resolution did not try the redirect first:\n%s", log)
				}
			} else if strings.Contains(log, "/releases/latest") {
				t.Errorf("pinned install requested the latest release:\n%s", log)
			}
			if tc.wantErr != "" && tc.checksum == "" && strings.Contains(log, "/releases/download/") {
				t.Errorf("installer downloaded assets before validating the version:\n%s", log)
			}
			if tc.checksum == "mismatch" || tc.checksum == "wrong asset" {
				if strings.Contains(log, "/checksums.txt") || strings.Contains(log, ".sha256") {
					t.Errorf("installer tried a different checksum after an authoritative verification failure:\n%s", log)
				}
			}
		})
	}
}
