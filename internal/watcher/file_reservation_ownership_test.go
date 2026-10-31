package watcher

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
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
	read    func(string) ([]agentmail.FileReservation, error)
}

func (c *ownershipWatcherClient) ReservePaths(_ context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	return c.reserve(opts)
}

func (c *ownershipWatcherClient) ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error) {
	return nil, nil
}

func (c *ownershipWatcherClient) ReadActiveReservations(_ context.Context, project string) ([]agentmail.FileReservation, error) {
	if c.read != nil {
		return c.read(project)
	}
	return nil, errors.New("readback unavailable")
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

// A conflict the server reports needs no ownership proof, so an unverified
// receipt still raises it, and that receipt's grant stays out of the owned set.
func TestWatcherRaisesConflictsFromAnUnverifiedReceipt(t *testing.T) {
	var raised []FileConflict
	w := NewFileReservationWatcher(WithAgentName("BlueLake"), WithProjectDir("/test/project"),
		WithConflictCallback(func(conflict FileConflict) { raised = append(raised, conflict) }))
	w.capturePaneOutput = func(context.Context, string, int) (string, error) {
		return "Edited src/main.go", nil
	}
	w.client = &ownershipWatcherClient{
		reserve: func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
			return &agentmail.ReservationResult{
				Granted:   []agentmail.FileReservation{{ID: 101, PathPattern: "src/main.go", AgentName: o.AgentName}},
				Conflicts: []agentmail.ReservationConflict{{Path: "src/main.go", Holders: []string{"GreenCastle"}}},
			}, errors.Join(agentmail.ErrReservationConflict, agentmail.ErrReservationUnverified)
		},
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	if len(raised) != 1 || raised[0].Path != "src/main.go" || !reflect.DeepEqual(raised[0].Holders, []string{"GreenCastle"}) {
		t.Fatalf("raised=%+v", raised)
	}
	if owned := w.activeReservations["%7"]; owned != nil && len(owned.ReservationID) != 0 {
		t.Fatalf("unverified grant entered the owned set: %+v", owned)
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

func newRecoveryWatcher(t *testing.T) (*FileReservationWatcher, *ownershipWatcherClient, *string, *int) {
	t.Helper()
	output := "Edited src/main.go"
	calls := 0
	w := NewFileReservationWatcher(WithAgentName("BlueLake"), WithProjectDir("/test/project"), WithExtendOnActivity(false))
	client := &ownershipWatcherClient{
		reserve: func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
			calls++
			grants := make([]agentmail.FileReservation, 0, len(o.Paths))
			for i, path := range o.Paths {
				grants = append(grants, agentmail.FileReservation{
					ID: calls*100 + i, AgentName: o.AgentName, ProjectID: 1,
					PathPattern: path, Exclusive: true, Reason: o.Reason,
					ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
				})
			}
			return &agentmail.ReservationResult{Granted: grants}, nil
		},
		release: func(_ string, _ []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
			return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
		},
	}
	w.client = client
	w.capturePaneOutput = func(context.Context, string, int) (string, error) { return output, nil }
	return w, client, &output, &calls
}

func TestWatcherRetriesLateIdentityWithoutNewOutput(t *testing.T) {
	w, _, _, calls := newRecoveryWatcher(t)
	name := ""
	w.agentName = ""
	w.agentNameResolver = func(string) string { return name }
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 0 || w.pendingEdits["%7"] == nil {
		t.Fatal("unregistered edit was attempted or forgotten")
	}
	observedAt := w.pendingEdits["%7"].observedAt
	name = "BlueLake"
	w.checkPaneOutputsFromOwnershipTest(t) // byte-identical capture
	if *calls != 1 || w.pendingEdits["%7"] != nil {
		t.Fatalf("calls=%d pending=%+v", *calls, w.pendingEdits)
	}
	if !w.activeReservations["%7"].LastActivity.Equal(observedAt) {
		t.Fatal("a retry fabricated new pane activity")
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 {
		t.Fatal("already held path was acquired again")
	}
}

func TestWatcherRetriesOnlyMissingVerifiedConflictPaths(t *testing.T) {
	w, client, output, calls := newRecoveryWatcher(t)
	*output = "Edited src/main.go\nEdited src/other.go"
	reserve := client.reserve
	client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if *calls == 0 {
			first := o
			first.Paths = o.Paths[:1]
			result, _ := reserve(first)
			result.Conflicts = []agentmail.ReservationConflict{{Path: "src/other.go", Holders: []string{"OtherAgent"}}}
			return result, agentmail.ErrReservationConflict
		}
		if !reflect.DeepEqual(o.Paths, []string{"src/other.go"}) {
			t.Fatalf("retry included already granted paths: %v", o.Paths)
		}
		return reserve(o)
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || !reflect.DeepEqual(w.pendingEdits["%7"].files, []string{"src/other.go"}) {
		t.Fatalf("partial receipt lost pending work: calls=%d pending=%+v", *calls, w.pendingEdits)
	}
	// Both file names scroll out while the other agent releases its conflict.
	*output = "Continuing implementation"
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 2 || len(w.activeReservations["%7"].ReservationID) != 2 || w.pendingEdits["%7"] != nil {
		t.Fatalf("pending edit was not recovered: calls=%d tracked=%+v pending=%+v", *calls, w.activeReservations["%7"], w.pendingEdits)
	}
}

func TestWatcherDoesNotBlindlyReplayUncertainAcquisitions(t *testing.T) {
	for _, failure := range []error{
		errors.New("connection lost after dispatch"),
		agentmail.ErrReservationUnverified,
		errors.Join(agentmail.ErrReservationConflict, agentmail.ErrReservationUnverified),
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			w, client, output, calls := newRecoveryWatcher(t)
			reserve := client.reserve
			client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				result, _ := reserve(o)
				return result, failure
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			w.checkPaneOutputsFromOwnershipTest(t)
			*output = "Edited src/new.go"
			w.checkPaneOutputsFromOwnershipTest(t)
			if *calls != 1 || !w.pendingEdits["%7"].blocked || len(w.activeReservations["%7"].ReservationID) != 0 {
				t.Fatalf("uncertain receipt was replayed or promoted: calls=%d pending=%+v tracked=%+v", *calls, w.pendingEdits["%7"], w.activeReservations["%7"])
			}
		})
	}
}

func expireRecoveryLease(t *testing.T, w *FileReservationWatcher) agentmail.FileReservation {
	t.Helper()
	tracked := w.activeReservations["%7"]
	if tracked == nil || len(tracked.leases) != 1 {
		t.Fatalf("expected one verified lease, got %+v", tracked)
	}
	id := tracked.ReservationID[0]
	lease := tracked.leases[id]
	lease.ExpiresTS = agentmail.FlexTime{Time: time.Now().Add(-time.Second)}
	tracked.leases[id] = lease
	return lease
}

func TestWatcherReacquiresExpiredLeaseWithoutNewOutput(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired-row-present=%v", present), func(t *testing.T) {
			w, client, _, calls := newRecoveryWatcher(t)
			w.checkPaneOutputsFromOwnershipTest(t)
			observedAt := w.activeReservations["%7"].LastActivity
			lease := expireRecoveryLease(t, w)
			reads := 0
			client.read = func(project string) ([]agentmail.FileReservation, error) {
				reads++
				if project != "/test/project" {
					t.Fatalf("wrong readback project: %q", project)
				}
				if present {
					return []agentmail.FileReservation{lease}, nil
				}
				return nil, nil
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			tracked := w.activeReservations["%7"]
			if *calls != 2 || reads != 1 || !reflect.DeepEqual(tracked.ReservationID, []int{200}) || !tracked.LastActivity.Equal(observedAt) {
				t.Fatalf("calls=%d reads=%d tracked=%+v", *calls, reads, tracked)
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			if *calls != 2 || reads != 1 {
				t.Fatal("fresh verified replacement was acquired/read again")
			}
		})
	}
}

