// Package tmuxenv decides whether the current process already owns an
// isolated tmux test server, and creates the private TMUX_TMPDIR roots that
// make such ownership provable. It has no dependency on internal/tmux so
// both tests/testutil (which does depend on internal/tmux) and internal/tmux's
// own internal test files (which internal/tmux cannot depend on without an
// import cycle) can share one implementation instead of hand-copying it.
package tmuxenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// TestTempBaseEnv names an explicit override for where isolated tmux
	// test roots are created, ahead of the portable fallbacks.
	TestTempBaseEnv = "NTM_TMUX_TEST_TMPDIR"

	// MaxSocketPathBytes is the sockaddr_un.sun_path budget. It is only 104
	// bytes on several supported Unix platforms; keeping the projected
	// pathname below 100 bytes, including its terminating NUL, means a test
	// root that works on Linux also works on BSD and macOS.
	MaxSocketPathBytes = 100

	// Pattern is the glob every tmux test temp directory is created with. A
	// TMUX_TMPDIR matching it is the only proof that this process (or one it
	// re-exec'd) created it, rather than inheriting an ambient one.
	Pattern = "ntm-tmux-test-*"
)

// Owned reports whether TMUX_TMPDIR is set and its base name matches
// Pattern. NTM_TEST_TMUX_ENV_OWNED is trustworthy only when Owned is also
// true; the flag by itself proves nothing about who set TMUX_TMPDIR.
func Owned() bool {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		return false
	}
	matched, err := filepath.Match(Pattern, filepath.Base(dir))
	return err == nil && matched
}

// TempDirCandidate is one base directory considered for an isolated tmux
// test root, tagged with where it came from (for diagnostics).
type TempDirCandidate struct {
	Source string
	Path   string
}

// CreateShortTmuxTempDir creates a private TMUX_TMPDIR whose projected
// default socket pathname fits conservative Unix-domain socket limits.
func CreateShortTmuxTempDir() (string, error) {
	return CreateFromCandidates(Candidates())
}

// CreateFromCandidates tries each candidate base directory in order,
// skipping any whose projected socket path would exceed MaxSocketPathBytes,
// and returns the first isolated root it can create.
func CreateFromCandidates(candidates []TempDirCandidate) (string, error) {
	var failures []string
	for _, candidate := range candidates {
		if err := ValidateBase(candidate.Path); err != nil {
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.Source, candidate.Path, err))
			continue
		}

		dir, err := os.MkdirTemp(candidate.Path, Pattern)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.Source, candidate.Path, err))
			continue
		}
		if err := ValidateSocketRoot(dir); err != nil {
			cleanupErr := os.Remove(dir)
			if cleanupErr != nil {
				err = fmt.Errorf("%w (also could not remove rejected directory %q: %v)", err, dir, cleanupErr)
			}
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.Source, candidate.Path, err))
			continue
		}
		return dir, nil
	}

	if len(failures) == 0 {
		failures = append(failures, "no candidate directories were configured")
	}
	return "", fmt.Errorf(
		"create tmux test directory: no writable base can produce a portable socket path; "+
			"set %s to a short writable directory; attempts: %s",
		TestTempBaseEnv,
		strings.Join(failures, "; "),
	)
}

// Candidates lists the base directories to try, in preference order: an
// explicit TestTempBaseEnv override, then portable Unix fallbacks, then
// os.TempDir(). Duplicate resolved paths are removed.
func Candidates() []TempDirCandidate {
	raw := []TempDirCandidate{
		{Source: TestTempBaseEnv, Path: os.Getenv(TestTempBaseEnv)},
	}
	if runtime.GOOS != "windows" {
		raw = append(raw,
			TempDirCandidate{Source: "portable fallback", Path: "/var/tmp"},
			TempDirCandidate{Source: "portable fallback", Path: "/tmp"},
		)
	}
	raw = append(raw, TempDirCandidate{Source: "os.TempDir fallback", Path: os.TempDir()})

	seen := make(map[string]struct{}, len(raw))
	candidates := make([]TempDirCandidate, 0, len(raw))
	for _, candidate := range raw {
		if candidate.Path == "" {
			continue
		}
		path, err := filepath.Abs(candidate.Path)
		if err == nil {
			candidate.Path = path
		} else {
			candidate.Path = filepath.Clean(candidate.Path)
		}
		key := candidate.Path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// ValidateBase reports whether base can host a portable tmux socket path
// once Go expands Pattern's '*' into a MkdirTemp suffix.
func ValidateBase(base string) error {
	// Go currently replaces '*' with a ten-digit random suffix. Reserving
	// twenty digits avoids depending on that implementation detail.
	projectedRoot := filepath.Join(base, "ntm-tmux-test-18446744073709551615")
	return ValidateSocketRoot(projectedRoot)
}

// ValidateSocketRoot reports whether tmux's default socket path under root
// fits within MaxSocketPathBytes.
func ValidateSocketRoot(root string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	// tmux's default socket is $TMUX_TMPDIR/tmux-$UID/default. Reserve the
	// largest decimal uint64 UID even though supported Unix systems use
	// narrower uid_t values.
	projected := filepath.Join(root, "tmux-18446744073709551615", "default")
	length := len([]byte(projected)) + 1 // sockaddr_un requires a trailing NUL.
	if length > MaxSocketPathBytes {
		return fmt.Errorf(
			"projected tmux socket path %q needs %d bytes (portable limit %d)",
			projected,
			length,
			MaxSocketPathBytes,
		)
	}
	return nil
}
