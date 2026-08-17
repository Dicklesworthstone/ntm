package coordinator

// Tests for the RunCycle conflict-negotiation wiring
// (bd-ws2-wire-or-delete-ykmcz.1). These are through-the-surface tests: a
// real Agent Mail wire protocol (httptest MCP JSON-RPC server), a real temp
// project store, a fake session state (no panes), and a genuine two-agent
// file conflict. The proof standard: the published negotiation OUTCOME must
// name the specific conflicting pair and the engine's resolution decision —
// invoking the engine and discarding its result fails these assertions.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// mailToolCall records one tools/call the coordinator made against the fake
// Agent Mail server.
type mailToolCall struct {
	Tool string
	Args map[string]any
}

// conflictWireRecorder captures all tool calls so tests can assert not just
// what happened but what did NOT (e.g. notify-only must not mutate).
type conflictWireRecorder struct {
	mu    sync.Mutex
	calls []mailToolCall
}

func (r *conflictWireRecorder) record(tool string, args map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, mailToolCall{Tool: tool, Args: args})
}

func (r *conflictWireRecorder) snapshot() []mailToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mailToolCall(nil), r.calls...)
}

func (r *conflictWireRecorder) callsFor(tool string) []mailToolCall {
	var out []mailToolCall
	for _, c := range r.snapshot() {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// newConflictWireMailServer starts a real HTTP MCP JSON-RPC server serving
// the given file reservations resource and recording every tool call.
func newConflictWireMailServer(t *testing.T, reservationsJSON string) (*agentmail.Client, *conflictWireRecorder) {
	t.Helper()
	recorder := &conflictWireRecorder{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var rpc struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil {
			t.Errorf("fake mail server: decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		idJSON, _ := json.Marshal(rpc.ID)

		switch rpc.Method {
		case "resources/read":
			contents, _ := json.Marshal(map[string]any{
				"contents": []map[string]any{{"text": reservationsJSON}},
			})
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, idJSON, contents)
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(rpc.Params, &params); err != nil {
				t.Errorf("fake mail server: decode tools/call params: %v", err)
				return
			}
			recorder.record(params.Name, params.Arguments)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"id":1}}`, idJSON)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"unknown method"}}`, idJSON)
		}
	}))
	t.Cleanup(server.Close)

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL))
	return client, recorder
}

// conflictingPairReservationsJSON builds the resource payload for a genuine
// two-agent exclusive conflict on the same file. AgentAlpha reserved first.
func conflictingPairReservationsJSON(pattern string) string {
	now := time.Now().UTC()
	payload, _ := json.Marshal([]map[string]any{
		{
			"id":           1,
			"agent":        "AgentAlpha",
			"path_pattern": pattern,
			"exclusive":    true,
			"reason":       "wiring lane C1",
			"created_ts":   now.Add(-30 * time.Minute).Format(time.RFC3339),
			"expires_ts":   now.Add(time.Hour).Format(time.RFC3339),
		},
		{
			"id":           2,
			"agent":        "AgentBeta",
			"path_pattern": pattern,
			"exclusive":    true,
			"reason":       "wiring lane C4",
			"created_ts":   now.Add(-5 * time.Minute).Format(time.RFC3339),
			"expires_ts":   now.Add(time.Hour).Format(time.RFC3339),
		},
	})
	return string(payload)
}

// newConflictWireCoordinator builds a coordinator on a real temp project
// store with a fake (empty) tmux session state so RunCycle's observation
// phase succeeds without a live tmux server.
func newConflictWireCoordinator(t *testing.T, client *agentmail.Client, cfg CoordinatorConfig) *SessionCoordinator {
	t.Helper()
	origGetPanes := getPanesWithActivity
	t.Cleanup(func() { getPanesWithActivity = origGetPanes })
	getPanesWithActivity = func(string) ([]tmux.PaneActivity, error) { return nil, nil }

	return New("conflict-wire-test", t.TempDir(), client, "TestCoordinator").WithConfig(cfg)
}

// drainConflictEvents collects all currently queued coordinator events.
func drainConflictEvents(c *SessionCoordinator) []CoordinatorEvent {
	var events []CoordinatorEvent
	for {
		select {
		case ev := <-c.Events():
			events = append(events, ev)
		default:
			return events
		}
	}
}

func findConflictOutcomeEvent(t *testing.T, events []CoordinatorEvent, wantType CoordinatorEventType) CoordinatorEvent {
	t.Helper()
	for _, ev := range events {
		if ev.Type == wantType {
			if _, ok := ev.Details["resolution"]; ok {
				return ev
			}
		}
	}
	t.Fatalf("no %s outcome event found in %d events: %+v", wantType, len(events), events)
	return CoordinatorEvent{}
}

func detailString(t *testing.T, ev CoordinatorEvent, key string) string {
	t.Helper()
	value, ok := ev.Details[key].(string)
	if !ok {
		t.Fatalf("event detail %q missing or not a string: %+v", key, ev.Details)
	}
	return value
}

