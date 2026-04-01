package tmux

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "''"},
		{name: "simple", in: "foo", want: "'foo'"},
		{name: "space", in: "foo bar", want: "'foo bar'"},
		{name: "single quote", in: "weird'quote", want: `'weird'\''quote'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShellQuote(tt.in)
			if got != tt.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildRemoteShellCommand(t *testing.T) {
	t.Parallel()

	got := buildRemoteShellCommand("tmux", "display-message", "-t", "sess", "hello world")
	want := `tmux 'display-message' '-t' 'sess' 'hello world'`
	if got != want {
		t.Fatalf("buildRemoteShellCommand() = %q, want %q", got, want)
	}

	got = buildRemoteShellCommand("tmux", "new-session", "-s", "x; rm -rf /")
	if !strings.Contains(got, `'x; rm -rf /'`) {
		t.Fatalf("buildRemoteShellCommand() did not quote dangerous arg: %q", got)
	}
}

func TestBuildPaneCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		projectDir string
		cmd        string
		want       string
		wantErr    bool
	}{
		{
			name:       "simple command",
			projectDir: "/projects/foo",
			cmd:        "ls -la",
			want:       "cd '/projects/foo' && ls -la",
			wantErr:    false,
		},
		{
			name:       "command with spaces",
			projectDir: "/projects/foo bar",
			cmd:        "echo hello",
			want:       "cd '/projects/foo bar' && echo hello",
			wantErr:    false,
		},
		{
			name:       "unsafe command",
			projectDir: "/projects/foo",
			cmd:        "echo hello\nrm -rf /",
			want:       "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPaneCommand(tt.projectDir, tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildPaneCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("BuildPaneCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetPanes_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	_, err := GetPanes("nonexistent_session_12345")
	if err == nil {
		t.Error("GetPanes should fail for non-existent session")
	}
}

func TestGetFirstWindow_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	_, err := GetFirstWindow("nonexistent_session_12345")
	if err == nil {
		t.Error("GetFirstWindow should fail for non-existent session")
	}
}

func TestGetDefaultPaneIndex_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	_, err := GetDefaultPaneIndex("nonexistent_session_12345")
	if err == nil {
		t.Error("GetDefaultPaneIndex should fail for non-existent session")
	}
}

func TestZoomPane_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	err := ZoomPane("nonexistent_session_12345", 0)
	if err == nil {
		t.Error("ZoomPane should fail for non-existent session")
	}
}

func TestGetCurrentSession_Simulated(t *testing.T) {
	// cannot run in parallel due to t.Setenv
	skipIfNoTmux(t)
	// Simulate being in tmux but command failing (since we aren't actually in a client)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")

	// usage of run() will likely fail or return empty because we aren't attached
	// but we want to ensure it doesn't panic
	session := GetCurrentSession()
	if session != "" {
		// If it actually works (e.g. nested tmux), that's fine too, but unlikely in this env
		t.Logf("GetCurrentSession returned: %q", session)
	}
}
func TestParseAgentFromTitle_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title       string
		wantType    AgentType
		wantVariant string
		wantTags    []string
	}{
		{"invalid_format", AgentUser, "", nil},
		{"session__invalid_1", AgentType("invalid"), "", nil}, // valid regex but invalid type
		{"session__cc_1", AgentClaude, "", nil},
		{"session__cc_1_variant", AgentClaude, "variant", nil},
		{"session__cod_2_gpt4", AgentCodex, "gpt4", nil},
		// With tags
		{"session__cc_1[frontend]", AgentClaude, "", []string{"frontend"}},
		{"session__cc_1[frontend,backend]", AgentClaude, "", []string{"frontend", "backend"}},
		{"session__cc_1_opus[api,urgent]", AgentClaude, "opus", []string{"api", "urgent"}},
		{"session__cod_1[]", AgentCodex, "", nil}, // empty tags
		{"session__gmi_1[test]", AgentGemini, "", []string{"test"}},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			gotType, _, gotVariant, gotTags := parseAgentFromTitle(tt.title)
			if gotType != tt.wantType {
				t.Errorf("parseAgentFromTitle() type = %v, want %v", gotType, tt.wantType)
			}
			if gotVariant != tt.wantVariant {
				t.Errorf("parseAgentFromTitle() variant = %q, want %q", gotVariant, tt.wantVariant)
			}
			if len(gotTags) != len(tt.wantTags) {
				t.Errorf("parseAgentFromTitle() tags = %v, want %v", gotTags, tt.wantTags)
			} else {
				for i := range gotTags {
					if gotTags[i] != tt.wantTags[i] {
						t.Errorf("parseAgentFromTitle() tags[%d] = %q, want %q", i, gotTags[i], tt.wantTags[i])
					}
				}
			}
		})
	}
}

func TestParseAgentFromTitle(t *testing.T) {
	tests := []struct {
		title       string
		wantType    AgentType
		wantIndex   int
		wantVariant string
		wantTags    []string
	}{
		{"myproj__cc_1", AgentClaude, 1, "", nil},
		{"myproj__cod_2_gpt4", AgentCodex, 2, "gpt4", nil},
		{"myproj__gmi_3[foo,bar]", AgentGemini, 3, "", []string{"foo", "bar"}},
		{"myproj__user", AgentUser, 0, "", nil},
		{"other", AgentUser, 0, "", nil},
	}

	for _, tt := range tests {
		gotType, gotIndex, gotVariant, gotTags := parseAgentFromTitle(tt.title)
		if gotType != tt.wantType {
			t.Errorf("parseAgentFromTitle(%q) type = %v, want %v", tt.title, gotType, tt.wantType)
		}
		if gotIndex != tt.wantIndex {
			t.Errorf("parseAgentFromTitle(%q) index = %v, want %v", tt.title, gotIndex, tt.wantIndex)
		}
		if gotVariant != tt.wantVariant {
			t.Errorf("parseAgentFromTitle(%q) variant = %v, want %v", tt.title, gotVariant, tt.wantVariant)
		}
		if len(gotTags) != len(tt.wantTags) {
			t.Errorf("parseAgentFromTitle(%q) tags = %v, want %v", tt.title, gotTags, tt.wantTags)
		}
	}
}

func TestFormatTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tags []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"frontend"}, "[frontend]"},
		{[]string{"frontend", "backend"}, "[frontend,backend]"},
		{[]string{"api", "urgent", "test"}, "[api,urgent,test]"},
	}

	for _, tt := range tests {
		name := "nil"
		if tt.tags != nil {
			name = FormatTags(tt.tags)
			if name == "" {
				name = "empty"
			}
		}
		t.Run(name, func(t *testing.T) {
			got := FormatTags(tt.tags)
			if got != tt.want {
				t.Errorf("FormatTags(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestStripTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"session__cc_1", "session__cc_1"},
		{"session__cc_1[frontend]", "session__cc_1"},
		{"session__cc_1_opus[backend,api]", "session__cc_1_opus"},
		{"session__cc_1[]", "session__cc_1"},
		{"title_with[brackets]_in_middle[tags]", "title_with[brackets]_in_middle"},
		{"no_tags_at_all", "no_tags_at_all"},
		// Edge case: [ found but no closing ]
		{"session__cc_1[incomplete", "session__cc_1[incomplete"},
		// Edge case: [ at the end with nothing after
		{"session__cc_1[", "session__cc_1["},
		// Edge case: ] without matching [ at end
		{"session__cc_1]", "session__cc_1]"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripTags(tt.input)
			if got != tt.want {
				t.Errorf("stripTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPaneTitleSessionAndSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title       string
		wantSession string
		wantSuffix  string
	}{
		{"project__cc_1", "project", "cc_1"},
		{"my__project__cursor_2", "my__project", "cursor_2"},
		{"my__project__cod_3_opus[frontend]", "my__project", "cod_3_opus[frontend]"},
		{"__cc_1", "", "cc_1"},
		{"plain_title", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			t.Parallel()

			if got := PaneTitleSession(tt.title); got != tt.wantSession {
				t.Fatalf("PaneTitleSession(%q) = %q, want %q", tt.title, got, tt.wantSession)
			}
			if got := PaneTitleSuffix(tt.title); got != tt.wantSuffix {
				t.Fatalf("PaneTitleSuffix(%q) = %q, want %q", tt.title, got, tt.wantSuffix)
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"frontend", []string{"frontend"}},
		{"frontend,backend", []string{"frontend", "backend"}},
		{"api, urgent, test", []string{"api", "urgent", "test"}}, // with spaces
		{",empty,", []string{"empty"}},                           // leading/trailing commas
	}

	for _, tt := range tests {
		name := tt.input
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got := parseTags(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("parseTags(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
