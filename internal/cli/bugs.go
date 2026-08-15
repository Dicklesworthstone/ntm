package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func newBugsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bugs",
		Short: "View and manage UBS (Ultimate Bug Scanner) findings",
		Long: `View and manage UBS scan findings, including notifications to agents.

Use this command to:
- List the most recent scan findings
- Notify relevant agents about bugs in their files
- Track bug counts across your session

Examples:
  ntm bugs list                # Show recent findings
  ntm bugs list --severity=critical  # Show only critical issues
  ntm bugs notify              # Send findings to agents via Agent Mail
  ntm bugs summary             # Show bug counts summary
  ntm bugs watch               # Route new findings to reservation holders`,
	}

	cmd.AddCommand(
		newBugsListCmd(),
		newBugsNotifyCmd(),
		newBugsSummaryCmd(),
		newBugsWatchCmd(),
	)

	return cmd
}

func newBugsListCmd() *cobra.Command {
	var (
		severity string
		limit    int
		showAll  bool
	)

	cmd := &cobra.Command{
		Use:   "list [path]",
		Short: "List recent UBS scan findings",
		Long: `List findings from the most recent UBS scan.

If no cached scan results exist, runs a quick scan on the specified path.

Examples:
  ntm bugs list                      # List from cache or scan current dir
  ntm bugs list --severity=critical  # Only critical issues
  ntm bugs list --all                # Include info-level findings
  ntm bugs list src/                 # Scan and list for specific path`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}

			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}

			// Try to load cached results first
			result, err := loadCachedScanResult(absPath)
			if err != nil || result == nil {
				// Run a fresh scan
				if !scanner.IsAvailable() {
					if jsonOutput {
						cause := scanner.ErrNotInstalled
						response := robot.NewErrorResponse(
							cause,
							robot.ErrCodeDependencyMissing,
							"Install UBS from https://github.com/nightowlai/ubs, then rerun 'ntm bugs list --json'",
						)
						response.OutputFormat = string(robot.FormatJSON)
						response.Meta = robot.NewResponseMeta("bugs list").WithExitCode(1)
						return emitJSONFailureEnvelopeWithCause(struct {
							robot.RobotResponse
							Available bool `json:"available"`
						}{
							RobotResponse: response,
							Available:     false,
						}, cause)
					}
					fmt.Println("UBS not installed. Install: https://github.com/nightowlai/ubs")
					return nil
				}

				s, scanErr := scanner.NewScannerWithConfig(&cfg.Scanner)
				if scanErr != nil {
					return fmt.Errorf("creating scanner: %w", scanErr)
				}

				if !jsonOutput {
					fmt.Printf("Scanning %s...\n", path)
				}

				result, err = s.Scan(context.Background(), absPath, scanner.DefaultOptions())
				if err != nil {
					return fmt.Errorf("scan failed: %w", err)
				}

				// Cache the results
				if err := saveCachedScanResult(absPath, result); err != nil {
					// Log but don't fail
					fmt.Fprintf(os.Stderr, "Warning: failed to cache scan results: %v\n", err)
				}
			}

			// Filter by severity if requested
			var findings []scanner.Finding
			if severity != "" {
				targetSeverity := scanner.Severity(strings.ToLower(severity))
				for _, f := range result.Findings {
					if f.Severity == targetSeverity {
						findings = append(findings, f)
					}
				}
			} else if !showAll {
				// By default, show only critical and warning
				for _, f := range result.Findings {
					if f.Severity == scanner.SeverityCritical || f.Severity == scanner.SeverityWarning {
						findings = append(findings, f)
					}
				}
			} else {
				findings = result.Findings
			}

			// Apply limit
			if limit > 0 && len(findings) > limit {
				findings = findings[:limit]
			}

			// JSON output
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"findings": findings,
					"totals":   result.Totals,
					"path":     absPath,
				})
			}

			// Text output
			t := theme.Current()
			if len(findings) == 0 {
				fmt.Printf("%s\u2713%s No issues found\n", colorize(t.Success), "\033[0m")
				return nil
			}

			fmt.Printf("\n%sBug Findings%s (%d total)\n", "\033[1m", "\033[0m", len(findings))
			fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2550", 50), "\033[0m")

			for i, f := range findings {
				icon := "\u26A0"
				color := colorize(t.Warning)
				switch f.Severity {
				case scanner.SeverityCritical:
					icon = "\u2717"
					color = colorize(t.Error)
				case scanner.SeverityInfo:
					icon = "\u2139"
					color = colorize(t.Info)
				}

				fmt.Printf("%s%s%s %s\n", color, icon, "\033[0m", f.Message)
				fmt.Printf("   %s%s:%d:%d%s\n", "\033[2m", f.File, f.Line, f.Column, "\033[0m")
				if f.Suggestion != "" {
					fmt.Printf("   \U0001F4A1 %s\n", f.Suggestion)
				}

				if i < len(findings)-1 {
					fmt.Println()
				}
			}

			// Summary
			fmt.Printf("\n%s%s%s\n", "\033[2m", strings.Repeat("\u2500", 50), "\033[0m")
			fmt.Printf("Critical: %d  Warning: %d  Info: %d\n",
				result.Totals.Critical, result.Totals.Warning, result.Totals.Info)

			return nil
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (critical, warning, info)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum findings to show")
	cmd.Flags().BoolVar(&showAll, "all", false, "Show all findings including info")

	return cmd
}

