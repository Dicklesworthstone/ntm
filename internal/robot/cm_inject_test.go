package robot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// --- fake MCP daemon scaffolding (mirrors internal/cm/client_test.go) ---

type cmRPCRequest struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type cmToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func cmWriteRPCResult(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Errorf("encode rpc result: %v", err)
	}
}

func cmWriteToolText(t *testing.T, w http.ResponseWriter, id any, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal tool payload: %v", err)
	}
	cmWriteRPCResult(t, w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(data)}},
		"isError": false,
	})
}

func cmWriteToolError(t *testing.T, w http.ResponseWriter, id any, msg string) {
	t.Helper()
	cmWriteRPCResult(t, w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	})
}

// newFakeCMDaemon starts an MCP-speaking fake cm daemon and returns it with
// its port. tools maps MCP tool names to handlers.
func newFakeCMDaemon(t *testing.T, tools map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage)) (*httptest.Server, int) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req cmRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode rpc request: %v (body=%s)", err, string(body))
			return
		}
		switch req.Method {
		case "tools/list":
			cmWriteRPCResult(t, w, req.ID, map[string]any{"tools": []any{}})
		case "tools/call":
			var params cmToolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				t.Errorf("decode tools/call params: %v", err)
				return
			}
			handler, ok := tools[params.Name]
			if !ok {
				t.Errorf("unexpected tool call: %s", params.Name)
				return
			}
			handler(t, w, req.ID, params.Arguments)
		default:
			t.Errorf("unexpected rpc method: %s", req.Method)
		}
	}))
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse fake daemon URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse fake daemon port: %v", err)
	}
	return ts, port
}

func cmContextPayload(rules ...cm.Rule) map[string]any {
	return map[string]any{
		"relevantBullets":      rules,
		"antiPatterns":         []cm.Rule{},
		"historySnippets":      []cm.HistorySnippet{},
		"suggestedCassQueries": []string{},
	}
}

// unavailableCMConfig returns a config whose daemon discovery and CLI
// fallback are both guaranteed to fail, regardless of what is installed on
// the machine running the tests.
func unavailableCMConfig(t *testing.T) CMInjectConfig {
	t.Helper()
	cfg := DefaultCMInjectConfig()
	cfg.ProjectDir = t.TempDir()
	cfg.CLIBinary = filepath.Join(cfg.ProjectDir, "definitely-not-cm")
	return cfg
}

// --- formatting / budget / top-N ---

func TestFormatCMRulesBlock_Basic(t *testing.T) {
	rules := []cm.Rule{
		{ID: "r1", Content: "Always run gofmt before committing"},
		{ID: "r2", Content: "Prefer table-driven tests"},
	}
	block, ids := formatCMRulesBlock(rules, 5, 1500)
	if !strings.HasPrefix(block, "## Project rules\n\n") {
		t.Fatalf("block missing header: %q", block)
	}
	if !strings.Contains(block, "- [r1] Always run gofmt before committing\n") {
		t.Errorf("block missing rule r1: %q", block)
	}
	if !strings.Contains(block, "- [r2] Prefer table-driven tests\n") {
		t.Errorf("block missing rule r2: %q", block)
	}
	if len(ids) != 2 || ids[0] != "r1" || ids[1] != "r2" {
		t.Errorf("ids = %v, want [r1 r2]", ids)
	}
}

func TestFormatCMRulesBlock_TopNCap(t *testing.T) {
	var rules []cm.Rule
	for i := 0; i < 10; i++ {
		rules = append(rules, cm.Rule{ID: fmt.Sprintf("r%d", i), Content: fmt.Sprintf("rule number %d", i)})
	}
	block, ids := formatCMRulesBlock(rules, 3, 1500)
	if len(ids) != 3 {
		t.Fatalf("len(ids) = %d, want 3 (top-N cap)", len(ids))
	}
	if strings.Contains(block, "[r3]") {
		t.Errorf("block should not contain rule beyond top-N: %q", block)
	}
}

