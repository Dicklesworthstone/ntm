// Package robot provides machine-readable output for AI agents.
// probe.go implements the --robot-probe command for active pane responsiveness testing.
package robot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// TmuxClient defines the interface for tmux operations needed by probe.
// This allows mocking for tests.
type TmuxClient interface {
	CaptureForStatusDetection(target string) (string, error)
	CapturePaneOutput(target string, lines int) (string, error)
	SendKeys(target, keys string, enter bool) error
	// SendKeyName presses a tmux KEY NAME (BSpace, Escape, ...). It is
	// distinct from SendKeys, which sends literally (-l) and would type the
	// key's name into the pane as characters.
	SendKeyName(target, keyName string) error
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

func (c *defaultTmuxClient) SendKeyName(target, keyName string) error {
	return tmux.SendKeyName(target, keyName)
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

	// ProbeMethodWakePing is the structured replacement for the raw-tmux
	// rate-limit probe folklore (`tmux send-keys "ping" Enter; sleep 5;
	// --robot-tail` — ntm-7rgt): keystroke-echo responsiveness mechanics
	// (space+backspace, no junk prompt submitted) plus rate-limit
	// classification of the post-probe screen and a short tail sample, so
	// one call answers both "is the TUI alive" and "is the wall still up".
	ProbeMethodWakePing ProbeMethod = "wake_ping"
)

// ValidProbeMethods returns the list of valid probe methods
func ValidProbeMethods() []ProbeMethod {
	return []ProbeMethod{ProbeMethodKeystrokeEcho, ProbeMethodInterruptTest, ProbeMethodWakePing}
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

	// Wake-ping extras (ProbeMethodWakePing only): whether the post-probe
	// screen still shows rate-limit patterns, and the last few visible
	// lines so orchestrators skip the follow-up --robot-tail round.
	StillRateLimited *bool    `json:"still_rate_limited,omitempty"`
	TailSample       []string `json:"tail_sample,omitempty"`
}

// ProbeOutput is the response for --robot-probe
type ProbeOutput struct {
	RobotResponse
	Session string `json:"session"`
	Pane    int    `json:"pane"`
	// PaneRef is the unambiguous address of the pane that was actually probed:
	// "window.pane" on a multi-window session, the bare pane index otherwise.
	// Pane echoes the SELECTOR that was requested, which on a multi-window
	// session names a whole window and can match more than one pane.
	PaneRef        string              `json:"pane_ref,omitempty"`
	Responsive     bool                `json:"responsive"`
	ProbeMethod    ProbeMethod         `json:"probe_method"`
	ProbeDetails   ProbeDetails        `json:"probe_details"`
	Confidence     ProbeConfidence     `json:"confidence"`
	Recommendation ProbeRecommendation `json:"recommendation"`
	Reasoning      string              `json:"reasoning"`
}

// ProbeEntry represents a single pane probe result inside a session response.
type ProbeEntry struct {
	Pane int `json:"pane"`
	// PaneRef names exactly which pane this entry describes; see ProbeOutput.
	PaneRef        string              `json:"pane_ref,omitempty"`
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
		LineCount:   util.CountNonEmptyLines(content),
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
	if baseline.ContentHash == current.ContentHash && baseline.LineCount == current.LineCount {
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
// probeWakePing runs the keystroke-echo responsiveness mechanics, then
// classifies the post-probe screen for rate-limit patterns and attaches a
// short tail sample (ntm-7rgt). Responsive means the TUI is alive;
// still_rate_limited answers whether the wall is up — the two are
// independent facts and are reported separately.
func probeWakePing(target string, agentType string, timeout time.Duration) ProbeResult {
	result := probeKeystrokeEcho(target, timeout)
	result.Details.InputSent = "Space+Backspace (wake-ping)"

	capture, err := CurrentTmuxClient.CapturePaneOutput(target, 15)
	if err != nil {
		result.Reasoning = strings.TrimSpace(result.Reasoning + "; post-probe capture failed: " + err.Error())
		return result
	}
	clean := status.StripANSI(capture)
	limited := isRateLimitPatternMatch(DefaultLibrary.Match(clean, agentType))
	result.Details.StillRateLimited = &limited

	lines := splitLines(clean)
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	result.Details.TailSample = lines

	if limited {
		result.Reasoning = strings.TrimSpace(result.Reasoning + "; rate-limit patterns still present on screen")
	} else if result.Responsive {
		result.Reasoning = strings.TrimSpace(result.Reasoning + "; no rate-limit patterns on screen")
	}
	return result
}

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

	// 2. Send an OBSERVABLE stimulus: a single space, cleaned up only after we
	// have looked for it.
	//
	// This used to send space and backspace back to back, before the first
	// capture. That pair is a net-zero edit: by the time the poll loop ran,
	// the rendered screen was byte-identical to the baseline, so the probe
	// could never observe its own stimulus. A healthy idle agent sitting at a
	// static composer was therefore reported not responsive / likely_stuck,
	// and any "responsive" verdict came from unrelated TUI repaint (a spinner
	// or token counter) rather than from the probe — uncorrelated with what
	// the surface claims to measure (bd-5bexl).
	probeStart := time.Now()
	if err := CurrentTmuxClient.SendKeys(target, " ", false); err != nil {
		result.Reasoning = fmt.Sprintf("failed to send probe space: %v", err)
		return result
	}
	// Erase the probe character on every exit path, including timeout and
	// capture failure, so the probe never leaves a stray space in a live
	// composer. Registered only after the space actually went out, so a
	// failed send cannot delete a real character the operator typed.
	defer func() {
		// BSpace is a tmux KEY NAME, so it must not go through SendKeys,
		// which sends literally (-l) and would type the six characters
		// "BSpace" into the pane — leaving text the operator's next Enter
		// submits. This was the pre-existing behavior and is why the probe
		// polluted live composers.
		_ = CurrentTmuxClient.SendKeyName(target, "BSpace")
	}()

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

	return encodeTerminalRobotOutput(&output, output.RobotResponse, "robot probe flag validation failed")
}

// probeResolvedPane probes one already-resolved pane. Both the single-pane and
// the batch surface funnel through it, so they cannot drift in how a pane is
// addressed or how a result is shaped.
//
// selector is echoed back as ProbeOutput.Pane: it is what the caller asked for,
// which on a multi-window session names a whole window and may have matched
// several panes. PaneRef is what was actually probed.
func probeResolvedPane(session string, targetPane tmux.Pane, multiWindow bool, selector int, flags ProbeFlags) *ProbeOutput {
	output := &ProbeOutput{
		RobotResponse:  NewRobotResponse(true),
		Session:        session,
		Pane:           selector,
		PaneRef:        probePaneRef(targetPane, multiWindow),
		ProbeMethod:    flags.Method,
		ProbeDetails:   ProbeDetails{},
		Responsive:     false,
		Confidence:     ProbeConfidenceLow,
		Recommendation: ProbeRecommendationLikelyStuck,
	}

	if err := validateProbePane(targetPane); err != nil {
		output.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPromptDeliveryCapabilityHint)
		return output
	}

	// The pane component must be the pane's OWN window-local index. Building it
	// from the selector addressed a pane that either does not exist (every
	// capture fails, so every healthy agent reads back "likely_stuck") or, in a
	// split window, belongs to a different agent that then receives the probe
	// keystrokes and the interrupt-test Ctrl-C.
	target := fmt.Sprintf("%s:%d.%d", session, targetPane.WindowIndex, targetPane.Index)
	timeout := time.Duration(flags.TimeoutMs) * time.Millisecond

	// Execute probe based on method
	var probeResult ProbeResult
	switch flags.Method {
	case ProbeMethodKeystrokeEcho:
		probeResult = probeKeystrokeEcho(target, timeout)
	case ProbeMethodInterruptTest:
		probeResult = probeInterruptTest(target, timeout)
	case ProbeMethodWakePing:
		// Wake-ping is an agent-pane surface: probing the operator's shell
		// answers nothing about rate limits and risks stray input there.
		if canonical := targetPane.Type.Canonical(); canonical == tmux.AgentUser || !canonical.IsValid() {
			output.RobotResponse = NewErrorResponse(
				fmt.Errorf("wake_ping targets agent panes; pane %s is %q", output.PaneRef, targetPane.Type),
				ErrCodeInvalidFlag,
				"Select an agent pane with --panes, or use keystroke_echo for shell panes",
			)
			return output
		}
		probeResult = probeWakePing(target, string(targetPane.Type), timeout)
	default:
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("unknown probe method: %s", flags.Method),
			ErrCodeInvalidFlag,
			"Valid methods: keystroke_echo, interrupt_test, wake_ping",
		)
		return output
	}

	// If keystroke_echo failed and aggressive mode is enabled, try interrupt_test
	if !probeResult.Responsive && flags.Aggressive && flags.Method == ProbeMethodKeystrokeEcho {
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

	return output
}

func probeEntryFromOutput(output *ProbeOutput) ProbeEntry {
	entry := ProbeEntry{
		Pane:           output.Pane,
		PaneRef:        output.PaneRef,
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

func validateProbePane(pane tmux.Pane) error {
	if err := pane.Type.ValidateAutomatedPromptDelivery(); err != nil {
		return fmt.Errorf("pane %d (%s) does not support automated probe input: %w", pane.Index, pane.Type.Canonical(), err)
	}
	return nil
}

// resolveProbePanes maps a --panes selector to every pane it addresses.
//
// pane_index is WINDOW-LOCAL, so a bare int is not a unique key on a
// multi-window session. The project-wide convention — implemented canonically
// by tmux.PaneSelector.Matches and shared by send, interrupt, restart-pane and
// --robot-is-working — is that on a multi-window session a bare index selects a
// whole WINDOW, and on a single-window session it is the window-local pane
// index.
//
// Probe used to resolve the same flag as an NTM agent index instead, so an
// agent that read pane numbers off --robot-is-working and fed them to
// --robot-probe addressed different panes on the two surfaces (bd-13squ). A
// selector may legitimately match several panes (a split window); every match
// is probed, exactly as send would deliver to every pane in the window.
func resolveProbePanes(panes []tmux.Pane, selector int) []tmux.Pane {
	multiWindow := sessionSpansMultipleWindows(panes)
	var matches []tmux.Pane
	for _, pane := range panes {
		key := pane.Index
		if multiWindow {
			key = pane.WindowIndex
		}
		if key == selector {
			matches = append(matches, pane)
		}
	}
	return matches
}

// probePaneRef is the unambiguous address of a probed pane: "window.pane" on a
// multi-window session, the bare pane index otherwise. It mirrors
// isWorkingPaneKey so the two surfaces describe a pane identically.
func probePaneRef(pane tmux.Pane, multiWindow bool) string {
	if !multiWindow {
		return strconv.Itoa(pane.Index)
	}
	return fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
}

// GetProbeSession probes panes in a session and returns aggregated output and exit code.
// Exit code: 0 = all responsive, 1 = partial or complete failure, 2 = unsupported.
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
		return output, 1
	}

	if !CurrentTmuxClient.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, 1
	}

	panes, err := CurrentTmuxClient.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, 1
	}

	multiWindow := sessionSpansMultipleWindows(panes)

	// Resolve to CONCRETE panes, not to selectors. A selector on a multi-window
	// session names a whole window, so it can match several panes; probing the
	// selector once would silently cover only one of them. Deduping by pane ID
	// keeps each physical pane probed exactly once even when the caller passes
	// overlapping selectors.
	type probeTarget struct {
		selector int
		pane     tmux.Pane
	}
	var targets []probeTarget
	seenPane := make(map[string]struct{}, len(panes))

	appendTarget := func(selector int, pane tmux.Pane) {
		if _, ok := seenPane[pane.ID]; ok {
			return
		}
		seenPane[pane.ID] = struct{}{}
		targets = append(targets, probeTarget{selector: selector, pane: pane})
	}

	if len(opts.Panes) == 0 {
		for _, pane := range panes {
			if detectAgentTypeFromPane(pane) == "user" {
				continue
			}
			selector := pane.Index
			if multiWindow {
				selector = pane.WindowIndex
			}
			appendTarget(selector, pane)
		}
	} else {
		for _, selector := range opts.Panes {
			for _, pane := range resolveProbePanes(panes, selector) {
				appendTarget(selector, pane)
			}
		}
	}

	if len(targets) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("no panes to probe in session '%s'", opts.Session),
			ErrCodePaneNotFound,
			"Use --panes to target a specific pane or spawn agents first",
		)
		return output, 1
	}

	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].pane.WindowIndex != targets[j].pane.WindowIndex {
			return targets[i].pane.WindowIndex < targets[j].pane.WindowIndex
		}
		return targets[i].pane.Index < targets[j].pane.Index
	})

	// Capability gate: refuse the whole batch up front rather than sending
	// input at panes that cannot accept it.
	for _, target := range targets {
		if err := validateProbePane(target.pane); err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeNotImplemented, agent.GrokPromptDeliveryCapabilityHint)
			return output, 2
		}
	}

	for _, target := range targets {
		probeOutput := probeResolvedPane(opts.Session, target.pane, multiWindow, target.selector, opts.Flags)
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
		return output, 1
	}
	if output.Summary.TotalProbed == 0 {
		return output, 1
	}
	if output.Summary.Responsive == output.Summary.TotalProbed {
		return output, 0
	}
	errorCode := ErrCodeTimeout
	hint := "Inspect unresponsive panes with --robot-tail before retrying or restarting them"
	for _, probe := range output.Probes {
		if probe.ErrorCode != "" {
			errorCode = probe.ErrorCode
			if probe.Hint != "" {
				hint = probe.Hint
			}
			break
		}
	}
	output.RobotResponse = NewErrorResponse(
		fmt.Errorf("%d of %d probed panes were unresponsive", output.Summary.Unresponsive, output.Summary.TotalProbed),
		errorCode,
		hint,
	)
	return output, 1
}

// PrintProbeSession outputs multi-pane probe results as JSON.
// Returns 0 on success, 1 for partial or complete failure, and 2 when unsupported.
func PrintProbeSession(opts ProbeSessionOptions) int {
	output, exitCode := GetProbeSession(opts)
	return printLegacyRobotOutput(output, output.RobotResponse, exitCode, "robot probe failed")
}
