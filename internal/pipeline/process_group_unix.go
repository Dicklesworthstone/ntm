//go:build !windows

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	commandCancelGracePeriod = 5 * time.Second
	commandPipeDrainTimeout  = time.Second
	commandGroupPollInterval = 50 * time.Millisecond
	// Keep the original, unreaped leader through a bounded read-only recovery
	// when the post-kill census failed. Reaping after the first short outage
	// would permanently discard the identity needed to certify settlement.
	commandSettlementRecoveryTimeout = 5 * time.Second
)

// Per-command fault injection keeps concurrent waiters independent. Cleanup
// contexts retain these values even after the command's context is cancelled.
type commandGroupObservationTestHookKey struct{}

// commandSettlementBudgetContextKey marks post-signal settlement/recovery
// contexts created with context.WithoutCancel(ctx), where only the local
// settlement deadline can expire and a completed quiet census remains valid.
type commandSettlementBudgetContextKey struct{}

func withCommandSettlementBudget(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	base := context.WithValue(context.WithoutCancel(parent), commandSettlementBudgetContextKey{}, true)
	return context.WithTimeout(base, timeout)
}

type commandCleanupResult struct {
	Err        error
	Cancelled  bool
	SignalSent string
	Settled    bool // The owned group is quiet and its input/output copies settled.
	// Linux can prove the leader exited before cleanup signalled the group.
	// Preserve only that leader's actual Wait failure, independently of later
	// cancellation or inspection errors. This is not settlement evidence.
	ExitBeforeSignalErr error

	// Internal proof acquired by Linux's final ownership check before TERM.
	leaderExitedBeforeSignal bool
}

// verifyCommandOutputSettlement runs after the process-group proof and the
// Cmd.Wait join. An inherited I/O pipe can outlive the group (setsid), and
// Wait's ExitError takes precedence over a concurrent ErrWaitDelay. Such a run
// must keep its launch journal unresolved, not authorize an automatic retry.
// This does not contain arbitrary detached work after the owned I/O completes.
func verifyCommandOutputSettlement(cmd *exec.Cmd, result *commandCleanupResult) {
	if !result.Settled {
		return
	}
	if errors.Is(result.Err, exec.ErrWaitDelay) {
		result.Settled = false
	}
	if input, ok := cmd.Stdin.(*commandInput); ok {
		if err := input.settlementError(); err != nil {
			result.Settled = false
			result.Err = errors.Join(result.Err, err)
		}
	}
	for _, stream := range []struct {
		name   string
		writer interface{}
	}{{"stdout", cmd.Stdout}, {"stderr", cmd.Stderr}} {
		capture, ok := stream.writer.(interface{ outputCaptureComplete() bool })
		if ok && !capture.outputCaptureComplete() {
			result.Settled = false
			result.Err = errors.Join(result.Err, fmt.Errorf("%s output capture did not reach EOF; inspect detached command work before replay", stream.name))
		}
	}
}

func configureCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Install this after Stdin is assigned and before Start creates its copy
	// goroutine. Direct files have no os/exec copier or hidden WaitDelay error.
	switch cmd.Stdin.(type) {
	case nil, *os.File, *commandInput:
	default:
		cmd.Stdin = &commandInput{source: cmd.Stdin}
	}
	// A descendant that deliberately leaves the group can still inherit
	// its I/O. It must not hold cmd.Wait (and run ownership) forever.
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = commandPipeDrainTimeout
	}
}

// commandInput preserves stdin-copy evidence independently of Cmd.Wait, whose
// ExitError masks a concurrent WaitDelay failure. EOF means the payload was
// delivered; EPIPE from the destination means all pipe readers closed. A forced
// close or source-reader failure proves neither, and must not authorize replay.
type commandInput struct {
	source  io.Reader
	mu      sync.Mutex
	started bool
	active  int
	failure error
}

func (input *commandInput) Read(data []byte) (int, error) {
	return input.source.Read(data)
}