func TestFormatCMRulesBlock_TokenBudget(t *testing.T) {
	long := strings.Repeat("verbose guidance ", 50) // ~850 chars => ~212 tokens per rule
	rules := []cm.Rule{
		{ID: "fits", Content: long},
		{ID: "cut", Content: long},
	}
	// Budget fits header + one rule (~217 tokens) but not two.
	block, ids := formatCMRulesBlock(rules, 5, 300)
	if len(ids) != 1 || ids[0] != "fits" {
		t.Fatalf("ids = %v, want exactly [fits] under budget", ids)
	}
	if strings.Contains(block, "[cut]") {
		t.Errorf("over-budget rule leaked into block")
	}
}

func TestFormatCMRulesBlock_BudgetTooSmall(t *testing.T) {
	rules := []cm.Rule{{ID: "r1", Content: strings.Repeat("x", 400)}}
	block, ids := formatCMRulesBlock(rules, 5, 10)
	if block != "" || ids != nil {
		t.Fatalf("expected empty result for impossible budget, got block=%q ids=%v", block, ids)
	}
}

func TestFormatCMRulesBlock_EmptyIDInjectedButNotReported(t *testing.T) {
	rules := []cm.Rule{
		{ID: "  ", Content: "anonymous guidance"},
		{ID: "r2", Content: "named guidance"},
	}
	block, ids := formatCMRulesBlock(rules, 5, 1500)
	if !strings.Contains(block, "- anonymous guidance\n") {
		t.Errorf("ID-less rule missing from block: %q", block)
	}
	if !strings.Contains(block, "- [r2] named guidance\n") {
		t.Errorf("named rule missing from block: %q", block)
	}
	// rules_injected feeds the automatic cm_outcome report; an empty-string
	// rule ID must never appear there.
	if len(ids) != 1 || ids[0] != "r2" {
		t.Errorf("ids = %q, want exactly [r2] (no empty IDs)", ids)
	}

	// An ID-less rule still counts against the top-N cap.
	block, ids = formatCMRulesBlock(rules, 1, 1500)
	if strings.Contains(block, "[r2]") {
		t.Errorf("maxRules=1 should stop after the first injected rule: %q", block)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %q, want none for a lone ID-less rule", ids)
	}
	if block == "" {
		t.Error("block should still be injected when the only rule has no ID")
	}
}

func TestFormatCMRulesBlock_CollapsesMultilineContent(t *testing.T) {
	rules := []cm.Rule{{ID: "r1", Content: "line one\nline two\n\tline three"}}
	block, _ := formatCMRulesBlock(rules, 5, 1500)
	if !strings.Contains(block, "- [r1] line one line two line three\n") {
		t.Errorf("multi-line content not collapsed: %q", block)
	}
}

// --- injection behavior against the fake daemon ---

func TestInjectCMRules_PrependsRulesBlock(t *testing.T) {
	var gotTask, gotWorkspace string
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			var parsed struct {
				Task      string `json:"task"`
				Workspace string `json:"workspace"`
			}
			_ = json.Unmarshal(args, &parsed)
			gotTask = parsed.Task
			gotWorkspace = parsed.Workspace
			cmWriteToolText(t, w, id, cmContextPayload(
				cm.Rule{ID: "rule-a", Content: "Run go vet"},
				cm.Rule{ID: "rule-b", Content: "Never commit secrets"},
			))
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Client = cm.NewPortClient(port, "sess-1")
	cfg.Workspace = "/work/dir"

	modified, info := InjectCMRules(context.Background(), "fix the auth bug", "fix the auth bug", cfg)

	if gotTask != "fix the auth bug" {
		t.Errorf("cm_context task = %q, want original message", gotTask)
	}
	if gotWorkspace != "/work/dir" {
		t.Errorf("cm_context workspace = %q, want /work/dir", gotWorkspace)
	}
	if !strings.HasPrefix(modified, "## Project rules\n\n") {
		t.Fatalf("modified message missing rules block: %q", modified)
	}
	if !strings.HasSuffix(modified, "\n---\n\nfix the auth bug") {
		t.Errorf("modified message missing separator + original: %q", modified)
	}
	if !info.Enabled {
		t.Error("info.Enabled = false, want true")
	}
	if info.SkippedReason != "" {
		t.Errorf("info.SkippedReason = %q, want empty", info.SkippedReason)
	}
	if len(info.RulesInjected) != 2 || info.RulesInjected[0] != "rule-a" || info.RulesInjected[1] != "rule-b" {
		t.Errorf("info.RulesInjected = %v, want [rule-a rule-b]", info.RulesInjected)
	}
	if info.TokensAdded <= 0 {
		t.Errorf("info.TokensAdded = %d, want > 0", info.TokensAdded)
	}
	if info.Source != "daemon" {
		t.Errorf("info.Source = %q, want daemon", info.Source)
	}
}

