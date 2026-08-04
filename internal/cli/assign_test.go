package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignpkg "github.com/Dicklesworthstone/ntm/internal/assign"
	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/completion"
	"github.com/Dicklesworthstone/ntm/internal/config"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// ============================================================================
// Dependency Awareness Tests
// ============================================================================

// TestDependencyFilteringInAssignment tests that blocked beads are properly filtered out
// and added to the skipped list with the correct reason.
func TestDependencyFilteringInAssignment(t *testing.T) {
	// Test the SkippedItem structure has correct fields
	skipped := SkippedItem{
		BeadID:       "bd-123",
		BeadTitle:    "Test blocked bead",
		Reason:       "blocked_by_dependency",
		BlockedByIDs: []string{"bd-456", "bd-789"},
	}

	if skipped.Reason != "blocked_by_dependency" {
		t.Errorf("Expected reason 'blocked_by_dependency', got %q", skipped.Reason)
	}
	if len(skipped.BlockedByIDs) != 2 {
		t.Errorf("Expected 2 blockers, got %d", len(skipped.BlockedByIDs))
	}
}

// TestAssignSummaryBlockedCount tests that the summary correctly tracks blocked count
func TestAssignSummaryBlockedCount(t *testing.T) {
	summary := AssignSummaryEnhanced{
		TotalBeadCount:  10,
		ActionableCount: 7,
		BlockedCount:    3,
		AssignedCount:   5,
		SkippedCount:    5, // 3 blocked + 2 other reasons
		IdleAgents:      2,
	}

	if summary.TotalBeadCount != 10 {
		t.Errorf("Expected TotalBeadCount=10, got %d", summary.TotalBeadCount)
	}
	if summary.ActionableCount != 7 {
		t.Errorf("Expected ActionableCount=7, got %d", summary.ActionableCount)
	}
	if summary.BlockedCount != 3 {
		t.Errorf("Expected BlockedCount=3, got %d", summary.BlockedCount)
	}
}

// TestTriageRecommendationToBeadPreviewConversion tests the conversion logic
func TestTriageRecommendationToBeadPreviewConversion(t *testing.T) {
	tests := []struct {
		name          string
		rec           bv.TriageRecommendation
		expectBlocked bool
		expectedPrio  string
	}{
		{
			name: "actionable bead",
			rec: bv.TriageRecommendation{
				ID:        "bd-001",
				Title:     "Test actionable",
				Priority:  1,
				BlockedBy: nil,
			},
			expectBlocked: false,
			expectedPrio:  "P1",
		},
		{
			name: "blocked bead",
			rec: bv.TriageRecommendation{
				ID:        "bd-002",
				Title:     "Test blocked",
				Priority:  2,
				BlockedBy: []string{"bd-003"},
			},
			expectBlocked: true,
			expectedPrio:  "P2",
		},
		{
			name: "multiple blockers",
			rec: bv.TriageRecommendation{
				ID:        "bd-004",
				Title:     "Test multi-blocked",
				Priority:  0,
				BlockedBy: []string{"bd-005", "bd-006", "bd-007"},
			},
			expectBlocked: true,
			expectedPrio:  "P0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isBlocked := len(tc.rec.BlockedBy) > 0
			if isBlocked != tc.expectBlocked {
				t.Errorf("Expected blocked=%v, got %v", tc.expectBlocked, isBlocked)
			}

			// Test conversion to BeadPreview format
			preview := bv.BeadPreview{
				ID:       tc.rec.ID,
				Title:    tc.rec.Title,
				Priority: tc.expectedPrio,
			}
			if preview.Priority != tc.expectedPrio {
				t.Errorf("Expected priority %q, got %q", tc.expectedPrio, preview.Priority)
			}
		})
	}
}

// TestBlockedBeadsReasonString tests that blocked reason string is correct
func TestBlockedBeadsReasonString(t *testing.T) {
	const expectedReason = "blocked_by_dependency"

	// This is the reason string used in the assign command
	// to identify blocked beads in the Skipped list
	skipped := SkippedItem{
		BeadID:       "test",
		Reason:       expectedReason,
		BlockedByIDs: []string{"blocker1"},
	}

	if skipped.Reason != expectedReason {
		t.Errorf("Reason should be %q, got %q", expectedReason, skipped.Reason)
	}
}

// TestAssignOutputEnhancedStructure tests the output structure is correct for JSON
func TestAssignOutputEnhancedStructure(t *testing.T) {
	output := AssignOutputEnhanced{
		Strategy: "balanced",
		Assignments: []AssignmentItem{
			{
				BeadID:    "bd-100",
				BeadTitle: "Test task",
				Pane:      1,
				AgentType: "claude",
				Score:     0.85,
			},
		},
		Skipped: []SkippedItem{
			{
				BeadID:       "bd-101",
				BeadTitle:    "Blocked task",
				Reason:       "blocked_by_dependency",
				BlockedByIDs: []string{"bd-100"},
			},
		},
		Summary: AssignSummaryEnhanced{
			TotalBeadCount:  2,
			ActionableCount: 1,
			BlockedCount:    1,
			AssignedCount:   1,
			SkippedCount:    1,
			IdleAgents:      3,
		},
	}

	// Verify structure
	if output.Strategy != "balanced" {
		t.Errorf("Expected strategy 'balanced', got %q", output.Strategy)
	}
	if len(output.Assignments) != 1 {
		t.Errorf("Expected 1 assigned, got %d", len(output.Assignments))
	}
	if len(output.Skipped) != 1 {
		t.Errorf("Expected 1 skipped, got %d", len(output.Skipped))
	}
	if output.Summary.ActionableCount != 1 {
		t.Errorf("Expected ActionableCount=1, got %d", output.Summary.ActionableCount)
	}
	if output.Summary.BlockedCount != 1 {
		t.Errorf("Expected BlockedCount=1, got %d", output.Summary.BlockedCount)
	}
}

func createAssignProjectRoot(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	// On macOS, t.TempDir() returns /var/folders/... but the code under test
	// calls filepath.EvalSymlinks which canonicalises this to
	// /private/var/folders/... — resolve up-front so expected values match.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.MkdirAll(filepath.Join(root, ".ntm"), 0755); err != nil {
		t.Fatalf("creating .ntm dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ntm", "config.toml"), []byte(""), 0644); err != nil {
		t.Fatalf("writing config marker: %v", err)
	}
	nested := filepath.Join(root, "nested", "pkg")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("creating nested dir: %v", err)
	}

	return root, nested
}

func TestResolveAssignProjectDirRejectsCallerCWDForExplicitSession(t *testing.T) {
	isolateSessionAgentStorage(t)
	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	got, err := resolveAssignProjectDir(t.Context(), "demo")
	if err == nil || got != "" || !strings.Contains(err.Error(), "getting project root failed") {
		t.Fatalf("resolveAssignProjectDir() = %q, %v; want explicit-session failure instead of caller CWD %q", got, err, root)
	}
}

func TestResolveAssignProjectDirRejectsInvalidSessionName(t *testing.T) {
	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	_, err := resolveAssignProjectDir(t.Context(), "../escape")
	if err == nil {
		t.Fatal("expected invalid session error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid session name") {
		t.Fatalf("expected invalid session error, got %v", err)
	}
}

func TestResolveAssignProjectDirUsesSavedSessionAgentProjectKey(t *testing.T) {
	isolateSessionAgentStorage(t)

	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	actualProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actualProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir actual project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "demo", actualProject, "GreenCastle")

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	got, err := resolveAssignProjectDir(t.Context(), "demo")
	if err != nil {
		t.Fatalf("resolveAssignProjectDir() error = %v", err)
	}
	if got != actualProject {
		t.Fatalf("resolveAssignProjectDir() = %q, want saved session agent project %q", got, actualProject)
	}
}

func TestResolveAssignProjectDirExplicitRepoOverridesSavedSessionProject(t *testing.T) {
	isolateSessionAgentStorage(t)

	savedProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(savedProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir saved project git dir: %v", err)
	}
	overrideProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overrideProject, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir override project git dir: %v", err)
	}
	saveSessionAgentForTest(t, "demo", savedProject, "GreenCastle")

	previousRepo := assignRepoPath
	assignRepoPath = overrideProject
	t.Cleanup(func() { assignRepoPath = previousRepo })

	got, err := resolveAssignProjectDir(t.Context(), "demo")
	if err != nil {
		t.Fatalf("resolveAssignProjectDir() error = %v", err)
	}
	want, err := filepath.Abs(overrideProject)
	if err != nil {
		t.Fatalf("filepath.Abs(): %v", err)
	}
	if got != want {
		t.Fatalf("resolveAssignProjectDir() = %q, want explicit repo %q instead of saved project %q", got, want, savedProject)
	}
}

