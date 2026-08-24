package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/spf13/cobra"
)

func TestWorkCmd(t *testing.T) {
	cmd := newWorkCmd()

	// Test that the command has expected subcommands
	expectedSubs := []string{"triage", "alerts", "search", "impact", "next", "queue-dry"}
	for _, sub := range expectedSubs {
		found := false
		for _, c := range cmd.Commands() {
			if c.Name() == sub {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected subcommand %q not found", sub)
		}
	}
}

func TestWorkTriageCmd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI - requires bv")
	}

	cmd := newWorkTriageCmd()
	if cmd.Use != "triage" {
		t.Errorf("expected Use to be 'triage', got %q", cmd.Use)
	}

	// Test help doesn't error
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("help command failed: %v", err)
	}
}

func TestWorkTriageCmdRejectsConflictingGroupedFlags(t *testing.T) {
	cmd := newWorkTriageCmd()
	cmd.SetArgs([]string{"--by-label", "--by-track"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("triage accepted --by-label and --by-track together")
	}
	if !strings.Contains(err.Error(), "if any flags in the group") {
		t.Fatalf("error = %q, want Cobra mutually-exclusive flag error", err)
	}
}

func TestWorkTriageCmdRejectsNegativeLimit(t *testing.T) {
	cmd := newWorkTriageCmd()
	cmd.SetArgs([]string{"--limit=-1"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("triage accepted a negative limit")
	}
	if !strings.Contains(err.Error(), "--limit must be zero or greater") {
		t.Fatalf("error = %q, want negative-limit validation error", err)
	}
}

func TestWorkAlertsCmd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI - requires bv")
	}

	cmd := newWorkAlertsCmd()
	if cmd.Use != "alerts" {
		t.Errorf("expected Use to be 'alerts', got %q", cmd.Use)
	}
}

func TestWorkSearchCmd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI - requires bv")
	}

	cmd := newWorkSearchCmd()
	if cmd.Use != "search <query>" {
		t.Errorf("expected Use to be 'search <query>', got %q", cmd.Use)
	}
}

func TestWorkImpactCmd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI - requires bv")
	}

	cmd := newWorkImpactCmd()
	if cmd.Use != "impact <paths...>" {
		t.Errorf("expected Use to be 'impact <paths...>', got %q", cmd.Use)
	}
}

func TestWorkNextCmd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI - requires bv")
	}

	cmd := newWorkNextCmd()
	if cmd.Use != "next" {
		t.Errorf("expected Use to be 'next', got %q", cmd.Use)
	}
}

func TestWorkForecastCmdRejectsExtraArguments(t *testing.T) {
	cmd := newWorkForecastCmd()
	cmd.SetArgs([]string{"ntm-123", "unexpected"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("forecast accepted an extra positional argument")
	}
	if !strings.Contains(err.Error(), "accepts at most 1 arg(s)") {
		t.Fatalf("error = %q, want Cobra maximum-argument error", err)
	}
}

func TestWorkQueueDryCmd(t *testing.T) {
	cmd := newWorkQueueDryCmd()
	if cmd.Use != "queue-dry" {
		t.Errorf("expected Use to be 'queue-dry', got %q", cmd.Use)
	}
}

func TestWorkCommandsRejectUnexpectedArguments(t *testing.T) {
	tests := []struct {
		name string
		new  func() *cobra.Command
		args []string
	}{
		{name: "commit-ready", new: newWorkCommitReadyCmd, args: []string{"unexpected"}},
		{name: "alerts", new: newWorkAlertsCmd, args: []string{"unexpected"}},
		{name: "next", new: newWorkNextCmd, args: []string{"unexpected"}},
		{name: "queue-dry mutation flags", new: newWorkQueueDryCmd, args: []string{"--ideate", "--create-beads", "--yes", "unexpected"}},
		{name: "history", new: newWorkHistoryCmd, args: []string{"unexpected"}},
		{name: "graph", new: newWorkGraphCmd, args: []string{"unexpected"}},
		{name: "label-health", new: newWorkLabelHealthCmd, args: []string{"unexpected"}},
		{name: "label-flow", new: newWorkLabelFlowCmd, args: []string{"unexpected"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.new()
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err == nil {
				t.Fatalf("%s accepted unexpected positional arguments", tt.name)
			}
		})
	}
}

func TestResolveTriageFormat(t *testing.T) {

	tests := []struct {
		input string
		want  string
	}{
		{"json", "json"},
		{"JSON", "json"},
		{"markdown", "markdown"},
		{"md", "markdown"},
		{"auto", "terminal"},
		{"", "terminal"},
		{"unknown", "terminal"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			if got := resolveTriageFormat(tc.input); got != tc.want {
				t.Errorf("resolveTriageFormat(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestWorkLabelCommandsSmoke(t *testing.T) {
	t.Setenv("PATH", filepath.Join(repoRoot(t), "testdata", "faketools")+":"+os.Getenv("PATH"))

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "label-health text output",
			args: []string{"work", "label-health"},
			// 9dc4ebaf realigned this with bv's live contract: the flat
			// Staleness float became Freshness.StaleCount, rendered as "Stale:".
			want: []string{"Label Health", "backend", "warning", "Velocity:", "Stale:", "Blocked: 3"},
		},
		{
			name: "label-flow text output",
			args: []string{"work", "label-flow"},
			want: []string{"Label Flow Analysis", "Bottleneck Labels:", "backend", "Top Dependencies:", "backend", "frontend", "(2)"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resetFlags()

			out, err := captureStdout(t, func() error {
				rootCmd.SetArgs(tc.args)
				return rootCmd.Execute()
			})
			if err != nil {
				t.Fatalf("Execute(%v) failed: %v", tc.args, err)
			}

			plain := status.StripANSI(out)
			for _, want := range tc.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("output missing %q\noutput:\n%s", want, plain)
				}
			}
		})
	}
}

// fakeToolsPATH prepends the fake bv tool to PATH so the work commands shell
// out to testdata/faketools/bv instead of a real bv.
func fakeToolsPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", filepath.Join(repoRoot(t), "testdata", "faketools")+":"+os.Getenv("PATH"))
}

