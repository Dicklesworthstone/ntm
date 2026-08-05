package robot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func TestGetProbeSessionFailureUsesGeneralErrorExit(t *testing.T) {
	output, exitCode := GetProbeSession(ProbeSessionOptions{})
	if exitCode != 1 {
		t.Fatalf("GetProbeSession() exit code = %d, want 1", exitCode)
	}
	if output.Success || output.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("GetProbeSession() response = %+v, want INVALID_FLAG failure", output.RobotResponse)
	}
}

func TestGetProbe_GrokRejectsBeforePaneInput(t *testing.T) {
	mock := setupMock(t)
	mock.Panes = []tmux.Pane{{ID: "%9", Index: 2, WindowIndex: 1, Type: tmux.AgentGrok}}

	output, err := GetProbe(ProbeOptions{
		Session: "proj",
		Pane:    2,
		Flags: ProbeFlags{
			Method:    ProbeMethodKeystrokeEcho,
			TimeoutMs: 1,
		},
	})
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("GetProbe() response = %+v, want NOT_IMPLEMENTED", output.RobotResponse)
	}
	if !strings.Contains(output.Error, agent.GrokPromptDeliveryCapabilityHint) {
		t.Fatalf("GetProbe() error = %q, want Grok capability hint", output.Error)
	}
	if mock.CaptureCount != 0 || mock.SendKeysCount != 0 || mock.InterruptCount != 0 {
		t.Fatalf("probe mutated Grok pane before rejection: capture=%d send=%d interrupt=%d", mock.CaptureCount, mock.SendKeysCount, mock.InterruptCount)
	}
}

func TestGetProbeSession_MixedBatchPreflightIsAtomic(t *testing.T) {
	mock := setupMock(t)
	mock.Panes = []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentGrok},
	}

	output, exitCode := GetProbeSession(ProbeSessionOptions{
		Session: "proj",
		Panes:   []int{1, 2},
		Flags: ProbeFlags{
			Method:    ProbeMethodInterruptTest,
			TimeoutMs: 1,
		},
	})
	if exitCode != 2 {
		t.Fatalf("GetProbeSession() exit code = %d, want 2", exitCode)
	}
	if output.Success || output.ErrorCode != ErrCodeNotImplemented {
		t.Fatalf("GetProbeSession() response = %+v, want NOT_IMPLEMENTED", output.RobotResponse)
	}
	if len(output.Probes) != 0 || output.Summary.TotalProbed != 0 {
		t.Fatalf("probe result mutated before batch preflight completed: probes=%v summary=%+v", output.Probes, output.Summary)
	}
	if mock.CaptureCount != 0 || mock.SendKeysCount != 0 || mock.InterruptCount != 0 {
		t.Fatalf("mixed batch mutated a pane before Grok rejection: capture=%d send=%d interrupt=%d", mock.CaptureCount, mock.SendKeysCount, mock.InterruptCount)
	}
}

func TestPrintProbeSessionUnresponsiveForcesCanonicalFailureJSON(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureOutput = "static content"

	originalFormat := GetOutputFormat()
	SetOutputFormat(FormatTOON)
	t.Cleanup(func() { SetOutputFormat(originalFormat) })

	var exitCode int
	stdout, err := captureStdout(t, func() error {
		exitCode = PrintProbeSession(ProbeSessionOptions{
			Session: "proj",
			Panes:   []int{0},
			Flags: ProbeFlags{
				Method:    ProbeMethodKeystrokeEcho,
				TimeoutMs: 1,
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("capture PrintProbeSession: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("PrintProbeSession exit = %d, want 1", exitCode)
	}
	var output ProbeSessionOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("probe failure is not canonical JSON: %v\noutput=%q", err, stdout)
	}
	if output.Success || output.ErrorCode != ErrCodeTimeout || output.OutputFormat != string(FormatJSON) {
		t.Fatalf("probe response = %+v, want TIMEOUT JSON failure", output.RobotResponse)
	}
	if output.Summary.TotalProbed != 1 || output.Summary.Unresponsive != 1 || len(output.Probes) != 1 {
		t.Fatalf("probe failure lost aggregate details: %+v", output)
	}
}

func TestValidProbeMethods(t *testing.T) {
	methods := ValidProbeMethods()
	if len(methods) != 3 {
		t.Errorf("expected 3 probe methods, got %d", len(methods))
	}

	// Check expected methods exist
	expected := map[ProbeMethod]bool{
		ProbeMethodKeystrokeEcho: false,
		ProbeMethodInterruptTest: false,
		ProbeMethodWakePing:      false,
	}
	for _, m := range methods {
		expected[m] = true
	}
	for m, found := range expected {
		if !found {
			t.Errorf("expected method %s not found", m)
		}
	}
}

func TestIsValidProbeMethod(t *testing.T) {
	tests := []struct {
		method string
		valid  bool
	}{
		{"keystroke_echo", true},
		{"interrupt_test", true},
		{"", false},
		{"invalid", false},
		{"KEYSTROKE_ECHO", false}, // Case sensitive
		{"keystroke-echo", false}, // Wrong separator
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got := IsValidProbeMethod(tt.method)
			if got != tt.valid {
				t.Errorf("IsValidProbeMethod(%q) = %v, want %v", tt.method, got, tt.valid)
			}
		})
	}
}

func TestDefaultProbeFlags(t *testing.T) {
	flags := DefaultProbeFlags()

	if flags.Method != ProbeMethodKeystrokeEcho {
		t.Errorf("default method = %v, want %v", flags.Method, ProbeMethodKeystrokeEcho)
	}
	if flags.TimeoutMs != 5000 {
		t.Errorf("default timeout = %d, want 5000", flags.TimeoutMs)
	}
	if flags.Aggressive {
		t.Errorf("default aggressive = true, want false")
	}
}

func TestParseProbeFlags(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		timeout     int
		aggressive  bool
		wantErr     bool
		errContains string
	}{
		{
			name:    "defaults when empty",
			method:  "",
			timeout: 0,
			wantErr: false,
		},
		{
			name:    "valid keystroke_echo",
			method:  "keystroke_echo",
			timeout: 5000,
			wantErr: false,
		},
		{
			name:    "valid interrupt_test",
			method:  "interrupt_test",
			timeout: 3000,
			wantErr: false,
		},
		{
			name:        "invalid method",
			method:      "invalid_method",
			timeout:     5000,
			wantErr:     true,
			errContains: "invalid method",
		},
		{
			name:        "timeout too low",
			method:      "keystroke_echo",
			timeout:     50,
			wantErr:     true,
			errContains: "timeout must be",
		},
		{
			name:        "timeout too high",
			method:      "keystroke_echo",
			timeout:     100000,
			wantErr:     true,
			errContains: "timeout must be",
		},
		{
			name:    "timeout at min boundary",
			method:  "keystroke_echo",
			timeout: 100,
			wantErr: false,
		},
		{
			name:    "timeout at max boundary",
			method:  "keystroke_echo",
			timeout: 60000,
			wantErr: false,
		},
		{
			name:       "aggressive with keystroke_echo",
			method:     "keystroke_echo",
			timeout:    5000,
			aggressive: true,
			wantErr:    false,
		},
		{
			name:        "aggressive with interrupt_test - invalid",
			method:      "interrupt_test",
			timeout:     5000,
			aggressive:  true,
			wantErr:     true,
			errContains: "--aggressive only valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := ParseProbeFlags(tt.method, tt.timeout, tt.aggressive)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if flags == nil {
				t.Errorf("expected flags, got nil")
				return
			}

			// Verify method
			if tt.method != "" && string(flags.Method) != tt.method {
				t.Errorf("method = %v, want %v", flags.Method, tt.method)
			}

			// Verify timeout (0 means use default)
			if tt.timeout != 0 && flags.TimeoutMs != tt.timeout {
				t.Errorf("timeout = %d, want %d", flags.TimeoutMs, tt.timeout)
			}

			// Verify aggressive
			if flags.Aggressive != tt.aggressive {
				t.Errorf("aggressive = %v, want %v", flags.Aggressive, tt.aggressive)
			}
		})
	}
}

