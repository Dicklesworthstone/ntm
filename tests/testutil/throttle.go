package testutil

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TimeoutScaleEnv multiplies every test time budget passed through
// ScaleTimeout. Set it when running the suite on a loaded machine.
//
// Several tests assert on wall-clock budgets they cannot avoid: they spawn a
// subprocess, or wait for another goroutine to create a sentinel file. Those
// budgets were fixed constants chosen against an idle machine, so on a box
// that is also running an agent swarm they fail while the code under test is
// perfectly correct — training everyone to ignore a red suite. Scaling is the
// honest knob: the assertions keep their meaning, the deadlines just stop
// pretending the machine is idle.
//
//	NTM_TEST_TIMEOUT_SCALE=4 go test -short ./...
const TimeoutScaleEnv = "NTM_TEST_TIMEOUT_SCALE"

const (
	tmuxTestTempBaseEnv = "NTM_TMUX_TEST_TMPDIR"

	// sockaddr_un.sun_path is only 104 bytes on several supported Unix
	// platforms. Keep the projected pathname below 100 bytes, including its
	// terminating NUL, so a test root that works on Linux also works on BSD and
	// macOS.
	maxTmuxSocketPathBytes = 100
	tmuxTempDirPattern     = "ntm-tmux-test-*"
)

type tmuxTempDirCandidate struct {
	source string
	path   string
}

// TmuxTestThrottle limits concurrent tmux session spawning in tests.
// This prevents fork bombs when running tests with high parallelism.
//
// The default limit is 8 concurrent tmux-spawning tests, which is safe
// even on systems with lower process limits. Override with NTM_TEST_PARALLEL.
var TmuxTestThrottle = newThrottle(getTmuxTestLimit())

func getTmuxTestLimit() int {
	if env := os.Getenv("NTM_TEST_PARALLEL"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			return n
		}
	}
	// Default to 8, or GOMAXPROCS/8 if that's larger, capped at 16
	limit := runtime.GOMAXPROCS(0) / 8
	if limit < 8 {
		limit = 8
	}
	if limit > 16 {
		limit = 16
	}
	return limit
}

// throttle is a counting semaphore for limiting concurrent operations.
type throttle struct {
	sem chan struct{}
	mu  sync.Mutex
}

func newThrottle(limit int) *throttle {
	return &throttle{
		sem: make(chan struct{}, limit),
	}
}

// Acquire acquires a slot from the throttle, blocking if necessary.
// Returns a release function that must be called when done.
func (th *throttle) Acquire() func() {
	th.sem <- struct{}{}
	return func() {
		<-th.sem
	}
}

// AcquireForTest acquires a slot and registers cleanup to release it.
// This is the recommended way to use the throttle in tests.
func (th *throttle) AcquireForTest(t *testing.T) {
	t.Helper()
	th.sem <- struct{}{}
	t.Cleanup(func() {
		<-th.sem
	})
}

// RequireTmuxThrottled combines RequireTmux with throttle acquisition.
// Use this at the start of any test that spawns tmux sessions.
//
// Example:
//
//	func TestSpawnSession(t *testing.T) {
//	    testutil.RequireTmuxThrottled(t)
//	    // ... test code that spawns tmux sessions
//	}
func RequireTmuxThrottled(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real tmux session integration in short mode")
	}
	RequireTmux(t)
	// Cross-process lock to prevent tmux overload when `go test ./...` runs
	// multiple packages in parallel.
	acquireGlobalTmuxTestLock(t)
	TmuxTestThrottle.AcquireForTest(t)
}

// AcquireGlobalTmuxTestLockForTest serializes tmux access across package test
// processes without skipping short-mode coverage or requiring a live session.
func AcquireGlobalTmuxTestLockForTest(t *testing.T) {
	t.Helper()
	acquireGlobalTmuxTestLock(t)
}

