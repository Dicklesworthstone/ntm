package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestGetBulkAssignRejectsInvalidStrategyBeforeExternalWork(t *testing.T) {
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{Session: "proj", FromBV: true, Strategy: "fastest"})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeInvalidFlag || output.Assignments == nil {
		t.Fatalf("invalid strategy output = %+v", output)
	}
	assertBulkAssignRequiredCollections(t, output)
}

func assertBulkAssignRequiredCollections(t *testing.T, output *BulkAssignOutput) {
	t.Helper()
	payload, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal bulk assignment output: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatalf("decode bulk assignment output: %v", err)
	}
	for _, field := range []string{"assignments", "unassigned_beads", "unassigned_panes"} {
		value, present := document[field]
		if !present || string(value) != "[]" {
			t.Fatalf("bulk assignment field %q = %s (present=%t), want checked-empty [] in %s", field, value, present, payload)
		}
	}
}

func TestGetBulkAssignRejectsNegativeStaggerBeforeExternalWork(t *testing.T) {
	listPanesCalled := false
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			listPanesCalled = true
			return nil, errors.New("must not be called")
		},
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "proj",
		FromBV:  true,
		Stagger: -time.Millisecond,
		Deps:    &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if listPanesCalled {
		t.Fatal("negative stagger reached external pane discovery")
	}
	if output.Success || output.ErrorCode != ErrCodeInvalidFlag || output.Assignments == nil || !strings.Contains(output.Error, "non-negative") {
		t.Fatalf("negative stagger output = %+v", output)
	}
}

func TestGetBulkAssignPreservesDependencyLocalDeadline(t *testing.T) {
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}}, nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) { return t.TempDir(), nil },
		FetchTriage:    func(context.Context, string) (*bv.TriageResponse, error) { return nil, context.DeadlineExceeded },
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{Session: "proj", FromBV: true, Strategy: "impact", Deps: &deps})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeTimeout {
		t.Fatalf("dependency deadline output = %+v, want TIMEOUT", output)
	}
}

func TestBulkAssignMissingBVAndBRDependenciesAreTyped(t *testing.T) {
	base := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%1", Index: 1, Title: "proj__cod_1", Type: tmux.AgentCodex}}, nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) { return t.TempDir(), nil },
	}
	tests := []struct {
		name string
		deps BulkAssignDependencies
	}{
		{
			name: "bv",
			deps: BulkAssignDependencies{
				FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
					return nil, fmt.Errorf("triage startup: %w", bv.ErrNotInstalled)
				},
			},
		},
		{
			name: "br",
			deps: BulkAssignDependencies{
				FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) { return &bv.TriageResponse{}, nil },
				FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
					return nil, &exec.Error{Name: "br", Err: exec.ErrNotFound}
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := base
			if test.deps.FetchTriage != nil {
				deps.FetchTriage = test.deps.FetchTriage
			}
			if test.deps.FetchInProgress != nil {
				deps.FetchInProgress = test.deps.FetchInProgress
			}
			output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
				Session: "proj", FromBV: true, Strategy: "impact", Deps: &deps,
			})
			if err != nil {
				t.Fatalf("GetBulkAssign transport error: %v", err)
			}
			if output.Success || output.ErrorCode != ErrCodeDependencyMissing || strings.TrimSpace(output.Hint) == "" {
				t.Fatalf("missing %s response=%+v", test.name, output)
			}
		})
	}
}

func TestBulkAssignImpactStrategySorting(t *testing.T) {
	triage := mockTriage(nil, []bv.BlockerToClear{
		{ID: "bd-1", Title: "A", UnblocksCount: 2, Actionable: true},
		{ID: "bd-2", Title: "B", UnblocksCount: 5, Actionable: true},
		{ID: "bd-3", Title: "C", UnblocksCount: 3, Actionable: true},
	})
	panes := mockPanes("proj", []int{1, 2, 3})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "impact"}, BulkAssignDependencies{}, panes, triage, nil)

	got := []string{}
	for _, a := range plan.Assignments {
		got = append(got, a.Bead)
	}
	expected := []string{"bd-2", "bd-3", "bd-1"}

	t.Logf("strategy=impact triage blockers=%v", triage.Triage.BlockersToClear)
	t.Logf("expected order=%v actual=%v", expected, got)

	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("impact strategy order mismatch: got %v, want %v", got, expected)
	}
}

func TestBulkAssignImpactStrategySkipsNonActionableBlockersBeforeAllocation(t *testing.T) {
	triage := mockTriage(
		[]bv.TriageRecommendation{{ID: "bd-ready", Title: "Ready fallback", Status: "ready", Priority: 1}},
		[]bv.BlockerToClear{
			{ID: "bd-not-actionable", Title: "Blocked candidate", UnblocksCount: 99},
			{ID: "bd-blocked-by", Title: "Transitively blocked", UnblocksCount: 50, Actionable: true, BlockedBy: []string{"bd-parent"}},
			{ID: "bd-actionable", Title: "Safe blocker", UnblocksCount: 3, Actionable: true},
		},
	)
	panes := mockPanes("proj", []int{1})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "impact"}, BulkAssignDependencies{}, panes, triage, nil)
	if len(plan.Assignments) != 1 || plan.Assignments[0].Bead != "bd-actionable" {
		t.Fatalf("impact plan=%+v, want only actionable blocker to consume pane", plan)
	}

	triage.Triage.BlockersToClear[2].Actionable = false
	plan = planBulkAssignFromBV(BulkAssignOptions{Strategy: "impact"}, BulkAssignDependencies{}, panes, triage, nil)
	if len(plan.Assignments) != 1 || plan.Assignments[0].Bead != "bd-ready" {
		t.Fatalf("impact fallback plan=%+v, want actionable ready work", plan)
	}
}

func TestBulkAssignReadyStrategyFilters(t *testing.T) {
	recs := []bv.TriageRecommendation{
		{ID: "bd-1", Title: "Open low", Status: "open", Priority: 2},
		{ID: "bd-2", Title: "Blocked", Status: "blocked", Priority: 0},
		{ID: "bd-3", Title: "Ready high", Status: "ready", Priority: 1},
	}
	triage := mockTriage(recs, nil)
	panes := mockPanes("proj", []int{1, 2, 3})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "ready"}, BulkAssignDependencies{}, panes, triage, nil)

	got := []string{}
	for _, a := range plan.Assignments {
		got = append(got, a.Bead)
	}
	expected := []string{"bd-3", "bd-1"}

	t.Logf("strategy=ready triage recs=%v", recs)
	t.Logf("expected=%v actual=%v", expected, got)

	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("ready strategy order mismatch: got %v, want %v", got, expected)
	}
}

func TestBulkAssignStaleStrategy(t *testing.T) {
	now := time.Date(2026, 1, 20, 1, 0, 0, 0, time.UTC)
	inProgress := []bv.BeadInProgress{
		{ID: "bd-1", Title: "Recent", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "bd-2", Title: "Stale", UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "bd-3", Title: "Oldest", UpdatedAt: now.Add(-72 * time.Hour)},
		{ID: "bd-owned", Title: "Owned stale", Assignee: "ExistingAgent", UpdatedAt: now.Add(-96 * time.Hour)},
	}
	panes := mockPanes("proj", []int{1, 2, 3})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "stale"}, BulkAssignDependencies{}, panes, nil, inProgress)

	got := []string{}
	for _, a := range plan.Assignments {
		got = append(got, a.Bead)
	}
	expected := []string{"bd-3", "bd-2", "bd-1"}

	t.Logf("strategy=stale in_progress=%v", inProgress)
	t.Logf("expected=%v actual=%v", expected, got)

	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("stale strategy order mismatch: got %v, want %v", got, expected)
	}
	for _, beadID := range got {
		if beadID == "bd-owned" {
			t.Fatal("stale strategy must not plan an owner-assigned bead onto an arbitrary pane")
		}
	}
}

func TestBulkAssignStaleRecoveryPinsOwnedClaimIntentToOriginalPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "bd-stale-recovery"
	store := assignment.NewStore("stale-recovery")
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Recover committed stale claim", Pane: 2,
		AgentType: "codex", AgentName: "RecoveryAgent", Status: assignment.StatusClaiming,
		AssignedAt: time.Now().UTC(), IdempotencyKey: "stale-recovery-key",
		ClaimActor: "RecoveryAgent/ntm-stale-recove", ClaimState: assignment.ClaimUnknown,
		PendingPrompt: "persisted recovery prompt", PromptSHA256: assignment.PromptSHA256("persisted recovery prompt"),
		IntentSHA256:   assignment.PromptSHA256("original unredacted recovery prompt"),
		DispatchTarget: "%42", OccupancyKey: "%42", DispatchState: assignment.DispatchPending,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed recovery ledger: %v", err)
	}
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%41", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"},
		{Ref: tmux.PaneRef{ID: "%42", WindowIndex: 0, PaneIndex: 2}, AgentType: "codex"},
	}
	inProgress := []bv.BeadInProgress{{
		ID: beadID, Title: "Tracker title", Assignee: "RecoveryAgent/ntm-stale-recove", UpdatedAt: time.Now().UTC(),
	}}
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "stale"}, BulkAssignDependencies{}, panes, nil, inProgress, store)
	if len(plan.Assignments) != 1 {
		t.Fatalf("recovery assignments=%+v, want one", plan.Assignments)
	}
	recovered := plan.Assignments[0]
	if recovered.Bead != beadID || recovered.PaneID != "%42" || !recovered.stale || recovered.Reason != "stale_recovery" {
		t.Fatalf("recovered assignment=%+v", recovered)
	}
	if recovered.recovery == nil || recovered.recovery.IdempotencyKey != "stale-recovery-key" ||
		recovered.recovery.PendingPrompt != "persisted recovery prompt" {
		t.Fatalf("recovered durable intent=%+v", recovered.recovery)
	}
	if !reflect.DeepEqual(plan.UnassignedPanes, []string{"1"}) {
		t.Fatalf("unassigned panes=%v, want remaining pane 1", plan.UnassignedPanes)
	}
}

func TestBulkAssignBalancedStrategyMix(t *testing.T) {
	triage := mockTriage(
		[]bv.TriageRecommendation{
			{ID: "bd-r1", Title: "Ready1", Status: "ready", Priority: 1},
			{ID: "bd-r2", Title: "Ready2", Status: "ready", Priority: 2},
		},
		[]bv.BlockerToClear{
			{ID: "bd-i1", Title: "Impact1", UnblocksCount: 5, Actionable: true},
			{ID: "bd-i2", Title: "Impact2", UnblocksCount: 3, Actionable: true},
		},
	)
	inProgress := []bv.BeadInProgress{
		{ID: "bd-s1", Title: "Stale1", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "bd-s2", Title: "Stale2", UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	panes := mockPanes("proj", []int{1, 2, 3, 4, 5, 6})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "balanced"}, BulkAssignDependencies{}, panes, triage, inProgress)

	got := []string{}
	for _, a := range plan.Assignments {
		got = append(got, a.Bead)
	}
	// Expected interleaving: impact1, ready1, stale1, impact2, ready2, stale2
	expected := []string{"bd-i1", "bd-r1", "bd-s1", "bd-i2", "bd-r2", "bd-s2"}

	t.Logf("strategy=balanced expected=%v actual=%v", expected, got)
	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("balanced strategy order mismatch: got %v, want %v", got, expected)
	}
}

func TestBulkAssignMoreBeadsThanPanes(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	beads := []bulkBead{{ID: "bd-1"}, {ID: "bd-2"}, {ID: "bd-3"}}
	plan := allocateBulkAssignBeads(panes, beads)

	t.Logf("beads=%v panes=%v", beads, panes)
	if len(plan.UnassignedBeads) != 1 {
		t.Fatalf("expected 1 unassigned bead, got %d", len(plan.UnassignedBeads))
	}
	if plan.UnassignedBeads[0] != "bd-3" {
		t.Errorf("expected unassigned bead bd-3, got %v", plan.UnassignedBeads)
	}
}

func TestBulkAssignMorePanesThanBeads(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2, 3})
	beads := []bulkBead{{ID: "bd-1"}}
	plan := allocateBulkAssignBeads(panes, beads)

	t.Logf("beads=%v panes=%v", beads, panes)
	if len(plan.UnassignedPanes) != 2 {
		t.Fatalf("expected 2 unassigned panes, got %d", len(plan.UnassignedPanes))
	}
}

func TestBulkAssignExactCounts(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	beads := []bulkBead{{ID: "bd-1"}, {ID: "bd-2"}}
	plan := allocateBulkAssignBeads(panes, beads)

	t.Logf("beads=%v panes=%v", beads, panes)
	if len(plan.UnassignedBeads) != 0 || len(plan.UnassignedPanes) != 0 {
		t.Fatalf("expected no unassigned items, got beads=%v panes=%v", plan.UnassignedBeads, plan.UnassignedPanes)
	}
}

func TestBulkAssignTemplateSubstitution(t *testing.T) {
	template := "{bead_id}:{bead_title}:{bead_type}:{bead_deps}:{session}:{pane}"
	result := expandBulkAssignTemplate(template, "bd-1", "Title", "task", []string{"bd-2", "bd-3"}, "proj", "0.2")
	expected := "bd-1:Title:task:bd-2, bd-3:proj:0.2"

	t.Logf("template=%q result=%q", template, result)
	if result != expected {
		t.Fatalf("template substitution mismatch: got %q want %q", result, expected)
	}
}

func TestBulkAssignTemplateSubstitutionDefaults(t *testing.T) {
	template := "{bead_id}:{bead_type}:{bead_deps}"
	result := expandBulkAssignTemplate(template, "bd-1", "Title", "", nil, "proj", "0.2")
	expected := "bd-1:unknown:none"

	t.Logf("template=%q result=%q", template, result)
	if result != expected {
		t.Fatalf("default substitution mismatch: got %q want %q", result, expected)
	}
}

func TestBulkAssignTemplateLoadingFromFile(t *testing.T) {
	opts := BulkAssignOptions{PromptTemplatePath: "testdata/bulk_assign_template.txt"}
	deps := bulkAssignDeps(nil)
	deps.ReadFile = func(path string) ([]byte, error) {
		return os.ReadFile(path)
	}
	template, err := loadBulkAssignTemplate(opts, deps)
	if err != nil {
		t.Fatalf("loadBulkAssignTemplate failed: %v", err)
	}

	t.Logf("loaded template=%q", template)
	if !strings.Contains(template, "{bead_id}") {
		t.Fatalf("expected template to contain {bead_id}, got %q", template)
	}
}