func TestInjectCMRules_TopNFromConfig(t *testing.T) {
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			cmWriteToolText(t, w, id, cmContextPayload(
				cm.Rule{ID: "r1", Content: "one"},
				cm.Rule{ID: "r2", Content: "two"},
				cm.Rule{ID: "r3", Content: "three"},
			))
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Client = cm.NewPortClient(port, "sess-1")
	cfg.MaxRules = 2

	_, info := InjectCMRules(context.Background(), "task", "task", cfg)
	if len(info.RulesInjected) != 2 {
		t.Fatalf("RulesInjected = %v, want 2 rules", info.RulesInjected)
	}
}

func TestInjectCMRules_DisabledByConfig(t *testing.T) {
	cfg := unavailableCMConfig(t)
	cfg.Enabled = false
	// A reachable client proves disabled short-circuits before any query.
	cfg.Client = cm.NewPortClient(1, "sess")

	modified, info := InjectCMRules(context.Background(), "task", "message body", cfg)
	if modified != "message body" {
		t.Errorf("message modified despite disabled config: %q", modified)
	}
	if info.Enabled {
		t.Error("info.Enabled = true, want false")
	}
	if !strings.Contains(info.SkippedReason, "disabled") {
		t.Errorf("SkippedReason = %q, want mention of disabled", info.SkippedReason)
	}
}

func TestInjectCMRules_UnavailableDegradesGracefully(t *testing.T) {
	cfg := unavailableCMConfig(t)

	modified, info := InjectCMRules(context.Background(), "task", "message body", cfg)
	if modified != "message body" {
		t.Errorf("message modified despite cm unavailable: %q", modified)
	}
	if info.SkippedReason == "" {
		t.Error("SkippedReason empty, want unavailability reason")
	}
	if len(info.RulesInjected) != 0 {
		t.Errorf("RulesInjected = %v, want none", info.RulesInjected)
	}
}

func TestInjectCMRules_QueryErrorDegradesGracefully(t *testing.T) {
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			cmWriteToolError(t, w, id, "index is locked")
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Client = cm.NewPortClient(port, "sess-1")

	modified, info := InjectCMRules(context.Background(), "task", "message body", cfg)
	if modified != "message body" {
		t.Errorf("message modified despite query error: %q", modified)
	}
	if !strings.Contains(info.SkippedReason, "query failed") {
		t.Errorf("SkippedReason = %q, want query failure", info.SkippedReason)
	}
}

func TestInjectCMRules_NoRelevantRules(t *testing.T) {
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			cmWriteToolText(t, w, id, cmContextPayload())
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Client = cm.NewPortClient(port, "sess-1")

	modified, info := InjectCMRules(context.Background(), "task", "message body", cfg)
	if modified != "message body" {
		t.Errorf("message modified despite empty context: %q", modified)
	}
	if !strings.Contains(info.SkippedReason, "no relevant rules") {
		t.Errorf("SkippedReason = %q, want no-rules reason", info.SkippedReason)
	}
}

// --- daemon discovery ---