func TestParseProbeFlags_DefaultValues(t *testing.T) {
	flags, err := ParseProbeFlags("", 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	defaults := DefaultProbeFlags()
	if flags.Method != defaults.Method {
		t.Errorf("method = %v, want default %v", flags.Method, defaults.Method)
	}
	if flags.TimeoutMs != defaults.TimeoutMs {
		t.Errorf("timeout = %d, want default %d", flags.TimeoutMs, defaults.TimeoutMs)
	}
	if flags.Aggressive != defaults.Aggressive {
		t.Errorf("aggressive = %v, want default %v", flags.Aggressive, defaults.Aggressive)
	}
}

func TestProbeConstants(t *testing.T) {
	// Verify timeout constants are reasonable
	if ProbeMinTimeoutMs < 1 {
		t.Errorf("ProbeMinTimeoutMs = %d, should be >= 1", ProbeMinTimeoutMs)
	}
	if ProbeMaxTimeoutMs < ProbeMinTimeoutMs {
		t.Errorf("ProbeMaxTimeoutMs (%d) < ProbeMinTimeoutMs (%d)", ProbeMaxTimeoutMs, ProbeMinTimeoutMs)
	}
	if ProbeMaxTimeoutMs > 60000 { // 10 minutes max seems reasonable
		t.Errorf("ProbeMaxTimeoutMs = %d, seems too high", ProbeMaxTimeoutMs)
	}
}

func TestProbeConfidenceValues(t *testing.T) {
	// Ensure confidence values are non-empty strings
	confidences := []ProbeConfidence{
		ProbeConfidenceHigh,
		ProbeConfidenceMedium,
		ProbeConfidenceLow,
	}
	for _, c := range confidences {
		if c == "" {
			t.Errorf("empty confidence value found")
		}
	}
}

func TestProbeRecommendationValues(t *testing.T) {
	// Ensure recommendation values are non-empty strings
	recommendations := []ProbeRecommendation{
		ProbeRecommendationHealthy,
		ProbeRecommendationLikelyStuck,
		ProbeRecommendationDefinitelyStuck,
	}
	for _, r := range recommendations {
		if r == "" {
			t.Errorf("empty recommendation value found")
		}
	}
}

// =============================================================================
// Baseline Capture Tests (bd-ok7rj)
// =============================================================================

func TestHashContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantSame bool // If true, hash should match previous test case
	}{
		{
			name:    "empty string",
			content: "",
		},
		{
			name:    "simple content",
			content: "hello world",
		},
		{
			name:    "multiline content",
			content: "line1\nline2\nline3\n",
		},
		{
			name:    "whitespace only",
			content: "   \t\n  \n",
		},
	}

	// Test that identical content produces identical hashes
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := hashContent(tt.content)
			hash2 := hashContent(tt.content)

			if hash1 != hash2 {
				t.Errorf("hashContent(%q) produced different results: %s vs %s", tt.content, hash1, hash2)
			}

			// Verify hash is 16 hex chars (64 bits)
			if len(hash1) != 16 {
				t.Errorf("hashContent(%q) = %s, want 16 chars", tt.content, hash1)
			}
		})
	}

	// Test that different content produces different hashes
	hash1 := hashContent("hello")
	hash2 := hashContent("world")
	if hash1 == hash2 {
		t.Errorf("hashContent should produce different hashes for different content")
	}
}

