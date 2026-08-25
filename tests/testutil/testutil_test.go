package testutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil/tmuxenv"
)

const (
	processGroupOwnerCrashEnv    = "_NTM_TEST_PROCESS_GROUP_OWNER_CRASH"
	processGroupOwnerCrashPIDEnv = "_NTM_TEST_PROCESS_GROUP_OWNER_CHILD_PID"
)

func TestProcessGroupGuardianKillsDescendantsWhenOwnerExits(t *testing.T) {
	if os.Getenv(processGroupOwnerCrashEnv) == "1" {
		pidPath := os.Getenv(processGroupOwnerCrashPIDEnv)
		cmd := exec.Command("/bin/sh", "-c", `sleep 600 & child=$!; printf '%s\n' "$child" > "$1"; wait "$child"`, "owner-crash", pidPath)
		group, err := NewProcessGroupForTest(context.Background(), cmd)
		if err != nil {
			t.Fatalf("create owner-crash process group: %v", err)
		}
		if err := cmd.Start(); err != nil {
			closeErr := group.Close()
			t.Fatalf("start owner-crash target: %v; close owned process group: %v", err, closeErr)
		}
		for {
			time.Sleep(time.Second)
			runtime.KeepAlive(group)
		}
	}
	if runtime.GOOS != "linux" {
		t.Skip("owner-crash descendant assertion uses Linux /proc")
	}

	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	owner := exec.Command(os.Args[0], "-test.run=^TestProcessGroupGuardianKillsDescendantsWhenOwnerExits$")
	overrides := map[string]string{
		processGroupOwnerCrashEnv:    "1",
		processGroupOwnerCrashPIDEnv: pidPath,
		processGroupGuardianEnv:      "0",
	}
	owner.Env = make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			owner.Env = append(owner.Env, entry)
		}
	}
	for key, value := range overrides {
		owner.Env = append(owner.Env, key+"="+value)
	}
	var output bytes.Buffer
	owner.Stdout = &output
	owner.Stderr = &output
	owner.WaitDelay = 2 * time.Second
	if err := owner.Start(); err != nil {
		t.Fatalf("start owner-crash helper: %v", err)
	}
	joined := false
	defer func() {
		if joined {
			return
		}
		_ = owner.Process.Kill()
		_ = owner.Wait()
	}()

	var descendantPID int
	readinessDeadline := time.Now().Add(30 * time.Second)
	for {
		pidBytes, err := os.ReadFile(pidPath)
		if err == nil {
			descendantPID, err = strconv.Atoi(strings.TrimSpace(string(pidBytes)))
			if err != nil || descendantPID <= 0 {
				t.Fatalf("parse owner-crash descendant pid %q: %v", pidBytes, err)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read owner-crash descendant pid: %v", err)
		}
		if time.Now().After(readinessDeadline) {
			t.Fatalf("owner-crash helper did not publish descendant pid: output=%s", output.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := owner.Process.Kill(); err != nil {
		t.Fatalf("kill owner-crash helper: %v", err)
	}
	waitErr := owner.Wait()
	joined = true
	if waitErr == nil {
		t.Fatal("killed owner-crash helper exited successfully")
	}

	statusPath := filepath.Join("/proc", strconv.Itoa(descendantPID), "status")
	terminationDeadline := time.Now().Add(10 * time.Second)
	for {
		status, err := os.ReadFile(statusPath)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("read owner-crash descendant status: %v", err)
		}
		for _, line := range strings.Split(string(status), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "State:" && fields[1] == "Z" {
				return
			}
		}
		if time.Now().After(terminationDeadline) {
			t.Fatalf("descendant process %d survived owner exit", descendantPID)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestNewTestLoggerStdout(t *testing.T) {
	logger := NewTestLoggerStdout(t)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
	if logger.testName != t.Name() {
		t.Errorf("expected testName %q, got %q", t.Name(), logger.testName)
	}

	// Test logging doesn't panic
	logger.Log("test message %d", 42)
	logger.LogSection("test section")
}

func TestNewTestLogger(t *testing.T) {
	tmpDir := t.TempDir()
	logger := NewTestLogger(t, tmpDir)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}

	// Log something
	logger.Log("test log entry")
	logger.LogSection("section")

	// Verify a log file was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected log file to be created")
	}

	// Verify file contains content
	logPath := filepath.Join(tmpDir, entries[0].Name())
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if len(content) == 0 {
		t.Error("expected log file to have content")
	}
}

func TestLoggerElapsed(t *testing.T) {
	logger := NewTestLoggerStdout(t)
	elapsed := logger.Elapsed()
	if elapsed < 0 {
		t.Errorf("elapsed time should be non-negative: %v", elapsed)
	}
}

func TestTryWithGlobalTmuxTestLock(t *testing.T) {
	ran := false
	acquired := tryWithGlobalTmuxTestLock(func() {
		ran = true
	})

	if acquired && !ran {
		t.Fatal("expected callback to run when lock acquisition succeeds")
	}
	if !acquired && ran {
		t.Fatal("expected callback to be skipped when lock is busy")
	}
}

func TestRequireTmux(t *testing.T) {
	// This test just verifies RequireTmux doesn't panic
	// It will skip if tmux is not installed
	RequireTmux(t)
	t.Log("tmux is installed")
}

func TestSessionExists(t *testing.T) {
	AcquireGlobalTmuxTestLockForTest(t)

	// Test with a session that definitely doesn't exist
	exists := SessionExists("nonexistent_session_" + t.Name())
	if exists {
		t.Error("session should not exist")
	}
}

func TestIsolateTmuxTestProcess(t *testing.T) {
	t.Setenv("TMUX", "/tmp/ambient-tmux,1,0")
	t.Setenv("TMUX_PANE", "%9")
	t.Setenv("TMUX_TMPDIR", "/tmp/ambient-tmux-root")

	cleanupTmux, err := IsolateTmuxTestProcess()
	if err != nil {
		t.Fatalf("IsolateTmuxTestProcess() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanupTmux(); err != nil {
			t.Errorf("cleanup IsolateTmuxTestProcess(): %v", err)
		}
	})
	if got := os.Getenv("TMUX"); got != "" {
		t.Fatalf("TMUX = %q, want empty", got)
	}
	if got := os.Getenv("TMUX_PANE"); got != "" {
		t.Fatalf("TMUX_PANE = %q, want empty", got)
	}
	tmuxTmpDir := os.Getenv("TMUX_TMPDIR")
	if tmuxTmpDir == "" || tmuxTmpDir == "/tmp/ambient-tmux-root" {
		t.Fatalf("TMUX_TMPDIR = %q, want unique test directory", tmuxTmpDir)
	}
	info, err := os.Stat(tmuxTmpDir)
	if err != nil {
		t.Fatalf("stat TMUX_TMPDIR: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("TMUX_TMPDIR = %q, want directory", tmuxTmpDir)
	}
	if err := tmuxenv.ValidateSocketRoot(tmuxTmpDir); err != nil {
		t.Fatalf("TMUX_TMPDIR = %q cannot host a portable tmux socket: %v", tmuxTmpDir, err)
	}
	if err := cleanupTmux(); err != nil {
		t.Fatalf("cleanup IsolateTmuxTestProcess(): %v", err)
	}
	if _, err := os.Stat(tmuxTmpDir); !os.IsNotExist(err) {
		t.Fatalf("isolated tmux cleanup left %q behind; stat error = %v", tmuxTmpDir, err)
	}
}

func TestIsolateTmuxTestProcessPreservesConfiguredFakeBinaryEnvironment(t *testing.T) {
	fakeDir := t.TempDir()
	invoked := filepath.Join(fakeDir, "invoked")
	fakeBinary := filepath.Join(fakeDir, "tmux")
	// Must match tmuxenv.Pattern: NTM_TEST_TMUX_ENV_OWNED is only trusted
	// when TMUX_TMPDIR proves it, not merely because it is set.
	ownedTmuxRoot := ShortTmuxTempDir(t)
	script := "#!/bin/sh\n: > \"$NTM_TEST_FAKE_TMUX_INVOKED\"\nexit 64\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux binary: %v", err)
	}
	t.Setenv("NTM_TEST_FAKE_TMUX_INVOKED", invoked)
	t.Setenv("NTM_TEST_TMUX_ENV_OWNED", "1")
	t.Setenv("NTM_TMUX_BINARY", fakeBinary)
	t.Setenv("TMUX", "fake-server,1,0")
	t.Setenv("TMUX_PANE", "%9")
	t.Setenv("TMUX_TMPDIR", ownedTmuxRoot)

	cleanupTmux, err := IsolateTmuxTestProcess()
	if err != nil {
		t.Fatalf("IsolateTmuxTestProcess() error = %v", err)
	}
	if got := os.Getenv("TMUX"); got != "fake-server,1,0" {
		t.Fatalf("TMUX = %q, want caller-owned fake environment", got)
	}
	if got := os.Getenv("TMUX_PANE"); got != "%9" {
		t.Fatalf("TMUX_PANE = %q, want caller-owned fake environment", got)
	}
	if got := os.Getenv("TMUX_TMPDIR"); got != ownedTmuxRoot {
		t.Fatalf("TMUX_TMPDIR = %q, want caller-owned %q", got, ownedTmuxRoot)
	}
	if err := cleanupTmux(); err != nil {
		t.Fatalf("cleanup IsolateTmuxTestProcess(): %v", err)
	}
	if _, err := os.Stat(invoked); !os.IsNotExist(err) {
		t.Fatalf("configured fake tmux was invoked during cleanup; stat error = %v", err)
	}
	if info, err := os.Stat(ownedTmuxRoot); err != nil || !info.IsDir() {
		t.Fatalf("caller-owned tmux root %q was changed during no-op cleanup: info=%v err=%v", ownedTmuxRoot, info, err)
	}
}

// TestIsolateTmuxTestProcessIgnoresUnprovenOwnershipFlag is the regression
// case: NTM_TEST_TMUX_ENV_OWNED=1 set by an ambient shell, with a TMUX_TMPDIR
// that was never produced by this package's own isolation, must not be
// trusted. Before the fix this combination short-circuited to a no-op
// cleanup and left TMUX_TMPDIR resolving to the caller's live tmux server.
func TestIsolateTmuxTestProcessIgnoresUnprovenOwnershipFlag(t *testing.T) {
	bogusRoot := t.TempDir() // does not match tmuxenv.Pattern
	t.Setenv("NTM_TEST_TMUX_ENV_OWNED", "1")
	t.Setenv("TMUX", "bystander-server,1,0")
	t.Setenv("TMUX_PANE", "%3")
	t.Setenv("TMUX_TMPDIR", bogusRoot)

	cleanupTmux, err := IsolateTmuxTestProcess()
	if err != nil {
		t.Fatalf("IsolateTmuxTestProcess() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanupTmux(); err != nil {
			t.Errorf("cleanup IsolateTmuxTestProcess(): %v", err)
		}
	})
	if got := os.Getenv("TMUX"); got != "" {
		t.Fatalf("TMUX = %q, want isolation to run despite the unproven flag", got)
	}
	if got := os.Getenv("TMUX_PANE"); got != "" {
		t.Fatalf("TMUX_PANE = %q, want isolation to run despite the unproven flag", got)
	}
	if got := os.Getenv("TMUX_TMPDIR"); got == "" || got == bogusRoot {
		t.Fatalf("TMUX_TMPDIR = %q, want a fresh isolated root, not the bogus %q", got, bogusRoot)
	}
}

