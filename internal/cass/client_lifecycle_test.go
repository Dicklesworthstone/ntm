package cass

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The test executable is the subprocess, so these execution tests do not
// depend on a host CASS index, shell, or installed scripting runtime.
func TestCASSExecutorChild(t *testing.T) {
	if os.Getenv("NTM_CASS_EXECUTOR_CHILD") != "1" {
		return
	}
	mode := os.Getenv("NTM_CASS_EXECUTOR_MODE")
	switch mode {
	case "json":
		_, _ = os.Stdout.WriteString(`{"hits":[],"total_matches":0}`)
	case "missing-index":
		os.Exit(3)
	case "failure":
		_, _ = os.Stderr.WriteString("bounded diagnostic")
		os.Exit(9)
	case "stdout-flood", "stderr-flood":
		stream := os.Stdout
		if mode == "stderr-flood" {
			stream = os.Stderr
		}
		limit := maxCASSStdoutBytes
		if mode == "stderr-flood" {
			limit = maxCASSStderrBytes
		}
		data := []byte(strings.Repeat("x", 32<<10))
		for written := 0; written <= limit; written += len(data) {
			if _, err := stream.Write(data); err != nil {
				os.Exit(8)
			}
		}
		for {
			time.Sleep(time.Hour)
		}
	case "wait":
		_ = os.WriteFile(os.Getenv("NTM_CASS_STARTED"), []byte("started"), 0o600)
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(11)
	}
	os.Exit(0)
}

func cassExecutorChild(t *testing.T, mode string) *DefaultExecutor {
	t.Helper()
	t.Setenv("NTM_CASS_EXECUTOR_CHILD", "1")
	t.Setenv("NTM_CASS_EXECUTOR_MODE", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &DefaultExecutor{BinaryPath: exe}
}

func TestCASSExecutorEnforcesCaptureBudgets(t *testing.T) {
	for _, stream := range []string{"stdout-flood", "stderr-flood"} {
		t.Run(stream, func(t *testing.T) {
			e := cassExecutorChild(t, stream)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := e.Run(ctx, "-test.run=^TestCASSExecutorChild$")
			if out != nil || !errors.Is(err, ErrOutputLimit) || ctx.Err() != nil {
				t.Fatalf("flood was not stopped by capture budget: bytes=%d err=%v ctx=%v", len(out), err, ctx.Err())
			}
		})
	}
}

func TestCASSExecutorPreservesExitClassification(t *testing.T) {
	for _, mode := range []string{"json", "missing-index", "failure"} {
		t.Run(mode, func(t *testing.T) {
			e := cassExecutorChild(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := e.Run(ctx, "-test.run=^TestCASSExecutorChild$")
			switch mode {
			case "json":
				if err != nil || string(out) != `{"hits":[],"total_matches":0}` {
					t.Fatalf("success changed: %q %v", out, err)
				}
			case "missing-index":
				if !errors.Is(err, ErrNotInitialized) || out != nil {
					t.Fatalf("missing-index classification changed: %q %v", out, err)
				}
			case "failure":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 9 || !strings.Contains(err.Error(), "bounded diagnostic") {
					t.Fatalf("exit information lost: %v", err)
				}
			}
		})
	}
}

func TestCASSExecutorCancellationJoinsProcess(t *testing.T) {
	e := cassExecutorChild(t, "wait")
	marker := t.TempDir() + "/started"
	t.Setenv("NTM_CASS_STARTED", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.Run(ctx, "-test.run=^TestCASSExecutorChild$")
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subprocess did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotInitialized) {
			t.Fatalf("cancellation misclassified: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled subprocess did not join")
	}
}

func TestCASSExecutorRefusesStoppedContextBeforeLaunch(t *testing.T) {
	e := &DefaultExecutor{BinaryPath: "/this/executable/must/not/be/resolved"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := e.Run(ctx); out != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost to binary lookup: %q %v", out, err)
	}
	if out, err := e.Run(nil); out != nil || err == nil {
		t.Fatal("nil context accepted")
	}
}