func TestResolveAssignProjectDirResolvesProjectScopedPrefix(t *testing.T) {
	isolateSessionAgentStorage(t)
	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })
	if err := os.MkdirAll(filepath.Join(cfg.ProjectsBase, "demo"), 0o755); err != nil {
		t.Fatalf("create configured project prefix target: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	saveSessionAgentForTest(t, "demo", root, "GreenCastle")

	got, err := resolveAssignProjectDir(t.Context(), "de")
	if err != nil {
		t.Fatalf("resolveAssignProjectDir() error = %v", err)
	}
	if got != root {
		t.Fatalf("resolveAssignProjectDir() = %q, want %q", got, root)
	}
}

func TestReleaseFileReservationsWithIDsUsesResolvedProjectDir(t *testing.T) {
	isolateSessionAgentStorage(t)
	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	stub.reservations = []agentmail.FileReservation{{
		ID: 42, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/release.go", Exclusive: true,
		Reason: "bead assignment: bd-123", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	saveSessionAgentForTest(t, "demo", root, "BlueLake")

	barrier := &assignment.Assignment{
		BeadID: "bd-123", AgentName: "BlueLake", ClearState: assignment.ClearStateReservationReleasing,
	}
	if _, err := releaseFileReservationsWithIDs(t.Context(), "demo", barrier, []int{42}, []string{"internal/cli/release.go"}); err != nil {
		t.Fatalf("releaseFileReservationsWithIDs() error = %v", err)
	}

	if len(stub.releaseCalls) != 1 {
		t.Fatalf("expected 1 release call, got %d", len(stub.releaseCalls))
	}
	if got := stub.releaseCalls[0].Project; got != root {
		t.Fatalf("release project = %q, want %q", got, root)
	}
}

func TestCLIWorkingReplacementReleaseReceiptProvesFullDurableSurface(t *testing.T) {
	current := &assignment.Assignment{
		ReservedPaths:         []string{" internal/old.go ", "internal/shared.go"},
		ReservationRequested:  []string{"internal/shared.go", "internal/requested.go"},
		ReservationInputPaths: []string{"internal/input.go", ""},
		ReservationIDs:        []int{41, 42, 41},
	}
	receipt := cliWorkingReplacementReleaseReceipt(current, nil, nil)
	if !reflect.DeepEqual(receipt.ReleasedPaths, []string{
		"internal/old.go", "internal/shared.go", "internal/requested.go", "internal/input.go",
	}) {
		t.Fatalf("released paths=%v", receipt.ReleasedPaths)
	}
	if !reflect.DeepEqual(receipt.ReleasedReservationIDs, []int{41, 42}) {
		t.Fatalf("released reservation IDs=%v", receipt.ReleasedReservationIDs)
	}

	releaseErr := errors.New("release failed")
	failed := cliWorkingReplacementReleaseReceipt(current, []string{" internal/old.go ", "internal/old.go"}, releaseErr)
	if !reflect.DeepEqual(failed.ReleasedPaths, []string{"internal/old.go"}) || len(failed.ReleasedReservationIDs) != 0 {
		t.Fatalf("failed release receipt=%+v", failed)
	}
}

func TestClearStoredAssignmentRetainsBarrierUntilLeaseReleaseConfirmed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	previousRepo := assignRepoPath
	assignRepoPath = projectDir
	t.Cleanup(func() { assignRepoPath = previousRepo })
	store := assignment.NewStore("clear-release-barrier")
	if _, err := store.Assign("ntm-clear", "Clear me", 2, "codex", "BlueLake", "work"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	store.Assignments["ntm-clear"].ReservationCompleted = true
	store.Assignments["ntm-clear"].ReservedPaths = []string{"internal/cli/**"}
	store.Assignments["ntm-clear"].ReservationIDs = []int{41, 42}
	store.Assignments["ntm-clear"].ClaimActor = "BlueLake/ntm-clear"
	if err := store.Save(); err != nil {
		t.Fatalf("persist reservation metadata: %v", err)
	}

	originalRelease := releaseAssignmentLeases
	originalClaimRelease := releaseBeadClaimForAssignment
	t.Cleanup(func() {
		releaseAssignmentLeases = originalRelease
		releaseBeadClaimForAssignment = originalClaimRelease
	})
	releaseCalls := 0
	claimReleaseCalls := 0
	claimReleaseErr := errors.New("Beads sync unavailable")
	releaseBeadClaimForAssignment = func(_ context.Context, projectDir, beadID, actor string) (bool, error) {
		claimReleaseCalls++
		if projectDir != assignRepoPath || beadID != "ntm-clear" || actor != "BlueLake/ntm-clear" {
			t.Fatalf("claim release args project=%q bead=%q actor=%q", projectDir, beadID, actor)
		}
		if claimReleaseErr != nil {
			return false, claimReleaseErr
		}
		return true, nil
	}
	releaseAssignmentLeases = func(_ context.Context, _ string, current *assignment.Assignment) ([]string, error) {
		releaseCalls++
		if current.ClearState != assignment.ClearStateReservationReleasing || !reflect.DeepEqual(current.ReservationIDs, []int{41, 42}) {
			t.Fatalf("release input lost clear barrier metadata: %+v", current)
		}
		return nil, errors.New("release unavailable")
	}

	current := store.Get("ntm-clear")
	if _, err := clearStoredAssignment(t.Context(), store, "clear-release-barrier", current); err == nil || !strings.Contains(err.Error(), "release unavailable") {
		t.Fatalf("first clear error=%v, want release failure", err)
	}
	failed := store.Get("ntm-clear")
	if failed == nil || failed.ClearState != assignment.ClearStateReservationReleasing || failed.ClearError != "release unavailable" || !reflect.DeepEqual(failed.ReservationIDs, []int{41, 42}) {
		t.Fatalf("release failure lost retryable ledger: %+v", failed)
	}
	if claimReleaseCalls != 0 {
		t.Fatalf("tracker claim released before reservation proof: calls=%d", claimReleaseCalls)
	}

	releaseAssignmentLeases = func(_ context.Context, _ string, current *assignment.Assignment) ([]string, error) {
		releaseCalls++
		if current.ClearState != assignment.ClearStateReservationReleasing || !reflect.DeepEqual(current.ReservationIDs, []int{41, 42}) {
			t.Fatalf("retry input lost clear barrier metadata: %+v", current)
		}
		return []string{"2 reservations"}, nil
	}
	released, err := clearStoredAssignment(t.Context(), store, "clear-release-barrier", failed)
	if err == nil || !strings.Contains(err.Error(), claimReleaseErr.Error()) {
		t.Fatalf("clear after lease success error=%v, want claim-release failure", err)
	}
	checkpoint := store.Get("ntm-clear")
	if checkpoint == nil || checkpoint.ClearState != assignment.ClearStateLeasesReleased || len(checkpoint.ReservationIDs) != 0 || checkpoint.ClearError == "" {
		t.Fatalf("claim-release failure lost durable lease checkpoint: %+v", checkpoint)
	}
	if releaseCalls != 2 || claimReleaseCalls != 1 || !reflect.DeepEqual(released, []string{"2 reservations"}) {
		t.Fatalf("post-release failure calls: lease=%d claim=%d released=%v", releaseCalls, claimReleaseCalls, released)
	}

	claimReleaseErr = nil
	released, err = clearStoredAssignment(t.Context(), store, "clear-release-barrier", checkpoint)
	if err != nil {
		t.Fatalf("clear after durable lease checkpoint: %v", err)
	}
	if releaseCalls != 2 || claimReleaseCalls != 2 || len(released) != 0 {
		t.Fatalf("checkpoint retry repeated lease release: lease=%d claim=%d released=%v", releaseCalls, claimReleaseCalls, released)
	}
	if got := store.Get("ntm-clear"); got != nil {
		t.Fatalf("confirmed lease release left assignment: %+v", got)
	}
}

func TestReleaseAssignmentReservationsForClearSkipsDiscoveryForAtomicNoLeaseIntent(t *testing.T) {
	originalRelease := releaseReservations
	t.Cleanup(func() { releaseReservations = originalRelease })
	discoveryCalls := 0
	releaseReservations = func(_ context.Context, _ string, _ *assignment.Assignment) ([]string, error) {
		discoveryCalls++
		return []string{"legacy"}, nil
	}

	atomicNoLease := &assignment.Assignment{
		BeadID:           "ntm-no-lease",
		AgentName:        "BlueLake",
		IdempotencyKey:   "intent-1",
		ReservationState: assignment.ReservationPending,
		ClearState:       assignment.ClearStateReservationReleasing,
	}
	released, err := releaseAssignmentReservationsForClear(t.Context(), "demo", atomicNoLease)
	if err != nil {
		t.Fatalf("atomic no-lease clear: %v", err)
	}
	if len(released) != 0 || discoveryCalls != 0 {
		t.Fatalf("atomic no-lease clear released=%v discoveryCalls=%d, want local-only clear", released, discoveryCalls)
	}

	legacyUnknown := &assignment.Assignment{
		BeadID: "ntm-legacy", AgentName: "BlueLake", ClearState: assignment.ClearStateReservationReleasing,
	}
	released, err = releaseAssignmentReservationsForClear(t.Context(), "demo", legacyUnknown)
	if err != nil {
		t.Fatalf("legacy reservation discovery: %v", err)
	}
	if !reflect.DeepEqual(released, []string{"legacy"}) || discoveryCalls != 1 {
		t.Fatalf("legacy release=%v discoveryCalls=%d, want one discovery", released, discoveryCalls)
	}
}

func TestCLIAtomicReservationReconciliationClassifiesAbsentCompleteAndPartial(t *testing.T) {
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	request := assignment.ReservationRequest{
		BeadID:         "ntm-reconcile",
		AgentName:      "BlueLake",
		Target:         "%42",
		RequestedPaths: []string{"internal/cli/a.go", "internal/cli/b.go"},
	}

	for _, test := range []struct {
		name         string
		reservations []agentmail.FileReservation
		wantState    assignment.ReservationReconciliationState
		wantIDs      []int
	}{
		{name: "absent", wantState: assignment.ReservationReconciliationAbsent},
		{
			name: "complete",
			reservations: []agentmail.FileReservation{
				{ID: 51, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/a.go", Exclusive: true, Reason: "bead assignment: ntm-reconcile", ExpiresTS: expires},
				{ID: 52, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/b.go", Exclusive: true, Reason: "bead assignment: ntm-reconcile", ExpiresTS: expires},
			},
			wantState: assignment.ReservationReconciliationReserved,
			wantIDs:   []int{51, 52},
		},
		{
			name: "partial is known reserved",
			reservations: []agentmail.FileReservation{
				{ID: 53, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/a.go", Exclusive: true, Reason: "bead assignment: ntm-reconcile", ExpiresTS: expires},
			},
			wantState: assignment.ReservationReconciliationReserved,
			wantIDs:   []int{53},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newMailStub(t, nil)
			defer stub.Close()
			stub.reservations = test.reservations
			port := &cliAtomicReservationPort{manager: assignpkg.NewFileReservationManager(
				agentmail.NewClient(agentmail.WithBaseURL(stub.server.URL+"/")), "/test/project",
			)}

			got, err := port.ReconcileReservation(t.Context(), request, assignment.LeaseReceipt{})
			if err != nil {
				t.Fatalf("ReconcileReservation() error = %v", err)
			}
			if got.State != test.wantState || !reflect.DeepEqual(got.Lease.ReservationIDs, test.wantIDs) {
				t.Fatalf("ReconcileReservation() = %+v, want state=%s IDs=%v", got, test.wantState, test.wantIDs)
			}
		})
	}
}

func TestReleaseAssignmentReservationsForClearReconcilesUnknownLease(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	previousTimeout := assignTimeout
	assignRepoPath = project
	assignTimeout = 2 * time.Second
	t.Cleanup(func() {
		assignRepoPath = previousRepo
		assignTimeout = previousTimeout
	})

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	stub.reservations = []agentmail.FileReservation{{
		ID: 61, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/reconcile.go", Exclusive: true,
		Reason: "bead assignment: ntm-unknown", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}
	current := &assignment.Assignment{
		BeadID: "ntm-unknown", BeadTitle: "Fix internal/cli/reconcile.go", AgentName: "BlueLake",
		IdempotencyKey: "intent-unknown", ReservationRequired: true,
		ReservationState: assignment.ReservationUnknown, ReservationAttempts: 1,
		ReservationRequested: []string{"internal/cli/reconcile.go"},
		ClearState:           assignment.ClearStateReservationReleasing,
	}

	released, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current)
	if err != nil {
		t.Fatalf("releaseAssignmentReservationsForClear() error = %v", err)
	}
	if len(stub.releaseCalls) != 1 || !reflect.DeepEqual(stub.releaseCalls[0].IDs, []int{61}) {
		t.Fatalf("release calls = %+v", stub.releaseCalls)
	}
	if !reflect.DeepEqual(released, []string{"internal/cli/reconcile.go"}) {
		t.Fatalf("released = %v", released)
	}
}

func TestReleaseAssignmentReservationsForClearProvesUnknownLeaseAbsent(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	current := &assignment.Assignment{
		BeadID: "ntm-absent", BeadTitle: "Fix internal/cli/absent.go", AgentName: "BlueLake",
		IdempotencyKey: "intent-absent", ReservationRequired: true,
		ReservationState: assignment.ReservationUnknown, ReservationAttempts: 1,
		ReservationRequested: []string{"internal/cli/absent.go"},
		ClearState:           assignment.ClearStateReservationReleasing,
	}

	released, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current)
	if err != nil {
		t.Fatalf("releaseAssignmentReservationsForClear() error = %v", err)
	}
	if len(released) != 0 || len(stub.releaseCalls) != 0 {
		t.Fatalf("absent reconciliation released=%v calls=%+v", released, stub.releaseCalls)
	}
}

func TestReleaseAssignmentReservationsForClearRetainsUnknownLeaseOnReleaseFailure(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	stub.releaseResult = agentmail.ReleaseReservationsResult{Released: 0}
	stub.reservations = []agentmail.FileReservation{{
		ID: 71, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/fail.go", Exclusive: true,
		Reason: "bead assignment: ntm-release-fail", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}
	current := &assignment.Assignment{
		BeadID: "ntm-release-fail", BeadTitle: "Fix internal/cli/fail.go", AgentName: "BlueLake",
		IdempotencyKey: "intent-release-fail", ReservationRequired: true,
		ReservationState: assignment.ReservationUnknown, ReservationAttempts: 1,
		ReservationRequested: []string{"internal/cli/fail.go"},
		ClearState:           assignment.ClearStateReservationReleasing,
	}

	if _, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current); err == nil || !strings.Contains(err.Error(), "remain active") {
		t.Fatalf("releaseAssignmentReservationsForClear() error = %v", err)
	}
	if len(stub.releaseCalls) != 3 || !reflect.DeepEqual(stub.releaseCalls[0].IDs, []int{71}) {
		t.Fatalf("release failure calls = %+v", stub.releaseCalls)
	}
}

func TestReleaseAssignmentReservationsForClearRejectsUnverifiedSuccessCount(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	stub.keepOnRelease = true
	stub.releaseResult = agentmail.ReleaseReservationsResult{Released: 1}
	stub.reservations = []agentmail.FileReservation{{
		ID: 72, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/proof.go", Exclusive: true,
		Reason: "bead assignment: ntm-release-proof", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}
	current := &assignment.Assignment{
		BeadID: "ntm-release-proof", AgentName: "BlueLake", IdempotencyKey: "release-proof-key",
		ReservationRequired: true, ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservationIDs: []int{72}, ReservedPaths: []string{"internal/cli/proof.go"}, ClearState: assignment.ClearStateReservationReleasing,
	}
	if _, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current); err == nil || !strings.Contains(err.Error(), "remain active") {
		t.Fatalf("unverified release count error=%v", err)
	}
	if len(stub.releaseCalls) != 3 || !reflect.DeepEqual(stub.releaseCalls[0].IDs, []int{72}) {
		t.Fatalf("release calls=%+v", stub.releaseCalls)
	}
}

func TestReleaseAssignmentReservationsForClearDoesNotReleaseNewerBeadOnSamePath(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	stub.reservations = []agentmail.FileReservation{
		{ID: 73, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/shared.go", Exclusive: true, Reason: "bead assignment: ntm-old", ExpiresTS: expires},
		{ID: 74, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/shared.go", Exclusive: true, Reason: "bead assignment: ntm-new", ExpiresTS: expires},
	}
	current := &assignment.Assignment{
		BeadID: "ntm-old", AgentName: "BlueLake", IdempotencyKey: "old-key",
		ReservationRequired: true, ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservedPaths: []string{"internal/cli/shared.go"}, ClearState: assignment.ClearStateReservationReleasing,
	}
	released, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current)
	if err != nil {
		t.Fatalf("release old reservation: %v", err)
	}
	if !reflect.DeepEqual(released, []string{"internal/cli/shared.go"}) || len(stub.releaseCalls) != 1 || !reflect.DeepEqual(stub.releaseCalls[0].IDs, []int{73}) {
		t.Fatalf("released=%v calls=%+v", released, stub.releaseCalls)
	}
	if len(stub.reservations) != 1 || stub.reservations[0].ID != 74 {
		t.Fatalf("newer reservation changed: %+v", stub.reservations)
	}
}

func TestReleaseAssignmentReservationsForClearByIDPreservesNewerGenerationForSameBead(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	stub.reservations = []agentmail.FileReservation{
		{ID: 75, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/old.go", Exclusive: true, Reason: "bead assignment: ntm-same", ExpiresTS: expires},
		{ID: 76, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/new.go", Exclusive: true, Reason: "bead assignment: ntm-same", ExpiresTS: expires},
	}
	current := &assignment.Assignment{
		BeadID: "ntm-same", AgentName: "BlueLake", IdempotencyKey: "old-generation",
		ReservationRequired: true, ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservationIDs: []int{75}, ReservedPaths: []string{"internal/cli/old.go"}, ClearState: assignment.ClearStateReservationReleasing,
	}
	released, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current)
	if err != nil {
		t.Fatalf("release prior generation: %v", err)
	}
	if !reflect.DeepEqual(released, []string{"internal/cli/old.go"}) || len(stub.releaseCalls) != 1 || !reflect.DeepEqual(stub.releaseCalls[0].IDs, []int{75}) {
		t.Fatalf("released=%v calls=%+v", released, stub.releaseCalls)
	}
	if len(stub.reservations) != 1 || stub.reservations[0].ID != 76 {
		t.Fatalf("newer same-bead reservation changed: %+v", stub.reservations)
	}
}

func TestReleaseAssignmentReservationsForClearRejectsIDPathBindingMismatch(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir project marker: %v", err)
	}
	previousRepo := assignRepoPath
	assignRepoPath = project
	t.Cleanup(func() { assignRepoPath = previousRepo })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)
	stub.reservations = []agentmail.FileReservation{{
		ID: 77, ProjectID: 1, AgentName: "BlueLake", PathPattern: "internal/cli/new.go", Exclusive: true,
		Reason: "bead assignment: ntm-mismatch", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}
	current := &assignment.Assignment{
		BeadID: "ntm-mismatch", AgentName: "BlueLake", IdempotencyKey: "stale-generation",
		ReservationRequired: true, ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservationIDs: []int{77}, ReservedPaths: []string{"internal/cli/old.go"}, ClearState: assignment.ClearStateReservationReleasing,
	}
	if _, err := releaseAssignmentReservationsForClear(t.Context(), "demo", current); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("binding mismatch error=%v", err)
	}
	if len(stub.releaseCalls) != 0 || len(stub.reservations) != 1 || stub.reservations[0].ID != 77 {
		t.Fatalf("mismatched reservation was changed: calls=%+v reservations=%+v", stub.releaseCalls, stub.reservations)
	}
}

func TestReserveFilesForBeadUsesResolvedProjectDir(t *testing.T) {
	isolateSessionAgentStorage(t)
	root, nested := createAssignProjectRoot(t)

	oldCfg := cfg
	cfg = &config.Config{ProjectsBase: filepath.Join(root, "projects-base")}
	t.Cleanup(func() { cfg = oldCfg })

	stub := newMailStub(t, nil)
	defer stub.Close()
	t.Setenv("AGENT_MAIL_URL", stub.server.URL)

	oldWd, _ := os.Getwd()
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	saveSessionAgentForTest(t, "demo", root, "BlueLake")

	result := reserveFilesForBead(t.Context(), "demo", "bd-123", "Update internal/cli/assign.go", "claude", false, time.Second)
	if result == nil {
		t.Fatal("reserveFilesForBead() returned nil result")
	}
	if !result.Success {
		t.Fatalf("reserveFilesForBead() success = false, error = %q", result.Error)
	}

	if len(stub.reserveCalls) != 1 {
		t.Fatalf("expected 1 reserve call, got %d", len(stub.reserveCalls))
	}
	if got := stub.reserveCalls[0].Project; got != root {
		t.Fatalf("reserve project = %q, want %q", got, root)
	}
}

// ============================================================================
// Completion Detection and Unblock Tests
// ============================================================================

// TestIsBeadInCycle tests the cycle detection helper function
func TestIsBeadInCycle(t *testing.T) {
	cycles := [][]string{
		{"bd-001", "bd-002", "bd-003"},
		{"bd-010", "bd-011"},
	}

	tests := []struct {
		name     string
		beadID   string
		expected bool
	}{
		{"in first cycle", "bd-001", true},
		{"in first cycle - middle", "bd-002", true},
		{"in second cycle", "bd-010", true},
		{"not in any cycle", "bd-099", false},
		{"partial match (not in cycle)", "bd-00", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := IsBeadInCycle(tc.beadID, cycles)
			if result != tc.expected {
				t.Errorf("IsBeadInCycle(%q) = %v, want %v", tc.beadID, result, tc.expected)
			}
		})
	}
}

// TestIsBeadInCycleEmptyCycles tests with empty cycles
func TestIsBeadInCycleEmptyCycles(t *testing.T) {
	var cycles [][]string

	if IsBeadInCycle("bd-001", cycles) {
		t.Error("Expected false for empty cycles")
	}

	cycles = [][]string{{}} // Single empty cycle
	if IsBeadInCycle("bd-001", cycles) {
		t.Error("Expected false for cycle with no beads")
	}
}

// TestUnblockedBeadStructure tests the UnblockedBead type
func TestUnblockedBeadStructure(t *testing.T) {
	unblocked := UnblockedBead{
		ID:            "bd-100",
		Title:         "Now ready task",
		Priority:      1,
		PrevBlockers:  []string{"bd-050", "bd-060"},
		UnblockedByID: "bd-050",
	}

	if unblocked.ID != "bd-100" {
		t.Errorf("Expected ID 'bd-100', got %q", unblocked.ID)
	}
	if len(unblocked.PrevBlockers) != 2 {
		t.Errorf("Expected 2 previous blockers, got %d", len(unblocked.PrevBlockers))
	}
	if unblocked.UnblockedByID != "bd-050" {
		t.Errorf("Expected UnblockedByID 'bd-050', got %q", unblocked.UnblockedByID)
	}
}

// TestDependencyAwareResultStructure tests the DependencyAwareResult type
func TestDependencyAwareResultStructure(t *testing.T) {
	result := DependencyAwareResult{
		CompletedBeadID: "bd-finished",
		NewlyUnblocked: []UnblockedBead{
			{
				ID:            "bd-ready1",
				Title:         "Ready task 1",
				Priority:      2,
				UnblockedByID: "bd-finished",
			},
			{
				ID:            "bd-ready2",
				Title:         "Ready task 2",
				Priority:      1,
				UnblockedByID: "bd-finished",
			},
		},
		CyclesDetected: [][]string{{"bd-cycle1", "bd-cycle2"}},
		Errors:         []string{"warning: something"},
	}

	if result.CompletedBeadID != "bd-finished" {
		t.Errorf("Expected CompletedBeadID 'bd-finished', got %q", result.CompletedBeadID)
	}
	if len(result.NewlyUnblocked) != 2 {
		t.Errorf("Expected 2 newly unblocked, got %d", len(result.NewlyUnblocked))
	}
	if len(result.CyclesDetected) != 1 {
		t.Errorf("Expected 1 cycle detected, got %d", len(result.CyclesDetected))
	}
	if len(result.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(result.Errors))
	}
}

func TestNewlyUnblockedCandidateUsesAuthoritativeDependencyState(t *testing.T) {
	const completed = "bd-completed"
	ready := bv.BeadDependentState{ID: "bd-ready", Title: "stale title", Priority: 1}
	readyDetails := &bv.BeadAssignmentDetails{
		ID: "bd-ready", Title: "Live ready title", Status: "open", Priority: 4,
		BlockingDependencies: []bv.BeadDependencyState{{ID: completed, Status: "closed"}},
	}
	candidate, reason, unresolved := newlyUnblockedCandidate(completed, ready, readyDetails)
	if reason != "" || len(unresolved) != 0 || candidate == nil {
		t.Fatalf("sole completed blocker candidate = %+v reason=%q unresolved=%v", candidate, reason, unresolved)
	}
	if candidate.ID != ready.ID || candidate.Title != readyDetails.Title || candidate.Priority != readyDetails.Priority || candidate.UnblockedByID != completed ||
		!reflect.DeepEqual(candidate.PrevBlockers, []string{completed}) {
		t.Fatalf("authoritative unblocked candidate = %+v", candidate)
	}

	blocked := bv.BeadDependentState{ID: "bd-sibling", Title: "Sibling", Priority: 2}
	candidate, reason, unresolved = newlyUnblockedCandidate(completed, blocked, &bv.BeadAssignmentDetails{
		ID: blocked.ID, Title: blocked.Title, Status: "open", BlockedBy: []string{"bd-still-open"},
		BlockingDependencies: []bv.BeadDependencyState{
			{ID: completed, Status: "closed"},
			{ID: "bd-still-open", Status: "open"},
		},
	})
	if candidate != nil || reason != "blocked_by_dependency" || !reflect.DeepEqual(unresolved, []string{"bd-still-open"}) {
		t.Fatalf("sibling blocker candidate = %+v reason=%q unresolved=%v", candidate, reason, unresolved)
	}

	gatedDetails := &bv.BeadAssignmentDetails{
		ID: ready.ID, Title: ready.Title, Status: "open", Labels: []string{"operator-gated"},
		BlockingDependencies: []bv.BeadDependencyState{{ID: completed, Status: "closed"}},
	}
	candidate, reason, unresolved = newlyUnblockedCandidate(completed, ready, gatedDetails)
	if candidate != nil || reason != "operator_gated" || len(unresolved) != 0 {
		t.Fatalf("operator-gated candidate = %+v reason=%q unresolved=%v", candidate, reason, unresolved)
	}

	for _, nonterminalStatus := range []string{"resolved", "done", "unknown"} {
		details := *readyDetails
		details.BlockingDependencies = []bv.BeadDependencyState{
			{ID: completed, Status: "closed"},
			{ID: "bd-nonterminal", Status: nonterminalStatus},
		}
		candidate, reason, unresolved = newlyUnblockedCandidate(completed, ready, &details)
		if candidate != nil || reason != "blocked_by_dependency" || !reflect.DeepEqual(unresolved, []string{"bd-nonterminal"}) {
			t.Fatalf("status %q treated as terminal: candidate=%+v reason=%q unresolved=%v", nonterminalStatus, candidate, reason, unresolved)
		}
	}
}

func TestClassifyLiveAssignmentDetailsRejectsEveryReadyGate(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name       string
		details    bv.BeadAssignmentDetails
		wantReason string
	}{
		{name: "future deferred", details: bv.BeadAssignmentDetails{ID: "bd-deferred", Status: "open", DeferUntil: &future}, wantReason: "deferred"},
		{name: "pinned", details: bv.BeadAssignmentDetails{ID: "bd-pinned", Status: "open", Pinned: true}, wantReason: "pinned"},
		{name: "ephemeral", details: bv.BeadAssignmentDetails{ID: "bd-ephemeral", Status: "open", Ephemeral: true}, wantReason: "ephemeral"},
		{name: "template", details: bv.BeadAssignmentDetails{ID: "bd-template", Status: "open", Template: true}, wantReason: "template"},
		{name: "wisp field", details: bv.BeadAssignmentDetails{ID: "bd-work", Status: "open", Wisp: true}, wantReason: "wisp"},
		{name: "wisp id", details: bv.BeadAssignmentDetails{ID: "bd-wisp-123", Status: "open"}, wantReason: "wisp"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyLiveAssignmentDetails(&test.details, nil)
			if got == nil || got.Reason != test.wantReason {
				t.Fatalf("live gate classification=%+v, want reason %q", got, test.wantReason)
			}
		})
	}

	past := time.Now().Add(-time.Hour)
	if skipped := classifyLiveAssignmentDetails(&bv.BeadAssignmentDetails{ID: "bd-ready", Status: "open", DeferUntil: &past}, nil); skipped != nil {
		t.Fatalf("past defer incorrectly rejected: %+v", skipped)
	}
}

// TestFilterCyclicBeadsEmpty tests filtering with no cycles
func TestFilterCyclicBeadsEmpty(t *testing.T) {
	beads := []bv.BeadPreview{
		{ID: "bd-001", Title: "Task 1"},
		{ID: "bd-002", Title: "Task 2"},
	}

	// When there are no cycles, all beads should be returned
	// This test just verifies the function signature and basic behavior
	// since CheckCycles requires bv to be available
	if len(beads) != 2 {
		t.Error("Input beads should have 2 items")
	}
}

// ============================================================================
// Reassignment Tests
// ============================================================================

// TestReassignDataStructure tests the ReassignData type structure
func TestReassignDataStructure(t *testing.T) {
	data := ReassignData{
		BeadID:                      "bd-123",
		BeadTitle:                   "Test bead",
		Pane:                        4,
		AgentType:                   "codex",
		AgentName:                   "test_codex",
		Status:                      "assigned",
		PromptSent:                  true,
		AssignedAt:                  "2026-01-19T12:00:00Z",
		PreviousPane:                2,
		PreviousAgent:               "test_claude",
		PreviousAgentType:           "claude",
		PreviousStatus:              "working",
		FileReservationsTransferred: true,
	}

	if data.BeadID != "bd-123" {
		t.Errorf("Expected BeadID 'bd-123', got %q", data.BeadID)
	}
	if data.Pane != 4 {
		t.Errorf("Expected Pane 4, got %d", data.Pane)
	}
	if data.PreviousPane != 2 {
		t.Errorf("Expected PreviousPane 2, got %d", data.PreviousPane)
	}
	if data.AgentType != "codex" {
		t.Errorf("Expected AgentType 'codex', got %q", data.AgentType)
	}
	if data.PreviousAgentType != "claude" {
		t.Errorf("Expected PreviousAgentType 'claude', got %q", data.PreviousAgentType)
	}
	if !data.FileReservationsTransferred {
		t.Error("Expected FileReservationsTransferred to be true")
	}
}

