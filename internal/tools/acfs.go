package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ACFSAdapter provides integration with the Agentic Coding Flywheel Setup (acfs) tool
type ACFSAdapter struct {
	*BaseAdapter
}

// NewACFSAdapter creates a new ACFS adapter
func NewACFSAdapter() *ACFSAdapter {
	return &ACFSAdapter{
		BaseAdapter: NewBaseAdapter(ToolACFS, "acfs"),
	}
}

// Detect checks if acfs is installed
func (a *ACFSAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed acfs version
func (a *ACFSAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get acfs version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of acfs capabilities
func (a *ACFSAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{
		CapRobotMode, // acfs supports --json output for info/doctor/cheatsheet
		"doctor",     // acfs doctor - system health check
		"info",       // acfs info - system overview
		"status",     // acfs status/continue - installation progress
		"update",     // acfs update - update tools
		"dashboard",  // acfs dashboard - generate HTML dashboard
		"session",    // acfs session - list/export/import sessions
	}

	return caps, nil
}

// Health checks if acfs is functioning correctly
func (a *ACFSAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "acfs not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check (fast)
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("acfs at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "acfs is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if acfs has a specific capability
func (a *ACFSAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete acfs tool information
func (a *ACFSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// ACFS-specific methods

// GetInfo returns system overview information
func (a *ACFSAdapter) GetInfo(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "info", "--json")
}

// Doctor runs system health check
func (a *ACFSAdapter) Doctor(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "doctor", "--json")
}

// GetStatus returns installation progress status
func (a *ACFSAdapter) GetStatus(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "status", "--json")
}

// runCommand executes an acfs command and returns raw JSON
func (a *ACFSAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second

	// Limit output to 10MB
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if strings.Contains(err.Error(), ErrOutputLimitExceeded.Error()) {
			return nil, fmt.Errorf("acfs output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("acfs %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from acfs", ErrSchemaValidation)
	}

	return output, nil
}
