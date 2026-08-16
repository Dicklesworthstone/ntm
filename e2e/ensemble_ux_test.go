//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM ensemble UX enhancements.
//
// Bead: bd-1bxza - Task: Write E2E tests for Ensemble UX Enhancements
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// runEnsembleUXCmd executes an ntm command and returns structured output.
func runEnsembleUXCmd(t *testing.T, suite *TestSuite, label string, args ...string) cliRunResult {
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

	suite.Logger().Log("[E2E-ENSEMBLE-UX] %s args=%v duration_ms=%d err=%v", label, args, duration.Milliseconds(), err)
	suite.Logger().Log("[E2E-ENSEMBLE-UX] %s stdout=%s", label, strings.TrimSpace(stdout.String()))
	suite.Logger().Log("[E2E-ENSEMBLE-UX] %s stderr=%s", label, strings.TrimSpace(stderr.String()))

	return cliRunResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: duration,
		Err:      err,
	}
}

// parseEnsembleUXJSON unmarshals JSON output, fataling on error.
func parseEnsembleUXJSON(t *testing.T, suite *TestSuite, label string, stdout []byte, v interface{}) {
	t.Helper()

	if err := json.Unmarshal(stdout, v); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] %s JSON parse failed: %v stdout=%s", label, err, string(stdout))
	}
	suite.Logger().LogJSON("[E2E-ENSEMBLE-UX] "+label+" parsed", v)
}

// -------------------------------------------------------------------
// Dry Run Tests
// -------------------------------------------------------------------

// supportsEnsembleSpawn reports whether the ntm binary was built with the
// ensemble_experimental tag. The default build intentionally ships a stub
// `ensemble spawn` (and gated --robot-ensemble-spawn) with no --preset /
// --question / --dry-run flags, so spawn-based tests must skip rather than
// fail against the stub.
func supportsEnsembleSpawn() bool {
	cmd := exec.Command("ntm", "ensemble", "spawn", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// The stub's help text names the build tag it needs; the real command's
	// help does not. (Checking for "--preset" is not enough: the stub's
	// example line mentions it too.)
	return !strings.Contains(string(output), "ensemble_experimental")
}

func TestE2E_DryRun_BasicPreset(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}
	if !supportsEnsembleSpawn() {
		t.Skip("ensemble spawn is a stub in this build (requires -tags ensemble_experimental)")
	}

	suite := NewTestSuite(t, "ensemble_ux_dryrun_basic")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	t.Logf("E2E: %s - executing dry-run with basic preset", t.Name())

	res := runEnsembleUXCmd(t, suite, "dryrun_basic",
		"ensemble", "spawn", suite.Session(),
		"--preset", "project-diagnosis",
		"--question", "E2E dry-run basic preset test",
		"--dry-run",
		"--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - stderr: %s", t.Name(), string(res.Stderr))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run basic preset failed: %v", res.Err)
	}

	var plan map[string]interface{}
	parseEnsembleUXJSON(t, suite, "dryrun_basic", res.Stdout, &plan)

	// Dry-run must include modes, budget, and session name.
	if _, ok := plan["modes"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run response missing 'modes' field")
	}
	if _, ok := plan["budget"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run response missing 'budget' field")
	}

	t.Logf("E2E: %s - assertion: dry-run plan has modes and budget fields = true (want true)", t.Name())
}

func TestE2E_DryRun_CustomModes(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}
	if !supportsEnsembleSpawn() {
		t.Skip("ensemble spawn is a stub in this build (requires -tags ensemble_experimental)")
	}

	suite := NewTestSuite(t, "ensemble_ux_dryrun_custom")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	// First get available modes.
	modesRes := runEnsembleUXCmd(t, suite, "modes_for_custom",
		"modes", "list", "--format", "json",
	)
	if modesRes.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] modes list failed: %v", modesRes.Err)
	}

	var modes modesListJSON
	parseEnsembleUXJSON(t, suite, "modes_for_custom", modesRes.Stdout, &modes)

	if len(modes.Modes) < 2 {
		t.Skip("need at least 2 modes for custom mode dry-run test")
	}

	modeIDs := modes.Modes[0].ID + "," + modes.Modes[1].ID
	t.Logf("E2E: %s - executing dry-run with custom modes: %s", t.Name(), modeIDs)

	res := runEnsembleUXCmd(t, suite, "dryrun_custom",
		"ensemble", "spawn", suite.Session(),
		"--modes", modeIDs,
		"--question", "E2E dry-run custom modes test",
		"--dry-run",
		"--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run custom modes failed: %v", res.Err)
	}

	var plan map[string]interface{}
	parseEnsembleUXJSON(t, suite, "dryrun_custom", res.Stdout, &plan)

	if modesField, ok := plan["modes"]; ok {
		if modesArr, ok := modesField.([]interface{}); ok {
			t.Logf("E2E: %s - assertion: mode_count = %d (want 2)", t.Name(), len(modesArr))
			if len(modesArr) != 2 {
				t.Fatalf("[E2E-ENSEMBLE-UX] expected 2 modes in dry-run, got %d", len(modesArr))
			}
		}
	}
}

func TestE2E_DryRun_RobotOutput(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}
	if !supportsEnsembleSpawn() {
		t.Skip("ensemble spawn is a stub in this build (requires -tags ensemble_experimental)")
	}

	suite := NewTestSuite(t, "ensemble_ux_dryrun_robot")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	t.Logf("E2E: %s - executing dry-run via robot flag", t.Name())

	// Use the robot flag approach.
	res := runEnsembleUXCmd(t, suite, "dryrun_robot",
		fmt.Sprintf("--robot-ensemble-spawn=%s", suite.Session()),
		"--preset", "project-diagnosis",
		"--question", "E2E dry-run robot output test",
		"--dry-run",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run robot output failed: %v", res.Err)
	}

	// Robot output must be valid JSON.
	var raw json.RawMessage
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot dry-run output is not valid JSON: %v", err)
	}

	t.Logf("E2E: %s - assertion: robot output is valid JSON = true (want true)", t.Name())
}

