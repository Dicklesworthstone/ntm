package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/status"
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
			want: []string{"Label Health", "backend", "warning", "Velocity:", "Staleness:", "Blocked: 3"},
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