// IsolateTmuxTestProcess gives a package test binary its own tmux server and
// IsolateGitConfigProcess points git's global and system configuration at
// empty process-private locations so neither tests nor code under test that
// shells out to `git` can read or write the developer's real git config. A
// real-machine incident (#225): a global core.hooksPath redirect routed
// repo-scoped test hook installs into the user's actual global hooks
// directory, where every repository on the machine executed them. Call from
// TestMain before m.Run(); the returned cleanup removes the private dir.
// GIT_CONFIG_GLOBAL may name a missing file — git treats it as empty.
func IsolateGitConfigProcess() (func() error, error) {
	dir, err := os.MkdirTemp("", "ntm-test-gitconfig-")
	if err != nil {
		return nil, fmt.Errorf("create private git config dir: %w", err)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig")); err != nil {
		return nil, errors.Join(fmt.Errorf("set GIT_CONFIG_GLOBAL: %w", err), os.RemoveAll(dir))
	}
	if err := os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull); err != nil {
		return nil, errors.Join(fmt.Errorf("set GIT_CONFIG_SYSTEM: %w", err), os.RemoveAll(dir))
	}
	return func() error { return os.RemoveAll(dir) }, nil
}

// returns an idempotent cleanup function. TestMain callers must run cleanup
// before os.Exit so the private server and its short socket root do not leak.
// NTM_TEST_TMUX_ENV_OWNED marks a helper process whose caller owns its tmux
// environment; isolation is a no-op so fake-binary contract tests stay intact.
func IsolateTmuxTestProcess() (func() error, error) {
	if os.Getenv("NTM_TEST_TMUX_ENV_OWNED") == "1" {
		return func() error { return nil }, nil
	}

	dir, err := createShortTmuxTempDir()
	if err != nil {
		return nil, err
	}

	settings := []struct {
		key   string
		value string
	}{
		{key: "TMUX", value: ""},
		{key: "TMUX_PANE", value: ""},
		{key: "TMUX_TMPDIR", value: dir},
	}
	for _, setting := range settings {
		if err := os.Setenv(setting.key, setting.value); err != nil {
			removeErr := os.RemoveAll(dir)
			return nil, errors.Join(
				fmt.Errorf("set %s for tmux test isolation: %w", setting.key, err),
				removeErr,
			)
		}
	}
	cleanupBinary := findSystemTmuxBinary()

	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			if cleanupBinary != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				cmd := exec.CommandContext(ctx, cleanupBinary, "kill-server")
				cmd.Env = isolatedTmuxEnvironment(dir)
				output, err := cmd.CombinedOutput()
				contextErr := ctx.Err()
				cancel()
				if err != nil {
					wrapped := fmt.Errorf(
						"%s kill-server: %w: %s",
						cleanupBinary,
						errors.Join(err, contextErr),
						strings.TrimSpace(string(output)),
					)
					class := tmux.ClassifyCommandError(wrapped)
					if class.Kind != tmux.CommandErrorNoServer {
						cleanupErr = fmt.Errorf("stop isolated tmux server: %w", wrapped)
					}
				}
			}
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(dir))
		})
		return cleanupErr
	}
	return cleanup, nil
}

func findSystemTmuxBinary() string {
	candidates := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
		"/bin/tmux",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "tmux"))
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	if strings.TrimSpace(os.Getenv("NTM_TMUX_BINARY")) == "" {
		if path, err := exec.LookPath("tmux"); err == nil {
			return path
		}
	}
	return ""
}

func isolatedTmuxEnvironment(root string) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TMUX_TMPDIR":
			continue
		default:
			env = append(env, entry)
		}
	}
	return append(env, "TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="+root)
}

// ShortTmuxTempDir creates a per-test TMUX_TMPDIR whose projected default
// socket pathname fits conservative Unix-domain socket limits. Set
// NTM_TMUX_TEST_TMPDIR to put these roots on an explicitly chosen filesystem.
// The directory and all tmux artifacts beneath it are removed during cleanup.
func ShortTmuxTempDir(t *testing.T) string {
	t.Helper()

	dir, err := createShortTmuxTempDir()
	if err != nil {
		t.Fatalf("create short tmux temp directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove short tmux temp directory %q: %v", dir, err)
		}
	})
	return dir
}

