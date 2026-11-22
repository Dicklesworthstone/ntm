package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Common errors returned by the scanner.
var (
	ErrNotInstalled   = errors.New("ubs is not installed")
	ErrTimeout        = errors.New("scan timed out")
	ErrScanFailed     = errors.New("scan failed")
	ErrOutputTooLarge = errors.New("scan output exceeded limit")
	ErrOutputNotJSON  = errors.New("scan output missing JSON")
	// ErrScanIncomplete reports a ubs run with no complete result: invalid
	// arguments, an environment error, a refused scan or a partial run (ubs
	// exit 2). It is never a clean scan.
	ErrScanIncomplete = errors.New("ubs scan incomplete")
)

// MaxScanOutputBytes limits the size of scan output to prevent OOM.
const MaxScanOutputBytes = 10 * 1024 * 1024

// Scanner wraps the UBS command-line tool.
type Scanner struct {
	binaryPath string
}

// boundedBuffer is an io.Writer that accumulates output up to limit bytes and
// records whether more was written. It never returns an error so that exec's
// internal stdout-copy goroutine is not aborted; the oversize condition is
// surfaced afterwards via the truncated flag.
type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining < len(p) {
		b.truncated = true
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
	} else {
		b.buf.Write(p)
	}
	return len(p), nil
}

// New creates a new Scanner instance.
// Returns an error if UBS is not installed.
func New() (*Scanner, error) {
	path, err := exec.LookPath("ubs")
	if err != nil {
		return nil, ErrNotInstalled
	}
	return &Scanner{binaryPath: path}, nil
}

// IsAvailable returns true if UBS is installed and accessible.
func IsAvailable() bool {
	_, err := exec.LookPath("ubs")
	return err == nil
}

// Version returns the UBS version string.
func (s *Scanner) Version() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.binaryPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("getting version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Scan runs UBS on the given path with the provided options.
func (s *Scanner) Scan(ctx context.Context, path string, opts ScanOptions) (*ScanResult, error) {
	args := s.buildArgs(path, opts)

	// Apply timeout if specified
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, s.binaryPath, args...)
	cmd.WaitDelay = 2 * time.Second

	// Capture stdout into a bounded buffer and stderr separately. Attaching
	// writers (rather than using StdoutPipe + a manual read) lets os/exec drain
	// stdout in its own goroutine and Wait only after that copy completes. This
	// avoids the "file already closed" race where the pipe was torn down (by
	// Wait/WaitDelay or process kill) while a read was still in flight, while
	// still enforcing the MaxScanOutputBytes memory cap.
	stdout := boundedBuffer{limit: MaxScanOutputBytes + 1}
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	waitErr := cmd.Run()
	duration := time.Since(startTime)
	output := stdout.buf.Bytes()

	// Check if output exceeded limit
	if stdout.truncated || len(output) > MaxScanOutputBytes {
		return nil, ErrOutputTooLarge
	}

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}

	// Parse the JSON output (capture warnings even if output is mixed)
	stderrWarnings := extractWarningLines(stderr.Bytes())
	result, warnings, parseErr := s.parseOutput(output)
	if len(stderrWarnings) > 0 {
		warnings = append(warnings, stderrWarnings...)
	}

	exitCode := 0
	if waitErr != nil {
		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			return nil, fmt.Errorf("running ubs: %w", waitErr)
		}
		exitCode = exitErr.ExitCode()
	}
	// ubs exit 2 (or a report it marks error/partial) is an environment
	// error, a refused scan or a partial run, never a result. Read as an empty
	// scan it passed the pre-commit hook and let --update-beads close every
	// open finding.
	if errors.Is(parseErr, ErrScanIncomplete) {
		return nil, parseErr
	}
	if exitCode == 2 || (result != nil && (result.Status == "error" || result.Status == "partial")) {
		detail := strings.Join(stderrWarnings, "; ")
		if detail == "" && result != nil {
			detail = "status " + result.Status
		}
		return nil, fmt.Errorf("%w (exit %d): %s", ErrScanIncomplete, exitCode, detail)
	}

	if parseErr != nil {
		// If we can't parse output but command succeeded, return basic result
		if waitErr == nil {
			if len(warnings) > 0 {
				return &ScanResult{
					Project:  path,
					Duration: duration,
					ExitCode: 0,
					Warnings: warnings,
				}, nil
			}
			return &ScanResult{
				Project:  path,
				Duration: duration,
				ExitCode: 0,
			}, nil
		}
		return nil, fmt.Errorf("parsing output: %w (stderr: %s)", parseErr, stderr.String())
	}

	if len(warnings) > 0 {
		result.Warnings = append(result.Warnings, warnings...)
	}

	result.Duration = duration
	result.ExitCode = exitCode
	// Exit 3: no supported language, so nothing was scanned. That is not a
	// clean scan either, and callers must not treat absent findings as fixed.
	if exitCode == 3 {
		result.NothingScanned = true
	}

	return result, nil
}

