package assign

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestDomainPromptTemplateAndHash(t *testing.T) {
	t.Parallel()

	impl := ExpandPromptTemplate("bd-42", "Fix router", "impl", "")
	if !strings.Contains(impl, "bd-42") || !strings.Contains(impl, "Fix router") || !strings.Contains(impl, "br dep tree bd-42") {
		t.Fatalf("unexpected impl prompt: %q", impl)
	}

	review := ExpandPromptTemplate("bd-43", "Audit auth", "review", "")
	if !strings.Contains(review, "Review and verify bead bd-43: Audit auth") {
		t.Fatalf("unexpected review prompt: %q", review)
	}

	tmplFile := filepath.Join(t.TempDir(), "custom.tmpl")
	if err := os.WriteFile(tmplFile, []byte("Custom {BEAD_ID} => {TITLE}"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	custom := ExpandPromptTemplate("bd-44", "Ship patch", "custom", tmplFile)
	if custom != "Custom bd-44 => Ship patch" {
		t.Fatalf("unexpected custom prompt: %q", custom)
	}

	if got := PromptHashSHA256("hello"); len(got) != 64 || got != PromptHashSHA256("hello") {
		t.Fatalf("unexpected SHA-256 hash: %q", got)
	}
}

func TestDomainWorkItemEligibilityAndLiveGates(t *testing.T) {
	t.Parallel()

	isEpic := func(typ string) bool { return strings.EqualFold(typ, "epic") }
	isGated := func(label string) bool { return label == "human-only" }
	active := map[string]struct{}{"bd-active": {}}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	cases := []struct {
		name       string
		item       CandidateWorkItem
		live       bool
		wantReason string
	}{
		{
			name:       "assignable open task",
			item:       CandidateWorkItem{ID: "bd-1", Title: "Fix bug", Type: "task", Status: "open"},
			wantReason: "",
		},
		{
			name:       "blocked dependency",
			item:       CandidateWorkItem{ID: "bd-2", Status: "open", BlockedBy: []string{"bd-0"}},
			wantReason: "blocked_by_dependency",
		},
		{
			name:       "container epic rejected before labels",
			item:       CandidateWorkItem{ID: "bd-3", Type: "epic", Status: "open", Labels: []string{"human-only"}},
			wantReason: "container_issue_type",
		},
		{
			name:       "operator gated label",
			item:       CandidateWorkItem{ID: "bd-4", Type: "task", Status: "open", Labels: []string{"human-only"}},
			wantReason: "operator_gated",
		},
		{
			name:       "in progress normalized",
			item:       CandidateWorkItem{ID: "bd-5", Status: "In-Progress"},
			wantReason: "already_in_progress",
		},
		{
			name:       "already assigned",
			item:       CandidateWorkItem{ID: "bd-active", Status: "ready"},
			wantReason: "already_assigned",
		},
		{
			name:       "live deferred rejected",
			item:       CandidateWorkItem{ID: "bd-6", Status: "open", DeferUntil: &future},
			live:       true,
			wantReason: "deferred",
		},
		{
			name:       "live wisp rejected",
			item:       CandidateWorkItem{ID: "bd-wisp-7", Status: "open"},
			live:       true,
			wantReason: "wisp",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var skip *SkippedBead
			if tc.live {
				skip = ClassifyLiveWorkItem(tc.item, active, isEpic, isGated, now)
			} else {
				skip = ClassifyWorkItem(tc.item, active, isEpic, isGated)
			}
			gotReason := ""
			if skip != nil {
				gotReason = skip.Reason
			}
			if gotReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

func TestDomainDependencyRelationAndCycleValidation(t *testing.T) {
	t.Parallel()

	related, unresolved := DependencyRelationToCompleted([]DependencyStatus{
		{ID: "bd-1", Status: "closed"},
		{ID: "bd-3", Status: "open"},
		{ID: "bd-2", Status: "in-progress"},
	}, "bd-1")
	if !related {
		t.Fatal("expected related=true")
	}
	if !reflect.DeepEqual(unresolved, []string{"bd-2", "bd-3"}) {
		t.Fatalf("unresolved = %v, want [bd-2 bd-3]", unresolved)
	}

	valid, warnings := ValidateCycles([][]string{
		{"bd-a", "bd-b"},
		{"bd-solo"},
		{"bd-x", "bd-x"},
	})
	if len(valid) != 1 || !IsBeadInCycle("bd-b", valid) || IsBeadInCycle("bd-solo", valid) {
		t.Fatalf("unexpected valid cycles: %v", valid)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 cycle validation warnings, got %v", warnings)
	}
}

func TestDomainPlannerScoringAndLegacyStrategies(t *testing.T) {
	t.Parallel()

	if got := InferTaskType("Fix crash on reload"); got != TaskBug {
		t.Fatalf("InferTaskType = %q, want bug", got)
	}
	if got := DetectAgentTypeFromTitle("proj__omp_2"); got != "omp" {
		t.Fatalf("DetectAgentTypeFromTitle = %q, want omp", got)
	}
	if got := DetectAgentTypeFromTitle("proj__pi_1"); got != "pi" {
		t.Fatalf("DetectAgentTypeFromTitle(pi) = %q, want pi", got)
	}
	for _, tc := range []struct {
		title string
		want  string
	}{
		{"ubuntu@compute-01: ~", "unknown"},
		{"~/components", "unknown"},
		{"prompt-user", "user"},
		{"compose-aider", "aider"},
		{"components-cursor", "cursor"},
	} {
		if got := DetectAgentTypeFromTitle(tc.title); got != tc.want {
			t.Fatalf("DetectAgentTypeFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
	if got := DetectModelFromTitle("proj__cc_1_opus"); got != "opus" {
		t.Fatalf("DetectModelFromTitle = %q, want opus", got)
	}
	if got := EstimateBeadResourceCost("Benchmark swarm load", TaskFeature); got != 0.80 {
		t.Fatalf("EstimateBeadResourceCost = %v, want 0.80", got)
	}
	if got := PressureAgentHeadroom("critical"); got != 0 {
		t.Fatalf("PressureAgentHeadroom(critical) = %d, want 0", got)
	}
	if got := EstimateAgentResourceHeadroom(0, 0.5, strings.Repeat("line\n", 400), 1); got <= 0 || got >= 1 {
		t.Fatalf("unexpected headroom %v", got)
	}

	agents := []LegacyPlanAgent{
		{Pane: tmux.Pane{ID: "%1", Index: 1, WindowIndex: 0}, AgentType: "claude", AgentName: "GreenLake"},
		{Pane: tmux.Pane{ID: "%2", Index: 2, WindowIndex: 0}, AgentType: "codex", AgentName: "BluePeak"},
	}
	beads := []LegacyPlanBead{
		{ID: "bd-1", Title: "Refactor auth layer", Priority: "P0"},
		{ID: "bd-2", Title: "Implement new endpoint", Priority: "P1"},
	}

	rr := PlanLegacyStrategy(agents, beads, LegacyPlanOptions{Strategy: "round-robin", AssignedAt: "2026-11-21T00:00:00Z"})
	if len(rr) != 2 || rr[0].PaneID != "%1" || rr[1].PaneID != "%2" {
		t.Fatalf("unexpected round-robin plan: %+v", rr)
	}

	quality := PlanLegacyStrategy(agents, beads, LegacyPlanOptions{Strategy: "quality", AssignedAt: "2026-11-21T00:00:00Z"})
	if len(quality) != 2 || quality[0].AgentType != "claude" || quality[1].AgentType != "codex" {
		t.Fatalf("unexpected quality plan: %+v", quality)
	}

	balanced := PlanLegacyStrategy(agents, beads[:1], LegacyPlanOptions{
		Strategy:      "balanced",
		AssignedAt:    "2026-11-21T00:00:00Z",
		InitialCounts: map[string]int{"%1": 2, "%2": 0},
	})
	if len(balanced) != 1 || balanced[0].PaneID != "%2" {
		t.Fatalf("balanced should pick less-loaded pane %%2, got %+v", balanced)
	}
}

func TestDomainAtomicReservationPortAndReleaseReceipt(t *testing.T) {
	t.Parallel()

	req := assignment.ReservationRequest{BeadID: "bd-9", AgentName: "BluePeak", Target: "%2"}
	lease, err := ClassifyReservationAttempt(req, &FileReservationResult{
		RequestedPaths: []string{"internal/a.go", "internal/b.go"},
		GrantedPaths:   []string{"internal/a.go"},
		ReservationIDs: []int{17},
		Conflicts:      []agentmail.ReservationConflict{{Path: "internal/b.go"}},
	}, nil)
	if !assignment.IsReservationReleaseRequired(err) || len(lease.ReservationIDs) != 1 {
		t.Fatalf("expected release-required error with partial lease, got lease=%+v err=%v", lease, err)
	}

	_, err = ClassifyReservationAttempt(req, &FileReservationResult{
		RequestedPaths: []string{"internal/b.go"},
		Conflicts:      []agentmail.ReservationConflict{{Path: "internal/b.go"}},
	}, nil)
	if !assignment.IsGuaranteedNoReservation(err) {
		t.Fatalf("expected guaranteed-no-reservation error, got %v", err)
	}

	receipt := WorkingReplacementReleaseReceipt(&assignment.Assignment{
		ReservedPaths:         []string{" internal/a.go ", "internal/a.go"},
		ReservationRequested:  []string{"internal/b.go"},
		ReservationInputPaths: []string{"internal/c.go"},
		ReservationIDs:        []int{5, 5, 0, 9},
	}, nil, nil)
	if !reflect.DeepEqual(receipt.ReleasedPaths, []string{"internal/a.go", "internal/b.go", "internal/c.go"}) {
		t.Fatalf("unexpected released paths: %v", receipt.ReleasedPaths)
	}
	if !reflect.DeepEqual(receipt.ReleasedReservationIDs, []int{5, 9}) {
		t.Fatalf("unexpected released reservation IDs: %v", receipt.ReleasedReservationIDs)
	}

	errReceipt := WorkingReplacementReleaseReceipt(&assignment.Assignment{
		ReservationIDs: []int{5},
	}, []string{" internal/old.go ", "internal/old.go"}, errors.New("boom"))
	if !reflect.DeepEqual(errReceipt.ReleasedPaths, []string{"internal/old.go"}) || len(errReceipt.ReleasedReservationIDs) != 0 {
		t.Fatalf("unexpected error release receipt: %+v", errReceipt)
	}

	// Nil manager must fail before invoking discoverPaths.
	discoverCalled := false
	nilPort := NewAtomicReservationPort(nil, "", func(context.Context, assignment.ReservationRequest) []string {
		discoverCalled = true
		return []string{"internal/x.go"}
	})
	rec, err := nilPort.ReconcileReservation(context.Background(), req, assignment.LeaseReceipt{})
	if err == nil || rec.State != assignment.ReservationReconciliationUnknown {
		t.Fatalf("nil manager ReconcileReservation = (%+v, %v), want unknown+error", rec, err)
	}
	if discoverCalled {
		t.Fatal("ReconcileReservation invoked discoverPaths even though manager is nil")
	}
}

func TestDomainPlannerDeterminismAndSingleStrategyBoost(t *testing.T) {
	t.Parallel()

	// Multi-keyword titles must score deterministically across repeated calls.
	first := CalculateMatchConfidence("claude", "Fix doc comment", "P1", "balanced")
	for i := 0; i < 100; i++ {
		if got := CalculateMatchConfidence("claude", "Fix doc comment", "P1", "balanced"); got != first {
			t.Fatalf("non-deterministic CalculateMatchConfidence on iteration %d: got %v, want %v", i, got, first)
		}
	}

	// Speed strategy must apply its boost once (not twice).
	agents := []LegacyPlanAgent{
		{Pane: tmux.Pane{ID: "%1", Index: 1}, AgentType: "codex", AgentName: "BluePeak"},
		{Pane: tmux.Pane{ID: "%2", Index: 2}, AgentType: "claude", AgentName: "GreenLake"},
	}
	speedPlan := PlanLegacyStrategy(agents, []LegacyPlanBead{
		{ID: "bd-s1", Title: "Refactor router", Priority: "P2"},
	}, LegacyPlanOptions{Strategy: "speed", AssignedAt: "2026-11-21T00:00:00Z"})
	wantSpeed := CalculateMatchConfidence("codex", "Refactor router", "P2", "speed")
	if len(speedPlan) != 1 || speedPlan[0].Score != wantSpeed {
		t.Fatalf("speed plan score = %+v, want %v (no double-boost)", speedPlan, wantSpeed)
	}

	// Dependency strategy on P1 must not double-boost a 0.8 agent to tie a 0.9 specialist.
	depAgents := []LegacyPlanAgent{
		{Pane: tmux.Pane{ID: "%1", Index: 1}, AgentType: "claude", AgentName: "GreenLake"}, // feature: 0.8 -> +0.1 = 0.90
		{Pane: tmux.Pane{ID: "%2", Index: 2}, AgentType: "codex", AgentName: "BluePeak"},   // feature: 0.9 -> +0.1 = 0.95
	}
	depPlan := PlanLegacyStrategy(depAgents, []LegacyPlanBead{
		{ID: "bd-d1", Title: "Implement new scheduler", Priority: "P1"},
	}, LegacyPlanOptions{Strategy: "dependency", AssignedAt: "2026-11-21T00:00:00Z"})
	if len(depPlan) != 1 || depPlan[0].AgentType != "codex" {
		t.Fatalf("dependency strategy should pick codex specialist (0.95 vs 0.90), got %+v", depPlan)
	}
}
