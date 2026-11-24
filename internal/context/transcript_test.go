package context

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRotationTranscriptRequiresFreshAssistantResponse(t *testing.T) {
	t.Parallel()
	const start, end = "NTM_START_request", "NTM_END_request"
	const body = "## CURRENT TASK\nFinish recovery.\n## PROGRESS\nThe receipt is durable."
	response := start + "\n" + body + "\n" + end
	for _, provider := range []string{"claude", "codex", "omp", "pi"} {
		t.Run(provider, func(t *testing.T) {
			r, _ := rotationTranscriptFixture(t, provider, rotationTranscriptRecord(t, provider, "assistant", response))
			if got := r.summary(t.Context(), start, end); got != "" {
				t.Fatalf("accepted a pre-request response: %q", got)
			}
			appendRotationTranscript(t, r, rotationTranscriptRecord(t, provider, "user", response))
			if got := r.summary(t.Context(), start, end); got != "" {
				t.Fatalf("accepted a user message as handoff: %q", got)
			}
			appendRotationTranscript(t, r, rotationTranscriptRecord(t, provider, "assistant", response))
			if got := r.summary(t.Context(), start, end); got != body {
				t.Fatalf("fresh assistant summary = %q, want %q", got, body)
			}
		})
	}
}

func TestRotationTranscriptRejectsUncertainEvidence(t *testing.T) {
	t.Parallel()
	const start, end = "NTM_START_request", "NTM_END_request"
	const body = "## CURRENT TASK\nFinish recovery.\n## PROGRESS\nThe receipt is durable."
	response := start + "\n" + body + "\n" + end
	for _, scenario := range []string{
		"tool_output", "reasoning", "sidechain", "split_messages", "unfinished_record", "old_partial_record",
		"changed_session", "weaker_binding", "replaced_inode", "truncated", "corrupt_record", "superseded", "unfinished_later_record", "canceled", "outside_read_bound",
	} {
		t.Run(scenario, func(t *testing.T) {
			record := rotationTranscriptRecord(t, "claude", "assistant", response)
			initial := "{}\n"
			if scenario == "old_partial_record" {
				initial = record[:len(record)/2]
			}
			r, binding := rotationTranscriptFixture(t, "claude", initial)
			ctx := t.Context()
			switch scenario {
			case "tool_output":
				record = rotationTranscriptRecord(t, "claude", "tool", response)
			case "reasoning":
				record = strings.Replace(record, `"type":"text"`, `"type":"thinking"`, 1)
			case "sidechain":
				record = `{"isSidechain":true,` + record[1:]
			case "split_messages":
				record = rotationTranscriptRecord(t, "claude", "assistant", start+"\n"+body) + rotationTranscriptRecord(t, "claude", "assistant", end)
			case "unfinished_record":
				record = strings.TrimSuffix(record, "\n")
			case "old_partial_record":
				record = record[len(record)/2:]
			case "changed_session":
				binding.SessionID = "another-conversation"
			case "weaker_binding":
				binding.Source = agentsession.DiscoverySourceNativeStore
			case "replaced_inode":
				if err := os.Rename(binding.SourcePath, binding.SourcePath+".previous"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(binding.SourcePath, []byte(initial), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(binding.SourcePath, 0); err != nil {
					t.Fatal(err)
				}
				record = ""
			case "corrupt_record":
				record += "{broken JSON}\n"
			case "superseded":
				record += rotationTranscriptRecord(t, "claude", "assistant", "A later response has no handoff markers.")
			case "unfinished_later_record":
				record += `{"type":"assistant","message":`
			case "canceled":
				var cancel stdcontext.CancelFunc
				ctx, cancel = stdcontext.WithCancel(ctx)
				cancel()
			case "outside_read_bound":
				record += `{"type":"tool_result","text":"` + strings.Repeat("x", transcriptScanLimit) + "\"}\n"
			}
			appendRotationTranscript(t, r, record)
			if got := r.summary(ctx, start, end); got != "" {
				t.Fatalf("accepted %s as retirement evidence: %q", scenario, got)
			}
		})
	}
}

func TestRotationTranscriptDiscoveryFailsClosed(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"remote", "unbound", "workspace_guess", "missing_id", "wrong_provider", "no_path", "missing_file", "nonregular"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "conversation.jsonl")
			if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			binding := &agentsession.Info{SessionID: "exact-session", Provider: "claude", SourcePath: path, Source: agentsession.DiscoverySourceProcessTree}
			local := true
			calls := 0
			switch scenario {
			case "remote":
				local = false
			case "unbound":
				binding = nil
			case "workspace_guess":
				binding.Source = agentsession.DiscoverySourceNativeStore
			case "missing_id":
				binding.SessionID = ""
			case "wrong_provider":
				binding.Provider = "codex"
			case "no_path":
				binding.SourcePath = ""
			case "missing_file":
				binding.SourcePath = path + ".missing"
			case "nonregular":
				binding.SourcePath = filepath.Dir(path)
			}
			reader := openRotationTranscript(t.Context(), tmux.Pane{ID: "%1", PID: 123, Type: tmux.AgentClaude}, local,
				func(stdcontext.Context, string, int) *agentsession.Info { calls++; return binding })
			if reader != nil {
				_ = reader.file.Close()
				t.Fatal("uncertain discovery enabled transcript fallback")
			}
			if scenario == "remote" && calls != 0 {
				t.Fatal("a remote pane inspected a local PID")
			}
		})
	}
}

