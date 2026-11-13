package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// UBSScanOptions configures --robot-ubs-scan.
type UBSScanOptions struct {
	Path             string
	File             string
	Staged           bool
	Diff             bool
	Languages        []string
	ExcludeLanguages []string
	Timeout          time.Duration
	FailOnWarning    bool
	Limit            int
	Severity         string
	FilterFile       string
}

// UBSScanSummary summarizes totals and health for --robot-ubs-scan.
type UBSScanSummary struct {
	Files          int   `json:"files"`
	Critical       int   `json:"critical"`
	Warning        int   `json:"warning"`
	Info           int   `json:"info"`
	TotalIssues    int   `json:"total_issues"`
	Healthy        bool  `json:"healthy"`
	NothingScanned bool  `json:"nothing_scanned,omitempty"`
	DurationMs     int64 `json:"duration_ms"`
}

// UBSScanOutput is the structured robot envelope for --robot-ubs-scan.
type UBSScanOutput struct {
	RobotResponse
	Mode          string                       `json:"mode"`
	Target        string                       `json:"target"`
	Findings      []scanner.Finding            `json:"findings"`
	Summary       UBSScanSummary               `json:"summary"`
	TriagePreview []scanner.PrioritizedFinding `json:"triage_preview"`
	Warnings      []string                     `json:"warnings,omitempty"`
}

const defaultUBSTriagePreviewLimit = 10

