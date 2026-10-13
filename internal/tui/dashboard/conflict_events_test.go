package dashboard

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

func dashboardConflictFixture(path string, at time.Time) watcher.FileConflictEvent {
	expires := at.Add(time.Hour)
	return watcher.FileConflictEvent{
		BaseEvent:  events.BaseEvent{Type: watcher.FileConflictEventType, Session: "demo", Timestamp: at},
		ProjectKey: "/project",
		Conflict: watcher.FileConflict{Path: path, RequestorAgent: "BlueLake", RequestorPane: "%7", SessionName: "demo",
			Holders: []string{"RedHill", "GreenTree"}, HolderReservationIDs: []int{91, 92}, DetectedAt: at, ExpiresAt: &expires},
	}
}

func TestDashboardConflictLiveScopeAndSnapshot(t *testing.T) {
	bus := events.NewEventBus(32)
	var got []watcher.FileConflict
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(_ context.Context, c watcher.FileConflict) {
		got = append(got, c)
		// Mutating a consumer's value must not mutate the event or its history.
		c.Holders[0] = "consumer"
		c.HolderReservationIDs[0] = 999
		*c.ExpiresAt = time.Time{}
	})
	defer stop()
	at := time.Now()
	valid := dashboardConflictFixture("valid.go", at)
	for _, mode := range []string{"session prefix", "different project", "inner session", "missing pane", "missing requestor", "missing path", "expired", "zero timestamp", "mismatched timestamp", "wrong type"} {
		event := dashboardConflictFixture(mode+".go", at)
		switch mode {
		case "session prefix":
			event.Session = "demo-api"
		case "different project":
			event.ProjectKey = "/another-project"
		case "inner session":
			event.Conflict.SessionName = "demo-api"
		case "missing pane":
			event.Conflict.RequestorPane = ""
		case "missing requestor":
			event.Conflict.RequestorAgent = ""
		case "missing path":
			event.Conflict.Path = ""
		case "expired":
			past := at.Add(-time.Second)
			event.Conflict.ExpiresAt = &past
		case "zero timestamp":
			event.Conflict.DetectedAt = time.Time{}
		case "mismatched timestamp":
			event.Timestamp = at.Add(-time.Second)
		case "wrong type":
			event.Type = "not-a-conflict"
		}
		bus.PublishSync(event)
	}
	bus.PublishSync(events.BaseEvent{Type: watcher.FileConflictEventType, Session: "demo", Timestamp: at})
	bus.PublishSync(valid)
	stop()
	if len(got) != 1 || got[0].Path != "valid.go" {
		t.Fatalf("wrong conflict delivery: %+v", got)
	}
	if valid.Conflict.Holders[0] != "RedHill" || valid.Conflict.HolderReservationIDs[0] != 91 || valid.Conflict.ExpiresAt.IsZero() {
		t.Fatal("consumer mutated event storage")
	}
	if bus.SubscriberCount(watcher.FileConflictEventType) != 0 {
		t.Fatal("subscription leaked")
	}
}

func TestDashboardConflictRetryAndOutOfOrderDeduplication(t *testing.T) {
	bus := events.NewEventBus(32)
	var got []watcher.FileConflict
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(_ context.Context, c watcher.FileConflict) { got = append(got, c) })
	defer stop()
	at := time.Now()
	original := dashboardConflictFixture("shared.go", at)
	bus.PublishSync(original)
	retry := original
	retry.Conflict = original.Snapshot()
	retry.Timestamp = at.Add(time.Second)
	retry.Conflict.DetectedAt = retry.Timestamp
	retry.Conflict.Holders = []string{"GreenTree", "RedHill"}
	retry.Conflict.HolderReservationIDs = []int{92, 91}
	bus.PublishSync(retry)
	changed := retry
	changed.Conflict = retry.Snapshot()
	changed.Timestamp = at.Add(2 * time.Second)
	changed.Conflict.DetectedAt = changed.Timestamp
	changed.Conflict.Holders = []string{"NewHolder"}
	changed.Conflict.HolderReservationIDs = []int{93}
	bus.PublishSync(&changed)
	bus.PublishSync(original) // async old callback must not restore RedHill
	// One requestor can occupy multiple panes; these are distinct observations.
	sibling := changed
	sibling.Conflict = changed.Snapshot()
	sibling.Conflict.RequestorPane = "%8"
	bus.PublishSync(sibling)
	stop()
	if len(got) != 3 || got[1].Holders[0] != "NewHolder" || got[2].RequestorPane != "%8" {
		t.Fatalf("lost update or duplicate notification: %+v", got)
	}
}

