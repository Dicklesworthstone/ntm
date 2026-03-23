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

// RanoAdapter provides integration with the rano network observer tool.
// rano monitors network traffic per-process, enabling per-agent API tracking.
type RanoAdapter struct {
	*BaseAdapter
}

// NewRanoAdapter creates a new rano adapter
func NewRanoAdapter() *RanoAdapter {
	return &RanoAdapter{
		BaseAdapter: NewBaseAdapter(ToolRano, "rano"),
	}
}

// Detect checks if rano is installed
func (a *RanoAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed rano version
func (a *RanoAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get rano version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of rano capabilities
func (a *RanoAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

	path, installed := a.Detect()
	if !installed {
		return caps, nil
	}

	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "help")
	cmd.WaitDelay = time.Second
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

// Health checks if rano is functioning correctly
func (a *RanoAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rano not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("rano at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	// Also check if rano has required permissions
	hasPerms := a.checkPermissions(ctx)
	if !hasPerms {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rano lacks required capabilities (CAP_NET_ADMIN)",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "rano is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if rano has a specific capability
func (a *RanoAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete rano tool information
func (a *RanoAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// rano-specific types and methods

// RanoAvailability represents the availability and compatibility of rano on PATH.
type RanoAvailability struct {
	Available     bool      `json:"available"`
	Compatible    bool      `json:"compatible"`
	HasCapability bool      `json:"has_capability"` // Has CAP_NET_ADMIN
	CanReadProc   bool      `json:"can_read_proc"`  // Can read /proc for PID mapping
	Version       Version   `json:"version,omitempty"`
	Path          string    `json:"path,omitempty"`
	LastChecked   time.Time `json:"last_checked"`
	Error         string    `json:"error,omitempty"`
}

// RanoStatus represents the current rano status
type RanoStatus struct {
	Running      bool   `json:"running"`
	Monitoring   bool   `json:"monitoring"`
	ProcessCount int    `json:"process_count"` // Number of processes being tracked
	RequestCount int    `json:"request_count"` // Total API requests observed
	BytesIn      int64  `json:"bytes_in"`      // Total bytes received
	BytesOut     int64  `json:"bytes_out"`     // Total bytes sent
	Error        string `json:"error,omitempty"`
}

// RanoProcessStats represents network stats for a single process/agent
type RanoProcessStats struct {
	PID          int    `json:"pid"`
	ProcessName  string `json:"process_name,omitempty"`
	RequestCount int    `json:"request_count"`
	BytesIn      int64  `json:"bytes_in"`
	BytesOut     int64  `json:"bytes_out"`
	LastRequest  string `json:"last_request,omitempty"` // ISO timestamp
}

var (
	ranoAvailabilityCache  RanoAvailability
	ranoAvailabilityExpiry time.Time
	ranoAvailabilityMutex  sync.RWMutex
	ranoAvailabilityTTL    = 2 * time.Minute // Shorter TTL since permissions may change
	ranoMinVersion         = Version{Major: 0, Minor: 1, Patch: 0}
	ranoLogger             = slog.Default().With("component", "tools.rano")
)

// GetAvailability returns whether rano is available and compatible, with caching.
func (a *RanoAdapter) GetAvailability(ctx context.Context) (*RanoAvailability, error) {
	ranoAvailabilityMutex.RLock()
	if time.Now().Before(ranoAvailabilityExpiry) {
		availability := ranoAvailabilityCache
		ranoAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	ranoAvailabilityMutex.RUnlock()

	availability := a.fetchAvailability(ctx)

	ranoAvailabilityMutex.Lock()
	ranoAvailabilityCache = *availability
	ranoAvailabilityExpiry = time.Now().Add(ranoAvailabilityTTL)
	ranoAvailabilityMutex.Unlock()

	return availability, nil
}

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *RanoAdapter) InvalidateAvailabilityCache() {
	ranoAvailabilityMutex.Lock()
	ranoAvailabilityExpiry = time.Time{}
	ranoAvailabilityMutex.Unlock()
}

// IsAvailable returns true if rano is installed, compatible, and has required permissions.
func (a *RanoAdapter) IsAvailable(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.Available && availability.Compatible && availability.HasCapability
}

// HasRequiredPermissions returns true if rano has the required capabilities.
func (a *RanoAdapter) HasRequiredPermissions(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.HasCapability
}

func (a *RanoAdapter) fetchAvailability(ctx context.Context) *RanoAvailability {
	availability := &RanoAvailability{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		availability.Error = err.Error()
		ranoLogger.Debug("rano binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		ranoLogger.Warn("rano version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !ranoCompatible(version) {
		ranoLogger.Warn("rano version incompatible", "path", path, "version", version.String(), "min_version", ranoMinVersion.String())
		return availability
	}

	availability.Compatible = true

	// Check permissions
	availability.HasCapability = a.checkPermissions(ctx)
	availability.CanReadProc = a.checkProcAccess()

	if !availability.HasCapability {
		ranoLogger.Warn("rano lacks required capabilities", "path", path)
	}

	return availability
}

func ranoCompatible(version Version) bool {
	return version.AtLeast(ranoMinVersion)
}

// checkPermissions checks if rano has the required CAP_NET_ADMIN capability
func (a *RanoAdapter) checkPermissions(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status", "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := stderr.String()
		// Check for permission-related errors
		if strings.Contains(errStr, "permission") ||
			strings.Contains(errStr, "CAP_NET") ||
			strings.Contains(errStr, "Operation not permitted") ||
			strings.Contains(errStr, "EPERM") {
			return false
		}
		// Other errors might not be permission-related
		ranoLogger.Debug("rano status check failed", "error", err, "stderr", errStr)
		// Still return false as we can't confirm it works
		return false
	}

	return true
}

// checkProcAccess checks if we can read /proc for PID mapping
func (a *RanoAdapter) checkProcAccess() bool {
	// Try to read /proc/self as a basic check
	cmd := exec.Command("ls", "/proc/self")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// GetStatus returns the current rano status
func (a *RanoAdapter) GetStatus(ctx context.Context) (*RanoStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status", "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		errStr := stderr.String()
		// Check if it's a permission error
		if strings.Contains(errStr, "permission") || strings.Contains(errStr, "CAP_NET") {
			return &RanoStatus{
				Running:    false,
				Monitoring: false,
				Error:      "missing required capabilities (CAP_NET_ADMIN)",
			}, nil
		}
		return nil, fmt.Errorf("rano status failed: %w: %s", err, errStr)
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return &RanoStatus{Running: true, Monitoring: false}, nil
	}

	var status RanoStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("failed to parse rano status: %w", err)
	}

	return &status, nil
}

// GetProcessStats returns network stats for a specific PID.
// Window is optional; empty means default window.
func (a *RanoAdapter) GetProcessStats(ctx context.Context, pid int) (*RanoProcessStats, error) {
	return a.GetProcessStatsWithWindow(ctx, pid, "")
}

// GetProcessStatsWithWindow returns network stats for a specific PID with a time window override.
// Window should be a string like "5m", "1h". Empty means default window.
func (a *RanoAdapter) GetProcessStatsWithWindow(ctx context.Context, pid int, window string) (*RanoProcessStats, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	args := []string{"stats", "--pid", fmt.Sprintf("%d", pid), "--json"}
	if window != "" {
		args = append(args, "--window", window)
	}

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
		return nil, fmt.Errorf("rano stats failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid JSON output from rano stats")
	}

	var stats RanoProcessStats
	if err := json.Unmarshal(output, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse rano stats: %w", err)
	}

	return &stats, nil
}

// GetAllProcessStats returns network stats for all tracked processes.
// Window is optional; empty means default window.
func (a *RanoAdapter) GetAllProcessStats(ctx context.Context) ([]RanoProcessStats, error) {
	return a.GetAllProcessStatsWithWindow(ctx, "")
}

// GetAllProcessStatsWithWindow returns network stats for all tracked processes with a time window override.
// Window should be a string like "5m", "1h". Empty means default window.
func (a *RanoAdapter) GetAllProcessStatsWithWindow(ctx context.Context, window string) ([]RanoProcessStats, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	args := []string{"stats", "--all", "--json"}
	if window != "" {
		args = append(args, "--window", window)
	}

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
		return nil, fmt.Errorf("rano stats failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid JSON output from rano stats")
	}

	var stats []RanoProcessStats
	if err := json.Unmarshal(output, &stats); err != nil {
		return nil, fmt.Errorf("failed to parse rano stats: %w", err)
	}

	return stats, nil
}
