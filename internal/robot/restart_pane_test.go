package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRestartPaneBeadPromptTemplate(t *testing.T) {
	// Verify the template contains the expected placeholders
	if !strings.Contains(restartPaneBeadPromptTemplate, "{bead_id}") {
		t.Fatal("template missing {bead_id} placeholder")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "{bead_title}") {
		t.Fatal("template missing {bead_title} placeholder")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "AGENTS.md") {
		t.Fatal("template should reference AGENTS.md")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "Agent Mail") {
		t.Fatal("template should reference Agent Mail")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "br show") {
		t.Fatal("template should reference br show for bead details")
	}
}

func TestRestartPaneBeadPromptExpansion(t *testing.T) {
	// Test that the template expands correctly using the same replacer logic
	beadID := "bd-abc12"
	beadTitle := "Fix authentication bug"

	prompt := strings.NewReplacer(
		"{bead_id}", beadID,
		"{bead_title}", beadTitle,
	).Replace(restartPaneBeadPromptTemplate)

	if strings.Contains(prompt, "{bead_id}") {
		t.Error("prompt still contains {bead_id} placeholder after expansion")
	}
	if strings.Contains(prompt, "{bead_title}") {
		t.Error("prompt still contains {bead_title} placeholder after expansion")
	}
	if !strings.Contains(prompt, beadID) {
		t.Errorf("prompt should contain bead ID %q", beadID)
	}
	if !strings.Contains(prompt, beadTitle) {
		t.Errorf("prompt should contain bead title %q", beadTitle)
	}
	// The bead_id should appear multiple times (in work-on and br show)
	if strings.Count(prompt, beadID) < 2 {
		t.Errorf("bead ID should appear at least twice in prompt (work-on + br show), got %d", strings.Count(prompt, beadID))
	}
}

func TestValidateRestartBeadTargetsRequiresOneAgentPane(t *testing.T) {
	tests := []struct {
		name    string
		targets []tmux.Pane
		want    string
	}{
		{name: "none", targets: nil, want: "exactly one"},
		{name: "multiple", targets: []tmux.Pane{{ID: "%1", Type: tmux.AgentCodex}, {ID: "%2", Type: tmux.AgentClaude}}, want: "exactly one"},
		{name: "user", targets: []tmux.Pane{{ID: "%1", Type: tmux.AgentUser}}, want: "not an agent pane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRestartBeadTargets(test.targets)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateRestartBeadTargets() error = %v, want containing %q", err, test.want)
			}
		})
	}
	if err := validateRestartBeadTargets([]tmux.Pane{{ID: "%7", Index: 2, Type: tmux.AgentCodex}}); err != nil {
		t.Fatalf("validateRestartBeadTargets(valid) error = %v", err)
	}
}

func TestPreflightRestartBeadOrdersPolicyPlanAndLiveEligibilityBeforeMutationPorts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "restart-preflight"
		beadID  = "ntm-restart-1"
	)
	pane := tmux.Pane{ID: "%7", Index: 2, WindowIndex: 0, Title: session + "__cod_1", Type: tmux.AgentCodex}
	var calls []string
	deps := RestartPaneDependencies{
		LoadAssignmentPolicy: func(projectDir, configPath string, require bool) (*config.Config, error) {
			calls = append(calls, "policy:"+projectDir+":"+configPath)
			if projectDir != "/authoritative/project" || configPath != "/selected/config.toml" || !require {
				t.Fatalf("policy scope = %q %q %t", projectDir, configPath, require)
			}
			return config.Default(), nil
		},
		FetchActionable: func(_ context.Context, projectDir string, limit int) ([]bv.TriageRecommendation, error) {
			calls = append(calls, "plan:"+projectDir)
			if limit != 0 {
				t.Fatalf("actionable limit = %d, want uncapped", limit)
			}
			return []bv.TriageRecommendation{{ID: beadID, Title: "Restart safely", Status: "open", Priority: 1}}, nil
		},
		AssignmentLedgerExists: func(got string) (bool, error) {
			calls = append(calls, "ledger-probe:"+got)
			return false, nil
		},
		FetchBeadDetails: func(_ context.Context, projectDir, got string) (*bv.BeadAssignmentDetails, error) {
			calls = append(calls, "live:"+projectDir+":"+got)
			return &bv.BeadAssignmentDetails{ID: beadID, Title: "Restart safely", Status: "open", Priority: 1}, nil
		},
		LoadStore: func(got string) (*assignment.AssignmentStore, error) {
			calls = append(calls, "store:"+got)
			return assignment.NewStore(got), nil
		},
		NewIdempotencyKey: func() (string, error) {
			calls = append(calls, "key")
			return "restart-preflight-key", nil
		},
		ListPanes: func(_ context.Context, got string) ([]tmux.Pane, error) {
			calls = append(calls, "pane-preflight:"+got)
			return []tmux.Pane{pane}, nil
		},
	}
	preflight, err := preflightRestartBead(t.Context(), RestartPaneOptions{
		Session: session, Bead: beadID, ProjectDir: "/authoritative/project",
		ConfigPath: "/selected/config.toml", RequireConfig: true,
	}, pane, restartPaneDeps(&deps))
	if err != nil {
		t.Fatalf("preflightRestartBead() error = %v", err)
	}
	wantCalls := []string{
		"policy:/authoritative/project:/selected/config.toml",
		"plan:/authoritative/project",
		"live:/authoritative/project:" + beadID,
		"ledger-probe:" + session,
		"store:" + session,
		"key",
		"pane-preflight:" + session,
	}
	if fmt.Sprint(calls) != fmt.Sprint(wantCalls) {
		t.Fatalf("preflight call order = %v, want %v", calls, wantCalls)
	}
	if preflight.Request.BeadID != beadID || preflight.Request.Target != "%7" || preflight.Request.OccupancyKey != "%7" ||
		preflight.Request.Pane != 2 || preflight.Request.AgentType != "codex" ||
		preflight.Request.AgentName != session+"-pane-7" || preflight.Request.Actor != session+"-pane-7" ||
		preflight.Request.IdempotencyKey != "restart-preflight-key" || preflight.Coordinator == nil || preflight.Store == nil {
		t.Fatalf("atomic restart request = %+v preflight=%+v", preflight.Request, preflight)
	}
	if !strings.Contains(preflight.Prompt, beadID) || !strings.Contains(preflight.Prompt, "Restart safely") {
		t.Fatalf("restart prompt = %q", preflight.Prompt)
	}
}

func TestPreflightRestartBeadPolicyAndPlanFailuresDoNotReachMutationPorts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "ntm-restart-fail"
	pane := tmux.Pane{ID: "%3", Index: 1, Type: tmux.AgentCodex, Title: "restart__cod_1"}
	tests := []struct {
		name       string
		policyErr  error
		plan       []bv.TriageRecommendation
		planErr    error
		want       string
		wantLedger int
		wantLive   int
	}{
		{name: "policy unavailable", policyErr: errors.New("invalid selected config"), want: "policy"},
		{name: "plan unavailable", planErr: bv.ErrActionablePlanUnverified, want: "plan"},
		{name: "bead absent from verified plan", plan: []bv.TriageRecommendation{{ID: "other", Status: "open"}}, want: "absent", wantLive: 1},
		{name: "plan status changed", plan: []bv.TriageRecommendation{{ID: beadID, Status: "closed"}}, want: "status", wantLive: 1},
		{name: "plan gained blocker", plan: []bv.TriageRecommendation{{ID: beadID, Status: "open", BlockedBy: []string{"dep"}}}, want: "blocked", wantLive: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var policyCalls, planCalls, ledgerCalls, liveCalls, storeCalls, keyCalls, paneCalls int
			expectedPlanCalls := 1
			if test.policyErr != nil {
				expectedPlanCalls = 0
			}
			deps := restartPaneDeps(&RestartPaneDependencies{
				LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) {
					policyCalls++
					return config.Default(), test.policyErr
				},
				FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
					planCalls++
					return test.plan, test.planErr
				},
				AssignmentLedgerExists: func(string) (bool, error) {
					ledgerCalls++
					return false, nil
				},
				FetchBeadDetails: func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
					liveCalls++
					return &bv.BeadAssignmentDetails{ID: beadID, Title: "Failure isolation", Status: "open"}, nil
				},
				LoadStore: func(session string) (*assignment.AssignmentStore, error) {
					storeCalls++
					return assignment.NewStore(session), nil
				},
				NewIdempotencyKey: func() (string, error) { keyCalls++; return "must-not-generate", nil },
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					paneCalls++
					return []tmux.Pane{pane}, nil
				},
			})
			preflight, err := preflightRestartBead(t.Context(), RestartPaneOptions{
				Session: "restart", Bead: beadID, ProjectDir: "/authoritative/project",
			}, pane, deps)
			if preflight != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("preflight=%+v error=%v, want failure containing %q", preflight, err, test.want)
			}
			if policyCalls != 1 || planCalls != expectedPlanCalls ||
				ledgerCalls != test.wantLedger || liveCalls != test.wantLive || storeCalls != 0 || keyCalls != 0 || paneCalls != 0 {
				t.Fatalf("calls policy=%d plan=%d ledger=%d live=%d store=%d key=%d pane=%d", policyCalls, planCalls, ledgerCalls, liveCalls, storeCalls, keyCalls, paneCalls)
			}
		})
	}
}

