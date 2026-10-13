package watcher

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// This fixture drives the actual poll/cleanup path. Only classification and
// external I/O are injected; all lifecycle decisions run in the watcher.
func activityWatcher(t *testing.T) (*FileReservationWatcher, *int, *int) {
	t.Helper()
	w := NewFileReservationWatcher(WithProjectDir("/test/project"), WithAgentName("BlueLake"))
	lease := agentmail.FileReservation{
		ID: 101, AgentName: "BlueLake", ProjectID: 1, PathPattern: "src/main.go",
		Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
	}
	w.activeReservations["%7"] = &PaneReservation{
		PaneID: "%7", AgentName: "BlueLake", Files: []string{lease.PathPattern}, ReservationID: []int{101},
		LastActivity: time.Now().Add(-time.Hour), LastOutput: "unchanged frame", leases: map[int]agentmail.FileReservation{101: lease},
	}
	w.paneOutputs["%7"] = "unchanged frame"
	w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "unchanged frame", nil }
	w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{"demo": {{ID: "%7", Type: tmux.AgentClaude}}}, nil
	}
	renews, releases := 0, 0
	w.client = &ownershipWatcherClient{
		renew: func(o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
			renews++
			if o.AgentName != "BlueLake" || !reflect.DeepEqual(o.ReservationIDs, []int{101}) || len(o.Paths) != 0 {
				t.Fatalf("heartbeat broadened ownership: %+v", o)
			}
			return &agentmail.RenewReservationsResult{Renewed: 1}, nil
		},
		release: func(name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
			releases++
			if name != "BlueLake" || !reflect.DeepEqual(ids, []int{101}) || len(paths) != 0 {
				t.Fatalf("idle release broadened ownership: %q %v %v", name, paths, ids)
			}
			return &agentmail.ReleaseReservationsResult{Released: 1}, nil
		},
	}
	return w, &renews, &releases
}

func TestWatcherQuietWorkingLeaseHeartbeat(t *testing.T) {
	w, renews, releases := activityWatcher(t)
	w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityWorking }
	before := time.Now()
	w.checkPaneOutputs(context.Background())
	w.releaseIdleReservations(context.Background())
	tracked := w.activeReservations["%7"]
	if *renews != 1 || *releases != 0 || tracked == nil || tracked.LastActivity.Before(before) {
		t.Fatalf("quiet work lost its lease: renews=%d releases=%d tracked=%+v", *renews, *releases, tracked)
	}
	if tracked.LastOutput != "unchanged frame" || w.paneOutputs["%7"] != "unchanged frame" || len(w.pendingEdits) != 0 {
		t.Fatal("heartbeat fabricated a new edit/output observation")
	}
	w.checkPaneOutputs(context.Background())
	if *renews != 1 {
		t.Fatalf("quiet heartbeat ignored its cadence: %d calls", *renews)
	}
	tracked.nextHeartbeat = time.Now().Add(-time.Second)
	w.checkPaneOutputs(context.Background())
	if *renews != 2 {
		t.Fatalf("due heartbeat not renewed: %d calls", *renews)
	}
}

func TestWatcherLivenessDoesNotOverrideRenewalOptOut(t *testing.T) {
	w, renews, releases := activityWatcher(t)
	w.extendOnActivity = false
	w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityWorking }
	w.checkPaneOutputs(context.Background())
	w.releaseIdleReservations(context.Background())
	if *renews != 0 || *releases != 0 {
		t.Fatalf("opt-out or quiet work ignored: renews=%d releases=%d", *renews, *releases)
	}
}

func TestWatcherIdleReleaseNeedsSuccessfulCurrentObservation(t *testing.T) {
	for _, mode := range []string{"idle", "unknown", "topology failed", "capture failed", "gone", "stale idle", "future idle"} {
		t.Run(mode, func(t *testing.T) {
			w, renews, releases := activityWatcher(t)
			w.paneActivity = map[string]reservationActivityObservation{"%7": {state: reservationActivityIdle, observedAt: time.Now()}}
			w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityIdle }
			switch mode {
			case "unknown":
				w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityUnknown }
			case "topology failed":
				w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) { return nil, errors.New("tmux unavailable") }
			case "capture failed":
				w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "", errors.New("capture timed out") }
			case "gone":
				w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil }
			}
			w.checkPaneOutputs(context.Background())
			if mode == "stale idle" {
				w.paneActivity["%7"] = reservationActivityObservation{state: reservationActivityIdle, observedAt: time.Now().Add(-time.Hour)}
			}
			if mode == "future idle" {
				w.paneActivity["%7"] = reservationActivityObservation{state: reservationActivityIdle, observedAt: time.Now().Add(time.Hour)}
			}
			w.releaseIdleReservations(context.Background())
			wantRelease := mode == "idle" || mode == "gone"
			if (*releases == 1) != wantRelease || *renews != 0 {
				t.Fatalf("mode=%s: renews=%d releases=%d", mode, *renews, *releases)
			}
			if !wantRelease {
				w.Stop() // Explicit shutdown still cleans exact owned IDs.
				if *releases != 1 {
					t.Fatal("observation gate prevented explicit shutdown cleanup")
				}
			}
		})
	}
}

