package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// PreCommitConfig holds configuration for the pre-commit hook.
type PreCommitConfig struct {
	// MaxCritical is the maximum allowed critical issues (default 0).
	MaxCritical int
	// MaxWarning is the maximum allowed warning issues (default 0).
	MaxWarning int
	// FailOnWarning treats warnings as errors.
	FailOnWarning bool
	// Timeout for the scan operation.
	Timeout time.Duration
	// Verbose enables detailed output.
	Verbose bool
	// SkipEmpty skips the scan if no staged files.
	SkipEmpty bool
}

// DefaultPreCommitConfig returns sensible defaults.
func DefaultPreCommitConfig() PreCommitConfig {
	return PreCommitConfig{
		MaxCritical:   0,
		MaxWarning:    0,
		FailOnWarning: true,
		Timeout:       60 * time.Second,
		Verbose:       false,
		SkipEmpty:     true,
	}
}

// PreCommitResult contains the result of running the pre-commit hook.
type PreCommitResult struct {
	Passed       bool              `json:"passed"`
	StagedFiles  []string          `json:"staged_files"`
	ScanResult   *scanner.ScanResult `json:"scan_result,omitempty"`
	BlockReason  string            `json:"block_reason,omitempty"`
	Duration     time.Duration     `json:"duration"`
	UBSAvailable bool              `json:"ubs_available"`
}

// RunPreCommit executes the pre-commit hook logic.
func RunPreCommit(ctx context.Context, repoPath string, config PreCommitConfig) (*PreCommitResult, error) {
	startTime := time.Now()
	result := &PreCommitResult{
		Passed:       true,
		UBSAvailable: scanner.IsAvailable(),
	}

	// Get staged files
	stagedFiles, err := getStagedFiles(repoPath)
	if err != nil {
		return nil, fmt.Errorf("getting staged files: %w", err)
	}
	result.StagedFiles = stagedFiles

	// Skip if no staged files
	if len(stagedFiles) == 0 && config.SkipEmpty {
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Check if UBS is available
	if !scanner.IsAvailable() {
		// Graceful degradation - pass but note UBS is not available
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Create scanner
	s, err := scanner.New()
	if err != nil {
		return nil, fmt.Errorf("creating scanner: %w", err)
	}

	// Apply timeout
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}

	// Run scan on staged files
	opts := scanner.ScanOptions{
		StagedOnly:    true,
		FailOnWarning: config.FailOnWarning,
		Verbose:       config.Verbose,
		Timeout:       config.Timeout,
	}

	scanResult, err := s.Scan(ctx, repoPath, opts)
	if err != nil {
		return nil, fmt.Errorf("running scan: %w", err)
	}
	result.ScanResult = scanResult
	result.Duration = time.Since(startTime)

	// Check thresholds
	if scanResult.Totals.Critical > config.MaxCritical {
		result.Passed = false
		result.BlockReason = fmt.Sprintf(
			"critical issues exceeded threshold: %d > %d",
			scanResult.Totals.Critical, config.MaxCritical,
		)
	} else if config.FailOnWarning && scanResult.Totals.Warning > config.MaxWarning {
		result.Passed = false
		result.BlockReason = fmt.Sprintf(
			"warning issues exceeded threshold: %d > %d",
			scanResult.Totals.Warning, config.MaxWarning,
		)
	}

	return result, nil
}

// getStagedFiles returns a list of staged file paths.
func getStagedFiles(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "diff", "--name-only", "--cached", "--diff-filter=ACMR")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.TrimSpace(string(output))
	if lines == "" {
		return nil, nil
	}

	return strings.Split(lines, "\n"), nil
}

// PrintPreCommitResult prints the result in a human-readable format.
func PrintPreCommitResult(result *PreCommitResult) {
	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		red    = "\033[31m"
		green  = "\033[32m"
		yellow = "\033[33m"
		dim    = "\033[2m"
	)

	fmt.Println()
	fmt.Printf("%s%sNTM Pre-commit Check%s\n", bold, "", reset)
	fmt.Printf("%s════════════════════════════════════════%s\n\n", dim, reset)

	// Staged files info
	fmt.Printf("  Staged files: %d\n", len(result.StagedFiles))
	fmt.Printf("  Duration:     %s\n\n", result.Duration.Round(time.Millisecond))

	// UBS availability
	if !result.UBSAvailable {
		fmt.Printf("  %s⚠%s UBS not installed - skipping scan\n", yellow, reset)
		fmt.Printf("    Install: %shttps://github.com/nightowlai/ubs%s\n\n", dim, reset)
		return
	}

	// No staged files
	if len(result.StagedFiles) == 0 {
		fmt.Printf("  %s•%s No staged files to check\n\n", dim, reset)
		return
	}

	// Scan results
	if result.ScanResult != nil {
		sr := result.ScanResult

		critColor := reset
		warnColor := reset
		if sr.Totals.Critical > 0 {
			critColor = red
		}
		if sr.Totals.Warning > 0 {
			warnColor = yellow
		}

		fmt.Printf("  %sCritical:%s  %s%d%s\n", bold, reset, critColor, sr.Totals.Critical, reset)
		fmt.Printf("  %sWarning:%s   %s%d%s\n", bold, reset, warnColor, sr.Totals.Warning, reset)
		fmt.Printf("  %sInfo:%s      %d\n\n", bold, reset, sr.Totals.Info)

		// Show findings summary
		if len(sr.Findings) > 0 {
			fmt.Printf("%sFindings:%s\n\n", bold, reset)
			for i, f := range sr.Findings {
				if i >= 5 {
					remaining := len(sr.Findings) - 5
					fmt.Printf("  %s... and %d more%s\n", dim, remaining, reset)
					break
				}
				severityColor := reset
				switch f.Severity {
				case scanner.SeverityCritical:
					severityColor = red
				case scanner.SeverityWarning:
					severityColor = yellow
				}
				fmt.Printf("  %s%s%s %s:%d - %s\n",
					severityColor, string(f.Severity), reset,
					f.File, f.Line, f.Message)
			}
			fmt.Println()
		}
	}

	// Result
	fmt.Printf("%s────────────────────────────────────────%s\n", dim, reset)
	if result.Passed {
		fmt.Printf("%s✓%s Pre-commit check passed\n\n", green, reset)
	} else {
		fmt.Printf("%s✗%s Pre-commit check failed\n", red, reset)
		fmt.Printf("  %s%s%s\n\n", red, result.BlockReason, reset)
		fmt.Printf("  Fix the issues above and try again.\n")
		fmt.Printf("  Run '%subs $(git diff --name-only --cached)%s' to see details.\n\n", dim, reset)
	}
}

// ExitCode returns the appropriate exit code for the result.
func (r *PreCommitResult) ExitCode() int {
	if r.Passed {
		return 0
	}
	return 1
}

// WriteResult writes the result as exit code to os.Exit.
func (r *PreCommitResult) Exit() {
	os.Exit(r.ExitCode())
}