func TestRotationTranscriptCodexEventAndTextBlocks(t *testing.T) {
	t.Parallel()
	const start, end = "NTM_START_request", "NTM_END_request"
	const body = "## CURRENT TASK\nFinish recovery.\n## PROGRESS\nThe receipt is durable."
	r, _ := rotationTranscriptFixture(t, "codex", "{}\n")
	event, err := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": start + "\n" + body + "\n" + end}})
	if err != nil {
		t.Fatal(err)
	}
	appendRotationTranscript(t, r, string(event)+"\n")
	appendRotationTranscript(t, r, "{\"type\":\"event_msg\",\"payload\":{\"type\":\"diagnostic\",\"message\":{\"details\":\"not an assistant reply\"}}}\n")
	if got := r.summary(t.Context(), start, end); got != body {
		t.Fatalf("Codex assistant event = %q", got)
	}
	// A request or a tool result that quotes the same event is not an event.
	user := rotationTranscriptRecord(t, "codex", "user", string(event))
	if text, assistant, valid := rotationTranscriptMessage([]byte(user), "codex"); !valid || assistant || text != "" {
		t.Fatalf("nested event escaped its user-message boundary: %q %v %v", text, assistant, valid)
	}
}

func rotationTranscriptFixture(t *testing.T, provider, initial string) (*rotationTranscript, *agentsession.Info) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	binding := &agentsession.Info{SessionID: "exact-session", Provider: provider, SourcePath: path, Source: agentsession.DiscoverySourceProcessTree}
	typ := map[string]tmux.AgentType{"claude": tmux.AgentClaude, "codex": tmux.AgentCodex, "omp": tmux.AgentOmp, "pi": tmux.AgentPi}[provider]
	r := openRotationTranscript(t.Context(), tmux.Pane{ID: "%1", PID: 123, Type: typ, Command: provider}, true,
		func(ctx stdcontext.Context, agentType string, pid int) *agentsession.Info {
			if agentType != string(typ) || pid != 123 {
				t.Fatalf("transcript lookup changed its physical pane: %s %d", agentType, pid)
			}
			return binding
		})
	if r == nil {
		t.Fatal("could not pin test transcript")
	}
	t.Cleanup(func() { _ = r.file.Close() })
	return r, binding
}

