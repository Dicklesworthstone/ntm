package webhook

import (
	"errors"
	"os"
)

var errOutboxBusy = errors.New("webhook outbox is busy; retry admission")

func openOutboxLock(path string) (*os.File, error) {
	return nil, errors.New("durable webhook outbox requires Unix file locking; leave outbox_dir unset on Windows")
}

func closeOutboxLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
