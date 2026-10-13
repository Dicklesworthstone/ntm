package watcher

import (
	"testing"
	"time"

	fswatch "github.com/Dicklesworthstone/ntm/internal/fs/watcher"
)

// The original debounce suite moved unchanged with its implementation. This
// checks that callers may still pass the legacy constructor to the lower layer.
func TestDebouncerCompatibility(t *testing.T) {
	t.Parallel()
	var d *fswatch.Debouncer = NewDebouncer(time.Millisecond)
	t.Cleanup(d.Cancel)
	done := make(chan struct{})
	d.Trigger(func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("compatibility debouncer did not invoke callback")
	}
	if DefaultDebounceDuration != fswatch.DefaultDebounceDuration {
		t.Fatal("debounce defaults diverged")
	}
}
