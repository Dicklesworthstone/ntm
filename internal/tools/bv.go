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

// BVAdapter provides integration with the beads_viewer (bv) tool
type BVAdapter struct {
	*BaseAdapter
}

// BVAlertOptions configures BV alert filtering.
type BVAlertOptions struct {
	AlertType string
	Severity  string
	Label     string
}

// BVGraphOptions configures BV graph output.
type BVGraphOptions struct {
	Format string
}

// BVSearchOptions configures BV search behavior.
type BVSearchOptions struct {
	Query string
	Limit int
	Mode  string
}

// BVGroupedTriageOptions configures grouped triage output.
type BVGroupedTriageOptions struct {
	ByLabel bool
	ByTrack bool
}

// NewBVAdapter creates a new BV adapter
func NewBVAdapter() *BVAdapter {
	return &BVAdapter{
		BaseAdapter: NewBaseAdapter(ToolBV, "bv"),
	}
}

// Detect checks if bv is installed
func (a *BVAdapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.BinaryName())
	if err != nil {
		return "", false
	}
	return path, true
}

// Version returns the installed bv version
func (a *BVAdapter) Version(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), "--version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("failed to get bv version: %w", err)
	}

	return ParseStandardVersion(stdout.String())
}

// Capabilities returns the list of bv capabilities
func (a *BVAdapter) Capabilities(ctx context.Context) ([]Capability, error) {
	caps := []Capability{CapRobotMode}

	// Check for specific robot mode commands
	version, err := a.Version(ctx)
	if err != nil {
		return caps, nil
	}

	// bv 0.30+ has robot-triage
	if version.AtLeast(Version{Major: 0, Minor: 30, Patch: 0}) {
		caps = append(caps, "robot_triage", "robot_plan", "robot_insights", "robot_next")
	}

	// bv 0.31+ includes additional analysis modes
	if version.AtLeast(Version{Major: 0, Minor: 31, Patch: 0}) {
		caps = append(caps,
			"robot_alerts",
			"robot_graph",
			"robot_forecast",
			"robot_suggest",
			"robot_impact",
			"robot_search",
			"robot_history",
			"robot_burndown",
			"robot_label_attention",
			"robot_label_flow",
			"robot_label_health",
			"robot_file_beads",
			"robot_file_hotspots",
			"robot_file_relations",
			"robot_triage_by_label",
			"robot_triage_by_track",
		)
	}

	return caps, nil
}

// Health checks if bv is functioning correctly
func (a *BVAdapter) Health(ctx context.Context) (*HealthStatus, error) {
	start := time.Now()

	path, installed := a.Detect()
	if !installed {
		return &HealthStatus{
			Healthy:     false,
			Message:     "bv not installed",
			LastChecked: time.Now(),
		}, nil
	}

	// Try to get version as a health check
	_, err := a.Version(ctx)
	latency := time.Since(start)

	if err != nil {
		return &HealthStatus{
			Healthy:     false,
			Message:     fmt.Sprintf("bv at %s not responding", path),
			Error:       err.Error(),
			LastChecked: time.Now(),
			Latency:     latency,
		}, nil
	}

	return &HealthStatus{
		Healthy:     true,
		Message:     "bv is healthy",
		LastChecked: time.Now(),
		Latency:     latency,
	}, nil
}

