//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM commands.
//
// Bead: bd-3vkzo - Task: Write E2E tests for ensemble CLI commands
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type cliRunResult struct {
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	Err      error
}

func runEnsembleCLICmd(t *testing.T, suite *TestSuite, label string, args ...string) cliRunResult {
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

	suite.Logger().Log("[E2E-ENSEMBLE-CLI] %s args=%v duration_ms=%d err=%v", label, args, duration.Milliseconds(), err)
	suite.Logger().Log("[E2E-ENSEMBLE-CLI] %s stdout=%s", label, strings.TrimSpace(stdout.String()))
	suite.Logger().Log("[E2E-ENSEMBLE-CLI] %s stderr=%s", label, strings.TrimSpace(stderr.String()))

	return cliRunResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: duration,
		Err:      err,
	}
}

func parseEnsembleCLIJSON(t *testing.T, suite *TestSuite, label string, stdout []byte, v interface{}) {
	t.Helper()

	if err := json.Unmarshal(stdout, v); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] %s JSON parse failed: %v stdout=%s", label, err, string(stdout))
	}
	suite.Logger().LogJSON("[E2E-ENSEMBLE-CLI] "+label+" parsed", v)
}

func supportsNTMSubcommand(name string) bool {
	cmd := exec.Command("ntm", name, "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// Defensive: some older binaries may print "unknown command" but still exit 0.
	return !strings.Contains(string(output), `unknown command`)
}

type presetsListJSON struct {
	GeneratedAt string `json:"generated_at"`
	Count       int    `json:"count"`
	Presets     []struct {
		Name      string `json:"name"`
		ModeCount int    `json:"mode_count"`
	} `json:"presets"`
}

type modesListJSON struct {
	GeneratedAt string `json:"generated_at"`
	Count       int    `json:"count"`
	Modes       []struct {
		ID   string `json:"id"`
		Tier string `json:"tier"`
	} `json:"modes"`
}

type ensembleStatusJSON struct {
	GeneratedAt string `json:"generated_at"`
	Session     string `json:"session"`
	Exists      bool   `json:"exists"`
}

type ensembleSynthesizeJSON struct {
	Synthesis *ensemble.SynthesisResult `json:"synthesis"`
	Audit     *ensemble.AuditReport     `json:"audit,omitempty"`
}

func TestE2E_EnsembleCLI_ListPresets_JSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_cli_presets")
	defer suite.Teardown()

	res := runEnsembleCLICmd(t, suite, "ensemble_list_json", "ensemble", "list", "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] ensemble list failed: %v", res.Err)
	}

	var out presetsListJSON
	parseEnsembleCLIJSON(t, suite, "ensemble_list_json", res.Stdout, &out)

	if out.Count != len(out.Presets) {
		t.Fatalf("[E2E-ENSEMBLE-CLI] preset count mismatch: count=%d len=%d", out.Count, len(out.Presets))
	}
	if out.Count == 0 {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected at least 1 preset")
	}
}

func TestE2E_EnsembleCLI_Modes_JSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("modes") {
		t.Skip("ntm binary does not support `modes` command")
	}

	suite := NewTestSuite(t, "ensemble_cli_modes")
	defer suite.Teardown()

	// Default: core only.
	res := runEnsembleCLICmd(t, suite, "modes_core_json", "modes", "list", "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes list failed: %v", res.Err)
	}

	var core modesListJSON
	parseEnsembleCLIJSON(t, suite, "modes_core_json", res.Stdout, &core)
	if core.Count != len(core.Modes) {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes core count mismatch: count=%d len=%d", core.Count, len(core.Modes))
	}
	for _, mode := range core.Modes {
		if strings.ToLower(mode.Tier) != "core" {
			t.Fatalf("[E2E-ENSEMBLE-CLI] expected core-only modes; got tier=%q id=%q", mode.Tier, mode.ID)
		}
	}

	// All tiers.
	res = runEnsembleCLICmd(t, suite, "modes_all_json", "modes", "list", "--all", "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes list --all failed: %v", res.Err)
	}

	var all modesListJSON
	parseEnsembleCLIJSON(t, suite, "modes_all_json", res.Stdout, &all)
	if all.Count != len(all.Modes) {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes all count mismatch: count=%d len=%d", all.Count, len(all.Modes))
	}

	var tiers struct {
		Core         int
		Advanced     int
		Experimental int
		Other        int
	}
	for _, mode := range all.Modes {
		switch strings.ToLower(mode.Tier) {
		case "core":
			tiers.Core++
		case "advanced":
			tiers.Advanced++
		case "experimental":
			tiers.Experimental++
		default:
			tiers.Other++
		}
	}
	suite.Logger().Log("[E2E-ENSEMBLE-CLI] tier_counts core=%d advanced=%d experimental=%d other=%d", tiers.Core, tiers.Advanced, tiers.Experimental, tiers.Other)

	// Explicit advanced tier.
	res = runEnsembleCLICmd(t, suite, "modes_advanced_json", "modes", "list", "--tier", "advanced", "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes list --tier advanced failed: %v", res.Err)
	}

	var advanced modesListJSON
	parseEnsembleCLIJSON(t, suite, "modes_advanced_json", res.Stdout, &advanced)
	if advanced.Count != len(advanced.Modes) {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes advanced count mismatch: count=%d len=%d", advanced.Count, len(advanced.Modes))
	}
	if advanced.Count == 0 {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected at least 1 advanced mode")
	}
	for _, mode := range advanced.Modes {
		if strings.ToLower(mode.Tier) != "advanced" {
			t.Fatalf("[E2E-ENSEMBLE-CLI] expected advanced-only modes; got tier=%q id=%q", mode.Tier, mode.ID)
		}
	}
}

