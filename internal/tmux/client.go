package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open (tmux
// has failed too many times consecutively and we are in backoff).
var ErrCircuitOpen = errors.New("tmux circuit breaker open: too many consecutive failures, backing off")

// Circuit breaker configuration.
const (
	// cbMaxFailures is the number of consecutive failures before the circuit
	// opens and starts rejecting calls immediately during the backoff window.
	cbMaxFailures = 5
	// cbBackoffDuration is how long the circuit stays open before allowing a
	// single probe call through (half-open state).
	cbBackoffDuration = 10 * time.Second
)

// Client handles tmux operations, optionally on a remote host.
// It includes a built-in circuit breaker that prevents hammering
// the tmux server when it is consistently failing.
type Client struct {
	Remote string // "user@host" or empty for local

	// Circuit breaker state
	cbFailures  atomic.Int64 // consecutive failure count
	cbOpenUntil atomic.Int64 // unix-nano timestamp when circuit closes (0 = closed)
	cbProbing   atomic.Bool  // true when a half-open probe is in flight
}

// NewClient creates a new tmux client
func NewClient(remote string) *Client {
	return &Client{Remote: remote}
}

// DefaultClient is the default local client
var DefaultClient = NewClient("")

// cbCheck returns ErrCircuitOpen if the circuit breaker is open and no
// probe should be attempted.  In half-open state it allows exactly one
// call through (the probe) and returns nil for that caller.
func (c *Client) cbCheck() error {
	openUntil := c.cbOpenUntil.Load()
	if openUntil == 0 {
		return nil // circuit closed
	}
	if time.Now().UnixNano() < openUntil {
		// Still in backoff window. Allow one probe through.
		if c.cbProbing.CompareAndSwap(false, true) {
			return nil // this caller is the half-open probe
		}
		return ErrCircuitOpen
	}
	// Backoff expired — close circuit, allow traffic.
	c.cbOpenUntil.Store(0)
	c.cbFailures.Store(0)
	c.cbProbing.Store(false)
	return nil
}

// cbRecordSuccess resets the circuit breaker to a healthy state.
func (c *Client) cbRecordSuccess() {
	c.cbFailures.Store(0)
	c.cbOpenUntil.Store(0)
	c.cbProbing.Store(false)
}

// cbRecordFailure increments the consecutive failure count and opens
// the circuit once the threshold is reached.
func (c *Client) cbRecordFailure() {
	n := c.cbFailures.Add(1)
	if n >= int64(cbMaxFailures) {
		wasAlreadyOpen := c.cbOpenUntil.Load() != 0
		deadline := time.Now().Add(cbBackoffDuration).UnixNano()
		c.cbOpenUntil.Store(deadline)
		c.cbProbing.Store(false)
		// Log only on the transition from closed to open, not on
		// every subsequent failure or half-open probe failure.
		if !wasAlreadyOpen {
			slog.Warn("tmux circuit breaker opened",
				"consecutive_failures", n,
				"backoff", cbBackoffDuration.String())
		}
	}
}

var (
	tmuxBinaryOnce sync.Once
	tmuxBinaryPath string
)

// BinaryPath returns the resolved tmux binary path for local execution.
// It prefers standard install locations and falls back to PATH lookup.
func BinaryPath() string {
	tmuxBinaryOnce.Do(func() {
		tmuxBinaryPath = resolveTmuxBinaryPath()
	})
	if tmuxBinaryPath == "" {
		return "tmux"
	}
	return tmuxBinaryPath
}

func resolveTmuxBinaryPath() string {
	candidates := []string{
		"/usr/bin/tmux",
		"/usr/local/bin/tmux",
		"/opt/homebrew/bin/tmux",
		"/bin/tmux",
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, fmt.Sprintf("%s/.local/bin/tmux", home))
	}

	for _, path := range candidates {
		if binaryExists(path) {
			return path
		}
	}
	if path, err := exec.LookPath("tmux"); err == nil && path != "" {
		return path
	}
	return "/usr/bin/tmux"
}

func binaryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// DefaultCommandTimeout is the maximum time a tmux command may run before
// being killed.  This prevents indefinite hangs when the tmux server is
// overloaded (e.g. during parallel tests) or a pane/session is wedged.
const DefaultCommandTimeout = 30 * time.Second

// Run executes a tmux command with a default timeout.
func (c *Client) Run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
	defer cancel()
	return c.RunContext(ctx, args...)
}

