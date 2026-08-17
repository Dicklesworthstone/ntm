package coordinator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// deadlockFixtureTimes gives the fixtures a strict creation order:
// t0 (earlier holder) < t1 (later claimant => waiter). They are anchored
// to the current wall clock (not absolute dates) because
// populateDeadlockAlerts evaluates reservations at real time.Now():
// with fixed dates the fixtures' created+24h expiry eventually passes,
// detectReservationConflictsAt filters every reservation as expired,
// and the digest test silently becomes a time bomb.
var (
	deadlockT0 = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	deadlockT1 = deadlockT0.Add(5 * time.Minute)
)

func deadlockReservation(id int, agent, pattern string, created time.Time) agentmail.FileReservation {
	return agentmail.FileReservation{
		ID:          id,
		PathPattern: pattern,
		AgentName:   agent,
		Exclusive:   true,
		Reason:      "test fixture",
		CreatedTS:   agentmail.FlexTime{Time: created},
		ExpiresTS:   agentmail.FlexTime{Time: created.Add(24 * time.Hour)},
	}
}

// twoCycleReservations models a real 2-cycle reservation deadlock:
// AgentA grabbed src/a/** first, AgentB grabbed docs/** first, and each
// then claimed a path inside the other's territory.
func twoCycleReservations() []agentmail.FileReservation {
	return []agentmail.FileReservation{
		deadlockReservation(1, "AgentA", "src/a/**", deadlockT0),
		deadlockReservation(2, "AgentB", "src/a/core.go", deadlockT1), // B waits on A
		deadlockReservation(3, "AgentB", "docs/**", deadlockT0),
		deadlockReservation(4, "AgentA", "docs/x.md", deadlockT1), // A waits on B
	}
}

// acyclicReservations has one-directional contention only: B waits on A,
// nothing waits on B.
func acyclicReservations() []agentmail.FileReservation {
	return []agentmail.FileReservation{
		deadlockReservation(1, "AgentA", "src/a/**", deadlockT0),
		deadlockReservation(2, "AgentB", "src/a/core.go", deadlockT1),
		deadlockReservation(3, "AgentB", "docs/**", deadlockT0),
	}
}

func TestWaitEdgesFromReservations_TwoCycleDetected(t *testing.T) {
	t.Parallel()

	now := deadlockT1.Add(time.Minute)
	edges := WaitEdgesFromReservations(twoCycleReservations(), now)
	report := DetectDeadlocks(edges, DetectDeadlockOptions{Now: func() time.Time { return now }})
	t.Logf("edges=%+v cycles=%+v", edges, report.Cycles)

	if len(report.Cycles) != 1 {
		t.Fatalf("expected exactly one cycle, got %d: %+v", len(report.Cycles), report.Cycles)
	}
	got := strings.Join(report.Cycles[0].Participants, "->")
	if got != "AgentA->AgentB" {
		t.Fatalf("cycle does not name the participants: %q", got)
	}
	if report.Cycles[0].Suggestion == "" {
		t.Fatalf("expected a resolution suggestion, got empty")
	}
}

func TestWaitEdgesFromReservations_AcyclicNoFalsePositive(t *testing.T) {
	t.Parallel()

	now := deadlockT1.Add(time.Minute)
	edges := WaitEdgesFromReservations(acyclicReservations(), now)
	report := DetectDeadlocks(edges, DetectDeadlockOptions{Now: func() time.Time { return now }})
	t.Logf("edges=%+v cycles=%+v", edges, report.Cycles)

	if len(edges) == 0 {
		t.Fatalf("expected the one-directional contention to produce wait edges")
	}
	if len(report.Cycles) != 0 {
		t.Fatalf("acyclic fixture must produce no cycles, got %+v", report.Cycles)
	}
}

func TestWaitEdgesFromReservations_TiedTimestampsProduceNoEdge(t *testing.T) {
	t.Parallel()

	reservations := []agentmail.FileReservation{
		deadlockReservation(1, "AgentA", "src/a/**", deadlockT0),
		deadlockReservation(2, "AgentB", "src/a/core.go", deadlockT0),
	}
	edges := WaitEdgesFromReservations(reservations, deadlockT0.Add(time.Minute))
	if len(edges) != 0 {
		t.Fatalf("unorderable contention must not fabricate edges, got %+v", edges)
	}
}