// TestLoadBulkAssignTemplatePrecedence verifies the resolution order for the
// dispatch prompt template (#153): explicit --bulk-assign-template path beats a
// configured default file, which beats a configured inline default, which beats
// the built-in const.
func TestLoadBulkAssignTemplatePrecedence(t *testing.T) {
	readers := func(byPath map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if content, ok := byPath[path]; ok {
				return []byte(content), nil
			}
			return nil, fmt.Errorf("unexpected ReadFile(%q)", path)
		}
	}

	cases := []struct {
		name string
		opts BulkAssignOptions
		read func(string) ([]byte, error)
		want string
	}{
		{
			name: "explicit path wins over configured defaults",
			opts: BulkAssignOptions{
				PromptTemplatePath:  "explicit.txt",
				DefaultTemplatePath: "configured.txt",
				DefaultTemplate:     "inline default",
			},
			read: readers(map[string]string{"explicit.txt": "explicit template", "configured.txt": "configured template"}),
			want: "explicit template",
		},
		{
			name: "configured file wins over inline default",
			opts: BulkAssignOptions{
				DefaultTemplatePath: "configured.txt",
				DefaultTemplate:     "inline default",
			},
			read: readers(map[string]string{"configured.txt": "configured template"}),
			want: "configured template",
		},
		{
			name: "blank configured file falls through to inline default",
			opts: BulkAssignOptions{
				DefaultTemplatePath: "configured.txt",
				DefaultTemplate:     "inline default",
			},
			read: readers(map[string]string{"configured.txt": "   \n"}),
			want: "inline default",
		},
		{
			name: "inline default used when no files configured",
			opts: BulkAssignOptions{DefaultTemplate: "inline default"},
			want: "inline default",
		},
		{
			name: "built-in const when nothing configured",
			opts: BulkAssignOptions{},
			want: defaultBulkAssignTemplate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := bulkAssignDeps(nil)
			if tc.read != nil {
				deps.ReadFile = tc.read
			}
			got, err := loadBulkAssignTemplate(tc.opts, deps)
			if err != nil {
				t.Fatalf("loadBulkAssignTemplate failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("template mismatch: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestLoadBulkAssignTemplateConfiguredFileError surfaces a read error for a
// configured default file rather than silently falling back, so a misconfigured
// path is visible to the operator.
func TestLoadBulkAssignTemplateConfiguredFileError(t *testing.T) {
	deps := bulkAssignDeps(nil)
	deps.ReadFile = func(path string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	_, err := loadBulkAssignTemplate(BulkAssignOptions{DefaultTemplatePath: "missing.txt"}, deps)
	if err == nil {
		t.Fatal("expected error for unreadable configured template file, got nil")
	}
	if !strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("expected error to mention the path, got %v", err)
	}
}

func bulkTestDeliverer(t *testing.T, deliver func(dispatchsvc.Delivery) error) dispatchsvc.Deliverer {
	t.Helper()
	return dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if delivery.Protocol != dispatchsvc.ProtocolDoubleEnter {
			t.Fatalf("bulk delivery protocol = %q, want %q", delivery.Protocol, dispatchsvc.ProtocolDoubleEnter)
		}
		if delivery.Target.Ref.ID == "" {
			t.Fatal("bulk delivery omitted canonical physical pane ID")
		}
		return deliver(delivery)
	})
}

func TestBulkAssignSequentialDeliveryOrdering(t *testing.T) {
	allocation := `{"2":"bd-2","1":"bd-1"}`
	panes := mockPanes("proj", []int{1, 2})
	callOrder := []string{}
	deps := BulkAssignDependencies{
		FetchBeadTitle: func(_ context.Context, _ string, beadID string) (string, error) { return "Title " + beadID, nil },
		Cwd:            func() (string, error) { return "/tmp", nil },
		ReadFile:       func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}

	plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), panes, mustParseAllocation(t, allocation))
	output := BulkAssignOutput{Session: "proj"}
	deps.DispatchDeliverer = bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
		callOrder = append(callOrder, delivery.Target.Ref.ID)
		return nil
	})
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)

	expectedOrder := []string{"%1", "%2"}
	if !reflect.DeepEqual(callOrder, expectedOrder) {
		t.Fatalf("send order mismatch: got %v want %v", callOrder, expectedOrder)
	}

	t.Logf("expected order=%v actual order=%v", expectedOrder, callOrder)
}

func TestPlanBulkAssignFromAllocationDeduplicatesPhysicalAliases(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%10", WindowIndex: 0, PaneIndex: 0}, AgentType: "codex", Title: "proj__cod_1"},
		{Ref: tmux.PaneRef{ID: "%11", WindowIndex: 1, PaneIndex: 0}, AgentType: "claude", Title: "proj__cc_2"},
	}
	deps := bulkAssignDeps(&BulkAssignDependencies{
		FetchBeadTitle: func(_ context.Context, _ string, beadID string) (string, error) { return "Title " + beadID, nil },
	})

	plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, deps, panes, map[string]string{
		"1.0": "bd-same",
		"%11": "bd-same",
	})

	if len(plan.Assignments) != 1 {
		t.Fatalf("physical aliases produced %d assignments, want 1: %+v", len(plan.Assignments), plan.Assignments)
	}
	assignment := plan.Assignments[0]
	if assignment.Pane != "1.0" || assignment.PaneID != "%11" || assignment.Bead != "bd-same" || assignment.Status != "planned" {
		t.Fatalf("deduplicated assignment = %+v", assignment)
	}
	if !reflect.DeepEqual(plan.UnassignedPanes, []string{"0.0"}) {
		t.Fatalf("unassigned panes = %v, want [0.0]", plan.UnassignedPanes)
	}
}

func TestPlanBulkAssignFromAllocationRejectsConflictingPhysicalAliases(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%10", WindowIndex: 0, PaneIndex: 0}, AgentType: "codex", Title: "proj__cod_1"},
		{Ref: tmux.PaneRef{ID: "%11", WindowIndex: 1, PaneIndex: 0}, AgentType: "claude", Title: "proj__cc_2"},
	}
	deps := bulkAssignDeps(&BulkAssignDependencies{
		FetchBeadTitle: func(_ context.Context, _ string, beadID string) (string, error) { return "Title " + beadID, nil },
	})

	plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, deps, panes, map[string]string{
		"1.0": "bd-one",
		"%11": "bd-two",
	})

	if len(plan.Assignments) != 2 {
		t.Fatalf("conflicting aliases produced %d results, want 2 failures: %+v", len(plan.Assignments), plan.Assignments)
	}
	for _, assignment := range plan.Assignments {
		if assignment.Status != "failed" || !strings.Contains(assignment.Error, "same physical pane") {
			t.Fatalf("conflicting alias was not rejected before execution: %+v", assignment)
		}
	}
}

func TestBulkAssignAllocationSummaryUsesFilteredPaneCount(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%10", WindowIndex: 0, PaneIndex: 0}, AgentType: "codex", Title: "proj__cod_1"},
		{Ref: tmux.PaneRef{ID: "%11", WindowIndex: 0, PaneIndex: 1}, AgentType: "claude", Title: "proj__cc_2"},
	}
	for name, allocation := range map[string]map[string]string{
		"unresolvable selector": {"9": "bd-missing-pane"},
		"conflicting aliases": {
			"0.1": "bd-one",
			"%11": "bd-two",
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&BulkAssignDependencies{}), panes, allocation)
			output := &BulkAssignOutput{}

			finishBulkAssignOutput(output, plan)

			if output.Summary.TotalPanes != len(panes) {
				t.Fatalf("summary total panes = %d, want %d: assignments=%+v unassigned=%v", output.Summary.TotalPanes, len(panes), output.Assignments, output.UnassignedPanes)
			}
		})
	}
}

func TestPlanBulkAssignFromAllocationRejectsDuplicateBeadAcrossPanes(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%10", WindowIndex: 0, PaneIndex: 0}, AgentType: "codex", Title: "proj__cod_1"},
		{Ref: tmux.PaneRef{ID: "%11", WindowIndex: 0, PaneIndex: 1}, AgentType: "claude", Title: "proj__cc_2"},
	}
	deps := bulkAssignDeps(&BulkAssignDependencies{
		FetchBeadTitle: func(_ context.Context, _ string, beadID string) (string, error) { return "Title " + beadID, nil },
	})

	plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, deps, panes, map[string]string{
		"%10": "bd-duplicate",
		"%11": "bd-duplicate",
	})

	if len(plan.Assignments) != 2 || plan.failed != 2 {
		t.Fatalf("duplicate bead plan = %+v failed=%d, want two failures", plan.Assignments, plan.failed)
	}
	for _, assignment := range plan.Assignments {
		if assignment.Status != "failed" || assignment.failureCode != ErrCodeInvalidFlag || !strings.Contains(assignment.Error, "same bead") {
			t.Fatalf("duplicate bead assignment was not rejected deterministically: %+v", assignment)
		}
	}
}

func TestBulkAssignStaggerOnlyPacesBetweenAtomicDispatchAttempts(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "One"}, {ID: "bd-2", Title: "Two"}})
	var events []string
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
			events = append(events, delivery.Target.Ref.ID)
			return nil
		}),
		Wait: func(_ context.Context, delay time.Duration) error {
			events = append(events, "wait:"+delay.String())
			return nil
		},
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	output := BulkAssignOutput{Session: "proj"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{Stagger: 25 * time.Millisecond}, bulkAssignDeps(&deps), &output, plan)
	want := []string{"%1", "wait:25ms", "%2"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

func TestBulkAssignSequentialCancellationStopsLaterClaimAndDispatch(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "One"}, {ID: "bd-2", Title: "Two"}})
	ctx, cancel := context.WithCancel(t.Context())
	var sends atomic.Int32
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(dispatchsvc.Delivery) error {
			sends.Add(1)
			cancel()
			return nil
		}),
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	originalClaim := deps.ClaimBead
	var claims atomic.Int32
	deps.ClaimBead = func(ctx context.Context, dir, beadID, actor string) (bv.BeadClaimResult, error) {
		claims.Add(1)
		return originalClaim(ctx, dir, beadID, actor)
	}
	output := BulkAssignOutput{Session: "proj"}
	applyBulkAssignPlan(ctx, BulkAssignOptions{Stagger: time.Hour}, bulkAssignDeps(&deps), &output, plan)

	if got := sends.Load(); got != 1 {
		t.Fatalf("dispatches after cancellation = %d, want exactly first dispatch", got)
	}
	if got := claims.Load(); got != 1 {
		t.Fatalf("claims after cancellation = %d, want exactly first claim", got)
	}
	if len(output.Assignments) != 2 || !strings.Contains(output.Assignments[1].Error, "canceled") || output.Assignments[1].PromptSent {
		t.Fatalf("second assignment after cancellation = %+v", output.Assignments)
	}
}

func TestBulkAssignCancellationPropagatesToTriageDependency(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	inProgressCalls := atomic.Int32{}
	projectDir := t.TempDir()
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
			return projectDir, nil
		},
		FetchTriage: func(callCtx context.Context, _ string) (*bv.TriageResponse, error) {
			close(started)
			<-callCtx.Done()
			return nil, callCtx.Err()
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
			inProgressCalls.Add(1)
			return nil, nil
		},
	}
	type result struct {
		output *BulkAssignOutput
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := GetBulkAssign(ctx, BulkAssignOptions{Session: "bulk-cancel-triage", FromBV: true, Deps: &deps})
		done <- result{output: output, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bulk triage dependency did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("GetBulkAssign returned transport error: %v", got.err)
		}
		if got.output == nil || got.output.Success || got.output.ErrorCode != ErrCodeTimeout {
			t.Fatalf("canceled triage output = %+v, want structured TIMEOUT failure", got.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bulk triage dependency ignored caller cancellation")
	}
	if got := inProgressCalls.Load(); got != 0 {
		t.Fatalf("in-progress read after triage cancellation = %d", got)
	}
}

func TestBulkAssignCancellationPropagatesToLiveProjectLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		PaneCurrentPath: func(callCtx context.Context, _ string) (string, error) {
			close(started)
			<-callCtx.Done()
			return "", callCtx.Err()
		},
	}
	type result struct {
		output *BulkAssignOutput
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := GetBulkAssign(ctx, BulkAssignOptions{Session: "bulk-cancel-project", FromBV: true, Deps: &deps})
		done <- result{output: output, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bulk live-project lookup did not start")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("GetBulkAssign returned transport error: %v", got.err)
		}
		if got.output == nil || got.output.Success || got.output.ErrorCode != ErrCodeTimeout {
			t.Fatalf("canceled live-project output = %+v, want structured TIMEOUT failure", got.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bulk live-project lookup ignored caller cancellation")
	}
}

func TestBulkAssignParallelCancellationJoinsAllWorkers(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "One"}, {ID: "bd-2", Title: "Two"}})
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{}, 2)
	var active atomic.Int32
	var calls atomic.Int32
	deps := BulkAssignDependencies{
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, _ dispatchsvc.Delivery) error {
			calls.Add(1)
			active.Add(1)
			defer active.Add(-1)
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}),
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	output := BulkAssignOutput{Session: "proj"}
	done := make(chan struct{})
	go func() {
		applyBulkAssignPlan(ctx, BulkAssignOptions{Parallel: true}, bulkAssignDeps(&deps), &output, plan)
		close(done)
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("parallel bulk worker did not reach dispatch")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parallel bulk assignment did not join canceled workers")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active workers after return = %d", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("dispatch calls after joined return = %d, want 2", got)
	}
}

func TestBulkAssignUsesPaneTargetWhenAvailable(t *testing.T) {
	panes, err := filterBulkAssignPanes([]tmux.Pane{
		{ID: "%11", Index: 1, Title: "proj__cc_1"},
	}, nil)
	if err != nil {
		t.Fatalf("filter panes: %v", err)
	}
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "Title1"}})

	var gotTarget string
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
			gotTarget = delivery.Target.Ref.ID
			return nil
		}),
		ReadFile: func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	output := BulkAssignOutput{Session: "proj"}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)

	if gotTarget != "%11" {
		t.Fatalf("expected send target %%11, got %q", gotTarget)
	}
}

func TestBulkAssignUsesAgentAwareCanonicalDelivery(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%21", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"},
	}
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "Title1"}})

	var (
		gotTarget    string
		gotAgentType tmux.AgentType
	)
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
			gotTarget = delivery.Target.Ref.ID
			gotAgentType = delivery.Target.AgentType
			return nil
		}),
		ReadFile: func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	output := BulkAssignOutput{Session: "proj"}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)

	if gotTarget != "%21" {
		t.Fatalf("expected send target %%21, got %q", gotTarget)
	}
	if gotAgentType != tmux.AgentCodex {
		t.Fatalf("expected codex agent type, got %q", gotAgentType)
	}
}

func TestBulkAssignCanonicalDelivererSubmitsPrompt(t *testing.T) {
	panes := []bulkPane{
		{Ref: tmux.PaneRef{ID: "%31", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"},
	}
	plan := allocateBulkAssignBeads(panes, []bulkBead{{ID: "bd-1", Title: "Title1"}})

	var (
		gotTarget string
		gotPrompt string
		gotCalls  int
	)
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
			gotTarget = delivery.Target.Ref.ID
			gotPrompt = delivery.Message
			gotCalls++
			return nil
		}),
		ReadFile: func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	output := BulkAssignOutput{Session: "proj"}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)

	if gotCalls != 1 {
		t.Fatalf("expected canonical deliverer to be called once, got %d", gotCalls)
	}
	if gotTarget != "%31" {
		t.Fatalf("expected send target %%31, got %q", gotTarget)
	}
	if !strings.Contains(gotPrompt, "bd-1") || !strings.Contains(gotPrompt, "Title1") {
		t.Fatalf("canonical delivery prompt = %q, want bead identity and title", gotPrompt)
	}
	if output.Assignments[0].Status != "assigned" {
		t.Fatalf("expected assigned status, got %q", output.Assignments[0].Status)
	}
}

func TestBulkAssignFailedDelivery(t *testing.T) {
	panes := mockPanes("proj", []int{1, 2})
	beads := []bulkBead{{ID: "bd-1", Title: "Title1"}, {ID: "bd-2", Title: "Title2"}}
	plan := allocateBulkAssignBeads(panes, beads)

	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(delivery dispatchsvc.Delivery) error {
			if delivery.Target.Ref.ID == "%2" {
				return errors.New("send failed")
			}
			return nil
		}),
		ReadFile: func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}
	output := BulkAssignOutput{Session: "proj"}
	deps = bulkAtomicTestDeps(t, "proj", plan, deps)
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)

	if output.Summary.Failed != 1 {
		t.Fatalf("expected 1 failed assignment, got %d", output.Summary.Failed)
	}
	if output.Assignments[1].Status != "failed" {
		t.Fatalf("expected failed status, got %q", output.Assignments[1].Status)
	}

	t.Logf("output=%+v", output)
}

