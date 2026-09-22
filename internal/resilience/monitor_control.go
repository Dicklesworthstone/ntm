package resilience

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

var ErrSessionMonitorOwned = errors.New("session already has a live monitor owner")

const monitorGenerationEnv = "NTM_INTERNAL_MONITOR_GENERATION"
const monitorStartupTimeout = 30 * time.Second
const monitorControlPoll = 100 * time.Millisecond

// SessionMonitorStatus describes an actual resident generation. Alive and
// Healthy are computed when read; a persisted "running" row alone is not proof.
type SessionMonitorStatus struct {
	Session         string    `json:"session"`
	Generation      string    `json:"generation"`
	PID             int       `json:"pid"`
	ProcessStarted  time.Time `json:"process_started"`
	State           string    `json:"state"`
	HeartbeatAt     time.Time `json:"heartbeat_at"`
	AccountRotation bool      `json:"account_rotation"`
	Error           string    `json:"error,omitempty"`
	Alive           bool      `json:"alive"`
	Healthy         bool      `json:"healthy"`
}

type monitorStopRequest struct {
	Generation string `json:"generation"`
}

// SessionMonitorLease is the sole writer and controller for one session's
// resident process. Close only after all actuation loops have returned.
type SessionMonitorLease struct {
	ctx       context.Context
	cancel    context.CancelFunc
	lock      *os.File
	prefix    string
	launched  bool
	mu        sync.Mutex
	status    SessionMonitorStatus
	done      chan struct{}
	closeOnce sync.Once
}

func monitorPrefix(session string) (string, error) {
	safe, err := sanitizeSessionName(session)
	if err != nil {
		return "", err
	}
	// Keep runtime control files separate from the saved launch manifests.
	dir := filepath.Join(ManifestDir(), "monitors")
	return filepath.Join(dir, safe+"-monitor"), nil
}

func prepareMonitorPrefix(session string) (string, error) {
	prefix, err := monitorPrefix(session)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0700); err != nil {
		return "", err
	}
	return prefix, nil
}

func newMonitorGeneration() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

func writeMonitorJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(path, data, 0600)
}

func readMonitorJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(value)
}

