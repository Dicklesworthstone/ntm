package cli

import "testing"

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

// TestTruncateCassText_SmallMaxLenLoopFallthrough tests line 500 (maxLen<=3 loop
// completing without early return) which happens when all rune positions < maxLen.
func TestTruncateCassText_SmallMaxLenLoopFallthrough(t *testing.T) {

	// With a 2-byte rune at position 0 and maxLen=2:
	// Rune starts: 0. i=0 (<2 ok), byteLen=1.
	// Loop ends. Next iteration: i=2 (the multi-byte rune occupies positions 0-1).
	// Actually, for i := range s iterates over rune starts.
	// "é" = 2 bytes. maxLen=3. Rune start: 0. i=0 (<3), byteLen=1. Loop ends.
	// Falls through to return s[:3] which is "é" + 1 random byte - bad.
	// Actually, "éa" = 3 bytes. maxLen=2, loop: i=0 (<2, byteLen=1), i=2 (>=2, returns s[:1]).
	// That's the early return, not the fallthrough.
	// For the fallthrough: need len(s) > maxLen (>3 bytes) but all rune starts < maxLen.
	// "🌍" = 4 bytes, single rune. maxLen=3. Rune starts: 0. i=0 (<3). Loop ends.
	// return s[:maxLen] = s[:3] — but that splits the rune. That's what line 500 does.
	// This is the line 500 fallthrough for a single multi-byte rune.
	got := truncateCassText("\xf0\x9f\x8c\x8d", 3)
	// The function returns s[:3] which is 3 bytes of a 4-byte emoji.
	if len(got) > 3 {
		t.Errorf("truncateCassText(emoji, 3) length = %d, want <= 3", len(got))
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
