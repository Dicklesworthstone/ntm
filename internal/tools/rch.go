package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// RCHAdapter provides integration with the Remote Compilation Helper (rch) tool.
// RCH offloads build commands to remote workers for faster compilation.
type RCHAdapter struct {
	*BaseAdapter
}

// NewRCHAdapter creates a new RCH adapter
func NewRCHAdapter() *RCHAdapter {
	return &RCHAdapter{
		BaseAdapter: NewBaseAdapter(ToolRCH, "rch"),
	}
}

// Detect checks if rch is installed
func (a *RCHAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed rch version
func (a *RCHAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get rch version: %w", err)
	}

	return parseVersion(stdout.String())
}

// Capabilities returns the list of rch capabilities
func (a *RCHAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "help")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // Ignore error, just check output

	output := stdout.String()

	// Check for known capabilities
	if strings.Contains(output, "--json") || strings.Contains(output, "status") {
		caps = append(caps, CapRobotMode)
	}

	return caps, nil
}

// Health checks if rch is functioning correctly
func (a *RCHAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rch not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("rch at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "rch is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if rch has a specific capability
func (a *RCHAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete rch tool information
func (a *RCHAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// RCH-specific types and methods

// RCHWorker represents a remote compilation worker
type RCHWorker struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"`
	Available bool   `json:"available"`
	Healthy   bool   `json:"healthy"`
	Load      int    `json:"load,omitempty"`      // 0-100 load percentage
	Queue     int    `json:"queue,omitempty"`     // Jobs in queue
	LastSeen  string `json:"last_seen,omitempty"` // ISO timestamp
}

// RCHStatus represents the current RCH status including workers
type RCHStatus struct {
	Enabled      bool        `json:"enabled"`
	WorkerCount  int         `json:"worker_count"`
	HealthyCount int         `json:"healthy_count"`
	Workers      []RCHWorker `json:"workers,omitempty"`
}

// RCHAvailability represents the availability and compatibility of rch on PATH.
type RCHAvailability struct {
	Available    bool      `json:"available"`
	Compatible   bool      `json:"compatible"`
	Version      Version   `json:"version,omitempty"`
	Path         string    `json:"path,omitempty"`
	WorkerCount  int       `json:"worker_count"`
	HealthyCount int       `json:"healthy_count"`
	LastChecked  time.Time `json:"last_checked"`
	Error        string    `json:"error,omitempty"`
}

var (
	rchAvailabilityCache  RCHAvailability
	rchAvailabilityExpiry time.Time
	rchAvailabilityMutex  sync.RWMutex
	rchAvailabilityTTL    = 30 * time.Second // Workers may come/go, so shorter TTL than DCG
	rchMinVersion         = Version{Major: 0, Minor: 1, Patch: 0}
	rchLogger             = slog.Default().With("component", "tools.rch")
)

// GetAvailability returns whether rch is available and compatible, with caching.
// It also checks worker availability since workers may come and go.
func (a *RCHAdapter) GetAvailability(ctx context.Context) (*RCHAvailability, error) {
	rchAvailabilityMutex.RLock()
	if time.Now().Before(rchAvailabilityExpiry) {
		availability := rchAvailabilityCache
		rchAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	rchAvailabilityMutex.RUnlock()

	availability := a.fetchAvailability(ctx)

	rchAvailabilityMutex.Lock()
	rchAvailabilityCache = *availability
	rchAvailabilityExpiry = time.Now().Add(rchAvailabilityTTL)
	rchAvailabilityMutex.Unlock()

	return availability, nil
}

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *RCHAdapter) InvalidateAvailabilityCache() {
	rchAvailabilityMutex.Lock()
	rchAvailabilityExpiry = time.Time{}
	rchAvailabilityMutex.Unlock()
}

// IsAvailable returns true if rch is installed and compatible.
func (a *RCHAdapter) IsAvailable(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.Available && availability.Compatible
}

// HasHealthyWorkers returns true if there are any healthy workers available.
func (a *RCHAdapter) HasHealthyWorkers(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.HealthyCount > 0
}

func (a *RCHAdapter) fetchAvailability(ctx context.Context) *RCHAvailability {
	availability := &RCHAvailability{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		availability.Error = err.Error()
		rchLogger.Debug("rch binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		rchLogger.Warn("rch version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !rchCompatible(version) {
		rchLogger.Warn("rch version incompatible", "path", path, "version", version.String(), "min_version", rchMinVersion.String())
		return availability
	}

	availability.Compatible = true

	// Check worker availability
	status, err := a.GetStatus(ctx)
	if err == nil {
		availability.WorkerCount = status.WorkerCount
		availability.HealthyCount = status.HealthyCount
	}

	return availability
}

func rchCompatible(version Version) bool {
	return version.AtLeast(rchMinVersion)
}

// GetStatus returns the current RCH status including worker information
func (a *RCHAdapter) GetStatus(ctx context.Context) (*RCHStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		// RCH might not have a status command or no workers configured
		return &RCHStatus{Enabled: true, WorkerCount: 0, HealthyCount: 0}, nil
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		// Return default status if output is not valid JSON
		return &RCHStatus{Enabled: true, WorkerCount: 0, HealthyCount: 0}, nil
	}

	var status RCHStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse rch status: %w", err)
	}

	// Count healthy workers if not already set
	if status.HealthyCount == 0 && len(status.Workers) > 0 {
		for _, w := range status.Workers {
			if w.Healthy && w.Available {
				status.HealthyCount++
			}
		}
	}

	return &status, nil
}

// GetWorkers returns the list of configured workers
func (a *RCHAdapter) GetWorkers(ctx context.Context) ([]RCHWorker, error) {
	status, err := a.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	return status.Workers, nil
}

// SelectWorker returns the best available worker for a build
func (a *RCHAdapter) SelectWorker(ctx context.Context, preferred string) (*RCHWorker, error) {
	workers, err := a.GetWorkers(ctx)
	if err != nil {
		return nil, err
	}

	// If a preferred worker is specified and available, use it
	if preferred != "" && preferred != "auto" {
		for _, w := range workers {
			if w.Name == preferred && w.Available && w.Healthy {
				return &w, nil
			}
		}
		// Preferred worker not available, fall through to auto selection
		rchLogger.Debug("preferred worker not available", "preferred", preferred)
	}

	// Auto-select: find the healthiest worker with lowest load
	var best *RCHWorker
	for i := range workers {
		w := &workers[i]
		if !w.Available || !w.Healthy {
			continue
		}
		if best == nil || w.Load < best.Load {
			best = w
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no healthy workers available")
	}

	return best, nil
}
