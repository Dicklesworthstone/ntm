package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
)

// =============================================================================
// assign.go helpers (not tested in assign_test.go)
// =============================================================================

func TestAssignmentAgentName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   string
		agentType string
		paneIndex int
		want      string
	}{
		{"normal", "dev-session", "claude", 0, "dev-session_claude_0"},
		{"codex pane 3", "prod", "codex", 3, "prod_codex_3"},
		{"empty session", "", "claude", 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := assignmentAgentName(tc.session, tc.agentType, tc.paneIndex)
			if got != tc.want {
				t.Errorf("assignmentAgentName(%q, %q, %d) = %q; want %q",
					tc.session, tc.agentType, tc.paneIndex, got, tc.want)
			}
		})
	}
}

func TestCalculateMatchConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		bead      bv.BeadPreview
		strategy  string
		wantMin   float64
		wantMax   float64
	}{
		{
			name:      "claude analysis task",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Analyze the codebase for issues"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "codex feature task",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Implement user authentication feature"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "gemini docs task",
			agentType: "gemini",
			bead:      bv.BeadPreview{Title: "Update documentation for API"},
			strategy:  "balanced",
			wantMin:   0.85,
			wantMax:   1.0,
		},
		{
			name:      "unknown agent generic task",
			agentType: "unknown",
			bead:      bv.BeadPreview{Title: "Do some work"},
			strategy:  "balanced",
			wantMin:   0.6,
			wantMax:   0.8,
		},
		{
			name:      "speed strategy raises confidence",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Generic task"},
			strategy:  "speed",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "dependency strategy with P0",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Fix critical bug", Priority: "P0"},
			strategy:  "dependency",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "dependency strategy with P1",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Fix high-priority bug", Priority: "P1"},
			strategy:  "dependency",
			wantMin:   0.75,
			wantMax:   0.95,
		},
		{
			name:      "codex bug task",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Fix broken login bug"},
			strategy:  "balanced",
			wantMin:   0.75,
			wantMax:   0.85,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := calculateMatchConfidence(tc.agentType, tc.bead, tc.strategy)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("calculateMatchConfidence(%q, %q, %q) = %f; want [%f, %f]",
					tc.agentType, tc.bead.Title, tc.strategy, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestBuildReasoning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		bead      bv.BeadPreview
		strategy  string
		wantSub   string
	}{
		{
			name:      "claude refactor",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Refactor the auth module"},
			strategy:  "balanced",
			wantSub:   "Claude excels",
		},
		{
			name:      "codex implement",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Implement new feature"},
			strategy:  "balanced",
			wantSub:   "Codex excels",
		},
		{
			name:      "gemini docs",
			agentType: "gemini",
			bead:      bv.BeadPreview{Title: "Update documentation"},
			strategy:  "balanced",
			wantSub:   "Gemini excels",
		},
		{
			name:      "P0 priority",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P0"},
			strategy:  "balanced",
			wantSub:   "critical priority",
		},
		{
			name:      "P1 priority",
			agentType: "codex",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P1"},
			strategy:  "balanced",
			wantSub:   "high priority",
		},
		{
			name:      "speed strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "speed",
			wantSub:   "optimizing for speed",
		},
		{
			name:      "quality strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "quality",
			wantSub:   "optimizing for quality",
		},
		{
			name:      "dependency strategy",
			agentType: "claude",
			bead:      bv.BeadPreview{Title: "Something"},
			strategy:  "dependency",
			wantSub:   "prioritizing unblocks",
		},
		{
			name:      "no special match",
			agentType: "unknown",
			bead:      bv.BeadPreview{Title: "Something", Priority: "P3"},
			strategy:  "",
			wantSub:   "available agent matched",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildReasoning(tc.agentType, tc.bead, tc.strategy)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("buildReasoning(%q, %q, %q) = %q; want substring %q",
					tc.agentType, tc.bead.Title, tc.strategy, got, tc.wantSub)
			}
		})
	}
}

// =============================================================================
// send.go helpers (normalizeCommandLine not tested elsewhere)
// =============================================================================

func TestNormalizeCommandLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"$ ls -la", "ls -la"},
		{"> git status", "git status"},
		{"# echo hello", "echo hello"},
		{"  $ npm install  ", "npm install"},
		{"plain command", "plain command"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeCommandLine(tc.input)
			if got != tc.want {
				t.Errorf("normalizeCommandLine(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// =============================================================================
// spawn.go helpers
// =============================================================================

func TestResolveSpawnAssignAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		agent                    string
		ccOnly, codOnly, gmiOnly bool
		want                     string
	}{
		{"explicit agent", "Claude", false, false, false, "claude"},
		{"cc only flag", "", true, false, false, "claude"},
		{"cod only flag", "", false, true, false, "codex"},
		{"gmi only flag", "", false, false, true, "gemini"},
		{"no agent no flags", "", false, false, false, ""},
		{"agent takes precedence", "codex", true, false, false, "codex"},
		{"whitespace agent", "  ", false, false, false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveSpawnAssignAgentType(tc.agent, tc.ccOnly, tc.codOnly, tc.gmiOnly)
			if got != tc.want {
				t.Errorf("resolveSpawnAssignAgentType(%q, %v, %v, %v) = %q; want %q",
					tc.agent, tc.ccOnly, tc.codOnly, tc.gmiOnly, got, tc.want)
			}
		})
	}
}

// =============================================================================
// pipeline.go helpers
// =============================================================================

func TestParseDurationCLI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"days", "7d", 7 * 24 * time.Hour, false},
		{"single day", "1d", 24 * time.Hour, false},
		{"weeks", "2w", 14 * 24 * time.Hour, false},
		{"hours fallback", "24h", 24 * time.Hour, false},
		{"minutes fallback", "30m", 30 * time.Minute, false},
		{"empty", "", 0, true},
		{"whitespace", "  ", 0, true},
		{"invalid day", "abcd", 0, true},
		{"invalid week", "abcw", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseDuration(%q) want error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuration(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseDuration(%q) = %v; want %v", tc.input, got, tc.want)
			}
		})
	}
}
