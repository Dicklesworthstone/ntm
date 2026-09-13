package agents

import (
	"testing"
	"time"
)

// Nothing in the shipped binary records task outcomes, so every seeded profile
// carries a SuccessRate that was assumed rather than observed. `ntm agents
// profile` printed it as a statistic, producing "Tasks Completed: 0" directly
// above "Success Rate: 90.0%" — a rate over no samples at all.
func TestSeededProfilesAreNotMeasured(t *testing.T) {
	pm := NewProfileMatcher()

	for _, profile := range pm.profiles {
		if profile == nil {
			continue
		}
		if profile.Performance.Measured() {
			t.Errorf("%s: seeded profile claims measured performance (tasks=%d last=%v)",
				profile.Type, profile.Performance.TasksCompleted, profile.Performance.LastUpdated)
		}
	}
}

func TestPerformanceMeasured(t *testing.T) {
	cases := map[string]struct {
		perf Performance
		want bool
	}{
		"seeded prior only": {
			perf: Performance{SuccessRate: 0.9},
			want: false,
		},
		"a task was completed": {
			perf: Performance{SuccessRate: 0.9, TasksCompleted: 1},
			want: true,
		},
		"recorded at a known time": {
			perf: Performance{SuccessRate: 0.9, LastUpdated: time.Now()},
			want: true,
		},
		"zero value": {
			perf: Performance{},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.perf.Measured(); got != tc.want {
				t.Errorf("Measured() = %v, want %v", got, tc.want)
			}
		})
	}
}
