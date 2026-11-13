package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

type scanRunner interface {
	Scan(context.Context, string, scanner.ScanOptions) (*scanner.ScanResult, error)
}

type incrementalScanRunner interface {
	ScanFile(context.Context, string) (*scanner.ScanResult, error)
	ScanStaged(context.Context, string) (*scanner.ScanResult, error)
	ScanDiff(context.Context, string) (*scanner.ScanResult, error)
}

var (
	scanIsAvailable = scanner.IsAvailable
	newScanRunner   = func() (scanRunner, error) {
		if cfg == nil {
			return scanner.New()
		}
		return scanner.NewScannerWithConfig(&cfg.Scanner)
	}
	createBeadsFromScan = scanner.CreateBeadsFromFindings
	updateBeadsFromScan = scanner.UpdateBeadsFromFindings
	notifyScanResults   = scanner.NotifyScanResults
)

func newScanCmd() *cobra.Command {
	var (
		languages      []string
		exclude        []string
		ci             bool
		failOnWarning  bool
		verbose        bool
		timeoutSeconds int
		stagedOnly     bool
		diffOnly       bool
		fileTarget     string
		filterSeverity string
		filterFile     string
		createBeads    bool
		updateBeads    bool
		minSeverity    string
		dryRun         bool
		watch          bool
		notifyAgents   bool
		analyzeImpact  bool
		showHotspots   bool
		priorityReport bool
	)

	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Run UBS (Ultimate Bug Scanner) on your codebase",
		Long: `Run the Ultimate Bug Scanner (UBS) on files or directories.

UBS is a meta-scanner that detects bugs, anti-patterns, and security issues
across multiple languages (Go, JS/TS, Python, Rust, Java, Ruby, Swift, C/C++).

Watch Mode:
  Use --watch to monitor for file changes and automatically re-scan.
  This provides instant feedback as you fix issues.

Examples:
  ntm scan                   # Scan current directory
  ntm scan --watch           # Watch mode (auto-scan on change)
  ntm scan src/              # Scan specific directory
  ntm scan main.go           # Scan single file
  ntm scan --file main.go    # Scan explicit single file
  ntm scan --staged          # Scan only staged files
  ntm scan --diff            # Scan only modified files
  ntm scan --only=golang     # Restrict to Go files
  ntm scan --json            # Output JSON for automation
  ntm scan --fail-on-warning # Exit non-zero on warnings
  ntm scan --create-beads    # Auto-create beads from findings
  ntm scan --update-beads    # Close beads for fixed issues
  ntm scan --notify          # Notify agents via Agent Mail
  ntm scan --analyze-impact  # Show findings sorted by graph impact
  ntm scan --hotspots        # Show quality hotspots by file
  ntm scan --priority-report # Show smart priority report`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			timeoutChanged := cmd.Flags().Changed("timeout")
			defer func() {
				languages = nil
				exclude = nil
				ci = false
				failOnWarning = false
				verbose = false
				timeoutSeconds = 120
				stagedOnly = false
				diffOnly = false
				fileTarget = ""
				filterSeverity = ""
				filterFile = ""
				createBeads = false
				updateBeads = false
				minSeverity = "warning"
				dryRun = false
				watch = false
				notifyAgents = false
				analyzeImpact = false
				showHotspots = false
				priorityReport = false
			}()

			if stagedOnly && diffOnly {
				return fmt.Errorf("--staged and --diff are mutually exclusive")
			}
			trimmedFile := strings.TrimSpace(fileTarget)
			if trimmedFile != "" {
				if stagedOnly || diffOnly {
					return fmt.Errorf("--file cannot be combined with --staged or --diff")
				}
				if len(args) > 0 {
					return fmt.Errorf("--file cannot be combined with a positional [path] argument")
				}
			}

			path := "."
			if trimmedFile != "" {
				path = trimmedFile
			} else if len(args) > 0 {
				path = args[0]
			}

			// Resolve to absolute path
			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}
			if trimmedFile != "" {
				info, statErr := os.Stat(absPath)
				if statErr != nil {
					return fmt.Errorf("invalid --file %q: %w", trimmedFile, statErr)
				}
				if info.IsDir() {
					return fmt.Errorf("invalid --file %q: target is a directory", trimmedFile)
				}
			}

			if trimmedSev := strings.TrimSpace(filterSeverity); trimmedSev != "" && !isValidScanSeverity(trimmedSev) {
				return fmt.Errorf("invalid --severity %q: must be critical, warning, or info", filterSeverity)
			}

			var baseScannerCfg *config.ScannerConfig
			if cfg != nil {
				baseScannerCfg = &cfg.Scanner
			}
			scanCfg, err := config.LoadProjectScannerConfig(absPath, baseScannerCfg)
			if err != nil {
				return fmt.Errorf("loading scanner config: %w", err)
			}

			var timeout time.Duration
			if timeoutChanged {
				timeout = time.Duration(timeoutSeconds) * time.Second
			} else if trimmedFile == "" && !stagedOnly && !diffOnly {
				timeout = scanCfg.Defaults.GetTimeout()
			}

			opts := scanner.ScanOptions{
				Languages:        languages,
				ExcludeLanguages: exclude,
				CI:               ci,
				FailOnWarning:    failOnWarning,
				Verbose:          verbose,
				Timeout:          timeout,
				StagedOnly:       stagedOnly,
				DiffOnly:         diffOnly,
			}
			applyScannerToolsConfig(&opts, scanCfg)

			bridgeCfg := scanner.BridgeConfig{
				MinSeverity: parseSeverity(minSeverity),
				DryRun:      dryRun,
				Verbose:     verbose,
			}

			filters := scanOutputFilters{
				severity:   strings.TrimSpace(filterSeverity),
				filterFile: strings.TrimSpace(filterFile),
			}

			if watch {
				return runScanWatch(absPath, opts, scanCfg, filters, createBeads, updateBeads, notifyAgents, bridgeCfg)
			}

			return runScanWithConfig(absPath, opts, scanCfg, filters, createBeads, updateBeads, notifyAgents, bridgeCfg, analyzeImpact, showHotspots, priorityReport)
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
	cmd.Flags().StringVar(&fileTarget, "file", "", "Scan a single file")
	cmd.Flags().StringVar(&filterSeverity, "severity", "", "Filter findings by severity (critical, warning, info)")
	cmd.Flags().StringVar(&filterFile, "filter-file", "", "Filter findings by file path or substring")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for file changes and re-scan")
	cmd.Flags().BoolVar(&notifyAgents, "notify", false, "Notify agents via Agent Mail about findings")

	// Beads integration flags
	cmd.Flags().BoolVar(&createBeads, "create-beads", false, "Auto-create beads from scan findings")
	cmd.Flags().BoolVar(&updateBeads, "update-beads", false, "Close beads for findings no longer present")
	cmd.Flags().StringVar(&minSeverity, "min-severity", "warning", "Minimum severity for bead creation (critical|warning|info)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what beads would be created without creating them")

	// Analysis flags
	cmd.Flags().BoolVar(&analyzeImpact, "analyze-impact", false, "Show findings sorted by dependency graph impact")
	cmd.Flags().BoolVar(&showHotspots, "hotspots", false, "Show quality hotspots by file")
	cmd.Flags().BoolVar(&priorityReport, "priority-report", false, "Show smart priority report combining severity and graph position")

	return cmd
}