func TestWorkHistoryDegradationSurfacesOnStdout(t *testing.T) {
	fakeToolsPATH(t)

	tests := []struct {
		name    string
		mode    string
		wantOut []string
	}{
		{
			name:    "non-zero exit with stderr reaches stdout",
			mode:    "robot_error",
			wantOut: []string{"Warning:", "robot mode failed"},
		},
		{
			name:    "version below gate refuses names version and mode",
			mode:    "old_version_refuses",
			wantOut: []string{"Warning:", "0.15.0", "--robot-history"},
		},
		{
			name:    "malformed json names the tool",
			mode:    "malformed",
			wantOut: []string{"Warning:", "bv"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKE_TOOL_MODE", tt.mode)
			resetFlags()

			out, err := captureStdout(t, func() error {
				return runWorkHistory()
			})
			if err == nil {
				t.Fatalf("runWorkHistory() = nil error, want non-zero exit")
			}

			plain := status.StripANSI(out)
			for _, want := range tt.wantOut {
				if !strings.Contains(plain, want) {
					t.Errorf("stdout missing %q\nstdout:\n%s", want, plain)
				}
			}
		})
	}
}

func TestWorkHistoryNoWarningWhenBVAnswers(t *testing.T) {
	fakeToolsPATH(t)

	tests := []struct {
		name string
		mode string
	}{
		{name: "version above gate", mode: "normal"},
		{name: "version below gate but answers", mode: "old_version_answers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKE_TOOL_MODE", tt.mode)
			resetFlags()

			out, err := captureStdout(t, func() error {
				return runWorkHistory()
			})
			if err != nil {
				t.Fatalf("runWorkHistory() error = %v, want nil", err)
			}

			plain := status.StripANSI(out)
			if strings.Contains(plain, "Warning:") {
				t.Errorf("stdout contains unexpected warning:\n%s", plain)
			}
			if !strings.Contains(plain, "Bead History") {
				t.Errorf("stdout missing history output:\n%s", plain)
			}
		})
	}
}

func TestWorkHistoryZeroCountsSourceNamed(t *testing.T) {
	fakeToolsPATH(t)
	t.Setenv("FAKE_TOOL_MODE", "zero_counts")
	resetFlags()

	out, err := captureStdout(t, func() error {
		return runWorkHistory()
	})
	if err != nil {
		t.Fatalf("runWorkHistory() error = %v, want nil", err)
	}

	plain := status.StripANSI(out)
	if !strings.Contains(plain, "Beads with Commits: 0") {
		t.Errorf("stdout missing zero beads_with_commits:\n%s", plain)
	}
	if !strings.Contains(plain, "(bv)") {
		t.Errorf("stdout missing source name (bv):\n%s", plain)
	}
}