func TestBulkAssignDryRunSkipsPromptSend(t *testing.T) {
	panes := mockPanes("proj", []int{1})
	beads := []bulkBead{{ID: "bd-1", Title: "Title1"}}
	plan := allocateBulkAssignBeads(panes, beads)

	sent := false
	deps := BulkAssignDependencies{
		DispatchDeliverer: bulkTestDeliverer(t, func(dispatchsvc.Delivery) error {
			sent = true
			return nil
		}),
		ReadFile: func(path string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
	}

	output := BulkAssignOutput{Session: "proj"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{DryRun: true}, bulkAssignDeps(&deps), &output, plan)

	if sent {
		t.Fatal("expected no send calls in dry run")
	}
	if output.Assignments[0].PromptSent {
		t.Fatal("expected prompt_sent false in dry run")
	}

	t.Logf("dry-run output=%+v", output)
}

func TestBulkAssignAtomicOrderAndDurableReplay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore("bulk-atomic")
	plan := allocateBulkAssignBeads(
		[]bulkPane{{Ref: tmux.PaneRef{ID: "%21", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"}},
		[]bulkBead{{ID: "bd-atomic", Title: "Atomic bulk"}},
	)
	var mu sync.Mutex
	var order []string
	claimCalls := 0
	reserveCalls := 0
	deliverCalls := 0
	keyCalls := 0
	observeCalls := 0
	policyLabels := []string{"bulk-release-gate"}
	deps := BulkAssignDependencies{
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		Cwd:      func() (string, error) { return t.TempDir(), nil },
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%21", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}}, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) { return store, nil },
		GetBeadAssignmentDetails: func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, "authorize")
			return &bv.BeadAssignmentDetails{ID: beadID, Status: "open"}, nil
		},
		ClaimBeadWithOperatorGatedLabels: func(_ context.Context, _ string, beadID, actor string, labels []string) (bv.BeadClaimResult, error) {
			mu.Lock()
			defer mu.Unlock()
			claimCalls++
			order = append(order, "claim")
			if !reflect.DeepEqual(labels, policyLabels) {
				t.Fatalf("guarded claim labels=%v, want captured %v", labels, policyLabels)
			}
			return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		},
		NewIdempotencyKey: func() (string, error) {
			keyCalls++
			return "bulk-atomic-key", nil
		},
		ReservationPort: testReservationFunc(func(_ context.Context, req assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			mu.Lock()
			defer mu.Unlock()
			reserveCalls++
			order = append(order, "reserve")
			expires := time.Now().UTC().Add(time.Hour)
			return assignment.LeaseReceipt{AgentName: req.AgentName, Target: req.Target, Requested: append([]string(nil), req.RequestedPaths...), Granted: append([]string(nil), req.RequestedPaths...), ReservationIDs: []int{42}, ExpiresAt: &expires}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(_ context.Context, delivery dispatchsvc.Delivery) error {
			mu.Lock()
			defer mu.Unlock()
			deliverCalls++
			order = append(order, "dispatch")
			if delivery.Target.Ref.ID != "%21" {
				t.Errorf("delivery target=%q, want %%21", delivery.Target.Ref.ID)
			}
			return nil
		}),
		LoadRedaction: func(string) (redaction.Config, error) {
			return redaction.Config{Mode: redaction.ModeOff}, nil
		},
		ObserveSession: func(ctx context.Context, session string) (statuspkg.SessionObservation, error) {
			observeCalls++
			if observeCalls > 2 {
				t.Fatal("durable replay re-ran the fresh-idle observation gate")
			}
			return bulkSafeObserver([]tmux.Pane{{ID: "%21", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}})(ctx, session)
		},
		ResolveAgentName: func(context.Context, string, string, string, string) (string, error) {
			return "AtomicAgent", nil
		},
	}

	first := BulkAssignOutput{Session: "bulk-atomic"}
	reservationPaths := []string{"internal/robot/**"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{RequireReservation: true, ReservationPaths: reservationPaths, operatorGatedLabels: policyLabels}, bulkAssignDeps(&deps), &first, plan)
	second := BulkAssignOutput{Session: "bulk-atomic"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{RequireReservation: true, ReservationPaths: reservationPaths, operatorGatedLabels: policyLabels}, bulkAssignDeps(&deps), &second, plan)
	if len(first.Assignments) != 1 || !first.Assignments[0].Claimed || !first.Assignments[0].PromptSent || first.Assignments[0].DispatchReceiptID == "" {
		t.Fatalf("first assignments=%+v", first.Assignments)
	}
	if len(second.Assignments) != 1 || !second.Assignments[0].PromptSent || second.Assignments[0].IdempotencyKey != first.Assignments[0].IdempotencyKey {
		t.Fatalf("replayed assignments=%+v", second.Assignments)
	}
	if !reflect.DeepEqual(order, []string{"authorize", "claim", "reserve", "dispatch"}) {
		t.Fatalf("side-effect order=%v", order)
	}
	if claimCalls != 1 || reserveCalls != 1 || deliverCalls != 1 || keyCalls != 1 || observeCalls != 2 {
		t.Fatalf("calls claim=%d reserve=%d dispatch=%d key=%d observe=%d", claimCalls, reserveCalls, deliverCalls, keyCalls, observeCalls)
	}
}

func TestRobotAtomicEligibilityAuthorizationCapturesOperatorGateSnapshot(t *testing.T) {
	projectDir := t.TempDir()
	labels := []string{"release-snapshot"}
	readCalls := 0
	port := newRobotAtomicEligibilityAuthorizationPort(
		projectDir,
		labels,
		func(_ context.Context, gotProject, beadID string) (*bv.BeadAssignmentDetails, error) {
			readCalls++
			if gotProject != projectDir || beadID != "bd-gated" {
				t.Fatalf("details lookup project=%q bead=%q", gotProject, beadID)
			}
			return &bv.BeadAssignmentDetails{
				ID: beadID, Status: "open", Labels: []string{"RELEASE-SNAPSHOT"},
			}, nil
		},
	)
	labels[0] = "mutated-after-admission"

	err := port.AuthorizeAssignment(t.Context(), assignment.AssignmentEligibilityAuthorizationRequest{
		BeadID: "bd-gated", ClaimActor: "PolicyAgent", AllowUnassignedOpen: true,
	})
	if !errors.Is(err, assignment.ErrClaimIneligible) || !strings.Contains(err.Error(), "RELEASE-SNAPSHOT") {
		t.Fatalf("authorization error=%v, want captured operator-gate rejection", err)
	}
	if readCalls != 1 {
		t.Fatalf("details reads=%d, want one", readCalls)
	}
}

func TestRobotAtomicStaleEligibilityRejectsGateBeforeLedgerOrExternalMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const beadID = "bd-stale-gated"
	store := assignment.NewStore("stale-eligibility-gate")
	var claimCalls, reservationCalls, dispatchCalls int
	port := newRobotAtomicStaleEligibilityAuthorizationPort(
		t.TempDir(),
		[]string{"stale-approval"},
		func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) {
			return &bv.BeadAssignmentDetails{
				ID: beadID, Status: "in_progress", Labels: []string{"STALE-APPROVAL"},
			}, nil
		},
	)
	coordinator := assignment.NewAtomicCoordinator(
		store,
		assignment.ClaimFunc(func(context.Context, string, string) (assignment.ClaimReceipt, error) {
			claimCalls++
			return assignment.ClaimReceipt{}, nil
		}),
		testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			reservationCalls++
			return assignment.LeaseReceipt{}, nil
		}),
		assignment.DispatchFunc(func(context.Context, assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
			dispatchCalls++
			return assignment.DispatchReceipt{}, nil
		}),
	).WithAssignmentEligibilityAuthorizationPort(port)

	result, err := coordinator.Execute(t.Context(), assignment.AtomicRequest{
		BeadID: beadID, BeadTitle: "Stale gated work", Target: "%88", OccupancyKey: "%88", Pane: 1,
		AgentType: "codex", AgentName: "StaleAgent", Actor: "StaleAgent", Prompt: "work",
		IdempotencyKey: "stale-gated-key",
	})
	if !errors.Is(err, assignment.ErrClaimIneligible) || !strings.Contains(err.Error(), "STALE-APPROVAL") {
		t.Fatalf("stale authorization result=%+v error=%v, want operator-gate rejection", result, err)
	}
	if stored := store.Get(beadID); stored != nil {
		t.Fatalf("stale gate persisted assignment before authorization: %+v", stored)
	}
	if claimCalls != 0 || reservationCalls != 0 || dispatchCalls != 0 {
		t.Fatalf("stale gate crossed external mutation: claim=%d reserve=%d dispatch=%d", claimCalls, reservationCalls, dispatchCalls)
	}
}

func TestBulkAssignStalePlanUsesGuardedStaleClaimer(t *testing.T) {
	panes := []bulkPane{{Ref: tmux.PaneRef{ID: "%22", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"}}
	plan := allocateBulkAssignBeads(panes, []bulkBead{{
		ID: "bd-stale-adopt", Title: "Adopt abandoned work", Source: bulkSourceStale,
		UpdatedAt: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
	}})
	var ordinaryClaims, staleClaims int
	policyLabels := []string{"stale-release-gate"}
	deps := BulkAssignDependencies{
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		DispatchDeliverer: bulkTestDeliverer(t, func(dispatchsvc.Delivery) error {
			return nil
		}),
	}
	deps = bulkAtomicTestDeps(t, "bulk-stale-adopt", plan, deps)
	deps.ClaimBead = func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
		ordinaryClaims++
		return bv.BeadClaimResult{}, errors.New("ordinary ready-work claimer must not adopt stale work")
	}
	deps.ClaimStaleBead = func(_ context.Context, _ string, beadID, actor string, expectedUpdatedAt time.Time) (bv.BeadClaimResult, error) {
		return bv.BeadClaimResult{}, errors.New("legacy stale claimer must not be used when a captured-policy claimer is available")
	}
	deps.ClaimStaleBeadWithOperatorGatedLabels = func(_ context.Context, _ string, beadID, actor string, expectedUpdatedAt time.Time, labels []string) (bv.BeadClaimResult, error) {
		staleClaims++
		if !expectedUpdatedAt.Equal(time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("stale claim expected update=%s", expectedUpdatedAt)
		}
		if !reflect.DeepEqual(labels, policyLabels) {
			t.Fatalf("stale guarded claim labels=%v, want %v", labels, policyLabels)
		}
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}

	output := BulkAssignOutput{Session: "bulk-stale-adopt"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{operatorGatedLabels: policyLabels}, bulkAssignDeps(&deps), &output, plan)
	if ordinaryClaims != 0 || staleClaims != 1 {
		t.Fatalf("claim calls ordinary=%d stale=%d, want guarded stale claimer exactly once", ordinaryClaims, staleClaims)
	}
	if len(output.Assignments) != 1 || output.Assignments[0].Status != "assigned" || !output.Assignments[0].Claimed {
		t.Fatalf("stale assignment output=%+v", output.Assignments)
	}
}

func TestRobotAtomicPaneDispatchRevalidatesFreshIdleBeforeActuation(t *testing.T) {
	pane := tmux.Pane{ID: "%31", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}
	tests := []struct {
		name    string
		observe func(context.Context, string) (statuspkg.SessionObservation, error)
	}{
		{
			name: "observation error",
			observe: func(context.Context, string) (statuspkg.SessionObservation, error) {
				return statuspkg.SessionObservation{}, errors.New("capture failed")
			},
		},
		{
			name: "stale observation",
			observe: func(context.Context, string) (statuspkg.SessionObservation, error) {
				observation := bulkSafeObservation("dispatch-guard", []tmux.Pane{pane})
				observation.ObservedAt = time.Now().Add(-statuspkg.DispatchObservationMaxAge - time.Second)
				return observation, nil
			},
		},
		{
			name: "pane became busy",
			observe: func(context.Context, string) (statuspkg.SessionObservation, error) {
				observedAt := time.Now().UTC()
				return statuspkg.SessionObservation{
					Session: "dispatch-guard", ObservedAt: observedAt, Complete: true,
					Panes: []statuspkg.PaneObservation{{
						Pane: pane.Ref(), Metadata: pane,
						Current: statuspkg.StateObservation{
							Status:     statuspkg.AgentStatus{PaneID: pane.ID, State: statuspkg.StateWorking, UpdatedAt: observedAt},
							ObservedAt: observedAt, Freshness: statuspkg.FreshnessFresh, Confidence: 1,
						},
					}},
				}, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deliveries := 0
			port := newRobotAtomicPaneDispatchPort(
				"dispatch-guard",
				func(context.Context, string) ([]tmux.Pane, error) { return []tmux.Pane{pane}, nil },
				test.observe,
				redaction.Config{Mode: redaction.ModeOff},
				dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
					deliveries++
					return nil
				}),
				nil,
			)
			_, err := port.Dispatch(t.Context(), assignment.DispatchRequest{
				Target: pane.ID, Prompt: "work", IdempotencyKey: "dispatch-guard-key",
			})
			if err == nil || !assignment.IsGuaranteedNoActuation(err) {
				t.Fatalf("Dispatch error=%v, want guaranteed no-actuation failure", err)
			}
			if deliveries != 0 {
				t.Fatalf("deliveries=%d, want zero", deliveries)
			}
		})
	}
}

func TestRobotAtomicPreflightBlocksSensitiveReservationPathBeforeTopologyLookup(t *testing.T) {
	port := newRobotAtomicPaneDispatchPort(
		"sensitive-path", nil, nil, redaction.DefaultConfig(), nil, nil,
	)
	_, err := port.Preflight(t.Context(), assignment.DispatchRequest{
		BeadID: "ntm-sensitive-path", BeadTitle: "Safe title", Target: "%31", Prompt: "safe prompt",
		RequestedPaths: []string{"internal/" + "sk-proj-FAKEtestkey1234567890123456789012345678901234" + ".txt"},
	})
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrRedactionBlocked || !strings.Contains(err.Error(), "reservation path") {
		t.Fatalf("sensitive path preflight error=%v", err)
	}
}

func TestBulkAssignDryRunSanitizesTitlesAndBlocksSensitiveReservationPaths(t *testing.T) {
	const secret = "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	deps := bulkAssignDeps(&BulkAssignDependencies{
		LoadRedaction: func(string) (redaction.Config, error) { return redaction.DefaultConfig(), nil },
	})

	t.Run("title", func(t *testing.T) {
		output := &BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: "dry-title", DryRun: true}
		plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{{
			Pane: "1", PaneID: "%31", Bead: "bd-title", BeadTitle: "Fix " + secret, AgentType: "cod", Status: "planned",
		}}}
		applyBulkAssignPlan(t.Context(), BulkAssignOptions{DryRun: true, projectDir: "/project"}, deps, output, plan)
		if len(output.Assignments) != 1 || strings.Contains(output.Assignments[0].BeadTitle, secret) || !strings.Contains(output.Assignments[0].BeadTitle, "[REDACTED:") {
			t.Fatalf("dry-run title output=%+v", output.Assignments)
		}
	})

	t.Run("reservation path", func(t *testing.T) {
		output := &BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: "dry-path", DryRun: true}
		plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{{
			Pane: "1", PaneID: "%31", Bead: "bd-path", BeadTitle: "Safe title", AgentType: "cod", Status: "planned",
		}}}
		applyBulkAssignPlan(t.Context(), BulkAssignOptions{
			DryRun: true, RequireReservation: true, ReservationPaths: []string{"internal/" + secret + ".txt"}, projectDir: "/project",
		}, deps, output, plan)
		if output.Success || len(output.Assignments) != 1 || output.Assignments[0].Status != "failed" ||
			!strings.Contains(output.Assignments[0].Error, "reservation path") || strings.Contains(fmt.Sprint(output), secret) {
			t.Fatalf("sensitive path dry-run output=%+v", output)
		}
	})
}

