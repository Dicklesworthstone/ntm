package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestResolveExplicitSessionName(t *testing.T) {
	sessions := []tmux.Session{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "my_project"},
		{Name: "my_test"},
	}

	cases := []struct {
		name        string
		input       string
		allowPrefix bool
		sessions    []tmux.Session
		want        string
		wantReason  string
		wantErr     bool
		errContains []string
	}{
		{
			name:        "exact match",
			input:       "my_project",
			allowPrefix: true,
			sessions:    sessions,
			want:        "my_project",
			wantReason:  "exact match",
		},
		{
			name:        "unique prefix",
			input:       "alp",
			allowPrefix: true,
			sessions:    sessions,
			want:        "alpha",
			wantReason:  "prefix match",
		},
		{
			name:        "ambiguous prefix",
			input:       "my",
			allowPrefix: true,
			sessions:    sessions,
			wantErr:     true,
			errContains: []string{"matches multiple sessions", "my_project", "my_test"},
		},
		{
			name:        "prefix disabled",
			input:       "alp",
			allowPrefix: false,
			sessions:    sessions,
			wantErr:     true,
			errContains: []string{"not found", "available"},
		},
		{
			name:        "no match",
			input:       "zzz",
			allowPrefix: true,
			sessions:    sessions,
			wantErr:     true,
			errContains: []string{"not found", "available", "alpha", "beta"},
		},
		{
			name:        "no sessions",
			input:       "anything",
			allowPrefix: true,
			sessions:    nil,
			wantErr:     true,
			errContains: []string{"no tmux sessions running"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, reason, err := resolveExplicitSessionName(tc.input, tc.sessions, tc.allowPrefix)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				for _, substr := range tc.errContains {
					if !strings.Contains(err.Error(), substr) {
						t.Fatalf("expected error to contain %q, got %q", substr, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tc.want {
				t.Fatalf("resolved %q, want %q", resolved, tc.want)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestTruncateWithEllipsis_Util(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{"empty string", "", 10, ""},
		{"shorter than max", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 8, "hello..."},
		{"max zero", "hello", 0, ""},
		{"max negative", "hello", -1, ""},
		{"max 3 or less", "hello", 3, "hel"},
		{"max 1", "hello", 1, "h"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateWithEllipsis(tc.input, tc.max)
			if got != tc.want {
				t.Errorf("truncateWithEllipsis(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
			}
		})
	}
}

func TestUniqueStrings_Util(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"empty", []string{}, []string{}},
		{"nil", nil, []string{}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"with duplicates", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"whitespace trimmed", []string{" a ", " b ", " a "}, []string{"a", "b"}},
		{"empty strings filtered", []string{"a", "", "b", "  ", "c"}, []string{"a", "b", "c"}},
		{"sorted output", []string{"c", "a", "b"}, []string{"a", "b", "c"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := uniqueStrings(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("uniqueStrings(%v) len = %d, want %d", tc.input, len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("uniqueStrings(%v)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCountAgentStates_Util(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		assignments []ensemble.ModeAssignment
		wantReady   int
		wantPending int
		wantWorking int
	}{
		{
			name:        "empty",
			assignments: nil,
			wantReady:   0,
			wantPending: 0,
			wantWorking: 0,
		},
		{
			name: "mixed states",
			assignments: []ensemble.ModeAssignment{
				{Status: ensemble.AssignmentDone},
				{Status: ensemble.AssignmentDone},
				{Status: ensemble.AssignmentPending},
				{Status: ensemble.AssignmentInjecting},
				{Status: ensemble.AssignmentActive},
			},
			wantReady:   2,
			wantPending: 2,
			wantWorking: 1,
		},
		{
			name: "all done",
			assignments: []ensemble.ModeAssignment{
				{Status: ensemble.AssignmentDone},
				{Status: ensemble.AssignmentDone},
				{Status: ensemble.AssignmentDone},
			},
			wantReady:   3,
			wantPending: 0,
			wantWorking: 0,
		},
		{
			name: "error status not counted",
			assignments: []ensemble.ModeAssignment{
				{Status: ensemble.AssignmentError},
			},
			wantReady:   0,
			wantPending: 0,
			wantWorking: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := &ensemble.EnsembleSession{Assignments: tc.assignments}
			ready, pending, working := countAgentStates(state)
			if ready != tc.wantReady {
				t.Errorf("ready = %d, want %d", ready, tc.wantReady)
			}
			if pending != tc.wantPending {
				t.Errorf("pending = %d, want %d", pending, tc.wantPending)
			}
			if working != tc.wantWorking {
				t.Errorf("working = %d, want %d", working, tc.wantWorking)
			}
		})
	}
}

func TestEscapeShellQuotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no quotes", "hello world", "hello world"},
		{"single double quote", `say "hello"`, `say \"hello\"`},
		{"multiple quotes", `"a" and "b"`, `\"a\" and \"b\"`},
		{"empty string", "", ""},
		{"only quotes", `""`, `\"\"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := escapeShellQuotes(tc.input)
			if got != tc.want {
				t.Errorf("escapeShellQuotes(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Verify uniqueStrings output is truly sorted
func TestUniqueStrings_SortOrder(t *testing.T) {
	t.Parallel()

	input := []string{"zebra", "apple", "mango", "banana", "apple", "zebra"}
	got := uniqueStrings(input)

	if !sort.StringsAreSorted(got) {
		t.Errorf("uniqueStrings output not sorted: %v", got)
	}
}

// =============================================================================
// parseEditorCommand
// =============================================================================

func TestParseEditorCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		editor   string
		wantCmd  string
		wantArgs []string
	}{
		{"simple editor", "vim", "vim", nil},
		{"editor with args", "code --wait", "code", []string{"--wait"}},
		{"editor with multiple args", "emacs -nw --no-init", "emacs", []string{"-nw", "--no-init"}},
		{"empty string", "", "", nil},
		{"whitespace only", "   ", "", nil},
		{"extra spaces", "  vim  --clean  ", "vim", []string{"--clean"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd, args := parseEditorCommand(tc.editor)
			if cmd != tc.wantCmd {
				t.Errorf("parseEditorCommand(%q) cmd = %q, want %q", tc.editor, cmd, tc.wantCmd)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("parseEditorCommand(%q) args len = %d, want %d", tc.editor, len(args), len(tc.wantArgs))
			}
			for i, a := range args {
				if a != tc.wantArgs[i] {
					t.Errorf("parseEditorCommand(%q) args[%d] = %q, want %q", tc.editor, i, a, tc.wantArgs[i])
				}
			}
		})
	}
}

// =============================================================================
// HasAnyTag
// =============================================================================

func TestHasAnyTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		paneTags   []string
		filterTags []string
		want       bool
	}{
		{"exact match", []string{"auth", "api"}, []string{"auth"}, true},
		{"case insensitive", []string{"Auth", "API"}, []string{"auth"}, true},
		{"no match", []string{"auth", "api"}, []string{"db"}, false},
		{"empty pane tags", []string{}, []string{"auth"}, false},
		{"empty filter tags", []string{"auth"}, []string{}, false},
		{"both empty", []string{}, []string{}, false},
		{"nil pane tags", nil, []string{"auth"}, false},
		{"nil filter tags", []string{"auth"}, nil, false},
		{"multiple matches", []string{"auth", "api", "db"}, []string{"db", "auth"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := HasAnyTag(tc.paneTags, tc.filterTags)
			if got != tc.want {
				t.Errorf("HasAnyTag(%v, %v) = %v, want %v", tc.paneTags, tc.filterTags, got, tc.want)
			}
		})
	}
}

// =============================================================================
// SanitizeFilename
// =============================================================================

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "hello", "hello"},
		{"spaces to underscores", "hello world", "hello_world"},
		{"slashes to underscores", "path/to/file", "path_to_file"},
		{"special chars", "file:name?test", "file_name_test"},
		{"leading underscores trimmed", "_leading", "leading"},
		{"trailing underscores trimmed", "trailing_", "trailing"},
		{"pipes replaced", "a|b", "a_b"},
		{"stars replaced", "a*b", "a_b"},
		{"quotes replaced", `a"b`, "a_b"},
		{"angle brackets", "a<b>c", "a_b_c"},
		{"backslash", `a\b`, "a_b"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilename_LongName(t *testing.T) {
	t.Parallel()

	// Name longer than 50 chars should be truncated
	long := strings.Repeat("a", 100)
	got := SanitizeFilename(long)
	if len(got) > 50 {
		t.Errorf("SanitizeFilename truncated length = %d, want <= 50", len(got))
	}
}

// =============================================================================
// sessionNames
// =============================================================================

func TestSessionNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sessions []tmux.Session
		want     []string
	}{
		{"empty", nil, []string{}},
		{"single", []tmux.Session{{Name: "alpha"}}, []string{"alpha"}},
		{"sorted output", []tmux.Session{{Name: "beta"}, {Name: "alpha"}}, []string{"alpha", "beta"}},
		{"skips empty names", []tmux.Session{{Name: "alpha"}, {Name: ""}, {Name: "beta"}}, []string{"alpha", "beta"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sessionNames(tc.sessions)
			if len(got) != len(tc.want) {
				t.Fatalf("sessionNames() len = %d, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("sessionNames()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// =============================================================================
// orderSessionsForSelection
// =============================================================================

func TestOrderSessionsForSelection(t *testing.T) {
	t.Parallel()

	sessions := []tmux.Session{
		{Name: "beta", Attached: false},
		{Name: "alpha", Attached: true},
		{Name: "gamma", Attached: false},
	}

	ordered := orderSessionsForSelection(sessions)

	// Attached sessions should come first
	if !ordered[0].Attached {
		t.Errorf("expected attached session first, got %q (attached=%v)", ordered[0].Name, ordered[0].Attached)
	}
	if ordered[0].Name != "alpha" {
		t.Errorf("expected 'alpha' first (attached), got %q", ordered[0].Name)
	}

	// Non-attached should be sorted alphabetically
	if ordered[1].Name != "beta" || ordered[2].Name != "gamma" {
		t.Errorf("expected non-attached sorted: beta, gamma; got %q, %q", ordered[1].Name, ordered[2].Name)
	}

	// Original slice should not be mutated
	if sessions[0].Name != "beta" {
		t.Errorf("original slice was mutated: sessions[0].Name = %q, want 'beta'", sessions[0].Name)
	}
}
