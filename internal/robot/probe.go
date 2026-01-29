// Package robot provides machine-readable output for AI agents.
// probe.go implements the --robot-probe command for active pane responsiveness testing.
package robot

import (
	"fmt"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TmuxClient defines the interface for tmux operations needed by probe.
// This allows mocking for tests.
type TmuxClient interface {
	CaptureForStatusDetection(target string) (string, error)
	CapturePaneOutput(target string, lines int) (string, error)
	SendKeys(target, keys string, enter bool) error
	SendInterrupt(target string) error
	SessionExists(name string) bool
	GetPanes(session string) ([]tmux.Pane, error)
}

// defaultTmuxClient wraps the tmux package functions to implement TmuxClient.
type defaultTmuxClient struct{}

func (c *defaultTmuxClient) CaptureForStatusDetection(target string) (string, error) {
	return tmux.CaptureForStatusDetection(target)
}

func (c *defaultTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	return tmux.CapturePaneOutput(target, lines)
}

func (c *defaultTmuxClient) SendKeys(target, keys string, enter bool) error {
	return tmux.SendKeys(target, keys, enter)
}

func (c *defaultTmuxClient) SendInterrupt(target string) error {
	return tmux.SendInterrupt(target)
}

func (c *defaultTmuxClient) SessionExists(name string) bool {
	return tmux.SessionExists(name)
}

func (c *defaultTmuxClient) GetPanes(session string) ([]tmux.Pane, error) {
	return tmux.GetPanes(session)
}

// CurrentTmuxClient is the client used for tmux operations.
// Tests can replace this with a mock.
var CurrentTmuxClient TmuxClient = &defaultTmuxClient{}

// =============================================================================
// Robot Probe Command (bd-1cu1f)
// =============================================================================
//
// The probe command actively tests whether a pane is responsive, not just running.
// A process can be in "running" state but completely hung. Active probing solves
// this by sending test input and checking if output changes.
//
// Output includes:
//   - responsive: whether the pane responded to the probe
//   - probe_method: which method was used (keystroke_echo, interrupt_test)
//   - confidence: high, medium, or low
//   - recommendation: healthy, likely_stuck, definitely_stuck

// ProbeMethod defines the valid probe methods
type ProbeMethod string

const (
	// ProbeMethodKeystrokeEcho sends a null/invisible char and checks cursor move
	ProbeMethodKeystrokeEcho ProbeMethod = "keystroke_echo"

	// ProbeMethodInterruptTest sends Ctrl-C and checks response (definitive but may interrupt work)
	ProbeMethodInterruptTest ProbeMethod = "interrupt_test"
)

// ValidProbeMethods returns the list of valid probe methods
func ValidProbeMethods() []ProbeMethod {
	return []ProbeMethod{ProbeMethodKeystrokeEcho, ProbeMethodInterruptTest}
}

// IsValidProbeMethod checks if a method string is valid
func IsValidProbeMethod(method string) bool {
	for _, valid := range ValidProbeMethods() {
		if string(valid) == method {
			return true
		}
	}
	return false
}

// ProbeConfidence represents the confidence level of probe results
type ProbeConfidence string

const (
	ProbeConfidenceHigh   ProbeConfidence = "high"   // Clear response to probe
	ProbeConfidenceMedium ProbeConfidence = "medium" // Ambiguous response
	ProbeConfidenceLow    ProbeConfidence = "low"    // No response but process shows activity
)

// ProbeRecommendation represents the probe recommendation
type ProbeRecommendation string

const (
	ProbeRecommendationHealthy         ProbeRecommendation = "healthy"
	ProbeRecommendationLikelyStuck     ProbeRecommendation = "likely_stuck"
	ProbeRecommendationDefinitelyStuck ProbeRecommendation = "definitely_stuck"
)

// ProbeFlags contains the parsed and validated CLI flags for --robot-probe
type ProbeFlags struct {
	Method     ProbeMethod // Probe method to use (default: keystroke_echo)
	TimeoutMs  int         // How long to wait for response in ms (default: 5000)
	Aggressive bool        // Use interrupt_test if keystroke_echo fails
}

// DefaultProbeFlags returns the default probe flag values
func DefaultProbeFlags() ProbeFlags {
	return ProbeFlags{
		Method:     ProbeMethodKeystrokeEcho,
		TimeoutMs:  5000,
		Aggressive: false,
	}
}

// ProbeOptions configures the probe command
type ProbeOptions struct {
	Session string     // Session name (required)
	Pane    int        // Pane index to probe (required)
	Flags   ProbeFlags // Parsed probe flags
}

// ProbeDetails contains detailed probe results
type ProbeDetails struct {
	InputSent        string `json:"input_sent"`         // What was sent (e.g., "\\x00")
	OutputChanged    bool   `json:"output_changed"`     // Whether output changed
	LatencyMs        int64  `json:"latency_ms"`         // Time between probe and response
	OutputDeltaLines int    `json:"output_delta_lines"` // How many lines changed
}

// ProbeOutput is the response for --robot-probe
type ProbeOutput struct {
	RobotResponse
	Session        string              `json:"session"`
	Pane           int                 `json:"pane"`
	Responsive     bool                `json:"responsive"`
	ProbeMethod    ProbeMethod         `json:"probe_method"`
	ProbeDetails   ProbeDetails        `json:"probe_details"`
	Confidence     ProbeConfidence     `json:"confidence"`
	Recommendation ProbeRecommendation `json:"recommendation"`
	Reasoning      string              `json:"reasoning"`
}

// ProbeEntry represents a single pane probe result inside a session response.
type ProbeEntry struct {
	Pane           int                 `json:"pane"`
	Responsive     bool                `json:"responsive"`
	ProbeMethod    ProbeMethod         `json:"probe_method"`
	ProbeDetails   ProbeDetails        `json:"probe_details"`
	Confidence     ProbeConfidence     `json:"confidence"`
	Recommendation ProbeRecommendation `json:"recommendation"`
	Reasoning      string              `json:"reasoning"`
	Error          string              `json:"error,omitempty"`
	ErrorCode      string              `json:"error_code,omitempty"`
	Hint           string              `json:"hint,omitempty"`
}

// ProbeSummary provides aggregate counts for a probe session.
type ProbeSummary struct {
	TotalProbed  int `json:"total_probed"`
	Responsive   int `json:"responsive"`
	Unresponsive int `json:"unresponsive"`
}

// ProbeSessionOutput is the response for --robot-probe with multi-pane support.
type ProbeSessionOutput struct {
	RobotResponse
	Session string       `json:"session"`
	Probes  []ProbeEntry `json:"probes"`
	Summary ProbeSummary `json:"summary"`
}

// ProbeSessionOptions configures multi-pane probe operations.
type ProbeSessionOptions struct {
	Session string
	Panes   []int
	Flags   ProbeFlags
}

// ProbeFlagError is the error output for invalid probe flags
type ProbeFlagError struct {
	RobotResponse
	ValidMethods []string `json:"valid_methods,omitempty"`
	MinTimeout   int      `json:"min_timeout,omitempty"`
	MaxTimeout   int      `json:"max_timeout,omitempty"`
}

// Probe flag validation constants
const (
	ProbeMinTimeoutMs = 100
	ProbeMaxTimeoutMs = 60000
)

// =============================================================================
// Baseline Capture (bd-ok7rj)
// =============================================================================
//
// All probe methods require capturing pane state before and after sending input.
// The baseline capture provides this shared infrastructure.

// PaneBaseline captures the state of a pane at a point in time.
// Used to detect changes after probe input is sent.
type PaneBaseline struct {
	Content     string    `json:"content"`      // Full pane content
	ContentHash string    `json:"content_hash"` // SHA-256 hash for quick comparison
	LineCount   int       `json:"line_count"`   // Number of non-empty lines
	CapturedAt  time.Time `json:"captured_at"`  // When the capture was taken
}

// PaneChange represents the difference between two pane states.
type PaneChange struct {
	Changed      bool  `json:"changed"`       // Whether content changed
	LinesDelta   int   `json:"lines_delta"`   // Change in line count (can be negative)
	LinesAdded   int   `json:"lines_added"`   // Approximate lines added
	LinesRemoved int   `json:"lines_removed"` // Approximate lines removed
	LatencyMs    int64 `json:"latency_ms"`    // Time between baseline and current capture
}

// CapturePaneBaseline captures the current state of a pane for later comparison.
// The target format is "session:window.pane" (e.g., "myproject:1.0").
func CapturePaneBaseline(target string) (*PaneBaseline, error) {
	// Use status detection line budget since probes need quick captures
	content, err := CurrentTmuxClient.CaptureForStatusDetection(target)
	if err != nil {
		return nil, fmt.Errorf("baseline capture failed: %w", err)
	}

	return &PaneBaseline{
		Content:     content,
		ContentHash: hashContent(content),
		LineCount:   countNonEmptyLines(content),
		CapturedAt:  time.Now(),
	}, nil
}

// CapturePaneBaselineWithLines captures baseline with a custom line count.
// Use this when you need more or fewer lines than the default.
func CapturePaneBaselineWithLines(target string, lines int) (*PaneBaseline, error) {
	content, err := CurrentTmuxClient.CapturePaneOutput(target, lines)
	if err != nil {
		return nil, fmt.Errorf("baseline capture failed: %w", err)
	}

	return &PaneBaseline{
		Content:     content,
		ContentHash: hashContent(content),
		LineCount:   countNonEmptyLines(content),
		CapturedAt:  time.Now(),
	}, nil
}

// ComparePaneState compares the current pane state against a baseline.
// Returns details about what changed between the two captures.
func ComparePaneState(baseline, current *PaneBaseline) PaneChange {
	if baseline == nil || current == nil {
		return PaneChange{Changed: true} // Can't compare, assume changed
	}

	latency := current.CapturedAt.Sub(baseline.CapturedAt).Milliseconds()

	// Quick hash comparison for unchanged case
	if baseline.ContentHash == current.ContentHash {
		return PaneChange{
			Changed:   false,
			LatencyMs: latency,
		}
	}

	// Content changed - compute line deltas
	linesDelta := current.LineCount - baseline.LineCount
	linesAdded := 0
	linesRemoved := 0

	if linesDelta > 0 {
		linesAdded = linesDelta
	} else if linesDelta < 0 {
		linesRemoved = -linesDelta
	}

	return PaneChange{
		Changed:      true,
		LinesDelta:   linesDelta,
		LinesAdded:   linesAdded,
		LinesRemoved: linesRemoved,
		LatencyMs:    latency,
	}
}

// hashContent computes a simple hash of content for quick comparison.
// Uses FNV-1a for speed (not cryptographic, just for change detection).
func hashContent(content string) string {
	// FNV-1a hash
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var hash uint64 = offset64
	for i := 0; i < len(content); i++ {
		hash ^= uint64(content[i])
		hash *= prime64
	}
	return fmt.Sprintf("%016x", hash)
}

// countNonEmptyLines counts lines that have non-whitespace content.
func countNonEmptyLines(content string) int {
	count := 0
	inLine := false
	hasContent := false

	for _, r := range content {
		if r == '\n' {
			if hasContent {
				count++
			}
			inLine = false
			hasContent = false
		} else {
			inLine = true
			if r != ' ' && r != '\t' && r != '\r' {
				hasContent = true
			}
		}
	}

	// Count last line if it has content and no trailing newline
	if inLine && hasContent {
		count++
	}

	return count
}

// =============================================================================
// Keystroke Echo Probe (bd-30nv1)
// =============================================================================
//
// The keystroke_echo method sends a non-disruptive character sequence and
// checks if the pane output changes. This indicates the process is responsive.

// ProbeResult contains the result of a probe operation.
type ProbeResult struct {
	Responsive     bool            // Whether the pane responded
	Details        ProbeDetails    // Detailed probe information
	Confidence     ProbeConfidence // Confidence level of the result
	Recommendation ProbeRecommendation
	Reasoning      string
}

// Probe poll interval for checking output changes
const probePollInterval = 50 * time.Millisecond

// probeKeystrokeEcho sends a non-disruptive keystroke and checks for response.
// It sends a space followed by backspace which should echo in most shells
// without changing state. Returns whether the pane responded within timeout.
func probeKeystrokeEcho(target string, timeout time.Duration) ProbeResult {
	result := ProbeResult{
		Responsive: false,
		Details: ProbeDetails{
			InputSent:     "Space+Backspace",
			OutputChanged: false,
			LatencyMs:     0,
		},
		Confidence:     ProbeConfidenceLow,
		Recommendation: ProbeRecommendationLikelyStuck,
		Reasoning:      "no response to probe input",
	}

	// 1. Capture baseline state
	baseline, err := CapturePaneBaseline(target)
	if err != nil {
		result.Reasoning = fmt.Sprintf("failed to capture baseline: %v", err)
		return result
	}

	// 2. Send non-disruptive probe: space followed by backspace
	// This is safer than null byte as it works in most shells and is visible feedback
	probeStart := time.Now()
	if err := CurrentTmuxClient.SendKeys(target, " ", false); err != nil {
		result.Reasoning = fmt.Sprintf("failed to send probe space: %v", err)
		return result
	}
	if err := CurrentTmuxClient.SendKeys(target, "BSpace", false); err != nil {
		result.Reasoning = fmt.Sprintf("failed to send probe backspace: %v", err)
		return result
	}

	// 3. Poll for response until timeout
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := CapturePaneBaseline(target)
		if err != nil {
			// Capture error, try again
			time.Sleep(probePollInterval)
			continue
		}

		change := ComparePaneState(baseline, current)
		if change.Changed {
			latency := time.Since(probeStart).Milliseconds()
			result.Responsive = true
			result.Details.OutputChanged = true
			result.Details.LatencyMs = latency
			result.Details.OutputDeltaLines = change.LinesDelta
			result.Confidence = ProbeConfidenceHigh
			result.Recommendation = ProbeRecommendationHealthy
			result.Reasoning = fmt.Sprintf("pane responded in %dms", latency)
			return result
		}

		time.Sleep(probePollInterval)
	}

	// 4. No response within timeout
	result.Details.LatencyMs = timeout.Milliseconds()
	result.Confidence = ProbeConfidenceMedium
	result.Reasoning = fmt.Sprintf("no output change detected within %dms", timeout.Milliseconds())
	return result
}