// ReapStaleTmuxTestServers kills isolated tmux test servers that leaked from a
// test binary killed mid-run. On a normal pass or fail the per-test cleanup and
// the TestMain cleanup stop each private server and remove its socket root, but
// a SIGKILL — an interrupted suite, an OOM, a host reboot — runs neither, so the
// detached tmux server and its "ntm-tmux-test-*" socket directory survive and
// accumulate. On a small-core host each leaked server is a standing contribution
// to process count and load, on exactly the metric that gates test parallelism,
// and it grows with every run.
//
// Every isolated server keeps its socket under a "ntm-tmux-test-*" directory in
// one of the short candidate bases: the process-wide server from
// IsolateTmuxTestProcess and every per-fixture server from ShortTmuxTempDir
// alike. This scans those same bases and, for each such directory whose mtime is
// older than minAge, kills the server on that socket and removes the directory.
// The age guard is load-bearing: a concurrent in-flight run's socket root is
// recent, so it — and the socket root this process just created for itself — is
// never touched. Call it from a package TestMain before m.Run() so leaks cannot
// accumulate across runs. It is best-effort by design: it never fails a suite,
// and returns the number of socket directories it removed.
func ReapStaleTmuxTestServers(minAge time.Duration) int {
	prefix := strings.TrimSuffix(tmuxTempDirPattern, "*")
	bin := findSystemTmuxBinary()
	now := time.Now()
	reaped := 0
	seen := make(map[string]struct{})
	for _, candidate := range shortTmuxTempDirCandidates() {
		entries, err := os.ReadDir(candidate.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			dir := filepath.Join(candidate.path, entry.Name())
			if _, dup := seen[dir]; dup {
				continue
			}
			seen[dir] = struct{}{}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) < minAge {
				continue // recent — may belong to an in-flight run
			}
			if bin != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				cmd := exec.CommandContext(ctx, bin, "kill-server")
				cmd.Env = isolatedTmuxEnvironment(dir)
				_ = cmd.Run() // best-effort; a dead or empty socket yields "no server"
				cancel()
			}
			if err := os.RemoveAll(dir); err == nil {
				reaped++
			}
		}
	}
	return reaped
}

func createShortTmuxTempDir() (string, error) {
	return createShortTmuxTempDirFromCandidates(shortTmuxTempDirCandidates())
}

func createShortTmuxTempDirFromCandidates(candidates []tmuxTempDirCandidate) (string, error) {
	var failures []string
	for _, candidate := range candidates {
		if err := validateTmuxTempBase(candidate.path); err != nil {
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.source, candidate.path, err))
			continue
		}

		dir, err := os.MkdirTemp(candidate.path, tmuxTempDirPattern)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.source, candidate.path, err))
			continue
		}
		if err := validateTmuxSocketRoot(dir); err != nil {
			cleanupErr := os.Remove(dir)
			if cleanupErr != nil {
				err = fmt.Errorf("%w (also could not remove rejected directory %q: %v)", err, dir, cleanupErr)
			}
			failures = append(failures, fmt.Sprintf("%s %q: %v", candidate.source, candidate.path, err))
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
		tmuxTestTempBaseEnv,
		strings.Join(failures, "; "),
	)
}

