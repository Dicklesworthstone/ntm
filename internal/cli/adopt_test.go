package cli

import (
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestParsePaneList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []int
	}{
		{name: "empty string", input: "", expected: nil},
		{name: "single value", input: "0", expected: []int{0}},
		{name: "multiple values", input: "0,1,2", expected: []int{0, 1, 2}},
		{name: "with spaces", input: "0, 1, 2", expected: []int{0, 1, 2}},
		{name: "range", input: "0-3", expected: []int{0, 1, 2, 3}},
		{name: "mixed range and individual", input: "0-2,5,7-9", expected: []int{0, 1, 2, 5, 7, 8, 9}},
		{name: "single value range", input: "5-5", expected: []int{5}},
		{name: "invalid range (reversed)", input: "5-3", expected: nil},
		{name: "trailing comma", input: "0,1,", expected: []int{0, 1}},
		{name: "leading comma", input: ",0,1", expected: []int{0, 1}},
		{name: "double comma", input: "0,,1", expected: []int{0, 1}},
		{name: "non-numeric ignored", input: "0,abc,2", expected: []int{0, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePaneList(tt.input)

			if tt.expected == nil && result == nil {
				return
			}
			if tt.expected == nil && len(result) == 0 {
				return
			}
			if len(result) == 0 && tt.expected == nil {
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("parsePaneList(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}

			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("parsePaneList(%q)[%d] = %d, want %d", tt.input, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestAdoptedAgentCountsTotal(t *testing.T) {
	tests := []struct {
		name     string
		counts   AdoptedAgentCounts
		expected int
	}{
		{name: "all zeros", counts: AdoptedAgentCounts{}, expected: 0},
		{name: "single type", counts: AdoptedAgentCounts{Cursor: 2}, expected: 2},
		{name: "mixed", counts: AdoptedAgentCounts{CC: 3, Cod: 2, Gmi: 1, Aider: 1, User: 1}, expected: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.counts.Total(); got != tt.expected {
				t.Errorf("Total() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestAdoptedAgentCountsSummary(t *testing.T) {
	counts := AdoptedAgentCounts{Cod: 2, Cursor: 1, User: 1}
	if got, want := counts.Summary(), "cod:2, cursor:1, user:1"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
}

func bare(indices ...int) []paneSpec {
	out := make([]paneSpec, 0, len(indices))
	for _, i := range indices {
		out = append(out, paneSpec{Window: paneWindowUnspecified, Pane: i})
	}
	return out
}

func TestValidateAdoptAssignments(t *testing.T) {
	tests := []struct {
		name        string
		assignments map[agentpkg.AgentType][]paneSpec
		wantErr     string
	}{
		{
			name: "valid mixed assignments",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeClaudeCode: bare(0, 1),
				agentpkg.AgentTypeCursor:     bare(2),
				agentpkg.AgentTypeUser:       bare(3),
			},
		},
		{
			name: "valid window.pane assignments",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeClaudeCode: {{Window: 1, Pane: 0}, {Window: 2, Pane: 0}},
				agentpkg.AgentTypeCodex:      {{Window: 3, Pane: 0}},
			},
		},
		{
			name: "duplicate within same type",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeClaudeCode: bare(1, 1),
			},
			wantErr: "assigned multiple times",
		},
		{
			name: "duplicate window.pane within same type",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeClaudeCode: {{Window: 1, Pane: 0}, {Window: 1, Pane: 0}},
			},
			wantErr: "assigned multiple times",
		},
		{
			name: "duplicate across types",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeCodex:  bare(2),
				agentpkg.AgentTypeGemini: bare(2),
			},
			wantErr: "assigned multiple times",
		},
		{
			name: "negative index",
			assignments: map[agentpkg.AgentType][]paneSpec{
				agentpkg.AgentTypeOllama: bare(-1),
			},
			wantErr: "must be non-negative",
		},
		{
			name:        "no panes",
			assignments: map[agentpkg.AgentType][]paneSpec{},
			wantErr:     "use one or more of --cc, --cod, --gmi, --cursor, --windsurf, --aider, --oc, --ollama, or --user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAdoptAssignments(tt.assignments)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAdoptAssignments() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAdoptAssignments() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateAdoptAssignments() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNewAdoptCmdSupportsAllAgentFlags(t *testing.T) {
	cmd := newAdoptCmd()
	for _, spec := range supportedAdoptTypes {
		if flag := cmd.Flags().Lookup(spec.Flag); flag == nil {
			t.Fatalf("expected flag --%s to be registered", spec.Flag)
		}
	}
}

func specsEqual(a, b []paneSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParsePaneSpecs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []paneSpec
		wantErr string
	}{
		{name: "empty string", input: "", want: nil},
		{name: "whitespace only", input: "   ", want: nil},
		{name: "single bare index", input: "0", want: bare(0)},
		{name: "multiple bare indices", input: "0,1,2", want: bare(0, 1, 2)},
		{name: "bare indices with spaces", input: "0, 1, 2", want: bare(0, 1, 2)},
		{name: "range", input: "0-3", want: bare(0, 1, 2, 3)},
		{name: "mixed range and individual", input: "0-2,5", want: bare(0, 1, 2, 5)},
		{
			name:  "window.pane single",
			input: "2.1",
			want:  []paneSpec{{Window: 2, Pane: 1}},
		},
		{
			name:  "window.pane multiple",
			input: "2.0,3.0",
			want:  []paneSpec{{Window: 2, Pane: 0}, {Window: 3, Pane: 0}},
		},
		{
			name:  "mixed bare and window.pane",
			input: "0,1.0,2",
			want:  []paneSpec{{Window: paneWindowUnspecified, Pane: 0}, {Window: 1, Pane: 0}, {Window: paneWindowUnspecified, Pane: 2}},
		},
		{name: "trailing comma", input: "0,1,", want: bare(0, 1)},
		{name: "leading comma", input: ",0,1", want: bare(0, 1)},
		{name: "double comma", input: "0,,1", want: bare(0, 1)},
		// Loud-failure cases (the legacy parser silently dropped these).
		{name: "non-numeric bare", input: "0,abc,2", wantErr: "invalid pane index"},
		{name: "negative bare", input: "-5", wantErr: "non-negative"},
		{name: "reversed range", input: "5-3", wantErr: "start must be <= end"},
		{name: "bad window.pane", input: "2.x", wantErr: "must be integers"},
		{name: "negative window.pane", input: "-1.0", wantErr: "non-negative"},
		{name: "triple-dotted", input: "1.2.3", wantErr: "expected window.pane"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePaneSpecs(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parsePaneSpecs(%q) = nil error, want error containing %q", tt.input, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parsePaneSpecs(%q) error = %q, want substring %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePaneSpecs(%q) unexpected error: %v", tt.input, err)
			}
			if !specsEqual(got, tt.want) {
				t.Fatalf("parsePaneSpecs(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// buildPaneIndexes mirrors the index construction in runAdopt so resolution can
// be exercised without a live tmux server.
func buildPaneIndexes(panes []tmux.Pane) (map[paneSpec]*tmux.Pane, map[int][]*tmux.Pane) {
	byWinPane := make(map[paneSpec]*tmux.Pane, len(panes))
	byBareIndex := make(map[int][]*tmux.Pane)
	for i := range panes {
		p := &panes[i]
		byWinPane[paneSpec{Window: p.WindowIndex, Pane: p.Index}] = p
		byBareIndex[p.Index] = append(byBareIndex[p.Index], p)
	}
	return byWinPane, byBareIndex
}

// singleWindowPanes models the classic layout: one window, several panes.
func singleWindowPanes() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", WindowIndex: 0, Index: 0, Title: "shell"},
		{ID: "%1", WindowIndex: 0, Index: 1, Title: "claude"},
		{ID: "%2", WindowIndex: 0, Index: 2, Title: "codex"},
	}
}

// windowPerAgentPanes models the #170 layout: N windows, each with a single
// pane at index 0 (so every pane shares the same window-local index).
func windowPerAgentPanes() []tmux.Pane {
	return []tmux.Pane{
		{ID: "%0", WindowIndex: 0, Index: 0, Title: "agent0"},
		{ID: "%1", WindowIndex: 1, Index: 0, Title: "agent1"},
		{ID: "%2", WindowIndex: 2, Index: 0, Title: "agent2"},
	}
}

func TestResolveAdoptPane_SingleWindowUnchanged(t *testing.T) {
	panes := singleWindowPanes()
	byWinPane, byBareIndex := buildPaneIndexes(panes)

	// Bare indices remain unambiguous in a single-window session.
	for _, tc := range []struct {
		idx    int
		wantID string
	}{{0, "%0"}, {1, "%1"}, {2, "%2"}} {
		got, err := resolveAdoptPane(paneSpec{Window: paneWindowUnspecified, Pane: tc.idx}, byWinPane, byBareIndex)
		if err != nil {
			t.Fatalf("resolveAdoptPane bare %d: unexpected error %v", tc.idx, err)
		}
		if got.ID != tc.wantID {
			t.Fatalf("resolveAdoptPane bare %d = %s, want %s", tc.idx, got.ID, tc.wantID)
		}
	}

	// Missing index fails clearly.
	if _, err := resolveAdoptPane(paneSpec{Window: paneWindowUnspecified, Pane: 9}, byWinPane, byBareIndex); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error for bare index 9, got %v", err)
	}
}

func TestResolveAdoptPane_MultiWindowFailsLoudOnAmbiguousBareIndex(t *testing.T) {
	panes := windowPerAgentPanes()
	byWinPane, byBareIndex := buildPaneIndexes(panes)

	// The correctness bug: bare index 0 exists in 3 windows. It must NOT
	// silently resolve to one pane — it must error and point at window.pane.
	_, err := resolveAdoptPane(paneSpec{Window: paneWindowUnspecified, Pane: 0}, byWinPane, byBareIndex)
	if err == nil {
		t.Fatal("resolveAdoptPane(bare 0) succeeded; expected ambiguity error (silent drop is the #170 bug)")
	}
	for _, want := range []string{"ambiguous", "0.0", "1.0", "2.0", "window.pane"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguity error %q missing %q", err.Error(), want)
		}
	}
}

func TestResolveAdoptPane_MultiWindowResolvesEachWindowPane(t *testing.T) {
	panes := windowPerAgentPanes()
	byWinPane, byBareIndex := buildPaneIndexes(panes)

	for _, tc := range []struct {
		win, pane int
		wantID    string
	}{{0, 0, "%0"}, {1, 0, "%1"}, {2, 0, "%2"}} {
		got, err := resolveAdoptPane(paneSpec{Window: tc.win, Pane: tc.pane}, byWinPane, byBareIndex)
		if err != nil {
			t.Fatalf("resolveAdoptPane %d.%d: unexpected error %v", tc.win, tc.pane, err)
		}
		if got.ID != tc.wantID {
			t.Fatalf("resolveAdoptPane %d.%d = %s, want %s", tc.win, tc.pane, got.ID, tc.wantID)
		}
	}

	// A window.pane address with no matching pane fails clearly.
	if _, err := resolveAdoptPane(paneSpec{Window: 5, Pane: 0}, byWinPane, byBareIndex); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error for 5.0, got %v", err)
	}
}

func TestByWindowAssignments(t *testing.T) {
	t.Run("window per agent maps each window", func(t *testing.T) {
		specs, err := byWindowAssignments(windowPerAgentPanes())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []paneSpec{{Window: 0, Pane: 0}, {Window: 1, Pane: 0}, {Window: 2, Pane: 0}}
		if !specsEqual(specs, want) {
			t.Fatalf("byWindowAssignments = %v, want %v", specs, want)
		}
	})

	t.Run("rejects multi-pane window", func(t *testing.T) {
		// Window 0 holds two panes: "the sole pane" is undefined.
		panes := []tmux.Pane{
			{ID: "%0", WindowIndex: 0, Index: 0},
			{ID: "%1", WindowIndex: 0, Index: 1},
			{ID: "%2", WindowIndex: 1, Index: 0},
		}
		if _, err := byWindowAssignments(panes); err == nil || !strings.Contains(err.Error(), "exactly one pane per window") {
			t.Fatalf("expected multi-pane-window rejection, got %v", err)
		}
	})

	t.Run("no panes", func(t *testing.T) {
		if _, err := byWindowAssignments(nil); err == nil {
			t.Fatal("expected error for empty session")
		}
	})
}

func TestWinPaneList(t *testing.T) {
	panes := windowPerAgentPanes()
	ptrs := []*tmux.Pane{&panes[0], &panes[1], &panes[2]}
	if got, want := winPaneList(ptrs), "0.0,1.0,2.0"; got != want {
		t.Fatalf("winPaneList = %q, want %q", got, want)
	}
}
