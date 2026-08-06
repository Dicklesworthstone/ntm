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
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get rch version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
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
	Name            string `json:"name"`
	Host            string `json:"host,omitempty"`
	Available       bool   `json:"available"`
	Healthy         bool   `json:"healthy"`
	Load            int    `json:"load,omitempty"`             // 0-100 load percentage
	Queue           int    `json:"queue,omitempty"`            // Jobs in queue
	LastSeen        string `json:"last_seen,omitempty"`        // ISO timestamp
	CurrentBuild    string `json:"current_build,omitempty"`    // Current build command (if provided)
	BuildsCompleted int    `json:"builds_completed,omitempty"` // Total builds completed (if provided)
	CPUPercent      int    `json:"cpu_percent,omitempty"`      // Current CPU percent (if provided)
}

// RCHStatus represents the current RCH status including workers
type RCHStatus struct {
	Enabled      bool             `json:"enabled"`
	WorkerCount  int              `json:"worker_count"`
	HealthyCount int              `json:"healthy_count"`
	Workers      []RCHWorker      `json:"workers,omitempty"`
	SessionStats *RCHSessionStats `json:"session_stats,omitempty"`
}

// RCHSessionStats represents per-session build stats emitted by rch (if available).
type RCHSessionStats struct {
	BuildsTotal      int `json:"builds_total"`
	BuildsRemote     int `json:"builds_remote"`
	BuildsLocal      int `json:"builds_local"`
	TimeSavedSeconds int `json:"time_saved_seconds"`
}

type rchStatusEnvelope struct {
	Success bool            `json:"success"`
	Data    rchStatusData   `json:"data"`
	Error   json.RawMessage `json:"error"`
}

type rchStatusData struct {
	Daemon rchDaemonStatus `json:"daemon"`
}

type rchDaemonStatus struct {
	Summary      rchDaemonSummary    `json:"daemon"`
	Workers      []rchDaemonWorker   `json:"workers"`
	ActiveBuilds []rchDaemonBuild    `json:"active_builds"`
	Stats        *rchDaemonStats     `json:"stats"`
	SavedTime    *rchDaemonSavedTime `json:"saved_time"`
}

type rchDaemonSummary struct {
	WorkersTotal   int `json:"workers_total"`
	WorkersHealthy int `json:"workers_healthy"`
}

type rchDaemonWorker struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Host            string `json:"host"`
	Status          string `json:"status"`
	UsedSlots       int    `json:"used_slots"`
	TotalSlots      int    `json:"total_slots"`
	LastSeen        string `json:"last_seen"`
	CurrentBuild    string `json:"current_build"`
	BuildsCompleted int    `json:"builds_completed"`
	CPUPercent      int    `json:"cpu_percent"`
}

type rchDaemonBuild struct {
	WorkerID string `json:"worker_id"`
	Command  string `json:"command"`
}

type rchDaemonStats struct {
	TotalBuilds int `json:"total_builds"`
	RemoteCount int `json:"remote_count"`
	LocalCount  int `json:"local_count"`
}

type rchDaemonSavedTime struct {
	TimeSavedMS int64 `json:"time_saved_ms"`
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
)

func rchLogger() *slog.Logger {
	return slog.Default().With("component", "tools.rch")
}

// GetAvailability returns whether rch is available and compatible, with caching.
// It also checks worker availability since workers may come and go.
func (a *RCHAdapter) GetAvailability(ctx context.Context) (*RCHAvailability, error) {
	if err := lockRCHAvailabilityRead(ctx); err != nil {
		return nil, err
	}
	if time.Now().Before(rchAvailabilityExpiry) {
		availability := rchAvailabilityCache
		rchAvailabilityMutex.RUnlock()
		return &availability, nil
	}
	rchAvailabilityMutex.RUnlock()

	if err := lockRCHAvailabilityWrite(ctx); err != nil {
		return nil, err
	}
	defer rchAvailabilityMutex.Unlock()

	if time.Now().Before(rchAvailabilityExpiry) {
		availability := rchAvailabilityCache
		return &availability, nil
	}

	availability := a.fetchAvailability(ctx)

	rchAvailabilityCache = *availability
	rchAvailabilityExpiry = time.Now().Add(rchAvailabilityTTL)

	return availability, nil
}

func lockRCHAvailabilityRead(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("RCH availability context is required")
	}
	for !rchAvailabilityMutex.TryRLock() {
		if err := waitForRCHAvailabilityLock(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		rchAvailabilityMutex.RUnlock()
		return err
	}
	return nil
}

func lockRCHAvailabilityWrite(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("RCH availability context is required")
	}
	for !rchAvailabilityMutex.TryLock() {
		if err := waitForRCHAvailabilityLock(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		rchAvailabilityMutex.Unlock()
		return err
	}
	return nil
}

func waitForRCHAvailabilityLock(ctx context.Context) error {
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
		rchLogger().Debug("rch binary not found", "error", err)
		return availability
	}

	availability.Available = true
	availability.Path = path

	version, err := a.Version(ctx)
	if err != nil {
		availability.Error = err.Error()
		rchLogger().Warn("rch version check failed", "path", path, "error", err)
		return availability
	}

	availability.Version = version
	if !rchCompatible(version) {
		rchLogger().Warn("rch version incompatible", "path", path, "version", version.String(), "min_version", rchMinVersion.String())
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
	cmd.WaitDelay = time.Second
	stdout := NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("rch status failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	output := stdout.Bytes()
	if !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from rch status", ErrSchemaValidation)
	}

	status, err := parseRCHStatusJSON(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rch status: %w", err)
	}

	return status, nil
}

