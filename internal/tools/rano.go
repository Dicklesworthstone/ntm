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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// RanoAdapter provides integration with the rano network observer tool.
// rano's monitor records socket connect/close events per process into an
// SQLite database; ntm reads that history through `rano export`, enabling
// per-agent connection attribution. ntm never starts the monitor itself.
type RanoAdapter struct {
	*BaseAdapter
	database string // observer database passed to `rano export --sqlite`; empty = RanoDefaultDatabase
}

// RanoDefaultDatabase is rano's own default observer database. rano's monitor,
// export, report and status commands all resolve it against their working
// directory unless --sqlite (or the monitor's sqlite= config key) overrides it.
const RanoDefaultDatabase = "observer.sqlite"

// ErrRanoNoDatabase reports that no rano observer database exists at the
// selected path, so there are no recorded connection observations to read.
var ErrRanoNoDatabase = errors.New("rano observer database not found")

// NewRanoAdapter creates a new rano adapter
func NewRanoAdapter() *RanoAdapter {
	return &RanoAdapter{
		BaseAdapter: NewBaseAdapter(ToolRano, "rano"),
	}
}

// SetDatabase selects the observer database the adapter reads (a leading ~ is
// expanded). Empty selects rano's default, observer.sqlite in the working
// directory. Configure it with [integrations.rano] sqlite_path.
func (a *RanoAdapter) SetDatabase(path string) {
	a.database = config.ExpandHome(strings.TrimSpace(path))
}