type scanOutputFilters struct {
	severity   string
	filterFile string
}

func isValidScanSeverity(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "warning", "info":
		return true
	default:
		return false
	}
}

func applyScannerToolsConfig(opts *scanner.ScanOptions, scanCfg *config.ScannerConfig) {
	if opts == nil || scanCfg == nil {
		return
	}
	hasToolRules := len(scanCfg.Tools.Enabled) > 0 || len(scanCfg.Tools.Disabled) > 0
	if len(opts.Languages) == 0 && len(scanCfg.Defaults.Languages) > 0 {
		for _, lang := range scanCfg.Defaults.Languages {
			if scanCfg.Tools.IsToolEnabled(lang) {
				opts.Languages = append(opts.Languages, lang)
			}
		}
		return
	}
	if !hasToolRules {
		return
	}
	if len(opts.Languages) > 0 {
		filtered := make([]string, 0, len(opts.Languages))
		for _, lang := range opts.Languages {
			if scanCfg.Tools.IsToolEnabled(lang) {
				filtered = append(filtered, lang)
			}
		}
		opts.Languages = filtered
		return
	}
	if len(scanCfg.Tools.Enabled) > 0 {
		for _, tool := range scanCfg.Tools.Enabled {
			if scanCfg.Tools.IsToolEnabled(tool) {
				opts.Languages = append(opts.Languages, tool)
			}
		}
		return
	}
	existing := make(map[string]struct{}, len(opts.ExcludeLanguages))
	for _, ex := range opts.ExcludeLanguages {
		existing[strings.ToLower(ex)] = struct{}{}
	}
	for _, disabled := range scanCfg.Tools.Disabled {
		if !scanCfg.Tools.IsToolEnabled(disabled) {
			key := strings.ToLower(disabled)
			if _, ok := existing[key]; !ok {
				existing[key] = struct{}{}
				opts.ExcludeLanguages = append(opts.ExcludeLanguages, disabled)
			}
		}
	}
}

