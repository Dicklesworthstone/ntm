//go:build linux

package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Lock files remain in place after release. Removing them would let an old
// inode's waiter and a newly created inode's owner both activate credentials.
func acquireFailoverProviderLock(ctx context.Context, provider string) (func(), error) {
	if ctx == nil {
		return nil, errors.New("provider lock requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if provider != "claude" && provider != "openai" {
		return nil, errors.New("unsupported global account provider")
	}
	statePath, err := accountFailoverStatePath()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(statePath), "account-recovery")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, provider+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = file.Close()
				return nil, err
			}
			held, heldErr := file.Stat()
			current, currentErr := os.Stat(path)
			if heldErr != nil || currentErr != nil || !held.Mode().IsRegular() || !os.SameFile(held, current) {
				_ = file.Close()
				return nil, errors.New("provider lock file changed during acquisition")
			}
			var once sync.Once
			return func() { once.Do(func() { _ = file.Close() }) }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