func TestRobotAtomicReplayMatchesRawIntentAgainstRedactedDurablePrompt(t *testing.T) {
	store := assignment.NewStore("redacted-replay")
	rawPrompt := "token=raw-secret"
	store.Assignments["bd-redacted"] = &assignment.Assignment{
		BeadID: "bd-redacted", Status: assignment.StatusAssigned,
		Pane: 1, AgentType: "codex", AgentName: "ExactAgent",
		IdempotencyKey: "existing-key", DispatchTarget: "%44", OccupancyKey: "%44",
		DispatchState: assignment.DispatchSent, IntentSHA256: assignment.PromptSHA256(rawPrompt),
		PendingPrompt: "token=[REDACTED]", PromptSent: "token=[REDACTED]",
	}
	keyCalls := 0
	key, err := robotAtomicIdempotencyKey(
		store, "bd-redacted", "%44", 9, "codex", "ExactAgent", rawPrompt, false, nil,
		func() (string, error) { keyCalls++; return "new-key", nil },
	)
	if err != nil || key != "existing-key" || keyCalls != 0 {
		t.Fatalf("key=%q calls=%d err=%v", key, keyCalls, err)
	}
	if replay := robotAtomicReplayIntent(store, "bd-redacted", "%44", 9, "codex", rawPrompt, false, nil); replay == nil || replay.IdempotencyKey != key || replay.Pane != 1 {
		t.Fatalf("raw intent did not match redacted durable replay: %+v", replay)
	}
}

func TestBulkDurableSentReplayIgnoresCurrentPolicyAndExternalServices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "sent-replay"
	recovery := &assignment.Assignment{
		BeadID: "bd-sent-replay", BeadTitle: "title now blocked by policy", Pane: 1,
		AgentType: "codex", AgentName: "ReplayAgent", Status: assignment.StatusAssigned,
		IdempotencyKey: "sent-replay-key", ClaimActor: "ReplayAgent/ntm-sent-replay",
		ClaimState: assignment.ClaimClaimed, ReservationRequired: true,
		ReservationInputPaths: []string{"blocked/secret/**"}, DispatchTarget: "%91", OccupancyKey: "%91",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "receipt-sent-replay",
		IntentSHA256: assignment.PromptSHA256("original prompt"), PromptSHA256: assignment.PromptSHA256("durable prompt"),
		PromptSent: "durable prompt",
	}
	store := assignment.NewStore(session)
	store.Assignments[recovery.BeadID] = recovery
	if err := store.Save(); err != nil {
		t.Fatalf("seed durable sent replay: %v", err)
	}
	plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{{
		Pane: "1", PaneID: "%91", Bead: recovery.BeadID, BeadTitle: recovery.BeadTitle,
		AgentType: recovery.AgentType, Status: "planned", recovery: recovery,
	}}}
	policyCalls := 0
	deps := bulkAssignDeps(&BulkAssignDependencies{
		LoadRedaction: func(string) (redaction.Config, error) {
			policyCalls++
			return redaction.Config{}, errors.New("current policy is unavailable")
		},
	})
	output := BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: session}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{RequireReservation: true}, deps, &output, plan)
	if policyCalls != 0 || len(output.Assignments) != 1 {
		t.Fatalf("policy calls=%d assignments=%+v", policyCalls, output.Assignments)
	}
	replayed := output.Assignments[0]
	if !output.Success || replayed.Status != "assigned" || !replayed.PromptSent || !replayed.Claimed ||
		replayed.IdempotencyKey != recovery.IdempotencyKey || replayed.DispatchReceiptID != recovery.DispatchReceiptID {
		t.Fatalf("durable sent replay output=%+v", output)
	}
}

func TestBulkDurableSentReplayCoexistsWithFreshPlanOutcomes(t *testing.T) {
	tests := []struct {
		name            string
		freshOutcome    string
		wantSuccess     bool
		wantFreshStatus string
		wantErrorCode   string
		wantDeliveries  int
		wantSummary     BulkAssignSummary
	}{
		{
			name: "fresh success", freshOutcome: "success", wantSuccess: true,
			wantFreshStatus: "assigned", wantDeliveries: 1,
			wantSummary: BulkAssignSummary{TotalPanes: 2, Assigned: 2},
		},
		{
			name: "fresh setup failure", freshOutcome: "setup_failure",
			wantFreshStatus: "failed", wantErrorCode: "ASSIGNMENT_FAILED",
			wantSummary: BulkAssignSummary{TotalPanes: 2, Assigned: 1, Failed: 1},
		},
		{
			name: "fresh cancellation", freshOutcome: "canceled",
			wantFreshStatus: "failed", wantErrorCode: ErrCodeTimeout,
			wantSummary: BulkAssignSummary{TotalPanes: 2, Assigned: 1, Failed: 1},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const session = "mixed-sent-replay"
			recovery := &assignment.Assignment{
				BeadID: "bd-mixed-replay", BeadTitle: "Durable replay", Pane: 1,
				AgentType: "codex", AgentName: "ReplayAgent", Status: assignment.StatusAssigned,
				IdempotencyKey: "mixed-replay-key", ClaimActor: "ReplayAgent/ntm-mixed-repla",
				ClaimState: assignment.ClaimClaimed, DispatchTarget: "%91", OccupancyKey: "%91",
				DispatchState: assignment.DispatchSent, DispatchReceiptID: "mixed-replay-receipt",
				IntentSHA256: assignment.PromptSHA256("original replay prompt"),
				PromptSHA256: assignment.PromptSHA256("durable replay prompt"), PromptSent: "durable replay prompt",
			}
			store := assignment.NewStore(session)
			store.Assignments[recovery.BeadID] = recovery
			if err := store.Save(); err != nil {
				t.Fatalf("seed mixed replay store: %v", err)
			}
			plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{
				{
					Pane: "1", PaneID: "%91", Bead: recovery.BeadID, BeadTitle: recovery.BeadTitle,
					AgentType: recovery.AgentType, Status: "planned", paneIndex: 1, recovery: recovery,
				},
				{
					Pane: "2", PaneID: "%92", Bead: "bd-mixed-fresh", BeadTitle: "Fresh work",
					AgentType: "codex", Status: "planned", paneIndex: 2,
				},
			}}
			panes := []tmux.Pane{
				{ID: "%91", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex},
				{ID: "%92", WindowIndex: 0, Index: 2, Type: tmux.AgentCodex},
			}
			ctx := t.Context()
			var cancelFresh context.CancelFunc
			if test.freshOutcome == "canceled" {
				ctx, cancelFresh = context.WithCancel(ctx)
				defer cancelFresh()
			}
			deliveries := 0
			deps := bulkAssignDeps(&BulkAssignDependencies{
				LoadStore: func(string) (*assignment.AssignmentStore, error) { return store, nil },
				LoadRedaction: func(string) (redaction.Config, error) {
					if test.freshOutcome == "setup_failure" {
						return redaction.Config{}, errors.New("fresh redaction policy unavailable")
					}
					return redaction.Config{Mode: redaction.ModeOff}, nil
				},
				ReadFile: func(string) ([]byte, error) {
					if test.freshOutcome == "canceled" {
						cancelFresh()
					}
					return []byte(defaultBulkAssignTemplate), nil
				},
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					return append([]tmux.Pane(nil), panes...), nil
				},
				ObserveSession:           bulkSafeObserver(panes),
				GetBeadAssignmentDetails: bulkOpenAssignmentDetails,
				ClaimBead: func(_ context.Context, _ string, beadID, actor string) (bv.BeadClaimResult, error) {
					return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
				},
				NewIdempotencyKey: func() (string, error) { return "mixed-fresh-key", nil },
				DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
					deliveries++
					return nil
				}),
			})
			output := BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: session}
			applyBulkAssignPlan(ctx, BulkAssignOptions{
				projectDir: t.TempDir(), PromptTemplatePath: "mixed-template.txt",
			}, deps, &output, plan)

			if output.Success != test.wantSuccess || output.ErrorCode != test.wantErrorCode ||
				output.Summary != test.wantSummary || deliveries != test.wantDeliveries || len(output.Assignments) != 2 {
				t.Fatalf("deliveries=%d output=%+v", deliveries, output)
			}
			replayed := output.Assignments[0]
			fresh := output.Assignments[1]
			if replayed.Status != "assigned" || !replayed.Claimed || !replayed.PromptSent ||
				replayed.DispatchReceiptID != recovery.DispatchReceiptID || replayed.Error != "" {
				t.Fatalf("durable replay was overwritten by fresh %s: %+v", test.freshOutcome, replayed)
			}
			if fresh.Status != test.wantFreshStatus {
				t.Fatalf("fresh %s result=%+v", test.freshOutcome, fresh)
			}
			if test.wantFreshStatus == "failed" && (fresh.Claimed || fresh.PromptSent || strings.TrimSpace(fresh.Error) == "") {
				t.Fatalf("fresh %s failure actuated or lacks detail: %+v", test.freshOutcome, fresh)
			}
		})
	}
}

func TestBulkDurableSentReplayRejectsChangedGenerationAfterPlanning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "sent-replay-generation-change"
	const beadID = "bd-sent-replay-generation-change"
	old := &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Old generation", Pane: 1,
		AgentType: "codex", AgentName: "OldAgent", Status: assignment.StatusAssigned,
		IdempotencyKey: "old-generation-key", ClaimActor: "OldAgent/ntm-old-generati",
		ClaimState: assignment.ClaimClaimed, DispatchTarget: "%93", OccupancyKey: "%93",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "old-generation-receipt",
		IntentSHA256: assignment.PromptSHA256("old prompt"), PromptSHA256: assignment.PromptSHA256("old prompt"),
		PromptSent: "old prompt",
	}
	store := assignment.NewStore(session)
	store.Assignments[beadID] = old
	if err := store.Save(); err != nil {
		t.Fatalf("seed old generation: %v", err)
	}
	plannedSnapshot := store.Get(beadID)
	current := store.Assignments[beadID]
	current.BeadTitle = "New generation"
	current.AgentName = "NewAgent"
	current.ClaimActor = "NewAgent/ntm-new-generati"
	current.IdempotencyKey = "new-generation-key"
	current.DispatchReceiptID = "new-generation-receipt"
	current.IntentSHA256 = assignment.PromptSHA256("new prompt")
	current.PromptSHA256 = assignment.PromptSHA256("new prompt")
	current.PromptSent = "new prompt"
	if err := store.Save(); err != nil {
		t.Fatalf("persist replacement generation: %v", err)
	}
	plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{{
		Pane: "1", PaneID: "%93", Bead: beadID, BeadTitle: plannedSnapshot.BeadTitle,
		AgentType: plannedSnapshot.AgentType, Status: "planned", recovery: plannedSnapshot,
	}}}
	output := BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: session}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(nil), &output, plan)
	if output.Success || len(output.Assignments) != 1 || output.Assignments[0].Status != "failed" ||
		output.Assignments[0].DispatchReceiptID == "old-generation-receipt" {
		t.Fatalf("stale replay snapshot was accepted: %+v", output)
	}
}

func TestBulkPendingRecoveryReusesValidReservationWithoutAgentMail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "bulk-valid-reservation-recovery"
	const beadID = "bd-valid-reservation-recovery"
	const prompt = "persisted reserved recovery prompt"
	const key = "valid-reservation-recovery-key"
	const actor = "ReservedAgent/ntm-valid-reserv"
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)
	claimedAt := now.Add(-time.Minute)
	recovery := &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Recover with durable lease", Pane: 1,
		AgentType: "codex", AgentName: "ReservedAgent", Status: assignment.StatusClaimed,
		AssignedAt: now.Add(-2 * time.Minute), IdempotencyKey: key, ClaimActor: actor,
		ClaimState: assignment.ClaimClaimed, ClaimStatus: "in_progress", ClaimedAt: &claimedAt,
		ReservationRequired: true, ReservationInputPaths: []string{"internal/robot/**"},
		ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservationAgent: "ReservedAgent", ReservationTarget: "%92",
		ReservationRequested: []string{"internal/robot/**"}, ReservedPaths: []string{"internal/robot/**"},
		ReservationIDs: []int{92}, ReservationExpiresAt: &expiresAt,
		DispatchTarget: "%92", OccupancyKey: "%92", DispatchState: assignment.DispatchPending,
		PendingPrompt: prompt, PromptSHA256: assignment.PromptSHA256(prompt),
		IntentSHA256: assignment.PromptSHA256("original unredacted reserved prompt"),
	}
	store := assignment.NewStore(session)
	store.Assignments[beadID] = recovery
	if err := store.Save(); err != nil {
		t.Fatalf("seed valid reservation recovery: %v", err)
	}
	panes := []tmux.Pane{{ID: "%92", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}}
	deliveries := 0
	deps := bulkAssignDeps(&BulkAssignDependencies{
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) { return t.TempDir(), nil },
		LoadStore:      func(string) (*assignment.AssignmentStore, error) { return store, nil },
		ListPanes:      func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
			return bulkSafeObservation(session, panes), nil
		},
		ClaimStaleBead: func(_ context.Context, _ string, gotBead, gotActor string, _ time.Time) (bv.BeadClaimResult, error) {
			if gotBead != beadID || gotActor != actor {
				t.Fatalf("stale claim bead=%q actor=%q", gotBead, gotActor)
			}
			return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: now}, nil
		},
		GetBeadStatus: func(context.Context, string, string) (string, error) { return "in_progress", nil },
		GetBeadAssignmentDetails: func(_ context.Context, _ string, gotBead string) (*bv.BeadAssignmentDetails, error) {
			if gotBead != beadID {
				t.Fatalf("eligibility bead=%q, want %q", gotBead, beadID)
			}
			return &bv.BeadAssignmentDetails{ID: beadID, Status: "in_progress"}, nil
		},
		NewIdempotencyKey: func() (string, error) {
			t.Fatal("recovery generated a new idempotency key")
			return "", nil
		},
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			deliveries++
			return nil
		}),
		LoadRedaction: func(string) (redaction.Config, error) { return redaction.Config{Mode: redaction.ModeOff}, nil },
	})
	plan := bulkAssignPlan{Assignments: []BulkAssignAssignment{{
		Pane: "1", PaneID: "%92", Bead: beadID, BeadTitle: recovery.BeadTitle,
		AgentType: recovery.AgentType, Status: "planned", paneIndex: 1,
		stale: true, staleUpdatedAt: now, recovery: recovery,
	}}}
	output := BulkAssignOutput{RobotResponse: NewRobotResponse(true), Session: session}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{projectDir: t.TempDir()}, deps, &output, plan)
	if !output.Success || deliveries != 1 || len(output.Assignments) != 1 ||
		output.Assignments[0].Status != "assigned" || output.Assignments[0].DispatchReceiptID == "" {
		t.Fatalf("deliveries=%d output=%+v", deliveries, output)
	}
}

func TestBulkAtomicRuntimeSharesFreshObservationAcrossConcurrentAssignments(t *testing.T) {
	var calls atomic.Int32
	panes := []tmux.Pane{
		{ID: "%1", WindowIndex: 0, Index: 0, Type: tmux.AgentCodex},
		{ID: "%2", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude},
	}
	runtime := &bulkAtomicRuntime{deps: BulkAssignDependencies{
		ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
			calls.Add(1)
			time.Sleep(25 * time.Millisecond)
			return bulkSafeObservation("bulk-observation", panes), nil
		},
	}}

	start := make(chan struct{})
	results := make(chan statuspkg.SessionObservation, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			observation, err := runtime.observeSession(t.Context(), "bulk-observation")
			if err != nil {
				t.Errorf("observeSession: %v", err)
				return
			}
			results <- observation
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("whole-session observation calls = %d, want 1", calls.Load())
	}
	for observation := range results {
		if !observation.SafeToDispatch("%1") || !observation.SafeToDispatch("%2") {
			t.Fatalf("shared observation lost safe panes: %+v", observation)
		}
	}

	runtime.observeMu.Lock()
	runtime.observation.ObservedAt = time.Now().Add(-statuspkg.DispatchObservationMaxAge - time.Second)
	runtime.observeMu.Unlock()
	if _, err := runtime.observeSession(t.Context(), "bulk-observation"); err != nil {
		t.Fatalf("refresh expired observation: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired observation calls = %d, want refresh to 2", calls.Load())
	}
}

