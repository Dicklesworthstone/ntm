//go:build !windows

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandOutputPreservesCompleteStdoutAndExitDiagnostics(t *testing.T) {
	for _, script := range []string{"printf 12345; printf abc >&2", "printf 12345; printf abc >&2; exit 7"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.Command("/bin/sh", "-c", script)
		out, err := runCommandOutput(ctx, cmd, 5, 3)
		cancel()
		if strings.HasSuffix(script, "exit 7") {
			var exit *exec.ExitError
			if out != nil || !errors.As(err, &exit) || exit.ExitCode() != 7 || string(exit.Stderr) != "abc" {
				t.Fatalf("lost failure diagnostics or exposed partial stdout: %q %v", out, err)
			}
		} else if err != nil || string(out) != "12345" {
			t.Fatalf("exact output limits rejected: %q %v", out, err)
		}
	}
}

func TestCommandOutputRejectsOverflowInsteadOfReturningPrefix(t *testing.T) {
	for name, script := range map[string]string{
		"stdout": "printf 123456",
		"stderr": "printf useful; printf abcdef >&2",
		"both":   "printf 123456; printf abcdef >&2",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := runCommandOutput(ctx, exec.Command("/bin/sh", "-c", script), 5, 5)
			if out != nil || !errors.Is(err, errCommandOutputLimit) {
				t.Fatalf("accepted truncated %s: %q %v", name, out, err)
			}
			if ctx.Err() != nil {
				t.Fatal("output limit cancelled the caller's context")
			}
		})
	}
}

func TestCommandOutputOverflowStopsUnboundedProducer(t *testing.T) {
	for _, redirect := range []string{"", ">&2"} {
		t.Run(redirect, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.Command("/bin/sh", "-c", "while :; do printf 012345678901234567890123456789 "+redirect+"; done")
			start := time.Now()
			out, err := runCommandOutput(ctx, cmd, 1024, 1024)
			if out != nil || !errors.Is(err, errCommandOutputLimit) || time.Since(start) > 2*time.Second {
				t.Fatalf("unbounded producer was not stopped by output limit: %q %v elapsed=%v", out, err, time.Since(start))
			}
			if cmd.ProcessState == nil {
				t.Fatal("output overflow returned before reaping producer")
			}
		})
	}
}

func TestCommandOutputPreCancelledStartsNoWork(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "printf dispatched > marker")
	cmd.Dir = root
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runCommandOutput(ctx, cmd, 100, 100)
	if out != nil || !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("pre-cancelled capture started work: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled command dispatched: %v", err)
	}
}

func TestCommandOutputDeadlineReturnsNoPartialWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	out, err := runCommandOutput(ctx, exec.Command("/bin/sh", "-c", "printf partial; sleep 30"), 100, 100)
	if out != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out selector exposed partial output: %q %v", out, err)
	}
}

func TestCommandOutputRejectsInvalidSetup(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		if _, err := runCommandOutput(context.Background(), cmd, limit, 100); err == nil || cmd.Process != nil {
			t.Fatal("unbounded capture started a command")
		}
	}
	if _, err := runCommandOutput(context.Background(), nil, 100, 100); err == nil {
		t.Fatal("nil command accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil || cmd.Process != nil {
		t.Fatal("competing context watcher accepted")
	}
	cmd = exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil {
		t.Fatal("reused command accepted")
	}
	cmd = exec.Command(filepath.Join(t.TempDir(), "missing"))
	if _, err := runCommandOutput(ctx, cmd, 100, 100); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestCancelOnTruncationWriterRetainsBoundedBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture := newCappedWriter(4)
	writer := &cancelOnTruncationWriter{capture: capture, cancel: cancel}
	if n, err := writer.Write([]byte("1234")); n != 4 || err != nil || ctx.Err() != nil {
		t.Fatal("exact cap treated as overflow")
	}
	if n, err := writer.Write([]byte(strings.Repeat("x", 10000))); n != 10000 || err != nil {
		t.Fatal("overflow writer stopped pipe drainage")
	}
	if capture.Len() != 4 || capture.Total() != 10004 || capture.String() != "1234" || ctx.Err() == nil {
		t.Fatalf("unbounded bytes or missing cancellation: kept=%d total=%d context=%v", capture.Len(), capture.Total(), ctx.Err())
	}
}