func waitMonitorLock(ctx context.Context, path string) (*os.File, error) {
	ticker := time.NewTicker(monitorControlPoll)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := tryMonitorLock(path)
		if !errors.Is(err, ErrSessionMonitorOwned) {
			return file, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// AcquireSessionMonitor fences every entry to internal-monitor, including
// direct invocations, before a manifest is read or any daemon is started.
func AcquireSessionMonitor(ctx context.Context, session string) (*SessionMonitorLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix, err := prepareMonitorPrefix(session)
	if err != nil {
		return nil, err
	}
	// A directly invoked monitor participates in the launch gate too. Children
	// started by StartSessionMonitor carry a generation and must enter while
	// their parent holds this gate for the readiness handshake.
	if os.Getenv(monitorGenerationEnv) == "" {
		launchLock, err := waitMonitorLock(ctx, prefix+".launch.lock")
		if err != nil {
			return nil, err
		}
		defer launchLock.Close()
	}
	lock, err := tryMonitorLock(prefix + ".owner.lock")
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	manifest, err := LoadManifest(session)
	if err != nil {
		return nil, err
	}
	if manifest.Session != session {
		return nil, errors.New("session monitor manifest identity mismatch")
	}
	generation := os.Getenv(monitorGenerationEnv)
	launched := generation != ""
	if launched {
		if len(generation) != 32 || generation != manifest.MonitorGeneration {
			return nil, errors.New("session monitor launch generation no longer matches its manifest")
		}
	} else if generation, err = newMonitorGeneration(); err != nil {
		return nil, err
	}
	started, err := process.StartTime(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("identify session monitor process: %w", err)
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	lease := &SessionMonitorLease{
		ctx: ownedCtx, cancel: cancel, lock: lock, prefix: prefix, launched: launched, done: make(chan struct{}),
		status: SessionMonitorStatus{
			Session: session, Generation: generation, PID: os.Getpid(), ProcessStarted: started,
			State: "starting", HeartbeatAt: time.Now().UTC(), AccountRotation: manifest.AccountRotation != nil,
		},
	}
	if err := lease.writeStatusLocked(); err != nil {
		cancel()
		return nil, err
	}
	keepLock = true
	go lease.watchControl()
	return lease, nil
}

func (l *SessionMonitorLease) Context() context.Context { return l.ctx }

func (l *SessionMonitorLease) writeStatusLocked() error {
	l.status.HeartbeatAt = time.Now().UTC()
	return writeMonitorJSON(l.prefix+".status.json", l.status)
}

func (l *SessionMonitorLease) watchControl() {
	defer close(l.done)
	ticker := time.NewTicker(monitorControlPoll)
	defer ticker.Stop()
	lastHeartbeat := time.Now()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			var stop monitorStopRequest
			if readMonitorJSON(l.prefix+".stop.json", &stop) == nil && stop.Generation == l.status.Generation {
				l.cancel()
				return
			}
			if time.Since(lastHeartbeat) >= 2*time.Second {
				l.mu.Lock()
				err := l.writeStatusLocked()
				l.mu.Unlock()
				if err != nil {
					l.cancel() // Loss of control/status must stop further actuation.
					return
				}
				lastHeartbeat = time.Now()
			}
		}
	}
}

// ConfirmReady must follow all read-only initialization and precede actuation.
// Detached launches require authorization from the parent after this process
// has published its own readiness. EOF, cancellation or timeout authorizes no
// work. Direct operator invocations are already authorized by their command.
func (l *SessionMonitorLease) ConfirmReady(ctx context.Context, input io.ReadCloser) error {
	if ctx == nil {
		ctx = l.ctx
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	l.status.State = "ready"
	err := l.writeStatusLocked()
	l.mu.Unlock()
	if err != nil {
		return err
	}
	if l.launched {
		if input == nil {
			return errors.New("session monitor requires a startup authorization pipe")
		}
		defer input.Close()
		result := make(chan error, 1)
		go func() {
			line, readErr := bufio.NewReader(io.LimitReader(input, 128)).ReadString('\n')
			if readErr == nil && strings.TrimSpace(line) != l.status.Generation {
				readErr = errors.New("invalid session monitor startup authorization")
			}
			result <- readErr
		}()
		timer := time.NewTimer(monitorStartupTimeout)
		defer timer.Stop()
		select {
		case err := <-result:
			if err != nil {
				return fmt.Errorf("session monitor startup was not authorized: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-l.ctx.Done():
			return l.ctx.Err()
		case <-timer.C:
			return errors.New("session monitor startup authorization timed out")
		}
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status.State = "running"
	return l.writeStatusLocked()
}

// Fail preserves an actionable startup/runtime error in the generation receipt.
func (l *SessionMonitorLease) Fail(err error) {
	if err == nil {
		return
	}
	l.cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status.State, l.status.Error = "failed", err.Error()
	_ = l.writeStatusLocked()
}

func (l *SessionMonitorLease) Close() {
	l.closeOnce.Do(func() {
		l.cancel()
		<-l.done
		l.mu.Lock()
		if l.status.State != "failed" {
			l.status.State = "stopped"
		}
		_ = l.writeStatusLocked()
		l.mu.Unlock()
		_ = l.lock.Close()
	})
}

func ReadSessionMonitorStatus(session string) (*SessionMonitorStatus, error) {
	prefix, err := monitorPrefix(session)
	if err != nil {
		return nil, err
	}
	var status SessionMonitorStatus
	if err := readMonitorJSON(prefix+".status.json", &status); err != nil {
		return nil, err
	}
	if status.Session != session || status.Generation == "" {
		return nil, errors.New("session monitor status identity mismatch")
	}
	if status.State != "stopped" && status.State != "failed" && process.IsAlive(status.PID) {
		started, err := process.StartTime(status.PID)
		status.Alive = err == nil && started.Equal(status.ProcessStarted)
	}
	status.Healthy = status.Alive && status.State == "running" && time.Since(status.HeartbeatAt) < 10*time.Second
	return &status, nil
}

// StopSessionMonitor never signals an arbitrary PID. It targets the currently
// owned generation, then waits until that owner has joined its loops and
// released the lease. A stale stop file cannot cancel a later generation.
func StopSessionMonitor(ctx context.Context, session string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, monitorStartupTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix, err := monitorPrefix(session)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(prefix)); errors.Is(err, os.ErrNotExist) {
		legacyAlive, err := unownedMonitorAlive(ctx, session)
		if err != nil {
			return err
		}
		if legacyAlive {
			return errors.New("an older session monitor has no generation control; stop it before replacing or stopping this session")
		}
		return nil
	} else if err != nil {
		return err
	}
	launchLock, err := waitMonitorLock(ctx, prefix+".launch.lock")
	if err != nil {
		return err
	}
	defer launchLock.Close()
	owner, err := tryMonitorLock(prefix + ".owner.lock")
	if err == nil {
		defer owner.Close()
		legacyAlive, err := unownedMonitorAlive(ctx, session)
		if err != nil {
			return err
		}
		if legacyAlive {
			return errors.New("an older session monitor has no generation control; stop it before replacing or stopping this session")
		}
		return nil
	}
	if !errors.Is(err, ErrSessionMonitorOwned) {
		return err
	}
	status, err := ReadSessionMonitorStatus(session)
	if err != nil {
		return fmt.Errorf("identify owned monitor before stopping: %w", err)
	}
	if err := writeMonitorJSON(prefix+".stop.json", monitorStopRequest{Generation: status.Generation}); err != nil {
		return err
	}
	ticker := time.NewTicker(monitorControlPoll)
	defer ticker.Stop()
	for {
		owner, err := tryMonitorLock(prefix + ".owner.lock")
		if err == nil {
			return owner.Close()
		}
		if !errors.Is(err, ErrSessionMonitorOwned) {
			return err
		}
		current, err := ReadSessionMonitorStatus(session)
		if err != nil || current.Generation != status.Generation {
			return errors.New("session monitor ownership changed while stopping")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for session monitor to stop: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