func TestApplyBulkAtomicExecutionResultKeepsDurableReceiptAuthoritativeAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output := &BulkAssignAssignment{
		Pane: "0", PaneID: "%41", Bead: "bd-post-success-cancel", BeadTitle: "Original", Status: "planned",
	}
	record := &assignment.Assignment{
		BeadID: "bd-post-success-cancel", BeadTitle: "Durable", IdempotencyKey: "post-success-key",
		Status: assignment.StatusAssigned, ClaimState: assignment.ClaimClaimed, ClaimActor: "ExactAgent",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "receipt-post-success",
	}
	applyBulkAtomicExecutionResult(ctx, output, "post-success-key", assignment.AtomicResult{
		Assignment: record,
		Sent:       true,
		Dispatch:   assignment.DispatchReceipt{DeliveryID: "receipt-post-success"},
	}, nil)

	if output.Status != "assigned" || !output.PromptSent || !output.Claimed || output.ClaimActor != "ExactAgent" ||
		output.DispatchReceiptID != "receipt-post-success" || output.Error != "" || output.failureCause != nil {
		t.Fatalf("post-success cancellation overwrote authoritative receipt: %+v", output)
	}

	unstarted := &BulkAssignAssignment{Pane: "1", PaneID: "%42", Bead: "bd-unstarted-cancel", Status: "planned"}
	applyBulkAtomicExecutionResult(ctx, unstarted, "unstarted-key", assignment.AtomicResult{}, nil)
	if unstarted.Status != "failed" || unstarted.PromptSent || unstarted.failureCode != ErrCodeTimeout || !errors.Is(unstarted.failureCause, context.Canceled) {
		t.Fatalf("unstarted canceled assignment = %+v", unstarted)
	}
}

func TestBulkAssignFailureClassReportsAmbiguousDispatchTruthfully(t *testing.T) {
	code, hint := bulkAssignFailureClass([]BulkAssignAssignment{{
		Status: "failed", failureCause: assignment.ErrDispatchOutcomeUnknown,
	}})
	if code != ErrCodeDispatchUnknown || !strings.Contains(hint, "outcome is unknown") || strings.Contains(hint, "no failed target was dispatched") {
		t.Fatalf("ambiguous dispatch classification code=%q hint=%q", code, hint)
	}
}

func TestBulkAssignClaimConflictNeverReservesOrDispatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plan := allocateBulkAssignBeads(
		[]bulkPane{{Ref: tmux.PaneRef{ID: "%7", WindowIndex: 0, PaneIndex: 1}, AgentType: "claude"}},
		[]bulkBead{{ID: "bd-owned", Title: "Owned"}},
	)
	reserveCalls := 0
	deliverCalls := 0
	deps := BulkAssignDependencies{
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		Cwd:      func() (string, error) { return t.TempDir(), nil },
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%7", WindowIndex: 0, Index: 1, Type: tmux.AgentClaude}}, nil
		},
		LoadStore:                func(string) (*assignment.AssignmentStore, error) { return assignment.NewStore("bulk-conflict"), nil },
		GetBeadAssignmentDetails: bulkOpenAssignmentDetails,
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			return bv.BeadClaimResult{}, bv.ErrBeadAlreadyClaimed
		},
		NewIdempotencyKey: func() (string, error) { return "conflict-key", nil },
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			reserveCalls++
			return assignment.LeaseReceipt{}, nil
		}),
		ResolveAgentName: func(context.Context, string, string, string, string) (string, error) {
			return "ClaimConflictAgent", nil
		},
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			deliverCalls++
			return nil
		}),
		LoadRedaction: func(string) (redaction.Config, error) { return redaction.Config{Mode: redaction.ModeOff}, nil },
	}
	output := BulkAssignOutput{Session: "bulk-conflict"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{RequireReservation: true, ReservationPaths: []string{"internal/robot/**"}}, bulkAssignDeps(&deps), &output, plan)
	if len(output.Assignments) != 1 || output.Assignments[0].Status != "failed" || output.Assignments[0].PromptSent || output.Assignments[0].Claimed {
		t.Fatalf("output=%+v", output.Assignments)
	}
	if reserveCalls != 0 || deliverCalls != 0 {
		t.Fatalf("reserve=%d dispatch=%d, want zero", reserveCalls, deliverCalls)
	}
}

func TestRobotAtomicClaimPortMapsAssignmentEligibilityRejection(t *testing.T) {
	port := newRobotAtomicClaimPort(t.TempDir(), func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
		return bv.BeadClaimResult{}, &bv.AssignmentEligibilityError{
			BeadID: "bd-gated", Status: "open", OperatorLabels: []string{"operator-gated"},
		}
	})
	_, err := port.Claim(t.Context(), "bd-gated", "StableActor")
	if !errors.Is(err, assignment.ErrClaimIneligible) || errors.Is(err, assignment.ErrClaimConflict) {
		t.Fatalf("robot atomic claim error=%v, want ineligible only", err)
	}
}

func TestBulkAssignDryRunHasNoAtomicOrPacingSideEffects(t *testing.T) {
	plan := allocateBulkAssignBeads(
		[]bulkPane{{Ref: tmux.PaneRef{ID: "%8", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"}},
		[]bulkBead{{ID: "bd-dry", Title: "Dry"}},
	)
	calls := 0
	deps := BulkAssignDependencies{
		ReadFile: func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			calls++
			return nil, errors.New("unexpected load")
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			calls++
			return bv.BeadClaimResult{}, nil
		},
		NewIdempotencyKey: func() (string, error) { calls++; return "", nil },
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			calls++
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error { calls++; return nil }),
		Wait:              func(context.Context, time.Duration) error { calls++; return nil },
	}
	output := BulkAssignOutput{Session: "bulk-dry"}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{DryRun: true, RequireReservation: true, ReservationPaths: []string{"internal/robot/**"}, Stagger: time.Hour}, bulkAssignDeps(&deps), &output, plan)
	if calls != 0 || len(output.Assignments) != 1 || output.Assignments[0].Status != "planned" {
		t.Fatalf("calls=%d output=%+v", calls, output.Assignments)
	}
}

func TestRobotAtomicIdempotencyKeyDoesNotReplayTerminalAssignment(t *testing.T) {
	for _, status := range []assignment.AssignmentStatus{
		assignment.StatusCompleted,
		assignment.StatusFailed,
		assignment.StatusReassigned,
	} {
		t.Run(string(status), func(t *testing.T) {
			store := assignment.NewStore("terminal-key-" + string(status))
			store.Assignments["bd-terminal"] = &assignment.Assignment{
				BeadID: "bd-terminal", Status: status, Pane: 1, AgentType: "codex", AgentName: "AgentOne",
				IdempotencyKey: "old-key", DispatchTarget: "%8", OccupancyKey: "%8",
				DispatchState: assignment.DispatchSent, IntentSHA256: assignment.PromptSHA256("prompt"), PromptSent: "prompt",
			}
			calls := 0
			key, err := robotAtomicIdempotencyKey(
				store, "bd-terminal", "%8", 1, "codex", "AgentOne", "prompt", false, nil,
				func() (string, error) { calls++; return "new-key", nil },
			)
			if err != nil {
				t.Fatalf("robotAtomicIdempotencyKey: %v", err)
			}
			if key != "new-key" || calls != 1 {
				t.Fatalf("terminal status %s reused key %q calls=%d", status, key, calls)
			}
			if replay := robotAtomicReplayIntent(store, "bd-terminal", "%8", 1, "codex", "prompt", false, nil); replay != nil {
				t.Fatalf("terminal status %s replayed stale receipt: %+v", status, replay)
			}
		})
	}
}

func TestBulkAssignTerminalGenerationRequiresReopenedBead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "bulk-terminal-closed"
	const beadID = "bd-terminal-closed"
	const oldKey = "terminal-closed-key"
	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Closed terminal bead", Pane: 1,
		AgentType: "codex", AgentName: "TerminalAgent", Status: assignment.StatusCompleted,
		AssignedAt: time.Now().UTC(), IdempotencyKey: oldKey, ClaimActor: "retained-actor",
		DispatchTarget: "%8", OccupancyKey: "%8", DispatchState: assignment.DispatchSent,
		DispatchReceiptID: "terminal-receipt", PromptSent: "already delivered",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal store: %v", err)
	}
	plan := allocateBulkAssignBeads(
		[]bulkPane{{Ref: tmux.PaneRef{ID: "%8", WindowIndex: 0, PaneIndex: 1}, AgentType: "codex"}},
		[]bulkBead{{ID: beadID, Title: "Closed terminal bead"}},
	)
	claimCalls := 0
	deliverCalls := 0
	panes := []tmux.Pane{{ID: "%8", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}}
	deps := BulkAssignDependencies{
		ReadFile:                 func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		Cwd:                      func() (string, error) { return t.TempDir(), nil },
		ListPanes:                func(context.Context, string) ([]tmux.Pane, error) { return append([]tmux.Pane(nil), panes...), nil },
		LoadStore:                func(string) (*assignment.AssignmentStore, error) { return store, nil },
		GetBeadStatus:            func(context.Context, string, string) (string, error) { return "closed", nil },
		GetBeadAssignmentDetails: bulkOpenAssignmentDetails,
		ObserveSession:           bulkSafeObserver(panes),
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			claimCalls++
			return bv.BeadClaimResult{}, errors.New("closed bead must not be claimed")
		},
		NewIdempotencyKey: func() (string, error) { return "new-terminal-key", nil },
		LoadRedaction:     func(string) (redaction.Config, error) { return redaction.Config{Mode: redaction.ModeOff}, nil },
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			deliverCalls++
			return nil
		}),
	}
	output := BulkAssignOutput{Session: session}
	applyBulkAssignPlan(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), &output, plan)
	if len(output.Assignments) != 1 || output.Assignments[0].Status != "failed" ||
		!strings.Contains(output.Assignments[0].Error, "tracker status is \"closed\"") || output.Assignments[0].PromptSent {
		t.Fatalf("closed terminal assignment output=%+v", output.Assignments)
	}
	if claimCalls != 0 || deliverCalls != 0 {
		t.Fatalf("closed terminal assignment actuated claim=%d deliver=%d", claimCalls, deliverCalls)
	}
	stored := store.Get(beadID)
	if stored == nil || stored.IdempotencyKey != oldKey || stored.DispatchReceiptID != "terminal-receipt" || stored.Status != assignment.StatusCompleted {
		t.Fatalf("closed terminal assignment replaced durable generation: %+v", stored)
	}
}

func TestBulkAssignAllocationParsing(t *testing.T) {
	allocation := `{"1":"bd-1","2":"bd-2"}`
	parsed, err := parseBulkAssignAllocation(allocation)
	if err != nil {
		t.Fatalf("parseBulkAssignAllocation failed: %v", err)
	}

	t.Logf("parsed allocation=%v", parsed)
	if parsed["1"] != "bd-1" || parsed["2"] != "bd-2" {
		t.Fatalf("unexpected allocation map: %v", parsed)
	}
}

func TestBulkAssignAllocationInvalidJSON(t *testing.T) {
	_, err := parseBulkAssignAllocation("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, err := parseBulkAssignAllocation(`{}`); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("empty allocation error = %v, want non-empty allocation validation", err)
	}
}

func TestBulkAssignSkipPanesApplied(t *testing.T) {
	panes := []tmux.Pane{
		{Index: 1, Title: "proj__cc_1"},
		{Index: 2, Title: "proj__cc_2"},
		{Index: 3, Title: "proj__cc_3"},
	}
	filtered, err := filterBulkAssignPanes(panes, []string{"2"})
	if err != nil {
		t.Fatalf("filter panes: %v", err)
	}

	got := []int{}
	for _, pane := range filtered {
		got = append(got, pane.Ref.PaneIndex)
	}
	sort.Ints(got)
	expected := []int{1, 3}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("filtered panes mismatch: got %v want %v", got, expected)
	}

	t.Logf("filtered panes=%v", got)
}

func TestBulkAssignEmptySession(t *testing.T) {
	triage := mockTriage([]bv.TriageRecommendation{{ID: "bd-1", Title: "Test", Status: "ready", Priority: 1}}, nil)
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "ready"}, BulkAssignDependencies{}, nil, triage, nil)

	if len(plan.Assignments) != 0 {
		t.Fatalf("expected no assignments, got %d", len(plan.Assignments))
	}
	if len(plan.UnassignedBeads) != 1 {
		t.Fatalf("expected 1 unassigned bead, got %v", plan.UnassignedBeads)
	}

	t.Logf("plan=%+v", plan)
}

func TestBulkAssignControlPaneOnly(t *testing.T) {
	panes := []tmux.Pane{{Index: 0, Title: "proj__user_0"}}
	filtered, err := filterBulkAssignPanes(panes, nil)
	if err != nil {
		t.Fatalf("filter panes: %v", err)
	}

	if len(filtered) != 0 {
		t.Fatalf("expected 0 agent panes, got %d", len(filtered))
	}
}

func TestBulkAssignFilterPrefersParsedPaneType(t *testing.T) {
	panes := []tmux.Pane{
		{Index: 1, ID: "%1", Title: "scratch", Type: tmux.AgentClaude},
		{Index: 2, ID: "%2", Title: "claude_notes", Type: tmux.AgentUser},
	}

	filtered, err := filterBulkAssignPanes(panes, nil)
	if err != nil {
		t.Fatalf("filter panes: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("expected 1 agent pane, got %d", len(filtered))
	}
	if filtered[0].Ref.ID != "%1" || filtered[0].AgentType != "claude" {
		t.Fatalf("filtered pane = %+v, want target %%1 type claude", filtered[0])
	}
}

func TestBulkAssignInvalidBeadIDInAllocation(t *testing.T) {
	allocation := map[string]string{"1": "bd-missing"}
	panes := mockPanes("proj", []int{1})
	deps := BulkAssignDependencies{
		FetchBeadTitle: func(_ context.Context, _ string, beadID string) (string, error) {
			return "", fmt.Errorf("bead %s not found", beadID)
		},
		Cwd: func() (string, error) { return "/tmp", nil },
	}

	plan := planBulkAssignFromAllocation(t.Context(), BulkAssignOptions{}, bulkAssignDeps(&deps), panes, allocation)
	if plan.Assignments[0].Status != "failed" {
		t.Fatalf("expected failed status, got %q", plan.Assignments[0].Status)
	}

	t.Logf("assignment=%+v", plan.Assignments[0])
}

func TestBulkAssignBVFailure(t *testing.T) {
	deps := BulkAssignDependencies{
		FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
			return nil, errors.New("bv failed")
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
			return nil, nil
		},
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		Cwd: func() (string, error) { return "/tmp", nil },
	}

	output, err := captureStdout(t, func() error {
		return PrintBulkAssign(t.Context(), BulkAssignOptions{Session: "proj", FromBV: true, Deps: &deps})
	})
	var exitErr *ProcessExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !exitErr.JSONWritten() {
		t.Fatalf("PrintBulkAssign error = %T %v, want written exit-1 error", err, err)
	}

	var result BulkAssignOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false when bv triage fails")
	}
	if result.ErrorCode != ErrCodeInternalError {
		t.Fatalf("expected error_code %s, got %s", ErrCodeInternalError, result.ErrorCode)
	}
	if !strings.Contains(result.Error, "verify actionable bulk assignment work") {
		t.Fatalf("expected error to mention actionable verification failure, got: %s", result.Error)
	}
}

