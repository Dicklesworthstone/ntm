package watcher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// These tests drive the real watch loop entry points through the same narrow
// service port used by the Agent Mail client. No network or live agents needed.
type ownershipWatcherClient struct {
	reserve func(agentmail.FileReservationOptions) (*agentmail.ReservationResult, error)
	renew   func(agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error)
	release func(string, []string, []int) (*agentmail.ReleaseReservationsResult, error)
}

func (c *ownershipWatcherClient) ReservePaths(_ context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	return c.reserve(opts)
}

func (c *ownershipWatcherClient) ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error) {
	return nil, nil
}

func (c *ownershipWatcherClient) RenewReservations(_ context.Context, opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	if c.renew != nil {
		return c.renew(opts)
	}
	return nil, nil
}

func (c *ownershipWatcherClient) ReleaseReservations(_ context.Context, _ string, name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	return c.release(name, paths, ids)
}

func TestWatcherNeverOwnsUnverifiedConflictGrants(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantIDs []int
	}{
		{"verified", nil, []int{101}},
		{"verified partial conflict", agentmail.ErrReservationConflict, []int{101}},
		{"unverified", agentmail.ErrReservationUnverified, nil},
		{"conflict and unverified", errors.Join(agentmail.ErrReservationConflict, agentmail.ErrReservationUnverified), nil},
		{"wrapped conflict and unverified", fmt.Errorf("request failed: %w", errors.Join(agentmail.ErrReservationConflict, agentmail.ErrReservationUnverified)), nil},
		{"transport error", errors.New("connection lost"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := NewFileReservationWatcher(WithAgentName("BlueLake"), WithProjectDir("/test/project"))
			w.capturePaneOutput = func(context.Context, string, int) (string, error) {
				return "Edited src/main.go", nil
			}
			var released []int
			w.client = &ownershipWatcherClient{
				reserve: func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
					if o.AgentName != "BlueLake" || !reflect.DeepEqual(o.Paths, []string{"src/main.go"}) {
						t.Fatalf("unexpected acquisition: %+v", o)
					}
					return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{{
						ID: 101, PathPattern: "src/main.go", AgentName: o.AgentName, ProjectID: 1,
						Exclusive: true, Reason: o.Reason,
						ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
					}}}, tc.err
				},
				release: func(name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
					if name != "BlueLake" || len(paths) != 0 {
						t.Fatalf("cleanup broadened ownership: name=%q paths=%v", name, paths)
					}
					released = append(released, ids...)
					return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
				},
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			owned := w.activeReservations["%7"]
			if owned == nil || !reflect.DeepEqual(owned.ReservationID, tc.wantIDs) {
				t.Fatalf("owned=%+v, want IDs %v", owned, tc.wantIDs)
			}
			w.Stop()
			if !reflect.DeepEqual(released, tc.wantIDs) {
				t.Fatalf("released=%v, want only verified IDs %v", released, tc.wantIDs)
			}
		})
	}
}

// Exercise topology, capture, edit detection and reservation together, not just
// a hand-built result passed directly to recordReservationResult.
func (w *FileReservationWatcher) checkPaneOutputsFromOwnershipTest(t *testing.T) {
	t.Helper()
	w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{"demo": {{ID: "%7", Type: tmux.AgentClaude}}}, nil
	}
	w.checkPaneOutputs(context.Background())
}

func TestWatcherCleanupSelectsOnlyRecordedIDs(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%v", stop), func(t *testing.T) {
			w := NewFileReservationWatcher(WithProjectDir("/test/project"))
			w.activeReservations["%7"] = &PaneReservation{
				PaneID: "%7", AgentName: "BlueLake", Files: []string{"src/main.go"},
				ReservationID: []int{101}, LastActivity: time.Now().Add(-time.Hour),
			}
			// The agent also owns a manual lease on the same path. A server
			// accepting paths OR IDs would release both if we sent both sets.
			manualLeaseReleased := false
			calls := 0
			w.client = &ownershipWatcherClient{release: func(name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
				calls++
				manualLeaseReleased = len(paths) != 0
				if name != "BlueLake" || !reflect.DeepEqual(ids, []int{101}) {
					t.Fatalf("wrong cleanup owner or IDs: %q %v", name, ids)
				}
				return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
			}}
			if stop {
				w.Stop()
			} else {
				w.releaseIdleReservations(context.Background())
			}
			if calls != 1 || manualLeaseReleased || len(w.activeReservations) != 0 {
				t.Fatalf("calls=%d manualReleased=%v tracked=%v", calls, manualLeaseReleased, w.activeReservations)
			}
		})
	}
}

func TestWatcherDoesNotRelabelLeasesAfterIdentityChange(t *testing.T) {
	w := NewFileReservationWatcher(WithAgentNameResolver(func(string) string { return "NewAgent" }))
	w.activeReservations["%7"] = &PaneReservation{
		PaneID: "%7", AgentName: "OldAgent", Files: []string{"src/old.go"}, ReservationID: []int{101},
	}
	name, paths := w.prepareReservationAttempt("%7", "demo", "new output", []string{"src/new.go"}, time.Now())
	if name != "" || len(paths) != 0 || w.activeReservations["%7"].AgentName != "OldAgent" {
		t.Fatalf("existing leases were reassigned: name=%q paths=%v tracked=%+v", name, paths, w.activeReservations["%7"])
	}
	// An intent with no granted IDs owns nothing and may adopt a now-known
	// identity. It must not be stuck forever on a missing/fallback name.
	w.activeReservations["%7"].ReservationID = nil
	w.activeReservations["%7"].Files = nil
	name, paths = w.prepareReservationAttempt("%7", "demo", "new output", []string{"src/new.go"}, time.Now())
	if name != "NewAgent" || !reflect.DeepEqual(paths, []string{"src/new.go"}) {
		t.Fatalf("unowned intent did not adopt identity: %q %v", name, paths)
	}
}

func TestWatcherNilRenewalReceiptAndNilClient(t *testing.T) {
	w := NewFileReservationWatcher(WithDebug(true), WithWatcherClient(nil))
	if w.client != nil {
		t.Fatal("typed nil client bypasses the disabled-client gate")
	}
	w.activeReservations["%7"] = &PaneReservation{PaneID: "%7", AgentName: "BlueLake", ReservationID: []int{101}}
	w.renewPaneReservation(context.Background(), "%7") // No client, no operation.
	w.client = &ownershipWatcherClient{}               // Missing receipt, including in debug mode.
	w.renewPaneReservation(context.Background(), "%7")
	if !reflect.DeepEqual(w.activeReservations["%7"].ReservationID, []int{101}) {
		t.Fatal("missing receipt discarded recovery handles")
	}
}
