package testutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLogger provides structured logging for integration tests.
// It writes timestamped log entries to both a file and the test output.
type TestLogger struct {
	t        *testing.T
	w        io.Writer
	testName string
	startTs  time.Time
	mu       sync.Mutex
}

// NewTestLogger creates a new test logger that writes to both a log file and test output.
// The log file is created in logDir with a name based on the test name and timestamp.
// The file is automatically closed when the test completes via t.Cleanup.
func NewTestLogger(t *testing.T, logDir string) *TestLogger {
	t.Helper()

	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("failed to create log directory: %v", err)
	}

	// Sanitize test name for filename (replace / with _)
	safeName := strings.ReplaceAll(t.Name(), "/", "_")
	filename := fmt.Sprintf("%s_%s.log", safeName, time.Now().Format("20060102_150405"))
	logPath := filepath.Join(logDir, filename)

	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	t.Cleanup(func() {
		f.Close()
	})

	logger := &TestLogger{
		t:        t,
		w:        io.MultiWriter(f, &testWriter{t: t}),
		testName: t.Name(),
		startTs:  time.Now(),
	}

	logger.Log("=== TEST START: %s ===", t.Name())
	logger.Log("Log file: %s", logPath)

	return logger
}

// NewTestLoggerStdout creates a logger that only writes to test output (no file).
// Useful for simple tests that don't need persistent logs.
func NewTestLoggerStdout(t *testing.T) *TestLogger {
	t.Helper()
	return &TestLogger{
		t:        t,
		w:        &testWriter{t: t},
		testName: t.Name(),
		startTs:  time.Now(),
	}
}

// Log writes a timestamped log entry.
func (l *TestLogger) Log(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format(time.RFC3339)
	elapsed := time.Since(l.startTs).Round(time.Millisecond)
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.w, "[%s] [+%s] %s\n", ts, elapsed, msg)
}

// LogSection writes a section header for grouping related log entries.
func (l *TestLogger) LogSection(name string) {
	l.Log("--- %s ---", name)
}

// Exec runs a command and logs its execution, output, and exit status.
// Returns the combined stdout/stderr output and any error.
func (l *TestLogger) Exec(cmd string, args ...string) ([]byte, error) {
	l.Log("EXEC: %s %s", cmd, strings.Join(args, " "))

	c := exec.Command(cmd, args...)
	if err := bindIsolatedTmuxCommand(cmd, c); err != nil {
		l.Log("EXIT: refused: %v", err)
		return nil, err
	}
	out, err := c.CombinedOutput()

	// Log output (truncate if very long)
	outStr := string(out)
	if len(outStr) > 2000 {
		outStr = outStr[:2000] + "\n... (truncated)"
	}
	if outStr != "" {
		l.Log("OUTPUT:\n%s", outStr)
	}

	if err != nil {
		l.Log("EXIT: error: %v", err)
	} else {
		l.Log("EXIT: success (exit 0)")
	}

	return out, err
}

// ExecContext runs a command with a timeout.
func (l *TestLogger) ExecContext(timeout time.Duration, cmd string, args ...string) ([]byte, error) {
	l.Log("EXEC (timeout=%s): %s %s", timeout, cmd, strings.Join(args, " "))

	c := exec.Command(cmd, args...)
	if err := bindIsolatedTmuxCommand(cmd, c); err != nil {
		l.Log("EXIT: refused: %v", err)
		return nil, err
	}

	// Create a channel for the result
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)

	go func() {
		out, err := c.CombinedOutput()
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		outStr := string(r.out)
		if len(outStr) > 2000 {
			outStr = outStr[:2000] + "\n... (truncated)"
		}
		if outStr != "" {
			l.Log("OUTPUT:\n%s", outStr)
		}
		if r.err != nil {
			l.Log("EXIT: error: %v", r.err)
		} else {
			l.Log("EXIT: success (exit 0)")
		}
		return r.out, r.err
	case <-time.After(timeout):
		if c.Process != nil {
			c.Process.Kill()
		}
		l.Log("EXIT: timeout after %s", timeout)
		return nil, fmt.Errorf("command timed out after %s", timeout)
	}
}

// Elapsed returns the time elapsed since the test started.
func (l *TestLogger) Elapsed() time.Duration {
	return time.Since(l.startTs)
}

// testWriter wraps testing.T to implement io.Writer
type testWriter struct {
	t *testing.T
}

func (tw *testWriter) Write(p []byte) (n int, err error) {
	tw.t.Helper()
	tw.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