// =============================================================================
// Interrupt Test Probe (bd-3ah0k)
// =============================================================================
//
// The interrupt_test method sends Ctrl-C and checks for response. This is more
// aggressive but definitive - if a process responds to interrupt, it's alive.
// WARNING: This may interrupt ongoing work and cause loss of in-progress output.

// probeInterruptTest sends Ctrl-C and checks for response.
// This is a definitive but disruptive test that may interrupt ongoing work.
// Use only when keystroke_echo is ambiguous or with --aggressive flag.
func probeInterruptTest(target string, timeout time.Duration) ProbeResult {
	result := ProbeResult{
		Responsive: false,
		Details: ProbeDetails{
			InputSent:     "Ctrl-C",
			OutputChanged: false,
			LatencyMs:     0,
		},
		Confidence:     ProbeConfidenceLow,
		Recommendation: ProbeRecommendationDefinitelyStuck,
		Reasoning:      "no response to interrupt signal",
	}

	// 1. Capture baseline state
	baseline, err := CapturePaneBaseline(target)
	if err != nil {
		result.Reasoning = fmt.Sprintf("failed to capture baseline: %v", err)
		return result
	}

	// 2. Send interrupt signal (Ctrl-C)
	probeStart := time.Now()
	if err := CurrentTmuxClient.SendInterrupt(target); err != nil {
		result.Reasoning = fmt.Sprintf("failed to send interrupt: %v", err)
		return result
	}

	// 3. Poll for response until timeout
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := CapturePaneBaseline(target)
		if err != nil {
			// Capture error, try again
			time.Sleep(probePollInterval)
			continue
		}

		change := ComparePaneState(baseline, current)
		if change.Changed {
			latency := time.Since(probeStart).Milliseconds()
			result.Responsive = true
			result.Details.OutputChanged = true
			result.Details.LatencyMs = latency
			result.Details.OutputDeltaLines = change.LinesDelta
			result.Confidence = ProbeConfidenceHigh
			result.Recommendation = ProbeRecommendationHealthy
			result.Reasoning = fmt.Sprintf("pane responded to interrupt in %dms (may have interrupted work)", latency)
			return result
		}

		time.Sleep(probePollInterval)
	}

	// 4. No response within timeout - process is definitely stuck
	result.Details.LatencyMs = timeout.Milliseconds()
	result.Confidence = ProbeConfidenceHigh
	result.Recommendation = ProbeRecommendationDefinitelyStuck
	result.Reasoning = fmt.Sprintf("no response to Ctrl-C within %dms - process appears hung", timeout.Milliseconds())
	return result
}

