package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeLine builds a realistic Claude Code assistant transcript entry.
func claudeLine(model string, input, cacheCreate, cacheRead, output int) string {
	return `{"parentUuid":"x","cwd":"/Users/x/proj","sessionId":"abc","type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","usage":{"input_tokens":` +
		itoa(input) + `,"cache_creation_input_tokens":` + itoa(cacheCreate) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `,"output_tokens":` + itoa(output) +
		`,"server_tool_use":{"web_search_requests":0}}},"timestamp":"2026-08-14T00:00:00.000Z"}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func writeTranscript(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadLatestTranscriptUsage_Claude(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "s.jsonl",
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		claudeLine("claude-opus-4-6-1m", 2, 400, 100000, 500),
		`{"type":"user","message":{"role":"user","content":"more"}}`,
		claudeLine("claude-opus-4-6-1m", 1, 1312, 685353, 1528),
	)

	u, err := ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	want := 1 + 1312 + 685353 + 1528
	if u.Tokens != want {
		t.Errorf("Tokens = %d, want %d", u.Tokens, want)
	}
	if u.Model != "claude-opus-4-6-1m" {
		t.Errorf("Model = %q", u.Model)
	}
	if u.CacheReadTokens != 685353 || u.OutputTokens != 1528 {
		t.Errorf("component tokens wrong: %+v", u)
	}
	if u.Path != path {
		t.Errorf("Path = %q", u.Path)
	}
}

func TestReadLatestTranscriptUsage_Codex(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "rollout.jsonl",
		`{"timestamp":"2026-08-02T02:33:59.696Z","type":"session_meta","payload":{"session_id":"019f","cwd":"/Users/x/proj","model_provider":"openai"}}`,
		`{"timestamp":"2026-08-02T02:34:00.000Z","type":"turn_context","payload":{"cwd":"/Users/x/proj","model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2026-08-02T02:34:06.831Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":7344122482,"cached_input_tokens":7222280192,"cache_write_input_tokens":0,"output_tokens":14046259,"reasoning_output_tokens":5684731,"total_tokens":7358168741},"last_token_usage":{"input_tokens":180736,"cached_input_tokens":177920,"cache_write_input_tokens":0,"output_tokens":69,"reasoning_output_tokens":0,"total_tokens":180805},"model_context_window":258400},"rate_limits":{"primary":{"used_percent":9.0}}}}`,
	)

	u, err := ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage, got nil")
	}
	if u.Tokens != 180805 {
		t.Errorf("Tokens = %d, want 180805", u.Tokens)
	}
	if u.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want gpt-5.6-sol", u.Model)
	}
	if u.ContextWindow != 258400 {
		t.Errorf("ContextWindow = %d, want 258400", u.ContextWindow)
	}
	if u.CacheReadTokens != 177920 {
		t.Errorf("CacheReadTokens = %d, want 177920", u.CacheReadTokens)
	}
}

func TestReadLatestTranscriptUsage_TailWindowTruncatedFirstLine(t *testing.T) {
	dir := t.TempDir()
	// Build a file larger than the tail window whose window start lands
	// mid-line: a huge junk first section, then valid entries at the end.
	junk := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", transcriptTailWindow) + `"}}`
	path := writeTranscript(t, dir, "big.jsonl",
		junk,
		claudeLine("claude-fable-5", 5, 10, 700000, 200),
	)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= transcriptTailWindow {
		t.Fatalf("fixture not larger than tail window: %d", fi.Size())
	}

	u, err := ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("expected usage from tail window, got nil")
	}
	if u.Tokens != 5+10+700000+200 {
		t.Errorf("Tokens = %d", u.Tokens)
	}
	if u.Model != "claude-fable-5" {
		t.Errorf("Model = %q", u.Model)
	}
}

