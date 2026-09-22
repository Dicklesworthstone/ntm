package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sharedLockTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", root)
	return root
}

func acquireSharedForTest(t *testing.T, pane, legacy string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := acquireSharedPaneLock(ctx, pane, legacy)
	if err != nil {
		t.Fatalf("acquire %s: %v", pane, err)
	}
	t.Cleanup(release)
	return release
}

func TestSharedPaneLockExcludesDifferentProjects(t *testing.T) {
	sharedLockTestRoot(t)
	first := filepath.Join(t.TempDir(), "pane.lock")
	second := filepath.Join(t.TempDir(), "pane.lock")
	release := acquireSharedForTest(t, "%shared", first)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if unexpected, err := acquireSharedPaneLock(ctx, "%shared", second); !errors.Is(err, context.DeadlineExceeded) {
		if unexpected != nil {
			unexpected()
		}
		t.Fatalf("different project bypassed pane ownership: %v", err)
	}
	release()
	acquireSharedForTest(t, "%shared", second)()
}

func TestSharedPaneLockDifferentPanesRemainIndependent(t *testing.T) {
	sharedLockTestRoot(t)
	acquireSharedForTest(t, "%one", filepath.Join(t.TempDir(), "one.lock"))
	acquireSharedForTest(t, "%two", filepath.Join(t.TempDir(), "two.lock"))()
}

func TestSharedPaneLockHonorsLegacyOwnerAndReleasesOnFailure(t *testing.T) {
	sharedLockTestRoot(t)
	legacyPath := filepath.Join(t.TempDir(), "legacy.lock")
	owner, err := openLockedFile(context.Background(), legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.unlockAndClose()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if release, err := acquireSharedPaneLock(ctx, "%legacy", legacyPath); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("did not respect an older process's project lock: %v", err)
	}
	// Failure at the second gate must not leave the first gate held.
	acquireSharedForTest(t, "%legacy", filepath.Join(t.TempDir(), "other.lock"))()
}

func TestSharedPaneLockLegacyOpenFailureReleasesSharedGate(t *testing.T) {
	sharedLockTestRoot(t)
	bad := filepath.Join(t.TempDir(), "missing", "pane.lock")
	if release, err := acquireSharedPaneLock(context.Background(), "%retry", bad); err == nil {
		release()
		t.Fatal("expected missing project directory to fail")
	}
	acquireSharedForTest(t, "%retry", "")()
}

func TestSharedPaneLockCancelledBeforeAcquireDoesNotWrite(t *testing.T) {
	root := sharedLockTestRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := acquireSharedPaneLock(ctx, "%cancelled", ""); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("cancelled acquisition returned %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cancelled acquisition touched the state directory: %v", err)
	}
}

func TestSharedPaneLockFailsClosedWithoutWritableStateDirectory(t *testing.T) {
	root := sharedLockTestRoot(t)
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireSharedPaneLock(context.Background(), "%blocked", ""); err == nil {
		release()
		t.Fatal("dispatch would proceed without the shared lock")
	}
}

func TestSharedPaneLockPathIgnoresRelativeStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", "relative-state")
	path, err := sharedPaneLockPath("../../escape")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "ntm", "pane-locks")
	if filepath.Dir(path) != want || !filepath.IsAbs(path) || strings.Contains(filepath.Base(path), "..") {
		t.Fatalf("unsafe or project-dependent shared lock path: %s", path)
	}
}

func TestSharedPaneLockPreservesPrivateStableRendezvous(t *testing.T) {
	sharedLockTestRoot(t)
	path, err := sharedPaneLockPath("%stable")
	if err != nil {
		t.Fatal(err)
	}
	release := acquireSharedForTest(t, "%stable", "")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if before.Mode().Perm() != 0o600 {
			t.Errorf("lock mode = %o, want 600", before.Mode().Perm())
		}
		dir, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if dir.Mode().Perm() != 0o700 {
			t.Errorf("lock directory mode = %o, want 700", dir.Mode().Perm())
		}
	}
	release()
	release() // Cleanup is safe even when two error paths share it.
	acquireSharedForTest(t, "%stable", "")()
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("release replaced or removed the rendezvous inode: %v", err)
	}
}

func TestSharedPaneLockProcessHelper(t *testing.T) {
	if os.Getenv("NTM_SHARED_PANE_LOCK_TEST_HELPER") != "1" {
		return
	}
	release, err := acquireSharedPaneLock(context.Background(), "%child", os.Getenv("NTM_SHARED_PANE_LOCK_TEST_LEGACY"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestSharedPaneLockSurvivesOwnerProcessDeath(t *testing.T) {
	sharedLockTestRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSharedPaneLockProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_SHARED_PANE_LOCK_TEST_HELPER=1", "NTM_SHARED_PANE_LOCK_TEST_LEGACY="+filepath.Join(t.TempDir(), "owner.lock"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "locked" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper did not acquire the lock: %v; %s", reader.Err(), stderr.String())
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer waitCancel()
	otherProject := filepath.Join(t.TempDir(), "waiter.lock")
	if release, err := acquireSharedPaneLock(waitCtx, "%child", otherProject); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("another process bypassed pane ownership: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	// No stale-lock cleanup, deletion, or lease timeout may be required.
	acquireSharedForTest(t, "%child", otherProject)()
}
