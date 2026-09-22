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
	"testing"
	"time"
)

func TestPaneLockMixedProjectModesContend(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	project := t.TempDir()
	for _, tc := range []struct {
		name, ownerDir, waiterDir string
	}{
		{"projectless owner", "", project},
		{"projectless waiter", project, ""},
		{"different projects", project, t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := newLockExecutor(tc.ownerDir, time.Second)
			waiter := newLockExecutor(tc.waiterDir, 50*time.Millisecond)
			release, err := owner.acquirePaneLockCrossProcess(context.Background(), "%mixed")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if unexpected, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%mixed"); !errors.Is(err, ErrPaneBusyOtherProcess) {
				if unexpected != nil {
					unexpected()
				}
				t.Fatalf("project mode bypassed pane ownership: %v", err)
			}
		})
	}
}

func TestPaneLockCancelledBeforeAcquireWritesNothing(t *testing.T) {
	for _, projectless := range []bool{false, true} {
		t.Run(fmt.Sprintf("projectless=%t", projectless), func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			t.Setenv("XDG_STATE_HOME", state)
			project := t.TempDir()
			dir := project
			if projectless {
				dir = ""
			}
			e := newLockExecutor(dir, time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// Repetition catches selecting a ready local-lock channel instead
			// of cancellation when both select branches can proceed.
			for i := 0; i < 100; i++ {
				if release, err := e.acquirePaneLockCrossProcess(ctx, "%cancel"); !errors.Is(err, context.Canceled) {
					if release != nil {
						release()
					}
					t.Fatalf("cancelled dispatch acquired ownership: %v", err)
				}
			}
			for _, path := range []string{state, filepath.Join(project, ".ntm")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("cancelled dispatch touched %s: %v", path, err)
				}
			}
		})
	}
}

func TestPaneLockProjectlessFailureReleasesLocalGate(t *testing.T) {
	badState := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badState, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", badState)
	e := newLockExecutor("", 50*time.Millisecond)
	if release, err := e.acquirePaneLockCrossProcess(context.Background(), "%retry"); err == nil {
		release()
		t.Fatal("projectless executor proceeded without an available shared lock")
	} else if errors.Is(err, ErrPaneBusyOtherProcess) {
		t.Fatalf("state directory failure was misclassified as pane contention: %v", err)
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := e.acquirePaneLockCrossProcess(ctx, "%retry")
	if err != nil {
		t.Fatalf("failed acquisition leaked the local gate: %v", err)
	}
	release()
}

func TestPaneLockDryRunNeverCreatesSharedState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", state)
	project := t.TempDir()
	for _, dir := range []string{"", project} {
		a := NewExecutor(ExecutorConfig{Session: "dry", ProjectDir: dir, DryRun: true, PaneLockWait: 50 * time.Millisecond})
		b := NewExecutor(ExecutorConfig{Session: "dry", ProjectDir: dir, DryRun: true, PaneLockWait: 50 * time.Millisecond})
		releaseA, err := a.acquirePaneLockCrossProcess(context.Background(), "dry-run-pane")
		if err != nil {
			t.Fatal(err)
		}
		defer releaseA()
		releaseB, err := b.acquirePaneLockCrossProcess(context.Background(), "dry-run-pane")
		if err != nil {
			t.Fatalf("dry runs contended: %v", err)
		}
		releaseB()
	}
	for _, path := range []string{state, filepath.Join(project, ".ntm")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("dry run touched %s: %v", path, err)
		}
	}
}

func TestPaneLockProjectlessProcessHelper(t *testing.T) {
	if os.Getenv("NTM_PROJECTLESS_LOCK_TEST_HELPER") != "1" {
		return
	}
	// Preserve the parent's rendezvous even if a suite TestMain isolates its
	// environment before this subprocess helper runs.
	t.Setenv("XDG_STATE_HOME", os.Getenv("NTM_PROJECTLESS_LOCK_TEST_STATE_HOME"))
	e := newLockExecutor("", time.Second)
	release, err := e.acquirePaneLockCrossProcess(context.Background(), "%projectless-child")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestPaneLockProjectlessProcessDeathReleasesOwnership(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPaneLockProjectlessProcessHelper$")
	cmd.Env = append(os.Environ(), "NTM_PROJECTLESS_LOCK_TEST_HELPER=1", "NTM_PROJECTLESS_LOCK_TEST_STATE_HOME="+state)
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
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewScanner(stdout)
	if !reader.Scan() || reader.Text() != "locked" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		waited = true
		t.Fatalf("helper did not acquire ownership: %v; %s", reader.Err(), stderr.String())
	}

	for _, dir := range []string{"", t.TempDir()} {
		waiter := newLockExecutor(dir, 50*time.Millisecond)
		if release, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%projectless-child"); !errors.Is(err, ErrPaneBusyOtherProcess) {
			if release != nil {
				release()
			}
			t.Fatalf("bypassed a projectless owner in another process: %v", err)
		}
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true

	waiter := newLockExecutor("", time.Second)
	release, err := waiter.acquirePaneLockCrossProcess(context.Background(), "%projectless-child")
	if err != nil {
		t.Fatalf("terminated owner left a stale lock: %v", err)
	}
	release()
}