func TestWatcherExpiryReadbackFailsClosedThenRecovers(t *testing.T) {
	w, client, _, calls := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	lease := expireRecoveryLease(t, w)
	client.read = func(string) ([]agentmail.FileReservation, error) { return nil, errors.New("service unavailable") }
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || !reflect.DeepEqual(w.activeReservations["%7"].ReservationID, []int{lease.ID}) {
		t.Fatal("read failure was treated as proof of expiry/absence")
	}
	client.read = func(string) ([]agentmail.FileReservation, error) { return nil, nil }
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 2 || !reflect.DeepEqual(w.activeReservations["%7"].ReservationID, []int{200}) {
		t.Fatal("unchanged pane did not recover after readback became available")
	}
}

func TestWatcherRecoversRenewalReadbackWithoutDuplicateAcquisition(t *testing.T) {
	w, client, _, calls := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	lease := expireRecoveryLease(t, w)
	lease.ExpiresTS = agentmail.FlexTime{Time: time.Now().Add(time.Hour)}
	client.read = func(string) ([]agentmail.FileReservation, error) { return []agentmail.FileReservation{lease}, nil }
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || !w.activeReservations["%7"].leases[lease.ID].ExpiresTS.Equal(lease.ExpiresTS.Time) {
		t.Fatalf("confirmed renewal was lost: calls=%d tracked=%+v", *calls, w.activeReservations["%7"])
	}
}

