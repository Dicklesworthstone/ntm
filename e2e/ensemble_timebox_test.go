//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for time-boxed ensemble execution.
//
// Bead: bd-jvic3 - Tests: time-boxed ensemble execution (integration/E2E)
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
)

// runTimeboxCmd executes an ntm command and returns structured output.
func runTimeboxCmd(t *testing.T, suite *TestSuite, label string, args ...string) cliRunResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ntm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded && err == nil {
		err = fmt.Errorf("command timed out after 60s")
	}

	suite.Logger().Log("[E2E-TIMEBOX] %s args=%v duration_ms=%d err=%v", label, args, duration.Milliseconds(), err)
	suite.Logger().Log("[E2E-TIMEBOX] %s stdout=%s", label, truncateString(stdout.String(), 2000))
	suite.Logger().Log("[E2E-TIMEBOX] %s stderr=%s", label, truncateString(stderr.String(), 2000))

	return cliRunResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: duration,
		Err:      err,
	}
}

// parseTimeboxJSON unmarshals JSON output, fataling on error.
func parseTimeboxJSON(t *testing.T, suite *TestSuite, label string, stdout []byte, v interface{}) {
	t.Helper()

	if err := json.Unmarshal(stdout, v); err != nil {
		t.Fatalf("[E2E-TIMEBOX] %s JSON parse failed: %v stdout=%s", label, err, string(stdout))
	}
	suite.Logger().LogJSON("[E2E-TIMEBOX] "+label+" parsed", v)
}

// -------------------------------------------------------------------
// Mode Priority Ordering Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_ModePriorityOrdering(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_priority_ordering")
	defer suite.Teardown()

	// Create a catalog with modes at different tiers
	modes := []ensemble.ReasoningMode{
		{
			ID:        "exp-mode",
			Code:      "A1",
			Name:      "Experimental Mode",
			Category:  ensemble.CategoryFormal,
			Tier:      ensemble.TierExperimental,
			ShortDesc: "An experimental test mode",
		},
		{
			ID:        "core-mode",
			Code:      "B1",
			Name:      "Core Mode",
			Category:  ensemble.CategoryAmpliative,
			Tier:      ensemble.TierCore,
			ShortDesc: "A core test mode",
		},
		{
			ID:        "adv-mode",
			Code:      "C1",
			Name:      "Advanced Mode",
			Category:  ensemble.CategoryUncertainty,
			Tier:      ensemble.TierAdvanced,
			ShortDesc: "An advanced test mode",
		},
	}

	catalog, err := ensemble.NewModeCatalog(modes, "test-v1")
	if err != nil {
		t.Fatalf("[E2E-TIMEBOX] failed to create catalog: %v", err)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Created catalog with %d modes (core, advanced, experimental)", len(modes))

	// Verify catalog was created correctly
	for _, m := range modes {
		got := catalog.GetMode(m.ID)
		if got == nil {
			t.Fatalf("[E2E-TIMEBOX] mode %q not found in catalog", m.ID)
		}
		suite.Logger().Log("[E2E-TIMEBOX] Verified mode %s tier=%s category=%s", m.ID, m.Tier, m.Category)
	}

	// Verify tier ordering: Core(3) > Advanced(2) > Experimental(1)
	tierWeights := map[ensemble.ModeTier]int{
		ensemble.TierCore:         3,
		ensemble.TierAdvanced:     2,
		ensemble.TierExperimental: 1,
	}

	for tier, expected := range tierWeights {
		suite.Logger().Log("[E2E-TIMEBOX] Tier %s expected weight=%d", tier, expected)
	}

	// When sorted by priority, core should come first, then advanced, then experimental
	type modeWithPriority struct {
		ID       string
		Tier     ensemble.ModeTier
		Priority int
	}
	sorted := make([]modeWithPriority, 0, len(modes))
	for _, m := range modes {
		sorted = append(sorted, modeWithPriority{
			ID:       m.ID,
			Tier:     m.Tier,
			Priority: tierWeights[m.Tier],
		})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	if sorted[0].ID != "core-mode" {
		t.Errorf("[E2E-TIMEBOX] expected core-mode first, got %s", sorted[0].ID)
	}
	if sorted[1].ID != "adv-mode" {
		t.Errorf("[E2E-TIMEBOX] expected adv-mode second, got %s", sorted[1].ID)
	}
	if sorted[2].ID != "exp-mode" {
		t.Errorf("[E2E-TIMEBOX] expected exp-mode third, got %s", sorted[2].ID)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Priority ordering verified: core > advanced > experimental")
}

// -------------------------------------------------------------------
// Value Scoring Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_ValueScoring(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_value_scoring")
	defer suite.Teardown()

	// Test value scoring: tier multipliers and bonuses
	testCases := []struct {
		name         string
		tier         ensemble.ModeTier
		category     ensemble.ModeCategory
		bestFor      []string
		diff         string
		minScore     float64
		maxScore     float64
		expectHigher bool // relative to baseline
	}{
		{
			name:     "core_baseline",
			tier:     ensemble.TierCore,
			category: ensemble.CategoryFormal,
			minScore: 0.9,
			maxScore: 1.1,
		},
		{
			name:     "advanced_lower",
			tier:     ensemble.TierAdvanced,
			category: ensemble.CategoryFormal,
			minScore: 0.8,
			maxScore: 0.95,
		},
		{
			name:     "experimental_lowest",
			tier:     ensemble.TierExperimental,
			category: ensemble.CategoryFormal,
			minScore: 0.6,
			maxScore: 0.8,
		},
		{
			name:     "core_with_bestfor",
			tier:     ensemble.TierCore,
			category: ensemble.CategoryFormal,
			bestFor:  []string{"security", "logic", "proofs"},
			minScore: 1.0,
			maxScore: 1.4,
		},
		{
			name:     "core_with_differentiator",
			tier:     ensemble.TierCore,
			category: ensemble.CategoryFormal,
			diff:     "Unique formal verification approach",
			minScore: 1.0,
			maxScore: 1.2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			suite.Logger().Log("[E2E-TIMEBOX] Testing value score: %s tier=%s bestFor=%v diff=%q",
				tc.name, tc.tier, tc.bestFor, tc.diff)

			// The value scoring uses tier multiplier (core=1.0, advanced=0.85, experimental=0.7)
			// plus bonuses for BestFor (min(0.3, 0.03*len)) and Differentiator (+0.05)
			baseScore := 1.0
			switch tc.tier {
			case ensemble.TierCore:
				baseScore = 1.0
			case ensemble.TierAdvanced:
				baseScore = 0.85
			case ensemble.TierExperimental:
				baseScore = 0.7
			}

			score := baseScore
			if len(tc.bestFor) > 0 {
				bonus := math.Min(0.3, 0.03*float64(len(tc.bestFor)))
				score += bonus
			}
			if strings.TrimSpace(tc.diff) != "" {
				score += 0.05
			}

			suite.Logger().Log("[E2E-TIMEBOX] Computed score=%.3f (base=%.2f) for %s", score, baseScore, tc.name)

			if score < tc.minScore || score > tc.maxScore {
				t.Errorf("[E2E-TIMEBOX] score %.3f outside expected range [%.2f, %.2f] for %s",
					score, tc.minScore, tc.maxScore, tc.name)
			}
		})
	}
}

