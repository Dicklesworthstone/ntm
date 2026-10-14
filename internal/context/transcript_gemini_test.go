package context

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
)

const geminiAccountingTime = "2026-10-01T12:00:00Z"
const geminiAccountingHeader = `{"sessionId":"gemini-accounting-session","projectHash":"workspace-hash","kind":"main"}`

func geminiAccountingMessage(id string, tokens int) string {
	return fmt.Sprintf(`{"id":%q,"type":"gemini","timestamp":%q,"model":"gemini-2.5-pro","content":"response","tokens":{"input":%d,"output":0,"cached":0,"total":%d}}`, id, geminiAccountingTime, tokens, tokens)
}

func writeGeminiAccountingFixture(t *testing.T, extension, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-accounting."+extension)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadLatestTranscriptUsageGeminiFormats(t *testing.T) {
	message := `{"id":"answer","type":"gemini","timestamp":"2026-10-01T12:00:00Z","model":"gemini-2.5-pro","content":[{"text":"ignore quoted token_count data"}],"tokens":{"input":8000,"output":30,"cached":6000,"thoughts":20,"tool":5,"total":8055}}`
	user := `{"id":"user","type":"user","content":"prompt","tokens":{"input":999999,"total":999999}}`
	for _, extension := range []string{"json", "jsonl"} {
		t.Run(extension, func(t *testing.T) {
			data := geminiAccountingHeader + "\n" + message + "\n" + user + "\n"
			if extension == "json" {
				data = `{"sessionId":"gemini-accounting-session","projectHash":"workspace-hash","messages":[` + message + "," + user + "]}"
				var pretty strings.Builder
				var value any
				if err := json.Unmarshal([]byte(data), &value); err != nil {
					t.Fatal(err)
				}
				encoder := json.NewEncoder(&pretty)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(value); err != nil {
					t.Fatal(err)
				}
				data = pretty.String()
			}
			path := writeGeminiAccountingFixture(t, extension, data)
			usage, err := ReadLatestTranscriptUsage(path)
			if err != nil || usage == nil {
				t.Fatalf("usage = %+v, %v", usage, err)
			}
			if usage.Tokens != 8055 || usage.InputTokens != 2000 || usage.CacheReadTokens != 6000 || usage.OutputTokens != 30 || usage.ContextWindow != 0 || usage.Model != "gemini-2.5-pro" || usage.Path != path {
				t.Fatalf("incorrect native accounting (cached is part of input): %+v", usage)
			}
			wantTime, _ := time.Parse(time.RFC3339, geminiAccountingTime)
			if !usage.UpdatedAt.Equal(wantTime) {
				t.Fatalf("usage time = %s, want %s", usage.UpdatedAt, wantTime)
			}
			if got := TranscriptConfidence(usage.UpdatedAt, wantTime.Add(time.Hour)); got != "medium" {
				t.Fatalf("new file mtime made old accounting fresh: %s", got)
			}
		})
	}
}

