package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client handles tmux operations, optionally on a remote host
type Client struct {
	Remote string // "user@host" or empty for local
}

// NewClient creates a new tmux client
func NewClient(remote string) *Client {
	return &Client{Remote: remote}
}

// DefaultClient is the default local client
var DefaultClient = NewClient("")

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
func (c *Client) RunContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.Remote == "" {
		return runLocalContext(ctx, args...)
	}

	// Remote execution via ssh
	remoteCmd := buildRemoteShellCommand("tmux", args...)
	// Use "--" to prevent Remote from being parsed as an ssh option.
	return runSSHContext(ctx, "--", c.Remote, remoteCmd)
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