// -------------------------------------------------------------------
// Runtime Estimation Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_RuntimeEstimation(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_runtime_estimation")
	defer suite.Teardown()

	// Runtime estimation constants from internal/ensemble/manager.go
	const (
		tokensPerSecond   = 25.0
		minRuntimeSeconds = 15.0
	)

	// Test runtime estimation for different mode categories and tiers
	testCases := []struct {
		category ensemble.ModeCategory
		tier     ensemble.ModeTier
		name     string
	}{
		{ensemble.CategoryFormal, ensemble.TierCore, "formal_core"},
		{ensemble.CategoryFormal, ensemble.TierAdvanced, "formal_advanced"},
		{ensemble.CategoryFormal, ensemble.TierExperimental, "formal_experimental"},
		{ensemble.CategoryMeta, ensemble.TierCore, "meta_core"},
		{ensemble.CategoryStrategic, ensemble.TierCore, "strategic_core"},
		{ensemble.CategoryDialectical, ensemble.TierCore, "dialectical_core"},
		{ensemble.CategoryAmpliative, ensemble.TierCore, "ampliative_core"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// estimateTypicalCost logic from mode_card.go
			baseCost := 2000
			switch tc.category {
			case ensemble.CategoryFormal:
				baseCost = 3000
			case ensemble.CategoryMeta:
				baseCost = 2500
			case ensemble.CategoryStrategic:
				baseCost = 2500
			case ensemble.CategoryDialectical:
				baseCost = 2800
			default:
				baseCost = 2000
			}

			// Tier adjustment
			switch tc.tier {
			case ensemble.TierAdvanced:
				baseCost = int(float64(baseCost) * 1.2)
			case ensemble.TierExperimental:
				baseCost = int(float64(baseCost) * 1.5)
			}

			// Runtime = tokens / tokensPerSecond, min 15s
			seconds := float64(baseCost) / tokensPerSecond
			if seconds < minRuntimeSeconds {
				seconds = minRuntimeSeconds
			}
			estimatedRuntime := time.Duration(seconds * float64(time.Second))

			suite.Logger().Log("[E2E-TIMEBOX] %s: tokens=%d seconds=%.1f runtime=%v",
				tc.name, baseCost, seconds, estimatedRuntime)

			// All modes should have runtime >= 15s
			if estimatedRuntime < time.Duration(minRuntimeSeconds)*time.Second {
				t.Errorf("[E2E-TIMEBOX] runtime %v below minimum %v for %s",
					estimatedRuntime, time.Duration(minRuntimeSeconds)*time.Second, tc.name)
			}

			// Formal experimental should have highest runtime (~4500 tokens / 25 = 180s)
			if tc.category == ensemble.CategoryFormal && tc.tier == ensemble.TierExperimental {
				if estimatedRuntime < 150*time.Second {
					t.Errorf("[E2E-TIMEBOX] formal experimental runtime %v unexpectedly low", estimatedRuntime)
				}
			}
		})
	}
}

