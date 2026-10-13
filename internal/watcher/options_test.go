package watcher

import "testing"

// Filesystem option tests live beside the shared engine in internal/fs/watcher.

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
