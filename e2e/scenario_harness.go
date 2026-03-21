//go:build e2e
// +build e2e

// Package e2e provides end-to-end test infrastructure for NTM.
//
// This file implements the shared scenario harness for attention-feed and overlay-feed
// e2e/transcript verification. It provides:
// - Deterministic tmux session management with cleanup registration
// - Standardized artifact directory layout, timeline logging, and retention
// - Shared command execution for robot, CLI, and transport-related scenarios
// - Cursor/event tracking helpers for operator-loop verification
// - Assertion helpers for common operator-loop invariants
//
// See br-7746h for design rationale.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ArtifactRetention controls how artifacts are handled after test completion.
type ArtifactRetention int

const (
	// RetainOnFailure keeps artifacts only when tests fail (default).
	RetainOnFailure ArtifactRetention = iota
	// RetainAlways keeps all artifacts regardless of test outcome.
	RetainAlways
	// RetainNever deletes all artifacts after test (for CI disk conservation).
	RetainNever
)

var scenarioSequence atomic.Uint64

// ArtifactConfig configures artifact storage for a scenario.
type ArtifactConfig struct {
	// BaseDir is the root directory for all artifacts. Defaults to /tmp/ntm-e2e-artifacts.
	BaseDir string
	// Retention controls when artifacts are kept vs deleted.
	Retention ArtifactRetention
	// SuiteName groups related scenarios (for example, "attention_feed_v1").
	SuiteName string
	// MaxArtifactAgeDays controls cleanup of old artifacts. 0 disables stale cleanup.
	MaxArtifactAgeDays int
}

// DefaultArtifactConfig returns sensible defaults for artifact storage.
func DefaultArtifactConfig() ArtifactConfig {
	baseDir := os.Getenv("E2E_ARTIFACT_DIR")
	if baseDir == "" {
		baseDir = "/tmp/ntm-e2e-artifacts"
	}

	retention := RetainOnFailure
	switch os.Getenv("E2E_RETAIN_ARTIFACTS") {
	case "always":
		retention = RetainAlways
	case "never":
		retention = RetainNever
	}

	return ArtifactConfig{
		BaseDir:            baseDir,
		Retention:          retention,
		SuiteName:          "default",
		MaxArtifactAgeDays: 7,
	}
}

// TimelineEntry records a scenario step for post-failure diagnosis.
type TimelineEntry struct {
	Timestamp time.Time              `json:"ts"`
	Step      int64                  `json:"step"`
	Kind      string                 `json:"kind"`
	Summary   string                 `json:"summary"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// CursorTraceEntry represents a single cursor observation in the trace log.
type CursorTraceEntry struct {
	Timestamp  time.Time `json:"ts"`
	Step       int64     `json:"step"`
	Command    string    `json:"command"`
	CursorIn   *int64    `json:"cursor_in,omitempty"`
	CursorOut  *int64    `json:"cursor_out,omitempty"`
	EventCount int       `json:"event_count,omitempty"`
	WakeReason string    `json:"wake_reason,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	Notes      string    `json:"notes,omitempty"`
}

// ArtifactManager manages artifact storage for e2e scenarios.
//
// Directory layout:
//
//	{BaseDir}/
//	  {SuiteName}/
//	    {ScenarioName}_{Timestamp}_{Ordinal}/
//	      manifest.json         # Scenario metadata
//	      timeline.jsonl        # Ordered scenario steps
//	      stdout/               # Command stdout captures
//	      stderr/               # Command stderr captures
//	      cursors/              # Cursor progression traces
//	      events/               # Event feed captures
//	      summaries/            # Rendered digest/attention summaries
//	      transport/            # WebSocket/SSE/HTTP captures
//	      screenshots/          # TUI captures
type ArtifactManager struct {
	config       ArtifactConfig
	scenarioName string
	scenarioDir  string
	startTime    time.Time
	stepCounter  atomic.Int64
	mu           sync.Mutex
}

