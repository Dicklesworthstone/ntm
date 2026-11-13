package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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

	// No candidate responded to --version; report as not working.
	return candidates[0], false
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

// Info returns complete bd tool information
func (a *BDAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}