func TestWorkBurndownNoSprintSurfacesOnStdout(t *testing.T) {
	fakeToolsPATH(t)
	t.Setenv("FAKE_TOOL_MODE", "no_sprint")
	resetFlags()

	out, err := captureStdout(t, func() error {
		return runWorkBurndown("current")
	})
	if err == nil {
		t.Fatal("runWorkBurndown() = nil error, want non-zero exit")
	}

	plain := status.StripANSI(out)
	if !strings.Contains(plain, "No active sprint found") {
		t.Errorf("stdout missing bv stderr text:\n%s", plain)
	}
}

func TestWorkBurndownRequiresSprintArgument(t *testing.T) {
	cmd := newWorkBurndownCmd()
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("burndown accepted no sprint argument")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg(s)") {
		t.Fatalf("error = %q, want ExactArgs(1) message", err)
	}
}

func TestWorkHistoryAndBurndownStdoutNonEmptyOnFailure(t *testing.T) {
	fakeToolsPATH(t)

	historyModes := []string{"robot_error", "old_version_refuses", "malformed"}
	for _, mode := range historyModes {
		t.Run("history/"+mode, func(t *testing.T) {
			t.Setenv("FAKE_TOOL_MODE", mode)
			resetFlags()

			out, err := captureStdout(t, func() error {
				return runWorkHistory()
			})
			if err == nil {
				t.Fatalf("runWorkHistory() = nil error in mode %q", mode)
			}
			if strings.TrimSpace(status.StripANSI(out)) == "" {
				t.Fatalf("runWorkHistory() stdout empty in mode %q", mode)
			}
		})
	}

	burndownModes := []string{"robot_error", "old_version_refuses", "malformed", "no_sprint"}
	for _, mode := range burndownModes {
		t.Run("burndown/"+mode, func(t *testing.T) {
			t.Setenv("FAKE_TOOL_MODE", mode)
			resetFlags()

			out, err := captureStdout(t, func() error {
				return runWorkBurndown("current")
			})
			if err == nil {
				t.Fatalf("runWorkBurndown() = nil error in mode %q", mode)
			}
			if strings.TrimSpace(status.StripANSI(out)) == "" {
				t.Fatalf("runWorkBurndown() stdout empty in mode %q", mode)
			}
		})
	}
}

func TestWorkHistoryJSONCarriesDegradation(t *testing.T) {
	fakeToolsPATH(t)
	t.Setenv("FAKE_TOOL_MODE", "robot_error")

	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	out, err := captureStdout(t, func() error {
		return runWorkHistory()
	})
	if err == nil {
		t.Fatal("runWorkHistory() = nil error, want non-zero exit")
	}

	var env struct {
		Degradation struct {
			Tool             string `json:"tool"`
			InstalledVersion string `json:"installed_version"`
			RequiredVersion  string `json:"required_version"`
			Complete         bool   `json:"complete"`
			Stderr           string `json:"stderr"`
		} `json:"degradation"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout:\n%s", err, out)
	}
	if env.Degradation.Tool != "bv" {
		t.Errorf("degradation.tool = %q, want bv", env.Degradation.Tool)
	}
	if env.Degradation.Complete {
		t.Error("degradation.complete = true, want false")
	}
	if !strings.Contains(env.Degradation.Stderr, "robot mode failed") {
		t.Errorf("degradation.stderr = %q, want contains robot mode failed", env.Degradation.Stderr)
	}
}

func TestWorkHistoryJSONCompleteCarriesData(t *testing.T) {
	fakeToolsPATH(t)
	t.Setenv("FAKE_TOOL_MODE", "normal")

	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	out, err := captureStdout(t, func() error {
		return runWorkHistory()
	})
	if err != nil {
		t.Fatalf("runWorkHistory() error = %v, want nil", err)
	}

	var env struct {
		Degradation struct {
			Tool     string `json:"tool"`
			Complete bool   `json:"complete"`
		} `json:"degradation"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout:\n%s", err, out)
	}
	if !env.Degradation.Complete {
		t.Error("degradation.complete = false, want true")
	}
	if len(env.Data) == 0 {
		t.Error("data field missing on complete answer")
	}
}
