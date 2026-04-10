package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// =============================================================================
// IsBeadInCycle — 0% → 100%
// =============================================================================

func TestIsBeadInCycle_Found(t *testing.T) {

	cycles := [][]string{
		{"bd-001", "bd-002", "bd-003"},
		{"bd-010", "bd-020"},
	}

	if !IsBeadInCycle("bd-002", cycles) {
		t.Error("expected bd-002 to be in cycle")
	}
	if !IsBeadInCycle("bd-020", cycles) {
		t.Error("expected bd-020 to be in cycle")
	}
}

func TestIsBeadInCycle_NotFound(t *testing.T) {

	cycles := [][]string{
		{"bd-001", "bd-002"},
	}

	if IsBeadInCycle("bd-999", cycles) {
		t.Error("expected bd-999 NOT to be in cycle")
	}
}

func TestIsBeadInCycle_EmptyCycles(t *testing.T) {

	if IsBeadInCycle("bd-001", nil) {
		t.Error("expected false for nil cycles")
	}
	if IsBeadInCycle("bd-001", [][]string{}) {
		t.Error("expected false for empty cycles")
	}
}

// =============================================================================
// getAgentStyle — 0% → 100%
// =============================================================================

func TestGetAgentStyle(t *testing.T) {

	th := theme.Current()

	tests := []struct {
		name      string
		agentType string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
		{"gemini", "gemini"},
		{"unknown", "aider"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := getAgentStyle(tt.agentType, th)
			// Style should be usable — render something to verify no panic
			rendered := style.Render("test")
			if rendered == "" {
				t.Error("expected non-empty styled string")
			}
		})
	}
}

// =============================================================================
// getPriorityStyle — 0% → 100%
// =============================================================================

func TestGetPriorityStyle(t *testing.T) {

	th := theme.Current()

	tests := []struct {
		name     string
		priority string
	}{
		{"P0", "P0"},
		{"P1", "P1"},
		{"P2", "P2"},
		{"P3_default", "P3"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			style := getPriorityStyle(tt.priority, th)
			rendered := style.Render("test")
			if rendered == "" {
				t.Error("expected non-empty styled string")
			}
		})
	}
}

// =============================================================================
// makeRetryEnvelope — 0% → 100%
// =============================================================================

func TestMakeRetryEnvelope_Success(t *testing.T) {

	data := &RetryData{
		Summary: RetrySummary{TotalFailed: 3, RetriedCount: 2, SkippedCount: 1},
	}
	env := makeRetryEnvelope("my-session", true, data, "", "", nil)

	if env.Command != "assign" {
		t.Errorf("Command = %q, want assign", env.Command)
	}
	if env.Subcommand != "retry" {
		t.Errorf("Subcommand = %q, want retry", env.Subcommand)
	}
	if env.Session != "my-session" {
		t.Errorf("Session = %q, want my-session", env.Session)
	}
	if !env.Success {
		t.Error("expected Success=true")
	}
	if env.Error != nil {
		t.Error("expected nil error")
	}
	if env.Data == nil {
		t.Fatal("expected non-nil Data")
	}
	if env.Data.Summary.TotalFailed != 3 {
		t.Errorf("Data.Summary.TotalFailed = %d, want 3", env.Data.Summary.TotalFailed)
	}
	// Nil warnings should be converted to empty slice
	if len(env.Warnings) != 0 {
		t.Errorf("Warnings = %v, want empty", env.Warnings)
	}
}

func TestMakeRetryEnvelope_WithError(t *testing.T) {

	env := makeRetryEnvelope("s", false, nil, "STORE_ERROR", "broken", []string{"w1"})

	if env.Success {
		t.Error("expected Success=false")
	}
	if env.Error == nil {
		t.Fatal("expected non-nil error")
	}
	if env.Error.Code != "STORE_ERROR" {
		t.Errorf("Error.Code = %q, want STORE_ERROR", env.Error.Code)
	}
	if env.Error.Message != "broken" {
		t.Errorf("Error.Message = %q, want broken", env.Error.Message)
	}
	if len(env.Warnings) != 1 || env.Warnings[0] != "w1" {
		t.Errorf("Warnings = %v, want [w1]", env.Warnings)
	}
}