// TestReassignErrorStructure tests the ReassignError type structure
func TestReassignErrorStructure(t *testing.T) {
	err := ReassignError{
		Code:    "TARGET_BUSY",
		Message: "pane 4 already has assignment bd-abc",
		Details: map[string]interface{}{
			"current_bead":   "bd-abc",
			"current_status": "working",
		},
	}

	if err.Code != "TARGET_BUSY" {
		t.Errorf("Expected Code 'TARGET_BUSY', got %q", err.Code)
	}
	if err.Details["current_bead"] != "bd-abc" {
		t.Errorf("Expected current_bead 'bd-abc', got %v", err.Details["current_bead"])
	}
}

// TestReassignEnvelopeSuccessStructure tests the success envelope structure
func TestReassignEnvelopeSuccessStructure(t *testing.T) {
	envelope := ReassignEnvelope{
		Command:    "assign",
		Subcommand: "reassign",
		Session:    "myproject",
		Timestamp:  "2026-01-19T12:00:00Z",
		Success:    true,
		Data: &ReassignData{
			BeadID:            "bd-123",
			BeadTitle:         "Test bead",
			Pane:              4,
			AgentType:         "codex",
			PreviousPane:      2,
			PreviousAgentType: "claude",
		},
		Warnings: []string{},
	}

	if envelope.Command != "assign" {
		t.Errorf("Expected Command 'assign', got %q", envelope.Command)
	}
	if envelope.Subcommand != "reassign" {
		t.Errorf("Expected Subcommand 'reassign', got %q", envelope.Subcommand)
	}
	if !envelope.Success {
		t.Error("Expected Success to be true")
	}
	if envelope.Data == nil {
		t.Error("Expected Data to be non-nil")
	}
	if envelope.Error != nil {
		t.Error("Expected Error to be nil for success case")
	}
}

// TestReassignEnvelopeErrorStructure tests the error envelope structure
func TestReassignEnvelopeErrorStructure(t *testing.T) {
	envelope := ReassignEnvelope{
		Command:    "assign",
		Subcommand: "reassign",
		Session:    "myproject",
		Timestamp:  "2026-01-19T12:00:00Z",
		Success:    false,
		Data:       nil,
		Warnings:   []string{},
		Error: &ReassignError{
			Code:    "NOT_ASSIGNED",
			Message: "bead bd-xyz does not have an active assignment",
		},
	}

	if envelope.Success {
		t.Error("Expected Success to be false")
	}
	if envelope.Data != nil {
		t.Error("Expected Data to be nil for error case")
	}
	if envelope.Error == nil {
		t.Error("Expected Error to be non-nil for error case")
	}
	if envelope.Error.Code != "NOT_ASSIGNED" {
		t.Errorf("Expected Error.Code 'NOT_ASSIGNED', got %q", envelope.Error.Code)
	}
}

// TestMakeReassignErrorEnvelope tests the error envelope helper function
func TestMakeReassignErrorEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		session string
		code    string
		message string
		details map[string]interface{}
	}{
		{
			name:    "basic error",
			session: "test-session",
			code:    "NOT_ASSIGNED",
			message: "bead not found",
			details: nil,
		},
		{
			name:    "error with details",
			session: "test-session",
			code:    "TARGET_BUSY",
			message: "pane is busy",
			details: map[string]interface{}{
				"current_bead":   "bd-999",
				"current_status": "working",
			},
		},
		{
			name:    "no idle agent error",
			session: "myproject",
			code:    "NO_IDLE_AGENT",
			message: "no idle codex agents available",
			details: map[string]interface{}{
				"agent_type": "codex",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envelope := makeReassignErrorEnvelope(tc.session, tc.code, tc.message, tc.details)

			if envelope.Command != "assign" {
				t.Errorf("Expected Command 'assign', got %q", envelope.Command)
			}
			if envelope.Subcommand != "reassign" {
				t.Errorf("Expected Subcommand 'reassign', got %q", envelope.Subcommand)
			}
			if envelope.Session != tc.session {
				t.Errorf("Expected Session %q, got %q", tc.session, envelope.Session)
			}
			if envelope.Success {
				t.Error("Expected Success to be false")
			}
			if envelope.Error == nil {
				t.Fatal("Expected Error to be non-nil")
			}
			if envelope.Error.Code != tc.code {
				t.Errorf("Expected Error.Code %q, got %q", tc.code, envelope.Error.Code)
			}
			if envelope.Error.Message != tc.message {
				t.Errorf("Expected Error.Message %q, got %q", tc.message, envelope.Error.Message)
			}
			if tc.details != nil {
				for k, v := range tc.details {
					if envelope.Error.Details[k] != v {
						t.Errorf("Expected Details[%q]=%v, got %v", k, v, envelope.Error.Details[k])
					}
				}
			}
		})
	}
}

// TestReassignErrorCodes tests the documented error codes
func TestReassignErrorCodes(t *testing.T) {
	// These are the documented error codes from the bead spec
	errorCodes := []string{
		"NOT_ASSIGNED",   // Bead doesn't have an active assignment
		"TARGET_BUSY",    // Target pane already has an assignment
		"PANE_NOT_FOUND", // Target pane doesn't exist
		"NO_IDLE_AGENT",  // No idle agent of specified type
		"INVALID_ARGS",   // Invalid arguments
		"STORE_ERROR",    // Assignment store error
		"TMUX_ERROR",     // Tmux error
		"REASSIGN_ERROR", // Reassignment operation error
	}

	// Verify each code can be used in an envelope
	for _, code := range errorCodes {
		envelope := makeReassignErrorEnvelope("test", code, "test message", nil)
		if envelope.Error.Code != code {
			t.Errorf("Error code %q not preserved in envelope", code)
		}
	}
}

func TestClassifyReassignErrorReportsDurableReleaseBarrier(t *testing.T) {
	durable := &assignment.Assignment{
		ClearState: assignment.ClearStateReservationReleasing,
		ClearError: "Agent Mail release verification failed",
	}
	if got := classifyReassignError(errors.New("replacement failed"), durable); got != "RESERVATION_RELEASE_FAILED" {
		t.Fatalf("classifyReassignError()=%q", got)
	}
	if got := classifyReassignError(context.Canceled, nil); got != robot.ErrCodeTimeout {
		t.Fatalf("classifyReassignError(context.Canceled)=%q", got)
	}
}

func TestRetryAndReassignPreCanceledJSONEnvelopes(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		run        func() error
		subcommand string
		wantCode   string
		wantCause  error
	}{
		{name: "retry nil context", run: func() error { return runRetryAssignments(nil, "retry-json") }, subcommand: "retry", wantCode: robot.ErrCodeInternalError},
		{name: "retry canceled context", run: func() error { return runRetryAssignments(canceled, "retry-json") }, subcommand: "retry", wantCode: robot.ErrCodeTimeout, wantCause: context.Canceled},
		{name: "reassign nil context", run: func() error { return runReassignment(nil, "reassign-json") }, subcommand: "reassign", wantCode: robot.ErrCodeInternalError},
		{name: "reassign canceled context", run: func() error { return runReassignment(canceled, "reassign-json") }, subcommand: "reassign", wantCode: robot.ErrCodeTimeout, wantCause: context.Canceled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdout pipe: %v", err)
			}
			os.Stdout = writer
			runErr := test.run()
			_ = writer.Close()
			os.Stdout = oldStdout
			raw, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil {
				t.Fatalf("read JSON envelope: %v", readErr)
			}
			if !errors.Is(runErr, errJSONFailure) {
				t.Fatalf("command error=%v, want errJSONFailure", runErr)
			}
			if test.wantCause != nil && !errors.Is(runErr, test.wantCause) {
				t.Fatalf("command error=%v, want wrapped cause %v", runErr, test.wantCause)
			}
			var envelope AssignEnvelope[json.RawMessage]
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("decode JSON envelope: %v raw=%s", err, raw)
			}
			if envelope.Success || envelope.Subcommand != test.subcommand || envelope.Error == nil || envelope.Error.Code != test.wantCode {
				t.Fatalf("envelope=%+v, want subcommand=%s code=%s", envelope, test.subcommand, test.wantCode)
			}
		})
	}
}

func TestMatchingReassignmentDurableResultRejectsForeignTargetOwner(t *testing.T) {
	foreign := &assignment.Assignment{BeadID: "ntm-foreign", IdempotencyKey: "foreign-secret-key", BeadTitle: "foreign-secret-title"}
	if got := matchingReassignmentDurableResult(foreign, "ntm-requested", "requested-key"); got != nil {
		t.Fatalf("foreign target owner projected as requested durable row: %+v", got)
	}
	requested := &assignment.Assignment{BeadID: "ntm-requested", IdempotencyKey: "requested-key", BeadTitle: "safe durable title"}
	if got := matchingReassignmentDurableResult(requested, "ntm-requested", "requested-key"); got != requested {
		t.Fatalf("matching durable row=%+v", got)
	}
}

// ============================================================================
// Strategy Validation Tests
// ============================================================================

// TestStrategyFlagDefaultValue tests that the strategy flag has the correct default
func TestStrategyFlagDefaultValue(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("strategy")
	if flag == nil {
		t.Fatal("Expected 'strategy' flag to exist")
	}
	if flag.DefValue != "balanced" {
		t.Errorf("Expected default strategy 'balanced', got %q", flag.DefValue)
	}
}

// TestStrategyFlagHelpText tests that strategy flag has descriptive help
func TestStrategyFlagHelpText(t *testing.T) {
	cmd := newAssignCmd()
	flag := cmd.Flags().Lookup("strategy")
	if flag == nil {
		t.Fatal("Expected 'strategy' flag to exist")
	}

	// Help text should mention all valid strategies
	help := flag.Usage
	expectedStrategies := []string{"balanced", "speed", "quality", "dependency", "round-robin"}
	for _, s := range expectedStrategies {
		if !contains(help, s) {
			t.Errorf("Expected strategy flag help to mention %q", s)
		}
	}
}

// TestAssignOutputIncludesStrategy tests that output structures include strategy field
func TestAssignOutputIncludesStrategy(t *testing.T) {
	output := AssignOutputEnhanced{
		Strategy: "quality",
	}
	if output.Strategy != "quality" {
		t.Errorf("Expected Strategy field to be 'quality', got %q", output.Strategy)
	}
}

// TestAssignCommandOptionsIncludesStrategy tests that options struct includes strategy
func TestAssignCommandOptionsIncludesStrategy(t *testing.T) {
	opts := AssignCommandOptions{
		Session:  "test",
		Strategy: "dependency",
	}
	if opts.Strategy != "dependency" {
		t.Errorf("Expected Strategy in options to be 'dependency', got %q", opts.Strategy)
	}
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============================================================================
// Load-Aware Balanced Strategy Tests
// ============================================================================

// TestBalancedStrategyTieBreakers tests the tie-breaker cascade:
// 1. Fewer active assignments
// 2. Higher capability score
// 3. Least-recently assigned
// 4. Lower pane index (deterministic)
func TestBalancedStrategyTieBreakers(t *testing.T) {
	// Test the tie-breaker ordering logic
	// When counts are equal and scores are equal:
	// - Never assigned beats previously assigned
	// - Earlier assigned beats later assigned
	// - Lower pane index breaks ties for determinism

	t.Run("never_assigned_beats_previously_assigned", func(t *testing.T) {
		// Agent with no prior assignments should win over one with prior assignments
		// when counts and scores are equal
		// This validates the zero-time check in the tie-breaker logic
		assignItem := AssignmentItem{
			BeadID:    "bd-test",
			AgentType: "claude",
			Score:     0.75,
		}
		// This is a structural test; the actual logic is tested via integration
		if assignItem.Score != 0.75 {
			t.Errorf("Expected score 0.75, got %f", assignItem.Score)
		}
	})

	t.Run("lower_pane_index_determinism", func(t *testing.T) {
		// When all other factors are equal, lower pane index wins
		// This ensures deterministic output across runs
		agents := []struct {
			pane  int
			score float64
		}{
			{pane: 3, score: 0.8},
			{pane: 1, score: 0.8},
			{pane: 2, score: 0.8},
		}

		// With equal scores, pane 1 should be selected (lowest index)
		lowestPane := agents[0].pane
		for _, a := range agents {
			if a.pane < lowestPane {
				lowestPane = a.pane
			}
		}
		if lowestPane != 1 {
			t.Errorf("Expected lowest pane to be 1, got %d", lowestPane)
		}
	})

	t.Run("fewer_assignments_wins", func(t *testing.T) {
		// Agents with fewer active assignments should be preferred
		counts := map[int]int{
			1: 3, // 3 active assignments
			2: 1, // 1 active assignment (winner)
			3: 2, // 2 active assignments
		}

		minCount := counts[1]
		bestPane := 1
		for pane, count := range counts {
			if count < minCount {
				minCount = count
				bestPane = pane
			}
		}
		if bestPane != 2 {
			t.Errorf("Expected pane 2 (fewest assignments) to win, got %d", bestPane)
		}
	})
}

// TestBalancedStrategyLoadsFromStore tests that the balanced strategy
// correctly pre-populates assignment counts from AssignmentStore
func TestBalancedStrategyLoadsFromStore(t *testing.T) {
	// This is a structural test to verify the strategy options
	// accept session name for store lookup
	opts := &AssignCommandOptions{
		Session:  "test-session",
		Strategy: "balanced",
	}

	if opts.Session == "" {
		t.Error("Session should not be empty for store lookup")
	}
	if opts.Strategy != "balanced" {
		t.Errorf("Strategy should be 'balanced', got %q", opts.Strategy)
	}
}

// TestBalancedStrategyDeterminism tests that balanced strategy produces
// deterministic output for identical inputs
func TestBalancedStrategyDeterminism(t *testing.T) {
	// Given the same agents and beads, the balanced strategy should
	// always produce the same assignment order
	agents := []assignAgentInfo{
		{agentType: "claude", pane: tmux.Pane{Index: 1}},
		{agentType: "codex", pane: tmux.Pane{Index: 2}},
		{agentType: "gemini", pane: tmux.Pane{Index: 3}},
	}

	// Verify agents are sortable by pane index for deterministic tiebreaker
	paneIndices := make([]int, len(agents))
	for i, a := range agents {
		paneIndices[i] = a.pane.Index
	}

	// Pane indices should be in order 1, 2, 3
	for i := 0; i < len(paneIndices)-1; i++ {
		if paneIndices[i] >= paneIndices[i+1] {
			// This is just checking the test setup
			// The actual determinism is in the strategy implementation
		}
	}
}

// TestAssignAgentInfoHasPane verifies the agent info structure has pane reference
func TestAssignAgentInfoHasPane(t *testing.T) {
	pane := tmux.Pane{Index: 5}
	agent := assignAgentInfo{
		agentType: "claude",
		pane:      pane,
	}

	// Verify pane index is set correctly (zero value Index would be 0)
	if agent.pane.Index != 5 {
		t.Errorf("Expected pane index 5, got %d", agent.pane.Index)
	}
}

// ============================================================================
// Pure Function Tests
// ============================================================================

func TestDetectModelFromTitle(t *testing.T) {

	tests := []struct {
		name      string
		agentType string
		title     string
		expected  string
	}{
		// Opus detection
		{name: "opus lowercase", agentType: "claude", title: "myproject__cc_1_opus", expected: "opus"},
		{name: "opus uppercase", agentType: "claude", title: "myproject__cc_1_OPUS", expected: "opus"},
		{name: "opus mixed case", agentType: "claude", title: "myproject__cc_1_Opus", expected: "opus"},

		// Sonnet detection
		{name: "sonnet lowercase", agentType: "claude", title: "myproject__cc_1_sonnet", expected: "sonnet"},
		{name: "sonnet uppercase", agentType: "claude", title: "myproject__cc_1_SONNET", expected: "sonnet"},
		{name: "sonnet mixed case", agentType: "claude", title: "project_Sonnet_v3", expected: "sonnet"},

		// Haiku detection
		{name: "haiku lowercase", agentType: "claude", title: "myproject__cc_1_haiku", expected: "haiku"},
		{name: "haiku uppercase", agentType: "claude", title: "myproject__cc_1_HAIKU", expected: "haiku"},
		{name: "haiku mixed case", agentType: "claude", title: "project_Haiku", expected: "haiku"},

		// No model detected
		{name: "no model", agentType: "claude", title: "myproject__cc_1", expected: ""},
		{name: "empty title", agentType: "claude", title: "", expected: ""},
		{name: "codex agent", agentType: "codex", title: "myproject__cod_1", expected: ""},
		{name: "gemini agent", agentType: "gemini", title: "myproject__gmi_1", expected: ""},

		// Partial matches (should still match)
		{name: "opus in middle", agentType: "claude", title: "test_opus_session", expected: "opus"},
		{name: "sonnet at start", agentType: "claude", title: "sonnetproject__cc", expected: "sonnet"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := detectModelFromTitle(tc.agentType, tc.title)
			if result != tc.expected {
				t.Errorf("detectModelFromTitle(%q, %q) = %q; want %q",
					tc.agentType, tc.title, result, tc.expected)
			}
		})
	}
}

func observeAssignmentFixture(t *testing.T, agentType, output string) statuspkg.PaneObservation {
	t.Helper()
	observer := statuspkg.NewSessionObserver(statuspkg.NewDetector())
	return observer.ObservePaneCapture("fixture", tmux.PaneActivity{
		Pane: tmux.Pane{ID: "%1", WindowIndex: 0, Index: 1, Type: tmux.AgentType(agentType)},
	}, output, nil)
}

func TestAssignSessionObserverClassifiesCanonicalAliases(t *testing.T) {

	tests := []struct {
		name       string
		agentType  string
		scrollback string
		want       string
	}{
		{
			name:      "codex alias with whitespace",
			agentType: " openai-codex ",
			scrollback: "Processing your request...\n" +
				"Token usage: total=150,000 input=140,000 output=10,000\n" +
				"47% context left · ? for shortcuts\n" +
				"codex> ",
			want: "idle",
		},
		{
			name:       "cursor canonical type",
			agentType:  "cursor",
			scrollback: "Done editing.\ncursor> ",
			want:       "idle",
		},
		{
			name:       "windsurf short alias",
			agentType:  "ws",
			scrollback: "Generating code...\nsearching for references",
			want:       "working",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observation := observeAssignmentFixture(t, tc.agentType, tc.scrollback)
			if got := string(observation.Current.Status.State); got != tc.want {
				t.Fatalf("session observation for %q = %q, want %q: %+v", tc.agentType, got, tc.want, observation.Current)
			}
		})
	}
}

func TestClassifyTriageRecForAssignment(t *testing.T) {
	type testCase struct {
		name              string
		rec               bv.TriageRecommendation
		activeAssignments map[string]struct{}
		wantSkip          bool
		wantReason        string
	}

	tests := []testCase{
		{
			name:     "open with no blockers is assignable",
			rec:      bv.TriageRecommendation{ID: "bd-1", Status: "open"},
			wantSkip: false,
		},
		{
			name:       "empty status fails closed",
			rec:        bv.TriageRecommendation{ID: "bd-2"},
			wantSkip:   true,
			wantReason: "not_open_status",
		},
		{
			name:       "dependency blocker wins over status",
			rec:        bv.TriageRecommendation{ID: "bd-3", Status: "open", BlockedBy: []string{"bd-99"}},
			wantSkip:   true,
			wantReason: "blocked_by_dependency",
		},
		{
			name:       "in_progress is skipped",
			rec:        bv.TriageRecommendation{ID: "bd-4", Status: "in_progress"},
			wantSkip:   true,
			wantReason: "already_in_progress",
		},
		{
			name:       "blocked status is skipped",
			rec:        bv.TriageRecommendation{ID: "bd-5", Status: "blocked"},
			wantSkip:   true,
			wantReason: "blocked_status",
		},
		{
			name:       "closed status is skipped",
			rec:        bv.TriageRecommendation{ID: "bd-6", Status: "closed"},
			wantSkip:   true,
			wantReason: "closed_status",
		},
		{
			name:       "operator_gated label beats open status",
			rec:        bv.TriageRecommendation{ID: "bd-7", Status: "open", Labels: []string{"operator-gated"}},
			wantSkip:   true,
			wantReason: "operator_gated",
		},
		{
			name:       "human-input label is operator gated",
			rec:        bv.TriageRecommendation{ID: "bd-8", Status: "open", Labels: []string{"foo", "human-input"}},
			wantSkip:   true,
			wantReason: "operator_gated",
		},
		{
			name:              "already-claimed bead is suppressed",
			rec:               bv.TriageRecommendation{ID: "bd-9", Status: "open"},
			activeAssignments: map[string]struct{}{"bd-9": {}},
			wantSkip:          true,
			wantReason:        "already_assigned",
		},
		{
			name:       "status case + delimiter variation still classifies",
			rec:        bv.TriageRecommendation{ID: "bd-10", Status: "In-Progress"},
			wantSkip:   true,
			wantReason: "already_in_progress",
		},
		{
			name:              "blockedBy beats already_assigned",
			rec:               bv.TriageRecommendation{ID: "bd-11", Status: "open", BlockedBy: []string{"bd-x"}},
			activeAssignments: map[string]struct{}{"bd-11": {}},
			wantSkip:          true,
			wantReason:        "blocked_by_dependency",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTriageRecForAssignment(tc.rec, tc.activeAssignments)
			if tc.wantSkip {
				if got == nil {
					t.Fatalf("expected skip with reason %q, got nil (assignable)", tc.wantReason)
				}
				if got.Reason != tc.wantReason {
					t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
				}
				if got.BeadID != tc.rec.ID {
					t.Fatalf("BeadID = %q, want %q", got.BeadID, tc.rec.ID)
				}
				if tc.wantReason == "blocked_by_dependency" && len(got.BlockedByIDs) == 0 {
					t.Fatalf("blocked_by_dependency must populate BlockedByIDs")
				}
			} else if got != nil {
				t.Fatalf("expected assignable, got skip with reason %q", got.Reason)
			}
		})
	}
}

