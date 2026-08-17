package coordinator

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping
// [rotation.thresholds] restart_if_tokens_above / restart_if_session_hours
// changes whether the coordinator rotation checker enqueues a rotation for
// the same pane state. Runs against real transcript fixtures through the
// same harness as the decision-table test.

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRotationChecker_RestartThresholdTriggers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/Users/x/proj"

	// Claude fixture: 172841 tokens against the fixed 200000 registry window
	// = 86.4% usage. The percent trigger is parked at 95 so only the restart
	// knobs under test can fire.
	seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 170000, 1528))

	sessionStart := time.Now().Add(-2 * time.Hour)

	cases := []struct {
		name         string
		tokensAbove  float64
		sessionAge   time.Duration
		wantEnqueued bool
	}{
		{name: "no_restart_knobs_no_trigger", tokensAbove: 0, sessionAge: 0, wantEnqueued: false},
		{name: "tokens_above_100k_triggers", tokensAbove: 100000, wantEnqueued: true},
		{name: "tokens_above_200k_does_not_trigger", tokensAbove: 200000, wantEnqueued: false},
		{name: "session_older_than_1h_triggers", sessionAge: time.Hour, wantEnqueued: true},
		{name: "session_younger_than_8h_does_not_trigger", sessionAge: 8 * time.Hour, wantEnqueued: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redirectPendingStore(t)
			pane := ccPane("%9", "rotsess__cc_1")
			env := newRotationTestEnv(t, 95, false,
				[]tmux.Pane{pane},
				map[string]string{"%9": cwd},
				map[string]string{"%9": idleCapture},
				map[string]error{},
			)
			env.rc.restartTokensAbove = tc.tokensAbove
			env.rc.restartSessionAge = tc.sessionAge
			env.rc.sessionCreated = func(string) (time.Time, bool) {
				return sessionStart, true
			}

			decisions := env.rc.runOnce(t.Context())
			enqueued := false
			for _, d := range decisions {
				if d.Action == "enqueued" {
					enqueued = true
				}
			}
			if enqueued != tc.wantEnqueued {
				t.Fatalf("enqueued = %v, want %v (restart thresholds must govern the trigger); decisions: %+v",
					enqueued, tc.wantEnqueued, decisions)
			}
		})
	}
}
