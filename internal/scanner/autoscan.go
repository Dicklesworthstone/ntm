// Package scanner provides auto-scan integration with file watching.
package scanner

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// AutoScannerConfig configures the auto-scanner behavior.
type AutoScannerConfig struct {
	// ProjectDir is the root directory to watch and scan.
	ProjectDir string

	// DebounceDuration is how long to wait after file changes before scanning.
	// Defaults to 1 second if not set.
	DebounceDuration time.Duration

	// ScanTimeout is the maximum time for a scan to complete.
	// Defaults to 60 seconds if not set.
	ScanTimeout time.Duration

	// ExcludePatterns are glob patterns for files/dirs to ignore.
	// Common defaults like .git, node_modules are added automatically.
	ExcludePatterns []string

	// ScanOptions are passed to the scanner.
	ScanOptions ScanOptions

	// OnScanStart is called when a scan begins.
	OnScanStart func()

	// OnScanComplete is called with scan results.
	OnScanComplete func(result *ScanResult, err error)

	// OnError is called when a watcher error occurs.
	OnError func(err error)
}

// DefaultAutoScannerConfig returns sensible defaults for auto-scanning.
func DefaultAutoScannerConfig(projectDir string) AutoScannerConfig {
	return AutoScannerConfig{
		ProjectDir:       projectDir,
		DebounceDuration: time.Second,
		ScanTimeout:      60 * time.Second,
		ExcludePatterns: []string{
			".git",
			"node_modules",
			"vendor",
			".beads",
			"*.min.js",
			"*.min.css",
		},
		ScanOptions: DefaultOptions(),
	}
}

// AutoScannerConfigFromProjectConfig creates an AutoScannerConfig using
// project configuration from .ntm.yaml.
func AutoScannerConfigFromProjectConfig(projectDir string, cfg *config.ScannerConfig) AutoScannerConfig {
	auto := DefaultAutoScannerConfig(projectDir)

	// Apply config defaults
	if cfg != nil {
		auto.ScanTimeout = cfg.Defaults.GetTimeout()
		if len(cfg.Defaults.Exclude) > 0 {
			auto.ExcludePatterns = cfg.Defaults.Exclude
		}
		auto.ScanOptions = ScanOptionsFromConfig(cfg, "dashboard")
	}

	return auto
}

// AutoScanner watches for file changes and triggers UBS scans automatically.
type AutoScanner struct {
	config  AutoScannerConfig
	scanner *Scanner
	watcher *watcher.Watcher

	mu            sync.Mutex
	running       bool
	lastScanTime  time.Time
	lastResult    *ScanResult
	pendingCancel context.CancelFunc
}

// NewAutoScanner creates a new AutoScanner instance.
// Returns an error if UBS is not available.
func NewAutoScanner(cfg AutoScannerConfig) (*AutoScanner, error) {
	s, err := New()
	if err != nil {
		return nil, err
	}

	return &AutoScanner{
		config:  cfg,
		scanner: s,
	}, nil
}

// NewAutoScannerWithScanner creates an AutoScanner with an existing Scanner.
// Useful when you want to reuse a scanner instance.
func NewAutoScannerWithScanner(cfg AutoScannerConfig, scanner *Scanner) *AutoScanner {
	return &AutoScanner{
		config:  cfg,
		scanner: scanner,
	}
}

// Start begins watching for file changes and auto-scanning.
func (a *AutoScanner) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return nil // Already running
	}

	// Create watcher with event handler
	w, err := watcher.New(
		a.handleEvents,
		watcher.WithDebounceDuration(a.config.DebounceDuration),
		watcher.WithEventFilter(watcher.Write|watcher.Create|watcher.Remove),
		watcher.WithRecursive(true),
		watcher.WithErrorHandler(func(err error) {
			if a.config.OnError != nil {
				a.config.OnError(err)
			}
		}),
	)
	if err != nil {
		return err
	}

	// Add project directory to watch
	if err := w.Add(a.config.ProjectDir); err != nil {
		w.Close()
		return err
	}

	a.watcher = w
	a.running = true

	return nil
}

// Stop stops watching and scanning.
func (a *AutoScanner) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.running {
		return nil
	}

	// Cancel any pending scan
	if a.pendingCancel != nil {
		a.pendingCancel()
		a.pendingCancel = nil
	}

	// Close watcher
	if a.watcher != nil {
		if err := a.watcher.Close(); err != nil {
			return err
		}
		a.watcher = nil
	}

	a.running = false
	return nil
}

// IsRunning returns true if the auto-scanner is active.
func (a *AutoScanner) IsRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// LastScanTime returns when the last scan completed.
func (a *AutoScanner) LastScanTime() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastScanTime
}

// LastResult returns the result of the most recent scan.
func (a *AutoScanner) LastResult() *ScanResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastResult
}

// TriggerScan manually triggers a scan (useful for initial scan).
func (a *AutoScanner) TriggerScan() {
	go a.runScan()
}

// handleEvents processes file change events from the watcher.
func (a *AutoScanner) handleEvents(events []watcher.Event) {
	// Filter out excluded paths
	relevant := a.filterEvents(events)
	if len(relevant) == 0 {
		return
	}

	// Trigger a scan
	go a.runScan()
}

// filterEvents removes events for excluded paths.
func (a *AutoScanner) filterEvents(events []watcher.Event) []watcher.Event {
	var filtered []watcher.Event
	for _, e := range events {
		if !a.isExcluded(e.Path) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// isExcluded checks if a path matches any exclude pattern.
func (a *AutoScanner) isExcluded(path string) bool {
	// Get relative path for pattern matching
	rel, err := filepath.Rel(a.config.ProjectDir, path)
	if err != nil {
		rel = path
	}

	for _, pattern := range a.config.ExcludePatterns {
		// Check if any path component matches the pattern
		parts := strings.Split(rel, string(filepath.Separator))
		for _, part := range parts {
			if match, _ := filepath.Match(pattern, part); match {
				return true
			}
		}

		// Also check the full relative path
		if match, _ := filepath.Match(pattern, rel); match {
			return true
		}
	}
	return false
}

// runScan executes a UBS scan.
func (a *AutoScanner) runScan() {
	a.mu.Lock()
	// Cancel any previous pending scan
	if a.pendingCancel != nil {
		a.pendingCancel()
	}

	// Create new context for this scan
	ctx, cancel := context.WithTimeout(context.Background(), a.config.ScanTimeout)
	a.pendingCancel = cancel
	a.mu.Unlock()

	defer cancel()

	// Notify scan start
	if a.config.OnScanStart != nil {
		a.config.OnScanStart()
	}

	// Run the scan
	result, err := a.scanner.Scan(ctx, a.config.ProjectDir, a.config.ScanOptions)

	// Store result
	a.mu.Lock()
	a.lastScanTime = time.Now()
	if err == nil {
		a.lastResult = result
	}
	a.mu.Unlock()

	// Notify completion
	if a.config.OnScanComplete != nil {
		a.config.OnScanComplete(result, err)
	}
}

// WatchAndScan is a convenience function that starts auto-scanning and blocks
// until the context is cancelled.
func WatchAndScan(ctx context.Context, cfg AutoScannerConfig) error {
	auto, err := NewAutoScanner(cfg)
	if err != nil {
		return err
	}

	if err := auto.Start(); err != nil {
		return err
	}
	defer auto.Stop()

	// Run initial scan
	auto.TriggerScan()

	// Block until context is done
	<-ctx.Done()
	return ctx.Err()
}
