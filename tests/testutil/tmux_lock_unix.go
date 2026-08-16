//go:build !windows

package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const processGroupGuardianEnv = "_NTM_TEST_PROCESS_GROUP_GUARD"

func init() {
	if os.Getenv(processGroupGuardianEnv) != "1" {
		return
	}

	signal.Ignore(os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	ready := os.NewFile(3, "process-group-guardian-ready")
	lifetime := os.NewFile(4, "process-group-guardian-lifetime")
	if ready == nil || lifetime == nil {
		os.Exit(125)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		_ = ready.Close()
		os.Exit(125)
	}
	_ = ready.Close()
	_, _ = io.Copy(io.Discard, lifetime)
	_ = lifetime.Close()
	_ = syscall.Kill(0, syscall.SIGKILL)
	os.Exit(125)
}

func acquireGlobalTmuxTestLock(t *testing.T) {
	t.Helper()

	lockPath := filepath.Join(os.TempDir(), "ntm_tmux_tests.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open tmux test lock: %v", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		t.Fatalf("flock tmux test lock: %v", err)
	}

	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

// ProcessGroupForTest owns an isolated subprocess group. Its guardian pins the
// group ID until Close, so a post-Wait cleanup signal cannot target a recycled
// process group.
type ProcessGroupForTest struct {
	guardian      *exec.Cmd
	pgid          int
	lifetimeWrite *os.File

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// NewProcessGroupForTest starts a guardian and configures cmd to join its
// process group. The caller must Close the returned handle after waiting cmd.
func NewProcessGroupForTest(ctx context.Context, cmd *exec.Cmd) (*ProcessGroupForTest, error) {
	if ctx == nil {
		return nil, errors.New("process-group context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create process group: %w", err)
	}
	if cmd == nil {
		return nil, errors.New("process-group command is nil")
	}

	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create process-group guardian readiness pipe: %w", err)
	}
	lifetimeReader, lifetimeWriter, err := os.Pipe()
	if err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		return nil, fmt.Errorf("create process-group guardian lifetime pipe: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = lifetimeReader.Close()
		_ = lifetimeWriter.Close()
		return nil, fmt.Errorf("resolve process-group guardian executable: %w", err)
	}
	guardian := exec.Command(executable)
	guardianEnvPrefix := processGroupGuardianEnv + "="
	guardian.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, guardianEnvPrefix) {
			guardian.Env = append(guardian.Env, entry)
		}
	}
	guardian.Env = append(guardian.Env, guardianEnvPrefix+"1")
	guardian.ExtraFiles = []*os.File{readyWriter, lifetimeReader}
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := guardian.Start(); err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = lifetimeReader.Close()
		_ = lifetimeWriter.Close()
		return nil, fmt.Errorf("start process-group guardian: %w", err)
	}
	_ = readyWriter.Close()
	_ = lifetimeReader.Close()

	readyResult := make(chan error, 1)
	go func() {
		var ready [1]byte
		_, readErr := io.ReadFull(readyReader, ready[:])
		readyResult <- readErr
	}()
	var readyErr error
	readyTimer := time.NewTimer(45 * time.Second)
	select {
	case readyErr = <-readyResult:
	case <-readyTimer.C:
		readyErr = errors.New("process-group guardian readiness timed out")
		_ = readyReader.Close()
		<-readyResult
	case <-ctx.Done():
		readyErr = ctx.Err()
		_ = readyReader.Close()
		<-readyResult
	}
	if !readyTimer.Stop() {
		select {
		case <-readyTimer.C:
		default:
		}
	}
	_ = readyReader.Close()
	if readyErr != nil {
		signalErr := syscall.Kill(-guardian.Process.Pid, syscall.SIGKILL)
		if errors.Is(signalErr, syscall.ESRCH) {
			signalErr = nil
		}
		lifetimeErr := lifetimeWriter.Close()
		waitErr := guardian.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok &&
				status.Signaled() && status.Signal() == syscall.SIGKILL {
				waitErr = nil
			}
		}
		return nil, errors.Join(
			fmt.Errorf("wait for process-group guardian readiness: %w", readyErr),
			signalErr,
			lifetimeErr,
			waitErr,
		)
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = guardian.Process.Pid
	return &ProcessGroupForTest{
		guardian:      guardian,
		pgid:          guardian.Process.Pid,
		lifetimeWrite: lifetimeWriter,
	}, nil
}

// Signal delivers signal to the owned subprocess group.
func (g *ProcessGroupForTest) Signal(signal os.Signal) error {
	if g == nil {
		return os.ErrProcessDone
	}
	sig, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported process-group signal %T", signal)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-g.pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// Close terminates the owned group and reaps its guardian exactly once.
func (g *ProcessGroupForTest) Close() error {
	if g == nil {
		return nil
	}
	g.closeOnce.Do(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.closed {
			return
		}

		signalErr := syscall.Kill(-g.pgid, syscall.SIGKILL)
		if errors.Is(signalErr, syscall.ESRCH) {
			signalErr = nil
		}
		// macOS quirk: once every member of the group is a zombie awaiting
		// reap (e.g. the caller already delivered SIGKILL via Signal and
		// waited the child, leaving only the unreaped guardian), XNU reports
		// EPERM for the pgid-addressed signal instead of ESRCH. The group
		// members are our own children, so a genuine permission failure is
		// not possible here; treat it as already-dead.
		if errors.Is(signalErr, syscall.EPERM) {
			signalErr = nil
		}
		lifetimeErr := g.lifetimeWrite.Close()
		waitErr := g.guardian.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok &&
				status.Signaled() && status.Signal() == syscall.SIGKILL {
				waitErr = nil
			}
		}
		g.closed = true
		g.closeErr = errors.Join(signalErr, lifetimeErr, waitErr)
	})
	return g.closeErr
}