func newBugsNotifyCmd() *cobra.Command {
	var (
		projectKey string
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "notify [path]",
		Short: "Notify agents about UBS findings via Agent Mail",
		Long: `Send UBS scan findings to relevant agents via Agent Mail.

Agents are matched by their file reservations - if an agent holds a lock
on files with findings, they receive a targeted notification.

Examples:
  ntm bugs notify              # Notify based on current dir scan
  ntm bugs notify --dry-run    # Show what would be sent
  ntm bugs notify src/         # Notify based on src/ scan`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}

			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}

			if projectKey == "" {
				projectKey = absPath
			}

			// Check UBS availability
			if !scanner.IsAvailable() {
				if jsonOutput {
					cause := scanner.ErrNotInstalled
					return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
						"success":   false,
						"error":     cause.Error(),
						"available": false,
					}, cause)
				}
				fmt.Println("UBS not installed. Install: https://github.com/nightowlai/ubs")
				return nil
			}

			// Load cached results or run fresh scan
			result, err := loadCachedScanResult(absPath)
			if err != nil || result == nil {
				s, scanErr := scanner.NewScannerWithConfig(&cfg.Scanner)
				if scanErr != nil {
					return fmt.Errorf("creating scanner: %w", scanErr)
				}

				if !jsonOutput {
					fmt.Printf("Scanning %s...\n", path)
				}

				result, err = s.Scan(context.Background(), absPath, scanner.DefaultOptions())
				if err != nil {
					return fmt.Errorf("scan failed: %w", err)
				}

				// Cache the results
				if cacheErr := saveCachedScanResult(absPath, result); cacheErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to cache scan results: %v\n", cacheErr)
				}
			}

			// Check if there are findings worth notifying about
			if !result.HasCritical() && !result.HasWarning() {
				if jsonOutput {
					return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
						"notified":  false,
						"reason":    "no critical or warning findings",
						"totals":    result.Totals,
						"success":   true,
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					})
				}
				fmt.Println("\u2713 No critical or warning findings to notify about")
				return nil
			}

			if dryRun {
				if jsonOutput {
					return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
						"dry_run":    true,
						"would_send": true,
						"critical":   result.Totals.Critical,
						"warning":    result.Totals.Warning,
						"path":       absPath,
						"success":    true,
						"timestamp":  time.Now().UTC().Format(time.RFC3339),
					})
				}
				fmt.Printf("Would notify agents about:\n")
				fmt.Printf("  - %d critical issues\n", result.Totals.Critical)
				fmt.Printf("  - %d warning issues\n", result.Totals.Warning)
				fmt.Printf("  Path: %s\n", absPath)
				return nil
			}

			// Send notifications
			ctx := context.Background()
			if err := scanner.NotifyScanResults(ctx, result, projectKey); err != nil {
				if jsonOutput {
					return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
						"success":   false,
						"error":     err.Error(),
						"timestamp": time.Now().UTC().Format(time.RFC3339),
					}, err)
				}
				return fmt.Errorf("notification failed: %w", err)
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"success":   true,
					"notified":  true,
					"critical":  result.Totals.Critical,
					"warning":   result.Totals.Warning,
					"path":      absPath,
					"timestamp": time.Now().UTC().Format(time.RFC3339),
				})
			}

			fmt.Printf("\u2713 Notified agents about %d critical, %d warning findings\n",
				result.Totals.Critical, result.Totals.Warning)
			return nil
		},
	}

	cmd.Flags().StringVar(&projectKey, "project", "", "Project key for Agent Mail (default: scan path)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be sent without sending")

	return cmd
}

func newBugsSummaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary [path]",
		Short: "Show bug counts summary",
		Long: `Show a summary of bug counts from the most recent scan.

Examples:
  ntm bugs summary         # Summary for current dir
  ntm bugs summary src/    # Summary for specific path`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}

			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}

			// Try to load cached results
			result, err := loadCachedScanResult(absPath)
			if err != nil || result == nil {
				if jsonOutput {
					cause := err
					if cause == nil {
						cause = fmt.Errorf("no cached scan results")
					}
					return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
						"success":   false,
						"available": false,
						"error":     cause.Error(),
						"message":   "no cached scan results, run 'ntm scan' or 'ntm bugs list' first",
					}, cause)
				}
				fmt.Println("No cached scan results. Run 'ntm scan' or 'ntm bugs list' first.")
				return nil
			}

			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
					"path":      absPath,
					"totals":    result.Totals,
					"healthy":   result.IsHealthy(),
					"timestamp": result.Timestamp,
				})
			}

			t := theme.Current()
			fmt.Printf("\n%sBug Summary%s\n", "\033[1m", "\033[0m")
			fmt.Printf("%s%s%s\n\n", "\033[2m", strings.Repeat("\u2550", 40), "\033[0m")

			// Status icon
			if result.IsHealthy() {
				fmt.Printf("%s\u2713%s Healthy - no critical or warning issues\n\n", colorize(t.Success), "\033[0m")
			} else if result.HasCritical() {
				fmt.Printf("%s\u2717%s Critical issues found\n\n", colorize(t.Error), "\033[0m")
			} else {
				fmt.Printf("%s\u26A0%s Warnings found\n\n", colorize(t.Warning), "\033[0m")
			}

			fmt.Printf("  Critical:  %s%d%s\n", colorize(t.Error), result.Totals.Critical, "\033[0m")
			fmt.Printf("  Warning:   %s%d%s\n", colorize(t.Warning), result.Totals.Warning, "\033[0m")
			fmt.Printf("  Info:      %s%d%s\n", colorize(t.Info), result.Totals.Info, "\033[0m")
			fmt.Printf("  Files:     %d\n", result.Totals.Files)
			fmt.Printf("\n  Scanned:   %s\n", result.Timestamp)

			return nil
		},
	}

	return cmd
}

// Scan result caching

const scanCacheFile = ".ntm/scan_cache.json"

type cachedScan struct {
	Path     string              `json:"path"`
	Result   *scanner.ScanResult `json:"result"`
	CachedAt time.Time           `json:"cached_at"`
}

func scanCachePath(projectPath string) string {
	// If path is a file, use its parent directory for cache
	fi, err := os.Stat(projectPath)
	if err == nil && !fi.IsDir() {
		projectPath = filepath.Dir(projectPath)
	}
	return filepath.Join(projectPath, scanCacheFile)
}

func loadCachedScanResult(projectPath string) (*scanner.ScanResult, error) {
	cachePath := scanCachePath(projectPath)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var cached cachedScan
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	// Expire cache after 1 hour
	if time.Since(cached.CachedAt) > time.Hour {
		return nil, fmt.Errorf("cache expired")
	}

	return cached.Result, nil
}

func saveCachedScanResult(projectPath string, result *scanner.ScanResult) error {
	cachePath := scanCachePath(projectPath)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return err
	}

	cached := cachedScan{
		Path:     projectPath,
		Result:   result,
		CachedAt: time.Now(),
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cachePath, data, 0644)
}
