//go:build unix

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunControlCancellationKeepsOwnershipUntilExecutionReturns(t *testing.T) {
	dir := t.TempDir()
	control, err := AcquireRunControl(context.Background(), dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := RequestRunCancellation(context.Background(), dir, "run"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(control.Context().Err(), context.Canceled) {
		t.Fatal("acknowledgment did not cancel the owning execution context")
	}
	other, err := AcquireRunControl(context.Background(), dir, "run")
	if other != nil {
		other.Close()
	}
	if !errors.Is(err, ErrRunAlreadyOwned) {
		t.Fatalf("cancellation released ownership before executor cleanup: %v", err)
	}
	control.Close()
	control.Close() // Repeated cleanup must not release a future owner's lock.
	next, err := AcquireRunControl(context.Background(), dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.record.Token == control.record.Token {
		t.Fatal("resumed attempt reused cancellation token")
	}
	// The prior owner's request and acknowledgment remain on disk. Neither
	// may affect the resumed attempt.
	time.Sleep(3 * runControlPollInterval)
	if err := next.Context().Err(); err != nil {
		t.Fatalf("stale cancellation stopped the new attempt: %v", err)
	}
}

func TestRunControlRejectsInvalidAndPrecancelledRequests(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if control, err := AcquireRunControl(ctx, dir, "run"); control != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel acquire: %v %v", control, err)
	}
	if err := RequestRunCancellation(ctx, dir, "run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel request: %v", err)
	}
	for _, id := range []string{"", ".", "..", "../outside", "a/b", "a\\b", "a\x00b"} {
		if control, err := AcquireRunControl(context.Background(), dir, id); err == nil {
			control.Close()
			t.Fatalf("invalid ID %q accepted", id)
		}
	}
	if control, err := AcquireRunControl(context.Background(), "", "run"); err == nil {
		control.Close()
		t.Fatal("missing project root accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected request mutated project: %v %v", entries, err)
	}
}

func TestRunControlRequiresLiveAcknowledgment(t *testing.T) {
	dir := t.TempDir()
	if err := RequestRunCancellation(context.Background(), dir, "missing"); !errors.Is(err, ErrRunControlUnavailable) {
		t.Fatalf("missing owner: %v", err)
	}
	control, err := AcquireRunControl(context.Background(), dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	// Simulate an unresponsive owner without falsifying its persisted state.
	control.cancel()
	<-control.done
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := RequestRunCancellation(ctx, dir, "run"); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrRunControlUnavailable) {
		t.Fatalf("unacknowledged request reported success: %v", err)
	}
	control.Close()
	if err := RequestRunCancellation(context.Background(), dir, "run"); !errors.Is(err, ErrRunControlUnavailable) {
		t.Fatalf("retired owner reported success: %v", err)
	}
}

func TestRunControlProjectIsolationAndCanonicalRoot(t *testing.T) {
	root, otherRoot := t.TempDir(), t.TempDir()
	first, err := AcquireRunControl(context.Background(), root, "same-id")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	other, err := AcquireRunControl(context.Background(), otherRoot, "same-id")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	duplicate, err := AcquireRunControl(context.Background(), alias, "same-id")
	if duplicate != nil {
		duplicate.Close()
	}
	if !errors.Is(err, ErrRunAlreadyOwned) {
		t.Fatalf("symlink alias bypassed ownership: %v", err)
	}
	if err := RequestRunCancellation(context.Background(), alias, "same-id"); err != nil {
		t.Fatal(err)
	}
	if other.Context().Err() != nil {
		t.Fatal("cancellation escaped its project")
	}
}

func TestRunControlConcurrentCancellationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	control, err := AcquireRunControl(context.Background(), dir, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- RequestRunCancellation(context.Background(), dir, "run") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("same-owner cancellation failed: %v", err)
		}
	}
}

func TestRunControlBoundsRecordReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
		t.Fatal(err)
	}
	var owner runControlRecord
	if err := readRunControl(path, &owner); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized control record accepted: %v", err)
	}
}

func TestRunControlAcrossProcesses(t *testing.T) {
	for _, mode := range []string{"cancel", "crash"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithTimeout(context.Background(), 8*time.Second)
			defer stop()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRunControlProcessHelper$")
			cmd.Env = append(os.Environ(), "NTM_RUN_CONTROL_HELPER="+mode, "NTM_RUN_CONTROL_ROOT="+dir, "NTM_RUN_CONTROL_READY="+ready)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				defer close(done)
				done <- cmd.Wait()
			}()
			defer func() {
				stop()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not start its controlled execution")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if mode == "cancel" {
				duplicate, err := AcquireRunControl(context.Background(), dir, "child-run")
				if duplicate != nil {
					duplicate.Close()
				}
				if !errors.Is(err, ErrRunAlreadyOwned) {
					t.Fatalf("second process bypassed ownership: %v", err)
				}
				if err := RequestRunCancellation(ctx, dir, "child-run"); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				t.Fatalf("controlled child failed: %v", err)
			}
			// A process exit, including one without Close, releases the kernel
			// lock. No stale PID detection or manual lock removal is needed.
			next, err := AcquireRunControl(context.Background(), dir, "child-run")
			if err != nil {
				t.Fatalf("exited child left run wedged: %v", err)
			}
			defer next.Close()
			time.Sleep(3 * runControlPollInterval)
			if next.Context().Err() != nil {
				t.Fatal("old child request canceled its replacement")
			}
		})
	}
}

func TestRunControlProcessHelper(t *testing.T) {
	mode := os.Getenv("NTM_RUN_CONTROL_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	control, err := AcquireRunControl(context.Background(), os.Getenv("NTM_RUN_CONTROL_ROOT"), "child-run")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "crash" {
		if err := os.WriteFile(os.Getenv("NTM_RUN_CONTROL_READY"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Deliberately omit Close to exercise kernel crash cleanup.
	}
	defer control.Close()
	// Real subprocess work inherits the control context. exec replaces the
	// shell so cancellation cannot leave a sleeping grandchild behind.
	cmd := exec.CommandContext(control.Context(), "sh", "-c", `echo ready > "$NTM_RUN_CONTROL_READY"; exec sleep 30`)
	if err := cmd.Run(); err == nil || !errors.Is(control.Context().Err(), context.Canceled) {
		t.Fatalf("external cancellation did not stop real subprocess work: %v", err)
	}
}