func TestPreflightRestartBeadDryRunValidatesDurableLedgerConflicts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "restart-dry-run-ledger"
		beadID  = "ntm-restart-dry-run"
		title   = "Preview restart safely"
	)
	pane := tmux.Pane{ID: "%7", Index: 2, WindowIndex: 0, Title: session + "__cod_1", Type: tmux.AgentCodex}
	completionDetectedAt := time.Now().UTC()

	tests := []struct {
		name string
		seed func(*assignment.AssignmentStore)
		want string
	}{
		{
			name: "occupied target",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments["ntm-other-work"] = &assignment.Assignment{
					BeadID: "ntm-other-work", Status: assignment.StatusAssigned,
					DispatchTarget: pane.ID, OccupancyKey: pane.ID,
				}
			},
			want: "already occupied",
		},
		{
			name: "conflicting active intent",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusAssigned, Pane: pane.Index + 1,
					AgentType: "codex", AgentName: "different-agent", IdempotencyKey: "different-intent",
					DispatchTarget: "%8", OccupancyKey: "%8", IntentSHA256: assignment.PromptSHA256("different prompt"),
				}
			},
			want: "different active assignment intent",
		},
		{
			name: "pending completion event",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, PendingCompletionEventID: "completion-event-1",
					CompletionDetectedAt: &completionDetectedAt,
				}
			},
			want: "unacknowledged completion event",
		},
		{
			name: "reserved state without required flag",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, ReservationState: assignment.ReservationReserved,
				}
			},
			want: "reservation receipts",
		},
		{
			name: "reserving state without required flag",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, ReservationState: assignment.ReservationReserving,
				}
			},
			want: "reservation receipts",
		},
		{
			name: "unknown reservation state without required flag",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, ReservationState: assignment.ReservationUnknown,
				}
			},
			want: "reservation receipts",
		},
		{
			name: "reservation IDs without required flag",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, ReservationIDs: []int{41},
				}
			},
			want: "reservation receipts",
		},
		{
			name: "reserved paths without required flag",
			seed: func(store *assignment.AssignmentStore) {
				store.Assignments[beadID] = &assignment.Assignment{
					BeadID: beadID, Status: assignment.StatusCompleted, ReservedPaths: []string{"internal/robot/**"},
				}
			},
			want: "reservation receipts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := assignment.NewStore(session)
			test.seed(store)
			var ledgerCalls, storeCalls, keyCalls, paneCalls int
			deps := restartPaneDeps(&RestartPaneDependencies{
				LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) {
					return config.Default(), nil
				},
				FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
					return []bv.TriageRecommendation{{ID: beadID, Title: title, Status: "open", Priority: 1}}, nil
				},
				FetchBeadDetails: func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
					return &bv.BeadAssignmentDetails{ID: beadID, Title: title, Status: "open", Priority: 1}, nil
				},
				AssignmentLedgerExists: func(got string) (bool, error) {
					ledgerCalls++
					if got != session {
						t.Fatalf("ledger session = %q, want %q", got, session)
					}
					return true, nil
				},
				LoadStore: func(string) (*assignment.AssignmentStore, error) {
					t.Fatal("dry-run used mutating assignment store loader")
					return nil, nil
				},
				LoadStoreReadOnly: func(got string) (*assignment.AssignmentStore, error) {
					storeCalls++
					if got != session {
						t.Fatalf("store session = %q, want %q", got, session)
					}
					return store, nil
				},
				NewIdempotencyKey: func() (string, error) {
					keyCalls++
					return "must-not-generate", nil
				},
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					paneCalls++
					return []tmux.Pane{pane}, nil
				},
			})

			preflight, err := preflightRestartBead(t.Context(), RestartPaneOptions{
				Session: session, Bead: beadID, ProjectDir: "/authoritative/project", DryRun: true,
			}, pane, deps)
			if preflight != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("preflight=%+v error=%v, want durable conflict containing %q", preflight, err, test.want)
			}
			if ledgerCalls != 1 || storeCalls != 1 || keyCalls != 0 || paneCalls != 0 {
				t.Fatalf("calls ledger=%d store=%d key=%d panes=%d, want 1/1/0/0", ledgerCalls, storeCalls, keyCalls, paneCalls)
			}
		})
	}
}

func TestPreflightRestartBeadDryRunHonorsCancellationAfterReadOnlyLedgerLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "restart-dry-run-canceled"
		beadID  = "ntm-restart-canceled"
	)
	pane := tmux.Pane{ID: "%7", Index: 2, Type: tmux.AgentCodex}
	ctx, cancel := context.WithCancel(t.Context())
	store := assignment.NewStore(session)
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) { return config.Default(), nil },
		FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
			return []bv.TriageRecommendation{{ID: beadID, Title: "Canceled preview", Status: "open"}}, nil
		},
		FetchBeadDetails: func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
			return &bv.BeadAssignmentDetails{ID: beadID, Title: "Canceled preview", Status: "open"}, nil
		},
		AssignmentLedgerExists: func(string) (bool, error) { return true, nil },
		LoadStoreReadOnly: func(string) (*assignment.AssignmentStore, error) {
			cancel()
			return store, nil
		},
		NewIdempotencyKey: func() (string, error) {
			t.Fatal("canceled dry-run generated assignment identity")
			return "", nil
		},
	})

	preflight, err := preflightRestartBead(ctx, RestartPaneOptions{
		Session: session, Bead: beadID, ProjectDir: "/authoritative/project", DryRun: true,
	}, pane, deps)
	if preflight != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dry-run preflight=%+v error=%v, want context.Canceled", preflight, err)
	}
}

func TestRestartMatchingAssignmentRejectsReservationEvidenceWithoutRequiredFlag(t *testing.T) {
	const prompt = "resume reserved work"
	base := assignment.Assignment{
		BeadID: "ntm-reserved-replay", Pane: 2, AgentType: "codex", AgentName: "restart-pane-7",
		DispatchTarget: "%7", OccupancyKey: "%7", IntentSHA256: assignment.PromptSHA256(prompt),
		IdempotencyKey: "restart-reserved-key",
	}
	if got := restartMatchingAssignment(&base, "%7", 2, "codex", "restart-pane-7", prompt); got == nil {
		t.Fatal("matching assignment without reservation evidence was rejected")
	}
	releasedRequired := base
	releasedRequired.ReservationRequired = true
	releasedRequired.ReservationState = assignment.ReservationReleased
	if got := restartMatchingAssignment(&releasedRequired, "%7", 2, "codex", "restart-pane-7", prompt); got == nil {
		t.Fatal("matching released assignment with sticky reservation policy was rejected")
	}
	tests := []struct {
		name string
		edit func(*assignment.Assignment)
	}{
		{name: "reserving state", edit: func(current *assignment.Assignment) { current.ReservationState = assignment.ReservationReserving }},
		{name: "reserved state", edit: func(current *assignment.Assignment) { current.ReservationState = assignment.ReservationReserved }},
		{name: "reserved error", edit: func(current *assignment.Assignment) {
			current.ReservationState = assignment.ReservationReserved
			current.ReservationCompleted = true
			current.ReservationError = "lease receipt is ambiguous"
		}},
		{name: "unknown state", edit: func(current *assignment.Assignment) { current.ReservationState = assignment.ReservationUnknown }},
		{name: "reservation IDs", edit: func(current *assignment.Assignment) { current.ReservationIDs = []int{9} }},
		{name: "reserved paths", edit: func(current *assignment.Assignment) { current.ReservedPaths = []string{"internal/robot/**"} }},
		{name: "clear in progress", edit: func(current *assignment.Assignment) {
			current.ClearState = assignment.ClearStateReservationReleasing
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := base
			test.edit(&current)
			if got := restartMatchingAssignment(&current, "%7", 2, "codex", "restart-pane-7", prompt); got != nil {
				t.Fatalf("reservation-bearing assignment was accepted: %+v", got)
			}
		})
	}
}

func TestValidateRestartStorePreflightAcceptsReleasedRequiredTerminalAssignment(t *testing.T) {
	store := assignment.NewStore("restart-released-terminal")
	store.Assignments["ntm-released"] = &assignment.Assignment{
		BeadID:              "ntm-released",
		Status:              assignment.StatusCompleted,
		ReservationRequired: true,
		ReservationState:    assignment.ReservationReleased,
	}

	if err := validateRestartStorePreflight(store, "ntm-released", "%7"); err != nil {
		t.Fatalf("released required terminal assignment was rejected: %v", err)
	}
}

