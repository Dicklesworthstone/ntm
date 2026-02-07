package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/metrics"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func newMetricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "View and manage success metrics",
		Long: `View and manage success metrics for NTM orchestration.

Track progress against improvement targets:
  - API call counts per tool
  - Operation latencies
  - Blocked commands
  - File conflicts

Subcommands:
  show     Display current metrics
  compare  Compare against baseline or snapshot
  export   Export metrics data

Examples:
  ntm metrics show                 # Current metrics summary
  ntm metrics show --session proj  # Metrics for specific session
  ntm metrics compare baseline     # Compare against baseline
  ntm metrics export --format csv  # Export as CSV`,
	}

	cmd.AddCommand(newMetricsShowCmd())
	cmd.AddCommand(newMetricsCompareCmd())
	cmd.AddCommand(newMetricsExportCmd())
	cmd.AddCommand(newMetricsSnapshotCmd())

	return cmd
}

func newMetricsShowCmd() *cobra.Command {
	var sessionID string
	var showTargets bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display current metrics",
		Long: `Display current success metrics including API calls, latencies, and targets.

Metrics tracked:
  - API call counts per tool/operation
  - Operation latency statistics (min, max, avg, percentiles)
  - Blocked command incidents
  - File reservation conflicts

Use --targets to see comparison against improvement targets.

Examples:
  ntm metrics show                  # All metrics
  ntm metrics show --session proj   # Session-specific metrics
  ntm metrics show --targets        # Include target comparison`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMetricsShow(sessionID, showTargets)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "filter to specific session")
	cmd.Flags().BoolVar(&showTargets, "targets", false, "show comparison against improvement targets")

	return cmd
}

func newMetricsCompareCmd() *cobra.Command {
	var sessionID string
	var baselineName string

	cmd := &cobra.Command{
		Use:   "compare [snapshot-name]",
		Short: "Compare metrics against baseline or snapshot",
		Long: `Compare current metrics against a saved snapshot.

Use 'ntm metrics snapshot save <name>' to create snapshots.
The special name 'baseline' compares against Tier 0 baselines.

Examples:
  ntm metrics compare baseline      # Compare against improvement baseline
  ntm metrics compare before-refactor  # Compare against saved snapshot`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "baseline"
			if len(args) > 0 {
				name = args[0]
			}
			return runMetricsCompare(sessionID, name)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "filter to specific session")
	cmd.Flags().StringVar(&baselineName, "baseline", "baseline", "baseline snapshot name")

	return cmd
}

func newMetricsExportCmd() *cobra.Command {
	var sessionID string
	var format string
	var outputFile string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export metrics data",
		Long: `Export metrics data in various formats for analysis.

Formats:
  json        Full metrics report as JSON (default)
  csv         Latency data as CSV
  prometheus  Prometheus exposition format

Examples:
  ntm metrics export                             # JSON to stdout
  ntm metrics export --format csv                # CSV to stdout
  ntm metrics export --format prometheus         # Prometheus to stdout
  ntm metrics export -o metrics.json             # JSON to file
  ntm metrics export --format prometheus -o m.prom`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMetricsExport(sessionID, format, outputFile)
		},
	}

	cmd.Flags().StringVar(&sessionID, "session", "", "filter to specific session")
	cmd.Flags().StringVarP(&format, "format", "f", "json", "output format: json, csv, prometheus")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "output file (default: stdout)")

	return cmd
}

func newMetricsSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage metrics snapshots",
	}

	var sessionID string

	saveCmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save current metrics as a named snapshot",
		Long: `Save the current metrics state as a named snapshot for later comparison.

Examples:
  ntm metrics snapshot save before-refactor
  ntm metrics snapshot save week-1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMetricsSnapshotSave(sessionID, args[0])
		},
	}
	saveCmd.Flags().StringVar(&sessionID, "session", "", "session to snapshot")
	cmd.AddCommand(saveCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List saved snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMetricsSnapshotList(sessionID)
		},
	}
	listCmd.Flags().StringVar(&sessionID, "session", "", "filter by session")
	cmd.AddCommand(listCmd)

	return cmd
}

// runMetricsShow displays current metrics.
func runMetricsShow(sessionID string, showTargets bool) error {
	store, collector, err := getMetricsCollector(sessionID)
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	if collector != nil {
		defer collector.Close()
	}

	report, err := collector.GenerateReport()
	if err != nil {
		return fmt.Errorf("generating report: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(report)
	}

	// Text output
	fmt.Printf("NTM Success Metrics - %s\n", report.SessionID)
	fmt.Println(strings.Repeat("=", 50))

	// API Calls
	if len(report.APICallCounts) > 0 {
		fmt.Printf("\nAPI Calls:\n")
		for key, count := range report.APICallCounts {
			fmt.Printf("  %-30s %d\n", key, count)
		}
	}

	// Latency Statistics
	if len(report.LatencyStats) > 0 {
		fmt.Printf("\nLatency Statistics:\n")
		for op, stats := range report.LatencyStats {
			fmt.Printf("  %s:\n", op)
			fmt.Printf("    Count: %d, Avg: %.1fms, P50: %.1fms, P95: %.1fms, P99: %.1fms\n",
				stats.Count, stats.AvgMs, stats.P50Ms, stats.P95Ms, stats.P99Ms)
		}
	}

	// Incidents
	fmt.Printf("\nIncidents:\n")
	fmt.Printf("  Blocked Commands: %d\n", report.BlockedCommands)
	fmt.Printf("  File Conflicts:   %d\n", report.FileConflicts)

	// Target Comparison
	if showTargets && len(report.TargetComparison) > 0 {
		fmt.Printf("\nTarget Comparison:\n")
		for _, tc := range report.TargetComparison {
			statusIcon := "✓"
			if tc.Status != "met" {
				statusIcon = "✗"
			}
			fmt.Printf("  %s %-30s current: %.1f, target: %.1f (%s)\n",
				statusIcon, tc.Metric, tc.Current, tc.Target, tc.Status)
		}
	}

	return nil
}

// runMetricsCompare compares current metrics against a baseline.
func runMetricsCompare(sessionID, baselineName string) error {
	store, collector, err := getMetricsCollector(sessionID)
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	if collector != nil {
		defer collector.Close()
	}

	currentReport, err := collector.GenerateReport()
	if err != nil {
		return fmt.Errorf("generating current report: %w", err)
	}

	var baselineReport *metrics.MetricsReport

	if baselineName == "baseline" {
		// Use hardcoded baselines
		baselineReport = &metrics.MetricsReport{
			SessionID:       "baseline",
			BlockedCommands: 0,
			FileConflicts:   0,
			LatencyStats: map[string]metrics.LatencyStats{
				"cm_query": {AvgMs: 500},
			},
		}
	} else {
		// Load saved snapshot
		baselineReport, err = collector.LoadSnapshot(baselineName)
		if err != nil {
			return fmt.Errorf("loading snapshot '%s': %w", baselineName, err)
		}
	}

	comparison := collector.CompareSnapshots(baselineReport, currentReport)

	if IsJSONOutput() {
		return output.PrintJSON(comparison)
	}

	// Text output
	fmt.Printf("Metrics Comparison: %s vs Current\n", baselineName)
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Baseline: %s\n", comparison.BaselineTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Current:  %s\n", comparison.CurrentTime.Format("2006-01-02 15:04:05"))

	if len(comparison.Improvements) > 0 {
		fmt.Printf("\nImprovements:\n")
		for _, imp := range comparison.Improvements {
			fmt.Printf("  ✓ %s\n", imp)
		}
	}

	if len(comparison.Regressions) > 0 {
		fmt.Printf("\nRegressions:\n")
		for _, reg := range comparison.Regressions {
			fmt.Printf("  ✗ %s\n", reg)
		}
	}

	if len(comparison.Improvements) == 0 && len(comparison.Regressions) == 0 {
		fmt.Printf("\nNo significant changes detected.\n")
	}

	return nil
}

// runMetricsExport exports metrics in the specified format.
func runMetricsExport(sessionID, format, outputFile string) error {
	store, collector, err := getMetricsCollector(sessionID)
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	if collector != nil {
		defer collector.Close()
	}

	report, err := collector.GenerateReport()
	if err != nil {
		return fmt.Errorf("generating report: %w", err)
	}

	var out *os.File
	if outputFile != "" {
		out, err = os.Create(outputFile)
		if err != nil {
			return fmt.Errorf("creating output file: %w", err)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	switch format {
	case "csv":
		return exportCSV(out, report)
	case "prometheus", "prom":
		_, err = fmt.Fprint(out, report.ExportPrometheus())
		return err
	case "json":
		fallthrough
	default:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
}

// exportCSV exports latency data as CSV.
func exportCSV(out *os.File, report *metrics.MetricsReport) error {
	w := csv.NewWriter(out)
	defer w.Flush()

	// Header
	if err := w.Write([]string{"operation", "count", "min_ms", "max_ms", "avg_ms", "p50_ms", "p95_ms", "p99_ms"}); err != nil {
		return err
	}

	// Data rows
	for op, stats := range report.LatencyStats {
		row := []string{
			op,
			fmt.Sprintf("%d", stats.Count),
			fmt.Sprintf("%.2f", stats.MinMs),
			fmt.Sprintf("%.2f", stats.MaxMs),
			fmt.Sprintf("%.2f", stats.AvgMs),
			fmt.Sprintf("%.2f", stats.P50Ms),
			fmt.Sprintf("%.2f", stats.P95Ms),
			fmt.Sprintf("%.2f", stats.P99Ms),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return nil
}

// runMetricsSnapshotSave saves current metrics as a named snapshot.
func runMetricsSnapshotSave(sessionID, name string) error {
	store, collector, err := getMetricsCollector(sessionID)
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	if collector != nil {
		defer collector.Close()
	}

	if err := collector.SaveSnapshot(name); err != nil {
		return fmt.Errorf("saving snapshot: %w", err)
	}

	if IsJSONOutput() {
		return output.PrintJSON(map[string]interface{}{
			"success": true,
			"name":    name,
			"message": fmt.Sprintf("Snapshot '%s' saved", name),
		})
	}

	fmt.Printf("Snapshot '%s' saved successfully\n", name)
	return nil
}

// runMetricsSnapshotList lists saved snapshots.
func runMetricsSnapshotList(sessionID string) error {
	// For now, just indicate this feature needs the database
	if IsJSONOutput() {
		return output.PrintJSON(map[string]interface{}{
			"snapshots": []string{},
			"message":   "Snapshot listing requires active session with state store",
		})
	}

	fmt.Println("Saved snapshots:")
	fmt.Println("  (requires active session with state store)")
	return nil
}

// getMetricsCollector returns a metrics collector for the given session.
func getMetricsCollector(sessionID string) (*state.Store, *metrics.Collector, error) {
	if sessionID == "" {
		sessionID = "default"
	}

	// Try to open the state store
	store, err := state.Open("")
	if err != nil {
		// Return a collector without persistence
		collector := metrics.NewCollector(nil, sessionID)
		return nil, collector, nil
	}

	// Ensure migrations are applied
	if err := store.Migrate(); err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("applying migrations: %w", err)
	}

	collector := metrics.NewCollector(store, sessionID)
	return store, collector, nil
}
