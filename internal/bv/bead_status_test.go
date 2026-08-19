package bv

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

func TestParseBeadStatusOutput_Object(t *testing.T) {
	t.Parallel()

	status, err := parseBeadStatusOutput(`{"id":"bd-123","status":"in_progress"}`)
	if err != nil {
		t.Fatalf("parseBeadStatusOutput returned error: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("status = %q, want %q", status, "in_progress")
	}
}

func TestParseBeadStatusOutput_Array(t *testing.T) {
	t.Parallel()

	status, err := parseBeadStatusOutput(`[{"id":"bd-123","status":"closed"}]`)
	if err != nil {
		t.Fatalf("parseBeadStatusOutput returned error: %v", err)
	}
	if status != "closed" {
		t.Fatalf("status = %q, want %q", status, "closed")
	}
}

func TestParseBeadStatusOutput_MissingStatus(t *testing.T) {
	t.Parallel()

	_, err := parseBeadStatusOutput(`{"id":"bd-123"}`)
	if err == nil {
		t.Fatal("expected error for missing status")
	}
}

func TestParseBeadStatusOutput_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := parseBeadStatusOutput(`{`)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseBeadAssignmentDetailsOutput(t *testing.T) {
	t.Parallel()

	details, err := parseBeadAssignmentDetailsOutput(`[{
		"id":"ntm-target",
		"title":"Exact assignment target",
		"status":"open",
		"priority":2,
		"assignee":"  ExactActor  ",
		"labels":["operator-gated","backend","backend"],
		"dependencies":[
			{"id":"ntm-open-b","status":"in_progress","dependency_type":"blocks"},
			{"id":"ntm-closed","status":"closed","dependency_type":"blocks"},
			{"id":"ntm-parent","status":"open","dependency_type":"parent-child"},
			{"id":"ntm-open-a","status":"open","dependency_type":"blocks"},
			{"id":"ntm-conditional","status":"open","dependency_type":"conditional-blocks"},
			{"id":"ntm-wait","status":"open","dependency_type":"waits-for"},
			{"id":"ntm-open-a","status":"open","dependency_type":"blocks"}
		]
	}]`)
	if err != nil {
		t.Fatalf("parseBeadAssignmentDetailsOutput: %v", err)
	}
	if details.ID != "ntm-target" || details.Title != "Exact assignment target" || details.Status != "open" || details.Priority != 2 || details.Assignee != "ExactActor" {
		t.Fatalf("details=%+v", details)
	}
	if want := []string{"ntm-conditional", "ntm-open-a", "ntm-open-b", "ntm-wait"}; !reflect.DeepEqual(details.BlockedBy, want) {
		t.Fatalf("blocked_by=%v, want %v", details.BlockedBy, want)
	}
	if want := []string{"backend", "operator-gated"}; !reflect.DeepEqual(details.Labels, want) {
		t.Fatalf("labels=%v, want %v", details.Labels, want)
	}
	wantDependencies := []BeadDependencyState{
		{ID: "ntm-closed", Status: "closed"},
		{ID: "ntm-conditional", Status: "open"},
		{ID: "ntm-open-a", Status: "open"},
		{ID: "ntm-open-b", Status: "in_progress"},
		{ID: "ntm-wait", Status: "open"},
	}
	if !reflect.DeepEqual(details.BlockingDependencies, wantDependencies) {
		t.Fatalf("blocking dependency snapshot=%+v, want %+v", details.BlockingDependencies, wantDependencies)
	}
}

func TestParseBeadAssignmentDetailsOutputReadyGates(t *testing.T) {
	t.Parallel()

	details, err := parseBeadAssignmentDetailsOutput(`[{
		"id":"ntm-wisp-ready-gates",
		"title":"Not actually ready",
		"status":"open",
		"defer_until":"2099-01-01T00:00:00Z",
		"pinned":true,
		"ephemeral":true,
		"is_template":true
	}]`)
	if err != nil {
		t.Fatalf("parse ready gates: %v", err)
	}
	if details.DeferUntil == nil || details.DeferUntil.Year() != 2099 || !details.Pinned || !details.Ephemeral || !details.Template || !details.Wisp {
		t.Fatalf("ready gates=%+v", details)
	}
}

func TestParseBeadAssignmentDetailsOutputRejectsAmbiguousOrMalformedRows(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"empty array":           `[]`,
		"multiple rows":         `[{"id":"a","status":"open"},{"id":"b","status":"open"}]`,
		"missing id":            `{"status":"open"}`,
		"missing status":        `{"id":"a"}`,
		"missing dependency id": `{"id":"a","status":"open","dependencies":[{"status":"open","dependency_type":"blocks"}]}`,
		"invalid defer time":    `{"id":"a","status":"open","defer_until":"tomorrow-ish"}`,
		"invalid json":          `{`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseBeadAssignmentDetailsOutput(input); err == nil {
				t.Fatalf("parseBeadAssignmentDetailsOutput(%q) error=nil", input)
			}
		})
	}
}

func TestBlockingDependencyStatesRetainsTerminalDependencies(t *testing.T) {
	t.Parallel()

	states, err := blockingDependencyStates([]beadShowDependency{
		{ID: "ntm-open", Status: "open", DependencyType: "blocks"},
		{ID: "ntm-closed", Status: "closed", DependencyType: "conditional-blocks"},
		{ID: "ntm-tombstone", Status: "tombstone", DependencyType: "waits-for"},
		{ID: "ntm-progress", Status: "in_progress", DependencyType: "blocks"},
		{ID: "ntm-parent", Status: "open", DependencyType: "parent-child"},
		{ID: "ntm-closed", Status: "closed", DependencyType: "blocks"},
	})
	if err != nil {
		t.Fatalf("blockingDependencyStates: %v", err)
	}
	want := []BeadDependencyState{
		{ID: "ntm-closed", Status: "closed"},
		{ID: "ntm-open", Status: "open"},
		{ID: "ntm-progress", Status: "in_progress"},
		{ID: "ntm-tombstone", Status: "tombstone"},
	}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("dependency states=%+v, want %+v", states, want)
	}
}