func TestWatcherRejectsMismatchedExpiryEvidence(t *testing.T) {
	for _, mutate := range []struct {
		name string
		edit func(*agentmail.FileReservation)
	}{
		{"owner", func(r *agentmail.FileReservation) { r.AgentName = "OtherAgent" }},
		{"project", func(r *agentmail.FileReservation) { r.ProjectID++ }},
		{"path", func(r *agentmail.FileReservation) { r.PathPattern = "other/file.go" }},
		{"reason", func(r *agentmail.FileReservation) { r.Reason = "manual lease" }},
		{"exclusivity", func(r *agentmail.FileReservation) { r.Exclusive = false }},
		{"missing expiry", func(r *agentmail.FileReservation) { r.ExpiresTS = agentmail.FlexTime{} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			w, client, _, calls := newRecoveryWatcher(t)
			w.checkPaneOutputsFromOwnershipTest(t)
			original := expireRecoveryLease(t, w)
			row := original
			mutate.edit(&row)
			client.read = func(string) ([]agentmail.FileReservation, error) { return []agentmail.FileReservation{row}, nil }
			w.checkPaneOutputsFromOwnershipTest(t)
			if *calls != 1 || !reflect.DeepEqual(w.activeReservations["%7"].leases[original.ID], original) {
				t.Fatalf("mismatched evidence changed ownership: calls=%d tracked=%+v", *calls, w.activeReservations["%7"])
			}
		})
	}
}

func TestWatcherRetriesFailedRenewalWithoutInventingActivity(t *testing.T) {
	w, client, output, _ := newRecoveryWatcher(t)
	w.extendOnActivity = true
	w.checkPaneOutputsFromOwnershipTest(t)
	renews := 0
	client.renew = func(o agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
		renews++
		if o.AgentName != "BlueLake" || len(o.Paths) != 0 || !reflect.DeepEqual(o.ReservationIDs, []int{100}) {
			t.Fatalf("renewal broadened ownership: %+v", o)
		}
		if renews == 1 {
			return nil, errors.New("temporary renewal failure")
		}
		return &agentmail.RenewReservationsResult{Renewed: 1}, nil
	}
	*output = "Edited src/main.go\nContinuing implementation"
	w.checkPaneOutputsFromOwnershipTest(t)
	observedAt := w.activeReservations["%7"].LastActivity
	w.checkPaneOutputsFromOwnershipTest(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	if renews != 2 || w.activeReservations["%7"].renewalPending || !w.activeReservations["%7"].LastActivity.Equal(observedAt) {
		t.Fatalf("renews=%d tracked=%+v", renews, w.activeReservations["%7"])
	}
}

func TestWatcherPendingRetriesRespectIdleAndTopology(t *testing.T) {
	w, client, _, calls := newRecoveryWatcher(t)
	client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		*calls++
		return &agentmail.ReservationResult{Conflicts: []agentmail.ReservationConflict{{Path: o.Paths[0]}}}, agentmail.ErrReservationConflict
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	w.pendingEdits["%7"].observedAt = time.Now().Add(-time.Hour)
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || w.pendingEdits["%7"] != nil {
		t.Fatal("idle retry renewed its own activity or acquired stale work")
	}
	w.mu.Lock()
	w.queueReservationEditsLocked("%7", []string{"src/main.go"}, "BlueLake", time.Now())
	w.mu.Unlock()
	w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) { return nil, errors.New("tmux unavailable") }
	w.checkPaneOutputs(context.Background())
	if w.pendingEdits["%7"] == nil {
		t.Fatal("topology failure erased pending work")
	}
	w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) { return map[string][]tmux.Pane{}, nil }
	w.checkPaneOutputs(context.Background())
	if len(w.pendingEdits) != 0 || len(w.paneOutputs) != 0 {
		t.Fatal("departed pane retained stale retry/capture state")
	}
}

