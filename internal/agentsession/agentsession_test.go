package agentsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeProvider(t *testing.T) {
	cases := map[string]string{
		"cc":          "claude",
		"claude":      "claude",
		"claude-code": "claude",
		"CC":          "claude",
		"cod":         "codex",
		"codex":       "codex",
		"gmi":         "gemini",
		"gemini":      "gemini",
		"user":        "",
		"cursor":      "",
		"":            "",
		"  cc  ":      "claude",
	}
	for in, want := range cases {
		if got := ResumeProvider(in); got != want {
			t.Errorf("ResumeProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeClaudeProjectDir(t *testing.T) {
	cases := map[string]string{
		"/data/projects/ntm": "-data-projects-ntm",
		"/home/u/my.app":     "-home-u-my-app",
		// Underscores must be PRESERVED — Claude Code does not transform them.
		// Verified against live ~/.claude/projects: /data/projects/mcp_agent_mail
		// encodes to -data-projects-mcp_agent_mail (underscore intact).
		"/a/b_c":                            "-a-b_c",
		"/data/projects/mcp_agent_mail":     "-data-projects-mcp_agent_mail",
		"/data/projects/jeffreys-skills.md": "-data-projects-jeffreys-skills-md",
		"/data/projects/ntm/":               "-data-projects-ntm", // trailing slash cleaned
	}
	for in, want := range cases {
		if got := encodeClaudeProjectDir(in); got != want {
			t.Errorf("encodeClaudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}

	// Explicit regression guard for the underscore-collapse bug (#175): an
	// earlier version mapped '_' -> '-', which could resolve to a *different*
	// real project's directory and resume the wrong Claude session.
	if got := encodeClaudeProjectDir("/data/projects/coding_agent_session_search"); strings.Contains(got, "_") == false {
		t.Errorf("encodeClaudeProjectDir collapsed underscore: got %q, want underscores preserved", got)
	}
}

func TestResumeCommandNative(t *testing.T) {
	// Force the native path by making casr unavailable.
	orig := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		prefer   bool
		want     string
	}{
		{"claude", "abc-123", true, "claude --resume 'abc-123'"},
		{"codex", "r1", false, "codex resume 'r1'"},
		{"gemini", "g9", true, "gemini --resume 'g9'"},
		{"claude", "", true, ""},
		{"unknown", "x", true, ""},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, c.prefer); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,%v) = %q, want %q", c.provider, c.id, c.prefer, got, c.want)
		}
	}
}

func TestResumeCommandCASR(t *testing.T) {
	// Make casr "available" so the casr path is taken.
	orig := lookPath
	lookPath = func(name string) (string, error) {
		if name == "casr" {
			return "/usr/bin/casr", nil
		}
		return "", os.ErrNotExist
	}
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		want     string
	}{
		{"claude", "abc-123", "casr -cc 'abc-123'"},
		{"codex", "r1", "casr -cod 'r1'"},
		{"gemini", "g9", "casr -gmi 'g9'"},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, true); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,casr) = %q, want %q", c.provider, c.id, got, c.want)
		}
	}

	// preferCASR=false must still use native even when casr is available.
	if got := ResumeCommand("claude", "x", false); got != "claude --resume 'x'" {
		t.Errorf("native override failed: got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"abc":      "'abc'",
		"":         "''",
		"a'b":      `'a'\''b'`,
		"uuid-123": "'uuid-123'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverClaude(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/demo"
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write two session files; the newer one should win.
	older := filepath.Join(projDir, "old-session.jsonl")
	newer := filepath.Join(projDir, "new-session.jsonl")
	if err := os.WriteFile(older, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make newer file genuinely newer.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("cc", workDir)
	if info == nil {
		t.Fatal("expected to discover a claude session, got nil")
	}
	if info.SessionID != "new-session" {
		t.Errorf("SessionID = %q, want new-session", info.SessionID)
	}
	if info.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", info.Provider)
	}
	if info.SourcePath != newer {
		t.Errorf("SourcePath = %q, want %q", info.SourcePath, newer)
	}
}

func TestDiscoverClaudeNoSession(t *testing.T) {
	home := t.TempDir()
	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	if info := Discover("cc", "/no/such/project"); info != nil {
		t.Errorf("expected nil for missing project, got %+v", info)
	}
}

func TestDiscoverNonResumableAgent(t *testing.T) {
	if info := Discover("user", "/data/projects/demo"); info != nil {
		t.Errorf("expected nil for user pane, got %+v", info)
	}
	if info := Discover("cursor", "/data/projects/demo"); info != nil {
		t.Errorf("expected nil for cursor pane, got %+v", info)
	}
}

func TestDiscoverCodex(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/codexdemo"
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roll := filepath.Join(dayDir, "rollout-7c3a.jsonl")
	content := `{"type":"session_meta","cwd":"` + workDir + `"}` + "\n"
	if err := os.WriteFile(roll, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("cod", workDir)
	if info == nil {
		t.Fatal("expected to discover a codex session, got nil")
	}
	if info.SessionID != "7c3a" {
		t.Errorf("SessionID = %q, want 7c3a", info.SessionID)
	}
	if info.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", info.Provider)
	}

	// A rollout for a different cwd must not match.
	if info := Discover("cod", "/some/other/dir"); info != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", info)
	}
}

func TestDiscoverGemini(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/gemdemo"
	chatsDir := filepath.Join(home, ".gemini", "tmp", "abcdef", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(chatsDir, "session-42.json")
	content := `{"workspace":"` + workDir + `"}`
	if err := os.WriteFile(sess, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("gmi", workDir)
	if info == nil {
		t.Fatal("expected to discover a gemini session, got nil")
	}
	if info.SessionID != "42" {
		t.Errorf("SessionID = %q, want 42", info.SessionID)
	}
}