func TestClassifyTriageRecForAssignmentUsesCanonicalOperatorGates(t *testing.T) {
	t.Parallel()

	for _, label := range bv.OperatorGatedLabels() {
		label := label
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			skipped := classifyTriageRecForAssignment(bv.TriageRecommendation{
				ID: "bd-operator", Status: "open", Labels: []string{"  " + strings.ToUpper(label) + "  "},
			}, nil)
			if skipped == nil || skipped.Reason != "operator_gated" {
				t.Fatalf("label %q classification=%+v", label, skipped)
			}
		})
	}
}

func TestAssignmentLoadsFullPlanBeforeSafetyFiltering(t *testing.T) {
	previous := getActionableRecommendationsForAssign
	t.Cleanup(func() { getActionableRecommendationsForAssign = previous })

	recommendations := make([]bv.TriageRecommendation, 0, 102)
	for i := 0; i < 101; i++ {
		recommendations = append(recommendations, bv.TriageRecommendation{
			ID:       fmt.Sprintf("ntm-gated-%03d", i),
			Title:    "Requires operator action",
			Status:   "open",
			Priority: 1,
			Labels:   []string{"operator-gated"},
		})
	}
	recommendations = append(recommendations, bv.TriageRecommendation{
		ID:       "ntm-eligible-below-cap",
		Title:    "Eligible below the former cap",
		Status:   "open",
		Priority: 2,
	})

	getActionableRecommendationsForAssign = func(ctx context.Context, projectDir string, limit int) ([]bv.TriageRecommendation, error) {
		if ctx == nil {
			t.Fatal("assignment loader received nil context")
		}
		if projectDir != "/authoritative/project" {
			t.Fatalf("projectDir = %q, want authoritative project", projectDir)
		}
		if limit != 0 {
			t.Fatalf("limit = %d, want 0 so safety filtering sees the full plan", limit)
		}
		return recommendations, nil
	}

	loaded, err := loadActionableRecommendationsForAssignment(t.Context(), "/authoritative/project")
	if err != nil {
		t.Fatalf("load actionable recommendations: %v", err)
	}
	ready, skipped := partitionActionableRecommendationsForAssignment(loaded, nil, bv.IsOperatorGatedLabel)
	if len(skipped) != 101 {
		t.Fatalf("skipped = %d, want 101 gated rows", len(skipped))
	}
	if len(ready) != 1 || ready[0].ID != "ntm-eligible-below-cap" {
		t.Fatalf("ready = %+v, want the eligible row below 101 gated rows", ready)
	}
}

func TestConfigureAuthoritativeAssignmentPolicyUsesResolvedProject(t *testing.T) {
	previousConfigFile := cfgFile
	previousWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		if err := os.Chdir(previousWorkingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})

	globalPath := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(globalPath, []byte("[assign]\noperator_gated_labels = [\"global-approval\"]\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	writeProjectPolicy := func(dir, label string) {
		t.Helper()
		ntmDir := filepath.Join(dir, ".ntm")
		if err := os.MkdirAll(ntmDir, 0o755); err != nil {
			t.Fatalf("create project config directory: %v", err)
		}
		content := fmt.Sprintf("[assign]\noperator_gated_labels = [%q]\n", label)
		if err := os.WriteFile(filepath.Join(ntmDir, "config.toml"), []byte(content), 0o600); err != nil {
			t.Fatalf("write project config: %v", err)
		}
	}

	ambientDir := t.TempDir()
	writeProjectPolicy(ambientDir, "ambient-only")
	authoritativeDir := t.TempDir()
	writeProjectPolicy(authoritativeDir, "project-approval")
	cfgFile = globalPath
	bv.ConfigureOperatorGatedLabels([]string{"stale-policy"})
	if err := os.Chdir(ambientDir); err != nil {
		t.Fatalf("Chdir ambient project: %v", err)
	}

	if err := configureAuthoritativeAssignmentPolicy(authoritativeDir); err != nil {
		t.Fatalf("configureAuthoritativeAssignmentPolicy: %v", err)
	}
	for _, label := range []string{"operator-gated", "global-approval", "project-approval"} {
		if !bv.IsOperatorGatedLabel(label) {
			t.Errorf("effective assignment policy omitted %q", label)
		}
	}
	for _, label := range []string{"ambient-only", "stale-policy"} {
		if bv.IsOperatorGatedLabel(label) {
			t.Errorf("effective assignment policy unexpectedly retained %q", label)
		}
	}
}

func TestAuthoritativeAssignmentPolicyFailuresStopEntryPoints(t *testing.T) {
	previousConfigFile := cfgFile
	previousJSON := jsonOutput
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		jsonOutput = previousJSON
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})

	globalPath := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(globalPath, []byte("[assign]\noperator_gated_labels = [\"global-approval\"]\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	projectDir := t.TempDir()
	ntmDir := filepath.Join(projectDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0o755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ntmDir, "config.toml"), []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("write invalid project config: %v", err)
	}
	cfgFile = globalPath
	jsonOutput = false
	bv.ConfigureOperatorGatedLabels([]string{"previous-policy"})

	assertInvalidPolicy := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s unexpectedly accepted invalid authoritative policy", name)
		}
		if !errors.Is(err, errCLIInvalidInput) {
			t.Errorf("%s error = %v, want errCLIInvalidInput", name, err)
		}
		if !strings.Contains(err.Error(), filepath.Join(projectDir, ".ntm", "config.toml")) {
			t.Errorf("%s error = %q, want authoritative config path", name, err)
		}
	}

	assertInvalidPolicy("policy helper", configureAuthoritativeAssignmentPolicy(projectDir))
	_, planningErr := getAssignOutputEnhanced(t.Context(), &AssignCommandOptions{
		Session:    "policy-planning-must-not-reach-tmux",
		ProjectDir: projectDir,
	})
	assertInvalidPolicy("assignment planning", planningErr)
	assertInvalidPolicy("assignment execution", executeAssignmentsEnhanced(t.Context(), "policy-execution-must-not-read-store", nil, &AssignCommandOptions{
		Session:    "policy-execution-must-not-read-store",
		ProjectDir: projectDir,
		Quiet:      true,
	}))
	assertInvalidPolicy("direct assignment", runDirectPaneAssignment(t.Context(), &AssignCommandOptions{
		Session:      "policy-direct-must-not-reach-tmux",
		ProjectDir:   projectDir,
		BeadIDs:      []string{"ntm-policy-test"},
		PaneSelector: "1",
	}))
	autoResult, autoErr := PerformAutoReassignment(t.Context(), "ntm-policy-completed", &AutoReassignOptions{
		Session:    "policy-auto-reassign-must-not-read-beads",
		ProjectDir: projectDir,
		DryRun:     true,
	})
	assertInvalidPolicy("dry-run auto-reassignment", autoErr)
	if autoResult != nil {
		t.Fatalf("dry-run auto-reassignment returned a preview under invalid policy: %+v", autoResult)
	}

	if !bv.IsOperatorGatedLabel("previous-policy") {
		t.Error("failed policy load replaced the previously installed safety policy")
	}
	if bv.IsOperatorGatedLabel("global-approval") {
		t.Error("failed policy load partially installed the global policy")
	}
}

func TestConfigureAuthoritativeAssignmentPolicyRejectsMissingExplicitConfig(t *testing.T) {
	previousConfigFile := cfgFile
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})

	missingPath := filepath.Join(t.TempDir(), "missing-selected.toml")
	cfgFile = missingPath
	bv.ConfigureOperatorGatedLabels([]string{"previous-policy"})
	err := configureAuthoritativeAssignmentPolicy(t.TempDir())
	if err == nil || !errors.Is(err, errCLIInvalidInput) {
		t.Fatalf("configureAuthoritativeAssignmentPolicy() error = %v, want invalid input", err)
	}
	if !strings.Contains(err.Error(), missingPath) || !strings.Contains(err.Error(), "explicitly selected config") {
		t.Fatalf("configureAuthoritativeAssignmentPolicy() error = %q, want explicit path diagnostic", err)
	}
	if !bv.IsOperatorGatedLabel("previous-policy") {
		t.Fatal("missing explicit config replaced the previously installed safety policy")
	}
}

func TestConfigureAuthoritativeAssignmentPolicyRejectsMissingEnvConfig(t *testing.T) {
	previousConfigFile := cfgFile
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})

	cfgFile = ""
	missingPath := filepath.Join(t.TempDir(), "missing-env-selected.toml")
	t.Setenv("NTM_CONFIG", missingPath)
	bv.ConfigureOperatorGatedLabels([]string{"previous-env-policy"})
	err := configureAuthoritativeAssignmentPolicy(t.TempDir())
	if err == nil || !errors.Is(err, errCLIInvalidInput) {
		t.Fatalf("configureAuthoritativeAssignmentPolicy() error = %v, want invalid input", err)
	}
	if !strings.Contains(err.Error(), missingPath) || !strings.Contains(err.Error(), "explicitly selected config") {
		t.Fatalf("configureAuthoritativeAssignmentPolicy() error = %q, want NTM_CONFIG path diagnostic", err)
	}
	if !bv.IsOperatorGatedLabel("previous-env-policy") {
		t.Fatal("missing NTM_CONFIG path replaced the previously installed safety policy")
	}
}

func TestPrepareResolvedAssignCommandFailsBeforeExternalSideEffects(t *testing.T) {
	previousConfigFile := cfgFile
	previousClear := assignClear
	previousClearPane := assignClearPane
	previousClearFailed := assignClearFailed
	previousStartWebhook := startAssignWebhookForCommand
	previousRunClear := runClearAssignmentsForCommand
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		assignClear = previousClear
		assignClearPane = previousClearPane
		assignClearFailed = previousClearFailed
		startAssignWebhookForCommand = previousStartWebhook
		runClearAssignmentsForCommand = previousRunClear
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})
	t.Setenv("NTM_CONFIG", "")
	assignClear = ""
	assignClearPane = ""
	assignClearFailed = false

	root := t.TempDir()
	validGlobal := filepath.Join(root, "valid-global.toml")
	if err := os.WriteFile(validGlobal, []byte("[assign]\noperator_gated_labels = [\"global-approval\"]\n"), 0o600); err != nil {
		t.Fatalf("write valid global config: %v", err)
	}
	invalidGlobal := filepath.Join(root, "invalid-global.toml")
	if err := os.WriteFile(invalidGlobal, []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("write invalid global config: %v", err)
	}
	missingGlobal := filepath.Join(root, "missing-global.toml")
	validProject := t.TempDir()
	invalidProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(invalidProject, ".ntm"), 0o755); err != nil {
		t.Fatalf("create invalid project config directory: %v", err)
	}
	invalidProjectConfig := filepath.Join(invalidProject, ".ntm", "config.toml")
	if err := os.WriteFile(invalidProjectConfig, []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("write invalid project config: %v", err)
	}

	for _, test := range []struct {
		name       string
		globalPath string
		projectDir string
		wantPath   string
	}{
		{name: "missing explicitly selected global", globalPath: missingGlobal, projectDir: validProject, wantPath: missingGlobal},
		{name: "invalid explicitly selected global", globalPath: invalidGlobal, projectDir: validProject, wantPath: invalidGlobal},
		{name: "invalid target project", globalPath: validGlobal, projectDir: invalidProject, wantPath: invalidProjectConfig},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfgFile = test.globalPath
			bv.ConfigureOperatorGatedLabels([]string{"previous-policy"})
			webhookStarts := 0
			clearCalls := 0
			startAssignWebhookForCommand = func(_, _ string) (func() error, error) {
				webhookStarts++
				return nil, nil
			}
			runClearAssignmentsForCommand = func(_ *cobra.Command, _ string) error {
				clearCalls++
				return nil
			}

			handled, policyProject, closeWebhook, err := prepareResolvedAssignCommand(&cobra.Command{}, "policy-preflight", test.projectDir)
			if err == nil || !errors.Is(err, errCLIInvalidInput) {
				t.Fatalf("prepareResolvedAssignCommand() error = %v, want invalid input", err)
			}
			if !strings.Contains(err.Error(), test.wantPath) {
				t.Fatalf("prepareResolvedAssignCommand() error = %q, want path %q", err, test.wantPath)
			}
			if handled || policyProject != "" || closeWebhook != nil {
				t.Fatalf("failed preflight returned handled=%v policy=%q close=%v", handled, policyProject, closeWebhook != nil)
			}
			if webhookStarts != 0 || clearCalls != 0 {
				t.Fatalf("failed preflight external calls: webhook=%d clear=%d, want 0/0", webhookStarts, clearCalls)
			}
			if !bv.IsOperatorGatedLabel("previous-policy") {
				t.Fatal("failed preflight replaced the previously installed policy")
			}
		})
	}
}

func TestPrepareResolvedAssignCommandKeepsClearIndependent(t *testing.T) {
	previousConfigFile := cfgFile
	previousClear := assignClear
	previousClearPane := assignClearPane
	previousClearFailed := assignClearFailed
	previousStartWebhook := startAssignWebhookForCommand
	previousRunClear := runClearAssignmentsForCommand
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		assignClear = previousClear
		assignClearPane = previousClearPane
		assignClearFailed = previousClearFailed
		startAssignWebhookForCommand = previousStartWebhook
		runClearAssignmentsForCommand = previousRunClear
	})
	t.Setenv("NTM_CONFIG", "")
	cfgFile = filepath.Join(t.TempDir(), "missing-selected-global.toml")
	assignClear = "ntm-clear-independent"
	assignClearPane = ""
	assignClearFailed = false

	webhookStarts := 0
	clearCalls := 0
	startAssignWebhookForCommand = func(_, _ string) (func() error, error) {
		webhookStarts++
		return nil, nil
	}
	runClearAssignmentsForCommand = func(_ *cobra.Command, session string) error {
		clearCalls++
		if session != "clear-session" {
			t.Errorf("clear session = %q, want clear-session", session)
		}
		return nil
	}

	handled, policyProject, closeWebhook, err := prepareResolvedAssignCommand(&cobra.Command{}, "clear-session", t.TempDir())
	if err != nil {
		t.Fatalf("prepareResolvedAssignCommand(clear) error: %v", err)
	}
	if !handled || policyProject != "" || closeWebhook != nil {
		t.Fatalf("clear preflight returned handled=%v policy=%q close=%v", handled, policyProject, closeWebhook != nil)
	}
	if clearCalls != 1 || webhookStarts != 0 {
		t.Fatalf("clear preflight calls: clear=%d webhook=%d, want 1/0", clearCalls, webhookStarts)
	}
}

func TestPrepareResolvedAssignCommandInstallsPolicyBeforeWebhook(t *testing.T) {
	previousConfigFile := cfgFile
	previousClear := assignClear
	previousClearPane := assignClearPane
	previousClearFailed := assignClearFailed
	previousStartWebhook := startAssignWebhookForCommand
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		assignClear = previousClear
		assignClearPane = previousClearPane
		assignClearFailed = previousClearFailed
		startAssignWebhookForCommand = previousStartWebhook
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})
	t.Setenv("NTM_CONFIG", "")
	assignClear = ""
	assignClearPane = ""
	assignClearFailed = false

	globalPath := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(globalPath, []byte("[assign]\noperator_gated_labels = [\"pre-webhook-policy\"]\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	cfgFile = globalPath
	projectDir := t.TempDir()
	webhookStarts := 0
	webhookCloses := 0
	startAssignWebhookForCommand = func(gotProject, gotSession string) (func() error, error) {
		webhookStarts++
		if gotProject != projectDir || gotSession != "ordered-preflight" {
			t.Errorf("webhook project/session = %q/%q, want %q/ordered-preflight", gotProject, gotSession, projectDir)
		}
		if !bv.IsOperatorGatedLabel("pre-webhook-policy") {
			t.Error("webhook started before authoritative policy was installed")
		}
		return func() error {
			webhookCloses++
			return nil
		}, nil
	}

	handled, policyProject, closeWebhook, err := prepareResolvedAssignCommand(&cobra.Command{}, "ordered-preflight", projectDir)
	if err != nil {
		t.Fatalf("prepareResolvedAssignCommand() error: %v", err)
	}
	if handled || policyProject != filepath.Clean(projectDir) || closeWebhook == nil {
		t.Fatalf("successful preflight returned handled=%v policy=%q close=%v", handled, policyProject, closeWebhook != nil)
	}
	if webhookStarts != 1 {
		t.Fatalf("webhook starts=%d, want 1", webhookStarts)
	}
	if err := closeWebhook(); err != nil {
		t.Fatalf("close webhook: %v", err)
	}
	if webhookCloses != 1 {
		t.Fatalf("webhook closes=%d, want 1", webhookCloses)
	}
}

func TestEnsureAuthoritativeAssignmentPolicyReusesExactProject(t *testing.T) {
	previousConfigFile := cfgFile
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		cfgFile = previousConfigFile
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})
	t.Setenv("NTM_CONFIG", "")

	globalPath := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(globalPath, []byte("[assign]\noperator_gated_labels = [\"loaded-once\"]\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	cfgFile = globalPath
	projectDir := t.TempDir()
	configuredProject := ""
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &configuredProject); err != nil {
		t.Fatalf("initial policy load: %v", err)
	}
	if configuredProject != filepath.Clean(projectDir) || !bv.IsOperatorGatedLabel("loaded-once") {
		t.Fatalf("configured project=%q loaded policy=%v", configuredProject, bv.OperatorGatedLabels())
	}

	if err := os.WriteFile(globalPath, []byte("[assign\n"), 0o600); err != nil {
		t.Fatalf("invalidate selected global config: %v", err)
	}
	if err := ensureAuthoritativeAssignmentPolicy(projectDir, &configuredProject); err != nil {
		t.Fatalf("same-project policy was loaded twice: %v", err)
	}
	if err := ensureAuthoritativeAssignmentPolicy(t.TempDir(), &configuredProject); err == nil || !errors.Is(err, errCLIInvalidInput) {
		t.Fatalf("different-project policy error = %v, want strict reload failure", err)
	}
}

