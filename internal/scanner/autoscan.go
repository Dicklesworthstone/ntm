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

	// UBSPath is the path to the ubs binary. If empty, looks in PATH.
	UBSPath string

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
			auto.ExcludePatterns = mergeExcludePatterns(auto.ExcludePatterns, cfg.Defaults.Exclude)
		}
		auto.UBSPath = cfg.UBSPath
		auto.ScanOptions = ScanOptionsFromConfig(cfg, "dashboard")
	}

	return auto
}

func normalizeAutoScannerConfig(cfg AutoScannerConfig) AutoScannerConfig {
	defaults := DefaultAutoScannerConfig(cfg.ProjectDir)

	if cfg.DebounceDuration <= 0 {
		cfg.DebounceDuration = defaults.DebounceDuration
	}
	if cfg.ScanTimeout <= 0 {
		cfg.ScanTimeout = defaults.ScanTimeout
	}
	if len(cfg.ExcludePatterns) == 0 {
		cfg.ExcludePatterns = defaults.ExcludePatterns
	} else {
		cfg.ExcludePatterns = mergeExcludePatterns(defaults.ExcludePatterns, cfg.ExcludePatterns)
	}
	if isZeroScanOptions(cfg.ScanOptions) {
		cfg.ScanOptions = defaults.ScanOptions
	}

	return cfg
}

func isZeroScanOptions(opts ScanOptions) bool {
	return len(opts.Languages) == 0 &&
		len(opts.ExcludeLanguages) == 0 &&
		!opts.CI &&
		!opts.FailOnWarning &&
		!opts.Verbose &&
		opts.Timeout <= 0 &&
		!opts.StagedOnly &&
		!opts.DiffOnly
}

// AutoScanner watches for file changes and triggers UBS scans automatically.
type AutoScanner struct {
	config  AutoScannerConfig
	scanner *Scanner
	watcher *watcher.Watcher
	scan    func(context.Context, string, ScanOptions) (*ScanResult, error)

	mu                sync.Mutex
	running           bool
	lastScanTime      time.Time
	lastResult        *ScanResult
	pendingCancel     context.CancelFunc
	pendingGeneration uint64
	scanGeneration    uint64
	scanWG            sync.WaitGroup
}

// NewAutoScanner creates a new AutoScanner instance.
// Returns an error if UBS is not available.
func NewAutoScanner(cfg AutoScannerConfig) (*AutoScanner, error) {
	cfg = normalizeAutoScannerConfig(cfg)

	var s *Scanner
	var err error

	if cfg.UBSPath != "" {
		s = &Scanner{binaryPath: cfg.UBSPath}
	} else {
		s, err = New()
	}

	if err != nil {
		return nil, err
	}

	return &AutoScanner{
		config:  cfg,
		scanner: s,
		scan:    s.Scan,
	}, nil
}

// NewAutoScannerWithScanner creates an AutoScanner with an existing Scanner.
// Useful when you want to reuse a scanner instance.
func NewAutoScannerWithScanner(cfg AutoScannerConfig, scanner *Scanner) *AutoScanner {
	cfg = normalizeAutoScannerConfig(cfg)

	return &AutoScanner{
		config:  cfg,
		scanner: scanner,
		scan:    scanner.Scan,
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
	if !a.running {
		a.mu.Unlock()
		return nil
	}

	a.running = false
	a.scanGeneration++

	// Cancel any pending scan
	if a.pendingCancel != nil {
		a.pendingCancel()
		a.pendingCancel = nil
		a.pendingGeneration = 0
	}

	w := a.watcher
	a.watcher = nil
	a.mu.Unlock()

	if w != nil {
		if err := w.Close(); err != nil {
			a.scanWG.Wait()
			return err
		}
	}

	a.scanWG.Wait()
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
	a.launchScan(true)
}

// handleEvents processes file change events from the watcher.
func (a *AutoScanner) handleEvents(events []watcher.Event) {
	// Filter out excluded paths
	relevant := a.filterEvents(events)
	if len(relevant) == 0 {
		return
	}

	// Trigger a scan
	a.launchScan(false)
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
	rel = filepath.Clean(rel)

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
		if matchRecursivePattern(rel, pattern) {
			return true
		}
	}
	return false
}

func mergeExcludePatterns(defaults, extras []string) []string {
	merged := make([]string, 0, len(defaults)+len(extras))
	seen := make(map[string]bool, len(defaults)+len(extras))

	for _, pattern := range defaults {
		if pattern == "" || seen[pattern] {
			continue
		}
		merged = append(merged, pattern)
		seen[pattern] = true
	}
	for _, pattern := range extras {
		if pattern == "" || seen[pattern] {
			continue
		}
		merged = append(merged, pattern)
		seen[pattern] = true
	}

	return merged
}

func matchRecursivePattern(rel, pattern string) bool {
	normalizedRel := filepath.ToSlash(strings.TrimPrefix(rel, "."+string(filepath.Separator)))
	normalizedPattern := filepath.ToSlash(pattern)

	if strings.HasSuffix(normalizedPattern, "/**") {
		base := strings.TrimSuffix(normalizedPattern, "/**")
		base = strings.TrimSuffix(base, "/")
		if base == "" {
			return normalizedRel != ""
		}
		return normalizedRel == base || strings.HasPrefix(normalizedRel, base+"/")
	}

	return false
}

// runScan executes a UBS scan.
func (a *AutoScanner) runScan(generation uint64) {
	defer a.scanWG.Done()

	a.mu.Lock()
	// Cancel any previous pending scan
	if a.pendingCancel != nil {
		a.pendingCancel()
	}

	// Create new context for this scan
	ctx, cancel := context.WithTimeout(context.Background(), a.config.ScanTimeout)
	a.pendingCancel = cancel
	a.pendingGeneration = generation
	scan := a.scan
	projectDir := a.config.ProjectDir
	scanOptions := a.config.ScanOptions
	onScanStart := a.config.OnScanStart
	onScanComplete := a.config.OnScanComplete
	a.mu.Unlock()

	defer cancel()

	// Notify scan start
	if onScanStart != nil {
		onScanStart()
	}

	// Run the scan
	result, err := scan(ctx, projectDir, scanOptions)
	completedAt := time.Now()

	// Store result
	a.mu.Lock()
	if a.pendingGeneration == generation {
		a.pendingCancel = nil
		a.pendingGeneration = 0
	}
	stale := generation != a.scanGeneration
	if !stale {
		a.lastScanTime = completedAt
		if err == nil {
			a.lastResult = result
		}
	}
	a.mu.Unlock()

	// Notify completion
	if !stale && onScanComplete != nil {
		onScanComplete(result, err)
	}
}

func (a *AutoScanner) launchScan(allowWhenStopped bool) {
	a.mu.Lock()
	if !allowWhenStopped && !a.running {
		a.mu.Unlock()
		return
	}
	a.scanGeneration++
	generation := a.scanGeneration
	a.scanWG.Add(1)
	a.mu.Unlock()

	go a.runScan(generation)
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
