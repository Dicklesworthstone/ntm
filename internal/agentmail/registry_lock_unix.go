//go:build unix

package agentmail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Each acquisition owns a separate descriptor, serializing goroutines as well
// as processes. The lock inode is never unlinked across registry replacements.
func acquireRegistryFileLock(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			unlock := func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}
			held, heldErr := file.Stat()
			named, namedErr := os.Lstat(path)
			if heldErr != nil || namedErr != nil || !held.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(held, named) {
				unlock()
				return nil, errors.New("agent registry lock is not a stable regular file")
			}
			if err := ctx.Err(); err != nil {
				unlock()
				return nil, err
			}
			return unlock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
