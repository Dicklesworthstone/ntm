package cass

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ErrNotInstalled is returned when the cass binary is not found
var ErrNotInstalled = fmt.Errorf("cass is not installed")

// ErrNotInitialized is returned when cass is installed but its index has
// not been built yet (cass exits with code 3 to signal this — see
// `cass --help`'s "exit 3: no database — run 'cass index --full' to
// create it"). Surfaced as a distinct sentinel so callers can degrade
// gracefully (e.g. skip dedup-check rather than fail the send) instead
// of treating "uninitialized" as the same class of failure as "binary
// crashed mid-search."
var ErrNotInitialized = errors.New("cass installed but not initialized (run: cass index --full)")

// cassExitNotInitialized is the exit code cass uses to signal "no
// database has been built yet" — see the cass --help output. Treated
// here as a soft / recoverable signal.
const cassExitNotInitialized = 3

// Executor interface allows mocking the cass binary execution
type Executor interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// DefaultExecutor runs the actual binary
type DefaultExecutor struct {
	BinaryPath string
}

func (e *DefaultExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.BinaryPath, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Translate cass's documented "no database yet" exit code
		// into a typed sentinel so callers can decide whether to
		// continue (e.g. dedup-check skips) or surface the error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == cassExitNotInitialized {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("cass execution failed: %w (stderr: %s)", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Client interacts with the CASS CLI
type Client struct {
	executor Executor
	timeout  time.Duration
}

// ClientOption configures the client
type ClientOption func(*Client)

// WithBinaryPath sets the path to the cass binary
func WithBinaryPath(path string) ClientOption {
	return func(c *Client) {
		if path == "" {
			return
		}
		if execImpl, ok := c.executor.(*DefaultExecutor); ok {
			execImpl.BinaryPath = path
		}
	}
}

// WithTimeout sets the command timeout
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = d
	}
}

// WithExecutor sets a custom executor (for testing)
func WithExecutor(e Executor) ClientOption {
	return func(c *Client) {
		c.executor = e
	}
}

// NewClient creates a new CASS client
func NewClient(opts ...ClientOption) *Client {
	// Default to "cass" in PATH
	binary := "cass"

	c := &Client{
		executor: &DefaultExecutor{BinaryPath: binary},
		timeout:  30 * time.Second,
	}

	for _, opt := range opts {
		opt(c)
	}
	return c
}

// IsInstalled checks if the cass binary is available
func (c *Client) IsInstalled() bool {
	if execImpl, ok := c.executor.(*DefaultExecutor); ok {
		// Check if binary path is valid/executable
		path, err := exec.LookPath(execImpl.BinaryPath)
		return err == nil && path != ""
	}
	return true // Assume custom executor is working
}