func rotationTranscriptRecord(t *testing.T, provider, role, text string) string {
	t.Helper()
	message := map[string]any{"role": role, "content": []map[string]string{{"type": "text", "text": text}}}
	entry := map[string]any{"type": "message", "message": message}
	if provider == "claude" {
		entry["type"] = role
	} else if provider == "codex" {
		message["type"] = "message"
		message["content"] = []map[string]string{{"type": "output_text", "text": text}}
		entry = map[string]any{"type": "response_item", "payload": message}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func appendRotationTranscript(t *testing.T, r *rotationTranscript, data string) {
	t.Helper()
	file, err := os.OpenFile(r.binding.SourcePath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMaxPaneContextUsageNativeEvidence(t *testing.T) {
	const ompGauge = "╭── 󰵗   Union Alpha   omp-probe   69.9%/262K 󰁨 ──────────────────󰁨──────╮\n╰─                                  ─╯"
	for _, tc := range []struct {
		name    string
		typ     tmux.AgentType
		visible string
		want    int
		known   bool
	}{
		{"gemini context", tmux.AgentGemini, "> Type your message or @path/to/file\n/model context\ngemini-pro 81% used", 81, true},
		{"measured zero", tmux.AgentGemini, "/model context\ngemini-pro 0% used", 0, true},
		{"gemini quota is not context", tmux.AgentGemini, "/model quota\ngemini-pro 99% used", 0, false},
		{"gemini stale footer", tmux.AgentGemini, "/model context\ngemini-pro 99% used\n> Type your message or @path/to/file", 0, false},
		{"omp live gauge", tmux.AgentOmp, ompGauge, 69, true},
		{"omp old composer", tmux.AgentOmp, ompGauge + "\nThe old frame above is an example.", 0, false},
		{"codex quoted percentage", tmux.AgentCodex, "Earlier output said 1% context left\n›", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, known := maxPaneContextUsage(t.Context(), []tmux.Pane{{ID: "%9", Type: tc.typ}}, 60, true,
				func(stdcontext.Context, string) (string, error) { return tc.visible, nil },
				func(stdcontext.Context, string, int) (*TranscriptUsage, bool) {
					t.Fatal("a pane without a PID must not use local transcript discovery")
					return nil, false
				}, time.Now)
			if used != tc.want || known != tc.known {
				t.Fatalf("native reading = (%d, %t), want (%d, %t)", used, known, tc.want, tc.known)
			}
		})
	}
}

func TestFreshTranscriptUsagePercentRequiresMeasuredWindowAndFreshness(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		usage TranscriptUsage
		want  float64
		known bool
	}{
		{"native window", TranscriptUsage{Tokens: 700, ContextWindow: 1000, UpdatedAt: now}, 70, true},
		{"known native model", TranscriptUsage{Tokens: 140000, Model: "claude-opus-4", UpdatedAt: now}, 70, true},
		{"zero is measured", TranscriptUsage{ContextWindow: 1000, UpdatedAt: now}, 0, true},
		{"window exceeded", TranscriptUsage{Tokens: 1400, ContextWindow: 1000, UpdatedAt: now}, 100, true},
		{"stale", TranscriptUsage{Tokens: 900, ContextWindow: 1000, UpdatedAt: now.Add(-TranscriptFreshness)}, 0, false},
		{"future", TranscriptUsage{Tokens: 900, ContextWindow: 1000, UpdatedAt: now.Add(time.Second)}, 0, false},
		{"no timestamp", TranscriptUsage{Tokens: 900, ContextWindow: 1000}, 0, false},
		{"unknown model", TranscriptUsage{Tokens: 90000, Model: "unknown-provider-model", UpdatedAt: now}, 0, false},
		{"no denominator", TranscriptUsage{Tokens: 90000, UpdatedAt: now}, 0, false},
		{"negative usage", TranscriptUsage{Tokens: -1, ContextWindow: 1000, UpdatedAt: now}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := freshTranscriptUsagePercent(&tc.usage, now)
			if got != tc.want || known != tc.known {
				t.Fatalf("transcript reading = (%v, %t), want (%v, %t)", got, known, tc.want, tc.known)
			}
		})
	}
}

func TestMaxPaneContextUsageSelectedPhysicalPanesAndEarlyStop(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "1", Type: tmux.AgentGemini}, // window-local index is not identity
		{ID: "%bad", Type: tmux.AgentGemini},
		{ID: "%1", Type: tmux.AgentGemini, Dead: true},
		{ID: "%2", Type: tmux.AgentGemini, Service: "cass"},
		{ID: "%3", Type: tmux.AgentUser},
		{ID: "%11", Index: 1, WindowIndex: 0, Type: tmux.AgentGemini, PID: 100},
		{ID: "%11", Index: 1, WindowIndex: 0, Type: tmux.AgentGemini, PID: 100},
		{ID: "%12", Index: 1, WindowIndex: 1, Type: tmux.AgentCodex, PID: 200},
		{ID: "%13", Index: 2, WindowIndex: 1, Type: tmux.AgentGemini, PID: 300},
	}
	var captures []string
	var transcripts []int
	used, known := maxPaneContextUsage(t.Context(), panes, 60, true,
		func(_ stdcontext.Context, id string) (string, error) {
			captures = append(captures, id)
			return "/model context\ngemini-pro 20% used", nil
		},
		func(_ stdcontext.Context, typ string, pid int) (*TranscriptUsage, bool) {
			transcripts = append(transcripts, pid)
			if typ != string(tmux.AgentCodex) || pid != 200 {
				t.Fatalf("measurement reached wrong process: %s/%d", typ, pid)
			}
			return &TranscriptUsage{Tokens: 730, ContextWindow: 1000, UpdatedAt: time.Now()}, true
		}, time.Now)
	if used != 73 || !known || fmt.Sprint(captures) != "[%11]" || fmt.Sprint(transcripts) != "[200]" {
		t.Fatalf("selected max = (%d, %t), captures=%v transcripts=%v", used, known, captures, transcripts)
	}
}

