package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPaneResponseFromTMUXPreservesPhysicalIdentity(t *testing.T) {
	pane := tmux.Pane{
		ID:          "%42",
		WindowIndex: 3,
		Index:       1,
		Title:       "project__cod_2",
		Type:        tmux.AgentCodex,
		Variant:     "high",
		Active:      true,
		Width:       120,
		Height:      40,
		Command:     "codex",
	}
	response := paneResponseFromTMUX(pane)
	if response.PaneID != "%42" || response.PaneTarget != "3.1" || response.WindowIndex != 3 || response.Index != 1 {
		t.Fatalf("pane response identity = %+v", response)
	}
	if response.Title != pane.Title || response.Type != "codex" || response.Variant != pane.Variant ||
		!response.Active || response.Width != 120 || response.Height != 40 || response.Command != "codex" {
		t.Fatalf("pane response metadata = %+v", response)
	}
}

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

func TestResolveSessionWithOptionsRejectsInvalidExplicitSessionName(t *testing.T) {

	_, err := ResolveSessionWithOptions("../escape", nil, SessionResolveOptions{})
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveSessionWithOptionsContextRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveSessionWithOptionsContext(ctx, "valid-session", nil, SessionResolveOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveSessionWithOptionsContext() error = %v, want context.Canceled", err)
	}
}

func TestTruncateWithEllipsis_Util(t *testing.T) {

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
			got := truncateWithEllipsis(tc.input, tc.max)
			if got != tc.want {
				t.Errorf("truncateWithEllipsis(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
			}
		})
	}
}

