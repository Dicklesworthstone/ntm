package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// S2PAdapter reports availability of the interactive Source-to-Prompt tool.
// Context packs prepare source files natively; s2p has no headless interface.
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

// Capabilities does not advertise automated context generation: the installed
// tool is interactive, and its hypothetical --format interface never existed.
func (a *S2PAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	return []Capability{}, nil
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
