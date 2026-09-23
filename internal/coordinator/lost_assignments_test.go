package coordinator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func lostAssignmentCoordinator(t *testing.T) (*SessionCoordinator, *assignmentstore.AssignmentStore, *fakeCoordinatorReservationClient, *int) {
	t.Helper()
	c, store, client := newAssignmentHeartbeatTestCoordinator(t)
	current := store.Assignments["ntm-heartbeat"]
	current.DispatchReceiptID = "agent-mail-message-94"
	expires := time.Now().UTC().Add(time.Hour)
	current.ReservationExpiresAt = &expires
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
		return []tmux.PaneActivity{{Pane: tmux.Pane{ID: "%97", Index: 2, Title: c.session + "__cc_2", Type: tmux.AgentClaude}}}, nil
	}, nil)
	c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
		return map[string][]tmux.Pane{c.session: {{ID: "%97", Index: 2}}}, nil
	}
	claims := new(int)
	c.releaseWorkItemClaimFn = func(_ context.Context, project, bead, actor string) (bool, error) {
		if project != c.projectKey || bead != current.BeadID || actor != current.ClaimActor {
			t.Fatalf("release changed claim ownership: project=%q bead=%q actor=%q", project, bead, actor)
		}
		for _, lease := range client.reservations {
			if lease.ID == 941 || lease.ID == 942 {
				t.Fatal("claim reopened before its exact leases were released")
			}
		}
		*claims++
		return true, nil
	}
	return c, store, client, claims
}

func setLostAssignmentObservation(c *SessionCoordinator, list func(context.Context, string) ([]tmux.PaneActivity, error), captureErr error) {
	c.monitor.observer = status.NewSessionObserverWithDependencies(status.NewDetector(), status.SessionObserverConfig{}, status.SessionObserverDependencies{
		ListPanes: list,
		CapturePane: func(context.Context, string, int) (string, error) {
			return idleCapture, captureErr
		},
	})
}

func TestRunCycleRecoversDeliveredAssignmentFromProvenLostOwner(t *testing.T) {
	c, store, client, claims := lostAssignmentCoordinator(t)
	before := store.Get("ntm-heartbeat")
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("recover lost assignment: %v", err)
	}
	if err := store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	after := store.Get(before.BeadID)
	if after.Status != assignmentstore.StatusFailed || !strings.Contains(after.FailReason, "%94") ||
		after.DispatchState != assignmentstore.DispatchSent || after.DispatchReceiptID != before.DispatchReceiptID ||
		after.IdempotencyKey != before.IdempotencyKey || after.ClaimActor != before.ClaimActor ||
		after.ClearState != assignmentstore.ClearStateNone || len(after.ReservationIDs) != 0 ||
		len(store.ListActive()) != 0 || len(store.ListPendingCompletionEvents()) != 0 {
		t.Fatalf("lost assignment was not retired with its delivery evidence intact: %+v", after)
	}
	if *claims != 1 || len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{941, 942}) ||
		len(client.reservations) != 1 || client.reservations[0].ID != 999 || len(client.renewRequests) != 0 {
		t.Fatalf("cleanup widened scope: claims=%d releases=%v remaining=%v renewals=%v", *claims, client.releaseIDs, client.reservations, client.renewRequests)
	}
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *claims != 1 || len(client.releaseIDs) != 1 {
		t.Fatal("repeated observation replayed external cleanup")
	}
}