func TestIsBlockingDependencyType(t *testing.T) {
	t.Parallel()
	for _, dependencyType := range []string{"blocks", "conditional-blocks", "waits-for", " BLOCKS "} {
		if !IsBlockingDependencyType(dependencyType) {
			t.Errorf("IsBlockingDependencyType(%q)=false, want true", dependencyType)
		}
	}
	for _, dependencyType := range []string{"", "parent-child", "relates-to", "external"} {
		if IsBlockingDependencyType(dependencyType) {
			t.Errorf("IsBlockingDependencyType(%q)=true, want false", dependencyType)
		}
	}
}

func TestBlockingDependentStatesIsUncappedAndSkipsExternalEndpoints(t *testing.T) {
	t.Parallel()
	rows := make([]beadShowDependency, 0, 106)
	for i := 0; i < 105; i++ {
		dependencyType := []string{"blocks", "conditional-blocks", "waits-for"}[i%3]
		rows = append(rows, beadShowDependency{
			ID: fmt.Sprintf("ntm-dependent-%03d", i), Title: fmt.Sprintf("Dependent %d", i),
			Status: "open", Priority: i % 5, DependencyType: dependencyType,
		})
	}
	rows = append(rows,
		beadShowDependency{ID: "external:other/repo:task", Status: "open", DependencyType: "blocks"},
		beadShowDependency{ID: "ntm-parent", Status: "open", DependencyType: "parent-child"},
	)
	states, err := blockingDependentStates(rows)
	if err != nil {
		t.Fatalf("blockingDependentStates: %v", err)
	}
	if len(states) != 105 {
		t.Fatalf("uncapped dependent count=%d, want 105", len(states))
	}
	if states[0].ID != "ntm-dependent-000" || states[104].ID != "ntm-dependent-104" || states[104].Priority != 4 {
		t.Fatalf("dependent boundary rows=%+v ... %+v", states[0], states[104])
	}
}

func TestBlockingDependencyStatesRejectsAmbiguousDependencies(t *testing.T) {
	t.Parallel()

	for name, dependencies := range map[string][]beadShowDependency{
		"missing id":     {{Status: "open", DependencyType: "blocks"}},
		"missing status": {{ID: "ntm-missing", DependencyType: "blocks"}},
		"conflicting duplicate": {
			{ID: "ntm-race", Status: "open", DependencyType: "blocks"},
			{ID: "ntm-race", Status: "closed", DependencyType: "blocks"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if states, err := blockingDependencyStates(dependencies); err == nil {
				t.Fatalf("blockingDependencyStates=%+v, want error", states)
			}
		})
	}
}

func TestParseBeadClaimOutput(t *testing.T) {
	t.Parallel()

	claim, err := parseBeadClaimOutput(`[{
		"id":"ntm-123",
		"title":"Atomic assignment",
		"status":"in_progress",
		"priority":1,
		"updated_at":"2026-07-11T12:00:00Z"
	}]`)
	if err != nil {
		t.Fatalf("parseBeadClaimOutput: %v", err)
	}
	if claim.ID != "ntm-123" || claim.Status != "in_progress" {
		t.Fatalf("claim=%+v", claim)
	}
	if claim.Title != "Atomic assignment" {
		t.Fatalf("claim title=%q", claim.Title)
	}
}

func TestParseBeadClaimOutputRejectsNonClaimedState(t *testing.T) {
	t.Parallel()

	if _, err := parseBeadClaimOutput(`[{"id":"ntm-123","status":"open"}]`); err == nil {
		t.Fatal("expected non-in_progress claim output to fail")
	}
	if _, err := parseBeadClaimOutput(`[]`); err == nil {
		t.Fatal("expected empty claim output to fail")
	}
}

func TestBeadClaimArgsUseAtomicPrimitive(t *testing.T) {
	t.Parallel()

	want := []string{"update", "ntm-123", "--claim", "--actor", "BlueLake/ntm-key", "--json"}
	if got := beadClaimArgs("ntm-123", "BlueLake/ntm-key"); !reflect.DeepEqual(got, want) {
		t.Fatalf("beadClaimArgs=%q, want %q", got, want)
	}
}

func TestOperatorGatedLabelsCanonicalVocabulary(t *testing.T) {
	t.Parallel()

	want := []string{
		"blocked-on-ivan",
		"blocked-on-operator",
		"business-input",
		"human-gated",
		"human-input",
		"needs-operator",
		"operator-action",
		"operator-gated",
	}
	got := OperatorGatedLabels()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OperatorGatedLabels()=%v, want %v", got, want)
	}
	for _, label := range want {
		if !IsOperatorGatedLabelInPolicy(label, nil) || !IsOperatorGatedLabelInPolicy("  "+strings.ToUpper(label)+"  ", nil) {
			t.Fatalf("canonical operator label %q was not recognized after normalization", label)
		}
	}
	for _, label := range []string{"", "backend", "operator", "blocked"} {
		if IsOperatorGatedLabelInPolicy(label, nil) {
			t.Fatalf("non-gated label %q was classified as operator gated", label)
		}
	}
	got[0] = "mutated"
	if fresh := OperatorGatedLabels(); !reflect.DeepEqual(fresh, want) {
		t.Fatalf("caller mutated canonical operator labels: %v", fresh)
	}
}