func TestGeminiJournalReplaysActiveHistory(t *testing.T) {
	first, second, third := geminiAccountingMessage("a", 100), geminiAccountingMessage("b", 500), geminiAccountingMessage("c", 900)
	base := geminiAccountingHeader + "\n" + first + "\n" + second + "\n" + third + "\n"
	cases := []struct {
		name, tail string
		want       int
	}{
		{"last turn, not cumulative", "", 900},
		{"update an old ID in place", geminiAccountingMessage("a", 4000), 900},
		{"update current ID", geminiAccountingMessage("c", 1200), 1200},
		{"rewind includes target", `{"$rewindTo":"b"}`, 100},
		{"rewind unknown clears all", `{"$rewindTo":"absent"}`, 0},
		{"new response after rewind", `{"$rewindTo":"a"}` + "\n" + geminiAccountingMessage("d", 50), 50},
		{"metadata does not refresh usage", `{"$set":{"lastUpdated":"2026-10-14T12:00:00Z","summary":"new summary"}}`, 900},
		{"replace history", `{"$set":{"messages":[` + geminiAccountingMessage("fresh", 40) + `]}}`, 40},
		{"clear history", `{"$set":{"messages":[]}}`, 0},
		{"remove current message", `{"$patch":{"removeIds":["c"]}}`, 500},
		{"reorder messages", `{"$patch":{"orderIds":["b","a"]}}`, 100},
		{"remove and reorder", `{"$patch":{"removeIds":["c"],"orderIds":["a","b","a","missing"]}}`, 500},
		{"content patch is not usage", `{"$patch":{"id":"c","content":"shortened","updates":[{"id":"b","content":"other"}]}}`, 900},
		{"subagent metadata is sticky", `{"$set":{"kind":"subagent"}}` + "\n" + `{"$set":{"kind":"main"}}`, 0},
		{"aborted response is not zero occupancy", `{"id":"abort","type":"gemini","timestamp":"2026-10-01T12:01:00Z","tokens":{"input":0,"total":0}}`, 900},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeGeminiAccountingFixture(t, "jsonl", base+tc.tail+"\n")
			usage, err := ReadLatestTranscriptUsage(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == 0 {
				if usage != nil {
					t.Fatalf("unattributable usage = %+v", usage)
				}
			} else if usage == nil || usage.Tokens != tc.want {
				t.Fatalf("usage = %+v, want %d", usage, tc.want)
			}
		})
	}
}

func TestGeminiJournalRewindOutsideOldTailWindow(t *testing.T) {
	noise := fmt.Sprintf(`{"id":"long-user","type":"user","content":%q}`, strings.Repeat("x", transcriptTailWindow*2))
	data := geminiAccountingHeader + "\n" + geminiAccountingMessage("old", 100) + "\n" + noise + "\n" + geminiAccountingMessage("removed", 900) + "\n" + `{"$rewindTo":"long-user"}` + "\n"
	path := writeGeminiAccountingFixture(t, "jsonl", data)
	usage, err := ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil || usage.Tokens != 100 {
		t.Fatalf("rewind beyond tail = %+v, %v", usage, err)
	}
}

func TestGeminiJournalRejectsUncertainAccounting(t *testing.T) {
	base := geminiAccountingHeader + "\n" + geminiAccountingMessage("good", 100) + "\n"
	cases := []string{
		`{"$rewindTo":`,
		`{"$futureHistoryOperation":true}`,
		`{"$rewindTo":"good","$set":{"messages":[]}}`,
		`{"$set":{"sessionId":"different"}}`,
		`{"$set":{"projectHash":"different"}}`,
		geminiAccountingHeader,
		`{"id":"bad","type":"gemini","timestamp":"bad","tokens":{"input":1}}`,
		`{"id":"bad","type":"gemini","tokens":{"input":-1}}`,
		`{"id":"bad","type":"gemini","tokens":{"input":1,"cached":2}}`,
		`{"id":"bad","type":"gemini","tokens":{"input":4,"total":2}}`,
		`{"id":"bad","type":"gemini","tokens":{"input":1.25}}`,
		`{"id":"bad","type":"future-role"}`,
		`null`,
	}
	for i, tail := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			path := writeGeminiAccountingFixture(t, "jsonl", base+tail)
			usage, err := ReadLatestTranscriptUsage(path)
			if err == nil || usage != nil {
				t.Fatalf("uncertain journal reused old count: %+v, %v", usage, err)
			}
		})
	}
}