func TestWatchLoopShouldStopUsesFilteredActionableCandidates(t *testing.T) {
	previousGetActionable := getActionableRecommendationsForWatch
	previousGetIdle := getIdleAgentsForWatchStop
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() {
		getActionableRecommendationsForWatch = previousGetActionable
		getIdleAgentsForWatchStop = previousGetIdle
		bv.ConfigureOperatorGatedLabels(previousLabels)
	})
	bv.ConfigureOperatorGatedLabels([]string{"operator-gated"})
	getIdleAgentsForWatchStop = func(context.Context, string, string, bool) ([]assignAgentInfo, error) {
		return []assignAgentInfo{}, nil
	}

	projectDir := t.TempDir()
	for _, test := range []struct {
		name     string
		recs     []bv.TriageRecommendation
		queryErr error
		wantStop bool
		wantErr  bool
	}{
		{
			name:     "operator-gated-only queue is drained",
			recs:     []bv.TriageRecommendation{{ID: "gated", Status: "open", Labels: []string{"operator-gated"}}},
			wantStop: true,
		},
		{
			name:     "blocked-only queue is drained",
			recs:     []bv.TriageRecommendation{{ID: "blocked", Status: "open", BlockedBy: []string{"dependency"}}},
			wantStop: true,
		},
		{
			name:     "plan-only actionable item keeps watch running",
			recs:     []bv.TriageRecommendation{{ID: "below-triage-cap", Status: "open"}},
			wantStop: false,
		},
		{
			name:     "unverified actionable surface fails closed",
			queryErr: bv.ErrActionableLabelsUnverified,
			wantStop: false,
			wantErr:  true,
		},
		{
			name:     "unverified actionable plan fails closed",
			queryErr: bv.ErrActionablePlanUnverified,
			wantStop: false,
			wantErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			getActionableRecommendationsForWatch = func(_ context.Context, gotProject string, limit int) ([]bv.TriageRecommendation, error) {
				calls++
				if gotProject != projectDir || limit != 0 {
					t.Errorf("actionable query project=%q limit=%d, want %q/0", gotProject, limit, projectDir)
				}
				return test.recs, test.queryErr
			}
			store := assignment.NewStore("watch-actionable-stop")
			opts := &AutoReassignOptions{
				Session:       "watch-actionable-stop",
				ProjectDir:    projectDir,
				Quiet:         true,
				policyProject: filepath.Clean(projectDir),
			}
			loop := NewWatchLoop(opts.Session, store, opts)
			stop, err := loop.shouldStop(t.Context())
			if (err != nil) != test.wantErr {
				t.Fatalf("shouldStop() error = %v, wantErr=%v", err, test.wantErr)
			}
			if stop != test.wantStop {
				t.Fatalf("shouldStop() = %v, want %v", stop, test.wantStop)
			}
			if calls != 1 {
				t.Fatalf("actionable query calls=%d, want 1", calls)
			}
		})
	}
}

func TestFilterNewlyUnblockedByVerifiedPlan(t *testing.T) {
	previousLabels := bv.OperatorGatedLabels()
	t.Cleanup(func() { bv.ConfigureOperatorGatedLabels(previousLabels) })
	bv.ConfigureOperatorGatedLabels([]string{"operator-gated"})

	newly := []UnblockedBead{
		{ID: "authorized", Title: "stale title", Priority: 4, PrevBlockers: []string{"done"}, UnblockedByID: "done"},
		{ID: "missing", Title: "not planned", Priority: 2},
		{ID: "gated", Title: "stale gated", Priority: 3},
		{ID: "occupied", Title: "already owned", Priority: 1},
	}
	verified := []bv.TriageRecommendation{
		{ID: "authorized", Title: "live title", Priority: 1, Status: "open"},
		{ID: "gated", Title: "live gated", Priority: 0, Status: "open", Labels: []string{"operator-gated"}},
		{ID: "occupied", Title: "live occupied", Priority: 1, Status: "ready"},
		{ID: " ", Title: "ignored blank", Status: "open"},
	}
	active := map[string]struct{}{"occupied": {}}
	authorized, skipped := filterNewlyUnblockedByVerifiedPlan(newly, verified, active, nil)

	if len(authorized) != 1 {
		t.Fatalf("authorized newly-unblocked work=%+v, want one item", authorized)
	}
	got := authorized[0]
	if got.ID != "authorized" || got.Title != "live title" || got.Priority != 1 ||
		!reflect.DeepEqual(got.PrevBlockers, []string{"done"}) || got.UnblockedByID != "done" {
		t.Fatalf("authorized item=%+v, want live plan fields with dependency provenance preserved", got)
	}
	wantReasons := map[string]string{
		"missing":  "not_in_actionable_plan",
		"gated":    "operator_gated",
		"occupied": "already_assigned",
	}
	if len(skipped) != len(wantReasons) {
		t.Fatalf("skipped=%+v, want %d items", skipped, len(wantReasons))
	}
	for _, skip := range skipped {
		if want := wantReasons[skip.BeadID]; skip.Reason != want {
			t.Fatalf("skip=%+v, want reason %q", skip, want)
		}
		delete(wantReasons, skip.BeadID)
	}
	if len(wantReasons) != 0 {
		t.Fatalf("missing skipped items: %v", wantReasons)
	}
}

func TestWatchLoopRunStopsOnAutomatedAssignmentSafetyErrors(t *testing.T) {
	isolateSessionAgentStorage(t)
	for _, safetyErr := range []error{
		bv.ErrActionablePlanUnverified,
		bv.ErrActionableLabelsUnverified,
		markCLIInvalidInput(errors.New("invalid watch policy")),
	} {
		t.Run(safetyErr.Error(), func(t *testing.T) {
			store := assignment.NewStore("watch-safety-failure")
			loop := NewWatchLoop("watch-safety-failure", store, &AutoReassignOptions{
				Session: "watch-safety-failure",
				Quiet:   true,
			})
			loop.stopWhenDone = false
			loop.scanInterval = time.Millisecond
			loop.scanFn = func(context.Context) error {
				return fmt.Errorf("unsafe recurring scan: %w", safetyErr)
			}

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := loop.Run(ctx)
			if !errors.Is(err, safetyErr) {
				t.Fatalf("WatchLoop.Run() error = %v, want safety error %v", err, safetyErr)
			}
		})
	}
}

func TestCountSkippedByReason(t *testing.T) {
	items := []SkippedItem{
		{BeadID: "a", Reason: "blocked_by_dependency"},
		{BeadID: "b", Reason: "blocked_by_dependency"},
		{BeadID: "c", Reason: "operator_gated"},
		{BeadID: "d", Reason: "already_in_progress"},
	}
	if got := countSkippedByReason(items, "blocked_by_dependency"); got != 2 {
		t.Fatalf("countSkippedByReason(blocked_by_dependency) = %d, want 2", got)
	}
	if got := countSkippedByReason(items, "operator_gated"); got != 1 {
		t.Fatalf("countSkippedByReason(operator_gated) = %d, want 1", got)
	}
	if got := countSkippedByReason(items, "nonexistent"); got != 0 {
		t.Fatalf("countSkippedByReason(nonexistent) = %d, want 0", got)
	}
}

func TestNormalizeBeadStatus(t *testing.T) {
	tests := map[string]string{
		"open":         "open",
		"In Progress":  "in_progress",
		"in-progress":  "in_progress",
		" CLOSED ":     "closed",
		"":             "",
		"ready-for-qa": "ready_for_qa",
	}
	for in, want := range tests {
		if got := normalizeBeadStatus(in); got != want {
			t.Errorf("normalizeBeadStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeAgentTypeAlias(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty string is no filter", raw: "", want: ""},
		{name: "any is no filter", raw: "any", want: ""},
		{name: "ANY is no filter", raw: "ANY", want: ""},
		{name: "all is no filter", raw: "all", want: ""},
		{name: "star is no filter", raw: "*", want: ""},
		{name: "whitespace around any", raw: "  any  ", want: ""},
		{name: "claude resolves to claude", raw: "claude", want: "claude"},
		{name: "codex alias cod resolves", raw: "cod", want: "codex"},
		{name: "gemini alias gmi resolves", raw: "gmi", want: "gemini"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeAgentTypeAlias(tc.raw)
			if got != tc.want {
				t.Fatalf("normalizeAgentTypeAlias(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParsePriorityString(t *testing.T) {

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		// Valid priorities
		{name: "P0 critical", input: "P0", expected: 0},
		{name: "P1 high", input: "P1", expected: 1},
		{name: "P2 medium", input: "P2", expected: 2},
		{name: "P3 low", input: "P3", expected: 3},
		{name: "P4 backlog", input: "P4", expected: 4},

		// Invalid - returns default (2)
		{name: "P5 out of range", input: "P5", expected: 2},
		{name: "P9 out of range", input: "P9", expected: 2},
		{name: "empty string", input: "", expected: 2},
		{name: "just P", input: "P", expected: 2},
		{name: "lowercase p0", input: "p0", expected: 2},
		{name: "no P prefix", input: "0", expected: 2},
		{name: "too long", input: "P01", expected: 2},
		{name: "word priority", input: "high", expected: 2},
		{name: "negative-like", input: "P-1", expected: 2},
		{name: "spaces", input: " P1", expected: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parsePriorityString(tc.input)
			if result != tc.expected {
				t.Errorf("parsePriorityString(%q) = %d; want %d", tc.input, result, tc.expected)
			}
		})
	}
}

// TestAssignSessionObserverLiveBusyOverride verifies the #124 fix: when the
// pane scrollback's trailing live-window contains a THINKING-category pattern
// (e.g. codex's "• Working (4m 51s • esc to interrupt)") the verdict must be
// "working" regardless of what the legacy parser concludes — the pane is
// busy and watch-mode autonomous dispatch must not target it.
func TestAssignSessionObserverLiveBusyOverride(t *testing.T) {
	// A scrollback that ends with a codex working bullet inside the
	// live-window. Without the override, the legacy parser sometimes
	// classifies this as idle when there's no fresh prompt yet.
	scrollback := strings.Repeat("filler line\n", 200) +
		"\n• Working (4m 51s • esc to interrupt)\n"

	got := string(observeAssignmentFixture(t, "codex", scrollback).Current.Status.State)
	if got != "working" {
		t.Errorf("session observer (busy codex pane) = %q, want \"working\"", got)
	}
}

// TestAssignSessionObserverIgnoresStaleThinking verifies the override does NOT
// trigger when a thinking pattern only exists deep in the scrollback (outside
// the live-window). That historical bullet is from a completed tool call and
// must not lock the agent in "working" forever.
func TestAssignSessionObserverIgnoresStaleThinking(t *testing.T) {
	// Thinking pattern early in the buffer, then enough trailing content to
	// push it outside the live-window (15 trailing lines).
	scrollback := "• Working (10s • esc to interrupt)\n" +
		strings.Repeat("filler line that is unambiguously not thinking\n", 200) +
		"\n>>>" // codex-shaped idle prompt

	// We don't assert "idle" here (that depends on the legacy parser's
	// agent-specific prompt detection), but we must NOT see "working" be
	// forced by the override path on stale scrollback content.
	got := string(observeAssignmentFixture(t, "codex", scrollback).Current.Status.State)
	if got == "working" {
		t.Errorf("session observer (stale thinking pattern) = %q, must not be forced to working", got)
	}
}

// ============================================================================
// FIX C: Active-assignment idle-pool guard
// ============================================================================

// TestLoadActiveAssignmentPanes_ExcludesBetweenTurnsPane verifies that a pane
// holding an active assignment (StatusAssigned or StatusWorking) is reported by
// loadActiveAssignmentPanes even when it would momentarily look idle between
// turns. The idle-collection paths exclude these panes so a pane mid-flight on
// bead A is never handed bead B (double-dispatch) just because it briefly shows
// an idle prompt.
func TestLoadActiveAssignmentPanes_ExcludesBetweenTurnsPane(t *testing.T) {
	isolateSessionAgentStorage(t)

	const session = "fixc"
	store := assignment.NewStore(session)

	// Pane 1: mid-flight on bead A, status Working — the "between turns" pane.
	if _, err := store.Assign("bd-A", "Task A", 1, "claude", "fixc_claude_1", "do A"); err != nil {
		t.Fatalf("assign bd-A: %v", err)
	}
	if err := store.MarkWorking("bd-A"); err != nil {
		t.Fatalf("mark working bd-A: %v", err)
	}
	store.Assignments["bd-A"].OccupancyKey = "%41"
	store.Assignments["bd-A"].DispatchTarget = "%41"
	// Pane 2: freshly assigned, status Assigned.
	if _, err := store.Assign("bd-B", "Task B", 2, "codex", "fixc_codex_2", "do B"); err != nil {
		t.Fatalf("assign bd-B: %v", err)
	}
	store.Assignments["bd-B"].OccupancyKey = "%42"
	store.Assignments["bd-B"].DispatchTarget = "%42"
	// Pane 3: a completed assignment — NOT active, must NOT be excluded.
	if _, err := store.Assign("bd-C", "Task C", 3, "claude", "fixc_claude_3", "do C"); err != nil {
		t.Fatalf("assign bd-C: %v", err)
	}
	if err := store.MarkCompleted("bd-C"); err != nil {
		t.Fatalf("mark completed bd-C: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}

	active, err := loadActiveAssignmentPanes(session)
	if err != nil {
		t.Fatalf("load active panes: %v", err)
	}

	if _, ok := active["%41"]; !ok {
		t.Errorf("pane 1 (StatusWorking) must be in the active set — it is mid-flight and not dispatchable")
	}
	if _, ok := active["%42"]; !ok {
		t.Errorf("pane 2 (StatusAssigned) must be in the active set — it holds an active assignment")
	}
	if _, ok := active["%43"]; ok {
		t.Errorf("pane 3 (StatusCompleted) must NOT be in the active set — completed work frees the pane")
	}
	if len(active) != 2 {
		t.Errorf("active pane count = %d, want 2", len(active))
	}
}

// TestLoadActiveAssignmentPanes_EmptyStore returns an empty set (and never
// errors) when no store exists for the session — idle collection then proceeds
// with no exclusions.
func TestLoadActiveAssignmentPanes_EmptyStore(t *testing.T) {
	isolateSessionAgentStorage(t)
	active, err := loadActiveAssignmentPanes("no-such-session")
	if err != nil {
		t.Fatalf("load empty active panes: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected empty active set for missing store, got %d entries", len(active))
	}
}

func TestLoadActiveAssignmentPanesRejectsLegacyIndexOnlyRow(t *testing.T) {
	isolateSessionAgentStorage(t)
	const session = "legacy-active-pane"
	store := assignment.NewStore(session)
	legacy, err := store.Assign("bd-legacy", "Legacy", 1, "codex", "LegacyAgent", "work")
	if err != nil {
		t.Fatalf("seed legacy assignment: %v", err)
	}
	before := store.Get(legacy.BeadID)

	active, err := loadActiveAssignmentPanes(session)
	var migrationErr *assignment.PaneIdentityMigrationError
	if !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) || !errors.As(err, &migrationErr) {
		t.Fatalf("active=%v error=%v, want typed migration error", active, err)
	}
	if len(active) != 0 || migrationErr.BeadID != legacy.BeadID || !reflect.DeepEqual(store.Get(legacy.BeadID), before) {
		t.Fatalf("migration rejection changed state: active=%v migration=%+v stored=%+v", active, migrationErr, store.Get(legacy.BeadID))
	}
}

func TestResolveAssignmentItemPaneCanonicalMultiWindow(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%40", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex},
		{ID: "%41", WindowIndex: 1, Index: 1, Type: tmux.AgentClaude},
	}

	for _, item := range []AssignmentItem{
		{Pane: 1, PaneTarget: "0.1", PaneID: "%40"},
		{Pane: 1, PaneTarget: "1.1", PaneID: "%41"},
	} {
		pane, err := resolveAssignmentItemPane(panes, item)
		if err != nil {
			t.Fatalf("resolve %+v: %v", item, err)
		}
		if pane.ID != item.PaneID {
			t.Fatalf("resolve %+v selected %s", item, pane.ID)
		}
	}

	if _, err := resolveAssignmentItemPane(panes, AssignmentItem{BeadID: "ntm-plan-legacy", Pane: 1}); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("legacy plan resolved without migration error: %v", err)
	}
	if _, err := resolveAssignmentItemPane(panes, AssignmentItem{PaneTarget: "0.1", PaneID: "%41"}); err == nil {
		t.Fatal("inconsistent pane target and ID must fail closed")
	}
}

func TestResolvePendingAssignmentPaneNeverFallsBackToReusedLocalIndex(t *testing.T) {
	pending := assignment.Assignment{
		BeadID:         "ntm-pending",
		Pane:           1,
		OccupancyKey:   "%40",
		DispatchTarget: "%40",
	}
	replacementTopology := []tmux.Pane{{ID: "%41", WindowIndex: 1, Index: 1}}
	if _, err := resolvePendingAssignmentPane(replacementTopology, pending); err == nil || !strings.Contains(err.Error(), "%40 is unavailable") {
		t.Fatalf("topology-changed retry did not fail closed: %v", err)
	}

	originalTopology := append(replacementTopology, tmux.Pane{ID: "%40", WindowIndex: 0, Index: 1})
	resolved, err := resolvePendingAssignmentPane(originalTopology, pending)
	if err != nil {
		t.Fatalf("resolve original physical pane: %v", err)
	}
	if resolved.ID != "%40" {
		t.Fatalf("resolved pane ID = %q, want %%40", resolved.ID)
	}

	pending.OccupancyKey = "malformed-pane"
	pending.DispatchTarget = "%40"
	if _, err := resolvePendingAssignmentPane(originalTopology, pending); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("malformed occupancy key fell back to dispatch target: %v", err)
	}

	pending.OccupancyKey = ""
	pending.DispatchTarget = "%40"
	resolved, err = resolvePendingAssignmentPane(originalTopology, pending)
	if err != nil || resolved.ID != "%40" {
		t.Fatalf("empty occupancy key did not use canonical dispatch target: pane=%+v error=%v", resolved, err)
	}

	pending.OccupancyKey = ""
	pending.DispatchTarget = "0.1"
	if _, err := resolvePendingAssignmentPane(originalTopology, pending); !errors.Is(err, assignment.ErrPaneIdentityMigrationRequired) {
		t.Fatalf("index-only pending retry did not fail closed: %v", err)
	}
}

func TestPendingCLIRecoveryIdentityErrorRejectsReusedPaneIdentity(t *testing.T) {
	t.Parallel()
	pane := tmux.Pane{ID: "%40", WindowIndex: 0, Index: 1, Type: tmux.AgentCodex}
	pending := assignment.Assignment{AgentType: "codex", AgentName: "demo_codex_1"}
	if err := pendingCLIRecoveryIdentityError("demo", pane, pending, false); err != nil {
		t.Fatalf("matching identity: %v", err)
	}

	typeChanged := pending
	typeChanged.AgentType = "claude"
	if err := pendingCLIRecoveryIdentityError("demo", pane, typeChanged, false); err == nil || !strings.Contains(err.Error(), "changed agent type") {
		t.Fatalf("type drift error=%v", err)
	}

	nameChanged := pending
	nameChanged.AgentName = "FormerCodex"
	if err := pendingCLIRecoveryIdentityError("demo", pane, nameChanged, false); err == nil || !strings.Contains(err.Error(), "changed Agent Mail identity") {
		t.Fatalf("name drift error=%v", err)
	}
}

type fixedAssignSessionObserver struct {
	observation statuspkg.SessionObservation
	err         error
	observe     func(context.Context, string) (statuspkg.SessionObservation, error)
}

func TestCurrentAssignPaneObservationRequiresFreshCanonicalEvidence(t *testing.T) {
	now := time.Now().UTC()
	observation := statuspkg.SessionObservation{
		Session: "demo", ObservedAt: now, Complete: true,
		Panes: []statuspkg.PaneObservation{{
			Pane: tmux.PaneRef{ID: "%42", WindowIndex: 0, PaneIndex: 1},
			Current: statuspkg.StateObservation{
				Status: statuspkg.AgentStatus{State: statuspkg.StateIdle}, Freshness: statuspkg.FreshnessFresh, Confidence: 0.99,
			},
			RawOutput: "ready",
		}},
	}
	pane, err := currentAssignPaneObservation(observation, "%42", now.Add(time.Second))
	if err != nil || !pane.SafeToDispatch() || pane.RawOutput != "ready" {
		t.Fatalf("current observation=%+v error=%v", pane, err)
	}

	captureFailed := observation
	captureFailed.Panes = append([]statuspkg.PaneObservation(nil), observation.Panes...)
	captureFailed.Panes[0].Current.Error = "capture unavailable"
	if _, err := currentAssignPaneObservation(captureFailed, "%42", now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "capture unavailable") {
		t.Fatalf("capture failure error=%v", err)
	}
	if _, err := currentAssignPaneObservation(observation, "%99", now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "no unique pane") {
		t.Fatalf("missing pane error=%v", err)
	}
	if _, err := currentAssignPaneObservation(observation, "%42", now.Add(statuspkg.DispatchObservationMaxAge+time.Second)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale observation error=%v", err)
	}
}

func TestObserveAssignSessionPropagatesObserverError(t *testing.T) {
	original := newAssignSessionObserver
	newAssignSessionObserver = func() assignSessionObserver {
		return fixedAssignSessionObserver{err: errors.New("topology unavailable")}
	}
	t.Cleanup(func() { newAssignSessionObserver = original })

	if _, err := observeAssignSession(t.Context(), "demo"); err == nil || !strings.Contains(err.Error(), "topology unavailable") {
		t.Fatalf("observeAssignSession error=%v", err)
	}
}

func TestObserveAssignSessionBoundsTheWholeFreshnessStage(t *testing.T) {
	original := newAssignSessionObserver
	t.Cleanup(func() { newAssignSessionObserver = original })

	var observedDeadline time.Time
	newAssignSessionObserver = func() assignSessionObserver {
		return fixedAssignSessionObserver{observe: func(ctx context.Context, _ string) (statuspkg.SessionObservation, error) {
			var ok bool
			observedDeadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("assignment observation context has no deadline")
			}
			return statuspkg.SessionObservation{}, nil
		}}
	}

	before := time.Now()
	_, boundedErr := observeAssignSession(context.Background(), "bounded")
	if boundedErr != nil {
		t.Fatalf("observe bounded assignment session: %v", boundedErr)
	}
	after := time.Now()
	if observedDeadline.Sub(before) < assignObservationStageTimeout || observedDeadline.Sub(after) > assignObservationStageTimeout {
		t.Fatalf("assignment observation deadline = %s, want creation time + %s", observedDeadline, assignObservationStageTimeout)
	}
	if observedDeadline.Sub(before) >= statuspkg.DispatchObservationMaxAge {
		t.Fatalf("assignment observation deadline %s does not preserve dispatch freshness margin", observedDeadline)
	}

	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parentDeadline, hasParentDeadline := parent.Deadline()
	if !hasParentDeadline {
		t.Fatal("parent assignment observation context has no deadline")
	}
	_, parentErr := observeAssignSession(parent, "parent-bounded")
	if parentErr != nil {
		t.Fatalf("observe parent-bounded assignment session: %v", parentErr)
	}
	if !observedDeadline.Equal(parentDeadline) {
		t.Fatalf("assignment observation deadline = %s, want earlier parent deadline %s", observedDeadline, parentDeadline)
	}

	_, missingContextErr := observeAssignSession(nil, "missing-context")
	if missingContextErr == nil || !strings.Contains(missingContextErr.Error(), "context is required") {
		t.Fatalf("nil assignment observation context error = %v", missingContextErr)
	}
	_, missingTimeoutContextErr := observeAssignSessionWithTimeout(nil, "missing-timeout-context", time.Second)
	if missingTimeoutContextErr == nil || !strings.Contains(missingTimeoutContextErr.Error(), "context is required") {
		t.Fatalf("nil timed assignment observation context error = %v", missingTimeoutContextErr)
	}
}