func TestValidateRestartFreshDetailsRejectsEveryLiveAutomationGate(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	base := bv.BeadAssignmentDetails{ID: "ntm-gated", Title: "Gated", Status: "open"}
	tests := []struct {
		name string
		edit func(*bv.BeadAssignmentDetails)
		want string
	}{
		{name: "blocked", edit: func(d *bv.BeadAssignmentDetails) { d.BlockedBy = []string{"dep"} }, want: "blocker"},
		{name: "operator label", edit: func(d *bv.BeadAssignmentDetails) { d.Labels = []string{"operator-gated"} }, want: "operator-gated"},
		{name: "not open", edit: func(d *bv.BeadAssignmentDetails) { d.Status = "in_progress" }, want: "want open"},
		{name: "assigned", edit: func(d *bv.BeadAssignmentDetails) { d.Assignee = "owner" }, want: "already assigned"},
		{name: "deferred", edit: func(d *bv.BeadAssignmentDetails) { d.DeferUntil = &future }, want: "deferred"},
		{name: "pinned", edit: func(d *bv.BeadAssignmentDetails) { d.Pinned = true }, want: "pinned"},
		{name: "ephemeral", edit: func(d *bv.BeadAssignmentDetails) { d.Ephemeral = true }, want: "ephemeral"},
		{name: "template", edit: func(d *bv.BeadAssignmentDetails) { d.Template = true }, want: "template"},
		{name: "wisp", edit: func(d *bv.BeadAssignmentDetails) { d.Wisp = true }, want: "wisp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			details := base
			test.edit(&details)
			err := validateRestartFreshDetails(&details, now)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("validateRestartFreshDetails() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRestartAssignmentEligibilityPortRechecksAuthoritativeLiveState(t *testing.T) {
	const (
		projectDir = "/authoritative/project"
		beadID     = "ntm-final-live-check"
	)
	var fetchCalls int
	port := newRestartAssignmentEligibilityPort(projectDir, nil, func(_ context.Context, gotProject, gotBead string) (*bv.BeadAssignmentDetails, error) {
		fetchCalls++
		if gotProject != projectDir || gotBead != beadID {
			t.Fatalf("final live scope = %q %q", gotProject, gotBead)
		}
		return &bv.BeadAssignmentDetails{
			ID: beadID, Title: "Final check", Status: "open", Labels: []string{"operator-gated"},
		}, nil
	})
	err := port.AuthorizeAssignment(t.Context(), assignment.AssignmentEligibilityAuthorizationRequest{
		BeadID: beadID, ClaimActor: "restart-pane/ntm-key", AllowUnassignedOpen: true,
	})
	if !errors.Is(err, assignment.ErrClaimIneligible) || fetchCalls != 1 {
		t.Fatalf("AuthorizeAssignment() error = %v calls=%d, want ErrClaimIneligible after one exact read", err, fetchCalls)
	}
}

func TestPreflightRestartBeadRecognizesExactDurableReplayWithoutNewIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "restart-replay"
		beadID  = "ntm-replay"
		title   = "Replay safely"
	)
	pane := tmux.Pane{ID: "%9", Index: 1, Type: tmux.AgentCodex, Title: session + "__cod_1"}
	prompt := buildRestartBeadPrompt(beadID, title)
	actorBase := restartPaneAssignmentActor(session, pane.ID)
	existing := &assignment.Assignment{
		BeadID: beadID, BeadTitle: title, Pane: pane.Index, AgentType: "codex", AgentName: actorBase,
		Status: assignment.StatusAssigned, IdempotencyKey: "durable-restart-key", ClaimActor: actorBase + "/ntm-durable-rest",
		ClaimState: assignment.ClaimClaimed, ClaimStatus: "in_progress", DispatchState: assignment.DispatchSent,
		DispatchTarget: pane.ID, OccupancyKey: pane.ID, IntentSHA256: assignment.PromptSHA256(prompt),
		PromptSHA256: assignment.PromptSHA256(prompt), PromptSent: prompt, DispatchReceiptID: "%9/tmux/durable-restart-key",
	}
	store := assignment.NewStore(session)
	store.Assignments[beadID] = existing
	newKeyCalls := 0
	deps := restartPaneDeps(&RestartPaneDependencies{
		LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) { return config.Default(), nil },
		FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
			return []bv.TriageRecommendation{}, nil
		},
		AssignmentLedgerExists: func(string) (bool, error) { return true, nil },
		LoadStore:              func(string) (*assignment.AssignmentStore, error) { return store, nil },
		FetchBeadDetails: func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
			return &bv.BeadAssignmentDetails{ID: beadID, Title: title, Status: "in_progress", Assignee: existing.ClaimActor}, nil
		},
		NewIdempotencyKey: func() (string, error) { newKeyCalls++; return "wrong-new-key", nil },
		ListPanes:         func(context.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil },
	})
	preflight, err := preflightRestartBead(t.Context(), RestartPaneOptions{
		Session: session, Bead: beadID, ProjectDir: "/authoritative/project",
	}, pane, deps)
	if err != nil {
		t.Fatalf("preflightRestartBead(replay) error = %v", err)
	}
	if preflight.Recovery == nil || preflight.Request.IdempotencyKey != existing.IdempotencyKey || newKeyCalls != 0 {
		t.Fatalf("replay preflight=%+v newKeyCalls=%d", preflight, newKeyCalls)
	}
	output := &RestartPaneOutput{}
	applyRestartAssignmentReplay(output, preflight.Recovery)
	if !output.PromptSent || !output.AssignmentReplayed || output.ClaimActor != existing.ClaimActor ||
		output.IdempotencyKey != existing.IdempotencyKey || output.DispatchReceiptID != existing.DispatchReceiptID {
		t.Fatalf("replay output = %+v", output)
	}
}

func TestRestartPaneOptionsPromptOverridesBead(t *testing.T) {
	// When both Bead and Prompt are set, Prompt should take precedence.
	// This tests the logic flow: promptToSend defaults to Prompt, falling back to beadPrompt.
	opts := RestartPaneOptions{
		Session: "test-session",
		Bead:    "bd-xyz",
		Prompt:  "Custom prompt override",
	}

	// Simulate the priority logic from GetRestartPane
	promptToSend := opts.Prompt
	beadPrompt := "generated from bead"
	if promptToSend == "" && beadPrompt != "" {
		promptToSend = beadPrompt
	}

	if promptToSend != "Custom prompt override" {
		t.Errorf("explicit --prompt should override bead template, got %q", promptToSend)
	}
}

func TestRestartPaneOptionsBeadPromptFallback(t *testing.T) {
	// When only Bead is set (no Prompt), beadPrompt should be used
	opts := RestartPaneOptions{
		Session: "test-session",
		Bead:    "bd-xyz",
	}

	promptToSend := opts.Prompt
	beadPrompt := "generated from bead"
	if promptToSend == "" && beadPrompt != "" {
		promptToSend = beadPrompt
	}

	if promptToSend != "generated from bead" {
		t.Errorf("bead template should be used when no explicit prompt, got %q", promptToSend)
	}
}

func TestRestartPaneOutputBeadFields(t *testing.T) {
	// Verify the output struct carries bead assignment info
	output := RestartPaneOutput{
		BeadAssigned: "bd-abc12",
		PromptSent:   true,
	}

	if output.BeadAssigned != "bd-abc12" {
		t.Errorf("BeadAssigned = %q, want %q", output.BeadAssigned, "bd-abc12")
	}
	if !output.PromptSent {
		t.Error("PromptSent should be true")
	}
}

func TestRestartPaneOutputPromptError(t *testing.T) {
	output := RestartPaneOutput{
		BeadAssigned: "bd-abc12",
		PromptSent:   false,
		PromptError:  "pane 1: connection refused",
	}

	if output.PromptSent {
		t.Error("PromptSent should be false when there's an error")
	}
	if output.PromptError == "" {
		t.Error("PromptError should be set when prompt sending fails")
	}
}

func TestSetRestartPanePromptFailureMarksTopLevelResponseFailed(t *testing.T) {
	output := &RestartPaneOutput{RobotResponse: NewRobotResponse(true), Restarted: []string{"1"}}

	setRestartPanePromptFailure(output, []string{"pane 1: send-keys failed"})

	if output.Success {
		t.Fatal("prompt delivery failure must set success=false")
	}
	if output.ErrorCode != ErrCodePromptSendFailed {
		t.Fatalf("ErrorCode = %q, want %q", output.ErrorCode, ErrCodePromptSendFailed)
	}
	if output.PromptSent || output.PromptError != "pane 1: send-keys failed" {
		t.Fatalf("prompt outcome = sent:%t error:%q", output.PromptSent, output.PromptError)
	}
	if !strings.Contains(output.Error, "did not receive") || output.Hint == "" {
		t.Fatalf("failure envelope lost error or hint: %+v", output.RobotResponse)
	}
}

func TestSetRestartPanePromptFailureIgnoresEmptyErrors(t *testing.T) {
	output := &RestartPaneOutput{RobotResponse: NewRobotResponse(true), PromptSent: true}

	setRestartPanePromptFailure(output, nil)

	if !output.Success || !output.PromptSent || output.ErrorCode != "" {
		t.Fatalf("empty errors changed response: %+v", output)
	}
}

