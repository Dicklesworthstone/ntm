package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// PTAdapter provides integration with the process_triage tool.
// The passive watch contract supplies suspected-abandonment candidates, not a
// complete classification of every process and never authority to terminate it.
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

	output := helpText(ctx, a.Timeout(), path, "--help")

	// Check for known capabilities; pt's robot surface is `pt agent ...`
	// ("Agent/robot subcommands").
	if strings.Contains(output, "--json") || strings.Contains(output, "Agent/robot") {
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

	// Discover the actual passive surface without scanning or creating a plan.
	if err := ptWatchSupported(ctx, path, a.Timeout()); err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     "pt passive watch unavailable",
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "pt passive watch supported (no process sample collected)",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
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
	Source         string           `json:"source,omitempty"`
	Recommendation string           `json:"recommendation,omitempty"`
	// Watch confidence is P(abandoned), not confidence that spare == useful.
	AbandonmentProbability *float64 `json:"abandonment_probability,omitempty"`
}

var (
	ptStatusCache  PTStatus
	ptStatusExpiry time.Time
	ptStatusMutex  sync.RWMutex
	ptStatusTTL    = 5 * time.Minute
	ptMinVersion   = Version{Major: 0, Minor: 1, Patch: 0}
)

func ptLogger() *slog.Logger {
	return slog.Default().With("component", "tools.pt")
}

// GetStatus returns the current pt status with caching
func (a *PTAdapter) GetStatus(ctx context.Context) (*PTStatus, error) {
	if ctx == nil {
		return nil, errors.New("pt status requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ptStatusMutex.RLock()
	if time.Now().Before(ptStatusExpiry) {
		status := ptStatusCache
		ptStatusMutex.RUnlock()
		return &status, nil
	}
	ptStatusMutex.RUnlock()

	status := a.fetchStatus(ctx)
	// An interrupted probe must not disable every later reader for five minutes.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

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
		ptLogger().Debug("pt binary not found", "error", err)
		return status
	}

	status.Available = true
	status.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		status.Error = err.Error()
		ptLogger().Warn("pt version check failed", "path", path, "error", err)
		return status
	}

	status.Version = version
	if !ptCompatible(version) {
		ptLogger().Warn("pt version incompatible", "path", path, "version", version.String(), "min_version", ptMinVersion.String())
		return status
	}

	if err := ptWatchSupported(ctx, path, a.Timeout()); err != nil {
		status.Error = err.Error()
		return status
	}
	status.Compatible = true
	return status
}

func ptCompatible(version Version) bool {
	return version.AtLeast(ptMinVersion)
}

// ClassifyProcesses samples one non-persistent, recommendation-only PT watch
// iteration for the entire host, then selects the requested PIDs. Unlike agent
// plan, watch does not create a new session directory on every monitor poll.
// Omitted PIDs, review and spare recommendations remain unknown: watch filters
// protected/young/low-posterior processes and cannot certify their health.
func (a *PTAdapter) ClassifyProcesses(ctx context.Context, pids []int) ([]PTProcessResult, error) {
	if ctx == nil {
		return nil, errors.New("pt watch requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wanted := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		if pid <= 0 || uint64(pid) > math.MaxUint32 {
			return nil, fmt.Errorf("invalid process PID %d", pid)
		}
		wanted[pid] = struct{}{}
	}
	if len(wanted) == 0 {
		return []PTProcessResult{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()
	started := time.Now()
	cmd := exec.CommandContext(ctx, a.BinaryName(), "agent", "watch", "--once", "--threshold", "low", "--format", "jsonl")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	stderr := NewLimitedBuffer(64 * 1024)
	// Hide bytes.Buffer's promoted ReadFrom: io.Copy would bypass Write's
	// limit through that fast path, especially for stderr-only failures.
	cmd.Stdout = struct{ io.Writer }{stdout}
	cmd.Stderr = struct{ io.Writer }{stderr}
	err := cmd.Run()
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.Join(ErrTimeout, ctx.Err())
		}
		return nil, ctx.Err()
	}
	if err != nil {
		// No prefix success and no raw stderr/command-line disclosure. Watch
		// exits zero on a successful snapshot; plan's exit-one contract differs.
		return nil, fmt.Errorf("pt passive watch failed: %w", err)
	}
	return parsePTWatch(stdout.Bytes(), wanted, started, time.Now())
}

func ptWatchSupported(ctx context.Context, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "agent", "watch", "--help")
	cmd.WaitDelay = time.Second
	stdout, stderr := NewLimitedBuffer(64*1024), NewLimitedBuffer(64*1024)
	cmd.Stdout, cmd.Stderr = struct{ io.Writer }{stdout}, struct{ io.Writer }{stderr}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("pt watch discovery failed: %w", err)
	}
	help := stdout.String() + stderr.String()
	for _, flag := range []string{"--once", "--threshold", "--format"} {
		if !strings.Contains(help, flag) {
			return fmt.Errorf("%w: pt agent watch lacks %s", ErrCapabilityMissing, flag)
		}
	}
	return nil
}

func parsePTWatch(data []byte, wanted map[int]struct{}, started, finished time.Time) ([]PTProcessResult, error) {
	byPID := make(map[int]PTProcessResult, len(wanted))
	seen := make(map[int]bool)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64*1024)
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event struct {
			Event          string   `json:"event"`
			PID            int      `json:"pid"`
			Classification string   `json:"classification"`
			Confidence     *float64 `json:"confidence"`
			Timestamp      string   `json:"timestamp"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return nil, fmt.Errorf("%w: invalid pt watch record %d", ErrSchemaValidation, line)
		}
		switch event.Event {
		case "goal_violated", "baseline_anomaly":
			continue // Host events are not process classifications.
		case "candidate_detected":
		default:
			return nil, fmt.Errorf("%w: unexpected pt watch event on record %d", ErrSchemaValidation, line)
		}
		stamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil || stamp.Before(started.Add(-5*time.Second)) || stamp.After(finished.Add(5*time.Second)) ||
			event.PID <= 0 || uint64(event.PID) > math.MaxUint32 || event.Confidence == nil ||
			math.IsNaN(*event.Confidence) || math.IsInf(*event.Confidence, 0) || *event.Confidence < 0.5 || *event.Confidence > 1 || seen[event.PID] {
			return nil, fmt.Errorf("%w: invalid or repeated pt watch candidate on record %d", ErrSchemaValidation, line)
		}
		seen[event.PID] = true
		classification := PTClassUnknown
		confidence := 0.0
		switch event.Classification {
		case "kill":
			classification, confidence = PTClassAbandoned, *event.Confidence
		case "spare", "review":
		default:
			return nil, fmt.Errorf("%w: unsupported pt recommendation on record %d", ErrSchemaValidation, line)
		}
		if _, ok := wanted[event.PID]; ok {
			byPID[event.PID] = PTProcessResult{
				PID: event.PID, Classification: classification, Confidence: confidence,
				Source: "pt_agent_watch", Recommendation: event.Classification,
				AbandonmentProbability: event.Confidence,
				Reason:                 fmt.Sprintf("PT watch recommends %s (abandonment probability %.3f); advisory only", event.Classification, *event.Confidence),
			}
		}
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("%w: pt watch record exceeds limit", ErrOutputLimitExceeded)
	}
	results := make([]PTProcessResult, 0, len(wanted))
	for pid := range wanted {
		result, ok := byPID[pid]
		if !ok {
			result = PTProcessResult{PID: pid, Classification: PTClassUnknown, Source: "pt_agent_watch",
				Reason: "not reported by thresholded PT watch; health is unknown"}
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].PID < results[j].PID })
	return results, nil
}