// NewArtifactManager creates an artifact manager for a scenario.
func NewArtifactManager(scenarioName string, config ArtifactConfig) (*ArtifactManager, error) {
	config = mergeArtifactConfig(config)
	suiteName := sanitizeFilename(config.SuiteName)
	scenarioSlug := sanitizeFilename(scenarioName)
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	ordinal := scenarioSequence.Add(1)
	suiteDir := filepath.Join(config.BaseDir, suiteName)
	scenarioDir := filepath.Join(suiteDir, fmt.Sprintf("%s_%s_%03d", scenarioSlug, stamp, ordinal))

	if err := pruneStaleArtifacts(suiteDir, config.MaxArtifactAgeDays); err != nil {
		return nil, fmt.Errorf("prune stale artifacts: %w", err)
	}

	dirs := []string{
		scenarioDir,
		filepath.Join(scenarioDir, "stdout"),
		filepath.Join(scenarioDir, "stderr"),
		filepath.Join(scenarioDir, "cursors"),
		filepath.Join(scenarioDir, "events"),
		filepath.Join(scenarioDir, "summaries"),
		filepath.Join(scenarioDir, "transport"),
		filepath.Join(scenarioDir, "screenshots"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create artifact dir %s: %w", dir, err)
		}
	}

	am := &ArtifactManager{
		config:       config,
		scenarioName: scenarioName,
		scenarioDir:  scenarioDir,
		startTime:    time.Now().UTC(),
	}

	if err := am.writeManifest(map[string]interface{}{
		"scenario":    scenarioName,
		"suite":       config.SuiteName,
		"started_at":  am.startTime.Format(time.RFC3339Nano),
		"status":      "running",
		"retention":   am.config.Retention.String(),
		"artifact_dir": am.scenarioDir,
	}); err != nil {
		return nil, err
	}
	if err := am.AppendTimeline(TimelineEntry{
		Timestamp: am.startTime,
		Step:      0,
		Kind:      "lifecycle",
		Summary:   "scenario started",
		Fields: map[string]interface{}{
			"suite":       config.SuiteName,
			"artifact_dir": am.scenarioDir,
		},
	}); err != nil {
		return nil, err
	}

	return am, nil
}

// Dir returns the scenario artifact directory.
func (am *ArtifactManager) Dir() string {
	return am.scenarioDir
}

// NextStep increments and returns the step counter for artifact naming.
func (am *ArtifactManager) NextStep() int64 {
	return am.stepCounter.Add(1)
}

// CurrentStep returns the current step counter without incrementing.
func (am *ArtifactManager) CurrentStep() int64 {
	return am.stepCounter.Load()
}

// WriteStdout saves command stdout to the stdout/ directory using a new step.
func (am *ArtifactManager) WriteStdout(command string, data []byte) error {
	_, err := am.WriteStdoutStep(am.NextStep(), command, data)
	return err
}

// WriteStdoutStep saves command stdout to the stdout/ directory for an existing step.
func (am *ArtifactManager) WriteStdoutStep(step int64, command string, data []byte) (string, error) {
	filename := fmt.Sprintf("%03d_%s.json", step, sanitizeFilename(command))
	return am.writeFile("stdout", filename, data)
}

// WriteStderr saves command stderr to the stderr/ directory using the current step.
func (am *ArtifactManager) WriteStderr(command string, data []byte) error {
	_, err := am.WriteStderrStep(am.CurrentStep(), command, data)
	return err
}

// WriteStderrStep saves command stderr to the stderr/ directory for an existing step.
func (am *ArtifactManager) WriteStderrStep(step int64, command string, data []byte) (string, error) {
	filename := fmt.Sprintf("%03d_%s.txt", step, sanitizeFilename(command))
	return am.writeFile("stderr", filename, data)
}

// AppendCursorTrace appends a cursor observation to the cursor trace file.
func (am *ArtifactManager) AppendCursorTrace(entry CursorTraceEntry) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	return appendJSONL(filepath.Join(am.scenarioDir, "cursors", "cursor_trace.jsonl"), entry)
}