// GetUBSScan executes a full or incremental UBS scan and returns a structured robot envelope.
func GetUBSScan(ctx context.Context, opts UBSScanOptions) (*UBSScanOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	meta, finish := StartResponseMeta("robot-ubs-scan")
	defer finish()

	fileTarget := strings.TrimSpace(opts.File)
	pathTarget := strings.TrimSpace(opts.Path)
	severityFilter := strings.ToLower(strings.TrimSpace(opts.Severity))
	filterFile := strings.TrimSpace(opts.FilterFile)

	emptyEnvelope := func(mode, target string) *UBSScanOutput {
		return &UBSScanOutput{
			Mode:          mode,
			Target:        target,
			Findings:      make([]scanner.Finding, 0),
			TriagePreview: make([]scanner.PrioritizedFinding, 0),
		}
	}

	if severityFilter != "" &&
		severityFilter != string(scanner.SeverityCritical) &&
		severityFilter != string(scanner.SeverityWarning) &&
		severityFilter != string(scanner.SeverityInfo) {
		out := emptyEnvelope("invalid", pathTarget)
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid scan severity filter %q", opts.Severity),
			ErrCodeInvalidFlag,
			"Use critical, warning, or info when filtering UBS scan findings by severity.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}

	if opts.Staged && opts.Diff {
		out := emptyEnvelope("invalid", pathTarget)
		out.RobotResponse = NewErrorResponse(
			errors.New("--scan-staged and --scan-diff are mutually exclusive"),
			ErrCodeInvalidFlag,
			"Choose at most one incremental scan mode: --scan-staged, --scan-diff, or --scan-file.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}
	if fileTarget != "" && (opts.Staged || opts.Diff) {
		out := emptyEnvelope("invalid", fileTarget)
		out.RobotResponse = NewErrorResponse(
			errors.New("--scan-file cannot be combined with --scan-staged or --scan-diff"),
			ErrCodeInvalidFlag,
			"Use --scan-file alone for a single file, or --scan-staged/--scan-diff on a directory.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}
	if fileTarget != "" && pathTarget != "" {
		out := emptyEnvelope("invalid", fileTarget)
		out.RobotResponse = NewErrorResponse(
			errors.New("--scan-file and --scan-path cannot both be set"),
			ErrCodeInvalidFlag,
			"Pass --scan-file for a single file or --scan-path for a directory.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}

	rawTarget := pathTarget
	mode := "directory"
	switch {
	case fileTarget != "":
		rawTarget = fileTarget
		mode = "file"
	case opts.Staged:
		mode = "staged"
	case opts.Diff:
		mode = "diff"
	}
	if rawTarget == "" {
		rawTarget = "."
	}

	absTarget, err := filepath.Abs(rawTarget)
	if err != nil {
		out := emptyEnvelope(mode, rawTarget)
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("resolving scan target %q: %w", rawTarget, err),
			ErrCodeInvalidFlag,
			"Provide a valid filesystem path.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}

	if fileTarget != "" {
		info, statErr := os.Stat(absTarget)
		if statErr != nil {
			out := emptyEnvelope("file", absTarget)
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("--scan-file target %q: %w", absTarget, statErr),
				ErrCodeInvalidFlag,
				"Provide an existing file path for --scan-file.",
			)
			out.Meta = meta.WithExitCode(1)
			return out, nil
		}
		if info.IsDir() {
			out := emptyEnvelope("file", absTarget)
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("--scan-file target %q is a directory, not a file", absTarget),
				ErrCodeInvalidFlag,
				"Use --scan-path for directories or pass a regular file to --scan-file.",
			)
			out.Meta = meta.WithExitCode(1)
			return out, nil
		}
	} else if mode == "directory" {
		if info, statErr := os.Stat(absTarget); statErr == nil && !info.IsDir() {
			mode = "file"
		}
	}

	sc, err := scanner.New()
	if err != nil {
		out := emptyEnvelope(mode, absTarget)
		if errors.Is(err, scanner.ErrNotInstalled) {
			out.RobotResponse = NewErrorResponse(
				err,
				ErrCodeNotImplemented,
				"Install UBS (ubs) on PATH to enable --robot-ubs-scan.",
			)
			out.Meta = meta.WithExitCode(ExitCodeForResponse(out.RobotResponse))
			return out, nil
		}
		out.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Verify UBS installation and permissions.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}

	hasModifiers := len(opts.Languages) > 0 ||
		len(opts.ExcludeLanguages) > 0 ||
		opts.Timeout > 0 ||
		opts.FailOnWarning

	var result *scanner.ScanResult
	if !hasModifiers {
		switch mode {
		case "file":
			result, err = sc.ScanFile(ctx, absTarget)
		case "staged":
			result, err = sc.ScanStaged(ctx, absTarget)
		case "diff":
			result, err = sc.ScanDiff(ctx, absTarget)
		default:
			result, err = sc.ScanDirectory(ctx, absTarget)
		}
	} else {
		scanOpts := scanner.DefaultOptions()
		scanOpts.Languages = opts.Languages
		scanOpts.ExcludeLanguages = opts.ExcludeLanguages
		scanOpts.FailOnWarning = opts.FailOnWarning
		scanOpts.StagedOnly = mode == "staged"
		scanOpts.DiffOnly = mode == "diff"
		if opts.Timeout > 0 {
			scanOpts.Timeout = opts.Timeout
		}
		result, err = sc.Scan(ctx, absTarget, scanOpts)
	}

	if err != nil {
		out := emptyEnvelope(mode, absTarget)
		errCode := ErrCodeInternalError
		if errors.Is(err, scanner.ErrTimeout) {
			errCode = ErrCodeTimeout
		}
		out.RobotResponse = NewErrorResponse(
			err,
			errCode,
			"Inspect UBS stderr or narrow the scan scope with --scan-staged, --scan-diff, or --scan-file.",
		)
		out.Meta = meta.WithExitCode(1)
		return out, nil
	}

	if filterFile != "" || severityFilter != "" {
		filtered := *result
		if filterFile != "" {
			filtered.Findings = filtered.FilterByFile(filterFile)
		}
		if severityFilter != "" {
			filtered.Findings = filtered.FilterBySeverity(scanner.Severity(severityFilter))
		}
		var crit, warn, info int
		files := make(map[string]struct{})
		for _, f := range filtered.Findings {
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
		filtered.Totals.Critical = crit
		filtered.Totals.Warning = warn
		filtered.Totals.Info = info
		if filterFile != "" {
			filtered.Totals.Files = len(files)
		}
		result = &filtered
	}

	findings := result.Findings
	if findings == nil {
		findings = make([]scanner.Finding, 0)
	}

	previewLimit := opts.Limit
	if previewLimit <= 0 {
		previewLimit = defaultUBSTriagePreviewLimit
	}
	triagePreview := make([]scanner.PrioritizedFinding, 0)
	if len(findings) > 0 {
		if priorityReport, priErr := scanner.ComputePriorities(result, nil); priErr == nil && priorityReport != nil {
			items := priorityReport.Findings
			if len(items) > previewLimit {
				items = items[:previewLimit]
			}
			for i := range items {
				if items[i].Reasoning == nil {
					items[i].Reasoning = make([]string, 0)
				}
			}
			triagePreview = append(triagePreview, items...)
		}
	}

	summary := UBSScanSummary{
		Files:          result.Totals.Files,
		Critical:       result.Totals.Critical,
		Warning:        result.Totals.Warning,
		Info:           result.Totals.Info,
		TotalIssues:    result.TotalIssues(),
		Healthy:        result.IsHealthy(),
		NothingScanned: result.NothingScanned,
		DurationMs:     result.Duration.Milliseconds(),
	}

	out := &UBSScanOutput{
		RobotResponse: NewRobotResponseWithMeta(true, meta.WithExitCode(0)),
		Mode:          mode,
		Target:        absTarget,
		Findings:      findings,
		Summary:       summary,
		TriagePreview: triagePreview,
		Warnings:      result.Warnings,
	}

	if opts.FailOnWarning && (result.HasCritical() || result.HasWarning()) {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("ubs scan reported %d critical and %d warning finding(s)", result.Totals.Critical, result.Totals.Warning),
			ErrCodeInternalError,
			"Resolve the reported findings or omit --fail-on-warning for advisory inspection.",
		)
		out.Meta = meta.WithExitCode(1)
	}

	return out, nil
}

// PrintUBSScan emits the JSON robot report for --robot-ubs-scan.
func PrintUBSScan(ctx context.Context, opts UBSScanOptions) error {
	output, err := GetUBSScan(ctx, opts)
	if err != nil {
		return fmt.Errorf("ubs scan: %w", err)
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ubs scan failed")
}