// ParseProbeFlags parses and validates probe flags from string values.
// Returns an error if any flag is invalid.
func ParseProbeFlags(methodStr string, timeoutMs int, aggressive bool) (*ProbeFlags, error) {
	flags := DefaultProbeFlags()

	// Parse method (use default if empty)
	if methodStr != "" {
		if !IsValidProbeMethod(methodStr) {
			return nil, fmt.Errorf("invalid method: %s, must be one of %v", methodStr, ValidProbeMethods())
		}
		flags.Method = ProbeMethod(methodStr)
	}

	// Parse timeout
	if timeoutMs != 0 {
		if timeoutMs < ProbeMinTimeoutMs || timeoutMs > ProbeMaxTimeoutMs {
			return nil, fmt.Errorf("timeout must be %d-%dms, got %d", ProbeMinTimeoutMs, ProbeMaxTimeoutMs, timeoutMs)
		}
		flags.TimeoutMs = timeoutMs
	}

	// Validate aggressive flag
	if aggressive && flags.Method != ProbeMethodKeystrokeEcho {
		return nil, fmt.Errorf("--aggressive only valid with --method=%s", ProbeMethodKeystrokeEcho)
	}
	flags.Aggressive = aggressive

	return &flags, nil
}

// PrintProbeFlagError outputs a structured error for invalid probe flags
func PrintProbeFlagError(err error) error {
	validMethods := make([]string, len(ValidProbeMethods()))
	for i, m := range ValidProbeMethods() {
		validMethods[i] = string(m)
	}

	output := ProbeFlagError{
		RobotResponse: NewRobotResponse(false),
		ValidMethods:  validMethods,
		MinTimeout:    ProbeMinTimeoutMs,
		MaxTimeout:    ProbeMaxTimeoutMs,
	}
	output.Error = err.Error()
	output.ErrorCode = ErrCodeInvalidFlag
	output.Hint = fmt.Sprintf("Valid methods: %v, timeout range: %d-%dms", validMethods, ProbeMinTimeoutMs, ProbeMaxTimeoutMs)

	return encodeJSON(output)
}

