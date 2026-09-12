package state

// Coverage for runtime-store primitives that gate actions rather than merely
// record progress.

import (
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