func TestBulkAssignUsesAuthoritativeSessionProjectForBVPlanning(t *testing.T) {
	authoritative := t.TempDir()
	wrongCWD := t.TempDir()
	var triageDir, inProgressDir string
	cwdCalls := 0
	deps := BulkAssignDependencies{
		ResolveProject: func(_ context.Context, session string, _ []tmux.Pane) (string, error) {
			if session != "authoritative-session" {
				t.Fatalf("ResolveProject session = %q", session)
			}
			return authoritative, nil
		},
		Cwd: func() (string, error) {
			cwdCalls++
			return wrongCWD, nil
		},
		FetchTriage: func(_ context.Context, dir string) (*bv.TriageResponse, error) {
			triageDir = dir
			return mockTriage([]bv.TriageRecommendation{{ID: "bd-auth", Title: "Scoped work", Status: "open", Priority: 1}}, nil), nil
		},
		FetchInProgress: func(_ context.Context, dir string, _ int) ([]bv.BeadInProgress, error) {
			inProgressDir = dir
			return nil, nil
		},
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
	}

	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "authoritative-session", FromBV: true, DryRun: true, Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign: %v", err)
	}
	if !output.Success || len(output.Assignments) != 1 {
		t.Fatalf("bulk assignment output = %+v", output)
	}
	if triageDir != authoritative || inProgressDir != authoritative {
		t.Fatalf("planning dirs = triage %q in-progress %q, want %q", triageDir, inProgressDir, authoritative)
	}
	if cwdCalls != 0 {
		t.Fatalf("caller CWD resolved %d time(s), want zero", cwdCalls)
	}
}

func TestBulkAssignLoadsAuthoritativePolicyBeforeParsingPlanningAndMutation(t *testing.T) {
	policyErr := errors.New("injected authoritative policy failure")
	for _, test := range []struct {
		name string
		opts BulkAssignOptions
	}{
		{
			name: "malformed explicit allocation is not parsed first",
			opts: BulkAssignOptions{AllocationJSON: `{"1":`},
		},
		{
			name: "from-bv planning and stale recovery do not start first",
			opts: BulkAssignOptions{FromBV: true, Strategy: "balanced"},
		},
		{
			name: "valid explicit allocation cannot actuate first",
			opts: BulkAssignOptions{
				AllocationJSON:     `{"1":"bd-policy"}`,
				RequireReservation: true,
				ReservationPaths:   []string{"internal/robot/**"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authoritative := t.TempDir()
			selectedConfig := filepath.Join(t.TempDir(), "selected.toml")
			calls := make([]string, 0, 3)
			forbidden := func(surface string) {
				t.Fatalf("authoritative policy failure reached %s; call order=%v", surface, calls)
			}
			deps := BulkAssignDependencies{
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
					calls = append(calls, "list-panes")
					return mockTmuxPanesForList([]int{1}), nil
				},
				ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
					calls = append(calls, "resolve-project")
					return authoritative, nil
				},
				LoadAssignmentPolicy: func(projectDir, configPath string, requireConfig bool) (*config.Config, error) {
					calls = append(calls, "load-policy")
					if projectDir != authoritative || configPath != selectedConfig || !requireConfig {
						t.Fatalf("policy args = project %q config %q required=%t, want %q %q true",
							projectDir, configPath, requireConfig, authoritative, selectedConfig)
					}
					return nil, policyErr
				},
				FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
					forbidden("actionable plan/label verification")
					return nil, nil
				},
				FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
					forbidden("triage")
					return nil, nil
				},
				FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
					forbidden("in-progress lookup")
					return nil, nil
				},
				ReadFile: func(string) ([]byte, error) {
					forbidden("prompt template I/O")
					return nil, nil
				},
				FetchBeadTitle: func(context.Context, string, string) (string, error) {
					forbidden("allocation title lookup")
					return "", nil
				},
				LoadStore: func(string) (*assignment.AssignmentStore, error) {
					forbidden("assignment ledger or stale recovery")
					return nil, nil
				},
				ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
					forbidden("Beads claim")
					return bv.BeadClaimResult{}, nil
				},
				ClaimStaleBead: func(context.Context, string, string, string, time.Time) (bv.BeadClaimResult, error) {
					forbidden("stale Beads claim")
					return bv.BeadClaimResult{}, nil
				},
				ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
					forbidden("Agent Mail reservation")
					return assignment.LeaseReceipt{}, nil
				}),
				ResolveAgentName: func(context.Context, string, string, string, string) (string, error) {
					forbidden("Agent Mail identity lookup")
					return "", nil
				},
				ObserveSession: func(context.Context, string) (statuspkg.SessionObservation, error) {
					forbidden("dispatch observation")
					return statuspkg.SessionObservation{}, nil
				},
				DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
					forbidden("prompt dispatch")
					return nil
				}),
				LoadRedaction: func(string) (redaction.Config, error) {
					forbidden("dispatch redaction setup")
					return redaction.Config{}, nil
				},
			}

			opts := test.opts
			opts.Session = "policy-order"
			opts.ConfigPath = selectedConfig
			opts.RequireConfig = true
			opts.Deps = &deps
			output, err := GetBulkAssign(t.Context(), opts)
			if err != nil {
				t.Fatalf("GetBulkAssign transport error: %v", err)
			}
			if output.Success || output.ErrorCode != ErrCodeInvalidFlag || output.Assignments == nil ||
				!strings.Contains(output.Error, policyErr.Error()) {
				t.Fatalf("policy failure output = %+v, want exact INVALID_FLAG boundary", output)
			}
			if want := []string{"list-panes", "resolve-project", "load-policy"}; !reflect.DeepEqual(calls, want) {
				t.Fatalf("call order = %v, want %v", calls, want)
			}
		})
	}
}

func TestBulkAssignMissingExplicitConfigIsInvalidFlagWithZeroSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	authoritative := t.TempDir()
	missingConfig := filepath.Join(t.TempDir(), "missing-selected.toml")
	forbiddenCalls := 0
	forbidden := func() { forbiddenCalls++ }
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
			return authoritative, nil
		},
		FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
			forbidden()
			return nil, nil
		},
		FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
			forbidden()
			return nil, nil
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
			forbidden()
			return nil, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			forbidden()
			return nil, nil
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			forbidden()
			return bv.BeadClaimResult{}, nil
		},
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			forbidden()
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			forbidden()
			return nil
		}),
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "missing-policy", ConfigPath: missingConfig, RequireConfig: true,
		AllocationJSON: `{"1":"bd-missing"}`, Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeInvalidFlag || output.Assignments == nil ||
		!strings.Contains(output.Error, "explicitly selected config") {
		t.Fatalf("missing explicit policy output = %+v", output)
	}
	if forbiddenCalls != 0 {
		t.Fatalf("missing explicit config reached %d planning or mutation dependencies", forbiddenCalls)
	}
	if _, err := os.Stat(missingConfig); !os.IsNotExist(err) {
		t.Fatalf("missing explicit config was created or changed: %v", err)
	}
	for _, path := range []string{
		filepath.Join(os.Getenv("HOME"), ".ntm", "sessions", "missing-policy", "assignments.json"),
		filepath.Join(os.Getenv("HOME"), ".ntm", "sessions", "missing-policy", "assignments.json.bak"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("policy rejection created assignment ledger %s: %v", path, err)
		}
	}
}

func TestBulkAssignActionableVerificationFailureHasZeroMutation(t *testing.T) {
	authoritative := t.TempDir()
	verificationErr := errors.New("live label coverage is incomplete")
	calls := make([]string, 0, 4)
	forbidden := func(surface string) {
		t.Fatalf("actionable verification failure reached %s; calls=%v", surface, calls)
	}
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			calls = append(calls, "list-panes")
			return mockTmuxPanesForList([]int{1}), nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
			calls = append(calls, "resolve-project")
			return authoritative, nil
		},
		LoadAssignmentPolicy: func(projectDir, _ string, _ bool) (*config.Config, error) {
			calls = append(calls, "load-policy")
			if projectDir != authoritative {
				t.Fatalf("policy project = %q, want %q", projectDir, authoritative)
			}
			return config.Default(), nil
		},
		FetchActionable: func(_ context.Context, projectDir string, limit int) ([]bv.TriageRecommendation, error) {
			calls = append(calls, "verify-actionable")
			if projectDir != authoritative || limit != 0 {
				t.Fatalf("actionable args = dir %q limit %d, want %q 0", projectDir, limit, authoritative)
			}
			return nil, verificationErr
		},
		FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
			forbidden("triage")
			return nil, nil
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
			forbidden("in-progress lookup")
			return nil, nil
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			forbidden("assignment ledger")
			return nil, nil
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			forbidden("Beads claim")
			return bv.BeadClaimResult{}, nil
		},
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			forbidden("Agent Mail reservation")
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			forbidden("prompt dispatch")
			return nil
		}),
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "unverified-actionable", FromBV: true, Strategy: "balanced", Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if output.Success || output.ErrorCode != ErrCodeInternalError || output.Assignments == nil ||
		!strings.Contains(output.Error, verificationErr.Error()) {
		t.Fatalf("actionable verification failure output = %+v", output)
	}
	if want := []string{"list-panes", "resolve-project", "load-policy", "verify-actionable"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("call order = %v, want %v", calls, want)
	}
}

func TestBulkAssignTargetProjectCustomGateOverridesCallerCWD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() { bv.ConfigureOperatorGatedLabels(previousLabels) })

	authoritative := t.TempDir()
	wrongCWD := t.TempDir()
	if err := os.MkdirAll(filepath.Join(authoritative, ".ntm"), 0o700); err != nil {
		t.Fatalf("create target config directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(authoritative, ".ntm", "config.toml"),
		[]byte("[assign]\noperator_gated_labels = [\"architecture-approval\"]\n"),
		0o600,
	); err != nil {
		t.Fatalf("write target assignment policy: %v", err)
	}
	triage := mockTriage(
		[]bv.TriageRecommendation{{
			ID: "bd-target-gated", Title: "Target-scoped gated work", Status: "open", Priority: 0,
			Labels: []string{"ARCHITECTURE-APPROVAL"},
		}},
		[]bv.BlockerToClear{{
			ID: "bd-target-gated", Title: "Stale high-impact copy", Actionable: true, UnblocksCount: 99,
		}},
	)
	cwdCalls := 0
	mutationCalls := 0
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
			return authoritative, nil
		},
		Cwd: func() (string, error) {
			cwdCalls++
			return wrongCWD, nil
		},
		FetchTriage: func(_ context.Context, dir string) (*bv.TriageResponse, error) {
			if dir != authoritative {
				t.Fatalf("triage dir = %q, want %q", dir, authoritative)
			}
			return triage, nil
		},
		FetchInProgress: func(_ context.Context, dir string, _ int) ([]bv.BeadInProgress, error) {
			if dir != authoritative {
				t.Fatalf("in-progress dir = %q, want %q", dir, authoritative)
			}
			return nil, nil
		},
		ClaimBead: func(context.Context, string, string, string) (bv.BeadClaimResult, error) {
			mutationCalls++
			return bv.BeadClaimResult{}, nil
		},
		ReservationPort: testReservationFunc(func(context.Context, assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
			mutationCalls++
			return assignment.LeaseReceipt{}, nil
		}),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error {
			mutationCalls++
			return nil
		}),
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "target-policy", FromBV: true, Strategy: "impact", Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if !output.Success || len(output.Assignments) != 0 || len(output.UnassignedBeads) != 0 {
		t.Fatalf("target-gated bulk output = %+v, want empty successful plan", output)
	}
	if cwdCalls != 0 {
		t.Fatalf("bulk policy consulted caller CWD %d time(s), want target project only", cwdCalls)
	}
	if mutationCalls != 0 {
		t.Fatalf("target-gated candidate reached %d mutation surfaces", mutationCalls)
	}
}

func TestBulkAssignVerifiedActionableSetRemovesOverlappingTriageCandidates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		gatedID = "bd-overlap-gated"
		safeID  = "bd-verified-safe"
	)
	triage := mockTriage(
		[]bv.TriageRecommendation{
			{ID: gatedID, Title: "Stale triage row", Status: "open", Priority: 0},
			{ID: safeID, Title: "Verified live row", Status: "open", Priority: 1},
		},
		[]bv.BlockerToClear{
			{ID: gatedID, Title: "Stale impact row", Actionable: true, UnblocksCount: 100},
			{ID: safeID, Title: "Verified impact row", Actionable: true, UnblocksCount: 1},
		},
	)
	claimIDs := make([]string, 0, 1)
	deps := BulkAssignDependencies{
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) {
			return mockTmuxPanesForList([]int{1}), nil
		},
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) {
			return t.TempDir(), nil
		},
		LoadAssignmentPolicy: func(string, string, bool) (*config.Config, error) {
			return config.Default(), nil
		},
		FetchActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
			return []bv.TriageRecommendation{{
				ID: safeID, Title: "Verified live row", Status: "open", Priority: 1, UnblocksIDs: []string{"bd-child"},
			}}, nil
		},
		FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
			return triage, nil
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) {
			return nil, nil
		},
		GetBeadAssignmentDetails: bulkOpenAssignmentDetails,
		ClaimBead: func(_ context.Context, _ string, beadID, _ string) (bv.BeadClaimResult, error) {
			claimIDs = append(claimIDs, beadID)
			return bv.BeadClaimResult{}, errors.New("stop after observing claim target")
		},
		LoadStore: func(string) (*assignment.AssignmentStore, error) {
			return assignment.NewStore("verified-overlap"), nil
		},
		NewIdempotencyKey: func() (string, error) { return "verified-overlap-key", nil },
		ResolveAgentName:  func(context.Context, string, string, string, string) (string, error) { return "VerifiedAgent", nil },
		ObserveSession:    bulkSafeObserver(mockTmuxPanesForList([]int{1})),
		LoadRedaction:     func(string) (redaction.Config, error) { return redaction.Config{Mode: redaction.ModeOff}, nil },
	}
	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "verified-overlap", FromBV: true, Strategy: "impact", Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign transport error: %v", err)
	}
	if len(output.Assignments) != 1 || output.Assignments[0].Bead != safeID {
		t.Fatalf("verified overlap output = %+v, want only %s", output, safeID)
	}
	if !reflect.DeepEqual(claimIDs, []string{safeID}) {
		t.Fatalf("claim IDs = %v, want only verified safe candidate %s", claimIDs, safeID)
	}
	for _, beadID := range append(append([]string(nil), claimIDs...), output.UnassignedBeads...) {
		if beadID == gatedID {
			t.Fatalf("overlapping stale candidate %s survived verified actionable restriction: %+v", gatedID, output)
		}
	}
}

func TestBulkAssignLiveSessionProjectOverridesCallerCWDWithoutSavedMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessionProject := t.TempDir()
	callerProject := t.TempDir()
	for _, projectDir := range []string{sessionProject, callerProject} {
		if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
			t.Fatalf("create beads project marker: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sessionProject, "internal", "robot"), 0o755); err != nil {
		t.Fatalf("create live pane subdirectory: %v", err)
	}
	var triageDir string
	const session = "exact-live-session"
	panes := mockTmuxPanesForList([]int{1})
	deps := BulkAssignDependencies{
		Cwd: func() (string, error) { return callerProject, nil },
		PaneCurrentPath: func(_ context.Context, paneID string) (string, error) {
			if paneID != "%1" {
				t.Fatalf("pane path lookup = %q, want %%1", paneID)
			}
			return filepath.Join(sessionProject, "internal", "robot"), nil
		},
		ListPanes: func(_ context.Context, gotSession string) ([]tmux.Pane, error) {
			if gotSession != session {
				t.Fatalf("ListPanes session = %q, want exact %q", gotSession, session)
			}
			return panes, nil
		},
		FetchTriage: func(_ context.Context, dir string) (*bv.TriageResponse, error) {
			triageDir = dir
			return mockTriage([]bv.TriageRecommendation{{ID: "bd-live", Title: "Live scoped work", Status: "open", Priority: 1}}, nil), nil
		},
		FetchInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) { return nil, nil },
	}

	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{Session: session, FromBV: true, DryRun: true, Deps: &deps})
	if err != nil {
		t.Fatalf("GetBulkAssign: %v", err)
	}
	if !output.Success || len(output.Assignments) != 1 {
		t.Fatalf("bulk assignment output = %+v", output)
	}
	if triageDir != sessionProject {
		t.Fatalf("triage dir = %q, want live session project %q instead of caller CWD %q", triageDir, sessionProject, callerProject)
	}
}

