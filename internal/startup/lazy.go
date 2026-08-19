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

// LazyValue is a simplified lazy initializer that doesn't return errors
type LazyValue[T any] struct {
	mu    sync.RWMutex
	value T
	init  func() T
	done  bool
	name  string
}