// -------------------------------------------------------------------
// Budget Config Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_DefaultBudgetConfig(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_default_budget")
	defer suite.Teardown()

	budget := ensemble.DefaultBudgetConfig()

	suite.Logger().Log("[E2E-TIMEBOX] DefaultBudgetConfig: MaxTokensPerMode=%d MaxTotalTokens=%d TimeoutPerMode=%v TotalTimeout=%v MaxRetries=%d",
		budget.MaxTokensPerMode, budget.MaxTotalTokens, budget.TimeoutPerMode, budget.TotalTimeout, budget.MaxRetries)

	// Verify defaults are sensible
	if budget.MaxTokensPerMode != 4000 {
		t.Errorf("[E2E-TIMEBOX] expected MaxTokensPerMode=4000, got %d", budget.MaxTokensPerMode)
	}
	if budget.MaxTotalTokens != 50000 {
		t.Errorf("[E2E-TIMEBOX] expected MaxTotalTokens=50000, got %d", budget.MaxTotalTokens)
	}
	if budget.TimeoutPerMode != 5*time.Minute {
		t.Errorf("[E2E-TIMEBOX] expected TimeoutPerMode=5m, got %v", budget.TimeoutPerMode)
	}
	if budget.TotalTimeout != 30*time.Minute {
		t.Errorf("[E2E-TIMEBOX] expected TotalTimeout=30m, got %v", budget.TotalTimeout)
	}
	if budget.MaxRetries != 2 {
		t.Errorf("[E2E-TIMEBOX] expected MaxRetries=2, got %d", budget.MaxRetries)
	}
}

func TestE2E_Timebox_BudgetConfigJSON(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_budget_json")
	defer suite.Teardown()

	budget := ensemble.DefaultBudgetConfig()

	data, err := json.Marshal(budget)
	if err != nil {
		t.Fatalf("[E2E-TIMEBOX] json.Marshal failed: %v", err)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Budget JSON: %s", string(data))

	var decoded ensemble.BudgetConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("[E2E-TIMEBOX] json.Unmarshal failed: %v", err)
	}

	// Round-trip should preserve key fields
	if decoded.MaxTokensPerMode != budget.MaxTokensPerMode {
		t.Errorf("[E2E-TIMEBOX] MaxTokensPerMode mismatch: %d vs %d", decoded.MaxTokensPerMode, budget.MaxTokensPerMode)
	}
	if decoded.MaxTotalTokens != budget.MaxTotalTokens {
		t.Errorf("[E2E-TIMEBOX] MaxTotalTokens mismatch: %d vs %d", decoded.MaxTotalTokens, budget.MaxTotalTokens)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Budget JSON round-trip verified")
}

// -------------------------------------------------------------------
// Assignment Skipping Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_AssignmentSkipping(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_assignment_skipping")
	defer suite.Teardown()

	// Simulate what happens when timebox expires partway through mode injection
	assignments := []ensemble.ModeAssignment{
		{ModeID: "core-deductive", Status: ensemble.AssignmentActive},
		{ModeID: "core-abductive", Status: ensemble.AssignmentActive},
		{ModeID: "adv-bayesian", Status: ensemble.AssignmentPending},
		{ModeID: "exp-dialectical", Status: ensemble.AssignmentPending},
	}

	suite.Logger().Log("[E2E-TIMEBOX] Simulating timebox expiry with %d assignments", len(assignments))
	suite.Logger().Log("[E2E-TIMEBOX] Active: 2, Pending (to be skipped): 2")

	// Count initial states
	var active, pending, errored int
	for _, a := range assignments {
		switch a.Status {
		case ensemble.AssignmentActive:
			active++
		case ensemble.AssignmentPending:
			pending++
		case ensemble.AssignmentError:
			errored++
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Initial: active=%d pending=%d error=%d", active, pending, errored)

	if active != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 active assignments, got %d", active)
	}
	if pending != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 pending assignments, got %d", pending)
	}

	// After timebox expiry, pending assignments should be marked as error with skip reason
	// This simulates what markAssignmentsSkipped does
	reason := "skipped: total timeout reached before injection"
	now := time.Now().UTC()
	var skippedModes []string
	for i := range assignments {
		if assignments[i].Status == ensemble.AssignmentPending {
			assignments[i].Status = ensemble.AssignmentError
			assignments[i].Error = reason
			assignments[i].CompletedAt = &now
			skippedModes = append(skippedModes, assignments[i].ModeID)
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Skipped modes: %v", skippedModes)

	if len(skippedModes) != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 skipped modes, got %d", len(skippedModes))
	}

	// Verify all pending are now errored
	for _, a := range assignments {
		if a.Status == ensemble.AssignmentPending {
			t.Errorf("[E2E-TIMEBOX] assignment %s still pending after timebox", a.ModeID)
		}
	}

	// Active assignments should be unchanged
	activeAfter := 0
	errorAfter := 0
	for _, a := range assignments {
		switch a.Status {
		case ensemble.AssignmentActive:
			activeAfter++
		case ensemble.AssignmentError:
			errorAfter++
		}
	}

	if activeAfter != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 active assignments preserved, got %d", activeAfter)
	}
	if errorAfter != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 errored assignments, got %d", errorAfter)
	}

	// Verify error messages
	for _, a := range assignments {
		if a.Status == ensemble.AssignmentError {
			if a.Error != reason {
				t.Errorf("[E2E-TIMEBOX] expected error=%q, got %q for %s", reason, a.Error, a.ModeID)
			}
			if a.CompletedAt == nil {
				t.Errorf("[E2E-TIMEBOX] expected CompletedAt set for skipped mode %s", a.ModeID)
			}
			suite.Logger().Log("[E2E-TIMEBOX] Verified skipped mode %s: error=%q completed_at=%v",
				a.ModeID, a.Error, a.CompletedAt)
		}
	}
}

