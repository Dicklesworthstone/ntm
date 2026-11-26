package cass

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	// WaitDelay bounds inherited output pipes after exit/cancellation. Zero
	// retains the ordinary client's two-second bound. Prompt enrichment uses
	// its existing shorter drain budget so optional history releases dispatch.
	WaitDelay time.Duration
}

func (e *DefaultExecutor) Run(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("cass execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Cancel on output overflow as well as caller cancellation. Merely stopping
	// a pipe reader can leave a subprocess blocked forever writing to that pipe.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, e.BinaryPath, args...)
	cmd.WaitDelay = e.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	stdout := newLimitedOutput(maxCASSStdoutBytes, cancel)
	stderr := newLimitedOutput(maxCASSStderrBytes, cancel)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	// The operation's lifetime wins over an exit code or a successful JSON
	// response that raced its cancellation. Never classify that as an empty index.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, ErrOutputLimit
	}
	if err != nil {
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

func expandCASSPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || (os.PathSeparator == '\\' && strings.HasPrefix(p, `~\`)) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// NewClient creates a new CASS client, honoring NTM_CASS_BINARY and
// NTM_CASS_TIMEOUT environment overrides when no explicit option overrides them.
func NewClient(opts ...ClientOption) *Client {
	// Default to "cass" in PATH
	binary := "cass"
	if envBin := expandCASSPath(os.Getenv("NTM_CASS_BINARY")); envBin != "" {
		binary = envBin
	}
	timeout := 30 * time.Second
	if envTimeout := strings.TrimSpace(os.Getenv("NTM_CASS_TIMEOUT")); envTimeout != "" {
		if secs, err := strconv.Atoi(envTimeout); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	c := &Client{
		executor: &DefaultExecutor{BinaryPath: binary},
		timeout:  timeout,
	}

	for _, opt := range opts {
		opt(c)
	}
	return c
}

// NewClientFromSettings builds a CASS client honoring configured binary_path
// and timeout_seconds (falling back to NTM_CASS_BINARY / "cass" on PATH and 30s).
func NewClientFromSettings(binaryPath string, timeoutSeconds int) *Client {
	var opts []ClientOption
	if expanded := expandCASSPath(binaryPath); expanded != "" {
		opts = append(opts, WithBinaryPath(expanded))
	}
	if timeoutSeconds > 0 {
		opts = append(opts, WithTimeout(time.Duration(timeoutSeconds)*time.Second))
	}
	return NewClient(opts...)
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
