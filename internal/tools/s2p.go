package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// S2PAdapter provides integration with Source-to-Prompt tool
type S2PAdapter struct {
	*BaseAdapter
}

// NewS2PAdapter creates a new S2P adapter
func NewS2PAdapter() *S2PAdapter {
	return &S2PAdapter{
		BaseAdapter: NewBaseAdapter(ToolS2P, "s2p"),
	}
}

// Detect checks if s2p is installed
func (a *S2PAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed s2p version
func (a *S2PAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get s2p version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns s2p capabilities
func (a *S2PAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	return []Capability{CapContextPack}, nil
}

// Health checks if s2p is functioning
func (a *S2PAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "s2p not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("s2p at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "s2p is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if s2p has a specific capability
func (a *S2PAdapter) HasCapability(ctx context.Context, cap Capability) bool {
	caps, err := a.Capabilities(ctx)
	if err != nil {
		return false
	}
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Info returns complete s2p tool information
func (a *S2PAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// S2P-specific methods

// GenerateContext generates context for given files/patterns
func (a *S2PAdapter) GenerateContext(ctx context.Context, dir string, patterns []string, format string) ([]byte, error) {
	args := []string{}
	if format != "" {
		args = append(args, "--format", format)
	}
	args = append(args, patterns...)

	return a.runCommand(ctx, dir, args...)
}

func (a *S2PAdapter) runCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	if dir != "" {
		cmd.Dir = dir
	}

	// Limit output to 10MB
	stdout := &LimitedBuffer{Limit: 10 * 1024 * 1024}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// Check if it was our limit error
		if strings.Contains(err.Error(), "output limit exceeded") {
			return nil, fmt.Errorf("s2p output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("s2p failed: %w: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}