func TestWatcherCleanupPreservesNewLeaseOnSamePath(t *testing.T) {
	w, client, _, _ := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	client.release = func(_ string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
		if len(paths) != 0 || !reflect.DeepEqual(ids, []int{100}) {
			t.Fatalf("wrong cleanup selection: paths=%v ids=%v", paths, ids)
		}
		// A newer verified acquisition arrives after the release snapshot.
		lease := w.activeReservations["%7"].leases[100]
		lease.ID = 200
		w.recordReservationResult("%7", &agentmail.ReservationResult{Granted: []agentmail.FileReservation{lease}}, time.Now())
		return &agentmail.ReleaseReservationsResult{Released: 1}, nil
	}
	w.releaseAllReservations()
	tracked := w.activeReservations["%7"]
	if tracked == nil || !reflect.DeepEqual(tracked.Files, []string{"src/main.go"}) || !reflect.DeepEqual(tracked.ReservationID, []int{200}) {
		t.Fatalf("old cleanup removed replacement coverage: %+v", tracked)
	}
}

func TestWatcherExpiryReadbackRefusesDuplicateAndCancelledEvidence(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%v", cancelled), func(t *testing.T) {
			w, client, _, calls := newRecoveryWatcher(t)
			w.checkPaneOutputsFromOwnershipTest(t)
			lease := expireRecoveryLease(t, w)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client.read = func(string) ([]agentmail.FileReservation, error) {
				if cancelled {
					cancel()
					return nil, nil
				}
				return []agentmail.FileReservation{lease, lease}, nil
			}
			w.checkPaneOutputs(ctx)
			if *calls != 1 || !reflect.DeepEqual(w.activeReservations["%7"].leases[lease.ID], lease) {
				t.Fatalf("incomplete evidence changed ownership: calls=%d tracked=%+v", *calls, w.activeReservations["%7"])
			}
		})
	}
}

func TestWatcherExpiryReadbackPreservesNewerEvidence(t *testing.T) {
	w, client, _, calls := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	lease := expireRecoveryLease(t, w)
	lease.ExpiresTS = agentmail.FlexTime{Time: time.Now().Add(time.Hour)}
	client.read = func(string) ([]agentmail.FileReservation, error) {
		// Independent newer evidence arrives while the older read is in flight.
		w.recordReservationResult("%7", &agentmail.ReservationResult{Granted: []agentmail.FileReservation{lease}}, time.Now())
		return nil, nil
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || !reflect.DeepEqual(w.activeReservations["%7"].leases[lease.ID], lease) {
		t.Fatalf("stale absence overwrote newer evidence: calls=%d tracked=%+v", *calls, w.activeReservations["%7"])
	}
}

func TestWatcherPartialExpiryPreservesLiveSiblingLease(t *testing.T) {
	w, client, output, calls := newRecoveryWatcher(t)
	*output = "Edited src/main.go\nEdited src/other.go"
	w.checkPaneOutputsFromOwnershipTest(t)
	tracked := w.activeReservations["%7"]
	expired := tracked.leases[100]
	expired.ExpiresTS = agentmail.FlexTime{Time: time.Now().Add(-time.Second)}
	tracked.leases[100] = expired
	live := tracked.leases[101]
	client.read = func(string) ([]agentmail.FileReservation, error) { return []agentmail.FileReservation{live}, nil }
	reserve := client.reserve
	client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
		if !reflect.DeepEqual(o.Paths, []string{"src/main.go"}) {
			t.Fatalf("expiry widened replacement to live sibling: %v", o.Paths)
		}
		return reserve(o)
	}
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 2 || !reflect.DeepEqual(tracked.ReservationID, []int{101, 200}) || !reflect.DeepEqual(tracked.leases[101], live) {
		t.Fatalf("live sibling was lost: calls=%d tracked=%+v", *calls, tracked)
	}
}