func TestIsolateTmuxTestProcessStillIsolatesConfiguredBinary(t *testing.T) {
	fakeDir := t.TempDir()
	invoked := filepath.Join(fakeDir, "invoked")
	fakeBinary := filepath.Join(fakeDir, "tmux")
	script := "#!/bin/sh\n: > \"$NTM_TEST_FAKE_TMUX_INVOKED\"\nexit 64\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake tmux binary: %v", err)
	}
	t.Setenv("NTM_TEST_FAKE_TMUX_INVOKED", invoked)
	t.Setenv("NTM_TMUX_BINARY", fakeBinary)
	t.Setenv("TMUX", "ambient-server,1,0")
	t.Setenv("TMUX_PANE", "%8")
	t.Setenv("TMUX_TMPDIR", "/tmp/ambient-tmux-root")

	cleanupTmux, err := IsolateTmuxTestProcess()
	if err != nil {
		t.Fatalf("IsolateTmuxTestProcess() error = %v", err)
	}
	tmuxRoot := os.Getenv("TMUX_TMPDIR")
	if os.Getenv("TMUX") != "" || os.Getenv("TMUX_PANE") != "" ||
		tmuxRoot == "" || tmuxRoot == "/tmp/ambient-tmux-root" {
		t.Fatalf("configured binary retained ambient tmux environment: TMUX=%q TMUX_PANE=%q TMUX_TMPDIR=%q",
			os.Getenv("TMUX"), os.Getenv("TMUX_PANE"), tmuxRoot)
	}
	if err := cleanupTmux(); err != nil {
		t.Fatalf("cleanup IsolateTmuxTestProcess(): %v", err)
	}
	if _, err := os.Stat(invoked); !os.IsNotExist(err) {
		t.Fatalf("configured fake tmux was invoked during cleanup; stat error = %v", err)
	}
	if _, err := os.Stat(tmuxRoot); !os.IsNotExist(err) {
		t.Fatalf("isolated tmux cleanup left %q behind; stat error = %v", tmuxRoot, err)
	}
}

