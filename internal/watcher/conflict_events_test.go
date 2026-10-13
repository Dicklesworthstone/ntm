package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/events"
)

func TestConfiguredConflictPublishesSnapshot(t *testing.T) {
	project := t.TempDir()
	at := time.Now().UTC()
	expires := at.Add(time.Hour)
	reserved := at.Add(-time.Minute)
	conflict := FileConflict{Path: "internal/api.go", RequestorAgent: "BlueLake", RequestorPane: "%7",
		SessionName: "conflict-publisher", Holders: []string{"RedHill"}, HolderReservationIDs: []int{91},
		DetectedAt: at, ExpiresAt: &expires, ReservedSince: &reserved}
	received := make(chan FileConflictEvent, 2)
	unsubscribe := events.Subscribe(FileConflictEventType, func(raw events.BusEvent) {
		event, ok := raw.(FileConflictEvent)
		if ok && event.ProjectKey == agentmail.CanonicalProjectKey(project) {
			received <- event
		}
	})
	defer unsubscribe()
	called := false
	w := NewFileReservationWatcherFromConfig(FileReservationConfigValues{Enabled: true, NotifyOnConflict: true},
		nil, project, "", conflict.SessionName, func(c FileConflict) {
			called = true
			// Caller-owned data remains mutable after publication.
			c.Holders[0] = "Changed"
			c.HolderReservationIDs[0] = 99
			*c.ExpiresAt = at.Add(2 * time.Hour)
			*c.ReservedSince = at
		})
	if w == nil || w.conflictCallback == nil {
		t.Fatal("configured watcher has no notification callback")
	}
	w.conflictCallback(conflict)
	if !called {
		t.Fatal("optional callback was lost")
	}
	var event FileConflictEvent
	select {
	case event = <-received:
	case <-time.After(time.Second):
		t.Fatal("configured conflict never reached the event bus")
	}
	if event.EventType() != FileConflictEventType || event.EventSession() != conflict.SessionName || !event.EventTimestamp().Equal(at) {
		t.Fatalf("wrong event envelope: %+v", event)
	}
	for attempt := 0; attempt < 2; attempt++ {
		copy := event.Snapshot()
		if copy.Holders[0] != "RedHill" || copy.HolderReservationIDs[0] != 91 ||
			!copy.ExpiresAt.Equal(at.Add(time.Hour)) || !copy.ReservedSince.Equal(at.Add(-time.Minute)) {
			t.Fatalf("snapshot aliases caller or another consumer: %+v", copy)
		}
		copy.Holders[0] = "UI change"
		copy.HolderReservationIDs[0] = 100
		*copy.ExpiresAt = at
		*copy.ReservedSince = at
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Type, Session string
		ProjectKey    string `json:"project_key"`
		Conflict      FileConflict
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil || envelope.Type != FileConflictEventType ||
		envelope.ProjectKey != agentmail.CanonicalProjectKey(project) || envelope.Conflict.HolderReservationIDs[0] != 91 {
		t.Fatalf("invalid JSON event: %s (%v)", encoded, err)
	}
}

func TestConfiguredConflictNotificationGates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		enabled, notify bool
	}{
		{"disabled watcher", false, true},
		{"disabled notifications", true, false},
		{"enabled without callback", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			w := NewFileReservationWatcherFromConfig(FileReservationConfigValues{Enabled: tc.enabled, NotifyOnConflict: tc.notify},
				nil, project, "", "gated", nil)
			if !tc.enabled {
				if w != nil {
					t.Fatal("disabled watcher was created")
				}
				return
			}
			if !tc.notify {
				if w.conflictCallback != nil {
					t.Fatal("notification opt-out installed a publisher")
				}
				return
			}
			received := make(chan struct{}, 1)
			unsubscribe := events.Subscribe(FileConflictEventType, func(raw events.BusEvent) {
				if event, ok := raw.(FileConflictEvent); ok && event.ProjectKey == agentmail.CanonicalProjectKey(project) {
					received <- struct{}{}
				}
			})
			defer unsubscribe()
			if w.conflictCallback == nil {
				t.Fatal("nil optional callback suppressed notifications")
			}
			w.conflictCallback(FileConflict{Path: "a.go", RequestorAgent: "BlueLake", RequestorPane: "%7", SessionName: "gated", DetectedAt: time.Now()})
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("enabled notification missing")
			}
		})
	}
}

func TestConflictEventCanonicalProjectScope(t *testing.T) {
	project := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	received := make(chan FileConflictEvent, 1)
	unsubscribe := events.Subscribe(FileConflictEventType, func(raw events.BusEvent) {
		if event, ok := raw.(FileConflictEvent); ok && event.EventSession() == alias {
			received <- event
		}
	})
	defer unsubscribe()
	w := NewFileReservationWatcherFromConfig(FileReservationConfigValues{Enabled: true, NotifyOnConflict: true},
		nil, alias, "", alias, nil)
	if w == nil || w.conflictCallback == nil {
		t.Fatal("configured watcher did not install conflict publication")
	}
	w.conflictCallback(FileConflict{Path: "a.go", RequestorAgent: "BlueLake", RequestorPane: "%7", SessionName: alias, DetectedAt: time.Now()})
	select {
	case event := <-received:
		if event.ProjectKey != agentmail.CanonicalProjectKey(project) {
			t.Fatalf("alias escaped canonical scope: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("notification missing")
	}
}