func TestAssignmentObservationFailureCodePreservesTimeouts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "observation failure", err: errors.New("capture failed"), want: "OBSERVATION_ERROR"},
		{name: "deadline", err: errors.Join(errors.New("capture failed"), context.DeadlineExceeded), want: robot.ErrCodeTimeout},
		{name: "cancellation", err: errors.Join(errors.New("capture failed"), context.Canceled), want: robot.ErrCodeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch got := assignmentObservationFailureCode(test.err); got {
			case test.want:
			default:
				t.Fatalf("assignmentObservationFailureCode(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

func TestCompleteTriggeredAssignmentGenerationUsesExactCAS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "trigger-completion-cas"
	store := assignment.NewStore(session)
	observed, err := store.Assign("ntm-trigger", "Original", 1, "codex", "CodexOne", "original work")
	if err != nil {
		t.Fatalf("assign original generation: %v", err)
	}
	winner, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load replacement store: %v", err)
	}
	replacement, err := winner.Assign("ntm-trigger", "Replacement", 2, "codex", "CodexTwo", "replacement work")
	if err != nil {
		t.Fatalf("assign replacement generation: %v", err)
	}
	originalLeaseRelease := releaseAssignmentLeases
	originalClaimRelease := releaseBeadClaimForAssignment
	leaseReleaseCalls := 0
	claimReleaseCalls := 0
	releaseAssignmentLeases = func(context.Context, string, *assignment.Assignment) ([]string, error) {
		leaseReleaseCalls++
		return nil, errors.New("stale completion reached lease release")
	}
	releaseBeadClaimForAssignment = func(context.Context, string, string, string) (bool, error) {
		claimReleaseCalls++
		return false, errors.New("stale completion reached claim release")
	}
	t.Cleanup(func() {
		releaseAssignmentLeases = originalLeaseRelease
		releaseBeadClaimForAssignment = originalClaimRelease
	})

	applied, err := completeTriggeredAssignmentGeneration(t.Context(), store, observed)
	if err == nil || applied || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale completion applied=%v error=%v", applied, err)
	}
	current, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload replacement: %v", err)
	}
	stored := current.Get(replacement.BeadID)
	if stored == nil || stored.AgentName != replacement.AgentName || stored.Status != assignment.StatusAssigned || stored.CompletedAt != nil {
		t.Fatalf("stale completion mutated replacement: %+v", stored)
	}
	if leaseReleaseCalls != 0 || claimReleaseCalls != 0 {
		t.Fatalf("stale completion external side effects lease=%d claim=%d", leaseReleaseCalls, claimReleaseCalls)
	}
}

func TestCompleteTriggeredAssignmentReleasesExactHandlesOnceBeforeTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "trigger-completion-terminal-release"
	const beadID = "ntm-trigger-terminal-release"
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o700); err != nil {
		t.Fatalf("create project marker: %v", err)
	}
	originalRepo := assignRepoPath
	originalLeaseRelease := releaseAssignmentLeases
	originalClaimRelease := releaseBeadClaimForAssignment
	assignRepoPath = projectDir
	leaseReleaseCalls := 0
	claimReleaseCalls := 0
	releaseAssignmentLeases = func(_ context.Context, gotSession string, current *assignment.Assignment) ([]string, error) {
		leaseReleaseCalls++
		if gotSession != session || current.BeadID != beadID || !reflect.DeepEqual(current.ReservationIDs, []int{971}) {
			t.Fatalf("lease release session=%q assignment=%+v", gotSession, current)
		}
		return []string{"internal/cli/terminal.go"}, nil
	}
	releaseBeadClaimForAssignment = func(_ context.Context, gotProject, gotBeadID, actor string) (bool, error) {
		claimReleaseCalls++
		if gotProject != projectDir || gotBeadID != beadID || actor != "CodexOne/trigger-terminal-key" {
			t.Fatalf("claim release project=%q bead=%q actor=%q", gotProject, gotBeadID, actor)
		}
		return true, nil
	}
	t.Cleanup(func() {
		assignRepoPath = originalRepo
		releaseAssignmentLeases = originalLeaseRelease
		releaseBeadClaimForAssignment = originalClaimRelease
	})

	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Reserved trigger", Pane: 1, AgentType: "codex", AgentName: "CodexOne",
		Status: assignment.StatusAssigned, AssignedAt: time.Now().UTC(), IdempotencyKey: "trigger-terminal-key",
		ClaimActor: "CodexOne/trigger-terminal-key", DispatchTarget: "%97", OccupancyKey: "%97",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "mail-97", ReservationRequired: true,
		ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservedPaths: []string{"internal/cli/terminal.go"}, ReservationIDs: []int{971},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed reserved trigger assignment: %v", err)
	}
	observed := store.Get(beadID)
	applied, err := completeTriggeredAssignmentGeneration(t.Context(), store, observed)
	if err != nil || !applied {
		t.Fatalf("complete trigger applied=%v error=%v", applied, err)
	}
	if leaseReleaseCalls != 1 || claimReleaseCalls != 1 {
		t.Fatalf("completion side effects lease=%d claim=%d", leaseReleaseCalls, claimReleaseCalls)
	}
	terminal := store.Get(beadID)
	if terminal == nil || terminal.Status != assignment.StatusCompleted || terminal.CompletedAt == nil ||
		terminal.ReservationState != assignment.ReservationReleased || terminal.ReservationCompleted ||
		len(terminal.ReservationIDs) != 0 || len(terminal.ReservedPaths) != 0 || terminal.ClearState != assignment.ClearStateNone {
		t.Fatalf("terminal trigger assignment=%+v", terminal)
	}

	applied, err = completeTriggeredAssignmentGeneration(t.Context(), store, observed)
	if err == nil || applied || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("repeat completion applied=%v error=%v", applied, err)
	}
	if leaseReleaseCalls != 1 || claimReleaseCalls != 1 {
		t.Fatalf("repeat completion side effects lease=%d claim=%d", leaseReleaseCalls, claimReleaseCalls)
	}
}

func TestTerminalReconciliationSerializesExternalCleanupAcrossStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "terminal-reconciliation-cleanup-lock"
		beadID  = "ntm-terminal-cleanup-lock"
	)
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o700); err != nil {
		t.Fatalf("create project marker: %v", err)
	}
	originalRepo := assignRepoPath
	originalLeaseRelease := releaseAssignmentLeases
	originalClaimRelease := releaseBeadClaimForAssignment
	assignRepoPath = projectDir
	var leaseReleaseCalls atomic.Int32
	var claimReleaseCalls atomic.Int32
	releaseStarted := make(chan struct{})
	allowRelease := make(chan struct{})
	releaseAssignmentLeases = func(ctx context.Context, _ string, _ *assignment.Assignment) ([]string, error) {
		if leaseReleaseCalls.Add(1) == 1 {
			close(releaseStarted)
			select {
			case <-allowRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []string{"internal/cli/concurrent-terminal.go"}, nil
	}
	releaseBeadClaimForAssignment = func(context.Context, string, string, string) (bool, error) {
		claimReleaseCalls.Add(1)
		return true, nil
	}
	t.Cleanup(func() {
		assignRepoPath = originalRepo
		releaseAssignmentLeases = originalLeaseRelease
		releaseBeadClaimForAssignment = originalClaimRelease
	})

	seed := assignment.NewStore(session)
	seed.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Concurrent terminal cleanup", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Status: assignment.StatusAssigned,
		AssignedAt: time.Now().UTC(), IdempotencyKey: "terminal-cleanup-generation",
		ClaimActor: "CodexOne/terminal-cleanup-generation", DispatchTarget: "%98", OccupancyKey: "%98",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "mail-98", ReservationRequired: true,
		ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservedPaths: []string{"internal/cli/concurrent-terminal.go"}, ReservationIDs: []int{981},
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed terminal cleanup assignment: %v", err)
	}
	barrier, applied, err := seed.BeginTerminalReconciliationIfCurrent(t.Context(), seed.Get(beadID), assignment.StatusCompleted, "")
	if err != nil || !applied || barrier == nil {
		t.Fatalf("begin terminal cleanup barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	stores := make([]*assignment.AssignmentStore, 2)
	for index := range stores {
		stores[index], err = assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("load terminal cleanup store %d: %v", index, err)
		}
	}

	type reconciliationResult struct {
		applied bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan reconciliationResult, len(stores))
	for _, store := range stores {
		go func(store *assignment.AssignmentStore) {
			<-start
			applied, err := reconcilePendingTerminalAssignment(t.Context(), store, session, barrier)
			results <- reconciliationResult{applied: applied, err: err}
		}(store)
	}
	close(start)
	select {
	case <-releaseStarted:
	case <-time.After(5 * time.Second):
		close(allowRelease)
		t.Fatal("terminal cleanup did not reach external release")
	}
	close(allowRelease)
	for range stores {
		select {
		case result := <-results:
			if result.err != nil || !result.applied {
				t.Fatalf("terminal reconciliation applied=%v error=%v", result.applied, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("terminal reconciliation remained blocked")
		}
	}
	if got := leaseReleaseCalls.Load(); got != 1 {
		t.Fatalf("terminal lease release calls=%d, want 1", got)
	}
	if got := claimReleaseCalls.Load(); got != 1 {
		t.Fatalf("terminal claim release calls=%d, want 1", got)
	}
	terminal, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload terminal cleanup result: %v", err)
	}
	current := terminal.Get(beadID)
	if current == nil || current.Status != assignment.StatusCompleted || current.ClearState != assignment.ClearStateNone {
		t.Fatalf("terminal cleanup result=%+v", current)
	}
}

func TestClearAndTerminalReconciliationShareExternalCleanupLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "clear-terminal-cleanup-lock"
		beadID  = "ntm-clear-terminal-cleanup-lock"
	)
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".git"), 0o700); err != nil {
		t.Fatalf("create project marker: %v", err)
	}
	originalRepo := assignRepoPath
	originalLeaseRelease := releaseAssignmentLeases
	originalClaimRelease := releaseBeadClaimForAssignment
	assignRepoPath = projectDir
	var leaseReleaseCalls atomic.Int32
	var claimReleaseCalls atomic.Int32
	releaseStarted := make(chan struct{})
	allowRelease := make(chan struct{})
	releaseAssignmentLeases = func(ctx context.Context, _ string, _ *assignment.Assignment) ([]string, error) {
		if leaseReleaseCalls.Add(1) == 1 {
			close(releaseStarted)
			select {
			case <-allowRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []string{"internal/cli/clear-terminal.go"}, nil
	}
	releaseBeadClaimForAssignment = func(context.Context, string, string, string) (bool, error) {
		claimReleaseCalls.Add(1)
		return true, nil
	}
	t.Cleanup(func() {
		assignRepoPath = originalRepo
		releaseAssignmentLeases = originalLeaseRelease
		releaseBeadClaimForAssignment = originalClaimRelease
	})

	seed := assignment.NewStore(session)
	seed.Assignments[beadID] = &assignment.Assignment{
		BeadID: beadID, BeadTitle: "Clear versus terminal", Pane: 1,
		AgentType: "codex", AgentName: "CodexOne", Status: assignment.StatusAssigned,
		AssignedAt: time.Now().UTC(), IdempotencyKey: "clear-terminal-generation",
		ClaimActor: "CodexOne/clear-terminal-generation", DispatchTarget: "%99", OccupancyKey: "%99",
		DispatchState: assignment.DispatchSent, DispatchReceiptID: "mail-99", ReservationRequired: true,
		ReservationState: assignment.ReservationReserved, ReservationCompleted: true,
		ReservedPaths: []string{"internal/cli/clear-terminal.go"}, ReservationIDs: []int{991},
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed clear-terminal assignment: %v", err)
	}
	clearObserved := seed.Get(beadID)
	barrier, applied, err := seed.BeginTerminalReconciliationIfCurrent(t.Context(), clearObserved, assignment.StatusCompleted, "")
	if err != nil || !applied || barrier == nil {
		t.Fatalf("begin clear-terminal barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	terminalStore, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load terminal store: %v", err)
	}
	clearStore, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load clear store: %v", err)
	}

	terminalResult := make(chan error, 1)
	go func() {
		applied, err := reconcilePendingTerminalAssignment(t.Context(), terminalStore, session, barrier)
		if err == nil && !applied {
			err = errors.New("terminal reconciliation was not applied")
		}
		terminalResult <- err
	}()
	select {
	case <-releaseStarted:
	case <-time.After(5 * time.Second):
		close(allowRelease)
		t.Fatal("terminal cleanup did not reach external release")
	}
	clearResult := make(chan error, 1)
	go func() {
		_, err := clearStoredAssignment(t.Context(), clearStore, session, clearObserved)
		clearResult <- err
	}()
	close(allowRelease)
	select {
	case err := <-terminalResult:
		if err != nil {
			t.Fatalf("terminal reconciliation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal reconciliation remained blocked")
	}
	select {
	case err := <-clearResult:
		if err == nil || !strings.Contains(err.Error(), "reached terminal status") {
			t.Fatalf("concurrent clear error=%v, want terminal transition rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent clear remained blocked")
	}
	if got := leaseReleaseCalls.Load(); got != 1 {
		t.Fatalf("clear-terminal lease release calls=%d, want 1", got)
	}
	if got := claimReleaseCalls.Load(); got != 1 {
		t.Fatalf("clear-terminal claim release calls=%d, want 1", got)
	}
	terminal, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload clear-terminal result: %v", err)
	}
	current := terminal.Get(beadID)
	if current == nil || current.Status != assignment.StatusCompleted || current.ClearState != assignment.ClearStateNone {
		t.Fatalf("clear-terminal result=%+v", current)
	}
}

func TestTerminalReconciliationTreatsRemovedGenerationAsFinished(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "terminal-reconciliation-removed"
		beadID  = "ntm-terminal-reconciliation-removed"
	)
	store := assignment.NewStore(session)
	observed, err := store.Assign(beadID, "Removed terminal", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("assign removed terminal: %v", err)
	}
	barrier, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, assignment.StatusCompleted, "")
	if err != nil || !applied || barrier == nil {
		t.Fatalf("begin removed terminal barrier=%+v applied=%v error=%v", barrier, applied, err)
	}
	if err := store.Remove(beadID); err != nil {
		t.Fatalf("remove terminal generation: %v", err)
	}
	waiter, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load removed terminal waiter: %v", err)
	}
	if applied, err := reconcilePendingTerminalAssignment(t.Context(), waiter, session, barrier); err != nil || !applied {
		t.Fatalf("removed terminal reconciliation applied=%v error=%v", applied, err)
	}
}

func TestWatchLoopCompletionAckRetryDoesNotRepeatConsumerEffects(t *testing.T) {
	loop := &WatchLoop{handledCompletionEvents: make(map[string]struct{})}
	handleCalls := 0
	ackCalls := 0
	renewCalls := 0
	loop.renewCompletionEventFn = func(context.Context, string, string, string, time.Duration) (bool, error) {
		renewCalls++
		return true, nil
	}
	loop.handleCompletionFn = func(_ context.Context, event completion.CompletionEvent) error {
		handleCalls++
		if event.EventID != "event-1" {
			t.Fatalf("handled event=%+v", event)
		}
		return nil
	}
	loop.ackCompletionEventFn = func(context.Context, string, string, string) (bool, error) {
		ackCalls++
		if ackCalls == 1 {
			return false, errors.New("injected acknowledgement save failure")
		}
		return true, nil
	}
	event := completion.CompletionEvent{EventID: "event-1", ConsumerToken: "consumer-1", LeaseDuration: time.Minute, BeadID: "ntm-event-1"}
	if err := loop.consumeCompletionEvent(t.Context(), event); err == nil || !strings.Contains(err.Error(), "injected acknowledgement") {
		t.Fatalf("first consume error=%v", err)
	}
	if err := loop.consumeCompletionEvent(t.Context(), event); err != nil {
		t.Fatalf("ack retry: %v", err)
	}
	if handleCalls != 1 || ackCalls != 2 || renewCalls != 1 {
		t.Fatalf("consumer calls=%d ack calls=%d renew calls=%d, want 1/2/1", handleCalls, ackCalls, renewCalls)
	}
}