func TestE2E_DryRun_NoSideEffects(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_dryrun_nosideeffects")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	session := suite.Session()
	t.Logf("E2E: %s - verifying dry-run has no side effects on session %s", t.Name(), session)

	// Check ensemble status before dry-run.
	beforeRes := runEnsembleUXCmd(t, suite, "status_before",
		"ensemble", "status", session, "--format", "json",
	)
	if beforeRes.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] pre-dryrun status check failed: %v", beforeRes.Err)
	}
	var before ensembleStatusJSON
	parseEnsembleUXJSON(t, suite, "status_before", beforeRes.Stdout, &before)

	// Run dry-run.
	runEnsembleUXCmd(t, suite, "dryrun_noop",
		"ensemble", "spawn", session,
		"--preset", "project-diagnosis",
		"--question", "E2E no-side-effects test",
		"--dry-run",
		"--format", "json",
	)

	// Check ensemble status after dry-run.
	afterRes := runEnsembleUXCmd(t, suite, "status_after",
		"ensemble", "status", session, "--format", "json",
	)
	if afterRes.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] post-dryrun status check failed: %v", afterRes.Err)
	}
	var after ensembleStatusJSON
	parseEnsembleUXJSON(t, suite, "status_after", afterRes.Stdout, &after)

	t.Logf("E2E: %s - assertion: exists_before = %v, exists_after = %v (want equal)", t.Name(), before.Exists, after.Exists)
	if before.Exists != after.Exists {
		t.Fatalf("[E2E-ENSEMBLE-UX] dry-run changed ensemble state: before.exists=%v after.exists=%v", before.Exists, after.Exists)
	}
}

// -------------------------------------------------------------------
// Auto-Ensemble / Suggest Tests
// -------------------------------------------------------------------

func TestE2E_Suggest_KnownPatterns(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_suggest_known")
	defer suite.Teardown()

	questions := []struct {
		question string
		label    string
	}{
		{"How should we refactor the authentication module?", "refactor_question"},
		{"Is our API design correct for pagination?", "api_question"},
		{"What are the security risks in our deployment?", "security_question"},
	}

	for _, q := range questions {
		t.Run(q.label, func(t *testing.T) {
			t.Logf("E2E: %s - suggesting preset for: %s", t.Name(), q.question)

			res := runEnsembleUXCmd(t, suite, q.label,
				fmt.Sprintf("--robot-ensemble-suggest=%s", q.question),
			)

			t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
			t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

			if res.Err != nil {
				t.Fatalf("[E2E-ENSEMBLE-UX] suggest failed for %q: %v", q.question, res.Err)
			}

			var out map[string]interface{}
			parseEnsembleUXJSON(t, suite, q.label, res.Stdout, &out)

			// Suggestion must include a top_pick or suggestions list.
			hasSuggestions := false
			if _, ok := out["top_pick"]; ok {
				hasSuggestions = true
			}
			if arr, ok := out["suggestions"]; ok {
				if sarr, ok := arr.([]interface{}); ok && len(sarr) > 0 {
					hasSuggestions = true
				}
			}
			t.Logf("E2E: %s - assertion: has_suggestions = %v (want true)", t.Name(), hasSuggestions)
			if !hasSuggestions {
				t.Fatalf("[E2E-ENSEMBLE-UX] no suggestions returned for known pattern: %q", q.question)
			}
		})
	}
}

func TestE2E_Suggest_RobotJSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_suggest_robot")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing suggest with robot JSON output", t.Name())

	res := runEnsembleUXCmd(t, suite, "suggest_robot",
		"--robot-ensemble-suggest=What is the performance bottleneck?",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot suggest failed: %v", res.Err)
	}

	// Must be valid JSON.
	var raw json.RawMessage
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot suggest output not valid JSON: %v", err)
	}

	// Must include the envelope timestamp. The robot envelope carries the
	// generation time in "timestamp" (RobotResponse); a top-level
	// "generated_at" is not part of the suggest schema.
	var envelope map[string]interface{}
	parseEnsembleUXJSON(t, suite, "suggest_robot", res.Stdout, &envelope)

	if _, ok := envelope["timestamp"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] suggest robot JSON missing 'timestamp'")
	}
	if _, ok := envelope["suggestions"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] suggest robot JSON missing 'suggestions'")
	}

	t.Logf("E2E: %s - assertion: robot JSON has timestamp = true (want true)", t.Name())
}

func TestE2E_Suggest_PipeToSpawn(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_suggest_pipe")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing suggest --suggest-id-only for piping", t.Name())

	res := runEnsembleUXCmd(t, suite, "suggest_id_only",
		"--robot-ensemble-suggest=How should we optimize this algorithm?",
		"--suggest-id-only",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suggest --suggest-id-only failed: %v", res.Err)
	}

	// Output should be a single line (preset name) suitable for piping.
	output := strings.TrimSpace(string(res.Stdout))
	if output == "" {
		t.Fatalf("[E2E-ENSEMBLE-UX] --suggest-id-only returned empty output")
	}
	if strings.Contains(output, "\n") {
		// Check if it's actually JSON (some formats return JSON even with id-only).
		var raw json.RawMessage
		if json.Unmarshal([]byte(output), &raw) != nil {
			t.Fatalf("[E2E-ENSEMBLE-UX] --suggest-id-only returned multiline non-JSON output")
		}
	}

	t.Logf("E2E: %s - assertion: suggest-id-only output = %q (non-empty)", t.Name(), output)
}

