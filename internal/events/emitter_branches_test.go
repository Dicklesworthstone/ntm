package events

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// ---------------------------------------------------------------------------
// NewEventEmitter — cover nil bus and buffer < 1 defaults
// ---------------------------------------------------------------------------

func TestNewEventEmitter_NilBus(t *testing.T) {
	t.Parallel()

	emitter := NewEventEmitter(nil, 10)
	if emitter.bus != DefaultBus {
		t.Errorf("NewEventEmitter(nil, 10).bus = %p, want DefaultBus (%p)", emitter.bus, DefaultBus)
	}
}

func TestNewEventEmitter_ZeroBuffer(t *testing.T) {
	t.Parallel()

	emitter := NewEventEmitter(DefaultBus, 0)
	if cap(emitter.ch) != 256 {
		t.Errorf("NewEventEmitter(_, 0) buffer cap = %d, want 256", cap(emitter.ch))
	}
}

func TestNewEventEmitter_NegativeBuffer(t *testing.T) {
	t.Parallel()

	emitter := NewEventEmitter(DefaultBus, -5)
	if cap(emitter.ch) != 256 {
		t.Errorf("NewEventEmitter(_, -5) buffer cap = %d, want 256", cap(emitter.ch))
	}
}

// ---------------------------------------------------------------------------
// Emit — cover nil event check
// ---------------------------------------------------------------------------

func TestEmit_NilEvent(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(10)
	emitter := NewEventEmitter(bus, 8)

	// Should not panic and return early
	emitter.Emit(nil)

	// Verify dropped counter is still 0 (nil events don't count as dropped)
	if emitter.dropped.Load() != 0 {
		t.Errorf("Emit(nil) incremented dropped counter to %d", emitter.dropped.Load())
	}
}

// ---------------------------------------------------------------------------
// redactDataMap — cover nil data branch
// ---------------------------------------------------------------------------

func TestRedactDataMap_Nil(t *testing.T) {
	t.Parallel()

	SetRedactionConfig(&redaction.Config{Mode: redaction.ModeRedact})
	defer SetRedactionConfig(nil)

	result := redactDataMap(nil)
	if result != nil {
		t.Errorf("redactDataMap(nil) = %v, want nil", result)
	}
}
