package state

// Coverage for runtime-store primitives that gate actions rather than merely
// record progress.

import (
	"fmt"
	"testing"
	"time"
)

// TestClaimWatermarkRejectsConcurrentClaim covers watermarks that gate an
// ACTION rather than record progress. The CAAM failover checker reads the
// per-pane switch watermark, then runs its remaining gates — including a real
// `caam list --json` subprocess — and only then records. Nothing stops two
// `ntm coordinator run` processes watching one session, so both could read
// "not recently switched", both pass every gate, and both rotate the same
// pane seconds apart, breaking the one-hour cooldown the gate documents.
func TestClaimWatermarkRejectsConcurrentClaim(t *testing.T) {
	store := testStore(t)

	const wmType, scope = "caam_failover", "sess:cc_1"
	seed := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	if err := store.SetWatermark(&OutputWatermark{
		WatermarkType: wmType, Scope: scope, LastTs: &seed,
		CreatedAt: seed, UpdatedAt: seed,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Both coordinators read the same watermark and decide to switch.
	observed, err := store.GetWatermark(wmType, scope)
	if err != nil || observed == nil || observed.LastTs == nil {
		t.Fatalf("read: %v (wm=%+v)", err, observed)
	}
	expected := *observed.LastTs

	firstAt := time.Now().UTC().Truncate(time.Second)
	claimed, err := store.ClaimWatermark(&OutputWatermark{
		WatermarkType: wmType, Scope: scope, LastTs: &firstAt,
		CreatedAt: firstAt, UpdatedAt: firstAt,
	}, &expected)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("the first claimant must win an uncontended slot")
	}

	secondAt := firstAt.Add(2 * time.Second)
	claimed, err = store.ClaimWatermark(&OutputWatermark{
		WatermarkType: wmType, Scope: scope, LastTs: &secondAt,
		CreatedAt: secondAt, UpdatedAt: secondAt,
	}, &expected)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Error("a peer claimed the same cooldown slot; the pane would be rotated twice inside the cooldown")
	}

	final, err := store.GetWatermark(wmType, scope)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !final.LastTs.Equal(firstAt) {
		t.Errorf("watermark = %v, want the first claimant's %v", final.LastTs, firstAt)
	}
}

