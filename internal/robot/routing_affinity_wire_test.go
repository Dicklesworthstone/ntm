package robot

// Tests for wireReservationAffinity (bd-ws2-wire-or-delete-ykmcz.3): the
// send/route-time wiring that populates the tested ReservationCache from
// Agent Mail so the affinity bonus is real instead of permanently 0.
//
// Hermetic: the Agent Mail server is an in-process httptest stub speaking the
// same JSON-RPC resources/read surface the real client consumes. No live
// agent-mail installation, no tmux.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// newStubAgentMailReservationServer serves resources/read with the given
// reservation rows (as the resource JSON text payload).
func newStubAgentMailReservationServer(t *testing.T, reservationsJSON string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		body, _ := json.Marshal(map[string]any{})
		_ = body
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "resources/read" {
			// The wiring path only reads the reservation resource; anything
			// else (e.g. tool fallbacks) gets a JSON-RPC error.
			resp := map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"contents": []map[string]any{{"text": reservationsJSON}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)
	return server
}

func affinityWireConfig(url string) *config.Config {
	return &config.Config{
		Routing:   config.RoutingConfig{AffinityEnabled: true},
		AgentMail: config.AgentMailConfig{Enabled: true, URL: url},
	}
}

// scoreAffinityAgents computes final scores for two synthetic agents where %1
// (the reservation holder) starts with a strictly worse base score than %2.
func scoreAffinityAgents(scorer *AgentScorer, prompt string) (holderScore, otherScore float64, holderBonus float64) {
	holder := ScoredAgent{PaneID: "%1", AgentType: "cc", PaneIndex: 1, State: StateWaiting, ContextUsage: 20}
	other := ScoredAgent{PaneID: "%2", AgentType: "cc", PaneIndex: 2, State: StateWaiting, ContextUsage: 0}

	holder.ScoreDetail = scorer.calculateScoreComponents(&holder, prompt)
	other.ScoreDetail = scorer.calculateScoreComponents(&other, prompt)
	holder.Score = scorer.calculateFinalScore(&holder)
	other.Score = scorer.calculateFinalScore(&other)
	return holder.Score, other.Score, holder.ScoreDetail.AffinityBonus
}

// TestWireReservationAffinity_RankingFlip is the behavior-change proof for
// the FLIPPED C3 decision: with a live reservation on a file named in the
// prompt, held by the agent mapped to pane %1, that pane's affinity bonus
// rises enough to FLIP the ranking against an otherwise-better pane %2.
func TestWireReservationAffinity_RankingFlip(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	expires := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	created := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	reservations := fmt.Sprintf(
		`[{"id":1,"agent":"GreenCastle","path_pattern":"internal/robot/*.go","exclusive":true,"reason":"C3","created_ts":%q,"expires_ts":%q}]`,
		created, expires)
	server := newStubAgentMailReservationServer(t, reservations)

	cfg := affinityWireConfig(server.URL)
	prompt := "Fix the scoring bug in internal/robot/routing.go"

	// Baseline: identical scorer, wiring NOT invoked — the audit's finding:
	// the bonus is 0 and the better-context pane %2 wins.
	baseline := NewAgentScorerFromConfig(cfg)
	baseline.MapPaneToAgent("%1", "GreenCastle")
	baseHolder, baseOther, baseBonus := scoreAffinityAgents(baseline, prompt)
	if baseBonus != 0 {
		t.Fatalf("baseline affinity bonus = %v, want 0 (nothing populates the cache)", baseBonus)
	}
	if baseHolder >= baseOther {
		t.Fatalf("baseline ranking not as constructed: holder %v >= other %v", baseHolder, baseOther)
	}

	// Wired: cache populated from the stub Agent Mail server at scorer setup.
	scorer := NewAgentScorerFromConfig(cfg)
	scorer.wireReservationAffinity(cfg, "ntm-c3-wire-test-session")
	if scorer.reservationCache == nil {
		t.Fatal("wireReservationAffinity did not set the reservation cache")
	}
	scorer.MapPaneToAgent("%1", "GreenCastle")

	holderScore, otherScore, bonus := scoreAffinityAgents(scorer, prompt)
	if bonus <= 0 {
		t.Fatalf("affinity bonus = %v, want > 0 for the reservation holder", bonus)
	}
	if holderScore <= otherScore {
		t.Fatalf("ranking did not flip: holder %v <= other %v (bonus %v)", holderScore, otherScore, bonus)
	}
}

// TestWireReservationAffinity_GatedOff verifies the wiring is a no-op unless
// BOTH [routing] affinity_enabled AND [agent_mail] enabled are true, and for
// a nil config.
func TestWireReservationAffinity_GatedOff(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	cases := []struct {
		name string
		cfg  *config.Config
	}{
		{"nil config", nil},
		{"affinity disabled", &config.Config{
			Routing:   config.RoutingConfig{AffinityEnabled: false},
			AgentMail: config.AgentMailConfig{Enabled: true, URL: "http://127.0.0.1:1"},
		}},
		{"agent mail disabled", &config.Config{
			Routing:   config.RoutingConfig{AffinityEnabled: true},
			AgentMail: config.AgentMailConfig{Enabled: false, URL: "http://127.0.0.1:1"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scorer := NewAgentScorerFromConfig(tc.cfg)
			scorer.wireReservationAffinity(tc.cfg, "ntm-c3-gate-test")
			if scorer.reservationCache != nil {
				t.Fatal("reservation cache should not be constructed when gated off")
			}
			if scorer.config.AgentMail.Enabled {
				t.Fatal("scorer AgentMail.Enabled should stay false when gated off")
			}
		})
	}
}

// TestWireReservationAffinity_AgentMailAway verifies the degraded path: the
// Agent Mail server is unreachable, the wiring still succeeds (best-effort),
// and the affinity bonus contributes exactly 0 — enrichment, never a gate.
func TestWireReservationAffinity_AgentMailAway(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	// Reserved port with no listener: connection refused, fast.
	cfg := affinityWireConfig("http://127.0.0.1:1/mcp/")
	scorer := NewAgentScorerFromConfig(cfg)
	scorer.wireReservationAffinity(cfg, "ntm-c3-degraded-test")
	if scorer.reservationCache == nil {
		t.Fatal("cache should be constructed even when the server is away")
	}
	scorer.MapPaneToAgent("%1", "GreenCastle")

	holderScore, otherScore, bonus := scoreAffinityAgents(scorer, "Fix internal/robot/routing.go")
	if bonus != 0 {
		t.Fatalf("affinity bonus = %v, want 0 when Agent Mail is away", bonus)
	}
	if holderScore >= otherScore {
		t.Fatalf("degraded scoring should match baseline ranking: holder %v >= other %v", holderScore, otherScore)
	}
}
