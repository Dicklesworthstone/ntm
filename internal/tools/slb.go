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

// SLBAdapter provides integration with the Simultaneous Launch Button (slb) tool
type SLBAdapter struct {
	*BaseAdapter
}

// NewSLBAdapter creates a new SLB adapter
func NewSLBAdapter() *SLBAdapter {
	return &SLBAdapter{
		BaseAdapter: NewBaseAdapter(ToolSLB, "slb"),
	}
}

// Detect checks if slb is installed
func (a *SLBAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed slb version
func (a *SLBAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get slb version: %w", err)
	}

	return parseSLBVersion(stdout.String())
}

// parseSLBVersion extracts version from slb --version output
// Format:
//
//	slb 0.1.0
//	  commit:  cc17518fe7d699363f4bcb48670ed4a3bbc71127
//	  built:   2025-12-25T03:35:46Z
//	  go:      go1.24.11
//	  config:  /home/ubuntu/.slb/config.toml
//	  db:      /data/projects/ntm/.slb/state.db
//	  project: /data/projects/ntm
func parseSLBVersion(output string) (Version, error) {
	output = strings.TrimSpace(output)

	// Get first line: "slb 0.1.0"
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return Version{Raw: output}, nil
	}

	firstLine := strings.TrimSpace(lines[0])

	// Extract version from "slb X.Y.Z"
	parts := strings.Fields(firstLine)
	if len(parts) < 2 {
		return Version{Raw: output}, nil
	}

	versionPart := parts[1]

	// Use the shared version regex from bv.go
	matches := versionRegex.FindStringSubmatch(versionPart)
	if len(matches) < 4 {
		return Version{Raw: output}, nil
	}

	var major, minor, patch int
	fmt.Sscanf(matches[1], "%d", &major)
	fmt.Sscanf(matches[2], "%d", &minor)
	fmt.Sscanf(matches[3], "%d", &patch)

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   output,
	}, nil
}

// Capabilities returns the list of slb capabilities
func (a *SLBAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{
		CapRobotMode,  // slb supports --json output
		CapDaemonMode, // slb can run as daemon (slb daemon)
		"request",     // slb request <command>
		"approve",     // slb approve <request-id>
		"deny",        // slb deny <request-id>
		"status",      // slb status
		"pending",     // slb pending
	}

	return caps, nil
}

// Health checks if slb is functioning correctly
func (a *SLBAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "slb not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check (fast)
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("slb at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "slb is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if slb has a specific capability
func (a *SLBAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete slb tool information
func (a *SLBAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// SLB-specific methods

// Status returns the current SLB daemon status
func (a *SLBAdapter) Status(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "status", "--json")
}

// Pending returns list of pending approval requests
func (a *SLBAdapter) Pending(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "pending", "--json")
}

// Request creates a new approval request for a command
func (a *SLBAdapter) Request(ctx context.Context, command string, reason string) (json.RawMessage, error) {
	args := []string{"request", command, "--json"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	return a.runCommand(ctx, args...)
}

// Approve approves a pending request
func (a *SLBAdapter) Approve(ctx context.Context, requestID string) (json.RawMessage, error) {
	return a.runCommand(ctx, "approve", requestID, "--json")
}

// Deny denies a pending request
func (a *SLBAdapter) Deny(ctx context.Context, requestID string, reason string) (json.RawMessage, error) {
	args := []string{"deny", requestID, "--json"}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	return a.runCommand(ctx, args...)
}

// runCommand executes an slb command and returns raw JSON
func (a *SLBAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("slb %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from slb", ErrSchemaValidation)
	}

	return output, nil
}