func TestGeminiAccountingFallbackTotalAndSubagents(t *testing.T) {
	message := `{"id":"answer","type":"gemini","timestamp":"2026-10-01T12:00:00Z","tokens":{"input":100,"output":7,"cached":80,"thoughts":9,"tool":3}}`
	path := writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n"+message+"\n")
	usage, err := ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil || usage.Tokens != 119 || usage.InputTokens != 20 {
		t.Fatalf("fallback accounting = %+v, %v", usage, err)
	}
	for _, ext := range []string{"json", "jsonl"} {
		data := `{"sessionId":"child","projectHash":"workspace-hash","kind":"subagent","messages":[` + message + `]}`
		if ext == "jsonl" {
			data = `{"sessionId":"child","projectHash":"workspace-hash","kind":"subagent"}` + "\n" + message + "\n" + `{"$set":{"kind":"main"}}`
		}
		path := writeGeminiAccountingFixture(t, ext, data)
		usage, err := ReadLatestTranscriptUsage(path)
		if err != nil || usage != nil {
			t.Fatalf("subagent %s attributed to parent: %+v, %v", ext, usage, err)
		}
	}
}

func TestGeminiAccountingSnapshotCache(t *testing.T) {
	base := geminiAccountingHeader + "\n" + geminiAccountingMessage("a", 100) + "\n"
	path := writeGeminiAccountingFixture(t, "jsonl", base)
	usage, err := ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil {
		t.Fatalf("initial usage = %+v, %v", usage, err)
	}
	usage.Tokens = 99999
	usage.Path = "changed-by-caller"
	usage, err = ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil || usage.Tokens != 100 || usage.Path != path {
		t.Fatalf("cache exposed mutable state: %+v, %v", usage, err)
	}

	// An appended rewind must invalidate the previous receipt.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"$rewindTo":"a"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	usage, err = ReadLatestTranscriptUsage(path)
	if err != nil || usage != nil {
		t.Fatalf("append did not invalidate cache: %+v, %v", usage, err)
	}

	// Replacement with a new inode is detected even at identical size/mtime.
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	usage, err = ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil || usage.Tokens != 100 {
		t.Fatalf("rewrite = %+v, %v", usage, err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "new-generation.jsonl")
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(base, ":100", ":200")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	usage, err = ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil || usage.Tokens != 200 {
		t.Fatalf("replacement = %+v, %v", usage, err)
	}
}

func TestGeminiAccountingReadBounds(t *testing.T) {
	path := writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n")
	if err := os.Truncate(path, geminiTranscriptMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if usage, err := ReadLatestTranscriptUsage(path); err == nil || usage != nil {
		t.Fatalf("oversized file = %+v, %v", usage, err)
	}

	line := fmt.Sprintf(`{"id":"huge","type":"user","content":%q}`, strings.Repeat("x", geminiTranscriptMaxLine))
	path = writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n"+line+"\n")
	if usage, err := ReadLatestTranscriptUsage(path); err == nil || usage != nil {
		t.Fatalf("oversized record = %+v, %v", usage, err)
	}

	var data strings.Builder
	data.WriteString(`{"sessionId":"s","projectHash":"p","messages":[`)
	for i := 0; i <= geminiTranscriptMaxMessages; i++ {
		if i > 0 {
			data.WriteByte(',')
		}
		fmt.Fprintf(&data, `{"id":"%d","type":"user"}`, i)
	}
	data.WriteString(`]}`)
	path = writeGeminiAccountingFixture(t, "json", data.String())
	if usage, err := ReadLatestTranscriptUsage(path); err == nil || usage != nil {
		t.Fatalf("oversized collection = %+v, %v", usage, err)
	}
}

func TestGeminiAccountingCacheConcurrentAndBounded(t *testing.T) {
	path := writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n"+geminiAccountingMessage("a", 100)+"\n")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				usage, err := ReadLatestTranscriptUsage(path)
				if err != nil || usage == nil || usage.Tokens != 100 {
					t.Errorf("concurrent read = %+v, %v", usage, err)
					return
				}
				usage.Tokens = 7
			}
		}()
	}
	wg.Wait()
	for i := 0; i < geminiTranscriptCacheSize+5; i++ {
		path := writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n"+geminiAccountingMessage("a", 100)+"\n")
		if _, err := ReadLatestTranscriptUsage(path); err != nil {
			t.Fatal(err)
		}
	}
	geminiUsageCache.Lock()
	count := len(geminiUsageCache.entries)
	geminiUsageCache.Unlock()
	if count > geminiTranscriptCacheSize {
		t.Fatalf("cache has %d entries", count)
	}
}

