//go:build unix

package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// createFIFO creates a named pipe (FIFO) at the given path.
func createFIFO(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("refusing to replace existing non-FIFO %q", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale FIFO %q: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("inspect FIFO path %q: %w", path, err)
	}

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return fmt.Errorf("create FIFO %q: %w", path, err)
	}
	return nil
}

func openLaunchEnvironmentWriter(ctx context.Context, path string) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err == nil {
			info, statErr := file.Stat()
			if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
				_ = file.Close()
				return nil, errors.New("recovery environment transport is not a pipe")
			}
			return file, nil
		}
		if !errors.Is(err, syscall.ENXIO) {
			return nil, err
		}
		if err := waitForSendDelay(ctx, 10*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// writeLaunchEnvironment writes the whole script, waiting while the FIFO is
// full. Go polls FIFOs on Linux but not on darwin (golang/go#24164), where a
// full FIFO returns EAGAIN instead of blocking until the reader drains it.
func writeLaunchEnvironment(ctx context.Context, writer *os.File, script string) error {
	for script != "" {
		n, err := writer.WriteString(script)
		script = script[n:]
		if err == nil {
			continue
		}
		if !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if err := waitForSendDelay(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func openLaunchEnvironmentAbort(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		_ = file.Close()
		return nil, errors.New("recovery environment transport is not a pipe")
	}
	return file, nil
}

// OpenLaunchEnvironmentFile avoids following a last-component symlink or
// blocking on a substituted FIFO while opening an operator-owned recovery file.
// Callers still verify permissions, size, and identity against their Lstat.
func OpenLaunchEnvironmentFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open private recovery environment file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("recovery environment file is not a regular file")
	}
	return file, nil
}