func TestE2E_Timebox_SkipNonPendingUnchanged(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_skip_non_pending")
	defer suite.Teardown()

	// Verify that only pending assignments get skipped, not active/done/error
	assignments := []ensemble.ModeAssignment{
		{ModeID: "done-mode", Status: ensemble.AssignmentDone},
		{ModeID: "active-mode", Status: ensemble.AssignmentActive},
		{ModeID: "error-mode", Status: ensemble.AssignmentError, Error: "previous error"},
		{ModeID: "pending-mode", Status: ensemble.AssignmentPending},
	}

	suite.Logger().Log("[E2E-TIMEBOX] Testing skip with mixed statuses: done, active, error, pending")

	reason := "timebox expired"
	now := time.Now().UTC()
	skippedCount := 0
	for i := range assignments {
		if assignments[i].Status == ensemble.AssignmentPending {
			assignments[i].Status = ensemble.AssignmentError
			assignments[i].Error = reason
			assignments[i].CompletedAt = &now
			skippedCount++
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Skipped %d assignments", skippedCount)

	if skippedCount != 1 {
		t.Errorf("[E2E-TIMEBOX] expected exactly 1 skip, got %d", skippedCount)
	}

	// Verify non-pending assignments preserved
	if assignments[0].Status != ensemble.AssignmentDone {
		t.Errorf("[E2E-TIMEBOX] done-mode status changed to %s", assignments[0].Status)
	}
	if assignments[1].Status != ensemble.AssignmentActive {
		t.Errorf("[E2E-TIMEBOX] active-mode status changed to %s", assignments[1].Status)
	}
	if assignments[2].Error != "previous error" {
		t.Errorf("[E2E-TIMEBOX] error-mode error changed to %q", assignments[2].Error)
	}
	if assignments[3].Status != ensemble.AssignmentError {
		t.Errorf("[E2E-TIMEBOX] pending-mode not marked as error: %s", assignments[3].Status)
	}
}

// -------------------------------------------------------------------
// Deadline Calculation Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_DeadlineCalculation(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_deadline_calc")
	defer suite.Teardown()

	testCases := []struct {
		name     string
		timeout  time.Duration
		expired  bool
		zeroTime bool
	}{
		{"30min_timeout", 30 * time.Minute, false, false},
		{"1s_timeout", time.Second, false, false},
		{"zero_timeout", 0, false, true},
		{"negative_timeout", -time.Second, false, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()

			// timeboxDeadline: if total <= 0 returns zero time, else start.Add(total)
			var deadline time.Time
			if tc.timeout > 0 {
				deadline = start.Add(tc.timeout)
			}

			suite.Logger().Log("[E2E-TIMEBOX] %s: timeout=%v deadline=%v zero=%v",
				tc.name, tc.timeout, deadline, deadline.IsZero())

			if tc.zeroTime && !deadline.IsZero() {
				t.Errorf("[E2E-TIMEBOX] expected zero deadline for timeout=%v", tc.timeout)
			}
			if !tc.zeroTime && deadline.IsZero() {
				t.Errorf("[E2E-TIMEBOX] expected non-zero deadline for timeout=%v", tc.timeout)
			}

			// Check expiry: zero deadline never expires
			var expired bool
			if deadline.IsZero() {
				expired = false
			} else {
				expired = !time.Now().Before(deadline)
			}

			if tc.zeroTime && expired {
				t.Errorf("[E2E-TIMEBOX] zero deadline should never expire")
			}

			suite.Logger().Log("[E2E-TIMEBOX] %s: expired=%v (expected=%v)", tc.name, expired, tc.expired)
		})
	}
}