func TestDiscoverCMDaemonClient(t *testing.T) {
	dir := t.TempDir()
	pidsDir := filepath.Join(dir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok := discoverCMDaemonClient(dir, "mysession"); ok {
		t.Fatal("discovered a daemon in an empty pids dir")
	}

	// Live PID (this test process) with a valid port.
	pidFile := filepath.Join(pidsDir, "cm-mysession.pid")
	writePID := func(pid, port int) {
		t.Helper()
		data, _ := json.Marshal(cm.PIDFileInfo{PID: pid, Port: port, StartedAt: time.Now()})
		if err := os.WriteFile(pidFile, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writePID(os.Getpid(), 8321)
	client, ok := discoverCMDaemonClient(dir, "mysession")
	if !ok {
		t.Fatal("live daemon pid file not discovered")
	}
	if got := client.Port(); got != 8321 {
		t.Errorf("client port = %d, want 8321", got)
	}

	// Dead PID must not be discovered.
	writePID(1<<30+12345, 8321)
	if _, ok := discoverCMDaemonClient(dir, "mysession"); ok {
		t.Error("discovered a daemon from a dead pid file")
	}
}

// --- outcome feedback ---

type outcomeCall struct {
	SessionID string   `json:"sessionId"`
	Outcome   string   `json:"outcome"`
	RulesUsed []string `json:"rulesUsed"`
	Notes     string   `json:"notes"`
}

func TestReportCMOutcome_SendsRuleIDs(t *testing.T) {
	var mu sync.Mutex
	var calls []outcomeCall
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_outcome": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			var call outcomeCall
			_ = json.Unmarshal(args, &call)
			mu.Lock()
			calls = append(calls, call)
			mu.Unlock()
			cmWriteToolText(t, w, id, map[string]any{"recorded": true})
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Outcome = cm.NewPortClient(port, "sess-42")

	reportCMOutcome(cfg, cm.OutcomeSuccess, []string{"rule-a", "rule-b"})

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("cm_outcome calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Outcome != "success" {
		t.Errorf("outcome = %q, want success", call.Outcome)
	}
	if call.SessionID != "sess-42" {
		t.Errorf("sessionId = %q, want sess-42", call.SessionID)
	}
	if len(call.RulesUsed) != 2 || call.RulesUsed[0] != "rule-a" || call.RulesUsed[1] != "rule-b" {
		t.Errorf("rulesUsed = %v, want [rule-a rule-b]", call.RulesUsed)
	}
}

func TestReportCMOutcome_NoRuleIDsIsNoop(t *testing.T) {
	called := false
	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_outcome": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			called = true
			cmWriteToolText(t, w, id, map[string]any{"recorded": true})
		},
	})

	cfg := unavailableCMConfig(t)
	cfg.Outcome = cm.NewPortClient(port, "sess")

	reportCMOutcome(cfg, cm.OutcomeSuccess, nil)
	if called {
		t.Error("cm_outcome called with no rule IDs")
	}
}

func TestReportCMOutcome_NoDaemonIsNoop(t *testing.T) {
	cfg := unavailableCMConfig(t)
	// No Outcome override and no daemon in ProjectDir: must silently no-op.
	reportCMOutcome(cfg, cm.OutcomeSuccess, []string{"rule-a"})
}

// --- outcome evidence gating (bd-3j6hm: confirmed ack => report; timeout/ambiguous => no call) ---

func TestShouldReportCMSendOutcome(t *testing.T) {
	injected := &CMInjectionInfo{Enabled: true, RulesInjected: []string{"r1"}}
	skipped := &CMInjectionInfo{Enabled: true, SkippedReason: "cm is not available"}

	cases := []struct {
		name          string
		withMemory    bool
		info          *CMInjectionInfo
		confirmations int
		timedOut      bool
		want          bool
	}{
		{"confirmed ack reports", true, injected, 1, false, true},
		{"multiple confirmations report", true, injected, 3, false, true},
		{"ack timeout is ambiguous", true, injected, 1, true, false},
		{"full timeout no confirmations", true, injected, 0, true, false},
		{"no confirmations no timeout", true, injected, 0, false, false},
		{"no rules injected", true, skipped, 1, false, false},
		{"nil info", true, nil, 1, false, false},
		{"memory not enabled", false, injected, 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldReportCMSendOutcome(tc.withMemory, tc.info, tc.confirmations, tc.timedOut)
			if got != tc.want {
				t.Errorf("shouldReportCMSendOutcome(%v, info, %d, %v) = %v, want %v",
					tc.withMemory, tc.confirmations, tc.timedOut, got, tc.want)
			}
		})
	}
}

// --- send envelope conventions ---