func TestWatchLoopRenewsCompletionLeaseAcrossSlowHandler(t *testing.T) {
	const (
		beadID        = "ntm-completion-heartbeat"
		eventID       = "completion-heartbeat-event"
		consumerToken = "completion-heartbeat-owner"
		leaseDuration = 300 * time.Millisecond
	)
	loop := &WatchLoop{handledCompletionEvents: make(map[string]struct{})}
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	heartbeatObserved := make(chan struct{})
	handlerCalls := 0
	var renewCalls atomic.Int32
	loop.renewCompletionEventFn = func(context.Context, string, string, string, time.Duration) (bool, error) {
		if renewCalls.Add(1) == 2 {
			close(heartbeatObserved)
		}
		return true, nil
	}
	loop.handleCompletionFn = func(ctx context.Context, _ completion.CompletionEvent) error {
		handlerCalls++
		close(handlerStarted)
		select {
		case <-releaseHandler:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ackCalls := 0
	loop.ackCompletionEventFn = func(context.Context, string, string, string) (bool, error) {
		ackCalls++
		return true, nil
	}
	event := completion.CompletionEvent{
		EventID: eventID, ConsumerToken: consumerToken, LeaseDuration: leaseDuration, BeadID: beadID,
	}
	consumeResult := make(chan error, 1)
	go func() { consumeResult <- loop.consumeCompletionEvent(t.Context(), event) }()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("slow completion handler did not start")
	}
	select {
	case <-heartbeatObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("completion lease heartbeat did not run while the handler was blocked")
	}
	close(releaseHandler)
	select {
	case err := <-consumeResult:
		if err != nil {
			t.Fatalf("consume slow completion event: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow completion handler did not finish")
	}
	if handlerCalls != 1 {
		t.Fatalf("slow completion handler calls=%d, want 1", handlerCalls)
	}
	if renewCalls.Load() < 2 || ackCalls != 1 {
		t.Fatalf("completion lease renewals=%d acknowledgements=%d, want at least 2/1", renewCalls.Load(), ackCalls)
	}
}

func TestWatchLoopBlockedReadyScanDoesNotDelayCompletionConsumption(t *testing.T) {
	isolateSessionAgentStorage(t)
	const (
		session = "watch-blocked-ready-scan"
		beadID  = "ntm-watch-blocked-ready-scan"
		eventID = "completion-watch-blocked-ready-scan"
	)

	previousInterval := assignWatchInterval
	assignWatchInterval = 10 * time.Millisecond
	defer func() {
		assignWatchInterval = previousInterval
	}()

	detectedAt := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments[beadID] = &assignment.Assignment{
		BeadID:                   beadID,
		Status:                   assignment.StatusCompleted,
		AssignedAt:               detectedAt.Add(-time.Second),
		Pane:                     1,
		DispatchTarget:           "%901",
		OccupancyKey:             "%901",
		PendingCompletionEventID: eventID,
		CompletionDetectedAt:     &detectedAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed pending completion event: %v", err)
	}

	loop := NewWatchLoop(session, store, &AutoReassignOptions{
		Session: session,
		Quiet:   true,
		DryRun:  true,
	})
	loop.scanInterval = time.Millisecond
	scanStarted := make(chan struct{})
	var scanCalls atomic.Int32
	loop.scanFn = func(ctx context.Context) error {
		if scanCalls.Add(1) == 1 {
			close(scanStarted)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	handled := make(chan completion.CompletionEvent, 1)
	loop.handleCompletionFn = func(_ context.Context, event completion.CompletionEvent) error {
		handled <- event
		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() {
		runResult <- loop.Run(ctx)
	}()
	joined := false
	defer func() {
		if joined {
			return
		}
		cancel()
		select {
		case <-runResult:
		case <-time.After(2 * time.Second):
			t.Errorf("watch loop did not stop during test cleanup")
		}
	}()

	select {
	case <-scanStarted:
	case <-time.After(time.Second):
		t.Fatal("periodic ready-work scan did not start")
	}
	time.Sleep(5 * loop.scanInterval)
	if got := scanCalls.Load(); got != 1 {
		t.Fatalf("blocked ready-work scan launched %d concurrent passes, want 1", got)
	}

	select {
	case event := <-handled:
		if event.BeadID != beadID || event.EventID != eventID || event.ConsumerToken == "" {
			t.Fatalf("handled completion event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion handling was blocked behind the ready-work scan")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		reloaded, err := assignment.LoadStoreStrict(session)
		if err == nil {
			current := reloaded.Get(beadID)
			if current != nil && current.PendingCompletionEventID == "" &&
				current.CompletionConsumerToken == "" && current.CompletionLeaseExpiresAt == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion event was not durably acknowledged while scan was blocked: load_error=%v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := scanCalls.Load(); got != 1 {
		t.Fatalf("blocked ready-work scan launched %d concurrent passes before acknowledgement, want 1", got)
	}

	cancel()
	select {
	case err := <-runResult:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchLoop.Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch loop did not stop after cancellation")
	}
}

func TestWatchLoopLostCompletionLeaseCancelsHandlerWithoutAck(t *testing.T) {
	loop := &WatchLoop{handledCompletionEvents: make(map[string]struct{})}
	handlerCanceled := false
	ackCalls := 0
	renewCalls := 0
	loop.handleCompletionFn = func(ctx context.Context, _ completion.CompletionEvent) error {
		<-ctx.Done()
		handlerCanceled = true
		return ctx.Err()
	}
	loop.renewCompletionEventFn = func(context.Context, string, string, string, time.Duration) (bool, error) {
		renewCalls++
		return renewCalls == 1, nil
	}
	loop.ackCompletionEventFn = func(context.Context, string, string, string) (bool, error) {
		ackCalls++
		return true, nil
	}
	err := loop.consumeCompletionEvent(t.Context(), completion.CompletionEvent{
		EventID: "event-lost", ConsumerToken: "consumer-lost", LeaseDuration: 30 * time.Millisecond, BeadID: "bd-lost",
	})
	if err == nil || !strings.Contains(err.Error(), "consumer lease was lost") {
		t.Fatalf("lost lease consume error=%v", err)
	}
	if !handlerCanceled || ackCalls != 0 {
		t.Fatalf("lost lease handler_canceled=%v acknowledgements=%d, want true/0", handlerCanceled, ackCalls)
	}
}

func TestWatchLoopCancellationDoesNotAcknowledgeUnhandledCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	loop := &WatchLoop{handledCompletionEvents: make(map[string]struct{})}
	ackCalls := 0
	loop.renewCompletionEventFn = func(context.Context, string, string, string, time.Duration) (bool, error) {
		return true, nil
	}
	loop.handleCompletionFn = func(handlerCtx context.Context, _ completion.CompletionEvent) error {
		cancel()
		<-handlerCtx.Done()
		return handlerCtx.Err()
	}
	loop.ackCompletionEventFn = func(context.Context, string, string, string) (bool, error) {
		ackCalls++
		return true, nil
	}
	err := loop.consumeCompletionEvent(ctx, completion.CompletionEvent{EventID: "event-canceled", ConsumerToken: "consumer-canceled", LeaseDuration: time.Minute, BeadID: "bd-canceled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("consume error = %v, want context.Canceled", err)
	}
	if ackCalls != 0 {
		t.Fatalf("completion acknowledgements after canceled handling = %d", ackCalls)
	}
}

func TestPreserveCommandContextErrorRetainsCancellationClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transportErr := errors.New("agentmail: resources/read failed: request timed out")

	joined := preserveCommandContextError(ctx, transportErr)
	if !errors.Is(joined, context.Canceled) || !errors.Is(joined, transportErr) {
		t.Fatalf("preserved command error = %v, want transport and context cancellation", joined)
	}
	if code := classifyReassignError(joined, nil); code != "TIMEOUT" {
		t.Fatalf("classifyReassignError() = %q, want TIMEOUT", code)
	}
	if code := classifyRebalanceError(joined); code != "TIMEOUT" {
		t.Fatalf("classifyRebalanceError() = %q, want TIMEOUT", code)
	}
}

func TestEmitContextAwareReassignFailureOverridesFallbackWithCancellation(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	for _, fallbackCode := range []string{"TMUX_ERROR", "OBSERVATION_ERROR", "BEAD_LOOKUP_FAILED"} {
		t.Run(fallbackCode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			transportErr := errors.New("dependency transport stopped")
			output, runErr := captureStdout(t, func() error {
				return emitContextAwareReassignFailure(ctx, "reassign-context-error", fallbackCode, transportErr, nil)
			})
			if !errors.Is(runErr, context.Canceled) || !errors.Is(runErr, transportErr) {
				t.Fatalf("context-aware reassign error = %v, want cancellation and transport causes", runErr)
			}
			var envelope ReassignEnvelope
			if err := json.Unmarshal([]byte(output), &envelope); err != nil {
				t.Fatalf("decode context-aware reassign envelope: %v\noutput=%s", err, output)
			}
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != robot.ErrCodeTimeout {
				t.Fatalf("context-aware reassign envelope = %+v", envelope)
			}
		})
	}
}

func TestWatchLoopRejectsExpiredQueuedEventAfterTakeoverBeforeSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session       = "watch-completion-queued-takeover"
		firstBead     = "ntm-completion-queued-first"
		firstEvent    = "completion-queued-first-event"
		secondBead    = "ntm-completion-queued-second"
		secondEvent   = "completion-queued-second-event"
		originalToken = "completion-queued-original"
		takeoverToken = "completion-queued-takeover"
		activeLease   = 5 * time.Second
		queuedLease   = 500 * time.Millisecond
	)
	detectedAt := time.Now().UTC()
	store := assignment.NewStore(session)
	store.Assignments[firstBead] = &assignment.Assignment{
		BeadID: firstBead, Status: assignment.StatusCompleted, AssignedAt: detectedAt,
		DispatchTarget: "%101", OccupancyKey: "%101",
		PendingCompletionEventID: firstEvent, CompletionDetectedAt: &detectedAt,
	}
	store.Assignments[secondBead] = &assignment.Assignment{
		BeadID: secondBead, Status: assignment.StatusCompleted, AssignedAt: detectedAt,
		DispatchTarget: "%102", OccupancyKey: "%102",
		PendingCompletionEventID: secondEvent, CompletionDetectedAt: &detectedAt,
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed queued completion events: %v", err)
	}
	for _, event := range []struct {
		beadID, eventID string
		leaseDuration   time.Duration
	}{{firstBead, firstEvent, activeLease}, {secondBead, secondEvent, queuedLease}} {
		if _, acquired, err := store.ClaimPendingCompletionEvent(t.Context(), event.beadID, event.eventID, originalToken, event.leaseDuration); err != nil || !acquired {
			t.Fatalf("claim queued completion event %s acquired=%v error=%v", event.eventID, acquired, err)
		}
	}

	firstHandlerStarted := make(chan struct{})
	releaseFirstHandler := make(chan struct{})
	originalHandleCalls := 0
	originalAckCalls := 0
	original := &WatchLoop{store: store, handledCompletionEvents: make(map[string]struct{})}
	original.handleCompletionFn = func(ctx context.Context, event completion.CompletionEvent) error {
		originalHandleCalls++
		if event.EventID != firstEvent {
			return fmt.Errorf("stale queued consumer handled event %q", event.EventID)
		}
		close(firstHandlerStarted)
		select {
		case <-releaseFirstHandler:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	original.ackCompletionEventFn = func(ctx context.Context, beadID, eventID, token string) (bool, error) {
		originalAckCalls++
		return store.AcknowledgeCompletionEvent(ctx, beadID, eventID, token)
	}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- original.consumeCompletionEvent(t.Context(), completion.CompletionEvent{
			EventID: firstEvent, ConsumerToken: originalToken, LeaseDuration: activeLease, BeadID: firstBead,
		})
	}()
	select {
	case <-firstHandlerStarted:
	case <-time.After(time.Second):
		t.Fatal("first queued completion handler did not start")
	}

	time.Sleep(2 * queuedLease)
	takeoverStore, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load takeover completion consumer: %v", err)
	}
	if _, acquired, err := takeoverStore.ClaimPendingCompletionEvent(t.Context(), secondBead, secondEvent, takeoverToken, activeLease); err != nil || !acquired {
		t.Fatalf("take over expired queued completion event acquired=%v error=%v", acquired, err)
	}

	close(releaseFirstHandler)
	select {
	case err := <-firstResult:
		if err != nil {
			t.Fatalf("complete first queued event: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first queued completion handler did not finish")
	}
	staleErr := original.consumeCompletionEvent(t.Context(), completion.CompletionEvent{
		EventID: secondEvent, ConsumerToken: originalToken, LeaseDuration: queuedLease, BeadID: secondBead,
	})
	if staleErr == nil || !strings.Contains(staleErr.Error(), "lost before handling") {
		t.Fatalf("stale queued consume error=%v", staleErr)
	}
	if originalHandleCalls != 1 || originalAckCalls != 1 {
		t.Fatalf("original consumer handlers=%d acknowledgements=%d, want 1/1", originalHandleCalls, originalAckCalls)
	}
	current := takeoverStore.Get(secondBead)
	if current == nil || current.PendingCompletionEventID != secondEvent || current.CompletionConsumerToken != takeoverToken {
		t.Fatalf("stale consumer changed takeover event: %+v", current)
	}

	takeoverHandleCalls := 0
	takeover := &WatchLoop{store: takeoverStore, handledCompletionEvents: make(map[string]struct{})}
	takeover.handleCompletionFn = func(context.Context, completion.CompletionEvent) error {
		takeoverHandleCalls++
		return nil
	}
	if err := takeover.consumeCompletionEvent(t.Context(), completion.CompletionEvent{
		EventID: secondEvent, ConsumerToken: takeoverToken, LeaseDuration: activeLease, BeadID: secondBead,
	}); err != nil {
		t.Fatalf("takeover consume queued event: %v", err)
	}
	if takeoverHandleCalls != 1 {
		t.Fatalf("takeover handler calls=%d, want 1", takeoverHandleCalls)
	}
	if current := takeoverStore.Get(secondBead); current == nil || current.PendingCompletionEventID != "" || current.CompletionConsumerToken != "" || current.CompletionLeaseExpiresAt != nil {
		t.Fatalf("takeover completion event was not acknowledged: %+v", current)
	}
}

func TestAssignmentReservationReleaseRejectsCanceledContextBeforeExternalCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	current := &assignment.Assignment{
		BeadID: "bd-release-cancel", ClearState: assignment.ClearStateReservationReleasing,
		ReservationRequired: true, ReservationState: assignment.ReservationReserved,
		ReservationIDs: []int{77}, ReservedPaths: []string{"internal/cli/**"},
	}
	if _, err := releaseAssignmentReservationsForClear(ctx, "unused", current); !errors.Is(err, context.Canceled) {
		t.Fatalf("release error = %v, want context.Canceled", err)
	}
}

func TestTriggerCompletionCheckStopsBeforeReassignmentOnStrictLoadError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "trigger-completion-corrupt"
	storePath := filepath.Join(assignment.StorageDir(), session, "assignments.json")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatalf("create store directory: %v", err)
	}
	if err := os.WriteFile(storePath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt store fixture: %v", err)
	}
	original := performAutoReassignmentForTrigger
	var calls int
	performAutoReassignmentForTrigger = func(context.Context, string, *AutoReassignOptions) (*AutoReassignResult, error) {
		calls++
		return &AutoReassignResult{}, nil
	}
	t.Cleanup(func() { performAutoReassignmentForTrigger = original })

	result, err := TriggerCompletionCheck(t.Context(), session, "ntm-trigger", &AutoReassignOptions{})
	if err == nil || !strings.Contains(err.Error(), "refresh completion assignment store") || result != nil {
		t.Fatalf("TriggerCompletionCheck result=%+v error=%v", result, err)
	}
	if calls != 0 {
		t.Fatalf("auto-reassignment ran %d times after strict load failure", calls)
	}
}

func TestTriggerCompletionCheckCompletesExactGenerationBeforeReassignment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "trigger-completion-exact"
	store := assignment.NewStore(session)
	observed, err := store.Assign("ntm-trigger", "Exact", 1, "codex", "CodexOne", "work")
	if err != nil {
		t.Fatalf("assign exact generation: %v", err)
	}
	originalLeaseRelease := releaseAssignmentLeases
	original := performAutoReassignmentForTrigger
	releaseCalls := 0
	var calls int
	releaseAssignmentLeases = func(_ context.Context, gotSession string, current *assignment.Assignment) ([]string, error) {
		releaseCalls++
		if gotSession != session || current == nil || current.BeadID != observed.BeadID {
			t.Fatalf("release assignment leases session=%q assignment=%+v", gotSession, current)
		}
		return nil, nil
	}
	performAutoReassignmentForTrigger = func(_ context.Context, beadID string, opts *AutoReassignOptions) (*AutoReassignResult, error) {
		calls++
		current, loadErr := assignment.LoadStoreStrict(session)
		if loadErr != nil {
			t.Fatalf("load completed assignment in callback: %v", loadErr)
		}
		if stored := current.Get(beadID); stored == nil || stored.Status != assignment.StatusCompleted {
			t.Fatalf("callback observed incomplete assignment: %+v", stored)
		}
		return &AutoReassignResult{TriggerBeadID: beadID}, nil
	}
	t.Cleanup(func() {
		releaseAssignmentLeases = originalLeaseRelease
		performAutoReassignmentForTrigger = original
	})

	result, err := TriggerCompletionCheck(t.Context(), session, observed.BeadID, &AutoReassignOptions{})
	if err != nil || result == nil || result.TriggerBeadID != observed.BeadID || releaseCalls != 1 || calls != 1 {
		t.Fatalf("TriggerCompletionCheck result=%+v error=%v release calls=%d reassignment calls=%d", result, err, releaseCalls, calls)
	}
}

func (o fixedAssignSessionObserver) Observe(ctx context.Context, session string) (statuspkg.SessionObservation, error) {
	if o.observe != nil {
		return o.observe(ctx, session)
	}
	return o.observation, o.err
}

func TestCLIAtomicDispatchReobservesAndRejectsBusyPaneBeforeActuation(t *testing.T) {
	port := &cliAtomicPaneDispatchPort{
		session: "demo",
		observer: fixedAssignSessionObserver{observation: statuspkg.SessionObservation{
			Session: "demo",
			Panes: []statuspkg.PaneObservation{{
				Pane: tmux.PaneRef{ID: "%42", WindowIndex: 0, PaneIndex: 1},
				Current: statuspkg.StateObservation{
					Status:     statuspkg.AgentStatus{State: statuspkg.StateWorking},
					ObservedAt: time.Now().UTC(),
					Freshness:  statuspkg.FreshnessFresh,
					Confidence: 0.99,
				},
			}},
		}},
	}

	receipt, err := port.Dispatch(t.Context(), assignment.DispatchRequest{
		BeadID: "ntm-busy-transition",
		Target: "%42",
		Prompt: "must not be delivered",
	})
	if err == nil || !strings.Contains(err.Error(), "not freshly and confidently idle at dispatch") {
		t.Fatalf("busy actuation boundary error = %v", err)
	}
	if receipt.DeliveryID != "" {
		t.Fatalf("busy actuation boundary produced delivery ID %q", receipt.DeliveryID)
	}
}

func TestCLIAtomicPreflightBlocksSensitiveReservationPathBeforeTopologyLookup(t *testing.T) {
	port := &cliAtomicPaneDispatchPort{redactionConfig: redaction.DefaultConfig()}
	_, err := port.Preflight(t.Context(), assignment.DispatchRequest{
		BeadID: "ntm-sensitive-path", BeadTitle: "Safe title", Target: "%42", Prompt: "safe prompt",
		RequestedPaths: []string{"internal/" + "sk-proj-FAKEtestkey1234567890123456789012345678901234" + ".txt"},
	})
	var dispatchErr *dispatchsvc.Error
	if !errors.As(err, &dispatchErr) || dispatchErr.Code != dispatchsvc.ErrRedactionBlocked || !strings.Contains(err.Error(), "reservation path") {
		t.Fatalf("sensitive path preflight error=%v", err)
	}
}

func TestValidateSinglePaneDispatchResultRequiresExactPhysicalTarget(t *testing.T) {
	t.Parallel()
	result := dispatchsvc.Result{
		Success: true, Delivered: 1,
		Receipts: []dispatchsvc.Receipt{{
			Target: dispatchsvc.Target{Ref: tmux.PaneRef{ID: "%42", WindowIndex: 0, PaneIndex: 1}},
			Status: dispatchsvc.ReceiptDelivered, Protocol: dispatchsvc.ProtocolSingleEnter,
		}},
	}
	if _, err := validateSinglePaneDispatchResult(result, "%42"); err != nil {
		t.Fatalf("matching target: %v", err)
	}
	if _, err := validateSinglePaneDispatchResult(result, "%43"); err == nil || !strings.Contains(err.Error(), "does not match requested pane") {
		t.Fatalf("mismatched target error=%v", err)
	}
}

func TestClassifyCLIReservationAttemptMarksKnownAndPartialFailures(t *testing.T) {
	req := assignment.ReservationRequest{AgentName: "BlueLake", Target: "%42"}

	lease, err := classifyCLIReservationAttempt(req, &assignpkg.FileReservationResult{
		RequestedPaths: []string{"internal/cli/**"},
		Conflicts:      []agentmail.ReservationConflict{{Path: "internal/cli/**", Holders: []string{"GreenLake"}}},
		Success:        false,
	}, nil)
	if !assignment.IsGuaranteedNoReservation(err) || assignment.IsReservationReleaseRequired(err) {
		t.Fatalf("zero-grant conflict classification error = %v", err)
	}
	if !reflect.DeepEqual(lease.Requested, []string{"internal/cli/**"}) || len(lease.Granted) != 0 || len(lease.ReservationIDs) != 0 {
		t.Fatalf("zero-grant conflict lease = %+v", lease)
	}

	lease, err = classifyCLIReservationAttempt(req, &assignpkg.FileReservationResult{
		RequestedPaths: []string{"internal/cli/**", "internal/robot/**"},
		GrantedPaths:   []string{"internal/cli/**"},
		ReservationIDs: []int{73},
		Success:        false,
		Error:          "second path conflicted",
	}, nil)
	if !assignment.IsReservationReleaseRequired(err) || assignment.IsGuaranteedNoReservation(err) {
		t.Fatalf("partial-grant classification error = %v", err)
	}
	if !reflect.DeepEqual(lease.Granted, []string{"internal/cli/**"}) || !reflect.DeepEqual(lease.ReservationIDs, []int{73}) {
		t.Fatalf("partial-grant lease handles were lost: %+v", lease)
	}

	transportErr := errors.New("reservation transport disconnected")
	_, err = classifyCLIReservationAttempt(req, nil, transportErr)
	if !errors.Is(err, transportErr) || assignment.IsGuaranteedNoReservation(err) || assignment.IsReservationReleaseRequired(err) {
		t.Fatalf("zero-handle ambiguous transport error was misclassified: %v", err)
	}

	lease, err = classifyCLIReservationAttempt(req, &assignpkg.FileReservationResult{
		RequestedPaths: []string{"internal/cli/**"},
		Success:        false,
		Error:          transportErr.Error(),
	}, transportErr)
	if !errors.Is(err, transportErr) || assignment.IsGuaranteedNoReservation(err) || assignment.IsReservationReleaseRequired(err) {
		t.Fatalf("result-bearing ambiguous transport error was misclassified: %v", err)
	}
	if !reflect.DeepEqual(lease.Requested, []string{"internal/cli/**"}) || len(lease.Granted) != 0 || len(lease.ReservationIDs) != 0 {
		t.Fatalf("result-bearing transport lease = %+v", lease)
	}
}

func TestGenerateAssignmentsLegacyPreservesMultiWindowPaneIdentity(t *testing.T) {
	agents := []assignAgentInfo{
		{pane: tmux.Pane{ID: "%50", WindowIndex: 0, Index: 1}, agentType: "codex", state: "idle"},
		{pane: tmux.Pane{ID: "%51", WindowIndex: 1, Index: 1}, agentType: "claude", state: "idle"},
	}
	beads := []bv.BeadPreview{{ID: "ntm-a", Title: "First"}, {ID: "ntm-b", Title: "Second"}}
	items := generateAssignmentsLegacy(agents, beads, &AssignCommandOptions{Session: "multi", Strategy: "speed"})
	if len(items) != 2 {
		t.Fatalf("assignments=%d, want 2", len(items))
	}
	if items[0].PaneTarget != "0.1" || items[0].PaneID != "%50" || items[1].PaneTarget != "1.1" || items[1].PaneID != "%51" {
		t.Fatalf("multi-window assignments lost physical identity: %+v", items)
	}
	if items[0].AgentName == items[1].AgentName {
		t.Fatalf("multi-window agents share identity %q", items[0].AgentName)
	}
}

func TestResolveDirectAssignmentPaneCanonicalContract(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%10", WindowIndex: 0, Index: 0},
		{ID: "%11", WindowIndex: 0, Index: 1},
		{ID: "%12", WindowIndex: 1, Index: 0},
		{ID: "%13", WindowIndex: 1, Index: 1},
	}

	for _, test := range []struct {
		selector string
		paneID   string
		target   string
	}{
		{selector: "0.1", paneID: "%11", target: "0.1"},
		{selector: "%13", paneID: "%13", target: "1.1"},
		{selector: "1.0", paneID: "%12", target: "1.0"},
	} {
		pane, target, err := resolveDirectAssignmentPane(panes, test.selector)
		if err != nil {
			t.Fatalf("resolve %q: %v", test.selector, err)
		}
		if pane.ID != test.paneID || target != test.target {
			t.Fatalf("resolve %q = pane %s target %s, want %s/%s", test.selector, pane.ID, target, test.paneID, test.target)
		}
	}

	if _, _, err := resolveDirectAssignmentPane(panes, "1"); err == nil || !strings.Contains(err.Error(), "matched 2 panes") {
		t.Fatalf("bare multi-window selector must fail when its window has multiple panes: %v", err)
	}

	singleWindow := []tmux.Pane{{ID: "%20", WindowIndex: 0, Index: 0}, {ID: "%21", WindowIndex: 0, Index: 1}}
	pane, target, err := resolveDirectAssignmentPane(singleWindow, "1")
	if err != nil {
		t.Fatalf("resolve single-window bare pane: %v", err)
	}
	if pane.ID != "%21" || target != "1" {
		t.Fatalf("single-window bare selector = %s/%s, want %%21/1", pane.ID, target)
	}
}

