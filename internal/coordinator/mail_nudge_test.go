package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// newMailInboxServer stands up a fake Agent Mail MCP server (JSON-RPC 2.0 at
// root, tools/call) whose fetch_inbox returns the configured per-agent inbox
// payloads. It mirrors the wire shape internal/agentmail's own httptest suite
// uses.
func newMailInboxServer(t *testing.T, inboxes map[string][]map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		params, _ := req.Params.(map[string]interface{})
		toolName, _ := params["name"].(string)
		if req.Method != "tools/call" || toolName != "fetch_inbox" {
			resp := agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID,
				Error: &agentmail.JSONRPCError{Code: -32601, Message: "unexpected call: " + req.Method + "/" + toolName}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		args, _ := params["arguments"].(map[string]interface{})
		agentName, _ := args["agent_name"].(string)
		payload, _ := json.Marshal(map[string]interface{}{"result": inboxes[agentName]})
		resp := agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(payload)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// mailNudgeTestEnv wires a checker with in-memory seams: a real agentmail
// client against the fake MCP server, fake panes/captures/dispatch, and a
// controllable clock.
type mailNudgeTestEnv struct {
	mc        *mailNudgeChecker
	dispatch  []string // pane IDs nudged, in order
	messages  []string // messages dispatched, in order
	published []robot.ActuationRecord
	clock     time.Time
	mu        sync.Mutex
}

func newMailNudgeTestEnv(t *testing.T, serverURL string, panes []tmux.Pane, agents map[string]string, capture func(paneID string) (string, error)) *mailNudgeTestEnv {
	t.Helper()
	client := agentmail.NewClient(agentmail.WithBaseURL(serverURL + "/"))
	cfg := DefaultCoordinatorConfig()
	cfg.MailNudge = true
	mc := newMailNudgeChecker("mailsess", "/proj", cfg, client)
	if mc == nil {
		t.Fatalf("checker not constructed with mail_nudge=true")
	}

	env := &mailNudgeTestEnv{mc: mc, clock: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	lastNudge := map[string]time.Time{}

	mc.getPanes = func(context.Context, string) ([]tmux.Pane, error) { return panes, nil }
	mc.agentLookup = func() func(paneTitle, paneID string) (string, bool) {
		return func(paneTitle, paneID string) (string, bool) {
			name, ok := agents[paneTitle]
			return name, ok
		}
	}
	mc.capturePane = func(_ context.Context, paneID string, _ int) (string, error) { return capture(paneID) }
	mc.dispatch = func(_ context.Context, _ []tmux.Pane, target tmux.Pane, message string) error {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.dispatch = append(env.dispatch, target.ID)
		env.messages = append(env.messages, message)
		return nil
	}
	mc.lastNudgeAt = func(scope string) (time.Time, bool) {
		env.mu.Lock()
		defer env.mu.Unlock()
		at, ok := lastNudge[scope]
		return at, ok
	}
	mc.recordNudge = func(scope, _ string, at time.Time) {
		env.mu.Lock()
		defer env.mu.Unlock()
		lastNudge[scope] = at
	}
	mc.publish = func(record robot.ActuationRecord) {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.published = append(env.published, record)
	}
	mc.now = func() time.Time { return env.clock }
	return env
}

func unreadMessage(id int) map[string]interface{} {
	return map[string]interface{}{"id": id, "subject": "please review", "from": "GreenCastle"}
}

func readMessage(id int) map[string]interface{} {
	// Real fetch_inbox rows carry "read_ts", not "read_at" (#277). The fixture
	// must emit the server's field name so the suite proves the decode path
	// against reality rather than against the code's own expectations.
	return map[string]interface{}{"id": id, "subject": "old news", "from": "GreenCastle", "read_ts": "2026-08-18T10:00:00Z"}
}

// TestMailNudgeDispatchedOnceWithCooldown is the headline loop: unread mail on
// the fake Agent Mail server + an idle pane → exactly one nudge; subsequent
// ticks inside the cooldown stay silent; after the cooldown elapses (mail
// still unread) the pane is nudged again.
func TestMailNudgeDispatchedOnceWithCooldown(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1), readMessage(2)},
	})
	defer server.Close()

	panes := []tmux.Pane{ccPane("%1", "mailsess__cc_1")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__cc_1": "BlueLake"},
		func(string) (string, error) { return idleCapture, nil })

	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].Action != "nudged" {
		t.Fatalf("first pass decisions = %+v, want one nudged", decisions)
	}
	if decisions[0].Unread != 1 {
		t.Fatalf("unread = %d, want 1 (read messages must not count)", decisions[0].Unread)
	}
	if len(env.dispatch) != 1 || env.dispatch[0] != "%1" {
		t.Fatalf("dispatched panes = %v, want [%%1]", env.dispatch)
	}
	if !strings.Contains(env.messages[0], "1 unread Agent Mail") {
		t.Fatalf("default nudge message = %q, want unread count mention", env.messages[0])
	}

	// Second tick 5s later: inside the 60s cooldown, silent (no decision, no
	// publish, no dispatch).
	env.clock = env.clock.Add(5 * time.Second)
	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 0 {
		t.Fatalf("cooldown pass decisions = %+v, want none", decisions)
	}
	if len(env.dispatch) != 1 {
		t.Fatalf("dispatch count after cooldown pass = %d, want 1", len(env.dispatch))
	}

	// After the cooldown elapses, the still-unread mail re-nudges.
	env.clock = env.clock.Add(defaultMailNudgeCooldown)
	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 1 || decisions[0].Action != "nudged" {
		t.Fatalf("post-cooldown decisions = %+v, want one nudged", decisions)
	}
	if len(env.dispatch) != 2 {
		t.Fatalf("dispatch count after cooldown elapsed = %d, want 2", len(env.dispatch))
	}

	// Every nudge was published to the attention feed with the shared fields.
	if len(env.published) != 2 {
		t.Fatalf("published records = %d, want 2", len(env.published))
	}
	for _, record := range env.published {
		if record.Action != "mail_nudge" || record.Source != "coordinator.mail_nudge" || record.ReasonCode != "mail_nudge_delivered" {
			t.Fatalf("published record = %+v, want mail_nudge/coordinator.mail_nudge/mail_nudge_delivered", record)
		}
	}
}