// AppendTimeline appends a timeline entry to timeline.jsonl.
func (am *ArtifactManager) AppendTimeline(entry TimelineEntry) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	return appendJSONL(filepath.Join(am.scenarioDir, "timeline.jsonl"), entry)
}

// WriteEvents saves an event feed capture to the events/ directory.
func (am *ArtifactManager) WriteEvents(cursorStart, cursorEnd int64, events []byte) error {
	filename := fmt.Sprintf("events_%d_%d.jsonl", cursorStart, cursorEnd)
	_, err := am.writeFile("events", filename, events)
	return err
}

// WriteSummary saves a digest/attention summary to the summaries/ directory.
func (am *ArtifactManager) WriteSummary(summaryType string, content []byte) error {
	step := am.CurrentStep()
	if step == 0 {
		step = am.NextStep()
	}
	filename := fmt.Sprintf("%s_%03d.md", sanitizeFilename(summaryType), step)
	_, err := am.writeFile("summaries", filename, content)
	return err
}

// WriteTransport saves a transport capture to the transport/ directory.
func (am *ArtifactManager) WriteTransport(label string, content []byte) error {
	step := am.CurrentStep()
	if step == 0 {
		step = am.NextStep()
	}
	_, err := am.WriteTransportStep(step, label, content)
	return err
}

// WriteTransportStep saves a transport capture for an existing step.
func (am *ArtifactManager) WriteTransportStep(step int64, label string, content []byte) (string, error) {
	filename := fmt.Sprintf("%03d_%s.jsonl", step, sanitizeFilename(label))
	return am.writeFile("transport", filename, content)
}

// WriteScreenshot saves a TUI screenshot to the screenshots/ directory.
func (am *ArtifactManager) WriteScreenshot(content string) error {
	step := am.NextStep()
	filename := fmt.Sprintf("step_%03d.txt", step)
	_, err := am.writeFile("screenshots", filename, []byte(content))
	return err
}

// Finalize updates the manifest with final status and handles retention.
func (am *ArtifactManager) Finalize(failed bool) error {
	finishedAt := time.Now().UTC()
	duration := finishedAt.Sub(am.startTime)
	status := "passed"
	if failed {
		status = "failed"
	}

	if err := am.writeManifest(map[string]interface{}{
		"scenario":     am.scenarioName,
		"suite":        am.config.SuiteName,
		"started_at":   am.startTime.Format(time.RFC3339Nano),
		"finished_at":  finishedAt.Format(time.RFC3339Nano),
		"duration_ms":  duration.Milliseconds(),
		"status":       status,
		"steps":        am.stepCounter.Load(),
		"retention":    am.config.Retention.String(),
		"artifact_dir": am.scenarioDir,
	}); err != nil {
		return err
	}
	if err := am.AppendTimeline(TimelineEntry{
		Timestamp: finishedAt,
		Step:      am.CurrentStep(),
		Kind:      "lifecycle",
		Summary:   "scenario finalized",
		Fields: map[string]interface{}{
			"status":      status,
			"duration_ms": duration.Milliseconds(),
			"retention":   am.config.Retention.String(),
		},
	}); err != nil {
		return err
	}

	shouldDelete := false
	switch am.config.Retention {
	case RetainNever:
		shouldDelete = true
	case RetainOnFailure:
		shouldDelete = !failed
	case RetainAlways:
		shouldDelete = false
	}
	if shouldDelete {
		return os.RemoveAll(am.scenarioDir)
	}
	return nil
}

func (am *ArtifactManager) writeManifest(data map[string]interface{}) error {
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(am.scenarioDir, "manifest.json"), content, 0o644)
}

