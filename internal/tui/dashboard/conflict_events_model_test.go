package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
)

// This native package test exercises the same model message as RunWithOptions;
// it does not run external tools or reservation mutations.
func TestConflictEventReachesPanelAndToast(t *testing.T) {
	bus := events.NewEventBus(8)
	messages := make(chan FileConflictMsg, 1)
	stop := subscribeDashboardConflicts(context.Background(), bus, "demo", "/project",
		func(_ context.Context, conflict watcher.FileConflict) {
			messages <- FileConflictMsg{Conflict: conflict}
		})
	defer stop()
	bus.PublishSync(dashboardConflictFixture("internal/api.go", time.Now()))
	var message FileConflictMsg
	select {
	case message = <-messages:
	case <-time.After(time.Second):
		t.Fatal("conflict event did not become a dashboard message")
	}
	model := newTestModel(120)
	if model.conflictsPanel == nil || model.toasts == nil {
		t.Fatal("dashboard is missing its conflict panel or toast manager")
	}
	before := model.toasts.Count()
	updated, _ := model.Update(message)
	got := updated.(Model)
	conflicts := got.conflictsPanel.GetConflicts()
	if len(conflicts) != 1 || conflicts[0].Path != "internal/api.go" ||
		conflicts[0].RequestorPane != "%7" || got.toasts.Count() != before+1 {
		t.Fatalf("conflict never reached panel/toast: conflicts=%+v toasts=%d", conflicts, got.toasts.Count())
	}
}