func TestWatcherDoesNotReacquireExpiredIdleWork(t *testing.T) {
	w, client, _, calls := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	lease := expireRecoveryLease(t, w)
	w.activeReservations["%7"].LastActivity = time.Now().Add(-time.Hour)
	client.read = func(string) ([]agentmail.FileReservation, error) { return []agentmail.FileReservation{lease}, nil }
	w.checkPaneOutputsFromOwnershipTest(t)
	w.releaseIdleReservations(context.Background())
	if *calls != 1 || w.activeReservations["%7"] != nil || w.pendingEdits["%7"] != nil {
		t.Fatalf("stale work was reacquired: calls=%d tracked=%+v pending=%+v", *calls, w.activeReservations, w.pendingEdits)
	}
}

func TestWatcherFailedCapturePreservesPendingWork(t *testing.T) {
	w, _, _, calls := newRecoveryWatcher(t)
	name := ""
	w.agentName = ""
	w.agentNameResolver = func(string) string { return name }
	w.checkPaneOutputsFromOwnershipTest(t)
	capture := w.capturePaneOutput
	w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "", errors.New("capture unavailable") }
	name = "BlueLake"
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 0 || w.pendingEdits["%7"] == nil {
		t.Fatal("capture failure acted on or forgot pending work")
	}
	w.capturePaneOutput = capture
	w.checkPaneOutputsFromOwnershipTest(t)
	if *calls != 1 || w.pendingEdits["%7"] != nil {
		t.Fatal("successful identical capture did not recover pending work")
	}
}

func TestWatcherReplacementIdentityDoesNotInheritRecovery(t *testing.T) {
	for _, newOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("new-output=%v", newOutput), func(t *testing.T) {
			w, client, output, calls := newRecoveryWatcher(t)
			w.checkPaneOutputsFromOwnershipTest(t)
			expireRecoveryLease(t, w)
			w.extendOnActivity = true
			w.agentNameResolver = func(string) string { return "NewAgent" }
			client.renew = func(agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
				t.Fatal("replacement identity renewed its predecessor's lease")
				return nil, nil
			}
			client.read = func(string) ([]agentmail.FileReservation, error) { return nil, nil }
			reserve := client.reserve
			client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				if o.AgentName != "NewAgent" || !reflect.DeepEqual(o.Paths, []string{"src/new.go"}) {
					t.Fatalf("replacement inherited old work: %+v", o)
				}
				return reserve(o)
			}
			wantCalls := 1
			if newOutput {
				*output = "Edited src/new.go"
				wantCalls++
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			if *calls != wantCalls || w.pendingEdits["%7"] != nil {
				t.Fatalf("calls=%d want=%d pending=%+v", *calls, wantCalls, w.pendingEdits)
			}
		})
	}
}

func TestWatcherReplacementIdentityDoesNotReplayOldConflicts(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%v", uncertain), func(t *testing.T) {
			w, client, output, calls := newRecoveryWatcher(t)
			client.reserve = func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				*calls++
				if uncertain {
					return nil, agentmail.ErrReservationUnverified
				}
				return &agentmail.ReservationResult{Conflicts: []agentmail.ReservationConflict{{Path: o.Paths[0]}}}, agentmail.ErrReservationConflict
			}
			w.checkPaneOutputsFromOwnershipTest(t)
			w.agentNameResolver = func(string) string { return "NewAgent" }
			w.checkPaneOutputsFromOwnershipTest(t)
			if *calls != 1 {
				t.Fatal("new identity replayed old pending work")
			}
			if uncertain {
				// Even new output cannot settle the predecessor's unknown call.
				*output = "Edited src/new.go"
				w.checkPaneOutputsFromOwnershipTest(t)
				if *calls != 1 || !w.pendingEdits["%7"].blocked || w.pendingEdits["%7"].agentName != "BlueLake" {
					t.Fatal("identity replacement erased uncertainty")
				}
			} else if w.pendingEdits["%7"] != nil {
				t.Fatal("old conflict retry was transferred to new identity")
			}
		})
	}
}

