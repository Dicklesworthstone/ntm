package cli

// Tests for the UBS push-routing watch engine (bd-eujr8): fingerprint
// diffing, glob-aware reservation routing, cooldown gating, working-pane
// deferral, unrouted digest fallback, Agent Mail outage degradation, a stub
// ubs binary exercising the real scanner invocation, and a fake Agent Mail
// httptest server exercising the real reservation-list client path.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/scanner"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newBugsWatchTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open temp store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate temp store: %v", err)
	}
	return store
}

func bugsWatchTestFinding(file string, line int, severity scanner.Severity, msg string) scanner.Finding {
	return scanner.Finding{
		File:       file,
		Line:       line,
		Column:     1,
		Severity:   severity,
		Category:   "correctness",
		Message:    msg,
		Suggestion: "fix it",
	}
}

func idleObservation(now time.Time, paneIDs ...string) statuspkg.SessionObservation {
	obs := statuspkg.SessionObservation{Session: "proj", ObservedAt: now, Complete: true}
	for _, id := range paneIDs {
		obs.Panes = append(obs.Panes, statuspkg.PaneObservation{
			Pane: tmux.PaneRef{ID: id},
			Current: statuspkg.StateObservation{
				Status:     statuspkg.AgentStatus{PaneID: id, State: statuspkg.StateIdle},
				ObservedAt: now,
				Freshness:  statuspkg.FreshnessFresh,
				Confidence: 1.0,
			},
		})
	}
	return obs
}

// bugsWatchHarness wires a fully faked engine over a real temp store.
type bugsWatchHarness struct {
	engine     *bugsWatchEngine
	now        time.Time
	findings   []scanner.Finding
	mailUp     bool
	reserved   []agentmail.FileReservation
	panes      []tmux.Pane
	agentNames map[string]string // paneID -> agent name
	working    map[string]bool   // paneID -> treat as working
	nudges     []string          // messages dispatched, in order
	nudgePanes []string
	digests    [][]bugsWatchFinding
	outages    []string
}

func newBugsWatchHarness(t *testing.T, store *state.Store) *bugsWatchHarness {
	t.Helper()
	h := &bugsWatchHarness{
		now:        time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		mailUp:     true,
		agentNames: map[string]string{},
		working:    map[string]bool{},
	}
	h.engine = &bugsWatchEngine{
		session:    "proj",
		projectKey: "/work/proj",
		scanPath:   "/work/proj",
		cooldown:   10 * time.Minute,
		store:      store,
	}
	h.engine.deps = bugsWatchDeps{
		scan: func(context.Context) (*scanner.ScanResult, error) {
			result := &scanner.ScanResult{Findings: append([]scanner.Finding(nil), h.findings...)}
			for _, f := range h.findings {
				switch f.Severity {
				case scanner.SeverityCritical:
					result.Totals.Critical++
				case scanner.SeverityWarning:
					result.Totals.Warning++
				default:
					result.Totals.Info++
				}
			}
			return result, nil
		},
		agentMailUp: func(context.Context) bool { return h.mailUp },
		listReservations: func(context.Context) ([]agentmail.FileReservation, error) {
			return h.reserved, nil
		},
		listPanes: func(context.Context) ([]tmux.Pane, error) { return h.panes, nil },
		agentForPane: func(p tmux.Pane) string {
			return h.agentNames[p.ID]
		},
		observe: func(context.Context, string) (statuspkg.SessionObservation, error) {
			ids := make([]string, 0, len(h.panes))
			for _, p := range h.panes {
				ids = append(ids, p.ID)
			}
			return idleObservation(h.now, ids...), nil
		},
		safeToDispatch: func(p statuspkg.PaneObservation) bool {
			return !h.working[p.Pane.ID] && p.SafeToDispatch()
		},
		dispatchNudge: func(_ context.Context, _ []tmux.Pane, target tmux.Pane, message string) error {
			h.nudges = append(h.nudges, message)
			h.nudgePanes = append(h.nudgePanes, target.ID)
			return nil
		},
		publishDigest: func(_ context.Context, findings []bugsWatchFinding) error {
			h.digests = append(h.digests, findings)
			return nil
		},
		publishOutage: func(_ []bugsWatchFinding, cause string) {
			h.outages = append(h.outages, cause)
		},
		now: func() time.Time { return h.now },
	}
	return h
}

