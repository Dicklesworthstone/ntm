package robot

// Regression tests for GitHub issue #254: --robot-snapshot and --robot-status
// returned an authoritative empty session list when every runtime projection
// row aged past stale_after. GetFreshRuntimeSessions returns zero rows and no
// error in that window, and the projection-backed builders used to treat that
// as a valid answer, short-circuiting the live tmux path.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newProjectionTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func upsertProjectionSession(t *testing.T, store *state.Store, name string, staleAfter time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         name,
		Attached:     true,
		WindowCount:  1,
		PaneCount:    1,
		AgentCount:   1,
		ActiveAgents: 1,
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now,
		StaleAfter:   staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeSession(%s): %v", name, err)
	}
}

func TestProjectionSnapshotStaleRowsRefusesEmptyAnswer(t *testing.T) {
	store := newProjectionTestStore(t)
	// Every projection row has aged out.
	upsertProjectionSession(t, store, "alpha", time.Now().UTC().Add(-time.Minute))

	live := []tmux.Session{{Name: "alpha", Attached: true}}
	_, err := buildProjectionBackedSnapshotSessions(store, live)
	if !errors.Is(err, errProjectionStaleEmpty) {
		t.Fatalf("stale projection with live tmux sessions must return errProjectionStaleEmpty, got %v", err)
	}
}

func TestProjectionSnapshotEmptyStoreWithLiveSessionsRefusesEmptyAnswer(t *testing.T) {
	store := newProjectionTestStore(t)

	live := []tmux.Session{{Name: "alpha"}, {Name: "beta"}}
	_, err := buildProjectionBackedSnapshotSessions(store, live)
	if !errors.Is(err, errProjectionStaleEmpty) {
		t.Fatalf("empty projection with live tmux sessions must return errProjectionStaleEmpty, got %v", err)
	}
}

func TestProjectionSnapshotGenuinelyIdleStaysAuthoritative(t *testing.T) {
	store := newProjectionTestStore(t)

	sessions, err := buildProjectionBackedSnapshotSessions(store, nil)
	if err != nil {
		t.Fatalf("idle machine (no live tmux sessions) must serve the projection, got %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestProjectionSnapshotFreshRowsServed(t *testing.T) {
	store := newProjectionTestStore(t)
	upsertProjectionSession(t, store, "alpha", time.Now().UTC().Add(time.Hour))

	live := []tmux.Session{{Name: "alpha", Attached: true}}
	sessions, err := buildProjectionBackedSnapshotSessions(store, live)
	if err != nil {
		t.Fatalf("fresh projection must be served: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Name != "alpha" {
		t.Fatalf("expected [alpha], got %+v", sessions)
	}
}

func withProjectionLiveSessions(t *testing.T, fn func() ([]tmux.Session, error)) {
	t.Helper()
	old := projectionLiveSessions
	projectionLiveSessions = fn
	t.Cleanup(func() { projectionLiveSessions = old })
}

func TestProjectionStatusStaleRowsFallsBackWhenTmuxHasSessions(t *testing.T) {
	store := newProjectionTestStore(t)
	upsertProjectionSession(t, store, "alpha", time.Now().UTC().Add(-time.Minute))

	withProjectionLiveSessions(t, func() ([]tmux.Session, error) {
		return []tmux.Session{{Name: "alpha"}}, nil
	})

	_, err := buildProjectionBackedStatus(store, config.Default(), PaginationOptions{})
	if !errors.Is(err, errProjectionStaleEmpty) {
		t.Fatalf("stale projection with live tmux sessions must return errProjectionStaleEmpty, got %v", err)
	}
}

func TestProjectionStatusStaleRowsFallsBackWhenTmuxCheckFails(t *testing.T) {
	store := newProjectionTestStore(t)

	withProjectionLiveSessions(t, func() ([]tmux.Session, error) {
		return nil, errors.New("tmux unavailable")
	})

	_, err := buildProjectionBackedStatus(store, config.Default(), PaginationOptions{})
	if !errors.Is(err, errProjectionStaleEmpty) {
		t.Fatalf("unverifiable empty projection must not be served as authoritative, got %v", err)
	}
}

func TestProjectionStatusGenuinelyIdleServed(t *testing.T) {
	store := newProjectionTestStore(t)

	withProjectionLiveSessions(t, func() ([]tmux.Session, error) {
		return nil, nil
	})

	output, err := buildProjectionBackedStatus(store, config.Default(), PaginationOptions{})
	if err != nil {
		t.Fatalf("confirmed-idle machine must serve the projection: %v", err)
	}
	if len(output.Sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(output.Sessions))
	}
}

func TestProjectionStatusFreshRowsServedWithoutLiveCheck(t *testing.T) {
	store := newProjectionTestStore(t)
	upsertProjectionSession(t, store, "alpha", time.Now().UTC().Add(time.Hour))

	withProjectionLiveSessions(t, func() ([]tmux.Session, error) {
		t.Fatal("fresh projection must not trigger a live tmux check")
		return nil, nil
	})

	output, err := buildProjectionBackedStatus(store, config.Default(), PaginationOptions{})
	if err != nil {
		t.Fatalf("fresh projection must be served: %v", err)
	}
	if len(output.Sessions) != 1 || output.Sessions[0].Name != "alpha" {
		t.Fatalf("expected [alpha], got %+v", output.Sessions)
	}
}