// TestMailNudgeWorkingPaneSkippedAndPublished: a generating pane with unread
// mail is never typed into; the skip is published with reason evidence.
func TestMailNudgeWorkingPaneSkippedAndPublished(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BusyFox": {unreadMessage(1)},
	})
	defer server.Close()

	workingCapture := "✻ Simmering… (esc to interrupt · 12s)\n\n ❯\n"
	panes := []tmux.Pane{ccPane("%2", "mailsess__cc_2")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__cc_2": "BusyFox"},
		func(string) (string, error) { return workingCapture, nil })

	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].Action != "skipped" || decisions[0].SkipReason != "working" {
		t.Fatalf("decisions = %+v, want one skipped/working", decisions)
	}
	if len(env.dispatch) != 0 {
		t.Fatalf("working pane was dispatched to: %v", env.dispatch)
	}
	if len(env.published) != 1 || env.published[0].ReasonCode != "mail_nudge_safety_skip" || !env.published[0].Blocked {
		t.Fatalf("published = %+v, want one blocked mail_nudge_safety_skip", env.published)
	}

	// The same skip on the next tick is suppressed (republish interval), but
	// still returned as a decision.
	env.clock = env.clock.Add(5 * time.Second)
	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 1 {
		t.Fatalf("second working pass decisions = %+v, want one", decisions)
	}
	if len(env.published) != 1 {
		t.Fatalf("published after suppressed repeat = %d records, want still 1", len(env.published))
	}
}

// TestMailNudgeFailsClosedForUnsupportedAgentTypes: agent types without a
// working detector are refused outright. (Gemini has had one since its
// rotation support; rotation_gemini_test.go covers its unknown frames.)
func TestMailNudgeFailsClosedForUnsupportedAgentTypes(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"AiderOwl": {unreadMessage(1)},
	})
	defer server.Close()

	panes := []tmux.Pane{{ID: "%3", Title: "mailsess__aider_1", Type: tmux.AgentAider, Width: 120}}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__aider_1": "AiderOwl"},
		func(string) (string, error) { return idleCapture, nil })

	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].SkipReason != "unsupported_agent_type" {
		t.Fatalf("decisions = %+v, want one skipped/unsupported_agent_type", decisions)
	}
	if len(env.dispatch) != 0 {
		t.Fatalf("unsupported agent type was dispatched to: %v", env.dispatch)
	}
}