func TestRestartPaneDryRunShowsBead(t *testing.T) {
	// In dry-run mode, BeadAssigned should still be populated
	output := RestartPaneOutput{
		DryRun:       true,
		WouldAffect:  []string{"1", "2"},
		BeadAssigned: "bd-abc12",
	}

	if output.BeadAssigned == "" {
		t.Error("BeadAssigned should be set even in dry-run mode")
	}
	if !output.DryRun {
		t.Error("DryRun should be true")
	}
}

func TestRestartPaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom title",
		Type:    tmux.AgentCodex,
		Command: "codex --model o3",
	}

	if got := restartPaneAgentType(pane); got != "codex" {
		t.Fatalf("restartPaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestSelectRestartPaneTargetsUsesParsedPaneTypeForFilters(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "notes", Type: tmux.AgentClaude, Command: "claude"},
		{ID: "%2", Index: 2, Title: "build monitor", Type: tmux.AgentCodex, Command: "codex"},
	}

	targets := selectRestartPaneTargets(panes, nil, "codex", false)
	if len(targets) != 1 {
		t.Fatalf("selectRestartPaneTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%2" {
		t.Fatalf("selectRestartPaneTargets() picked %s, want %%2", targets[0].ID)
	}
}

func TestRespawnRestartPaneTargetsRejectsGrokBatchBeforeMutation(t *testing.T) {
	tests := []struct {
		name    string
		targets []tmux.Pane
	}{
		{
			name:    "grok only",
			targets: []tmux.Pane{{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentGrok}},
		},
		{
			name: "mixed batch rejects before earlier supported pane",
			targets: []tmux.Pane{
				{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude},
				{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentGrok},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutations := 0
			output := &RestartPaneOutput{Restarted: []string{}, Failed: []RestartError{}}
			info, err := respawnRestartPaneTargetsContext(
				t.Context(),
				tt.targets,
				false,
				output,
				func(context.Context, string, bool) error {
					mutations++
					return nil
				},
				func(context.Context, string) (int, error) {
					t.Fatal("rejected batch observed a pane PID")
					return 0, nil
				},
			)
			if !errors.Is(err, agent.ErrAutomatedRelaunchNotImplemented) {
				t.Fatalf("respawnRestartPaneTargetsContext() error = %v, want Grok relaunch sentinel", err)
			}
			if mutations != 0 {
				t.Fatalf("respawn callback called %d time(s), want zero", mutations)
			}
			if len(info) != 0 || len(output.Restarted) != 0 || len(output.Failed) != 0 {
				t.Fatalf("rejected batch mutated output: info=%v output=%+v", info, output)
			}
		})
	}
}

func TestRespawnRestartPaneTargetsContextStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	output := &RestartPaneOutput{Restarted: []string{}, Failed: []RestartError{}}
	targets := []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentCodex},
		{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%3", Index: 3, WindowIndex: 0, Type: tmux.AgentGemini},
	}
	var calls int
	info, err := respawnRestartPaneTargetsContext(
		ctx,
		targets,
		false,
		output,
		func(gotCtx context.Context, target string, kill bool) error {
			calls++
			if gotCtx != ctx || target != "%1" || !kill {
				t.Fatalf("first respawn call context=%v target=%q kill=%t", gotCtx, target, kill)
			}
			cancel()
			return nil
		},
		func(gotCtx context.Context, target string) (int, error) {
			if gotCtx != ctx || target != "%1" {
				t.Fatalf("pre-respawn PID observation context=%v target=%q", gotCtx, target)
			}
			return 100, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("respawnRestartPaneTargetsContext() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("respawn callback calls = %d, want one before cancellation", calls)
	}
	if len(output.Restarted) != 1 || output.Restarted[0] != "1" || len(info) != 1 {
		t.Fatalf("partial restart details info=%v output=%+v", info, output)
	}
	if len(output.Failed) != 3 || output.Failed[0].Pane != "1" || output.Failed[1].Pane != "2" || output.Failed[2].Pane != "3" {
		t.Fatalf("canceled pending pane details = %+v", output.Failed)
	}
	for index, failure := range output.Failed {
		if !strings.Contains(failure.Reason, "canceled") {
			t.Fatalf("canceled pane failure %d=%+v", index, failure)
		}
	}
	if !strings.Contains(output.Failed[0].Reason, "post-respawn lifecycle") {
		t.Fatalf("mutated pane failure is not explicit about incomplete lifecycle: %+v", output.Failed[0])
	}
	for _, failure := range output.Failed[1:] {
		if !strings.Contains(failure.Reason, "respawn skipped") {
			t.Fatalf("unattempted pane failure is not explicit: %+v", failure)
		}
	}
}

func TestRespawnRestartPaneTargetsContextReportsMutationWhenCommandReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &RestartPaneOutput{Restarted: []string{}, Failed: []RestartError{}}
	targets := []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentCodex},
		{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentClaude},
	}
	var pidCalls int
	info, err := respawnRestartPaneTargetsContext(
		ctx,
		targets,
		false,
		output,
		func(gotCtx context.Context, target string, kill bool) error {
			if gotCtx != ctx || target != "%1" || !kill {
				t.Fatalf("respawn context=%v target=%q kill=%t", gotCtx, target, kill)
			}
			cancel()
			return context.Canceled
		},
		func(gotCtx context.Context, target string) (int, error) {
			pidCalls++
			if target != "%1" {
				t.Fatalf("PID observation target=%q, want %%1", target)
			}
			switch pidCalls {
			case 1:
				if gotCtx != ctx {
					t.Fatalf("pre-respawn PID context=%v, want caller context", gotCtx)
				}
				return 100, nil
			case 2:
				if gotCtx.Err() != nil {
					t.Fatalf("post-cancellation PID context is canceled: %v", gotCtx.Err())
				}
				if _, ok := gotCtx.Deadline(); !ok {
					t.Fatal("post-cancellation PID context has no independent deadline")
				}
				return 200, nil
			default:
				t.Fatalf("unexpected PID observation call %d", pidCalls)
				return 0, nil
			}
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("respawnRestartPaneTargetsContext() error=%v, want context.Canceled", err)
	}
	if pidCalls != 2 || !slices.Equal(output.Restarted, []string{"1"}) || len(info) != 1 {
		t.Fatalf("mutation evidence pidCalls=%d info=%v output=%+v", pidCalls, info, output)
	}
	if len(output.Failed) != 2 || output.Failed[0].Pane != "1" || output.Failed[1].Pane != "2" {
		t.Fatalf("canceled mutation failures=%+v, want mutated current plus skipped remaining", output.Failed)
	}
	if !strings.Contains(output.Failed[0].Reason, "100 to 200") ||
		!strings.Contains(output.Failed[0].Reason, "lifecycle is incomplete") {
		t.Fatalf("mutated pane failure does not preserve known actuation: %+v", output.Failed[0])
	}
	if !strings.Contains(output.Failed[1].Reason, "respawn skipped") {
		t.Fatalf("remaining pane failure does not report skipped actuation: %+v", output.Failed[1])
	}
}

func TestRestartPaneReadinessAndDelayHonorCancellation(t *testing.T) {
	t.Run("readiness capture", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var captureCalls, childCalls int
		ready, err := waitForPaneAgentReadyWithContext(
			ctx, "%7", 1234, "codex", time.Minute, time.Second,
			func(gotCtx context.Context, target string, lines int) (string, error) {
				captureCalls++
				if gotCtx != ctx || target != "%7" || lines != 50 {
					t.Fatalf("capture context=%v target=%q lines=%d", gotCtx, target, lines)
				}
				cancel()
				return "", nil
			},
			func(int) bool { childCalls++; return true },
		)
		if ready || !errors.Is(err, context.Canceled) || captureCalls != 1 || childCalls != 0 {
			t.Fatalf("readiness ready=%t err=%v captureCalls=%d childCalls=%d", ready, err, captureCalls, childCalls)
		}
	})

	t.Run("settle delay", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		started := time.Now()
		err := waitForRestartPaneDelay(ctx, time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForRestartPaneDelay() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("canceled restart delay took %s", elapsed)
		}
	})
}

