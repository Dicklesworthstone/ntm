package cass

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type mockExecutor struct {
	output []byte
	err    error
}

func (m *mockExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	return m.output, m.err
}

func TestNewClient(t *testing.T) {
	client := NewClient(WithTimeout(5 * time.Second))
	if client.timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", client.timeout)
	}
}

func TestClient_Search(t *testing.T) {
	mockResp := `{"count": 1, "hits": [{"title": "test session", "score": 1.0}]}`
	client := NewClient(WithExecutor(&mockExecutor{output: []byte(mockResp), err: nil}))

	resp, err := client.Search(context.Background(), SearchOptions{Query: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Count != 1 {
		t.Errorf("expected count 1, got %d", resp.Count)
	}
	if len(resp.Hits) != 1 || resp.Hits[0].Title != "test session" {
		t.Errorf("unexpected hits: %v", resp.Hits)
	}
}

func TestClient_Status(t *testing.T) {
	mockResp := `{"healthy": true, "conversations": 42}`
	client := NewClient(WithExecutor(&mockExecutor{output: []byte(mockResp), err: nil}))

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !status.Healthy {
		t.Error("expected healthy status")
	}
	if status.Conversations != 42 {
		t.Errorf("expected 42 conversations, got %d", status.Conversations)
	}
}

// Regression test for acfs#266: when the cass binary is installed but
// no index has been built, cass exits with code 3. The DefaultExecutor
// must surface this as ErrNotInitialized so downstream callers (ntm
// send's dedup-check) can degrade gracefully.
func TestDefaultExecutor_NotInitializedExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script-based fake binary is POSIX-only")
	}
	tmp, err := os.MkdirTemp("", "cass-fake-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	bin := filepath.Join(tmp, "cass")
	body := []byte("#!/bin/sh\nexit 3\n")
	if err := os.WriteFile(bin, body, 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	exe := &DefaultExecutor{BinaryPath: bin}
	_, err = exe.Run(context.Background(), "search", "--json", "anything")

	if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected ErrNotInitialized for cass exit 3, got %v", err)
	}
}

// Verify that other non-zero exit codes still surface as the generic
// "cass execution failed" error — only exit 3 is treated specially.
func TestDefaultExecutor_OtherExitCodesUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script-based fake binary is POSIX-only")
	}
	tmp, err := os.MkdirTemp("", "cass-fake-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })

	bin := filepath.Join(tmp, "cass")
	body := []byte("#!/bin/sh\nexit 1\n")
	if err := os.WriteFile(bin, body, 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	exe := &DefaultExecutor{BinaryPath: bin}
	_, err = exe.Run(context.Background(), "search", "--json", "anything")

	if err == nil {
		t.Fatal("expected error for cass exit 1, got nil")
	}
	if errors.Is(err, ErrNotInitialized) {
		t.Fatalf("exit 1 should NOT be classified as ErrNotInitialized, got %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected wrapped *exec.ExitError, got %T: %v", err, err)
	}
}
