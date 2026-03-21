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
)

const (
	defaultArtifactRootEnv = "NTM_E2E_ARTIFACT_ROOT"
	legacyLogDirEnv        = "E2E_LOG_DIR"
	retainPolicyEnv        = "NTM_E2E_RETAIN"
	debugArtifactsEnv      = "NTM_E2E_DEBUG"
	defaultRunTimeout      = 30 * time.Second
)

type ArtifactKind string

const (
	ArtifactLogs      ArtifactKind = "logs"
	ArtifactCommands  ArtifactKind = "commands"
	ArtifactCaptures  ArtifactKind = "captures"
	ArtifactSummaries ArtifactKind = "summaries"
	ArtifactTransport ArtifactKind = "transport"
	ArtifactTraces    ArtifactKind = "traces"
)

var allArtifactKinds = []ArtifactKind{
	ArtifactLogs,
	ArtifactCommands,
	ArtifactCaptures,
	ArtifactSummaries,
	ArtifactTransport,
	ArtifactTraces,
}

type ArtifactRetention string

const (
	RetainOnFailure ArtifactRetention = "on_failure"
	RetainAlways    ArtifactRetention = "always"
	RetainNever     ArtifactRetention = "never"
)

type HarnessOptions struct {
	Scenario     string
	ArtifactRoot string
	SessionPrefix string
	RunToken     string
	Debug        bool
	Retain       ArtifactRetention
	Clock        func() time.Time
	FailureState func() bool
	LookPath     func(string) (string, error)
	Runner       RunnerFunc
}

type TmuxSessionOptions struct {
	Width      int
	Height     int
	WorkingDir string
	ExtraArgs  []string
	Timeout    time.Duration
}

type CommandSpec struct {
	Name         string
	Path         string
	Args         []string
	Dir          string
	Env          []string
	Timeout      time.Duration
	AllowFailure bool
}

type CommandResult struct {
	Name         string        `json:"name"`
	Path         string        `json:"path"`
	Args         []string      `json:"args,omitempty"`
	Dir          string        `json:"dir,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	CompletedAt  time.Time     `json:"completed_at"`
	Duration     time.Duration `json:"duration"`
	ExitCode     int           `json:"exit_code"`
	TimedOut     bool          `json:"timed_out,omitempty"`
	Stdout       []byte        `json:"-"`
	Stderr       []byte        `json:"-"`
	StdoutPath   string        `json:"stdout_path,omitempty"`
	StderrPath   string        `json:"stderr_path,omitempty"`
	MetadataPath string        `json:"metadata_path,omitempty"`
}

type RunnerFunc func(context.Context, CommandSpec) (CommandResult, error)

type OperatorLoopState struct {
	Cursor      string         `json:"cursor,omitempty"`
	WakeReason  string         `json:"wake_reason,omitempty"`
	WakeReasons []string       `json:"wake_reasons,omitempty"`
	Degraded    bool           `json:"degraded,omitempty"`
	FocusTarget string         `json:"focus_target,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type OperatorLoopExpectation struct {
	RequireCursor      bool `json:"require_cursor,omitempty"`
	RequireWakeReason  bool `json:"require_wake_reason,omitempty"`
	RequireFocusTarget bool `json:"require_focus_target,omitempty"`
	AllowDegraded      bool `json:"allow_degraded,omitempty"`
}

type cleanupEntry struct {
	name string
	fn   func() error
}