// HasCapability checks if bv has a specific capability
func (a *BVAdapter) HasCapability(ctx context.Context, cap Capability) bool {
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

// Info returns complete bv tool information
func (a *BVAdapter) Info(ctx context.Context) (*ToolInfo, error) {
	return a.BaseAdapter.Info(ctx, a)
}

// BV-specific methods

// GetTriage returns the robot-triage output
func (a *BVAdapter) GetTriage(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-triage")
}

// GetPlan returns the robot-plan output
func (a *BVAdapter) GetPlan(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-plan")
}

// GetInsights returns the robot-insights output
func (a *BVAdapter) GetInsights(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-insights")
}

// GetNext returns the robot-next output (single top pick)
func (a *BVAdapter) GetNext(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-next")
}

// Analysis mode methods for advanced BV analysis
func (a *BVAdapter) GetAlerts(ctx context.Context, dir string, opts BVAlertOptions) (json.RawMessage, error) {
	args := []string{"-robot-alerts"}
	if opts.AlertType != "" {
		args = append(args, "-alert-type", opts.AlertType)
	}
	if opts.Label != "" {
		args = append(args, "-alert-label", opts.Label)
	}
	if opts.Severity != "" {
		args = append(args, "-severity", opts.Severity)
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetGraph(ctx context.Context, dir string, opts BVGraphOptions) (json.RawMessage, error) {
	args := []string{"-robot-graph"}
	if opts.Format != "" {
		args = append(args, "-graph-format", opts.Format)
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetGroupedTriage(ctx context.Context, dir string, opts BVGroupedTriageOptions) (json.RawMessage, error) {
	if opts.ByLabel {
		return a.runRobotCommand(ctx, dir, "-robot-triage-by-label")
	}
	if opts.ByTrack {
		return a.runRobotCommand(ctx, dir, "-robot-triage-by-track")
	}
	return a.runRobotCommand(ctx, dir, "-robot-triage")
}

func (a *BVAdapter) GetHistory(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-history")
}

func (a *BVAdapter) GetBurndown(ctx context.Context, dir string, sprint string) (json.RawMessage, error) {
	args := []string{"-robot-burndown"}
	if sprint != "" {
		args = append(args, sprint)
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetForecast(ctx context.Context, dir string, target string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-forecast", target)
}

func (a *BVAdapter) GetSuggestions(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-suggest")
}

func (a *BVAdapter) GetImpact(ctx context.Context, dir string, filePath string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-impact", filePath)
}

func (a *BVAdapter) GetSearch(ctx context.Context, dir string, query string) (json.RawMessage, error) {
	return a.GetSearchWithOptions(ctx, dir, BVSearchOptions{Query: query})
}

func (a *BVAdapter) GetSearchWithOptions(ctx context.Context, dir string, opts BVSearchOptions) (json.RawMessage, error) {
	args := []string{"-robot-search"}
	if opts.Query != "" {
		args = append(args, "-search", opts.Query)
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("-search-limit=%d", opts.Limit))
	}
	if opts.Mode != "" {
		args = append(args, "-search-mode", opts.Mode)
	}
	return a.runRobotCommand(ctx, dir, args...)
}

// Label mode methods for label-based analysis
func (a *BVAdapter) GetLabelAttention(ctx context.Context, dir string, limit int) (json.RawMessage, error) {
	args := []string{"-robot-label-attention"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-attention-limit=%d", limit))
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetLabelFlow(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-label-flow")
}

func (a *BVAdapter) GetLabelHealth(ctx context.Context, dir string) (json.RawMessage, error) {
	return a.runRobotCommand(ctx, dir, "-robot-label-health")
}

// File mode methods for file-based analysis
func (a *BVAdapter) GetFileBeads(ctx context.Context, dir string, filePath string, limit int) (json.RawMessage, error) {
	args := []string{"-robot-file-beads", filePath}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-file-beads-limit=%d", limit))
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetFileHotspots(ctx context.Context, dir string, limit int) (json.RawMessage, error) {
	args := []string{"-robot-file-hotspots"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-hotspots-limit=%d", limit))
	}
	return a.runRobotCommand(ctx, dir, args...)
}

func (a *BVAdapter) GetFileRelations(ctx context.Context, dir string, filePath string, limit int, threshold float64) (json.RawMessage, error) {
	args := []string{"-robot-file-relations", filePath}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-relations-limit=%d", limit))
	}
	if threshold > 0 {
		args = append(args, fmt.Sprintf("-relations-threshold=%.2f", threshold))
	}
	return a.runRobotCommand(ctx, dir, args...)
}

// runRobotCommand executes a bv robot command and returns raw JSON
func (a *BVAdapter) runRobotCommand(ctx context.Context, dir string, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, a.BinaryName(), args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start bv: %w", err)
	}

	// Limit output to 10MB to prevent OOM
	const maxOutput = 10 * 1024 * 1024
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, maxOutput+1))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("failed to read bv output: %w", err)
	}
	if len(output) > maxOutput {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("bv output exceeded limit of %d bytes", maxOutput)
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("bv %s failed: %w: %s", strings.Join(args, " "), err, stderr.String())
	}

	// Validate JSON
	if !json.Valid(output) {
		return nil, fmt.Errorf("%w: invalid JSON from bv", ErrSchemaValidation)
	}

	return output, nil
}
