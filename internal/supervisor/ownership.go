package supervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrDaemonOwned means another supervisor holds this project's daemon.
	ErrDaemonOwned = errors.New("daemon is owned by another supervisor")
	// ErrDaemonRecoveryRequired forbids replay of an uncertain launch or
	// replacement of a process that may still be using the daemon's store.
	ErrDaemonRecoveryRequired = errors.New("daemon ownership requires inspection")
)

// DaemonOwnershipError identifies the retained launch evidence. It never
// grants permission to signal the recorded PID: that PID may have been reused.
type DaemonOwnershipError struct {
	Name    string
	Path    string
	OwnerID string
	PID     int
	Cause   error
}

func (e *DaemonOwnershipError) Error() string {
	return fmt.Sprintf("daemon %q: %v (owner=%q pid=%d; inspect %s before restarting)", e.Name, e.Cause, e.OwnerID, e.PID, e.Path)
}

func (e *DaemonOwnershipError) Unwrap() error { return e.Cause }

const daemonOwnershipVersion = 1
const maxDaemonOwnershipBytes = 64 << 10

type daemonOwnershipRecord struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	OwnerID    string    `json:"owner_id"`
	Supervisor int       `json:"supervisor_pid"`
	PID        int       `json:"pid"`
	Phase      string    `json:"phase"` // launching, running, stopped
	UpdatedAt  time.Time `json:"updated_at"`
}

// The stable lock inode is separate from the atomically replaced record.
// The same lease is handed to each restart generation, including backoff.
// Never unlink the lock file: doing so creates independent competing locks.
type daemonOwnership struct {
	mu       sync.Mutex
	lock     *os.File
	lockPath string
	path     string
	record   daemonOwnershipRecord
}

func validateDaemonComponent(value, kind string) error {
	if strings.TrimSpace(value) == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	return nil
}

func acquireDaemonOwnership(ntmDir, name, owner string) (*daemonOwnership, error) {
	dir := filepath.Join(ntmDir, "pids")
	// A canonical path makes project symlink aliases share one namespace.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon ownership directory: %w", err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf(".daemon-%x", sha256.Sum256([]byte(name)))
	lease := &daemonOwnership{
		lockPath: filepath.Join(dir, key+".lock"),
		path:     filepath.Join(dir, key+".json"),
		record: daemonOwnershipRecord{
			Version: daemonOwnershipVersion, Name: name, OwnerID: owner,
			Supervisor: os.Getpid(), Phase: "stopped",
		},
	}
	lease.lock, err = lockDaemonOwnership(lease.lockPath)
	if err != nil {
		return nil, &DaemonOwnershipError{Name: name, Path: lease.path, Cause: err}
	}
	ok := false
	defer func() {
		if !ok {
			_ = lease.closeLocked()
		}
	}()
	previous, err := readDaemonOwnership(lease.path)
	if err != nil {
		return nil, lease.recoveryError(err)
	}
	if previous != nil {
		if previous.Version != daemonOwnershipVersion || previous.Name != name || previous.OwnerID == "" || previous.PID < 0 {
			return nil, lease.recoveryError(errors.New("invalid or mismatched ownership record"))
		}
		switch previous.Phase {
		case "stopped":
			if previous.PID != 0 {
				return nil, lease.recoveryError(errors.New("stopped ownership record retains a PID"))
			}
		case "running":
			if previous.PID == 0 {
				return nil, lease.recoveryError(errors.New("running ownership record has no PID"))
			}
			live, probeErr := daemonProcessMayLive(previous.PID)
			if live || probeErr != nil {
				return nil, &DaemonOwnershipError{Name: name, Path: lease.path, OwnerID: previous.OwnerID, PID: previous.PID,
					Cause: errors.Join(ErrDaemonRecoveryRequired, probeErr, errors.New("previous daemon process or process group may still be alive"))}
			}
		case "launching":
			// The former owner could have died after exec but before saving
			// its child's PID. An unlocked file does not prove no child exists.
			return nil, &DaemonOwnershipError{Name: name, Path: lease.path, OwnerID: previous.OwnerID,
				Cause: errors.Join(ErrDaemonRecoveryRequired, errors.New("previous launch outcome is unknown"))}
		default:
			return nil, lease.recoveryError(errors.New("unknown ownership phase"))
		}
	}
	ok = true
	return lease, nil
}