func applyScanFindingFilters(result *scanner.ScanResult, scanCfg *config.ScannerConfig, filters scanOutputFilters) *scanner.ScanResult {
	if result == nil {
		return nil
	}
	hasToolRules := scanCfg != nil && (len(scanCfg.Tools.Enabled) > 0 || len(scanCfg.Tools.Disabled) > 0)
	if !hasToolRules && filters.severity == "" && filters.filterFile == "" {
		return result
	}
	cloned := *result
	if hasToolRules && len(cloned.Scanners) > 0 {
		scanners := make([]scanner.ScannerResult, 0, len(cloned.Scanners))
		for _, sr := range cloned.Scanners {
			if scanCfg.Tools.IsToolEnabled(sr.Language) {
				scanners = append(scanners, sr)
			}
		}
		cloned.Scanners = scanners
	}
	if filters.filterFile != "" {
		cloned.Findings = cloned.FilterByFile(filters.filterFile)
	}
	if filters.severity != "" {
		cloned.Findings = cloned.FilterBySeverity(parseSeverity(filters.severity))
	}
	if filters.filterFile != "" || filters.severity != "" {
		if cloned.Findings == nil {
			cloned.Findings = make([]scanner.Finding, 0)
		}
		var crit, warn, info int
		files := make(map[string]struct{})
		for _, f := range cloned.Findings {
			switch f.Severity {
			case scanner.SeverityCritical:
				crit++
			case scanner.SeverityWarning:
				warn++
			case scanner.SeverityInfo:
				info++
			}
			if f.File != "" {
				files[f.File] = struct{}{}
			}
		}
		cloned.Totals.Critical = crit
		cloned.Totals.Warning = warn
		cloned.Totals.Info = info
		if filters.filterFile != "" {
			cloned.Totals.Files = len(files)
		}
	}
	return &cloned
}

// parseSeverity converts a string severity to scanner.Severity.
func parseSeverity(s string) scanner.Severity {
	switch strings.ToLower(s) {
	case "critical":
		return scanner.SeverityCritical
	case "warning":
		return scanner.SeverityWarning
	case "info":
		return scanner.SeverityInfo
	default:
		return scanner.SeverityWarning
	}
}

func hasCustomScanModifiers(opts scanner.ScanOptions) bool {
	return len(opts.Languages) > 0 ||
		len(opts.ExcludeLanguages) > 0 ||
		opts.CI ||
		opts.FailOnWarning ||
		opts.Verbose ||
		opts.Timeout > 0
}

func executeScanWithRunner(ctx context.Context, s scanRunner, path string, opts scanner.ScanOptions) (*scanner.ScanResult, error) {
	if inc, ok := s.(incrementalScanRunner); ok && !hasCustomScanModifiers(opts) {
		switch {
		case opts.StagedOnly:
			return inc.ScanStaged(ctx, path)
		case opts.DiffOnly:
			return inc.ScanDiff(ctx, path)
		default:
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return inc.ScanFile(ctx, path)
			}
		}
	}
	return s.Scan(ctx, path, opts)
}

