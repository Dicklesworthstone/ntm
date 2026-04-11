package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
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

func TestAutoScanner_isExcluded_RecursiveGlobPatterns(t *testing.T) {
	t.Parallel()

	auto := &AutoScanner{config: AutoScannerConfig{
		ProjectDir: "/project",
		ExcludePatterns: []string{
			".git/**",
			"vendor/**",
			"build/**",
		},
	}}

	tests := []struct {
		path     string
		excluded bool
	}{
		{"/project/.git/config", true},
		{"/project/.git/hooks/pre-commit", true},
		{"/project/vendor/github.com/pkg/errors/errors.go", true},
		{"/project/build/output/app.js", true},
		{"/project/internal/build/output.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := auto.isExcluded(tc.path); got != tc.excluded {
				t.Fatalf("isExcluded(%q) = %v, want %v", tc.path, got, tc.excluded)
			}
		})
	}
}

func TestAutoScannerConfigFromProjectConfig_MergesDefaultExcludes(t *testing.T) {
	t.Parallel()

	scannerCfg := &config.ScannerConfig{
		Defaults: config.ScannerDefaults{
			Timeout: "45s",
			Exclude: []string{"build/**", ".cache/**"},
		},
	}

	auto := AutoScannerConfigFromProjectConfig("/project", scannerCfg)

	if auto.ScanTimeout != 45*time.Second {
		t.Fatalf("ScanTimeout = %v, want 45s", auto.ScanTimeout)
	}

	required := []string{".git", "node_modules", "vendor", ".beads", "*.min.js", "*.min.css", "build/**", ".cache/**"}
	for _, pattern := range required {
		found := false
		for _, existing := range auto.ExcludePatterns {
			if existing == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected merged exclude pattern %q in %#v", pattern, auto.ExcludePatterns)
		}
	}
}

func TestNewAutoScannerWithScanner_AppliesDocumentedDefaults(t *testing.T) {
	t.Parallel()

	auto := NewAutoScannerWithScanner(AutoScannerConfig{
		ProjectDir: "/project",
	}, &Scanner{binaryPath: "ubs"})

	if auto.config.DebounceDuration != time.Second {
		t.Fatalf("DebounceDuration = %v, want 1s", auto.config.DebounceDuration)
	}
	if auto.config.ScanTimeout != 60*time.Second {
		t.Fatalf("ScanTimeout = %v, want 60s", auto.config.ScanTimeout)
	}
	if len(auto.config.ExcludePatterns) == 0 {
		t.Fatal("expected default exclude patterns to be applied")
	}
	if auto.config.ScanOptions.Timeout != 60*time.Second {
		t.Fatalf("ScanOptions.Timeout = %v, want 60s", auto.config.ScanOptions.Timeout)
	}
}

func TestNewAutoScannerWithScanner_MergesCustomExcludesWithDefaults(t *testing.T) {
	t.Parallel()

	auto := NewAutoScannerWithScanner(AutoScannerConfig{
		ProjectDir:       "/project",
		ExcludePatterns:  []string{"build/**"},
		ScanOptions:      ScanOptions{Verbose: true},
		DebounceDuration: 25 * time.Millisecond,
		ScanTimeout:      10 * time.Second,
	}, &Scanner{binaryPath: "ubs"})

	if auto.config.DebounceDuration != 25*time.Millisecond {
		t.Fatalf("DebounceDuration = %v, want 25ms", auto.config.DebounceDuration)
	}
	if auto.config.ScanTimeout != 10*time.Second {
		t.Fatalf("ScanTimeout = %v, want 10s", auto.config.ScanTimeout)
	}
	if !auto.config.ScanOptions.Verbose {
		t.Fatal("expected custom ScanOptions to be preserved")
	}

	required := []string{".git", "node_modules", "vendor", ".beads", "*.min.js", "*.min.css", "build/**"}
	for _, pattern := range required {
		found := false
		for _, existing := range auto.config.ExcludePatterns {
			if existing == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected exclude pattern %q in %#v", pattern, auto.config.ExcludePatterns)
		}
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

func TestAutoScanner_StopWaitsForInFlightScan(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultAutoScannerConfig(tmpDir)
	cfg.ScanTimeout = time.Second

	auto := NewAutoScannerWithScanner(cfg, &Scanner{binaryPath: "ubs"})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once

	auto.scan = func(ctx context.Context, path string, opts ScanOptions) (*ScanResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	}

	if err := auto.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		_ = auto.Stop()
	}()

	auto.TriggerScan()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}

	stopped := make(chan error, 1)
	go func() {
		stopped <- auto.Stop()
	}()

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before in-flight scan finished cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not wait for in-flight scan cleanup")
	}
}

func TestAutoScanner_StaleScanDoesNotOverwriteLatestResult(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultAutoScannerConfig(tmpDir)
	cfg.ScanTimeout = time.Second

	auto := NewAutoScannerWithScanner(cfg, &Scanner{binaryPath: "ubs"})

	firstStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	completions := make(chan string, 2)
	result1 := &ScanResult{Project: "first"}
	result2 := &ScanResult{Project: "second"}

	auto.config.OnScanComplete = func(result *ScanResult, err error) {
		if err != nil {
			completions <- "err"
			return
		}
		if result == nil {
			completions <- "nil"
			return
		}
		completions <- result.Project
	}

	var mu sync.Mutex
	calls := 0
	auto.scan = func(ctx context.Context, path string, opts ScanOptions) (*ScanResult, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()

		if call == 1 {
			select {
			case firstStarted <- struct{}{}:
			default:
			}
			<-releaseFirst
			return result1, nil
		}

		return result2, nil
	}

	auto.TriggerScan()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first scan did not start")
	}

	auto.TriggerScan()
	close(releaseFirst)
	auto.scanWG.Wait()

	if got := auto.LastResult(); got != result2 {
		t.Fatalf("LastResult = %#v, want latest result %#v", got, result2)
	}
	if auto.LastScanTime().IsZero() {
		t.Fatal("expected LastScanTime to be set by latest scan")
	}

	select {
	case got := <-completions:
		if got != "second" {
			t.Fatalf("first completion = %q, want second", got)
		}
	default:
		t.Fatal("expected latest scan completion callback")
	}

	select {
	case got := <-completions:
		t.Fatalf("unexpected stale completion callback %q", got)
	default:
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
