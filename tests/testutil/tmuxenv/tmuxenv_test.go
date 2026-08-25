package tmuxenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOwnedRequiresPatternMatch(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "")
	if Owned() {
		t.Fatal("Owned() = true with empty TMUX_TMPDIR, want false")
	}

	t.Setenv("TMUX_TMPDIR", filepath.Join(t.TempDir(), "not-a-tmux-test-dir"))
	if Owned() {
		t.Fatal("Owned() = true for a TMUX_TMPDIR outside the pattern, want false")
	}

	dir, err := CreateShortTmuxTempDir()
	if err != nil {
		t.Fatalf("CreateShortTmuxTempDir(): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	if !Owned() {
		t.Fatalf("Owned() = false for %q, want true", dir)
	}
}

func TestCandidatesOverrideFirstAndDeduplicated(t *testing.T) {
	t.Setenv(TestTempBaseEnv, os.TempDir()+string(filepath.Separator))

	candidates := Candidates()
	if len(candidates) == 0 {
		t.Fatal("Candidates() returned no candidates")
	}
	want, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", os.TempDir(), err)
	}
	if got := candidates[0]; got.Source != TestTempBaseEnv || got.Path != want {
		t.Fatalf("first candidate = %#v, want source %q path %q", got, TestTempBaseEnv, want)
	}

	count := 0
	for _, candidate := range candidates {
		if candidate.Path == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("candidate %q appears %d times, want exactly once: %#v", want, count, candidates)
	}
}

func TestCandidatesPreferShortUnixBasesToLongTMPDIR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fallback ordering does not apply on Windows")
	}

	t.Setenv(TestTempBaseEnv, "")
	longTempDir := filepath.Join(string(filepath.Separator), strings.Repeat("x", MaxSocketPathBytes))
	t.Setenv("TMPDIR", longTempDir)

	candidates := Candidates()
	if len(candidates) != 3 {
		t.Fatalf("Candidates() = %#v, want /var/tmp, /tmp, and long TMPDIR", candidates)
	}
	if candidates[0].Path != "/var/tmp" || candidates[1].Path != "/tmp" || candidates[2].Path != longTempDir {
		t.Fatalf("candidate order = %#v, want /var/tmp, /tmp, then %q", candidates, longTempDir)
	}
	if err := ValidateBase(candidates[2].Path); err == nil {
		t.Fatalf("long TMPDIR candidate %q passed projected socket-path validation", candidates[2].Path)
	}
}

func TestValidateSocketRootRejectsLongUnixPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain socket path limits do not apply on Windows")
	}

	if err := ValidateSocketRoot(filepath.Join(string(filepath.Separator), "var", "tmp", "ntm")); err != nil {
		t.Fatalf("short root rejected: %v", err)
	}
	longRoot := filepath.Join(string(filepath.Separator), strings.Repeat("x", MaxSocketPathBytes))
	err := ValidateSocketRoot(longRoot)
	if err == nil {
		t.Fatalf("ValidateSocketRoot(%q) succeeded, want path-length error", longRoot)
	}
	if !strings.Contains(err.Error(), "projected tmux socket path") ||
		!strings.Contains(err.Error(), "portable limit") {
		t.Fatalf("ValidateSocketRoot() error = %q, want actionable path-limit details", err)
	}
}

func TestCreateFromCandidatesReportsAllCandidateFailures(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	candidates := []TempDirCandidate{
		{Source: "explicit override", Path: filepath.Join(missingRoot, "one")},
		{Source: "portable fallback", Path: filepath.Join(missingRoot, "two")},
	}

	_, err := CreateFromCandidates(candidates)
	if err == nil {
		t.Fatal("CreateFromCandidates() succeeded, want aggregate error")
	}
	for _, want := range []string{
		"explicit override",
		candidates[0].Path,
		"portable fallback",
		candidates[1].Path,
		TestTempBaseEnv,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("aggregate error %q does not contain %q", err, want)
		}
	}
}

func TestCreateShortTmuxTempDirCreatesValidatedDirectory(t *testing.T) {
	dir, err := CreateShortTmuxTempDir()
	if err != nil {
		t.Fatalf("CreateShortTmuxTempDir(): %v", err)
	}
	defer os.RemoveAll(dir)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat CreateShortTmuxTempDir(): %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("CreateShortTmuxTempDir() = %q, want directory", dir)
	}
	if err := ValidateSocketRoot(dir); err != nil {
		t.Fatalf("CreateShortTmuxTempDir() = %q cannot host a portable tmux socket: %v", dir, err)
	}
	matched, err := filepath.Match(Pattern, filepath.Base(dir))
	if err != nil || !matched {
		t.Fatalf("CreateShortTmuxTempDir() base %q does not match pattern %q", filepath.Base(dir), Pattern)
	}
}