// RunContext executes a tmux command with cancellation support.
// It checks the circuit breaker before executing and records the
// result (success or failure) to update circuit state.
func (c *Client) RunContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Circuit breaker: reject early if tmux has been consistently failing.
	if err := c.cbCheck(); err != nil {
		return "", err
	}

	var out string
	var err error
	if c.Remote == "" {
		out, err = runLocalContext(ctx, args...)
	} else {
		// Remote execution via ssh
		remoteCmd := buildRemoteShellCommand("tmux", args...)
		// Use "--" to prevent Remote from being parsed as an ssh option.
		out, err = runSSHContext(ctx, "--", c.Remote, remoteCmd)
	}

	if err != nil && isInfrastructureError(err) {
		c.cbRecordFailure()
	} else {
		// Both success (err==nil) and application-level errors (tmux ran
		// but returned non-zero) prove tmux is responsive.  Reset the
		// consecutive infrastructure failure counter.
		c.cbRecordSuccess()
	}
	return out, err
}

// isInfrastructureError returns true for errors indicating the tmux
// process itself is unavailable (timeouts, exec failures, connection
// errors) as opposed to tmux responding normally with an error status.
func isInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	// Context timeout or cancellation → tmux is hung or too slow.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// exec.Error means the binary couldn't be started at all (not found,
	// permission denied).
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	// exec.ExitError means the process ran and exited non-zero.  For local
	// tmux this is an application-level error ("session not found" etc.).
	// For SSH, exit code 255 specifically indicates a connection/protocol
	// failure — that IS an infrastructure error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 255 {
			return true // SSH connection failure
		}
		return false // normal non-zero exit from tmux
	}
	// Unknown error type — conservatively treat as infrastructure failure.
	return true
}

// ShellQuote returns a POSIX-shell-safe single-quoted string.
//
// This is required for ssh remote commands because OpenSSH transmits a single
// command string to the remote shell (not an argv vector).
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}

	// Close-quote, escape single quote, reopen: ' -> '\''.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildRemoteShellCommand(command string, args ...string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, ShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func runLocalContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	binary := BinaryPath()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runSSHContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Inject /bin/sh -c to ensure consistent shell behavior for the remote command.
	// The args passed here are already built by buildRemoteShellCommand, which
	// produces a single string like "tmux 'arg1' 'arg2'".
	// We want: ssh host /bin/sh -c "tmux 'arg1' 'arg2'"
	//
	// args[0] is flags like "-t"
	// args[1] is "--"
	// args[2] is remote host
	// args[3] is the command string

	if len(args) > 0 {
		commandIndex := len(args) - 1
		originalCommand := args[commandIndex]
		args[commandIndex] = fmt.Sprintf("/bin/sh -c %s", ShellQuote(originalCommand))
	}

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("ssh %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunSilent executes a tmux command ignoring output
func (c *Client) RunSilent(args ...string) error {
	_, err := c.Run(args...)
	return err
}

// RunSilentContext executes a tmux command with cancellation support, ignoring stdout.
func (c *Client) RunSilentContext(ctx context.Context, args ...string) error {
	_, err := c.RunContext(ctx, args...)
	return err
}

// IsInstalled checks if tmux is available on the target host
func (c *Client) IsInstalled() bool {
	if c.Remote == "" {
		return binaryExists(BinaryPath())
	}
	// Check remote
	err := c.RunSilent("-V")
	return err == nil
}

// RespawnPane respawns a pane, optionally killing the current process (-k)
func (c *Client) RespawnPane(target string, kill bool) error {
	return c.RespawnPaneContext(context.Background(), target, kill)
}

// RespawnPaneContext respawns a pane with cancellation support
func (c *Client) RespawnPaneContext(ctx context.Context, target string, kill bool) error {
	args := []string{"respawn-pane", "-t", target}
	if kill {
		args = append(args, "-k")
	}
	return c.RunSilentContext(ctx, args...)
}

// RespawnPane respawns a pane, optionally killing the current process (-k) (default client)
func RespawnPane(target string, kill bool) error {
	return DefaultClient.RespawnPane(target, kill)
}

// RespawnPaneContext respawns a pane with cancellation support (default client)
func RespawnPaneContext(ctx context.Context, target string, kill bool) error {
	return DefaultClient.RespawnPaneContext(ctx, target, kill)
}

// ApplyTiledLayout applies tiled layout to all windows