func TestUniqueStrings_Util(t *testing.T) {

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
			got := escapeShellQuotes(tc.input)
			if got != tc.want {
				t.Errorf("escapeShellQuotes(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Verify uniqueStrings output is truly sorted
func TestUniqueStrings_SortOrder(t *testing.T) {

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

	tests := []struct {
		name     string
		editor   string
		wantCmd  string
		wantArgs []string
	}{
		{"simple editor", "vim", "vim", nil},
		{"editor with args", "code --wait", "code", []string{"--wait"}},
		{"editor with multiple args", "emacs -nw --no-init", "emacs", []string{"-nw", "--no-init"}},
		{"quoted executable path", `"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code" --wait`, "/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code", []string{"--wait"}},
		{"single quoted executable path", `'C:\Program Files\Neovim\bin\nvim.exe' --headless`, `C:\Program Files\Neovim\bin\nvim.exe`, []string{"--headless"}},
		{"escaped spaces", `my\ editor --flag`, "my editor", []string{"--flag"}},
		{"empty string", "", "", nil},
		{"whitespace only", "   ", "", nil},
		{"extra spaces", "  vim  --clean  ", "vim", []string{"--clean"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

func TestBuildEditorCommandWithFallback(t *testing.T) {
	t.Run("uses visual when editor empty", func(t *testing.T) {
		t.Setenv("EDITOR", "")
		t.Setenv("VISUAL", "cat -n")

		cmd, err := buildEditorCommandWithFallback("/tmp/demo.txt", "vi")
		if err != nil {
			t.Fatalf("buildEditorCommandWithFallback returned error: %v", err)
		}
		if got := filepath.Base(cmd.Path); got != "cat" {
			t.Fatalf("cmd.Path base = %q, want %q", got, "cat")
		}
		if len(cmd.Args) != 3 {
			t.Fatalf("len(cmd.Args) = %d, want 3 (%v)", len(cmd.Args), cmd.Args)
		}
		if cmd.Args[1] != "-n" {
			t.Fatalf("cmd.Args[1] = %q, want %q", cmd.Args[1], "-n")
		}
		if cmd.Args[2] != "/tmp/demo.txt" {
			t.Fatalf("cmd.Args[2] = %q, want %q", cmd.Args[2], "/tmp/demo.txt")
		}
	})

	t.Run("falls back on unsafe editor", func(t *testing.T) {
		t.Setenv("EDITOR", "vim;rm -rf /")
		t.Setenv("VISUAL", "")

		cmd, err := buildEditorCommandWithFallback("/tmp/demo.txt", "cat")
		if err != nil {
			t.Fatalf("buildEditorCommandWithFallback returned error: %v", err)
		}
		if got := filepath.Base(cmd.Path); got != "cat" {
			t.Fatalf("cmd.Path base = %q, want %q", got, "cat")
		}
		if len(cmd.Args) != 2 {
			t.Fatalf("len(cmd.Args) = %d, want 2 (%v)", len(cmd.Args), cmd.Args)
		}
		if cmd.Args[1] != "/tmp/demo.txt" {
			t.Fatalf("cmd.Args[1] = %q, want %q", cmd.Args[1], "/tmp/demo.txt")
		}
	})
}

// =============================================================================
// HasAnyTag
// =============================================================================

func TestHasAnyTag(t *testing.T) {

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
			got := SanitizeFilename(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilename_LongName(t *testing.T) {

	// Name longer than 50 chars should be truncated
	long := strings.Repeat("a", 100)
	got := SanitizeFilename(long)
	if len(got) > 50 {
		t.Errorf("SanitizeFilename truncated length = %d, want <= 50", len(got))
	}
}

// =============================================================================
// orderSessionsForSelection
// =============================================================================

func TestOrderSessionsForSelection(t *testing.T) {

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

// =============================================================================
// inferSessionFromCWD — label disambiguation (three-tier logic from bd-3cu02.8)
// =============================================================================

func TestInferSessionFromCWD_LabelDisambiguation(t *testing.T) {
	// Save and restore global state that inferSessionFromCWD reads.
	origCfg := cfg
	origRemote := tmux.DefaultClient.Remote
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		tmux.DefaultClient.Remote = origRemote
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	// Ensure we are not in "remote" mode.
	tmux.DefaultClient.Remote = ""

	// Create a temp directory tree: projectsBase/myproject/.
	// canonicalTempDir resolves macOS /var → /private/var so paths
	// returned by os.Getwd() after chdir match those built from this base.
	projectsBase := canonicalTempDir(t)
	projectDir := filepath.Join(projectsBase, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Point config at our temp projects base.
	cfg = &config.Config{ProjectsBase: projectsBase}

	// chdir into the project directory so inferSessionFromCWD can match.
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Run("single_match_returns_it", func(t *testing.T) {
		sessions := []tmux.Session{
			{Name: "myproject"},
			{Name: "other"},
		}
		got, reason := inferSessionFromCWD(sessions)
		if got != "myproject" {
			t.Errorf("got %q, want %q", got, "myproject")
		}
		if reason != "current directory" {
			t.Errorf("reason = %q, want %q", reason, "current directory")
		}
	})

	t.Run("single_labeled_match_returns_it", func(t *testing.T) {
		sessions := []tmux.Session{
			{Name: "myproject--frontend"},
			{Name: "other"},
		}
		got, reason := inferSessionFromCWD(sessions)
		if got != "myproject--frontend" {
			t.Errorf("got %q, want %q", got, "myproject--frontend")
		}
		if reason != "current directory" {
			t.Errorf("reason = %q, want %q", reason, "current directory")
		}
	})

	t.Run("tier1_unlabeled_preferred_over_labeled", func(t *testing.T) {
		// Multiple matches: unlabeled session should be preferred (tier 1).
		sessions := []tmux.Session{
			{Name: "myproject--frontend"},
			{Name: "myproject"},
			{Name: "myproject--backend"},
		}
		got, reason := inferSessionFromCWD(sessions)
		if got != "myproject" {
			t.Errorf("got %q, want %q (unlabeled should be preferred)", got, "myproject")
		}
		if !strings.Contains(reason, "base session preferred") {
			t.Errorf("reason = %q, want containing %q", reason, "base session preferred")
		}
	})

	t.Run("tier2_all_labeled_picks_first_alphabetically", func(t *testing.T) {
		// All labeled, no unlabeled base: pick first alphabetically (tier 2).
		sessions := []tmux.Session{
			{Name: "myproject--frontend"},
			{Name: "myproject--backend"},
			{Name: "myproject--api"},
		}
		got, reason := inferSessionFromCWD(sessions)
		if got != "myproject--api" {
			t.Errorf("got %q, want %q (first alphabetically)", got, "myproject--api")
		}
		if !strings.Contains(reason, "first labeled session") {
			t.Errorf("reason = %q, want containing %q", reason, "first labeled session")
		}
	})

	t.Run("no_matching_sessions", func(t *testing.T) {
		sessions := []tmux.Session{
			{Name: "other-project"},
			{Name: "something-else"},
		}
		got, _ := inferSessionFromCWD(sessions)
		if got != "" {
			t.Errorf("got %q, want empty (no match)", got)
		}
	})

	t.Run("remote_mode_skips_inference", func(t *testing.T) {
		tmux.DefaultClient.Remote = "user@host"
		defer func() { tmux.DefaultClient.Remote = "" }()

		sessions := []tmux.Session{
			{Name: "myproject"},
		}
		got, _ := inferSessionFromCWD(sessions)
		if got != "" {
			t.Errorf("got %q, want empty (remote mode should skip)", got)
		}
	})

	t.Run("empty_session_list", func(t *testing.T) {
		got, _ := inferSessionFromCWD(nil)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	// Test from a subdirectory of the project.
	subDir := filepath.Join(projectDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	t.Run("subdirectory_matches_project", func(t *testing.T) {
		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("chdir subdir: %v", err)
		}
		defer func() {
			if err := os.Chdir(projectDir); err != nil {
				t.Errorf("restore project directory: %v", err)
			}
		}()

		sessions := []tmux.Session{
			{Name: "myproject--frontend"},
			{Name: "myproject"},
		}
		got, reason := inferSessionFromCWD(sessions)
		if got != "myproject" {
			t.Errorf("got %q, want %q (unlabeled preferred from subdir)", got, "myproject")
		}
		if !strings.Contains(reason, "base session preferred") {
			t.Errorf("reason = %q, want containing %q", reason, "base session preferred")
		}
	})
}

func TestResolveProjectDirForSession_PrefersCWDForInferredSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	configProject := filepath.Join(projectsBase, "ntm")
	if err := os.MkdirAll(configProject, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectDirForSession("ntm", false)
	if got != cwdRepo {
		t.Errorf("resolveProjectDirForSession inferred = %q, want cwd repo %q", got, cwdRepo)
	}
}

func TestResolveProjectDirForSession_PrefersConfiguredDirForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	configProject := filepath.Join(projectsBase, "ntm")
	if err := os.MkdirAll(configProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configProject, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectDirForSession("ntm", true)
	if got != configProject {
		t.Errorf("resolveProjectDirForSession explicit = %q, want configured dir %q", got, configProject)
	}
}

func TestResolveProjectDirForSession_ExplicitFallsBackToUsableWorkspace(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	configProject := filepath.Join(projectsBase, "ntm")
	if err := os.MkdirAll(configProject, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectDirForSession("ntm", true)
	if got != cwdRepo {
		t.Errorf("resolveProjectDirForSession explicit fallback = %q, want workspace dir %q", got, cwdRepo)
	}
}

func TestResolveProjectDirForSession_PrefersSavedSessionAgentProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	configProject := filepath.Join(projectsBase, "ntm")
	if err := os.MkdirAll(configProject, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveSessionAgentForTest(t, "ntm", actualProject, "GreenCastle")

	got := resolveProjectDirForSession("ntm", true)
	if got != actualProject {
		t.Errorf("resolveProjectDirForSession saved project = %q, want %q", got, actualProject)
	}
}

func TestResolveProjectDirForSession_InvalidSessionReturnsEmpty(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	got := resolveProjectDirForSession("../escape", true)
	if got != "" {
		t.Fatalf("resolveProjectDirForSession invalid = %q, want empty", got)
	}
}

func TestResolveExplicitProjectDirForSession_RejectsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	_, err := resolveExplicitProjectDirWithoutLiveSession(t, "ntm")
	if err == nil {
		t.Fatal("expected missing session project error")
	}
	if !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("expected project root error, got %v", err)
	}
}

func TestResolveExplicitProjectDirForSession_UsesSavedSessionAgentProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveSessionAgentForTest(t, "ntm", actualProject, "GreenCastle")

	got, err := resolveExplicitProjectDirWithoutLiveSession(t, "ntm")
	if err != nil {
		t.Fatalf("resolveExplicitProjectDirForSession() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveExplicitProjectDirForSession() = %q, want %q", got, actualProject)
	}
}

func TestResolveCommandProjectDirForSession_ExplicitRejectsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := resolveCommandProjectDirForSession(t.Context(), "ntm-explicit-missing-project-test", false); got != "" {
		t.Fatalf("resolveCommandProjectDirForSession explicit = %q, want empty", got)
	}
}

func TestResolveCommandProjectDirForSession_InferredAllowsWorkspaceFallback(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	projectsBase := canonicalTempDir(t)
	cfg = &config.Config{ProjectsBase: projectsBase}

	cwdRepo := canonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwdRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwdRepo); err != nil {
		t.Fatal(err)
	}

	if got := resolveCommandProjectDirForSession(t.Context(), "ntm", true); got != cwdRepo {
		t.Fatalf("resolveCommandProjectDirForSession inferred = %q, want %q", got, cwdRepo)
	}
}

func TestResolveExplicitProjectDirForSession_IgnoresCWDMatchedSavedProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg = &config.Config{ProjectsBase: t.TempDir()}

	wrongProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wrongProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(wrongProject); err != nil {
		t.Fatal(err)
	}

	session := "ntm"
	wrongInfo := &agentmail.SessionAgentInfo{
		AgentName:    "WrongLake",
		ProjectKey:   wrongProject,
		RegisteredAt: time.Now().Add(-2 * time.Hour),
		LastActiveAt: time.Now().Add(-2 * time.Hour),
	}
	if err := agentmail.SaveSessionAgent(session, wrongProject, wrongInfo); err != nil {
		t.Fatalf("save wrong session agent: %v", err)
	}
	actualInfo := &agentmail.SessionAgentInfo{
		AgentName:    "RightLake",
		ProjectKey:   actualProject,
		RegisteredAt: time.Now().Add(-time.Hour),
		LastActiveAt: time.Now().Add(-time.Hour),
	}
	if err := agentmail.SaveSessionAgent(session, actualProject, actualInfo); err != nil {
		t.Fatalf("save actual session agent: %v", err)
	}

	got, err := resolveExplicitProjectDirWithoutLiveSession(t, session)
	if err != nil {
		t.Fatalf("resolveExplicitProjectDirForSession() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveExplicitProjectDirForSession() = %q, want %q", got, actualProject)
	}
}

func TestResolveExplicitProjectDirForSession_UsesLiveSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	origCfg := cfg
	origDir, _ := os.Getwd()
	t.Cleanup(func() {
		cfg = origCfg
		if err := os.Chdir(origDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	cfg = &config.Config{ProjectsBase: t.TempDir()}

	callerProject := t.TempDir()
	sessionProject := t.TempDir()
	for _, projectDir := range []string{callerProject, sessionProject} {
		if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
			t.Fatalf("create beads marker: %v", err)
		}
	}
	paneDir := filepath.Join(sessionProject, "internal", "cli")
	if err := os.MkdirAll(paneDir, 0o755); err != nil {
		t.Fatalf("create pane directory: %v", err)
	}
	if err := os.Chdir(callerProject); err != nil {
		t.Fatal(err)
	}

	panes := []tmux.Pane{{ID: "%11"}, {ID: "%12"}}
	got, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		"live-session-project-test",
		func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		func(_ context.Context, paneID string) (string, error) {
			if paneID == "%11" {
				return paneDir, nil
			}
			return sessionProject, nil
		},
	)
	if err != nil {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() error = %v", err)
	}
	if got != sessionProject {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() = %q, want live project %q instead of caller %q", got, sessionProject, callerProject)
	}
}

func TestResolveExplicitProjectDirForSession_RejectsMixedLiveProjects(t *testing.T) {
	firstProject := t.TempDir()
	secondProject := t.TempDir()
	for _, projectDir := range []string{firstProject, secondProject} {
		if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
	}

	panes := []tmux.Pane{{ID: "%21"}, {ID: "%22"}}
	_, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		"mixed-live-project-test",
		func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		func(_ context.Context, paneID string) (string, error) {
			if paneID == "%21" {
				return firstProject, nil
			}
			return secondProject, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "panes span multiple project roots") {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() error = %v, want mixed-root failure", err)
	}
}

func TestResolveExplicitProjectDirForSessionContext_PropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := resolveExplicitProjectDirForSessionContext(ctx, "canceled-live-project-test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolveExplicitProjectDirForSessionContext() error = %v, want context canceled", err)
	}
}

func TestResolveExplicitProjectDirForSessionContext_RequiresContext(t *testing.T) {
	_, err := resolveExplicitProjectDirForSessionContext(nil, "nil-context-live-project-test")
	if err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("resolveExplicitProjectDirForSessionContext(nil) error = %v, want required-context error", err)
	}
}

func TestResolveExplicitProjectDirForSessionContext_CancelsInFlightListPanes(t *testing.T) {
	tempDir := t.TempDir()
	marker := filepath.Join(tempDir, "list-panes-started")
	wrapper := filepath.Join(tempDir, "tmux-block-list-panes")
	script := `#!/bin/sh
if [ "${1:-}" = "list-panes" ]; then
  printf 'started\n' > "$NTM_TEST_LIST_PANES_STARTED"
  exec sleep 30
fi
exit 97
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write tmux wrapper: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", wrapper)
	t.Setenv("NTM_TEST_LIST_PANES_STARTED", marker)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := resolveExplicitProjectDirForSessionContext(ctx, "in-flight-live-project-test")
		result <- err
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat list-panes marker: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("list-panes call exited before wrapper marker: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for list-panes wrapper")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolveExplicitProjectDirForSessionContext(in-flight cancel) error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight list-panes did not join after cancellation")
	}
}

func TestResolveExplicitProjectDirForSession_PropagatesLivePaneListingFailure(t *testing.T) {
	wantErr := errors.New("tmux transport unavailable")
	_, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		"live-pane-list-failure",
		func(context.Context, string) ([]tmux.Pane, error) { return nil, wantErr },
		nil,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() error = %v, want %v", err, wantErr)
	}
}

func TestResolveExplicitProjectDirForSession_MissingLiveSessionUsesConfiguredProject(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	projectsBase := t.TempDir()
	session := "configured-missing-live-session"
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create configured project: %v", err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	got, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		session,
		func(context.Context, string) ([]tmux.Pane, error) {
			return nil, errors.New("tmux list-panes: exit status 1: can't find window: " + session)
		},
		nil,
	)
	if err != nil {
		t.Fatalf("resolve configured project after missing live session: %v", err)
	}
	if got != projectDir {
		t.Fatalf("resolved project = %q, want %q", got, projectDir)
	}
}

func resolveExplicitProjectDirWithoutLiveSession(t *testing.T, session string) (string, error) {
	t.Helper()
	return resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		session,
		func(context.Context, string) ([]tmux.Pane, error) { return nil, nil },
		nil,
	)
}

func TestNormalizeProjectScopedSessionName_UsesConfiguredProjectPrefix(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	projectsBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsBase, "myproject"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	got, err := normalizeProjectScopedSessionName(t.Context(), "mypro", true)
	if err != nil {
		t.Fatalf("normalizeProjectScopedSessionName() error = %v", err)
	}
	if got != "myproject" {
		t.Fatalf("normalizeProjectScopedSessionName() = %q, want %q", got, "myproject")
	}
}

func TestNormalizeExplicitLiveSessionNameRequiresContext(t *testing.T) {
	_, err := normalizeExplicitLiveSessionName(nil, "myproject", true)
	if err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("normalizeExplicitLiveSessionName(nil) error = %v, want required-context error", err)
	}
}

func TestNormalizeExplicitLiveSessionNameHonorsPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := normalizeExplicitLiveSessionName(ctx, "myproject", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("normalizeExplicitLiveSessionName(canceled) error = %v, want context.Canceled", err)
	}
}

func TestNormalizeExplicitLiveSessionNameCancelsInFlightListSessions(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "list-sessions-started")
	wrapper := filepath.Join(t.TempDir(), "tmux-block-list-sessions")
	script := "#!/bin/sh\nprintf 'started\\n' > " + tmux.ShellQuote(marker) + "\nexec sleep 30\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write tmux wrapper: %v", err)
	}
	t.Setenv("NTM_TMUX_BINARY", wrapper)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := normalizeExplicitLiveSessionName(ctx, "myproject", true)
		result <- err
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat list-sessions marker: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("list-sessions call exited before wrapper marker: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for list-sessions wrapper")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("normalizeExplicitLiveSessionName(in-flight cancel) error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight list-sessions did not join after cancellation")
	}
}

func TestNormalizeProjectScopedSessionName_PreservesLabelWithConfiguredProjectPrefix(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	projectsBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsBase, "myproject"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	got, err := normalizeProjectScopedSessionName(t.Context(), "mypro--frontend", true)
	if err != nil {
		t.Fatalf("normalizeProjectScopedSessionName() error = %v", err)
	}
	if got != "myproject--frontend" {
		t.Fatalf("normalizeProjectScopedSessionName() = %q, want %q", got, "myproject--frontend")
	}
}

func TestNormalizeProjectScopedSessionName_RejectsAmbiguousConfiguredProjectPrefix(t *testing.T) {
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	projectsBase := t.TempDir()
	for _, name := range []string{"myproject", "myproject2"} {
		if err := os.MkdirAll(filepath.Join(projectsBase, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg = &config.Config{ProjectsBase: projectsBase}

	_, err := normalizeProjectScopedSessionName(t.Context(), "mypro", true)
	if err == nil {
		t.Fatal("expected ambiguous configured project error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error, got %v", err)
	}
}

// ntm#311: `ntm add` failed project resolution when a retained dead pane
// (remain-on-exit) reported an empty current path. Dead panes must be skipped
// on the way through resolveExplicitProjectDirForSessionWithLookup, and an
// all-dead session must surface a deliberate no-live-panes reason instead of
// a multi-root conflict or the generic "getting project root failed".
func TestResolveExplicitProjectDirForSession_SkipsDeadPanes(t *testing.T) {
	isolateSessionAgentStorage(t)
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })
	cfg = &config.Config{ProjectsBase: t.TempDir()}

	sessionProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sessionProject, ".git"), 0o755); err != nil {
		t.Fatalf("create project marker: %v", err)
	}

	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2", Dead: true}}
	var asked []string
	got, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		"dead-pane-live-test",
		func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		func(_ context.Context, paneID string) (string, error) {
			asked = append(asked, paneID)
			if paneID == "%2" {
				return "", nil
			}
			return sessionProject, nil
		},
	)
	if err != nil {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() error = %v", err)
	}
	if got != sessionProject {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() = %q, want live project %q", got, sessionProject)
	}
	if len(asked) != 1 || asked[0] != "%1" {
		t.Fatalf("current-path lookups = %v, want only the live pane", asked)
	}
}

func TestResolveExplicitProjectDirForSession_LivePaneEmptyPathStillFails(t *testing.T) {
	isolateSessionAgentStorage(t)
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })
	cfg = &config.Config{ProjectsBase: t.TempDir()}

	panes := []tmux.Pane{{ID: "%1"}}
	_, err := resolveExplicitProjectDirForSessionWithLookup(
		t.Context(),
		"live-empty-path-test",
		func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		func(context.Context, string) (string, error) { return "", nil },
	)
	if err == nil || !strings.Contains(err.Error(), `current path "" is not absolute`) {
		t.Fatalf("resolveExplicitProjectDirForSessionWithLookup() error = %v, want not-absolute rejection for a live pane", err)
	}
}

func TestResolveExplicitProjectDirForSession_AllDeadPanes(t *testing.T) {
	isolateSessionAgentStorage(t)
	origCfg := cfg
	t.Cleanup(func() { cfg = origCfg })

	panes := []tmux.Pane{{ID: "%1", Dead: true}, {ID: "%2", Dead: true}}
	listPanes := func(context.Context, string) ([]tmux.Pane, error) { return panes, nil }
	paneCurrentPath := func(context.Context, string) (string, error) {
		t.Fatal("current-path lookup must not run for dead panes")
		return "", nil
	}

	// No persisted project metadata: the dead-pane reason is the error.
	cfg = &config.Config{ProjectsBase: t.TempDir()}
	_, err := resolveExplicitProjectDirForSessionWithLookup(t.Context(), "all-dead-test", listPanes, paneCurrentPath)
	if err == nil {
		t.Fatal("all-dead session with no saved project resolved a directory")
	}
	if !errors.Is(err, robot.ErrNoLivePanes) || !strings.Contains(err.Error(), "all 2 panes are dead") {
		t.Fatalf("all-dead error = %v, want ErrNoLivePanes naming the dead panes", err)
	}
	if strings.Contains(err.Error(), "multiple project roots") || strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("all-dead error must be deliberate, got %v", err)
	}

	// A validated configured session directory is an acceptable fallback,
	// exactly as it is when the live session cannot be listed at all.
	projectsBase := t.TempDir()
	cfg = &config.Config{ProjectsBase: projectsBase}
	configured := cfg.GetProjectDir("all-dead-test")
	if err := os.MkdirAll(filepath.Join(configured, ".git"), 0o755); err != nil {
		t.Fatalf("create configured project: %v", err)
	}
	got, err := resolveExplicitProjectDirForSessionWithLookup(t.Context(), "all-dead-test", listPanes, paneCurrentPath)
	if err != nil {
		t.Fatalf("all-dead session with configured project: %v", err)
	}
	if got != configured {
		t.Fatalf("all-dead fallback = %q, want configured session project %q", got, configured)
	}
}