// TestMailNudgeNoUnreadMailStaysSilent: an idle registered pane with a fully
// read inbox produces no decision, no dispatch, no publication.
func TestMailNudgeNoUnreadMailStaysSilent(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {readMessage(1), readMessage(2)},
	})
	defer server.Close()

	panes := []tmux.Pane{ccPane("%1", "mailsess__cc_1")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__cc_1": "BlueLake"},
		func(string) (string, error) { return idleCapture, nil })

	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
	if len(env.dispatch) != 0 || len(env.published) != 0 {
		t.Fatalf("dispatch=%v published=%v, want none", env.dispatch, env.published)
	}
}

// TestMailNudgeUnregisteredPanesIgnored: panes without an Agent Mail identity
// never trigger inbox probes or captures.
func TestMailNudgeUnregisteredPanesIgnored(t *testing.T) {
	server := newMailInboxServer(t, nil)
	defer server.Close()

	panes := []tmux.Pane{ccPane("%9", "mailsess__cc_9")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{}, // registry knows nobody
		func(string) (string, error) {
			t.Fatalf("unregistered pane was captured")
			return "", nil
		})

	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none", decisions)
	}
}

// TestMailNudgeMessageOverride: [coordinator] nudge_message replaces the
// built-in prompt verbatim.
func TestMailNudgeMessageOverride(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)},
	})
	defer server.Close()

	panes := []tmux.Pane{ccPane("%1", "mailsess__cc_1")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__cc_1": "BlueLake"},
		func(string) (string, error) { return idleCapture, nil })
	env.mc.message = "Custom: check your mailbox."

	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 1 || decisions[0].Action != "nudged" {
		t.Fatalf("decisions = %+v, want one nudged", decisions)
	}
	if len(env.messages) != 1 || env.messages[0] != "Custom: check your mailbox." {
		t.Fatalf("messages = %v, want the verbatim override", env.messages)
	}
}

// TestMailNudgeDispatchFailureStartsCooldown: an attempt that fails still
// records the watermark, so a broken dispatch is not hammered every tick.
func TestMailNudgeDispatchFailureStartsCooldown(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)},
	})
	defer server.Close()

	panes := []tmux.Pane{ccPane("%1", "mailsess__cc_1")}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__cc_1": "BlueLake"},
		func(string) (string, error) { return idleCapture, nil })
	attempts := 0
	env.mc.dispatch = func(context.Context, []tmux.Pane, tmux.Pane, string) error {
		attempts++
		return errors.New("composer never became ready")
	}

	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].Action != "nudge_failed" {
		t.Fatalf("decisions = %+v, want one nudge_failed", decisions)
	}
	if len(env.published) != 1 || env.published[0].ReasonCode != "mail_nudge_failed" {
		t.Fatalf("published = %+v, want one mail_nudge_failed", env.published)
	}

	env.clock = env.clock.Add(5 * time.Second)
	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 0 {
		t.Fatalf("post-failure cooldown decisions = %+v, want none", decisions)
	}
	if attempts != 1 {
		t.Fatalf("dispatch attempts = %d, want 1 (attempt starts the cooldown)", attempts)
	}
}

// TestNewMailNudgeCheckerGates: default-off and missing-client construction
// gates, plus the cooldown fallback for non-positive configuration.
func TestNewMailNudgeCheckerGates(t *testing.T) {
	client := agentmail.NewClient(agentmail.WithBaseURL("http://127.0.0.1:1/"))

	cfg := DefaultCoordinatorConfig()
	if mc := newMailNudgeChecker("s", "/p", cfg, client); mc != nil {
		t.Fatalf("checker constructed with mail_nudge=false")
	}

	cfg.MailNudge = true
	if mc := newMailNudgeChecker("s", "/p", cfg, nil); mc != nil {
		t.Fatalf("checker constructed without an Agent Mail client")
	}

	mc := newMailNudgeChecker("s", "/p", cfg, client)
	if mc == nil {
		t.Fatalf("checker not constructed when enabled with a client")
	}
	if mc.cooldown != defaultMailNudgeCooldown {
		t.Fatalf("cooldown = %s, want default %s", mc.cooldown, defaultMailNudgeCooldown)
	}

	cfg.NudgeCooldown = -3 * time.Second
	if mc := newMailNudgeChecker("s", "/p", cfg, client); mc.cooldown != defaultMailNudgeCooldown {
		t.Fatalf("non-positive cooldown = %s, want fallback %s", mc.cooldown, defaultMailNudgeCooldown)
	}

	cfg.NudgeCooldown = 5 * time.Minute
	cfg.NudgeMessage = "  custom  "
	mc = newMailNudgeChecker("s", "/p", cfg, client)
	if mc.cooldown != 5*time.Minute {
		t.Fatalf("cooldown = %s, want 5m", mc.cooldown)
	}
	if mc.message != "custom" {
		t.Fatalf("message = %q, want trimmed override", mc.message)
	}
}

