package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/spf13/cobra"
)

var (
	sharedHermeticBeadsOnce sync.Once
	sharedHermeticBeadsDir  string
	sharedHermeticBeadsSkip string
	sharedHermeticBeadsErr  error
)

// hermeticBeadsRepo builds one shared read-only 2-issue git+br repository once
// per test binary so live installed bv tests (history, graph, burndown) run
// deterministically without repeating git+br init/create/commit per test.
func hermeticBeadsRepo(t *testing.T) string {
	t.Helper()
	sharedHermeticBeadsOnce.Do(func() {
		for _, tool := range []string{"bv", "br", "git"} {
			if _, err := exec.LookPath(tool); err != nil {
				sharedHermeticBeadsSkip = fmt.Sprintf("%s not installed", tool)
				return
			}
		}
		dir, err := os.MkdirTemp("", "ntm-work-beads-*")
		if err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		sharedHermeticBeadsDir = dir
		run := func(name string, args ...string) ([]byte, error) {
			cmd := exec.Command(name, args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
			}
			return out, nil
		}
		create := func(title string) (string, error) {
			raw, err := run("br", "create", "--title", title, "--type", "task", "--json")
			if err != nil {
				return "", err
			}
			var created struct {
				ID      string `json:"id"`
				Created struct {
					ID string `json:"id"`
				} `json:"created"`
			}
			if err := json.Unmarshal(raw, &created); err != nil {
				return "", fmt.Errorf("decode br create: %w", err)
			}
			if created.ID == "" {
				created.ID = created.Created.ID
			}
			return created.ID, nil
		}
		if _, err := run("git", "init", "-q"); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("br", "init", "--prefix", "zz"); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		first, err := create("first task")
		if err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		second, err := create("second task")
		if err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("br", "dep", "add", second, first); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("br", "close", first, "--reason", "done"); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("br", "sync", "--flush-only"); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		sprint, _ := json.Marshal(map[string]any{
			"id": "sprint-1", "name": "Sprint 1",
			"start_date": time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02T00:00:00Z"),
			"end_date":   time.Now().UTC().AddDate(0, 0, 4).Format("2006-01-02T00:00:00Z"),
			"bead_ids":   []string{first, second},
		})
		if err := os.WriteFile(filepath.Join(dir, ".beads", "sprints.jsonl"), append(sprint, '\n'), 0o644); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("git", "add", "-A"); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
		if _, err := run("git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "complete "+first+" and track "+second); err != nil {
			sharedHermeticBeadsErr = err
			return
		}
	})
	if sharedHermeticBeadsSkip != "" {
		t.Skip(sharedHermeticBeadsSkip)
	}
	if sharedHermeticBeadsErr != nil {
		t.Fatalf("build hermetic beads repo: %v", sharedHermeticBeadsErr)
	}
	return sharedHermeticBeadsDir
}

// TestWorkHistoryDecodesInstalledBV decodes bead history from the installed bv:
// histories are keyed by bead ID and events carry event_type.
func TestWorkHistoryDecodesInstalledBV(t *testing.T) {
	t.Parallel()
	root := hermeticBeadsRepo(t)
	out, err := tools.NewBVAdapter().GetHistory(context.Background(), root, "30d")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	var resp HistoryResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode bv history: %v", err)
	}
	if resp.Stats.TotalBeads == 0 || len(resp.Histories) == 0 {
		t.Fatalf("history stats=%+v histories=%d", resp.Stats, len(resp.Histories))
	}
	for id, bead := range resp.Histories {
		if bead.BeadID != id {
			t.Fatalf("history %q has bead_id %q", id, bead.BeadID)
		}
		for _, event := range bead.Events {
			if event.EventType == "" || event.Timestamp.IsZero() {
				t.Fatalf("history %q event missing type/time: %+v", id, event)
			}
		}
	}
}

// TestWorkGraphTextFromInstalledBV extracts DOT and Mermaid from bv's JSON
// envelope rather than printing the envelope.
func TestWorkGraphTextFromInstalledBV(t *testing.T) {
	t.Parallel()
	root := hermeticBeadsRepo(t)
	for format, prefix := range map[string]string{"dot": "digraph", "mermaid": "graph"} {
		out, err := tools.NewBVAdapter().GetGraph(context.Background(), root, tools.BVGraphOptions{Format: format})
		if err != nil {
			t.Fatalf("GetGraph(%s): %v", format, err)
		}
		text, err := bvGraphText(out, format)
		if err != nil || !strings.HasPrefix(strings.TrimSpace(text), prefix) {
			t.Fatalf("bvGraphText(%s) = %.80q, %v; want %s text", format, text, err, prefix)
		}
	}
}

// TestWorkBurndownDecodesInstalledBV builds a two-issue sprint with the
// installed br and git, and decodes bv's issue-based burndown for it.
func TestWorkBurndownDecodesInstalledBV(t *testing.T) {
	t.Parallel()
	dir := hermeticBeadsRepo(t)

	out, err := tools.NewBVAdapter().GetBurndown(context.Background(), dir, "sprint-1")
	if err != nil {
		t.Fatalf("GetBurndown: %v", err)
	}
	var resp BurndownResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode bv burndown: %v", err)
	}
	if resp.SprintName != "Sprint 1" || resp.TotalIssues != 2 || resp.CompletedIssues != 1 || resp.TotalDays == 0 {
		t.Fatalf("burndown = %+v", resp)
	}
}

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