func TestMakeRetryEnvelope_JSONRoundtrip(t *testing.T) {

	env := makeRetryEnvelope("sess", true, nil, "", "", nil)
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["command"] != "assign" {
		t.Errorf("decoded command = %v", decoded["command"])
	}
}

// =============================================================================
// makeDirectAssignEnvelope — 0% → 100%
// =============================================================================

func TestMakeDirectAssignEnvelope_Success(t *testing.T) {

	data := &DirectAssignData{
		Assignment: &DirectAssignItem{BeadID: "bd-1"},
	}
	env := makeDirectAssignEnvelope("sess", true, data, "", "", nil)

	if env.Command != "assign" {
		t.Errorf("Command = %q", env.Command)
	}
	if env.Subcommand != "pane" {
		t.Errorf("Subcommand = %q", env.Subcommand)
	}
	if !env.Success {
		t.Error("expected success")
	}
	if env.Error != nil {
		t.Error("expected nil error")
	}
	// nil warnings → empty
	if len(env.Warnings) != 0 {
		t.Errorf("Warnings = %v", env.Warnings)
	}
}

func TestMakeDirectAssignEnvelope_WithError(t *testing.T) {

	env := makeDirectAssignEnvelope("s", false, nil, "INVALID_ARGS", "bad", []string{"w"})

	if env.Success {
		t.Error("expected failure")
	}
	if env.Error == nil || env.Error.Code != "INVALID_ARGS" {
		t.Errorf("Error = %+v", env.Error)
	}
}

// =============================================================================
// marshalAssignOutput — 0% → 100%
// =============================================================================

func TestMarshalAssignOutput_Nil(t *testing.T) {

	data, err := marshalAssignOutput(nil)
	if err != nil {
		t.Fatalf("marshalAssignOutput(nil): %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON")
	}
}

func TestShouldOfferAssignWatchOverlay(t *testing.T) {

	tests := []struct {
		name           string
		session        string
		inTmux         bool
		currentSession string
		want           bool
	}{
		{name: "matching tmux session", session: "proj", inTmux: true, currentSession: "proj", want: true},
		{name: "outside tmux", session: "proj", inTmux: false, currentSession: "proj", want: false},
		{name: "different tmux session", session: "proj", inTmux: true, currentSession: "other", want: false},
		{name: "missing target session", session: "", inTmux: true, currentSession: "proj", want: false},
		{name: "missing current session", session: "proj", inTmux: true, currentSession: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldOfferAssignWatchOverlay(tt.session, tt.inTmux, tt.currentSession); got != tt.want {
				t.Fatalf("shouldOfferAssignWatchOverlay(%q, %v, %q) = %v, want %v", tt.session, tt.inTmux, tt.currentSession, got, tt.want)
			}
		})
	}
}

func TestBuildAssignWatchOverlayHint(t *testing.T) {

	if got := buildAssignWatchOverlayHint("F12", false); got != "Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running." {
		t.Fatalf("existing binding hint = %q", got)
	}

	if got := buildAssignWatchOverlayHint("F12", true); got != "Hint: installed the F12 overlay binding. Press F12 for the attention-aware dashboard overlay while assign --watch is running." {
		t.Fatalf("installed binding hint = %q", got)
	}
}

func TestBuildAssignWatchOverlayWarning(t *testing.T) {

	got := buildAssignWatchOverlayWarning("F12", errors.New("boom"))
	want := "Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut."
	if got != want {
		t.Fatalf("buildAssignWatchOverlayWarning() = %q, want %q", got, want)
	}
}

func TestPrepareAssignWatchOverlay_SkipsOutsideUsefulContext(t *testing.T) {

	boundCalls := 0
	ensureCalls := 0
	prep := prepareAssignWatchOverlay(
		"proj",
		false,
		"proj",
		func(string) bool {
			boundCalls++
			return false
		},
		func(string) error {
			ensureCalls++
			return nil
		},
	)
	t.Logf("outside useful context => prep=%+v boundCalls=%d ensureCalls=%d", prep, boundCalls, ensureCalls)

	if prep.Hint != "" || prep.Warning != "" {
		t.Fatalf("unexpected prep outside tmux: %+v", prep)
	}
	if boundCalls != 0 {
		t.Fatalf("isBound called %d times, want 0", boundCalls)
	}
	if ensureCalls != 0 {
		t.Fatalf("ensureBinding called %d times, want 0", ensureCalls)
	}
}