func TestShortTmuxTempDirCreatesAndCleansDirectory(t *testing.T) {
	var dir string
	t.Run("create", func(t *testing.T) {
		dir = ShortTmuxTempDir(t)
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat ShortTmuxTempDir(): %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("ShortTmuxTempDir() = %q, want directory", dir)
		}
		if err := tmuxenv.ValidateSocketRoot(dir); err != nil {
			t.Fatalf("ShortTmuxTempDir() = %q cannot host a portable tmux socket: %v", dir, err)
		}
	})

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("ShortTmuxTempDir cleanup left %q behind; stat error = %v", dir, err)
	}
}

func TestAgentConfig(t *testing.T) {
	config := AgentConfig{
		Claude: 2,
		Codex:  1,
		Gemini: 1,
	}

	if config.Claude != 2 {
		t.Errorf("expected Claude=2, got %d", config.Claude)
	}
	if config.Codex != 1 {
		t.Errorf("expected Codex=1, got %d", config.Codex)
	}
	if config.Gemini != 1 {
		t.Errorf("expected Gemini=1, got %d", config.Gemini)
	}
}

func TestSkipConditions(t *testing.T) {
	// These are just sanity checks that the skip functions exist and don't panic
	t.Run("RequireUnix", func(t *testing.T) {
		RequireUnix(t)
	})

	t.Run("IntegrationPrecheck", func(t *testing.T) {
		// This will skip because NTM_INTEGRATION_TESTS is not set
		// Just verify it doesn't panic
		if os.Getenv("NTM_INTEGRATION_TESTS") != "" {
			IntegrationTestPrecheck(t)
		}
	})
}

