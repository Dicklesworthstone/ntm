package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Environment contract for the subprocess half of the two-process test.
const (
	paneLockHelperEnv    = "NTM_TEST_PANE_LOCK_HELPER_DIR"
	paneLockHelperPaneID = "%117"
	// paneLockHelperHold is short on purpose: this test runs in the ordinary
	// `go test -short ./...` gate, and a regression test for a two-process
	// bug that only runs outside the gate protects nothing.
	//
	// It still has to comfortably outlast the parent's 300ms acquire attempt
	// even when the suite has saturated the machine and the parent is
	// descheduled mid-probe — hence a 5x margin rather than the tightest
	// value that passes on an idle box.
	paneLockHelperHold = 1500 * time.Millisecond
	// paneLockHelperReadyFile is touched once the lock is actually held, so
	// the parent never races the child's startup.
	paneLockHelperReadyFile = "helper-holds-lock"
)

// TestMain lets this test binary re-exec itself as the lock-holding child.
// The unit tests model two processes with two Executors, which is faithful
// (flock belongs to the open file description, so two descriptions contend
// even in one process) — but ntm#324 is a two-PROCESS bug, so it is worth one
// test that is literally two processes.
func TestMain(m *testing.M) {
	if dir := os.Getenv(paneLockHelperEnv); dir != "" {
		os.Exit(runPaneLockHelper(dir))
	}
	os.Exit(m.Run())
}

// runPaneLockHelper takes the pane lock, signals readiness, holds, releases.
func runPaneLockHelper(dir string) int {
	e := NewExecutor(ExecutorConfig{
		Session:      "helper",
		ProjectDir:   dir,
		PaneLockWait: time.Second,
	})

	release, err := e.acquirePaneLockCrossProcess(context.Background(), paneLockHelperPaneID)
	if err != nil {
		return 1
	}
	defer release()

	if err := os.WriteFile(dir+"/"+paneLockHelperReadyFile, []byte("held"), 0o600); err != nil {
		return 1
	}
	time.Sleep(paneLockHelperHold)
	return 0
}

// TestPaneLockExcludesASeparateProcess is the ntm#324 regression against a
// real second process: while the child owns the pane, this process must be
// refused, and once the child exits the pane must become available.
//
// It also proves the crash-safety property the design relies on: the child is
// never asked to clean up, so the lock's release is the OS closing its file
// descriptors at exit.
func TestPaneLockExcludesASeparateProcess(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "/" + paneLockHelperReadyFile

	child := exec.Command(os.Args[0], "-test.run=TestPaneLockExcludesASeparateProcess")
	child.Env = append(os.Environ(), paneLockHelperEnv+"="+dir)
	if err := child.Start(); err != nil {
		t.Fatalf("start lock-holding child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})

	// Wait for the child to actually hold the lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never signalled that it holds the pane lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// While the child owns the pane, this process must be refused.
	e := NewExecutor(ExecutorConfig{
		Session:      "parent",
		ProjectDir:   dir,
		PaneLockWait: 300 * time.Millisecond,
	})
	if _, err := e.acquirePaneLockCrossProcess(context.Background(), paneLockHelperPaneID); !errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Fatalf("acquired a pane another PROCESS owns (err=%v); dispatch is not exclusive across processes", err)
	}

	// Once the child exits, the OS drops its lock and the pane frees.
	if err := child.Wait(); err != nil {
		t.Fatalf("lock-holding child failed: %v", err)
	}

	e2 := NewExecutor(ExecutorConfig{
		Session:      "parent",
		ProjectDir:   dir,
		PaneLockWait: 5 * time.Second,
	})
	release, err := e2.acquirePaneLockCrossProcess(context.Background(), paneLockHelperPaneID)
	if err != nil {
		t.Fatalf("pane stayed locked after the owning process exited: %v", err)
	}
	release()
}