func TestWatcherRunRetriesConflictAndStopsWithExactCleanup(t *testing.T) {
	w := NewFileReservationWatcher(WithProjectDir("/test/project"), WithAgentName("BlueLake"), WithReservationPollInterval(time.Millisecond))
	w.listAllPanes = func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{"demo": {{ID: "%7", Type: tmux.AgentClaude}}}, nil
	}
	w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "Edited src/main.go", nil }
	var calls atomic.Int64
	granted := make(chan struct{}, 1)
	var released []int
	w.client = &ownershipWatcherClient{
		reserve: func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
			if calls.Add(1) == 1 {
				return &agentmail.ReservationResult{Conflicts: []agentmail.ReservationConflict{{Path: o.Paths[0]}}}, agentmail.ErrReservationConflict
			}
			select {
			case granted <- struct{}{}:
			default:
			}
			return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{{
				ID: 101, AgentName: o.AgentName, ProjectID: 1, PathPattern: o.Paths[0],
				Reason: o.Reason, Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
			}}}, nil
		},
		release: func(name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
			if name != "BlueLake" || len(paths) != 0 {
				return nil, errors.New("cleanup scope was broadened")
			}
			released = append(released, ids...)
			return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
		},
	}
	w.Start(context.Background())
	t.Cleanup(w.Stop)
	select {
	case <-granted:
	case <-time.After(3 * time.Second):
		t.Fatal("run loop never retried the conflict on an unchanged frame")
	}
	w.Stop() // Joins the worker before reading any of its mutable state.
	if calls.Load() != 2 || !reflect.DeepEqual(released, []int{101}) || len(w.pendingEdits) != 0 || len(w.activeReservations) != 0 || len(w.paneOutputs) != 0 {
		t.Fatalf("calls=%d released=%v pending=%+v tracked=%+v outputs=%+v", calls.Load(), released, w.pendingEdits, w.activeReservations, w.paneOutputs)
	}
}

// This probe uses only the pre-existing polling entry point and verified
// reservation models, so the same test can be run against the old behavior.
func TestWatcherPollingRecoveryBehavior(t *testing.T) {
	for _, lateIdentity := range []bool{false, true} {
		t.Run(fmt.Sprintf("late-identity=%v", lateIdentity), func(t *testing.T) {
			name := "BlueLake"
			if lateIdentity {
				name = ""
			}
			w := NewFileReservationWatcher(WithProjectDir("/test/project"), WithAgentNameResolver(func(string) string { return name }))
			w.capturePaneOutput = func(context.Context, string, int) (string, error) { return "Edited src/main.go", nil }
			calls := 0
			w.client = &ownershipWatcherClient{reserve: func(o agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				calls++
				if !lateIdentity && calls == 1 {
					return &agentmail.ReservationResult{Conflicts: []agentmail.ReservationConflict{{Path: o.Paths[0]}}}, agentmail.ErrReservationConflict
				}
				return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{{
					ID: 101, PathPattern: o.Paths[0], AgentName: o.AgentName, ProjectID: 1,
					Exclusive: true, Reason: o.Reason, ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
				}}}, nil
			}}
			w.checkPaneOutputsFromOwnershipTest(t)
			name = "BlueLake"
			w.checkPaneOutputsFromOwnershipTest(t) // Identical output; prerequisites recovered.
			wantCalls := 2
			if lateIdentity {
				wantCalls = 1
			}
			tracked := w.activeReservations["%7"]
			if calls != wantCalls || tracked == nil || !reflect.DeepEqual(tracked.ReservationID, []int{101}) {
				t.Fatalf("polling did not recover: calls=%d want=%d tracked=%+v", calls, wantCalls, tracked)
			}
		})
	}
}

// A service call that cooperates with cancellation must not hold Stop hostage.
// The fallback channel only lets a failing baseline probe finish cleanly.
type cancellingWatcherClient struct {
	*ownershipWatcherClient
	stage        string
	entered      chan struct{}
	fallback     chan struct{}
	lateGrant    bool
	released     []int
	cleanupError error
}

func (c *cancellingWatcherClient) block(ctx context.Context) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.fallback:
		return errors.New("probe released a stuck operation")
	}
}

func (c *cancellingWatcherClient) ReservePaths(ctx context.Context, opts agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	if c.stage != "acquire" {
		return c.ownershipWatcherClient.ReservePaths(ctx, opts)
	}
	err := c.block(ctx)
	if c.lateGrant {
		// Verification may have completed just before cancellation. Such a
		// receipt still owns a handle that shutdown must not abandon.
		return &agentmail.ReservationResult{Granted: []agentmail.FileReservation{{
			ID: 101, AgentName: opts.AgentName, ProjectID: 1, PathPattern: opts.Paths[0],
			Reason: opts.Reason, Exclusive: true, ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
		}}}, nil
	}
	return nil, err
}

func (c *cancellingWatcherClient) RenewReservations(ctx context.Context, opts agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	if c.stage == "renew" {
		return nil, c.block(ctx)
	}
	return c.ownershipWatcherClient.RenewReservations(ctx, opts)
}