// ScanFile runs UBS on a single file.
func (s *Scanner) ScanFile(ctx context.Context, file string) (*ScanResult, error) {
	return s.Scan(ctx, file, DefaultOptions())
}

// ScanDirectory runs UBS on a directory.
func (s *Scanner) ScanDirectory(ctx context.Context, dir string) (*ScanResult, error) {
	return s.Scan(ctx, dir, DefaultOptions())
}

// ScanStaged runs UBS on staged files only.
func (s *Scanner) ScanStaged(ctx context.Context, dir string) (*ScanResult, error) {
	opts := DefaultOptions()
	opts.StagedOnly = true
	return s.Scan(ctx, dir, opts)
}

// ScanDiff runs UBS on modified files only.
func (s *Scanner) ScanDiff(ctx context.Context, dir string) (*ScanResult, error) {
	opts := DefaultOptions()
	opts.DiffOnly = true
	return s.Scan(ctx, dir, opts)
}

// buildArgs constructs command-line arguments for UBS.
func (s *Scanner) buildArgs(path string, opts ScanOptions) []string {
	args := []string{"--format=json"}

	if len(opts.Languages) > 0 {
		args = append(args, "--only="+strings.Join(opts.Languages, ","))
	}
	if len(opts.ExcludeLanguages) > 0 {
		// ubs --exclude takes path globs; languages are --exclude-langs.
		args = append(args, "--exclude-langs="+strings.Join(opts.ExcludeLanguages, ","))
	}
	if opts.CI {
		args = append(args, "--ci")
	}
	if opts.FailOnWarning {
		args = append(args, "--fail-on-warning")
	}
	if opts.Verbose {
		args = append(args, "-v")
	}
	if opts.StagedOnly {
		args = append(args, "--staged")
	}
	if opts.DiffOnly {
		args = append(args, "--diff")
	}

	if strings.HasPrefix(path, "-") {
		args = append(args, "--", path)
	} else {
		args = append(args, path)
	}
	return args
}

// parseOutput parses UBS JSON output into a ScanResult.
func (s *Scanner) parseOutput(data []byte) (*ScanResult, []string, error) {
	if len(data) == 0 {
		return &ScanResult{}, nil, nil
	}

	var doc ubsDocument
	var warnings []string
	if err := json.Unmarshal(data, &doc); err != nil {
		jsonBlob, lines := splitJSONAndWarnings(data)
		warnings = lines
		if len(jsonBlob) == 0 || json.Unmarshal(jsonBlob, &doc) != nil {
			if len(warnings) > 0 {
				return nil, warnings, ErrOutputNotJSON
			}
			return nil, nil, fmt.Errorf("unmarshaling result: %w", err)
		}
	}
	if doc.Error != "" {
		return nil, warnings, fmt.Errorf("%w: %s (%s)", ErrScanIncomplete, doc.Message, doc.Reason)
	}
	return doc.toResult(), warnings, nil
}

// ubsDocument is ubs's --format=json stdout (`ubs --schema=json`): a scan
// report, a no-supported-languages result (exit 3) or an error envelope
// (exit 2). Only the fields ntm reads are decoded.
type ubsDocument struct {
	Project   string       `json:"project"`
	Timestamp string       `json:"timestamp"`
	Status    string       `json:"status"`
	Error     string       `json:"error"`
	Reason    string       `json:"reason"`
	Message   string       `json:"message"`
	Scanners  []ubsScanner `json:"scanners"`
	Totals    ScanTotals   `json:"totals"`
	Findings  []ubsFinding `json:"findings"`
}