func TestCountNonEmptyLines(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"single_empty_line", "\n", 0},
		{"single_content_line", "hello", 1},
		{"content_with_newline", "hello\n", 1},
		{"two_lines", "hello\nworld", 2},
		{"trailing_empty_lines", "hello\n\n\n", 1},
		{"leading_empty_lines", "\n\nhello", 1},
		{"mixed_empty_lines", "\nhello\n\nworld\n", 2},
		{"whitespace_only_lines", "  \n\t\n \r \n", 0},
		{"lines_with_whitespace", "  hello  \n  world  ", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := util.CountNonEmptyLines(tt.content)
			if got != tt.want {
				t.Errorf("CountNonEmptyLines(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}

func mockCapture(content string) PaneBaseline {
	return PaneBaseline{
		CapturedAt:  time.Now(),
		ContentHash: hashContent(content),
		LineCount:   util.CountNonEmptyLines(content),
	}
}

func TestAnalyzeChange(t *testing.T) {
	cases := []struct {
		name     string
		baseline PaneBaseline
		current  PaneBaseline
		want     bool
	}{
		{
			"no_change",
			PaneBaseline{ContentHash: "h1", LineCount: 10},
			PaneBaseline{ContentHash: "h1", LineCount: 10},
			false,
		},
		{
			"content_changed",
			PaneBaseline{ContentHash: "h1", LineCount: 10},
			PaneBaseline{ContentHash: "h2", LineCount: 10},
			true,
		},
		{
			"line_count_changed",
			PaneBaseline{ContentHash: "h1", LineCount: 10},
			PaneBaseline{ContentHash: "h1", LineCount: 11},
			true,
		},
		{
			"hash_and_line_count_changed",
			PaneBaseline{ContentHash: "h1", LineCount: 10},
			PaneBaseline{ContentHash: "h2", LineCount: 11},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			change := ComparePaneState(&tc.baseline, &tc.current)
			if change.Changed != tc.want {
				t.Errorf("ComparePaneState() changed = %v, want %v", change.Changed, tc.want)
			}
		})
	}
}

func TestPaneBaselineStruct(t *testing.T) {
	// Test that PaneBaseline can be created with expected fields
	content := "test content\nline 2\n"
	baseline := &PaneBaseline{
		Content:     content,
		ContentHash: hashContent(content),
		LineCount:   util.CountNonEmptyLines(content),
	}

	if baseline.Content != content {
		t.Errorf("Content = %q, want %q", baseline.Content, content)
	}
	if baseline.ContentHash == "" {
		t.Errorf("ContentHash should not be empty")
	}
	if baseline.LineCount != 2 {
		t.Errorf("LineCount = %d, want 2", baseline.LineCount)
	}
}

func TestComparePaneState_NoChange(t *testing.T) {
	content := "test content\nline 2\n"
	baseline := &PaneBaseline{
		Content:     content,
		ContentHash: hashContent(content),
		LineCount:   util.CountNonEmptyLines(content),
	}
	current := &PaneBaseline{
		Content:     content,
		ContentHash: hashContent(content),
		LineCount:   util.CountNonEmptyLines(content),
	}

	change := ComparePaneState(baseline, current)

	if change.Changed {
		t.Errorf("Changed = true, want false for identical content")
	}
	if change.LinesDelta != 0 {
		t.Errorf("LinesDelta = %d, want 0", change.LinesDelta)
	}
}

func TestComparePaneState_ContentChanged(t *testing.T) {
	baseline := &PaneBaseline{
		Content:     "original content\n",
		ContentHash: hashContent("original content\n"),
		LineCount:   1,
	}
	current := &PaneBaseline{
		Content:     "new content\nmore lines\n",
		ContentHash: hashContent("new content\nmore lines\n"),
		LineCount:   2,
	}

	change := ComparePaneState(baseline, current)

	if !change.Changed {
		t.Errorf("Changed = false, want true for different content")
	}
	if change.LinesDelta != 1 {
		t.Errorf("LinesDelta = %d, want 1", change.LinesDelta)
	}
	if change.LinesAdded != 1 {
		t.Errorf("LinesAdded = %d, want 1", change.LinesAdded)
	}
	if change.LinesRemoved != 0 {
		t.Errorf("LinesRemoved = %d, want 0", change.LinesRemoved)
	}
}

func TestComparePaneState_LinesRemoved(t *testing.T) {
	baseline := &PaneBaseline{
		Content:     "line1\nline2\nline3\n",
		ContentHash: hashContent("line1\nline2\nline3\n"),
		LineCount:   3,
	}
	current := &PaneBaseline{
		Content:     "line1\n",
		ContentHash: hashContent("line1\n"),
		LineCount:   1,
	}

	change := ComparePaneState(baseline, current)

	if !change.Changed {
		t.Errorf("Changed = false, want true")
	}
	if change.LinesDelta != -2 {
		t.Errorf("LinesDelta = %d, want -2", change.LinesDelta)
	}
	if change.LinesAdded != 0 {
		t.Errorf("LinesAdded = %d, want 0", change.LinesAdded)
	}
	if change.LinesRemoved != 2 {
		t.Errorf("LinesRemoved = %d, want 2", change.LinesRemoved)
	}
}

func TestComparePaneState_NilBaseline(t *testing.T) {
	current := &PaneBaseline{
		Content:     "content",
		ContentHash: hashContent("content"),
		LineCount:   1,
	}

	change := ComparePaneState(nil, current)

	// When baseline is nil, assume changed
	if !change.Changed {
		t.Errorf("Changed = false, want true for nil baseline")
	}
}

func TestComparePaneState_NilCurrent(t *testing.T) {
	baseline := &PaneBaseline{
		Content:     "content",
		ContentHash: hashContent("content"),
		LineCount:   1,
	}

	change := ComparePaneState(baseline, nil)

	// When current is nil, assume changed
	if !change.Changed {
		t.Errorf("Changed = false, want true for nil current")
	}
}

func TestComparePaneState_BothNil(t *testing.T) {
	change := ComparePaneState(nil, nil)

	// When both are nil, assume changed (edge case)
	if !change.Changed {
		t.Errorf("Changed = false, want true when both nil")
	}
}

func TestPaneChangeStruct(t *testing.T) {
	// Test PaneChange JSON field names
	change := PaneChange{
		Changed:      true,
		LinesDelta:   5,
		LinesAdded:   5,
		LinesRemoved: 0,
		LatencyMs:    100,
	}

	if !change.Changed {
		t.Errorf("Changed = false, want true")
	}
	if change.LinesDelta != 5 {
		t.Errorf("LinesDelta = %d, want 5", change.LinesDelta)
	}
	if change.LatencyMs != 100 {
		t.Errorf("LatencyMs = %d, want 100", change.LatencyMs)
	}
}

// =============================================================================
// Keystroke Echo Probe Tests (bd-30nv1)
// =============================================================================

func TestProbeResultStruct(t *testing.T) {
	// Test that ProbeResult can be created with all fields
	result := ProbeResult{
		Responsive: true,
		Details: ProbeDetails{
			InputSent:        "Space+Backspace",
			OutputChanged:    true,
			LatencyMs:        150,
			OutputDeltaLines: 2,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationHealthy,
		Reasoning:      "pane responded in 150ms",
	}

	if !result.Responsive {
		t.Errorf("Responsive = true, want false")
	}
	if result.Details.InputSent != "Space+Backspace" {
		t.Errorf("Details.InputSent = %q, want %q", result.Details.InputSent, "Space+Backspace")
	}
	if !result.Details.OutputChanged {
		t.Errorf("Details.OutputChanged = false, want true")
	}
	if result.Details.LatencyMs != 150 {
		t.Errorf("Details.LatencyMs = %d, want 150", result.Details.LatencyMs)
	}
	if result.Details.OutputDeltaLines != 2 {
		t.Errorf("Details.OutputDeltaLines = %d, want 2", result.Details.OutputDeltaLines)
	}
	if result.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s, want %s", result.Confidence, ProbeConfidenceHigh)
	}
	if result.Recommendation != ProbeRecommendationHealthy {
		t.Errorf("Recommendation = %s, want %s", result.Recommendation, ProbeRecommendationHealthy)
	}
	if result.Reasoning != "pane responded in 150ms" {
		t.Errorf("Reasoning = %q, want %q", result.Reasoning, "pane responded in 150ms")
	}
}

func TestProbeResultUnresponsive(t *testing.T) {
	// Test unresponsive ProbeResult configuration
	result := ProbeResult{
		Responsive: false,
		Details: ProbeDetails{
			InputSent:     "Space+Backspace",
			OutputChanged: false,
			LatencyMs:     5000,
		},
		Confidence:     ProbeConfidenceMedium,
		Recommendation: ProbeRecommendationLikelyStuck,
		Reasoning:      "no output change detected within 5000ms",
	}

	if result.Responsive {
		t.Errorf("Responsive = true, want false")
	}
	if result.Details.OutputChanged {
		t.Errorf("Details.OutputChanged = true, want false")
	}
	if result.Confidence != ProbeConfidenceMedium {
		t.Errorf("Confidence = %s, want %s", result.Confidence, ProbeConfidenceMedium)
	}
	if result.Recommendation != ProbeRecommendationLikelyStuck {
		t.Errorf("Recommendation = %s, want %s", result.Recommendation, ProbeRecommendationLikelyStuck)
	}
}

func TestProbePollIntervalConstant(t *testing.T) {
	// Verify poll interval is reasonable (50ms)
	if probePollInterval < 10*time.Millisecond {
		t.Errorf("probePollInterval = %v, seems too fast", probePollInterval)
	}
	if probePollInterval > 500*time.Millisecond {
		t.Errorf("probePollInterval = %v, seems too slow", probePollInterval)
	}
	// Check exact value matches documentation
	if probePollInterval != 50*time.Millisecond {
		t.Errorf("probePollInterval = %v, want 50ms", probePollInterval)
	}
}

func TestProbeDetailsStruct(t *testing.T) {
	// Test ProbeDetails can hold various states
	tests := []struct {
		name    string
		details ProbeDetails
	}{
		{
			name: "responsive with change",
			details: ProbeDetails{
				InputSent:        "Space+Backspace",
				OutputChanged:    true,
				LatencyMs:        100,
				OutputDeltaLines: 1,
			},
		},
		{
			name: "no response",
			details: ProbeDetails{
				InputSent:        "Space+Backspace",
				OutputChanged:    false,
				LatencyMs:        5000,
				OutputDeltaLines: 0,
			},
		},
		{
			name: "negative delta lines",
			details: ProbeDetails{
				InputSent:        "Space+Backspace",
				OutputChanged:    true,
				LatencyMs:        200,
				OutputDeltaLines: -3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify fields are set correctly
			if tt.details.InputSent == "" {
				t.Errorf("InputSent should not be empty")
			}
			if tt.details.LatencyMs < 0 {
				t.Errorf("LatencyMs = %d, should be non-negative", tt.details.LatencyMs)
			}
		})
	}
}

func TestProbeOutputIntegration(t *testing.T) {
	// Test that ProbeOutput correctly includes ProbeResult fields
	output := ProbeOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Pane:          0,
		Responsive:    true,
		ProbeMethod:   ProbeMethodKeystrokeEcho,
		ProbeDetails: ProbeDetails{
			InputSent:        "Space+Backspace",
			OutputChanged:    true,
			LatencyMs:        100,
			OutputDeltaLines: 0,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationHealthy,
		Reasoning:      "pane responded in 100ms",
	}

	if output.Session != "test-session" {
		t.Errorf("Session = %q, want %q", output.Session, "test-session")
	}
	if output.Pane != 0 {
		t.Errorf("Pane = %d, want 0", output.Pane)
	}
	if !output.Responsive {
		t.Errorf("Responsive = false, want true")
	}
	if output.ProbeMethod != ProbeMethodKeystrokeEcho {
		t.Errorf("ProbeMethod = %s, want %s", output.ProbeMethod, ProbeMethodKeystrokeEcho)
	}
	if output.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s, want %s", output.Confidence, ProbeConfidenceHigh)
	}
	if output.Recommendation != ProbeRecommendationHealthy {
		t.Errorf("Recommendation = %s, want %s", output.Recommendation, ProbeRecommendationHealthy)
	}
}

// =============================================================================
// Interrupt Test Probe Tests (bd-3ah0k)
// =============================================================================

func TestInterruptTestProbeResultStructure(t *testing.T) {
	// Test that ProbeResult can hold interrupt_test specific values
	result := ProbeResult{
		Responsive: true,
		Details: ProbeDetails{
			InputSent:        "Ctrl-C",
			OutputChanged:    true,
			LatencyMs:        75,
			OutputDeltaLines: 1,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationHealthy,
		Reasoning:      "pane responded to interrupt in 75ms (may have interrupted work)",
	}

	if !result.Responsive {
		t.Errorf("Responsive = true, want false")
	}
	if result.Details.InputSent != "Ctrl-C" {
		t.Errorf("Details.InputSent = %q, want %q", result.Details.InputSent, "Ctrl-C")
	}
	if result.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s, want %s", result.Confidence, ProbeConfidenceHigh)
	}
	if result.Recommendation != ProbeRecommendationHealthy {
		t.Errorf("Recommendation = %s, want %s", result.Recommendation, ProbeRecommendationHealthy)
	}
}

func TestInterruptTestUnresponsive(t *testing.T) {
	// Test unresponsive result structure for interrupt_test
	result := ProbeResult{
		Responsive: false,
		Details: ProbeDetails{
			InputSent:     "Ctrl-C",
			OutputChanged: false,
			LatencyMs:     5000,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationDefinitelyStuck,
		Reasoning:      "no response to Ctrl-C within 5000ms - process appears hung",
	}

	if result.Responsive {
		t.Errorf("Responsive = true, want false")
	}
	if result.Details.OutputChanged {
		t.Errorf("Details.OutputChanged = true, want false")
	}
	// interrupt_test gives high confidence even when unresponsive
	if result.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s, want %s", result.Confidence, ProbeConfidenceHigh)
	}
	// No response to interrupt means definitely stuck
	if result.Recommendation != ProbeRecommendationDefinitelyStuck {
		t.Errorf("Recommendation = %s, want %s", result.Recommendation, ProbeRecommendationDefinitelyStuck)
	}
}

func TestInterruptTestProbeOutputIntegration(t *testing.T) {
	// Test that ProbeOutput works with interrupt_test method
	output := ProbeOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Pane:          1,
		Responsive:    false,
		ProbeMethod:   ProbeMethodInterruptTest,
		ProbeDetails: ProbeDetails{
			InputSent:        "Ctrl-C",
			OutputChanged:    false,
			LatencyMs:        3000,
			OutputDeltaLines: 0,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationDefinitelyStuck,
		Reasoning:      "no response to Ctrl-C within 3000ms - process appears hung",
	}

	if output.ProbeMethod != ProbeMethodInterruptTest {
		t.Errorf("ProbeMethod = %s, want %s", output.ProbeMethod, ProbeMethodInterruptTest)
	}
	if output.Responsive {
		t.Errorf("Responsive = true, want false")
	}
	// Verify high confidence for interrupt_test
	if output.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s, want %s", output.Confidence, ProbeConfidenceHigh)
	}
	// Verify definitely_stuck for unresponsive interrupt_test
	if output.Recommendation != ProbeRecommendationDefinitelyStuck {
		t.Errorf("Recommendation = %s, want %s", output.Recommendation, ProbeRecommendationDefinitelyStuck)
	}
}