func (c *cancellingWatcherClient) ReadActiveReservations(ctx context.Context, project string) ([]agentmail.FileReservation, error) {
	if c.stage == "readback" {
		return nil, c.block(ctx)
	}
	return c.ownershipWatcherClient.ReadActiveReservations(ctx, project)
}

func (c *cancellingWatcherClient) ReleaseReservations(ctx context.Context, _ string, name string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	if ctx.Err() != nil || name != "BlueLake" || len(paths) != 0 {
		c.cleanupError = fmt.Errorf("invalid cleanup context/selection: err=%v name=%q paths=%v", ctx.Err(), name, paths)
		return nil, c.cleanupError
	}
	c.released = append(c.released, ids...)
	return &agentmail.ReleaseReservationsResult{Released: len(ids)}, nil
}

func TestWatcherStopCancelsInflightWork(t *testing.T) {
	for _, stage := range []string{"topology", "capture", "acquire", "renew", "readback", "late verified grant"} {
		t.Run(stage, func(t *testing.T) {
			w, client, _, _ := newRecoveryWatcher(t)
			w.pollInterval = time.Millisecond
			c := &cancellingWatcherClient{
				ownershipWatcherClient: client, stage: stage,
				entered: make(chan struct{}, 1), fallback: make(chan struct{}),
			}
			if stage == "late verified grant" {
				c.stage, c.lateGrant = "acquire", true
			}
			if stage == "renew" || stage == "readback" {
				w.checkPaneOutputsFromOwnershipTest(t)
				if stage == "readback" {
					expireRecoveryLease(t, w)
				} else {
					w.extendOnActivity = true
					w.activeReservations["%7"].renewalPending = true
				}
			}
			w.client = c
			w.listAllPanes = func(ctx context.Context) (map[string][]tmux.Pane, error) {
				if stage == "topology" {
					return nil, c.block(ctx)
				}
				return map[string][]tmux.Pane{"demo": {{ID: "%7", Type: tmux.AgentClaude}}}, nil
			}
			if stage == "capture" {
				w.capturePaneOutput = func(ctx context.Context, _ string, _ int) (string, error) { return "", c.block(ctx) }
			}
			w.Start(context.Background())
			select {
			case <-c.entered:
			case <-time.After(time.Second):
				close(c.fallback)
				w.Stop()
				t.Fatal("polling never entered the operation")
			}
			done := make(chan struct{})
			go func() { w.Stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				close(c.fallback)
				<-done
				t.Fatal("Stop did not cancel the in-flight operation")
			}
			if c.cleanupError != nil {
				t.Fatal(c.cleanupError)
			}
			var want []int
			if stage == "renew" || stage == "readback" {
				want = []int{100}
			} else if c.lateGrant {
				want = []int{101}
			}
			if !reflect.DeepEqual(c.released, want) || len(w.activeReservations) != 0 || len(w.pendingEdits) != 0 {
				t.Fatalf("shutdown leaked ownership: released=%v want=%v tracked=%+v pending=%+v", c.released, want, w.activeReservations, w.pendingEdits)
			}
		})
	}
}

func TestWatcherParentCancellationCleansOwnedLeases(t *testing.T) {
	w, client, _, _ := newRecoveryWatcher(t)
	w.checkPaneOutputsFromOwnershipTest(t)
	w.pollInterval = time.Millisecond
	c := &cancellingWatcherClient{ownershipWatcherClient: client, entered: make(chan struct{}, 1), fallback: make(chan struct{})}
	w.client = c
	w.capturePaneOutput = func(ctx context.Context, _ string, _ int) (string, error) { return "", c.block(ctx) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		cancel()
		w.Stop()
		t.Fatal("worker never entered capture")
	}
	cancel()
	// Join without Stop: parent cancellation itself must clean up ownership.
	w.wg.Wait()
	if c.cleanupError != nil || !reflect.DeepEqual(c.released, []int{100}) || len(w.activeReservations) != 0 {
		t.Errorf("parent cancellation abandoned verified leases: released=%v error=%v", c.released, c.cleanupError)
		w.Stop()
		return
	}
	w.Stop()
	if !reflect.DeepEqual(c.released, []int{100}) {
		t.Fatal("Stop repeated already-completed cleanup")
	}
}