func TestDeadlockDigestLine(t *testing.T) {
	t.Parallel()

	if line := DeadlockDigestLine(DeadlockReport{}); line != "" {
		t.Fatalf("acyclic report must render no digest line, got %q", line)
	}

	report := DeadlockReport{Cycles: []DeadlockCycle{{
		Participants: []string{"AgentA", "AgentB"},
		Suggestion:   "ask AgentA to release reservations first",
	}}}
	line := DeadlockDigestLine(report)
	t.Logf("digest line: %s", line)
	for _, want := range []string{"AgentA -> AgentB -> AgentA", "deadlock", "ask AgentA to release reservations first"} {
		if !strings.Contains(line, want) {
			t.Fatalf("digest line %q missing %q", line, want)
		}
	}
}

// fakeAgentMailReservations serves the MCP resources/read endpoint with a
// fixed reservation list, mimicking a real Agent Mail server closely
// enough for Client.ListReservations' preferred resource path.
func fakeAgentMailReservations(t *testing.T, reservations []agentmail.FileReservation) *httptest.Server {
	t.Helper()
	payload, err := json.Marshal(reservations)
	if err != nil {
		t.Fatalf("marshaling fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method != "resources/read" {
			t.Errorf("unexpected method %q", req.Method)
		}
		contents := map[string]any{
			"contents": []map[string]any{{
				"uri":      "resource://file_reservations/test",
				"mimeType": "application/json",
				"text":     string(payload),
			}},
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": contents}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
}

func TestPopulateDeadlockAlerts_DigestNamesCycle(t *testing.T) {
	server := fakeAgentMailReservations(t, twoCycleReservations())
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	c := New("digest-session", "/test/project", client, "NTM-Coordinator")

	digest := DigestSummary{Session: "digest-session", GeneratedAt: time.Now()}
	c.populateDeadlockAlerts(context.Background(), &digest)

	if len(digest.Deadlocks) != 1 {
		t.Fatalf("expected one deadlock cycle in digest, got %+v", digest.Deadlocks)
	}
	found := false
	for _, alert := range digest.Alerts {
		if strings.Contains(alert, "AgentA -> AgentB -> AgentA") {
			found = true
		}
	}
	if !found {
		t.Fatalf("digest alerts do not name the cycle: %+v", digest.Alerts)
	}

	// The digest the user actually sees (bead review round 5): the
	// rendered markdown must carry the deadlock line.
	markdown := c.formatDigestMarkdown(digest)
	t.Logf("raw digest markdown:\n%s", markdown)
	if !strings.Contains(markdown, "AgentA -> AgentB -> AgentA") {
		t.Fatalf("rendered digest does not name the deadlock cycle")
	}
}

func TestPopulateDeadlockAlerts_AcyclicNoDigestLine(t *testing.T) {
	server := fakeAgentMailReservations(t, acyclicReservations())
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	c := New("digest-session", "/test/project", client, "NTM-Coordinator")

	digest := DigestSummary{Session: "digest-session", GeneratedAt: time.Now()}
	c.populateDeadlockAlerts(context.Background(), &digest)

	if len(digest.Deadlocks) != 0 {
		t.Fatalf("acyclic fixture must not attach deadlocks: %+v", digest.Deadlocks)
	}
	markdown := c.formatDigestMarkdown(digest)
	t.Logf("raw digest markdown:\n%s", markdown)
	if strings.Contains(strings.ToLower(markdown), "deadlock") {
		t.Fatalf("acyclic digest must carry no deadlock line:\n%s", markdown)
	}
}

func TestPopulateDeadlockAlerts_UnavailableMailIsSilent(t *testing.T) {
	t.Parallel()

	c := New("digest-session", "/test/project", nil, "NTM-Coordinator")
	digest := DigestSummary{}
	c.populateDeadlockAlerts(context.Background(), &digest)
	if len(digest.Alerts) != 0 || len(digest.Deadlocks) != 0 {
		t.Fatalf("nil mail client must be a silent no-op: %+v", digest)
	}

	// A reachable-but-erroring server must also stay silent rather than
	// fabricate (or suppress) state noisily.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	c2 := New("digest-session", "/test/project", agentmail.NewClient(agentmail.WithBaseURL(server.URL+"/")), "NTM-Coordinator")
	digest2 := DigestSummary{}
	c2.populateDeadlockAlerts(context.Background(), &digest2)
	if len(digest2.Alerts) != 0 || len(digest2.Deadlocks) != 0 {
		t.Fatalf("erroring mail server must be a silent no-op: %+v", digest2)
	}
}

// Guard against fixture drift: the fixtures must be "active" from the
// conflict detector's perspective at the analysis time the tests use.
func TestDeadlockFixturesAreActive(t *testing.T) {
	t.Parallel()
	now := deadlockT1.Add(time.Minute)
	for _, r := range twoCycleReservations() {
		if !reservationActiveAt(r, now) {
			t.Fatalf("fixture reservation %d (%s) not active at %s", r.ID, r.PathPattern, now)
		}
	}
}
