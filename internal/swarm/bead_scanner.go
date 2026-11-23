package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// BeadScanner discovers projects and counts their open beads.
type BeadScanner struct {
	// BaseDir is the root directory to scan (e.g., "/dp")
	BaseDir string

	// ExplicitProjects overrides directory scanning with specific paths
	ExplicitProjects []string

	// brPath is the path to the br binary (default: "br")
	brPath string

	// Parallelism is the number of concurrent project scans (default: 4)
	Parallelism int

	// Logger for structured logging
	Logger *slog.Logger
}

// ScanResult contains the complete scan output.
type ScanResult struct {
	Projects      []ProjectBeadCount `json:"projects"`
	TotalProjects int                `json:"total_projects"`
	TotalBeads    int                `json:"total_beads"`
	ScanDuration  time.Duration      `json:"scan_duration"`
	Errors        []ScanError        `json:"errors,omitempty"`
}

// ScanError records a project-specific scan error.
type ScanError struct {
	Project string `json:"project"`
	Error   string `json:"error"`
}

// BeadScannerOption configures a BeadScanner.
type BeadScannerOption func(*BeadScanner)

// WithExplicitProjects sets explicit project paths to scan.
func WithExplicitProjects(projects []string) BeadScannerOption {
	return func(bs *BeadScanner) {
		bs.ExplicitProjects = projects
	}
}

// NewBeadScanner creates a new BeadScanner with the given base directory.
func NewBeadScanner(baseDir string, opts ...BeadScannerOption) *BeadScanner {
	bs := &BeadScanner{
		BaseDir:     baseDir,
		brPath:      "br",
		Parallelism: 4,
		Logger:      slog.Default(),
	}

	for _, opt := range opts {
		opt(bs)
	}

	return bs
}

// Scan discovers all projects and counts their beads.
func (bs *BeadScanner) Scan(ctx context.Context) (*ScanResult, error) {
	start := time.Now()

	bs.Logger.Info("[BeadScanner] Starting scan",
		"base_dir", bs.BaseDir,
		"explicit_projects", len(bs.ExplicitProjects),
		"parallelism", bs.Parallelism)

	// Discover projects
	projectPaths, err := bs.discoverProjects()
	if err != nil {
		return nil, fmt.Errorf("discover projects: %w", err)
	}

	bs.Logger.Info("[BeadScanner] Discovered projects",
		"count", len(projectPaths))

	if len(projectPaths) == 0 {
		return &ScanResult{
			Projects:      []ProjectBeadCount{},
			TotalProjects: 0,
			TotalBeads:    0,
			ScanDuration:  time.Since(start),
		}, nil
	}

	// Scan projects concurrently
	result := bs.scanProjectsConcurrently(ctx, projectPaths)
	result.ScanDuration = time.Since(start)

	bs.Logger.Info("[BeadScanner] Scan complete",
		"projects", result.TotalProjects,
		"beads", result.TotalBeads,
		"errors", len(result.Errors),
		"duration", result.ScanDuration)

	return result, nil
}

// ScanProject counts beads for a single project.
func (bs *BeadScanner) ScanProject(ctx context.Context, projectPath string) (ProjectBeadCount, error) {
	result := ProjectBeadCountFromPath(projectPath, 0)

	if _, err := os.Stat(projectPath); err != nil {
		return result, fmt.Errorf("project not found: %s", projectPath)
	}

	if !hasBeads(projectPath) {
		bs.Logger.Debug("[BeadScanner] no_beads_dir",
			"project", projectPath)
		return result, nil
	}

	bs.Logger.Debug("[BeadScanner] Scanning project",
		"project", projectPath)

	count, err := bs.countBeads(ctx, projectPath)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		bs.Logger.Warn("[BeadScanner] Failed to count beads",
			"project", projectPath,
			"error", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			// Degrade gracefully when the optional br CLI is not installed.
			return result, nil
		}
		return result, err
	}

	bs.Logger.Debug("[BeadScanner] Bead count",
		"project", projectPath,
		"beads", count)

	result.OpenBeads = count
	return result, nil
}

