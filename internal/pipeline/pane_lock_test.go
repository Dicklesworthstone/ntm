package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newLockExecutor builds an executor whose pane locks live under dir.
func newLockExecutor(dir string, wait time.Duration) *Executor {
	return NewExecutor(ExecutorConfig{
		Session:      "lock-test",
		ProjectDir:   dir,
		PaneLockWait: wait,
	})
}

// TestPaneLockIsExclusiveAcrossExecutors is the ntm#324 regression at the unit
// level. Two Executors model two `ntm pipeline run` processes: each builds its
// own in-memory paneLocks map, so only the file lock can separate them.
func TestPaneLockIsExclusiveAcrossExecutors(t *testing.T) {
	dir := t.TempDir()
	a := newLockExecutor(dir, 100*time.Millisecond)
	b := newLockExecutor(dir, 100*time.Millisecond)

	releaseA, err := a.acquirePaneLockCrossProcess(context.Background(), "%117")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// While A owns the pane, B must not be admitted.
	_, err = b.acquirePaneLockCrossProcess(context.Background(), "%117")
	if err == nil {
		t.Fatal("second executor acquired a pane the first still owns; dispatch is not exclusive")
	}
	if !errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Fatalf("want ErrPaneBusyOtherProcess, got %v", err)
	}

	// After A releases, B goes next.
	releaseA()
	releaseB, err := b.acquirePaneLockCrossProcess(context.Background(), "%117")
	if err != nil {
		t.Fatalf("second executor blocked after the first released: %v", err)
	}
	releaseB()
}

// TestPaneLockDoesNotSerializeDifferentPanes guards against the lock being too
// coarse: two executors working different panes must not contend.
func TestPaneLockDoesNotSerializeDifferentPanes(t *testing.T) {
	dir := t.TempDir()
	a := newLockExecutor(dir, 100*time.Millisecond)
	b := newLockExecutor(dir, 100*time.Millisecond)

	releaseA, err := a.acquirePaneLockCrossProcess(context.Background(), "%1")
	if err != nil {
		t.Fatalf("acquire %%1: %v", err)
	}
	defer releaseA()

	releaseB, err := b.acquirePaneLockCrossProcess(context.Background(), "%2")
	if err != nil {
		t.Fatalf("a lock on %%1 blocked an unrelated pane %%2: %v", err)
	}
	releaseB()
}

// TestPaneLockWaitsThenFailsTyped pins the contention policy: the later
// pipeline waits for the owner, and on expiry reports a typed
// not-dispatched reason rather than hanging or pasting.
func TestPaneLockWaitsThenFailsTyped(t *testing.T) {
	dir := t.TempDir()
	const wait = 300 * time.Millisecond

	owner := newLockExecutor(dir, wait)
	waiter := newLockExecutor(dir, wait)

	releaseOwner, err := owner.acquirePaneLockCrossProcess(context.Background(), "%9")
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}

	// Release midway through the waiter's budget: it must succeed by waiting.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(wait / 3)
		releaseOwner()
	}()

	start := time.Now()
	release, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%9")
	elapsed := time.Since(start)
	wg.Wait()

	if err != nil {
		t.Fatalf("waiter gave up on a pane that freed inside the budget: %v", err)
	}
	release()
	if elapsed < wait/4 {
		t.Errorf("waiter returned in %s without waiting for the owner", elapsed)
	}

	// Now hold it for the whole budget: the waiter must fail, typed.
	releaseOwner2, err := owner.acquirePaneLockCrossProcess(context.Background(), "%9")
	if err != nil {
		t.Fatalf("owner re-acquire: %v", err)
	}
	defer releaseOwner2()

	start = time.Now()
	_, err = waiter.acquirePaneLockCrossProcess(context.Background(), "%9")
	elapsed = time.Since(start)

	if !errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Fatalf("want ErrPaneBusyOtherProcess after the budget expired, got %v", err)
	}
	if elapsed < wait {
		t.Errorf("failed after %s, before the %s budget elapsed", elapsed, wait)
	}
	if elapsed > wait*4 {
		t.Errorf("waited %s, far beyond the %s budget", elapsed, wait)
	}
}

// TestPaneLockHonorsCallerCancellation verifies an operator cancelling the run
// is reported as cancelled, not as a busy pane.
func TestPaneLockHonorsCallerCancellation(t *testing.T) {
	dir := t.TempDir()
	owner := newLockExecutor(dir, time.Minute)
	waiter := newLockExecutor(dir, time.Minute)

	releaseOwner, err := owner.acquirePaneLockCrossProcess(context.Background(), "%5")
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}
	defer releaseOwner()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err = waiter.acquirePaneLockCrossProcess(ctx, "%5")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled on operator cancellation, got %v", err)
	}
	if errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Error("operator cancellation was misreported as a busy pane")
	}
}

// TestPaneLockReleaseFreesInProcessLockToo guards the layering: the returned
// release must drop BOTH the file lock and the in-memory channel, or the same
// executor deadlocks against itself on the next step.
func TestPaneLockReleaseFreesInProcessLockToo(t *testing.T) {
	dir := t.TempDir()
	e := newLockExecutor(dir, 500*time.Millisecond)

	for i := 0; i < 3; i++ {
		release, err := e.acquirePaneLockCrossProcess(context.Background(), "%3")
		if err != nil {
			t.Fatalf("acquire %d failed; a previous release did not free both locks: %v", i, err)
		}
		release()
	}
}

