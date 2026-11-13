package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	casspkg "github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestTruncateCassText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello world",
			maxLen:   20,
			expected: "hello world",
		},
		{
			name:     "long string truncated",
			input:    "this is a very long string that exceeds the limit",
			maxLen:   20,
			expected: "this is a very lo...",
		},
		{
			name:     "exact length unchanged",
			input:    "exactly twenty chars",
			maxLen:   20,
			expected: "exactly twenty chars",
		},
		{
			name:     "newlines replaced with spaces",
			input:    "line one\nline two",
			maxLen:   30,
			expected: "line one line two",
		},
		{
			name:     "whitespace trimmed",
			input:    "  hello world  ",
			maxLen:   20,
			expected: "hello world",
		},
		{
			name:     "newlines and truncation combined",
			input:    "first\nsecond\nthird\nfourth line here",
			maxLen:   20,
			expected: "first second thir...",
		},
		{
			name:     "maxLen zero returns empty",
			input:    "hello",
			maxLen:   0,
			expected: "",
		},
		{
			name:     "maxLen negative returns empty",
			input:    "hello",
			maxLen:   -5,
			expected: "",
		},
		{
			name:     "maxLen 1 truncates without ellipsis",
			input:    "hello",
			maxLen:   1,
			expected: "h",
		},
		{
			name:     "maxLen 3 truncates without ellipsis",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "maxLen 3 keeps exact three byte rune",
			input:    "\xe2\x82\xacx",
			maxLen:   3,
			expected: "\xe2\x82\xac",
		},
		{
			name:     "maxLen 3 keeps rune plus ascii",
			input:    "\xc3\xa9ab",
			maxLen:   3,
			expected: "\xc3\xa9a",
		},
		{
			name:     "maxLen 3 with short string unchanged",
			input:    "hi",
			maxLen:   3,
			expected: "hi",
		},
		{
			name:     "maxLen 4 uses ellipsis",
			input:    "hello world",
			maxLen:   4,
			expected: "h...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateCassText(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateCassText(%q, %d) = %q; want %q",
					tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestTruncateCassText_MultibyteLoopFallthrough tests the fallthrough at
// end of the for-range loop (line 513) when all rune starts fit within targetLen.
func TestTruncateCassText_MultibyteLoopFallthrough(t *testing.T) {

	// "aaaa🌍" = 8 bytes. maxLen=7, targetLen=4.
	// Rune starts: 0,1,2,3,4. All <= 4. Loop completes. prevI=4.
	// return s[:4]+"..." = "aaaa..." (7 bytes)
	s := "aaaa\xf0\x9f\x8c\x8d" // "aaaa🌍"
	got := truncateCassText(s, 7)
	want := "aaaa..."
	if got != want {
		t.Errorf("truncateCassText(%q, 7) = %q, want %q", s, got, want)
	}
}

// TestTruncateCassText_SmallMaxLenLoopFallthrough tests the maxLen<=3 loop
// completing without an early return when a single rune is wider than maxLen.
func TestTruncateCassText_SmallMaxLenLoopFallthrough(t *testing.T) {
	got := truncateCassText("\xf0\x9f\x8c\x8d", 3)
	if got != "" {
		t.Errorf("truncateCassText(emoji, 3) = %q, want empty string", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncateCassText(emoji, 3) produced invalid UTF-8: %q", got)
	}
}

// TestSessionWorkspaceMatchesCassWorkspaceFilter: cass filters --workspace by
// full project path, so `ntm search --session=NAME` must pass the session's
// project directory, not the bare name.
func TestSessionWorkspaceMatchesCassWorkspaceFilter(t *testing.T) {
	saved := cfg
	t.Cleanup(func() { cfg = saved })
	cfg = &config.Config{ProjectsBase: "/data/projects"}

	for in, want := range map[string]string{
		"frankentui":     "/data/projects/frankentui",
		"frankentui--ui": "/data/projects/frankentui", // labels share the base project
		"/abs/workspace": "/abs/workspace",
		"":               "",
	} {
		if got := sessionWorkspace(in); got != want {
			t.Errorf("sessionWorkspace(%q) = %q, want %q", in, got, want)
		}
	}

	client := casspkg.NewClient()
	if !client.IsInstalled() {
		t.Skip("cass not installed")
	}
	matches := func(workspace string) int {
		t.Helper()
		resp, err := client.Search(context.Background(), casspkg.SearchOptions{Query: "*", Limit: 1, Since: "60d", Workspace: workspace})
		if err != nil {
			t.Fatalf("cass search --workspace=%q: %v", workspace, err)
		}
		return resp.TotalMatches
	}
	project := sessionWorkspace("frankentui")
	if matches(project) == 0 {
		t.Skipf("no sessions indexed under %s on this machine", project)
	}
	if n := matches("frankentui"); n != 0 {
		t.Fatalf("cass matched %d hits for the bare name; the workspace contract changed", n)
	}
}

func TestExtractSessionNameFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "empty path",
			path:     "",
			expected: "unknown",
		},
		{
			name:     "simple filename",
			path:     "/path/to/session.jsonl",
			expected: "session",
		},
		{
			name:     "json extension",
			path:     "/path/to/session.json",
			expected: "session",
		},
		{
			name:     "no extension",
			path:     "/path/to/session_name",
			expected: "session_name",
		},
		{
			name:     "path ending with slash",
			path:     "/path/to/dir/",
			expected: "unknown",
		},
		{
			name:     "long filename truncated",
			path:     "/path/to/this_is_a_very_long_session_name_that_exceeds_forty_chars.jsonl",
			expected: "this_is_a_very_long_session_name_that...",
		},
		{
			name:     "date-based path",
			path:     "/sessions/2025/01/05/claude-ntm-project.jsonl",
			expected: "claude-ntm-project",
		},
		{
			name:     "windows-style path",
			path:     "C:/Users/test/sessions/session.jsonl",
			expected: "session",
		},
		{
			name:     "filename is just extension jsonl",
			path:     "/path/to/.jsonl",
			expected: "unknown",
		},
		{
			name:     "filename is just extension json",
			path:     "/path/to/.json",
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSessionNameFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("extractSessionNameFromPath(%q) = %q; want %q",
					tt.path, result, tt.expected)
			}
		})
	}
}