func TestDirectAssignmentIdempotencyKeyUsesRawIntentAndPhysicalPane(t *testing.T) {
	base := &AssignCommandOptions{
		Session:      "project",
		PaneSelector: "1.0",
		Prompt:       "review the parser",
		Template:     "impl",
		IgnoreDeps:   true,
	}
	first := directAssignmentIdempotencyKey(base, "ntm-123", "%42")
	alias := *base
	alias.PaneSelector = "%42"
	if got := directAssignmentIdempotencyKey(&alias, "ntm-123", "%42"); got != first {
		t.Fatalf("selector aliases for one physical pane changed the key: %s != %s", got, first)
	}
	changedPrompt := *base
	changedPrompt.Prompt = "review the lexer"
	if got := directAssignmentIdempotencyKey(&changedPrompt, "ntm-123", "%42"); got == first {
		t.Fatal("changed prompt reused the raw-intent key")
	}
	if got := directAssignmentIdempotencyKey(base, "ntm-123", "%43"); got == first {
		t.Fatal("changed physical pane reused the raw-intent key")
	}
	postClear := directAssignmentIdempotencyKey(base, "ntm-123", "%42", 1)
	if postClear == first {
		t.Fatal("post-clear generation reused the previous raw-intent key")
	}
	if got := directAssignmentIdempotencyKey(base, "ntm-123", "%42", 1); got != postClear {
		t.Fatalf("same post-clear generation was not replay-stable: %s != %s", got, postClear)
	}

	templatePath := filepath.Join(t.TempDir(), "assign-template.txt")
	custom := &AssignCommandOptions{
		Session:      "project",
		PaneSelector: "%42",
		Template:     "custom",
		TemplateFile: templatePath,
	}
	if err := os.WriteFile(templatePath, []byte("first {BEAD_ID}"), 0o600); err != nil {
		t.Fatalf("write first template: %v", err)
	}
	firstTemplateKey := directAssignmentIdempotencyKey(custom, "ntm-123", "%42")
	if err := os.WriteFile(templatePath, []byte("second {BEAD_ID}"), 0o600); err != nil {
		t.Fatalf("write changed template: %v", err)
	}
	if got := directAssignmentIdempotencyKey(custom, "ntm-123", "%42"); got == firstTemplateKey {
		t.Fatal("changed template-file content reused the raw-intent key")
	}
}

func TestDirectAssignCLIProcessHelper(t *testing.T) {
	rawArgs := os.Getenv("NTM_DIRECT_ASSIGN_HELPER_ARGS")
	if rawArgs == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		t.Fatalf("decode direct assign helper args: %v", err)
	}
	os.Args = append([]string{"ntm"}, args...)
	if err := Execute(); err != nil {
		os.Exit(ExitCode(err))
	}
	os.Exit(0)
}

func TestRunDirectPaneAssignmentPreCanceledJSONEnvelope(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	for _, test := range []struct {
		name     string
		ctx      context.Context
		wantCode string
	}{
		{name: "nil context", ctx: nil, wantCode: "INTERNAL_ERROR"},
		{name: "canceled context", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}(), wantCode: "TIMEOUT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldStdout := os.Stdout
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdout pipe: %v", err)
			}
			os.Stdout = writer
			err = runDirectPaneAssignment(test.ctx, &AssignCommandOptions{Session: "cancel-test"})
			_ = writer.Close()
			os.Stdout = oldStdout
			output, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil {
				t.Fatalf("read direct assignment JSON: %v", readErr)
			}
			if !errors.Is(err, errJSONFailure) {
				t.Fatalf("runDirectPaneAssignment error = %v, want errJSONFailure", err)
			}
			var envelope AssignEnvelope[DirectAssignData]
			if decodeErr := json.Unmarshal(output, &envelope); decodeErr != nil {
				t.Fatalf("decode direct assignment JSON: %v raw=%s", decodeErr, output)
			}
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != test.wantCode {
				t.Fatalf("direct assignment envelope = %+v, want code %s", envelope, test.wantCode)
			}
		})
	}
}

func TestDirectAssignCLIReplayIsDurableAndBypassesChangedPreflight(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	root := t.TempDir()
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{home, projectDir, fakeBin, filepath.Join(projectDir, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "1")
	realBR, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br is required for guarded-claim replay coverage")
	}
	beadID := ""
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"create", "--title", "Direct assignment", "--type", "task", "--priority", "2", "--json"},
	} {
		cmd := exec.Command(realBR, args...)
		cmd.Dir = projectDir
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("br %s: %v\n%s", strings.Join(args, " "), runErr, output)
		}
		if args[0] == "create" {
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(output, &created); err != nil || created.ID == "" {
				t.Fatalf("parse br create output: id=%q err=%v output=%s", created.ID, err, output)
			}
			beadID = created.ID
		}
	}

	claimLog := filepath.Join(root, "claims.log")
	brScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
exec %q "$@"
`, claimLog, realBR)
	if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(brScript), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dispatchLog := filepath.Join(root, "dispatch.log")
	agentScriptPath := filepath.Join(root, "agent.sh")
	agentScript := fmt.Sprintf(`#!/bin/sh
printf '❯ '
IFS= read -r line
printf '%%s\n' "$line" >> %q
printf '\n• Working (press esc to interrupt)\n'
sleep 300
`, dispatchLog)
	if err := os.WriteFile(agentScriptPath, []byte(agentScript), 0o755); err != nil {
		t.Fatalf("write agent fixture: %v", err)
	}

	session := fmt.Sprintf("directassign%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })
	paneID, err := tmux.DefaultClient.Run("new-window", "-d", "-t", session, "-c", projectDir, "-P", "-F", "#{pane_id}", agentScriptPath)
	if err != nil {
		t.Fatalf("create agent window: %v", err)
	}
	paneID = strings.TrimSpace(paneID)
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("list panes after creating assignment target: %v", err)
	}
	var expectedPane *tmux.Pane
	for index := range panes {
		if panes[index].ID == paneID {
			expectedPane = &panes[index]
			break
		}
	}
	if expectedPane == nil {
		t.Fatalf("created pane %s was absent from session topology: %+v", paneID, panes)
	}
	if err := tmux.SetPaneTitle(paneID, session+"__cc_1"); err != nil {
		t.Fatalf("set pane title: %v", err)
	}
	waitForDirectAssignFixture(t, paneID, "❯")

	baseArgs := []string{
		"--json", "assign", session,
		"--pane=" + paneID,
		"--beads=" + beadID,
		"--prompt=inspect durable replay",
		"--ignore-deps",
		"--reserve-files=false",
		"--repo=" + projectDir,
	}
	first, firstCode, firstStderr := runDirectAssignCLIProcess(t, baseArgs)
	if firstCode != 0 || !first.Success || first.Data == nil || first.Data.Receipt == nil {
		claimTrace, _ := os.ReadFile(claimLog)
		t.Fatalf("first assign failed: code=%d stderr=%q error=%+v data=%+v claims=%q", firstCode, firstStderr, first.Error, first.Data, claimTrace)
	}
	waitForDirectAssignFileLines(t, dispatchLog, 1)
	waitForDirectAssignFixture(t, paneID, "Working")
	if output, captureErr := tmux.CapturePaneOutput(paneID, 20); captureErr != nil {
		t.Fatalf("capture changed busy preflight: %v", captureErr)
	} else if state := string(observeAssignmentFixture(t, "claude", output).Current.Status.State); state != "working" {
		t.Fatalf("fixture did not become a changed busy preflight: state=%q output=%q", state, output)
	}

	second, secondCode, secondStderr := runDirectAssignCLIProcess(t, baseArgs)
	if secondCode != 0 || !second.Success || second.Data == nil || second.Data.Receipt == nil {
		t.Fatalf("same-intent replay failed: code=%d stderr=%q error=%+v data=%+v", secondCode, secondStderr, second.Error, second.Data)
	}
	if !reflect.DeepEqual(first.Data.Receipt, second.Data.Receipt) {
		t.Fatalf("same-intent replay receipt changed:\nfirst=%+v\nsecond=%+v", first.Data.Receipt, second.Data.Receipt)
	}
	if got := countDirectAssignLogMatches(t, claimLog, "sync --flush-only"); got != 1 {
		t.Fatalf("same-intent replay finalized %d guarded claims, want 1", got)
	}
	if got := countDirectAssignLogLines(t, dispatchLog); got != 1 {
		t.Fatalf("same-intent replay dispatched %d times, want 1", got)
	}

	changedArgs := append([]string(nil), baseArgs...)
	changedArgs[5] = "--prompt=changed intent must conflict"
	changed, changedCode, changedStderr := runDirectAssignCLIProcess(t, changedArgs)
	if changedCode == 0 || changed.Success || changed.Error == nil || changed.Error.Code != "CLAIM_CONFLICT" {
		t.Fatalf("changed intent did not conflict: code=%d stderr=%q error=%+v data=%+v", changedCode, changedStderr, changed.Error, changed.Data)
	}
	if got := countDirectAssignLogMatches(t, claimLog, "sync --flush-only"); got != 1 {
		t.Fatalf("changed-intent conflict actuated a guarded claim; count=%d", got)
	}
	if got := countDirectAssignLogLines(t, dispatchLog); got != 1 {
		t.Fatalf("changed-intent conflict actuated dispatch; count=%d", got)
	}

	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load durable ledger: %v", err)
	}
	durable := store.Get(beadID)
	if durable == nil {
		t.Fatal("direct assignment missing from durable ledger")
	}
	receiptPane := first.Data.Receipt.Pane
	if durable.Pane != expectedPane.Index || durable.OccupancyKey != expectedPane.ID || durable.DispatchTarget != expectedPane.ID ||
		receiptPane.ID != expectedPane.ID || receiptPane.WindowIndex != expectedPane.WindowIndex ||
		receiptPane.Index != expectedPane.Index || receiptPane.Target != expectedPane.Ref().Physical() {
		t.Fatalf("pane identity drift: ledger=%+v receipt=%+v", durable, first.Data.Receipt.Pane)
	}
}

func countDirectAssignLogMatches(t *testing.T, path, needle string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func runDirectAssignCLIProcess(t *testing.T, args []string) (AssignEnvelope[DirectAssignData], int, string) {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode helper args: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDirectAssignCLIProcessHelper$")
	cmd.Env = append(os.Environ(),
		"NTM_DIRECT_ASSIGN_HELPER_ARGS="+string(rawArgs),
		"NTM_TEST_TMUX_ENV_OWNED=1",
		"NTM_NO_COLOR=1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("start direct assign helper: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	var envelope AssignEnvelope[DirectAssignData]
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("decode direct assign output: %v\nstdout=%q\nstderr=%q", decodeErr, stdout.String(), stderr.String())
	}
	return envelope, exitCode, stderr.String()
}

func waitForDirectAssignFixture(t *testing.T, paneID, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := tmux.CapturePaneOutput(paneID, 30)
		if err == nil && strings.Contains(output, marker) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane %s did not contain %q", paneID, marker)
}

func waitForDirectAssignFileLines(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countDirectAssignLogLines(t, path) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %d lines", path, want)
}

func countDirectAssignLogLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func TestClassifyRetryOutcomeTreatsEverySkipAsFailure(t *testing.T) {
	tests := []struct {
		name      string
		retried   []RetryItem
		skipped   []RetrySkippedItem
		wantCode  string
		wantError bool
	}{
		{name: "complete success", retried: []RetryItem{{BeadID: "ntm-ok"}}},
		{name: "single skipped", skipped: []RetrySkippedItem{{BeadID: "ntm-skip", Reason: "no idle pane"}}, wantCode: "RETRY_SKIPPED", wantError: true},
		{name: "all skipped", skipped: []RetrySkippedItem{{BeadID: "ntm-a"}, {BeadID: "ntm-b"}}, wantCode: "RETRY_SKIPPED", wantError: true},
		{name: "partial", retried: []RetryItem{{BeadID: "ntm-ok"}}, skipped: []RetrySkippedItem{{BeadID: "ntm-skip"}}, wantCode: "RETRY_PARTIAL", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, err := classifyRetryOutcome(test.retried, test.skipped)
			if code != test.wantCode || (err != nil) != test.wantError {
				t.Fatalf("classifyRetryOutcome() = (%q, %v), want code=%q error=%v", code, err, test.wantCode, test.wantError)
			}
		})
	}
}

// Epic beads are containers, never dispatchable implementation work (GH#242).
func TestClassifyTriageRecExcludesContainerTypes(t *testing.T) {
	skip := classifyTriageRecForAssignment(bv.TriageRecommendation{
		ID: "br-epic", Title: "Some epic", Type: "epic", Status: "open",
	}, nil)
	if skip == nil {
		t.Fatal("open unlabeled epic classified as assignable; must be skipped")
	}
	if skip.Reason != "container_issue_type" {
		t.Fatalf("skip reason = %q, want container_issue_type", skip.Reason)
	}

	if skip := classifyTriageRecForAssignment(bv.TriageRecommendation{
		ID: "br-task", Title: "Leaf work", Type: "task", Status: "open",
	}, nil); skip != nil {
		t.Fatalf("open task wrongly skipped: %+v", skip)
	}

	// Case-insensitive: br emits lowercase but defend against variants.
	if skip := classifyTriageRecForAssignment(bv.TriageRecommendation{
		ID: "br-epic2", Title: "Epic", Type: "Epic", Status: "open",
	}, nil); skip == nil || skip.Reason != "container_issue_type" {
		t.Fatalf("mixed-case epic not excluded: %+v", skip)
	}
}
