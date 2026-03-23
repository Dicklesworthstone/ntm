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

// MSAdapter provides integration with the Meta Skill (ms) tool
type MSAdapter struct {
	*BaseAdapter
}

// NewMSAdapter creates a new MS adapter
func NewMSAdapter() *MSAdapter {
	return &MSAdapter{
		BaseAdapter: NewBaseAdapter(ToolMS, "ms"),
	}
}

// Detect checks if ms is installed
func (a *MSAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed ms version
func (a *MSAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get ms version: %w", err)
	}

	return parseMSVersion(stdout.String())
}

// parseMSVersion extracts version from ms --version output
// Expected format: "ms 0.1.0" or just "0.1.0"
func parseMSVersion(output string) (Version, error) {
	output = strings.TrimSpace(output)

	// Try to extract "ms X.Y.Z" format first
	if strings.HasPrefix(output, "ms ") {
		output = strings.TrimPrefix(output, "ms ")
		output = strings.TrimSpace(output)
	}

	// Use the shared version regex from adapter.go
	matches := VersionRegex.FindStringSubmatch(output)
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

// Capabilities returns the list of ms capabilities
func (a *MSAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{
		CapRobotMode, // ms supports --json output
		CapSearch,    // ms search <query>
		"suggest",    // ms suggest <task>
		"list",       // ms list
		"show",       // ms show <id>
	}

	return caps, nil
}

// Health checks if ms is functioning correctly
func (a *MSAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "ms not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check (fast)
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("ms at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "ms is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if ms has a specific capability
func (a *MSAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete ms tool information
func (a *MSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// MS-specific methods

// Search searches for skills matching a query
func (a *MSAdapter) Search(ctx context.Context, query string) (json.RawMessage, error) {
	return a.runCommand(ctx, "search", query, "--json")
}

// Suggest returns skill suggestions for a task
func (a *MSAdapter) Suggest(ctx context.Context, task string) (json.RawMessage, error) {
	return a.runCommand(ctx, "suggest", task, "--json")
}

// List returns all available skills
func (a *MSAdapter) List(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "list", "--json")
}

// Show returns details for a specific skill
func (a *MSAdapter) Show(ctx context.Context, id string) (json.RawMessage, error) {
	return a.runCommand(ctx, "show", id, "--json")
}

// runCommand executes an ms command and returns raw JSON
func (a *MSAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		if strings.Contains(err.Error(), ErrOutputLimitExceeded.Error()) {
			return nil, fmt.Errorf("ms output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("ms %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from ms", ErrSchemaValidation)
	}

	return output, nil
}