// TestConfigDirIsolationGuard fails if a package whose tests are known to
// reach os.UserConfigDir() has a TestMain that does not call
// IsolateUserConfigProcess. The list below is the audit from the bd-dst bead:
// it must be extended whenever a new package's tests start persisting
// os.UserConfigDir()-derived state, so the isolation cannot silently decay.
func TestConfigDirIsolationGuard(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot, err := findRepoRoot(filepath.Dir(file))
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	packages := []string{
		"e2e",
		"internal/agentmail",
		"internal/cli",
		"internal/coordinator",
		"internal/robot",
		"tests/e2e",
	}
	for _, pkg := range packages {
		pkgDir := filepath.Join(repoRoot, filepath.FromSlash(pkg))
		if !packageTestMainIsolatesUserConfig(t, pkgDir) {
			t.Errorf("package %s reaches os.UserConfigDir() in tests but its TestMain does not call IsolateUserConfigProcess", pkg)
		}
	}
}

// packageTestMainIsolatesUserConfig reports whether any *_test.go file in the
// package declares a TestMain and references IsolateUserConfigProcess. The
// helper is process-wide and only meaningful from TestMain, so its presence in
// a package's test sources is the signal the guard checks.
func packageTestMainIsolatesUserConfig(t *testing.T, pkgDir string) bool {
	t.Helper()
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package dir %s: %v", pkgDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		src := string(data)
		// Both signals must come from the SAME file, and the call must be a
		// call rather than a mention. Tracking them independently across the
		// package let a bare reference in any test file — a comment naming the
		// helper was enough — satisfy the guard for a TestMain that had
		// stopped calling it, which is the decay this guard exists to catch.
		if strings.Contains(src, "func TestMain(") &&
			strings.Contains(src, "IsolateUserConfigProcess()") {
			return true
		}
	}
	return false
}