// TestClaimWatermarkFirstClaimantWinsEmptySlot covers the no-row case: two
// processes that both saw no watermark must not both believe they claimed it.
func TestClaimWatermarkFirstClaimantWinsEmptySlot(t *testing.T) {
	store := testStore(t)

	const wmType, scope = "caam_failover", "sess:cc_2"
	first := time.Now().UTC().Truncate(time.Second)
	ok, err := store.ClaimWatermark(&OutputWatermark{
		WatermarkType: wmType, Scope: scope, LastTs: &first,
		CreatedAt: first, UpdatedAt: first,
	}, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !ok {
		t.Fatal("claiming an empty slot must succeed")
	}

	second := first.Add(time.Second)
	ok, err = store.ClaimWatermark(&OutputWatermark{
		WatermarkType: wmType, Scope: scope, LastTs: &second,
		CreatedAt: second, UpdatedAt: second,
	}, nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if ok {
		t.Error("a second claimant that also saw no row took the slot as well")
	}
}

func TestRuntimeAgentContextPercentRoundTripsUnknownAndZero(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	if err := store.UpsertRuntimeSession(&RuntimeSession{Name: "context", CollectedAt: now, StaleAfter: expires}); err != nil {
		t.Fatal(err)
	}
	zero, measured := 0.0, 73.5
	for _, mode := range []string{"direct", "transaction"} {
		t.Run(mode, func(t *testing.T) {
			agent := &RuntimeAgent{
				ID: "context:" + mode, SessionName: "context", Pane: mode,
				AgentType: "claude", State: AgentStateIdle, HealthStatus: HealthStatusHealthy,
				CollectedAt: now, StaleAfter: expires,
			}
			for _, want := range []*float64{nil, &zero, &measured, nil} {
				agent.ContextPercent = want
				var err error
				if mode == "transaction" {
					err = store.Transaction(func(tx *Tx) error { return tx.UpsertRuntimeAgent(agent) })
				} else {
					err = store.UpsertRuntimeAgent(agent)
				}
				if err != nil {
					t.Fatal(err)
				}
				byID, err := store.GetRuntimeAgent(agent.ID)
				if err != nil || byID == nil {
					t.Fatalf("GetRuntimeAgent: %+v, %v", byID, err)
				}
				rows, err := store.GetRuntimeAgentsBySession("context")
				if err != nil {
					t.Fatal(err)
				}
				var listed *RuntimeAgent
				for i := range rows {
					if rows[i].ID == agent.ID {
						listed = &rows[i]
					}
				}
				if listed == nil {
					t.Fatalf("agent %s missing from fresh session rows", agent.ID)
				}
				for _, got := range []*RuntimeAgent{byID, listed} {
					if (got.ContextPercent == nil) != (want == nil) {
						t.Fatalf("context presence = %v, want %v", got.ContextPercent, want)
					}
					if want != nil && *got.ContextPercent != *want {
						t.Fatalf("context = %v, want %v", *got.ContextPercent, *want)
					}
				}
			}
		})
	}
	// Rows written by an older binary omit the new column. Missing evidence
	// must read as unknown rather than as a measured empty context.
	if _, err := store.db.Exec("INSERT INTO runtime_agents (id, session_name, pane, agent_type, collected_at, stale_after) VALUES (?, ?, ?, ?, ?, ?)",
		"context:legacy", "context", "legacy", "claude", now, expires); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.GetRuntimeAgent("context:legacy")
	if err != nil || legacy == nil || legacy.ContextPercent != nil {
		t.Fatalf("legacy context should remain unknown: %+v, %v", legacy, err)
	}
}

func TestRunGCCompactsAuditDecisionLogAndGetDecisionHistoryFilters(t *testing.T) {
	store := testStore(t)

	empty, err := store.GetDecisionHistory("nonexistent", "", 10)
	if err != nil {
		t.Fatalf("GetDecisionHistory empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty decisions = %#v, want non-nil empty slice", empty)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 12; i++ {
		entityType := "attention_item"
		if i%2 == 1 {
			entityType = "approval"
		}
		_, err := store.RecordAuditDecision(&AuditDecision{
			DecisionType: "ack",
			DecisionAt:   base.Add(time.Duration(i) * time.Second),
			ActorType:    "user",
			ActorID:      "operator",
			EntityType:   entityType,
			EntityID:     fmt.Sprintf("item-%02d", i),
			Reason:       "routine triage",
		})
		if err != nil {
			t.Fatalf("RecordAuditDecision(%d): %v", i, err)
		}
	}

	all, err := store.GetDecisionHistory("", "", 0)
	if err != nil {
		t.Fatalf("GetDecisionHistory all: %v", err)
	}
	if len(all) != 12 {
		t.Fatalf("len(all) = %d, want 12", len(all))
	}
	if all[0].EntityID != "item-11" {
		t.Fatalf("newest decision = %q, want item-11", all[0].EntityID)
	}

	onlyAttention, err := store.GetDecisionHistory("attention_item", "", 0)
	if err != nil {
		t.Fatalf("GetDecisionHistory filtered by type: %v", err)
	}
	if len(onlyAttention) != 6 {
		t.Fatalf("len(onlyAttention) = %d, want 6", len(onlyAttention))
	}

	gcRes, err := store.RunGC(RuntimeGCConfig{MaxAuditDecisionRows: 5})
	if err != nil {
		t.Fatalf("RunGC: %v", err)
	}
	if gcRes.CompactedAuditDecisions != 7 {
		t.Fatalf("CompactedAuditDecisions = %d, want 7", gcRes.CompactedAuditDecisions)
	}

	remaining, err := store.GetDecisionHistory("", "", 0)
	if err != nil {
		t.Fatalf("GetDecisionHistory after GC: %v", err)
	}
	if len(remaining) != 5 {
		t.Fatalf("len(remaining) = %d, want 5", len(remaining))
	}
	if remaining[0].EntityID != "item-11" || remaining[4].EntityID != "item-07" {
		t.Fatalf("remaining window = [%s..%s], want [item-11..item-07]", remaining[0].EntityID, remaining[4].EntityID)
	}
}