func TestGeminiReaderPreservesOtherProviderAccounting(t *testing.T) {
	cases := []struct {
		name, line string
		want       int
	}{
		{"claude", `{"type":"assistant","message":{"model":"claude-test","usage":{"input_tokens":100,"cache_read_input_tokens":200,"output_tokens":3}}}`, 303},
		{"codex", `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":5,"total_tokens":105},"model_context_window":1000}}}`, 105},
		{"omp", `{"type":"message","message":{"role":"assistant","model":"model","provider":"provider","usage":{"input":10,"cacheRead":50,"output":5,"totalTokens":65}}}`, 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeGeminiAccountingFixture(t, "jsonl", tc.line+"\n")
			usage, err := ReadLatestTranscriptUsage(path)
			if err != nil || usage == nil || usage.Tokens != tc.want {
				t.Fatalf("%s = %+v, %v", tc.name, usage, err)
			}
		})
	}
}

func TestGeminiWorkspaceNativeAccounting(t *testing.T) {
	for _, layout := range []string{"hashed", "registry"} {
		t.Run(layout, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cwd))))
			slug := hash
			if layout == "registry" {
				slug = "workspace-registered"
			}
			chats := filepath.Join(home, ".gemini", "tmp", slug, "chats")
			if err := os.MkdirAll(chats, 0700); err != nil {
				t.Fatal(err)
			}
			if layout == "registry" {
				registry, err := json.Marshal(map[string]any{"projects": map[string]string{cwd: slug}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".gemini", "projects.json"), registry, 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(chats, "session-main.jsonl")
			if err := os.WriteFile(path, []byte(geminiAccountingHeader+"\n"+geminiAccountingMessage("a", 8100)+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, agentType := range []string{"gemini", "gmi"} {
				usage, ok := LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
				if !ok || usage == nil || usage.Tokens != 8100 || usage.Path != path || usage.ProviderSessionID != "gemini-accounting-session" {
					t.Fatalf("%s/%s native usage = %+v, %v", layout, agentType, usage, ok)
				}
			}
			if usage, ok := LatestAgentTranscriptUsage("agy", cwd, time.Time{}); ok || usage != nil {
				t.Fatal("Gemini and Antigravity must remain distinct")
			}
			if usage, ok := LatestAgentTranscriptUsage("gemini", cwd+"-other", time.Time{}); ok || usage != nil {
				t.Fatal("other workspace acquired this conversation")
			}
			if usage, ok := LatestAgentTranscriptUsage("gemini", cwd, time.Now().Add(time.Hour)); ok || usage != nil {
				t.Fatal("explicit mtime bound was ignored")
			}

			// A newer unaccountable conversation must not fall back to an older
			// convenient reading, or a subagent's tiny context.
			for _, data := range []string{
				`{"sessionId":"new","projectHash":"h","kind":"subagent","messages":[` + geminiAccountingMessage("sub", 5) + `]}`,
				`{"sessionId":"new","projectHash":"h","messages":[`,
			} {
				newer := filepath.Join(chats, "session-new.json")
				if err := os.WriteFile(newer, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				later := time.Now().Add(time.Second)
				if err := os.Chtimes(newer, later, later); err != nil {
					t.Fatal(err)
				}
				if usage, ok := LatestAgentTranscriptUsage("gemini", cwd, time.Time{}); ok || usage != nil {
					t.Fatalf("unaccountable latest session produced %+v", usage)
				}
			}
		})
	}
}

func TestGeminiNativeDiscoveryScopeAndCancellation(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cancelled, cancel := stdcontext.WithCancel(stdcontext.Background())
	cancel()
	for _, tc := range []struct {
		name      string
		ctx       stdcontext.Context
		home, cwd string
	}{
		{"nil context", nil, home, cwd},
		{"cancelled", cancelled, home, cwd},
		{"empty home", stdcontext.Background(), "", cwd},
		{"empty cwd", stdcontext.Background(), home, ""},
		{"relative cwd", stdcontext.Background(), home, "relative"},
		{"missing workspace", stdcontext.Background(), home, cwd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if path, ok := agentsession.FindGeminiTranscriptForCwd(tc.ctx, tc.home, tc.cwd, time.Time{}); ok || path != "" {
				t.Fatalf("discovery = %q, %v", path, ok)
			}
		})
	}
}