// Database returns the absolute path of the observer database the adapter reads.
func (a *RanoAdapter) Database() (string, error) {
	path := a.database
	if path == "" {
		path = RanoDefaultDatabase
	}
	return filepath.Abs(path)
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

	// rano has no `help` subcommand ("Unexpected argument: help").
	output := helpText(ctx, a.Timeout(), path, "--help")

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

	// Confirm rano is actually operational by running its status command.
	// A failure here (other than a genuine permission error) means rano cannot
	// function on this host.
	if !a.checkOperational(ctx) {
		return &HealthStatus{
			Healthy:     false,
			Message:     "rano status check failed",
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

// Info returns complete rano tool information
func (a *RanoAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// rano-specific types and methods

// RanoAvailability represents the availability and compatibility of rano on PATH.
type RanoAvailability struct {
	Available   bool      `json:"available"`
	Compatible  bool      `json:"compatible"`
	Operational bool      `json:"operational"`   // `rano status` succeeded; no kernel capability is required
	CanReadProc bool      `json:"can_read_proc"` // Can read /proc for PID mapping
	Version     Version   `json:"version,omitempty"`
	Path        string    `json:"path,omitempty"`
	LastChecked time.Time `json:"last_checked"`
	Error       string    `json:"error,omitempty"`
}

// RanoProcessStats aggregates one process's recorded connection events. rano
// records socket connect/close events, not HTTP requests or transferred bytes,
// so neither is measured here.
type RanoProcessStats struct {
	PID             int            `json:"pid"`
	ProcessName     string         `json:"process_name,omitempty"`
	ConnectionCount int            `json:"connection_count"`
	LastConnection  string         `json:"last_connection,omitempty"`
	Providers       map[string]int `json:"providers,omitempty"`
}

var (
	ranoAvailabilityCache  RanoAvailability
	ranoAvailabilityExpiry time.Time
	ranoAvailabilityMutex  sync.RWMutex
	ranoAvailabilityTTL    = 2 * time.Minute // Shorter TTL since permissions may change
	ranoMinVersion         = Version{Major: 0, Minor: 1, Patch: 0}
)

func ranoLogger() *slog.Logger {
	return slog.Default().With("component", "tools.rano")
}

// GetAvailability returns whether rano is available and compatible, with caching.
func (a *RanoAdapter) GetAvailability(ctx context.Context) (*RanoAvailability, error) {
	ranoAvailabilityMutex.RLock()
	if time.Now().Before(ranoAvailabilityExpiry) {
		availability := ranoAvailabilityCache
		ranoAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	ranoAvailabilityMutex.RUnlock()

	ranoAvailabilityMutex.Lock()
	defer ranoAvailabilityMutex.Unlock()

	if time.Now().Before(ranoAvailabilityExpiry) {
		availability := ranoAvailabilityCache
		return &availability, nil
	}

	availability := a.fetchAvailability(ctx)

	ranoAvailabilityCache = *availability
	ranoAvailabilityExpiry = time.Now().Add(ranoAvailabilityTTL)

	return availability, nil
}

// IsAvailable returns true if rano is installed, compatible, and operational.
func (a *RanoAdapter) IsAvailable(ctx context.Context) bool {
	availability, err := a.GetAvailability(ctx)
	if err != nil || availability == nil {
		return false
	}
	return availability.Available && availability.Compatible && availability.Operational
}

func (a *RanoAdapter) fetchAvailability(ctx context.Context) *RanoAvailability {
	availability := &RanoAvailability{
		LastChecked: time.Now(),
	}

	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		availability.Error = err.Error()
		ranoLogger().Debug("rano binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		ranoLogger().Warn("rano version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !ranoCompatible(version) {
		ranoLogger().Warn("rano version incompatible", "path", path, "version", version.String(), "min_version", ranoMinVersion.String())
		return availability
	}

	availability.Compatible = true

	// Confirm rano is operational. rano's default observation mode enumerates
	// sockets via /proc and needs no elevated capabilities for same-user
	// processes, so a successful `rano status` is the correct availability
	// signal.
	availability.Operational = a.checkOperational(ctx)
	availability.CanReadProc = a.checkProcAccess()

	if !availability.Operational {
		ranoLogger().Warn("rano status check failed", "path", path)
	}

	return availability
}

func ranoCompatible(version Version) bool {
	return version.AtLeast(ranoMinVersion)
}

// checkOperational verifies rano is functional by running its status command.
//
// The current rano CLI exposes `rano status` (a one-line prompt-integration
// status). The older `rano status --json` form was removed: passing --json now
// errors with "Unknown status flag: --json" (exit 1), which previously made
// every availability/health check fail — silently disabling rano integration
// fleet-wide and surfacing a false "lacks CAP_NET_ADMIN" warning in
// `ntm doctor` (issue #202). `rano status` exits 0 on a healthy host even with
// no observer running, so its success is the correct operational signal.
func (a *RanoAdapter) checkOperational(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "status")
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Surface genuine permission failures distinctly for logging, but the
		// return value is the same: rano is not operational here.
		errStr := stderr.String()
		if strings.Contains(errStr, "permission") ||
			strings.Contains(errStr, "CAP_NET") ||
			strings.Contains(errStr, "Operation not permitted") ||
			strings.Contains(errStr, "EPERM") {
			ranoLogger().Debug("rano status blocked by permissions", "stderr", errStr)
			return false
		}
		ranoLogger().Debug("rano status check failed", "error", err, "stderr", errStr)
		return false
	}

	return true
}

// checkProcAccess checks if we can read /proc for PID mapping
func (a *RanoAdapter) checkProcAccess() bool {
	// This is a filesystem probe, not an unbounded subprocess from PATH.
	info, err := os.Stat("/proc/self")
	return err == nil && info.IsDir()
}

// GetAllProcessStats returns network stats for all tracked processes.
// Window is optional; empty means default window.
func (a *RanoAdapter) GetAllProcessStats(ctx context.Context) ([]RanoProcessStats, error) {
	return a.GetAllProcessStatsWithWindow(ctx, "")
}

// GetAllProcessStatsWithWindow aggregates persisted connection observations,
// not HTTP traffic. Rano has no stats subcommand; its supported JSONL export
// (`rano export --format jsonl --sqlite DB`) supplies PID, timestamp, event
// kind and provider. One bounded export serves the whole fleet, and the time
// range is rechecked after decoding. A missing observer database is reported
// as ErrRanoNoDatabase rather than as zero traffic. This never starts an
// observer or enables packet capture. Export itself may migrate optional
// columns in an older database; it is not a read-only SQLite API.
func (a *RanoAdapter) GetAllProcessStatsWithWindow(ctx context.Context, window string) ([]RanoProcessStats, error) {
	if ctx == nil {
		return nil, errors.New("rano export requires a context")
	}
	duration, err := RanoWindowDuration(window)
	if err != nil {
		return nil, err
	}
	database, err := a.Database()
	if err != nil {
		return nil, fmt.Errorf("resolve rano observer database: %w", err)
	}
	if info, statErr := os.Stat(database); statErr != nil || info.IsDir() {
		return nil, fmt.Errorf("%w at %s: start rano's monitor with --sqlite %s, or point [integrations.rano] sqlite_path at its database",
			ErrRanoNoDatabase, database, database)
	}
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	until := time.Now().UTC()
	since := until.Add(-duration)
	// Rano filters text timestamps in SQLite. Widen the SQL interval by one
	// second so mixed whole/fractional-second encodings cannot lose a boundary
	// row; the reducer applies the exact [since, until) instants below.
	args := []string{"export", "--format", "jsonl", "--sqlite", database, "--fields", "ts,event,pid,comm,provider",
		"--since", since.Add(-time.Second).Format(time.RFC3339),
		"--until", until.Add(time.Second).Format(time.RFC3339)}
	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	stderr := NewLimitedBuffer(64 * 1024)
	// Writer-only wrappers hide bytes.Buffer's promoted ReadFrom, which io.Copy
	// would otherwise use to bypass LimitedBuffer.Write's cap.
	cmd.Stdout = struct{ io.Writer }{stdout}
	cmd.Stderr = struct{ io.Writer }{stderr}

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, errors.Join(ErrTimeout, ctx.Err())
			}
			return nil, ctx.Err()
		}
		if stdout.Len() >= stdout.Limit || stderr.Len() >= stderr.Limit {
			return nil, fmt.Errorf("rano export exceeded capture limit: %w", errors.Join(ErrOutputLimitExceeded, err))
		}
		// Do not echo arbitrary tool output (which can include command lines).
		return nil, fmt.Errorf("rano export --sqlite %s failed (run it by hand to see rano's error): %w", database, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return aggregateRanoExport(stdout.Bytes(), since, until)
}

// RanoWindowDuration is shared by the adapter and robot validation. Durations
// must be positive; d/w are whole-number extensions of Go's duration grammar.
// Checked multiplication prevents large day/week inputs from wrapping around.
func RanoWindowDuration(window string) (time.Duration, error) {
	if window == "" {
		window = "5m"
	}
	var d time.Duration
	var err error
	if strings.HasSuffix(window, "d") || strings.HasSuffix(window, "w") {
		unit := 24 * time.Hour
		if strings.HasSuffix(window, "w") {
			unit *= 7
		}
		var n uint64
		n, err = strconv.ParseUint(window[:len(window)-1], 10, 64)
		if err == nil && n > uint64((1<<63-1)/unit) {
			err = errors.New("duration overflow")
		}
		if err == nil {
			d = time.Duration(n) * unit
		}
	} else {
		d, err = time.ParseDuration(window)
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid rano window %q: use a positive duration such as 5m, 1h or 1d", window)
	}
	return d, nil
}

func aggregateRanoExport(data []byte, since, until time.Time) ([]RanoProcessStats, error) {
	byPID := make(map[int]*RanoProcessStats)
	latest := make(map[int]time.Time)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for line := 1; scanner.Scan(); line++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var row *struct {
			Timestamp string `json:"ts"`
			Event     string `json:"event"`
			PID       *int   `json:"pid"`
			Command   string `json:"comm"`
			Provider  string `json:"provider"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("rano export row %d: %w", line, err)
		}
		if row == nil || (row.Event != "connect" && row.Event != "close" && row.Event != "alert") {
			return nil, fmt.Errorf("rano export row %d: missing or unsupported event kind", line)
		}
		at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("rano export row %d: invalid timestamp", line)
		}
		// Threshold alerts may have no PID. Close events are not new
		// connections; counting them would double each completed connection.
		if row.Event != "connect" {
			continue
		}
		// Rano also persists unattributed sockets (NULL pid). They cannot
		// contribute to a per-process measurement and are deliberately omitted.
		if row.PID == nil {
			continue
		}
		if *row.PID <= 0 {
			return nil, fmt.Errorf("rano export row %d: connection has no positive PID", line)
		}
		if at.Before(since) || !at.Before(until) {
			continue
		}
		pid := *row.PID
		stat := byPID[pid]
		if stat == nil {
			stat = &RanoProcessStats{PID: pid, Providers: make(map[string]int)}
			byPID[pid] = stat
		}
		stat.ConnectionCount++
		provider := strings.ToLower(strings.TrimSpace(row.Provider))
		if provider == "" {
			provider = "unknown"
		}
		stat.Providers[provider]++
		if prev, ok := latest[pid]; !ok || at.After(prev) || (at.Equal(prev) && row.Command < stat.ProcessName) {
			latest[pid] = at
			stat.LastConnection = at.UTC().Format(time.RFC3339Nano)
			stat.ProcessName = row.Command
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("rano export is incomplete: %w", err)
	}
	stats := make([]RanoProcessStats, 0, len(byPID))
	for _, stat := range byPID {
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].PID < stats[j].PID })
	return stats, nil
}