func TestNewCassPreviewCmd(t *testing.T) {
	cmd := newCassPreviewCmd()

	// Verify command structure
	if cmd.Use != "preview <prompt>" {
		t.Errorf("Use = %q; want %q", cmd.Use, "preview <prompt>")
	}
	if cmd.Short == "" {
		t.Error("Short description is empty")
	}
	if cmd.Long == "" {
		t.Error("Long description is empty")
	}

	// Verify flags exist
	flags := []string{"max-results", "max-age", "format", "max-tokens"}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("Flag %q not found", flag)
		}
	}

	// Verify default values
	maxResults, _ := cmd.Flags().GetInt("max-results")
	if maxResults != 5 {
		t.Errorf("max-results default = %d; want 5", maxResults)
	}

	maxAge, _ := cmd.Flags().GetInt("max-age")
	if maxAge != 30 {
		t.Errorf("max-age default = %d; want 30", maxAge)
	}

	format, _ := cmd.Flags().GetString("format")
	if format != "markdown" {
		t.Errorf("format default = %q; want %q", format, "markdown")
	}

	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	if maxTokens != 500 {
		t.Errorf("max-tokens default = %d; want 500", maxTokens)
	}
}

func TestCassPreviewCmdAddedToParent(t *testing.T) {
	cmd := newCassCmd()

	// Find preview subcommand
	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "preview" {
			found = true
			break
		}
	}

	if !found {
		t.Error("preview subcommand not found in cass command")
	}
}

func TestNewSearchCmd(t *testing.T) {
	cmd := newSearchCmd()

	if cmd.Use != "search <query>" {
		t.Errorf("Use = %q; want %q", cmd.Use, "search <query>")
	}
	if cmd.Short == "" {
		t.Error("Short description is empty")
	}
	if cmd.Long == "" {
		t.Error("Long description is empty")
	}
	if cmd.Example == "" {
		t.Error("Example is empty")
	}
}

