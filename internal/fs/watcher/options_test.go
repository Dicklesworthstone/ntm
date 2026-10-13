package watcher

import (
	"testing"
	"time"
)

// Moved from internal/watcher without changing the option contract or cases.
func TestWithDebounceDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dur        time.Duration
		wantNonNil bool
	}{
		{"positive_duration", 500 * time.Millisecond, true},
		{"zero_ignored", 0, false},
		{"negative_ignored", -1 * time.Second, false},
		{"one_second", time.Second, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &Watcher{}
			opt := WithDebounceDuration(tc.dur)
			opt(w)

			if tc.wantNonNil && w.debouncer == nil {
				t.Error("expected debouncer to be set")
			}
			if !tc.wantNonNil && w.debouncer != nil {
				t.Error("expected debouncer to remain nil")
			}
			if tc.wantNonNil && w.debouncer != nil {
				if w.debouncer.Duration() != tc.dur {
					t.Errorf("debouncer.Duration() = %v, want %v", w.debouncer.Duration(), tc.dur)
				}
			}
		})
	}
}
