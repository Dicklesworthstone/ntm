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
		CapRobotMode, // ms -O json
		CapSearch,    // ms search <query>
		"suggest",    // ms suggest (context-aware, no task argument)
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

// Info returns complete ms tool information
func (a *MSAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// MS-specific methods

// MSSkillMatch is one entry of the results array printed by
// `ms -O json search`.
type MSSkillMatch struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	Layer        string  `json:"layer,omitempty"`
	Score        float64 `json:"score"`
	Quality      float64 `json:"quality,omitempty"`
	IsDeprecated bool    `json:"is_deprecated,omitempty"`
}

// Search searches for skills matching a query, best match first.
func (a *MSAdapter) Search(ctx context.Context, query string) ([]MSSkillMatch, error) {
	raw, err := a.runCommand(ctx, "search", "--", query)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []MSSkillMatch `json:"results"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: ms search: %v", ErrSchemaValidation, err)
	}
	if resp.Results == nil {
		return nil, fmt.Errorf("%w: ms search response has no results array", ErrSchemaValidation)
	}
	return resp.Results, nil
}

// Show returns the skill object from `ms -O json show`.
func (a *MSAdapter) Show(ctx context.Context, id string) (json.RawMessage, error) {
	raw, err := a.runCommand(ctx, "show", "--", id)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Skill json.RawMessage `json:"skill"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: ms show: %v", ErrSchemaValidation, err)
	}
	if len(resp.Skill) == 0 || string(resp.Skill) == "null" {
		return nil, fmt.Errorf("%w: ms show response has no skill", ErrSchemaValidation)
	}
	return resp.Skill, nil
}

// runCommand executes an ms subcommand with JSON output and returns stdout.
func (a *MSAdapter) runCommand(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	args = append([]string{"-O", "json"}, args...)
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
		detail := cliErrorLine(stderr.String())
		if detail == "" && len(stdout.Bytes()) > 0 {
			var errEnv struct {
				Message string `json:"message"`
				Error   any    `json:"error"`
			}
			if json.Unmarshal(stdout.Bytes(), &errEnv) == nil {
				if strings.TrimSpace(errEnv.Message) != "" {
					detail = strings.TrimSpace(errEnv.Message)
				} else if errStr, ok := errEnv.Error.(string); ok && strings.TrimSpace(errStr) != "" {
					detail = strings.TrimSpace(errStr)
				}
			}
			if detail == "" {
				detail = cliErrorLine(stdout.String())
			}
		}
		return nil, fmt.Errorf("ms %s failed: %w: %s", strings.Join(args, " "), err, detail)
	}

	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from ms", ErrSchemaValidation)
	}

	return output, nil
}