func TestProjectOperatorGatedLabelsRemainIsolatedUnderConcurrentAccess(t *testing.T) {
	projects := []struct {
		dir   string
		gate  string
		other string
	}{
		{dir: t.TempDir(), gate: "project-alpha-approval", other: "project-beta-approval"},
		{dir: t.TempDir(), gate: "project-beta-approval", other: "project-alpha-approval"},
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(projects))
	for _, project := range projects {
		project := project
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ConfigureProjectOperatorGatedLabels(project.dir, []string{project.gate}); err != nil {
				errs <- err
				return
			}
			for i := 0; i < 1000; i++ {
				if !IsOperatorGatedLabelForProject(project.dir, project.gate) {
					errs <- fmt.Errorf("project %s lost its configured gate %s", project.dir, project.gate)
					return
				}
				if IsOperatorGatedLabelForProject(project.dir, project.other) {
					errs <- fmt.Errorf("project %s inherited unrelated gate %s", project.dir, project.other)
					return
				}
				if !IsOperatorGatedLabelForProject(project.dir, "operator-gated") {
					errs <- fmt.Errorf("project %s lost the built-in gate vocabulary", project.dir)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectOperatorGatedLabelsUseAuthoritativeRootAliases(t *testing.T) {
	originalGlobal := OperatorGatedLabels()
	ConfigureOperatorGatedLabels([]string{"global-only-gate"})
	t.Cleanup(func() { ConfigureOperatorGatedLabels(originalGlobal) })

	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "internal", "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested project directory: %v", err)
	}
	if err := ConfigureProjectOperatorGatedLabels(projectDir, []string{"project-only-gate"}); err != nil {
		t.Fatalf("configure project gate: %v", err)
	}

	for _, alias := range []string{projectDir, nestedDir} {
		if !IsOperatorGatedLabelForProject(alias, "project-only-gate") {
			t.Fatalf("project gate missing through alias %q", alias)
		}
		if IsOperatorGatedLabelForProject(alias, "global-only-gate") {
			t.Fatalf("alias %q fell back to conflicting process-global policy", alias)
		}
	}

	t.Chdir(nestedDir)
	if !IsOperatorGatedLabelForProject("", "project-only-gate") {
		t.Fatal("project gate missing through empty cwd alias")
	}
	if IsOperatorGatedLabelForProject("", "global-only-gate") {
		t.Fatal("empty cwd alias fell back to conflicting process-global policy")
	}
}

func TestValidateBeadAssignmentAuthorizationAllowsExplicitStaleOwnershipState(t *testing.T) {
	details := &BeadAssignmentDetails{ID: "ntm-stale", Status: "in_progress"}
	authorization := BeadAssignmentAuthorization{
		BeadID: "ntm-stale", AllowUnassignedInProgress: true,
	}
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, authorization, []string{"project-approval"}); err != nil {
		t.Fatalf("authorize unassigned in_progress stale work: %v", err)
	}

	withoutAllowance := authorization
	withoutAllowance.AllowUnassignedInProgress = false
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, withoutAllowance, nil); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("unapproved unassigned in_progress error=%v, want ineligible", err)
	}

	owned := *details
	owned.Assignee = "OtherActor"
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(&owned, authorization, nil); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("owned stale work error=%v, want ineligible", err)
	}

	gated := *details
	gated.Labels = []string{"project-approval"}
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(&gated, authorization, []string{"project-approval"}); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("gated stale work error=%v, want ineligible", err)
	}
}

func TestClaimBeadForAssignmentTransactionRejectsDirectStartBlockers(t *testing.T) {
	requireRealBR(t)

	for _, dependencyType := range []string{"blocks", "conditional-blocks", "waits-for"} {
		t.Run(dependencyType, func(t *testing.T) {
			dir := t.TempDir()
			runRealBR(t, dir, "init", "--quiet")
			targetID := createRealBRBead(t, dir, "assignment claim target "+dependencyType)
			blockerID := createRealBRBead(t, dir, "assignment claim blocker "+dependencyType)
			runRealBR(t, dir, "dep", "add", targetID, blockerID, "--type", dependencyType, "--json")

			result, changed, err := claimBeadForAssignmentTransaction(
				t.Context(), realBRDatabasePath(t, dir), targetID, "AtomicActor", OperatorGatedLabels(),
			)
			var eligibilityErr *AssignmentEligibilityError
			if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) {
				t.Fatalf("claim result=%+v changed=%v error=%v, want typed ineligible error", result, changed, err)
			}
			if changed || eligibilityErr.BeadID != targetID || eligibilityErr.Status != "open" ||
				!reflect.DeepEqual(eligibilityErr.UnresolvedBlockers, []string{blockerID}) || len(eligibilityErr.OperatorLabels) != 0 {
				t.Fatalf("eligibility error=%+v changed=%v", eligibilityErr, changed)
			}
			assertRealBRStatusAndAssignee(t, dir, targetID, "open", "")
		})
	}
}

func TestClaimBeadForAssignmentTransactionRejectsEveryOperatorGate(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	targetID := createRealBRBead(t, dir, "assignment operator gate target")
	for _, label := range OperatorGatedLabels() {
		runRealBR(t, dir, "update", targetID, "--add-label", label, "--json")
	}

	result, changed, err := claimBeadForAssignmentTransaction(
		t.Context(), realBRDatabasePath(t, dir), targetID, "AtomicActor", OperatorGatedLabels(),
	)
	var eligibilityErr *AssignmentEligibilityError
	if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) {
		t.Fatalf("claim result=%+v changed=%v error=%v, want typed ineligible error", result, changed, err)
	}
	if changed || !reflect.DeepEqual(eligibilityErr.OperatorLabels, OperatorGatedLabels()) || len(eligibilityErr.UnresolvedBlockers) != 0 {
		t.Fatalf("eligibility error=%+v changed=%v", eligibilityErr, changed)
	}
	assertRealBRStatusAndAssignee(t, dir, targetID, "open", "")
}

func TestClaimBeadForAssignmentMirrorsStartBlockerSemantics(t *testing.T) {
	requireRealBR(t)

	t.Run("parent-child rollup does not block start", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		parentID := createRealBRBead(t, dir, "claimable parent")
		childID := createRealBRBead(t, dir, "open child")
		runRealBR(t, dir, "dep", "add", childID, parentID, "--type", "parent-child", "--json")
		claim, err := ClaimBeadForAssignment(t.Context(), dir, parentID, "AtomicActor")
		if err != nil || claim.ID != parentID || claim.Status != "in_progress" {
			t.Fatalf("parent claim=%+v error=%v", claim, err)
		}
		assertRealBRStatusAndAssignee(t, dir, parentID, "in_progress", "AtomicActor")
	})

	t.Run("closed direct blocker does not block start", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		targetID := createRealBRBead(t, dir, "claimable after blocker closes")
		blockerID := createRealBRBead(t, dir, "closed blocker")
		runRealBR(t, dir, "dep", "add", targetID, blockerID, "--type", "blocks", "--json")
		runRealBR(t, dir, "close", blockerID, "--reason", "test prerequisite complete", "--json")
		claim, err := ClaimBeadForAssignment(t.Context(), dir, targetID, "AtomicActor")
		if err != nil || claim.ID != targetID || claim.Status != "in_progress" {
			t.Fatalf("unblocked claim=%+v error=%v", claim, err)
		}
	})
}