// GetProbe performs probe on a pane and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetProbe(opts ProbeOptions) (*ProbeOutput, error) {
	output := &ProbeOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        opts.Session,
		Pane:           opts.Pane,
		ProbeMethod:    opts.Flags.Method,
		ProbeDetails:   ProbeDetails{},
		Responsive:     false,
		Confidence:     ProbeConfidenceLow,
		Recommendation: ProbeRecommendationLikelyStuck,
		Reasoning:      "",
	}

	// Check if session exists
	if !CurrentTmuxClient.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	// Get pane info to verify it exists
	panes, err := CurrentTmuxClient.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get pane info: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	var targetPane *tmux.Pane
	for i := range panes {
		if panes[i].Index == opts.Pane {
			targetPane = &panes[i]
			break
		}
	}

	if targetPane == nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("pane %d not found in session '%s'", opts.Pane, opts.Session),
			ErrCodePaneNotFound,
			fmt.Sprintf("Session has %d pane(s), indices 0-%d", len(panes), len(panes)-1),
		)
		return output, nil
	}

	// Build target string for tmux commands
	target, err := tmux.FormatPaneTarget(opts.Session, opts.Pane)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get window index: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}
	timeout := time.Duration(opts.Flags.TimeoutMs) * time.Millisecond

	// Execute probe based on method
	var probeResult ProbeResult
	switch opts.Flags.Method {
	case ProbeMethodKeystrokeEcho:
		probeResult = probeKeystrokeEcho(target, timeout)
	case ProbeMethodInterruptTest:
		probeResult = probeInterruptTest(target, timeout)
	default:
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("unknown probe method: %s", opts.Flags.Method),
			ErrCodeInvalidFlag,
			"Valid methods: keystroke_echo, interrupt_test",
		)
		return output, nil
	}

	// If keystroke_echo failed and aggressive mode is enabled, try interrupt_test
	if !probeResult.Responsive && opts.Flags.Aggressive && opts.Flags.Method == ProbeMethodKeystrokeEcho {
		// Escalate to interrupt_test for definitive answer
		probeResult = probeInterruptTest(target, timeout)
		if probeResult.Responsive {
			probeResult.Reasoning = "escalated from keystroke_echo: " + probeResult.Reasoning
		}
	}

	// Populate output from probe result
	output.Responsive = probeResult.Responsive
	output.ProbeDetails = probeResult.Details
	output.Confidence = probeResult.Confidence
	output.Recommendation = probeResult.Recommendation
	output.Reasoning = probeResult.Reasoning

	return output, nil
}