func activeReservation(id int, pattern, agent string, exclusive bool, now time.Time) agentmail.FileReservation {
	return agentmail.FileReservation{
		ID:          id,
		PathPattern: pattern,
		AgentName:   agent,
		Exclusive:   exclusive,
		CreatedTS:   agentmail.FlexTime{Time: now.Add(-time.Hour)},
		ExpiresTS:   agentmail.FlexTime{Time: now.Add(time.Hour)},
	}
}

func TestBugsFindingFingerprint(t *testing.T) {
	base := bugsWatchTestFinding("internal/foo.go", 42, scanner.SeverityCritical, "nil deref")
	same := base
	same.Column = 99 // column and suggestion are not part of identity
	same.Suggestion = "different"
	if bugsFindingFingerprint(base) != bugsFindingFingerprint(same) {
		t.Fatal("fingerprint should ignore column/suggestion")
	}

	variants := []scanner.Finding{
		bugsWatchTestFinding("internal/foo.go", 43, scanner.SeverityCritical, "nil deref"),
		bugsWatchTestFinding("internal/bar.go", 42, scanner.SeverityCritical, "nil deref"),
		bugsWatchTestFinding("internal/foo.go", 42, scanner.SeverityCritical, "other message"),
	}
	seen := map[string]bool{bugsFindingFingerprint(base): true}
	for i, v := range variants {
		fp := bugsFindingFingerprint(v)
		if seen[fp] {
			t.Fatalf("variant %d collided with a prior fingerprint", i)
		}
		seen[fp] = true
	}
	t.Logf("fingerprint sample: %s", bugsFindingFingerprint(base))
}

func TestBugsReservationHolderGlobMatching(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	projectKey := "/work/proj"
	reservations := []agentmail.FileReservation{
		activeReservation(1, "internal/cli/**", "GreenLake", true, now),
		activeReservation(2, "docs/readme.md", "BlueRiver", true, now),
		activeReservation(3, "internal/state", "RedFox", true, now),
		activeReservation(4, "internal/shared/**", "SharedOnly", false, now),
		activeReservation(5, "*.md", "TopGlob", true, now),
	}
	expired := activeReservation(6, "expired/**", "Ghost", true, now)
	expired.ExpiresTS = agentmail.FlexTime{Time: now.Add(-time.Minute)}
	reservations = append(reservations, expired)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"glob dir match", "internal/cli/bugs.go", "GreenLake"},
		{"exact file", "docs/readme.md", "BlueRiver"},
		{"directory prefix", "internal/state/store.go", "RedFox"},
		{"shared fallback", "internal/shared/util.go", "SharedOnly"},
		{"single star stays in segment", "README.md", "TopGlob"},
		{"single star does not span dirs", "sub/README.md", ""},
		{"expired reservation ignored", "expired/x.go", ""},
		{"no match", "cmd/main.go", ""},
		{"absolute path under project", "/work/proj/internal/cli/x.go", "GreenLake"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comparable := bugsComparableFindingPath(tc.path, projectKey, projectKey)
			got := bugsReservationHolder(reservations, comparable, projectKey, now)
			t.Logf("path=%q comparable=%q holder=%q", tc.path, comparable, got)
			if got != tc.want {
				t.Fatalf("holder for %q = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestBugsWatchTickRoutesNewFindingToHolderAndDedupes(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/bugs.go", 10, scanner.SeverityCritical, "nil deref"),
		bugsWatchTestFinding("internal/cli/bugs.go", 20, scanner.SeverityInfo, "info noise"),
	}
	h.reserved = []agentmail.FileReservation{
		activeReservation(1, "internal/cli/**", "GreenLake", true, h.now),
	}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	t.Logf("tick 1: %+v", tick)
	if tick.Findings != 1 {
		t.Fatalf("info-severity finding should not be route-worthy; findings=%d", tick.Findings)
	}
	if tick.New != 1 || tick.Nudged != 1 || tick.Deferred != 0 || tick.Digested != 0 {
		t.Fatalf("unexpected tick 1 counts: %+v", tick)
	}
	if len(h.nudges) != 1 || h.nudgePanes[0] != "%1" {
		t.Fatalf("expected one nudge to %%1, got %v -> %v", h.nudgePanes, h.nudges)
	}
	msg := h.nudges[0]
	for _, want := range []string{"internal/cli/bugs.go:10", "critical/correctness", "nil deref", "Suggested fix: fix it"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("nudge message missing %q:\n%s", want, msg)
		}
	}

	// Second tick with the identical scan result: dedupe means no re-nudge.
	h.now = h.now.Add(time.Hour) // well past the cooldown, so only dedupe can stop it
	tick2, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	t.Logf("tick 2: %+v", tick2)
	if tick2.New != 0 || tick2.Nudged != 0 {
		t.Fatalf("finding nudged twice: %+v", tick2)
	}
	if len(h.nudges) != 1 {
		t.Fatalf("expected exactly one nudge total, got %d", len(h.nudges))
	}

	// A fresh finding at the same file routes again. Refresh the
	// reservation's TTL: the fake clock has moved past the original expiry.
	h.findings = append(h.findings, bugsWatchTestFinding("internal/cli/bugs.go", 30, scanner.SeverityWarning, "shadowed err"))
	h.now = h.now.Add(time.Hour)
	h.reserved = []agentmail.FileReservation{
		activeReservation(1, "internal/cli/**", "GreenLake", true, h.now),
	}
	tick3, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if tick3.New != 1 || tick3.Nudged != 1 {
		t.Fatalf("new finding not routed: %+v", tick3)
	}
}