func TestClaimBeadForAssignmentStatusConflictAndIdempotency(t *testing.T) {
	requireRealBR(t)

	t.Run("terminal status is ineligible", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "terminal assignment claim")
		runRealBR(t, dir, "close", beadID, "--reason", "terminal test", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "AtomicActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) || eligibilityErr.Status != "closed" {
			t.Fatalf("terminal claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "closed", "")
	})

	t.Run("other owner is conflict", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "assignment owner conflict")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "FirstActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "OtherActor"); !errors.Is(err, ErrBeadAlreadyClaimed) || errors.Is(err, ErrBeadAssignmentIneligible) {
			t.Fatalf("other-owner claim error=%v, want conflict only", err)
		}
	})

	t.Run("same owner retry is idempotent while gates remain clear", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "idempotent assignment claim")
		first, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		second, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		if err != nil || second.ID != first.ID || second.Actor != first.Actor || second.Status != "in_progress" {
			t.Fatalf("idempotent claim=%+v first=%+v error=%v", second, first, err)
		}
	})

	t.Run("same owner retry rechecks operator gates", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "gated assignment recovery")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		runRealBR(t, dir, "update", beadID, "--add-label", "operator-gated", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) ||
			!reflect.DeepEqual(eligibilityErr.OperatorLabels, []string{"operator-gated"}) {
			t.Fatalf("same-owner gated claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "in_progress", "StableActor")
	})

	t.Run("same owner retry rechecks blockers", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "blocked assignment recovery")
		blockerID := createRealBRBead(t, dir, "new blocker")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		runRealBR(t, dir, "dep", "add", beadID, blockerID, "--type", "blocks", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) ||
			!reflect.DeepEqual(eligibilityErr.UnresolvedBlockers, []string{blockerID}) {
			t.Fatalf("same-owner blocked claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "in_progress", "StableActor")
	})
}

func TestClaimStaleBeadForAssignmentAdoptsOnlyUnownedInProgressWork(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const actor = "StaleRecoveryActor"
	staleUpdatedAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)

	openID := createRealBRBead(t, dir, "open work is not stale")
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, openID, actor, staleUpdatedAt); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("open stale claim error=%v, want assignment-ineligible", err)
	}
	assertRealBRStatusAndAssignee(t, dir, openID, "open", "")

	ownedID := createRealBRBead(t, dir, "owned in-progress work")
	if _, err := ClaimBeadForAssignment(t.Context(), dir, ownedID, "ExistingActor"); err != nil {
		t.Fatalf("seed owned in-progress work: %v", err)
	}
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, ownedID, actor, staleUpdatedAt); !errors.Is(err, ErrBeadAlreadyClaimed) {
		t.Fatalf("owned stale claim error=%v, want ownership conflict", err)
	}
	assertRealBRStatusAndAssignee(t, dir, ownedID, "in_progress", "ExistingActor")

	staleID := createRealBRBead(t, dir, "unowned in-progress work")
	runRealBR(t, dir, "update", staleID, "--status", "in_progress", "--json")
	databasePath := realBRDatabasePath(t, dir)
	execDatabase := func(query string, args ...any) {
		t.Helper()
		database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
		if err != nil {
			t.Fatalf("open Beads database: %v", err)
		}
		if _, err := database.Exec(query, args...); err != nil {
			database.Close()
			t.Fatalf("mutate Beads database: %v", err)
		}
		if err := database.Close(); err != nil {
			t.Fatalf("close Beads database: %v", err)
		}
	}
	setUpdatedAt := func(beadID string, updatedAt time.Time) {
		t.Helper()
		execDatabase("UPDATE issues SET updated_at = ? WHERE id = ?", updatedAt.Format(time.RFC3339Nano), beadID)
	}
	freshID := createRealBRBead(t, dir, "fresh in-progress work")
	runRealBR(t, dir, "update", freshID, "--status", "in_progress", "--json")
	freshUpdatedAt := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Microsecond)
	setUpdatedAt(freshID, freshUpdatedAt)
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, freshID, actor, freshUpdatedAt); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("fresh stale claim error=%v, want assignment-ineligible", err)
	}
	assertRealBRStatusAndAssignee(t, dir, freshID, "in_progress", "")

	setUpdatedAt(staleID, staleUpdatedAt)
	activityAt := time.Now().UTC()
	setUpdatedAt(staleID, activityAt)
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, staleID, actor, staleUpdatedAt); !errors.Is(err, ErrBeadAlreadyClaimed) {
		t.Fatalf("changed stale claim error=%v, want compare-and-set conflict", err)
	}
	setUpdatedAt(staleID, staleUpdatedAt)
	first, err := ClaimStaleBeadForAssignment(t.Context(), dir, staleID, actor, staleUpdatedAt)
	if err != nil || first.ID != staleID || first.Actor != actor || first.Status != "in_progress" {
		t.Fatalf("stale claim=%+v error=%v", first, err)
	}
	second, err := ClaimStaleBeadForAssignment(t.Context(), dir, staleID, actor, staleUpdatedAt)
	if err != nil || second.ID != first.ID || second.Actor != first.Actor || second.Status != first.Status {
		t.Fatalf("stale retry=%+v first=%+v error=%v", second, first, err)
	}
	assertRealBRStatusAndAssignee(t, dir, staleID, "in_progress", actor)

	pinnedID := createRealBRBead(t, dir, "pinned stale work")
	runRealBR(t, dir, "update", pinnedID, "--status", "in_progress", "--json")
	execDatabase("UPDATE issues SET pinned = 1, updated_at = ? WHERE id = ?", staleUpdatedAt.Format(time.RFC3339Nano), pinnedID)
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, pinnedID, actor, staleUpdatedAt); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("pinned stale claim error=%v, want assignment-ineligible", err)
	}

	gatedID := createRealBRBead(t, dir, "operator-gated stale work")
	runRealBR(t, dir, "update", gatedID, "--status", "in_progress", "--add-label", "operator-gated", "--json")
	setUpdatedAt(gatedID, staleUpdatedAt)
	if _, err := ClaimStaleBeadForAssignment(t.Context(), dir, gatedID, actor, staleUpdatedAt); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("operator-gated stale claim error=%v, want assignment-ineligible", err)
	}

	blockedID := createRealBRBead(t, dir, "dependency-blocked stale work")
	blockerID := createRealBRBead(t, dir, "stale work prerequisite")
	runRealBR(t, dir, "update", blockedID, "--status", "in_progress", "--json")
	runRealBR(t, dir, "dep", "add", blockedID, blockerID, "--type", "blocks", "--json")
	setUpdatedAt(blockedID, staleUpdatedAt)
	_, err = ClaimStaleBeadForAssignment(t.Context(), dir, blockedID, actor, staleUpdatedAt)
	var blockedEligibility *AssignmentEligibilityError
	if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &blockedEligibility) || !reflect.DeepEqual(blockedEligibility.UnresolvedBlockers, []string{blockerID}) {
		t.Fatalf("dependency-blocked stale claim error=%v eligibility=%+v", err, blockedEligibility)
	}
}

