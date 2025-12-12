package tmux

import (
	"bytes"
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
	if c.Remote == "" {
		return runLocal(args...)
	}

	// Remote execution via ssh
	remoteCmd := buildRemoteShellCommand("tmux", args...)
	// Use "--" to prevent Remote from being parsed as an ssh option.
	return runSSH("--", c.Remote, remoteCmd)
}

// shellQuote returns a POSIX-shell-safe single-quoted string.
//
// This is required for ssh remote commands because OpenSSH transmits a single
// command string to the remote shell (not an argv vector).
func shellQuote(s string) string {
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
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// runLocal executes a tmux command locally
func runLocal(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// runSSH executes an ssh command and returns stdout
func runSSH(args ...string) (string, error) {
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("ssh %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunSilent executes a tmux command ignoring output
func (c *Client) RunSilent(args ...string) error {
	_, err := c.Run(args...)
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
