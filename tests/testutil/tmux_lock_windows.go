//go:build windows

package testutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// processGroupGuardianEnv mirrors the Unix guardian toggle so tests that
// disable the guardian (testutil_test.go) compile on Windows, where no
// guardian subprocess exists and the value is simply inert.
const processGroupGuardianEnv = "_NTM_TEST_PROCESS_GROUP_GUARD"

func acquireGlobalTmuxTestLock(t *testing.T) {
	t.Helper()
}

// ProcessGroupForTest falls back to the root process on Windows, where these
// tmux E2E subprocesses are not run.
type ProcessGroupForTest struct {
	cmd       *exec.Cmd
	closeOnce sync.Once
	closeErr  error
}

func NewProcessGroupForTest(ctx context.Context, cmd *exec.Cmd) (*ProcessGroupForTest, error) {
	if ctx == nil {
		return nil, errors.New("process-group context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, errors.New("process-group command is nil")
	}
	return &ProcessGroupForTest{cmd: cmd}, nil
}

func (g *ProcessGroupForTest) Signal(signal os.Signal) error {
	if g == nil || g.cmd == nil || g.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return g.cmd.Process.Signal(signal)
}

func (g *ProcessGroupForTest) Close() error {
	if g == nil {
		return nil
	}
	g.closeOnce.Do(func() {
		if err := g.Signal(os.Kill); err != nil && !errors.Is(err, os.ErrProcessDone) {
			g.closeErr = err
		}
	})
	return g.closeErr
}
