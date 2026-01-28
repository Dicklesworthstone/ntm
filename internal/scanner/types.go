// Package scanner provides a Go wrapper for UBS (Ultimate Bug Scanner) integration.
// It executes UBS scans and parses the output into structured Go types.
package scanner

import "time"

// Severity represents the severity level of a finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Finding represents a single issue found by UBS.
type Finding struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Column     int      `json:"column"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
	RuleID     string   `json:"rule_id,omitempty"`
}

// ScannerResult represents results from a single language scanner.
type ScannerResult struct {
	Language  string `json:"language"`
	Project   string `json:"project"`
	Files     int    `json:"files"`
	Critical  int    `json:"critical"`
	Warning   int    `json:"warning"`
	Info      int    `json:"info"`
	Timestamp string `json:"timestamp"`
}

// ScanTotals represents the aggregate totals from a scan.
type ScanTotals struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
	Files    int `json:"files"`
}

// ScanResult represents the complete output of a UBS scan.
type ScanResult struct {
	Project   string          `json:"project"`
	Timestamp string          `json:"timestamp"`
	Scanners  []ScannerResult `json:"scanners"`
	Totals    ScanTotals      `json:"totals"`
	Findings  []Finding       `json:"findings,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
	Duration  time.Duration   `json:"duration,omitempty"`
	ExitCode  int             `json:"exit_code"`
}

// ScanOptions configures a UBS scan.
type ScanOptions struct {
	// Languages to scan (empty = auto-detect)
	Languages []string
	// ExcludeLanguages to skip
	ExcludeLanguages []string
	// CI mode for stable output
	CI bool
	// FailOnWarning exits non-zero on warnings
	FailOnWarning bool
	// Verbose enables detailed output
	Verbose bool
	// Timeout for the scan (0 = no timeout)
	Timeout time.Duration
	// StagedOnly scans only staged files
	StagedOnly bool
	// DiffOnly scans only modified files
	DiffOnly bool
}

// DefaultOptions returns sensible default scan options.
func DefaultOptions() ScanOptions {
	return ScanOptions{
		Timeout: 60 * time.Second,
	}
}

// IsHealthy returns true if the scan found no critical or warning issues.
func (r *ScanResult) IsHealthy() bool {
	return r.Totals.Critical == 0 && r.Totals.Warning == 0
}

// HasCritical returns true if any critical issues were found.
func (r *ScanResult) HasCritical() bool {
	return r.Totals.Critical > 0
}

// HasWarning returns true if any warning issues were found.
func (r *ScanResult) HasWarning() bool {
	return r.Totals.Warning > 0
}

// TotalIssues returns the total number of issues found.
func (r *ScanResult) TotalIssues() int {
	return r.Totals.Critical + r.Totals.Warning + r.Totals.Info
}

// FilterBySeverity returns findings matching the given severity.
func (r *ScanResult) FilterBySeverity(severity Severity) []Finding {
	var filtered []Finding
	for _, f := range r.Findings {
		if f.Severity == severity {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// FilterByFile returns findings for a specific file.
func (r *ScanResult) FilterByFile(file string) []Finding {
	var filtered []Finding
	for _, f := range r.Findings {
		if f.File == file {
			filtered = append(filtered, f)
		}
	}
	return filtered
}