func TestE2E_EnsembleCLI_Status_JSON_NoState(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_cli_status")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] suite setup failed: %v", err)
	}

	res := runEnsembleCLICmd(t, suite, "status_json_no_state", "ensemble", "status", suite.Session(), "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] ensemble status failed: %v", res.Err)
	}

	var out ensembleStatusJSON
	parseEnsembleCLIJSON(t, suite, "status_json_no_state", res.Stdout, &out)
	if out.Session != suite.Session() {
		t.Fatalf("[E2E-ENSEMBLE-CLI] status session mismatch: got=%q want=%q", out.Session, suite.Session())
	}
	if out.Exists {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected exists=false with no persisted ensemble state")
	}
}

func sendYAMLModeOutput(t *testing.T, session string, pane int, thesis string) {
	t.Helper()

	// Print a minimal, schema-valid YAML mode output inside a ```yaml block
	// so the ensemble output capture can extract and parse it deterministically.
	lines := []string{
		"```yaml",
		fmt.Sprintf("thesis: %s", thesis),
		"top_findings:",
		"  - finding: E2E synthetic finding",
		"    impact: low",
		"confidence: 0.7",
		"```",
	}

	quoted := make([]string, 0, len(lines))
	for _, line := range lines {
		quoted = append(quoted, fmt.Sprintf("'%s'", strings.ReplaceAll(line, "'", "'\"'\"'")))
	}

	// printf '%s\n' 'line1' 'line2' ...
	cmdStr := "printf '%s\\n' " + strings.Join(quoted, " ")

	target := fmt.Sprintf("%s:%d", session, pane)
	cmd := exec.Command(tmux.BinaryPath(), "send-keys", "-t", target, cmdStr, "Enter")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] failed to send YAML output to pane: %v output=%s", err, string(out))
	}
}

func ensembleSpawnIsExperimental() bool {
	cmd := exec.Command("ntm", "ensemble", "spawn", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return true
	}
	return strings.Contains(string(output), "requires building with -tags ensemble_experimental") ||
		strings.Contains(string(output), "rebuild with -tags ensemble_experimental")
}