func TestInterruptTestVsKeystrokeEchoConfidence(t *testing.T) {
	// Verify interrupt_test gives definitive results (high confidence) when unresponsive
	// while keystroke_echo gives medium confidence
	keystrokeResult := ProbeResult{
		Responsive:     false,
		Confidence:     ProbeConfidenceMedium,
		Recommendation: ProbeRecommendationLikelyStuck,
	}

	interruptResult := ProbeResult{
		Responsive:     false,
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationDefinitelyStuck,
	}

	// keystroke_echo unresponsive = medium confidence, likely stuck
	if keystrokeResult.Confidence != ProbeConfidenceMedium {
		t.Errorf("keystroke_echo unresponsive should have medium confidence")
	}
	if keystrokeResult.Recommendation != ProbeRecommendationLikelyStuck {
		t.Errorf("keystroke_echo unresponsive should recommend likely_stuck")
	}

	// interrupt_test unresponsive = high confidence, definitely stuck
	if interruptResult.Confidence != ProbeConfidenceHigh {
		t.Errorf("interrupt_test unresponsive should have high confidence")
	}
	if interruptResult.Recommendation != ProbeRecommendationDefinitelyStuck {
		t.Errorf("interrupt_test unresponsive should recommend definitely_stuck")
	}
}

// =============================================================================
// Confidence Scoring and Recommendation Tests (bd-x0f5i)
// =============================================================================

func TestConfidenceRecommendationMatrix(t *testing.T) {
	// Verify the confidence/recommendation mapping matches the spec:
	// | Confidence | Response | Recommendation      |
	// |------------|----------|---------------------|
	// | high       | yes      | healthy             |
	// | high       | no       | definitely_stuck    |
	// | medium     | yes      | healthy             |
	// | medium     | no       | likely_stuck        |
	// | low        | yes      | healthy             |
	// | low        | no       | possibly_stuck      |

	tests := []struct {
		name           string
		confidence     ProbeConfidence
		responsive     bool
		recommendation ProbeRecommendation
		method         string
	}{
		// High confidence cases
		{
			name:           "high/yes/healthy (interrupt_test responsive)",
			confidence:     ProbeConfidenceHigh,
			responsive:     true,
			recommendation: ProbeRecommendationHealthy,
			method:         "interrupt_test",
		},
		{
			name:           "high/yes/healthy (keystroke_echo responsive)",
			confidence:     ProbeConfidenceHigh,
			responsive:     true,
			recommendation: ProbeRecommendationHealthy,
			method:         "keystroke_echo",
		},
		{
			name:           "high/no/definitely_stuck (interrupt_test unresponsive)",
			confidence:     ProbeConfidenceHigh,
			responsive:     false,
			recommendation: ProbeRecommendationDefinitelyStuck,
			method:         "interrupt_test",
		},

		// Medium confidence cases
		{
			name:           "medium/no/likely_stuck (keystroke_echo unresponsive)",
			confidence:     ProbeConfidenceMedium,
			responsive:     false,
			recommendation: ProbeRecommendationLikelyStuck,
			method:         "keystroke_echo",
		},

		// Low confidence cases (initial/error states)
		{
			name:           "low/no/likely_stuck (initial state)",
			confidence:     ProbeConfidenceLow,
			responsive:     false,
			recommendation: ProbeRecommendationLikelyStuck,
			method:         "keystroke_echo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProbeResult{
				Responsive:     tt.responsive,
				Confidence:     tt.confidence,
				Recommendation: tt.recommendation,
			}

			// Verify confidence-responsive-recommendation consistency
			if result.Responsive && result.Recommendation != ProbeRecommendationHealthy {
				t.Errorf("responsive=true should have recommendation=healthy, got %s", result.Recommendation)
			}

			// High confidence + unresponsive = definitely_stuck (for interrupt_test)
			if tt.method == "interrupt_test" && !result.Responsive && result.Confidence == ProbeConfidenceHigh {
				if result.Recommendation != ProbeRecommendationDefinitelyStuck {
					t.Errorf("interrupt_test high confidence + unresponsive should be definitely_stuck, got %s", result.Recommendation)
				}
			}

			// Medium confidence + unresponsive = likely_stuck (for keystroke_echo)
			if tt.method == "keystroke_echo" && !result.Responsive && result.Confidence == ProbeConfidenceMedium {
				if result.Recommendation != ProbeRecommendationLikelyStuck {
					t.Errorf("keystroke_echo medium confidence + unresponsive should be likely_stuck, got %s", result.Recommendation)
				}
			}
		})
	}
}