func TestBugsWatchTickCooldownDefersThenDelivers(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/a.go", 1, scanner.SeverityWarning, "bug a"),
	}
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GreenLake", true, h.now)}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	// Persisted prior nudge 1 minute ago -> within the 10m cooldown.
	if err := h.engine.recordNudge("%1", h.now.Add(-time.Minute)); err != nil {
		t.Fatalf("seed cooldown: %v", err)
	}

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	t.Logf("tick 1 (inside cooldown): %+v", tick)
	if tick.Deferred != 1 || tick.Nudged != 0 || len(h.nudges) != 0 {
		t.Fatalf("cooldown did not defer: %+v", tick)
	}

	// Advance beyond the cooldown: the deferred finding is retried and lands.
	h.now = h.now.Add(15 * time.Minute)
	tick2, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	t.Logf("tick 2 (past cooldown): %+v", tick2)
	if tick2.Nudged != 1 || len(h.nudges) != 1 {
		t.Fatalf("deferred finding not delivered after cooldown: %+v", tick2)
	}

	// The delivery refreshed the persisted cooldown stamp.
	last, ok, err := h.engine.lastNudgeAt("%1")
	if err != nil || !ok {
		t.Fatalf("lastNudgeAt after delivery: ok=%v err=%v", ok, err)
	}
	if !last.Equal(h.now) {
		t.Fatalf("cooldown stamp = %s, want %s", last, h.now)
	}
}

func TestBugsWatchTickNeverNudgesWorkingPane(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/a.go", 1, scanner.SeverityCritical, "bug a"),
	}
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GreenLake", true, h.now)}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"
	h.working["%1"] = true

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	t.Logf("tick 1 (working pane): %+v", tick)
	if tick.Deferred != 1 || tick.Nudged != 0 || len(h.nudges) != 0 {
		t.Fatalf("working pane was nudged: %+v", tick)
	}

	// Next tick the pane is idle again: the finding is retried, not lost.
	h.working["%1"] = false
	tick2, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	t.Logf("tick 2 (idle pane): %+v", tick2)
	if tick2.Nudged != 1 || len(h.nudges) != 1 {
		t.Fatalf("deferred finding not retried once pane idled: %+v", tick2)
	}
}

func TestBugsWatchTickUnreservedFindingsBatchIntoDigest(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("cmd/main.go", 5, scanner.SeverityWarning, "unreserved a"),
		bugsWatchTestFinding("cmd/other.go", 6, scanner.SeverityWarning, "unreserved b"),
		bugsWatchTestFinding("internal/cli/a.go", 7, scanner.SeverityWarning, "holder without pane"),
	}
	// One reservation whose holder has no live pane: falls back to digest.
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GhostAgent", true, h.now)}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	t.Logf("tick 1: %+v", tick)
	if tick.Digested != 3 || tick.Nudged != 0 {
		t.Fatalf("expected all 3 findings digested: %+v", tick)
	}
	if len(h.digests) != 1 || len(h.digests[0]) != 3 {
		t.Fatalf("expected one batched digest with 3 findings, got %v", h.digests)
	}

	// Digested findings are handled: no repeat digest next tick.
	tick2, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if tick2.Digested != 0 || len(h.digests) != 1 {
		t.Fatalf("digest repeated for handled findings: %+v", tick2)
	}
}