func TestSearchCmdFlags(t *testing.T) {
	cmd := newSearchCmd()

	flags := []struct {
		name      string
		shorthand string
	}{
		{"session", "s"},
		{"agent", "a"},
		{"since", ""},
		{"limit", "n"},
		{"offset", ""},
	}

	for _, f := range flags {
		flag := cmd.Flags().Lookup(f.name)
		if flag == nil {
			t.Errorf("Flag %q not found", f.name)
			continue
		}
		if f.shorthand != "" && flag.Shorthand != f.shorthand {
			t.Errorf("Flag %q shorthand = %q; want %q", f.name, flag.Shorthand, f.shorthand)
		}
	}
}

func TestSearchCmdDefaults(t *testing.T) {
	cmd := newSearchCmd()

	limit, err := cmd.Flags().GetInt("limit")
	if err != nil {
		t.Fatalf("GetInt(limit) error: %v", err)
	}
	if limit != 20 {
		t.Errorf("limit default = %d; want 20", limit)
	}

	offset, err := cmd.Flags().GetInt("offset")
	if err != nil {
		t.Fatalf("GetInt(offset) error: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset default = %d; want 0", offset)
	}

	session, err := cmd.Flags().GetString("session")
	if err != nil {
		t.Fatalf("GetString(session) error: %v", err)
	}
	if session != "" {
		t.Errorf("session default = %q; want empty", session)
	}

	agent, err := cmd.Flags().GetString("agent")
	if err != nil {
		t.Fatalf("GetString(agent) error: %v", err)
	}
	if agent != "" {
		t.Errorf("agent default = %q; want empty", agent)
	}

	since, err := cmd.Flags().GetString("since")
	if err != nil {
		t.Fatalf("GetString(since) error: %v", err)
	}
	if since != "" {
		t.Errorf("since default = %q; want empty", since)
	}
}

func TestSearchCmdRequiresExactlyOneArg(t *testing.T) {
	cmd := newSearchCmd()

	// No args should fail
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error with no args, got nil")
	}

	// Two args should fail
	cmd = newSearchCmd()
	cmd.SetArgs([]string{"arg1", "arg2"})
	if err := cmd.Execute(); err == nil {
		t.Error("expected error with two args, got nil")
	}
}

func TestCassSubcommands(t *testing.T) {
	cmd := newCassCmd()
	expected := map[string]bool{
		"status":   false,
		"search":   false,
		"insights": false,
		"timeline": false,
		"preview":  false,
	}

	for _, sub := range cmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("subcommand %q not found in cass command", name)
		}
	}
}

func TestCassSearchCmdFlags(t *testing.T) {
	cmd := newCassSearchCmd()

	flags := []string{"agent", "workspace", "since", "limit", "offset"}
	for _, f := range flags {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("Flag %q not found on cass search command", f)
		}
	}

	limit, _ := cmd.Flags().GetInt("limit")
	if limit != 10 {
		t.Errorf("cass search limit default = %d; want 10", limit)
	}
}

func TestSearchVsCassSearchDifferences(t *testing.T) {
	searchCmd := newSearchCmd()
	cassSearchCmd := newCassSearchCmd()

	// Top-level search uses "session" flag, cass search uses "workspace"
	if searchCmd.Flags().Lookup("session") == nil {
		t.Error("top-level search should have --session flag")
	}
	if cassSearchCmd.Flags().Lookup("workspace") == nil {
		t.Error("cass search should have --workspace flag")
	}

	// Top-level search has higher default limit (20 vs 10)
	searchLimit, _ := searchCmd.Flags().GetInt("limit")
	cassLimit, _ := cassSearchCmd.Flags().GetInt("limit")
	if searchLimit <= cassLimit {
		t.Errorf("top-level search limit (%d) should be > cass search limit (%d)", searchLimit, cassLimit)
	}

	// Top-level search has shorthand flags
	if searchCmd.Flags().ShorthandLookup("s") == nil {
		t.Error("top-level search should have -s shorthand for --session")
	}
	if searchCmd.Flags().ShorthandLookup("a") == nil {
		t.Error("top-level search should have -a shorthand for --agent")
	}
	if searchCmd.Flags().ShorthandLookup("n") == nil {
		t.Error("top-level search should have -n shorthand for --limit")
	}
}

func TestNewCassClient(t *testing.T) {
	// Ensure newCassClient doesn't panic even without config
	oldCfg := cfg
	cfg = nil
	defer func() { cfg = oldCfg }()

	client := newCassClient()
	if client == nil {
		t.Error("newCassClient() returned nil with nil config")
	}
}