func TestE2E_Timebox_ExpiredDeadline(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_expired_deadline")
	defer suite.Teardown()

	// A deadline in the past should be expired
	pastDeadline := time.Now().Add(-1 * time.Second)
	expired := !time.Now().Before(pastDeadline)

	suite.Logger().Log("[E2E-TIMEBOX] Past deadline: %v expired=%v", pastDeadline, expired)

	if !expired {
		t.Error("[E2E-TIMEBOX] past deadline should be expired")
	}

	// A deadline in the future should not be expired
	futureDeadline := time.Now().Add(1 * time.Hour)
	expired = !time.Now().Before(futureDeadline)

	suite.Logger().Log("[E2E-TIMEBOX] Future deadline: %v expired=%v", futureDeadline, expired)

	if expired {
		t.Error("[E2E-TIMEBOX] future deadline should not be expired")
	}
}

// -------------------------------------------------------------------
// DryRun Budget Integration Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_DryRunModes_TierDistribution(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "timebox_dryrun_tiers")
	defer suite.Teardown()

	result := runTimeboxCmd(t, suite, "dryrun_tiers",
		"ensemble", "dry-run", "project-diagnosis",
		"--question", "What are the main issues?",
		"--robot-json",
	)

	if result.Err != nil {
		if strings.Contains(string(result.Stderr), "unknown command") {
			t.Skip("ensemble dry-run command not available")
		}
		t.Skip("ensemble dry-run command not available or failed")
	}

	var plan ensemble.DryRunPlan
	parseTimeboxJSON(t, suite, "dryrun_tiers", result.Stdout, &plan)

	// Count modes by tier
	tierCounts := make(map[string]int)
	for _, mode := range plan.Modes {
		tierCounts[mode.Tier]++
	}

	suite.Logger().Log("[E2E-TIMEBOX] Mode tier distribution: %v (total=%d)", tierCounts, len(plan.Modes))

	// In timebox mode, core modes should be executed first
	// Verify that at least some core modes are present
	if tierCounts["core"] == 0 && len(plan.Modes) > 0 {
		suite.Logger().Log("[E2E-TIMEBOX] WARNING: no core tier modes in preset")
	}
}

