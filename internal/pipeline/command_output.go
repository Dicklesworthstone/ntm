package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

var errCommandOutputLimit = errors.New("pipeline command output exceeds capture limit")

// runCommandOutput captures an authoritative branch or iteration-source result.
// Unlike ordinary diagnostic command output, a truncated key or work list must
// NEVER be used for dispatch. Exceeding either stream's limit cancels the owned
// process group immediately and returns no usable output. The same group waiter
// as command steps keeps descendant work inside this call's lifetime.
//
// cmd must be a fresh exec.Command, not exec.CommandContext: a second context
// watcher or Cmd.Wait would compete with the group waiter's ownership protocol.
func runCommandOutput(ctx context.Context, cmd *exec.Cmd, stdoutLimit, stderrLimit int64) ([]byte, error) {
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
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	result := waitCommandWithProcessGroupCleanup(ownedCtx, cmd)
	if stdout.Truncated() || stderr.Truncated() {
		return nil, errors.Join(fmt.Errorf("%w (stdout: %d/%d bytes; stderr: %d/%d bytes)",
			errCommandOutputLimit, stdout.Total(), stdoutLimit, stderr.Total(), stderrLimit), result.Err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, result.Err)
	}
	if result.Err != nil {
		// Preserve exec.Output's useful exit diagnostics without retaining
		// unlimited stderr or exposing partial stdout to a work selector.
		var exit *exec.ExitError
		if errors.As(result.Err, &exit) {
			exit.Stderr = stderr.Bytes()
		}
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