func TestPrepareAssignWatchOverlay_UsesExistingBinding(t *testing.T) {

	boundCalls := 0
	ensureCalls := 0
	prep := prepareAssignWatchOverlay(
		"proj",
		true,
		"proj",
		func(key string) bool {
			boundCalls++
			if key != assignWatchOverlayKey {
				t.Fatalf("unexpected key %q", key)
			}
			return true
		},
		func(string) error {
			ensureCalls++
			return nil
		},
	)
	t.Logf("existing binding => prep=%+v boundCalls=%d ensureCalls=%d", prep, boundCalls, ensureCalls)

	if prep.Warning != "" {
		t.Fatalf("unexpected warning: %q", prep.Warning)
	}
	if prep.Hint != "Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running." {
		t.Fatalf("unexpected hint: %q", prep.Hint)
	}
	if strings.Contains(prep.Hint, "installed") {
		t.Fatalf("did not expect install hint: %q", prep.Hint)
	}
	if boundCalls != 1 {
		t.Fatalf("isBound called %d times, want 1", boundCalls)
	}
	if ensureCalls != 0 {
		t.Fatalf("ensureBinding called %d times, want 0", ensureCalls)
	}
}

func TestPrepareAssignWatchOverlay_InstallsMissingBinding(t *testing.T) {

	boundCalls := 0
	ensureCalls := 0
	prep := prepareAssignWatchOverlay(
		"proj",
		true,
		"proj",
		func(key string) bool {
			boundCalls++
			if key != assignWatchOverlayKey {
				t.Fatalf("unexpected key %q", key)
			}
			return false
		},
		func(key string) error {
			ensureCalls++
			if key != assignWatchOverlayKey {
				t.Fatalf("unexpected key %q", key)
			}
			return nil
		},
	)
	t.Logf("missing binding => prep=%+v boundCalls=%d ensureCalls=%d", prep, boundCalls, ensureCalls)

	if prep.Warning != "" {
		t.Fatalf("unexpected warning: %q", prep.Warning)
	}
	if prep.Hint != "Hint: installed the F12 overlay binding. Press F12 for the attention-aware dashboard overlay while assign --watch is running." {
		t.Fatalf("unexpected install hint: %q", prep.Hint)
	}
	if boundCalls != 1 {
		t.Fatalf("isBound called %d times, want 1", boundCalls)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensureBinding called %d times, want 1", ensureCalls)
	}
}

func TestPrepareAssignWatchOverlay_ReportsBindingSetupFailure(t *testing.T) {

	boundCalls := 0
	ensureCalls := 0
	prep := prepareAssignWatchOverlay(
		"proj",
		true,
		"proj",
		func(string) bool {
			boundCalls++
			return false
		},
		func(string) error {
			ensureCalls++
			return errors.New("boom")
		},
	)
	t.Logf("binding setup failure => prep=%+v boundCalls=%d ensureCalls=%d", prep, boundCalls, ensureCalls)

	if prep.Hint != "" {
		t.Fatalf("unexpected hint: %q", prep.Hint)
	}
	want := "Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut."
	if prep.Warning != want {
		t.Fatalf("warning = %q, want %q", prep.Warning, want)
	}
	if boundCalls != 1 {
		t.Fatalf("isBound called %d times, want 1", boundCalls)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensureBinding called %d times, want 1", ensureCalls)
	}
}

func TestPrepareAssignWatchOverlay_SkipsWhenBindingHooksUnavailable(t *testing.T) {

	t.Run("missing isBound hook", func(t *testing.T) {

		prep := prepareAssignWatchOverlay("proj", true, "proj", nil, func(string) error {
			t.Fatal("ensureBinding should not be called when isBound is unavailable")
			return nil
		})
		t.Logf("missing isBound hook => prep=%+v", prep)
		if prep != (assignWatchOverlayPreparation{}) {
			t.Fatalf("unexpected prep: %+v", prep)
		}
	})

	t.Run("missing ensureBinding hook", func(t *testing.T) {

		prep := prepareAssignWatchOverlay("proj", true, "proj", func(string) bool { return false }, nil)
		t.Logf("missing ensureBinding hook => prep=%+v", prep)
		if prep != (assignWatchOverlayPreparation{}) {
			t.Fatalf("unexpected prep: %+v", prep)
		}
	})
}

