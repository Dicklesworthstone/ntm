//go:build unix

package tmux

import (
	"fmt"
	"os"
	"syscall"
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