func TestConfidenceLevelValues(t *testing.T) {
	// Verify confidence level string values match expected output
	tests := []struct {
		confidence ProbeConfidence
		expected   string
	}{
		{ProbeConfidenceHigh, "high"},
		{ProbeConfidenceMedium, "medium"},
		{ProbeConfidenceLow, "low"},
	}

	for _, tt := range tests {
		t.Run(string(tt.confidence), func(t *testing.T) {
			if string(tt.confidence) != tt.expected {
				t.Errorf("ProbeConfidence %v = %q, want %q", tt.confidence, string(tt.confidence), tt.expected)
			}
		})
	}
}

func TestRecommendationValues(t *testing.T) {
	// Verify recommendation string values match expected output
	tests := []struct {
		recommendation ProbeRecommendation
		expected       string
	}{
		{ProbeRecommendationHealthy, "healthy"},
		{ProbeRecommendationLikelyStuck, "likely_stuck"},
		{ProbeRecommendationDefinitelyStuck, "definitely_stuck"},
	}

	for _, tt := range tests {
		t.Run(string(tt.recommendation), func(t *testing.T) {
			if string(tt.recommendation) != tt.expected {
				t.Errorf("ProbeRecommendation %v = %q, want %q", tt.recommendation, string(tt.recommendation), tt.expected)
			}
		})
	}
}

func TestConfidenceExplanation(t *testing.T) {
	// Test that reasoning strings explain the confidence level
	tests := []struct {
		name       string
		result     ProbeResult
		shouldHave []string
	}{
		{
			name: "high confidence responsive",
			result: ProbeResult{
				Responsive:     true,
				Confidence:     ProbeConfidenceHigh,
				Recommendation: ProbeRecommendationHealthy,
				Reasoning:      "pane responded in 50ms",
			},
			shouldHave: []string{"responded"},
		},
		{
			name: "high confidence definitely stuck",
			result: ProbeResult{
				Responsive:     false,
				Confidence:     ProbeConfidenceHigh,
				Recommendation: ProbeRecommendationDefinitelyStuck,
				Reasoning:      "no response to Ctrl-C within 5000ms - process appears hung",
			},
			shouldHave: []string{"no response", "hung"},
		},
		{
			name: "medium confidence likely stuck",
			result: ProbeResult{
				Responsive:     false,
				Confidence:     ProbeConfidenceMedium,
				Recommendation: ProbeRecommendationLikelyStuck,
				Reasoning:      "no output change detected within 5000ms",
			},
			shouldHave: []string{"no", "change"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, expected := range tt.shouldHave {
				if !strings.Contains(strings.ToLower(tt.result.Reasoning), expected) {
					t.Errorf("Reasoning %q should contain %q", tt.result.Reasoning, expected)
				}
			}
		})
	}
}

// =============================================================================
// Timeout Behavior Tests (bd-43oxm)
// =============================================================================

func TestTimeoutBoundaries(t *testing.T) {
	// Test timeout validation at boundaries
	tests := []struct {
		name    string
		timeout int
		wantErr bool
	}{
		{"below_min", 50, true},         // Below 100ms min
		{"at_min", 100, false},          // Exactly at minimum
		{"normal", 5000, false},         // Normal value
		{"at_max", 60000, false},        // Exactly at maximum
		{"above_max", 100000, true},     // Above 60000ms max
		{"zero_uses_default", 0, false}, // Zero means use default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := ParseProbeFlags("keystroke_echo", tt.timeout, false)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for timeout %d, got nil", tt.timeout)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tt.timeout == 0 {
				// Zero means default (5000)
				if flags.TimeoutMs != 5000 {
					t.Errorf("timeout = %d, want default 5000", flags.TimeoutMs)
				}
			} else {
				if flags.TimeoutMs != tt.timeout {
					t.Errorf("timeout = %d, want %d", flags.TimeoutMs, tt.timeout)
				}
			}
		})
	}
}

func TestTimeoutConstants(t *testing.T) {
	// Verify timeout constants match documentation
	if ProbeMinTimeoutMs != 100 {
		t.Errorf("ProbeMinTimeoutMs = %d, want 100", ProbeMinTimeoutMs)
	}
	if ProbeMaxTimeoutMs != 60000 {
		t.Errorf("ProbeMaxTimeoutMs = %d, want 60000", ProbeMaxTimeoutMs)
	}
}

func TestTimeoutDurationConversion(t *testing.T) {
	// Test that timeout is correctly converted to duration
	flags, err := ParseProbeFlags("keystroke_echo", 3000, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	duration := time.Duration(flags.TimeoutMs) * time.Millisecond
	if duration != 3*time.Second {
		t.Errorf("timeout duration = %v, want 3s", duration)
	}
}

// =============================================================================
// Error Output Format Tests (bd-43oxm)
// =============================================================================

func TestProbeFlagErrorStruct(t *testing.T) {
	// Test ProbeFlagError structure fields
	validMethods := []string{"keystroke_echo", "interrupt_test"}
	errOutput := ProbeFlagError{
		RobotResponse: NewRobotResponse(false),
		ValidMethods:  validMethods,
		MinTimeout:    ProbeMinTimeoutMs,
		MaxTimeout:    ProbeMaxTimeoutMs,
	}
	errOutput.Error = "invalid method: bad_method"
	errOutput.ErrorCode = ErrCodeInvalidFlag

	if errOutput.Success {
		t.Error("Success should be false for error")
	}
	if errOutput.Error != "invalid method: bad_method" {
		t.Errorf("Error = %q, want %q", errOutput.Error, "invalid method: bad_method")
	}
	if errOutput.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("ErrorCode = %s, want %s", errOutput.ErrorCode, ErrCodeInvalidFlag)
	}
	if len(errOutput.ValidMethods) != 2 {
		t.Errorf("ValidMethods length = %d, want 2", len(errOutput.ValidMethods))
	}
	if errOutput.MinTimeout != 100 {
		t.Errorf("MinTimeout = %d, want 100", errOutput.MinTimeout)
	}
	if errOutput.MaxTimeout != 60000 {
		t.Errorf("MaxTimeout = %d, want 60000", errOutput.MaxTimeout)
	}
}

