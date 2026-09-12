package pipeline

// Pipeline dispatch has to be exclusive against a tmux pane across NTM
// PROCESSES, not just within one.
//
// Executor.paneLocks serializes the capture → paste → confirm → wait window
// correctly, but it is a per-Executor in-memory map and every `ntm pipeline
// run` builds a fresh Executor. Two pipeline processes aimed at the same pane
// therefore had no mutual exclusion at all: one could paste while the other
// still owned the pane, and after an interruption the older prompt could run
// in place of the assignment NTM had just reported delivered (ntm#324).
//
// This is not the same guarantee as the submission check from ntm#320. That
// verifies THIS call's payload left the composer; it says nothing about who
// owns the pane next.
//
// The primitive is an advisory file lock, the same one internal/assignment,
// internal/config and internal/session already use. Its decisive property
// here is that the OS drops the lock when the holder dies, so a pipeline
// killed mid-dispatch cannot wedge a pane — there is no lease to expire, no
// stale-lock reaper, and no PID liveness check to get wrong.
//
// Layering matches internal/assignment/store_lock.go: the in-process channel
// lock stays as the fast path (same-process contention never reaches a
// syscall), with the file lock as the outer, cross-process gate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// paneLockDirName is the directory under .ntm/pipelines holding one lock file
// per pane.
const paneLockDirName = "locks"

// DefaultPaneLockWait bounds how long a pipeline waits for another process to
// release a pane before giving up.
//
// Waiting is right for the common case: two pipelines that merely overlap
// should interleave, not fail. Waiting FOREVER is not, because a wedged owner
// would hang the waiter with nothing to report. On expiry the step fails with
// a typed not-dispatched reason and nothing is pasted — an ambiguous prompt is
// never queued behind someone else's work.
const DefaultPaneLockWait = 2 * time.Minute

// ErrPaneBusyOtherProcess reports that another NTM process held the pane for
// the whole wait budget. It means nothing was dispatched.
var ErrPaneBusyOtherProcess = errors.New("pane is held by another ntm process")

// paneLockFileName maps a tmux pane identity to a lock file name.
//
// Pane ids are "%17"-shaped and targets can carry session/window text, so the
// name is hashed rather than embedded: a pane identity must never be able to
// introduce a path separator, a "..", or a platform-reserved name. A readable
// prefix is kept for humans debugging a stuck lock directory.
func paneLockFileName(paneID string) string {
	sum := sha256.Sum256([]byte(paneID))
	readable := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, paneID)
	if len(readable) > 24 {
		readable = readable[:24]
	}
	return fmt.Sprintf("pane-%s-%s.lock", readable, hex.EncodeToString(sum[:6]))
}

// paneLockPath returns the lock file path for a pane under the project's
// pipeline state directory.
func paneLockPath(projectDir, paneID string) string {
	return filepath.Join(pipelineStateDir(projectDir), paneLockDirName, paneLockFileName(paneID))
}

// acquirePaneLockCrossProcess takes the in-process pane lock and then the
// cross-process file lock, returning a release func that drops both.
//
// projectDir empty disables the cross-process half: without a project root
// there is no agreed location for the lock file, and a lock in a
// process-specific temp directory would be exclusion theatre — it would appear
// to work while excluding nobody. In that case this degrades to exactly the
// previous in-process behaviour rather than pretending to more.
//
// The lock files are never unlinked. A pane's lock file is a stable rendezvous
// point reused by every process that ever dispatches to that pane, and
// removing it opens the classic unlink race where two processes hold flocks on
// different inodes for the same path. They are empty and bounded by the number
// of panes.
func (e *Executor) acquirePaneLockCrossProcess(ctx context.Context, paneID string) (func(), error) {
	if paneID == "" {
		return func() {}, nil
	}

	// Fast path first: same-process contention is settled without a syscall,
	// and holding the in-process lock while waiting on the file lock keeps
	// this process's own steps queued in one place.
	releaseLocal, err := e.acquirePaneLock(ctx, paneID)
	if err != nil {
		return nil, err
	}

	projectDir := e.config.ProjectDir
	if projectDir == "" {
		return releaseLocal, nil
	}

	lockPath := paneLockPath(projectDir, paneID)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		// A pipeline that cannot create its lock directory must not silently
		// dispatch unprotected: that is the exact failure this prevents.
		releaseLocal()
		return nil, fmt.Errorf("prepare pane lock directory: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, e.paneLockWait())
	defer cancel()

	locked, err := openLockedFile(waitCtx, lockPath)
	if err != nil {
		releaseLocal()
		// Distinguish "the caller cancelled" from "we waited out the budget";
		// only the latter is a busy pane.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s (waited %s)", ErrPaneBusyOtherProcess, paneID, e.paneLockWait())
		}
		return nil, fmt.Errorf("acquire pane lock: %w", err)
	}

	return func() {
		locked.unlockAndClose()
		releaseLocal()
	}, nil
}

// paneLockWait returns the configured cross-process wait budget.
func (e *Executor) paneLockWait() time.Duration {
	if e.config.PaneLockWait > 0 {
		return e.config.PaneLockWait
	}
	return DefaultPaneLockWait
}

// applyPaneLockFailure records a failed pane acquisition on a step result.
//
// The outcomes are reported differently on purpose. A cancelled context is the
// operator stopping the run; a busy pane is another process owning the target,
// which is not this run's fault; anything else is the lock itself failing.
// All of them mean nothing was dispatched — the lock is taken before the first
// capture, so no prompt was pasted and none was queued.
//
// StatusCancelled is load-bearing, not cosmetic: shouldRerunStep re-runs a
// cancelled step on resume, which is exactly right for a step that never
// dispatched. Reclassifying any of these as StatusSkipped would make resume
// treat an undelivered step as done.
func applyPaneLockFailure(result *StepResult, paneID string, err error) {
	result.Status = StatusCancelled
	result.FinishedAt = time.Now()

	switch {
	case errors.Is(err, ErrPaneBusyOtherProcess):
		result.SkipKind = SkipKindPaneBusy
		result.SkipReason = fmt.Sprintf(
			"not dispatched: pane %s is held by another ntm process; nothing was sent", paneID)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		result.SkipKind = SkipKindCancelled
		result.SkipReason = "cancelled while waiting for pane"
	default:
		// The lock could not be taken at all (unreadable lock directory,
		// permissions, a full disk). Naming it beats reporting a cancellation
		// that never happened.
		result.SkipKind = SkipKindCancelled
		result.SkipReason = fmt.Sprintf("not dispatched: could not lock pane %s: %v", paneID, err)
	}
}
