package startup

import (
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/profiler"
)

// Lazy provides thread-safe lazy initialization for any type T
type Lazy[T any] struct {
	mu      sync.RWMutex
	value   T
	init    func() (T, error)
	done    bool
	initErr error
	name    string
	phase   string
}

// NewLazy creates a lazy initializer with the given initialization function
func NewLazy[T any](name string, init func() (T, error)) *Lazy[T] {
	return &Lazy[T]{
		name:  name,
		init:  init,
		phase: "deferred",
	}
}

// NewLazyWithPhase creates a lazy initializer with custom phase annotation
func NewLazyWithPhase[T any](name, phase string, init func() (T, error)) *Lazy[T] {
	return &Lazy[T]{
		name:  name,
		init:  init,
		phase: phase,
	}
}

// Get returns the lazily initialized value, initializing it if necessary
func (l *Lazy[T]) Get() (T, error) {
	// Fast path: already initialized
	l.mu.RLock()
	if l.done {
		val, err := l.value, l.initErr
		l.mu.RUnlock()
		return val, err
	}
	l.mu.RUnlock()

	// Slow path: acquire write lock and initialize
	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if l.done {
		return l.value, l.initErr
	}

	span := profiler.StartWithPhase("lazy_init_"+l.name, l.phase)
	defer span.End()

	l.value, l.initErr = l.init()
	if l.initErr != nil {
		span.Tag("error", l.initErr.Error())
	}
	l.done = true
	markInitialized(l.name)

	return l.value, l.initErr
}

// MustGet returns the value, panicking on error
func (l *Lazy[T]) MustGet() T {
	val, err := l.Get()
	if err != nil {
		panic("lazy init failed for " + l.name + ": " + err.Error())
	}
	return val
}

// IsInitialized returns true if the value has been initialized
func (l *Lazy[T]) IsInitialized() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return IsInitialized(l.name)
}

// Reset allows re-initialization (useful for testing)
func (l *Lazy[T]) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = false
	l.initErr = nil
	var zero T
	l.value = zero
}

// LazyValue is a simplified lazy initializer that doesn't return errors
type LazyValue[T any] struct {
	mu    sync.RWMutex
	value T
	init  func() T
	done  bool
	name  string
}

// NewLazyValue creates a lazy initializer that doesn't fail
func NewLazyValue[T any](name string, init func() T) *LazyValue[T] {
	return &LazyValue[T]{
		name: name,
		init: init,
	}
}

// Get returns the lazily initialized value
func (l *LazyValue[T]) Get() T {
	// Fast path: already initialized
	l.mu.RLock()
	if l.done {
		val := l.value
		l.mu.RUnlock()
		return val
	}
	l.mu.RUnlock()

	// Slow path: acquire write lock and initialize
	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if l.done {
		return l.value
	}

	span := profiler.StartWithPhase("lazy_value_"+l.name, "deferred")
	defer span.End()
	l.value = l.init()
	l.done = true
	markInitialized(l.name)

	return l.value
}

// Reset allows re-initialization
func (l *LazyValue[T]) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = false
	var zero T
	l.value = zero
}