func shortTmuxTempDirCandidates() []tmuxTempDirCandidate {
	raw := []tmuxTempDirCandidate{
		{source: tmuxTestTempBaseEnv, path: os.Getenv(tmuxTestTempBaseEnv)},
	}
	if runtime.GOOS != "windows" {
		raw = append(raw,
			tmuxTempDirCandidate{source: "portable fallback", path: "/var/tmp"},
			tmuxTempDirCandidate{source: "portable fallback", path: "/tmp"},
		)
	}
	raw = append(raw, tmuxTempDirCandidate{source: "os.TempDir fallback", path: os.TempDir()})

	seen := make(map[string]struct{}, len(raw))
	candidates := make([]tmuxTempDirCandidate, 0, len(raw))
	for _, candidate := range raw {
		if candidate.path == "" {
			continue
		}
		path, err := filepath.Abs(candidate.path)
		if err == nil {
			candidate.path = path
		} else {
			candidate.path = filepath.Clean(candidate.path)
		}
		key := candidate.path
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

func validateTmuxTempBase(base string) error {
	// Go currently replaces '*' with a ten-digit random suffix. Reserving
	// twenty digits avoids depending on that implementation detail.
	projectedRoot := filepath.Join(base, "ntm-tmux-test-18446744073709551615")
	return validateTmuxSocketRoot(projectedRoot)
}

func validateTmuxSocketRoot(root string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	// tmux's default socket is $TMUX_TMPDIR/tmux-$UID/default. Reserve the
	// largest decimal uint64 UID even though supported Unix systems use
	// narrower uid_t values.
	projected := filepath.Join(root, "tmux-18446744073709551615", "default")
	length := len([]byte(projected)) + 1 // sockaddr_un requires a trailing NUL.
	if length > maxTmuxSocketPathBytes {
		return fmt.Errorf(
			"projected tmux socket path %q needs %d bytes (portable limit %d)",
			projected,
			length,
			maxTmuxSocketPathBytes,
		)
	}
	return nil
}

// IntegrationTestPrecheckThrottled runs integration prechecks with throttling.
// Use this instead of IntegrationTestPrecheck for tests that spawn tmux.
func IntegrationTestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireIntegration(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}

// E2ETestPrecheckThrottled runs E2E prechecks with throttling.
// Use this instead of E2ETestPrecheck for tests that spawn tmux.
func E2ETestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireE2E(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}

// TimeoutScale returns the multiplier applied to test time budgets.
//
// An explicit TimeoutScaleEnv wins. Otherwise the scale is MEASURED from how
// long this machine actually takes to spawn a subprocess, so the default adapts
// to a loaded box instead of requiring every developer to know about an env
// var — the whole point being that the suite should be trustworthy by default.
//
// Values below 1 are clamped: a scale makes budgets more forgiving on a slow
// machine, never tighter, which would only add flakiness. An unparseable value
// is treated as unset rather than failing the suite — a mistyped env var must
// not be able to turn a green run red.
func TimeoutScale() float64 {
	if raw := strings.TrimSpace(os.Getenv(TimeoutScaleEnv)); raw != "" {
		scale, err := strconv.ParseFloat(raw, 64)
		if err == nil && scale >= 1 {
			return scale
		}
		if err == nil {
			return 1
		}
	}
	measuredScaleOnce.Do(func() { measuredScaleVal = measureTimeoutScale() })
	return measuredScaleVal
}

// ScaleTimeout scales a fixed test budget by TimeoutScale, rounding up so a
// scaled budget is never shorter than the original.
func ScaleTimeout(d time.Duration) time.Duration {
	scale := TimeoutScale()
	if scale <= 1 {
		return d
	}
	scaled := time.Duration(math.Ceil(float64(d) * scale))
	if scaled < d {
		// Overflow guard: a preposterous scale must not wrap to a tiny budget.
		return d
	}
	return scaled
}

// measuredScale caches the calibration so the probe runs at most once per test
// binary.
var (
	measuredScaleOnce sync.Once
	measuredScaleVal  float64
)

// nominalSubprocessStartup is what spawning a trivial shell costs on an idle
// machine. Budgets in these tests were all chosen against roughly this.
const nominalSubprocessStartup = 25 * time.Millisecond

// maxMeasuredScale caps the calibration. A machine slow enough to exceed it is
// in trouble for reasons a test budget cannot paper over, and an unbounded
// scale would let one wedged run hang the suite instead of failing it.
const maxMeasuredScale = 8

// measureTimeoutScale derives a scale from how long this machine actually takes
// to spawn a subprocess.
//
// Every budget these helpers guard is really a bet about latency the test does
// not control: shell startup, or a goroutine that shells out. Hardcoding the
// bet against an idle machine is what made the suite fail on a box running an
// agent swarm — the code under test was correct and the deadline was fiction.
// Measuring turns the bet into an observation, so the default adapts instead of
// requiring every developer to know about an env var.
func measureTimeoutScale() float64 {
	shell, err := exec.LookPath("sh")
	if err != nil {
		return 1
	}
	worst := time.Duration(0)
	for i := 0; i < 3; i++ {
		start := time.Now()
		cmd := exec.Command(shell, "-c", ":")
		if err := cmd.Run(); err != nil {
			return 1
		}
		if elapsed := time.Since(start); elapsed > worst {
			worst = elapsed
		}
	}
	scale := math.Ceil(float64(worst) / float64(nominalSubprocessStartup))
	if scale < 1 {
		return 1
	}
	if scale > maxMeasuredScale {
		return maxMeasuredScale
	}
	return scale
}