func TestClaimBeadForAssignmentTransactionRejectsNonReadyOpenWork(t *testing.T) {
	requireRealBR(t)

	tests := []struct {
		name       string
		updateSQL  string
		wantReason func(*AssignmentEligibilityError) bool
	}{
		{
			name:       "future deferred",
			updateSQL:  "UPDATE issues SET defer_until = '2099-01-01T00:00:00Z' WHERE id = ?",
			wantReason: func(err *AssignmentEligibilityError) bool { return err.Deferred },
		},
		{
			name:       "pinned",
			updateSQL:  "UPDATE issues SET pinned = 1 WHERE id = ?",
			wantReason: func(err *AssignmentEligibilityError) bool { return err.Pinned },
		},
		{
			name:       "ephemeral",
			updateSQL:  "UPDATE issues SET ephemeral = 1 WHERE id = ?",
			wantReason: func(err *AssignmentEligibilityError) bool { return err.Ephemeral },
		},
		{
			name:       "template",
			updateSQL:  "UPDATE issues SET is_template = 1 WHERE id = ?",
			wantReason: func(err *AssignmentEligibilityError) bool { return err.Template },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			runRealBR(t, dir, "init", "--quiet")
			beadID := createRealBRBead(t, dir, "non-ready assignment "+test.name)
			databasePath := realBRDatabasePath(t, dir)
			database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
			if err != nil {
				t.Fatalf("open Beads database: %v", err)
			}
			if _, err := database.Exec(test.updateSQL, beadID); err != nil {
				_ = database.Close()
				t.Fatalf("apply readiness gate: %v", err)
			}
			_ = database.Close()

			result, changed, err := claimBeadForAssignmentTransaction(
				t.Context(), databasePath, beadID, "AtomicActor", OperatorGatedLabels(),
			)
			var eligibilityErr *AssignmentEligibilityError
			if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) || !test.wantReason(eligibilityErr) {
				t.Fatalf("claim result=%+v changed=%v error=%v eligibility=%+v", result, changed, err, eligibilityErr)
			}
			if changed || eligibilityErr.Status != "open" || len(eligibilityErr.UnresolvedBlockers) != 0 || len(eligibilityErr.OperatorLabels) != 0 {
				t.Fatalf("eligibility error=%+v changed=%v", eligibilityErr, changed)
			}
			assertAssignmentClaimDatabaseState(t, databasePath, beadID, "open", "")

			staleUpdatedAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
			database, err = sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
			if err != nil {
				t.Fatalf("reopen Beads database: %v", err)
			}
			if _, err := database.Exec("UPDATE issues SET status = 'in_progress', updated_at = ? WHERE id = ?", staleUpdatedAt.Format(time.RFC3339Nano), beadID); err != nil {
				_ = database.Close()
				t.Fatalf("prepare stale readiness gate: %v", err)
			}
			_ = database.Close()
			result, changed, err = claimBeadNonTerminalTransaction(t.Context(), databasePath, beadID, "StaleActor", staleUpdatedAt, OperatorGatedLabels())
			var staleEligibilityErr *AssignmentEligibilityError
			if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &staleEligibilityErr) || !test.wantReason(staleEligibilityErr) {
				t.Fatalf("stale claim result=%+v changed=%v error=%v eligibility=%+v", result, changed, err, staleEligibilityErr)
			}
			if changed || staleEligibilityErr.Status != "in_progress" || len(staleEligibilityErr.UnresolvedBlockers) != 0 || len(staleEligibilityErr.OperatorLabels) != 0 {
				t.Fatalf("stale eligibility error=%+v changed=%v", staleEligibilityErr, changed)
			}
			assertAssignmentClaimDatabaseState(t, databasePath, beadID, "in_progress", "")
		})
	}

	t.Run("wisp", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		databasePath := realBRDatabasePath(t, dir)
		const beadID = "unit-wisp-claim"
		database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
		if err != nil {
			t.Fatalf("open Beads database: %v", err)
		}
		if _, err := database.Exec("INSERT INTO issues (id, title, status) VALUES (?, ?, 'open')", beadID, "wisp assignment claim"); err != nil {
			_ = database.Close()
			t.Fatalf("insert wisp fixture: %v", err)
		}
		_ = database.Close()

		_, changed, err := claimBeadForAssignmentTransaction(
			t.Context(), databasePath, beadID, "AtomicActor", OperatorGatedLabels(),
		)
		var eligibilityErr *AssignmentEligibilityError
		if changed || !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) || !eligibilityErr.Wisp {
			t.Fatalf("wisp changed=%v error=%v eligibility=%+v", changed, err, eligibilityErr)
		}
		assertAssignmentClaimDatabaseState(t, databasePath, beadID, "open", "")

		staleUpdatedAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
		database, err = sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
		if err != nil {
			t.Fatalf("reopen Beads database: %v", err)
		}
		if _, err := database.Exec("UPDATE issues SET status = 'in_progress', updated_at = ? WHERE id = ?", staleUpdatedAt.Format(time.RFC3339Nano), beadID); err != nil {
			_ = database.Close()
			t.Fatalf("prepare stale wisp: %v", err)
		}
		_ = database.Close()
		_, changed, err = claimBeadNonTerminalTransaction(t.Context(), databasePath, beadID, "StaleActor", staleUpdatedAt, OperatorGatedLabels())
		var staleEligibilityErr *AssignmentEligibilityError
		if changed || !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &staleEligibilityErr) || !staleEligibilityErr.Wisp {
			t.Fatalf("stale wisp changed=%v error=%v eligibility=%+v", changed, err, staleEligibilityErr)
		}
		assertAssignmentClaimDatabaseState(t, databasePath, beadID, "in_progress", "")
	})
}

