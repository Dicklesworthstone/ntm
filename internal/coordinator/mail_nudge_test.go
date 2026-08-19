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

	mc.getPanes = func(string) ([]tmux.Pane, error) { return panes, nil }
	mc.agentLookup = func() func(paneTitle, paneID string) (string, bool) {
		return func(paneTitle, paneID string) (string, bool) {
			name, ok := agents[paneTitle]
			return name, ok
		}
	}
	mc.capturePane = func(paneID string, _ int) (string, error) { return capture(paneID) }
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
	return map[string]interface{}{"id": id, "subject": "old news", "from": "GreenCastle", "read_at": "2026-08-18T10:00:00Z"}
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
// working detector are refused outright.
func TestMailNudgeFailsClosedForUnsupportedAgentTypes(t *testing.T) {
	server := newMailInboxServer(t, map[string][]map[string]interface{}{
		"GemBird": {unreadMessage(1)},
	})
	defer server.Close()

	panes := []tmux.Pane{{ID: "%3", Title: "mailsess__gmi_1", Type: tmux.AgentGemini, Width: 120}}
	env := newMailNudgeTestEnv(t, server.URL, panes,
		map[string]string{"mailsess__gmi_1": "GemBird"},
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
