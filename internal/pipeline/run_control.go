package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

var (
	ErrRunAlreadyOwned       = errors.New("pipeline run has a live owner")
	ErrRunControlUnavailable = errors.New("pipeline run has no acknowledging control owner")
	ErrRunControlChanged     = errors.New("pipeline run ownership changed; inspect the current run before retrying")
)

const runControlPollInterval = 25 * time.Millisecond

// RunControl owns one execution attempt, not a PID or a persisted status row.
// The OS lock fences concurrent executors; a random token fences delayed
// cancellation requests from later attempts reusing the same run ID.
// Call Close only after the executor and its cleanup workers have returned.
// Lock files are stable rendezvous points and are never unlinked.
//
// This is wired into serve's file/inline/resume paths (including Jobs). A
// legacy or CLI execution that has not acquired this control is deliberately
// not advertised as remotely cancellable.
type RunControl struct {
	ctx    context.Context
	cancel context.CancelFunc
	lock   *lockedFile
	prefix string
	record runControlRecord
	done   chan struct{}
	once   sync.Once
}

type runControlRecord struct {
	Token  string `json:"token"`
	Active bool   `json:"active"`
}

type runControlSignal struct {
	Token string `json:"token"`
}

// AcquireRunControl must precede loading resume state and any execution-side
// writes. Contention fails rather than queuing a stale resume behind a live
// executor. The kernel releases the lock after a crash without a stale-PID
// heuristic or a lease-expiry race.
func AcquireRunControl(ctx context.Context, projectDir, runID string) (*RunControl, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix, err := runControlPrefix(projectDir, runID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0700); err != nil {
		return nil, fmt.Errorf("prepare run control: %w", err)
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, 100*time.Millisecond)
	lock, err := openLockedFile(lockCtx, prefix+".lock")
	cancelLock()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s", ErrRunAlreadyOwned, runID)
		}
		return nil, fmt.Errorf("acquire run control: %w", err)
	}
	if err := ctx.Err(); err != nil {
		lock.unlockAndClose()
		return nil, err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		lock.unlockAndClose()
		return nil, fmt.Errorf("create run control token: %w", err)
	}
	record := runControlRecord{Token: hex.EncodeToString(token[:]), Active: true}
	if err := writeRunControl(prefix+".owner", record); err != nil {
		lock.unlockAndClose()
		return nil, fmt.Errorf("publish run control: %w", err)
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	control := &RunControl{ctx: ownedCtx, cancel: cancel, lock: lock, prefix: prefix, record: record, done: make(chan struct{})}
	go control.watchCancellation()
	return control, nil
}

// Context is the context the executor must inherit for cancellation to stop
// the actual work rather than only change a bookkeeping row.
func (c *RunControl) Context() context.Context { return c.ctx }

// Close joins the watcher before retiring ownership and releasing the lock.
// A cancellation cannot acknowledge itself after the next owner starts.
func (c *RunControl) Close() {
	c.once.Do(func() {
		c.cancel()
		<-c.done
		c.record.Active = false
		if err := writeRunControl(c.prefix+".owner", c.record); err != nil {
			slog.Warn("retire pipeline run control", "error", err)
		}
		c.lock.unlockAndClose()
	})
}

func (c *RunControl) watchCancellation() {
	defer close(c.done)
	ticker := time.NewTicker(runControlPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			var request runControlSignal
			if readRunControl(c.prefix+".request", &request) != nil || request.Token != c.record.Token {
				continue
			}
			c.cancel()
			// This acknowledges delivery to the owning execution context, not
			// completion. Only the executor may persist a terminal run status.
			if err := writeRunControl(c.prefix+".ack", request); err != nil {
				slog.Warn("acknowledge pipeline cancellation", "error", err)
			}
			return
		}
	}
}

// RequestRunCancellation asks the current owner to cancel its execution
// context and waits for its acknowledgment. A stale on-disk "running" row,
// dead process, or late request from a prior attempt cannot report success.
// The wait is bounded even when the HTTP request has no deadline.
func RequestRunCancellation(ctx context.Context, projectDir, runID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix, err := runControlPrefix(projectDir, runID)
	if err != nil {
		return err
	}
	var owner runControlRecord
	if err := readRunControl(prefix+".owner", &owner); err != nil {
		return fmt.Errorf("%w: %v", ErrRunControlUnavailable, err)
	}
	if !owner.Active || owner.Token == "" {
		return ErrRunControlUnavailable
	}
	if err := writeRunControl(prefix+".request", runControlSignal{Token: owner.Token}); err != nil {
		return fmt.Errorf("request pipeline cancellation: %w", err)
	}
	ticker := time.NewTicker(runControlPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrRunControlUnavailable, err)
		}
		if runControlAcknowledged(prefix, owner.Token) {
			return nil
		}
		var current runControlRecord
		if err := readRunControl(prefix+".owner", &current); err != nil {
			return fmt.Errorf("%w: %v", ErrRunControlUnavailable, err)
		}
		if current.Token != owner.Token {
			return ErrRunControlChanged
		}
		if !current.Active {
			// Close can retire ownership between the two reads above. Check
			// the final acknowledgment before reporting an unavailable owner.
			if runControlAcknowledged(prefix, owner.Token) {
				return nil
			}
			return ErrRunControlUnavailable
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrRunControlUnavailable, ctx.Err())
		case <-ticker.C:
		}
	}
}

func runControlAcknowledged(prefix, token string) bool {
	var ack runControlSignal
	return readRunControl(prefix+".ack", &ack) == nil && ack.Token == token
}

func runControlPrefix(projectDir, runID string) (string, error) {
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	if strings.TrimSpace(projectDir) == "" {
		return "", errors.New("project directory is required for run control")
	}
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(pipelineStateDir(normalizeLockRoot(projectDir)), "control", hex.EncodeToString(sum[:])), nil
}

func readRunControl(path string, into interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("run control record exceeds size limit")
	}
	return json.Unmarshal(data, into)
}

func writeRunControl(path string, record interface{}) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(path, data, 0600)
}

// LoadPipelineSnapshot reads a run from an explicit project, independently of
// the controller's working directory or in-memory registry.
func LoadPipelineSnapshot(projectDir, runID string) *PipelineExecution {
	state, err := LoadState(projectDir, runID)
	if err != nil {
		return nil
	}
	return snapshotFromPersistedState(state)
}