func TestClaimBeadNonTerminalGuardRefusesCloseAfterReopenRead(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	beadID := createRealBRBead(t, dir, "guarded reopen race")
	runRealBR(t, dir, "close", beadID, "--reason", "first close", "--json")
	runRealBR(t, dir, "reopen", beadID, "--reason", "explicit reopen", "--json")
	status, err := GetBeadStatus(dir, beadID)
	if err != nil || status != "open" {
		t.Fatalf("advisory reopen status=%q err=%v, want open", status, err)
	}

	// This close is the exact interleaving the old show->claim sequence lost:
	// br --claim would reopen this row. The guarded compare-and-set must not.
	runRealBR(t, dir, "close", beadID, "--reason", "concurrent close", "--json")
	ctx := assignmentstore.WithNonTerminalClaimGuard(context.Background())
	_, claimErr := ClaimBead(ctx, dir, beadID, "GuardedActor")
	if !errors.Is(claimErr, ErrBeadTerminal) || !errors.Is(claimErr, ErrBeadAlreadyClaimed) {
		t.Fatalf("ClaimBead error=%v, want terminal claim refusal", claimErr)
	}
	status, err = GetBeadStatus(dir, beadID)
	if err != nil || status != "closed" {
		t.Fatalf("guarded claim final status=%q err=%v, want closed", status, err)
	}
}

func TestClaimBeadNonTerminalGuardPreservesBeadsMutationInvariants(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const title = "guarded claim hash parity"
	const actor = "GuardedActor"
	ordinaryID := createRealBRBead(t, dir, title)
	guardedID := createRealBRBead(t, dir, title)
	if _, err := ClaimBead(context.Background(), dir, ordinaryID, actor); err != nil {
		t.Fatalf("ordinary ClaimBead: %v", err)
	}
	ctx := assignmentstore.WithNonTerminalClaimGuard(context.Background())
	guarded, err := ClaimBead(ctx, dir, guardedID, actor)
	if err != nil {
		t.Fatalf("guarded ClaimBead: %v", err)
	}
	if guarded.ID != guardedID || guarded.Actor != actor || guarded.Status != "in_progress" {
		t.Fatalf("guarded claim receipt=%+v", guarded)
	}

	databasePath := realBRDatabasePath(t, dir)
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	var ordinaryHash, guardedHash, status, assignee string
	if err := database.QueryRow("SELECT content_hash FROM issues WHERE id = ?", ordinaryID).Scan(&ordinaryHash); err != nil {
		t.Fatalf("read ordinary content hash: %v", err)
	}
	if err := database.QueryRow("SELECT content_hash, status, assignee FROM issues WHERE id = ?", guardedID).Scan(&guardedHash, &status, &assignee); err != nil {
		t.Fatalf("read guarded issue: %v", err)
	}
	if ordinaryHash == "" || guardedHash != ordinaryHash {
		tx, txErr := database.BeginTx(t.Context(), nil)
		if txErr != nil {
			t.Fatalf("begin diagnostic read: %v", txErr)
		}
		ordinaryIssue, ordinaryErr := loadGuardedClaimIssue(t.Context(), tx, ordinaryID)
		guardedIssue, guardedErr := loadGuardedClaimIssue(t.Context(), tx, guardedID)
		_ = tx.Rollback()
		t.Fatalf("guarded content hash=%q, want ordinary br hash %q; ordinary=%+v (%v) guarded=%+v (%v)", guardedHash, ordinaryHash, ordinaryIssue, ordinaryErr, guardedIssue, guardedErr)
	}
	if status != "in_progress" || assignee != actor {
		t.Fatalf("guarded durable issue status=%q assignee=%q", status, assignee)
	}
	for eventType, want := range map[string]int{"status_changed": 1, "assignee_changed": 1} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", guardedID, eventType).Scan(&count); err != nil {
			t.Fatalf("count %s events: %v", eventType, err)
		}
		if count != want {
			t.Fatalf("%s event count=%d, want %d", eventType, count, want)
		}
	}
	var dirtyCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM dirty_issues WHERE issue_id = ?", guardedID).Scan(&dirtyCount); err != nil {
		t.Fatalf("count dirty markers: %v", err)
	}
	if dirtyCount != 0 {
		t.Fatalf("guarded claim left %d dirty markers after flush", dirtyCount)
	}

	jsonl, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatalf("read exported issues.jsonl: %v", err)
	}
	var exported map[string]any
	for _, line := range strings.Split(string(jsonl), "\n") {
		if !strings.Contains(line, `"id":"`+guardedID+`"`) {
			continue
		}
		if err := json.Unmarshal([]byte(line), &exported); err != nil {
			t.Fatalf("parse guarded JSONL row: %v", err)
		}
		break
	}
	if exported == nil || exported["status"] != "in_progress" || exported["assignee"] != actor {
		t.Fatalf("guarded JSONL row=%v", exported)
	}
}

