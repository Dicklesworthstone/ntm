package pressure

import (
	"context"
	"sync"
)

// FakeProvider is the deterministic provider used by tests. Readings can
// be updated atomically with Set.
type FakeProvider struct {
	name string

	mu       sync.RWMutex
	readings []Reading
	err      error
}

// NewFakeProvider builds a fake provider with an initial reading set.
func NewFakeProvider(name string, readings ...Reading) *FakeProvider {
	rs := append([]Reading(nil), readings...)
	return &FakeProvider{name: name, readings: rs}
}

// Name returns the provider name.
func (f *FakeProvider) Name() string { return f.name }

// Read returns the most recent set of readings.
func (f *FakeProvider) Read(ctx context.Context) ([]Reading, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Reading, len(f.readings))
	copy(out, f.readings)
	return out, nil
}

// Set replaces the provider's readings.
func (f *FakeProvider) Set(readings ...Reading) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readings = append([]Reading(nil), readings...)
}

// SetError stores an error to be returned by the next Read call. Pass
// nil to clear.
func (f *FakeProvider) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}
