package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdviseWorktrees_PrimaryCheckoutDirtyAndSymlinkRisks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	report := AdviseWorktrees([]WorktreeRiskInput{
		{
			AgentName:           "cc-1",
			Path:                "/repo",
			BranchName:          "ntm/test-session/cc-1",
			SessionID:           "test-session",
			Dirty:               true,
			PrimaryCheckout:     true,
			SymlinkedRepo:       true,
			LastUsed:            now.Add(-2 * time.Hour),
			OwnershipConfidence: 0.95,
		},
	}, WorktreeAdvisorOptions{Now: now})

	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	requireWorktreeText(t, rec.Action, WorktreeActionAskHuman)
	requireWorktreeText(t, rec.Risk, "critical")
	for _, want := range []string{"dirty_worktree", "primary_checkout", "symlinked_repo"} {
		if !containsWorktreeString(rec.ReasonCodes, want) {
			t.Fatalf("ReasonCodes missing %q: %#v", want, rec.ReasonCodes)
		}
	}
	if len(report.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(report.LogRows))
	}
	log := report.LogRows[0]
	requireWorktreeText(t, log.WorktreePath, "/repo")
	requireWorktreeText(t, log.Holder, "cc-1")
	requireWorktreeText(t, log.PathPattern, "")
	if log.ReservationID != 0 {
		t.Fatalf("ReservationID = %d, want 0", log.ReservationID)
	}
}

func TestAdviseWorktrees_BranchMismatchLowersConfidence(t *testing.T) {
	t.Parallel()
	report := AdviseWorktrees([]WorktreeRiskInput{
		{
			AgentName:           "cod-1",
			Path:                "/tmp/wt",
			BranchName:          "feature/loose",
			SessionID:           "test-session",
			OwnershipConfidence: 0.45,
		},
	}, WorktreeAdvisorOptions{Now: time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)})

	rec := report.Recommendations[0]
	requireWorktreeText(t, rec.Action, WorktreeActionAskHuman)
	if !containsWorktreeString(rec.ReasonCodes, "branch_name_mismatch") {
		t.Fatalf("ReasonCodes missing branch mismatch: %#v", rec.ReasonCodes)
	}
	if !containsWorktreeString(rec.ReasonCodes, "low_ownership_confidence") {
		t.Fatalf("ReasonCodes missing low confidence: %#v", rec.ReasonCodes)
	}
}

func TestInspectRiskInput_DetectsSymlinkedRepo(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	target := filepath.Join(projectDir, "target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(projectDir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	input := InspectRiskInput(&WorktreeInfo{
		AgentName:  "cc-1",
		Path:       link,
		BranchName: "ntm/test-session/cc-1",
		SessionID:  "test-session",
	}, projectDir)
	if !input.SymlinkedRepo {
		t.Fatalf("SymlinkedRepo = false, want true: %+v", input)
	}
}

func containsWorktreeString(values []string, want string) bool {
	for _, value := range values {
		if strings.Compare(value, want) == 0 {
			return true
		}
	}
	return false
}

func requireWorktreeText(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}