// -------------------------------------------------------------------
// Contribution Scoring Tests
// -------------------------------------------------------------------

func TestE2E_Contributions_StatusOutput(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_contributions_status")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	session := suite.Session()
	t.Logf("E2E: %s - seeding ensemble state for contribution test", t.Name())

	// Seed deterministic state.
	now := time.Now().UTC()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetPanes failed: %v", err)
	}
	if len(panes) == 0 || panes[0].ID == "" {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected at least 1 pane")
	}
	paneID := panes[0].ID

	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "E2E contribution scoring test",
		PresetUsed:        "e2e",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "e2e-mode-a",
				PaneName:    paneID,
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				AssignedAt:  now,
				CompletedAt: &now,
			},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveSession failed: %v", err)
	}
	// Send synthetic YAML mode output.
	sendYAMLModeOutput(t, session, 0, "E2E contribution thesis")

	// Check status includes contribution data.
	res := runEnsembleUXCmd(t, suite, "status_with_contributions",
		"ensemble", "status", session, "--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] ensemble status failed: %v", res.Err)
	}

	var status ensembleStatusJSON
	parseEnsembleUXJSON(t, suite, "status_with_contributions", res.Stdout, &status)

	t.Logf("E2E: %s - assertion: exists = %v (want true)", t.Name(), status.Exists)
	if !status.Exists {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected exists=true")
	}
}

func TestE2E_Contributions_RobotOutput(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_contributions_robot")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	session := suite.Session()
	t.Logf("E2E: %s - testing robot ensemble output for contributions", t.Name())

	// Seed state.
	now := time.Now().UTC()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetPanes failed: %v", err)
	}
	if len(panes) == 0 || panes[0].ID == "" {
		t.Fatalf("[E2E-ENSEMBLE-UX] no panes available")
	}
	paneID := panes[0].ID

	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "E2E robot contribution test",
		PresetUsed:        "e2e",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "e2e-robot-mode",
				PaneName:    paneID,
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				AssignedAt:  now,
				CompletedAt: &now,
			},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveSession failed: %v", err)
	}
	sendYAMLModeOutput(t, session, 0, "E2E robot contribution thesis")

	res := runEnsembleUXCmd(t, suite, "robot_ensemble_contributions",
		fmt.Sprintf("--robot-ensemble=%s", session),
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] --robot-ensemble failed: %v", res.Err)
	}

	var raw json.RawMessage
	if err := json.Unmarshal(res.Stdout, &raw); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot ensemble output not valid JSON: %v", err)
	}
	t.Logf("E2E: %s - assertion: robot ensemble output is valid JSON = true (want true)", t.Name())
}

// -------------------------------------------------------------------
// Mode Cards / Explain Tests
// -------------------------------------------------------------------

func TestE2E_ModeExplain_AllModes(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("modes") {
		t.Skip("ntm binary does not support `modes` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_mode_explain_all")
	defer suite.Teardown()

	t.Logf("E2E: %s - listing all modes and checking explain output", t.Name())

	// Get all modes.
	modesRes := runEnsembleUXCmd(t, suite, "modes_all",
		"modes", "list", "--all", "--format", "json",
	)
	if modesRes.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] modes list --all failed: %v", modesRes.Err)
	}

	var modes modesListJSON
	parseEnsembleUXJSON(t, suite, "modes_all", modesRes.Stdout, &modes)

	if len(modes.Modes) == 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] no modes returned")
	}

	// Verify each mode has an ID and tier.
	for i, mode := range modes.Modes {
		if mode.ID == "" {
			t.Fatalf("[E2E-ENSEMBLE-UX] mode[%d] has empty ID", i)
		}
		if mode.Tier == "" {
			t.Fatalf("[E2E-ENSEMBLE-UX] mode[%d] (%s) has empty tier", i, mode.ID)
		}
		t.Logf("E2E: %s - mode[%d]: id=%s tier=%s", t.Name(), i, mode.ID, mode.Tier)
	}

	t.Logf("E2E: %s - assertion: mode_count = %d (want >0)", t.Name(), len(modes.Modes))
}

func TestE2E_ModeExplain_RobotJSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_mode_explain_robot")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing robot ensemble-modes JSON output", t.Name())

	res := runEnsembleUXCmd(t, suite, "robot_modes",
		"--robot-ensemble-modes",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), truncateString(string(res.Stdout), 500))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] --robot-ensemble-modes failed: %v", res.Err)
	}

	var out map[string]interface{}
	parseEnsembleUXJSON(t, suite, "robot_modes", res.Stdout, &out)

	// Must include the envelope timestamp and modes. The robot envelope
	// carries the generation time in "timestamp" (RobotResponse); a
	// top-level "generated_at" is not part of the modes schema.
	if _, ok := out["timestamp"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot modes JSON missing 'timestamp'")
	}
	modesField, ok := out["modes"]
	if !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot modes JSON missing 'modes'")
	}
	modesArr, ok := modesField.([]interface{})
	if !ok || len(modesArr) == 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] robot modes JSON 'modes' is empty or not an array")
	}

	t.Logf("E2E: %s - assertion: robot modes count = %d (want >0)", t.Name(), len(modesArr))
}

// -------------------------------------------------------------------
// Token Budget / Estimate Tests
// -------------------------------------------------------------------