func TestExecuteRestartBeadWaitsForCanonicalSafeDispatchObservation(t *testing.T) {
	const (
		session = "restart-safe-dispatch"
		paneID  = "%7"
	)
	request := assignment.AtomicRequest{BeadID: "ntm-safe", Target: paneID}
	observation := func(state statuspkg.AgentState, observedAt time.Time) statuspkg.SessionObservation {
		return statuspkg.SessionObservation{
			Session: session, ObservedAt: observedAt, Complete: true,
			Panes: []statuspkg.PaneObservation{{
				Pane: tmux.PaneRef{ID: paneID},
				Current: statuspkg.StateObservation{
					Status:     statuspkg.AgentStatus{PaneID: paneID, State: state},
					ObservedAt: observedAt, Freshness: statuspkg.FreshnessFresh, Confidence: 0.95,
				},
			}},
		}
	}

	t.Run("delayed canonical idle", func(t *testing.T) {
		var observeCalls, executeCalls int
		result, err := executeRestartBeadAfterSafeObservation(
			t.Context(), session, request, time.Second, time.Millisecond,
			func(_ context.Context, gotSession string) (statuspkg.SessionObservation, error) {
				observeCalls++
				if gotSession != session {
					t.Fatalf("observed session=%q, want %q", gotSession, session)
				}
				switch observeCalls {
				case 1:
					return statuspkg.SessionObservation{}, errors.New("transient observation failure")
				case 2:
					return observation(statuspkg.StateIdle, time.Now().Add(-statuspkg.DispatchObservationMaxAge-time.Second)), nil
				case 3:
					return observation(statuspkg.StateWorking, time.Now()), nil
				default:
					return observation(statuspkg.StateIdle, time.Now()), nil
				}
			},
			func(_ context.Context, got assignment.AtomicRequest) (assignment.AtomicResult, error) {
				executeCalls++
				if got.BeadID != request.BeadID || got.Target != paneID {
					t.Fatalf("executed request=%+v, want %+v", got, request)
				}
				return assignment.AtomicResult{Sent: true}, nil
			},
		)
		if err != nil || !result.Sent || observeCalls != 4 || executeCalls != 1 {
			t.Fatalf("result=%+v error=%v observations=%d executions=%d", result, err, observeCalls, executeCalls)
		}
	})

	t.Run("caller cancellation prevents execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var executeCalls int
		_, err := executeRestartBeadAfterSafeObservation(
			ctx, session, request, time.Minute, time.Millisecond,
			func(context.Context, string) (statuspkg.SessionObservation, error) {
				cancel()
				return observation(statuspkg.StateWorking, time.Now()), nil
			},
			func(context.Context, assignment.AtomicRequest) (assignment.AtomicResult, error) {
				executeCalls++
				return assignment.AtomicResult{}, nil
			},
		)
		if !errors.Is(err, context.Canceled) || executeCalls != 0 {
			t.Fatalf("error=%v executions=%d, want context cancellation before execution", err, executeCalls)
		}
	})

	t.Run("local timeout prevents execution", func(t *testing.T) {
		var observeCalls, executeCalls int
		_, err := executeRestartBeadAfterSafeObservation(
			t.Context(), session, request, 25*time.Millisecond, time.Millisecond,
			func(context.Context, string) (statuspkg.SessionObservation, error) {
				observeCalls++
				return observation(statuspkg.StateWorking, time.Now()), nil
			},
			func(context.Context, assignment.AtomicRequest) (assignment.AtomicResult, error) {
				executeCalls++
				return assignment.AtomicResult{}, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "did not become safe to dispatch") ||
			errors.Is(err, context.DeadlineExceeded) || observeCalls == 0 || executeCalls != 0 {
			t.Fatalf("error=%v observations=%d executions=%d", err, observeCalls, executeCalls)
		}
	})
}

func TestAppendRestartCancellationFailuresMarksEveryUnfinishedPane(t *testing.T) {
	output := &RestartPaneOutput{
		Restarted: []string{"0", "1", "2"},
		Failed:    []RestartError{{Pane: "0", Reason: "readiness canceled: context canceled"}},
	}
	appendRestartCancellationFailures(output, output.Restarted, 1, "agent readiness skipped", context.Canceled)
	if len(output.Failed) != 3 {
		t.Fatalf("cancellation failures=%+v, want current plus every remaining pane", output.Failed)
	}
	for index, pane := range []string{"0", "1", "2"} {
		if output.Failed[index].Pane != pane || !strings.Contains(output.Failed[index].Reason, "canceled") {
			t.Fatalf("cancellation failure %d=%+v", index, output.Failed[index])
		}
	}
}

func TestSendRestartPromptsUsesAgentAwareSender(t *testing.T) {
	targets := []restartPromptTarget{
		{Pane: "1", Target: "%1", AgentType: tmux.AgentCodex},
		{Pane: "2", Target: "%2", AgentType: tmux.AgentClaude},
	}

	var calls []string
	errs, canceledPanes, deliveryStatus, err := sendRestartPromptsContext(t.Context(), targets, "resume work", func(_ context.Context, target, keys string, agentType tmux.AgentType) error {
		calls = append(calls, fmt.Sprintf("%s|%s|%s", target, keys, agentType))
		return nil
	})
	if err != nil || len(errs) != 0 || len(canceledPanes) != 0 {
		t.Fatalf("sendRestartPromptsContext() errors=%v canceled=%v error=%v", errs, canceledPanes, err)
	}
	if len(calls) != 2 {
		t.Fatalf("sendRestartPromptsContext() made %d calls, want 2", len(calls))
	}
	if calls[0] != "%1|resume work|cod" {
		t.Fatalf("first call = %q, want %q", calls[0], "%1|resume work|cod")
	}
	if calls[1] != "%2|resume work|cc" {
		t.Fatalf("second call = %q, want %q", calls[1], "%2|resume work|cc")
	}
	if deliveryStatus["1"] != RestartPromptDelivered || deliveryStatus["2"] != RestartPromptDelivered {
		t.Fatalf("prompt delivery status=%v, want confirmed delivery for both panes", deliveryStatus)
	}
}

func TestSendRestartPromptsContextStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	targets := []restartPromptTarget{
		{Pane: "1", Target: "%1", AgentType: tmux.AgentCodex},
		{Pane: "2", Target: "%2", AgentType: tmux.AgentClaude},
		{Pane: "3", Target: "%3", AgentType: tmux.AgentGemini},
	}
	var calls int
	errs, canceledPanes, deliveryStatus, err := sendRestartPromptsContext(ctx, targets, "resume work", func(gotCtx context.Context, target, keys string, agentType tmux.AgentType) error {
		calls++
		if gotCtx != ctx || target != "%1" || keys != "resume work" || agentType != tmux.AgentCodex {
			t.Fatalf("first prompt call context=%v target=%q keys=%q type=%s", gotCtx, target, keys, agentType)
		}
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("sendRestartPromptsContext() error=%v calls=%d, want canceled after one", err, calls)
	}
	if len(errs) != 3 || !slices.Equal(canceledPanes, []string{"1", "2", "3"}) {
		t.Fatalf("canceled prompt errors=%v panes=%v", errs, canceledPanes)
	}
	for index, pane := range []string{"1", "2", "3"} {
		if !strings.Contains(errs[index], "pane "+pane) || !strings.Contains(errs[index], "canceled") {
			t.Fatalf("canceled prompt error %d=%q", index, errs[index])
		}
	}
	if deliveryStatus["1"] != RestartPromptUnknown || deliveryStatus["2"] != RestartPromptSkipped || deliveryStatus["3"] != RestartPromptSkipped {
		t.Fatalf("canceled prompt delivery status=%v, want unknown current and skipped remaining", deliveryStatus)
	}
}

func TestSendRestartPromptsContextPreservesConfirmedDeliveryWhenCancellationFollowsNilReturn(t *testing.T) {
	for _, targetCount := range []int{1, 2} {
		t.Run(fmt.Sprintf("targets_%d", targetCount), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			targets := []restartPromptTarget{{Pane: "1", Target: "%1", AgentType: tmux.AgentCodex}}
			if targetCount == 2 {
				targets = append(targets, restartPromptTarget{Pane: "2", Target: "%2", AgentType: tmux.AgentClaude})
			}
			calls := 0
			errs, canceledPanes, deliveryStatus, err := sendRestartPromptsContext(
				ctx,
				targets,
				"resume work",
				func(context.Context, string, string, tmux.AgentType) error {
					calls++
					cancel()
					return nil
				},
			)
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("send result error=%v calls=%d, want cancellation after one confirmed delivery", err, calls)
			}
			if deliveryStatus["1"] != RestartPromptDelivered {
				t.Fatalf("current delivery status=%v, want delivered", deliveryStatus)
			}
			if targetCount == 1 {
				if len(errs) != 0 || len(canceledPanes) != 0 {
					t.Fatalf("last-target cancellation errors=%v panes=%v", errs, canceledPanes)
				}
				return
			}
			if len(errs) != 1 || !slices.Equal(canceledPanes, []string{"2"}) || deliveryStatus["2"] != RestartPromptSkipped {
				t.Fatalf("remaining delivery errors=%v panes=%v status=%v", errs, canceledPanes, deliveryStatus)
			}
		})
	}
}