func TestSendOutput_MemoryInjectionOmittedWhenNil(t *testing.T) {
	out := SendOutput{RobotResponse: NewRobotResponse(true)}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "memory_injection") {
		t.Errorf("memory_injection should be omitted when nil: %s", data)
	}
}

func TestSendOutput_MemoryInjectionFieldPresence(t *testing.T) {
	out := SendOutput{
		RobotResponse: NewRobotResponse(true),
		MemoryInjection: &CMInjectionInfo{
			Enabled:       true,
			RulesInjected: []string{"rule-a"},
			TokensAdded:   12,
			Source:        "daemon",
		},
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	raw, ok := decoded["memory_injection"]
	if !ok {
		t.Fatalf("memory_injection missing from send envelope: %s", data)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enabled", "rules_injected", "tokens_added", "source"} {
		if _, ok := info[key]; !ok {
			t.Errorf("memory_injection missing key %q: %s", key, raw)
		}
	}
	if _, ok := info["skipped_reason"]; ok {
		t.Errorf("skipped_reason should be omitted when empty: %s", raw)
	}
}

func TestSendOptions_WithMemoryChangesBindingHash(t *testing.T) {
	base := SendOptions{Session: "proj", Message: "hello", AgentTypes: []string{"claude"}}
	withMemory := base
	withMemory.WithMemory = true

	if sendOperationBindingHash(base) == sendOperationBindingHash(withMemory) {
		t.Error("--with-memory toggle must be part of the idempotency binding hash")
	}
}

// TestSendOptions_BindingHashStableAcrossVersionsWithoutMemory pins the
// cross-version replay guarantee: a send WITHOUT --with-memory must produce
// the exact binding hash pre-1.24 NTM computed (which had no WithMemory
// field), so operation IDs recorded before the upgrade replay their stored
// outcome instead of failing with IDEMPOTENCY_CONFLICT. The expected value
// re-implements the pre-1.24 formula field-for-field.
func TestSendOptions_BindingHashStableAcrossVersionsWithoutMemory(t *testing.T) {
	opts := SendOptions{
		Session:    "proj",
		Message:    "hello",
		AgentTypes: []string{"claude"},
		Panes:      []string{"2", "1"},
		Exclude:    []string{"3"},
		ClearInput: true,
		WithCASS:   true,
	}

	// Pre-1.24 formula (send_idempotency.go as of v1.23.0).
	legacy := func(opts SendOptions) string {
		h := sha256.New()
		writeField := func(field string) {
			h.Write([]byte(field))
			h.Write([]byte{0})
		}
		writeList := func(values []string) {
			sorted := make([]string, 0, len(values))
			for _, v := range values {
				v = strings.TrimSpace(v)
				if v != "" {
					sorted = append(sorted, v)
				}
			}
			sort.Strings(sorted)
			for _, v := range sorted {
				writeField(v)
			}
			h.Write([]byte{1})
		}
		writeField(opts.Session)
		if opts.All {
			writeField("all")
		}
		writeField(opts.Pane)
		writeList(opts.Panes)
		writeList(opts.AgentTypes)
		writeList(opts.Exclude)
		enter := "default"
		if opts.Enter != nil {
			enter = strconv.FormatBool(*opts.Enter)
		}
		writeField(enter)
		writeField(strconv.FormatBool(opts.ClearInput))
		writeField(strconv.FormatBool(opts.WithCASS))
		inputSHA, _ := sendPayloadDigest(opts.Message)
		writeField(inputSHA)
		return hex.EncodeToString(h.Sum(nil))
	}

	if got, want := sendOperationBindingHash(opts), legacy(opts); got != want {
		t.Errorf("binding hash without --with-memory changed across versions:\n got %s\nwant %s\npre-upgrade operation IDs would spuriously conflict", got, want)
	}
}

// TestGetSendWithMemoryDeliversInjectedBlockRealTmux proves the injected rules
// block reaches the actually delivered payload, not just the formatter: a real
// tmux pane receives the typed keystrokes, so the pane capture must show the
// "## Project rules" block ABOVE the caller's message, and the send envelope
// must carry the matching memory_injection metadata.
func TestGetSendWithMemoryDeliversInjectedBlockRealTmux(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	const ruleMarker = "NTM_CM_RULE_MARKER"
	const baseMarker = "NTM_CM_BASE_MSG"

	_, port := newFakeCMDaemon(t, map[string]func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			cmWriteToolText(t, w, id, cmContextPayload(
				cm.Rule{ID: "rule-e2e", Content: ruleMarker + " always run go vet"},
			))
		},
	})

	session := "ntm-send-cm-inject"
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		if err := tmux.KillSession(session); err != nil {
			t.Errorf("kill tmux session: %v", err)
		}
	})

	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("get tmux panes: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("pane count = %d, want one newly-created pane", len(panes))
	}

	memCfg := unavailableCMConfig(t)
	memCfg.Client = cm.NewPortClient(port, "sess-e2e")

	output, err := GetSend(SendOptions{
		Session:      session,
		Pane:         panes[0].ID,
		Message:      "printf '" + baseMarker + "\\n'",
		WithMemory:   true,
		MemoryInject: &memCfg,
	})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if !output.Success {
		t.Fatalf("with-memory send failed: %+v", output)
	}
	if output.MemoryInjection == nil {
		t.Fatal("memory_injection missing from send envelope")
	}
	if output.MemoryInjection.SkippedReason != "" {
		t.Fatalf("injection skipped: %q", output.MemoryInjection.SkippedReason)
	}
	if len(output.MemoryInjection.RulesInjected) != 1 || output.MemoryInjection.RulesInjected[0] != "rule-e2e" {
		t.Fatalf("rules_injected = %v, want [rule-e2e]", output.MemoryInjection.RulesInjected)
	}

	// The typed keystrokes land in the pane's scrollback; poll briefly for the
	// shell to finish echoing everything.
	deadline := time.Now().Add(testutil.ScaleTimeout(5 * time.Second))
	var captured string
	for {
		captured, err = tmux.CapturePaneVisible(panes[0].ID)
		if err == nil &&
			strings.Contains(captured, "## Project rules") &&
			strings.Contains(captured, ruleMarker) &&
			strings.Contains(captured, baseMarker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivered payload missing injected block or message (err=%v): %q", err, captured)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Ordering: the rules block was prepended, so it must appear before the
	// caller's message in the delivered keystrokes.
	if strings.Index(captured, "## Project rules") > strings.Index(captured, baseMarker) {
		t.Errorf("rules block should precede the original message: %q", captured)
	}
}

// Exercise the public send engines with real CM HTTP/CLI traffic and recording
// tmux/SSH transports. Each subprocess isolates tmux discovery and global state.
func TestSendMemoryTargetScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording transport requires a POSIX shell")
	}
	for _, tracked := range []bool{false, true} {
		for _, mode := range []string{"daemon", "cli", "dry-run", "unknown", "remote", "canceled", "cancel-query", "disabled", "changed-daemon"} {
			t.Run(fmt.Sprintf("tracked=%t/%s", tracked, mode), func(t *testing.T) {
				root := t.TempDir()
				const transport = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_CM_SCOPE_ROOT/tmux-calls"
case "$1" in
  -V) printf 'tmux 3.4\n' ;;
  has-session) ;;
  list-panes)
    printf '%%7_NTM_SEP_1_NTM_SEP_target__aider_1_NTM_SEP_aider_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_%s_NTM_SEP_0_NTM_SEP_aider_NTM_SEP__NTM_SEP__NTM_SEP_0\n' "$NTM_CM_SCOPE_PID"
    ;;
  display-message)
    if [ "$NTM_CM_SCOPE_MODE" = unknown ]; then printf 'relative/app\n'; else printf '%s\n' "$NTM_CM_SCOPE_TARGET"; fi
    ;;
  capture-pane)
    printf 'Ready for work\n'
    if [ -f "$NTM_CM_SCOPE_ROOT/delivered" ]; then printf 'Understood; working on the requested task.\n'; fi
    ;;
  load-buffer) cat >> "$NTM_CM_SCOPE_ROOT/payload" ;;
  paste-buffer) : > "$NTM_CM_SCOPE_ROOT/delivered" ;;
  send-keys)
    printf '%s\n' "$@" >> "$NTM_CM_SCOPE_ROOT/payload"
    : > "$NTM_CM_SCOPE_ROOT/delivered"
    ;;
  delete-buffer) ;;
  *) printf 'unexpected tmux command: %s\n' "$*" >&2; exit 21 ;;
