package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RUAdapter provides integration with the Repo Updater (ru) tool.
// RU is a CLI for synchronizing GitHub repositories across multiple projects,
// supporting smart commits, issue/PR review, and automation workflows.
type RUAdapter struct {
	*BaseAdapter
}

// NewRUAdapter creates a new RU adapter
func NewRUAdapter() *RUAdapter {
	return &RUAdapter{
		BaseAdapter: NewBaseAdapter(ToolRU, "ru"),
	}
}

// Detect checks if ru is installed
func (a *RUAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed ru version
func (a *RUAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get ru version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of ru capabilities
func (a *RUAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	// Check if ru has specific capabilities by examining help output
	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	output := helpText(ctx, a.Timeout(), path, "--help")

	// Check for known capabilities
	if strings.Contains(output, "--json") {
		caps = append(caps, CapRobotMode)
	}
	if strings.Contains(output, "sync") {
		caps = append(caps, CapSearch) // Sync can find repos
	}

	return caps, nil
}

// Health checks if ru is functioning correctly
func (a *RUAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "ru not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("ru at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "ru is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// Info returns complete ru tool information
func (a *RUAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// Doctor runs ru doctor diagnostics
func (a *RUAdapter) Doctor(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "doctor")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("ru doctor failed: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