func TestCassMissingBinaryJSONCommandsReturnUnavailable(t *testing.T) {
	oldCfg := cfg
	oldJSONOutput := jsonOutput
	cfg = &config.Config{}
	cfg.CASS.BinaryPath = "/definitely/missing/ntm-test-cass"
	jsonOutput = true
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	})

	tests := []struct {
		name             string
		run              func() error
		wantNotInstalled bool
	}{
		{name: "status", run: func() error { return runCassStatusWithOptions(false) }, wantNotInstalled: true},
		{name: "search", run: func() error { return runCassSearchWithMode("terminal contracts", "", "", "", 5, 0, false) }, wantNotInstalled: true},
		{name: "timeline", run: func() error { return runCassTimeline("24h", "hour") }, wantNotInstalled: true},
		{name: "preview", run: func() error { return runCassPreview("terminal contract history", 5, 30, "markdown", 500) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, runErr := captureStdout(t, tt.run)
			var exitErr *robot.ProcessExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("error = %v, want ProcessExitError", runErr)
			}
			if exitErr.ExitCode() != 2 || !exitErr.JSONWritten() {
				t.Fatalf("exit result = code %d, json_written %v; want 2, true", exitErr.ExitCode(), exitErr.JSONWritten())
			}
			if tt.wantNotInstalled && !errors.Is(runErr, casspkg.ErrNotInstalled) {
				t.Fatalf("error = %v, want preserved cass.ErrNotInstalled cause", runErr)
			}

			var response map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("single JSON response decode failed: %v\nstdout=%s", err, stdout)
			}
			if response["success"] != false {
				t.Fatalf("success = %v, want false", response["success"])
			}
			if response["error_code"] != robot.ErrCodeNotImplemented {
				t.Fatalf("error_code = %v, want %s", response["error_code"], robot.ErrCodeNotImplemented)
			}
			if response["error"] == "" || response["error"] == nil {
				t.Fatalf("error = %v, want non-empty cause", response["error"])
			}
		})
	}
}

func TestCassStatusHealthAndQuickSearchCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub cass uses POSIX sh")
	}

	binDir := t.TempDir()
	argsLog := filepath.Join(binDir, "args.log")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$*\" >> \"" + argsLog + "\"\n" +
		"if [ \"${1:-}\" = \"health\" ]; then\n" +
		"  echo '{\"healthy\":true,\"index\":{\"exists\":true,\"healthy\":true},\"database\":{\"exists\":true,\"opened\":true,\"healthy\":true}}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo '{\"query\":\"oauth\",\"count\":1,\"total_matches\":1,\"hits\":[{\"source_path\":\"/s/1.jsonl\",\"agent\":\"codex\",\"title\":\"OAuth flow\",\"score\":0.9}]}'\n"
	cassBin := filepath.Join(binDir, "cass")
	if err := os.WriteFile(cassBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}

	oldCfg := cfg
	oldJSONOutput := jsonOutput
	cfg = &config.Config{}
	cfg.CASS.BinaryPath = cassBin
	jsonOutput = true
	t.Cleanup(func() {
		cfg = oldCfg
		jsonOutput = oldJSONOutput
	})

	statusOut, err := captureStdout(t, func() error {
		cmd := newCassCmd()
		cmd.SetArgs([]string{"status", "--health"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("cass status --health --json: %v", err)
	}
	var statusResp casspkg.StatusResponse
	if err := json.Unmarshal([]byte(statusOut), &statusResp); err != nil || !statusResp.Healthy {
		t.Fatalf("unexpected cass health status: err=%v out=%s", err, statusOut)
	}

	searchOut, err := captureStdout(t, func() error {
		cmd := newCassCmd()
		cmd.SetArgs([]string{"search", "oauth", "--quick"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("cass search --quick --json: %v", err)
	}
	var searchResp casspkg.SearchResponse
	if err := json.Unmarshal([]byte(searchOut), &searchResp); err != nil || !searchResp.HasResults() {
		t.Fatalf("unexpected cass quick search response: err=%v out=%s", err, searchOut)
	}

	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	logStr := string(logged)
	if !strings.Contains(logStr, "health --json") || !strings.Contains(logStr, "--fields=summary") {
		t.Fatalf("expected health --json and SearchQuick --fields=summary in args log, got:\n%s", logStr)
	}
}