type ubsScanner struct {
	ScannerResult
	Findings []ubsFinding `json:"findings"`
}

// ubsFinding covers the three finding shapes ubs emits: the merged top-level
// list (rule_id/file), a module's own findings (rule/path) and a per-category
// summary (title/count/samples).
type ubsFinding struct {
	RuleID      string      `json:"rule_id"`
	Rule        string      `json:"rule"`
	CategoryID  string      `json:"category_id"`
	Severity    string      `json:"severity"`
	File        string      `json:"file"`
	Path        string      `json:"path"`
	Line        int         `json:"line"`
	Col         int         `json:"col"`
	Message     string      `json:"message"`
	Remediation string      `json:"remediation"`
	Suppressed  bool        `json:"suppressed"`
	Title       string      `json:"title"`
	Samples     []ubsSample `json:"samples"`
}

type ubsSample struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// toResult converts a ubs report into ntm's ScanResult. The merged top-level
// list, when ubs emits one, is the deduplicated set; otherwise findings come
// from each scanner.
func (d *ubsDocument) toResult() *ScanResult {
	result := &ScanResult{
		Project:   d.Project,
		Timestamp: d.Timestamp,
		Status:    d.Status,
		Totals:    d.Totals,
	}
	findings := d.Findings
	for _, s := range d.Scanners {
		result.Scanners = append(result.Scanners, s.ScannerResult)
		if len(d.Findings) == 0 {
			findings = append(findings, s.Findings...)
		}
	}
	for _, f := range findings {
		result.Findings = append(result.Findings, f.toFindings()...)
	}
	return result
}

func (f ubsFinding) toFindings() []Finding {
	severity := Severity(f.Severity)
	if f.Suppressed || (severity != SeverityCritical && severity != SeverityWarning && severity != SeverityInfo) {
		return nil // suppressed, or a summary's "good" entry
	}
	file, rule := f.File, f.RuleID
	if file == "" {
		file = f.Path
	}
	if rule == "" {
		rule = f.Rule
	}
	if file != "" {
		return []Finding{{
			File:       file,
			Line:       f.Line,
			Column:     f.Col,
			Severity:   severity,
			Category:   f.CategoryID,
			Message:    f.Message,
			Suggestion: f.Remediation,
			RuleID:     rule,
		}}
	}
	// A per-category summary names its locations as samples.
	findings := make([]Finding, 0, len(f.Samples))
	for _, sample := range f.Samples {
		findings = append(findings, Finding{
			File:     sample.File,
			Line:     sample.Line,
			Severity: severity,
			Category: f.Title,
			Message:  f.Title,
		})
	}
	return findings
}

func splitJSONAndWarnings(data []byte) ([]byte, []string) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}

	start := bytes.IndexByte(trimmed, '{')
	end := bytes.LastIndexByte(trimmed, '}')
	if start == -1 || end == -1 || end < start {
		return nil, extractWarningLines(trimmed)
	}

	jsonBlob := bytes.TrimSpace(trimmed[start : end+1])
	prefix := strings.TrimSpace(string(trimmed[:start]))
	suffix := strings.TrimSpace(string(trimmed[end+1:]))

	warnings := make([]string, 0, 4)
	warnings = append(warnings, extractWarningLines([]byte(prefix))...)
	warnings = append(warnings, extractWarningLines([]byte(suffix))...)
	return jsonBlob, warnings
}

func extractWarningLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	warnings := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		warnings = append(warnings, line)
	}
	return warnings
}

// QuickScanWithOptions is like QuickScan but accepts custom options.
func QuickScanWithOptions(ctx context.Context, path string, opts ScanOptions) (*ScanResult, error) {
	scanner, err := New()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return nil, nil
		}
		return nil, err
	}
	return scanner.Scan(ctx, path, opts)
}
