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

// PTAdapter provides integration with the process_triage tool.
// process_triage uses Bayesian classification to identify useful, abandoned, and zombie processes.
type PTAdapter struct {
	*BaseAdapter
}

// NewPTAdapter creates a new process_triage adapter
func NewPTAdapter() *PTAdapter {
	return &PTAdapter{
		BaseAdapter: NewBaseAdapter(ToolPT, "pt"),
	}
}

// Detect checks if pt is installed
func (a *PTAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed pt version
func (a *PTAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get pt version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of pt capabilities
func (a *PTAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{}

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
	if strings.Contains(output, "--json") || strings.Contains(output, "classify") {
		caps = append(caps, CapRobotMode)
	}
	if strings.Contains(output, "daemon") || strings.Contains(output, "watch") {
		caps = append(caps, CapDaemonMode)
	}

	return caps, nil
}

// Health checks if pt is functioning correctly
func (a *PTAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "pt not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a basic health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("pt at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	// Try a basic classify call to verify functionality
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try self-classify as a sanity check (classify our own process)
	cmd := exec.CommandContext(ctx2, path, "health", "--json")
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// health command might not exist, try version-based health
		return &HealthStatus{
			Healthy:     true,
			Message:     "pt is healthy (version check passed)",
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "pt is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if pt has a specific capability
func (a *PTAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete pt tool information
func (a *PTAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// pt-specific types and methods

// PTStatus represents the availability and compatibility of pt on PATH.
type PTStatus struct {
	Available   bool      `json:"available"`
	Compatible  bool      `json:"compatible"`
	Version     Version   `json:"version,omitempty"`
	Path        string    `json:"path,omitempty"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

// PTClassification represents a process classification result
type PTClassification string

const (
	PTClassUseful    PTClassification = "useful"
	PTClassAbandoned PTClassification = "abandoned"
	PTClassZombie    PTClassification = "zombie"
	PTClassUnknown   PTClassification = "unknown"
)

// PTProcessResult represents classification result for a single process
type PTProcessResult struct {
	PID            int              `json:"pid"`
	Name           string           `json:"name,omitempty"`
	Classification PTClassification `json:"classification"`
	Confidence     float64          `json:"confidence"` // 0.0 to 1.0
	Reason         string           `json:"reason,omitempty"`
}

var (
	ptStatusCache  PTStatus
	ptStatusExpiry time.Time
	ptStatusMutex  sync.RWMutex
	ptStatusTTL    = 5 * time.Minute
	ptMinVersion   = Version{Major: 0, Minor: 1, Patch: 0}
	ptLogger       = slog.Default().With("component", "tools.pt")
)

// GetStatus returns the current pt status with caching
func (a *PTAdapter) GetStatus(ctx context.Context) (*PTStatus, error) {
	ptStatusMutex.RLock()
	if time.Now().Before(ptStatusExpiry) {
		status := ptStatusCache
		ptStatusMutex.RUnlock()
		return &status, nil
	}
	ptStatusMutex.RUnlock()

	status := a.fetchStatus(ctx)

	ptStatusMutex.Lock()
	ptStatusCache = *status
	ptStatusExpiry = time.Now().Add(ptStatusTTL)
	ptStatusMutex.Unlock()

	return status, nil
}

// InvalidateStatusCache forces the next GetStatus call to re-check
func (a *PTAdapter) InvalidateStatusCache() {
	ptStatusMutex.Lock()
	ptStatusExpiry = time.Time{}
	ptStatusMutex.Unlock()
}

// IsAvailable returns true if pt is installed and compatible
func (a *PTAdapter) IsAvailable(ctx context.Context) bool {
	status, err := a.GetStatus(ctx)
	if err != nil || status == nil {
		return false
	}
	return status.Available && status.Compatible
}

func (a *PTAdapter) fetchStatus(ctx context.Context) *PTStatus {
	status := &PTStatus{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		status.Error = err.Error()
		ptLogger.Debug("pt binary not found", "error", err)
		return status
	}

	status.Available = true
	status.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		status.Error = err.Error()
		ptLogger.Warn("pt version check failed", "path", path, "error", err)
		return status
	}

	status.Version = version
	if !ptCompatible(version) {
		ptLogger.Warn("pt version incompatible", "path", path, "version", version.String(), "min_version", ptMinVersion.String())
		return status
	}

	status.Compatible = true
	return status
}

func ptCompatible(version Version) bool {
	return version.AtLeast(ptMinVersion)
}

// ClassifyProcess classifies a single process by PID
func (a *PTAdapter) ClassifyProcess(ctx context.Context, pid int) (*PTProcessResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "classify", "--pid", fmt.Sprintf("%d", pid), "--json")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("pt classify failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid JSON output from pt classify")
	}

	var result PTProcessResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse pt classify result: %w", err)
	}

	return &result, nil
}

// ClassifyProcesses classifies multiple processes
func (a *PTAdapter) ClassifyProcesses(ctx context.Context, pids []int) ([]PTProcessResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	// Build PID args
	args := []string{"classify", "--json"}
	for _, pid := range pids {
		args = append(args, "--pid", fmt.Sprintf("%d", pid))
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
		return nil, fmt.Errorf("pt classify failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid JSON output from pt classify")
	}

	var results []PTProcessResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse pt classify results: %w", err)
	}

	return results, nil
}

// WatchSession monitors agent processes in a session
func (a *PTAdapter) WatchSession(ctx context.Context, sessionName string) ([]PTProcessResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "watch", "--session", sessionName, "--json", "--once")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("pt watch failed: %w: %s", err, stderr.String())
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid JSON output from pt watch")
	}

	var results []PTProcessResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse pt watch results: %w", err)
	}

	return results, nil
}
