package robot

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// synthesisStatus tests
// =============================================================================

func TestSynthesisStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      ensemble.EnsembleStatus
		assignments []ensemble.ModeAssignment
		want        string
	}{
		{
			name:   "synthesizing status",
			status: ensemble.EnsembleSynthesizing,
			want:   "running",
		},
		{
			name:   "complete status",
			status: ensemble.EnsembleComplete,
			want:   "complete",
		},
		{
			name:   "error status",
			status: ensemble.EnsembleError,
			want:   "error",
		},
		{
			name:        "no assignments returns not_started",
			status:      ensemble.EnsembleActive,
			assignments: nil,
			want:        "not_started",
		},
		{
			name:        "empty assignments returns not_started",
			status:      ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{},
			want:        "not_started",
		},
		{
			name:   "assignment with error returns error",
			status: ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{
				{ModeID: "a", Status: ensemble.AssignmentDone},
				{ModeID: "b", Status: ensemble.AssignmentError},
			},
			want: "error",
		},
		{
			name:   "pending assignment returns not_started",
			status: ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{
				{ModeID: "a", Status: ensemble.AssignmentDone},
				{ModeID: "b", Status: ensemble.AssignmentPending},
			},
			want: "not_started",
		},
		{
			name:   "active assignment returns not_started",
			status: ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{
				{ModeID: "a", Status: ensemble.AssignmentActive},
			},
			want: "not_started",
		},
		{
			name:   "injecting assignment returns not_started",
			status: ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{
				{ModeID: "a", Status: ensemble.AssignmentInjecting},
			},
			want: "not_started",
		},
		{
			name:   "all done returns ready",
			status: ensemble.EnsembleActive,
			assignments: []ensemble.ModeAssignment{
				{ModeID: "a", Status: ensemble.AssignmentDone},
				{ModeID: "b", Status: ensemble.AssignmentDone},
			},
			want: "ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := synthesisStatus(tt.status, tt.assignments)
			if got != tt.want {
				t.Errorf("synthesisStatus(%v, %v) = %q, want %q", tt.status, tt.assignments, got, tt.want)
			}
		})
	}
}

// =============================================================================
// mergeBudgetConfig tests
// =============================================================================

func TestMergeBudgetConfig_Override(t *testing.T) {
	t.Parallel()

	t.Run("override all fields", func(t *testing.T) {
		t.Parallel()
		base := ensemble.BudgetConfig{
			MaxTokensPerMode:       1000,
			MaxTotalTokens:         10000,
			SynthesisReserveTokens: 500,
			ContextReserveTokens:   200,
			TimeoutPerMode:         5 * time.Minute,
			TotalTimeout:           30 * time.Minute,
			MaxRetries:             2,
		}
		override := ensemble.BudgetConfig{
			MaxTokensPerMode:       2000,
			MaxTotalTokens:         20000,
			SynthesisReserveTokens: 1000,
			ContextReserveTokens:   400,
			TimeoutPerMode:         10 * time.Minute,
			TotalTimeout:           60 * time.Minute,
			MaxRetries:             5,
		}
		got := mergeBudgetConfig(base, override)
		if got.MaxTokensPerMode != 2000 {
			t.Errorf("MaxTokensPerMode = %d, want 2000", got.MaxTokensPerMode)
		}
		if got.MaxTotalTokens != 20000 {
			t.Errorf("MaxTotalTokens = %d, want 20000", got.MaxTotalTokens)
		}
		if got.SynthesisReserveTokens != 1000 {
			t.Errorf("SynthesisReserveTokens = %d, want 1000", got.SynthesisReserveTokens)
		}
		if got.ContextReserveTokens != 400 {
			t.Errorf("ContextReserveTokens = %d, want 400", got.ContextReserveTokens)
		}
		if got.TimeoutPerMode != 10*time.Minute {
			t.Errorf("TimeoutPerMode = %v, want 10m", got.TimeoutPerMode)
		}
		if got.TotalTimeout != 60*time.Minute {
			t.Errorf("TotalTimeout = %v, want 60m", got.TotalTimeout)
		}
		if got.MaxRetries != 5 {
			t.Errorf("MaxRetries = %d, want 5", got.MaxRetries)
		}
	})

	t.Run("zero overrides keep base values", func(t *testing.T) {
		t.Parallel()
		base := ensemble.BudgetConfig{
			MaxTokensPerMode:       1000,
			MaxTotalTokens:         10000,
			SynthesisReserveTokens: 500,
			ContextReserveTokens:   200,
			TimeoutPerMode:         5 * time.Minute,
			TotalTimeout:           30 * time.Minute,
			MaxRetries:             2,
		}
		override := ensemble.BudgetConfig{} // all zero
		got := mergeBudgetConfig(base, override)
		if got.MaxTokensPerMode != 1000 {
			t.Errorf("MaxTokensPerMode = %d, want 1000", got.MaxTokensPerMode)
		}
		if got.MaxTotalTokens != 10000 {
			t.Errorf("MaxTotalTokens = %d, want 10000", got.MaxTotalTokens)
		}
		if got.MaxRetries != 2 {
			t.Errorf("MaxRetries = %d, want 2", got.MaxRetries)
		}
	})

	t.Run("partial override", func(t *testing.T) {
		t.Parallel()
		base := ensemble.BudgetConfig{
			MaxTokensPerMode: 1000,
			MaxTotalTokens:   10000,
			MaxRetries:       2,
		}
		override := ensemble.BudgetConfig{
			MaxTotalTokens: 50000,
		}
		got := mergeBudgetConfig(base, override)
		if got.MaxTokensPerMode != 1000 {
			t.Errorf("MaxTokensPerMode = %d, want 1000 (unchanged)", got.MaxTokensPerMode)
		}
		if got.MaxTotalTokens != 50000 {
			t.Errorf("MaxTotalTokens = %d, want 50000 (overridden)", got.MaxTotalTokens)
		}
		if got.MaxRetries != 2 {
			t.Errorf("MaxRetries = %d, want 2 (unchanged)", got.MaxRetries)
		}
	})
}

