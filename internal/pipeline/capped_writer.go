package pipeline

import (
	"bytes"
	"io"
	"sync"
)

// cappedWriter is a bounded io.Writer backed by an in-memory buffer. Writes
// past the cap are silently dropped from the buffer but the total observed
// byte count is preserved on Truncated/Total so callers can report accurate
// truncation diagnostics. Used by executeCommand to bound stdout and stderr
// during command execution rather than after-the-fact, so stderr-heavy
// commands cannot consume unbounded memory before cmd.Wait() returns
// (bd-g7cu9).
//
// cappedWriter is safe for concurrent reads (Len/Total/Truncated/String/Bytes)
// alongside writes. exec.Cmd already serialises writes per stream and across
// shared writers when stdout and stderr point at the same target, so the
// internal mutex only contends with the heartbeat goroutine sampling Len()
// while the writer goroutine is mid-Write (bd-1vhq5).
type cappedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	cap       int64
	truncated bool
	total     int64 // total bytes observed across all Write calls
	// A command can leave inherited pipes open after its group exits. Keep
	// capture completion separate from byte truncation and from Cmd.Wait's
	// exit error, which can hide a simultaneous pipe-drain timeout.
	readStarted bool
	activeReads int
	readFailed  bool
}

// newCappedWriter returns a writer that drops bytes once cap is reached.
// A non-positive cap disables truncation (unbounded buffer).
func newCappedWriter(cap int64) *cappedWriter {
	return &cappedWriter{cap: cap}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	if w.cap <= 0 {
		w.buf.Write(p)
		return len(p), nil
	}
	remaining := w.cap - int64(w.buf.Len())
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// ReadFrom records actual EOF from the command's output pipe. Continue reading
// after reaching the byte cap: dropping retained bytes is not EOF evidence.
// Hide both optional copy interfaces to avoid recursion and a source WriterTo
// returning without the copy loop having observed the end of the stream.
func (w *cappedWriter) ReadFrom(r io.Reader) (n int64, err error) {
	return w.readFrom(r, w)
}

// readFrom keeps EOF accounting attached to the capture while routing every
// chunk through its owner's writer. Wrappers such as cancelOnTruncationWriter
// must retain both this completion evidence and their per-write behavior.
func (w *cappedWriter) readFrom(r io.Reader, dst io.Writer) (n int64, err error) {
	w.mu.Lock()
	w.readStarted = true
	w.activeReads++
	w.mu.Unlock()
	complete := false
	defer func() {
		w.mu.Lock()
		w.activeReads--
		w.readFailed = w.readFailed || !complete
		w.mu.Unlock()
	}()
	n, err = io.Copy(struct{ io.Writer }{dst}, struct{ io.Reader }{r})
	complete = err == nil
	return n, err
}

// outputCaptureComplete is inspected only after Cmd.Wait has joined its copy
// goroutines. A later successful copy must not erase an earlier incomplete one.
func (w *cappedWriter) outputCaptureComplete() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.readStarted && w.activeReads == 0 && !w.readFailed
}

// Bytes returns a copy of the captured (possibly truncated) payload. The
// copy decouples callers from the underlying buffer so concurrent writers
// cannot mutate the slice after it is returned.
func (w *cappedWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	src := w.buf.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// String is the convenience accessor used by callers that immediately
// stringify the captured output.
func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Len returns the size of the captured payload (post-truncation).
func (w *cappedWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}

// Total returns the total number of bytes observed by Write across all
// calls, including bytes that were dropped after the cap.
func (w *cappedWriter) Total() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total
}

// Truncated reports whether at least one byte was dropped due to the cap.
func (w *cappedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

// ensureCappedWriterIsWriter is a compile-time assertion.
var _ io.Writer = (*cappedWriter)(nil)
var _ io.ReaderFrom = (*cappedWriter)(nil)