// discoverProjects finds all projects to scan.
func (bs *BeadScanner) discoverProjects() ([]string, error) {
	// Use explicit projects if provided
	if len(bs.ExplicitProjects) > 0 {
		paths := make([]string, 0, len(bs.ExplicitProjects))
		for _, p := range bs.ExplicitProjects {
			path := p
			if !filepath.IsAbs(path) {
				path = filepath.Join(bs.BaseDir, p)
			}
			// Verify it exists
			if _, err := os.Stat(path); err != nil {
				bs.Logger.Debug("[BeadScanner] project_skipped",
					"path", path,
					"reason", "not_found")
				continue
			}
			if !isProject(path) {
				bs.Logger.Debug("[BeadScanner] project_skipped",
					"path", path,
					"reason", "missing_markers")
				continue
			}
			bs.Logger.Debug("[BeadScanner] project_discovered",
				"path", path)
			paths = append(paths, path)
		}
		bs.Logger.Info("[BeadScanner] discovery_complete",
			"count", len(paths))
		return paths, nil
	}

	// Scan base directory
	entries, err := os.ReadDir(bs.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", bs.BaseDir, err)
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			bs.Logger.Debug("[BeadScanner] project_skipped",
				"path", filepath.Join(bs.BaseDir, entry.Name()),
				"reason", "not_directory")
			continue
		}

		// Skip hidden directories
		if strings.HasPrefix(entry.Name(), ".") {
			bs.Logger.Debug("[BeadScanner] project_skipped",
				"path", filepath.Join(bs.BaseDir, entry.Name()),
				"reason", "hidden")
			continue
		}

		projectPath := filepath.Join(bs.BaseDir, entry.Name())

		// Check if it looks like a project
		if !isProject(projectPath) {
			bs.Logger.Debug("[BeadScanner] project_skipped",
				"path", projectPath,
				"reason", "missing_markers")
			continue
		}

		bs.Logger.Debug("[BeadScanner] project_discovered",
			"path", projectPath)
		paths = append(paths, projectPath)
	}

	bs.Logger.Info("[BeadScanner] discovery_complete",
		"count", len(paths))
	return paths, nil
}

// isProject checks if a directory looks like a project.
func isProject(path string) bool {
	// Check for .git directory
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	// Check for .beads directory
	if _, err := os.Stat(filepath.Join(path, ".beads")); err == nil {
		return true
	}
	return false
}

// hasBeads checks if a project has bead tracking enabled.
func hasBeads(path string) bool {
	beadsPath := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsPath); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(beadsPath, "issues.jsonl")); err != nil {
		return false
	}
	return true
}

// scanProjectsConcurrently scans all projects with bounded parallelism.
func (bs *BeadScanner) scanProjectsConcurrently(ctx context.Context, projectPaths []string) *ScanResult {
	type scanJob struct {
		path string
	}

	type scanResult struct {
		project ProjectBeadCount
		err     error
	}

	jobs := make(chan scanJob, len(projectPaths))
	results := make(chan scanResult, len(projectPaths))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < bs.Parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				select {
				case <-ctx.Done():
					results <- scanResult{err: ctx.Err()}
					return
				default:
					project, err := bs.ScanProject(ctx, job.path)
					results <- scanResult{project: project, err: err}
				}
			}
		}()
	}

	// Send jobs
	for _, path := range projectPaths {
		jobs <- scanJob{path: path}
	}
	close(jobs)

	// Wait for completion in a separate goroutine
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var projects []ProjectBeadCount
	var errors []ScanError
	totalBeads := 0

	for result := range results {
		if result.err != nil {
			errors = append(errors, ScanError{
				Project: result.project.Path,
				Error:   result.err.Error(),
			})
			continue
		}
		projects = append(projects, result.project)
		totalBeads += result.project.OpenBeads
	}

	return &ScanResult{
		Projects:      projects,
		TotalProjects: len(projects),
		TotalBeads:    totalBeads,
		Errors:        errors,
	}
}

// countBeads runs `br list --status open --json` and parses the output.
func (bs *BeadScanner) countBeads(ctx context.Context, projectPath string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Check if .beads directory exists
	if !hasBeads(projectPath) {
		// No beads directory or issues.jsonl means no beads
		return 0, nil
	}

	// Run br command
	cmd := exec.CommandContext(ctx, bs.brPath, "list", "--status", "open", "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = projectPath

	// Capture output
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			bs.Logger.Debug("[BeadScanner] br command failed",
				"project", projectPath,
				"exit_code", exitErr.ExitCode(),
				"stderr", stderr)
			if stderr != "" {
				return 0, fmt.Errorf("br list failed for %s (exit %d): %s: %w", projectPath, exitErr.ExitCode(), stderr, err)
			}
			return 0, fmt.Errorf("br list failed for %s (exit %d): %w", projectPath, exitErr.ExitCode(), err)
		}
		return 0, fmt.Errorf("run %s list for %s: %w", bs.brPath, projectPath, err)
	}

	// Parse JSON output from br list --json.
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return 0, nil
	}
	issues, err := bv.UnmarshalBdList[json.RawMessage](trimmed)
	if err != nil {
		bs.Logger.Debug("[BeadScanner] Failed to parse br output",
			"project", projectPath,
			"output", trimmed,
			"error", err)
		return 0, fmt.Errorf("parse br output for %s: %w", projectPath, err)
	}

	return len(issues), nil
}