// TestMailNudgeGrokIdlePaneNudged: Grok Build panes have a live working
// detector (GrokActivelyWorking) and composer inspection, both capture-based,
// so the shared safety gate admits them for mail nudging — an idle Grok pane
// with unread mail gets exactly one nudge instead of a permanent
// unsupported_agent_type refusal.
func TestMailNudgeGrokIdlePaneNudged(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"CalmBison": {unreadMessage(1)},
	})
	defer server.Close()

	grokIdle := "│ ❯                                        │\n╰─ Grok 4.6 (high) · always-approve ──────╯\n"
	panes := []tmux.Pane{{ID: "%7", Title: "mailsess__grok_1", Type: tmux.AgentGrok, Width: 120}}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__grok_1": "CalmBison"},
		func(string) (string, error) { return grokIdle, nil })

	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].Action != "nudged" || decisions[0].SkipReason != "" {
		t.Fatalf("decisions = %+v, want one nudged Grok pane", decisions)
	}
	if len(env.dispatch) != 1 || env.dispatch[0] != "%7" {
		t.Fatalf("dispatched = %v, want [%%7]", env.dispatch)
	}
}

// TestMailNudgeGrokSafetyVetoes: the two gates that make Grok nudging safe —
// an in-flight turn (spinner / Esc:cancel footer) and text already held in
// the bordered composer — must both veto the nudge.
func TestMailNudgeGrokSafetyVetoes(t *testing.T) {
	tests := []struct {
		name       string
		capture    string
		wantReason string
	}{
		{
			name:       "in-flight turn",
			capture:    "⠹ Waiting for response… 1.2s\n│ ❯                  │\nShift+Tab:mode │ Esc:cancel │ Ctrl+x:shortcuts\n",
			wantReason: "working",
		},
		{
			name:       "held composer text",
			capture:    "│ ❯ keep going on the previous task        │\n╰─ Grok 4.6 (high) · always-approve ──────╯\n",
			wantReason: "unsubmitted_input",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newMailInboxServer(t, map[string][]map[string]interface{}{
				"CalmBison": {unreadMessage(1)},
			})
			defer server.Close()

			panes := []tmux.Pane{{ID: "%7", Title: "mailsess__grok_1", Type: tmux.AgentGrok, Width: 120}}
			env := newMailNudgeTestEnv(t, server.URL, panes,
				map[string]string{"mailsess__grok_1": "CalmBison"},
				func(string) (string, error) { return tc.capture, nil })

			decisions := env.mc.runOnce(t.Context())
			if len(decisions) != 1 || decisions[0].Action != "skipped" || decisions[0].SkipReason != tc.wantReason {
				t.Fatalf("decisions = %+v, want one skipped/%s", decisions, tc.wantReason)
			}
			if len(env.dispatch) != 0 {
				t.Fatalf("Grok pane dispatched despite %s veto: %v", tc.wantReason, env.dispatch)
			}
		})
	}
}

func piNudgeCapture(draft string, working bool) string {
	top := strings.Repeat("─", 40)
	if working {
		top = "─⠋" + strings.Repeat("─", 38)
	}
	return "Completed the previous task.\n" + top + "\n" + draft + "\n" +
		strings.Repeat("─", 40) + "\n/work/project\n↑1.2k ↓200 90%/200k\n"
}

func antigravityNudgeCapture(draft string, working bool) string {
	activity := "Completed the previous task.\n"
	if working {
		activity = "⠋ Working\nEsc to cancel\n"
	}
	return "Antigravity\n" + activity + "> " + draft + "\n? for shortcuts\n"
}