func TestDashboardConflictReplaysRecentStartupHistory(t *testing.T) {
	bus := events.NewEventBus(32)
	at := time.Now()
	bus.PublishSync(dashboardConflictFixture("old.go", at.Add(-2*dashboardConflictHistoryAge)))
	expired := dashboardConflictFixture("expired.go", at)
	past := at.Add(-time.Second)
	expired.Conflict.ExpiresAt = &past
	bus.PublishSync(expired)
	valid := dashboardConflictFixture("startup.go", at)
	bus.PublishSync(valid)
	arrived := make(chan struct{}, 2)
	var paths []string
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(_ context.Context, c watcher.FileConflict) { paths = append(paths, c.Path); arrived <- struct{}{} })
	defer stop()
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("startup observation was lost before subscription")
	}
	bus.PublishSync(valid) // overlap with the replay must not make another toast
	stop()
	if !reflect.DeepEqual(paths, []string{"startup.go"}) {
		t.Fatalf("wrong startup replay: %v", paths)
	}
}

func TestDashboardConflictStopCancelsBlockedDelivery(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%v", replay), func(t *testing.T) {
			bus := events.NewEventBus(32)
			event := dashboardConflictFixture("shared.go", time.Now())
			if replay {
				bus.PublishSync(event)
			}
			entered := make(chan struct{}, 1)
			var calls atomic.Int64
			stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(ctx context.Context, _ watcher.FileConflict) {
				calls.Add(1)
				entered <- struct{}{}
				<-ctx.Done()
			})
			if !replay {
				bus.Publish(event)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				stop()
				t.Fatal("delivery never started")
			}
			// Fill some async handler slots while one delivery owns the serial gate.
			for i := 0; i < 20; i++ {
				bus.Publish(dashboardConflictFixture(fmt.Sprintf("queued-%d.go", i), event.Timestamp))
			}
			done := make(chan struct{})
			go func() { stop(); stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stop did not cancel and join delivery")
			}
			bus.PublishSync(event)
			if !bus.WaitHandlers(time.Second) || calls.Load() != 1 || bus.SubscriberCount(watcher.FileConflictEventType) != 0 {
				t.Fatalf("post-stop callbacks or handlers leaked: calls=%d", calls.Load())
			}
		})
	}
}

func TestDashboardConflictConcurrentPublicationAndStop(t *testing.T) {
	bus := events.NewEventBus(32)
	var calls atomic.Int64
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(context.Context, watcher.FileConflict) { calls.Add(1) })
	var writers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			for j := 0; j < 50; j++ {
				bus.Publish(dashboardConflictFixture(fmt.Sprintf("%d-%d.go", index, j), time.Now()))
			}
		}(i)
	}
	stop()
	atStop := calls.Load()
	writers.Wait()
	if !bus.WaitHandlers(time.Second) || calls.Load() != atStop {
		t.Fatal("delivery continued after subscription stop")
	}
}

func TestDashboardConflictBoundsDeduplicationWithoutDroppingNewConflicts(t *testing.T) {
	bus := events.NewEventBus(1)
	var count int
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", func(context.Context, watcher.FileConflict) { count++ })
	defer stop()
	for i := 0; i < dashboardConflictDedupeLimit+10; i++ {
		bus.PublishSync(dashboardConflictFixture(fmt.Sprintf("file-%d.go", i), time.Now()))
	}
	stop()
	if count != dashboardConflictDedupeLimit+10 {
		t.Fatalf("dedupe capacity discarded new conflicts: %d", count)
	}
}

func TestDashboardConflictInvalidSubscriptionDoesNotRegister(t *testing.T) {
	bus := events.NewEventBus(1)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deliver := func(context.Context, watcher.FileConflict) { t.Error("unexpected delivery") }
	for _, stop := range []func(){
		subscribeDashboardConflicts(nil, bus, "demo", "/project", deliver),
		subscribeDashboardConflicts(cancelled, bus, "demo", "/project", deliver),
		subscribeDashboardConflicts(context.Background(), nil, "demo", "/project", deliver),
		subscribeDashboardConflicts(context.Background(), bus, "", "/project", deliver),
		subscribeDashboardConflicts(context.Background(), bus, "demo", "", deliver),
		subscribeDashboardConflicts(context.Background(), bus, "demo", "/project", nil),
	} {
		stop()
	}
	if bus.SubscriberCount(watcher.FileConflictEventType) != 0 {
		t.Fatal("invalid subscription registered")
	}
}
