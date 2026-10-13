// Package watcher provides file watching with debouncing.
package watcher

import (
	"sync"
	"time"
)

// DefaultDebounceDuration is the default debounce window.
const DefaultDebounceDuration = 250 * time.Millisecond

// Debouncer coalesces rapid events into a single callback invocation.
// When Trigger is called multiple times within the debounce duration,
// only the last callback is executed after the duration elapses.
type Debouncer struct {
	duration time.Duration
	timer    *time.Timer
	mu       sync.Mutex
}

// NewDebouncer creates a new Debouncer with the specified duration.
// If duration is 0, DefaultDebounceDuration is used.
func NewDebouncer(duration time.Duration) *Debouncer {
	if duration == 0 {
		duration = DefaultDebounceDuration
	}
	return &Debouncer{
		duration: duration,
	}
}

// Trigger schedules the callback to be called after the debounce duration.
// If Trigger is called again before the duration elapses, the previous
// scheduled callback is canceled and a new one is scheduled.
func (d *Debouncer) Trigger(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.duration, callback)
}

// Cancel cancels any pending callback.
func (d *Debouncer) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
