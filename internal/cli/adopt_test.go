package cli

import (
	"strings"
	"testing"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
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

func TestValidateAdoptAssignments(t *testing.T) {
	tests := []struct {
		name        string
		assignments map[agentpkg.AgentType][]int
		wantErr     string
	}{
		{
			name: "valid mixed assignments",
			assignments: map[agentpkg.AgentType][]int{
				agentpkg.AgentTypeClaudeCode: {0, 1},
				agentpkg.AgentTypeCursor:     {2},
				agentpkg.AgentTypeUser:       {3},
			},
		},
		{
			name: "duplicate within same type",
			assignments: map[agentpkg.AgentType][]int{
				agentpkg.AgentTypeClaudeCode: {1, 1},
			},
			wantErr: "assigned multiple times",
		},
		{
			name: "duplicate across types",
			assignments: map[agentpkg.AgentType][]int{
				agentpkg.AgentTypeCodex:  {2},
				agentpkg.AgentTypeGemini: {2},
			},
			wantErr: "assigned multiple times",
		},
		{
			name: "negative index",
			assignments: map[agentpkg.AgentType][]int{
				agentpkg.AgentTypeOllama: {-1},
			},
			wantErr: "must be non-negative",
		},
		{
			name:        "no panes",
			assignments: map[agentpkg.AgentType][]int{},
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
