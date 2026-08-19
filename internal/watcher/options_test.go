package watcher

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// DefaultFileReservationConfigValues
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// WithConflictCallback
// ---------------------------------------------------------------------------

func TestWithConflictCallback(t *testing.T) {
	t.Parallel()

	called := false
	cb := func(fc FileConflict) {
		called = true
	}

	w := &FileReservationWatcher{}
	opt := WithConflictCallback(cb)
	opt(w)

	if w.conflictCallback == nil {
		t.Fatal("conflictCallback should not be nil after WithConflictCallback")
	}
	// Invoke to verify it's the right callback
	w.conflictCallback(FileConflict{})
	if !called {
		t.Error("expected callback to be invoked")
	}
}

// ---------------------------------------------------------------------------
// WithCaptureLines
// ---------------------------------------------------------------------------

func TestWithCaptureLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		lines   int
		wantSet bool
		wantVal int
	}{
		{"positive", 50, true, 50},
		{"zero_ignored", 0, false, 0},
		{"negative_ignored", -1, false, 0},
		{"large_value", 1000, true, 1000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &FileReservationWatcher{}
			opt := WithCaptureLines(tc.lines)
			opt(w)

			if tc.wantSet && w.captureLines != tc.wantVal {
				t.Errorf("captureLines = %d, want %d", w.captureLines, tc.wantVal)
			}
			if !tc.wantSet && w.captureLines != 0 {
				t.Errorf("captureLines = %d, want 0 (unchanged)", w.captureLines)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithDebounceDuration
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// NewFileReservationWatcherFromConfig
// ---------------------------------------------------------------------------

func TestNewFileReservationWatcherFromConfig_Disabled(t *testing.T) {
	t.Parallel()

	cfg := DefaultFileReservationConfigValues()
	cfg.Enabled = false

	result := NewFileReservationWatcherFromConfig(cfg, nil, "/tmp/project", "agent-1", "session-1", nil)
	if result != nil {
		t.Error("expected nil when Enabled is false")
	}
}

func TestNewFileReservationWatcherFromConfig_AppliesReservationBehavior(t *testing.T) {
	t.Parallel()

	cfg := DefaultFileReservationConfigValues()
	cfg.AutoReserve = false
	cfg.ExtendOnActivity = false

	watcher := NewFileReservationWatcherFromConfig(cfg, nil, "/tmp/project", "agent-1", "session-1", nil)
	if watcher == nil {
		t.Fatal("expected watcher when enabled")
	}
	if watcher.autoReserve {
		t.Error("autoReserve = true, want false")
	}
	if watcher.extendOnActivity {
		t.Error("extendOnActivity = true, want false")
	}
}
