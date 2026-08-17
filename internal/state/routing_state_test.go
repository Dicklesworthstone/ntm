package state

import (
	"path/filepath"
	"testing"
)

func TestRoutingState_RoundTripAndUpsert(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// No history yet: nil, no error.
	rs, err := store.GetRoutingState("proj")
	if err != nil || rs != nil {
		t.Fatalf("GetRoutingState(empty) = %+v, %v; want nil, nil", rs, err)
	}

	if err := store.SaveRoutingState(&RoutingState{SessionName: "proj", LastAgent: "%2", RotationCursor: 1}); err != nil {
		t.Fatalf("save: %v", err)
	}
	rs, err = store.GetRoutingState("proj")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rs == nil || rs.LastAgent != "%2" || rs.RotationCursor != 1 || rs.UpdatedAt.IsZero() {
		t.Fatalf("round-trip mismatch: %+v", rs)
	}

	// Upsert advances in place.
	if err := store.SaveRoutingState(&RoutingState{SessionName: "proj", LastAgent: "%3", RotationCursor: 2}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rs, err = store.GetRoutingState("proj")
	if err != nil || rs == nil || rs.LastAgent != "%3" || rs.RotationCursor != 2 {
		t.Fatalf("upsert mismatch: %+v (err %v)", rs, err)
	}

	// Sessions are isolated.
	other, err := store.GetRoutingState("otherproj")
	if err != nil || other != nil {
		t.Fatalf("cross-session leak: %+v, %v", other, err)
	}

	if err := store.SaveRoutingState(nil); err == nil {
		t.Fatal("SaveRoutingState(nil) must error")
	}
	if err := store.SaveRoutingState(&RoutingState{}); err == nil {
		t.Fatal("SaveRoutingState without session must error")
	}
}
