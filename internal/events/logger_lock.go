package events

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const eventLogLockTimeout = 5 * time.Second

// canonicalEventLogPath is called after the parent directory exists. The
// canonical name is both the append destination and the lock namespace.
func canonicalEventLogPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve event log path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve event log path: %w", err)
	}
	// A dangling file symlink must not turn into two different lock names.
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("event log path is a dangling symlink")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve event log directory: %w", err)
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func openEventLogWriter(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("event log must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	if err := verifyEventLogFile(f, path); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func verifyEventLogFile(f *os.File, path string) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	visible, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !visible.Mode().IsRegular() || !os.SameFile(opened, visible) {
		return fmt.Errorf("event log file identity changed: %s", path)
	}
	return nil
}

func lockEventLog(path string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), eventLogLockTimeout)
	defer cancel()
	return acquireEventLogLock(ctx, path)
}

// The sidecar inode is permanent: removing it after unlock would allow a
// waiting process and a new opener to acquire independent locks. Each call
// opens its own descriptor, so independent Logger instances in one process
// also contend. The OS releases ownership if a writer crashes.
func acquireEventLogLock(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockPath := path + ".lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("event log lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open event log lock: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("acquire event log lock: %w", err)
		}
		locked, err := tryLockEventLogFile(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock event log: %w", err)
		}
		if locked {
			if err := verifyEventLogFile(f, lockPath); err != nil {
				releaseEventLogLock(f)
				return nil, err
			}
			return f, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func releaseEventLogLock(f *os.File) {
	unlockEventLogFile(f)
	_ = f.Close()
}
