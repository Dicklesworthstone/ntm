//go:build windows

package supervisor

import (
	"errors"
	"os"
	"syscall"
)

func lockDaemonOwnership(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// No sharing: independently opened handles contend in this process too.
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33)) {
			return nil, errors.Join(ErrDaemonOwned, err)
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func daemonProcessMayLive(pid int) (bool, error) {
	if pid <= 0 {
		return true, errors.New("invalid daemon PID")
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if errors.Is(err, syscall.Errno(87)) { // ERROR_INVALID_PARAMETER: no such PID
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true, err
	}
	return code == 259, nil // STILL_ACTIVE
}

func syncDaemonOwnershipDirectory(string) error {
	// Windows does not support os.File.Sync on directory handles. Record
	// files are synced; no power-loss durability guarantee is made here.
	return nil
}