func runScanWithConfig(path string, opts scanner.ScanOptions, scanCfg *config.ScannerConfig, filters scanOutputFilters, createBeads, updateBeads, notifyAgents bool, bridgeCfg scanner.BridgeConfig, analyzeImpact, showHotspots, priorityReport bool) error {
	t := theme.Current()

	// Check if UBS is available
	if !scanIsAvailable() {
		cause := scanner.ErrNotInstalled
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success":    false,
				"error":      "ubs not installed",
				"error_code": "DEPENDENCY_MISSING",
				"available":  false,
			}, cause)
		}
		fmt.Printf("%s✗%s UBS not installed\n", colorize(t.Error), "\033[0m")
		fmt.Printf("  Install: %shttps://github.com/nightowlai/ubs%s\n", "\033[2m", "\033[0m")
		return cause
	}

	// Create scanner
	s, err := newScanRunner()
	if err != nil {
		cause := fmt.Errorf("creating scanner: %w", err)
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success":    false,
				"error":      cause.Error(),
				"error_code": "SCAN_SETUP_FAILED",
			}, cause)
		}
		return cause
	}

	// Show scanning message if not JSON output
	if !jsonOutput {
		fmt.Printf("Scanning %s...\n", path)
	}

	// Run scan
	ctx := context.Background()
	result, err := executeScanWithRunner(ctx, s, path, opts)
	if err != nil {
		cause := fmt.Errorf("scan failed: %w", err)
		if jsonOutput {
			return emitJSONFailureEnvelopeWithCause(map[string]interface{}{
				"success":    false,
				"error":      cause.Error(),
				"error_code": "SCAN_FAILED",
			}, cause)
		}
		return cause
	}

	result = applyScanFindingFilters(result, scanCfg, filters)

	// Run beads bridge if requested
	var bridgeResult *scanner.BridgeResult
	var updateResult *scanner.BridgeResult
	var terminalFailures []error
	if createBeads && len(result.Findings) > 0 {
		bridgeResult, err = createBeadsFromScan(result, bridgeCfg)
		if err != nil {
			terminalFailures = append(terminalFailures, fmt.Errorf("creating beads: %w", err))
			if !jsonOutput {
				fmt.Printf("%s✗%s Beads creation failed: %v\n", colorize(t.Error), "\033[0m", err)
			}
		} else if bridgeResult != nil && bridgeResult.Errors > 0 {
			terminalFailures = append(terminalFailures, fmt.Errorf("creating beads: %d operation(s) failed", bridgeResult.Errors))
		}
	}
	if updateBeads {
		updateResult, err = updateBeadsFromScan(result, bridgeCfg)
		if err != nil {
			terminalFailures = append(terminalFailures, fmt.Errorf("updating beads: %w", err))
			if !jsonOutput {
				fmt.Printf("%s✗%s Beads update failed: %v\n", colorize(t.Error), "\033[0m", err)
			}
		} else if updateResult != nil && updateResult.Errors > 0 {
			terminalFailures = append(terminalFailures, fmt.Errorf("updating beads: %d operation(s) failed", updateResult.Errors))
		}
	}

	// Notify agents if requested
	if notifyAgents && (result.HasCritical() || result.HasWarning()) {
		if err := notifyScanResults(ctx, result, path); err != nil {
			terminalFailures = append(terminalFailures, fmt.Errorf("notifying agents: %w", err))
			if !jsonOutput {
				fmt.Printf("⚠ Notification failed: %v\n", err)
			}
		} else if !jsonOutput {
			fmt.Println("✓ Notified agents")
		}
	}

	thresholdTriggered := false
	if scanCfg != nil && opts.StagedOnly {
		if scanCfg.Thresholds.PreCommit.ShouldBlock(result.Totals.Critical, result.Totals.Warning) {
			terminalFailures = append(terminalFailures, errors.New("scan exceeded pre-commit threshold"))
			thresholdTriggered = true
		}
	} else if scanCfg != nil && opts.CI {
		if scanCfg.Thresholds.CI.ShouldFail(result.Totals.Critical, result.Totals.Warning) {
			terminalFailures = append(terminalFailures, errors.New("scan exceeded CI threshold"))
			thresholdTriggered = true
		}
	} else if result.HasCritical() {
		terminalFailures = append(terminalFailures, errors.New("scan found critical issues"))
		thresholdTriggered = true
	}
	if !thresholdTriggered && opts.FailOnWarning && result.HasWarning() {
		terminalFailures = append(terminalFailures, errors.New("scan found warnings and --fail-on-warning is set"))
	}

	// Output results
	if jsonOutput {
		output := map[string]interface{}{
			"success": len(terminalFailures) == 0,
			"scan":    result,
		}
		if bridgeResult != nil {
			output["beads_created"] = bridgeResult
		}
		if updateResult != nil {
			output["beads_updated"] = updateResult
		}

		// Add analysis data to JSON output if requested
		existingBeadIDs := make(map[string]string)
		if bridgeResult != nil {
			for i, f := range result.Findings {
				if i < len(bridgeResult.BeadIDs) {
					sig := scanner.FindingSignature(f)
					existingBeadIDs[sig] = bridgeResult.BeadIDs[i]
				}
			}
		}

		if analyzeImpact || showHotspots {
			analysis, err := scanner.AnalyzeImpact(result, existingBeadIDs)
			if err == nil {
				if analyzeImpact {
					output["impact_analysis"] = analysis
				}
				if showHotspots {
					output["hotspots"] = analysis.Hotspots
				}
			}
		}
		if priorityReport {
			report, err := scanner.ComputePriorities(result, existingBeadIDs)
			if err == nil {
				output["priority_report"] = report
			}
		}

		if len(terminalFailures) > 0 {
			cause := errors.Join(terminalFailures...)
			output["error"] = cause.Error()
			output["error_code"] = "SCAN_FAILED"
			return emitJSONFailureEnvelopeWithCause(output, cause)
		}
		return json.NewEncoder(os.Stdout).Encode(output)
	}

	// Text output
	printScanResults(t, result, opts.FailOnWarning)

	// Print beads bridge results
	if bridgeResult != nil {
		printBeadsBridgeResults(t, bridgeResult, bridgeCfg.DryRun)
	}
	if updateResult != nil {
		printBeadsUpdateResults(t, updateResult, bridgeCfg.DryRun)
	}

	// Build existing bead signatures for analysis
	existingBeadIDs := make(map[string]string)
	if bridgeResult != nil {
		for i, f := range result.Findings {
			if i < len(bridgeResult.BeadIDs) {
				sig := scanner.FindingSignature(f)
				existingBeadIDs[sig] = bridgeResult.BeadIDs[i]
			}
		}
	}

	// Run impact analysis if requested
	if analyzeImpact && len(result.Findings) > 0 {
		analysis, err := scanner.AnalyzeImpact(result, existingBeadIDs)
		if err != nil {
			fmt.Printf("⚠ Impact analysis failed: %v\n", err)
		} else {
			fmt.Println()
			fmt.Print(scanner.FormatImpactReport(analysis))
		}
	}

	// Show hotspots if requested
	if showHotspots && len(result.Findings) > 0 {
		analysis, err := scanner.AnalyzeImpact(result, existingBeadIDs)
		if err != nil {
			fmt.Printf("⚠ Hotspot analysis failed: %v\n", err)
		} else {
			fmt.Println()
			fmt.Println("Quality Hotspots")
			fmt.Println("════════════════════════════════════════════════════")
			for i, h := range analysis.Hotspots {
				if i >= 10 {
					fmt.Printf("  ... and %d more\n", len(analysis.Hotspots)-10)
					break
				}
				fmt.Printf("  %d. %s - %d findings (impact: %.1f)\n",
					i+1, h.File, h.FindingCount, h.ImpactScore)
			}
			fmt.Println()
		}
	}

	// Show priority report if requested
	if priorityReport && len(result.Findings) > 0 {
		report, err := scanner.ComputePriorities(result, existingBeadIDs)
		if err != nil {
			fmt.Printf("⚠ Priority report failed: %v\n", err)
		} else {
			fmt.Println()
			fmt.Print(scanner.FormatPriorityReport(report))
		}
	}

	return errors.Join(terminalFailures...)
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
	if result.NothingScanned {
		fmt.Printf("%s⚠%s Nothing scanned: ubs found no supported language here\n", colorize(t.Warning), "\033[0m")
	} else if result.IsHealthy() {
		fmt.Printf("%s✓%s No critical or warning issues found\n", colorize(t.Success), "\033[0m")
	} else if result.HasCritical() {
		fmt.Printf("%s✗%s Critical issues found - fix before committing\n", colorize(t.Error), "\033[0m")
	} else if result.HasWarning() {
		fmt.Printf("%s⚠%s Warnings found - consider reviewing\n", colorize(t.Warning), "\033[0m")
	}
	fmt.Println()
}

