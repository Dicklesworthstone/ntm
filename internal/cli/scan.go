package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/spf13/cobra"
)

func newScanCmd() *cobra.Command {
	var (
		languages       []string
		exclude         []string
		ci              bool
		failOnWarning   bool
		verbose         bool
		timeoutSeconds  int
		stagedOnly      bool
		diffOnly        bool
	)

	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Run UBS (Ultimate Bug Scanner) on your codebase",
		Long: `Run the Ultimate Bug Scanner (UBS) on files or directories.

UBS is a meta-scanner that detects bugs, anti-patterns, and security issues
across multiple languages (Go, JS/TS, Python, Rust, Java, Ruby, Swift, C/C++).

Examples:
  ntm scan                   # Scan current directory
  ntm scan .                 # Same as above
  ntm scan src/              # Scan specific directory
  ntm scan main.go           # Scan single file
  ntm scan --staged          # Scan only staged files
  ntm scan --diff            # Scan only modified files
  ntm scan --only=golang     # Restrict to Go files
  ntm scan --json            # Output JSON for automation
  ntm scan --fail-on-warning # Exit non-zero on warnings`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}

			// Resolve to absolute path
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}

			opts := scanner.ScanOptions{
				Languages:        languages,
				ExcludeLanguages: exclude,
				CI:               ci,
				FailOnWarning:    failOnWarning,
				Verbose:          verbose,
				Timeout:          time.Duration(timeoutSeconds) * time.Second,
				StagedOnly:       stagedOnly,
				DiffOnly:         diffOnly,
			}

			return runScan(absPath, opts)
		},
	}

	cmd.Flags().StringSliceVar(&languages, "only", nil, "Restrict to languages (comma-separated: golang,js,python,rust,java,ruby,swift,cpp)")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "Exclude languages (comma-separated)")
	cmd.Flags().BoolVar(&ci, "ci", false, "CI mode (stable timestamps)")
	cmd.Flags().BoolVar(&failOnWarning, "fail-on-warning", false, "Exit non-zero on warnings")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().IntVar(&timeoutSeconds, "timeout", 120, "Timeout in seconds")
	cmd.Flags().BoolVar(&stagedOnly, "staged", false, "Scan only staged files")
	cmd.Flags().BoolVar(&diffOnly, "diff", false, "Scan only modified files")

	return cmd
}

func runScan(path string, opts scanner.ScanOptions) error {
	t := theme.Current()

	// Check if UBS is available
	if !scanner.IsAvailable() {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error":     "ubs not installed",
				"available": false,
			})
		}
		fmt.Printf("%s✗%s UBS not installed\n", colorize(t.Error), "\033[0m")
		fmt.Printf("  Install: %shttps://github.com/nightowlai/ubs%s\n", "\033[2m", "\033[0m")
		return nil
	}

	// Create scanner
	s, err := scanner.New()
	if err != nil {
		return fmt.Errorf("creating scanner: %w", err)
	}

	// Show scanning message if not JSON output
	if !jsonOutput {
		fmt.Printf("Scanning %s...\n", path)
	}

	// Run scan
	ctx := context.Background()
	result, err := s.Scan(ctx, path, opts)
	if err != nil {
		if jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
				"error": err.Error(),
			})
		}
		return fmt.Errorf("scan failed: %w", err)
	}

	// Output results
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	// Text output
	printScanResults(t, result, opts.FailOnWarning)

	// Exit with error if requested and issues found
	if opts.FailOnWarning && result.HasWarning() {
		os.Exit(1)
	}
	if result.HasCritical() {
		os.Exit(1)
	}

	return nil
}

func printScanResults(t theme.Theme, result *scanner.ScanResult, showWarnings bool) {
	fmt.Println()
	fmt.Printf("%sScan Results%s\n", "\033[1m", "\033[0m")
	fmt.Printf("%s═══════════════════════════════════════════════════%s\n\n", "\033[2m", "\033[0m")

	// Summary stats
	fmt.Printf("  Files scanned:   %d\n", result.Totals.Files)
	fmt.Printf("  Duration:        %s\n", result.Duration.Round(time.Millisecond))
	fmt.Println()

	// Issue counts with colors
	critColor := "\033[0m"
	warnColor := "\033[0m"
	infoColor := "\033[0m"

	if result.Totals.Critical > 0 {
		critColor = colorize(t.Error)
	}
	if result.Totals.Warning > 0 {
		warnColor = colorize(t.Warning)
	}
	if result.Totals.Info > 0 {
		infoColor = colorize(t.Info)
	}

	fmt.Printf("  %sCritical:%s  %s%d%s\n", "\033[1m", "\033[0m", critColor, result.Totals.Critical, "\033[0m")
	fmt.Printf("  %sWarning:%s   %s%d%s\n", "\033[1m", "\033[0m", warnColor, result.Totals.Warning, "\033[0m")
	fmt.Printf("  %sInfo:%s      %s%d%s\n", "\033[1m", "\033[0m", infoColor, result.Totals.Info, "\033[0m")
	fmt.Println()

	// Per-language breakdown
	if len(result.Scanners) > 0 {
		fmt.Printf("%sLanguage Breakdown:%s\n\n", "\033[1m", "\033[0m")
		for _, sr := range result.Scanners {
			if sr.Files == 0 && sr.Critical == 0 && sr.Warning == 0 {
				continue // Skip languages with no activity
			}
			issues := sr.Critical + sr.Warning
			statusIcon := "✓"
			statusColor := colorize(t.Success)
			if sr.Critical > 0 {
				statusIcon = "✗"
				statusColor = colorize(t.Error)
			} else if sr.Warning > 0 {
				statusIcon = "⚠"
				statusColor = colorize(t.Warning)
			}
			fmt.Printf("  %s%s%s %-10s  %d file(s), %d issue(s)\n",
				statusColor, statusIcon, "\033[0m",
				sr.Language, sr.Files, issues)
		}
		fmt.Println()
	}

	// Summary line
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")
	if result.IsHealthy() {
		fmt.Printf("%s✓%s No critical or warning issues found\n", colorize(t.Success), "\033[0m")
	} else if result.HasCritical() {
		fmt.Printf("%s✗%s Critical issues found - fix before committing\n", colorize(t.Error), "\033[0m")
	} else if result.HasWarning() {
		fmt.Printf("%s⚠%s Warnings found - consider reviewing\n", colorize(t.Warning), "\033[0m")
	}
	fmt.Println()
}