func TestE2E_EnsembleCLI_AdvancedGating(t *testing.T) {
	CommonE2EPrerequisites(t)
	SkipIfNoAgents(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	if ensembleSpawnIsExperimental() {
		t.Skip("ensemble spawn requires -tags ensemble_experimental; skipping advanced gating test")
	}

	suite := NewTestSuite(t, "ensemble_cli_advanced_gating")
	defer suite.Teardown()

	// Find an advanced mode via CLI JSON.
	modesRes := runEnsembleCLICmd(t, suite, "modes_advanced_for_gating", "modes", "list", "--tier", "advanced", "--format", "json")
	if modesRes.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] modes list --tier advanced failed: %v", modesRes.Err)
	}
	var adv modesListJSON
	parseEnsembleCLIJSON(t, suite, "modes_advanced_for_gating", modesRes.Stdout, &adv)
	if len(adv.Modes) == 0 {
		t.Skip("no advanced modes available; skipping advanced gating test")
	}

	advancedModeID := adv.Modes[0].ID
	agent := GetAvailableAgent()
	if agent == "" {
		t.Skip("no agent CLI available for ensemble spawn")
	}

	failSession := fmt.Sprintf("%s_adv_fail", suite.Session())
	suite.cleanup = append(suite.cleanup, func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", failSession).Run()
	})
	failArgs := []string{
		"--json",
		"ensemble", "spawn", failSession,
		"--modes", advancedModeID,
		"--question", "E2E advanced gating blocked",
		"--agent-mix", fmt.Sprintf("%s=1", agent),
	}
	res := runEnsembleCLICmd(t, suite, "spawn_advanced_blocked", failArgs...)
	if res.Err == nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected gating failure without --allow-advanced")
	}

	// Log error code if present (output.ErrorResponse uses "code").
	var blocked map[string]interface{}
	if err := json.Unmarshal(res.Stdout, &blocked); err == nil {
		suite.Logger().LogJSON("[E2E-ENSEMBLE-CLI] spawn_advanced_blocked parsed", blocked)
		if code, ok := blocked["code"].(string); ok && code != "" {
			suite.Logger().Log("[E2E-ENSEMBLE-CLI] gating_error_code=%s", code)
		}
	}

	okSession := fmt.Sprintf("%s_adv_ok", suite.Session())
	suite.cleanup = append(suite.cleanup, func() {
		exec.Command(tmux.BinaryPath(), "kill-session", "-t", okSession).Run()
	})

	okArgs := []string{
		"--json",
		"ensemble", "spawn", okSession,
		"--modes", advancedModeID,
		"--allow-advanced",
		"--question", "E2E advanced gating allowed",
		"--agent-mix", fmt.Sprintf("%s=1", agent),
	}
	res = runEnsembleCLICmd(t, suite, "spawn_advanced_allowed", okArgs...)
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected advanced spawn success: %v", res.Err)
	}
}

func TestE2E_EnsembleCLI_Synthesize_JSON(t *testing.T) {
	CommonE2EPrerequisites(t)

	if !supportsNTMSubcommand("ensemble") {
		t.Skip("ntm binary does not support `ensemble` command")
	}

	suite := NewTestSuite(t, "ensemble_cli_synthesize")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] suite setup failed: %v", err)
	}

	session := suite.Session()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] GetPanes failed: %v", err)
	}
	if len(panes) == 0 || panes[0].ID == "" {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected at least 1 pane with an ID")
	}
	paneID := panes[0].ID

	// Seed deterministic ensemble state into the shared SQLite store so the CLI can load it.
	now := time.Now().UTC()
	state := &ensemble.EnsembleSession{
		SessionName:       session,
		Question:          "E2E: deterministic synthesize without real agents",
		PresetUsed:        "e2e",
		Status:            ensemble.EnsembleActive,
		SynthesisStrategy: ensemble.StrategyManual,
		CreatedAt:         now,
		Assignments: []ensemble.ModeAssignment{
			{
				ModeID:      "e2e-mode",
				PaneName:    paneID, // use pane ID to avoid reliance on pane titles
				AgentType:   "cc",
				Status:      ensemble.AssignmentDone,
				AssignedAt:  now,
				CompletedAt: &now,
			},
		},
	}

	if err := ensemble.SaveSession(session, state); err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] SaveSession failed: %v", err)
	}

	sendYAMLModeOutput(t, session, 0, "E2E deterministic thesis")

	// Verify status JSON now shows Exists=true
	res := runEnsembleCLICmd(t, suite, "status_json_with_state", "ensemble", "status", session, "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] ensemble status (with state) failed: %v", res.Err)
	}
	var status ensembleStatusJSON
	parseEnsembleCLIJSON(t, suite, "status_json_with_state", res.Stdout, &status)
	if !status.Exists {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected exists=true after seeding state")
	}

	// Synthesize to JSON.
	res = runEnsembleCLICmd(t, suite, "synthesize_json", "ensemble", "synthesize", session, "--format", "json")
	if res.Err != nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] ensemble synthesize failed: %v", res.Err)
	}

	var out ensembleSynthesizeJSON
	parseEnsembleCLIJSON(t, suite, "synthesize_json", res.Stdout, &out)
	if out.Synthesis == nil {
		t.Fatalf("[E2E-ENSEMBLE-CLI] synthesize output missing synthesis field")
	}
	if len(out.Synthesis.Findings) == 0 {
		t.Fatalf("[E2E-ENSEMBLE-CLI] expected at least 1 synthesized finding")
	}
}