func TestBugsWatchTickAgentMailDownLogsAndRetries(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.mailUp = false
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/a.go", 1, scanner.SeverityCritical, "bug a"),
	}
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GreenLake", true, h.now)}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	t.Logf("tick 1 (mail down): %+v outages=%v", tick, h.outages)
	if !tick.AgentMailDown || tick.Nudged != 0 || len(h.nudges) != 0 {
		t.Fatalf("expected outage path with no nudges: %+v", tick)
	}
	if len(h.outages) != 1 {
		t.Fatalf("expected one outage publication, got %v", h.outages)
	}

	// Mail comes back: the same finding is still unhandled and routes now.
	h.mailUp = true
	tick2, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	t.Logf("tick 2 (mail up): %+v", tick2)
	if tick2.Nudged != 1 || len(h.nudges) != 1 {
		t.Fatalf("finding not retried after outage: %+v", tick2)
	}
}

func TestBugsWatchAttentionItemPersistsToStore(t *testing.T) {
	store := newBugsWatchTestStore(t)
	findings := []bugsWatchFinding{
		{Finding: bugsWatchTestFinding("a.go", 1, scanner.SeverityCritical, "boom"), Fingerprint: "fp1"},
	}
	publishBugsAttentionItem(store, "proj", "/work/proj", findings, "Agent Mail server unavailable")

	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("reading attention events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 attention event, got %d", len(events))
	}
	ev := events[0]
	t.Logf("attention event: %+v", ev)
	if ev.Source != "bugs_watch" || ev.EventType != "ubs_findings_undelivered" {
		t.Fatalf("unexpected event classification: %+v", ev)
	}
	if ev.Severity != state.SeverityCritical {
		t.Fatalf("severity should escalate to critical, got %s", ev.Severity)
	}
	if !strings.Contains(ev.Details, "a.go:1") {
		t.Fatalf("details missing finding: %s", ev.Details)
	}
}

// TestBugsWatchStubUBSBinary drives the real scanner invocation (the same
// Scan call bugs.go makes) against a stub ubs binary that prints fixture
// JSON, proving the watch engine consumes genuine UBS output end to end.
func TestBugsWatchStubUBSBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub ubs binary is a shell script")
	}
	dir := t.TempDir()
	fixture := scanner.ScanResult{
		Project: dir,
		Totals:  scanner.ScanTotals{Critical: 1, Warning: 1, Files: 2},
		Findings: []scanner.Finding{
			bugsWatchTestFinding("internal/cli/a.go", 12, scanner.SeverityCritical, "stub critical"),
			bugsWatchTestFinding("cmd/b.go", 34, scanner.SeverityWarning, "stub warning"),
		},
	}
	payload, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	stub := filepath.Join(dir, "ubs")
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\n", payload)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub ubs: %v", err)
	}

	s, err := scanner.NewScannerWithConfig(&config.ScannerConfig{UBSPath: stub})
	if err != nil {
		t.Fatalf("scanner with stub: %v", err)
	}

	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.engine.deps.scan = func(ctx context.Context) (*scanner.ScanResult, error) {
		return s.Scan(ctx, dir, scanner.DefaultOptions())
	}
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GreenLake", true, h.now)}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	t.Logf("stub ubs tick: %+v", tick)
	if tick.Findings != 2 || tick.New != 2 {
		t.Fatalf("stub findings not parsed: %+v", tick)
	}
	if tick.Nudged != 1 || tick.Digested != 1 {
		t.Fatalf("expected 1 nudge (reserved) + 1 digest (unreserved): %+v", tick)
	}
	if len(h.nudges) != 1 || !strings.Contains(h.nudges[0], "stub critical") {
		t.Fatalf("nudge does not carry stub finding: %v", h.nudges)
	}
}