// =============================================================================
// buildEnsembleHints tests
// =============================================================================

func TestBuildEnsembleHints_Actions(t *testing.T) {
	t.Parallel()

	t.Run("pending modes suggest wait", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{
				TotalModes: 3,
				Completed:  1,
				Working:    1,
				Pending:    1,
			},
			Ensemble: EnsembleState{
				Modes: []EnsembleMode{},
			},
		}
		hints := buildEnsembleHints(output)
		if hints == nil {
			t.Fatal("expected non-nil hints")
		}
		if hints.Summary == "" {
			t.Error("expected non-empty summary")
		}
		found := false
		for _, action := range hints.SuggestedActions {
			if action.Action == "wait" {
				found = true
			}
		}
		if !found {
			t.Error("expected 'wait' suggested action when modes are pending")
		}
	})

	t.Run("all complete and ready suggests synthesize", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{
				TotalModes: 3,
				Completed:  3,
				Working:    0,
				Pending:    0,
			},
			Ensemble: EnsembleState{
				Modes: []EnsembleMode{},
				Synthesis: EnsembleSynthesis{
					Status: "ready",
				},
			},
		}
		hints := buildEnsembleHints(output)
		if hints == nil {
			t.Fatal("expected non-nil hints")
		}
		found := false
		for _, action := range hints.SuggestedActions {
			if action.Action == "synthesize" {
				found = true
			}
		}
		if !found {
			t.Error("expected 'synthesize' suggested action when all modes complete")
		}
	})

	t.Run("error mode adds warning", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{
				TotalModes: 2,
				Completed:  2,
			},
			Ensemble: EnsembleState{
				Modes: []EnsembleMode{
					{ID: "a", Status: string(ensemble.AssignmentDone)},
					{ID: "b", Status: string(ensemble.AssignmentError)},
				},
				Synthesis: EnsembleSynthesis{Status: "ready"},
			},
		}
		hints := buildEnsembleHints(output)
		if hints == nil {
			t.Fatal("expected non-nil hints")
		}
		found := false
		for _, w := range hints.Warnings {
			if w == "one or more modes reported errors; review pane output" {
				found = true
			}
		}
		if !found {
			t.Error("expected error warning in hints")
		}
	})

	t.Run("zero total returns nil when no content", func(t *testing.T) {
		t.Parallel()
		output := EnsembleOutput{
			Summary: EnsembleSummary{
				TotalModes: 0,
			},
			Ensemble: EnsembleState{
				Modes: []EnsembleMode{},
			},
		}
		hints := buildEnsembleHints(output)
		// With zero modes, still gets warnings, so hints may not be nil
		// but there should be no summary
		if hints != nil && hints.Summary != "" {
			t.Error("expected empty summary with zero modes")
		}
	})
}

func TestOverlayPopupInnerCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ntmBin  string
		session string
		cursor  int64
	}{
		{
			name:    "without cursor",
			ntmBin:  "/usr/local/bin/ntm",
			session: "proj",
		},
		{
			name:    "with cursor and shell quoting",
			ntmBin:  "/tmp/odd path/ntm's",
			session: "proj's main",
			cursor:  42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := "NTM_POPUP=1 " + tmux.ShellQuote(tt.ntmBin) + " dashboard --popup"
			if tt.cursor > 0 {
				want += fmt.Sprintf(" --attention-cursor %d", tt.cursor)
			}
			want += " " + tmux.ShellQuote(tt.session)

			if got := overlayPopupInnerCommand(tt.ntmBin, tt.session, tt.cursor); got != want {
				t.Fatalf("overlayPopupInnerCommand() = %q, want %q", got, want)
			}
		})
	}
}

func TestOverlayPopupArgs(t *testing.T) {
	t.Parallel()

	args := overlayPopupArgs("/usr/local/bin/ntm", "proj", 9)
	if len(args) != 7 {
		t.Fatalf("len(args) = %d, want 7", len(args))
	}
	if args[0] != "display-popup" || args[1] != "-E" {
		t.Fatalf("unexpected tmux popup prefix: %v", args[:2])
	}
	if args[2] != "-w" || args[3] != "95%" || args[4] != "-h" || args[5] != "95%" {
		t.Fatalf("unexpected popup sizing args: %v", args[2:6])
	}
	wantCmd := "NTM_POPUP=1 " + tmux.ShellQuote("/usr/local/bin/ntm") + " dashboard --popup --attention-cursor 9 " + tmux.ShellQuote("proj")
	if args[6] != wantCmd {
		t.Fatalf("args[6] = %q, want %q", args[6], wantCmd)
	}
}

func restoreOverlayTestDeps() func() {
	prevExecCommand := overlayExecCommand
	prevBinaryPath := overlayBinaryPath
	prevIsInstalled := overlayIsInstalled
	prevInTmux := overlayInTmux
	prevSessionExists := overlaySessionExists
	prevCurrentSession := overlayCurrentSession
	prevOSExecutable := overlayOSExecutable
	prevLaunchProbeDelay := overlayLaunchProbeDelay

	return func() {
		overlayExecCommand = prevExecCommand
		overlayBinaryPath = prevBinaryPath
		overlayIsInstalled = prevIsInstalled
		overlayInTmux = prevInTmux
		overlaySessionExists = prevSessionExists
		overlayCurrentSession = prevCurrentSession
		overlayOSExecutable = prevOSExecutable
		overlayLaunchProbeDelay = prevLaunchProbeDelay
	}
}

func decodeOverlayOutput(t *testing.T, out string) OverlayOutput {
	t.Helper()

	var resp OverlayOutput
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal output: %v\nraw: %s", err, out)
	}
	return resp
}

func TestPrintOverlayRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		opts     OverlayOptions
		wantCode string
		wantHint string
	}{
		{
			name:     "missing session outside tmux",
			opts:     OverlayOptions{},
			wantCode: ErrCodeInvalidFlag,
			wantHint: "Pass --overlay-session=<session> or run --robot-overlay inside the target tmux session",
		},
		{
			name:     "explicit session outside tmux",
			opts:     OverlayOptions{Session: "proj"},
			wantCode: ErrCodeInternalError,
			wantHint: "Run --robot-overlay from inside tmux so tmux can draw the popup",
		},
		{
			name:     "negative cursor",
			opts:     OverlayOptions{Session: "proj", Cursor: -1},
			wantCode: ErrCodeInvalidFlag,
			wantHint: "Use --overlay-cursor with a non-negative event cursor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := restoreOverlayTestDeps()
			defer restore()
			overlayIsInstalled = func() bool { return true }
			overlayInTmux = func() bool { return false }
			overlayCurrentSession = func() string { return "" }

			out, err := captureStdout(t, func() error {
				return PrintOverlay(tt.opts)
			})
			if err != nil {
				t.Fatalf("PrintOverlay returned error: %v", err)
			}

			resp := decodeOverlayOutput(t, out)
			if resp.Success {
				t.Fatalf("expected failure response, got success: %+v", resp)
			}
			if resp.ErrorCode != tt.wantCode {
				t.Fatalf("error_code = %q, want %q", resp.ErrorCode, tt.wantCode)
			}
			if resp.Hint != tt.wantHint {
				t.Fatalf("hint = %q, want %q", resp.Hint, tt.wantHint)
			}
		})
	}
}

