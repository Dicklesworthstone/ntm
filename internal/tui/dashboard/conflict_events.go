package dashboard

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

const dashboardConflictHistoryAge = time.Minute
const dashboardConflictDedupeLimit = 256

type dashboardConflictKey struct {
	path, requestor, pane string
}

type dashboardConflictSeen struct {
	at          time.Time
	fingerprint string
}

// subscribeDashboardConflicts bridges the existing reservation event to the
// existing FileConflictMsg UI path. Delivery must return when ctx is cancelled
// (Bubble Tea's Send does, through WithContext). Stop cancels before joining;
// neither the event-bus registry lock nor the admission lock is held at delivery.
//
// Subscribe before taking recent history so conflicts detected during model
// construction are not missed. Snapshot/live overlap and asynchronous delivery
// are deduplicated per conflict, without an unbounded queue or goroutine pool.
// This in-process history is not a current reservation listing or crash replay.
func subscribeDashboardConflicts(parent context.Context, bus *events.EventBus, session, projectKey string,
	deliver func(context.Context, watcher.FileConflict),
) func() {
	if parent == nil || parent.Err() != nil || bus == nil || session == "" || projectKey == "" || deliver == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	var admission sync.Mutex
	var active sync.WaitGroup
	closed := false
	serial := make(chan struct{}, 1)
	seen := make(map[dashboardConflictKey]dashboardConflictSeen)

	handle := func(raw events.BusEvent) {
		admission.Lock()
		if closed || ctx.Err() != nil {
			admission.Unlock()
			return
		}
		active.Add(1)
		admission.Unlock()
		defer active.Done()

		var event watcher.FileConflictEvent
		switch value := raw.(type) {
		case watcher.FileConflictEvent:
			event = value
		case *watcher.FileConflictEvent:
			if value == nil {
				return
			}
			event = *value
		default:
			return
		}
		if event.EventType() != watcher.FileConflictEventType || event.EventSession() != session ||
			event.ProjectKey != projectKey || event.Conflict.SessionName != session {
			return
		}
		conflict := event.Snapshot()
		if conflict.Path == "" || conflict.RequestorAgent == "" || conflict.RequestorPane == "" ||
			conflict.DetectedAt.IsZero() || !event.EventTimestamp().Equal(conflict.DetectedAt) {
			return
		}
		select {
		case serial <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-serial }()
		if ctx.Err() != nil || conflict.IsExpired() {
			return
		}
		key := dashboardConflictKey{conflict.Path, conflict.RequestorAgent, conflict.RequestorPane}
		// Holder/resource order is not a new conflict. Use a separate snapshot
		// for the signature so display order and the bus payload are untouched.
		signature := event.Snapshot()
		signature.DetectedAt = time.Time{}
		sort.Strings(signature.Holders)
		sort.Ints(signature.HolderReservationIDs)
		encoded, err := json.Marshal(signature)
		if err != nil {
			return
		}
		previous, exists := seen[key]
		if exists && conflict.DetectedAt.Before(previous.at) {
			return // A late callback must not replace a newer holder observation.
		}
		fingerprint := string(encoded)
		if exists && previous.fingerprint == fingerprint {
			seen[key] = dashboardConflictSeen{conflict.DetectedAt, fingerprint}
			return // Unchanged retry observations must not create toast storms.
		}
		if !exists && len(seen) >= dashboardConflictDedupeLimit {
			var oldest dashboardConflictKey
			var oldestAt time.Time
			for candidate, item := range seen {
				if oldestAt.IsZero() || item.at.Before(oldestAt) {
					oldest, oldestAt = candidate, item.at
				}
			}
			delete(seen, oldest)
		}
		seen[key] = dashboardConflictSeen{conflict.DetectedAt, fingerprint}
		deliver(ctx, conflict)
	}

	unsubscribe := bus.Subscribe(watcher.FileConflictEventType, handle)
	history := bus.History(0)
	cutoff := time.Now().Add(-dashboardConflictHistoryAge)
	replayed := make(chan struct{})
	go func() {
		defer close(replayed)
		// Oldest first; newer live observations may already have arrived and
		// will win via the per-key timestamp guard above.
		for i := len(history) - 1; i >= 0 && ctx.Err() == nil; i-- {
			if !history[i].EventTimestamp().Before(cutoff) {
				handle(history[i])
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			admission.Lock()
			closed = true
			admission.Unlock()
			cancel()
			unsubscribe()
			<-replayed
			active.Wait()
		})
	}
}