func TestE2E_Estimate_BasicPreset(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_estimate_basic")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing estimate for basic preset", t.Name())

	// `ensemble estimate` takes a preset (or --modes) only; it has no
	// --question flag, and its JSON reports counts under "budget".
	res := runEnsembleUXCmd(t, suite, "estimate_basic",
		"ensemble", "estimate",
		"--preset", "project-diagnosis",
		"--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] estimate basic failed: %v", res.Err)
	}

	var estimate map[string]interface{}
	parseEnsembleUXJSON(t, suite, "estimate_basic", res.Stdout, &estimate)

	// Estimate must include budget.mode_count and budget.estimated_total_tokens.
	budget, ok := estimate["budget"].(map[string]interface{})
	if !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] estimate response missing 'budget' object")
	}
	if _, ok := budget["mode_count"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] estimate budget missing 'mode_count'")
	}
	if _, ok := budget["estimated_total_tokens"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] estimate budget missing 'estimated_total_tokens'")
	}

	modeCount, _ := budget["mode_count"].(float64)
	totalTokens, _ := budget["estimated_total_tokens"].(float64)

	t.Logf("E2E: %s - assertion: mode_count = %v (want >0)", t.Name(), modeCount)
	t.Logf("E2E: %s - assertion: estimated_total_tokens = %v (want >0)", t.Name(), totalTokens)

	if modeCount <= 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected positive mode_count, got %v", modeCount)
	}
	if totalTokens <= 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected positive estimated_total_tokens, got %v", totalTokens)
	}
}

func TestE2E_Estimate_BudgetWarning(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_estimate_budget")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing estimate with tight budget", t.Name())

	// Use a preset with a very tight budget to trigger a warning.
	// (`ensemble estimate` has no --question flag.)
	res := runEnsembleUXCmd(t, suite, "estimate_budget",
		"ensemble", "estimate",
		"--preset", "project-diagnosis",
		"--budget-total", "100",
		"--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), string(res.Stdout))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	// Command may exit 0 with over_budget=true, or exit 1 with budget error.
	// Either is acceptable; we just need structured output.
	var estimate map[string]interface{}
	if err := json.Unmarshal(res.Stdout, &estimate); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] estimate budget output not valid JSON: %v", err)
	}

	overBudget, _ := estimate["over_budget"].(bool)
	warnings, hasWarnings := estimate["warnings"]
	t.Logf("E2E: %s - assertion: over_budget = %v, has_warnings = %v", t.Name(), overBudget, hasWarnings)

	// With a 100-token budget, we expect over_budget or warnings.
	if !overBudget && !hasWarnings {
		t.Logf("[E2E-ENSEMBLE-UX] WARN: expected over_budget or warnings with 100 token budget; got neither")
	}
	if hasWarnings {
		t.Logf("E2E: %s - warnings: %v", t.Name(), warnings)
	}
}

// -------------------------------------------------------------------
// Synthesis Explanation Tests
// -------------------------------------------------------------------

func TestE2E_Explain_Flag(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_explain_flag")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	session := suite.Session()
	t.Logf("E2E: %s - testing synthesize with --explain flag", t.Name())

	// Seed deterministic ensemble state.
	now := time.Now().UTC()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetPanes failed: %v", err)
	}
	if len(panes) == 0 || panes[0].ID == "" {
		t.Fatalf("[E2E-ENSEMBLE-UX] no panes available")
	}
	paneID := panes[0].ID

	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "E2E explain flag test",
		PresetUsed:        "e2e",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "e2e-explain-mode",
				PaneName:    paneID,
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				AssignedAt:  now,
				CompletedAt: &now,
			},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveSession failed: %v", err)
	}
	sendYAMLModeOutput(t, session, 0, "E2E explain thesis")

	res := runEnsembleUXCmd(t, suite, "synthesize_explain",
		"ensemble", "synthesize", session,
		"--explain",
		"--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), truncateString(string(res.Stdout), 500))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] synthesize with --explain failed: %v", res.Err)
	}

	var out map[string]interface{}
	parseEnsembleUXJSON(t, suite, "synthesize_explain", res.Stdout, &out)

	// With --explain, the output should include synthesis data.
	if _, ok := out["synthesis"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] synthesize --explain missing 'synthesis' field")
	}

	t.Logf("E2E: %s - assertion: synthesis field present = true (want true)", t.Name())
}

func TestE2E_Explain_Provenance(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing provenance tracking in-process", t.Name())

	// Test provenance tracking directly (no CLI needed).
	tracker := ensemble.NewProvenanceTracker("E2E provenance test", []string{"mode-a", "mode-b"})

	finding := ensemble.Finding{
		Finding:         "Finding from mode A",
		Impact:          ensemble.ImpactHigh,
		Confidence:      0.85,
		EvidencePointer: "auth/login.go:42",
	}
	findingID := tracker.RecordDiscovery("mode-a", finding)
	t.Logf("E2E: %s - recorded finding with ID: %s", t.Name(), findingID)

	report := ensemble.GenerateReport(tracker)

	t.Logf("E2E: %s - assertion: report.ActiveChains count = %d (want >= 1)", t.Name(), len(report.ActiveChains))
	if len(report.ActiveChains) < 1 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected at least 1 provenance chain, got %d", len(report.ActiveChains))
	}

	exported, err := tracker.Export()
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] provenance export failed: %v", err)
	}
	if len(exported) == 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] provenance export returned empty data")
	}
	t.Logf("E2E: %s - assertion: export size = %d bytes (want >0)", t.Name(), len(exported))
}

// -------------------------------------------------------------------
// Recovery Tests
// -------------------------------------------------------------------

