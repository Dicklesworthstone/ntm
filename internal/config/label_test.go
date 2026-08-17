package config

import (
	"strings"
	"testing"
)

func TestParseSessionLabel(t *testing.T) {
	tests := []struct {
		input     string
		wantBase  string
		wantLabel string
	}{
		{"myproject", "myproject", ""},
		{"myproject--frontend", "myproject", "frontend"},
		{"my-project--frontend", "my-project", "frontend"},
		{"foo--bar--baz", "foo--bar", "baz"}, // collision rule: split on LAST "--"
		{"proj--my-label", "proj", "my-label"},
		{"--frontend", "", "frontend"},     // degenerate: empty base
		{"myproject--", "myproject", ""},   // degenerate: empty label
		{"a--b", "a", "b"},                 // minimal
		{"abc", "abc", ""},                 // no separator
		{"a-b-c--d-e-f", "a-b-c", "d-e-f"}, // dashes everywhere
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotBase, gotLabel := ParseSessionLabel(tt.input)
			if gotBase != tt.wantBase {
				t.Errorf("ParseSessionLabel(%q) base = %q, want %q", tt.input, gotBase, tt.wantBase)
			}
			if gotLabel != tt.wantLabel {
				t.Errorf("ParseSessionLabel(%q) label = %q, want %q", tt.input, gotLabel, tt.wantLabel)
			}
		})
	}
}

func TestFormatSessionName(t *testing.T) {
	tests := []struct {
		base  string
		label string
		want  string
	}{
		{"myproject", "", "myproject"},
		{"myproject", "frontend", "myproject--frontend"},
		{"my-project", "backend", "my-project--backend"},
		{"a", "b", "a--b"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatSessionName(tt.base, tt.label)
			if got != tt.want {
				t.Errorf("FormatSessionName(%q, %q) = %q, want %q", tt.base, tt.label, got, tt.want)
			}
		})
	}
}

func TestFormatSessionName_RoundTrip(t *testing.T) {
	// FormatSessionName(ParseSessionLabel(x)) == x for valid labeled names
	inputs := []string{
		"myproject",
		"myproject--frontend",
		"my-project--backend",
		"a--b",
	}
	for _, input := range inputs {
		base, label := ParseSessionLabel(input)
		got := FormatSessionName(base, label)
		if got != input {
			t.Errorf("round-trip failed: input=%q, base=%q, label=%q, got=%q", input, base, label, got)
		}
	}
}