func TestPrintOverlayDefaultsToCurrentSession(t *testing.T) {
	restore := restoreOverlayTestDeps()
	defer restore()

	var gotName string
	var gotArgs []string

	overlayIsInstalled = func() bool { return true }
	overlayInTmux = func() bool { return true }
	overlayCurrentSession = func() string { return "proj-current" }
	overlaySessionExists = func(session string) bool { return session == "proj-current" }
	overlayBinaryPath = func() string { return "/usr/bin/tmux" }
	overlayOSExecutable = func() (string, error) { return "/usr/local/bin/ntm", nil }
	overlayExecCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.Command("true")
	}

	out, err := captureStdout(t, func() error {
		return PrintOverlay(OverlayOptions{Cursor: 7})
	})
	if err != nil {
		t.Fatalf("PrintOverlay returned error: %v", err)
	}

	resp := decodeOverlayOutput(t, out)
	if !resp.Success {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if resp.Session != "proj-current" {
		t.Fatalf("session = %q, want %q", resp.Session, "proj-current")
	}
	if resp.Cursor != 7 {
		t.Fatalf("cursor = %d, want 7", resp.Cursor)
	}
	if !resp.Launched || !resp.Dismissed {
		t.Fatalf("expected launched+dismissed response, got %+v", resp)
	}
	if gotName != "/usr/bin/tmux" {
		t.Fatalf("command name = %q, want /usr/bin/tmux", gotName)
	}
	wantArgs := overlayPopupArgs("/usr/local/bin/ntm", "proj-current", 7)
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestPrintOverlayNoWaitReturnsLaunchStatus(t *testing.T) {
	restore := restoreOverlayTestDeps()
	defer restore()

	overlayIsInstalled = func() bool { return true }
	overlayInTmux = func() bool { return true }
	overlayCurrentSession = func() string { return "proj" }
	overlaySessionExists = func(session string) bool { return session == "proj" }
	overlayBinaryPath = func() string { return "/usr/bin/tmux" }
	overlayOSExecutable = func() (string, error) { return "/usr/local/bin/ntm", nil }
	overlayLaunchProbeDelay = 5 * time.Millisecond
	overlayExecCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sleep", "0.1")
	}

	out, err := captureStdout(t, func() error {
		return PrintOverlay(OverlayOptions{Session: "proj", Cursor: 11, NoWait: true})
	})
	if err != nil {
		t.Fatalf("PrintOverlay returned error: %v", err)
	}

	resp := decodeOverlayOutput(t, out)
	if !resp.Success {
		t.Fatalf("expected success response, got %+v", resp)
	}
	if !resp.Launched || resp.Dismissed {
		t.Fatalf("expected launched without dismissed, got %+v", resp)
	}
	if !resp.NoWait {
		t.Fatalf("no_wait = false, want true")
	}
	if resp.PID <= 0 {
		t.Fatalf("pid = %d, want > 0", resp.PID)
	}
	if resp.Cursor != 11 {
		t.Fatalf("cursor = %d, want 11", resp.Cursor)
	}
}

func TestPrintOverlayNoWaitReportsImmediateCommandFailure(t *testing.T) {
	restore := restoreOverlayTestDeps()
	defer restore()

	prevProbeDelay := overlayLaunchProbeDelay
	overlayLaunchProbeDelay = 20 * time.Millisecond
	defer func() {
		overlayLaunchProbeDelay = prevProbeDelay
	}()

	overlayIsInstalled = func() bool { return true }
	overlayInTmux = func() bool { return true }
	overlayCurrentSession = func() string { return "proj" }
	overlaySessionExists = func(session string) bool { return session == "proj" }
	overlayBinaryPath = func() string { return "/usr/bin/tmux" }
	overlayOSExecutable = func() (string, error) { return "/usr/local/bin/ntm", nil }
	overlayExecCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("false")
	}

	out, err := captureStdout(t, func() error {
		return PrintOverlay(OverlayOptions{Session: "proj", Cursor: 19, NoWait: true})
	})
	if err != nil {
		t.Fatalf("PrintOverlay returned error: %v", err)
	}

	resp := decodeOverlayOutput(t, out)
	if resp.Success {
		t.Fatalf("expected failure response, got %+v", resp)
	}
	if resp.ErrorCode != ErrCodeInternalError {
		t.Fatalf("error_code = %q, want %q", resp.ErrorCode, ErrCodeInternalError)
	}
	if resp.Launched {
		t.Fatalf("expected launched=false on immediate failure, got %+v", resp)
	}
	if resp.Cursor != 19 || !resp.NoWait {
		t.Fatalf("response lost cursor/no-wait state: %+v", resp)
	}
}