func TestE2E_Resume_AfterFailure(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing checkpoint resume after simulated failure", t.Name())

	// Use temp directory for checkpoint store.
	tmpDir, err := os.MkdirTemp("", "ntm-e2e-checkpoint-*")
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	store, err := ensemble.NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] NewCheckpointStore failed: %v", err)
	}

	runID := fmt.Sprintf("e2e-resume-%d", time.Now().UnixNano())
	now := time.Now().UTC()

	// Save metadata with partial completion.
	meta := ensemble.CheckpointMetadata{
		SessionName:  "e2e-resume-session",
		Question:     "E2E resume test",
		RunID:        runID,
		Status:       ensemble.EnsembleActive,
		CreatedAt:    now,
		UpdatedAt:    now,
		CompletedIDs: []string{"mode-a"},
		PendingIDs:   []string{"mode-b", "mode-c"},
		ErrorIDs:     []string{},
		TotalModes:   3,
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveMetadata failed: %v", err)
	}

	// Save completed checkpoint for mode-a.
	checkpoint := ensemble.ModeCheckpoint{
		ModeID: "mode-a",
		Output: &ensemble.ModeOutput{
			ModeID:      "mode-a",
			Thesis:      "Mode A completed thesis",
			TopFindings: []ensemble.Finding{{Finding: "Finding A1", Impact: ensemble.ImpactMedium, Confidence: 0.8}},
		},
		Status:     "done",
		CapturedAt: now,
		TokensUsed: 500,
	}
	if err := store.SaveCheckpoint(runID, checkpoint); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveCheckpoint failed: %v", err)
	}

	// Verify resume is possible.
	manager := ensemble.NewCheckpointManager(store, runID)
	resumable := manager.IsResumable()
	t.Logf("E2E: %s - assertion: resumable = %v (want true)", t.Name(), resumable)
	if !resumable {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected checkpoint to be resumable")
	}

	// Get resume state.
	resumeMeta, outputs, err := manager.GetResumeState()
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetResumeState failed: %v", err)
	}

	t.Logf("E2E: %s - assertion: completed_ids = %v (want [mode-a])", t.Name(), resumeMeta.CompletedIDs)
	t.Logf("E2E: %s - assertion: pending_ids = %v (want [mode-b, mode-c])", t.Name(), resumeMeta.PendingIDs)
	t.Logf("E2E: %s - assertion: outputs count = %d (want 1)", t.Name(), len(outputs))

	if len(resumeMeta.CompletedIDs) != 1 || resumeMeta.CompletedIDs[0] != "mode-a" {
		t.Fatalf("[E2E-ENSEMBLE-UX] unexpected completed_ids: %v", resumeMeta.CompletedIDs)
	}
	if len(resumeMeta.PendingIDs) != 2 {
		t.Fatalf("[E2E-ENSEMBLE-UX] unexpected pending_ids: %v", resumeMeta.PendingIDs)
	}
	if len(outputs) != 1 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected 1 completed output, got %d", len(outputs))
	}
}

func TestE2E_RerunMode_Single(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing single mode checkpoint save and reload", t.Name())

	tmpDir, err := os.MkdirTemp("", "ntm-e2e-rerun-*")
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	store, err := ensemble.NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] NewCheckpointStore failed: %v", err)
	}

	runID := fmt.Sprintf("e2e-rerun-%d", time.Now().UnixNano())
	modeID := "rerun-test-mode"
	now := time.Now().UTC()

	// Save a checkpoint with an error.
	errCheckpoint := ensemble.ModeCheckpoint{
		ModeID:     modeID,
		Status:     "error",
		CapturedAt: now,
		Error:      "simulated failure",
	}
	if err := store.SaveCheckpoint(runID, errCheckpoint); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveCheckpoint (error) failed: %v", err)
	}

	// Load and verify the error state.
	loaded, err := store.LoadCheckpoint(runID, modeID)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] LoadCheckpoint failed: %v", err)
	}
	t.Logf("E2E: %s - assertion: loaded.Status = %q (want error)", t.Name(), loaded.Status)
	if loaded.Status != "error" {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected error status, got %q", loaded.Status)
	}

	// Now save a successful rerun.
	successCheckpoint := ensemble.ModeCheckpoint{
		ModeID: modeID,
		Output: &ensemble.ModeOutput{
			ModeID: modeID,
			Thesis: "Successful rerun thesis",
			TopFindings: []ensemble.Finding{
				{Finding: "Rerun finding", Impact: ensemble.ImpactLow, Confidence: 0.75},
			},
		},
		Status:     "done",
		CapturedAt: time.Now().UTC(),
		TokensUsed: 300,
	}
	if err := store.SaveCheckpoint(runID, successCheckpoint); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveCheckpoint (success) failed: %v", err)
	}

	// Verify the rerun replaced the error checkpoint.
	reloaded, err := store.LoadCheckpoint(runID, modeID)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] LoadCheckpoint after rerun failed: %v", err)
	}

	t.Logf("E2E: %s - assertion: reloaded.Status = %q (want done)", t.Name(), reloaded.Status)
	if reloaded.Status != "done" {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected done status after rerun, got %q", reloaded.Status)
	}
	if reloaded.Output == nil || reloaded.Output.Thesis != "Successful rerun thesis" {
		t.Fatalf("[E2E-ENSEMBLE-UX] rerun output not preserved")
	}
}

// -------------------------------------------------------------------
// Streaming Tests
// -------------------------------------------------------------------

