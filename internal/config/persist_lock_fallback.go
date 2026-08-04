//go:build !windows && !linux && !darwin && !dragonfly && !freebsd && !illumos && !netbsd && !openbsd

package config

import (
	"context"
	"sync"
)

var (
	fallbackConfigLocksMu sync.Mutex
	fallbackConfigLocks   = make(map[string]chan struct{})
)

// Platforms without a portable advisory file-lock syscall still serialize
// independent handles within this ntm process. PersistTOMLKeys also holds its
// package-wide transaction mutex; this keyed fallback primarily preserves the
// acquireConfigPersistenceLock contract for direct callers and tests.
func acquireConfigPersistenceLock(ctx context.Context, lockPath string) (func(), error) {
	fallbackConfigLocksMu.Lock()
	lock, ok := fallbackConfigLocks[lockPath]
	if !ok {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		fallbackConfigLocks[lockPath] = lock
	}
	fallbackConfigLocksMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			lock <- struct{}{}
		})
	}, nil
}
