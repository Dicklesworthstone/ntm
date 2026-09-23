package checkpoint

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func renameCheckpointImport(parent *os.File, stage, target string, replace bool) error {
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = unix.RENAME_EXCHANGE
	}
	err := unix.Renameat2(int(parent.Fd()), stage, int(parent.Fd()), target, flags)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("%w: %w", ErrCheckpointImportAtomicUnavailable, err)
	}
	return err
}
