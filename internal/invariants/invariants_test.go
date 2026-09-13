package invariants

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

func TestAllInvariants(t *testing.T) {
	invariants := AllInvariants()
	if len(invariants) != 6 {
		t.Errorf("expected 6 invariants, got %d", len(invariants))
	}

	expected := map[InvariantID]bool{
		InvariantNoSilentDataLoss:        true,
		InvariantGracefulDegradation:     true,
		InvariantIdempotentOrchestration: true,
		InvariantRecoverableState:        true,
		InvariantAuditableActions:        true,
		InvariantSafeByDefault:           true,
	}

	for _, id := range invariants {
		if !expected[id] {
			t.Errorf("unexpected invariant: %s", id)
		}
	}
}

func TestDefinitions(t *testing.T) {
	defs := Definitions()
	if len(defs) != 6 {
		t.Errorf("expected 6 definitions, got %d", len(defs))
	}

	for id, def := range defs {
		if def.ID != id {
			t.Errorf("definition ID mismatch: %s != %s", def.ID, id)
		}
		if def.Name == "" {
			t.Errorf("definition %s has empty name", id)
		}
		if def.Description == "" {
			t.Errorf("definition %s has empty description", id)
		}
		if def.Enforcement == "" {
			t.Errorf("definition %s has empty enforcement", id)
		}
	}
}

func TestNewChecker(t *testing.T) {
	tmpDir := t.TempDir()
	checker := NewChecker(tmpDir)

	if checker == nil {
		t.Fatal("NewChecker returned nil")
	}
	if checker.projectDir != tmpDir {
		t.Errorf("projectDir mismatch: %s != %s", checker.projectDir, tmpDir)
	}
}

func TestCheckAll(t *testing.T) {
	tmpDir := t.TempDir()
	checker := NewChecker(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	report := checker.CheckAll(ctx)

	if report == nil {
		t.Fatal("CheckAll returned nil")
	}
	if len(report.Results) != 6 {
		t.Errorf("expected 6 results, got %d", len(report.Results))
	}
	if report.Timestamp.IsZero() {
		t.Error("report timestamp is zero")
	}

	// Every result must carry a recognized status and say something. This used
	// to assert that all six passed, which was satisfiable only because every
	// check ended in an unconditional `Passed = true`.
	for id, result := range report.Results {
		switch result.Status {
		case StatusOK, StatusWarning, StatusError, StatusUnverified:
		default:
			t.Errorf("invariant %s has unrecognized status %q", id, result.Status)
		}
		if result.Message == "" {
			t.Errorf("invariant %s reported no message", id)
		}
		if result.Passed && result.Status != StatusOK {
			t.Errorf("invariant %s claims Passed with status %q", id, result.Status)
		}
		if result.CheckedAt.IsZero() {
			t.Errorf("invariant %s has no CheckedAt", id)
		}
	}
}

// A bare temp directory has no pre-commit guard, so the No Silent Data Loss
// invariant must report that rather than tick. This is the regression that
// matters: the check gathered this exact evidence and then ignored it.
func TestCheckAllReportsMissingProtections(t *testing.T) {
	checker := NewChecker(t.TempDir())
	checker.loadPolicy = func() (*policy.Policy, error) { return policy.DefaultPolicy(), nil }

	report := checker.CheckAll(context.Background())

	result := report.Results[InvariantNoSilentDataLoss]
	if result.Status != StatusWarning {
		t.Errorf("status = %q, want %q with no pre-commit guard installed", result.Status, StatusWarning)
	}
	if result.Passed {
		t.Error("reported a pass with no pre-commit guard installed")
	}
}

func TestCheckNoSilentDataLoss_WithPolicyFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .ntm directory with policy file
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatal(err)
	}

	policyPath := filepath.Join(ntmDir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := NewChecker(tmpDir)
	ctx := context.Background()

	result := checker.checkNoSilentDataLoss(ctx)

	// A policy file alone does not establish the invariant: the verdict turns on
	// whether a pre-commit guard is installed, and this fixture has none.
	if result.Passed {
		t.Error("a policy.yaml alone must not satisfy the invariant without a guard")
	}

	found := false
	for _, detail := range result.Details {
		if strings.Contains(detail, "policy.yaml exists") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected detail about policy.yaml existing")
	}
}