func TestWatcherWorkingThenIdleReleasesAfterInactivity(t *testing.T) {
	w, renews, releases := activityWatcher(t)
	phase := reservationActivityWorking
	w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return phase }
	w.checkPaneOutputs(context.Background())
	phase = reservationActivityIdle
	w.checkPaneOutputs(context.Background())
	w.releaseIdleReservations(context.Background())
	if *releases != 0 {
		t.Fatal("idle transition released before the inactivity window")
	}
	w.activeReservations["%7"].LastActivity = time.Now().Add(-2 * w.idleTimeout)
	w.checkPaneOutputs(context.Background())
	w.releaseIdleReservations(context.Background())
	if *renews != 1 || *releases != 1 || len(w.activeReservations) != 0 {
		t.Fatalf("idle completion retained lease: renews=%d releases=%d", *renews, *releases)
	}
}

func TestWatcherReplacementActivityCannotExtendOldOwner(t *testing.T) {
	w, renews, releases := activityWatcher(t)
	w.agentNameResolver = func(string) string { return "NewAgent" }
	w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "new occupant frame", nil }
	w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityWorking }
	oldActivity := w.activeReservations["%7"].LastActivity
	w.checkPaneOutputs(context.Background())
	if *renews != 0 || !w.activeReservations["%7"].LastActivity.Equal(oldActivity) {
		t.Fatal("replacement agent prolonged its predecessor's reservation")
	}
	w.Stop()
	if *releases != 1 {
		t.Fatal("old verified owner was not used for cleanup")
	}
}

func TestWatcherQuietHeartbeatThroughRunLoop(t *testing.T) {
	w, _, _ := activityWatcher(t)
	w.pollInterval = time.Millisecond
	w.classifyPaneActivity = func(tmux.Pane, string, time.Time) reservationActivity { return reservationActivityWorking }
	renewed := make(chan struct{}, 1)
	var releases atomic.Int32
	w.client = &ownershipWatcherClient{
		renew: func(agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
			select {
			case renewed <- struct{}{}:
			default:
			}
			return &agentmail.RenewReservationsResult{Renewed: 1}, nil
		},
		release: func(string, []string, []int) (*agentmail.ReleaseReservationsResult, error) {
			releases.Add(1)
			return &agentmail.ReleaseReservationsResult{Released: 1}, nil
		},
	}
	w.Start(context.Background())
	defer w.Stop()
	select {
	case <-renewed:
	case <-time.After(2 * time.Second):
		t.Fatal("quiet running agent never received a lease heartbeat")
	}
	if releases.Load() != 0 {
		t.Fatal("working lease released by poll cleanup")
	}
	w.Stop()
	if releases.Load() != 1 || len(w.paneActivity) != 0 {
		t.Fatalf("shutdown cleanup: releases=%d observations=%v", releases.Load(), w.paneActivity)
	}
}

// This test uses the real canonical detector when run in the native package.
// It is deliberately separate from the injected-boundary lifecycle tests.
func TestReservationActivityClassifierLiveFrames(t *testing.T) {
	for _, tc := range []struct {
		name, agent, output string
		height              int
		want                reservationActivity
	}{
		{"claude working", "cc", "✻ Working… (ctrl+c to interrupt · 2m 44s · thinking)\n❯", 30, reservationActivityWorking},
		{"claude idle", "cc", "❯", 30, reservationActivityIdle},
		{"codex working", "cod", "• Working (esc to interrupt)\n› Ask Codex to do anything", 30, reservationActivityWorking},
		{"codex idle", "cod", "› Ask Codex to do anything\n47% context left", 30, reservationActivityIdle},
		{"gemini working", "gmi", "⠋ Thinking... (esc to cancel, 90s)\n> Type your message or @path/to/file", 30, reservationActivityWorking},
		{"gemini idle", "gmi", "> Type your message or @path/to/file\n/model context\ngemini-pro 14% used", 30, reservationActivityIdle},
		{"rate limited is not idle", "gmi", "RESOURCE_EXHAUSTED\n> Type your message or @path/to/file", 30, reservationActivityUnknown},
		{"short prose is not idle", "gmi", "done", 30, reservationActivityUnknown},
		{"empty startup", "gmi", "", 30, reservationActivityUnknown},
		{"stale spinner outside viewport", "cod", "• Working (esc to interrupt)\nold line\n› Ask Codex to do anything\n47% context left", 2, reservationActivityIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyReservationActivity(tmux.Pane{ID: "%7", Type: tmux.AgentType(tc.agent), Height: tc.height}, tc.output, time.Now())
			if got != tc.want {
				t.Fatalf("activity=%v, want %v", got, tc.want)
			}
		})
	}
}