func TestInvalidMethodError(t *testing.T) {
	_, err := ParseProbeFlags("invalid_method", 5000, false)
	if err == nil {
		t.Fatal("expected error for invalid method")
	}
	if !strings.Contains(err.Error(), "invalid method") {
		t.Errorf("error %q should contain 'invalid method'", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid_method") {
		t.Errorf("error %q should contain the invalid method name", err.Error())
	}
}

func TestTimeoutRangeError(t *testing.T) {
	_, err := ParseProbeFlags("keystroke_echo", 50, false)
	if err == nil {
		t.Fatal("expected error for timeout below minimum")
	}
	if !strings.Contains(err.Error(), "timeout must be") {
		t.Errorf("error %q should mention timeout range", err.Error())
	}
	if !strings.Contains(err.Error(), "100") && !strings.Contains(err.Error(), "60000") {
		t.Errorf("error %q should contain valid range", err.Error())
	}
}

// =============================================================================
// Aggressive Mode Tests (bd-43oxm)
// =============================================================================

func TestAggressiveModeParsing(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		aggressive bool
		wantErr    bool
	}{
		{
			name:       "aggressive with keystroke_echo",
			method:     "keystroke_echo",
			aggressive: true,
			wantErr:    false,
		},
		{
			name:       "aggressive with interrupt_test - invalid",
			method:     "interrupt_test",
			aggressive: true,
			wantErr:    true,
		},
		{
			name:       "non-aggressive with keystroke_echo",
			method:     "keystroke_echo",
			aggressive: false,
			wantErr:    false,
		},
		{
			name:       "non-aggressive with interrupt_test",
			method:     "interrupt_test",
			aggressive: false,
			wantErr:    false,
		},
		{
			name:       "aggressive with default method",
			method:     "",
			aggressive: true,
			wantErr:    false, // Default is keystroke_echo
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, err := ParseProbeFlags(tt.method, 5000, tt.aggressive)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if flags.Aggressive != tt.aggressive {
				t.Errorf("Aggressive = %v, want %v", flags.Aggressive, tt.aggressive)
			}
		})
	}
}

func TestAggressiveModeDescription(t *testing.T) {
	// Verify the aggressive mode error message mentions the correct method
	_, err := ParseProbeFlags("interrupt_test", 5000, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--aggressive") {
		t.Errorf("error should mention --aggressive flag")
	}
	if !strings.Contains(err.Error(), "keystroke_echo") {
		t.Errorf("error should mention keystroke_echo method")
	}
}

// =============================================================================
// ProbeOptions Tests (bd-43oxm)
// =============================================================================

func TestProbeOptionsStruct(t *testing.T) {
	flags := DefaultProbeFlags()
	opts := ProbeOptions{
		Session: "test-session",
		Pane:    1,
		Flags:   flags,
	}

	if opts.Session != "test-session" {
		t.Errorf("Session = %q, want %q", opts.Session, "test-session")
	}
	if opts.Pane != 1 {
		t.Errorf("Pane = %d, want 1", opts.Pane)
	}
	if opts.Flags.Method != ProbeMethodKeystrokeEcho {
		t.Errorf("Flags.Method = %v, want %v", opts.Flags.Method, ProbeMethodKeystrokeEcho)
	}
}

func TestProbeOutputErrorFields(t *testing.T) {
	// Test that ProbeOutput can hold error information
	output := ProbeOutput{
		RobotResponse: RobotResponse{
			Success:   false,
			Error:     "session not found",
			ErrorCode: ErrCodeSessionNotFound,
			Hint:      "Use 'ntm list' to see available sessions",
		},
		Session:        "missing-session",
		Pane:           0,
		Responsive:     false,
		ProbeMethod:    ProbeMethodKeystrokeEcho,
		Confidence:     ProbeConfidenceLow,
		Recommendation: ProbeRecommendationLikelyStuck,
	}

	if output.Success {
		t.Error("Success should be false for error")
	}
	if output.Error != "session not found" {
		t.Errorf("Error = %q, want %q", output.Error, "session not found")
	}
	if output.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("ErrorCode = %s, want %s", output.ErrorCode, ErrCodeSessionNotFound)
	}
	if output.Hint == "" {
		t.Error("Hint should be set for errors")
	}
}

// =============================================================================
// FormatProbeDuration Tests (bd-43oxm)
// =============================================================================

func TestFormatProbeDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		wantMs   int64
	}{
		{100 * time.Millisecond, 100},
		{5 * time.Second, 5000},
		{1 * time.Minute, 60000},
		{500 * time.Microsecond, 0}, // Rounds down
		{1500 * time.Millisecond, 1500},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := FormatProbeDuration(tt.duration)
			if got != tt.wantMs {
				t.Errorf("FormatProbeDuration(%v) = %d, want %d", tt.duration, got, tt.wantMs)
			}
		})
	}
}

// =============================================================================
// Edge Case Tests (bd-43oxm)
// =============================================================================

func TestProbeResultDefaults(t *testing.T) {
	// Test that a zero-value ProbeResult has safe defaults
	var result ProbeResult
	if result.Responsive {
		t.Error("default Responsive should be false")
	}
	if result.Confidence != "" {
		// Note: zero value is empty, functions should set this
		t.Log("Confidence is empty for zero-value struct (expected)")
	}
}

func TestPaneChangeLatency(t *testing.T) {
	// Test that latency is calculated correctly
	baseline := &PaneBaseline{
		Content:     "content",
		ContentHash: hashContent("content"),
		LineCount:   1,
		CapturedAt:  time.Now().Add(-100 * time.Millisecond),
	}
	current := &PaneBaseline{
		Content:     "new content",
		ContentHash: hashContent("new content"),
		LineCount:   1,
		CapturedAt:  time.Now(),
	}

	change := ComparePaneState(baseline, current)
	if !change.Changed {
		t.Error("should detect content change")
	}
	// Latency should be approximately 100ms
	if change.LatencyMs < 90 || change.LatencyMs > 150 {
		t.Errorf("LatencyMs = %d, expected ~100", change.LatencyMs)
	}
}

func TestHashContentDeterminism(t *testing.T) {
	// Test that hash is deterministic across calls
	content := "test content with special chars: !@#$%^&*()"
	hash1 := hashContent(content)
	hash2 := hashContent(content)
	hash3 := hashContent(content)

	if hash1 != hash2 || hash2 != hash3 {
		t.Error("hashContent should be deterministic")
	}
}

func TestHashContentCollisionResistance(t *testing.T) {
	// Test that similar strings produce different hashes
	hashes := make(map[string]bool)
	testStrings := []string{
		"test",
		"Test",
		"TEST",
		"test ",
		" test",
		"test1",
		"test2",
		"tset",
		"",
		" ",
		"\n",
		"\t",
	}

	for _, s := range testStrings {
		hash := hashContent(s)
		if hashes[hash] {
			t.Errorf("collision detected for %q", s)
		}
		hashes[hash] = true
	}
}

func TestProbeOutputWithAllFields(t *testing.T) {
	// Test that ProbeOutput correctly serializes all fields
	output := ProbeOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test-session",
		Pane:          2,
		Responsive:    true,
		ProbeMethod:   ProbeMethodKeystrokeEcho,
		ProbeDetails: ProbeDetails{
			InputSent:        "Space+Backspace",
			OutputChanged:    true,
			LatencyMs:        150,
			OutputDeltaLines: 3,
		},
		Confidence:     ProbeConfidenceHigh,
		Recommendation: ProbeRecommendationHealthy,
		Reasoning:      "pane responded in 150ms",
	}

	// Verify all key fields
	if !output.Success {
		t.Error("Success should be true")
	}
	if output.Session != "test-session" {
		t.Errorf("Session = %q", output.Session)
	}
	if output.Pane != 2 {
		t.Errorf("Pane = %d", output.Pane)
	}
	if !output.Responsive {
		t.Error("Responsive should be true")
	}
	if output.ProbeDetails.LatencyMs != 150 {
		t.Errorf("LatencyMs = %d", output.ProbeDetails.LatencyMs)
	}
	if output.Confidence != ProbeConfidenceHigh {
		t.Errorf("Confidence = %s", output.Confidence)
	}
	if output.Recommendation != ProbeRecommendationHealthy {
		t.Errorf("Recommendation = %s", output.Recommendation)
	}
}

