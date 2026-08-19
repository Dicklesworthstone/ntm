package startup

import "time"

// Test-only helpers replicating removed production accessors so tests can
// isolate and assert live phase/lazy/config behavior.

// Reset clears all startup state between tests.
func Reset() {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.currentPhase = PhaseNone
	global.phase1Done = false
	global.phase2Done = false
	global.phase1Start = time.Time{}
	global.phase2Start = time.Time{}
	global.phase1Time = 0
	global.phase2Time = 0
	global.deferredFuncs = nil
	global.initialized = make(map[string]bool)
}

// CurrentPhase returns the current startup phase.
func CurrentPhase() Phase {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.currentPhase
}

// IsPhase1Complete returns true if Phase 1 is done.
func IsPhase1Complete() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.phase1Done
}

// IsPhase2Complete returns true if Phase 2 is done.
func IsPhase2Complete() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.phase2Done
}

// IsInitialized reports whether the lazy value has been initialized.
func (l *Lazy[T]) IsInitialized() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.initialized[l.name]
}

// IsConfigLoaded returns true if config has been loaded.
func IsConfigLoaded() bool {
	return configLoader.IsInitialized()
}