// PrintProbe outputs probe results for a pane.
// This is a thin wrapper around GetProbe() for CLI output.
func PrintProbe(opts ProbeOptions) error {
	output, err := GetProbe(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

func probeEntryFromOutput(output *ProbeOutput) ProbeEntry {
	entry := ProbeEntry{
		Pane:           output.Pane,
		Responsive:     output.Responsive,
		ProbeMethod:    output.ProbeMethod,
		ProbeDetails:   output.ProbeDetails,
		Confidence:     output.Confidence,
		Recommendation: output.Recommendation,
		Reasoning:      output.Reasoning,
	}
	if !output.Success {
		entry.Error = output.Error
		entry.ErrorCode = output.ErrorCode
		entry.Hint = output.Hint
	}
	return entry
}

// GetProbeSession probes panes in a session and returns aggregated output and exit code.
// Exit code: 0 = all responsive, 1 = partial failures, 2 = complete failure.
func GetProbeSession(opts ProbeSessionOptions) (*ProbeSessionOutput, int) {
	output := &ProbeSessionOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Probes:        []ProbeEntry{},
		Summary:       ProbeSummary{},
	}

	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide a session name: ntm --robot-probe=myproject",
		)
		return output, 2
	}

	if !CurrentTmuxClient.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, 2
	}

	panes, err := CurrentTmuxClient.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, 2
	}

	var targetPanes []int
	if len(opts.Panes) == 0 {
		for _, pane := range panes {
			agentType := detectAgentTypeFromPane(pane)
			if agentType == "user" {
				continue
			}
			targetPanes = append(targetPanes, pane.Index)
		}
	} else {
		seen := make(map[int]struct{}, len(opts.Panes))
		for _, pane := range opts.Panes {
			if _, ok := seen[pane]; ok {
				continue
			}
			seen[pane] = struct{}{}
			targetPanes = append(targetPanes, pane)
		}
	}

	if len(targetPanes) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no panes to probe in session '%s'", opts.Session),
			ErrCodePaneNotFound,
			"Use --panes to target a specific pane or spawn agents first",
		)
		return output, 2
	}

	sort.Ints(targetPanes)

	for _, paneIndex := range targetPanes {
		probeOutput, _ := GetProbe(ProbeOptions{
			Session: opts.Session,
			Pane:    paneIndex,
			Flags:   opts.Flags,
		})
		entry := probeEntryFromOutput(probeOutput)
		output.Probes = append(output.Probes, entry)
		output.Summary.TotalProbed++
		if entry.Error == "" && entry.Responsive {
			output.Summary.Responsive++
		} else {
			output.Summary.Unresponsive++
		}
	}

	if !output.Success {
		return output, 2
	}
	if output.Summary.TotalProbed == 0 {
		return output, 2
	}
	if output.Summary.Responsive == output.Summary.TotalProbed {
		return output, 0
	}
	if output.Summary.Responsive == 0 {
		return output, 2
	}
	return output, 1
}

// PrintProbeSession outputs multi-pane probe results as JSON.
// Returns 0 on success, 1 on partial failure, 2 on complete failure.
func PrintProbeSession(opts ProbeSessionOptions) int {
	output, exitCode := GetProbeSession(opts)
	outputJSON(output)
	return exitCode
}

// FormatProbeDuration formats a probe duration in milliseconds
func FormatProbeDuration(d time.Duration) int64 {
	return d.Milliseconds()
}