func TestProbeMethodStringConversion(t *testing.T) {
	// Test ProbeMethod to string conversion
	if string(ProbeMethodKeystrokeEcho) != "keystroke_echo" {
		t.Errorf("ProbeMethodKeystrokeEcho = %q", ProbeMethodKeystrokeEcho)
	}
	if string(ProbeMethodInterruptTest) != "interrupt_test" {
		t.Errorf("ProbeMethodInterruptTest = %q", ProbeMethodInterruptTest)
	}
}

// =============================================================================
// Mock Tmux Client
// =============================================================================

type MockTmuxClient struct {
	CaptureOutput    string
	CaptureError     error
	SendKeysError    error
	InterruptError   error
	SessionExistsVal bool
	Panes            []tmux.Pane
	PanesError       error

	// Call counters
	CaptureCount   int
	SendKeysCount  int
	InterruptCount int

	// SentLiterals records every payload sent through SendKeys, which uses
	// tmux's literal (-l) mode. A tmux KEY NAME appearing here means it was
	// typed as characters instead of pressed.
	SentLiterals []string

	// SentKeyNames records key presses (BSpace, Escape, ...), which go
	// through a different tmux invocation than literal text.
	SentKeyNames  []string
	SendKeyNameFn func(target, keyName string) error

	// Targets records every "session:window.pane" address the probe built, for
	// every tmux operation. Discarding these is what let the probe address the
	// wrong pane undetected: the batch resolved the right tmux.Pane and then
	// interpolated the wrong index into the target string.
	Targets []string
}

func (m *MockTmuxClient) recordTarget(target string) {
	m.Targets = append(m.Targets, target)
}

func (m *MockTmuxClient) CaptureForStatusDetection(target string) (string, error) {
	m.CaptureCount++
	m.recordTarget(target)
	return m.CaptureOutput, m.CaptureError
}

func (m *MockTmuxClient) CapturePaneOutput(target string, lines int) (string, error) {
	m.CaptureCount++
	m.recordTarget(target)
	return m.CaptureOutput, m.CaptureError
}

func (m *MockTmuxClient) SendKeys(target, keys string, enter bool) error {
	m.SendKeysCount++
	m.recordTarget(target)
	m.SentLiterals = append(m.SentLiterals, keys)
	return m.SendKeysError
}

func (m *MockTmuxClient) SendKeyName(target, keyName string) error {
	m.recordTarget(target)
	m.SentKeyNames = append(m.SentKeyNames, keyName)
	if m.SendKeyNameFn != nil {
		return m.SendKeyNameFn(target, keyName)
	}
	return nil
}

func (m *MockTmuxClient) SendInterrupt(target string) error {
	m.InterruptCount++
	m.recordTarget(target)
	return m.InterruptError
}

func (m *MockTmuxClient) SessionExists(name string) bool {
	return m.SessionExistsVal
}

func (m *MockTmuxClient) GetPanes(session string) ([]tmux.Pane, error) {
	return m.Panes, m.PanesError
}

// Helper to reset and inject mock
func setupMock(t *testing.T) *MockTmuxClient {
	mock := &MockTmuxClient{
		SessionExistsVal: true,
		Panes:            []tmux.Pane{{ID: "0", Index: 0}},
	}
	original := CurrentTmuxClient
	CurrentTmuxClient = mock
	t.Cleanup(func() {
		CurrentTmuxClient = original
	})
	return mock
}

// =============================================================================
// Capture Tests with Mock
// =============================================================================

func TestCapturePaneBaseline_Success(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureOutput = "test content"

	baseline, err := CapturePaneBaseline("test:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if baseline.Content != "test content" {
		t.Errorf("content = %q, want %q", baseline.Content, "test content")
	}
	if baseline.LineCount != 1 {
		t.Errorf("line count = %d, want 1", baseline.LineCount)
	}
}

func TestCapturePaneBaseline_Empty(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureOutput = ""

	baseline, err := CapturePaneBaseline("test:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if baseline.Content != "" {
		t.Errorf("content = %q, want empty", baseline.Content)
	}
	if baseline.LineCount != 0 {
		t.Errorf("line count = %d, want 0", baseline.LineCount)
	}
}

func TestCapturePaneBaseline_Failure(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureError = fmt.Errorf("tmux error")

	_, err := CapturePaneBaseline("test:0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "baseline capture failed") {
		t.Errorf("error = %q, want 'baseline capture failed'", err.Error())
	}
}

// =============================================================================
// Probe Execution Tests with Mock
// =============================================================================

func TestProbeKeystrokeEcho_Responsive(t *testing.T) {
	// First capture (baseline)
	// Second capture (current) - different content
	// Since we can't easily change mock output between calls in this simple mock,
	// we assume the implementation calls Capture twice.
	// We need a smarter mock that returns sequence of outputs.

	// Improving mock for this test
	mockSeq := &MockTmuxClientSequence{
		Outputs: []string{"baseline", "changed output"},
	}
	original := CurrentTmuxClient
	CurrentTmuxClient = mockSeq
	t.Cleanup(func() { CurrentTmuxClient = original })

	result := probeKeystrokeEcho("test:0", 100*time.Millisecond)

	if !result.Responsive {
		t.Error("expected responsive result")
	}
	if result.Confidence != ProbeConfidenceHigh {
		t.Errorf("confidence = %s, want High", result.Confidence)
	}
	// The probe types ONE literal character (the space) and PRESSES the
	// backspace key. Both used to go through SendKeys, which sends literally,
	// so "BSpace" was typed into the pane as six characters instead of
	// erasing the probe character.
	if mockSeq.SendKeysCount != 1 {
		t.Errorf("SendKeysCount = %d, want 1 (the literal space only)", mockSeq.SendKeysCount)
	}
	if got := mockSeq.SentKeyNames; len(got) != 1 || got[0] != "BSpace" {
		t.Errorf("SentKeyNames = %v, want exactly [BSpace] pressed as a key", got)
	}
}

func TestProbeKeystrokeEcho_Unresponsive(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureOutput = "static content"

	result := probeKeystrokeEcho("test:0", 50*time.Millisecond)

	if result.Responsive {
		t.Error("expected unresponsive result")
	}
	if result.Confidence != ProbeConfidenceMedium {
		t.Errorf("confidence = %s, want Medium", result.Confidence)
	}
	if result.Recommendation != ProbeRecommendationLikelyStuck {
		t.Errorf("recommendation = %s, want LikelyStuck", result.Recommendation)
	}
}

func TestProbeInterruptTest_Responsive(t *testing.T) {
	mockSeq := &MockTmuxClientSequence{
		Outputs: []string{"baseline", "changed output"},
	}
	original := CurrentTmuxClient
	CurrentTmuxClient = mockSeq
	t.Cleanup(func() { CurrentTmuxClient = original })

	result := probeInterruptTest("test:0", 100*time.Millisecond)

	if !result.Responsive {
		t.Error("expected responsive result")
	}
	if mockSeq.InterruptCount != 1 {
		t.Errorf("InterruptCount = %d, want 1", mockSeq.InterruptCount)
	}
}