func TestReadLatestTranscriptUsage_NoUsage(t *testing.T) {
	dir := t.TempDir()
	path := writeTranscript(t, dir, "empty.jsonl",
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"system","subtype":"init"}`,
	)
	u, err := ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Errorf("expected nil usage, got %+v", u)
	}
}

func TestMungeProjectPath(t *testing.T) {
	cases := map[string]string{
		"/Users/jemanuel/projects/ntm": "-Users-jemanuel-projects-ntm",
		"/Users/x/my.proj_v2":          "-Users-x-my-proj-v2",
		"/a b/c":                       "-a-b-c",
	}
	for in, want := range cases {
		if got := MungeProjectPath(in); got != want {
			t.Errorf("MungeProjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindClaudeTranscript(t *testing.T) {
	projects := t.TempDir()
	cwd := "/Users/x/proj"
	dir := filepath.Join(projects, MungeProjectPath(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	old := writeTranscript(t, dir, "old.jsonl", claudeLine("m", 1, 0, 0, 1))
	newer := writeTranscript(t, dir, "new.jsonl", claudeLine("m", 2, 0, 0, 2))
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Prefers files newer than the threshold.
	got, ok := FindClaudeTranscript(projects, cwd, now.Add(-time.Hour))
	if !ok || got != newer {
		t.Errorf("got %q ok=%v, want %q", got, ok, newer)
	}

	// Falls back to the newest overall when nothing beats the threshold.
	got, ok = FindClaudeTranscript(projects, cwd, now.Add(time.Hour))
	if !ok || got != newer {
		t.Errorf("fallback: got %q ok=%v, want %q", got, ok, newer)
	}

	// Unknown project directory: not found.
	if _, ok := FindClaudeTranscript(projects, "/no/such/dir", time.Time{}); ok {
		t.Error("expected not found for unknown cwd")
	}
}

func TestFindCodexTranscript(t *testing.T) {
	sessions := t.TempDir()
	day := filepath.Join(sessions, "2026", "08", "14")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := func(cwd string) string {
		return `{"timestamp":"2026-08-14T00:00:00Z","type":"session_meta","payload":{"session_id":"s","cwd":"` + cwd + `"}}`
	}
	match := writeTranscript(t, day, "rollout-a.jsonl", meta("/Users/x/proj"), "")
	writeTranscript(t, day, "rollout-b.jsonl", meta("/Users/x/other"), "")

	got, ok := FindCodexTranscript(sessions, "/Users/x/proj", time.Time{})
	if !ok || got != match {
		t.Errorf("got %q ok=%v, want %q", got, ok, match)
	}
	if _, ok := FindCodexTranscript(sessions, "/Users/x/nomatch", time.Time{}); ok {
		t.Error("expected not found for unmatched cwd")
	}
}

// ompSessionHead is the head of a real omp v18.2.3 transcript: a space-padded
// title line (rewritten in place) precedes the session header.
func ompSessionHead(cwd string) []string {
	return []string{
		`{"type":"title","v":1,"title":"Reply With the Word Ok","source":"auto","updatedAt":"2026-09-17T05:10:17.723Z","pad":"                "}`,
		`{"type":"session","version":3,"id":"01a0adc4-823b-70d0-aa1d-80adef4fd1b1","timestamp":"2026-09-17T05:08:51.899Z","cwd":"` + cwd + `","title":"Reply With the Word Ok","titleSource":"auto"}`,
		`{"type":"model_change","id":"452397cb","parentId":null,"timestamp":"2026-09-17T05:08:52.004Z","model":"openrouter/stealth/union-alpha","resolvedModelIsFallback":false}`,
	}
}

func ompAssistantLine(input, output, cacheRead, cacheWrite int) string {
	total := input + output + cacheRead + cacheWrite
	return `{"type":"message","id":"cd1ea0f6","parentId":"79ae777c","timestamp":"2026-09-17T05:09:57.893Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"api":"openrouter","provider":"openrouter","model":"stealth/union-alpha","usage":{"input":` +
		itoa(input) + `,"output":` + itoa(output) + `,"cacheRead":` + itoa(cacheRead) + `,"cacheWrite":` + itoa(cacheWrite) + `,"totalTokens":` + itoa(total) +
		`,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop"}}`
}

func TestReadLatestTranscriptUsage_Omp(t *testing.T) {
	dir := t.TempDir()
	lines := append(ompSessionHead("/data/proj"),
		`{"type":"message","id":"79ae777c","message":{"role":"user","content":[{"type":"text","text":"reply with the word ok"}]}}`,
		ompAssistantLine(812, 52, 16503, 0),
		// An aborted turn records all-zero usage; it must not mask the
		// previous real reading.
		ompAssistantLine(0, 0, 0, 0),
	)
	path := writeTranscript(t, dir, "2026-09-17T05-08-51-899Z_01a0adc4.jsonl", lines...)

	u, err := ReadLatestTranscriptUsage(path)
	if err != nil || u == nil {
		t.Fatalf("ReadLatestTranscriptUsage = (%+v, %v)", u, err)
	}
	if u.Tokens != 17367 || u.InputTokens != 812 || u.CacheReadTokens != 16503 || u.OutputTokens != 52 {
		t.Errorf("usage = %+v, want tokens=17367 input=812 cacheRead=16503 output=52", u)
	}
	if u.Model != "openrouter/stealth/union-alpha" {
		t.Errorf("model = %q, want the provider-qualified omp spelling", u.Model)
	}
}

// TestLatestAgentTranscriptUsage_Omp resolves an omp pane's transcript through
// the real store layout under a temp HOME and reads its last usage record.
func TestLatestAgentTranscriptUsage_Omp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"PI_CODING_AGENT_SESSION_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE", "PI_CONFIG_DIR", "XDG_DATA_HOME"} {
		t.Setenv(k, "")
	}
	dir := filepath.Join(home, ".omp", "agent", "sessions", "--data-proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeTranscript(t, dir, "2026-09-17T05-08-51-899Z_01a0adc4-823b-70d0-aa1d-80adef4fd1b1.jsonl",
		append(ompSessionHead("/data/proj"), ompAssistantLine(812, 52, 16503, 0))...)

	u, ok := LatestAgentTranscriptUsage("omp", "/data/proj", time.Time{})
	if !ok || u.Path != path || u.Tokens != 17367 {
		t.Fatalf("LatestAgentTranscriptUsage(omp) = (%+v, %v), want 17367 tokens from %s", u, ok, path)
	}
	if _, ok := LatestAgentTranscriptUsage("omp", "/data/other", time.Time{}); ok {
		t.Fatal("a transcript for another cwd must not be attributed")
	}
}