func TestMaxPaneContextUsageRemoteNeverUsesLocalProcess(t *testing.T) {
	for _, typ := range []tmux.AgentType{tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini, tmux.AgentOmp} {
		used, known := maxPaneContextUsage(t.Context(), []tmux.Pane{{ID: "%7", Type: typ, PID: os.Getpid()}}, 60, false,
			func(stdcontext.Context, string) (string, error) { return "unknown visible frame", nil },
			func(stdcontext.Context, string, int) (*TranscriptUsage, bool) {
				t.Fatal("remote pane PID was looked up on the local machine")
				return nil, false
			}, time.Now)
		if used != 0 || known {
			t.Fatalf("remote %s without native evidence = (%d, %t)", typ, used, known)
		}
	}
}

func TestMaxPaneContextUsageExactProcessTranscripts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exact process fixture exercises Linux /proc file-descriptor binding")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	cwd := filepath.Join(root, "project")
	sessions := filepath.Join(root, ".codex", "sessions")
	for _, dir := range []string{cwd, sessions} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	start := func(id string, tokens int, at time.Time) (int, string) {
		t.Helper()
		path := writeTranscript(t, sessions, "rollout-"+id+".jsonl",
			fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`, id, cwd),
			fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"total_tokens":%d},"model_context_window":1000}}}`, at.UTC().Format(time.RFC3339Nano), tokens, tokens))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd := exec.Command("sleep", "60")
		cmd.Args[0], cmd.Dir, cmd.ExtraFiles = "codex", cwd, []*os.File{f}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid, path
	}
	now := time.Now()
	coolPID, coolPath := start("11111111-1111-4111-8111-111111111111", 200, now.Add(-time.Minute))
	hotPID, hotPath := start("22222222-2222-4222-8222-222222222222", 900, now)
	cool := tmux.Pane{ID: "%8", Index: 1, WindowIndex: 0, Type: tmux.AgentCodex, PID: coolPID}
	hot := tmux.Pane{ID: "%9", Index: 1, WindowIndex: 1, Type: tmux.AgentCodex, PID: hotPID}
	for _, tc := range []struct {
		name  string
		panes []tmux.Pane
		want  int
	}{
		{"cool selected, newer hot unselected", []tmux.Pane{cool}, 20},
		{"hot selected", []tmux.Pane{hot}, 90},
		{"shared broadcast", []tmux.Pane{cool, hot}, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, known, err := MaxPaneContextUsage(t.Context(), tmux.NewClient(""), tc.panes, 60)
			if err != nil || !known || used != tc.want {
				t.Fatalf("usage=(%d,%t,%v), want %d", used, known, err, tc.want)
			}
		})
	}
	for pid, path := range map[int]string{coolPID: coolPath, hotPID: hotPath} {
		usage, ok := ProcessTranscriptUsage(t.Context(), "codex", pid)
		if !ok || usage.Path != path {
			t.Fatalf("PID %d read another pane's transcript: %+v, found=%t, want %s", pid, usage, ok, path)
		}
	}
	used, known, err := MaxPaneContextUsage(t.Context(), tmux.NewClient("remote-host"), []tmux.Pane{hot}, 60)
	if err != nil || known || used != 0 {
		t.Fatalf("remote PID reused local evidence: (%d,%t,%v)", used, known, err)
	}
	ctx, cancel := stdcontext.WithCancel(t.Context())
	cancel()
	if usage, ok := ProcessTranscriptUsage(ctx, "codex", hotPID); ok || usage != nil {
		t.Fatalf("canceled discovery returned %+v", usage)
	}
}

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

