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
	ErrNotInstalled = errors.New("ubs is not installed")
	ErrTimeout      = errors.New("scan timed out")
	ErrScanFailed   = errors.New("scan failed")
	ErrOutputTooLarge = errors.New("scan output exceeded limit")
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

	// Read output with limit
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, MaxScanOutputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading output: %w", err)
	}

	// Check if output exceeded limit
	if len(output) > MaxScanOutputBytes {
		_ = cmd.Process.Kill()
		return nil, ErrOutputTooLarge
	}

	waitErr := cmd.Wait()
	duration := time.Since(startTime)

	// Check for timeout
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}

	// Parse the JSON output
	result, parseErr := s.parseOutput(output)
	if parseErr != nil {
		// If we can't parse output but command succeeded, return basic result
		if waitErr == nil {
			return &ScanResult{
				Project:  path,
				Duration: duration,
				ExitCode: 0,
			}, nil
		}
		return nil, fmt.Errorf("parsing output: %w (stderr: %s)", parseErr, stderr.String())
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
func (s *Scanner) parseOutput(data []byte) (*ScanResult, error) {
	if len(data) == 0 {
		return &ScanResult{}, nil
	}

	var result ScanResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshaling result: %w", err)
	}

	return &result, nil
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