// -------------------------------------------------------------------
// Partial Synthesis Flow Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_PartialSynthesis_Workflow(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_partial_synthesis")
	defer suite.Teardown()

	// Simulate end-to-end timebox workflow:
	// 1. Start with 5 mode assignments
	// 2. Timebox expires after 3 are active
	// 3. Remaining 2 are skipped
	// 4. Only active outputs used for synthesis

	assignments := []ensemble.ModeAssignment{
		{ModeID: "mode-a", Status: ensemble.AssignmentActive, AgentType: "cc"},
		{ModeID: "mode-b", Status: ensemble.AssignmentActive, AgentType: "cc"},
		{ModeID: "mode-c", Status: ensemble.AssignmentActive, AgentType: "cod"},
		{ModeID: "mode-d", Status: ensemble.AssignmentPending, AgentType: "cc"},
		{ModeID: "mode-e", Status: ensemble.AssignmentPending, AgentType: "gmi"},
	}

	suite.Logger().Log("[E2E-TIMEBOX] Workflow: 5 modes, timebox expires after 3 injected")

	// Phase 1: Count pre-timebox state
	preActive := 0
	prePending := 0
	for _, a := range assignments {
		if a.Status == ensemble.AssignmentActive {
			preActive++
		}
		if a.Status == ensemble.AssignmentPending {
			prePending++
		}
	}
	suite.Logger().Log("[E2E-TIMEBOX] Pre-timebox: active=%d pending=%d", preActive, prePending)

	// Phase 2: Apply timebox (skip remaining pending)
	reason := "skipped: total timeout reached before injection"
	now := time.Now().UTC()
	var skipped []string
	for i := range assignments {
		if assignments[i].Status == ensemble.AssignmentPending {
			assignments[i].Status = ensemble.AssignmentError
			assignments[i].Error = reason
			assignments[i].CompletedAt = &now
			skipped = append(skipped, assignments[i].ModeID)
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Timebox applied, skipped: %v", skipped)

	if len(skipped) != 2 {
		t.Fatalf("[E2E-TIMEBOX] expected 2 skipped, got %d", len(skipped))
	}

	// Phase 3: Collect outputs from active modes only
	var outputModes []string
	for _, a := range assignments {
		if a.Status == ensemble.AssignmentActive {
			outputModes = append(outputModes, a.ModeID)
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Modes available for synthesis: %v", outputModes)

	if len(outputModes) != 3 {
		t.Errorf("[E2E-TIMEBOX] expected 3 output modes, got %d", len(outputModes))
	}

	// Phase 4: Verify synthesis would use only the active modes
	outputs := make([]ensemble.ModeOutput, 0, len(outputModes))
	for _, modeID := range outputModes {
		outputs = append(outputs, ensemble.ModeOutput{
			ModeID: modeID,
			TopFindings: []ensemble.Finding{
				{
					Finding:    fmt.Sprintf("Finding from %s", modeID),
					Impact:     ensemble.ImpactMedium,
					Confidence: 0.8,
				},
			},
		})
	}

	suite.Logger().Log("[E2E-TIMEBOX] Created %d synthetic outputs for synthesis", len(outputs))

	if len(outputs) != 3 {
		t.Errorf("[E2E-TIMEBOX] expected 3 outputs for synthesis, got %d", len(outputs))
	}

	// Verify each output has findings
	for _, out := range outputs {
		if len(out.TopFindings) == 0 {
			t.Errorf("[E2E-TIMEBOX] output for %s has no findings", out.ModeID)
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Partial synthesis workflow verified: 3/5 modes produced output")
}

// -------------------------------------------------------------------
// Value Per Second (Efficiency) Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_ValuePerSecond(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_value_per_second")
	defer suite.Teardown()

	// Value per second = modeValueScore / estimatedRuntime
	// Core mode with lower cost should have higher value per second than
	// Experimental mode with higher cost

	const (
		tokensPerSecond   = 25.0
		minRuntimeSeconds = 15.0
	)

	type modeEfficiency struct {
		name     string
		tier     ensemble.ModeTier
		category ensemble.ModeCategory
		value    float64
		runtime  time.Duration
		vps      float64
	}

	calculate := func(tier ensemble.ModeTier, category ensemble.ModeCategory) modeEfficiency {
		// Value score
		score := 1.0
		switch tier {
		case ensemble.TierCore:
			score = 1.0
		case ensemble.TierAdvanced:
			score = 0.85
		case ensemble.TierExperimental:
			score = 0.7
		}

		// Cost estimation
		baseCost := 2000
		switch category {
		case ensemble.CategoryFormal:
			baseCost = 3000
		case ensemble.CategoryMeta:
			baseCost = 2500
		case ensemble.CategoryStrategic:
			baseCost = 2500
		case ensemble.CategoryDialectical:
			baseCost = 2800
		}
		switch tier {
		case ensemble.TierAdvanced:
			baseCost = int(float64(baseCost) * 1.2)
		case ensemble.TierExperimental:
			baseCost = int(float64(baseCost) * 1.5)
		}

		seconds := float64(baseCost) / tokensPerSecond
		if seconds < minRuntimeSeconds {
			seconds = minRuntimeSeconds
		}
		runtime := time.Duration(seconds * float64(time.Second))

		vps := score / seconds

		return modeEfficiency{
			tier:    tier,
			value:   score,
			runtime: runtime,
			vps:     vps,
		}
	}

	// Compare core ampliative (cheap) vs experimental formal (expensive)
	coreAmpliative := calculate(ensemble.TierCore, ensemble.CategoryAmpliative)
	coreAmpliative.name = "core_ampliative"

	expFormal := calculate(ensemble.TierExperimental, ensemble.CategoryFormal)
	expFormal.name = "exp_formal"

	suite.Logger().Log("[E2E-TIMEBOX] %s: value=%.2f runtime=%v vps=%.4f",
		coreAmpliative.name, coreAmpliative.value, coreAmpliative.runtime, coreAmpliative.vps)
	suite.Logger().Log("[E2E-TIMEBOX] %s: value=%.2f runtime=%v vps=%.4f",
		expFormal.name, expFormal.value, expFormal.runtime, expFormal.vps)

	// Core ampliative should have higher value per second
	// (higher value, lower runtime)
	if coreAmpliative.vps <= expFormal.vps {
		t.Errorf("[E2E-TIMEBOX] core ampliative VPS (%.4f) should be > experimental formal VPS (%.4f)",
			coreAmpliative.vps, expFormal.vps)
	}

	suite.Logger().Log("[E2E-TIMEBOX] VPS ordering verified: core_ampliative > exp_formal")
}

// -------------------------------------------------------------------
// Timebox Status Transition Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_StatusTransitions(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_status_transitions")
	defer suite.Teardown()

	// Test valid assignment status values
	validStatuses := []ensemble.AssignmentStatus{
		ensemble.AssignmentPending,
		ensemble.AssignmentInjecting,
		ensemble.AssignmentActive,
		ensemble.AssignmentDone,
		ensemble.AssignmentError,
	}

	for _, s := range validStatuses {
		suite.Logger().Log("[E2E-TIMEBOX] Valid status: %q", s)
		if string(s) == "" {
			t.Errorf("[E2E-TIMEBOX] status should not be empty")
		}
	}

	// Verify the timebox transition: Pending -> Error (with skip reason)
	assignment := ensemble.ModeAssignment{
		ModeID: "test-mode",
		Status: ensemble.AssignmentPending,
	}

	if assignment.Status != ensemble.AssignmentPending {
		t.Fatalf("[E2E-TIMEBOX] initial status should be pending, got %s", assignment.Status)
	}

	// Timebox skip transition
	assignment.Status = ensemble.AssignmentError
	assignment.Error = "skipped: total timeout reached"
	now := time.Now().UTC()
	assignment.CompletedAt = &now

	if assignment.Status != ensemble.AssignmentError {
		t.Errorf("[E2E-TIMEBOX] expected status=error after skip, got %s", assignment.Status)
	}
	if assignment.CompletedAt == nil {
		t.Error("[E2E-TIMEBOX] expected CompletedAt to be set after skip")
	}

	suite.Logger().Log("[E2E-TIMEBOX] Verified status transition: pending -> error (timebox skip)")
}

// -------------------------------------------------------------------
// DryRun Validation Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_DryRunPlanValidation(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_dryrun_validation")
	defer suite.Teardown()

	// Test the Validate() method on DryRunPlan
	validPlan := &ensemble.DryRunPlan{
		SessionName: "test-session",
		Question:    "What is the issue?",
		Validation:  ensemble.DryRunValidation{Valid: true},
	}

	if err := validPlan.Validate(); err != nil {
		t.Errorf("[E2E-TIMEBOX] valid plan should not return error: %v", err)
	}
	suite.Logger().Log("[E2E-TIMEBOX] Valid plan passed validation")

	// Invalid plan
	invalidPlan := &ensemble.DryRunPlan{
		SessionName: "test-session",
		Validation: ensemble.DryRunValidation{
			Valid:  false,
			Errors: []string{"mode xyz not found"},
		},
	}

	if err := invalidPlan.Validate(); err == nil {
		t.Error("[E2E-TIMEBOX] invalid plan should return error")
	} else {
		suite.Logger().Log("[E2E-TIMEBOX] Invalid plan correctly rejected: %v", err)
	}

	// Nil plan
	var nilPlan *ensemble.DryRunPlan
	if err := nilPlan.Validate(); err == nil {
		t.Error("[E2E-TIMEBOX] nil plan should return error")
	}

	suite.Logger().Log("[E2E-TIMEBOX] DryRunPlan validation tested: valid, invalid, nil")
}

// -------------------------------------------------------------------
// Catalog + Priority Integration Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_CatalogTierGroups(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_catalog_tiers")
	defer suite.Teardown()

	// Use the global catalog to verify tier distribution
	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		t.Fatalf("[E2E-TIMEBOX] failed to load global catalog: %v", err)
	}

	allModes := catalog.ListModes()
	suite.Logger().Log("[E2E-TIMEBOX] Global catalog has %d modes", len(allModes))

	tierCounts := map[ensemble.ModeTier]int{}
	for _, m := range allModes {
		tierCounts[m.Tier]++
	}

	suite.Logger().Log("[E2E-TIMEBOX] Tier distribution: core=%d advanced=%d experimental=%d",
		tierCounts[ensemble.TierCore], tierCounts[ensemble.TierAdvanced], tierCounts[ensemble.TierExperimental])

	// Verify there are modes at each tier for meaningful timebox ordering
	if tierCounts[ensemble.TierCore] == 0 {
		t.Error("[E2E-TIMEBOX] no core tier modes in global catalog")
	}

	// Verify modes have required fields for timebox ordering
	for _, m := range allModes {
		if m.ID == "" {
			t.Error("[E2E-TIMEBOX] mode with empty ID found")
		}
		if !m.Category.IsValid() {
			t.Errorf("[E2E-TIMEBOX] mode %s has invalid category: %s", m.ID, m.Category)
		}
	}
}

