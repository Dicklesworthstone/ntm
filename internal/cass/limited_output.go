package cass

import (
	"bytes"
	"errors"
)

// ErrOutputLimit means a CASS subprocess exceeded its capture budget. Partial
// stdout is never decoded as a successful response or an empty search result.
var ErrOutputLimit = errors.New("cass output exceeds capture budget (32 MiB stdout or 64 KiB stderr)")

const (
	maxCASSStdoutBytes = 32 << 20
	maxCASSStderrBytes = 64 << 10
)

// Each writer is owned by one os/exec pipe-copy goroutine. Its fields are read
// only after Cmd.Run has joined those copies, including the WaitDelay path.
type limitedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   func()
}

func newLimitedOutput(limit int, cancel func()) *limitedOutput {
	return &limitedOutput{limit: limit, cancel: cancel}
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if w.exceeded {
		return 0, ErrOutputLimit
	}
	remaining := w.limit - w.buffer.Len()
	if len(p) <= remaining {
		return w.buffer.Write(p)
	}
	n, _ := w.buffer.Write(p[:remaining])
	w.exceeded = true
	w.cancel()
	return n, ErrOutputLimit
}

func (w *limitedOutput) Bytes() []byte { return w.buffer.Bytes() }

func (w *limitedOutput) String() string { return w.buffer.String() }