func TestOmpStatusBarUsage(t *testing.T) {
	at := time.Date(2026, 9, 17, 5, 10, 0, 0, time.UTC)
	capture := " ok\n\n" +
		"╭── ⬢ Union Alpha > 🗑 omp-probe ▶─────7%─────────────────────────────────────────────────────────┃──────────262K───╮\n" +
		"╰─                                                                                                                    ─╯\n"
	u, ok := OmpStatusBarUsage(capture, at)
	if !ok || u.Tokens != 18340 || u.ContextWindow != 262000 || !u.UpdatedAt.Equal(at) || u.Path != "" {
		t.Fatalf("OmpStatusBarUsage = (%+v, %v), want tokens=18340 window=262000", u, ok)
	}
	if _, ok := OmpStatusBarUsage("● Done.\n\n❯ \n", at); ok {
		t.Error("a non-omp capture must not produce a gauge reading")
	}
}

func TestTranscriptConfidence(t *testing.T) {
	now := time.Now()
	if got := TranscriptConfidence(now.Add(-time.Minute), now); got != "high" {
		t.Errorf("fresh transcript: got %q, want high", got)
	}
	if got := TranscriptConfidence(now.Add(-time.Hour), now); got != "medium" {
		t.Errorf("stale transcript: got %q, want medium", got)
	}
}