func printBeadsBridgeResults(t theme.Theme, br *scanner.BridgeResult, dryRun bool) {
	fmt.Println()
	if dryRun {
		fmt.Printf("%sBeads Bridge (Dry Run)%s\n", "\033[1m", "\033[0m")
	} else {
		fmt.Printf("%sBeads Bridge%s\n", "\033[1m", "\033[0m")
	}
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")

	if br.Created > 0 {
		if dryRun {
			fmt.Printf("  Would create:  %s%d%s beads\n", colorize(t.Success), br.Created, "\033[0m")
		} else {
			fmt.Printf("  Created:       %s%d%s beads\n", colorize(t.Success), br.Created, "\033[0m")
		}
	}
	if br.Duplicates > 0 {
		fmt.Printf("  Duplicates:    %d (skipped)\n", br.Duplicates)
	}
	if br.Skipped > 0 {
		fmt.Printf("  Below threshold: %d (skipped)\n", br.Skipped)
	}
	if br.Errors > 0 {
		fmt.Printf("  Errors:        %s%d%s\n", colorize(t.Error), br.Errors, "\033[0m")
	}

	// Show created bead IDs
	if len(br.BeadIDs) > 0 && !dryRun {
		fmt.Printf("\n  New beads: %s\n", strings.Join(br.BeadIDs, ", "))
	}

	// Show any messages
	for _, msg := range br.Messages {
		fmt.Printf("  %s\n", msg)
	}
	fmt.Println()
}