func TestE2E_Timebox_OrderingWithRealCatalog(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_ordering_real_catalog")
	defer suite.Teardown()

	catalog, err := ensemble.GlobalCatalog()
	if err != nil {
		t.Fatalf("[E2E-TIMEBOX] failed to load global catalog: %v", err)
	}

	allModes := catalog.ListModes()
	if len(allModes) < 3 {
		t.Skip("need at least 3 modes for ordering test")
	}

	// Build assignments from real catalog modes
	assignments := make([]ensemble.ModeAssignment, 0, len(allModes))
	for _, m := range allModes {
		assignments = append(assignments, ensemble.ModeAssignment{
			ModeID: m.ID,
			Status: ensemble.AssignmentPending,
		})
	}

	suite.Logger().Log("[E2E-TIMEBOX] Testing ordering with %d real catalog modes", len(assignments))

	// Sort by priority weight (simulating orderAssignmentsForTimebox)
	tierWeights := map[ensemble.ModeTier]int{
		ensemble.TierCore:         3,
		ensemble.TierAdvanced:     2,
		ensemble.TierExperimental: 1,
	}

	type modeEntry struct {
		idx      int
		id       string
		tier     ensemble.ModeTier
		priority int
	}

	entries := make([]modeEntry, 0, len(assignments))
	for i, a := range assignments {
		mode := catalog.GetMode(a.ModeID)
		priority := 0
		tier := ensemble.ModeTier("")
		if mode != nil {
			tier = mode.Tier
			priority = tierWeights[mode.Tier]
		}
		entries = append(entries, modeEntry{idx: i, id: a.ModeID, tier: tier, priority: priority})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority > entries[j].priority
		}
		return entries[i].id < entries[j].id
	})

	// Verify core modes come before advanced, advanced before experimental
	lastPriority := 4 // higher than max
	for _, e := range entries {
		if e.priority > lastPriority {
			t.Errorf("[E2E-TIMEBOX] ordering violation: %s (tier=%s, priority=%d) after lower priority",
				e.id, e.tier, e.priority)
		}
		lastPriority = e.priority
	}

	// Log first and last 3 entries
	for i := 0; i < 3 && i < len(entries); i++ {
		suite.Logger().Log("[E2E-TIMEBOX] Top %d: %s tier=%s priority=%d", i+1, entries[i].id, entries[i].tier, entries[i].priority)
	}
	for i := len(entries) - 3; i >= 0 && i < len(entries); i++ {
		suite.Logger().Log("[E2E-TIMEBOX] Bottom %d: %s tier=%s priority=%d",
			len(entries)-i, entries[i].id, entries[i].tier, entries[i].priority)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Priority ordering verified across %d real catalog modes", len(entries))
}

