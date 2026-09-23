package checkpoint

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func renameCheckpointImport(parent *os.File, stage, target string, replace bool) error {
	flags := uint32(unix.RENAME_EXCL)
	if replace {
		flags = unix.RENAME_SWAP
	}
	err := unix.RenameatxNp(int(parent.Fd()), stage, int(parent.Fd()), target, flags)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("%w: %w", ErrCheckpointImportAtomicUnavailable, err)
	}
	return err
}