func printBeadsUpdateResults(t theme.Theme, br *scanner.BridgeResult, dryRun bool) {
	// Note: br.Created is reused for "closed" count in UpdateBeadsFromFindings
	if br.Created == 0 && br.Errors == 0 {
		return // Nothing to report
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("%sBeads Update (Dry Run)%s\n", "\033[1m", "\033[0m")
	} else {
		fmt.Printf("%sBeads Update%s\n", "\033[1m", "\033[0m")
	}
	fmt.Printf("%s───────────────────────────────────────────────────%s\n", "\033[2m", "\033[0m")

	for _, msg := range br.Messages {
		fmt.Printf("  %s\n", msg)
	}
	fmt.Println()
}

func runScanWatch(path string, opts scanner.ScanOptions, scanCfg *config.ScannerConfig, filters scanOutputFilters, createBeads, updateBeads, notifyAgents bool, bridgeCfg scanner.BridgeConfig) error {
	// Create watcher with debouncing (500ms)
	w, err := watcher.New(func(events []watcher.Event) {
		// Clear screen
		fmt.Print("\033[H\033[2J")
		fmt.Printf("Change detected, re-scanning %s...\n", path)

		// Run scan
		// Note: We ignore error here to keep watching. Analysis flags disabled in watch mode.
		if err := runScanWithConfig(path, opts, scanCfg, filters, createBeads, updateBeads, notifyAgents, bridgeCfg, false, false, false); err != nil {
			fmt.Printf("\nError running scan: %v\n", err)
		}
		fmt.Println("\nWaiting for changes... (Ctrl+C to stop)")
	},
		watcher.WithDebouncer(watcher.NewDebouncer(500*time.Millisecond)),
		watcher.WithRecursive(true),
		watcher.WithEventFilter(watcher.Write|watcher.Create|watcher.Remove|watcher.Rename),
		watcher.WithIgnorePaths([]string{
			"node_modules",
			".git",
			"__pycache__",
			".venv",
			"target",
			"dist",
			"build",
			"vendor",
		}),
	)

	if err != nil {
		return fmt.Errorf("creating watcher: %w", err)
	}
	defer w.Close()

	if err := w.Add(path); err != nil {
		return fmt.Errorf("adding path to watcher: %w", err)
	}

	// Run initial scan
	fmt.Print("\033[H\033[2J")
	fmt.Printf("Initial scan of %s...\n", path)
	if err := runScanWithConfig(path, opts, scanCfg, filters, createBeads, updateBeads, notifyAgents, bridgeCfg, false, false, false); err != nil {
		fmt.Printf("\nError running scan: %v\n", err)
	}
	fmt.Println("\nWaiting for changes... (Ctrl+C to stop)")

	// Block until interrupt
	select {}
}