func TestReleaseBeadClaimPreservesBeadsMutationInvariants(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const actor = "ReleaseActor"
	const title = "claim release hash parity"
	ordinaryID := createRealBRBead(t, dir, title)
	guardedID := createRealBRBead(t, dir, title)
	if _, err := ClaimBead(context.Background(), dir, ordinaryID, actor); err != nil {
		t.Fatalf("claim ordinary bead: %v", err)
	}
	if _, err := ClaimBead(context.Background(), dir, guardedID, actor); err != nil {
		t.Fatalf("claim guarded bead: %v", err)
	}
	runRealBR(t, dir, "update", ordinaryID, "--status", "open", "--assignee", "", "--actor", actor, "--json")
	released, err := ReleaseBeadClaim(t.Context(), dir, guardedID, actor)
	if err != nil || !released {
		t.Fatalf("ReleaseBeadClaim() released=%v error=%v", released, err)
	}

	databasePath := realBRDatabasePath(t, dir)
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	var ordinaryHash, guardedHash, status string
	var assignee sql.NullString
	if err := database.QueryRow("SELECT content_hash FROM issues WHERE id = ?", ordinaryID).Scan(&ordinaryHash); err != nil {
		t.Fatalf("read ordinary released hash: %v", err)
	}
	if err := database.QueryRow("SELECT content_hash, status, assignee FROM issues WHERE id = ?", guardedID).Scan(&guardedHash, &status, &assignee); err != nil {
		t.Fatalf("read guarded released issue: %v", err)
	}
	if ordinaryHash == "" || guardedHash != ordinaryHash {
		t.Fatalf("guarded released hash=%q, want ordinary br hash %q", guardedHash, ordinaryHash)
	}
	if status != "open" || strings.TrimSpace(assignee.String) != "" {
		t.Fatalf("released issue status=%q assignee=%q", status, assignee.String)
	}
	for eventType, want := range map[string]int{"status_changed": 2, "assignee_changed": 2} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", guardedID, eventType).Scan(&count); err != nil {
			t.Fatalf("count %s events: %v", eventType, err)
		}
		if count != want {
			t.Fatalf("%s event count=%d, want %d", eventType, count, want)
		}
	}
	var dirtyCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM dirty_issues WHERE issue_id = ?", guardedID).Scan(&dirtyCount); err != nil {
		t.Fatalf("count released dirty markers: %v", err)
	}
	if dirtyCount != 0 {
		t.Fatalf("released claim left %d dirty markers after flush", dirtyCount)
	}

	jsonl, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatalf("read released issues.jsonl: %v", err)
	}
	var exported map[string]any
	for _, line := range strings.Split(string(jsonl), "\n") {
		if !strings.Contains(line, `"id":"`+guardedID+`"`) {
			continue
		}
		if err := json.Unmarshal([]byte(line), &exported); err != nil {
			t.Fatalf("parse released JSONL row: %v", err)
		}
		break
	}
	assigneeValue, hasAssignee := exported["assignee"]
	if exported == nil || exported["status"] != "open" ||
		(hasAssignee && assigneeValue != nil && strings.TrimSpace(fmt.Sprint(assigneeValue)) != "") {
		t.Fatalf("released JSONL row=%v", exported)
	}
	if released, err := ReleaseBeadClaim(t.Context(), dir, guardedID, actor); err != nil || released {
		t.Fatalf("idempotent ReleaseBeadClaim() released=%v error=%v", released, err)
	}
}

func TestReleaseBeadClaimRetryFinalizesCommittedSQLiteMutation(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const actor = "ReleaseRetryActor"
	beadID := createRealBRBead(t, dir, "claim release retry finalization")
	if _, err := ClaimBead(context.Background(), dir, beadID, actor); err != nil {
		t.Fatalf("claim bead: %v", err)
	}
	databasePath := realBRDatabasePath(t, dir)

	// Simulate a process loss after the SQLite CAS commits but before `br sync`
	// and content-hash repair complete.
	txnResult, err := releaseBeadClaimTransaction(t.Context(), databasePath, beadID, actor)
	if err != nil || !txnResult.Released || !txnResult.NeedsFinalization || txnResult.Status != "open" {
		t.Fatalf("release transaction result=%+v error=%v", txnResult, err)
	}
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	var dirty int
	var contentHash sql.NullString
	if err := database.QueryRow("SELECT EXISTS(SELECT 1 FROM dirty_issues WHERE issue_id = ?), content_hash FROM issues WHERE id = ?", beadID, beadID).Scan(&dirty, &contentHash); err != nil {
		t.Fatalf("inspect interrupted release: %v", err)
	}
	if dirty != 1 || contentHash.Valid {
		t.Fatalf("interrupted release dirty=%d content_hash=%q", dirty, contentHash.String)
	}

	released, err := ReleaseBeadClaim(t.Context(), dir, beadID, actor)
	if err != nil || released {
		t.Fatalf("recovery ReleaseBeadClaim() released=%v error=%v", released, err)
	}
	if err := database.QueryRow("SELECT EXISTS(SELECT 1 FROM dirty_issues WHERE issue_id = ?), content_hash FROM issues WHERE id = ?", beadID, beadID).Scan(&dirty, &contentHash); err != nil {
		t.Fatalf("inspect finalized release: %v", err)
	}
	if dirty != 0 || !contentHash.Valid || strings.TrimSpace(contentHash.String) == "" {
		t.Fatalf("finalized release dirty=%d content_hash=%q", dirty, contentHash.String)
	}
	assertRealBRStatusAndAssignee(t, dir, beadID, "open", "")
}

func TestReleaseBeadClaimLeavesDifferentOwnerAndPreservesTerminalStatus(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const actor = "OriginalActor"

	ownedID := createRealBRBead(t, dir, "different owner release")
	if _, err := ClaimBead(context.Background(), dir, ownedID, actor); err != nil {
		t.Fatalf("claim owned bead: %v", err)
	}
	if released, err := ReleaseBeadClaim(t.Context(), dir, ownedID, "DifferentActor"); err != nil || released {
		t.Fatalf("different-owner release=%v error=%v", released, err)
	}
	assertRealBRStatusAndAssignee(t, dir, ownedID, "in_progress", actor)

	terminalID := createRealBRBead(t, dir, "terminal release")
	if _, err := ClaimBead(context.Background(), dir, terminalID, actor); err != nil {
		t.Fatalf("claim terminal bead: %v", err)
	}
	runRealBR(t, dir, "close", terminalID, "--reason", "terminal release test", "--json")
	if released, err := ReleaseBeadClaim(t.Context(), dir, terminalID, actor); err != nil || !released {
		t.Fatalf("terminal release=%v error=%v", released, err)
	}
	assertRealBRStatusAndAssignee(t, dir, terminalID, "closed", "")
	if released, err := ReleaseBeadClaim(t.Context(), dir, terminalID, actor); err != nil || released {
		t.Fatalf("terminal idempotent release=%v error=%v", released, err)
	}
}

