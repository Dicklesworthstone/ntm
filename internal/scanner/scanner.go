package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// MaxScanOutputBytes limits the size of scan output to prevent OOM.
const MaxScanOutputBytes = 10 * 1024 * 1024

// Scanner wraps the UBS command-line tool.
type Scanner struct {
	binaryPath string
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
	cmd := exec.Command(s.binaryPath, "--version")
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

	// Capture stderr separately
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ubs: %w", err)
	}

	// Ensure process is cleaned up if we return early (e.g. read error)
	// We need to ensure we don't double-wait or double-kill in the success path.
	// We will handle cleanup explicitly in error paths or rely on Wait() at the end.
	// But defer is safer.
	var waitDone bool
	defer func() {
		if !waitDone && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait() // Reap the zombie
		}
	}()

	// Read output with limit
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, MaxScanOutputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading output: %w", err)
	}

	// Check if output exceeded limit
	if len(output) > MaxScanOutputBytes {
		return nil, ErrOutputTooLarge
	}

	waitErr := cmd.Wait()
	waitDone = true
	duration := time.Since(startTime)

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}

	// Parse the JSON output (capture warnings even if output is mixed)
	result, warnings, parseErr := s.parseOutput(output)
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

	// Get exit code
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("running ubs: %w", waitErr)
		}
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
		args = append(args, "--exclude="+strings.Join(opts.ExcludeLanguages, ","))
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

	args = append(args, path)
	return args
}

// parseOutput parses UBS JSON output into a ScanResult.
func (s *Scanner) parseOutput(data []byte) (*ScanResult, []string, error) {
	if len(data) == 0 {
		return &ScanResult{}, nil, nil
	}

	var result ScanResult
	if err := json.Unmarshal(data, &result); err == nil {
		return &result, nil, nil
	} else {
		jsonBlob, warnings := splitJSONAndWarnings(data)
		if len(jsonBlob) > 0 {
			if err := json.Unmarshal(jsonBlob, &result); err == nil {
				return &result, warnings, nil
			}
		}
		if len(warnings) > 0 {
			return nil, warnings, ErrOutputNotJSON
		}
		return nil, nil, fmt.Errorf("unmarshaling result: %w", err)
	}
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

// QuickScan is a convenience function that creates a scanner and runs a scan.
// Returns nil, nil if UBS is not installed (graceful degradation).
func QuickScan(ctx context.Context, path string) (*ScanResult, error) {
	scanner, err := New()
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			return nil, nil // Graceful degradation
		}
		return nil, err
	}
	return scanner.Scan(ctx, path, DefaultOptions())
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