func TestBulkAssignRejectsLiveSessionWithMultipleProjectRoots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	firstProject := t.TempDir()
	secondProject := t.TempDir()
	for _, projectDir := range []string{firstProject, secondProject} {
		if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
			t.Fatalf("create project marker: %v", err)
		}
	}
	panes := mockTmuxPanesForList([]int{1, 2})
	deps := BulkAssignDependencies{
		Cwd: func() (string, error) { return secondProject, nil },
		PaneCurrentPath: func(_ context.Context, paneID string) (string, error) {
			if paneID == "%1" {
				return firstProject, nil
			}
			return secondProject, nil
		},
		ListPanes: func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		FetchTriage: func(context.Context, string) (*bv.TriageResponse, error) {
			t.Fatal("triage must not run for ambiguous live project roots")
			return nil, nil
		},
	}

	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{Session: "mixed-live-session", FromBV: true, DryRun: true, Deps: &deps})
	if err != nil {
		t.Fatalf("GetBulkAssign: %v", err)
	}
	if output.Success || !strings.Contains(output.Error, "multiple project roots") {
		t.Fatalf("bulk assignment output = %+v, want mixed-root failure", output)
	}
}

func TestBulkAssignUsesAuthoritativeSessionProjectForActuation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	authoritative := t.TempDir()
	wrongCWD := t.TempDir()
	store := assignment.NewStore("authoritative-actuation")
	var titleDir, claimDir, redactionDir string
	cwdCalls := 0
	panes := mockTmuxPanesForList([]int{1})
	deps := BulkAssignDependencies{
		ResolveProject: func(context.Context, string, []tmux.Pane) (string, error) { return authoritative, nil },
		Cwd: func() (string, error) {
			cwdCalls++
			return wrongCWD, nil
		},
		FetchBeadTitle: func(_ context.Context, dir, beadID string) (string, error) {
			titleDir = dir
			return "Scoped " + beadID, nil
		},
		ListPanes:                func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		ReadFile:                 func(string) ([]byte, error) { return []byte(defaultBulkAssignTemplate), nil },
		LoadStore:                func(string) (*assignment.AssignmentStore, error) { return store, nil },
		GetBeadAssignmentDetails: bulkOpenAssignmentDetails,
		ClaimBead: func(_ context.Context, dir, beadID, actor string) (bv.BeadClaimResult, error) {
			claimDir = dir
			return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		},
		NewIdempotencyKey: func() (string, error) { return "authoritative-key", nil },
		ResolveAgentName: func(context.Context, string, string, string, string) (string, error) {
			return "ScopedAgent", nil
		},
		ObserveSession:    bulkSafeObserver(panes),
		DispatchDeliverer: dispatchsvc.DelivererFunc(func(context.Context, dispatchsvc.Delivery) error { return nil }),
		LoadRedaction: func(dir string) (redaction.Config, error) {
			redactionDir = dir
			return redaction.Config{Mode: redaction.ModeOff}, nil
		},
	}

	output, err := GetBulkAssign(t.Context(), BulkAssignOptions{
		Session: "authoritative-actuation", AllocationJSON: `{"1":"bd-auth"}`, Deps: &deps,
	})
	if err != nil {
		t.Fatalf("GetBulkAssign: %v", err)
	}
	if !output.Success || len(output.Assignments) != 1 || output.Assignments[0].Status != "assigned" {
		t.Fatalf("bulk assignment output = %+v", output)
	}
	if titleDir != authoritative || claimDir != authoritative || redactionDir != authoritative {
		t.Fatalf("actuation dirs = title %q claim %q redaction %q, want %q", titleDir, claimDir, redactionDir, authoritative)
	}
	if cwdCalls != 0 {
		t.Fatalf("caller CWD resolved %d time(s), want zero", cwdCalls)
	}
}

func TestRobotReservationPreflightFailuresGuaranteeNoLease(t *testing.T) {
	runtime := &robotAgentMailReservationRuntime{
		client: &fakeRobotReservationClient{}, projectKey: "/project", projectID: 7,
		registered: map[string]agentmail.Agent{"KnownAgent": {Name: "KnownAgent", ProjectID: 7}},
	}
	for _, test := range []struct {
		name string
		req  assignment.ReservationRequest
	}{
		{name: "unregistered recipient", req: assignment.ReservationRequest{AgentName: "MissingAgent", RequestedPaths: []string{"internal/**"}}},
		{name: "invalid paths", req: assignment.ReservationRequest{AgentName: "KnownAgent", RequestedPaths: []string{"internal/**", "internal/**"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtime.Reserve(t.Context(), test.req)
			if err == nil || !assignment.IsGuaranteedNoReservation(err) {
				t.Fatalf("Reserve() error = %v, want guaranteed no-reservation failure", err)
			}
		})
	}
}

func TestRobotReservationPartialConflictPreservesLeaseHandles(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	client := &fakeRobotReservationClient{reserveResult: &agentmail.ReservationResult{
		Granted: []agentmail.FileReservation{{
			ID: 41, PathPattern: "internal/robot/**", AgentName: "KnownAgent", ProjectID: 7, Exclusive: true,
			Reason: "bead assignment: bd-partial", ExpiresTS: agentmail.FlexTime{Time: expiresAt},
		}},
		Conflicts: []agentmail.ReservationConflict{{Path: "internal/cli/**", Holders: []string{"OtherAgent"}}},
	}}
	runtime := &robotAgentMailReservationRuntime{
		client: client, projectKey: "/project", projectID: 7,
		registered: map[string]agentmail.Agent{"KnownAgent": {Name: "KnownAgent", ProjectID: 7}},
	}
	lease, err := runtime.Reserve(t.Context(), assignment.ReservationRequest{
		BeadID: "bd-partial", AgentName: "KnownAgent", Target: "%1",
		RequestedPaths: []string{"internal/robot/**", "internal/cli/**"},
	})
	if err == nil || assignment.IsGuaranteedNoReservation(err) {
		t.Fatalf("Reserve() error = %v, want ambiguous partial-lease failure", err)
	}
	if !reflect.DeepEqual(lease.Granted, []string{"internal/robot/**"}) || !reflect.DeepEqual(lease.ReservationIDs, []int{41}) {
		t.Fatalf("partial lease = %+v, want retained release handles", lease)
	}
}

func TestRobotReservationRejectsMalformedGrantReceipts(t *testing.T) {
	valid := agentmail.FileReservation{
		ID: 42, PathPattern: "internal/robot/**", AgentName: "KnownAgent", ProjectID: 7, Exclusive: true,
		Reason: "bead assignment: bd-malformed", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}
	for _, test := range []struct {
		name   string
		mutate func(*agentmail.FileReservation)
	}{
		{name: "wrong reason", mutate: func(row *agentmail.FileReservation) { row.Reason = "bead assignment: other" }},
		{name: "already released", mutate: func(row *agentmail.FileReservation) {
			released := agentmail.FlexTime{Time: time.Now().UTC()}
			row.ReleasedTS = &released
		}},
		{name: "missing expiry", mutate: func(row *agentmail.FileReservation) { row.ExpiresTS = agentmail.FlexTime{} }},
		{name: "expired", mutate: func(row *agentmail.FileReservation) {
			row.ExpiresTS = agentmail.FlexTime{Time: time.Now().UTC().Add(-time.Second)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			runtime := &robotAgentMailReservationRuntime{
				client:     &fakeRobotReservationClient{reserveResult: &agentmail.ReservationResult{Granted: []agentmail.FileReservation{row}}},
				projectKey: "/project", projectID: 7,
				registered: map[string]agentmail.Agent{"KnownAgent": {Name: "KnownAgent", ProjectID: 7}},
			}
			lease, err := runtime.Reserve(t.Context(), assignment.ReservationRequest{
				BeadID: "bd-malformed", AgentName: "KnownAgent", Target: "%1", RequestedPaths: []string{"internal/robot/**"},
			})
			if err == nil {
				t.Fatal("malformed grant was accepted")
			}
			if !reflect.DeepEqual(lease.ReservationIDs, []int{42}) || !reflect.DeepEqual(lease.Granted, []string{"internal/robot/**"}) {
				t.Fatalf("malformed grant lost cleanup handles: %+v", lease)
			}
		})
	}
}

func TestRobotReservationReconciliationDistinguishesAbsentPartialAndReserved(t *testing.T) {
	base := agentmail.FileReservation{
		ID: 51, PathPattern: "internal/robot/**", AgentName: "KnownAgent", ProjectID: 7,
		Exclusive: true, Reason: "bead assignment: bd-reconcile",
		ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}
	second := base
	second.ID = 52
	second.PathPattern = "internal/cli/**"
	missingExpiry := base
	missingExpiry.ExpiresTS = agentmail.FlexTime{}
	request := assignment.ReservationRequest{
		BeadID: "bd-reconcile", AgentName: "KnownAgent", Target: "%1",
		RequestedPaths: []string{"internal/robot/**", "internal/cli/**"},
	}
	for _, test := range []struct {
		name         string
		reservations []agentmail.FileReservation
		want         assignment.ReservationReconciliationState
	}{
		{name: "absent", want: assignment.ReservationReconciliationAbsent},
		{name: "partial", reservations: []agentmail.FileReservation{base}, want: assignment.ReservationReconciliationUnknown},
		{name: "one missing expiry", reservations: []agentmail.FileReservation{missingExpiry, second}, want: assignment.ReservationReconciliationUnknown},
		{name: "reserved", reservations: []agentmail.FileReservation{base, second}, want: assignment.ReservationReconciliationReserved},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &robotAgentMailReservationRuntime{
				client:     &fakeRobotReservationClient{reservations: test.reservations},
				projectKey: "/project", projectID: 7,
			}
			got, err := runtime.ReconcileReservation(t.Context(), request, assignment.LeaseReceipt{})
			if err != nil {
				t.Fatalf("ReconcileReservation: %v", err)
			}
			if got.State != test.want {
				t.Fatalf("state = %q, want %q; lease=%+v", got.State, test.want, got.Lease)
			}
			if test.name == "one missing expiry" && !reflect.DeepEqual(got.Lease.ReservationIDs, []int{51}) {
				t.Fatalf("malformed reconciliation lost cleanup handle: %+v", got.Lease)
			}
			if test.want == assignment.ReservationReconciliationReserved && len(got.Lease.ReservationIDs) != 2 {
				t.Fatalf("reserved lease = %+v", got.Lease)
			}
		})
	}
}

func TestBulkAssignPaneListMapsMissingSessionAndTimeout(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "tmux missing window wording", err: errors.New("can't find window: absent"), code: ErrCodeSessionNotFound},
		{name: "context timeout", err: context.DeadlineExceeded, code: ErrCodeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			startedAt := time.Now().UTC()
			deps := BulkAssignDependencies{
				ListPanes: func(context.Context, string) ([]tmux.Pane, error) { return nil, test.err },
			}
			output, err := GetBulkAssign(t.Context(), BulkAssignOptions{Session: "absent", AllocationJSON: `{}`, Deps: &deps})
			if err != nil {
				t.Fatalf("GetBulkAssign returned transport error: %v", err)
			}
			if output.Success || output.ErrorCode != test.code || output.Assignments == nil {
				t.Fatalf("response=%+v, want success=false code=%s and non-nil assignments", output, test.code)
			}
			timestamp, parseErr := time.Parse(time.RFC3339Nano, output.Timestamp)
			if parseErr != nil || timestamp.Before(startedAt.Add(-time.Second)) || timestamp.After(time.Now().UTC().Add(time.Second)) {
				t.Fatalf("early failure timestamp = %q parsed=%v err=%v", output.Timestamp, timestamp, parseErr)
			}
		})
	}
}