func (am *ArtifactManager) writeFile(subdir, filename string, data []byte) (string, error) {
	path := filepath.Join(am.scenarioDir, subdir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func mergeArtifactConfig(config ArtifactConfig) ArtifactConfig {
	defaults := DefaultArtifactConfig()
	if config.BaseDir == "" {
		config.BaseDir = defaults.BaseDir
	}
	if config.SuiteName == "" {
		config.SuiteName = defaults.SuiteName
	}
	return config
}

func pruneStaleArtifacts(suiteDir string, maxAgeDays int) error {
	if maxAgeDays <= 0 {
		return nil
	}
	entries, err := os.ReadDir(suiteDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(suiteDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendJSONL(path string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(string(data) + "\n")
	return err
}

func sanitizeFilename(s string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"=", "_",
	)
	s = replacer.Replace(s)
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.Trim(s, "_.")
	if s == "" {
		return "artifact"
	}
	return s
}

func sanitizeSessionComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "scenario"
	}
	return out
}

func buildSessionName(prefix, name string) string {
	ordinal := scenarioSequence.Add(1)
	return fmt.Sprintf(
		"%s_%s_%s_%03d",
		sanitizeSessionComponent(prefix),
		sanitizeSessionComponent(name),
		time.Now().UTC().Format("20060102_150405"),
		ordinal,
	)
}

// CommandResult holds the result of a command invocation.
type CommandResult struct {
	Command    string
	Args       []string
	Stdout     []byte
	Stderr     []byte
	ExitCode   int
	Duration   time.Duration
	ParsedJSON map[string]interface{}
	Error      error
}

// RobotResult is an alias kept for scenario code that thinks in robot-command terms.
type RobotResult = CommandResult

// Success returns true if the command succeeded.
func (r *CommandResult) Success() bool {
	return r.ExitCode == 0 && r.Error == nil
}

// CommandLine returns the shell-style representation of the command.
func (r *CommandResult) CommandLine() string {
	if len(r.Args) == 0 {
		return r.Command
	}
	return strings.TrimSpace(r.Command + " " + strings.Join(r.Args, " "))
}

// LookupJSONPath resolves a dot-separated path against ParsedJSON.
func (r *CommandResult) LookupJSONPath(path string) (interface{}, bool) {
	if r.ParsedJSON == nil {
		return nil, false
	}
	if path == "" {
		return r.ParsedJSON, true
	}
	var current interface{} = r.ParsedJSON
	for _, part := range strings.Split(path, ".") {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		next, ok := asMap[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// GetString extracts a string field from the parsed JSON response.
func (r *CommandResult) GetString(path string) string {
	if v, ok := r.LookupJSONPath(path); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetBool extracts a bool field from the parsed JSON response.
func (r *CommandResult) GetBool(path string) bool {
	if v, ok := r.LookupJSONPath(path); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// GetInt extracts an integer field from the parsed JSON response.
func (r *CommandResult) GetInt(path string) int64 {
	v, ok := r.LookupJSONPath(path)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		parsed, _ := n.Int64()
		return parsed
	}
	return 0
}

// GetArray extracts an array field from the parsed JSON response.
func (r *CommandResult) GetArray(path string) []interface{} {
	if v, ok := r.LookupJSONPath(path); ok {
		if arr, ok := v.([]interface{}); ok {
			return arr
		}
	}
	return nil
}

type cleanupAction struct {
	label string
	run   func() error
}

// ScenarioHarness provides the full e2e scenario infrastructure.
type ScenarioHarness struct {
	t          *testing.T
	name       string
	artifacts  *ArtifactManager
	session    string
	startTime  time.Time
	timeout    time.Duration
	cleanup    []cleanupAction
	lastCursor *int64
	mu         sync.Mutex
	failed     bool
}

// ScenarioConfig configures a scenario harness.
type ScenarioConfig struct {
	Name           string
	ArtifactConfig ArtifactConfig
	SessionPrefix  string // Defaults to "e2e"
	Timeout        time.Duration
}

// NewScenarioHarness creates a new scenario harness for e2e testing.
func NewScenarioHarness(t *testing.T, config ScenarioConfig) (*ScenarioHarness, error) {
	t.Helper()

	if config.Name == "" {
		return nil, fmt.Errorf("scenario name is required")
	}
	if config.SessionPrefix == "" {
		config.SessionPrefix = "e2e"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	artifacts, err := NewArtifactManager(config.Name, config.ArtifactConfig)
	if err != nil {
		return nil, fmt.Errorf("create artifact manager: %w", err)
	}

	h := &ScenarioHarness{
		t:         t,
		name:      config.Name,
		artifacts: artifacts,
		session:   buildSessionName(config.SessionPrefix, config.Name),
		startTime: time.Now().UTC(),
		timeout:   config.Timeout,
	}

	t.Logf("[HARNESS] Scenario %q started session=%s artifact_root=%s timeout=%s",
		config.Name, h.session, artifacts.Dir(), config.Timeout)
	return h, nil
}

// Session returns the tmux session name.
func (h *ScenarioHarness) Session() string {
	return h.session
}

// Artifacts returns the artifact manager.
func (h *ScenarioHarness) Artifacts() *ArtifactManager {
	return h.artifacts
}

// SetupTmux creates the tmux session for the scenario.
func (h *ScenarioHarness) SetupTmux() error {
	h.t.Helper()

	if !tmux.DefaultClient.IsInstalled() {
		return fmt.Errorf("tmux not found")
	}
	if tmux.SessionExists(h.session) {
		return fmt.Errorf("tmux session %s already exists", h.session)
	}

	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tmux.BinaryPath(), "new-session", "-d", "-s", h.session, "-x", "200", "-y", "50")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("create tmux session %s: %w", h.session, ctx.Err())
		}
		return fmt.Errorf("create tmux session %s: %w stderr=%s", h.session, err, stderr.String())
	}

	h.RegisterCleanup("kill tmux session "+h.session, func() error {
		if !tmux.SessionExists(h.session) {
			return nil
		}
		return tmux.KillSession(h.session)
	})
	h.Log("tmux session created session=%s", h.session)
	return nil
}

// RegisterCleanup adds a cleanup action that runs during Cleanup in reverse order.
func (h *ScenarioHarness) RegisterCleanup(label string, fn func() error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanup = append(h.cleanup, cleanupAction{label: label, run: fn})
	h.Log("registered cleanup action=%q", label)
}

// AnnotateStep appends a manual timeline entry and returns the allocated step number.
func (h *ScenarioHarness) AnnotateStep(summary string, fields map[string]interface{}) int64 {
	h.t.Helper()
	step := h.artifacts.NextStep()
	if err := h.artifacts.AppendTimeline(TimelineEntry{
		Timestamp: time.Now().UTC(),
		Step:      step,
		Kind:      "annotation",
		Summary:   summary,
		Fields:    fields,
	}); err != nil {
		h.t.Logf("[HARNESS] Warning: failed to append annotation step: %v", err)
	}
	h.Log("annotation step=%d summary=%q", step, summary)
	return step
}

// Cleanup runs all registered cleanup functions and finalizes artifacts.
func (h *ScenarioHarness) Cleanup() {
	h.t.Helper()

	h.mu.Lock()
	cleanup := append([]cleanupAction(nil), h.cleanup...)
	h.cleanup = nil
	h.mu.Unlock()

	h.Log("cleanup starting actions=%d artifact_root=%s session=%s", len(cleanup), h.artifacts.Dir(), h.session)
	for i := len(cleanup) - 1; i >= 0; i-- {
		action := cleanup[i]
		h.t.Logf("[HARNESS] Cleanup action=%q", action.label)
		if err := action.run(); err != nil {
			h.failed = true
			h.t.Logf("[HARNESS] Cleanup action failed action=%q err=%v", action.label, err)
		}
	}

	if err := h.artifacts.Finalize(h.failed || h.t.Failed()); err != nil {
		h.failed = true
		h.t.Logf("[HARNESS] Warning: failed to finalize artifacts: %v", err)
	}

	status := "passed"
	if h.failed || h.t.Failed() {
		status = "failed"
	}
	h.t.Logf("[HARNESS] Scenario %q completed status=%s duration=%s artifact_root=%s",
		h.name, status, time.Since(h.startTime).Round(time.Millisecond), h.artifacts.Dir())
}

// MarkFailed marks the scenario as failed for retention and reporting.
func (h *ScenarioHarness) MarkFailed() {
	h.failed = true
}

// RunCommand executes a command and captures stdout, stderr, and timeline artifacts.
func (h *ScenarioHarness) RunCommand(name string, args ...string) *CommandResult {
	h.t.Helper()

	step := h.artifacts.NextStep()
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	result := &CommandResult{
		Command:  name,
		Args:     append([]string(nil), args...),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: duration,
		ExitCode: 0,
	}

	switch {
	case err == nil:
		result.ExitCode = 0
	case ctx.Err() != nil:
		result.ExitCode = -1
		result.Error = ctx.Err()
	case errors.As(err, new(*exec.ExitError)):
		var exitErr *exec.ExitError
		_ = errors.As(err, &exitErr)
		result.ExitCode = exitErr.ExitCode()
		result.Error = err
	default:
		result.ExitCode = -1
		result.Error = err
	}

	if len(result.Stdout) > 0 {
		var parsed map[string]interface{}
		if jsonErr := json.Unmarshal(result.Stdout, &parsed); jsonErr == nil {
			result.ParsedJSON = parsed
		}
	}

	stdoutPath, stdoutErr := h.artifacts.WriteStdoutStep(step, result.CommandLine(), result.Stdout)
	if stdoutErr != nil {
		h.t.Logf("[HARNESS] Warning: failed to save stdout artifact: %v", stdoutErr)
	}

	var stderrPath string
	if len(result.Stderr) > 0 {
		stderrPath, err = h.artifacts.WriteStderrStep(step, result.CommandLine(), result.Stderr)
		if err != nil {
			h.t.Logf("[HARNESS] Warning: failed to save stderr artifact: %v", err)
		}
	}

	fields := map[string]interface{}{
		"command":     result.Command,
		"args":        result.Args,
		"exit_code":   result.ExitCode,
		"duration_ms": result.Duration.Milliseconds(),
	}
	if stdoutPath != "" {
		fields["stdout_path"] = filepath.Base(filepath.Dir(stdoutPath)) + "/" + filepath.Base(stdoutPath)
	}
	if stderrPath != "" {
		fields["stderr_path"] = filepath.Base(filepath.Dir(stderrPath)) + "/" + filepath.Base(stderrPath)
	}
	if result.Error != nil {
		fields["error"] = result.Error.Error()
	}
	if err := h.artifacts.AppendTimeline(TimelineEntry{
		Timestamp: time.Now().UTC(),
		Step:      step,
		Kind:      "command",
		Summary:   result.CommandLine(),
		Fields:    fields,
	}); err != nil {
		h.t.Logf("[HARNESS] Warning: failed to append command timeline: %v", err)
	}

	h.Log("command step=%d cmd=%q exit=%d duration=%s stdout=%d stderr=%d",
		step, result.CommandLine(), result.ExitCode, result.Duration.Round(time.Millisecond), len(result.Stdout), len(result.Stderr))
	return result
}

// RunRobot executes an ntm robot command and captures the result.
func (h *ScenarioHarness) RunRobot(args ...string) *CommandResult {
	h.t.Helper()
	return h.RunCommand("ntm", args...)
}

// RunRobotStatus runs --robot-status.
func (h *ScenarioHarness) RunRobotStatus() *CommandResult {
	return h.RunRobot("--robot-status")
}

// RunRobotSnapshot runs --robot-snapshot with an optional since cursor.
func (h *ScenarioHarness) RunRobotSnapshot(since *int64) *CommandResult {
	args := []string{"--robot-snapshot"}
	if since != nil {
		args = append(args, fmt.Sprintf("--since=%d", *since))
	}
	return h.RunRobot(args...)
}

// RunRobotWait runs --robot-wait with a condition.
func (h *ScenarioHarness) RunRobotWait(session, condition string, timeout time.Duration) *CommandResult {
	return h.RunRobot(
		fmt.Sprintf("--robot-wait=%s", session),
		fmt.Sprintf("--condition=%s", condition),
		fmt.Sprintf("--timeout=%s", timeout),
	)
}

// CaptureTransport stores a transport artifact and records it in the timeline.
func (h *ScenarioHarness) CaptureTransport(label string, data []byte) error {
	h.t.Helper()
	step := h.artifacts.NextStep()
	path, err := h.artifacts.WriteTransportStep(step, label, data)
	if err != nil {
		return err
	}
	if err := h.artifacts.AppendTimeline(TimelineEntry{
		Timestamp: time.Now().UTC(),
		Step:      step,
		Kind:      "transport",
		Summary:   label,
		Fields: map[string]interface{}{
			"path": filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path),
			"bytes": len(data),
		},
	}); err != nil {
		return err
	}
	h.Log("transport captured step=%d label=%q bytes=%d", step, label, len(data))
	return nil
}

// TrackCursor records a cursor observation for later analysis.
func (h *ScenarioHarness) TrackCursor(command string, cursorIn, cursorOut *int64, eventCount int, wakeReason, notes string, duration time.Duration, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	step := h.artifacts.CurrentStep()
	if step == 0 {
		step = h.artifacts.NextStep()
	}

	entry := CursorTraceEntry{
		Timestamp:  time.Now().UTC(),
		Step:       step,
		Command:    command,
		CursorIn:   cursorIn,
		CursorOut:  cursorOut,
		EventCount: eventCount,
		WakeReason: wakeReason,
		DurationMs: duration.Milliseconds(),
		Notes:      notes,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if saveErr := h.artifacts.AppendCursorTrace(entry); saveErr != nil {
		h.t.Logf("[HARNESS] Warning: failed to append cursor trace: %v", saveErr)
	}
	if saveErr := h.artifacts.AppendTimeline(TimelineEntry{
		Timestamp: entry.Timestamp,
		Step:      step,
		Kind:      "cursor",
		Summary:   command,
		Fields: map[string]interface{}{
			"event_count": eventCount,
			"wake_reason": wakeReason,
			"notes":       notes,
		},
	}); saveErr != nil {
		h.t.Logf("[HARNESS] Warning: failed to append cursor timeline: %v", saveErr)
	}

	if cursorOut != nil {
		cursor := *cursorOut
		h.lastCursor = &cursor
	}
}

// LastCursor returns the last observed cursor value.
func (h *ScenarioHarness) LastCursor() *int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastCursor == nil {
		return nil
	}
	cursor := *h.lastCursor
	return &cursor
}

// AssertSuccess fails the test if the result indicates failure.
func (h *ScenarioHarness) AssertSuccess(result *CommandResult, msgAndArgs ...interface{}) {
	h.t.Helper()
	if result.Success() {
		return
	}
	h.failed = true
	msg := "expected success"
	if len(msgAndArgs) > 0 {
		msg = fmt.Sprintf(msgAndArgs[0].(string), msgAndArgs[1:]...)
	}
	h.t.Fatalf("[ASSERT] %s: command=%q exit=%d err=%v stderr=%s",
		msg, result.CommandLine(), result.ExitCode, result.Error, string(result.Stderr))
}

// AssertNoError fails if the result has an unexpected execution error.
func (h *ScenarioHarness) AssertNoError(result *CommandResult) {
	h.t.Helper()
	if result.Error == nil {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] unexpected error: %v", result.Error)
}

// AssertExitCode fails if the exit code doesn't match.
func (h *ScenarioHarness) AssertExitCode(result *CommandResult, expected int) {
	h.t.Helper()
	if result.ExitCode == expected {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] exit code: expected %d got %d for %q", expected, result.ExitCode, result.CommandLine())
}

// AssertJSONField fails if a JSON path doesn't match the expected value.
func (h *ScenarioHarness) AssertJSONField(result *CommandResult, path string, expected interface{}) {
	h.t.Helper()
	actual, ok := result.LookupJSONPath(path)
	if !ok {
		h.failed = true
		h.t.Fatalf("[ASSERT] JSON path %q missing in %q", path, result.CommandLine())
	}
	if actual == expected {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] JSON path %q expected %v got %v", path, expected, actual)
}

// AssertStringField fails if a JSON path doesn't match the expected string.
func (h *ScenarioHarness) AssertStringField(result *CommandResult, path, expected string) {
	h.t.Helper()
	actual := result.GetString(path)
	if actual == expected {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] JSON string %q expected %q got %q", path, expected, actual)
}

// AssertBoolField fails if a JSON path doesn't match the expected bool.
func (h *ScenarioHarness) AssertBoolField(result *CommandResult, path string, expected bool) {
	h.t.Helper()
	actual := result.GetBool(path)
	if actual == expected {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] JSON bool %q expected %v got %v", path, expected, actual)
}

// AssertErrorCode fails if the error_code field doesn't match.
func (h *ScenarioHarness) AssertErrorCode(result *CommandResult, expected string) {
	h.t.Helper()
	h.AssertStringField(result, "error_code", expected)
}

// AssertArrayNotEmpty fails if the specified array field is empty.
func (h *ScenarioHarness) AssertArrayNotEmpty(result *CommandResult, path string) {
	h.t.Helper()
	arr := result.GetArray(path)
	if len(arr) > 0 {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] array %q is empty", path)
}

// AssertCursorMonotonic fails if the output cursor is not greater than input cursor.
func (h *ScenarioHarness) AssertCursorMonotonic(cursorIn, cursorOut int64) {
	h.t.Helper()
	if cursorOut > cursorIn {
		return
	}
	h.failed = true
	h.t.Fatalf("[ASSERT] cursor monotonicity violated: in=%d out=%d", cursorIn, cursorOut)
}

// AssertWakeReason verifies a wake_reason field at the given JSON path.
func (h *ScenarioHarness) AssertWakeReason(result *CommandResult, path, expected string) {
	h.t.Helper()
	h.AssertStringField(result, path, expected)
}

// AssertDegradedMarker verifies a degraded marker at the given JSON path.
func (h *ScenarioHarness) AssertDegradedMarker(result *CommandResult, path string, expected bool) {
	h.t.Helper()
	h.AssertBoolField(result, path, expected)
}

// AssertFocusTargetsPresent verifies that the given focus-target path is a non-empty array.
func (h *ScenarioHarness) AssertFocusTargetsPresent(result *CommandResult, path string) {
	h.t.Helper()
	h.AssertArrayNotEmpty(result, path)
}

// ParseCursor extracts a cursor value from a JSON path.
func ParseCursor(result *CommandResult, path string) (*int64, error) {
	v, ok := result.LookupJSONPath(path)
	if !ok {
		return nil, nil
	}
	switch cv := v.(type) {
	case float64:
		cursor := int64(cv)
		return &cursor, nil
	case int64:
		cursor := cv
		return &cursor, nil
	case string:
		cursor, err := strconv.ParseInt(cv, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse cursor %q: %w", cv, err)
		}
		return &cursor, nil
	case json.Number:
		cursor, err := cv.Int64()
		if err != nil {
			return nil, fmt.Errorf("parse cursor number %q: %w", cv.String(), err)
		}
		return &cursor, nil
	}
	return nil, fmt.Errorf("cursor path %q has unexpected type %T", path, v)
}

// WaitForCondition polls until a condition is met or timeout.
func (h *ScenarioHarness) WaitForCondition(check func() bool, timeout, pollInterval time.Duration) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

// Log writes a timestamped message to the test log.
func (h *ScenarioHarness) Log(format string, args ...interface{}) {
	h.t.Helper()
	elapsed := time.Since(h.startTime).Round(time.Millisecond)
	h.t.Logf("[%s] %s", elapsed, fmt.Sprintf(format, args...))
}

// String returns the human-readable retention mode.
func (r ArtifactRetention) String() string {
	switch r {
	case RetainAlways:
		return "always"
	case RetainNever:
		return "never"
	default:
		return "on_failure"
	}
}