// corruptRealBRIssuesRootPage overwrites the issues table's b-tree root page
// with a bogus interior-page header so the next UPDATE against issues fails
// with SQLITE_CORRUPT ("database disk image is malformed (11)") — the exact
// live signature from #227 — while leaving the file header readable.
func corruptRealBRIssuesRootPage(t *testing.T, databasePath string) {
	t.Helper()
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)"))
	if err != nil {
		t.Fatalf("open Beads database for corruption setup: %v", err)
	}
	var rootPage int64
	var pageSize int64
	if err := database.QueryRow("SELECT rootpage FROM sqlite_master WHERE name = 'issues'").Scan(&rootPage); err != nil {
		database.Close()
		t.Fatalf("resolve issues root page: %v", err)
	}
	if err := database.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		database.Close()
		t.Fatalf("resolve page size: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database before corruption: %v", err)
	}
	file, err := os.OpenFile(databasePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open database file for corruption: %v", err)
	}
	defer file.Close()
	garbage := append([]byte{0x05}, bytes.Repeat([]byte{0xFF}, 100)...)
	if _, err := file.WriteAt(garbage, (rootPage-1)*pageSize); err != nil {
		t.Fatalf("corrupt issues root page: %v", err)
	}
}

// TestGuardedClaimRebuildsAndRetriesOnCorruption is the #227 regression
// guard: a corrupted beads SQLite index used to surface from the guarded
// CAS-claim as a permanent "database disk image is malformed (11)" assignment
// failure, stalling the self-feeder until a human ran `br sync --rebuild`.
// The claim paths now trigger that rebuild themselves and retry once.
func TestGuardedClaimRebuildsAndRetriesOnCorruption(t *testing.T) {
	requireRealBR(t)

	t.Run("assignment claim recovers", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "claim across corruption")
		databasePath := realBRDatabasePath(t, dir)
		corruptRealBRIssuesRootPage(t, databasePath)

		claim, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "RecoveryActor")
		if err != nil {
			t.Fatalf("claim across corruption: %v", err)
		}
		if claim.ID != beadID || claim.Status != "in_progress" || claim.Actor != "RecoveryActor" {
			t.Fatalf("claim=%+v, want %s in_progress by RecoveryActor", claim, beadID)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "in_progress", "RecoveryActor")
	})

	t.Run("claim release recovers", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "release across corruption")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "RecoveryActor"); err != nil {
			t.Fatalf("initial claim: %v", err)
		}
		databasePath := realBRDatabasePath(t, dir)
		corruptRealBRIssuesRootPage(t, databasePath)

		released, err := ReleaseBeadClaim(t.Context(), dir, beadID, "RecoveryActor")
		if err != nil {
			t.Fatalf("release across corruption: %v", err)
		}
		if !released {
			t.Fatal("release across corruption returned released=false")
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "open", "")
	})
}

func TestIsBeadsDBCorruptionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"malformed image", fmt.Errorf("compare-and-set guarded Beads claim: database disk image is malformed (11)"), true},
		{"wrapped malformed schema", fmt.Errorf("outer: %w", errors.New("malformed database schema (users)")), true},
		{"not a database", errors.New("file is not a database"), true},
		{"free-page corruption class", errors.New("database corruption detected by pager"), true},
		{"already claimed", ErrBeadAlreadyClaimed, false},
		{"generic busy", errors.New("database is locked"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isBeadsDBCorruptionError(tc.err); got != tc.want {
				t.Fatalf("isBeadsDBCorruptionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func assertRealBRStatusAndAssignee(t *testing.T, dir, beadID, wantStatus, wantAssignee string) {
	t.Helper()
	output := runRealBR(t, dir, "show", beadID, "--json")
	var rows []struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(output, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("parse br show for %s: rows=%v error=%v output=%s", beadID, rows, err, output)
	}
	if rows[0].Status != wantStatus || rows[0].Assignee != wantAssignee {
		t.Fatalf("bead %s status=%q assignee=%q, want %q/%q", beadID, rows[0].Status, rows[0].Assignee, wantStatus, wantAssignee)
	}
}

func assertAssignmentClaimDatabaseState(t *testing.T, databasePath, beadID, wantStatus, wantAssignee string) {
	t.Helper()
	database, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(databasePath, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatalf("open Beads database: %v", err)
	}
	defer database.Close()
	var status string
	var assignee sql.NullString
	if err := database.QueryRow("SELECT status, assignee FROM issues WHERE id = ?", beadID).Scan(&status, &assignee); err != nil {
		t.Fatalf("read assignment claim state for %s: %v", beadID, err)
	}
	if status != wantStatus || strings.TrimSpace(assignee.String) != wantAssignee {
		t.Fatalf("bead %s database status=%q assignee=%q, want %q/%q", beadID, status, assignee.String, wantStatus, wantAssignee)
	}
}

func requireRealBR(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br is not installed")
	}
}

func runRealBR(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("br", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("br %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, output, stderr.Bytes())
	}
	return output
}

func createRealBRBead(t *testing.T, dir, title string) string {
	t.Helper()
	output := runRealBR(t, dir, "create", "--title", title, "--type", "task", "--priority", "2", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &created); err != nil {
		t.Fatalf("parse br create output: %v\n%s", err, output)
	}
	if created.ID == "" {
		t.Fatalf("br create returned no ID: %s", output)
	}
	return created.ID
}

func realBRDatabasePath(t *testing.T, dir string) string {
	t.Helper()
	output := runRealBR(t, dir, "info", "--json", "--no-auto-import", "--no-auto-flush")
	var info beadsWorkspaceInfo
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatalf("parse br info output: %v\n%s", err, output)
	}
	if info.DatabasePath == "" {
		t.Fatalf("br info returned no database path: %s", output)
	}
	return info.DatabasePath
}

// TestComputeBeadsContentHashGolden pins the Go reimplementation to a hash
// produced by real br 0.2.19 (export_hashes row for a bead with title
// "repro", status open, priority 2, type task, created_by jemanuel, all
// other fields empty/false). If this breaks, the field order, length-prefix
// encoding, or reserved-slot layout has drifted from
// beads_rust/src/util/hash.rs and local finalization would write wrong
// hashes into the Beads DB.
func TestComputeBeadsContentHashGolden(t *testing.T) {
	got := computeBeadsContentHash(
		"repro", "", "", "", "",
		"open", "2", "task",
		"", "", "jemanuel", "", "",
		false, false,
	)
	const want = "0f415f66b3f6f02a4c920709887383d7cbc8978a278494611a24c3e9a588a8c2"
	if got != want {
		t.Fatalf("computeBeadsContentHash = %s, want br-produced %s", got, want)
	}
}
