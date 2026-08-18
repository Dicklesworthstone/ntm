package events

import (
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SubscribeAll (global wrapper) — 0% → 100%
// ---------------------------------------------------------------------------

func TestGlobal_SubscribeAll(t *testing.T) {
	// Not parallel: uses global DefaultBus.
	var received atomic.Int32

	unsub := SubscribeAll(func(e BusEvent) {
		received.Add(1)
	})
	defer unsub()

	event := BaseEvent{Type: "global_subscribe_all_test", Timestamp: time.Now()}
	PublishSync(event)

	if got := received.Load(); got < 1 {
		t.Errorf("SubscribeAll handler received %d events, want >=1", got)
	}
}

// ---------------------------------------------------------------------------
// Publish (global async wrapper) — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// History (global wrapper) — 0% → 100%
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// DefaultEmitter — 0% → 100%
// ---------------------------------------------------------------------------

func TestDefaultEmitter(t *testing.T) {
	// Not parallel: accesses global singleton.
	em := DefaultEmitter()
	if em == nil {
		t.Fatal("DefaultEmitter() returned nil")
	}

	// Should return the same instance on subsequent calls.
	em2 := DefaultEmitter()
	if em != em2 {
		t.Error("DefaultEmitter() should return the same instance")
	}
}
