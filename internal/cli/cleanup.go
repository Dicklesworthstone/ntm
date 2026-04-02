package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func newCleanupCmd() *cobra.Command {
	var (
		dryRun     bool
		maxAgeHrs  int
		verbose    bool
		forceClean bool
	)

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up stale NTM temporary files and directories",
		Long: `Remove stale NTM temporary files and directories from /tmp.

This command identifies and removes:
  - ntm-lifecycle-* directories (from interrupted tests)
  - ntm-*-desc.md files (description files)
  - ntm-graph.html files (graph exports)
  - test-ntm-* directories (test artifacts)
  - ntm-atomic-* files (orphaned atomic writes)
  - ntm-prompt-* files (orphaned prompt edits)
  - ntm-mail-* files (orphaned mail compositions)

Files older than --max-age (default: 24 hours) are considered stale.

Examples:
  ntm cleanup              # Clean files older than 24h
  ntm cleanup --dry-run    # Preview what would be deleted
  ntm cleanup --max-age=1  # Clean files older than 1 hour
  ntm cleanup --force      # Clean all NTM temp files regardless of age
  ntm cleanup --json       # JSON output for automation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCleanup(dryRun, maxAgeHrs, verbose, forceClean)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be deleted without actually deleting")
	cmd.Flags().IntVar(&maxAgeHrs, "max-age", 24, "Maximum age in hours before a file is considered stale")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show detailed information about each file")
	cmd.Flags().BoolVar(&forceClean, "force", false, "Clean all NTM temp files regardless of age")

	return cmd
}

// cleanupResult represents the result of cleanup for a single file/directory
type cleanupResult struct {
	Path     string    `json:"path"`
	Type     string    `json:"type"` // "file" or "directory"
	Size     int64     `json:"size_bytes"`
	Age      string    `json:"age"`
	AgeHours float64   `json:"age_hours"`
	ModTime  time.Time `json:"mod_time"`
	Deleted  bool      `json:"deleted"`
	Error    string    `json:"error,omitempty"`
	Pattern  string    `json:"pattern"` // which pattern matched
}

// cleanupResponse is the JSON response for cleanup command
type cleanupResponse struct {
	output.TimestampedResponse
	DryRun       bool            `json:"dry_run"`
	MaxAgeHours  int             `json:"max_age_hours"`
	Results      []cleanupResult `json:"results"`
	TotalFiles   int             `json:"total_files"`
	TotalSize    int64           `json:"total_size_bytes"`
	DeletedFiles int             `json:"deleted_files"`
	DeletedSize  int64           `json:"deleted_size_bytes"`
	SkippedFiles int             `json:"skipped_files"`
	ErrorCount   int             `json:"error_count"`
}

// ntmTempPatterns defines patterns to match NTM temp files
var ntmTempPatterns = []string{
	"ntm-lifecycle-*",
	"ntm-*-desc.md",
	"ntm-graph.html",
	"test-ntm-*",
	"ntm-atomic-*",
	"ntm-prompt-*.md",
	"ntm-mail-*.md",
	".handoff-*.tmp",
	"events-rotate-*.jsonl",
	"rotation-*.tmp",
	"pending-*.tmp",
}

func runCleanup(dryRun bool, maxAgeHrs int, verbose bool, forceClean bool) error {
	tmpDir := os.TempDir()
	cutoff := time.Now().Add(-time.Duration(maxAgeHrs) * time.Hour)

	var results []cleanupResult
	var totalSize, deletedSize int64
	var deletedCount, errorCount, skippedCount int

	// Scan /tmp for NTM files
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read temp directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		matchedPattern := matchesNTMPattern(name)
		if matchedPattern == "" {
			continue
		}

		fullPath := filepath.Join(tmpDir, name)
		info, err := entry.Info()
		if err != nil {
			results = append(results, cleanupResult{
				Path:    fullPath,
				Pattern: matchedPattern,
				Error:   err.Error(),
			})
			errorCount++
			continue
		}

		// Get size (for directories, calculate recursively)
		var size int64
		if entry.IsDir() {
			size = getDirSize(fullPath)
		} else {
			size = info.Size()
		}
		totalSize += size

		modTime := info.ModTime()
		age := time.Since(modTime)
		ageHours := age.Hours()

		result := cleanupResult{
			Path:     fullPath,
			Type:     getFileType(entry.IsDir()),
			Size:     size,
			Age:      formatCleanupDuration(age),
			AgeHours: ageHours,
			ModTime:  modTime,
			Pattern:  matchedPattern,
		}

		// Check if file is stale (older than cutoff) or force is enabled
		isStale := modTime.Before(cutoff) || forceClean
		if !isStale {
			result.Deleted = false
			skippedCount++
			results = append(results, result)
			continue
		}

		// Delete if not dry run
		if !dryRun {
			var removeErr error
			if entry.IsDir() {
				removeErr = os.RemoveAll(fullPath)
			} else {
				removeErr = os.Remove(fullPath)
			}

			if removeErr != nil {
				result.Error = removeErr.Error()
				errorCount++
			} else {
				result.Deleted = true
				deletedCount++
				deletedSize += size
			}
		} else {
			result.Deleted = false // Would be deleted
			deletedCount++         // Count as would-be-deleted for reporting
			deletedSize += size
		}

		results = append(results, result)
	}

	// JSON output
	if IsJSONOutput() {
		resp := cleanupResponse{
			TimestampedResponse: output.NewTimestamped(),
			DryRun:              dryRun,
			MaxAgeHours:         maxAgeHrs,
			Results:             results,
			TotalFiles:          len(results),
			TotalSize:           totalSize,
			DeletedFiles:        deletedCount,
			DeletedSize:         deletedSize,
			SkippedFiles:        skippedCount,
			ErrorCount:          errorCount,
		}
		return output.PrintJSON(resp)
	}

	// Text output
	t := theme.Current()

	fmt.Println()
	if dryRun {
		fmt.Printf("%s NTM Cleanup (Dry Run)%s\n", "\033[1m", "\033[0m")
	} else {
		fmt.Printf("%s NTM Cleanup%s\n", "\033[1m", "\033[0m")
	}
	fmt.Printf("%s═══════════════════════════════════════════════════%s\n\n", "\033[2m", "\033[0m")

	if len(results) == 0 {
		fmt.Printf("%s✓%s No stale NTM temp files found in %s\n\n", colorize(t.Success), "\033[0m", tmpDir)
		return nil
	}

	// Group by pattern for cleaner output
	byPattern := make(map[string][]cleanupResult)
	for _, r := range results {
		byPattern[r.Pattern] = append(byPattern[r.Pattern], r)
	}

	for pattern, items := range byPattern {
		fmt.Printf("%s%s:%s\n", "\033[1m", pattern, "\033[0m")
		for _, r := range items {
			var statusIcon, statusColor string
			if r.Error != "" {
				statusIcon = "✗"
				statusColor = colorize(t.Error)
			} else if r.Deleted || dryRun {
				statusIcon = "✓"
				statusColor = colorize(t.Success)
			} else {
				statusIcon = "○"
				statusColor = colorize(t.Overlay)
			}

			action := "deleted"
			if dryRun {
				action = "would delete"
			}
			if !r.Deleted && !dryRun && r.Error == "" {
				action = "skipped (too recent)"
			}

			fmt.Printf("  %s%s%s %s\n", statusColor, statusIcon, "\033[0m", filepath.Base(r.Path))
			if verbose {
				fmt.Printf("      Size: %s, Age: %s, Action: %s\n",
					formatCleanupBytes(r.Size), r.Age, action)
				if r.Error != "" {
					fmt.Printf("      Error: %s\n", r.Error)
				}
			}
		}
		fmt.Println()
	}

	// Summary
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")
	if dryRun {
		fmt.Printf("Would delete: %d files/directories (%s)\n", deletedCount, util.FormatBytes(deletedSize))
		fmt.Printf("Would skip: %d files/directories (younger than %dh)\n", skippedCount, maxAgeHrs)
		if len(results) > 0 {
			fmt.Printf("\n%sRun without --dry-run to delete these files.%s\n", "\033[2m", "\033[0m")
		}
	} else {
		fmt.Printf("Deleted: %d files/directories (%s)\n", deletedCount, util.FormatBytes(deletedSize))
		if skippedCount > 0 {
			fmt.Printf("Skipped: %d files/directories (younger than %dh)\n", skippedCount, maxAgeHrs)
		}
		if errorCount > 0 {
			fmt.Printf("%sErrors: %d%s\n", colorize(t.Error), errorCount, "\033[0m")
		}
	}
	fmt.Println()

	return nil
}

// matchesNTMPattern checks if a filename matches any NTM temp pattern
func matchesNTMPattern(name string) string {
	for _, pattern := range ntmTempPatterns {
		matched, err := filepath.Match(pattern, name)
		if err == nil && matched {
			return pattern
		}
	}

	return ""
}

// getDirSize calculates the total size of a directory recursively
func getDirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

// getFileType returns "directory" or "file"
func getFileType(isDir bool) string {
	if isDir {
		return "directory"
	}
	return "file"
}

// formatCleanupDuration formats a duration in a human-readable way
func formatCleanupDuration(d time.Duration) string {
	hours := d.Hours()
	if hours < 1 {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	if hours < 24 {
		return fmt.Sprintf("%.1fh", hours)
	}
	days := hours / 24
	return fmt.Sprintf("%.1fd", days)
}

// CleanupStats holds statistics from a cleanup operation
type CleanupStats struct {
	TotalFiles   int
	TotalSize    int64
	DeletedFiles int
	DeletedSize  int64
	Errors       int
}

// lastCleanupFile returns the path to the file tracking last cleanup time.
func lastCleanupFile() string {
	return filepath.Join(selectedConfigDir(), ".last-cleanup")
}

// shouldRunStartupCleanup checks if enough time has passed since the last cleanup
func shouldRunStartupCleanup(minIntervalHours int) bool {
	path := lastCleanupFile()
	info, err := os.Stat(path)
	if err != nil {
		// File doesn't exist or error - run cleanup
		return true
	}

	// Check if enough time has passed
	elapsed := time.Since(info.ModTime())
	return elapsed.Hours() >= float64(minIntervalHours)
}

// markCleanupDone updates the last cleanup timestamp
func markCleanupDone() {
	path := lastCleanupFile()
	// Create parent directory if needed
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	// Touch the file
	f, err := os.Create(path)
	if err == nil {
		f.Close()
	}
}

// MaybeRunStartupCleanup runs cleanup if auto cleanup is enabled and enough
// time has passed since the last cleanup. This prevents cleanup from running
// on every NTM command invocation.
func MaybeRunStartupCleanup(enabled bool, maxAgeHours int, verbose bool) {
	if !enabled {
		return
	}

	// Only run cleanup once per maxAgeHours period
	if !shouldRunStartupCleanup(maxAgeHours) {
		return
	}

	stats, err := RunStartupCleanup(maxAgeHours, verbose)
	if err != nil && verbose {
		fmt.Fprintf(os.Stderr, "ntm: startup cleanup failed: %v\n", err)
	}

	if stats.DeletedFiles > 0 && verbose {
		fmt.Fprintf(os.Stderr, "ntm: cleaned up %d stale temp files (%s)\n",
			stats.DeletedFiles, util.FormatBytes(stats.DeletedSize))
	}

	markCleanupDone()
}

// RunStartupCleanup performs a silent cleanup of stale NTM temp files.
// This is called during startup if auto cleanup is enabled in config.
// It returns the cleanup stats and any error encountered.
func RunStartupCleanup(maxAgeHours int, verbose bool) (CleanupStats, error) {
	tmpDir := os.TempDir()
	cutoff := time.Now().Add(-time.Duration(maxAgeHours) * time.Hour)

	var stats CleanupStats

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return stats, fmt.Errorf("failed to read temp directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		matchedPattern := matchesNTMPattern(name)
		if matchedPattern == "" {
			continue
		}

		fullPath := filepath.Join(tmpDir, name)
		info, err := entry.Info()
		if err != nil {
			stats.Errors++
			continue
		}

		// Get size
		var size int64
		if entry.IsDir() {
			size = getDirSize(fullPath)
		} else {
			size = info.Size()
		}
		stats.TotalSize += size
		stats.TotalFiles++

		// Check if file is stale
		if !info.ModTime().Before(cutoff) {
			continue
		}

		// Delete the file/directory
		var removeErr error
		if entry.IsDir() {
			removeErr = os.RemoveAll(fullPath)
		} else {
			removeErr = os.Remove(fullPath)
		}

		if removeErr != nil {
			stats.Errors++
			if verbose {
				fmt.Fprintf(os.Stderr, "ntm: cleanup failed for %s: %v\n", fullPath, removeErr)
			}
		} else {
			stats.DeletedFiles++
			stats.DeletedSize += size
			if verbose {
				fmt.Fprintf(os.Stderr, "ntm: cleaned up %s (%s)\n", fullPath, util.FormatBytes(size))
			}
		}
	}

	return stats, nil
}