func (input *commandInput) WriteTo(dst io.Writer) (int64, error) {
	input.mu.Lock()
	input.started = true
	input.active++
	input.mu.Unlock()
	// Hide optional copy interfaces so a source WriterTo cannot bypass EOF
	// accounting and destination writes retain their own failure provenance.
	pipe := &commandInputPipeWriter{writer: dst}
	n, err := io.Copy(pipe, struct{ io.Reader }{input.source})
	input.mu.Lock()
	input.active--
	if err != nil && !pipe.readersClosed {
		input.failure = errors.Join(input.failure, err)
	}
	input.mu.Unlock()
	// Keep the original write error: os/exec recognizes and ignores the
	// kernel's EPIPE when a normal command simply declines unused input.
	return n, err
}

func (input *commandInput) settlementError() error {
	input.mu.Lock()
	defer input.mu.Unlock()
	if input.started && input.active == 0 && input.failure == nil {
		return nil
	}
	return errors.Join(errors.New("stdin copy did not settle; inspect detached command work before replay"), input.failure)
}

type commandInputPipeWriter struct {
	writer        io.Writer
	readersClosed bool
}

func (pipe *commandInputPipeWriter) Write(data []byte) (int, error) {
	n, err := pipe.writer.Write(data)
	if errors.Is(err, syscall.EPIPE) {
		pipe.readersClosed = true
	}
	return n, err
}

// cancelCommandProcessGroup force-stops an owned group. The caller must still
// hold its leader identity; it must not be used after a competing Cmd.Wait.
func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	return signalCommandProcessGroup(cmd, syscall.SIGKILL)
}

// waitCommandGroupObservation supervises the same three observation outcomes on
// every Unix platform: live, quiet, or unknown. Unknown never authorizes a signal
// or settlement. Callers own the lifecycle deadline, including independent grace
// and post-kill budgets, and retain the leader identity until supervision ends.
func waitCommandGroupObservation(ctx context.Context, pgid int, observe func() (bool, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	switch hook := ctx.Value(commandGroupObservationTestHookKey{}).(type) {
	case func(int) (bool, error):
		if hook != nil {
			observe = func() (bool, error) { return hook(pgid) }
		}
	case func(context.Context, int) (bool, error):
		if hook != nil {
			observe = func() (bool, error) { return hook(ctx, pgid) }
		}
	}
	ticker := time.NewTicker(commandGroupPollInterval)
	defer ticker.Stop()
	var observationErr error
	interrupted := func() error {
		if observationErr != nil {
			return errors.Join(ctx.Err(), fmt.Errorf("command group observation failed: %w", observationErr))
		}
		return ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return interrupted()
		}
		alive, err := observe()
		observationErr = err
		settlementBudget, _ := ctx.Value(commandSettlementBudgetContextKey{}).(bool)
		if settlementBudget && err == nil && !alive {
			return nil
		}
		// A caller can cancel during the observation. Do not let a late quiet
		// snapshot bypass the cancellation path and its cleanup evidence.
		if ctx.Err() != nil {
			return interrupted()
		}
		if err == nil && !alive {
			return nil
		}
		// In particular, do not reap after an observation failure: WaitDelay
		// could close a healthy child's output pipe, and reaping would release
		// the identity needed for any subsequent cancellation.
		select {
		case <-ctx.Done():
			return interrupted()
		case <-ticker.C:
		}
	}
}