func readDaemonOwnership(path string) (*daemonOwnershipRecord, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("ownership record is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("ownership record changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDaemonOwnershipBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDaemonOwnershipBytes {
		return nil, errors.New("ownership record exceeds size limit")
	}
	var record daemonOwnershipRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&record); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("ownership record contains trailing data")
	}
	return &record, nil
}

func (o *daemonOwnership) recoveryError(err error) error {
	return &DaemonOwnershipError{Name: o.record.Name, Path: o.path, OwnerID: o.record.OwnerID, PID: o.record.PID,
		Cause: errors.Join(ErrDaemonRecoveryRequired, err)}
}

func (o *daemonOwnership) saveLocked() error {
	if o.lock == nil {
		return o.recoveryError(errors.New("ownership fence is closed"))
	}
	opened, err := o.lock.Stat()
	current, statErr := os.Lstat(o.lockPath)
	if err != nil || statErr != nil || !os.SameFile(opened, current) {
		return o.recoveryError(errors.New("ownership fence was replaced"))
	}
	o.record.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(o.record)
	if err != nil {
		return err
	}
	dir := filepath.Dir(o.path)
	f, err := os.CreateTemp(dir, ".daemon-record-*")
	if err != nil {
		return o.recoveryError(err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), o.path)
	}
	if err == nil {
		err = syncDaemonOwnershipDirectory(dir)
	}
	if err != nil {
		return o.recoveryError(err)
	}
	return nil
}

// prepare persists uncertainty BEFORE invoking exec. No command or environment
// is stored; these can carry secrets. The PID is saved separately after start.
func (o *daemonOwnership) prepare() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.record.Phase == "launching" {
		return o.recoveryError(errors.New("previous launch has not been finalized"))
	}
	if o.record.PID > 0 {
		live, err := daemonProcessMayLive(o.record.PID)
		if live || err != nil {
			return o.recoveryError(errors.Join(err, errors.New("previous process group has not exited")))
		}
	}
	o.record.Phase, o.record.PID = "launching", 0
	return o.saveLocked()
}

func (o *daemonOwnership) started(pid int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.record.Phase, o.record.PID = "running", pid
	return o.saveLocked()
}

// startFailed is used only when exec itself failed and no child was created.
func (o *daemonOwnership) startFailed() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.record.Phase, o.record.PID = "stopped", 0
	return o.saveLocked()
}

// finish releases ownership only after recording known quiescence. Failed
// finalization still closes the descriptor, but preserves the running/unknown
// record so a new supervisor cannot blindly replay the launch.
func (o *daemonOwnership) finish() error { return o.finishWithCleanup(nil) }

func (o *daemonOwnership) finishWithCleanup(cleanup func()) error {
	if o == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil // Manually constructed in-memory test generations.
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lock == nil {
		return nil
	}
	var err error
	if o.record.Phase == "launching" {
		err = o.recoveryError(errors.New("launch outcome has not been recorded"))
	} else if o.record.PID > 0 {
		live, probeErr := daemonProcessMayLive(o.record.PID)
		if live || probeErr != nil {
			err = o.recoveryError(errors.Join(probeErr, errors.New("daemon process or process group may still be alive")))
		}
	}
	if err == nil {
		o.record.Phase, o.record.PID = "stopped", 0
		err = o.saveLocked()
	}
	if err == nil && cleanup != nil {
		// Retiring workers cannot remove a newer owner's compatibility PID
		// record. Cleanup runs before releasing the cross-process fence.
		cleanup()
	}
	return errors.Join(err, o.closeLocked())
}

func (o *daemonOwnership) closeLocked() error {
	if o.lock == nil {
		return nil
	}
	err := o.lock.Close() // Closing releases the kernel lock, never unlink it.
	o.lock = nil
	return err
}