// TestBugsWatchFakeAgentMailReservations exercises the real
// agentmail.Client.ListReservations path against a fake Agent Mail MCP
// httptest server (mirroring the stub pattern in
// internal/agentmail/tools_httptest_test.go) and feeds the result through
// the engine's routing.
func TestBugsWatchFakeAgentMailReservations(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	reservations := []map[string]interface{}{
		{
			"id":           1,
			"path_pattern": "internal/cli/**",
			"agent_name":   "GreenLake",
			"exclusive":    true,
			"created_ts":   now.Add(-time.Hour).Format(time.RFC3339),
			"expires_ts":   now.Add(time.Hour).Format(time.RFC3339),
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Method  string      `json:"method"`
			Params  struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
				URI       string                 `json:"uri"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode rpc request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		write := func(result interface{}, rpcErr map[string]interface{}) {
			resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
			if rpcErr != nil {
				resp["error"] = rpcErr
			} else {
				resp["result"] = result
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Errorf("encode rpc response: %v", err)
			}
		}
		switch req.Method {
		case "resources/read":
			// Force the legacy tools/call fallback path.
			write(nil, map[string]interface{}{"code": -32602, "message": "resource not found"})
		case "tools/call":
			if req.Params.Name != "list_file_reservations" && req.Params.Name != "list_reservations" {
				write(nil, map[string]interface{}{"code": -32601, "message": "unknown tool " + req.Params.Name})
				return
			}
			payload, _ := json.Marshal(reservations)
			write(map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": string(payload)}},
			}, nil)
		default:
			write(map[string]interface{}{}, nil)
		}
	}))
	t.Cleanup(server.Close)

	client := agentmail.NewClient(
		agentmail.WithBaseURL(server.URL+"/"),
		agentmail.WithProjectKey("/work/proj"),
	)
	listed, err := client.ListReservations(context.Background(), "/work/proj", "", true)
	if err != nil {
		t.Fatalf("ListReservations via fake server: %v", err)
	}
	if len(listed) != 1 || listed[0].AgentName != "GreenLake" {
		t.Fatalf("unexpected reservations from fake server: %+v", listed)
	}
	t.Logf("fake agent mail returned %d reservation(s): %+v", len(listed), listed[0])

	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.now = now
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/a.go", 3, scanner.SeverityCritical, "via fake mail"),
	}
	h.engine.deps.listReservations = func(ctx context.Context) ([]agentmail.FileReservation, error) {
		return client.ListReservations(ctx, "/work/proj", "", true)
	}
	h.panes = []tmux.Pane{{ID: "%1", Title: "proj__cc_1", Type: tmux.AgentClaude}}
	h.agentNames["%1"] = "GreenLake"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if tick.Nudged != 1 || len(h.nudgePanes) != 1 || h.nudgePanes[0] != "%1" {
		t.Fatalf("finding did not route via fake-server reservation: %+v", tick)
	}
}

func TestBugsWatchPaneMappingFallsBackToDigestWhenHolderHasNoPane(t *testing.T) {
	store := newBugsWatchTestStore(t)
	h := newBugsWatchHarness(t, store)
	h.findings = []scanner.Finding{
		bugsWatchTestFinding("internal/cli/a.go", 9, scanner.SeverityWarning, "orphan holder"),
	}
	h.reserved = []agentmail.FileReservation{activeReservation(1, "internal/**", "GreenLake", true, h.now)}
	// Live pane exists but maps to a different agent identity.
	h.panes = []tmux.Pane{{ID: "%2", Title: "proj__cod_1", Type: tmux.AgentCodex}}
	h.agentNames["%2"] = "BlueRiver"

	tick, err := h.engine.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	t.Logf("tick: %+v", tick)
	if tick.Nudged != 0 || tick.Digested != 1 {
		t.Fatalf("holder without live pane should digest, got %+v", tick)
	}
}

func TestBugsWatchCommandRequiresPushRoutingOrForce(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()
	cfg.Bugs = config.DefaultBugsConfig()

	if cfg.Bugs.PushRouting {
		t.Fatal("push_routing must default to false")
	}
	if got := cfg.Bugs.EffectiveInterval(); got != 5*time.Minute {
		t.Fatalf("default interval = %s, want 5m", got)
	}
	if got := cfg.Bugs.EffectiveCooldown(); got != 10*time.Minute {
		t.Fatalf("default cooldown = %s, want 10m", got)
	}

	cmd := newBugsWatchCmd()
	err := runBugsWatch(cmd, "", ".", time.Minute, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "push_routing") {
		t.Fatalf("watch without push_routing should refuse politely, got %v", err)
	}
	t.Logf("gate error: %v", err)
}
