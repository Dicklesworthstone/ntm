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

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

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

// HasCapability checks if ru has a specific capability
func (a *RUAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete ru tool information
func (a *RUAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// RU-specific methods

// RUStatus represents the current ru status output
type RUStatus struct {
	Repos      []RURepoStatus `json:"repos,omitempty"`
	TotalRepos int            `json:"total_repos"`
	NeedsSync  int            `json:"needs_sync"`
	UpToDate   int            `json:"up_to_date"`
	HasChanges int            `json:"has_changes"`
}

// RURepoStatus represents the status of a single repo
type RURepoStatus struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Branch     string `json:"branch,omitempty"`
	Ahead      int    `json:"ahead,omitempty"`
	Behind     int    `json:"behind,omitempty"`
	HasChanges bool   `json:"has_changes,omitempty"`
}

// GetStatus returns the current ru status for all tracked repos
func (a *RUAdapter) GetStatus(ctx context.Context) (*RUStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status", "--json", "--no-fetch")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// Return empty status if command fails
		return &RUStatus{}, nil
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		// Return empty status if output is not valid JSON
		return &RUStatus{}, nil
	}

	var status RUStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse ru status: %w", err)
	}

	return &status, nil
}

// Sync runs ru sync with optional flags
func (a *RUAdapter) Sync(ctx context.Context, dryRun bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // Sync can take a while
	defer cancel()

	args := []string{"sync", "--non-interactive"}
	if dryRun {
		args = append(args, "--dry-run")
	}

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ErrTimeout
		}
		return fmt.Errorf("ru sync failed: %w: %s", err, stderr.String())
	}

	return nil
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