// reconcileCommandGroupSettlement gives a failed final census an independent
// recovery budget while the caller still pins the original group leader and
// owns its execution journal. Call only after a successful final group signal.
// A plain deadline carries no failed inspection to reconcile and keeps the
// ordinary bounded cleanup contract. Unknown evidence receives read-only
// retries, never another signal or permission to replay.
func reconcileCommandGroupSettlement(ctx context.Context, pgid int, observationErr error) error {
	if observationErr == nil || observationErr == context.DeadlineExceeded {
		return observationErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recovery, cancel := withCommandSettlementBudget(ctx, commandSettlementRecoveryTimeout)
	defer cancel()
	if err := waitCommandGroupQuiescent(recovery, pgid); err != nil {
		return errors.Join(observationErr, fmt.Errorf("command group settlement recovery exhausted: %w", err))
	}
	return nil
}

// observeCommandGroupWithPS is the portable Unix census. Keep its real process
// boundary shared so the production observer and retry policy can be tested on
// Linux as well as the platforms that use it for command cleanup.
func observeCommandGroupWithPS(ctx context.Context, pgid int) (bool, error) {
	if pgid <= 1 {
		return false, fmt.Errorf("inspect command process group: invalid group ID %d", pgid)
	}
	probe, cancel := context.WithTimeout(ctx, commandPipeDrainTimeout)
	defer cancel()
	// BSD and AIX ps name this column "state"; Solaris and illumos use "s".
	stateColumn := "state="
	if runtime.GOOS == "solaris" || runtime.GOOS == "illumos" {
		stateColumn = "s="
	}
	cmd := exec.CommandContext(probe, "ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", stateColumn)
	cmd.WaitDelay = commandPipeDrainTimeout
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("inspect command process group: %w", errors.Join(probe.Err(), err))
	}
	return parseCommandGroupPSOutput(output, pgid)
}

// The leader must appear in every complete snapshot until Cmd.Wait. Missing or
// malformed output fails closed; restricted ps output or a permission failure
// from kill(0) is not proof that the owned group stopped executing.
func parseCommandGroupPSOutput(output []byte, pgid int) (bool, error) {
	if pgid <= 1 {
		return false, fmt.Errorf("inspect command process group: invalid group ID %d", pgid)
	}
	leaderSeen, alive := false, false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return false, fmt.Errorf("inspect command process group: malformed ps row %q", line)
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil || pid < 0 || group < 0 {
			return false, fmt.Errorf("inspect command process group: invalid ps process identity %q", line)
		}
		if pid == pgid {
			if group != pgid {
				return false, errors.New("inspect command process group: leader changed process group")
			}
			leaderSeen = true
		}
		if group == pgid && fields[2][0] != 'Z' {
			alive = true
		}
	}
	if !leaderSeen {
		return false, errors.New("inspect command process group: unreaped leader missing from ps output")
	}
	return alive, nil
}

func signalCommandProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if cmd.Process.Pid <= 1 || cmd.ProcessState != nil || cmd.SysProcAttr == nil ||
		!cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		return errors.New("signal command process group: an owned, unreaped group leader is required")
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if errors.Is(err, syscall.EPERM) && groupSignalFoundOnlyZombies(cmd.Process.Pid) {
		return os.ErrProcessDone
	}
	// A failed group signal is not repaired by killing only its leader.
	// Keep the original failure so the caller cannot publish settlement on
	// the strength of a partial fallback. Linux can additionally stop group
	// members through identity-stable handles, never stale numeric PIDs.
	groupErr := fmt.Errorf("signal command process group: %w", err)
	if sig == syscall.SIGKILL {
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(groupErr, killErr, killProcessGroupDescendants(cmd.Process.Pid))
	}
	fallbackErr := cmd.Process.Signal(sig)
	if errors.Is(fallbackErr, os.ErrProcessDone) {
		fallbackErr = nil
	}
	return errors.Join(groupErr, fallbackErr)
}

// parseProcStatPGID extracts the process group id (field 5) from
// /proc/<pid>/stat, taking care to skip past the comm field which can
// contain spaces and parentheses.
func parseProcStatPGID(stat []byte) (int, bool) {
	idx := -1
	for i := len(stat) - 1; i >= 0; i-- {
		if stat[i] == ')' {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(stat) {
		return 0, false
	}
	rest := strings.TrimSpace(string(stat[idx+1:]))
	fields := strings.Fields(rest)
	// fields[0]=state, fields[1]=ppid, fields[2]=pgrp.
	if len(fields) < 3 {
		return 0, false
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, false
	}
	return pgid, true
}