func TestLostAssignmentRecoveryRequiresCompleteOwnershipProof(t *testing.T) {
	for _, failure := range []string{
		"topology unavailable", "empty pane identity", "repeated pane identity", "unreadable live owner", "unrecognized live owner",
		"owner returned during proof", "recipient rebound", "registry owner changed", "claim owner changed", "claim unreadable",
		"pane moved to another session", "recipient rebound in another session", "server topology unavailable", "server topology incomplete",
		"recipient rebound during server query", "recipient rebound to existing pane during server query",
		"delivery receipt missing", "reservation target changed", "reservation owner changed", "reservation IDs missing",
	} {
		t.Run(failure, func(t *testing.T) {
			c, store, client, claims := lostAssignmentCoordinator(t)
			current := store.Assignments["ntm-heartbeat"]
			switch failure {
			case "topology unavailable":
				setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
					return nil, errors.New("tmux unavailable")
				}, nil)
			case "empty pane identity", "repeated pane identity", "unreadable live owner", "unrecognized live owner":
				id, agentType := "%94", tmux.AgentClaude
				var captureErr error
				if failure == "empty pane identity" {
					id = ""
				}
				if failure == "unrecognized live owner" {
					agentType = tmux.AgentUser
				}
				if failure == "unreadable live owner" {
					captureErr = errors.New("capture unavailable")
				}
				panes := []tmux.PaneActivity{{Pane: tmux.Pane{ID: id, Type: agentType}}}
				if failure == "repeated pane identity" {
					panes = []tmux.PaneActivity{{Pane: tmux.Pane{ID: "%97", Type: agentType}}, {Pane: tmux.Pane{ID: "%97", Type: agentType}}}
				}
				setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) { return panes, nil }, captureErr)
			case "owner returned during proof":
				calls := 0
				setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
					calls++
					id := "%97"
					if calls > 1 {
						id = "%94"
					}
					return []tmux.PaneActivity{{Pane: tmux.Pane{ID: id, Type: tmux.AgentClaude}}}, nil
				}, nil)
			case "recipient rebound", "registry owner changed":
				registry := agentmail.NewSessionAgentRegistry(c.session, c.projectKey)
				registry.PaneIDMap["%94"] = current.AgentName
				if failure == "recipient rebound" {
					// Even an unrecognized live pane is evidence that this name
					// may still act on its delivered Agent Mail assignment.
					registry.PaneIDMap["%97"] = current.AgentName
					setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
						return []tmux.PaneActivity{{Pane: tmux.Pane{ID: "%97", Type: tmux.AgentUser}}}, nil
					}, nil)
				} else {
					registry.PaneIDMap["%94"] = "RedLake"
				}
				if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
					t.Fatal(err)
				}
			case "claim owner changed", "claim unreadable":
				c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
					if failure == "claim unreadable" {
						return nil, errors.New("br unavailable")
					}
					return &bv.BeadAssignmentDetails{ID: current.BeadID, Status: "in_progress", Assignee: "DifferentGeneration"}, nil
				}
			case "pane moved to another session", "recipient rebound in another session", "server topology unavailable", "server topology incomplete":
				if failure == "recipient rebound in another session" {
					registry := agentmail.NewSessionAgentRegistry("other-session", c.projectKey)
					registry.AddAgent("other-user", "%98", current.AgentName)
					if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
						t.Fatal(err)
					}
				}
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					if failure == "server topology unavailable" {
						return nil, errors.New("global list unavailable")
					}
					if failure == "server topology incomplete" {
						return map[string][]tmux.Pane{}, nil
					}
					id := "%98"
					if failure == "pane moved to another session" {
						id = "%94"
					}
					return map[string][]tmux.Pane{c.session: {{ID: "%97"}}, "other-session": {{ID: id}}}, nil
				}
			case "recipient rebound during server query", "recipient rebound to existing pane during server query":
				c.allPanesForRecoveryFn = func(context.Context) (map[string][]tmux.Pane, error) {
					registry, err := agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
					if err != nil {
						t.Fatal(err)
					}
					panes := []tmux.Pane{{ID: "%97"}}
					rebound := "%97"
					if failure == "recipient rebound during server query" {
						rebound = "%98"
						panes = append(panes, tmux.Pane{ID: rebound})
					}
					// Keep the old binding too so this exercises the fresh
					// live-recipient check, not only missing original metadata.
					registry.PaneIDMap[rebound] = current.AgentName
					if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
						t.Fatal(err)
					}
					return map[string][]tmux.Pane{c.session: panes}, nil
				}
			case "delivery receipt missing":
				current.DispatchReceiptID = ""
			case "reservation target changed":
				current.ReservationTarget = "%98"
			case "reservation owner changed":
				current.ReservationAgent = "RedLake"
			case "reservation IDs missing":
				current.ReservationIDs = nil
			}
			if err := store.Save(); err != nil {
				t.Fatal(err)
			}
			before := store.Get(current.BeadID)
			_, _ = c.RunCycle(t.Context())
			if err := store.LoadStrict(); err != nil {
				t.Fatal(err)
			}
			if after := store.Get(current.BeadID); !reflect.DeepEqual(before, after) || *claims != 0 || len(client.releaseIDs) != 0 || len(client.renewRequests) != 0 {
				t.Fatalf("uncertain owner lost its assignment: before=%+v after=%+v claims=%d releases=%v", before, after, *claims, client.releaseIDs)
			}
		})
	}
}