// An idle native harness must be woken through the same mail checker that the
// coordinator tick uses. Merely registering a type is not behavioral support:
// unknown/stale frames, live turns, drafts and menus must still refuse input.
func TestMailNudgeNativeHarnessSafety(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)},
	})
	defer server.Close()

	for _, harness := range []struct {
		name     string
		typeName tmux.AgentType
		idle     string
		working  string
		draft    string
	}{
		{
			name: "pi", typeName: tmux.AgentPi,
			idle: piNudgeCapture("", false), working: piNudgeCapture("", true),
			draft: piNudgeCapture("keep my unfinished request", false),
		},
		{
			name: "opencode", typeName: tmux.AgentOpencode,
			idle:    opencodeGaugeCapture(t, "", "180,000", "90"),
			working: opencodeGaugeCapture(t, "", "180,000", "90") + "  esc interrupt\n",
			draft:   opencodeGaugeCapture(t, "keep my unfinished request", "180,000", "90"),
		},
		{
			name: "antigravity", typeName: tmux.AgentAntigravity,
			idle: antigravityNudgeCapture("", false), working: antigravityNudgeCapture("", true),
			draft: antigravityNudgeCapture("keep my unfinished request", false),
		},
	} {
		t.Run(harness.name, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				capture    string
				captureErr error
				wantReason string
			}{
				{name: "idle", capture: harness.idle},
				{name: "working", capture: harness.working, wantReason: "working"},
				{name: "unsubmitted draft", capture: harness.draft, wantReason: "unsubmitted_input"},
				{name: "empty capture", wantReason: "unknown_agent_state"},
				{name: "initializing", capture: "Initializing agent...\n", wantReason: "unknown_agent_state"},
				{name: "stale composer", capture: harness.idle + strings.Repeat("new unframed output\n", 3), wantReason: "unknown_agent_state"},
				{name: "missing capture", captureErr: errors.New("capture unavailable"), wantReason: "capture_failed"},
				{name: "rate limited", capture: harness.idle + "Error: rate limit exceeded\n", wantReason: "rate_limited"},
				{name: "interactive gate", capture: harness.idle + "Browser didn't open? Use the URL below:\nhttps://auth.example.com/device\n", wantReason: "interactive_gate:"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					pane := tmux.Pane{ID: "%90", Title: "mailsess__" + string(harness.typeName) + "_1", Type: harness.typeName, Width: 120}
					if harness.typeName == tmux.AgentOpencode {
						pane.Width = 200
					}
					env := newMailNudgeTestEnv(t, server.URL, []tmux.Pane{pane},
						map[string]string{pane.Title: "BlueLake"},
						func(string) (string, error) { return tc.capture, tc.captureErr })
					decisions := env.mc.runOnce(t.Context())
					t.Logf("harness=%s capture=%q decisions=%+v dispatch=%v", harness.name, tc.capture, decisions, env.dispatch)
					if len(decisions) != 1 {
						t.Fatalf("decisions = %+v, want one", decisions)
					}
					if tc.wantReason != "" {
						if decisions[0].Action != "skipped" || !strings.HasPrefix(decisions[0].SkipReason, tc.wantReason) || len(env.dispatch) != 0 {
							t.Fatalf("unsafe nudge: decisions=%+v dispatch=%v, want %s", decisions, env.dispatch, tc.wantReason)
						}
						return
					}
					if decisions[0].Action != "nudged" || len(env.dispatch) != 1 || env.dispatch[0] != pane.ID {
						t.Fatalf("idle native harness was not nudged: decisions=%+v dispatch=%v", decisions, env.dispatch)
					}
					if again := env.mc.runOnce(t.Context()); len(again) != 0 || len(env.dispatch) != 1 {
						t.Fatalf("repeat tick ignored cooldown: decisions=%+v dispatch=%v", again, env.dispatch)
					}
				})
			}
		})
	}
}