func TestPiStatusBarUsage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 17, 5, 10, 0, 0, time.UTC)
	capture := "────────────────────────────────────────\n\n────────────────────────────────────────\n" +
		"~/repo (main)\n↑1.2k ↓500 12.5%/200k (auto)  (anthropic) claude-sonnet-4 • high\n"
	u, ok := PiStatusBarUsage(capture, at)
	if !ok || u.Tokens != 25000 || u.ContextWindow != 200000 || !u.UpdatedAt.Equal(at) || u.Path != "" {
		t.Fatalf("PiStatusBarUsage = (%+v, %v), want tokens=25000 window=200000", u, ok)
	}
	unknown := "────────────────────────────────────────\n\n────────────────────────────────────────\n" +
		"~/repo (main)\n↑0 ↓0 ?/200k (auto)  (anthropic) claude-sonnet-4 • high\n"
	if _, ok := PiStatusBarUsage(unknown, at); ok {
		t.Error("unknown ?/200k pi gauge must not produce a reading")
	}
	if _, ok := PiStatusBarUsage("● Done.\n\n❯ \n", at); ok {
		t.Error("a non-pi capture must not produce a gauge reading")
	}
}

func TestAttributePaneTranscripts(t *testing.T) {
	reading := func(path string) *TranscriptUsage { return &TranscriptUsage{Path: path, Tokens: 1} }
	gauge := reading("gauge:%9")
	byProcess := func(agentType string, pid int) (*TranscriptUsage, bool) {
		if agentType == "claude" && pid == 11 {
			return reading("session-of-pid-11"), true
		}
		return nil, false
	}
	cwdCalls := map[string]int{}
	byCwd := func(agentType, cwd string) (*TranscriptUsage, bool) {
		cwdCalls[agentType+"@"+cwd]++
		return reading("newest:" + agentType + "@" + cwd), true
	}
	panes := []PaneTranscriptRef{
		// Swarm directory: three Claude panes, one bound through its process.
		{PaneID: "%1", AgentType: "claude", PID: 11, Cwd: "/swarm"},
		{PaneID: "%2", AgentType: "claude", PID: 12, Cwd: "/swarm"},
		{PaneID: "%3", AgentType: "claude", PID: 13, Cwd: "/swarm"},
		// Sole pane of its type in a directory: the cwd rule applies.
		{PaneID: "%4", AgentType: "codex", Cwd: "/swarm"},
		// A bound pane alone in its directory keeps its exact reading; the
		// cwd fallback is neither consulted nor allowed to override it.
		{PaneID: "%5", AgentType: "claude", PID: 11, Cwd: "/solo"},
		// A pane with a known reading counts toward its directory: its
		// unbound omp sibling may be looking at the gauged pane's transcript.
		{PaneID: "%9", AgentType: "omp", Cwd: "/omp"},
		{PaneID: "%10", AgentType: "omp", Cwd: "/omp"},
		// No cwd and no binding: nothing to read.
		{PaneID: "%11", AgentType: "claude"},
	}

	got := AttributePaneTranscripts(panes, map[string]*TranscriptUsage{"%9": gauge}, byProcess, byCwd)

	want := map[string]string{
		"%1": "session-of-pid-11",
		"%4": "newest:codex@/swarm",
		"%5": "session-of-pid-11",
		"%9": "gauge:%9",
	}
	if len(got) != len(want) {
		t.Errorf("attributed %d panes, want %d: %+v", len(got), len(want), got)
	}
	for paneID, path := range want {
		if got[paneID] == nil || got[paneID].Path != path {
			t.Errorf("pane %s = %+v, want reading %q", paneID, got[paneID], path)
		}
	}
	for _, paneID := range []string{"%2", "%3", "%10", "%11"} {
		if got[paneID] != nil {
			t.Errorf("pane %s was handed %q; an ambiguous or unlocatable pane must get no reading", paneID, got[paneID].Path)
		}
	}
	// Only unambiguous, unresolved groups cost a lookup.
	if len(cwdCalls) != 1 || cwdCalls["codex@/swarm"] != 1 {
		t.Errorf("cwd lookups = %v, want exactly one for codex@/swarm", cwdCalls)
	}

	// Without a cwd lookup, process bindings and known readings still apply.
	got = AttributePaneTranscripts(panes, nil, byProcess, nil)
	if len(got) != 2 || got["%1"] == nil || got["%5"] == nil {
		t.Errorf("process-only attribution = %+v, want %%1 and %%5", got)
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

func TestReadLatestTranscriptUsage_PiModelChangeInvalidatesPriorModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi-session.jsonl")
	lines := strings.Join([]string{
		`{"type":"session","version":3,"id":"pi-1","cwd":"/work/proj"}`,
		`{"type":"message","message":{"role":"assistant","provider":"anthropic","model":"claude-sonnet-4-5","usage":{"input":1000,"output":200,"cacheRead":0,"cacheWrite":0,"totalTokens":1200}}}`,
		`{"type":"model_change","provider":"openai","modelId":"gpt-5.4"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err := ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatalf("ReadLatestTranscriptUsage: %v", err)
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil after Pi model_change to a different model without subsequent usage", usage)
	}

	// Subsequent usage on the new model must be attributed to openai/gpt-5.4.
	lines += `{"type":"message","message":{"role":"assistant","provider":"openai","model":"gpt-5.4","usage":{"input":500,"output":100,"cacheRead":0,"cacheWrite":0,"totalTokens":600}}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	usage, err = ReadLatestTranscriptUsage(path)
	if err != nil {
		t.Fatalf("ReadLatestTranscriptUsage after new turn: %v", err)
	}
	if usage == nil || usage.Model != "openai/gpt-5.4" || usage.InputTokens != 500 {
		t.Fatalf("usage = %+v, want model=openai/gpt-5.4 input=500", usage)
	}
}