func TestLostAssignmentCleanupRetryPreservesOutboxAndBlocksNewWork(t *testing.T) {
	c, store, client, claims := lostAssignmentCoordinator(t)
	c.WithCompletionEvents()
	c.config.AutoAssign = true
	client.releaseErr = errors.New("lease server unavailable")
	dispatchPasses := 0
	c.assignWorkFn = func(context.Context) ([]AssignmentResult, error) {
		dispatchPasses++
		fresh, err := assignmentstore.LoadStoreStrictReadOnly(c.session)
		if err != nil {
			t.Fatal(err)
		}
		if row := fresh.Get("ntm-heartbeat"); row.Status != assignmentstore.StatusFailed || row.ClearState != assignmentstore.ClearStateNone || *claims != 1 {
			t.Fatalf("new work admitted before cleanup completed: %+v claims=%d", row, *claims)
		}
		return nil, nil
	}
	if _, err := c.RunCycle(t.Context()); err == nil {
		t.Fatal("unreleased leases reported recovery success")
	}
	if err := store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	barrier := store.Get("ntm-heartbeat")
	eventID := barrier.PendingCompletionEventID
	if barrier.PendingTerminalStatus != assignmentstore.StatusFailed || barrier.ClearState != assignmentstore.ClearStateReservationReleasing ||
		eventID == "" || dispatchPasses != 0 || *claims != 0 || len(store.ListPendingCompletionEvents()) != 0 {
		t.Fatalf("failed cleanup lost its barrier or admitted work: %+v", barrier)
	}
	failedAttempts := len(client.releaseIDs)
	client.releaseErr = nil
	c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
		t.Fatal("retry tried to reconstruct already committed owner-loss proof")
		return nil, nil
	}
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("resume owner loss cleanup: %v", err)
	}
	if err := store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	terminal := store.Get(barrier.BeadID)
	if dispatchPasses != 1 || *claims != 1 || len(client.releaseIDs) != failedAttempts+1 || terminal.PendingCompletionEventID != eventID || len(store.ListPendingCompletionEvents()) != 1 {
		t.Fatalf("cleanup retry lost or duplicated the failed completion: %+v claims=%d releases=%v", terminal, *claims, client.releaseIDs)
	}
	const consumer = "lost-owner-consumer"
	if _, claimed, err := store.ClaimPendingCompletionEvent(t.Context(), terminal.BeadID, eventID, consumer, time.Minute); err != nil || !claimed {
		t.Fatalf("claim failed completion: claimed=%t err=%v", claimed, err)
	}
	if acked, err := store.AcknowledgeCompletionEvent(t.Context(), terminal.BeadID, eventID, consumer); err != nil || !acked {
		t.Fatalf("acknowledge failed completion: acked=%t err=%v", acked, err)
	}
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	if *claims != 1 || len(client.releaseIDs) != failedAttempts+1 || len(store.ListPendingCompletionEvents()) != 0 {
		t.Fatal("later maintenance replayed cleanup or resurrected an acknowledged event")
	}
}

func TestLostAssignmentRecoveryAllowsOrdinaryAdmissionAfterCleanup(t *testing.T) {
	c, store, client, claims := lostAssignmentCoordinator(t)
	c.config.AutoAssign, c.config.IdleThreshold = true, 0
	c.mailClient = &agentmail.Client{}
	registry, err := agentmail.LoadSessionAgentRegistry(c.session, c.projectKey)
	if err != nil {
		t.Fatal(err)
	}
	registry.AddAgent(c.session+"__cc_2", "%97", "GreenLake")
	if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
		t.Fatal(err)
	}
	c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
		details := &bv.BeadAssignmentDetails{ID: "ntm-heartbeat", Title: "Recover interrupted work", IssueType: "task", Status: "in_progress", Assignee: "BlueLake:heartbeat-generation"}
		if *claims > 0 {
			details.Status, details.Assignee = "open", ""
		}
		return details, nil
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		if *claims != 1 || len(client.releaseIDs) != 1 {
			t.Fatal("planned replacement work before ownership cleanup")
		}
		return []bv.TriageRecommendation{{ID: "ntm-heartbeat", Title: "Recover interrupted work", Type: "task", Status: "open", Score: 1}}, nil
	}
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		return successfulCoordinatorAtomicFactory("replacement-delivery")(store).
			WithWorkItemStatusPort(assignmentstore.WorkItemStatusFunc(func(context.Context, string) (string, error) {
				return "open", nil
			}))
	}
	results, err := c.RunCycle(t.Context())
	if err != nil || len(results) != 1 || !results[0].Success {
		t.Fatalf("normal admission did not recover available work: results=%+v err=%v", results, err)
	}
	if err := store.LoadStrict(); err != nil {
		t.Fatal(err)
	}
	current := store.Get("ntm-heartbeat")
	if current.Status != assignmentstore.StatusAssigned || current.DispatchReceiptID != "replacement-delivery" ||
		current.DispatchTarget != "%97" || current.AgentName != "GreenLake" || current.IdempotencyKey == "heartbeat-generation" {
		t.Fatalf("replacement reused the dead owner's dispatch generation: %+v", current)
	}
}

func TestLostAssignmentRecoveryCancellationAndStaleTopologyDoNotRelease(t *testing.T) {
	for _, stage := range []string{"stale topology", "canceled proof"} {
		t.Run(stage, func(t *testing.T) {
			c, store, client, claims := lostAssignmentCoordinator(t)
			before := store.Get("ntm-heartbeat")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if stage == "stale topology" {
				setCoordinatorLivePanes(c, "%97")
				c.livePaneTopologyAt = time.Now().Add(-time.Hour)
				setLostAssignmentObservation(c, func(context.Context, string) ([]tmux.PaneActivity, error) {
					return nil, errors.New("fresh topology unavailable")
				}, nil)
				if err := c.reconcileLostAssignments(ctx, store); err == nil {
					t.Fatal("stale topology authorized recovery without a fresh observation")
				}
			} else {
				c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
					cancel()
					return nil, context.Canceled
				}
				if _, err := c.RunCycle(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation was lost: %v", err)
				}
			}
			if err := store.LoadStrict(); err != nil {
				t.Fatal(err)
			}
			if after := store.Get(before.BeadID); !reflect.DeepEqual(before, after) || *claims != 0 || len(client.releaseIDs) != 0 {
				t.Fatalf("unproven owner loss mutated the assignment: %+v", after)
			}
		})
	}
}
