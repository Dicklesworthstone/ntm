//go:build unix

package swarm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// openSingleLinkRegularFile opens path for reading and returns it with its
// metadata only when it is the file lstat describes, a regular file, and has
// exactly one link. Otherwise it returns a nil file and no error.
//
// The open does not follow a final symlink and does not block on a FIFO, and
// the checks run on the opened descriptor, so replacing the path between the
// lstat and the open cannot change what was checked.
func openSingleLinkRegularFile(path string, lstat fs.FileInfo) (*os.File, fs.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil // replaced by a link, or removed, since the lstat
		}
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !os.SameFile(lstat, info) || uint64(stat.Nlink) != 1 {
		_ = file.Close()
		return nil, nil, nil
	}
	return file, info, nil
}