func TestMailNudgePiCompletionMenuRefused(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)},
	})
	defer server.Close()
	capture := strings.Replace(piNudgeCapture("", false), "/work/project", "→ /help\n  /settings\n/work/project", 1)
	pane := tmux.Pane{ID: "%91", Title: "mailsess__pi_1", Type: tmux.AgentPi, Width: 120}
	env := newMailNudgeTestEnv(t, server.URL, []tmux.Pane{pane}, map[string]string{pane.Title: "BlueLake"},
		func(string) (string, error) { return capture, nil })
	decisions := env.mc.runOnce(t.Context())
	if len(decisions) != 1 || decisions[0].SkipReason != "interactive_gate:completion_menu" || len(env.dispatch) != 0 {
		t.Fatalf("completion menu received input: decisions=%+v dispatch=%v capture=%q", decisions, env.dispatch, capture)
	}
}

func TestMailNudgeDeadAndServicePanesIgnored(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)},
	})
	defer server.Close()
	panes := []tmux.Pane{
		{ID: "%92", Title: "mailsess__pi_1", Type: tmux.AgentPi, Dead: true},
		{ID: "%93", Title: "mailsess__oc_1", Type: tmux.AgentOpencode, Service: "cm"},
	}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{panes[0].Title: "BlueLake", panes[1].Title: "BlueLake"},
		func(string) (string, error) { t.Error("ineligible pane captured"); return "", nil })
	env.mc.fetchInbox = func(context.Context, agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error) {
		t.Error("ineligible pane probed its inbox")
		return nil, nil
	}
	if decisions := env.mc.runOnce(t.Context()); len(decisions) != 0 || len(env.dispatch) != 0 {
		t.Fatalf("ineligible panes acted upon: decisions=%+v dispatch=%v", decisions, env.dispatch)
	}
}

func TestRunCycleNativeHarnessNudgesAndOpenCodeRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	redirectPendingStore(t)
	panes := []tmux.Pane{
		{ID: "%94", Title: "mailsess__pi_1", Type: tmux.AgentPi, Width: 120},
		{ID: "%95", Title: "mailsess__oc_1", Type: tmux.AgentOpencode, Width: 200},
		{ID: "%96", Title: "mailsess__agy_1", Type: tmux.AgentAntigravity, Width: 120},
	}
	captures := map[string]string{
		"%94": piNudgeCapture("", false),
		"%95": opencodeGaugeCapture(t, "", "180,000", "90"),
		"%96": antigravityNudgeCapture("", false),
	}
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"BlueLake": {unreadMessage(1)}, "GreenHill": {unreadMessage(2)}, "RedRock": {unreadMessage(3)},
	})
	defer server.Close()
	env := newMailNudgeTestEnv(t, server.URL, panes, map[string]string{
		panes[0].Title: "BlueLake", panes[1].Title: "GreenHill", panes[2].Title: "RedRock",
	}, func(id string) (string, error) { return captures[id], nil })
	rot := newRotationTestEnv(t, 80, false, panes, nil, captures, nil)

	originalList, originalCapture := getPanesWithActivity, captureForHealthCheckWithCtx
	t.Cleanup(func() {
		getPanesWithActivity, captureForHealthCheckWithCtx = originalList, originalCapture
	})
	getPanesWithActivity = func(context.Context, string) ([]tmux.PaneActivity, error) {
		activity := make([]tmux.PaneActivity, len(panes))
		for i, pane := range panes {
			activity[i] = tmux.PaneActivity{Pane: pane, LastActivity: time.Now().UTC()}
		}
		return activity, nil
	}
	captureForHealthCheckWithCtx = func(_ context.Context, id string) (string, error) { return captures[id], nil }
	c := New("mailsess", t.TempDir(), nil, "Coordinator")
	c.monitor = NewAgentMonitor(c.session, nil, c.projectKey)
	c.mailClient = agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	c.config.MailNudge = true
	c.config.RotationUsageThreshold = 80
	c.mailNudge, c.rotation = env.mc, rot.rc
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("native-harness coordinator tick: %v", err)
	}
	t.Logf("nudged=%v rotation_events=%+v", env.dispatch, rot.published)
	if len(env.dispatch) != len(panes) {
		t.Fatalf("native harnesses not nudged by tick: %v", env.dispatch)
	}
	if len(rot.published) != 1 || rot.published[0].ReasonCode != "context_rotation_enqueued" ||
		len(rot.published[0].Targets) != 1 || rot.published[0].Targets[0] != panes[1].Title {
		t.Fatalf("tick did not enqueue measured OpenCode rotation: %+v", rot.published)
	}
}