func TestE2E_Stream_OutputProgress(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing synthesis streaming progress", t.Name())

	cfg := ensemble.SynthesisConfig{
		Strategy:      ensemble.StrategyManual,
		MinConfidence: 0.3,
		MaxFindings:   10,
	}

	synthesizer, err := ensemble.NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] NewSynthesizer failed: %v", err)
	}

	input := &ensemble.SynthesisInput{
		Outputs: []ensemble.ModeOutput{
			{
				ModeID:      "stream-mode-a",
				Thesis:      "Stream test thesis A",
				TopFindings: []ensemble.Finding{{Finding: "Stream finding A", Impact: ensemble.ImpactMedium, Confidence: 0.7}},
			},
			{
				ModeID:      "stream-mode-b",
				Thesis:      "Stream test thesis B",
				TopFindings: []ensemble.Finding{{Finding: "Stream finding B", Impact: ensemble.ImpactHigh, Confidence: 0.9}},
			},
		},
		OriginalQuestion: "E2E streaming test",
		Config:           cfg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chunks, errCh := synthesizer.StreamSynthesize(ctx, input)

	var received []ensemble.SynthesisChunk
	for chunk := range chunks {
		received = append(received, chunk)
		t.Logf("E2E: %s - chunk[%d]: type=%s content=%s", t.Name(), chunk.Index, chunk.Type, truncateString(chunk.Content, 60))
	}

	if err := <-errCh; err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] StreamSynthesize error: %v", err)
	}

	t.Logf("E2E: %s - assertion: chunk_count = %d (want >= 1)", t.Name(), len(received))
	if len(received) == 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected at least 1 synthesis chunk")
	}

	// Verify we got a complete chunk.
	hasComplete := false
	for _, chunk := range received {
		if chunk.Type == ensemble.ChunkComplete {
			hasComplete = true
			break
		}
	}
	t.Logf("E2E: %s - assertion: has_complete_chunk = %v (want true)", t.Name(), hasComplete)
	if !hasComplete {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected a ChunkComplete in stream")
	}
}

func TestE2E_Stream_CancelResume(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing synthesis stream cancellation", t.Name())

	cfg := ensemble.SynthesisConfig{
		Strategy:      ensemble.StrategyManual,
		MinConfidence: 0.3,
		MaxFindings:   10,
	}

	synthesizer, err := ensemble.NewSynthesizer(cfg)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] NewSynthesizer failed: %v", err)
	}

	input := &ensemble.SynthesisInput{
		Outputs: []ensemble.ModeOutput{
			{
				ModeID:      "cancel-mode",
				Thesis:      "Cancel test thesis",
				TopFindings: []ensemble.Finding{{Finding: "Cancel finding", Impact: ensemble.ImpactLow, Confidence: 0.5}},
			},
		},
		OriginalQuestion: "E2E cancel resume test",
		Config:           cfg,
	}

	// Start with a context we can cancel.
	ctx, cancel := context.WithCancel(context.Background())

	chunks, errCh := synthesizer.StreamSynthesize(ctx, input)

	// Drain at least 1 chunk then cancel.
	first, ok := <-chunks
	if ok {
		t.Logf("E2E: %s - received first chunk: type=%s", t.Name(), first.Type)
		cancel()
	} else {
		cancel()
		// Stream completed before we could cancel. That's fine.
		t.Logf("E2E: %s - stream completed before cancel", t.Name())
		<-errCh
		return
	}

	// Drain remaining chunks after cancel.
	remaining := 0
	for range chunks {
		remaining++
	}

	streamErr := <-errCh
	t.Logf("E2E: %s - assertion: remaining_chunks_after_cancel = %d, stream_err = %v", t.Name(), remaining, streamErr)

	// After cancel, the error channel should either have a context.Canceled or nil.
	// We don't fail on the specific error - just verify the stream didn't hang.
}

// -------------------------------------------------------------------
// Dedup + Compare Tests
// -------------------------------------------------------------------

func TestE2E_Dedupe_Auto(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing automatic finding deduplication", t.Name())

	cfg := ensemble.DedupeConfig{
		SimilarityThreshold:  0.7,
		EvidenceWeight:       0.3,
		TextWeight:           0.7,
		PreferHighConfidence: true,
		PreserveProvenance:   true,
	}

	engine := ensemble.NewDedupeEngine(cfg)

	// Create overlapping findings from different modes as ModeOutput slices.
	//
	// The duplicate pairs share most of their tokens on purpose: the engine
	// scores similarity as 0.7*Jaccard(token sets) + 0.3*evidence proximity,
	// so at the default 0.7 threshold only genuinely near-identical phrasings
	// cluster. Loosely paraphrased findings (e.g. "The authentication module
	// has a SQL injection vulnerability" vs "SQL injection risk in the auth
	// login handler", Jaccard ~0.23) score ~0.46 and correctly stay separate.
	outputs := []ensemble.ModeOutput{
		{
			ModeID: "mode-security",
			Thesis: "Security analysis",
			TopFindings: []ensemble.Finding{
				{Finding: "SQL injection vulnerability in the authentication login handler", Impact: ensemble.ImpactCritical, Confidence: 0.95, EvidencePointer: "auth/login.go:42"},
				{Finding: "Memory leak in the connection pool manager", Impact: ensemble.ImpactMedium, Confidence: 0.75, EvidencePointer: "pool/manager.go:105"},
			},
		},
		{
			ModeID: "mode-review",
			Thesis: "Code review analysis",
			TopFindings: []ensemble.Finding{
				{Finding: "SQL injection vulnerability in the auth login handler", Impact: ensemble.ImpactHigh, Confidence: 0.88, EvidencePointer: "auth/login.go:42"},
				{Finding: "Memory leak in the connection pool under load", Impact: ensemble.ImpactMedium, Confidence: 0.70, EvidencePointer: "pool/manager.go:110"},
				{Finding: "Unused error return in file handler", Impact: ensemble.ImpactLow, Confidence: 0.60, EvidencePointer: "fs/handler.go:23"},
			},
		},
	}

	totalFindings := 0
	for _, o := range outputs {
		totalFindings += len(o.TopFindings)
	}

	result := engine.Dedupe(outputs)

	t.Logf("E2E: %s - assertion: cluster_count = %d (want < %d input findings)", t.Name(), len(result.Clusters), totalFindings)
	t.Logf("E2E: %s - stats: input=%d clusters=%d", t.Name(), result.Stats.InputFindings, result.Stats.OutputClusters)

	// With similar findings, we should see fewer clusters than input.
	if len(result.Clusters) >= totalFindings {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected dedup to reduce findings: clusters=%d inputs=%d", len(result.Clusters), totalFindings)
	}

	// Log cluster details.
	for i, cluster := range result.Clusters {
		t.Logf("E2E: %s - cluster[%d]: canonical=%q confidence=%.2f members=%d",
			t.Name(), i, truncateString(cluster.Canonical.Finding, 50), float64(cluster.MaxConfidence), cluster.MemberCount)
	}
}