func TestRelaunchRestartPaneAgentContextObservesReadyAgentAfterSenderReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	info := restartPromptTarget{
		Pane:         "1",
		Target:       "%1",
		AgentType:    tmux.AgentCodex,
		ResolvedType: "codex",
	}
	mutated := false
	var pidCalls int
	outcome, phase, err := relaunchRestartPaneAgentContext(
		ctx,
		info,
		"codex",
		time.Minute,
		func(gotCtx context.Context, target, command string, enter bool, agentType tmux.AgentType) error {
			if gotCtx != ctx || target != "%1" || command != "codex" || !enter || agentType != tmux.AgentCodex {
				t.Fatalf("launch context=%v target=%q command=%q enter=%t type=%s", gotCtx, target, command, enter, agentType)
			}
			mutated = true
			cancel()
			return context.Canceled
		},
		func(gotCtx context.Context, target string) (int, error) {
			pidCalls++
			if target != "%1" {
				t.Fatalf("PID target=%q, want %%1", target)
			}
			if pidCalls == 1 {
				if gotCtx != ctx {
					t.Fatalf("pre-launch PID context=%v, want caller context", gotCtx)
				}
				return 100, nil
			}
			if gotCtx.Err() != nil {
				t.Fatalf("post-cancellation PID context is canceled: %v", gotCtx.Err())
			}
			if _, ok := gotCtx.Deadline(); !ok {
				t.Fatal("post-cancellation PID context has no deadline")
			}
			return 100, nil
		},
		func(gotCtx context.Context, target string, shellPID int, agentType string, timeout time.Duration) (bool, error) {
			if !mutated || gotCtx.Err() != nil || target != "%1" || shellPID != 100 || agentType != "codex" || timeout != restartPaneMutationObservationTimeout {
				t.Fatalf("readiness mutated=%t contextErr=%v target=%q pid=%d type=%q timeout=%s", mutated, gotCtx.Err(), target, shellPID, agentType, timeout)
			}
			return true, nil
		},
		func(pid int) bool { return mutated && pid == 100 },
	)
	if !errors.Is(err, context.Canceled) || phase != "launch" || pidCalls != 2 {
		t.Fatalf("relaunch error=%v phase=%q pidCalls=%d", err, phase, pidCalls)
	}
	if outcome.Status != RestartAgentRelaunchReady || !outcome.Ready || !outcome.ProcessAlive || outcome.ShellPID != 100 {
		t.Fatalf("post-cancellation relaunch outcome=%+v, want confirmed ready agent", outcome)
	}
	reason := formatRestartAgentLifecycleError(phase, err, outcome)
	if !strings.Contains(reason, "became ready") || !strings.Contains(reason, "lifecycle is incomplete") {
		t.Fatalf("post-cancellation relaunch reason=%q", reason)
	}
}

func TestApplyRestartAtomicResultMapsCancellationToTimeout(t *testing.T) {
	output := &RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Restarted:     []string{"0"},
		Failed:        []RestartError{{Pane: "0", Reason: "context canceled"}},
	}
	applyRestartAtomicResult(output, assignment.AtomicResult{}, context.Canceled)
	if output.Success || output.ErrorCode != ErrCodeTimeout || output.PromptSent ||
		!strings.Contains(output.Error, "atomic assignment") || !strings.Contains(output.PromptError, "canceled") {
		t.Fatalf("canceled atomic restart output = %+v", output)
	}
	if len(output.Restarted) != 1 || output.Restarted[0] != "0" || len(output.Failed) != 1 {
		t.Fatalf("cancellation lost partial restart details: %+v", output)
	}
}

func TestSetRestartPaneCancellationPreservesConfirmedOrdinaryPromptDelivery(t *testing.T) {
	output := &RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		PromptSent:    true,
		PromptDelivery: map[string]RestartPromptDeliveryStatus{
			"1": RestartPromptDelivered,
		},
	}
	setRestartPaneCancellation(output, context.Canceled, "restart canceled after prompt delivery")
	if output.Success || output.ErrorCode != ErrCodeTimeout || !output.PromptSent || output.PromptDelivery["1"] != RestartPromptDelivered {
		t.Fatalf("confirmed delivery was lost during aggregate cancellation: %+v", output)
	}
}

func TestRestartPaneOutputJSONFields(t *testing.T) {
	output := RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		RestartedAt:   time.Date(2026, 1, 28, 12, 0, 0, 0, time.UTC),
		Restarted:     []string{"1", "2"},
		Failed:        []RestartError{{Pane: "3", Reason: "timeout"}},
		BeadAssigned:  "bd-test1",
		PromptSent:    true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Check that bead_assigned and prompt_sent are present
	if parsed["bead_assigned"] != "bd-test1" {
		t.Errorf("bead_assigned = %v, want %q", parsed["bead_assigned"], "bd-test1")
	}
	if parsed["prompt_sent"] != true {
		t.Errorf("prompt_sent = %v, want true", parsed["prompt_sent"])
	}
	if parsed["session"] != "myproject" {
		t.Errorf("session = %v, want %q", parsed["session"], "myproject")
	}
}

func TestRestartPaneOutputJSONOmitEmpty(t *testing.T) {
	// When no bead is used, bead fields should be omitted from JSON
	output := RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		RestartedAt:   time.Now().UTC(),
		Restarted:     []string{"1"},
		Failed:        []RestartError{},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, "bead_assigned") {
		t.Error("bead_assigned should be omitted when empty")
	}
	if strings.Contains(jsonStr, "prompt_error") {
		t.Error("prompt_error should be omitted when empty")
	}
}

func TestRestartPaneOptionsDefaults(t *testing.T) {
	opts := RestartPaneOptions{
		Session: "test-session",
	}

	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
	if opts.All {
		t.Error("All should default to false")
	}
	if opts.Bead != "" {
		t.Error("Bead should default to empty")
	}
	if opts.Prompt != "" {
		t.Error("Prompt should default to empty")
	}
	if len(opts.Panes) != 0 {
		t.Error("Panes should default to empty")
	}
	if opts.Config != nil {
		t.Error("Config should default to nil")
	}
}

func TestRestartPaneOptionsAllFieldsSet(t *testing.T) {
	effectiveConfig := config.Default()
	opts := RestartPaneOptions{
		Session: "proj",
		Panes:   []string{"1", "2", "3"},
		Type:    "cc",
		All:     true,
		DryRun:  true,
		Bead:    "bd-abc12",
		Prompt:  "Work on this task",
		Config:  effectiveConfig,
	}

	if opts.Session != "proj" {
		t.Error("Session mismatch")
	}
	if len(opts.Panes) != 3 {
		t.Error("Panes length mismatch")
	}
	if opts.Type != "cc" {
		t.Error("Type mismatch")
	}
	if !opts.All {
		t.Error("All should be true")
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
	if opts.Bead != "bd-abc12" {
		t.Error("Bead mismatch")
	}
	if opts.Prompt != "Work on this task" {
		t.Error("Prompt mismatch")
	}
	if opts.Config != effectiveConfig {
		t.Error("Config mismatch")
	}
}

func TestRestartErrorStructure(t *testing.T) {
	re := RestartError{
		Pane:   "2",
		Reason: "failed to respawn: pane not found",
	}

	data, err := json.Marshal(re)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["pane"] != "2" {
		t.Errorf("pane = %q, want %q", parsed["pane"], "2")
	}
	if parsed["reason"] != "failed to respawn: pane not found" {
		t.Errorf("reason = %q, want proper error message", parsed["reason"])
	}
}

func TestRestartTargetIsAgent(t *testing.T) {
	tests := []struct {
		resolvedType string
		want         bool
	}{
		{"claude", true},
		{"codex", true},
		{"gemini", true},
		{"antigravity", true},
		{"grok", true},
		{"oc", true},
		{"ollama", true},
		{"user", false},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := restartTargetIsAgent(tt.resolvedType); got != tt.want {
			t.Errorf("restartTargetIsAgent(%q) = %v, want %v", tt.resolvedType, got, tt.want)
		}
	}
}

func TestRestartAgentLaunchCommandNilConfigFallsBackToAlias(t *testing.T) {
	// Without a config the canonical launch alias must be used (#187).
	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "cc"},
		{"codex", "cod"},
		{"gemini", "gmi"},
		{"antigravity", "agy"},
	}

	for _, tt := range tests {
		if got := restartAgentLaunchCommand(nil, tt.agentType, ""); got != tt.want {
			t.Errorf("restartAgentLaunchCommand(nil, %q) = %q, want %q", tt.agentType, got, tt.want)
		}
	}
}

func TestRestartAgentLaunchCommandGrokNeverFallsBackToClaude(t *testing.T) {
	if got := restartAgentLaunchCommand(nil, "grok", ""); got != "" {
		t.Fatalf("restartAgentLaunchCommand(nil, grok) = %q, want empty unsupported command", got)
	}
	if got := restartLaunchAlias("grok-build"); got != "" {
		t.Fatalf("restartLaunchAlias(grok-build) = %q, want empty unsupported command", got)
	}
}

func TestRestartAgentLaunchCommandUsesConfiguredCommand(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Claude = "claude --dangerously-skip-permissions"
	cfg.Agents.Codex = "codex --yolo"

	if got := restartAgentLaunchCommand(cfg, "claude", ""); got != "claude --dangerously-skip-permissions" {
		t.Errorf("restartAgentLaunchCommand(cfg, claude) = %q, want configured command", got)
	}
	if got := restartAgentLaunchCommand(cfg, "codex", ""); got != "codex --yolo" {
		t.Errorf("restartAgentLaunchCommand(cfg, codex) = %q, want configured command", got)
	}
	// Unconfigured type falls back to the alias.
	if got := restartAgentLaunchCommand(cfg, "gemini", ""); got != "gmi" {
		t.Errorf("restartAgentLaunchCommand(cfg, gemini) = %q, want %q", got, "gmi")
	}
}