func TestProbeInterruptTest_Unresponsive(t *testing.T) {
	mock := setupMock(t)
	mock.CaptureOutput = "static content"

	result := probeInterruptTest("test:0", 50*time.Millisecond)

	if result.Responsive {
		t.Error("expected unresponsive result")
	}
	if result.Confidence != ProbeConfidenceHigh {
		t.Errorf("confidence = %s, want High", result.Confidence)
	}
	if result.Recommendation != ProbeRecommendationDefinitelyStuck {
		t.Errorf("recommendation = %s, want DefinitelyStuck", result.Recommendation)
	}
}

// Mock with sequential outputs
type MockTmuxClientSequence struct {
	MockTmuxClient
	Outputs   []string
	CallIndex int
}

func (m *MockTmuxClientSequence) CaptureForStatusDetection(target string) (string, error) {
	m.CaptureCount++
	if m.CallIndex < len(m.Outputs) {
		out := m.Outputs[m.CallIndex]
		m.CallIndex++
		return out, nil
	}
	// Return last output if exhausted
	if len(m.Outputs) > 0 {
		return m.Outputs[len(m.Outputs)-1], nil
	}
	return "", nil
}

// Wake-ping probe (ntm-7rgt): one structured call answers TUI liveness AND
// whether the rate-limit wall is still up, with a tail sample — replacing the
// raw `tmux send-keys "ping" Enter; sleep 5; --robot-tail` folklore.
func TestProbeWakePing(t *testing.T) {
	t.Run("rate-limited pane reports still limited", func(t *testing.T) {
		mock := setupMock(t)
		mock.Panes = []tmux.Pane{{ID: "%5", Index: 2, Type: tmux.AgentClaude, Title: "proj__cc_1"}}
		mock.CaptureOutput = "some output\nrate limit reached, resets at 4pm\n"

		out, err := GetProbe(ProbeOptions{
			Session: "proj", Pane: 2,
			Flags: ProbeFlags{Method: ProbeMethodWakePing, TimeoutMs: 50},
		})
		if err != nil {
			t.Fatalf("GetProbe: %v", err)
		}
		if !out.Success {
			t.Fatalf("wake_ping failed: %s", out.Error)
		}
		if out.ProbeDetails.StillRateLimited == nil || !*out.ProbeDetails.StillRateLimited {
			t.Fatalf("still_rate_limited = %v, want true", out.ProbeDetails.StillRateLimited)
		}
		if len(out.ProbeDetails.TailSample) == 0 {
			t.Error("tail_sample missing")
		}
	})

	t.Run("clear pane reports wall down", func(t *testing.T) {
		mock := setupMock(t)
		mock.Panes = []tmux.Pane{{ID: "%5", Index: 2, Type: tmux.AgentCodex, Title: "proj__cod_1"}}
		mock.CaptureOutput = "› \n100% context left\n"

		out, err := GetProbe(ProbeOptions{
			Session: "proj", Pane: 2,
			Flags: ProbeFlags{Method: ProbeMethodWakePing, TimeoutMs: 50},
		})
		if err != nil {
			t.Fatalf("GetProbe: %v", err)
		}
		if out.ProbeDetails.StillRateLimited == nil || *out.ProbeDetails.StillRateLimited {
			t.Fatalf("still_rate_limited = %v, want false", out.ProbeDetails.StillRateLimited)
		}
	})

	t.Run("user pane refused", func(t *testing.T) {
		mock := setupMock(t)
		mock.Panes = []tmux.Pane{{ID: "%1", Index: 0, Type: tmux.AgentUser, Title: "proj"}}

		out, err := GetProbe(ProbeOptions{
			Session: "proj", Pane: 0,
			Flags: ProbeFlags{Method: ProbeMethodWakePing, TimeoutMs: 50},
		})
		if err != nil {
			t.Fatalf("GetProbe: %v", err)
		}
		if out.Success {
			t.Fatal("wake_ping against a user pane must be refused")
		}
		if out.ErrorCode != ErrCodeInvalidFlag {
			t.Fatalf("error code = %s, want %s", out.ErrorCode, ErrCodeInvalidFlag)
		}
	})
}

// pane_index is window-local, so a window-per-agent layout gives every agent
// pane index 0. Enumerating and resolving by that bare index made the whole
// batch collapse onto window 0's pane: three probes all hit the same agent
// while the response reported results for panes that were never touched.
func TestGetProbeSession_WindowPerAgentDoesNotCollapse(t *testing.T) {
	mock := setupMock(t)
	mock.Panes = []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 0, WindowIndex: 1, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		{ID: "%3", Index: 0, WindowIndex: 2, NTMIndex: 3, Title: "proj__cc_3", Type: tmux.AgentClaude},
	}

	output, exit := GetProbeSession(ProbeSessionOptions{
		Session: "proj",
		Flags:   ProbeFlags{Method: ProbeMethodKeystrokeEcho, TimeoutMs: 1},
	})
	_ = exit

	if len(output.Probes) != 3 {
		t.Fatalf("probed %d panes, want 3 (one per window)", len(output.Probes))
	}
	seen := map[int]int{}
	for _, probe := range output.Probes {
		seen[probe.Pane]++
	}
	if len(seen) != 3 {
		t.Fatalf("probe keys collapsed onto %d distinct pane(s): %v — each window's agent must be addressed separately", len(seen), seen)
	}
	for pane, count := range seen {
		if count != 1 {
			t.Fatalf("pane %d probed %d times, want exactly 1", pane, count)
		}
	}

	// Resolving the right tmux.Pane is only half the job: the target string
	// must carry that pane's own window-local index. Interpolating the NTM
	// index instead produced "proj:1.2" for a window that has only pane 0, so
	// every capture failed and every healthy agent read back "likely_stuck" —
	// and where a window did have that many panes, the probe keystrokes and
	// the interrupt-test Ctrl-C landed on a different agent entirely.
	wantTargets := map[string]bool{"proj:0.0": true, "proj:1.0": true, "proj:2.0": true}
	if len(mock.Targets) == 0 {
		t.Fatal("probe issued no tmux operations")
	}
	for _, target := range mock.Targets {
		if !wantTargets[target] {
			t.Fatalf("probe addressed %q; want one of %v (window-local pane index, not the NTM agent index)", target, wantTargets)
		}
	}
}

// SendKeys sends with tmux's literal (-l) flag, so a KEY NAME passed to it is
// typed as characters. The probe's cleanup used SendKeys("BSpace"), which put
// the six letters "BSpace" into the pane rather than erasing the probe
// character — verified live before the fix, where an agent composer was left
// reading "❯  BSpace", text the operator's next Enter would submit.
func TestProbeKeystrokeEchoNeverTypesKeyNamesLiterally(t *testing.T) {
	mock := setupMock(t)
	mock.Panes = []tmux.Pane{{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude}}
	mock.CaptureOutput = "static screen"

	_, err := GetProbe(ProbeOptions{
		Session: "proj",
		Pane:    1,
		Flags:   ProbeFlags{Method: ProbeMethodKeystrokeEcho, TimeoutMs: 1},
	})
	if err != nil {
		t.Fatalf("GetProbe() error = %v", err)
	}

	for _, sent := range mock.SentLiterals {
		if sent == "BSpace" || sent == "Escape" || sent == "Enter" || sent == "C-u" {
			t.Fatalf("probe sent the tmux key name %q as literal text; it would be typed into the pane, not pressed", sent)
		}
	}
}
