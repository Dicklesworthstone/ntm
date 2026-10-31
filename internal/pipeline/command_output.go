package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

var errCommandOutputLimit = errors.New("pipeline command output exceeds capture limit")
var errCommandOutputUnsettled = errors.New("pipeline command output lacks verified process and stream settlement")

// A selector owner can persist launch intent before any process starts, then
// publish settlement while it still owns the run. These callbacks are local
// to one evaluation and never write state after the capture call returns.
type commandOutputLifecycleKey struct{}

type commandOutputLifecycle struct {
	beforeStart func() error
	afterWait   func(cmd *exec.Cmd, settled bool, err error) error
}

// runCommandOutput captures an authoritative branch or iteration-source result.
// Unlike ordinary diagnostic command output, a truncated key or work list must
// NEVER be used for dispatch. Exceeding either stream's limit cancels the owned
// process group immediately and returns no usable output. The same group waiter
// as command steps keeps descendant work inside this call's lifetime.
//
// cmd must be a fresh exec.Command, not exec.CommandContext: a second context
// watcher or Cmd.Wait would compete with the group waiter's ownership protocol.
func runCommandOutput(ctx context.Context, cmd *exec.Cmd, stdoutLimit, stderrLimit int64) (output []byte, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cmd == nil || cmd.Process != nil || cmd.Cancel != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("command output capture requires a fresh command without a context watcher or output writers")
	}
	if stdoutLimit <= 0 || stderrLimit <= 0 {
		return nil, errors.New("command output capture requires positive stdout and stderr byte limits")
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout, stderr := newCappedWriter(stdoutLimit), newCappedWriter(stderrLimit)
	cmd.Stdout = &cancelOnTruncationWriter{capture: stdout, cancel: cancel}
	cmd.Stderr = &cancelOnTruncationWriter{capture: stderr, cancel: cancel}
	configureCommandProcessGroup(cmd)
	lifecycle, _ := ctx.Value(commandOutputLifecycleKey{}).(*commandOutputLifecycle)
	if lifecycle != nil && lifecycle.beforeStart != nil {
		if err := lifecycle.beforeStart(); err != nil {
			return nil, err
		}
	}
	settled := true // Before Start there can be no owned command process.
	defer func() {
		if lifecycle != nil && lifecycle.afterWait != nil {
			if err := lifecycle.afterWait(cmd, settled, retErr); err != nil {
				output = nil
				retErr = errors.Join(retErr, err)
			}
		}
		// Settlement persistence may itself outlive the caller's deadline.
		// The process proof stays valid, but cancelled evaluation output must
		// not be published as a fresh branch or iteration decision.
		if err := ctx.Err(); err != nil {
			output = nil
			if !errors.Is(retErr, err) {
				retErr = errors.Join(err, retErr)
			}
		}
	}()
	// Durable intent can take long enough for cancellation to arrive. Record
	// the no-process proof without dispatching work after that boundary.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settled = false
	if err := cmd.Start(); err != nil {
		settled = true
		return nil, err
	}
	result := waitCommandWithProcessGroupCleanup(ownedCtx, cmd)
	// Neither an exit status nor a bounded prefix proves that a selector's
	// work ended. ExitError can hide a concurrent pipe-drain timeout, and a
	// process-group observation failure can leave cleanup unresolved even
	// after both pipes close. Preserve these independent admission checks.
	settled = result.Settled && stdout.outputCaptureComplete() && stderr.outputCaptureComplete()
	if !settled {
		result.Err = errors.Join(errCommandOutputUnsettled, result.Err)
	}
	// Preserve exec.Output's useful exit diagnostics on every failure path,
	// including cancellation, overflow, and uncertain cleanup. The retained
	// stderr remains bounded and stdout is never returned on an error.
	var exit *exec.ExitError
	if errors.As(result.Err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	if stdout.Truncated() || stderr.Truncated() {
		return nil, errors.Join(fmt.Errorf("%w (stdout: %d/%d bytes; stderr: %d/%d bytes)",
			errCommandOutputLimit, stdout.Total(), stdoutLimit, stderr.Total(), stderrLimit), result.Err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, result.Err)
	}
	if result.Err != nil {
		return nil, result.Err
	}
	return stdout.Bytes(), nil
}

type cancelOnTruncationWriter struct {
	capture *cappedWriter
	cancel  context.CancelFunc
}

func (w *cancelOnTruncationWriter) Write(data []byte) (int, error) {
	n, err := w.capture.Write(data)
	if w.capture.Truncated() {
		w.cancel()
	}
	return n, err
}

func (w *cancelOnTruncationWriter) ReadFrom(r io.Reader) (int64, error) {
	return w.capture.readFrom(r, w)
}

func (w *cancelOnTruncationWriter) outputCaptureComplete() bool {
	return w.capture.outputCaptureComplete()
}

var _ io.ReaderFrom = (*cancelOnTruncationWriter)(nil)