func TestIsolateUserConfigProcessRedirectsUserConfigDir(t *testing.T) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	cleanup, err := IsolateUserConfigProcess()
	if err != nil {
		t.Fatalf("IsolateUserConfigProcess() error = %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup IsolateUserConfigProcess(): %v", err)
		}
	})

	root := filepath.Dir(os.Getenv("XDG_CONFIG_HOME"))
	if root == "" || root == "." {
		t.Fatal("XDG_CONFIG_HOME not set by helper")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	if !strings.HasPrefix(configDir, root) {
		t.Fatalf("UserConfigDir() = %q, want under isolated root %q", configDir, root)
	}
	if strings.HasPrefix(configDir, realHome) {
		t.Fatalf("UserConfigDir() = %q still under real home %q", configDir, realHome)
	}
}

func TestIsolateUserConfigProcessRestoresPreviousValues(t *testing.T) {
	ambientXDG, ambientXDGSet := os.LookupEnv("XDG_CONFIG_HOME")
	ambientHome, ambientHomeSet := os.LookupEnv("HOME")
	t.Cleanup(func() {
		if ambientXDGSet {
			os.Setenv("XDG_CONFIG_HOME", ambientXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
		if ambientHomeSet {
			os.Setenv("HOME", ambientHome)
		} else {
			os.Unsetenv("HOME")
		}
	})

	// Both set: cleanup restores the exact previous values.
	os.Setenv("XDG_CONFIG_HOME", "/ambient/xdg")
	os.Setenv("HOME", "/ambient/home")
	cleanup, err := IsolateUserConfigProcess()
	if err != nil {
		t.Fatalf("IsolateUserConfigProcess() error = %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if got := os.Getenv("XDG_CONFIG_HOME"); got != "/ambient/xdg" {
		t.Fatalf("XDG_CONFIG_HOME = %q, want /ambient/xdg", got)
	}
	if got := os.Getenv("HOME"); got != "/ambient/home" {
		t.Fatalf("HOME = %q, want /ambient/home", got)
	}

	// XDG_CONFIG_HOME unset: cleanup restores it to unset, not empty.
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", "/ambient/home")
	cleanup, err = IsolateUserConfigProcess()
	if err != nil {
		t.Fatalf("IsolateUserConfigProcess() error = %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		t.Fatalf("XDG_CONFIG_HOME = %q, want unset", os.Getenv("XDG_CONFIG_HOME"))
	}
	if got := os.Getenv("HOME"); got != "/ambient/home" {
		t.Fatalf("HOME = %q, want /ambient/home", got)
	}
}
