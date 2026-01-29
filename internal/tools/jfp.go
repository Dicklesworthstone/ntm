package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// JFPAdapter provides integration with the JeffreysPrompts CLI (jfp)
type JFPAdapter struct {
	*BaseAdapter
}

// NewJFPAdapter creates a new JFP adapter
func NewJFPAdapter() *JFPAdapter {
	return &JFPAdapter{
		BaseAdapter: NewBaseAdapter(ToolJFP, "jfp"),
	}
}

// Detect checks if jfp is installed
func (a *JFPAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed jfp version
func (a *JFPAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get jfp version: %w", err)
	}

	return parseJFPVersion(stdout.String())
}

// parseJFPVersion extracts version from jfp --version output
// Format: "jfp/1.0.0 linux-x64 node-v24.3.0"
func parseJFPVersion(output string) (Version, error) {
	output = strings.TrimSpace(output)

	// Extract version from "jfp/X.Y.Z ..."
	parts := strings.Fields(output)
	if len(parts) == 0 {
		return Version{Raw: output}, nil
	}

	// Parse "jfp/1.0.0"
	versionPart := parts[0]
	if idx := strings.Index(versionPart, "/"); idx >= 0 {
		versionPart = versionPart[idx+1:]
	}

	// Use the shared version regex from adapter.go
	matches := VersionRegex.FindStringSubmatch(versionPart)
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

// Capabilities returns the list of jfp capabilities
func (a *JFPAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{
		CapRobotMode, // jfp supports --json output
		CapSearch,    // jfp search <query>
		"list",       // jfp list
		"show",       // jfp show <id>
		"install",    // jfp install [...ids]
		"export",     // jfp export [...ids]
		"suggest",    // jfp suggest <task>
		"mcp_server", // jfp serve (MCP server mode)
	}

	return caps, nil
}

// Health checks if jfp is functioning correctly
func (a *JFPAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "jfp not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check (fast)
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("jfp at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "jfp is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if jfp has a specific capability
func (a *JFPAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete jfp tool information
func (a *JFPAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// JFP-specific methods

// List returns all prompts
func (a *JFPAdapter) List(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "list", "--json")
}

// ListByCategory returns prompts filtered by category
func (a *JFPAdapter) ListByCategory(ctx context.Context, category string) (json.RawMessage, error) {
	return a.runCommand(ctx, "list", "--category", category, "--json")
}

// ListByTag returns prompts filtered by tag
func (a *JFPAdapter) ListByTag(ctx context.Context, tag string) (json.RawMessage, error) {
	return a.runCommand(ctx, "list", "--tag", tag, "--json")
}

// Search performs fuzzy search for prompts
func (a *JFPAdapter) Search(ctx context.Context, query string) (json.RawMessage, error) {
	return a.runCommand(ctx, "search", query, "--json")
}

// Show returns details for a specific prompt
func (a *JFPAdapter) Show(ctx context.Context, id string) (json.RawMessage, error) {
	return a.runCommand(ctx, "show", id, "--json")
}

// Suggest returns prompt suggestions for a task
func (a *JFPAdapter) Suggest(ctx context.Context, task string) (json.RawMessage, error) {
	return a.runCommand(ctx, "suggest", task, "--json")
}

// Status returns registry cache status and settings
func (a *JFPAdapter) Status(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "status", "--json")
}

// Installed returns list of installed Claude Code skills
func (a *JFPAdapter) Installed(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "installed", "--json")
}

// Categories returns all categories with counts
func (a *JFPAdapter) Categories(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "categories", "--json")
}

// Tags returns all tags with counts
func (a *JFPAdapter) Tags(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "tags", "--json")
}

// Bundles returns all prompt bundles
func (a *JFPAdapter) Bundles(ctx context.Context) (json.RawMessage, error) {
	return a.runCommand(ctx, "bundles", "--json")
}

// Bundle returns details for a specific bundle
func (a *JFPAdapter) Bundle(ctx context.Context, id string) (json.RawMessage, error) {
	return a.runCommand(ctx, "bundle", id, "--json")
}

// runCommand executes a jfp command and returns raw JSON
func (a *JFPAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start jfp: %w", err)
	}

	// Limit output to 10MB
	const maxOutput = 10 * 1024 * 1024
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, maxOutput+1))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to read jfp output: %w", err)
	}
	if len(output) > maxOutput {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("jfp output exceeded limit of %d bytes", maxOutput)
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("jfp %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from jfp", ErrSchemaValidation)
	}

	return output, nil
}
