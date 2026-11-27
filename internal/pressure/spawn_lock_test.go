//go:build linux || darwin

package pressure

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSpawnAdmissionLockCancellationAndBoundedWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := acquireSpawnAdmissionFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if unlock, err := acquireSpawnAdmissionFile(ctx, path, time.Second); unlock != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller cancellation lost: unlock=%t err=%v", unlock != nil, err)
	}
	if unlock, err := acquireSpawnAdmissionFile(context.Background(), path, 40*time.Millisecond); unlock != nil || !errors.Is(err, ErrSpawnAdmissionBusy) {
		t.Fatalf("bounded contention lost: unlock=%t err=%v", unlock != nil, err)
	}
	release()
	// Idempotent release must not close a later owner's descriptor.
	next, err := acquireSpawnAdmissionFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer next()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); release() }()
	}
	wg.Wait()
	if unlock, err := acquireSpawnAdmissionFile(context.Background(), path, 40*time.Millisecond); unlock != nil || !errors.Is(err, ErrSpawnAdmissionBusy) {
		t.Fatalf("stale release unlocked a later owner: %v", err)
	}
}

func TestSpawnAdmissionLockSerializesIndependentOpeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	const workers = 16
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			unlock, err := acquireSpawnAdmissionFile(context.Background(), path, 5*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			n := inside.Add(1)
			for old := maxInside.Load(); n > old && !maxInside.CompareAndSwap(old, n); old = maxInside.Load() {
			}
			// Yield long enough for independently opened contenders to try.
			time.Sleep(time.Millisecond)
			inside.Add(-1)
		}()
	}
	close(start)
	wg.Wait()
	if maxInside.Load() != 1 || inside.Load() != 0 {
		t.Fatalf("overlapping owners: maximum=%d remaining=%d", maxInside.Load(), inside.Load())
	}
}

func TestSpawnAdmissionLockPreservesRendezvousAndRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lock")
	const retained = "never truncate the rendezvous"
	if err := os.WriteFile(path, []byte(retained), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		release, err := acquireSpawnAdmissionFile(context.Background(), path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	after, err := os.Stat(path)
	data, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !os.SameFile(before, after) || string(data) != retained {
		t.Fatalf("rendezvous replaced or truncated: %q %v %v", data, err, readErr)
	}
	for _, kind := range []string{"symlink", "directory", "fifo", "hardlink", "shared"} {
		t.Run(kind, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "lock")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(path, candidate)
			case "directory":
				err = os.Mkdir(candidate, 0o700)
			case "fifo":
				err = syscall.Mkfifo(candidate, 0o600)
			case "hardlink":
				err = os.Link(path, candidate)
			case "shared":
				err = os.WriteFile(candidate, nil, 0o600)
				if err == nil {
					err = os.Chmod(candidate, 0o666)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if unlock, err := acquireSpawnAdmissionFile(context.Background(), candidate, time.Second); unlock != nil || err == nil {
				t.Fatalf("accepted unsafe %s: %v", kind, err)
			}
		})
	}
}

func TestAcquireSpawnAdmissionRejectsCancelledAndInvalidCacheWithoutFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := AcquireSpawnAdmission(ctx); release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %t, %v", release != nil, err)
	}
	if release, err := AcquireSpawnAdmission(nil); release != nil || err == nil {
		t.Fatal("nil context accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled request created files: %v %v", entries, err)
	}
}

func TestAcquireSpawnAdmissionUsesSharedCacheNotProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	first, err := AcquireSpawnAdmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	// A different selected NTM configuration must not create a second lock.
	t.Setenv("NTM_CONFIG", filepath.Join(root, "other-project", "config.toml"))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if release, err := AcquireSpawnAdmission(ctx); release != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configuration switch bypassed owner: %t %v", release != nil, err)
	}
}

func TestSpawnAdmissionProcessHelper(t *testing.T) {
	mode := os.Getenv("NTM_SPAWN_LOCK_TEST_MODE")
	if mode == "" {
		return
	}
	var release func()
	if mode == "own" {
		// Intentionally do not release: exercise OS cleanup on process exit.
		var err error
		release, err = acquireSpawnAdmissionFile(context.Background(), os.Getenv("NTM_SPAWN_LOCK_TEST_PATH"), time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	// Keep ownership reachable until exit rather than letting a finalizer
	// close the descriptor while the helper is still alive.
	runtime.KeepAlive(release)
	os.Exit(0)
}

func startSpawnLockProcess(t *testing.T, mode, path string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSpawnAdmissionProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_SPAWN_LOCK_TEST_MODE="+mode, "NTM_SPAWN_LOCK_TEST_PATH="+path, "GORACE=atexit_sleep_ms=0")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	stop := sync.OnceFunc(func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("helper process: %v", err)
		}
		cancel()
	})
	t.Cleanup(stop)
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper startup: %q %v", line, err)
	}
	return stop
}

func TestSpawnAdmissionLockCrossProcessAndOwnerExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	stop := startSpawnLockProcess(t, "own", path)
	if release, err := acquireSpawnAdmissionFile(context.Background(), path, 40*time.Millisecond); release != nil || !errors.Is(err, ErrSpawnAdmissionBusy) {
		t.Fatalf("second process bypassed active owner: %t %v", release != nil, err)
	}
	stop()
	release, err := acquireSpawnAdmissionFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("dead process wedged admission: %v", err)
	}
	release()
}

func TestSpawnAdmissionLockDoesNotSurviveExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := acquireSpawnAdmissionFile(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stop := startSpawnLockProcess(t, "idle", path)
	defer stop()
	release()
	// The exec'd child is still alive. It must not have inherited ownership.
	next, err := acquireSpawnAdmissionFile(context.Background(), path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("child process retained admission ownership: %v", err)
	}
	next()
}

func TestScaleAdmissionSeparatesSessionsAndFleet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)

	release, err := AcquireScaleAdmission(t.Context(), "scale-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	other, err := AcquireScaleAdmission(t.Context(), "scale-b")
	if err != nil {
		t.Fatalf("different sessions shared a scale fence: %v", err)
	}
	other()

	fleet, err := AcquireSpawnAdmission(t.Context())
	if err != nil {
		t.Fatalf("scale fence deadlocked nested fleet admission: %v", err)
	}
	fleet()

	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if next, err := AcquireScaleAdmission(ctx, "scale-a"); next != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-session scale bypassed owner: release=%t err=%v", next != nil, err)
	}

	release()
	next, err := AcquireScaleAdmission(t.Context(), "scale-a")
	if err != nil {
		t.Fatalf("released scale fence remained owned: %v", err)
	}
	next()
}

func TestScaleAdmissionRejectsInvalidInputWithoutFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)

	if release, err := AcquireScaleAdmission(nil, "scale-a"); release != nil || err == nil {
		t.Fatal("nil context accepted")
	}
	for _, session := range []string{"", " scale-a", "scale-a "} {
		if release, err := AcquireScaleAdmission(t.Context(), session); release != nil || err == nil {
			t.Fatalf("invalid exact session %q accepted", session)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if release, err := AcquireScaleAdmission(ctx, "scale-a"); release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scale accepted: release=%t err=%v", release != nil, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid scale created files: %v %v", entries, err)
	}
}