// =============================================================================
// yesNo tests
// =============================================================================

func TestYesNo(t *testing.T) {
	t.Parallel()
	if yesNo(true) != "yes" {
		t.Errorf("yesNo(true) = %q, want %q", yesNo(true), "yes")
	}
	if yesNo(false) != "no" {
		t.Errorf("yesNo(false) = %q, want %q", yesNo(false), "no")
	}
}

// =============================================================================
// escapeMarkdownCell tests
// =============================================================================

func TestEscapeMarkdownCell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty string", "", 0, ""},
		{"no escaping needed", "hello world", 0, "hello world"},
		{"pipe escaped", "foo|bar", 0, "foo\\|bar"},
		{"newline replaced", "foo\nbar", 0, "foo bar"},
		{"carriage return replaced", "foo\rbar", 0, "foo bar"},
		{"multiple pipes", "a|b|c", 0, "a\\|b\\|c"},
		{"mixed special chars", "a|b\nc\rd", 0, "a\\|b c d"},
		{"leading/trailing whitespace trimmed", "  hello  ", 0, "hello"},
		{"truncated to maxLen", "hello world this is long", 10, "hello w..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := escapeMarkdownCell(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("escapeMarkdownCell(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// =============================================================================
// dashboardCounts tests
// =============================================================================

func TestDashboardCounts(t *testing.T) {
	t.Parallel()

	t.Run("empty sessions", func(t *testing.T) {
		t.Parallel()
		total, user, counts := dashboardCounts(nil)
		if total != 0 {
			t.Errorf("total = %d, want 0", total)
		}
		if user != 0 {
			t.Errorf("user = %d, want 0", user)
		}
		if counts["claude"] != 0 || counts["codex"] != 0 {
			t.Errorf("expected zero counts, got %v", counts)
		}
	})

	t.Run("multiple sessions with mixed agents", func(t *testing.T) {
		t.Parallel()
		sessions := []SnapshotSession{
			{
				Name: "proj1",
				Agents: []SnapshotAgent{
					{Type: "claude"},
					{Type: "codex"},
					{Type: "user"},
				},
			},
			{
				Name: "proj2",
				Agents: []SnapshotAgent{
					{Type: "claude"},
					{Type: "gemini"},
				},
			},
		}
		total, user, counts := dashboardCounts(sessions)
		if total != 5 {
			t.Errorf("total = %d, want 5", total)
		}
		if user != 1 {
			t.Errorf("user = %d, want 1", user)
		}
		if counts["claude"] != 2 {
			t.Errorf("claude = %d, want 2", counts["claude"])
		}
		if counts["codex"] != 1 {
			t.Errorf("codex = %d, want 1", counts["codex"])
		}
		if counts["gemini"] != 1 {
			t.Errorf("gemini = %d, want 1", counts["gemini"])
		}
	})
}

// =============================================================================
// appendUnique tests
// =============================================================================

func TestAppendUnique(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		list  []string
		value string
		want  []string
	}{
		{"add to empty", nil, "a", []string{"a"}},
		{"add new value", []string{"a", "b"}, "c", []string{"a", "b", "c"}},
		{"skip duplicate", []string{"a", "b"}, "a", []string{"a", "b"}},
		{"skip duplicate middle", []string{"a", "b", "c"}, "b", []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := appendUnique(tt.list, tt.value)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("result[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// =============================================================================
// isUnknownJSONFlag tests
// =============================================================================

func TestIsUnknownJSONFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"empty string", "", false},
		{"no json mention", "something went wrong", false},
		{"json but no flag error", "json output enabled", false},
		{"unknown flag with json", "unknown flag: --json", true},
		{"flag provided but not defined with json", "flag provided but not defined: -json", true},
		{"case insensitive json", "Unknown flag: --JSON", true},
		{"case insensitive flag", "UNKNOWN FLAG: --json", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isUnknownJSONFlag(tt.stderr)
			if got != tt.want {
				t.Errorf("isUnknownJSONFlag(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// =============================================================================
// removeFlag tests
// =============================================================================

func TestRemoveFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		flag string
		want []string
	}{
		{"remove existing", []string{"--json", "--verbose"}, "--json", []string{"--verbose"}},
		{"remove absent", []string{"--verbose"}, "--json", []string{"--verbose"}},
		{"remove from empty", nil, "--json", []string{}},
		{"remove multiple occurrences", []string{"--json", "a", "--json"}, "--json", []string{"a"}},
		{"remove only exact match", []string{"--json-output", "--json"}, "--json", []string{"--json-output"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := removeFlag(tt.args, tt.flag)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got=%v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("result[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// =============================================================================
// resolveEnsembleBudget tests (partial - only empty preset path)
// =============================================================================

func TestResolveEnsembleBudget_EmptyPreset(t *testing.T) {
	t.Parallel()
	// With empty preset, should return default budget
	budget := resolveEnsembleBudget("")
	defaults := ensemble.DefaultBudgetConfig()
	if budget.MaxTotalTokens != defaults.MaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", budget.MaxTotalTokens, defaults.MaxTotalTokens)
	}
	if budget.MaxTokensPerMode != defaults.MaxTokensPerMode {
		t.Errorf("MaxTokensPerMode = %d, want %d", budget.MaxTokensPerMode, defaults.MaxTokensPerMode)
	}
}

func TestResolveEnsembleBudget_WhitespacePreset(t *testing.T) {
	t.Parallel()
	budget := resolveEnsembleBudget("   ")
	defaults := ensemble.DefaultBudgetConfig()
	if budget.MaxTotalTokens != defaults.MaxTotalTokens {
		t.Errorf("MaxTotalTokens = %d, want %d", budget.MaxTotalTokens, defaults.MaxTotalTokens)
	}
}

// =============================================================================
// snapshot attention summary tests
// =============================================================================

func TestBuildSnapshotAttentionSummary_UsesReplayWindowAndEventActions(t *testing.T) {
	t.Parallel()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       2,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	feed.Append(AttentionEvent{
		Category:      EventCategorySystem,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "older bootstrap event",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent resumed output",
	})
	expectedAction := NextAction{
		Action: "robot-tail",
		Args:   "--robot-tail=proj --panes=2 --lines=50",
		Reason: "Inspect the failing agent output",
	}
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityError,
		Summary:       "agent failed in proj pane 2",
		NextActions:   []NextAction{expectedAction},
	})

	summary := buildSnapshotAttentionSummary(feed)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.TotalEvents != 2 {
		t.Fatalf("TotalEvents = %d, want 2 (only replayable events)", summary.TotalEvents)
	}
	if _, ok := summary.ByCategoryCount[string(EventCategorySystem)]; ok {
		t.Fatalf("expected evicted category to be absent from replay-window summary, got %+v", summary.ByCategoryCount)
	}
	if summary.ActionRequiredCount != 1 {
		t.Fatalf("ActionRequiredCount = %d, want 1", summary.ActionRequiredCount)
	}
	if len(summary.TopItems) != 1 || summary.TopItems[0].Summary != "agent failed in proj pane 2" {
		t.Fatalf("TopItems = %+v, want single latest action-required item", summary.TopItems)
	}
	if len(summary.NextSteps) == 0 {
		t.Fatal("expected mechanical next steps")
	}
	if summary.NextSteps[0].Action != expectedAction.Action || summary.NextSteps[0].Args != expectedAction.Args {
		t.Fatalf("first next step = %+v, want event-provided action %+v", summary.NextSteps[0], expectedAction)
	}
	if !hasSnapshotNextAction(summary.NextSteps, "robot-events", "--robot-events --since=3 --limit=20") {
		t.Fatalf("expected follow-cursor next step in %+v", summary.NextSteps)
	}
}

func TestBuildSnapshotAttentionSummary_UsesDigestWhenOnlyInterestingEventsExist(t *testing.T) {
	t.Parallel()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       8,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent switched profiles",
	})

	summary := buildSnapshotAttentionSummary(feed)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.ActionRequiredCount != 0 {
		t.Fatalf("ActionRequiredCount = %d, want 0", summary.ActionRequiredCount)
	}
	if summary.InterestingCount != 1 {
		t.Fatalf("InterestingCount = %d, want 1", summary.InterestingCount)
	}
	if len(summary.TopItems) != 0 {
		t.Fatalf("TopItems = %+v, want none when no action-required events exist", summary.TopItems)
	}
	if !hasSnapshotNextAction(summary.NextSteps, "robot-digest", "--robot-digest") {
		t.Fatalf("expected digest handoff in %+v", summary.NextSteps)
	}
	if !hasSnapshotNextAction(summary.NextSteps, "robot-snapshot", "--robot-snapshot") {
		t.Fatalf("expected resync handoff in %+v", summary.NextSteps)
	}
}

func TestDashboardAttentionHeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary *SnapshotAttentionSummary
		want    string
	}{
		{
			name:    "nil summary means feed unavailable",
			summary: nil,
			want:    "feed unavailable",
		},
		{
			name:    "empty summary is clear",
			summary: &SnapshotAttentionSummary{TotalEvents: 0},
			want:    "clear",
		},
		{
			name: "action required only",
			summary: &SnapshotAttentionSummary{
				TotalEvents:         2,
				ActionRequiredCount: 2,
			},
			want: "2 action-required",
		},
		{
			name: "mixed action and interesting",
			summary: &SnapshotAttentionSummary{
				TotalEvents:         5,
				ActionRequiredCount: 2,
				InterestingCount:    3,
			},
			want: "2 action-required, 3 interesting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := dashboardAttentionHeadline(tt.summary); got != tt.want {
				t.Fatalf("dashboardAttentionHeadline(%+v) = %q, want %q", tt.summary, got, tt.want)
			}
		})
	}
}

func TestWriteAttentionSection_UnavailableAndClear(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary *SnapshotAttentionSummary
		want    string
	}{
		{
			name:    "nil summary",
			summary: nil,
			want:    "_Attention feed not available._\n",
		},
		{
			name:    "empty summary",
			summary: &SnapshotAttentionSummary{TotalEvents: 0},
			want:    "_No attention events. Feed is clear._\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var sb strings.Builder
			writeAttentionSection(&sb, tt.summary)
			if got := sb.String(); got != tt.want {
				t.Fatalf("writeAttentionSection() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteAttentionSection_SummaryTable verifies the summary table rendering.
func TestWriteAttentionSection_SummaryTable(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         10,
		ActionRequiredCount: 3,
		InterestingCount:    5,
	})

	got := sb.String()

	checks := []string{
		"| Key | Value |",
		"|---|---|",
		"| Total Events | 10 |",
		"| Action Required | 3 |",
		"| Interesting | 5 |",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Errorf("missing %q in output:\n%s", check, got)
		}
	}
}

// TestWriteAttentionSection_TopItems verifies top action items rendering.
func TestWriteAttentionSection_TopItems(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         5,
		ActionRequiredCount: 2,
		InterestingCount:    1,
		TopItems: []SnapshotAttentionItem{
			{Cursor: 100, Category: "agent", Severity: "error", Summary: "Agent crashed"},
			{Cursor: 101, Category: "session", Severity: "warning", Summary: "Session idle"},
		},
	})

	got := sb.String()

	checks := []string{
		"### Top Action Items",
		"| Cursor | Category | Severity | Summary |",
		"| 100 | agent | error | Agent crashed |",
		"| 101 | session | warning | Session idle |",
	}
	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Errorf("missing %q in output:\n%s", check, got)
		}
	}
}