// TestRunCycle_ConflictNegotiate_PublishesOutcomeNamingPair is the bead's
// core proof: flag ON + a genuine two-agent conflict → RunCycle publishes a
// negotiation outcome on the event envelope that names the specific
// conflicting pair AND the engine's resolution decision, and the engine's
// negotiation request actually reaches the losing holder over the wire.
func TestRunCycle_ConflictNegotiate_PublishesOutcomeNamingPair(t *testing.T) {
	const pattern = "internal/coordinator/coordinator.go"
	client, recorder := newConflictWireMailServer(t, conflictingPairReservationsJSON(pattern))

	cfg := DefaultCoordinatorConfig()
	cfg.ConflictNegotiate = true
	c := newConflictWireCoordinator(t, client, cfg)

	if _, err := c.RunCycle(context.Background()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	// The envelope must carry the outcome: pair + resolution decision.
	ev := findConflictOutcomeEvent(t, drainConflictEvents(c), EventConflictResolved)
	holders, ok := ev.Details["holders"].([]string)
	if !ok {
		t.Fatalf("outcome event holders missing or wrong type: %+v", ev.Details)
	}
	if len(holders) != 2 || holders[0] != "AgentAlpha" || holders[1] != "AgentBeta" {
		t.Fatalf("outcome must name the conflicting pair AgentAlpha/AgentBeta, got %v", holders)
	}
	resolution := detailString(t, ev, "resolution")
	for _, want := range []string{"AgentAlpha", "AgentBeta", "release", pattern} {
		if !strings.Contains(resolution, want) {
			t.Fatalf("resolution %q must contain %q (naming the pair and the decision)", resolution, want)
		}
	}
	if got := detailString(t, ev, "requester"); got != "AgentAlpha" {
		t.Fatalf("requester = %q, want AgentAlpha (earliest reservation wins)", got)
	}
	if got := detailString(t, ev, "asked_to_release"); got != "AgentBeta" {
		t.Fatalf("asked_to_release = %q, want AgentBeta (latest reservation yields)", got)
	}
	if got := detailString(t, ev, "pattern"); got != pattern {
		t.Fatalf("pattern = %q, want %q", got, pattern)
	}

	// The engine's decision must have reached the losing holder over the
	// real wire protocol: one ack-required negotiation request to AgentBeta.
	sends := recorder.callsFor("send_message")
	if len(sends) != 1 {
		t.Fatalf("expected exactly 1 send_message, got %d: %+v", len(sends), recorder.snapshot())
	}
	args := sends[0].Args
	to, _ := args["to"].([]any)
	if len(to) != 1 || to[0] != "AgentBeta" {
		t.Fatalf("negotiation request must target AgentBeta, got to=%v", to)
	}
	if ack, _ := args["ack_required"].(bool); !ack {
		t.Fatalf("negotiation request must be ack_required, args=%v", args)
	}
	subject, _ := args["subject"].(string)
	if !strings.Contains(subject, pattern) {
		t.Fatalf("negotiation subject %q must name the conflicting pattern %q", subject, pattern)
	}
}

// TestRunCycle_ConflictFlagsOff_EngineNeverInvoked pins the default-off
// guarantee: with both persisted flags disabled, RunCycle must not invoke
// conflict detection or the negotiation engine at all — asserted both via
// the detection seam and via the total absence of wire traffic.
func TestRunCycle_ConflictFlagsOff_EngineNeverInvoked(t *testing.T) {
	client, recorder := newConflictWireMailServer(t, conflictingPairReservationsJSON("internal/coordinator/assign.go"))

	cfg := DefaultCoordinatorConfig()
	cfg.ConflictNotify = false
	cfg.ConflictNegotiate = false
	c := newConflictWireCoordinator(t, client, cfg)

	detectCalls := 0
	c.detectConflictsFn = func(context.Context) ([]Conflict, error) {
		detectCalls++
		return nil, nil
	}

	if _, err := c.RunCycle(context.Background()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	if detectCalls != 0 {
		t.Fatalf("conflict detection invoked %d times with both flags off; want 0", detectCalls)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("expected zero Agent Mail tool calls with both flags off, got %+v", calls)
	}
	for _, ev := range drainConflictEvents(c) {
		if ev.Type == EventConflictDetected || ev.Type == EventConflictResolved {
			t.Fatalf("unexpected conflict event with both flags off: %+v", ev)
		}
	}
}

// TestRunCycle_ConflictNotifyOnly_NotificationWithoutMutation covers the
// notify-only mode (the persisted default): both holders are informed of the
// named conflict, but no release is requested and nothing is mutated.
func TestRunCycle_ConflictNotifyOnly_NotificationWithoutMutation(t *testing.T) {
	const pattern = "internal/coordinator/monitor.go"
	client, recorder := newConflictWireMailServer(t, conflictingPairReservationsJSON(pattern))

	cfg := DefaultCoordinatorConfig() // ConflictNotify=true, ConflictNegotiate=false
	c := newConflictWireCoordinator(t, client, cfg)

	if _, err := c.RunCycle(context.Background()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	ev := findConflictOutcomeEvent(t, drainConflictEvents(c), EventConflictDetected)
	if got, _ := ev.Details["mode"].(string); got != "notify" {
		t.Fatalf("mode = %q, want notify", got)
	}
	resolution := detailString(t, ev, "resolution")
	for _, want := range []string{"AgentAlpha", "AgentBeta", "notify-only", pattern} {
		if !strings.Contains(resolution, want) {
			t.Fatalf("notify resolution %q must contain %q", resolution, want)
		}
	}

	// Exactly one notification, to both holders, no ack demand — and no
	// other tool call of any kind (no release, no mutation).
	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].Tool != "send_message" {
		t.Fatalf("expected exactly one send_message and nothing else, got %+v", calls)
	}
	args := calls[0].Args
	to, _ := args["to"].([]any)
	if len(to) != 2 || to[0] != "AgentAlpha" || to[1] != "AgentBeta" {
		t.Fatalf("notification must go to both holders, got to=%v", to)
	}
	if ack, ok := args["ack_required"].(bool); ok && ack {
		t.Fatalf("notify-only must not demand acknowledgement, args=%v", args)
	}
}

// TestRunCycle_ConflictCooldown_BoundsRepeatedTicks: the same persisting
// conflict must not re-trigger mail on every poll tick.
func TestRunCycle_ConflictCooldown_BoundsRepeatedTicks(t *testing.T) {
	const pattern = "internal/coordinator/digest.go"
	client, recorder := newConflictWireMailServer(t, conflictingPairReservationsJSON(pattern))

	cfg := DefaultCoordinatorConfig()
	cfg.ConflictNegotiate = true
	c := newConflictWireCoordinator(t, client, cfg)

	for i := 0; i < 3; i++ {
		if _, err := c.RunCycle(context.Background()); err != nil {
			t.Fatalf("RunCycle #%d: %v", i+1, err)
		}
	}

	if sends := recorder.callsFor("send_message"); len(sends) != 1 {
		t.Fatalf("cooldown must bound repeat negotiation: got %d send_message calls, want 1", len(sends))
	}
}

// TestRunConflictCycle_BoundedPerTick pins the per-tick work bound with many
// simultaneous conflicts.
func TestRunConflictCycle_BoundedPerTick(t *testing.T) {
	cfg := DefaultCoordinatorConfig() // notify-only
	c := newConflictWireCoordinator(t, nil, cfg)

	now := time.Now()
	var conflicts []Conflict
	for i := 0; i < maxConflictOutcomesPerCycle+4; i++ {
		conflicts = append(conflicts, Conflict{
			ID:      fmt.Sprintf("conflict-%d", i),
			Pattern: fmt.Sprintf("internal/pkg%02d/file.go", i),
			Holders: []Holder{
				{AgentName: "AgentAlpha", ReservedAt: now.Add(-time.Hour)},
				{AgentName: "AgentBeta", ReservedAt: now.Add(-time.Minute)},
			},
			DetectedAt: now,
		})
	}
	c.detectConflictsFn = func(context.Context) ([]Conflict, error) { return conflicts, nil }

	outcomes := c.runConflictCycle(context.Background())
	if len(outcomes) != maxConflictOutcomesPerCycle {
		t.Fatalf("per-tick bound violated: got %d outcomes, want %d", len(outcomes), maxConflictOutcomesPerCycle)
	}
}

// TestPrioritizeHolders_Deterministic pins requester/target selection:
// earliest reservation wins; ties break by agent name; priorities are
// mutated so NegotiateConflict targets the reported holder.
func TestPrioritizeHolders_Deterministic(t *testing.T) {
	now := time.Now()
	holders := []Holder{
		{AgentName: "Zeta", ReservedAt: now.Add(-time.Minute)},
		{AgentName: "Alpha", ReservedAt: now.Add(-time.Hour)},
		{AgentName: "Beta", ReservedAt: now.Add(-time.Hour)},
	}
	requester, target := prioritizeHolders(holders)
	if requester != "Alpha" {
		t.Fatalf("requester = %q, want Alpha (earliest, name tie-break)", requester)
	}
	if target != "Zeta" {
		t.Fatalf("target = %q, want Zeta (latest reservation yields)", target)
	}
	for _, h := range holders {
		switch h.AgentName {
		case "Alpha":
			if h.Priority != 0 {
				t.Fatalf("Alpha priority = %d, want 0", h.Priority)
			}
		case "Zeta":
			if h.Priority != 2 {
				t.Fatalf("Zeta priority = %d, want 2", h.Priority)
			}
		}
	}
}