func TestRestartAgentLaunchCommandRendersTemplate(t *testing.T) {
	cfg := &config.Config{}
	// Template with optional fields renders with empty vars — same as the
	// robot-spawn pattern (spawn.go getAgentCommands).
	cfg.Agents.Claude = "claude {{.Model}}"

	if got := restartAgentLaunchCommand(cfg, "claude", ""); got != "claude" {
		t.Errorf("restartAgentLaunchCommand template render = %q, want %q", got, "claude")
	}
}

func TestRestartAgentLaunchCommandInvalidTemplateFallsBack(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Claude = "claude {{.Broken"

	if got := restartAgentLaunchCommand(cfg, "claude", ""); got != "cc" {
		t.Errorf("restartAgentLaunchCommand invalid template = %q, want fallback %q", got, "cc")
	}
}

func TestRestartAgentLaunchCommandRejectsControlCharacters(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Claude = "claude\nrm -x"

	if got := restartAgentLaunchCommand(cfg, "claude", ""); got != "cc" {
		t.Errorf("restartAgentLaunchCommand control chars = %q, want fallback %q", got, "cc")
	}
}

func TestRestartPaneOutputAgentRelaunchedJSON(t *testing.T) {
	output := RestartPaneOutput{
		RobotResponse:       NewRobotResponse(true),
		Session:             "myproject",
		Restarted:           []string{"1"},
		Failed:              []RestartError{},
		AgentRelaunched:     map[string]bool{"1": true},
		AgentRelaunchStatus: map[string]RestartAgentRelaunchStatus{"1": RestartAgentRelaunchReady},
		PromptDelivery:      map[string]RestartPromptDeliveryStatus{"1": RestartPromptUnknown},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	relaunched, ok := parsed["agent_relaunched"].(map[string]interface{})
	if !ok {
		t.Fatalf("agent_relaunched missing or wrong type: %v", parsed["agent_relaunched"])
	}
	if relaunched["1"] != true {
		t.Errorf("agent_relaunched[1] = %v, want true", relaunched["1"])
	}
	if status, ok := parsed["agent_relaunch_status"].(map[string]interface{}); !ok || status["1"] != string(RestartAgentRelaunchReady) {
		t.Fatalf("agent_relaunch_status=%v, want ready", parsed["agent_relaunch_status"])
	}
	if status, ok := parsed["prompt_delivery"].(map[string]interface{}); !ok || status["1"] != string(RestartPromptUnknown) {
		t.Fatalf("prompt_delivery=%v, want unknown", parsed["prompt_delivery"])
	}

	// And omitted when empty (e.g., only user panes were restarted).
	output.AgentRelaunched = nil
	output.AgentRelaunchStatus = nil
	output.PromptDelivery = nil
	data, err = json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if strings.Contains(string(data), "agent_relaunched") {
		t.Error("agent_relaunched should be omitted when empty")
	}
}

func TestRestartPaneBeadPromptTemplateMatchesBulkAssign(t *testing.T) {
	// The restart-pane bead template should be compatible with the bulk-assign template.
	// Both should include: AGENTS.md reference, Agent Mail, bead ID, br show, ultrathink.
	template := restartPaneBeadPromptTemplate

	expectedParts := []string{
		"AGENTS.md",
		"Agent Mail",
		"{bead_id}",
		"{bead_title}",
		"br show",
		"ultrathink",
	}

	for _, part := range expectedParts {
		if !strings.Contains(template, part) {
			t.Errorf("template missing expected part %q", part)
		}
	}
}

func TestRestartPanePromptOnlyNoBeadAssigned(t *testing.T) {
	// When --prompt is used without --bead, BeadAssigned should be empty
	output := RestartPaneOutput{
		Restarted:  []string{"1"},
		PromptSent: true,
	}

	if output.BeadAssigned != "" {
		t.Error("BeadAssigned should be empty when only --prompt is used")
	}
	if !output.PromptSent {
		t.Error("PromptSent should be true")
	}
}

func TestRestartAgentLaunchCommandPreservesModelPin(t *testing.T) {
	// #223: restarting a pane pinned to a specific model must not silently
	// come back as the bare account-default command. The pin is recovered
	// from the pane-title variant via the configured model alias table.
	cfg := &config.Config{}
	cfg.Agents.Claude = "claude --dangerously-skip-permissions{{if .Model}} --model {{shellQuote .Model}}{{end}}"
	cfg.Models.Claude = map[string]string{"opus": "claude-opus-4-6"}

	got := restartAgentLaunchCommand(cfg, "claude", "opus")
	want := "claude --dangerously-skip-permissions --model 'claude-opus-4-6'"
	if got != want {
		t.Errorf("restartAgentLaunchCommand(cfg, claude, opus) = %q, want %q", got, want)
	}

	// A full model name from the alias table is honored as-is (case-insensitive).
	got = restartAgentLaunchCommand(cfg, "claude", "Claude-Opus-4-6")
	if got != want {
		t.Errorf("restartAgentLaunchCommand full-name variant = %q, want %q", got, want)
	}

	// An unknown variant (e.g. a persona name in the same title slot) must
	// not be guessed into a bogus --model value.
	got = restartAgentLaunchCommand(cfg, "claude", "architect")
	want = "claude --dangerously-skip-permissions"
	if got != want {
		t.Errorf("restartAgentLaunchCommand persona variant = %q, want %q", got, want)
	}

	// No variant keeps the exact pre-#223 behavior.
	got = restartAgentLaunchCommand(cfg, "claude", "")
	if got != want {
		t.Errorf("restartAgentLaunchCommand empty variant = %q, want %q", got, want)
	}
}

func TestRestartPaneMultiplePanesPromptErrors(t *testing.T) {
	// Test that prompt errors for multiple panes are joined with semicolons
	errors := []string{
		"pane 1: connection refused",
		"pane 3: timeout",
	}
	joined := strings.Join(errors, "; ")

	if !strings.Contains(joined, "pane 1") {
		t.Error("should contain first pane error")
	}
	if !strings.Contains(joined, "pane 3") {
		t.Error("should contain second pane error")
	}
	if strings.Count(joined, "; ") != 1 {
		t.Errorf("expected 1 semicolon separator, got %d", strings.Count(joined, "; "))
	}
}

// A hard-pinned agent type must never be relaunched with an empty --model. agy's
// template injects --model unconditionally, so an empty value renders
// `--model ”` and the pane dies at the ready gate instead of restarting.
func TestRestartModelVarsKeepsPinnedAndAliasedModels(t *testing.T) {
	cfg := config.Default()

	for _, agentType := range []string{"antigravity", "agy"} {
		vars := restartModelVars(cfg, agentType, "")
		if vars.Model != config.AntigravityRequiredModel {
			t.Fatalf("restartModelVars(%q, \"\").Model = %q, want the pinned %q",
				agentType, vars.Model, config.AntigravityRequiredModel)
		}
	}

	// An unrecognized variant (a persona name shares this title slot) must still
	// keep the pin rather than falling back to an empty model.
	if got := restartModelVars(cfg, "agy", "reviewer-persona").Model; got != config.AntigravityRequiredModel {
		t.Fatalf("unknown variant dropped the pin: Model = %q", got)
	}

	// Alias tables must resolve through short agent-type forms too.
	cfg.Models.Opencode = map[string]string{"fast": "anthropic/claude-haiku"}
	vars := restartModelVars(cfg, "oc", "fast")
	if vars.Model != "anthropic/claude-haiku" {
		t.Fatalf("restartModelVars(\"oc\", \"fast\").Model = %q, want the aliased model", vars.Model)
	}
	if !vars.ModelRequested || vars.ModelAlias != "fast" {
		t.Fatalf("alias metadata lost: requested=%t alias=%q", vars.ModelRequested, vars.ModelAlias)
	}
}

// Robot spawn must render every agent type's default model, not only grok's.
func TestGetAgentCommandsRendersPinnedAgyModel(t *testing.T) {
	commands := getAgentCommands(config.Default())

	agy, ok := commands["antigravity"]
	if !ok {
		t.Fatalf("no antigravity command rendered: %v", commands)
	}
	if strings.Contains(agy, "--model ''") {
		t.Fatalf("agy rendered with an empty model and would not start: %s", agy)
	}
	if !strings.Contains(agy, config.AntigravityRequiredModel) {
		t.Fatalf("agy command %q lacks the pinned model %q", agy, config.AntigravityRequiredModel)
	}
}

// Tests for the restart relaunch override (ntm-yusj).
func TestRestartAgentLaunchCommandWithOverride(t *testing.T) {
	legacyCodex := `codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.6-sol -c model_reasoning_effort=ultra`
	templateCodex := `codex -m {{.Model}} -c model_reasoning_effort={{.ReasoningEffort}}`

	cases := []struct {
		name      string
		agentType string
		codexCmd  string
		claudeCmd string
		override  restartLaunchOverride
		want      []string // substrings expected in the composed command
		wantErr   string
	}{
		{
			name:      "legacy codex config gets appended last-flag-wins override",
			agentType: "codex",
			codexCmd:  legacyCodex,
			override:  restartLaunchOverride{Model: "gpt-5.6-terra", Effort: "high"},
			want:      []string{"-m gpt-5.6-sol", "-m 'gpt-5.6-terra'", "model_reasoning_effort='high'"},
		},
		{
			name:      "template codex config renders override directly",
			agentType: "codex",
			codexCmd:  templateCodex,
			override:  restartLaunchOverride{Model: "gpt-5.6-terra", Effort: "high"},
			want:      []string{"-m gpt-5.6-terra", "model_reasoning_effort=high"},
		},
		{
			name:      "claude legacy config appends --model/--effort",
			agentType: "claude",
			claudeCmd: "claude --dangerously-skip-permissions",
			override:  restartLaunchOverride{Model: "opus-x", Effort: "max"},
			want:      []string{"claude --dangerously-skip-permissions", "--model 'opus-x'", "--effort 'max'"},
		},
		{
			name:      "raw args appended last",
			agentType: "codex",
			codexCmd:  legacyCodex,
			override:  restartLaunchOverride{Args: "--search"},
			want:      []string{legacyCodex + " --search"},
		},
		{
			name:      "unsupported type rejects model override",
			agentType: "aider",
			override:  restartLaunchOverride{Model: "whatever"},
			wantErr:   "does not support a restart model override",
		},
		{
			name:      "gemini rejects effort override",
			agentType: "gemini",
			override:  restartLaunchOverride{Model: "gemini-pro", Effort: "high"},
			wantErr:   "no reasoning-effort flag",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Agents.Codex = tc.codexCmd
			cfg.Agents.Claude = tc.claudeCmd
			got, err := restartAgentLaunchCommandWithOverride(cfg, tc.agentType, "", tc.override)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("command %q missing %q", got, want)
				}
			}
			// last-flag-wins: the override model must come after the configured one.
			if tc.override.Model != "" && strings.Contains(got, "-m gpt-5.6-sol") {
				if strings.LastIndex(got, tc.override.Model) < strings.Index(got, "gpt-5.6-sol") {
					t.Errorf("override model does not come after configured model: %q", got)
				}
			}
		})
	}
}