type timelineEntry struct {
	Timestamp string         `json:"timestamp"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Details   map[string]any `json:"details,omitempty"`
}

type cleanupReport struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type HarnessManifest struct {
	Scenario      string                     `json:"scenario"`
	Session       string                     `json:"session"`
	RunToken      string                     `json:"run_token"`
	ArtifactRoot  string                     `json:"artifact_root"`
	StartedAt     string                     `json:"started_at"`
	CompletedAt   string                     `json:"completed_at"`
	RetainPolicy  ArtifactRetention          `json:"retain_policy"`
	Debug         bool                       `json:"debug"`
	Failed        bool                       `json:"failed"`
	Retained      bool                       `json:"retained"`
	Directories   map[ArtifactKind]string    `json:"directories"`
	TimelinePath  string                     `json:"timeline_path"`
	Cleanup       []cleanupReport            `json:"cleanup"`
	CommandCount  uint64                     `json:"command_count"`
	ArtifactCount uint64                     `json:"artifact_count"`
}

type ScenarioHarness struct {
	tb          testing.TB
	scenario    string
	slug        string
	runToken    string
	startedAt   time.Time
	root        string
	sessionName string
	timeline    string
	retain      ArtifactRetention
	debug       bool
	failed      func() bool
	lookPath    func(string) (string, error)
	runner      RunnerFunc

	dirs      map[ArtifactKind]string
	cleanup   []cleanupEntry
	cleanupMu sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once

	commandSeq  atomic.Uint64
	artifactSeq atomic.Uint64
}

var harnessRunCounter atomic.Uint64

func NewScenarioHarness(tb testing.TB, opts HarnessOptions) (*ScenarioHarness, error) {
	tb.Helper()

	scenario := strings.TrimSpace(opts.Scenario)
	if scenario == "" {
		scenario = sanitizeName(tb.Name())
	}
	slug := sanitizeName(scenario)
	if slug == "" {
		slug = "scenario"
	}

	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	startedAt := clock().UTC()
	runToken := strings.TrimSpace(opts.RunToken)
	if runToken == "" {
		runToken = fmt.Sprintf("%03d", harnessRunCounter.Add(1))
	}
	runToken = sanitizeName(runToken)
	if runToken == "" {
		runToken = "run"
	}

	artifactRoot := resolveArtifactRoot(opts.ArtifactRoot)
	runRoot := filepath.Join(artifactRoot, slug, fmt.Sprintf("%s-%s", formatArtifactTimestamp(startedAt), runToken))
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact root %q: %w", runRoot, err)
	}

	dirs := make(map[ArtifactKind]string, len(allArtifactKinds))
	for _, kind := range allArtifactKinds {
		dir := filepath.Join(runRoot, string(kind))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s artifact dir: %w", kind, err)
		}
		dirs[kind] = dir
	}

	retain := resolveRetentionPolicy(opts.Retain)
	debug := opts.Debug || os.Getenv(debugArtifactsEnv) == "1"
	failureState := opts.FailureState
	if failureState == nil {
		failureState = tb.Failed
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	runner := opts.Runner
	if runner == nil {
		runner = defaultRunner
	}

	h := &ScenarioHarness{
		tb:          tb,
		scenario:    scenario,
		slug:        slug,
		runToken:    runToken,
		startedAt:   startedAt,
		root:        runRoot,
		sessionName: makeSessionName(opts.SessionPrefix, slug, startedAt, runToken),
		timeline:    filepath.Join(runRoot, "timeline.jsonl"),
		retain:      retain,
		debug:       debug,
		failed:      failureState,
		lookPath:    lookPath,
		runner:      runner,
		dirs:        dirs,
	}

	if err := h.RecordStep("harness_started", map[string]any{
		"scenario":      scenario,
		"artifact_root": runRoot,
		"session":       h.sessionName,
		"retain":        h.retain,
		"debug":         h.debug,
	}); err != nil {
		return nil, err
	}

	tb.Cleanup(h.Close)
	return h, nil
}

func (h *ScenarioHarness) Root() string {
	return h.root
}

func (h *ScenarioHarness) SessionName() string {
	return h.sessionName
}

func (h *ScenarioHarness) Dir(kind ArtifactKind) string {
	return h.dirs[kind]
}

func (h *ScenarioHarness) AddCleanup(name string, fn func() error) {
	h.cleanupMu.Lock()
	defer h.cleanupMu.Unlock()
	h.cleanup = append(h.cleanup, cleanupEntry{name: name, fn: fn})
}

func (h *ScenarioHarness) SetupTmuxSession(opts TmuxSessionOptions) error {
	tmuxPath, err := h.lookPath("tmux")
	if err != nil {
		_ = h.RecordStep("tmux_missing", map[string]any{"error": err.Error()})
		return fmt.Errorf("resolve tmux: %w", err)
	}

	width := opts.Width
	if width == 0 {
		width = 200
	}
	height := opts.Height
	if height == 0 {
		height = 50
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	args := []string{"new-session", "-d", "-s", h.sessionName, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height)}
	if opts.WorkingDir != "" {
		args = append(args, "-c", opts.WorkingDir)
	}
	args = append(args, opts.ExtraArgs...)

	if _, err := h.RunCommand(CommandSpec{
		Name:    "tmux-new-session",
		Path:    tmuxPath,
		Args:    args,
		Timeout: timeout,
	}); err != nil {
		return fmt.Errorf("create tmux session %q: %w", h.sessionName, err)
	}

	h.AddCleanup("tmux-kill-session", func() error {
		result, err := h.RunCommand(CommandSpec{
			Name:         "tmux-kill-session",
			Path:         tmuxPath,
			Args:         []string{"kill-session", "-t", h.sessionName},
			Timeout:      10 * time.Second,
			AllowFailure: true,
		})
		if err == nil {
			return nil
		}
		if isBenignTmuxCleanupError(result.Stderr) {
			return nil
		}
		return err
	})

	return nil
}

func (h *ScenarioHarness) RunNTM(args ...string) (CommandResult, error) {
	ntmPath, err := h.resolveNTMBinary()
	if err != nil {
		return CommandResult{}, err
	}
	return h.RunCommand(CommandSpec{
		Name: "ntm",
		Path: ntmPath,
		Args: args,
	})
}

func (h *ScenarioHarness) RunCommand(spec CommandSpec) (CommandResult, error) {
	h.tb.Helper()

	if spec.Path == "" {
		return CommandResult{}, errors.New("command path is required")
	}
	if spec.Timeout == 0 {
		spec.Timeout = defaultRunTimeout
	}
	if spec.Name == "" {
		spec.Name = filepath.Base(spec.Path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), spec.Timeout)
	defer cancel()

	result, runErr := h.runner(ctx, spec)
	result.Name = spec.Name
	result.Path = spec.Path
	result.Args = append([]string(nil), spec.Args...)
	result.Dir = spec.Dir
	if result.StartedAt.IsZero() {
		result.StartedAt = time.Now().UTC()
	}
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now().UTC()
	}
	if result.Duration == 0 {
		result.Duration = result.CompletedAt.Sub(result.StartedAt)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		if result.ExitCode == 0 {
			result.ExitCode = -1
		}
	}

	seq := h.commandSeq.Add(1)
	base := fmt.Sprintf("%03d-%s", seq, sanitizeName(spec.Name))

	stdoutPath, err := h.writeArtifact(ArtifactCommands, base+".stdout.txt", result.Stdout)
	if err != nil {
		return result, err
	}
	stderrPath, err := h.writeArtifact(ArtifactCommands, base+".stderr.txt", result.Stderr)
	if err != nil {
		return result, err
	}
	result.StdoutPath = stdoutPath
	result.StderrPath = stderrPath

	metadata := map[string]any{
		"name":          result.Name,
		"path":          result.Path,
		"args":          result.Args,
		"dir":           result.Dir,
		"started_at":    result.StartedAt.Format(time.RFC3339Nano),
		"completed_at":  result.CompletedAt.Format(time.RFC3339Nano),
		"duration_ms":   result.Duration.Milliseconds(),
		"exit_code":     result.ExitCode,
		"timed_out":     result.TimedOut,
		"stdout_path":   stdoutPath,
		"stderr_path":   stderrPath,
		"allow_failure": spec.AllowFailure,
	}
	if runErr != nil {
		metadata["error"] = runErr.Error()
	}
	metadataPath, err := h.WriteJSONArtifact(ArtifactCommands, base+".json", metadata)
	if err != nil {
		return result, err
	}
	result.MetadataPath = metadataPath

	stepDetails := map[string]any{
		"path":         result.Path,
		"args":         result.Args,
		"dir":          result.Dir,
		"exit_code":    result.ExitCode,
		"timed_out":    result.TimedOut,
		"stdout_path":  stdoutPath,
		"stderr_path":  stderrPath,
		"metadata_path": metadataPath,
	}
	if runErr != nil {
		stepDetails["error"] = runErr.Error()
	}
	if err := h.RecordStep("command", map[string]any{
		"name":   result.Name,
		"result": stepDetails,
	}); err != nil {
		return result, err
	}

	if runErr != nil && !spec.AllowFailure {
		return result, runErr
	}
	return result, nil
}

func (h *ScenarioHarness) RecordStep(name string, details map[string]any) error {
	entry := timelineEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Kind:      "step",
		Name:      name,
		Details:   details,
	}
	return h.appendTimeline(entry)
}

func (h *ScenarioHarness) WriteCapture(name string, data []byte) (string, error) {
	return h.writeArtifact(ArtifactCaptures, name, data)
}

func (h *ScenarioHarness) WriteRenderedSummary(name string, data []byte) (string, error) {
	return h.writeArtifact(ArtifactSummaries, name, data)
}

func (h *ScenarioHarness) WriteTransportCapture(name string, data []byte) (string, error) {
	return h.writeArtifact(ArtifactTransport, name, data)
}

func (h *ScenarioHarness) WriteCursorTrace(name string, data []byte) (string, error) {
	return h.writeArtifact(ArtifactTraces, name, data)
}

func (h *ScenarioHarness) WriteJSONArtifact(kind ArtifactKind, name string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal %s artifact: %w", kind, err)
	}
	return h.writeArtifact(kind, name, append(data, '\n'))
}

func (h *ScenarioHarness) AssertOperatorLoopState(name string, state OperatorLoopState, expect OperatorLoopExpectation) error {
	var problems []string

	if expect.RequireCursor && strings.TrimSpace(state.Cursor) == "" {
		problems = append(problems, "cursor is required")
	}
	if expect.RequireWakeReason && strings.TrimSpace(state.WakeReason) == "" && len(state.WakeReasons) == 0 {
		problems = append(problems, "wake reason is required")
	}
	if expect.RequireFocusTarget && strings.TrimSpace(state.FocusTarget) == "" {
		problems = append(problems, "focus target is required")
	}
	if !expect.AllowDegraded && state.Degraded {
		problems = append(problems, "degraded marker was not expected")
	}

	payload := map[string]any{
		"state":       state,
		"expectation": expect,
		"problems":    problems,
	}
	_, _ = h.WriteJSONArtifact(ArtifactSummaries, sanitizeName(name)+"-operator-loop.json", payload)

	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func (h *ScenarioHarness) Close() {
	h.closeOnce.Do(func() {
		h.cleanupMu.Lock()
		cleanup := append([]cleanupEntry(nil), h.cleanup...)
		h.cleanupMu.Unlock()

		reports := make([]cleanupReport, 0, len(cleanup))
		for i := len(cleanup) - 1; i >= 0; i-- {
			entry := cleanup[i]
			err := entry.fn()
			report := cleanupReport{Name: entry.name, Status: "ok"}
			if err != nil {
				report.Status = "error"
				report.Error = err.Error()
				h.tb.Logf("[E2E-CLEANUP] %s failed: %v", entry.name, err)
			}
			reports = append(reports, report)
			_ = h.RecordStep("cleanup", map[string]any{
				"name":   entry.name,
				"status": report.Status,
				"error":  report.Error,
			})
		}

		retained := h.shouldRetainArtifacts()
		manifest := HarnessManifest{
			Scenario:      h.scenario,
			Session:       h.sessionName,
			RunToken:      h.runToken,
			ArtifactRoot:  h.root,
			StartedAt:     h.startedAt.Format(time.RFC3339Nano),
			CompletedAt:   time.Now().UTC().Format(time.RFC3339Nano),
			RetainPolicy:  h.retain,
			Debug:         h.debug,
			Failed:        h.failed(),
			Retained:      retained,
			Directories:   h.dirs,
			TimelinePath:  h.timeline,
			Cleanup:       reports,
			CommandCount:  h.commandSeq.Load(),
			ArtifactCount: h.artifactSeq.Load(),
		}
		_, _ = h.writeRootJSON("manifest.json", manifest)

		if retained {
			h.tb.Logf("[E2E-ARTIFACTS] retained at %s", h.root)
			return
		}
		if err := os.RemoveAll(h.root); err != nil {
			h.tb.Logf("[E2E-ARTIFACTS] failed to remove %s: %v", h.root, err)
		}
	})
}

func (h *ScenarioHarness) shouldRetainArtifacts() bool {
	if h.debug {
		return true
	}
	switch h.retain {
	case RetainAlways:
		return true
	case RetainNever:
		return false
	default:
		return h.failed()
	}
}

func (h *ScenarioHarness) resolveNTMBinary() (string, error) {
	if override := strings.TrimSpace(os.Getenv("E2E_NTM_BIN")); override != "" {
		return override, nil
	}
	ntmPath, err := h.lookPath("ntm")
	if err != nil {
		return "", fmt.Errorf("resolve ntm binary: %w", err)
	}
	return ntmPath, nil
}

func (h *ScenarioHarness) writeArtifact(kind ArtifactKind, name string, data []byte) (string, error) {
	dir, ok := h.dirs[kind]
	if !ok {
		return "", fmt.Errorf("unknown artifact kind %q", kind)
	}
	seq := h.artifactSeq.Add(1)
	filename := fmt.Sprintf("%03d-%s", seq, sanitizeFileName(name))
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s artifact %q: %w", kind, path, err)
	}
	return path, nil
}

func (h *ScenarioHarness) writeRootJSON(name string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(h.root, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (h *ScenarioHarness) appendTimeline(entry timelineEntry) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	f, err := os.OpenFile(h.timeline, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open timeline %q: %w", h.timeline, err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal timeline entry: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append timeline entry: %w", err)
	}
	return nil
}

func defaultRunner(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	started := time.Now().UTC()
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	completed := time.Now().UTC()

	result := CommandResult{
		StartedAt:   started,
		CompletedAt: completed,
		Duration:    completed.Sub(started),
		Stdout:      stdout.Bytes(),
		Stderr:      stderr.Bytes(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	return result, err
}

func resolveArtifactRoot(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if value := strings.TrimSpace(os.Getenv(defaultArtifactRootEnv)); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv(legacyLogDirEnv)); value != "" {
		return value
	}
	return filepath.Join(os.TempDir(), "ntm-e2e-artifacts")
}

func resolveRetentionPolicy(explicit ArtifactRetention) ArtifactRetention {
	if explicit != "" {
		return explicit
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(retainPolicyEnv))) {
	case "always":
		return RetainAlways
	case "never":
		return RetainNever
	default:
		return RetainOnFailure
	}
}

func formatArtifactTimestamp(ts time.Time) string {
	return ts.UTC().Format("20060102T150405.000Z")
}

func formatSessionTimestamp(ts time.Time) string {
	return ts.UTC().Format("20060102T150405Z")
}

func makeSessionName(prefix, scenario string, startedAt time.Time, runToken string) string {
	prefix = sanitizeName(prefix)
	if prefix == "" {
		prefix = "ntm-e2e"
	}
	scenario = sanitizeName(scenario)
	if scenario == "" {
		scenario = "scenario"
	}
	name := fmt.Sprintf("%s-%s-%s-%s", prefix, scenario, formatSessionTimestamp(startedAt), sanitizeName(runToken))
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

func sanitizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "artifact"
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	base = sanitizeName(base)
	if base == "" {
		base = "artifact"
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return base
	}
	ext = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			return r
		default:
			return -1
		}
	}, ext)
	if ext == "." {
		ext = ""
	}
	return base + ext
}

func isBenignTmuxCleanupError(stderr []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(stderr)))
	return strings.Contains(text, "can't find session") || strings.Contains(text, "no server running")
}