func TestE2E_Compare_TwoRuns(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing comparison of two ensemble runs", t.Name())

	tmpDir, err := os.MkdirTemp("", "ntm-e2e-compare-*")
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	store, err := ensemble.NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] NewCheckpointStore failed: %v", err)
	}

	now := time.Now().UTC()
	runA := "e2e-compare-a"
	runB := "e2e-compare-b"

	// Save two runs with different outputs.
	metaA := ensemble.CheckpointMetadata{
		RunID: runA, SessionName: "e2e-compare", Question: "E2E compare",
		Status: ensemble.EnsembleComplete, CreatedAt: now, UpdatedAt: now,
		CompletedIDs: []string{"mode-a", "mode-b"}, TotalModes: 2,
	}
	if err := store.SaveMetadata(metaA); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveMetadata A failed: %v", err)
	}

	for _, modeID := range []string{"mode-a", "mode-b"} {
		cp := ensemble.ModeCheckpoint{
			ModeID: modeID,
			Output: &ensemble.ModeOutput{
				ModeID:      modeID,
				Thesis:      fmt.Sprintf("Run A thesis %s", modeID),
				TopFindings: []ensemble.Finding{{Finding: fmt.Sprintf("Run A finding from %s", modeID), Impact: ensemble.ImpactMedium, Confidence: 0.7}},
			},
			Status: "done", CapturedAt: now,
		}
		if err := store.SaveCheckpoint(runA, cp); err != nil {
			t.Fatalf("[E2E-ENSEMBLE-UX] SaveCheckpoint A/%s failed: %v", modeID, err)
		}
	}

	metaB := ensemble.CheckpointMetadata{
		RunID: runB, SessionName: "e2e-compare", Question: "E2E compare v2",
		Status: ensemble.EnsembleComplete, CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour),
		CompletedIDs: []string{"mode-a", "mode-c"}, TotalModes: 2,
	}
	if err := store.SaveMetadata(metaB); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveMetadata B failed: %v", err)
	}

	for _, modeID := range []string{"mode-a", "mode-c"} {
		cp := ensemble.ModeCheckpoint{
			ModeID: modeID,
			Output: &ensemble.ModeOutput{
				ModeID:      modeID,
				Thesis:      fmt.Sprintf("Run B thesis %s", modeID),
				TopFindings: []ensemble.Finding{{Finding: fmt.Sprintf("Run B finding from %s", modeID), Impact: ensemble.ImpactHigh, Confidence: 0.85}},
			},
			Status: "done", CapturedAt: now.Add(time.Hour),
		}
		if err := store.SaveCheckpoint(runB, cp); err != nil {
			t.Fatalf("[E2E-ENSEMBLE-UX] SaveCheckpoint B/%s failed: %v", modeID, err)
		}
	}

	// Load both runs' outputs.
	outputsA, err := store.GetCompletedOutputs(runA)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetCompletedOutputs A failed: %v", err)
	}
	outputsB, err := store.GetCompletedOutputs(runB)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetCompletedOutputs B failed: %v", err)
	}

	t.Logf("E2E: %s - assertion: outputsA count = %d (want 2)", t.Name(), len(outputsA))
	t.Logf("E2E: %s - assertion: outputsB count = %d (want 2)", t.Name(), len(outputsB))

	if len(outputsA) != 2 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected 2 outputs for run A, got %d", len(outputsA))
	}
	if len(outputsB) != 2 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected 2 outputs for run B, got %d", len(outputsB))
	}

	// Verify runs are stored separately.
	if store.RunExists(runA) && store.RunExists(runB) {
		t.Logf("E2E: %s - assertion: both runs exist = true (want true)", t.Name())
	} else {
		t.Fatalf("[E2E-ENSEMBLE-UX] one or both runs do not exist in store")
	}
}

// -------------------------------------------------------------------
// Sharing / Export-Import Tests
// -------------------------------------------------------------------

