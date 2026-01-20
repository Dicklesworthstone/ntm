package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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

// Run executes a tmux command
func (c *Client) Run(args ...string) (string, error) {
	return c.RunContext(context.Background(), args...)
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
	cmd := exec.CommandContext(ctx, "tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runSSHContext(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
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
		_, err := exec.LookPath("tmux")
		return err == nil
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