func TestGeminiAccountingBindsProviderSessionIdentity(t *testing.T) {
	path := writeGeminiAccountingFixture(t, "jsonl", geminiAccountingHeader+"\n"+geminiAccountingMessage("a", 400)+"\n")
	for _, tc := range []struct {
		name, provider, id string
		want               bool
	}{
		{"exact Gemini binding", "gemini", "gemini-accounting-session", true},
		{"different session", "gemini", "other", false},
		{"missing session", "gemini", "", false},
		{"different provider", "codex", "gemini-accounting-session", false},
		{"missing provider", "", "gemini-accounting-session", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, ok := readBoundTranscriptUsage(&agentsession.Info{SourcePath: path, Provider: tc.provider, SessionID: tc.id})
			if ok != tc.want || (ok && (usage == nil || usage.Tokens != 400)) {
				t.Fatalf("bound usage = %+v, %v", usage, ok)
			}
		})
	}
	// A path retained by a prior discovery must not silently follow replacement
	// by another conversation, even though the filename stayed unchanged.
	replacement := strings.ReplaceAll(geminiAccountingHeader, "gemini-accounting-session", "replacement-session") + "\n" + geminiAccountingMessage("a", 500) + "\n"
	if err := os.WriteFile(path, []byte(replacement), 0600); err != nil {
		t.Fatal(err)
	}
	if usage, ok := readBoundTranscriptUsage(&agentsession.Info{SourcePath: path, Provider: "gemini", SessionID: "gemini-accounting-session"}); ok || usage != nil {
		t.Fatalf("replacement rebound old session: %+v", usage)
	}
	if usage, ok := readBoundTranscriptUsage(nil); ok || usage != nil {
		t.Fatal("nil binding produced usage")
	}
}

func TestGeminiAccountingPaneAttribution(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cwd))))
	chats := filepath.Join(home, ".gemini", "tmp", hash, "chats")
	if err := os.MkdirAll(chats, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chats, "session-main.jsonl")
	if err := os.WriteFile(path, []byte(geminiAccountingHeader+"\n"+geminiAccountingMessage("a", 700)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	byCwd := func(agentType, cwd string) (*TranscriptUsage, bool) {
		return LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
	}
	panes := []PaneTranscriptRef{{PaneID: "%1", AgentType: "gemini", Cwd: cwd, PID: 10}}
	got := AttributePaneTranscripts(panes, nil, nil, byCwd)
	if len(got) != 1 || got["%1"].Tokens != 700 {
		t.Fatalf("unique Gemini pane = %+v", got)
	}
	panes = append(panes, PaneTranscriptRef{PaneID: "%2", AgentType: "gemini", Cwd: cwd, PID: 20})
	if got := AttributePaneTranscripts(panes, nil, nil, byCwd); len(got) != 0 {
		t.Fatalf("ambiguous shared-directory attribution = %+v", got)
	}
	byProcess := func(agentType string, pid int) (*TranscriptUsage, bool) {
		if agentType != "gemini" || pid != 10 {
			return nil, false
		}
		return readBoundTranscriptUsage(&agentsession.Info{SourcePath: path, Provider: "gemini", SessionID: "gemini-accounting-session"})
	}
	got = AttributePaneTranscripts(panes, nil, byProcess, byCwd)
	if len(got) != 1 || got["%1"].Tokens != 700 || got["%2"] != nil {
		t.Fatalf("exact process binding must not resolve ambiguous sibling: %+v", got)
	}
}