// TestWriteAttentionSection_NextSteps verifies next steps rendering.
func TestWriteAttentionSection_NextSteps(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         3,
		ActionRequiredCount: 1,
		InterestingCount:    1,
		NextSteps: []NextAction{
			{Action: "robot-tail", Args: "--session proj", Reason: "Check agent output"},
			{Action: "robot-send", Args: "--pane 2", Reason: "Confirm prompt"},
		},
	})

	got := sb.String()

	if !strings.Contains(got, "### Suggested Next Steps") {
		t.Errorf("missing next steps header in output:\n%s", got)
	}
	if !strings.Contains(got, "ntm --session proj") {
		t.Errorf("missing first next step in output:\n%s", got)
	}
	if !strings.Contains(got, "Check agent output") {
		t.Errorf("missing reason for first step in output:\n%s", got)
	}
}

// TestWriteAttentionSection_UnsupportedSignals verifies unsupported signals rendering.
func TestWriteAttentionSection_UnsupportedSignals(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         1,
		ActionRequiredCount: 0,
		InterestingCount:    1,
		UnsupportedSignals:  []string{"network_latency", "memory_pressure"},
	})

	got := sb.String()

	if !strings.Contains(got, "### Unsupported Signals") {
		t.Errorf("missing unsupported signals header in output:\n%s", got)
	}
	if !strings.Contains(got, "network_latency, memory_pressure") {
		t.Errorf("missing signal list in output:\n%s", got)
	}
}