func TestCheckAuditableActions_WithLogsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .ntm/logs directory
	logsDir := filepath.Join(tmpDir, ".ntm", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatal(err)
	}

	checker := NewChecker(tmpDir)
	ctx := context.Background()

	result := checker.checkAuditableActions(ctx)

	if !result.Passed {
		t.Errorf("expected pass with logs dir: %s", result.Message)
	}

	found := false
	for _, detail := range result.Details {
		if strings.Contains(detail, "logs directory exists") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected detail about logs directory existing")
	}
}

func TestCheckRecoverableState_WithStateDB(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .ntm directory with state.db
	ntmDir := filepath.Join(tmpDir, ".ntm")
	if err := os.MkdirAll(ntmDir, 0755); err != nil {
		t.Fatal(err)
	}

	stateDBPath := filepath.Join(ntmDir, "state.db")
	if err := os.WriteFile(stateDBPath, []byte("sqlite db"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := NewChecker(tmpDir)
	ctx := context.Background()

	result := checker.checkRecoverableState(ctx)

	if !result.Passed {
		t.Errorf("expected pass with state.db: %s", result.Message)
	}

	found := false
	for _, detail := range result.Details {
		if strings.Contains(detail, "state.db exists") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected detail about state.db existing")
	}
}

func TestCheckNoSilentDataLoss_WithPreCommitGuard(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .git/hooks directory with pre-commit containing ntm guard
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}

	preCommit := filepath.Join(hooksDir, "pre-commit")
	script := `#!/bin/bash
# ntm-precommit-guard
echo "checking..."
`
	if err := os.WriteFile(preCommit, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	checker := NewChecker(tmpDir)
	ctx := context.Background()

	result := checker.checkNoSilentDataLoss(ctx)

	if !result.Passed {
		t.Errorf("expected pass with pre-commit guard: %s", result.Message)
	}

	found := false
	for _, detail := range result.Details {
		if strings.Contains(detail, "pre-commit guard installed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected detail about pre-commit guard, got: %v", result.Details)
	}
}

// TestInvariantViolation tests that violations are properly detected.
// These tests verify the invariant checking logic correctly identifies
// when invariants are NOT being enforced.
func TestInvariantViolationDetection(t *testing.T) {
	t.Run("missing infrastructure is detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		checker := NewChecker(tmpDir)
		ctx := context.Background()

		// With no .ntm directory, checks should still pass but with "will be created" messages
		result := checker.checkAuditableActions(ctx)
		if !result.Passed {
			t.Error("should pass even without logs dir (infrastructure is optional)")
		}

		foundWillCreate := false
		for _, detail := range result.Details {
			if strings.Contains(detail, "will be created") {
				foundWillCreate = true
				break
			}
		}
		if !foundWillCreate {
			t.Error("should indicate logs will be created")
		}
	})

	// These two invariants are structural and nothing here measures them. They
	// must say so rather than claim a pass — this test used to assert they
	// "should always pass", which is what kept the fabricated tick in place.
	t.Run("structural invariants report unverified, not ok", func(t *testing.T) {
		tmpDir := t.TempDir()
		checker := NewChecker(tmpDir)
		ctx := context.Background()

		for name, result := range map[string]CheckResult{
			"graceful degradation":     checker.checkGracefulDegradation(ctx),
			"idempotent orchestration": checker.checkIdempotentOrchestration(ctx),
		} {
			if result.Status != StatusUnverified {
				t.Errorf("%s: status = %q, want %q", name, result.Status, StatusUnverified)
			}
			if result.Passed {
				t.Errorf("%s: reported a pass it never measured", name)
			}
			if result.Status == StatusOK {
				t.Errorf("%s: an unmeasured invariant must not render as a tick", name)
			}
			if len(result.Details) == 0 {
				t.Errorf("%s: should explain what is and is not covered", name)
			}
		}
	})
}
