// Package e2e contains end-to-end tests for NTM dependency-aware assignment.
// Bead: bd-2soni - E2E Tests: Dependency-aware assignment and unblock reassignment
//
// These tests verify:
// - Only unblocked beads get assigned
// - Completing a bead unblocks its dependents
// - Multi-level dependency chains work correctly
// - Unblock reassignment in watch mode
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// DepTestSuite manages E2E test sessions for dependency-aware assignment tests.
type DepTestSuite struct {
	t           *testing.T
	logger      *testutil.TestLogger
	configPath  string
	projectDir  string
	projectBase string
	session     string
	beadA       string // root bead
	beadB       string // depends on A
	beadC       string // depends on A
	beadD       string // depends on B and C
	cleanup     []func()
}

// NewDepTestSuite creates a new test suite for dependency assignment testing.
func NewDepTestSuite(t *testing.T, scenario string) *DepTestSuite {
	testutil.E2ETestPrecheck(t)
	requireBr(t)
	ensureBDShim(t)

	logDir := t.TempDir()
	logger := testutil.NewTestLogger(t, logDir)

	projectBase := t.TempDir()
	session := fmt.Sprintf("ntm_dep_%s_%d", scenario, time.Now().UnixNano())
	projectDir := filepath.Join(projectBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Create config file
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"
`, projectBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	s := &DepTestSuite{
		t:           t,
		logger:      logger,
		configPath:  configPath,
		projectDir:  projectDir,
		projectBase: projectBase,
		session:     session,
		cleanup:     []func(){},
	}

	// Cleanup on test completion
	s.cleanup = append(s.cleanup, func() {
		exec.Command("tmux", "kill-session", "-t", s.session).Run()
	})

	return s
}

// InitDependencyChain initializes beads with dependency structure: A -> (B, C) -> D
func (s *DepTestSuite) InitDependencyChain() error {
	s.logger.LogSection("Initializing dependency chain")

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	// Initialize br
	out, err := exec.Command("br", "init").CombinedOutput()
	if err != nil {
		return fmt.Errorf("br init failed: %w, output: %s", err, string(out))
	}

	// Create bead A (root)
	s.beadA, err = s.createBead("Bead A - root task")
	if err != nil {
		return fmt.Errorf("creating bead A: %w", err)
	}
	s.logger.Log("Created bead_a=%s (root bead, no deps)", s.beadA)

	// Create bead B (depends on A)
	s.beadB, err = s.createBead("Bead B - depends on A")
	if err != nil {
		return fmt.Errorf("creating bead B: %w", err)
	}
	s.logger.Log("Created bead_b=%s (depends on A)", s.beadB)

	// Create bead C (depends on A)
	s.beadC, err = s.createBead("Bead C - depends on A")
	if err != nil {
		return fmt.Errorf("creating bead C: %w", err)
	}
	s.logger.Log("Created bead_c=%s (depends on A)", s.beadC)

	// Create bead D (depends on B and C)
	s.beadD, err = s.createBead("Bead D - depends on B and C")
	if err != nil {
		return fmt.Errorf("creating bead D: %w", err)
	}
	s.logger.Log("Created bead_d=%s (depends on B and C)", s.beadD)

	// Add dependencies
	if err := s.addDep(s.beadB, s.beadA); err != nil {
		return fmt.Errorf("adding dep B->A: %w", err)
	}
	if err := s.addDep(s.beadC, s.beadA); err != nil {
		return fmt.Errorf("adding dep C->A: %w", err)
	}
	if err := s.addDep(s.beadD, s.beadB); err != nil {
		return fmt.Errorf("adding dep D->B: %w", err)
	}
	if err := s.addDep(s.beadD, s.beadC); err != nil {
		return fmt.Errorf("adding dep D->C: %w", err)
	}

	// Sync
	exec.Command("br", "sync", "--flush-only").Run()

	return nil
}

func (s *DepTestSuite) createBead(title string) (string, error) {
	out, err := exec.Command("br", "create", title, "-t", "task", "-p", "1", "--json").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("br create failed: %w, output: %s", err, string(out))
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		// Try single object format
		var single map[string]interface{}
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return "", fmt.Errorf("parsing bead output: %s", string(out))
		}
		if id, ok := single["id"].(string); ok {
			return id, nil
		}
		return "", fmt.Errorf("no id in output: %s", string(out))
	}
	if len(result) > 0 {
		if id, ok := result[0]["id"].(string); ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("no id in output: %s", string(out))
}

func (s *DepTestSuite) addDep(bead, blockedBy string) error {
	out, err := exec.Command("br", "dep", "add", bead, blockedBy).CombinedOutput()
	if err != nil {
		return fmt.Errorf("br dep add failed: %w, output: %s", err, string(out))
	}
	s.logger.Log("Added dependency: %s blocked_by %s", bead, blockedBy)
	return nil
}

func (s *DepTestSuite) getReadyBeads() ([]string, error) {
	out, err := exec.Command("br", "ready", "--json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("br ready failed: %w, output: %s", err, string(out))
	}

	var beads []map[string]interface{}
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parsing ready beads: %w", err)
	}

	var ids []string
	for _, b := range beads {
		if id, ok := b["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *DepTestSuite) closeBead(beadID string) error {
	out, err := exec.Command("br", "update", beadID, "--status", "closed").CombinedOutput()
	if err != nil {
		return fmt.Errorf("br update failed: %w, output: %s", err, string(out))
	}
	exec.Command("br", "sync", "--flush-only").Run()
	s.logger.Log("Closed bead_id=%s status=closed", beadID)
	return nil
}

// SpawnSession spawns a new tmux session with agents.
func (s *DepTestSuite) SpawnSession(ccCount int) error {
	s.logger.LogSection("Spawning session")

	args := []string{
		"--config", s.configPath,
		"--json",
		"spawn", s.session,
		fmt.Sprintf("--cc=%d", ccCount),
		"--no-user",
	}

	cmd := exec.Command("ntm", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("NTM_PROJECTS_BASE=%s", s.projectBase))

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ntm spawn failed: %w, output: %s", err, string(out))
	}

	s.logger.Log("Spawned session=%s agents=%d output_len=%d", s.session, ccCount, len(out))
	return nil
}

// RunAssign executes ntm assign and returns the JSON output.
func (s *DepTestSuite) RunAssign(args ...string) (map[string]interface{}, error) {
	baseArgs := []string{
		"--config", s.configPath,
		"--json",
		"assign", s.session,
	}
	baseArgs = append(baseArgs, args...)

	cmd := exec.Command("ntm", baseArgs...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("NTM_PROJECTS_BASE=%s", s.projectBase))
	cmd.Dir = s.projectDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ntm assign failed: %w, output: %s", err, string(out))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing assign output: %w, raw: %s", err, string(out))
	}

	return result, nil
}

// ClearAssignments clears any existing assignments.
func (s *DepTestSuite) ClearAssignments() error {
	args := []string{
		"--config", s.configPath,
		"assign", s.session,
		"--clear",
	}

	cmd := exec.Command("ntm", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("NTM_PROJECTS_BASE=%s", s.projectBase))
	cmd.Run() // Ignore errors - may not have any assignments
	return nil
}

// Teardown cleans up test resources.
func (s *DepTestSuite) Teardown() {
	s.logger.LogSection("Teardown")
	for _, fn := range s.cleanup {
		fn()
	}
}

// getAssignedBeadIDs extracts bead IDs from assignment output.
func getAssignedBeadIDs(result map[string]interface{}) []string {
	var ids []string
	if assignments, ok := result["assignments"].([]interface{}); ok {
		for _, a := range assignments {
			if aMap, ok := a.(map[string]interface{}); ok {
				if id, ok := aMap["bead_id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// getAssignedCount gets the assigned_count from summary.
func getAssignedCount(result map[string]interface{}) int {
	if summary, ok := result["summary"].(map[string]interface{}); ok {
		if count, ok := summary["assigned_count"].(float64); ok {
			return int(count)
		}
	}
	return 0
}

// TestDependencyAssignment_InitialState verifies only root bead is ready initially.
func TestDependencyAssignment_InitialState(t *testing.T) {
	s := NewDepTestSuite(t, "initial")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	// Verify only bead A is ready
	ready, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads: %v", err)
	}

	s.logger.Log("Initial ready beads: expected=1 got=%d beads=%v", len(ready), ready)

	if len(ready) != 1 {
		t.Fatalf("expected 1 ready bead, got %d: %v", len(ready), ready)
	}

	if ready[0] != s.beadA {
		t.Fatalf("expected bead A (%s) to be ready, got %s", s.beadA, ready[0])
	}

	s.logger.Log("PASS test=InitialState ready=%s", s.beadA)
}

// TestDependencyAssignment_OnlyAssignUnblocked verifies only unblocked beads get assigned.
func TestDependencyAssignment_OnlyAssignUnblocked(t *testing.T) {
	s := NewDepTestSuite(t, "unblocked")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	if err := s.SpawnSession(2); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	// Run assignment - should only assign bead A
	result, err := s.RunAssign("--auto", "--strategy=dependency")
	if err != nil {
		t.Fatalf("RunAssign: %v", err)
	}

	assigned := getAssignedBeadIDs(result)
	s.logger.Log("First assignment: expected=[%s] got=%v", s.beadA, assigned)

	if len(assigned) != 1 || assigned[0] != s.beadA {
		t.Fatalf("expected only bead A to be assigned, got: %v", assigned)
	}

	s.logger.Log("PASS test=OnlyAssignUnblocked assigned=%v", assigned)
}

// TestDependencyAssignment_UnblockOnComplete verifies completing a bead unblocks dependents.
func TestDependencyAssignment_UnblockOnComplete(t *testing.T) {
	s := NewDepTestSuite(t, "unblock")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	// Close bead A
	if err := s.closeBead(s.beadA); err != nil {
		t.Fatalf("closeBead A: %v", err)
	}

	// Verify B and C are now ready
	ready, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads: %v", err)
	}

	expected := []string{s.beadB, s.beadC}
	sort.Strings(expected)
	s.logger.Log("After closing A: expected=%v got=%v", expected, ready)

	if len(ready) != 2 {
		t.Fatalf("expected 2 ready beads, got %d: %v", len(ready), ready)
	}

	if ready[0] != expected[0] || ready[1] != expected[1] {
		t.Fatalf("expected %v to be ready, got %v", expected, ready)
	}

	s.logger.Log("PASS test=UnblockOnComplete unblocked=%v", ready)
}

// TestDependencyAssignment_FullChain tests the complete dependency chain workflow.
func TestDependencyAssignment_FullChain(t *testing.T) {
	s := NewDepTestSuite(t, "fullchain")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	if err := s.SpawnSession(2); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	// Phase 1: Assign only A
	result1, err := s.RunAssign("--auto", "--strategy=round-robin")
	if err != nil {
		t.Fatalf("RunAssign phase 1: %v", err)
	}

	assigned1 := getAssignedBeadIDs(result1)
	if len(assigned1) != 1 || assigned1[0] != s.beadA {
		t.Fatalf("phase 1: expected [%s], got %v", s.beadA, assigned1)
	}
	s.logger.Log("Phase 1: assigned=%v expected=%s", assigned1, s.beadA)

	// Complete A
	if err := s.closeBead(s.beadA); err != nil {
		t.Fatalf("closeBead A: %v", err)
	}

	// Phase 2: Assign B and C
	s.ClearAssignments()
	result2, err := s.RunAssign("--auto", "--strategy=round-robin")
	if err != nil {
		t.Fatalf("RunAssign phase 2: %v", err)
	}

	assigned2 := getAssignedBeadIDs(result2)
	expected2 := []string{s.beadB, s.beadC}
	sort.Strings(expected2)

	if len(assigned2) != 2 {
		t.Fatalf("phase 2: expected 2 assignments, got %d: %v", len(assigned2), assigned2)
	}
	s.logger.Log("Phase 2: assigned=%v expected=%v", assigned2, expected2)

	// Complete B and C
	if err := s.closeBead(s.beadB); err != nil {
		t.Fatalf("closeBead B: %v", err)
	}
	if err := s.closeBead(s.beadC); err != nil {
		t.Fatalf("closeBead C: %v", err)
	}

	// Phase 3: Assign D
	s.ClearAssignments()
	result3, err := s.RunAssign("--auto")
	if err != nil {
		t.Fatalf("RunAssign phase 3: %v", err)
	}

	assigned3 := getAssignedBeadIDs(result3)
	if len(assigned3) != 1 || assigned3[0] != s.beadD {
		t.Fatalf("phase 3: expected [%s], got %v", s.beadD, assigned3)
	}
	s.logger.Log("Phase 3: assigned=%v expected=%s", assigned3, s.beadD)

	s.logger.Log("PASS test=FullChain phases=3")
}

// TestDependencyAssignment_IgnoreDepsFlag tests the --ignore-deps flag.
func TestDependencyAssignment_IgnoreDepsFlag(t *testing.T) {
	s := NewDepTestSuite(t, "ignoredeps")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	if err := s.SpawnSession(4); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	// Without --ignore-deps: should only assign A
	result1, err := s.RunAssign("--auto")
	if err != nil {
		t.Fatalf("RunAssign without ignore-deps: %v", err)
	}

	count1 := getAssignedCount(result1)
	s.logger.Log("Without --ignore-deps: assigned_count=%d expected=1", count1)

	if count1 != 1 {
		t.Fatalf("without --ignore-deps: expected 1 assignment, got %d", count1)
	}

	// Clear and try with --ignore-deps: should assign more
	s.ClearAssignments()
	result2, err := s.RunAssign("--auto", "--ignore-deps")
	if err != nil {
		t.Fatalf("RunAssign with ignore-deps: %v", err)
	}

	count2 := getAssignedCount(result2)
	s.logger.Log("With --ignore-deps: assigned_count=%d expected_min=2", count2)

	// Should assign more than 1 since we're ignoring dependency constraints
	if count2 <= 1 {
		t.Fatalf("with --ignore-deps: expected >1 assignments, got %d", count2)
	}

	s.logger.Log("PASS test=IgnoreDepsFlag normal=%d ignored=%d", count1, count2)
}

// TestDependencyAssignment_DependencyStrategy tests the dependency assignment strategy.
func TestDependencyAssignment_DependencyStrategy(t *testing.T) {
	s := NewDepTestSuite(t, "depstrategy")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	if err := s.SpawnSession(2); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	// Use dependency strategy - should prioritize beads that unblock others
	result, err := s.RunAssign("--auto", "--strategy=dependency")
	if err != nil {
		t.Fatalf("RunAssign: %v", err)
	}

	assigned := getAssignedBeadIDs(result)
	s.logger.Log("Dependency strategy: assigned=%v expected_contains=%s", assigned, s.beadA)

	// A should definitely be assigned since it unblocks B and C
	found := false
	for _, id := range assigned {
		if id == s.beadA {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("dependency strategy should prioritize A (unblocks 2), got: %v", assigned)
	}

	s.logger.Log("PASS test=DependencyStrategy assigned=%v", assigned)
}

// TestDependencyAssignment_MultiLevelUnblock verifies multi-level dependency unblocking.
func TestDependencyAssignment_MultiLevelUnblock(t *testing.T) {
	s := NewDepTestSuite(t, "multilevel")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	// Verify D is blocked (needs B AND C which need A)
	ready, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads: %v", err)
	}

	// Only A should be ready
	if len(ready) != 1 || ready[0] != s.beadA {
		t.Fatalf("expected only A ready initially, got: %v", ready)
	}

	// Close A - B and C should become ready, but not D
	if err := s.closeBead(s.beadA); err != nil {
		t.Fatalf("closeBead A: %v", err)
	}

	ready2, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads after A: %v", err)
	}

	s.logger.Log("After closing A: ready=%v", ready2)

	// D should NOT be ready (still blocked by B and C)
	for _, id := range ready2 {
		if id == s.beadD {
			t.Fatalf("D should not be ready yet (B and C not closed)")
		}
	}

	// Close B only - D still blocked by C
	if err := s.closeBead(s.beadB); err != nil {
		t.Fatalf("closeBead B: %v", err)
	}

	ready3, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads after B: %v", err)
	}

	s.logger.Log("After closing B: ready=%v", ready3)

	for _, id := range ready3 {
		if id == s.beadD {
			t.Fatalf("D should not be ready yet (C not closed)")
		}
	}

	// Close C - D should now be ready
	if err := s.closeBead(s.beadC); err != nil {
		t.Fatalf("closeBead C: %v", err)
	}

	ready4, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads after C: %v", err)
	}

	s.logger.Log("After closing C: ready=%v", ready4)

	found := false
	for _, id := range ready4 {
		if id == s.beadD {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("D should be ready after both B and C closed, got: %v", ready4)
	}

	s.logger.Log("PASS test=MultiLevelUnblock final_ready=%v", ready4)
}

// TestDependencyAssignment_StopWhenDone tests the --stop-when-done flag concept.
func TestDependencyAssignment_StopWhenDone(t *testing.T) {
	s := NewDepTestSuite(t, "stopwhendone")
	defer s.Teardown()

	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(s.projectDir)

	if err := s.InitDependencyChain(); err != nil {
		t.Fatalf("InitDependencyChain: %v", err)
	}

	if err := s.SpawnSession(2); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	// Close all beads to simulate completion
	if err := s.closeBead(s.beadA); err != nil {
		t.Fatalf("closeBead A: %v", err)
	}
	if err := s.closeBead(s.beadB); err != nil {
		t.Fatalf("closeBead B: %v", err)
	}
	if err := s.closeBead(s.beadC); err != nil {
		t.Fatalf("closeBead C: %v", err)
	}
	if err := s.closeBead(s.beadD); err != nil {
		t.Fatalf("closeBead D: %v", err)
	}

	// Verify no ready beads
	ready, err := s.getReadyBeads()
	if err != nil {
		t.Fatalf("getReadyBeads: %v", err)
	}

	if len(ready) != 0 {
		t.Fatalf("expected 0 ready beads after all closed, got: %v", ready)
	}

	// Run assign - should report no work
	s.ClearAssignments()
	result, err := s.RunAssign("--auto")
	if err != nil {
		// This might fail gracefully with "no work" message
		if !strings.Contains(err.Error(), "no") && !strings.Contains(err.Error(), "ready") {
			t.Fatalf("RunAssign: %v", err)
		}
	}

	count := getAssignedCount(result)
	s.logger.Log("After all closed: assigned_count=%d expected=0", count)

	if count != 0 {
		t.Fatalf("expected 0 assignments when no work, got %d", count)
	}

	s.logger.Log("PASS test=StopWhenDone ready_beads=%d", len(ready))
}

// TestDependencyAssignment_CircularDetection tests that circular deps are handled.
func TestDependencyAssignment_CircularDetection(t *testing.T) {
	testutil.E2ETestPrecheck(t)
	requireBr(t)
	ensureBDShim(t)

	// This test creates a potential cycle and verifies the system handles it
	t.Skip("Circular dependency detection requires specific br version support")
}
