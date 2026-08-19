package context

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewPendingRotationOutput
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// RemainingSeconds
// ---------------------------------------------------------------------------

func TestRemainingSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timeoutAt time.Time
		wantMin   int
		wantMax   int
	}{
		{
			name:      "five_minutes_future",
			timeoutAt: time.Now().Add(5 * time.Minute),
			wantMin:   295, // ~5min minus tolerance
			wantMax:   305,
		},
		{
			name:      "expired_returns_zero",
			timeoutAt: time.Now().Add(-1 * time.Minute),
			wantMin:   0,
			wantMax:   0,
		},
		{
			name:      "just_expired_returns_zero",
			timeoutAt: time.Now().Add(-1 * time.Second),
			wantMin:   0,
			wantMax:   0,
		},
		{
			name:      "one_second_future",
			timeoutAt: time.Now().Add(1 * time.Second),
			wantMin:   0,
			wantMax:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PendingRotation{TimeoutAt: tc.timeoutAt}
			got := p.RemainingSeconds()
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("RemainingSeconds() = %d, want [%d, %d]", got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IsExpired
// ---------------------------------------------------------------------------

func TestIsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timeoutAt time.Time
		want      bool
	}{
		{
			name:      "future_not_expired",
			timeoutAt: time.Now().Add(5 * time.Minute),
			want:      false,
		},
		{
			name:      "past_expired",
			timeoutAt: time.Now().Add(-5 * time.Minute),
			want:      true,
		},
		{
			name:      "just_past_expired",
			timeoutAt: time.Now().Add(-1 * time.Second),
			want:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PendingRotation{TimeoutAt: tc.timeoutAt}
			if got := p.IsExpired(); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}