func parseRCHStatusJSON(output []byte) (*RCHStatus, error) {
	var envelope rchStatusEnvelope
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, err
	}
	if envelope.Success || envelope.Data.Daemon.hasData() {
		return envelope.Data.Daemon.toStatus(envelope.Success), nil
	}
	if len(bytes.TrimSpace(envelope.Error)) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return nil, fmt.Errorf("rch status reported failure: %s", strings.TrimSpace(string(envelope.Error)))
	}

	var status RCHStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, err
	}
	normalizeRCHStatus(&status)
	return &status, nil
}

func (s rchDaemonStatus) hasData() bool {
	return s.Summary.WorkersTotal > 0 ||
		s.Summary.WorkersHealthy > 0 ||
		len(s.Workers) > 0 ||
		len(s.ActiveBuilds) > 0 ||
		s.Stats != nil ||
		s.SavedTime != nil
}

func (s rchDaemonStatus) toStatus(success bool) *RCHStatus {
	currentBuilds := make(map[string]string, len(s.ActiveBuilds))
	for _, build := range s.ActiveBuilds {
		workerID := strings.TrimSpace(build.WorkerID)
		if workerID == "" {
			continue
		}
		command := strings.TrimSpace(build.Command)
		if command == "" {
			command = "build in progress"
		}
		if existing := currentBuilds[workerID]; existing != "" {
			currentBuilds[workerID] = existing + "; " + command
			continue
		}
		currentBuilds[workerID] = command
	}

	workers := make([]RCHWorker, 0, len(s.Workers))
	for _, worker := range s.Workers {
		workers = append(workers, worker.toRCHWorker(currentBuilds))
	}

	status := &RCHStatus{
		Enabled:      success || s.hasData(),
		WorkerCount:  s.Summary.WorkersTotal,
		HealthyCount: s.Summary.WorkersHealthy,
		Workers:      workers,
	}
	if s.Stats != nil || s.SavedTime != nil {
		status.SessionStats = &RCHSessionStats{}
		if s.Stats != nil {
			status.SessionStats.BuildsTotal = s.Stats.TotalBuilds
			status.SessionStats.BuildsRemote = s.Stats.RemoteCount
			status.SessionStats.BuildsLocal = s.Stats.LocalCount
		}
		if s.SavedTime != nil {
			status.SessionStats.TimeSavedSeconds = millisecondsToSeconds(s.SavedTime.TimeSavedMS)
		}
	}
	normalizeRCHStatus(status)
	return status
}

func (w rchDaemonWorker) toRCHWorker(currentBuilds map[string]string) RCHWorker {
	name := strings.TrimSpace(w.ID)
	if name == "" {
		name = strings.TrimSpace(w.Name)
	}

	status := strings.ToLower(strings.TrimSpace(w.Status))
	currentBuild := strings.TrimSpace(w.CurrentBuild)
	if currentBuild == "" {
		currentBuild = strings.TrimSpace(currentBuilds[name])
	}
	if currentBuild == "" && w.UsedSlots > 0 {
		currentBuild = "build in progress"
	}

	return RCHWorker{
		Name:            name,
		Host:            w.Host,
		Available:       rchDaemonWorkerAvailable(status),
		Healthy:         rchDaemonWorkerHealthy(status),
		Load:            rchSlotLoadPercent(w.UsedSlots, w.TotalSlots),
		LastSeen:        w.LastSeen,
		CurrentBuild:    currentBuild,
		BuildsCompleted: w.BuildsCompleted,
		CPUPercent:      w.CPUPercent,
	}
}

func normalizeRCHStatus(status *RCHStatus) {
	if status == nil {
		return
	}
	if status.WorkerCount == 0 && len(status.Workers) > 0 {
		status.WorkerCount = len(status.Workers)
	}
	if status.HealthyCount == 0 && len(status.Workers) > 0 {
		for _, worker := range status.Workers {
			if worker.Healthy && worker.Available {
				status.HealthyCount++
			}
		}
	}
	if !status.Enabled && (status.WorkerCount > 0 || status.HealthyCount > 0 || len(status.Workers) > 0 || status.SessionStats != nil) {
		status.Enabled = true
	}
}

func rchDaemonWorkerAvailable(status string) bool {
	switch status {
	case "healthy", "available", "online", "busy", "degraded":
		return true
	default:
		return false
	}
}

func rchDaemonWorkerHealthy(status string) bool {
	switch status {
	case "healthy", "available", "online", "busy":
		return true
	default:
		return false
	}
}

func rchSlotLoadPercent(usedSlots, totalSlots int) int {
	if usedSlots <= 0 || totalSlots <= 0 {
		return 0
	}
	percent := (usedSlots*100 + totalSlots/2) / totalSlots
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func millisecondsToSeconds(milliseconds int64) int {
	if milliseconds <= 0 {
		return 0
	}
	return int((milliseconds + 500) / 1000)
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
		rchLogger().Debug("preferred worker not available", "preferred", preferred)
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