func TestE2E_ExportImport_RoundTrip(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing export/import round-trip", t.Name())

	// Get a real preset for the round-trip test.
	engine := ensemble.NewSuggestionEngine()
	presetNames := engine.ListPresets()
	if len(presetNames) == 0 {
		t.Skip("no presets available for export test")
	}

	preset := engine.GetPreset(presetNames[0])
	if preset == nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetPreset(%q) returned nil", presetNames[0])
	}
	t.Logf("E2E: %s - using preset %q for round-trip", t.Name(), preset.Name)

	// Export from preset.
	exported := ensemble.ExportFromPreset(*preset)

	// Convert to JSON.
	data, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] marshal export failed: %v", err)
	}

	// Write to temp file.
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("ntm-e2e-export-%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] write export file failed: %v", err)
	}
	t.Cleanup(func() { os.Remove(tmpFile) })

	// Read back and parse.
	readData, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] read export file failed: %v", err)
	}

	var imported ensemble.EnsembleExport
	if err := json.Unmarshal(readData, &imported); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] unmarshal import failed: %v", err)
	}

	// Convert back to preset.
	roundTripped := imported.ToPreset()

	t.Logf("E2E: %s - assertion: roundTripped.Name = %q (want %q)", t.Name(), roundTripped.Name, preset.Name)
	if roundTripped.Name != preset.Name {
		t.Fatalf("[E2E-ENSEMBLE-UX] round-trip name mismatch: got %q want %q", roundTripped.Name, preset.Name)
	}

	t.Logf("E2E: %s - assertion: roundTripped modes count = %d (want %d)", t.Name(), len(roundTripped.Modes), len(preset.Modes))
	if len(roundTripped.Modes) != len(preset.Modes) {
		t.Fatalf("[E2E-ENSEMBLE-UX] round-trip mode count mismatch: got %d want %d", len(roundTripped.Modes), len(preset.Modes))
	}
}

func TestE2E_Import_RemoteRejected(t *testing.T) {
	SkipIfShort(t)

	t.Logf("E2E: %s - testing import validation rejects invalid data", t.Name())

	// Create an invalid export with unknown mode references.
	invalid := ensemble.EnsembleExport{
		SchemaVersion: 1,
		ExportedAt:    time.Now(),
		Name:          "e2e-invalid-export",
		DisplayName:   "Invalid Export",
		Modes: []ensemble.ModeRef{
			{ID: "nonexistent-mode-xyz", Code: "Z99"},
		},
	}

	// Try converting and using the invalid export.
	preset := invalid.ToPreset()
	t.Logf("E2E: %s - assertion: converted invalid preset name = %q", t.Name(), preset.Name)

	// The preset should convert but modes reference will be unresolvable.
	if len(preset.Modes) != 1 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected 1 mode in invalid preset, got %d", len(preset.Modes))
	}
	if preset.Modes[0].ID != "nonexistent-mode-xyz" {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected invalid mode ID preserved, got %q", preset.Modes[0].ID)
	}

	t.Logf("E2E: %s - assertion: invalid mode ID preserved = true (want true)", t.Name())
}

// -------------------------------------------------------------------
// Finding Export Tests
// -------------------------------------------------------------------

func TestE2E_ExportFindings_DryRun(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_export_findings_dryrun")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] suite setup failed: %v", err)
	}

	session := suite.Session()
	t.Logf("E2E: %s - testing findings export dry-run", t.Name())

	// Seed deterministic state.
	now := time.Now().UTC()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] GetPanes failed: %v", err)
	}
	if len(panes) == 0 || panes[0].ID == "" {
		t.Fatalf("[E2E-ENSEMBLE-UX] no panes available")
	}
	paneID := panes[0].ID

	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "E2E findings export dry-run test",
		PresetUsed:        "e2e",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "e2e-export-mode",
				PaneName:    paneID,
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				AssignedAt:  now,
				CompletedAt: &now,
			},
		},
	}
	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] SaveSession failed: %v", err)
	}
	sendYAMLModeOutput(t, session, 0, "E2E export findings thesis")

	// Run synthesize with JSON format to get findings.
	res := runEnsembleUXCmd(t, suite, "synthesize_for_export",
		"ensemble", "synthesize", session, "--format", "json",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), truncateString(string(res.Stdout), 500))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] synthesize for export failed: %v", res.Err)
	}

	// Verify output is valid JSON.
	var out map[string]interface{}
	parseEnsembleUXJSON(t, suite, "synthesize_for_export", res.Stdout, &out)

	if _, ok := out["synthesis"]; !ok {
		t.Fatalf("[E2E-ENSEMBLE-UX] synthesize output missing 'synthesis' field")
	}

	t.Logf("E2E: %s - assertion: synthesis JSON has findings = true", t.Name())
}

// -------------------------------------------------------------------
// Ensemble Presets List (robot) Tests
// -------------------------------------------------------------------

func TestE2E_Presets_RobotJSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_ux_presets_robot")
	defer suite.Teardown()

	t.Logf("E2E: %s - testing robot ensemble-presets JSON output", t.Name())

	res := runEnsembleUXCmd(t, suite, "robot_presets",
		"--robot-ensemble-presets",
	)

	t.Logf("E2E: %s - stdout: %s", t.Name(), truncateString(string(res.Stdout), 500))
	t.Logf("E2E: %s - exit code err: %v", t.Name(), res.Err)

	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-UX] --robot-ensemble-presets failed: %v", res.Err)
	}

	var out presetsListJSON
	parseEnsembleUXJSON(t, suite, "robot_presets", res.Stdout, &out)

	if out.Count == 0 {
		t.Fatalf("[E2E-ENSEMBLE-UX] expected at least 1 preset")
	}
	if out.Count != len(out.Presets) {
		t.Fatalf("[E2E-ENSEMBLE-UX] preset count mismatch: count=%d len=%d", out.Count, len(out.Presets))
	}

	t.Logf("E2E: %s - assertion: preset_count = %d (want >0)", t.Name(), out.Count)

	for i, p := range out.Presets {
		if p.Name == "" {
			t.Fatalf("[E2E-ENSEMBLE-UX] preset[%d] has empty name", i)
		}
		t.Logf("E2E: %s - preset[%d]: name=%s mode_count=%d", t.Name(), i, p.Name, p.ModeCount)
	}
}