func TestAnnounceAssignWatchOverlay_SkipsEmptyPreparation(t *testing.T) {

	var logs []string
	announceAssignWatchOverlay(func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}, assignWatchOverlayPreparation{})

	if len(logs) != 0 {
		t.Fatalf("expected no logs, got %v", logs)
	}
}

func TestAnnounceAssignWatchOverlay_LogsPreparedHintAndWarning(t *testing.T) {

	tests := []struct {
		name string
		prep assignWatchOverlayPreparation
		want []string
	}{
		{
			name: "hint only",
			prep: assignWatchOverlayPreparation{
				Hint: "Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
			want: []string{
				"Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
		},
		{
			name: "warning only",
			prep: assignWatchOverlayPreparation{
				Warning: "Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.",
			},
			want: []string{
				"Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.",
			},
		},
		{
			name: "warning then hint",
			prep: assignWatchOverlayPreparation{
				Warning: "warning",
				Hint:    "hint",
			},
			want: []string{"warning", "hint"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			var logs []string
			announceAssignWatchOverlay(func(format string, args ...interface{}) {
				logs = append(logs, fmt.Sprintf(format, args...))
			}, tt.prep)

			if len(logs) != len(tt.want) {
				t.Fatalf("log count = %d, want %d (%v)", len(logs), len(tt.want), logs)
			}
			for i := range tt.want {
				if logs[i] != tt.want[i] {
					t.Fatalf("log[%d] = %q, want %q", i, logs[i], tt.want[i])
				}
			}
		})
	}
}

func TestAnnounceAssignWatchOverlay_WithPreparedScenarios(t *testing.T) {

	tests := []struct {
		name    string
		prepFn  func(*testing.T) assignWatchOverlayPreparation
		wantLog []string
	}{
		{
			name: "outside useful context",
			prepFn: func(*testing.T) assignWatchOverlayPreparation {
				return prepareAssignWatchOverlay("proj", false, "proj", func(string) bool { return false }, func(string) error { return nil })
			},
			wantLog: nil,
		},
		{
			name: "existing binding",
			prepFn: func(t *testing.T) assignWatchOverlayPreparation {
				return prepareAssignWatchOverlay("proj", true, "proj", func(string) bool { return true }, func(string) error {
					t.Fatal("ensureBinding should not be called when binding exists")
					return nil
				})
			},
			wantLog: []string{
				"Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
		},
		{
			name: "installs missing binding",
			prepFn: func(*testing.T) assignWatchOverlayPreparation {
				return prepareAssignWatchOverlay("proj", true, "proj", func(string) bool { return false }, func(string) error {
					return nil
				})
			},
			wantLog: []string{
				"Hint: installed the F12 overlay binding. Press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
		},
		{
			name: "binding setup failure",
			prepFn: func(*testing.T) assignWatchOverlayPreparation {
				return prepareAssignWatchOverlay("proj", true, "proj", func(string) bool { return false }, func(string) error {
					return errors.New("boom")
				})
			},
			wantLog: []string{
				"Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			prep := tt.prepFn(t)
			var logs []string
			announceAssignWatchOverlay(func(format string, args ...interface{}) {
				logs = append(logs, fmt.Sprintf(format, args...))
			}, prep)

			if len(logs) != len(tt.wantLog) {
				t.Fatalf("log count = %d, want %d (%v)", len(logs), len(tt.wantLog), logs)
			}
			for i := range tt.wantLog {
				if logs[i] != tt.wantLog[i] {
					t.Fatalf("log[%d] = %q, want %q", i, logs[i], tt.wantLog[i])
				}
			}
		})
	}
}

func TestPrepareAndAnnounceAssignWatchOverlay(t *testing.T) {

	tests := []struct {
		name             string
		session          string
		inTmux           bool
		currentSession   string
		isBound          func(*testing.T, string) bool
		ensureBinding    func(*testing.T, string) error
		wantLogs         []string
		wantIsBoundCalls int
		wantEnsureCalls  int
	}{
		{
			name:           "outside useful context stays silent",
			session:        "proj",
			inTmux:         false,
			currentSession: "proj",
			isBound: func(t *testing.T, _ string) bool {
				t.Fatal("isBound should not be called outside useful context")
				return false
			},
			ensureBinding: func(t *testing.T, _ string) error {
				t.Fatal("ensureBinding should not be called outside useful context")
				return nil
			},
		},
		{
			name:           "existing binding logs discoverability hint",
			session:        "proj",
			inTmux:         true,
			currentSession: "proj",
			isBound: func(*testing.T, string) bool {
				return true
			},
			ensureBinding: func(t *testing.T, _ string) error {
				t.Fatal("ensureBinding should not be called when binding already exists")
				return nil
			},
			wantLogs: []string{
				"Hint: press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
			wantIsBoundCalls: 1,
		},
		{
			name:           "missing binding installs and logs upgraded hint",
			session:        "proj",
			inTmux:         true,
			currentSession: "proj",
			isBound: func(*testing.T, string) bool {
				return false
			},
			ensureBinding: func(*testing.T, string) error {
				return nil
			},
			wantLogs: []string{
				"Hint: installed the F12 overlay binding. Press F12 for the attention-aware dashboard overlay while assign --watch is running.",
			},
			wantIsBoundCalls: 1,
			wantEnsureCalls:  1,
		},
		{
			name:           "binding setup failure logs warning",
			session:        "proj",
			inTmux:         true,
			currentSession: "proj",
			isBound: func(*testing.T, string) bool {
				return false
			},
			ensureBinding: func(*testing.T, string) error {
				return errors.New("boom")
			},
			wantLogs: []string{
				"Warning: Could not auto-set up the F12 overlay binding (boom); run 'ntm bind --overlay' if you want the attention-aware dashboard overlay shortcut.",
			},
			wantIsBoundCalls: 1,
			wantEnsureCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			var logs []string
			isBoundCalls := 0
			ensureCalls := 0

			prep := prepareAndAnnounceAssignWatchOverlay(
				func(format string, args ...interface{}) {
					logs = append(logs, fmt.Sprintf(format, args...))
				},
				tt.session,
				tt.inTmux,
				tt.currentSession,
				func(key string) bool {
					isBoundCalls++
					return tt.isBound(t, key)
				},
				func(key string) error {
					ensureCalls++
					return tt.ensureBinding(t, key)
				},
			)
			t.Logf(
				"session=%q inTmux=%v currentSession=%q logs=%v isBoundCalls=%d ensureCalls=%d prep=%+v",
				tt.session,
				tt.inTmux,
				tt.currentSession,
				logs,
				isBoundCalls,
				ensureCalls,
				prep,
			)

			if len(logs) != len(tt.wantLogs) {
				t.Fatalf("log count = %d, want %d (%v)", len(logs), len(tt.wantLogs), logs)
			}
			for i := range tt.wantLogs {
				if logs[i] != tt.wantLogs[i] {
					t.Fatalf("log[%d] = %q, want %q", i, logs[i], tt.wantLogs[i])
				}
			}
			if isBoundCalls != tt.wantIsBoundCalls {
				t.Fatalf("isBoundCalls = %d, want %d", isBoundCalls, tt.wantIsBoundCalls)
			}
			if ensureCalls != tt.wantEnsureCalls {
				t.Fatalf("ensureCalls = %d, want %d", ensureCalls, tt.wantEnsureCalls)
			}

			wantPrep := assignWatchOverlayPreparation{}
			if len(tt.wantLogs) == 1 {
				switch {
				case strings.HasPrefix(tt.wantLogs[0], "Hint:"):
					wantPrep.Hint = tt.wantLogs[0]
				case strings.HasPrefix(tt.wantLogs[0], "Warning:"):
					wantPrep.Warning = tt.wantLogs[0]
				}
			}
			if prep != wantPrep {
				t.Fatalf("prep = %+v, want %+v", prep, wantPrep)
			}
		})
	}
}