func TestHasLabel(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"myproject", false},
		{"my-project", false},
		{"myproject--frontend", true},
		{"a--b", true},
		{"foo--bar--baz", true},
		{"--frontend", true}, // degenerate but has --
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := HasLabel(tt.input)
			if got != tt.want {
				t.Errorf("HasLabel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSessionBase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"myproject", "myproject"},
		{"myproject--frontend", "myproject"},
		{"my-project--frontend", "my-project"},
		{"foo--bar--baz", "foo--bar"}, // collision rule: LAST "--" separates
		{"a--b", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SessionBase(tt.input)
			if got != tt.want {
				t.Errorf("SessionBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateLabel(t *testing.T) {
	valid := []string{
		"frontend",
		"backend",
		"bugfix-123",
		"my_label",
		"a",
		"A1b2C3",
		"test-run-1",
	}
	for _, label := range valid {
		t.Run("valid_"+label, func(t *testing.T) {
			if err := ValidateLabel(label); err != nil {
				t.Errorf("ValidateLabel(%q) unexpected error: %v", label, err)
			}
		})
	}

	invalid := []struct {
		label       string
		errContains string
	}{
		{"", "empty"},
		{strings.Repeat("a", 51), "50 characters"},
		{"my--label", "separator"},
		{"-bad", "alphanumeric"},
		{"_bad", "alphanumeric"},
		{"bad!", "alphanumeric"},
		{"bad label", "alphanumeric"},
	}
	for _, tt := range invalid {
		name := tt.label
		if name == "" {
			name = "empty"
		}
		if len(name) > 20 {
			name = name[:20] + "..."
		}
		t.Run("invalid_"+name, func(t *testing.T) {
			err := ValidateLabel(tt.label)
			if err == nil {
				t.Errorf("ValidateLabel(%q) expected error containing %q, got nil", tt.label, tt.errContains)
				return
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("ValidateLabel(%q) error = %q, want containing %q", tt.label, err.Error(), tt.errContains)
			}
		})
	}
}

// TestParseSessionLabel_EmptyInput ensures empty string is handled gracefully.
func TestParseSessionLabel_EmptyInput(t *testing.T) {
	base, label := ParseSessionLabel("")
	if base != "" {
		t.Errorf("ParseSessionLabel(%q) base = %q, want %q", "", base, "")
	}
	if label != "" {
		t.Errorf("ParseSessionLabel(%q) label = %q, want %q", "", label, "")
	}
}

// TestFormatSessionName_RoundTrip_Extended verifies round-trip for more cases,
// including multiple separators, and documents that degenerate inputs do NOT round-trip.
func TestFormatSessionName_RoundTrip_Extended(t *testing.T) {
	// These should all round-trip: FormatSessionName(ParseSessionLabel(x)) == x
	roundTrips := []string{
		"simple",
		"a--b",
		"my-project--backend",
		"foo--bar--baz",          // multi-separator: base="foo--bar", label="baz" (LAST-split rule)
		"x--y-z",                 // label with single dash
		"proj--label_underscore", // label with underscore
	}
	for _, input := range roundTrips {
		t.Run("roundtrip/"+input, func(t *testing.T) {
			base, label := ParseSessionLabel(input)
			got := FormatSessionName(base, label)
			if got != input {
				t.Errorf("round-trip failed: input=%q, base=%q, label=%q, got=%q",
					input, base, label, got)
			}
		})
	}

	// Degenerate cases that do NOT round-trip (documenting expected behavior)
	nonRoundTrips := []struct {
		input    string
		expected string // what FormatSessionName(Parse(x)) actually returns
	}{
		// "--frontend" -> base="", label="frontend" -> Format("","frontend") = "--frontend"
		// Actually this DOES round-trip because Format("","frontend") = "" + "--" + "frontend" = "--frontend"
		{"--frontend", "--frontend"},
		// "myproject--" -> base="myproject", label="" -> Format("myproject","") = "myproject"
		// This does NOT round-trip.
		{"myproject--", "myproject"},
	}
	for _, tt := range nonRoundTrips {
		t.Run("degenerate/"+tt.input, func(t *testing.T) {
			base, label := ParseSessionLabel(tt.input)
			got := FormatSessionName(base, label)
			if got != tt.expected {
				t.Errorf("degenerate: input=%q, base=%q, label=%q, got=%q, want=%q",
					tt.input, base, label, got, tt.expected)
			}
		})
	}
}

// TestHasLabel_Extended adds edge cases not covered by the main table.
func TestHasLabel_Extended(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},           // empty string
		{"myproject--", true}, // degenerate: trailing separator
		{"---", true},         // triple dash has "--" inside
		{"a-b", false},        // single dash only
	}
	for _, tt := range tests {
		name := tt.input
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got := HasLabel(tt.input)
			if got != tt.want {
				t.Errorf("HasLabel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestSessionBase_Extended adds degenerate and edge cases.
func TestSessionBase_Extended(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},                     // empty string
		{"--frontend", ""},           // degenerate: empty base
		{"myproject--", "myproject"}, // degenerate: trailing separator
		{"---", "-"},                 // triple dash: LAST "--" at index 1
	}
	for _, tt := range tests {
		name := tt.input
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got := SessionBase(tt.input)
			if got != tt.want {
				t.Errorf("SessionBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestValidateLabel_Extended adds boundary and additional invalid-pattern tests.
func TestValidateLabel_Extended(t *testing.T) {
	valid := []string{
		strings.Repeat("a", 50), // exactly 50 chars: boundary
		"a-",                    // trailing dash is valid per regex
		"a_",                    // trailing underscore is valid
		"Z",                     // single uppercase
		"9",                     // single digit
		"abc-def_ghi",           // mixed separators
	}
	for _, label := range valid {
		name := label
		if len(name) > 20 {
			name = name[:20] + "..."
		}
		t.Run("valid_ext/"+name, func(t *testing.T) {
			if err := ValidateLabel(label); err != nil {
				t.Errorf("ValidateLabel(%q) unexpected error: %v", label, err)
			}
		})
	}

	invalid := []struct {
		label       string
		errContains string
	}{
		{"my.label", "alphanumeric"},               // dot not allowed
		{"foo/bar", "alphanumeric"},                // slash not allowed
		{"hello\tworld", "alphanumeric"},           // tab not allowed
		{".hidden", "alphanumeric"},                // starts with dot
		{"foo--bar", "separator"},                  // double-dash in middle
		{strings.Repeat("b", 51), "50 characters"}, // 51 chars
		{"bad@label", "alphanumeric"},              // at sign
		{"a b", "alphanumeric"},                    // internal space
	}
	for _, tt := range invalid {
		name := tt.label
		if len(name) > 20 {
			name = name[:20] + "..."
		}
		// Sanitize the test name for subtests (avoid slashes, etc.)
		name = strings.ReplaceAll(name, "/", "_slash_")
		name = strings.ReplaceAll(name, "\t", "_tab_")
		t.Run("invalid_ext/"+name, func(t *testing.T) {
			err := ValidateLabel(tt.label)
			if err == nil {
				t.Errorf("ValidateLabel(%q) expected error containing %q, got nil", tt.label, tt.errContains)
				return
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("ValidateLabel(%q) error = %q, want containing %q", tt.label, err.Error(), tt.errContains)
			}
		})
	}
}

func TestValidateProjectName(t *testing.T) {
	valid := []string{
		"myproject",
		"my-project",
		"my_project",
		"a",
		"project123",
		"foo-bar-baz",
	}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := ValidateProjectName(name); err != nil {
				t.Errorf("ValidateProjectName(%q) unexpected error: %v", name, err)
			}
		})
	}

	invalid := []struct {
		name        string
		errContains string
	}{
		{"my--project", "reserved"},
		{"--frontend", "reserved"},
		{"project--", "reserved"},
		{"a--b--c", "reserved"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			err := ValidateProjectName(tt.name)
			if err == nil {
				t.Errorf("ValidateProjectName(%q) expected error containing %q, got nil", tt.name, tt.errContains)
				return
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("ValidateProjectName(%q) error = %q, want containing %q", tt.name, err.Error(), tt.errContains)
			}
		})
	}
}

func TestSessionLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"myproject", ""},
		{"myproject--frontend", "frontend"},
		{"my-project--backend", "backend"},
		{"foo--bar--baz", "baz"}, // collision rule: LAST "--" separates
		{"a--b", "b"},
		{"simple", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SessionLabel(tt.input)
			if got != tt.want {
				t.Errorf("SessionLabel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestGetProjectDir_WithLabel_TableDriven provides table-driven coverage for label stripping
// in GetProjectDir, complementing the inline assertions below.
func TestGetProjectDir_WithLabel_TableDriven(t *testing.T) {
	c := &Config{ProjectsBase: "/srv/projects"}

	tests := []struct {
		session string
		want    string
	}{
		{"alpha", "/srv/projects/alpha"},
		{"alpha--frontend", "/srv/projects/alpha"},
		{"alpha--backend", "/srv/projects/alpha"},
		{"beta", "/srv/projects/beta"},
		{"beta--v2", "/srv/projects/beta"},
		{"my-app--feature-x", "/srv/projects/my-app"},
		{"my-app", "/srv/projects/my-app"},
	}
	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			got := c.GetProjectDir(tt.session)
			if got != tt.want {
				t.Errorf("GetProjectDir(%q) = %q, want %q", tt.session, got, tt.want)
			}
		})
	}

	// Verify that labeled variants of the same project all resolve identically.
	variants := []string{"myproject", "myproject--a", "myproject--z", "myproject--foo-bar"}
	first := c.GetProjectDir(variants[0])
	for _, v := range variants[1:] {
		if got := c.GetProjectDir(v); got != first {
			t.Errorf("GetProjectDir(%q) = %q, want same as %q (%q)", v, got, variants[0], first)
		}
	}
}

// TestSessionLabel_DelimiterCollision pins the WS0-G6 collision rule (bead
// bd-ws1-truth-safety-l5ddi.7): a base name that itself contains "--" must
// round-trip make -> extract exactly, which forces the LAST-"--" split.
func TestSessionLabel_DelimiterCollision(t *testing.T) {
	base := "my--project--fix" // pathological base with TWO internal separators
	label := "frontend"

	name := FormatSessionName(base, label)
	if want := "my--project--fix--frontend"; name != want {
		t.Fatalf("FormatSessionName(%q, %q) = %q, want %q", base, label, name, want)
	}
	gotBase, gotLabel := ParseSessionLabel(name)
	if gotBase != base || gotLabel != label {
		t.Errorf("ParseSessionLabel(%q) = (%q, %q), want (%q, %q)", name, gotBase, gotLabel, base, label)
	}

	// Simpler collision fixture from the bead: my--project + fix
	gotBase, gotLabel = ParseSessionLabel(FormatSessionName("my--project", "fix"))
	if gotBase != "my--project" || gotLabel != "fix" {
		t.Errorf("collision round-trip = (%q, %q), want (%q, %q)", gotBase, gotLabel, "my--project", "fix")
	}

	// Unlabeled and labeled round-trips through the same canonical pair.
	for _, tc := range []struct{ base, label string }{
		{"myproject", ""},
		{"myproject", "frontend"},
		{"my-project", "fix-1"},
	} {
		b, l := ParseSessionLabel(FormatSessionName(tc.base, tc.label))
		if b != tc.base || l != tc.label {
			t.Errorf("round-trip (%q, %q) = (%q, %q)", tc.base, tc.label, b, l)
		}
	}
}

func TestPaneDisplayLabel(t *testing.T) {
	tests := []struct {
		session string
		title   string
		index   int
		want    string
	}{
		{"myproject", "myproject__cc_1", 2, "cc_1"},        // canonical make -> extract
		{"myproject", "  myproject__cod_1  ", 0, "cod_1"},  // whitespace trimmed
		{"myproject", "custom title", 1, "custom title"},   // non-canonical title passes through
		{"myproject", "", 3, "pane 3"},                     // empty title falls back
		{"myproject", "myproject__", 7, "pane 7"},          // empty label falls back
		{"other", "myproject__cc_1", 4, "myproject__cc_1"}, // prefix only stripped for own session
	}
	for _, tt := range tests {
		if got := PaneDisplayLabel(tt.session, tt.title, tt.index); got != tt.want {
			t.Errorf("PaneDisplayLabel(%q, %q, %d) = %q, want %q", tt.session, tt.title, tt.index, got, tt.want)
		}
	}

	// Round-trip: maker-shaped title (session + PaneTitleSeparator + label) -> PaneDisplayLabel.
	if got := PaneDisplayLabel("s", "s"+PaneTitleSeparator+"agent_2", 0); got != "agent_2" {
		t.Errorf("pane-title round-trip = %q, want %q", got, "agent_2")
	}
}

func TestGetProjectDir_WithLabel(t *testing.T) {
	cfg := &Config{ProjectsBase: "/home/user/projects"}

	// Unlabeled: unchanged behavior
	got := cfg.GetProjectDir("myproject")
	want := "/home/user/projects/myproject"
	if got != want {
		t.Errorf("GetProjectDir(%q) = %q, want %q", "myproject", got, want)
	}

	// Labeled: label stripped, returns base project dir
	got = cfg.GetProjectDir("myproject--frontend")
	if got != want {
		t.Errorf("GetProjectDir(%q) = %q, want %q", "myproject--frontend", got, want)
	}

	got = cfg.GetProjectDir("myproject--backend")
	if got != want {
		t.Errorf("GetProjectDir(%q) = %q, want %q", "myproject--backend", got, want)
	}

	// Multiple sessions resolve to SAME directory
	dir1 := cfg.GetProjectDir("myproject")
	dir2 := cfg.GetProjectDir("myproject--frontend")
	dir3 := cfg.GetProjectDir("myproject--backend")
	if dir1 != dir2 || dir2 != dir3 {
		t.Errorf("labeled sessions should resolve to same dir: %q, %q, %q", dir1, dir2, dir3)
	}

	// Different projects still resolve to different dirs
	dirA := cfg.GetProjectDir("project-a")
	dirB := cfg.GetProjectDir("project-b")
	if dirA == dirB {
		t.Errorf("different projects should resolve to different dirs: %q, %q", dirA, dirB)
	}

	// Dashes in project name preserved
	got = cfg.GetProjectDir("my-project--label")
	want = "/home/user/projects/my-project"
	if got != want {
		t.Errorf("GetProjectDir(%q) = %q, want %q", "my-project--label", got, want)
	}
}

// TestUserPaneTitle pins the canonical user-pane title maker (H8,
// bd-ws7-docs-ux-truth-tqh3l.8): {session}__user_0, round-tripping through
// PaneDisplayLabel like agent-pane titles do.
func TestUserPaneTitle(t *testing.T) {
	got := UserPaneTitle("myproj")
	if got != "myproj__user_0" {
		t.Errorf("UserPaneTitle(myproj) = %q, want %q", got, "myproj__user_0")
	}
	if label := PaneDisplayLabel("myproj", got, 0); label != "user_0" {
		t.Errorf("PaneDisplayLabel of user pane title = %q, want %q", label, "user_0")
	}
	if labeled := UserPaneTitle("proj--front"); labeled != "proj--front__user_0" {
		t.Errorf("UserPaneTitle(proj--front) = %q, want %q", labeled, "proj--front__user_0")
	}
}
