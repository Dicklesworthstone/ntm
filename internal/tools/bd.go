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

// BDAdapter provides integration with the beads (bd) tool
type BDAdapter struct {
	*BaseAdapter
}

// NewBDAdapter creates a new BD adapter
func NewBDAdapter() *BDAdapter {
	return &BDAdapter{
		BaseAdapter: NewBaseAdapter(ToolBD, "bd"),
	}
}

func (a *BDAdapter) binaryCandidates() []string {
	seen := make(map[string]struct{}, 2)
	candidates := make([]string, 0, 2)

	for _, candidate := range []string{"br", a.BinaryName()} {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			candidates = append(candidates, path)
		}
	}
	return candidates
}

func (a *BDAdapter) versionFromBinary(ctx context.Context, binary string) (Version, error) {
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get beads_rust version from %s: %w", binary, err)
	}

	return ParseStandardVersion(stdout.String())
}

func (a *BDAdapter) workingBinary(ctx context.Context, candidates []string) (string, Version, error) {
	if len(candidates) == 0 {
		return "", Version{}, ErrToolNotInstalled
	}

	var lastErr error
	for _, candidate := range candidates {
		version, err := a.versionFromBinary(ctx, candidate)
		if err == nil {
			return candidate, version, nil
		}
		lastErr = err
	}

	return candidates[0], Version{}, lastErr
}

func (a *BDAdapter) resolveBinary() string {
	candidates := a.binaryCandidates()
	if len(candidates) == 0 {
		return a.BinaryName()
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.Timeout())
	defer cancel()

	if path, _, err := a.workingBinary(ctx, candidates); err == nil {
		return path
	}

	return candidates[0]
}

// Detect checks if bd is installed
func (a *BDAdapter) Detect() (string, bool) {
	candidates := a.binaryCandidates()
	if len(candidates) == 0 {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.Timeout())
	defer cancel()

	if path, _, err := a.workingBinary(ctx, candidates); err == nil {
		return path, true
	}

	return candidates[0], true
}

// Version returns the installed bd version
func (a *BDAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	path, version, err := a.workingBinary(ctx, a.binaryCandidates())
	if err != nil {
		if path == "" {
			return Version{}, fmt.Errorf("failed to get beads_rust version: %w", err)
		}
		return Version{}, fmt.Errorf("failed to get beads_rust version: %w", err)
	}
	return version, nil
}

// Capabilities returns the list of bd capabilities
func (a *BDAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	binary, _, err := a.workingBinary(ctx, a.binaryCandidates())
	if err != nil {
		return caps, nil
	}

	if a.supportsDaemonMode(ctx, binary) {
		caps = append(caps, CapDaemonMode)
	}

	return caps, nil
}

func (a *BDAdapter) supportsDaemonMode(ctx context.Context, binary string) bool {
	cmd := exec.CommandContext(ctx, binary, "help", "daemon")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return false
	}

	return true
}

// Health checks if bd is functioning correctly
func (a *BDAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "beads_rust (br) not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("beads_rust at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "beads_rust is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if bd has a specific capability
func (a *BDAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete bd tool information
func (a *BDAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// BD-specific methods

// GetStats returns bd stats output
func (a *BDAdapter) GetStats(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runCommand(ctx, dir, "stats", "--json")
}

// GetReady returns ready issues
func (a *BDAdapter) GetReady(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runCommand(ctx, dir, "ready", "--json")
}

// GetBlocked returns blocked issues
func (a *BDAdapter) GetBlocked(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runCommand(ctx, dir, "blocked", "--json")
}

// GetList returns issues matching filter
func (a *BDAdapter) GetList(ctx context.Context, dir string, status string) (json.RawMessage, error) {
	args := []string{"list", "--json"}
	if status != "" {
		args = append(args, "--status="+status)
	}
	return a.runCommand(ctx, dir, args...)
}

// Show returns details for a specific issue
func (a *BDAdapter) Show(ctx context.Context, dir, issueID string) (json.RawMessage, error) {
	return a.runCommand(ctx, dir, "show", issueID, "--json")
}

// runCommand executes a bd command and returns raw JSON
func (a *BDAdapter) runCommand(ctx context.Context, dir string, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	binary, _, err := a.workingBinary(ctx, a.binaryCandidates())
	if err != nil {
		return nil, fmt.Errorf("failed to find usable beads_rust binary: %w", err)
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.WaitDelay = time.Second
	if dir != "" {
		cmd.Dir = dir
	}

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
			return nil, fmt.Errorf("beads_rust output exceeded 10MB limit")
		}
		return nil, fmt.Errorf("beads_rust %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	// Validate JSON
	output := stdout.Bytes()
	if len(output) > 0 && !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from beads_rust", ErrSchemaValidation)
	}

	return output, nil
}
