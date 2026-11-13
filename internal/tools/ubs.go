package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// UBSAdapter provides integration with the Ultimate Bug Scanner (ubs) tool.
// UBS performs code review and bug detection, identifying potential issues
// before they reach production.
type UBSAdapter struct {
	*BaseAdapter
}

// NewUBSAdapter creates a new UBS adapter
func NewUBSAdapter() *UBSAdapter {
	return &UBSAdapter{
		BaseAdapter: NewBaseAdapter(ToolUBS, "ubs"),
	}
}

// Detect checks if ubs is installed
func (a *UBSAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed ubs version
func (a *UBSAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get ubs version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of ubs capabilities
func (a *UBSAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	// Check if ubs has specific capabilities by examining help output
	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	output := helpText(ctx, a.Timeout(), path, "--help")

	// Check for known capabilities
	if strings.Contains(output, "--json") || strings.Contains(output, "robot") {
		caps = append(caps, CapRobotMode)
	}
	if strings.Contains(output, "scan") {
		caps = append(caps, CapSearch) // Scan is a form of search
	}

	return caps, nil
}

// Health checks if ubs is functioning correctly
func (a *UBSAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "ubs not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("ubs at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "ubs is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// Info returns complete ubs tool information
func (a *UBSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// Doctor runs UBS diagnostics
func (a *UBSAdapter) Doctor(ctx context.Context) (string, error) {
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
		return "", fmt.Errorf("ubs doctor failed: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