// TestPaneLockFailedAcquireReleasesLocalLock covers the error path: when the
// file lock cannot be taken, the in-process lock must be handed back, or the
// pane stays wedged inside this process for the rest of the run.
func TestPaneLockFailedAcquireReleasesLocalLock(t *testing.T) {
	dir := t.TempDir()
	owner := newLockExecutor(dir, 50*time.Millisecond)
	waiter := newLockExecutor(dir, 50*time.Millisecond)

	releaseOwner, err := owner.acquirePaneLockCrossProcess(context.Background(), "%7")
	if err != nil {
		t.Fatalf("owner acquire: %v", err)
	}

	if _, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%7"); !errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Fatalf("want busy, got %v", err)
	}
	releaseOwner()

	// The waiter's own in-process lock must be free again.
	done := make(chan error, 1)
	go func() {
		release, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%7")
		if err == nil {
			release()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("retry after a failed acquire: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry deadlocked: the failed acquire leaked the in-process pane lock")
	}
}

// TestPaneLockWithoutProjectDirDegradesInProcess pins the documented
// degradation: with no project root there is no agreed lock location, so the
// lock is in-process only rather than a lock file somewhere useless.
func TestPaneLockWithoutProjectDirDegradesInProcess(t *testing.T) {
	a := NewExecutor(ExecutorConfig{Session: "s"})
	b := NewExecutor(ExecutorConfig{Session: "s"})

	releaseA, err := a.acquirePaneLockCrossProcess(context.Background(), "%1")
	if err != nil {
		t.Fatalf("acquire without ProjectDir: %v", err)
	}
	defer releaseA()

	// No cross-process exclusion is claimed here, so b succeeds.
	releaseB, err := b.acquirePaneLockCrossProcess(context.Background(), "%1")
	if err != nil {
		t.Fatalf("without ProjectDir the lock must degrade to in-process, got %v", err)
	}
	releaseB()

	// The same executor still serializes itself.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.acquirePaneLockCrossProcess(ctx, "%1"); err == nil {
		t.Error("the in-process lock stopped serializing the same executor")
	}
}

// TestPaneLockEmptyPaneIDIsNoop covers steps with no pane target.
func TestPaneLockEmptyPaneIDIsNoop(t *testing.T) {
	e := newLockExecutor(t.TempDir(), time.Second)
	release, err := e.acquirePaneLockCrossProcess(context.Background(), "")
	if err != nil {
		t.Fatalf("empty pane id: %v", err)
	}
	release()
}

// TestPaneLockFileNameIsPathSafe verifies a pane identity cannot escape the
// lock directory or collide across distinct panes.
func TestPaneLockFileNameIsPathSafe(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"..",
		".",
		"%1/../../../tmp/x",
		"sess:0.1",
		strings.Repeat("a", 500),
		"a\x00b",
	}

	seen := map[string]string{}
	for _, paneID := range append(hostile, "%1", "%2", "%117") {
		name := paneLockFileName(paneID)

		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			t.Errorf("pane %q produced an unsafe lock file name %q", paneID, name)
		}
		if filepath.Base(name) != name {
			t.Errorf("pane %q produced a non-basename lock file name %q", paneID, name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("panes %q and %q collide on lock file %q", prev, paneID, name)
		}
		seen[name] = paneID
	}
}

// TestPaneLockCreatesLockUnderProjectState verifies the lock file lands in the
// project's pipeline state tree, where a second process will look for it.
func TestPaneLockCreatesLockUnderProjectState(t *testing.T) {
	dir := t.TempDir()
	e := newLockExecutor(dir, time.Second)

	release, err := e.acquirePaneLockCrossProcess(context.Background(), "%42")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	want := filepath.Join(dir, ".ntm", pipelineStateDirName, paneLockDirName)
	entries, err := os.ReadDir(want)
	if err != nil {
		t.Fatalf("lock directory %s not created: %v", want, err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 lock file in %s, got %d", want, len(entries))
	}
}

// TestApplyPaneLockFailureClassifies pins the two operator-visible outcomes.
func TestApplyPaneLockFailureClassifies(t *testing.T) {
	t.Run("busy pane is typed and says nothing was sent", func(t *testing.T) {
		var result StepResult
		applyPaneLockFailure(&result, "%117", ErrPaneBusyOtherProcess)

		if result.SkipKind != SkipKindPaneBusy {
			t.Errorf("SkipKind = %q, want %q", result.SkipKind, SkipKindPaneBusy)
		}
		if !strings.Contains(result.SkipReason, "%117") {
			t.Errorf("reason must name the pane: %q", result.SkipReason)
		}
		if !strings.Contains(result.SkipReason, "nothing was sent") {
			t.Errorf("reason must state that no prompt was dispatched: %q", result.SkipReason)
		}
	})

	t.Run("cancellation stays cancelled", func(t *testing.T) {
		var result StepResult
		applyPaneLockFailure(&result, "%117", context.Canceled)

		if result.SkipKind != SkipKindCancelled {
			t.Errorf("SkipKind = %q, want %q", result.SkipKind, SkipKindCancelled)
		}
	})
}
