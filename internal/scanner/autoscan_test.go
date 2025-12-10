package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDefaultAutoScannerConfig(t *testing.T) {
	cfg := DefaultAutoScannerConfig("/test/dir")

	if cfg.ProjectDir != "/test/dir" {
		t.Errorf("expected project dir /test/dir, got %s", cfg.ProjectDir)
	}
	if cfg.DebounceDuration != time.Second {
		t.Errorf("expected debounce 1s, got %v", cfg.DebounceDuration)
	}
	if cfg.ScanTimeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", cfg.ScanTimeout)
	}
	if len(cfg.ExcludePatterns) == 0 {
		t.Error("expected default exclude patterns")
	}
}

func TestAutoScanner_isExcluded(t *testing.T) {
	cfg := DefaultAutoScannerConfig("/project")
	auto := &AutoScanner{config: cfg}

	tests := []struct {
		path     string
		excluded bool
	}{
		{"/project/.git/config", true},
		{"/project/node_modules/pkg/file.js", true},
		{"/project/vendor/lib/mod.go", true},
		{"/project/.beads/issues.jsonl", true},
		{"/project/src/main.go", false},
		{"/project/README.md", false},
		{"/project/internal/pkg/file.go", false},
		{"/project/dist/app.min.js", true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := auto.isExcluded(tc.path)
			if got != tc.excluded {
				t.Errorf("isExcluded(%q) = %v, want %v", tc.path, got, tc.excluded)
			}
		})
	}
}

func TestAutoScanner_StartStop(t *testing.T) {
	// Skip if UBS is not available
	if !IsAvailable() {
		t.Skip("UBS not installed, skipping integration test")
	}

	tmpDir := t.TempDir()
	cfg := DefaultAutoScannerConfig(tmpDir)
	cfg.DebounceDuration = 50 * time.Millisecond

	auto, err := NewAutoScanner(cfg)
	if err != nil {
		t.Fatalf("NewAutoScanner: %v", err)
	}

	// Start
	if err := auto.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !auto.IsRunning() {
		t.Error("expected auto scanner to be running")
	}

	// Starting again should be a no-op
	if err := auto.Start(); err != nil {
		t.Fatalf("Start (second): %v", err)
	}

	// Stop
	if err := auto.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if auto.IsRunning() {
		t.Error("expected auto scanner to be stopped")
	}

	// Stopping again should be a no-op
	if err := auto.Stop(); err != nil {
		t.Fatalf("Stop (second): %v", err)
	}
}

func TestAutoScanner_TriggerScan(t *testing.T) {
	// Skip if UBS is not available
	if !IsAvailable() {
		t.Skip("UBS not installed, skipping integration test")
	}

	tmpDir := t.TempDir()

	// Create a simple test file
	testFile := filepath.Join(tmpDir, "test.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var (
		mu          sync.Mutex
		scanStarted bool
		scanDone    bool
		scanResult  *ScanResult
		scanErr     error
	)

	cfg := DefaultAutoScannerConfig(tmpDir)
	cfg.ScanTimeout = 30 * time.Second
	cfg.OnScanStart = func() {
		mu.Lock()
		scanStarted = true
		mu.Unlock()
	}
	cfg.OnScanComplete = func(result *ScanResult, err error) {
		mu.Lock()
		scanDone = true
		scanResult = result
		scanErr = err
		mu.Unlock()
	}

	auto, err := NewAutoScanner(cfg)
	if err != nil {
		t.Fatalf("NewAutoScanner: %v", err)
	}

	// Trigger scan without starting watcher
	auto.TriggerScan()

	// Wait for scan to complete
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := scanDone
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	if !scanStarted {
		t.Error("expected scan to start")
	}
	if !scanDone {
		t.Error("expected scan to complete")
	}
	if scanErr != nil {
		t.Errorf("scan error: %v", scanErr)
	}
	if scanResult == nil {
		t.Error("expected scan result")
	}

	// Verify LastResult
	if auto.LastResult() != scanResult {
		t.Error("LastResult mismatch")
	}
	if auto.LastScanTime().IsZero() {
		t.Error("expected LastScanTime to be set")
	}
}

func TestAutoScanner_FilterEvents(t *testing.T) {
	cfg := DefaultAutoScannerConfig("/project")
	auto := &AutoScanner{config: cfg}

	events := []struct {
		path    string
		wantLen int
	}{
		// Single events
		{"/project/src/main.go", 1},
		{"/project/.git/HEAD", 0},
		{"/project/node_modules/pkg/index.js", 0},
	}

	for _, tc := range events {
		t.Run(tc.path, func(t *testing.T) {
			// Create a mock event slice (we can't use watcher.Event directly
			// without importing, but we can test isExcluded)
			excluded := auto.isExcluded(tc.path)
			wantExcluded := tc.wantLen == 0
			if excluded != wantExcluded {
				t.Errorf("path %q: excluded=%v, want=%v", tc.path, excluded, wantExcluded)
			}
		})
	}
}

func TestWatchAndScan_Cancelled(t *testing.T) {
	// Skip if UBS is not available
	if !IsAvailable() {
		t.Skip("UBS not installed, skipping integration test")
	}

	tmpDir := t.TempDir()
	cfg := DefaultAutoScannerConfig(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := WatchAndScan(ctx, cfg)
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestNewAutoScannerWithScanner(t *testing.T) {
	cfg := DefaultAutoScannerConfig("/test")

	// Create a mock scanner (nil binaryPath - will fail if used)
	scanner := &Scanner{binaryPath: ""}

	auto := NewAutoScannerWithScanner(cfg, scanner)
	if auto == nil {
		t.Fatal("expected non-nil AutoScanner")
	}
	if auto.scanner != scanner {
		t.Error("expected scanner to be set")
	}
}