esac
`
				const ssh = `#!/bin/sh
set -eu
for command do :; done
exec /bin/sh -c "$command"
`
				const cli = `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$NTM_CM_SCOPE_ROOT/cm-argv"
if [ "$1" = context ] && [ "$4" = --workspace ] && [ "$5" = "$NTM_CM_SCOPE_TARGET" ]; then
  printf '%s\n' '{"success":true,"data":{"relevantBullets":[{"id":"target-rule","content":"TARGET_PROJECT_RULE"}]}}'
else
  printf '%s\n' '{"success":true,"data":{"relevantBullets":[{"id":"wrong-rule","content":"CALLER_PROJECT_RULE"}]}}'
fi
`
				for name, script := range map[string]string{"tmux": transport, "ssh": ssh, "cm": cli} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSendMemoryTargetScopeHelper$")
				cmd.Env = append(os.Environ(), "NTM_TEST_TMUX_ENV_OWNED=1", "NTM_TMUX_BINARY="+filepath.Join(root, "tmux"),
					"NTM_CM_SCOPE_ROOT="+root, "NTM_CM_SCOPE_MODE="+mode, "NTM_CM_SCOPE_TRACKED="+strconv.FormatBool(tracked))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("scoped memory: %v\n%s", err, output)
				}
			})
		}
	}
}

func TestSendMemoryTargetScopeHelper(t *testing.T) {
	root := os.Getenv("NTM_CM_SCOPE_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("NTM_CM_SCOPE_MODE")
	tracked := os.Getenv("NTM_CM_SCOPE_TRACKED") == "true"
	caller, target := filepath.Join(root, "client A", "app"), filepath.Join(root, "client B", "app")
	for _, dir := range []string{caller, target} {
		if err := os.MkdirAll(filepath.Join(dir, ".ntm", "pids"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(caller)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NTM_CM_SCOPE_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("NTM_CM_SCOPE_TARGET", target)
	t.Setenv("NTM_CONFIG", filepath.Join(root, "config.toml"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wrongQueries, queries, outcomes atomic.Int32
	_, wrongPort := newFakeCMDaemon(t, map[string]func(*testing.T, http.ResponseWriter, any, json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			wrongQueries.Add(1)
			cmWriteToolText(t, w, id, cmContextPayload(cm.Rule{ID: "wrong", Content: "CALLER_PROJECT_RULE"}))
		},
		"cm_outcome": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			wrongQueries.Add(1)
			cmWriteToolText(t, w, id, map[string]bool{"recorded": true})
		},
	})
	writePID := func(dir, session string, port int) {
		t.Helper()
		data, err := json.Marshal(cm.PIDFileInfo{PID: os.Getpid(), Port: port})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".ntm", "pids", "cm-"+session+".pid"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, targetPort := newFakeCMDaemon(t, map[string]func(*testing.T, http.ResponseWriter, any, json.RawMessage){
		"cm_context": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			queries.Add(1)
			var request struct {
				Workspace string `json:"workspace"`
			}
			if err := json.Unmarshal(args, &request); err != nil || request.Workspace != target {
				t.Errorf("target daemon received wrong workspace: %s (%v)", args, err)
			}
			if mode == "changed-daemon" {
				writePID(target, "target", wrongPort)
			}
			if mode == "cancel-query" {
				cancel()
			}
			cmWriteToolText(t, w, id, cmContextPayload(cm.Rule{ID: "target-rule", Content: "TARGET_PROJECT_RULE"}))
		},
		"cm_outcome": func(t *testing.T, w http.ResponseWriter, id any, args json.RawMessage) {
			outcomes.Add(1)
			var report outcomeCall
			if err := json.Unmarshal(args, &report); err != nil || report.SessionID != "target" || len(report.RulesUsed) != 1 || report.RulesUsed[0] != "target-rule" {
				t.Errorf("feedback lost retrieval identity: %s (%v)", args, err)
			}
			cmWriteToolText(t, w, id, map[string]bool{"recorded": true})
		},
	})
	// The alphabetically-first same-project daemon and caller's identically
	// named session must both be ignored. Feedback retains the queried daemon.
	writePID(caller, "target", wrongPort)
	writePID(target, "aaa-other-session", wrongPort)
	if mode != "cli" {
		writePID(target, "target", targetPort)
	}
	oldClient := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = oldClient })
	if mode == "remote" {
		tmux.DefaultClient.Remote = "test-host"
	}
	mem := DefaultCMInjectConfig()
	// Stale caller defaults must never override observed send targets.
	mem.ProjectDir, mem.Workspace = caller, caller
	mem.CLIBinary = filepath.Join(root, "cm")
	mem.Enabled = mode != "disabled"
	opts := SendOptions{Context: ctx, Session: "target", Pane: "%7", Message: "SCOPED_MEMORY_TASK", WithMemory: true, MemoryInject: &mem, DryRun: mode == "dry-run"}
	if mode == "canceled" {
		cancel()
	}
	var output *SendOutput
	var err error
	if tracked {
		var combined *SendAndAckOutput
		combined, err = GetSendAndAck(SendAndAckOptions{SendOptions: opts, AckTimeoutMs: 1000, AckPollMs: 10})
		if combined != nil {
			output = &combined.Send
			if err == nil && !combined.Success {
				t.Errorf("tracked operation failed: %+v", combined)
			}
		}
	} else {
		output, err = GetSend(opts)
	}
	payload, _ := os.ReadFile(filepath.Join(root, "payload"))
	argv, _ := os.ReadFile(filepath.Join(root, "cm-argv"))
	if wrongQueries.Load() != 0 || strings.Contains(string(payload), "CALLER_PROJECT_RULE") {
		t.Fatalf("memory crossed target scope: wrong queries=%d payload=%s", wrongQueries.Load(), payload)
	}
	if mode == "canceled" || mode == "cancel-query" {
		if !errors.Is(err, context.Canceled) || len(payload) != 0 || outcomes.Load() != 0 {
			t.Fatalf("cancellation queried feedback or dispatched: err=%v payload=%s outcomes=%d", err, payload, outcomes.Load())
		}
		return
	}
	if err != nil || output == nil || !output.Success || output.MemoryInjection == nil {
		t.Fatalf("send failed: output=%+v err=%v", output, err)
	}
	info := output.MemoryInjection
	skipped := mode == "unknown" || mode == "remote" || mode == "disabled"
	if skipped {
		if info.SkippedReason == "" || queries.Load() != 0 || len(argv) != 0 || outcomes.Load() != 0 || strings.Contains(string(payload), "TARGET_PROJECT_RULE") {
			t.Fatalf("unsafe scope did not skip memory: info=%+v queries=%d argv=%s payload=%s", info, queries.Load(), argv, payload)
		}
	} else {
		if info.SkippedReason != "" || info.Workspace != target || len(info.RulesInjected) != 1 || info.RulesInjected[0] != "target-rule" {
			t.Fatalf("wrong memory receipt: %+v", info)
		}
		if mode == "cli" && !strings.Contains(string(argv), "--workspace\n"+target+"\n") {
			t.Fatalf("CM CLI lost target workspace: %s", argv)
		}
		wantOutcomes := int32(0)
		if tracked && mode != "cli" && mode != "dry-run" {
			wantOutcomes = 1
		}
		if outcomes.Load() != wantOutcomes {
			t.Fatalf("feedback count=%d want=%d", outcomes.Load(), wantOutcomes)
		}
	}
	if mode == "dry-run" {
		if len(payload) != 0 || !output.DryRun {
			t.Fatalf("dry run actuated: payload=%s output=%+v", payload, output)
		}
	} else if !strings.Contains(string(payload), "SCOPED_MEMORY_TASK") || (!skipped && !strings.Contains(string(payload), "TARGET_PROJECT_RULE")) {
		t.Fatalf("actual delivery omitted target memory/task: %s", payload)
	}
}