// TestWriteAttentionSection_MarkdownEscaping verifies pipe characters are escaped.
func TestWriteAttentionSection_MarkdownEscaping(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         1,
		ActionRequiredCount: 1,
		TopItems: []SnapshotAttentionItem{
			{Cursor: 1, Category: "test|cat", Severity: "warn", Summary: "text with | pipe"},
		},
	})

	got := sb.String()

	// Pipes in cell content should be escaped
	if strings.Contains(got, "test|cat") {
		t.Errorf("unescaped pipe in category:\n%s", got)
	}
	if !strings.Contains(got, "test\\|cat") {
		t.Errorf("expected escaped pipe in category:\n%s", got)
	}
}

// TestWriteAttentionSection_EmptySections verifies empty sections are not rendered.
func TestWriteAttentionSection_EmptySections(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeAttentionSection(&sb, &SnapshotAttentionSummary{
		TotalEvents:         5,
		ActionRequiredCount: 2,
		InterestingCount:    3,
		TopItems:            []SnapshotAttentionItem{},
		NextSteps:           []NextAction{},
		UnsupportedSignals:  []string{},
	})

	got := sb.String()

	// Empty sections should NOT appear
	if strings.Contains(got, "### Top Action Items") {
		t.Errorf("unexpected top action items section:\n%s", got)
	}
	if strings.Contains(got, "### Suggested Next Steps") {
		t.Errorf("unexpected next steps section:\n%s", got)
	}
	if strings.Contains(got, "### Unsupported Signals") {
		t.Errorf("unexpected unsupported signals section:\n%s", got)
	}
}

func TestBuildAttentionHintFromSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary *SnapshotAttentionSummary
		want    string
	}{
		{
			name:    "nil summary",
			summary: nil,
			want:    "clear",
		},
		{
			name:    "empty summary",
			summary: &SnapshotAttentionSummary{TotalEvents: 0},
			want:    "clear",
		},
		{
			name: "action only",
			summary: &SnapshotAttentionSummary{
				TotalEvents:         2,
				ActionRequiredCount: 2,
			},
			want: "2!action",
		},
		{
			name: "interesting only",
			summary: &SnapshotAttentionSummary{
				TotalEvents:      4,
				InterestingCount: 4,
			},
			want: "4?interest",
		},
		{
			name: "mixed summary",
			summary: &SnapshotAttentionSummary{
				TotalEvents:         7,
				ActionRequiredCount: 2,
				InterestingCount:    5,
			},
			want: "2!action 5?interest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := buildAttentionHintFromSummary(tt.summary); got != tt.want {
				t.Fatalf("buildAttentionHintFromSummary(%+v) = %q, want %q", tt.summary, got, tt.want)
			}
		})
	}
}

func TestPrintDashboardMarkdown_RendersAttentionOnce(t *testing.T) {
	output := DashboardOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Unix(1700000000, 0).UTC(),
		Fleet:         "ntm",
		System: SystemInfo{
			Version:   "test-version",
			GoVersion: "go1.test",
			OS:        "linux",
			Arch:      "amd64",
		},
		Summary: StatusSummary{},
		Attention: &SnapshotAttentionSummary{
			TotalEvents:         4,
			ActionRequiredCount: 2,
			InterestingCount:    1,
		},
	}

	rendered, err := captureStdout(t, func() error { return printDashboardMarkdown(output) })
	if err != nil {
		t.Fatalf("printDashboardMarkdown() error = %v", err)
	}
	if count := strings.Count(rendered, "## Attention\n"); count != 1 {
		t.Fatalf("expected one attention section, got %d:\n%s", count, rendered)
	}
	if count := strings.Count(rendered, "| Attention | 2 action-required, 1 interesting |\n"); count != 1 {
		t.Fatalf("expected one summary attention headline, got %d:\n%s", count, rendered)
	}
	if attn := strings.Index(rendered, "## Attention\n"); attn == -1 {
		t.Fatalf("dashboard markdown missing attention section:\n%s", rendered)
	} else if sessions := strings.Index(rendered, "## Sessions\n"); sessions == -1 || attn > sessions {
		t.Fatalf("attention section should appear before sessions:\n%s", rendered)
	}
}

func hasSnapshotNextAction(actions []NextAction, action, args string) bool {
	for _, candidate := range actions {
		if candidate.Action == action && candidate.Args == args {
			return true
		}
	}
	return false
}