func TestBulkAssignLargeTriage(t *testing.T) {
	var recs []bv.TriageRecommendation
	for i := 0; i < 120; i++ {
		recs = append(recs, bv.TriageRecommendation{
			ID:       fmt.Sprintf("bd-%03d", i),
			Title:    fmt.Sprintf("Task %d", i),
			Status:   "ready",
			Priority: i % 5,
		})
	}
	triage := mockTriage(recs, nil)
	panes := mockPanes("proj", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	plan := planBulkAssignFromBV(BulkAssignOptions{Strategy: "ready"}, BulkAssignDependencies{}, panes, triage, nil)

	if len(plan.Assignments) != 10 {
		t.Fatalf("expected 10 assignments, got %d", len(plan.Assignments))
	}
	if len(plan.UnassignedBeads) != 110 {
		t.Fatalf("expected 110 unassigned beads, got %d", len(plan.UnassignedBeads))
	}

	t.Logf("assignments=%d unassigned=%d", len(plan.Assignments), len(plan.UnassignedBeads))
}

func TestBulkAssignBVSelectionDeduplicatesOverlappingCandidateLists(t *testing.T) {
	triage := mockTriage(
		[]bv.TriageRecommendation{
			{ID: "bd-overlap", Title: "Ready duplicate", Status: "ready", Priority: 1},
			{ID: "bd-ready", Title: "Ready only", Status: "ready", Priority: 2},
		},
		[]bv.BlockerToClear{{ID: "bd-overlap", Title: "Impact duplicate", UnblocksCount: 4, Actionable: true}},
	)
	inProgress := []bv.BeadInProgress{
		{ID: "bd-overlap", Title: "Stale duplicate", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "bd-stale", Title: "Stale only", UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	selected := selectBulkAssignBeads("balanced", buildBulkAssignCandidates(triage, inProgress))

	want := []string{"bd-ready", "bd-overlap", "bd-stale"}
	got := make([]string, 0, len(selected))
	for _, bead := range selected {
		got = append(got, bead.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("balanced selection IDs = %v, want unique stable order %v", got, want)
	}
	if selected[1].Source != bulkSourceStale || !selected[1].UpdatedAt.Equal(inProgress[0].UpdatedAt) {
		t.Fatalf("overlap source=%q updated_at=%s, want newer stale state", selected[1].Source, selected[1].UpdatedAt)
	}
}

func TestBulkAssignNewerInProgressSnapshotOverridesCachedTriageForEveryStrategy(t *testing.T) {
	triage := mockTriage(
		[]bv.TriageRecommendation{
			{ID: "bd-overlap", Title: "Cached ready", Status: "ready", Priority: 0},
			{ID: "bd-ready", Title: "Current ready", Status: "ready", Priority: 1},
		},
		[]bv.BlockerToClear{{ID: "bd-overlap", Title: "Cached impact", UnblocksCount: 99, Actionable: true}},
	)
	inProgress := []bv.BeadInProgress{{
		ID: "bd-overlap", Title: "Current stale", UpdatedAt: time.Now().UTC().Add(-48 * time.Hour),
	}}
	candidates := buildBulkAssignCandidates(triage, inProgress)
	tests := []struct {
		strategy string
		wantID   string
		want     bulkBeadSource
	}{
		{strategy: "impact", wantID: "bd-ready", want: bulkSourceReady},
		{strategy: "ready", wantID: "bd-ready", want: bulkSourceReady},
		{strategy: "stale", wantID: "bd-overlap", want: bulkSourceStale},
		{strategy: "balanced", wantID: "bd-ready", want: bulkSourceReady},
	}
	for _, test := range tests {
		t.Run(test.strategy, func(t *testing.T) {
			selected := selectBulkAssignBeads(test.strategy, candidates)
			if len(selected) == 0 || selected[0].ID != test.wantID || selected[0].Source != test.want {
				t.Fatalf("selection=%+v, want first %s via %s", selected, test.wantID, test.want)
			}
			for _, bead := range selected {
				if bead.ID == "bd-overlap" && bead.Source != bulkSourceStale {
					t.Fatalf("strategy %s routed overlap through %s", test.strategy, bead.Source)
				}
			}
		})
	}
}

func TestBulkAssignConcurrentSafety(t *testing.T) {
	triage := mockTriage([]bv.TriageRecommendation{{ID: "bd-1", Title: "Test", Status: "ready", Priority: 1}}, nil)
	inProgress := []bv.BeadInProgress{{ID: "bd-2", Title: "Stale", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	candidates := buildBulkAssignCandidates(triage, inProgress)
	before := candidates

	_ = selectBalancedBeads(candidates)
	if !reflect.DeepEqual(before, candidates) {
		t.Fatalf("expected candidates to remain unchanged")
	}
}

// helpers

type fakeRobotReservationClient struct {
	reserveResult *agentmail.ReservationResult
	reserveErr    error
	reservations  []agentmail.FileReservation
	listErr       error
}

func (f *fakeRobotReservationClient) EnsureProject(context.Context, string) (*agentmail.Project, error) {
	return &agentmail.Project{ID: 7, HumanKey: "/project"}, nil
}

func (f *fakeRobotReservationClient) ListAgents(context.Context, string) ([]agentmail.Agent, error) {
	return []agentmail.Agent{{Name: "KnownAgent", ProjectID: 7}}, nil
}

func (f *fakeRobotReservationClient) ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error) {
	return append([]agentmail.FileReservation(nil), f.reservations...), f.listErr
}

func (f *fakeRobotReservationClient) ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	return f.reserveResult, f.reserveErr
}

func mockTriage(recs []bv.TriageRecommendation, blockers []bv.BlockerToClear) *bv.TriageResponse {
	return &bv.TriageResponse{
		Triage: bv.TriageData{
			Recommendations: recs,
			BlockersToClear: blockers,
		},
	}
}

func mockPanes(session string, indices []int) []bulkPane {
	panes := make([]bulkPane, 0, len(indices))
	for _, idx := range indices {
		panes = append(panes, bulkPane{Ref: tmux.PaneRef{ID: fmt.Sprintf("%%%d", idx), WindowIndex: 0, PaneIndex: idx}, AgentType: "claude"})
	}
	sort.Slice(panes, func(i, j int) bool { return panes[i].Ref.PaneIndex < panes[j].Ref.PaneIndex })
	return panes
}

func mockTmuxPanesForList(indices []int) []tmux.Pane {
	panes := make([]tmux.Pane, 0, len(indices))
	for _, idx := range indices {
		panes = append(panes, tmux.Pane{ID: fmt.Sprintf("%%%d", idx), WindowIndex: 0, Index: idx, Title: fmt.Sprintf("proj__cc_%d", idx)})
	}
	return panes
}

// GH#251 phase 2: grok now supports automated prompt delivery, so a mixed
// claude+grok batch passes the batch-level preflight instead of failing closed.
func TestValidateBulkAssignPromptDeliveryAcceptsMixedGrokBatch(t *testing.T) {
	assignments := []BulkAssignAssignment{
		{Pane: "0.1", AgentType: "claude", Status: "planned"},
		{Pane: "0.2", AgentType: "grok-build", Status: "planned"},
	}
	if err := validateBulkAssignPromptDelivery(assignments); err != nil {
		t.Fatalf("validateBulkAssignPromptDelivery() error = %v, want nil (grok delivery is supported)", err)
	}
	if got := bulkAssignTMUXAgentType("grok-build"); got != tmux.AgentGrok {
		t.Fatalf("bulkAssignTMUXAgentType(grok-build) = %s, want grok", got)
	}

	assignments[1].Status = "failed"
	if err := validateBulkAssignPromptDelivery(assignments); err != nil {
		t.Fatalf("already-failed Grok assignment blocked remaining supported plan: %v", err)
	}
}

func mustParseAllocation(t *testing.T, allocation string) map[string]string {
	parsed, err := parseBulkAssignAllocation(allocation)
	if err != nil {
		t.Fatalf("allocation parse failed: %v", err)
	}
	return parsed
}

func bulkAtomicTestDeps(t *testing.T, session string, plan bulkAssignPlan, deps BulkAssignDependencies) BulkAssignDependencies {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store := assignment.NewStore(session)
	var mu sync.Mutex
	owners := make(map[string]string)
	key := 0

	deps.LoadStore = func(string) (*assignment.AssignmentStore, error) { return store, nil }
	deps.ClaimBead = func(_ context.Context, _ string, beadID, actor string) (bv.BeadClaimResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if owner := owners[beadID]; owner != "" && owner != actor {
			return bv.BeadClaimResult{}, bv.ErrBeadAlreadyClaimed
		}
		owners[beadID] = actor
		return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}
	deps.ClaimStaleBead = func(ctx context.Context, dir, beadID, actor string, _ time.Time) (bv.BeadClaimResult, error) {
		return deps.ClaimBead(ctx, dir, beadID, actor)
	}
	if deps.GetBeadAssignmentDetails == nil {
		staleIDs := make(map[string]struct{})
		for _, planned := range plan.Assignments {
			if planned.stale {
				staleIDs[planned.Bead] = struct{}{}
			}
		}
		deps.GetBeadAssignmentDetails = func(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
			status := "open"
			if _, stale := staleIDs[beadID]; stale {
				status = "in_progress"
			}
			return &bv.BeadAssignmentDetails{ID: beadID, Status: status}, nil
		}
	}
	deps.NewIdempotencyKey = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		key++
		return fmt.Sprintf("bulk-test-key-%d", key), nil
	}
	deps.LoadRedaction = func(string) (redaction.Config, error) {
		return redaction.Config{Mode: redaction.ModeOff}, nil
	}
	if deps.ResolveAgentName == nil {
		deps.ResolveAgentName = func(_ context.Context, _, _, paneID, _ string) (string, error) {
			return "TestAgent-" + strings.TrimPrefix(paneID, "%"), nil
		}
	}
	if deps.Cwd == nil {
		workDir := t.TempDir()
		deps.Cwd = func() (string, error) { return workDir, nil }
	}
	if deps.ListPanes == nil {
		panes := make([]tmux.Pane, 0, len(plan.Assignments))
		for _, planned := range plan.Assignments {
			panes = append(panes, tmux.Pane{
				ID: planned.PaneID, WindowIndex: 0, Index: planned.paneIndex,
				Type: bulkAssignTMUXAgentType(planned.AgentType),
			})
		}
		deps.ListPanes = func(context.Context, string) ([]tmux.Pane, error) {
			return append([]tmux.Pane(nil), panes...), nil
		}
	}
	deps.ObserveSession = func(_ context.Context, observedSession string) (statuspkg.SessionObservation, error) {
		panes, err := deps.ListPanes(context.Background(), observedSession)
		if err != nil {
			return statuspkg.SessionObservation{}, err
		}
		return bulkSafeObservation(observedSession, panes), nil
	}
	return deps
}

func bulkOpenAssignmentDetails(_ context.Context, _ string, beadID string) (*bv.BeadAssignmentDetails, error) {
	return &bv.BeadAssignmentDetails{ID: beadID, Status: "open"}, nil
}

func bulkSafeObserver(panes []tmux.Pane) func(context.Context, string) (statuspkg.SessionObservation, error) {
	return func(_ context.Context, session string) (statuspkg.SessionObservation, error) {
		return bulkSafeObservation(session, panes), nil
	}
}

func bulkSafeObservation(session string, panes []tmux.Pane) statuspkg.SessionObservation {
	observedAt := time.Now().UTC()
	observation := statuspkg.SessionObservation{Session: session, ObservedAt: observedAt, Complete: true}
	for _, pane := range panes {
		observation.Panes = append(observation.Panes, statuspkg.PaneObservation{
			Pane: pane.Ref(), Metadata: pane,
			Current: statuspkg.StateObservation{
				Status:     statuspkg.AgentStatus{PaneID: pane.ID, State: statuspkg.StateIdle, UpdatedAt: observedAt},
				ObservedAt: observedAt, Freshness: statuspkg.FreshnessFresh, Confidence: 1,
			},
		})
	}
	return observation
}

func osReadFile(path string) ([]byte, error) {
	return osReadFileImpl(path)
}

var osReadFileImpl = func(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// A pane whose agent type cannot receive an automated prompt must be excluded at
// planning time. Planning it and rejecting it later failed the whole batch:
// validateBulkAssignPromptDelivery returns a batch-level error on the first such
// target, every other assignment is marked NOT_IMPLEMENTED, and
// ExitCodeForResponse maps that to exit 2 — documented as "skip gracefully". One
// Grok pane thus made every healthy pane in the session unassignable.
// GH#251 phase 2: grok panes are deliverable, so the filter keeps them in the
// plan alongside claude and codex (only non-agent panes are excluded).
func TestFilterBulkAssignPanesIncludesGrokAgents(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Title: "claude", Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Title: "codex", Type: tmux.AgentCodex},
		{ID: "%3", Index: 3, Title: "grok", Type: tmux.AgentGrok},
		{ID: "%4", Index: 4, Title: "shell", Type: tmux.AgentUser},
	}

	filtered, err := filterBulkAssignPanes(panes, nil)
	if err != nil {
		t.Fatalf("filterBulkAssignPanes: %v", err)
	}

	got := make([]string, 0, len(filtered))
	for _, p := range filtered {
		got = append(got, p.AgentType)
	}
	t.Logf("BULK_ASSIGN_TEST: planned agent types = %v", got)

	if len(filtered) != 3 {
		t.Fatalf("planned %d panes (%v), want 3 (claude, codex, and grok)", len(filtered), got)
	}
	sawGrok := false
	for _, p := range filtered {
		if normalizeAgentType(p.AgentType) == "grok" {
			sawGrok = true
		}
		if err := bulkAssignTMUXAgentType(p.AgentType).ValidateAutomatedPromptDelivery(); err != nil {
			t.Fatalf("planned pane %s cannot receive a prompt: %v", p.Ref.StableKey(), err)
		}
	}
	if !sawGrok {
		t.Fatal("grok pane was excluded from the plan; grok delivery is now supported")
	}

	// The surviving plan must pass the batch-level delivery check that used to
	// reject everything.
	planned := make([]BulkAssignAssignment, 0, len(filtered))
	for _, p := range filtered {
		planned = append(planned, BulkAssignAssignment{Pane: p.Ref.StableKey(), AgentType: p.AgentType})
	}
	if err := validateBulkAssignPromptDelivery(planned); err != nil {
		t.Fatalf("the surviving plan was rejected by the batch delivery check: %v", err)
	}
}

// sameResolverPath compares two paths after symlink resolution (macOS temp
// dirs live behind the /var → /private/var symlink).
func sameResolverPath(t *testing.T, got, want string) bool {
	t.Helper()
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotResolved = filepath.Clean(got)
	}
	wantResolved, err := filepath.EvalSymlinks(want)
	if err != nil {
		wantResolved = filepath.Clean(want)
	}
	return gotResolved == wantResolved
}

// runResolverGitCommand runs git in dir for linked-worktree resolver tests,
// skipping the test when git itself is unavailable.
func runResolverGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v failed: %v\n%s", args, err, out)
	}
}

// setupResolverRepoWithWorktrees creates a real git repo at a temp base
// checkout plus n linked worktrees under base/.ntm/worktrees/<session>/cod_N,
// mirroring the `ntm spawn --worktrees` topology from issue #252.
func setupResolverRepoWithWorktrees(t *testing.T, session string, n int) (base string, worktrees []string) {
	t.Helper()
	base = t.TempDir()
	runResolverGitCommand(t, base, "init", "-b", "main")
	runResolverGitCommand(t, base, "config", "user.email", "test@test.com")
	runResolverGitCommand(t, base, "config", "user.name", "Test")
	runResolverGitCommand(t, base, "commit", "--allow-empty", "-m", "init")
	for i := 1; i <= n; i++ {
		wt := filepath.Join(base, ".ntm", "worktrees", session, fmt.Sprintf("cod_%d", i))
		runResolverGitCommand(t, base, "worktree", "add", "-b", fmt.Sprintf("cod_%d", i), wt)
		worktrees = append(worktrees, wt)
	}
	return base, worktrees
}

// TestResolveLiveSessionProjectAcceptsLinkedWorktrees — issue #252: a base
// checkout plus its linked worktrees are one physical repository (they share
// one git common directory), so the resolver must unify them and return the
// base checkout as the canonical project root instead of failing closed with
// "panes span multiple project roots".
func TestResolveLiveSessionProjectAcceptsLinkedWorktrees(t *testing.T) {
	const session = "worktree-session"
	base, worktrees := setupResolverRepoWithWorktrees(t, session, 3)

	paneDirs := map[string]string{"%1": base}
	panes := []tmux.Pane{{ID: "%1"}}
	for i, wt := range worktrees {
		paneID := fmt.Sprintf("%%%d", i+2)
		paneDirs[paneID] = wt
		panes = append(panes, tmux.Pane{ID: paneID})
	}

	got, err := ResolveLiveSessionProjectContext(t.Context(), session, panes, func(_ context.Context, paneID string) (string, error) {
		return paneDirs[paneID], nil
	})
	if err != nil {
		t.Fatalf("linked-worktree topology rejected: %v", err)
	}
	if !sameResolverPath(t, got, base) {
		t.Fatalf("resolved project = %q, want base checkout %q", got, base)
	}
}

// TestResolveLiveSessionProjectWorktreesOnlyResolveToBase — even when no pane
// sits in the base checkout, panes that are all linked worktrees of one repo
// must resolve to that repo's base checkout, not an arbitrary worker worktree.
func TestResolveLiveSessionProjectWorktreesOnlyResolveToBase(t *testing.T) {
	const session = "worktree-only-session"
	base, worktrees := setupResolverRepoWithWorktrees(t, session, 2)

	paneDirs := make(map[string]string)
	panes := make([]tmux.Pane, 0, len(worktrees))
	for i, wt := range worktrees {
		paneID := fmt.Sprintf("%%%d", i+1)
		paneDirs[paneID] = wt
		panes = append(panes, tmux.Pane{ID: paneID})
	}

	got, err := ResolveLiveSessionProjectContext(t.Context(), session, panes, func(_ context.Context, paneID string) (string, error) {
		return paneDirs[paneID], nil
	})
	if err != nil {
		t.Fatalf("worktree-only topology rejected: %v", err)
	}
	if !sameResolverPath(t, got, base) {
		t.Fatalf("resolved project = %q, want base checkout %q", got, base)
	}
}

// TestResolveLiveSessionProjectRejectsDistinctRealRepos — the linked-worktree
// unification must not weaken the genuine fail-closed case: two unrelated
// real repositories have different git common directories and stay rejected.
func TestResolveLiveSessionProjectRejectsDistinctRealRepos(t *testing.T) {
	first, _ := setupResolverRepoWithWorktrees(t, "distinct-a", 0)
	second, _ := setupResolverRepoWithWorktrees(t, "distinct-b", 0)

	paneDirs := map[string]string{"%1": first, "%2": second}
	panes := []tmux.Pane{{ID: "%1"}, {ID: "%2"}}

	_, err := ResolveLiveSessionProjectContext(t.Context(), "distinct-repos", panes, func(_ context.Context, paneID string) (string, error) {
		return paneDirs[paneID], nil
	})
	if err == nil || !strings.Contains(err.Error(), "panes span multiple project roots") {
		t.Fatalf("distinct real repos error = %v, want multiple-project-roots rejection", err)
	}
}