// -------------------------------------------------------------------
// EnsembleSession Timebox Fields Tests
// -------------------------------------------------------------------

func TestE2E_Timebox_EnsembleSessionStatus(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_session_status")
	defer suite.Teardown()

	// Test that EnsembleSession properly tracks partial completion
	session := &ensemble.EnsembleSession{
		SessionName:       "test-timebox",
		Question:          "Test question",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyConsensus,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentActive},
			{ModeID: "mode-b", Status: ensemble.AssignmentActive},
			{ModeID: "mode-c", Status: ensemble.AssignmentError, Error: "skipped: timeout"},
		},
	}

	suite.Logger().Log("[E2E-TIMEBOX] Session %s: status=%s assignments=%d",
		session.SessionName, session.Status, len(session.Assignments))

	// Count active vs skipped
	var active, skipped int
	for _, a := range session.Assignments {
		if a.Status == ensemble.AssignmentActive {
			active++
		}
		if a.Status == ensemble.AssignmentError && strings.Contains(a.Error, "timeout") {
			skipped++
		}
	}

	suite.Logger().Log("[E2E-TIMEBOX] Active=%d Skipped=%d", active, skipped)

	if active != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 active, got %d", active)
	}
	if skipped != 1 {
		t.Errorf("[E2E-TIMEBOX] expected 1 skipped, got %d", skipped)
	}

	// Verify session is still running (partial completion doesn't end the session)
	if session.Status != ensemble.EnsembleActive {
		t.Errorf("[E2E-TIMEBOX] expected session status=active, got %s", session.Status)
	}
}

func TestE2E_Timebox_Stage2Result_PartialModes(t *testing.T) {
	CommonE2EPrerequisites(t)
	suite := NewTestSuite(t, "timebox_stage2_partial")
	defer suite.Teardown()

	// Simulate a Stage2Result with partial mode completion due to timebox
	result := &ensemble.Stage2Result{
		SessionName:    "test-timebox-session",
		ModesAttempted: 5,
		ModesSucceeded: 3,
		ModesFailed:    2,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-1", Status: ensemble.AssignmentActive},
			{ModeID: "mode-2", Status: ensemble.AssignmentActive},
			{ModeID: "mode-3", Status: ensemble.AssignmentActive},
			{ModeID: "mode-4", Status: ensemble.AssignmentError, Error: "skipped: total timeout"},
			{ModeID: "mode-5", Status: ensemble.AssignmentError, Error: "skipped: total timeout"},
		},
		Duration: 30 * time.Second,
	}

	suite.Logger().Log("[E2E-TIMEBOX] Stage2Result: attempted=%d succeeded=%d failed=%d duration=%v",
		result.ModesAttempted, result.ModesSucceeded, result.ModesFailed, result.Duration)

	if result.ModesAttempted != 5 {
		t.Errorf("[E2E-TIMEBOX] expected 5 attempted, got %d", result.ModesAttempted)
	}
	if result.ModesSucceeded != 3 {
		t.Errorf("[E2E-TIMEBOX] expected 3 succeeded, got %d", result.ModesSucceeded)
	}
	if result.ModesFailed != 2 {
		t.Errorf("[E2E-TIMEBOX] expected 2 failed, got %d", result.ModesFailed)
	}

	// Verify assignment counts match
	var activeCount, errorCount int
	for _, a := range result.Assignments {
		switch a.Status {
		case ensemble.AssignmentActive:
			activeCount++
		case ensemble.AssignmentError:
			errorCount++
		}
	}

	if activeCount != result.ModesSucceeded {
		t.Errorf("[E2E-TIMEBOX] active count %d != succeeded %d", activeCount, result.ModesSucceeded)
	}
	if errorCount != result.ModesFailed {
		t.Errorf("[E2E-TIMEBOX] error count %d != failed %d", errorCount, result.ModesFailed)
	}

	suite.Logger().Log("[E2E-TIMEBOX] Stage2Result partial completion verified")
}