func TestParseRestartLaunchOverride(t *testing.T) {
	if o, err := parseRestartLaunchOverride("gpt-5.6-terra@high", "--search"); err != nil ||
		o.Model != "gpt-5.6-terra" || o.Effort != "high" || o.Args != "--search" {
		t.Fatalf("parse = %+v err=%v", o, err)
	}
	if _, err := parseRestartLaunchOverride("model@", ""); err == nil {
		t.Fatal("expected error for empty effort")
	}
	if _, err := parseRestartLaunchOverride("@high", ""); err == nil {
		t.Fatal("expected error for empty model")
	}
	if o, err := parseRestartLaunchOverride("", ""); err != nil || !o.empty() {
		t.Fatalf("empty override parse = %+v err=%v", o, err)
	}
}

// Respawn PID evidence + soft-restart detection (ntm-tgkb): a "successful"
// respawn whose shell PID did not change was NOT a restart; it must be
// reported as a failure with the PID evidence instead of counted restarted.
func TestRespawnRecordsPIDEvidenceAndDetectsSoftRestart(t *testing.T) {
	output := &RestartPaneOutput{Restarted: []string{}, Failed: []RestartError{}}
	targets := []tmux.Pane{
		{ID: "%1", Index: 1, WindowIndex: 0, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, WindowIndex: 0, Type: tmux.AgentCodex},
	}
	pids := map[string][]int{
		"%1": {100, 100}, // soft restart: PID unchanged
		"%2": {200, 201}, // real restart: PID replaced
	}
	calls := map[string]int{}
	info, err := respawnRestartPaneTargetsContext(
		t.Context(),
		targets,
		false,
		output,
		func(_ context.Context, target string, kill bool) error {
			if !kill {
				t.Fatalf("respawn without kill for %s", target)
			}
			return nil
		},
		func(_ context.Context, target string) (int, error) {
			seq := pids[target]
			idx := calls[target]
			calls[target]++
			if idx >= len(seq) {
				idx = len(seq) - 1
			}
			return seq[idx], nil
		},
	)
	if err != nil {
		t.Fatalf("respawnRestartPaneTargetsContext: %v", err)
	}
	if len(output.Restarted) != 1 || output.Restarted[0] != "2" {
		t.Fatalf("Restarted = %v, want only the truly-respawned pane 2", output.Restarted)
	}
	if _, tracked := info["1"]; tracked {
		t.Fatal("soft-restarted pane must not enter the relaunch pipeline")
	}
	if len(output.Failed) != 1 || !strings.Contains(output.Failed[0].Reason, "soft restart") {
		t.Fatalf("Failed = %+v, want one soft-restart failure", output.Failed)
	}
	if got := output.PaneShellPIDs["1"]; got.Before != 100 || got.After != 100 {
		t.Fatalf("pane 1 PID evidence = %+v, want before=100 after=100", got)
	}
	if got := output.PaneShellPIDs["2"]; got.Before != 200 || got.After != 201 {
		t.Fatalf("pane 2 PID evidence = %+v, want before=200 after=201", got)
	}
}

// bd-qs6rj: a respawn must relaunch the pane on the model and reasoning effort
// it was SPAWNED with. Recovering only known aliases meant an exact model ID
// (`--cod=8:gpt-5.6-terra:high`) fell through to the config default, and the
// effort was never recovered at all — so a whole swarm silently changed model
// and reasoning budget on any recovery, with no operator signal.
func TestRestartModelVarsRecoversExactModelAndEffort(t *testing.T) {
	cfg := config.Default()

	t.Run("exact model id and effort survive", func(t *testing.T) {
		vars := restartModelVars(cfg, "codex", "gpt-5.6-terra@high")
		if vars.Model != "gpt-5.6-terra" {
			t.Fatalf("Model = %q, want gpt-5.6-terra (an exact model ID must not fall back to the default)", vars.Model)
		}
		if vars.ReasoningEffort != "high" {
			t.Fatalf("ReasoningEffort = %q, want high", vars.ReasoningEffort)
		}
		if !vars.ModelRequested {
			t.Fatal("ModelRequested = false; the render guard would let the model be silently dropped")
		}
	})

	t.Run("a bare unknown variant is still treated as a persona, not a model", func(t *testing.T) {
		// Deliberate: spawn writes a persona name into the same field, and
		// rendering `--model 'architect'` would kill the pane at its ready
		// gate. Only the `model@effort` form proves the variant is a model.
		vars := restartModelVars(cfg, "codex", "reviewer-persona")
		if vars.Model != cfg.Models.DefaultCodex {
			t.Fatalf("Model = %q, want the default %q — a persona name must not become a model", vars.Model, cfg.Models.DefaultCodex)
		}
	})

	t.Run("configured alias still resolves to its full name", func(t *testing.T) {
		aliased := config.Default()
		aliased.Models.Codex = map[string]string{"max": "gpt-5.1-codex-max"}
		vars := restartModelVars(aliased, "codex", "max@high")
		if vars.Model != "gpt-5.1-codex-max" {
			t.Fatalf("Model = %q, want the alias target gpt-5.1-codex-max", vars.Model)
		}
		if vars.ModelAlias != "max" {
			t.Fatalf("ModelAlias = %q, want max", vars.ModelAlias)
		}
		if vars.ReasoningEffort != "high" {
			t.Fatalf("ReasoningEffort = %q, want high — an alias must not cost the effort", vars.ReasoningEffort)
		}
	})

	t.Run("no variant keeps the configured default", func(t *testing.T) {
		vars := restartModelVars(cfg, "codex", "")
		if vars.Model != cfg.Models.DefaultCodex {
			t.Fatalf("Model = %q, want the default %q", vars.Model, cfg.Models.DefaultCodex)
		}
	})
}

// A pane whose title carries a reasoning effort must still relaunch when the
// configured template cannot render one. The effort is recovered for the
// templates that CAN place it; for the rest it has to stay a quiet no-op, or
// the silent-drop guard turns every respawn of such a pane into a hard failure.
func TestRestartLaunchCommandToleratesEffortTemplateCannotRender(t *testing.T) {
	cfg := config.Default()
	// A legacy hardcoded command: no placeholders at all.
	cfg.Agents.Codex = `codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.6-sol`

	cmd, err := restartAgentLaunchCommandWithOverride(cfg, "cod", "gpt-5.6-terra@high", restartLaunchOverride{})
	if err != nil {
		t.Fatalf("respawn of an effort-carrying pane failed against a template that cannot render effort: %v", err)
	}
	if cmd == "" {
		t.Fatal("empty relaunch command")
	}
}
