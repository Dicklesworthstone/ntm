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

// CommandErrorKind is the stable category for a tmux command failure.
type CommandErrorKind string

const (
	CommandErrorNone              CommandErrorKind = ""
	CommandErrorTimeout           CommandErrorKind = "timeout"
	CommandErrorCanceled          CommandErrorKind = "canceled"
	CommandErrorCircuitOpen       CommandErrorKind = "circuit_open"
	CommandErrorBinaryUnavailable CommandErrorKind = "binary_unavailable"
	CommandErrorPermissionDenied  CommandErrorKind = "permission_denied"
	CommandErrorRemoteUnavailable CommandErrorKind = "remote_unavailable"
	CommandErrorSessionNotFound   CommandErrorKind = "session_not_found"
	CommandErrorPaneNotFound      CommandErrorKind = "pane_not_found"
	CommandErrorNoServer          CommandErrorKind = "no_server"
	CommandErrorMalformedOutput   CommandErrorKind = "malformed_output"
	CommandErrorCommandFailed     CommandErrorKind = "command_failed"
	CommandErrorUnknown           CommandErrorKind = "unknown"
)

// CommandErrorClass describes how callers should treat a tmux command error.
type CommandErrorClass struct {
	Kind           CommandErrorKind
	Infrastructure bool
	Retryable      bool
}

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
		// Deliberately does NOT clear cbProbing. Clearing it here meant a
		// FAILED half-open probe immediately re-armed the gate, so the very
		// next caller won the CompareAndSwap in cbCheck and was admitted as a
		// fresh probe — and so on, forever. Against a fast-failing tmux
		// (missing binary, permission-denied socket) the breaker therefore
		// shed no load at all: a 40-pane status sweep issued 40 exec attempts
		// instead of failing fast after 5. The flag is cleared where the
		// backoff window actually retires (cbCheck) and on success
		// (cbRecordSuccess), which is what "one probe per window" requires.
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
// An explicit override is evaluated on every call; only normal installation
// discovery is cached.
func BinaryPath() string {
	if override := strings.TrimSpace(os.Getenv("NTM_TMUX_BINARY")); override != "" {
		return override
	}
	tmuxBinaryOnce.Do(func() {
		tmuxBinaryPath = resolveInstalledTmuxBinaryPath()
	})
	if tmuxBinaryPath == "" {
		return "tmux"
	}
	return tmuxBinaryPath
}

func resolveTmuxBinaryPath() string {
	if override := strings.TrimSpace(os.Getenv("NTM_TMUX_BINARY")); override != "" {
		return override
	}
	return resolveInstalledTmuxBinaryPath()
}

func resolveInstalledTmuxBinaryPath() string {
	if path := findInstalledTmuxBinaryPath(); path != "" {
		return path
	}
	return "/usr/bin/tmux"
}

func findInstalledTmuxBinaryPath() string {
	if path := findStandardTmuxBinaryPath(); path != "" {
		return path
	}
	if path, err := exec.LookPath("tmux"); err == nil && path != "" {
		return path
	}
	return ""
}

func findStandardTmuxBinaryPath() string {
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
	return ""
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

	if err != nil && ClassifyCommandError(err).Infrastructure {
		c.cbRecordFailure()
	} else {
		// Both success (err==nil) and application-level errors (tmux ran
		// but returned non-zero) prove tmux is responsive.  Reset the
		// consecutive infrastructure failure counter.
		c.cbRecordSuccess()
	}
	return out, err
}

// ClassifyCommandError returns the stable class for a tmux command failure.
// It keeps caller decisions about retry and circuit-breaker accounting aligned.
func ClassifyCommandError(err error) CommandErrorClass {
	if err == nil {
		return CommandErrorClass{Kind: CommandErrorNone}
	}
	msg := strings.ToLower(err.Error())

	if errors.Is(err, ErrCircuitOpen) {
		return CommandErrorClass{Kind: CommandErrorCircuitOpen, Infrastructure: true, Retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if errors.Is(err, context.Canceled) {
			return CommandErrorClass{Kind: CommandErrorCanceled, Infrastructure: true}
		}
		return CommandErrorClass{Kind: CommandErrorTimeout, Infrastructure: true, Retryable: true}
	}
	if strings.Contains(msg, "can't find pane") || strings.Contains(msg, "can't find window") {
		return CommandErrorClass{Kind: CommandErrorPaneNotFound}
	}
	if strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no such session") ||
		strings.Contains(msg, "session not found") {
		return CommandErrorClass{Kind: CommandErrorSessionNotFound}
	}
	if strings.Contains(msg, "permission denied") {
		return CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true}
	}
	if strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "error connecting to") ||
		strings.Contains(msg, "no sessions") {
		// Deliberately NOT Infrastructure. Infrastructure is consumed by
		// exactly one thing — circuit-breaker accounting — and "no server
		// running" is an instant, definitive answer meaning "there are no
		// tmux sessions", not evidence that tmux is sick. tmux returns it
		// without connecting to anything, so retrying costs nothing and there
		// is no load to shed. Counting it conflated "tmux is unhealthy" with
		// "tmux has nothing running": on a machine with no server, the sixth
		// consecutive session query stopped reporting SESSION_NOT_FOUND and
		// started reporting an INTERNAL_ERROR circuit-open instead — a
		// correct, actionable answer degraded into a confusing one.
		// Retryable stays true: a server may be started later.
		return CommandErrorClass{Kind: CommandErrorNoServer, Retryable: true}
	}
	if strings.Contains(msg, "unexpected session format") ||
		strings.Contains(msg, "malformed tmux output") ||
		strings.Contains(msg, "malformed output") {
		return CommandErrorClass{Kind: CommandErrorMalformedOutput, Infrastructure: true, Retryable: true}
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		switch {
		case errors.Is(execErr.Err, exec.ErrNotFound):
			return CommandErrorClass{Kind: CommandErrorBinaryUnavailable, Infrastructure: true}
		case errors.Is(execErr.Err, os.ErrPermission):
			return CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true}
		default:
			return CommandErrorClass{Kind: CommandErrorBinaryUnavailable, Infrastructure: true}
		}
	}

	if exitCode, ok := commandExitCode(err); ok {
		if exitCode == 255 {
			return CommandErrorClass{Kind: CommandErrorRemoteUnavailable, Infrastructure: true, Retryable: true}
		}
		return CommandErrorClass{Kind: CommandErrorCommandFailed}
	}

	return CommandErrorClass{Kind: CommandErrorUnknown, Infrastructure: true, Retryable: true}
}

// commandExitCode returns the process exit code carried by err, including when
// the exec.ExitError is wrapped with command and stderr context.
func commandExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	return exitErr.ExitCode(), true
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
