//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/ratelimit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	spawnAssignmentProjectID     = 77
	spawnAssignmentAgentID       = 88
	spawnAssignmentReservationID = 701
	spawnAssignmentPath          = "internal/robot/**"
	spawnAssignmentRecipient     = "ExactRecipient"
	spawnAssignmentDisplayName   = "DisplayAlias"
	spawnAssignmentToken         = "e2e-registration-token"
)

type spawnGrokE2EFixture struct {
	ntmPath      string
	tmuxPath     string
	root         string
	projectDir   string
	projectsBase string
	fakeBin      string
	configPath   string
	argvLog      string
	stdinLog     string
	failureLog   string
	cassLog      string
	tmuxAudit    string
	env          []string
}

type spawnGrokProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type spawnGrokPaneJSON struct {
	PaneID       string `json:"pane_id"`
	Pane         string `json:"pane"`
	Index        int    `json:"index"`
	Title        string `json:"title"`
	Type         string `json:"type"`
	Variant      string `json:"variant"`
	Command      string `json:"command"`
	ContextModel string `json:"context_model"`
	Ready        bool   `json:"ready"`
	Error        string `json:"error"`
}

type spawnGrokCountsJSON struct {
	Claude int `json:"claude"`
	Grok   int `json:"grok"`
	User   int `json:"user"`
	Other  int `json:"other"`
	Total  int `json:"total"`
}

type spawnGrokStatusJSON struct {
	Session     string              `json:"session"`
	Exists      bool                `json:"exists"`
	Panes       []spawnGrokPaneJSON `json:"panes"`
	AgentCounts spawnGrokCountsJSON `json:"agent_counts"`
}

type spawnGrokMutationState struct {
	topology      string
	mutatingAudit string
	durableState  string
	argv          []byte
	stdin         []byte
}

// TestE2ESpawnGrokPhaseOneBuiltBinary proves the complete phase-one contract
// through the built ntm binary and a private real tmux server. Supported paths
// launch and discover Grok Build exactly; every unsupported prompt or lifecycle
// path must reject before it changes tmux, the process, or agent stdin.
func TestE2ESpawnGrokPhaseOneBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newSpawnGrokE2EFixture(t)
	cliSession := fixture.sessionName("cli")
	cliDir := fixture.prepareProject(t, cliSession)
	robotSession := fixture.sessionName("robot")
	robotDir := fixture.prepareProject(t, robotSession)

	t.Run("cli_spawn_add_status_and_exact_argv", func(t *testing.T) {
		spawnArgvStart := fixture.launchLogPosition(t)
		spawned := fixture.runNTM(t, cliDir,
			"--json", "spawn", cliSession,
			"--grok=1:model-alpha:high",
			"--no-user", "--no-hooks", "--no-cass-context", "--no-recovery",
		)
		fixture.requireSuccess(t, spawned)

		var spawnOutput struct {
			Session     string              `json:"session"`
			Created     bool                `json:"created"`
			Panes       []spawnGrokPaneJSON `json:"panes"`
			AgentCounts spawnGrokCountsJSON `json:"agent_counts"`
		}
		fixture.decodeSingleJSON(t, spawned.stdout, &spawnOutput)
		if spawnOutput.Session != cliSession || !spawnOutput.Created ||
			spawnOutput.AgentCounts.Grok != 1 || spawnOutput.AgentCounts.Total != 1 ||
			len(spawnOutput.Panes) != 1 {
			t.Fatalf("Grok CLI spawn envelope = %+v", spawnOutput)
		}
		assertSpawnGrokPaneIdentity(t, spawnOutput.Panes[0], cliSession+"__grok_1_model-alpha", "model-alpha")
		fixture.waitForPaneCommand(t, spawnOutput.Panes[0].PaneID, "grok")
		fixture.waitForArgvDelta(t, spawnArgvStart, []string{
			"--always-approve\t--model\tmodel-alpha\t--effort\thigh",
		})
		fixture.assertNoAgentInput(t)

		addArgvStart := fixture.launchLogPosition(t)
		added := fixture.runNTM(t, cliDir,
			"--json", "add", cliSession,
			"--grok=1:model-beta:low", "--no-cass-context",
		)
		fixture.requireSuccess(t, added)
		var addOutput struct {
			Session    string              `json:"session"`
			AddedGrok  int                 `json:"added_grok"`
			TotalAdded int                 `json:"total_added"`
			NewPanes   []spawnGrokPaneJSON `json:"new_panes"`
		}
		fixture.decodeSingleJSON(t, added.stdout, &addOutput)
		if addOutput.Session != cliSession || addOutput.AddedGrok != 1 ||
			addOutput.TotalAdded != 1 || len(addOutput.NewPanes) != 1 {
			t.Fatalf("Grok add envelope = %+v", addOutput)
		}
		addedPane := addOutput.NewPanes[0]
		if addedPane.PaneID == "" || addedPane.Title != cliSession+"__grok_2_model-beta" ||
			addedPane.Type != "grok" || addedPane.Variant != "model-beta" ||
			!strings.Contains(addedPane.Command, "grok --always-approve") ||
			!strings.Contains(addedPane.Command, "--model 'model-beta'") ||
			!strings.Contains(addedPane.Command, "--effort 'low'") {
			t.Fatalf("Grok add pane = %+v", addedPane)
		}
		fixture.waitForPaneCommand(t, addedPane.PaneID, "grok")
		fixture.waitForArgvDelta(t, addArgvStart, []string{
			"--always-approve\t--model\tmodel-beta\t--effort\tlow",
		})

		modelOnlyArgvStart := fixture.launchLogPosition(t)
		modelOnly := fixture.runNTM(t, cliDir,
			"--json", "add", cliSession,
			"--grok=1:model-gamma", "--no-cass-context",
		)
		fixture.requireSuccess(t, modelOnly)
		var modelOnlyOutput struct {
			Session    string              `json:"session"`
			AddedGrok  int                 `json:"added_grok"`
			TotalAdded int                 `json:"total_added"`
			NewPanes   []spawnGrokPaneJSON `json:"new_panes"`
		}
		fixture.decodeSingleJSON(t, modelOnly.stdout, &modelOnlyOutput)
		if modelOnlyOutput.Session != cliSession || modelOnlyOutput.AddedGrok != 1 ||
			modelOnlyOutput.TotalAdded != 1 || len(modelOnlyOutput.NewPanes) != 1 {
			t.Fatalf("model-only Grok add envelope = %+v", modelOnlyOutput)
		}
		modelOnlyPane := modelOnlyOutput.NewPanes[0]
		if modelOnlyPane.PaneID == "" || modelOnlyPane.Title != cliSession+"__grok_3_model-gamma" ||
			modelOnlyPane.Type != "grok" || modelOnlyPane.Variant != "model-gamma" ||
			!strings.Contains(modelOnlyPane.Command, "grok --always-approve") ||
			!strings.Contains(modelOnlyPane.Command, "--model 'model-gamma'") ||
			strings.Contains(modelOnlyPane.Command, "--effort") {
			t.Fatalf("model-only Grok add pane = %+v", modelOnlyPane)
		}
		fixture.waitForPaneCommand(t, modelOnlyPane.PaneID, "grok")
		fixture.waitForArgvDelta(t, modelOnlyArgvStart, []string{
			"--always-approve\t--model\tmodel-gamma",
		})

		status := fixture.readStatus(t, cliDir, cliSession)
		if status.AgentCounts.Grok != 3 || status.AgentCounts.Total != 3 ||
			status.AgentCounts.User != 0 || status.AgentCounts.Other != 0 || len(status.Panes) != 3 {
			t.Fatalf("Grok CLI status = %+v", status)
		}
		assertSpawnGrokPaneJSON(t, status.Panes[0], cliSession+"__grok_1_model-alpha", "model-alpha")
		assertSpawnGrokPaneJSON(t, status.Panes[1], cliSession+"__grok_2_model-beta", "model-beta")
		assertSpawnGrokPaneJSON(t, status.Panes[2], cliSession+"__grok_3_model-gamma", "model-gamma")
		fixture.assertNoAgentInput(t)
	})

	t.Run("robot_spawn_and_status", func(t *testing.T) {
		spawnArgvStart := fixture.launchLogPosition(t)
		spawned := fixture.runNTM(t, robotDir,
			"--robot-format=json",
			"--robot-spawn="+robotSession,
			"--spawn-grok=1",
			"--spawn-no-user",
			"--spawn-dir="+robotDir,
		)
		fixture.requireSuccess(t, spawned)
		var output struct {
			Success bool                `json:"success"`
			Session string              `json:"session"`
			Agents  []spawnGrokPaneJSON `json:"agents"`
		}
		fixture.decodeSingleJSON(t, spawned.stdout, &output)
		if !output.Success || output.Session != robotSession || len(output.Agents) != 1 {
			t.Fatalf("Grok robot spawn envelope = %+v", output)
		}
		if output.Agents[0].Pane == "" || output.Agents[0].Type != "grok" ||
			output.Agents[0].Title != robotSession+"__grok_1" || output.Agents[0].Ready ||
			output.Agents[0].Variant != "" || output.Agents[0].Error != "" {
			t.Fatalf("Grok robot spawn agent = %+v", output.Agents[0])
		}
		fixture.waitForPaneCommand(t, output.Agents[0].Pane, "grok")
		fixture.waitForArgvDelta(t, spawnArgvStart, []string{
			"--always-approve",
		})

		status := fixture.readStatus(t, robotDir, robotSession)
		if status.AgentCounts.Grok != 1 || status.AgentCounts.Total != 1 || len(status.Panes) != 1 {
			t.Fatalf("Grok robot status = %+v", status)
		}
		assertSpawnGrokPaneJSON(t, status.Panes[0], robotSession+"__grok_1", "")
		fixture.assertNoAgentInput(t)

		for _, tc := range []struct {
			name string
			flag string
		}{
			{name: "wait_ready", flag: "--spawn-wait"},
			{name: "assign_work", flag: "--spawn-assign-work"},
		} {
			t.Run("reject_"+tc.name, func(t *testing.T) {
				rejectedSession := fixture.sessionName("robot-" + tc.name)
				rejectedDir := fixture.prepareProject(t, rejectedSession)
				result := fixture.assertGrokFailureWithoutMutation(t, rejectedDir,
					"--robot-format=json",
					"--robot-spawn="+rejectedSession,
					"--spawn-grok=1",
					"--spawn-no-user",
					"--spawn-dir="+rejectedDir,
					tc.flag,
				)
				if result.exitCode != 2 {
					t.Fatalf("unsupported Grok robot spawn exit=%d, want 2", result.exitCode)
				}
				var envelope struct {
					Success   bool   `json:"success"`
					ErrorCode string `json:"error_code"`
					Hint      string `json:"hint"`
				}
				fixture.decodeSingleJSON(t, result.stdout, &envelope)
				if envelope.Success || envelope.ErrorCode != "NOT_IMPLEMENTED" || envelope.Hint == "" {
					t.Fatalf("unsupported Grok robot spawn envelope=%+v", envelope)
				}
				if fixture.sessionExists(t, rejectedSession) {
					t.Fatalf("unsupported Grok robot spawn created session %q", rejectedSession)
				}
			})
		}
	})

	t.Run("occupied_reused_sessions_reject_before_mutation", func(t *testing.T) {
		cliStatus := fixture.readStatus(t, cliDir, cliSession)
		robotStatus := fixture.readStatus(t, robotDir, robotSession)
		if len(cliStatus.Panes) == 0 || len(robotStatus.Panes) == 0 {
			t.Fatalf("occupied Grok fixtures are missing panes: cli=%+v robot=%+v", cliStatus, robotStatus)
		}

		cases := []struct {
			name    string
			session string
			dir     string
			pane    spawnGrokPaneJSON
			args    []string
		}{
			{
				name:    "cli",
				session: cliSession,
				dir:     cliDir,
				pane:    cliStatus.Panes[0],
				args: []string{
					"--json", "spawn", cliSession, "--grok=1", "--no-user",
					"--no-hooks", "--no-cass-context", "--no-recovery",
				},
			},
			{
				name:    "robot",
				session: robotSession,
				dir:     robotDir,
				pane:    robotStatus.Panes[0],
				args: []string{
					"--robot-format=json", "--robot-spawn=" + robotSession,
					"--spawn-grok=1", "--spawn-no-user", "--spawn-dir=" + robotDir,
				},
			},
			{
				name:    "robot_missing_second_target",
				session: robotSession,
				dir:     robotDir,
				pane:    robotStatus.Panes[0],
				args: []string{
					"--robot-format=json", "--robot-spawn=" + robotSession,
					"--spawn-grok=2", "--spawn-no-user", "--spawn-dir=" + robotDir,
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.pane.PaneID == "" || tc.pane.Command != "grok" {
					t.Fatalf("occupied %s precondition pane = %+v, want running Grok", tc.name, tc.pane)
				}
				before := fixture.mutationState(t)
				argvStart := fixture.launchLogPosition(t)

				result := fixture.runNTM(t, tc.dir, tc.args...)
				if result.exitCode == 0 {
					t.Fatalf("occupied %s spawn exited zero: stdout=%s stderr=%s", tc.name, result.stdout, result.stderr)
				}
				var envelope map[string]any
				fixture.decodeSingleJSON(t, result.stdout, &envelope)
				if success, ok := envelope["success"].(bool); !ok || success {
					t.Fatalf("occupied %s spawn envelope = %+v, want success:false", tc.name, envelope)
				}
				diagnostics := strings.ToLower(string(result.stdout) + "\n" + string(result.stderr))
				for _, want := range []string{"pre-launch current command", "non-shell"} {
					if !strings.Contains(diagnostics, want) {
						t.Fatalf("occupied %s spawn diagnostics omit %q: stdout=%s stderr=%s",
							tc.name, want, result.stdout, result.stderr)
					}
				}

				afterStatus := fixture.readStatus(t, tc.dir, tc.session)
				var afterCommand string
				for _, pane := range afterStatus.Panes {
					if pane.PaneID == tc.pane.PaneID {
						afterCommand = pane.Command
						break
					}
				}
				if afterCommand != tc.pane.Command {
					t.Fatalf("occupied %s pane %s command changed from %q to %q",
						tc.name, tc.pane.PaneID, tc.pane.Command, afterCommand)
				}
				fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
				fixture.assertNoLaunchDelta(t, argvStart)
				fixture.assertNoAgentInput(t)
			})
		}

		t.Run("cli_mixed_batch_rejects_late_occupied_grok_before_earlier_launch", func(t *testing.T) {
			session := fixture.sessionName("cli-mixed-late-grok")
			dir := fixture.prepareProject(t, session)
			fixture.createShellSession(t, session, dir)
			grokPaneID := fixture.addTitledShellPane(t, session, dir, session+"__grok_1")

			setupArgvStart := fixture.launchLogPosition(t)
			ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
			cmd := exec.CommandContext(ctx, fixture.tmuxPath,
				"send-keys", "-t", grokPaneID, "grok --always-approve", "Enter")
			cmd.Env = append([]string(nil), fixture.env...)
			output, launchErr := cmd.CombinedOutput()
			cancel()
			if launchErr != nil {
				t.Fatalf("launch occupied mixed-batch Grok fixture: %v output=%s", launchErr, output)
			}
			fixture.waitForPaneCommand(t, grokPaneID, "grok")
			fixture.waitForArgvDelta(t, setupArgvStart, []string{"--always-approve"})

			before := fixture.mutationState(t)
			argvStart := fixture.launchLogPosition(t)
			result := fixture.runNTM(t, dir,
				"--json", "spawn", session,
				"--cc=1", "--grok=1", "--no-user",
				"--no-hooks", "--no-cass-context", "--no-recovery",
			)
			if result.exitCode == 0 {
				t.Fatalf("mixed late-Grok spawn exited zero: stdout=%s stderr=%s", result.stdout, result.stderr)
			}
			var envelope map[string]any
			fixture.decodeSingleJSON(t, result.stdout, &envelope)
			if success, ok := envelope["success"].(bool); !ok || success {
				t.Fatalf("mixed late-Grok spawn envelope = %+v, want success:false", envelope)
			}
			diagnostics := strings.ToLower(string(result.stdout) + "\n" + string(result.stderr))
			for _, want := range []string{"pre-launch current command", "non-shell"} {
				if !strings.Contains(diagnostics, want) {
					t.Fatalf("mixed late-Grok diagnostics omit %q: stdout=%s stderr=%s",
						want, result.stdout, result.stderr)
				}
			}
			fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
			fixture.assertNoLaunchDelta(t, argvStart)
			fixture.assertNoAgentInput(t)
		})
	})

	t.Run("configured_default_grok_reaches_argv_detailed_status_and_compact_robot_summary", func(t *testing.T) {
		const configuredModel = "grok-e2e-configured-default"
		session := fixture.sessionName("robot-configured-default")
		dir := fixture.prepareProject(t, session)
		configPath := fixture.grokDefaultModelConfig(t, "robot-configured-default", configuredModel)
		configEnv := map[string]string{"NTM_CONFIG": configPath}
		argvStart := fixture.launchLogPosition(t)

		spawned := fixture.runNTMWithEnv(t, dir, configEnv,
			"--robot-format=json", "--robot-spawn="+session,
			"--spawn-grok=1", "--spawn-no-user", "--spawn-dir="+dir,
		)
		fixture.requireSuccess(t, spawned)
		var spawnOutput struct {
			Success bool                `json:"success"`
			Session string              `json:"session"`
			Agents  []spawnGrokPaneJSON `json:"agents"`
		}
		fixture.decodeSingleJSON(t, spawned.stdout, &spawnOutput)
		if !spawnOutput.Success || spawnOutput.Session != session || len(spawnOutput.Agents) != 1 {
			t.Fatalf("configured-default Grok robot spawn = %+v", spawnOutput)
		}
		pane := spawnOutput.Agents[0]
		if pane.Pane == "" || pane.Type != "grok" || pane.Title != session+"__grok_1" || pane.Ready || pane.Error != "" {
			t.Fatalf("configured-default Grok robot agent = %+v", pane)
		}
		fixture.waitForPaneCommand(t, pane.Pane, "grok")
		fixture.waitForArgvDelta(t, argvStart, []string{
			"--always-approve\t--model\t" + configuredModel,
		})

		humanStatusResult := fixture.runNTMWithEnv(t, dir, configEnv, "--json", "status", session)
		fixture.requireSuccess(t, humanStatusResult)
		var humanStatus spawnGrokStatusJSON
		fixture.decodeSingleJSON(t, humanStatusResult.stdout, &humanStatus)
		if humanStatus.Session != session || !humanStatus.Exists || len(humanStatus.Panes) != 1 ||
			humanStatus.Panes[0].Type != "grok" || humanStatus.Panes[0].ContextModel != configuredModel {
			t.Fatalf("configured-default human JSON status = %+v, want one Grok with context_model %q",
				humanStatus, configuredModel)
		}

		statusResult := fixture.runNTMWithEnv(t, dir, configEnv, "--robot-format=json", "--robot-status")
		fixture.requireSuccess(t, statusResult)
		var statusOutput struct {
			Success  bool `json:"success"`
			Sessions []struct {
				Name       string          `json:"name"`
				AgentCount int             `json:"agent_count"`
				Agents     json.RawMessage `json:"agents"`
			} `json:"sessions"`
			Summary struct {
				GrokCount    int            `json:"grok_count"`
				AgentsByType map[string]int `json:"agents_by_type"`
			} `json:"summary"`
		}
		fixture.decodeSingleJSON(t, statusResult.stdout, &statusOutput)
		if !statusOutput.Success {
			t.Fatalf("configured-default robot status = %+v", statusOutput)
		}
		foundSession := false
		for _, statusSession := range statusOutput.Sessions {
			if statusSession.Name != session {
				continue
			}
			foundSession = true
			if statusSession.AgentCount != 1 {
				t.Fatalf("configured-default compact robot session agent_count = %d, want 1; status=%s",
					statusSession.AgentCount, statusResult.stdout)
			}
			if len(statusSession.Agents) != 0 {
				t.Fatalf("configured-default compact robot session unexpectedly includes nested agents: %s",
					statusResult.stdout)
			}
		}
		if !foundSession {
			t.Fatalf("configured-default compact robot status omits session %q: %s", session, statusResult.stdout)
		}
		if statusOutput.Summary.GrokCount < 1 ||
			statusOutput.Summary.AgentsByType["grok"] != statusOutput.Summary.GrokCount {
			t.Fatalf("configured-default compact robot Grok summary = count %d/by-type %d, want matching positive counts; status=%s",
				statusOutput.Summary.GrokCount, statusOutput.Summary.AgentsByType["grok"], statusResult.stdout)
		}
		fixture.assertNoAgentInput(t)
	})

	t.Run("unsupported_spawn_and_add_modifiers_are_preflight_only", func(t *testing.T) {
		marchingOrders := filepath.Join(fixture.root, "grok-marching-orders.txt")
		if err := os.WriteFile(marchingOrders, []byte("pane:0 do not dispatch\n"), 0o600); err != nil {
			t.Fatalf("write Grok marching orders: %v", err)
		}
		autoRestartConfig := filepath.Join(fixture.root, "grok-auto-restart.toml")
		if err := os.WriteFile(autoRestartConfig, []byte("[resilience]\nauto_restart = true\n"), 0o600); err != nil {
			t.Fatalf("write Grok automatic-restart config: %v", err)
		}

		spawnCases := []struct {
			name  string
			extra []string
		}{
			{name: "prompt", extra: []string{"--prompt=do-not-send"}},
			{name: "init_prompt", extra: []string{"--init-prompt=do-not-send"}},
			{name: "cass_context", extra: []string{"--cass-context=history"}},
			{name: "marching_orders", extra: []string{"--marching-orders=" + marchingOrders}},
			{name: "assignment", extra: []string{"--assign"}},
			{name: "automatic_restart", extra: []string{"--auto-restart"}},
			{name: "configured_automatic_restart", extra: []string{"--config=" + autoRestartConfig}},
			{name: "persona", extra: []string{"--persona=grok-reviewer"}},
		}
		for _, tc := range spawnCases {
			t.Run("spawn_"+tc.name, func(t *testing.T) {
				session := fixture.sessionName("reject-" + tc.name)
				dir := fixture.prepareProject(t, session)
				args := []string{
					"--json", "spawn", session, "--grok=1", "--no-user",
					"--no-hooks", "--no-cass-context", "--no-recovery",
				}
				if tc.name == "cass_context" {
					args = []string{
						"--json", "spawn", session, "--grok=1", "--no-user",
						"--no-hooks", "--no-recovery",
					}
				}
				if tc.name == "persona" {
					args = []string{
						"--json", "spawn", session, "--no-user",
						"--no-hooks", "--no-cass-context", "--no-recovery",
					}
				}
				if tc.name == "configured_automatic_restart" {
					args = append([]string{tc.extra[0]}, args...)
				} else {
					args = append(args, tc.extra...)
				}
				fixture.assertGrokFailureWithoutMutation(t, dir, args...)
				if fixture.sessionExists(t, session) {
					t.Fatalf("unsupported Grok spawn created session %q", session)
				}
			})
		}

		addCases := []struct {
			name string
			args []string
		}{
			{name: "prompt", args: []string{"--grok=1", "--no-cass-context", "--prompt=do-not-send"}},
			{name: "cass_context", args: []string{"--grok=1", "--cass-context=history"}},
			{name: "persona", args: []string{"--persona=grok-reviewer", "--no-cass-context"}},
		}
		for _, tc := range addCases {
			t.Run("add_"+tc.name, func(t *testing.T) {
				args := append([]string{"--json", "add", cliSession}, tc.args...)
				fixture.assertGrokFailureWithoutMutation(t, cliDir, args...)
			})
		}
	})

	t.Run("prompt_interrupt_and_restart_surfaces_fail_before_actuation", func(t *testing.T) {
		status := fixture.readStatus(t, cliDir, cliSession)
		if len(status.Panes) < 1 {
			t.Fatalf("Grok status has no target panes: %+v", status)
		}
		paneID := status.Panes[0].PaneID
		paneIndex := fmt.Sprintf("%d", status.Panes[0].Index)
		marker := "GROK_E2E_MUST_NOT_REACH_STDIN"
		settledBefore := fixture.mutationState(t)
		cases := []struct {
			name  string
			robot bool
			hint  string
			args  []string
		}{
			{
				name: "shell_send",
				args: []string{"--json", "send", cliSession, "--pane=" + paneID,
					"--no-hooks", "--no-cass-check", marker},
			},
			{
				name:  "robot_send_submit",
				robot: true,
				hint:  agent.GrokPromptDeliveryCapabilityHint,
				args: []string{"--robot-format=json", "--robot-send=" + cliSession,
					"--pane=" + paneID, "--msg=" + marker},
			},
			{
				name:  "robot_send_stage_only",
				robot: true,
				hint:  agent.GrokPromptDeliveryCapabilityHint,
				args: []string{"--robot-format=json", "--robot-send=" + cliSession,
					"--pane=" + paneID, "--msg=" + marker, "--enter=false"},
			},
			{
				name:  "robot_send_track",
				robot: true,
				hint:  agent.GrokPromptDeliveryCapabilityHint,
				args: []string{"--robot-format=json", "--robot-send=" + cliSession,
					"--pane=" + paneID, "--msg=" + marker, "--track", "--timeout=200ms", "--poll=50ms"},
			},
			{
				name:  "robot_interrupt_with_message",
				robot: true,
				hint:  agent.GrokPromptDeliveryCapabilityHint,
				args: []string{"--robot-format=json", "--robot-interrupt=" + cliSession,
					"--panes=" + paneIndex, "--interrupt-msg=" + marker,
					"--interrupt-force", "--interrupt-no-wait"},
			},
			{
				name:  "robot_restart_pane",
				robot: true,
				hint:  agent.GrokPhaseOneCapabilityHint,
				args: []string{"--robot-format=json", "--robot-restart-pane=" + cliSession,
					"--panes=" + paneIndex},
			},
			{
				name:  "robot_smart_restart",
				robot: true,
				hint:  agent.GrokPhaseOneCapabilityHint,
				args: []string{"--robot-format=json", "--robot-smart-restart=" + cliSession,
					"--panes=" + paneIndex, "--force"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result := fixture.assertGrokFailureWithoutMutation(t, cliDir, tc.args...)
				if tc.robot {
					fixture.requireUnavailableJSONFailure(t, result, tc.hint)
					return
				}
				fixture.requireSingleShellJSONFailure(t, result)
			})
		}
		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		fixture.assertMutationStateEqual(t, settledBefore, fixture.mutationState(t))
		fixture.assertNoAgentInput(t)
	})

	t.Run("rest_spawn_supported_and_wait_ready_rejected", func(t *testing.T) {
		baseURL, client := fixture.startServer(t, cliDir)
		restSession := fixture.sessionName("rest")
		spawnArgvStart := fixture.launchLogPosition(t)
		statusCode, response := fixture.postRESTSpawn(t, client, baseURL, restSession, `{"grok_count":1}`)
		if statusCode != http.StatusOK || response["success"] != true || response["session"] != restSession {
			t.Fatalf("supported Grok REST spawn status=%d response=%+v", statusCode, response)
		}
		agents, ok := response["agents"].([]any)
		if !ok || len(agents) != 2 {
			t.Fatalf("supported Grok REST agents = %#v, want user and Grok", response["agents"])
		}
		var grokAgent map[string]any
		for _, raw := range agents {
			agent, _ := raw.(map[string]any)
			if agent["type"] == "grok" {
				grokAgent = agent
			}
		}
		grokPane, _ := grokAgent["pane"].(string)
		if grokAgent == nil || grokPane == "" || grokAgent["title"] != restSession+"__grok_1" ||
			grokAgent["ready"] != false || grokAgent["error"] != nil {
			t.Fatalf("supported Grok REST agent = %+v", grokAgent)
		}
		fixture.waitForPaneCommand(t, grokPane, "grok")
		fixture.waitForArgvDelta(t, spawnArgvStart, []string{
			"--always-approve",
		})
		status := fixture.readStatus(t, cliDir, restSession)
		if status.AgentCounts.Grok != 1 || status.AgentCounts.User != 1 ||
			status.AgentCounts.Total != 2 || len(status.Panes) != 2 {
			t.Fatalf("supported Grok REST status = %+v", status)
		}
		fixture.assertNoAgentInput(t)

		var restGrokPane spawnGrokPaneJSON
		for _, pane := range status.Panes {
			if pane.Type == "grok" {
				restGrokPane = pane
				break
			}
		}
		if restGrokPane.PaneID == "" {
			t.Fatalf("supported Grok REST status omits Grok pane: %+v", status)
		}
		t.Run("raw_pane_input_rejected_before_send", func(t *testing.T) {
			before := fixture.mutationState(t)
			statusCode, inputResponse := fixture.postRESTPaneInput(
				t, client, baseURL, restSession, restGrokPane.Index,
				`{"text":"GROK_REST_INPUT_MUST_NOT_SEND","enter":true}`,
			)
			if statusCode != http.StatusNotImplemented || inputResponse["success"] != false ||
				inputResponse["error_code"] != "NOT_IMPLEMENTED" ||
				!spawnGrokTextNamesCapability(inputResponse) {
				t.Fatalf("Grok REST pane input status=%d response=%+v", statusCode, inputResponse)
			}
			hint, _ := inputResponse["hint"].(string)
			if hint == "" {
				t.Fatalf("Grok REST pane input omitted capability hint: %+v", inputResponse)
			}
			fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
			fixture.assertNoAgentInput(t)
		})

		for _, tc := range []struct {
			name   string
			action string
			body   string
		}{
			{
				name:   "aggregate_send_rejected_before_dispatch",
				action: "send",
				body: fmt.Sprintf(
					`{"panes":[%q],"message":"GROK_REST_SEND_MUST_NOT_SEND"}`,
					restGrokPane.PaneID,
				),
			},
			{
				name:   "aggregate_interrupt_message_rejected_before_ctrl_c",
				action: "interrupt",
				body: fmt.Sprintf(
					`{"panes":[%q],"message":"GROK_REST_INTERRUPT_MUST_NOT_SEND","force":true,"no_wait":true}`,
					restGrokPane.PaneID,
				),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := fixture.mutationState(t)
				statusCode, actionResponse := fixture.postRESTAgentAction(
					t, client, baseURL, restSession, tc.action, tc.body,
				)
				hint, _ := actionResponse["hint"].(string)
				if statusCode != http.StatusNotImplemented || actionResponse["success"] != false ||
					actionResponse["error_code"] != "NOT_IMPLEMENTED" ||
					hint != agent.GrokPromptDeliveryCapabilityHint {
					t.Fatalf("Grok REST %s status=%d response=%+v", tc.action, statusCode, actionResponse)
				}
				fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
				fixture.assertNoAgentInput(t)
			})
		}

		rejectedSession := fixture.sessionName("rest-wait")
		before := fixture.mutationState(t)
		statusCode, response = fixture.postRESTSpawn(t, client, baseURL, rejectedSession, `{"grok_count":1,"wait_ready":true}`)
		if statusCode != http.StatusNotImplemented || response["success"] != false ||
			response["error_code"] != "NOT_IMPLEMENTED" || !spawnGrokTextNamesCapability(response) {
			t.Fatalf("rejected Grok REST wait status=%d response=%+v", statusCode, response)
		}
		fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
		if fixture.sessionExists(t, rejectedSession) {
			t.Fatalf("rejected Grok REST wait created session %q", rejectedSession)
		}
	})

	t.Run("mixed_batch_hardening_and_saved_state_routes", func(t *testing.T) {
		hardeningSession := fixture.sessionName("hardening")
		hardeningDir := fixture.prepareProject(t, hardeningSession)
		if err := os.WriteFile(filepath.Join(hardeningDir, "AGENTS.md"),
			[]byte("GROK_CONTEXT_MUST_NOT_SEND\n"), 0o600); err != nil {
			t.Fatalf("write Grok context fixture: %v", err)
		}

		spawnArgvStart := fixture.launchLogPosition(t)
		spawned := fixture.runNTM(t, hardeningDir,
			"--json", "spawn", hardeningSession, "--grok=1", "--no-user",
			"--no-hooks", "--no-cass-context", "--no-recovery",
		)
		fixture.requireSuccess(t, spawned)
		fixture.waitForArgvDelta(t, spawnArgvStart, []string{"--always-approve"})
		claudePaneID := fixture.addTitledShellPane(t, hardeningSession, hardeningDir, hardeningSession+"__cc_2")

		status := fixture.readStatus(t, hardeningDir, hardeningSession)
		if status.AgentCounts.Grok != 1 || status.AgentCounts.Claude != 1 ||
			status.AgentCounts.User != 0 || status.AgentCounts.Other != 0 ||
			status.AgentCounts.Total != 2 || len(status.Panes) != 2 {
			t.Fatalf("mixed hardening session status = %+v", status)
		}
		var grokPane spawnGrokPaneJSON
		for _, pane := range status.Panes {
			if pane.Type == "grok" {
				grokPane = pane
			}
		}
		if grokPane.PaneID == "" {
			t.Fatalf("mixed hardening session omits Grok pane: %+v", status)
		}
		fixture.waitForPaneCommand(t, grokPane.PaneID, "grok")

		t.Run("context_injection_rejects_entire_mixed_batch", func(t *testing.T) {
			shellResult := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--json", "context", "inject", hardeningSession,
				"--files=AGENTS.md", "--all",
			)
			if shellResult.exitCode != 1 {
				t.Fatalf("shell Grok context inject exit=%d, want 1", shellResult.exitCode)
			}
			var shellEnvelope map[string]any
			fixture.decodeSingleJSON(t, shellResult.stdout, &shellEnvelope)
			if shellEnvelope["success"] != false || !spawnGrokTextNamesCapability(shellEnvelope) {
				t.Fatalf("shell Grok context inject envelope = %+v", shellEnvelope)
			}

			robotResult := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--robot-format=json", "--robot-context-inject="+hardeningSession,
				"--inject-files=AGENTS.md", "--inject-all",
			)
			fixture.requireUnavailableJSONFailure(t, robotResult, agent.GrokPromptDeliveryCapabilityHint)
		})

		t.Run("review_queue_rejects_before_confirmation_or_send", func(t *testing.T) {
			fixture.prepareReviewCommits(t, hardeningDir, 2)
			before := fixture.mutationState(t)
			result := fixture.runNTM(t, hardeningDir,
				"review-queue", hardeningSession, "--send", "--commits=2", "--idle-threshold=0s",
			)
			if result.exitCode != 1 {
				t.Fatalf("Grok review-queue exit=%d, want 1", result.exitCode)
			}
			if !spawnGrokTextNamesCapability(string(result.stdout) + "\n" + string(result.stderr)) {
				t.Fatalf("Grok review-queue omitted capability error: stdout=%s stderr=%s",
					result.stdout, result.stderr)
			}
			report := string(result.stdout)
			for _, want := range []string{"Pending Reviews: 2", "Suggested Assignments:", "Review commit", "review source"} {
				if !strings.Contains(report, want) {
					t.Fatalf("Grok review-queue durable report missing %q: %s", want, report)
				}
			}
			if strings.Contains(report, "Send these prompts?") {
				t.Fatalf("Grok review-queue reached confirmation prompt: %s", result.stdout)
			}
			fixture.assertMutationStateEqual(t, before, fixture.mutationState(t))
			fixture.assertNoAgentInput(t)
		})

		t.Run("plain_dashboard_and_robot_markdown_count_grok_explicitly", func(t *testing.T) {
			statusResult := fixture.runNTM(t, hardeningDir, "status", hardeningSession)
			fixture.requireSuccess(t, statusResult)
			statusText := string(statusResult.stdout)
			for _, want := range []string{"Grok", "Claude", "1 instance(s)"} {
				if !strings.Contains(statusText, want) {
					t.Fatalf("human status missing %q: %s", want, statusText)
				}
			}
			if strings.Contains(statusText, "No agents running") {
				t.Fatalf("human status treated mixed Grok session as empty: %s", statusText)
			}

			activity := fixture.runNTM(t, hardeningDir, "activity", hardeningSession)
			fixture.requireSuccess(t, activity)
			activityText := string(activity.stdout)
			for _, want := range []string{"grok", "claude", "2 total"} {
				if !strings.Contains(activityText, want) {
					t.Fatalf("human activity missing %q: %s", want, activityText)
				}
			}

			summaryResult := fixture.runNTM(t, hardeningDir, "summary", hardeningSession, "--since=30m")
			fixture.requireSuccess(t, summaryResult)
			if summaryText := string(summaryResult.stdout); !strings.Contains(summaryText, "GROK_E2E_SUMMARY_INCLUDED") {
				t.Fatalf("human summary omitted Grok pane output: %s", summaryText)
			}

			plain := fixture.runNTM(t, hardeningDir, "dashboard", hardeningSession, "--no-tui")
			fixture.requireSuccess(t, plain)
			plainText := string(plain.stdout)
			for _, want := range []string{
				"Grok=1", "Claude=1", "User=0", "Other=0", "[grok]", "[cc]",
			} {
				if !strings.Contains(plainText, want) {
					t.Fatalf("plain dashboard missing %q: %s", want, plainText)
				}
			}

			markdown := fixture.runNTM(t, hardeningDir,
				"--robot-markdown", "--md-session="+hardeningSession, "--md-sections=sessions",
			)
			fixture.requireSuccess(t, markdown)
			markdownText := string(markdown.stdout)
			for _, want := range []string{hardeningSession, "cc:1", "grok:1"} {
				if !strings.Contains(markdownText, want) {
					t.Fatalf("robot markdown missing %q: %s", want, markdownText)
				}
			}
			if strings.Contains(markdownText, "oth:1") || strings.Contains(markdownText, "usr:1") {
				t.Fatalf("robot markdown misclassified Grok: %s", markdownText)
			}
		})

		t.Run("direct_and_bulk_assignment_reject_before_bead_ledger_or_input_mutation", func(t *testing.T) {
			fixture.runBR(t, hardeningDir, "init", "--prefix=groke2e", "--json")
			directBead := strings.TrimSpace(string(fixture.runBR(
				t, hardeningDir, "create", "Grok direct assignment guard", "--type=task", "--priority=1", "--silent",
			)))
			bulkBead := strings.TrimSpace(string(fixture.runBR(
				t, hardeningDir, "create", "Grok mixed bulk assignment guard", "--type=task", "--priority=1", "--silent",
			)))
			if directBead == "" || bulkBead == "" || directBead == bulkBead {
				t.Fatalf("invalid Grok E2E bead IDs: direct=%q bulk=%q", directBead, bulkBead)
			}

			beadSnapshot := fixture.beadSnapshot(t, hardeningDir, directBead, bulkBead)
			ledgerSnapshot := fixture.sessionStateSnapshot(t, hardeningSession)
			direct := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--json", "assign", hardeningSession,
				"--beads="+directBead,
				"--pane="+grokPane.PaneID,
				"--prompt=GROK_DIRECT_ASSIGN_MUST_NOT_SEND",
				"--ignore-deps", "--reserve-files=false", "--force",
			)
			if direct.exitCode != 1 {
				t.Fatalf("direct Grok assignment exit=%d, want 1", direct.exitCode)
			}
			fixture.assertBeadAndLedgerStateEqual(
				t, hardeningDir, hardeningSession, beadSnapshot, ledgerSnapshot, directBead, bulkBead,
			)

			allocation, err := json.Marshal(map[string]string{
				grokPane.PaneID: directBead,
				claudePaneID:    bulkBead,
			})
			if err != nil {
				t.Fatalf("marshal mixed Grok bulk allocation: %v", err)
			}
			bulk := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--robot-format=json",
				"--robot-bulk-assign="+hardeningSession,
				"--allocation="+string(allocation),
			)
			fixture.requireUnavailableJSONFailure(t, bulk, agent.GrokPhaseOneCapabilityHint)
			var bulkEnvelope struct {
				Success     bool   `json:"success"`
				ErrorCode   string `json:"error_code"`
				Assignments []struct {
					Status     string `json:"status"`
					Claimed    bool   `json:"claimed"`
					PromptSent bool   `json:"prompt_sent"`
				} `json:"assignments"`
			}
			fixture.decodeSingleJSON(t, bulk.stdout, &bulkEnvelope)
			if bulkEnvelope.Success || bulkEnvelope.ErrorCode != "NOT_IMPLEMENTED" || len(bulkEnvelope.Assignments) != 2 {
				t.Fatalf("mixed Grok bulk envelope = %+v", bulkEnvelope)
			}
			for _, assignment := range bulkEnvelope.Assignments {
				if assignment.Status != "failed" || assignment.Claimed || assignment.PromptSent {
					t.Fatalf("mixed Grok bulk assignment crossed preflight: %+v", assignment)
				}
			}
			fixture.assertBeadAndLedgerStateEqual(
				t, hardeningDir, hardeningSession, beadSnapshot, ledgerSnapshot, directBead, bulkBead,
			)
			fixture.assertNoAgentInput(t)
		})

		t.Run("saved_session_topology_restore_and_launch_routes", func(t *testing.T) {
			savedName := hardeningSession + "-saved"
			saved := fixture.runNTM(t, hardeningDir,
				"--json", "sessions", "save", hardeningSession, "--name="+savedName,
			)
			fixture.requireSuccess(t, saved)
			var saveEnvelope struct {
				Success bool   `json:"success"`
				SavedAs string `json:"saved_as"`
				State   struct {
					Agents struct {
						Claude int `json:"cc"`
						Grok   int `json:"grok"`
					} `json:"agents"`
					Panes []struct {
						AgentType string `json:"agent_type"`
					} `json:"panes"`
				} `json:"state"`
			}
			fixture.decodeSingleJSON(t, saved.stdout, &saveEnvelope)
			if !saveEnvelope.Success || saveEnvelope.SavedAs != savedName ||
				saveEnvelope.State.Agents.Grok != 1 || saveEnvelope.State.Agents.Claude != 1 ||
				len(saveEnvelope.State.Panes) != 2 {
				t.Fatalf("saved mixed Grok state = %+v", saveEnvelope)
			}

			topologyTarget := fixture.sessionName("saved-topology")
			topologyArgvStart := fixture.launchLogPosition(t)
			topologyStdinBefore := append([]byte(nil), fixture.readFile(t, fixture.stdinLog)...)
			topologyRestore := fixture.runNTM(t, hardeningDir,
				"--json", "sessions", "restore", savedName,
				"--name="+topologyTarget, "--force", "--skip-git-check",
			)
			fixture.requireSuccess(t, topologyRestore)
			var topologyEnvelope struct {
				Success    bool   `json:"success"`
				SavedName  string `json:"saved_name"`
				RestoredAs string `json:"restored_as"`
				AgentCount int    `json:"agent_count"`
				State      *struct {
					Agents struct {
						Claude int `json:"cc"`
						Grok   int `json:"grok"`
					} `json:"agents"`
					Panes []struct {
						Title     string `json:"title"`
						AgentType string `json:"agent_type"`
					} `json:"panes"`
				} `json:"state"`
			}
			fixture.decodeSingleJSON(t, topologyRestore.stdout, &topologyEnvelope)
			if !topologyEnvelope.Success || topologyEnvelope.SavedName != savedName ||
				topologyEnvelope.RestoredAs != topologyTarget || topologyEnvelope.AgentCount != 0 ||
				topologyEnvelope.State == nil || topologyEnvelope.State.Agents.Grok != 1 ||
				topologyEnvelope.State.Agents.Claude != 1 || len(topologyEnvelope.State.Panes) != 2 {
				t.Fatalf("topology-only Grok restore envelope = %+v", topologyEnvelope)
			}

			topologyStatus := fixture.readStatus(t, hardeningDir, topologyTarget)
			if topologyStatus.AgentCounts.Grok != 1 || topologyStatus.AgentCounts.Claude != 1 ||
				topologyStatus.AgentCounts.User != 0 || topologyStatus.AgentCounts.Other != 0 ||
				topologyStatus.AgentCounts.Total != 2 || len(topologyStatus.Panes) != 2 {
				t.Fatalf("topology-only Grok restore status = %+v", topologyStatus)
			}
			wantRestoredPanes := map[string]string{
				hardeningSession + "__grok_1": "grok",
				hardeningSession + "__cc_2":   "claude",
			}
			for _, pane := range topologyStatus.Panes {
				wantType, ok := wantRestoredPanes[pane.Title]
				if !ok {
					t.Fatalf("topology-only restore returned unexpected pane: %+v", pane)
				}
				if pane.PaneID == "" || pane.Type != wantType || !tmux.PaneCommandIsShell(pane.Command) {
					t.Fatalf("topology-only restored pane = %+v, want title=%q type=%q shell command",
						pane, pane.Title, wantType)
				}
				delete(wantRestoredPanes, pane.Title)
			}
			if len(wantRestoredPanes) != 0 {
				t.Fatalf("topology-only restore omitted panes: %+v", wantRestoredPanes)
			}
			fixture.assertNoLaunchDelta(t, topologyArgvStart)
			if topologyStdinAfter := fixture.readFile(t, fixture.stdinLog); !bytes.Equal(topologyStdinBefore, topologyStdinAfter) {
				t.Fatalf("topology-only restore wrote Grok stdin: before=%q after=%q",
					topologyStdinBefore, topologyStdinAfter)
			}
			fixture.assertNoAgentInput(t)

			launchRestoreTarget := fixture.sessionName("saved-launch-restore")
			restored := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--json", "sessions", "restore", savedName,
				"--name="+launchRestoreTarget, "--launch", "--force", "--skip-git-check",
			)
			fixture.requireSingleShellJSONFailure(t, restored)
			if fixture.sessionExists(t, launchRestoreTarget) {
				t.Fatalf("rejected Grok saved-session restore created %q", launchRestoreTarget)
			}

			resumeTarget := fixture.sessionName("saved-resume")
			resumed := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--json", "sessions", "resume", savedName,
				"--name="+resumeTarget, "--force", "--native",
			)
			fixture.requireSingleShellJSONFailure(t, resumed)
			if fixture.sessionExists(t, resumeTarget) {
				t.Fatalf("rejected Grok saved-session resume created %q", resumeTarget)
			}

			checkpointed := fixture.runNTM(t, hardeningDir,
				"--json", "checkpoint", "save", hardeningSession,
				"--scrollback=20", "--no-git",
			)
			fixture.requireSuccess(t, checkpointed)
			var checkpointEnvelope struct {
				ID        string `json:"id"`
				Session   string `json:"session"`
				PaneCount int    `json:"pane_count"`
			}
			fixture.decodeSingleJSON(t, checkpointed.stdout, &checkpointEnvelope)
			if checkpointEnvelope.ID == "" || checkpointEnvelope.Session != hardeningSession ||
				checkpointEnvelope.PaneCount != 2 {
				t.Fatalf("Grok checkpoint envelope = %+v", checkpointEnvelope)
			}
			for _, tc := range []struct {
				name  string
				extra []string
			}{
				{name: "dry_run", extra: []string{"--dry-run"}},
				{name: "launch", extra: []string{"--force", "--skip-git-check"}},
			} {
				t.Run("checkpoint_restore_"+tc.name, func(t *testing.T) {
					args := []string{"--json", "checkpoint", "restore", hardeningSession, checkpointEnvelope.ID}
					args = append(args, tc.extra...)
					checkpointRestore := fixture.assertGrokFailureWithoutMutation(t, hardeningDir, args...)
					fixture.requireSingleShellJSONFailure(t, checkpointRestore)
				})
			}
		})

		t.Run("respawn_stuck_health_and_diagnose_fix_reject_mixed_batch", func(t *testing.T) {
			respawned := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"respawn", hardeningSession, "--force",
			)
			if respawned.exitCode != 1 {
				t.Fatalf("shell Grok respawn exit=%d, want 1", respawned.exitCode)
			}

			healthThreshold := 30 * time.Second
			fixture.waitForPaneIdleAtLeast(t, grokPane.PaneID, healthThreshold+time.Second)
			threshold := healthThreshold.String()

			beforeDryRun := fixture.mutationState(t)
			plainDryRun := fixture.runNTM(t, hardeningDir,
				"health", hardeningSession, "--auto-restart-stuck", "--threshold="+threshold, "--dry-run",
			)
			fixture.requireSuccess(t, plainDryRun)
			plainDryRunText := string(plainDryRun.stdout)
			if !strings.Contains(strings.ToLower(plainDryRunText), "dry run") ||
				!strings.Contains(plainDryRunText, strconv.Itoa(grokPane.Index)) {
				t.Fatalf("human health dry-run omitted Grok candidate: %s", plainDryRunText)
			}
			fixture.assertMutationStateEqual(t, beforeDryRun, fixture.mutationState(t))

			beforeJSONDryRun := fixture.mutationState(t)
			jsonDryRun := fixture.runNTM(t, hardeningDir,
				"--json", "health", hardeningSession, "--auto-restart-stuck", "--threshold="+threshold, "--dry-run",
			)
			fixture.requireSuccess(t, jsonDryRun)
			var dryRunEnvelope struct {
				Success    bool  `json:"success"`
				DryRun     bool  `json:"dry_run"`
				StuckPanes []int `json:"stuck_panes"`
			}
			fixture.decodeSingleJSON(t, jsonDryRun.stdout, &dryRunEnvelope)
			if !dryRunEnvelope.Success || !dryRunEnvelope.DryRun || !containsInt(dryRunEnvelope.StuckPanes, grokPane.Index) {
				t.Fatalf("JSON health dry-run envelope = %+v", dryRunEnvelope)
			}
			fixture.assertMutationStateEqual(t, beforeJSONDryRun, fixture.mutationState(t))

			beforePlainHealth := fixture.mutationState(t)
			plainHealthRestart := fixture.runNTM(t, hardeningDir,
				"health", hardeningSession, "--auto-restart-stuck", "--threshold="+threshold,
			)
			if plainHealthRestart.exitCode != 1 ||
				!spawnGrokTextNamesCapability(string(plainHealthRestart.stdout)+"\n"+string(plainHealthRestart.stderr)) {
				t.Fatalf("human Grok health restart = %+v, want capability failure", plainHealthRestart)
			}
			fixture.assertMutationStateEqual(t, beforePlainHealth, fixture.mutationState(t))

			humanHealthRestart := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--json", "health", hardeningSession, "--auto-restart-stuck", "--threshold="+threshold,
			)
			fixture.requireUnavailableJSONFailure(t, humanHealthRestart, agent.GrokPhaseOneCapabilityHint)

			healthRestart := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--robot-format=json", "--robot-health-restart-stuck="+hardeningSession,
				"--stuck-threshold="+threshold,
			)
			fixture.requireUnavailableJSONFailure(t, healthRestart, agent.GrokPhaseOneCapabilityHint)

			fixture.interruptPaneForCrashFixture(t, grokPane.PaneID)
			fixture.waitForPaneCommandChange(t, grokPane.PaneID, "grok")
			diagnosis := fixture.runNTM(t, hardeningDir,
				"--robot-format=json", "--robot-diagnose="+hardeningSession,
			)
			fixture.requireSuccess(t, diagnosis)
			var diagnosisEnvelope struct {
				Success      bool `json:"success"`
				AutoFixAvail bool `json:"auto_fix_available"`
			}
			fixture.decodeSingleJSON(t, diagnosis.stdout, &diagnosisEnvelope)
			if !diagnosisEnvelope.Success || !diagnosisEnvelope.AutoFixAvail {
				t.Fatalf("mixed Grok diagnose fixture is not auto-fixable: %+v raw=%s",
					diagnosisEnvelope, diagnosis.stdout)
			}

			diagnosed := fixture.assertGrokFailureWithoutMutation(t, hardeningDir,
				"--robot-format=json", "--robot-diagnose="+hardeningSession, "--fix",
			)
			fixture.requireUnavailableJSONFailure(t, diagnosed, agent.GrokPhaseOneCapabilityHint)
		})

		fixture.assertNoAgentInput(t)
	})

	t.Run("plain_spawn_skips_default_context_and_recovery_delivery", func(t *testing.T) {
		ambientSession := fixture.sessionName("ambient-context")
		ambientDir := fixture.prepareProject(t, ambientSession)
		recipeName := "ambient-grok-query"
		recipeBody := fmt.Sprintf(`[[recipes]]
name = %q
description = "Grok-only recipe that would become a default CASS query"
[[recipes.agents]]
type = "xai_grok_build"
count = 1
model = "grok-4-recipe"
reasoning_effort = "medium"
`, recipeName)
		if err := os.WriteFile(filepath.Join(ambientDir, ".ntm", "recipes.toml"), []byte(recipeBody), 0o600); err != nil {
			t.Fatalf("write Grok ambient-context recipe: %v", err)
		}
		if err := os.WriteFile(fixture.cassLog, nil, 0o600); err != nil {
			t.Fatalf("reset Grok ambient-context CASS invocation log: %v", err)
		}
		checkpointMarker := "GROK_RECOVERY_CONTEXT_MUST_NOT_SEND"
		// checkpoint.NewStorageWithDir was removed in the G1 dead-code
		// burndown (670f6380); BaseDir is exported, construct directly.
		checkpointStorage := &checkpoint.Storage{
			BaseDir: filepath.Join(fixture.root, "home", checkpoint.DefaultCheckpointDir),
		}
		ambientCheckpoint := &checkpoint.Checkpoint{
			Version:     checkpoint.CurrentVersion,
			ID:          "ambient-context",
			Name:        "ambient context",
			Description: checkpointMarker,
			SessionName: ambientSession,
			WorkingDir:  ambientDir,
			CreatedAt:   time.Now().UTC(),
			Session: checkpoint.SessionState{Panes: []checkpoint.PaneState{{
				Index:     0,
				ID:        "%ambient",
				Title:     ambientSession + "__cc_1",
				AgentType: "cc",
			}}},
			PaneCount: 1,
		}
		if err := checkpointStorage.Save(ambientCheckpoint); err != nil {
			t.Fatalf("write recoverable Grok checkpoint fixture: %v", err)
		}

		spawnArgvStart := fixture.launchLogPosition(t)
		spawned := fixture.runNTM(t, ambientDir,
			"--json", "spawn", ambientSession, "--recipe="+recipeName, "--no-user", "--no-hooks",
		)
		fixture.requireSuccess(t, spawned)
		var output struct {
			Session     string                     `json:"session"`
			Created     bool                       `json:"created"`
			Panes       []spawnGrokPaneJSON        `json:"panes"`
			AgentCounts spawnGrokCountsJSON        `json:"agent_counts"`
			Recovery    map[string]json.RawMessage `json:"recovery"`
		}
		fixture.decodeSingleJSON(t, spawned.stdout, &output)
		if output.Session != ambientSession || !output.Created ||
			output.AgentCounts.Grok != 1 || output.AgentCounts.Total != 1 ||
			len(output.Panes) != 1 {
			t.Fatalf("default-context Grok spawn envelope = %+v", output)
		}
		if output.Recovery != nil {
			t.Fatalf("Grok-only spawn unexpectedly resolved recovery context: %+v", output.Recovery)
		}
		recipePaneTitle := ambientSession + "__grok_1_grok-4-recipe"
		assertSpawnGrokPaneIdentity(t, output.Panes[0], recipePaneTitle, "grok-4-recipe")
		fixture.waitForPaneCommand(t, output.Panes[0].PaneID, "grok")
		fixture.waitForArgvDelta(t, spawnArgvStart, []string{
			"--always-approve\t--model\tgrok-4-recipe\t--effort\tmedium",
		})
		ambientStatus := fixture.readStatus(t, ambientDir, ambientSession)
		if len(ambientStatus.Panes) != 1 {
			t.Fatalf("default-context Grok status = %+v", ambientStatus)
		}
		assertSpawnGrokPaneJSON(t, ambientStatus.Panes[0], recipePaneTitle, "grok-4-recipe")
		if calls := strings.TrimSpace(string(fixture.readFile(t, fixture.cassLog))); calls != "" {
			t.Fatalf("Grok-only default context unexpectedly queried CASS: %s", calls)
		}
		fixture.assertNoAgentInput(t)
	})

	t.Run("doctor_sanitizes_hostile_version_and_treats_absence_as_optional", func(t *testing.T) {
		present := fixture.runNTM(t, cliDir, "--json", "doctor")
		fixture.requireSuccess(t, present)
		presentDep := decodeSpawnGrokDoctorDependency(t, fixture, present.stdout)
		if !presentDep.Installed || presentDep.Status != "ok" ||
			presentDep.Version != "grok 9.9.9 build 8b63" {
			t.Fatalf("present Grok doctor dependency = %+v", presentDep)
		}
		for _, unsafe := range []string{"\x1b", "\x00", "\n", "\t", "\u202e", "spoofed title"} {
			if strings.Contains(presentDep.Version, unsafe) {
				t.Fatalf("doctor emitted unsafe Grok version %q", presentDep.Version)
			}
		}

		absentPath := fixture.doctorPathWithoutGrok(t)
		absent := fixture.runNTMWithEnv(t, cliDir, map[string]string{"PATH": absentPath}, "--json", "doctor")
		fixture.requireSuccess(t, absent)
		absentDep := decodeSpawnGrokDoctorDependency(t, fixture, absent.stdout)
		if absentDep.Installed || absentDep.Status != "ok" || absentDep.Version != "" ||
			!strings.Contains(strings.ToLower(absentDep.Message), "optional") {
			t.Fatalf("absent Grok doctor dependency = %+v", absentDep)
		}
	})

	t.Run("adopt_capabilities_schema_and_completion_discover_grok", func(t *testing.T) {
		adoptSession := fixture.sessionName("adopt")
		adoptDir := fixture.prepareProject(t, adoptSession)
		fixture.createShellSession(t, adoptSession, adoptDir)
		adopted := fixture.runNTM(t, adoptDir, "--json", "adopt", adoptSession, "--grok=0")
		fixture.requireSuccess(t, adopted)
		var adoptOutput struct {
			Success bool `json:"success"`
			Agents  struct {
				Grok int `json:"grok"`
			} `json:"agents"`
			AdoptedPanes []struct {
				AgentType string `json:"agent_type"`
				NewTitle  string `json:"new_title"`
			} `json:"adopted_panes"`
		}
		fixture.decodeSingleJSON(t, adopted.stdout, &adoptOutput)
		if !adoptOutput.Success || adoptOutput.Agents.Grok != 1 || len(adoptOutput.AdoptedPanes) != 1 ||
			adoptOutput.AdoptedPanes[0].AgentType != "grok" ||
			adoptOutput.AdoptedPanes[0].NewTitle != adoptSession+"__grok_1" {
			t.Fatalf("Grok adopt envelope = %+v", adoptOutput)
		}
		adoptStatus := fixture.readStatus(t, adoptDir, adoptSession)
		if adoptStatus.AgentCounts.Grok != 1 || len(adoptStatus.Panes) != 1 ||
			adoptStatus.Panes[0].Title != adoptSession+"__grok_1" {
			t.Fatalf("Grok adopted status = %+v", adoptStatus)
		}

		capabilities := fixture.runNTM(t, cliDir,
			"--robot-format=json", "--robot-capabilities", "--capability-command=spawn")
		fixture.requireSuccess(t, capabilities)
		var capabilityOutput struct {
			Success  bool `json:"success"`
			Commands []struct {
				Parameters []struct {
					Flag        string `json:"flag"`
					Description string `json:"description"`
				} `json:"parameters"`
			} `json:"commands"`
		}
		fixture.decodeSingleJSON(t, capabilities.stdout, &capabilityOutput)
		foundGrok := false
		for _, command := range capabilityOutput.Commands {
			for _, parameter := range command.Parameters {
				if parameter.Flag == "--spawn-grok" {
					foundGrok = strings.Contains(strings.ToLower(parameter.Description), "launch only")
				}
			}
		}
		if !capabilityOutput.Success || !foundGrok {
			t.Fatalf("spawn capabilities omit Grok launch-only contract: %s", capabilities.stdout)
		}

		statusSchema := fixture.runNTM(t, cliDir, "--robot-format=json", "--robot-schema=status")
		fixture.requireSuccess(t, statusSchema)
		schemaText := strings.ToLower(string(statusSchema.stdout))
		if !strings.Contains(schemaText, `"summary"`) || !strings.Contains(schemaText, `"grok_count"`) {
			t.Fatalf("status schema omits Grok agent count: %s", statusSchema.stdout)
		}

		for _, command := range []string{"spawn", "add", "adopt"} {
			completion := fixture.runNTM(t, cliDir, "__complete", command, "--gro")
			fixture.requireSuccess(t, completion)
			if !strings.Contains(string(completion.stdout), "--grok") {
				t.Fatalf("%s completion omits --grok: stdout=%s stderr=%s", command, completion.stdout, completion.stderr)
			}
		}
		profileCompletion := fixture.runNTM(t, cliDir,
			"__complete", "profiles", "switch", "--session="+cliSession, "")
		fixture.requireSuccess(t, profileCompletion)
		if strings.Contains(strings.ToLower(string(profileCompletion.stdout)), "grok_") {
			t.Fatalf("profile-switch completion advertised unsupported Grok target: %s", profileCompletion.stdout)
		}
	})

	t.Run("launch_success_requires_a_stable_process", func(t *testing.T) {
		absentPath := fixture.doctorPathWithoutGrok(t)
		missingExecutable := filepath.Join(fixture.root, "missing-grok-bin", "grok-must-not-exist")
		missingConfig := fixture.grokCommandConfig(t, "missing-grok-config",
			tmux.ShellQuote(missingExecutable)+" --always-approve")

		missingSession := fixture.sessionName("missing-executable")
		missingDir := fixture.prepareProject(t, missingSession)
		missingArgvStart := fixture.launchLogPosition(t)
		missingAuditStart := len(fixture.readFile(t, fixture.tmuxAudit))
		missing := fixture.runNTMWithEnv(t, missingDir, map[string]string{
			"PATH":       absentPath,
			"NTM_CONFIG": missingConfig,
		},
			"--json", "spawn", missingSession, "--grok=1", "--no-user",
			"--no-hooks", "--no-cass-context", "--no-recovery",
		)
		fixture.requireStableProcessLaunchFailure(t, missing)
		missingAuditData := fixture.readFile(t, fixture.tmuxAudit)
		missingAudit := string(missingAuditData[missingAuditStart:])
		if !strings.Contains(missingAudit, missingExecutable) {
			t.Fatalf("CLI missing-executable audit omitted configured path %q: %s", missingExecutable, missingAudit)
		}
		fixture.assertNoLaunchDelta(t, missingArgvStart)

		robotSession := fixture.sessionName("robot-missing-executable")
		robotDir := fixture.prepareProject(t, robotSession)
		robotArgvStart := fixture.launchLogPosition(t)
		robotAuditStart := len(fixture.readFile(t, fixture.tmuxAudit))
		robotMissing := fixture.runNTMWithEnv(t, robotDir, map[string]string{
			"PATH":       absentPath,
			"NTM_CONFIG": missingConfig,
		},
			"--robot-format=json", "--robot-spawn="+robotSession, "--spawn-grok=1",
			"--spawn-no-user", "--spawn-dir="+robotDir,
		)
		fixture.requireStableProcessLaunchFailure(t, robotMissing)
		robotAuditData := fixture.readFile(t, fixture.tmuxAudit)
		robotAudit := string(robotAuditData[robotAuditStart:])
		if !strings.Contains(robotAudit, missingExecutable) {
			t.Fatalf("robot missing-executable audit omitted configured path %q: %s", missingExecutable, robotAudit)
		}
		fixture.assertNoLaunchDelta(t, robotArgvStart)

		immediateBin := filepath.Join(fixture.root, "immediate-exit-bin")
		if err := os.MkdirAll(immediateBin, 0o700); err != nil {
			t.Fatalf("create immediate-exit Grok PATH: %v", err)
		}
		immediateExecutable := filepath.Join(immediateBin, "grok")
		writeSpawnGrokImmediateExitAgent(t, immediateExecutable)
		immediateConfig := fixture.grokCommandConfig(t, "immediate-grok-config",
			tmux.ShellQuote(immediateExecutable)+" --always-approve")
		immediateArgvStart := fixture.launchLogPosition(t)
		immediateAuditStart := len(fixture.readFile(t, fixture.tmuxAudit))
		immediate := fixture.runNTMWithEnv(t, cliDir, map[string]string{
			"PATH":       immediateBin + string(os.PathListSeparator) + absentPath,
			"NTM_CONFIG": immediateConfig,
		}, "--json", "add", cliSession, "--grok=1", "--no-cass-context")
		fixture.requireStableProcessLaunchFailure(t, immediate)

		immediateAuditData := fixture.readFile(t, fixture.tmuxAudit)
		immediateAudit := string(immediateAuditData[immediateAuditStart:])
		if !strings.Contains(immediateAudit, immediateExecutable) {
			t.Fatalf("immediate-exit audit omitted configured path %q: %s", immediateExecutable, immediateAudit)
		}
		identities := strings.Split(strings.TrimSpace(string(fixture.readFile(t, fixture.failureLog))), "\n")
		if len(identities) != 1 || identities[0] == "" {
			t.Fatalf("immediate-exit Grok identities = %q, want exactly one", identities)
		}
		wantIdentity, err := os.Stat(immediateExecutable)
		if err != nil {
			t.Fatalf("stat immediate-exit Grok: %v", err)
		}
		gotIdentity, statErr := os.Stat(identities[0])
		if statErr != nil {
			t.Fatalf("stat recorded immediate-exit Grok identity %q: %v", identities[0], statErr)
		}
		if !os.SameFile(gotIdentity, wantIdentity) {
			t.Fatalf("immediate-exit Grok identity = %q, want %q", identities[0], immediateExecutable)
		}
		fixture.assertNoLaunchDelta(t, immediateArgvStart)
	})

	t.Run("plain_text_and_json_interrupt_grok", func(t *testing.T) {
		textSession := fixture.sessionName("interrupt-text")
		textDir := fixture.prepareProject(t, textSession)
		textArgvStart := fixture.launchLogPosition(t)
		textSpawn := fixture.runNTM(t, textDir,
			"--json", "spawn", textSession, "--grok=2", "--no-user",
			"--no-hooks", "--no-cass-context", "--no-recovery",
		)
		fixture.requireSuccess(t, textSpawn)
		fixture.waitForArgvDelta(t, textArgvStart, []string{
			"--always-approve",
			"--always-approve",
		})
		textStatus := fixture.readStatus(t, textDir, textSession)
		if textStatus.AgentCounts.Grok != 2 || textStatus.AgentCounts.Total != 2 || len(textStatus.Panes) != 2 {
			t.Fatalf("text-interrupt Grok fixture = %+v, want two Grok panes", textStatus)
		}

		jsonSession := fixture.sessionName("interrupt-json")
		jsonDir := fixture.prepareProject(t, jsonSession)
		jsonArgvStart := fixture.launchLogPosition(t)
		jsonSpawn := fixture.runNTM(t, jsonDir,
			"--robot-format=json", "--robot-spawn="+jsonSession,
			"--spawn-grok=1", "--spawn-no-user", "--spawn-dir="+jsonDir,
		)
		fixture.requireSuccess(t, jsonSpawn)
		fixture.waitForArgvDelta(t, jsonArgvStart, []string{"--always-approve"})
		jsonStatus := fixture.readStatus(t, jsonDir, jsonSession)
		if jsonStatus.AgentCounts.Grok != 1 || jsonStatus.AgentCounts.Total != 1 || len(jsonStatus.Panes) != 1 {
			t.Fatalf("JSON-interrupt Grok fixture = %+v, want one Grok pane", jsonStatus)
		}

		for _, pane := range append(append([]spawnGrokPaneJSON(nil), textStatus.Panes...), jsonStatus.Panes...) {
			if pane.PaneID == "" || pane.Type != "grok" || pane.Command != "grok" {
				t.Fatalf("Grok interrupt precondition pane = %+v, want live Grok", pane)
			}
		}
		fixture.assertNoAgentInput(t)

		plain := fixture.runNTM(t, textDir, "interrupt", textSession)
		fixture.requireSuccess(t, plain)
		wantPlain := fmt.Sprintf("Sent Ctrl+C to %d agent pane(s)", len(textStatus.Panes))
		if got := strings.TrimSpace(string(plain.stdout)); got != wantPlain {
			t.Fatalf("plain Grok interrupt output = %q, want %q; stderr=%s", got, wantPlain, plain.stderr)
		}

		structured := fixture.runNTM(t, jsonDir, "--json", "interrupt", jsonSession)
		fixture.requireSuccess(t, structured)
		var structuredOutput struct {
			Session       string `json:"session"`
			Interrupted   int    `json:"interrupted"`
			Skipped       int    `json:"skipped"`
			TargetedPanes []int  `json:"targeted_panes"`
		}
		fixture.decodeSingleJSON(t, structured.stdout, &structuredOutput)
		if structuredOutput.Session != jsonSession || structuredOutput.Interrupted != 1 || structuredOutput.Skipped != 0 ||
			len(structuredOutput.TargetedPanes) != 1 || structuredOutput.TargetedPanes[0] != jsonStatus.Panes[0].Index {
			t.Fatalf("structured Grok interrupt = %+v, want one exact target pane %d",
				structuredOutput, jsonStatus.Panes[0].Index)
		}

		for _, pane := range textStatus.Panes {
			fixture.waitForPaneCommandChange(t, pane.PaneID, "grok")
		}
		fixture.waitForPaneCommandChange(t, jsonStatus.Panes[0].PaneID, "grok")
		fixture.assertNoAgentInput(t)
	})
}

func newSpawnGrokE2EFixture(t *testing.T) *spawnGrokE2EFixture {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skipf("br is required for Grok assignment E2E coverage: %v", err)
	}
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	fixture := &spawnGrokE2EFixture{
		ntmPath:      ntmPath,
		tmuxPath:     tmuxPath,
		root:         root,
		projectDir:   filepath.Join(root, "project"),
		projectsBase: filepath.Join(root, "projects"),
		fakeBin:      filepath.Join(root, "bin"),
		configPath:   filepath.Join(root, "config", "ntm", "config.toml"),
		argvLog:      filepath.Join(root, "grok-argv.log"),
		stdinLog:     filepath.Join(root, "grok-stdin.log"),
		failureLog:   filepath.Join(root, "grok-failure.log"),
		cassLog:      filepath.Join(root, "cass-invocations.log"),
		tmuxAudit:    filepath.Join(root, "tmux-audit.log"),
	}
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{
		fixture.projectDir,
		fixture.projectsBase,
		fixture.fakeBin,
		homeDir,
		configDir,
		filepath.Join(configDir, "ntm"),
		filepath.Join(root, "data"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create Grok E2E directory %s: %v", dir, err)
		}
	}
	for _, path := range []string{fixture.argvLog, fixture.stdinLog, fixture.failureLog, fixture.cassLog, fixture.tmuxAudit} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("initialize Grok E2E log %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated Grok E2E shell\n"), 0o600); err != nil {
		t.Fatalf("write isolated Grok E2E shell config: %v", err)
	}
	configBody := strings.Join([]string{
		"[agent_mail]",
		"enabled = false",
		"",
		"[cass.context]",
		"enabled = true",
		"",
		"[recovery]",
		"enabled = true",
		"auto_inject_on_spawn = true",
		"include_agent_mail = false",
		"include_cm_memories = false",
		"include_beads_context = false",
		"",
		"[spawn_pacing]",
		"enabled = false",
		"",
		"[tmux]",
		"pane_init_delay_ms = 25",
		"",
		"[resilience]",
		"auto_restart = false",
		"",
	}, "\n")
	if err := os.WriteFile(fixture.configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write Grok E2E config: %v", err)
	}

	writeSpawnGrokFakeAgent(t, filepath.Join(fixture.fakeBin, "grok"))
	writeSpawnGrokFakeCass(t, filepath.Join(fixture.fakeBin, "cass"))
	tmuxWrapper := filepath.Join(fixture.fakeBin, "tmux-audit")
	writeSpawnGrokTMUXAuditWrapper(t, tmuxWrapper)
	fixture.env = spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fixture.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            fixture.projectsBase,
		"NTM_TMUX_BINARY":              tmuxWrapper,
		"NTM_E2E_REAL_TMUX":            tmuxPath,
		"NTM_E2E_TMUX_AUDIT":           fixture.tmuxAudit,
		"NTM_E2E_GROK_ARGV":            fixture.argvLog,
		"NTM_E2E_GROK_STDIN":           fixture.stdinLog,
		"NTM_E2E_GROK_FAILURE_LOG":     fixture.failureLog,
		"NTM_E2E_CASS_LOG":             fixture.cassLog,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
		"NTM_OUTPUT_FORMAT":            "",
		"NTM_ROBOT_FORMAT":             "",
		"TOON_DEFAULT_FORMAT":          "",
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.tmuxPath, "kill-server")
		cmd.Env = append([]string(nil), fixture.env...)
		output, err := cmd.CombinedOutput()
		if ctx.Err() == context.DeadlineExceeded {
			t.Errorf("Grok E2E private tmux cleanup timed out after %s: output=%s", defaultTmuxSetupTimeout, output)
			return
		}
		if err != nil && !isBenignTmuxCleanupError(output) {
			t.Errorf("Grok E2E private tmux cleanup failed: %v output=%s", err, output)
		}
	})
	return fixture
}

func writeSpawnGrokFakeAgent(t *testing.T, path string) {
	t.Helper()
	buildSpawnGrokProgram(t, path, `package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		_, _ = os.Stdout.Write([]byte("\x1b]0;spoofed title\x07\x1b[31mgrok 9.9.9\x1b[0m\n\tbuild 8b63\u202e\x00"))
		return
	}
	argv, err := os.OpenFile(os.Getenv("NTM_E2E_GROK_ARGV"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	_, _ = fmt.Fprintln(argv, strings.Join(os.Args[1:], "\t"))
	_ = argv.Close()
	fmt.Println("GROK_E2E_RUNNING")
	fmt.Println("Completed GROK_E2E_SUMMARY_INCLUDED")
	input, err := os.OpenFile(os.Getenv("NTM_E2E_GROK_STDIN"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer input.Close()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		_, _ = fmt.Fprintln(input, scanner.Text())
		fmt.Printf("UNEXPECTED_INPUT:%s\n", scanner.Text())
	}
}
`)
}

func writeSpawnGrokImmediateExitAgent(t *testing.T, path string) {
	t.Helper()
	buildSpawnGrokProgram(t, path, `package main

import (
	"fmt"
	"os"
)

func main() {
	logFile, err := os.OpenFile(os.Getenv("NTM_E2E_GROK_FAILURE_LOG"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	_, _ = fmt.Fprintln(logFile, executable)
}
`)
}

func writeSpawnGrokFakeCass(t *testing.T, path string) {
	t.Helper()
	buildSpawnGrokProgram(t, path, `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	logFile, err := os.OpenFile(os.Getenv("NTM_E2E_CASS_LOG"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	_, _ = fmt.Fprintln(logFile, strings.Join(os.Args[1:], "\t"))
	_ = logFile.Close()
	fmt.Print("{\"query\":\"\",\"count\":0,\"total_matches\":0,\"hits\":[]}")
}
`)
}

func buildSpawnGrokProgram(t *testing.T, path, source string) {
	t.Helper()
	sourcePath := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write fake Grok Go source: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", path, sourcePath)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for fake Grok build: %v", groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 10 * time.Second
	output, runErr := cmd.CombinedOutput()
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close owned process group for fake Grok build: %v output=%s", closeErr, output)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("build fake Grok executable timed out after 2m: output=%s", output)
	}
	if runErr != nil {
		t.Fatalf("build fake Grok executable: %v output=%s", runErr, output)
	}
}

func writeSpawnGrokTMUXAuditWrapper(t *testing.T, path string) {
	t.Helper()
	content := `#!/bin/sh
{
    first=1
    for arg do
        if [ "$first" -eq 0 ]; then
            printf '\t'
        fi
        printf '%s' "$arg"
        first=0
    done
    printf '\n'
} >> "$NTM_E2E_TMUX_AUDIT"
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write tmux audit wrapper: %v", err)
	}
}

func (f *spawnGrokE2EFixture) sessionName(suffix string) string {
	replacer := strings.NewReplacer("_", "-", "/", "-", " ", "-")
	return fmt.Sprintf("ntm-e2e-grok-%s-%d", replacer.Replace(suffix), time.Now().UnixNano())
}

func (f *spawnGrokE2EFixture) prepareProject(t *testing.T, session string) string {
	t.Helper()
	dir := filepath.Join(f.projectsBase, session)
	personaDir := filepath.Join(dir, ".ntm")
	if err := os.MkdirAll(personaDir, 0o700); err != nil {
		t.Fatalf("create Grok E2E project %s: %v", dir, err)
	}
	personas := `[[personas]]
name = "grok-reviewer"
description = "Unsupported Grok prompt persona"
agent_type = "grok"
model = "model-persona"
system_prompt = "This prompt must never be injected in phase one."
`
	if err := os.WriteFile(filepath.Join(personaDir, "personas.toml"), []byte(personas), 0o600); err != nil {
		t.Fatalf("write Grok E2E persona registry: %v", err)
	}
	return dir
}

func (f *spawnGrokE2EFixture) grokCommandConfig(t *testing.T, name, command string) string {
	t.Helper()
	baseline := f.readFile(t, f.configPath)
	path := filepath.Join(f.root, name+".toml")
	body := fmt.Sprintf("%s\n[agents]\ngrok = %s\n", baseline, strconv.Quote(command))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write Grok command config %s: %v", path, err)
	}
	return path
}

func (f *spawnGrokE2EFixture) grokDefaultModelConfig(t *testing.T, name, model string) string {
	t.Helper()
	baseline := f.readFile(t, f.configPath)
	path := filepath.Join(f.root, name+".toml")
	body := fmt.Sprintf("%s\n[models]\ndefault_grok = %s\n", baseline, strconv.Quote(model))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write Grok default-model config %s: %v", path, err)
	}
	return path
}

func (f *spawnGrokE2EFixture) runNTM(t *testing.T, dir string, args ...string) spawnGrokProcessResult {
	t.Helper()
	return f.runNTMWithEnv(t, dir, nil, args...)
}

func (f *spawnGrokE2EFixture) runBR(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "br", args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("Grok E2E br command timed out: args=%q stderr=%s", args, stderr.String())
	}
	if err != nil {
		t.Fatalf("Grok E2E br command failed: args=%q error=%v stdout=%s stderr=%s", args, err, output, stderr.String())
	}
	return output
}

func (f *spawnGrokE2EFixture) beadSnapshot(t *testing.T, dir string, beadIDs ...string) string {
	t.Helper()
	ids := append([]string(nil), beadIDs...)
	sort.Strings(ids)
	var parts []string
	for _, beadID := range ids {
		parts = append(parts, beadID+"\n"+string(f.runBR(t, dir, "show", beadID, "--json")))
	}
	return strings.Join(parts, "\n")
}

func (f *spawnGrokE2EFixture) sessionStateSnapshot(t *testing.T, session string) string {
	t.Helper()
	root := filepath.Join(f.root, "home", ".ntm", "sessions", session)
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return ""
	} else if err != nil {
		t.Fatalf("stat Grok E2E session state %s: %v", root, err)
	}
	var records []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(info.Name(), ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		records = append(records, "file\t"+rel+"\t"+hex.EncodeToString(data))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot Grok E2E session state %s: %v", root, err)
	}
	sort.Strings(records)
	return strings.Join(records, "\n")
}

func (f *spawnGrokE2EFixture) assertBeadAndLedgerStateEqual(
	t *testing.T,
	dir string,
	session string,
	wantBeads string,
	wantLedger string,
	beadIDs ...string,
) {
	t.Helper()
	if got := f.beadSnapshot(t, dir, beadIDs...); got != wantBeads {
		t.Fatalf("Grok assignment mutated Beads state:\nbefore=%s\nafter=%s", wantBeads, got)
	}
	if got := f.sessionStateSnapshot(t, session); got != wantLedger {
		t.Fatalf("Grok assignment mutated durable session ledger:\nbefore=%s\nafter=%s", wantLedger, got)
	}
}

func (f *spawnGrokE2EFixture) runNTMWithEnv(t *testing.T, dir string, extraEnv map[string]string, args ...string) spawnGrokProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	cmd.Dir = dir
	cmd.Env = spawnAssignmentMergeEnv(f.env, extraEnv)
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for Grok E2E ntm %q: %v", args, groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	commandErr := cmd.Run()
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close owned process group for Grok E2E ntm %q: %v", args, closeErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("Grok E2E ntm command timed out: args=%q stdout=%s stderr=%s", args, stdout.String(), stderr.String())
	}
	exitCode := 0
	if commandErr != nil {
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run Grok E2E ntm command %q: %v", args, commandErr)
		}
	}
	t.Logf("[E2E-GROK] exit=%d args=%q stdout=%s stderr=%s", exitCode, args,
		truncateString(stdout.String(), 800), truncateString(stderr.String(), 800))
	return spawnGrokProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func (f *spawnGrokE2EFixture) requireSuccess(t *testing.T, result spawnGrokProcessResult) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("Grok E2E command exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
}

func (f *spawnGrokE2EFixture) requireStableProcessLaunchFailure(t *testing.T, result spawnGrokProcessResult) {
	t.Helper()
	if result.exitCode == 0 {
		t.Fatalf("unstable Grok launch exited zero: stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	combined := strings.ToLower(string(result.stdout) + "\n" + string(result.stderr))
	if !strings.Contains(combined, "stable process") || !strings.Contains(combined, "did not keep a non-shell process") {
		t.Fatalf("unstable Grok launch omitted process-start evidence: stdout=%s stderr=%s", result.stdout, result.stderr)
	}
	if payload := bytes.TrimSpace(result.stdout); len(payload) > 0 {
		var envelope map[string]any
		f.decodeSingleJSON(t, payload, &envelope)
		if success, exists := envelope["success"]; exists && success != false {
			t.Fatalf("unstable Grok launch reported success: %+v", envelope)
		}
	}
}

func (f *spawnGrokE2EFixture) decodeSingleJSON(t *testing.T, data []byte, dst any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(data)))
	if err := decoder.Decode(dst); err != nil {
		t.Fatalf("decode Grok E2E JSON: %v raw=%s", err, data)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("Grok E2E output contains trailing JSON: err=%v trailing=%v raw=%s", err, trailing, data)
	}
}

func assertSpawnGrokPaneJSON(t *testing.T, pane spawnGrokPaneJSON, title, variant string) {
	t.Helper()
	assertSpawnGrokPaneIdentity(t, pane, title, variant)
	if pane.Command != "grok" {
		t.Fatalf("Grok pane = %+v, want title=%q variant=%q current_command=grok", pane, title, variant)
	}
}

func assertSpawnGrokPaneIdentity(t *testing.T, pane spawnGrokPaneJSON, title, variant string) {
	t.Helper()
	if pane.PaneID == "" || pane.Title != title || pane.Type != "grok" || pane.Variant != variant {
		t.Fatalf("Grok pane = %+v, want title=%q type=grok variant=%q", pane, title, variant)
	}
}

func (f *spawnGrokE2EFixture) readStatus(t *testing.T, dir, session string) spawnGrokStatusJSON {
	t.Helper()
	result := f.runNTM(t, dir, "--json", "status", session)
	f.requireSuccess(t, result)
	var status spawnGrokStatusJSON
	f.decodeSingleJSON(t, result.stdout, &status)
	if status.Session != session || !status.Exists {
		t.Fatalf("Grok session status = %+v", status)
	}
	return status
}

func (f *spawnGrokE2EFixture) readArgvLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.argvLog)
	if err != nil {
		t.Fatalf("read fake Grok argv log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (f *spawnGrokE2EFixture) launchLogPosition(t *testing.T) int {
	t.Helper()
	return len(f.readArgvLines(t))
}

func (f *spawnGrokE2EFixture) waitForArgvDelta(t *testing.T, start int, want []string) {
	t.Helper()
	deadline := time.Now().Add(defaultTmuxSetupTimeout)
	for {
		all := f.readArgvLines(t)
		if start < 0 || start > len(all) {
			t.Fatalf("fake Grok argv start=%d is outside log with %d line(s): %v", start, len(all), all)
		}
		wantEnd := start + len(want)
		if len(all) > wantEnd {
			t.Fatalf("fake Grok argv delta has %d line(s), want %d; delta=%v all=%v",
				len(all)-start, len(want), all[start:], all)
		}
		if len(all) == wantEnd {
			got := all[start:]
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("fake Grok argv delta[%d]=%q, want %q; delta=%v all=%v",
						i, got[i], want[i], got, all)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for fake Grok argv delta %v at position %d; all=%v",
				want, start, all)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *spawnGrokE2EFixture) assertNoLaunchDelta(t *testing.T, start int) {
	t.Helper()
	all := f.readArgvLines(t)
	if start < 0 || start > len(all) {
		t.Fatalf("fake Grok argv start=%d is outside log with %d line(s): %v", start, len(all), all)
	}
	if delta := all[start:]; len(delta) != 0 {
		t.Fatalf("operation unexpectedly launched Grok with argv delta=%v; all=%v", delta, all)
	}
}

func (f *spawnGrokE2EFixture) assertNoAgentInput(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(f.stdinLog)
	if err != nil {
		t.Fatalf("read fake Grok stdin log: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("phase-one Grok unexpectedly received stdin: %q", data)
	}
}

func (f *spawnGrokE2EFixture) mutationState(t *testing.T) spawnGrokMutationState {
	t.Helper()
	return spawnGrokMutationState{
		topology:      f.globalPaneSnapshot(t),
		mutatingAudit: f.mutatingTMUXAudit(t),
		durableState:  f.snapshotDurableLifecycleState(t),
		argv:          f.readFile(t, f.argvLog),
		stdin:         f.readFile(t, f.stdinLog),
	}
}

func (f *spawnGrokE2EFixture) snapshotDurableLifecycleState(t *testing.T) string {
	t.Helper()
	roots := []string{
		filepath.Join(f.root, "home", ".ntm", "sessions"),
		filepath.Join(f.root, "home", checkpoint.DefaultCheckpointDir),
		filepath.Join(f.root, "data", "ntm", "manifests"),
	}
	var records []string
	for _, root := range roots {
		label, err := filepath.Rel(f.root, root)
		if err != nil {
			t.Fatalf("label Grok durable state root %s: %v", root, err)
		}
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatalf("stat Grok durable state root %s: %v", root, err)
		}
		err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			key := filepath.ToSlash(filepath.Join(label, rel))
			if info.IsDir() || strings.HasSuffix(info.Name(), ".lock") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			records = append(records, "file\t"+key+"\t"+hex.EncodeToString(data))
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot Grok durable state root %s: %v", root, err)
		}
	}
	sort.Strings(records)
	return strings.Join(records, "\n")
}

func (f *spawnGrokE2EFixture) readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Grok E2E state %s: %v", path, err)
	}
	return data
}

func (f *spawnGrokE2EFixture) globalPaneSnapshot(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "list-panes", "-a", "-F",
		"#{session_name}\x1f#{pane_id}\x1f#{window_index}\x1f#{pane_index}\x1f#{pane_title}\x1f#{pane_current_command}\x1f#{pane_pid}")
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture Grok E2E tmux topology: %v output=%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (f *spawnGrokE2EFixture) mutatingTMUXAudit(t *testing.T) string {
	t.Helper()
	data := f.readFile(t, f.tmuxAudit)
	mutating := map[string]bool{
		"break-pane": true, "join-pane": true, "kill-pane": true, "kill-session": true,
		"kill-server": true, "link-window": true, "move-pane": true, "new-session": true, "new-window": true,
		"delete-buffer": true, "display-popup": true, "load-buffer": true, "paste-buffer": true,
		"pipe-pane": true, "resize-pane": true, "resize-window": true, "rotate-window": true, "run-shell": true,
		"rename-session": true, "rename-window": true, "respawn-pane": true,
		"respawn-window": true, "select-layout": true, "select-pane": true, "send-keys": true,
		"set-buffer": true, "set-environment": true, "set-option": true, "set-window-option": true,
		"split-window": true, "swap-pane": true, "unlink-window": true,
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		command := line
		if idx := strings.IndexByte(command, '\t'); idx >= 0 {
			command = command[:idx]
		}
		if mutating[command] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func (f *spawnGrokE2EFixture) assertMutationStateEqual(t *testing.T, before, after spawnGrokMutationState) {
	t.Helper()
	if before.topology != after.topology {
		t.Fatalf("Grok rejection mutated tmux topology:\nbefore=%q\nafter=%q", before.topology, after.topology)
	}
	if before.mutatingAudit != after.mutatingAudit {
		t.Fatalf("Grok rejection issued a mutating tmux command:\nbefore=%q\nafter=%q", before.mutatingAudit, after.mutatingAudit)
	}
	if before.durableState != after.durableState {
		t.Fatalf("Grok rejection mutated durable lifecycle state:\nbefore=%q\nafter=%q", before.durableState, after.durableState)
	}
	if !bytes.Equal(before.argv, after.argv) {
		t.Fatalf("Grok rejection launched or relaunched a process: before=%q after=%q", before.argv, after.argv)
	}
	if !bytes.Equal(before.stdin, after.stdin) {
		t.Fatalf("Grok rejection wrote agent stdin: before=%q after=%q", before.stdin, after.stdin)
	}
}

func (f *spawnGrokE2EFixture) assertGrokFailureWithoutMutation(t *testing.T, dir string, args ...string) spawnGrokProcessResult {
	t.Helper()
	before := f.mutationState(t)
	result := f.runNTM(t, dir, args...)
	if result.exitCode == 0 {
		t.Fatalf("unsupported Grok command exited zero: args=%q stdout=%s stderr=%s", args, result.stdout, result.stderr)
	}
	if !spawnGrokTextNamesCapability(string(result.stdout) + "\n" + string(result.stderr)) {
		t.Fatalf("unsupported Grok command omitted capability error: args=%q stdout=%s stderr=%s", args, result.stdout, result.stderr)
	}
	if payload := bytes.TrimSpace(result.stdout); len(payload) > 0 {
		var envelope map[string]any
		f.decodeSingleJSON(t, payload, &envelope)
		if success, exists := envelope["success"]; exists && success != false {
			t.Fatalf("unsupported Grok command reported success: args=%q envelope=%+v", args, envelope)
		}
	}
	f.assertMutationStateEqual(t, before, f.mutationState(t))
	return result
}

func (f *spawnGrokE2EFixture) requireUnavailableJSONFailure(
	t *testing.T,
	result spawnGrokProcessResult,
	wantHint string,
) {
	t.Helper()
	if result.exitCode != 2 {
		t.Fatalf("Grok JSON capability exit=%d, want 2; stdout=%s stderr=%s",
			result.exitCode, result.stdout, result.stderr)
	}
	var envelope map[string]any
	f.decodeSingleJSON(t, result.stdout, &envelope)
	if envelope["success"] != false || envelope["error_code"] != "NOT_IMPLEMENTED" ||
		!spawnGrokTextNamesCapability(envelope) {
		t.Fatalf("Grok JSON capability envelope = %+v", envelope)
	}
	hint, _ := envelope["hint"].(string)
	if hint != wantHint {
		t.Fatalf("Grok capability hint = %q, want %q; envelope=%+v", hint, wantHint, envelope)
	}
}

func (f *spawnGrokE2EFixture) requireSingleShellJSONFailure(t *testing.T, result spawnGrokProcessResult) {
	t.Helper()
	if result.exitCode != 1 {
		t.Fatalf("Grok shell JSON capability exit=%d, want 1; stdout=%s stderr=%s",
			result.exitCode, result.stdout, result.stderr)
	}
	var envelope map[string]any
	f.decodeSingleJSON(t, result.stdout, &envelope)
	if envelope["success"] != false || !spawnGrokTextNamesCapability(envelope) {
		t.Fatalf("Grok shell JSON capability envelope = %+v", envelope)
	}
}

func spawnGrokTextNamesCapability(value any) bool {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	default:
		encoded, _ := json.Marshal(typed)
		text = string(encoded)
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "grok") &&
		(strings.Contains(lower, "not implemented") || strings.Contains(lower, "not support") ||
			strings.Contains(lower, "unsupported") || strings.Contains(lower, "phase one") ||
			strings.Contains(lower, "phase-one"))
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (f *spawnGrokE2EFixture) sessionExists(t *testing.T, session string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "has-session", "-t", session)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("check Grok E2E session %q timed out after %s: output=%s", session, defaultTmuxSetupTimeout, output)
	}
	if isBenignTmuxCleanupError(output) {
		return false
	}
	t.Fatalf("check Grok E2E session %q: %v output=%s", session, err, output)
	return false
}

func (f *spawnGrokE2EFixture) createShellSession(t *testing.T, session, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "new-session", "-d", "-s", session, "-c", dir)
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create Grok E2E shell session %q: %v output=%s", session, err, output)
	}
}

func (f *spawnGrokE2EFixture) addTitledShellPane(t *testing.T, session, dir, title string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath,
		"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", session, "-c", dir)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create mixed-batch shell pane: %v output=%s", err, output)
	}
	paneID := strings.TrimSpace(string(output))
	if paneID == "" {
		t.Fatal("create mixed-batch shell pane returned no pane ID")
	}

	ctx, cancel = context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd = exec.CommandContext(ctx, f.tmuxPath, "select-pane", "-t", paneID, "-T", title)
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("title mixed-batch shell pane %s: %v output=%s", paneID, err, output)
	}
	return paneID
}

func (f *spawnGrokE2EFixture) prepareReviewCommits(t *testing.T, dir string, count int) {
	t.Helper()
	f.runGit(t, dir, "init", "-b", "main")
	f.runGit(t, dir, "config", "user.name", "NTM Grok E2E")
	f.runGit(t, dir, "config", "user.email", "ntm-grok-e2e@example.invalid")
	f.runGit(t, dir, "config", "commit.gpgsign", "false")
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("review-source-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte(fmt.Sprintf("review source %d\n", i)), 0o600); err != nil {
			t.Fatalf("write review source %d: %v", i, err)
		}
		f.runGit(t, dir, "add", "--", name)
		f.runGit(t, dir, "commit", "-m", fmt.Sprintf("review source %d", i))
	}
}

func (f *spawnGrokE2EFixture) runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepare Grok review fixture: git %q: %v output=%s", args, err, output)
	}
}

func (f *spawnGrokE2EFixture) interruptPaneForCrashFixture(t *testing.T, paneID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "send-keys", "-t", paneID, "C-c")
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("interrupt fake Grok crash fixture %s: %v output=%s", paneID, err, output)
	}
}

func (f *spawnGrokE2EFixture) waitForPaneCommandChange(t *testing.T, paneID, previous string) {
	t.Helper()
	deadline := time.Now().Add(defaultTmuxSetupTimeout)
	var lastCommand string
	var lastErr error
	var lastOutput []byte
	for {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		cmd := exec.CommandContext(ctx, f.tmuxPath, "display-message", "-p", "-t", paneID, "#{pane_current_command}")
		cmd.Env = append([]string(nil), f.env...)
		output, err := cmd.CombinedOutput()
		cancel()
		lastErr = err
		lastOutput = append(lastOutput[:0], output...)
		if err == nil {
			lastCommand = strings.TrimSpace(string(output))
			if lastCommand != "" && lastCommand != previous {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s current command=%q did not change from %q: error=%v output=%s", paneID, lastCommand, previous, lastErr, lastOutput)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *spawnGrokE2EFixture) waitForPaneCommand(t *testing.T, paneID, want string) {
	t.Helper()
	deadline := time.Now().Add(defaultTmuxSetupTimeout)
	var lastCommand string
	var lastErr error
	var lastOutput []byte
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, f.tmuxPath, "display-message", "-p", "-t", paneID, "#{pane_current_command}")
		cmd.Env = append([]string(nil), f.env...)
		output, err := cmd.CombinedOutput()
		cancel()
		lastErr = err
		lastOutput = append(lastOutput[:0], output...)
		if err == nil {
			lastCommand = strings.TrimSpace(string(output))
			if lastCommand == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s current command=%q, want %q: error=%v output=%s", paneID, lastCommand, want, lastErr, lastOutput)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *spawnGrokE2EFixture) waitForPaneIdleAtLeast(t *testing.T, paneID string, minimum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(minimum + defaultTmuxSetupTimeout)
	var idleFor time.Duration
	var lastErr error
	var lastOutput []byte
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, f.tmuxPath, "display-message", "-p", "-t", paneID, "#{window_activity}")
		cmd.Env = append([]string(nil), f.env...)
		output, err := cmd.CombinedOutput()
		cancel()
		lastErr = err
		lastOutput = append(lastOutput[:0], output...)
		if err == nil {
			activityUnix, parseErr := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
			if parseErr != nil {
				t.Fatalf("parse pane %s activity timestamp %q: %v", paneID, output, parseErr)
			}
			idleFor = time.Since(time.Unix(activityUnix, 0))
			if idleFor >= minimum {
				t.Logf("[E2E-GROK] pane %s idle for %s", paneID, idleFor.Round(time.Second))
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s idle for %s, want at least %s: error=%v output=%s", paneID, idleFor, minimum, lastErr, lastOutput)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *spawnGrokE2EFixture) startServer(t *testing.T, dir string) (string, *http.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate Grok REST E2E port: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	logPath := filepath.Join(f.root, "grok-serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create Grok REST E2E log: %v", err)
	}
	serverCtx, cancelServer := context.WithCancel(context.Background())
	cmd := exec.CommandContext(serverCtx, f.ntmPath, "serve", "--host=127.0.0.1", fmt.Sprintf("--port=%d", port))
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	group, groupErr := testutil.NewProcessGroupForTest(serverCtx, cmd)
	if groupErr != nil {
		cancelServer()
		_ = logFile.Close()
		t.Fatalf("create owned process group for Grok REST E2E server: %v", groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 2 * time.Second
	if err := listener.Close(); err != nil {
		cancelServer()
		closeErr := group.Close()
		_ = logFile.Close()
		t.Fatalf("release Grok REST E2E port: %v; close owned process group: %v", err, closeErr)
	}
	if err := cmd.Start(); err != nil {
		cancelServer()
		closeErr := group.Close()
		_ = logFile.Close()
		t.Fatalf("start Grok REST E2E server: %v; close owned process group: %v", err, closeErr)
	}
	type serverWaitResult struct {
		commandErr error
		closeErr   error
	}
	done := make(chan serverWaitResult, 1)
	go func() {
		done <- serverWaitResult{
			commandErr: cmd.Wait(),
			closeErr:   group.Close(),
		}
	}()
	t.Cleanup(func() {
		cancelServer()
		_ = group.Signal(os.Kill)
		select {
		case result := <-done:
			if result.closeErr != nil {
				t.Errorf("close owned process group for Grok REST E2E server: %v (command error: %v)", result.closeErr, result.commandErr)
			}
		case <-time.After(45 * time.Second):
			t.Errorf("Grok REST E2E server did not stop within 45s after process-group cancellation")
		}
		_ = logFile.Close()
	})

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport}
	t.Cleanup(client.CloseIdleConnections)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(defaultTmuxSetupTimeout)
	for {
		request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/health", nil)
		if requestErr != nil {
			t.Fatalf("create Grok REST readiness request: %v", requestErr)
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return baseURL, client
			}
		}
		if time.Now().After(deadline) {
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("Grok REST server did not become ready: %v log=%s", requestErr, logData)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *spawnGrokE2EFixture) postRESTSpawn(t *testing.T, client *http.Client, baseURL, session, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		baseURL+"/api/v1/sessions/"+url.PathEscape(session)+"/agents/spawn", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create Grok REST spawn request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Grok REST spawn request: %v", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read Grok REST spawn response: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close Grok REST spawn response: %v", closeErr)
	}
	var envelope map[string]any
	f.decodeSingleJSON(t, data, &envelope)
	return response.StatusCode, envelope
}

func (f *spawnGrokE2EFixture) postRESTPaneInput(
	t *testing.T,
	client *http.Client,
	baseURL, session string,
	pane int,
	body string,
) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		fmt.Sprintf("%s/api/v1/sessions/%s/panes/%d/input", baseURL, url.PathEscape(session), pane),
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("create Grok REST pane-input request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Grok REST pane-input request: %v", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read Grok REST pane-input response: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close Grok REST pane-input response: %v", closeErr)
	}
	var envelope map[string]any
	f.decodeSingleJSON(t, data, &envelope)
	return response.StatusCode, envelope
}

func (f *spawnGrokE2EFixture) postRESTAgentAction(
	t *testing.T,
	client *http.Client,
	baseURL, session, action, body string,
) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		fmt.Sprintf("%s/api/v1/sessions/%s/agents/%s", baseURL, url.PathEscape(session), url.PathEscape(action)),
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("create Grok REST %s request: %v", action, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Grok REST %s request: %v", action, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		t.Fatalf("read Grok REST %s response: %v", action, readErr)
	}
	if closeErr != nil {
		t.Fatalf("close Grok REST %s response: %v", action, closeErr)
	}
	var envelope map[string]any
	f.decodeSingleJSON(t, data, &envelope)
	return response.StatusCode, envelope
}

type spawnGrokDoctorDependency struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

func decodeSpawnGrokDoctorDependency(t *testing.T, fixture *spawnGrokE2EFixture, data []byte) spawnGrokDoctorDependency {
	t.Helper()
	var report struct {
		Dependencies []spawnGrokDoctorDependency `json:"dependencies"`
	}
	fixture.decodeSingleJSON(t, data, &report)
	for _, dependency := range report.Dependencies {
		if dependency.Name == "grok" {
			return dependency
		}
	}
	t.Fatalf("doctor output omits Grok dependency: %s", data)
	return spawnGrokDoctorDependency{}
}

func (f *spawnGrokE2EFixture) doctorPathWithoutGrok(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(f.root, "doctor-without-grok")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create Grok-absent doctor PATH: %v", err)
	}
	goPath, err := exec.LookPath("go")
	if err == nil {
		if err := os.Symlink(goPath, filepath.Join(dir, "go")); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatalf("link Go into Grok-absent doctor PATH: %v", err)
		}
	}
	return dir
}

// TestE2EAtomicSpawnAssignmentProductionCLI crosses the complete production spawn
// assignment path: a built ntm process, private real tmux server, real br
// database, persisted pane identity, concrete Agent Mail HTTP client, atomic
// ledger, and tmux prompt delivery. The replay is a second OS process.
func TestE2EAtomicSpawnAssignmentProductionCLI(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newSpawnAssignmentCLIFixture(t)
	args := fixture.spawnArgs()

	firstResult := fixture.runNTM(t, args...)
	first := decodeSpawnAssignmentOutput(t, firstResult)
	firstAssignment := assertSpawnAssignmentOutput(t, first, fixture)
	fixture.waitForMarkerCount(t, 1)
	fixture.assertBead(t, "in_progress", firstAssignment.ClaimActor)

	firstRecord := fixture.readAssignment(t)
	assertSpawnAssignmentRecord(t, firstRecord, firstAssignment, fixture)
	firstMCPCounts := fixture.stub.assertCleanMutationCount(t, 1)

	firstDispatchedAt := *firstRecord.DispatchedAt
	firstReservationExpiresAt := *firstRecord.ReservationExpiresAt

	// Re-run the identical spawn intent from a fresh process. The existing
	// session is intentionally reused; its launch command is harmless input to
	// the fake agent, while the work marker must not be delivered again.
	secondResult := fixture.runNTM(t, args...)
	second := decodeSpawnAssignmentOutput(t, secondResult)
	secondAssignment := assertSpawnAssignmentOutput(t, second, fixture)

	if secondAssignment.IdempotencyKey != firstAssignment.IdempotencyKey ||
		secondAssignment.ClaimActor != firstAssignment.ClaimActor ||
		secondAssignment.DispatchReceiptID != firstAssignment.DispatchReceiptID ||
		!equalInts(secondAssignment.ReservationIDs, firstAssignment.ReservationIDs) {
		t.Fatalf("spawn replay identity changed: first=%+v second=%+v", firstAssignment, secondAssignment)
	}
	fixture.assertMarkerCount(t, 1)
	fixture.assertBead(t, "in_progress", firstAssignment.ClaimActor)
	secondMCPCounts := fixture.stub.assertCleanMutationCount(t, 1)
	if secondMCPCounts.ensure < firstMCPCounts.ensure || secondMCPCounts.list < firstMCPCounts.list {
		t.Fatalf("Agent Mail discovery counters moved backwards: first=%+v second=%+v", firstMCPCounts, secondMCPCounts)
	}

	replayed := fixture.readAssignment(t)
	if replayed.ClaimAttempts != 1 || replayed.ReservationAttempts != 1 || replayed.DispatchAttempts != 1 ||
		replayed.IdempotencyKey != firstRecord.IdempotencyKey ||
		replayed.DispatchReceiptID != firstRecord.DispatchReceiptID ||
		replayed.DispatchedAt == nil || !replayed.DispatchedAt.Equal(firstDispatchedAt) ||
		replayed.ReservationExpiresAt == nil || !replayed.ReservationExpiresAt.Equal(firstReservationExpiresAt) {
		t.Fatalf("spawn replay mutated durable side-effect receipts: before=%+v after=%+v", firstRecord, replayed)
	}
}

// TestE2EAtomicSpawnAssignmentSafetyPreflightBuiltBinary proves the built robot CLI
// resolves assignment policy and verifies plan membership plus live labels
// before either dry-run preview or any tmux/Beads/ledger/mail actuation.
func TestE2EAtomicSpawnAssignmentSafetyPreflightBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("invalid_or_missing_selected_config_is_pre_tmux", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			dryRun        bool
			configure     func(*testing.T, *spawnAssignmentPreflightFixture) string
			errorFragment string
		}{
			{
				name: "missing selected global",
				configure: func(_ *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					return filepath.Join(fixture.root, "missing-selected.toml")
				},
				errorFragment: "selected",
			},
			{
				name:   "missing selected global in dry run",
				dryRun: true,
				configure: func(_ *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					return filepath.Join(fixture.root, "missing-selected-dry-run.toml")
				},
				errorFragment: "selected",
			},
			{
				name: "invalid selected global",
				configure: func(t *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					path := filepath.Join(fixture.root, "invalid-selected.toml")
					if err := os.WriteFile(path, []byte("not valid toml {{{\n"), 0o600); err != nil {
						t.Fatalf("write invalid selected config: %v", err)
					}
					return path
				},
				errorFragment: "config",
			},
			{
				name: "invalid target project policy from outside target",
				configure: func(t *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					globalPath := fixture.writeGlobalConfig(t)
					projectConfig := filepath.Join(fixture.projectDir, ".ntm", "config.toml")
					if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
						t.Fatalf("create target project config directory: %v", err)
					}
					if err := os.WriteFile(projectConfig, []byte("[assign\ninvalid = true\n"), 0o600); err != nil {
						t.Fatalf("write invalid target project config: %v", err)
					}
					return globalPath
				},
				errorFragment: "project",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newSpawnAssignmentPreflightFixture(t)
				fixture.writeFailIfCalledTools(t)
				selectedConfig := test.configure(t, fixture)
				mailCalls, mailURL := spawnAssignmentPreflightMailCounter(t)
				args := fixture.spawnArgs(selectedConfig)
				if test.dryRun {
					args = append(args, "--dry-run")
				}

				result := fixture.runNTM(t, map[string]string{"AGENT_MAIL_URL": mailURL}, args...)
				output := decodeSpawnAssignmentPreflightFailure(t, result, "INVALID_FLAG", test.errorFragment)
				if output.Session != "" && output.Session != fixture.session {
					t.Fatalf("config failure session=%q, want empty or %q", output.Session, fixture.session)
				}
				fixture.assertNoActuation(t)
				fixture.assertToolLogEmpty(t)
				if got := mailCalls(); got != 0 {
					t.Fatalf("config preflight attempted %d mutating Agent Mail call(s)", got)
				}
			})
		}
	})

	t.Run("unverified_plan_or_labels_is_pre_tmux", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			dryRun        bool
			unverified    string
			errorFragment string
		}{
			{name: "plan command failure", unverified: "plan", errorFragment: "plan"},
			{name: "plan command failure in dry run", dryRun: true, unverified: "plan", errorFragment: "plan"},
			{name: "missing live label evidence", unverified: "labels", errorFragment: "label"},
			{name: "missing live label evidence in dry run", dryRun: true, unverified: "labels", errorFragment: "label"},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newSpawnAssignmentPreflightFixture(t)
				selectedConfig := fixture.writeGlobalConfig(t)
				fixture.writeUnverifiedPlanningTools(t, test.unverified)
				mailCalls, mailURL := spawnAssignmentPreflightMailCounter(t)
				args := fixture.spawnArgs(selectedConfig)
				if test.dryRun {
					args = append(args, "--dry-run")
				}

				result := fixture.runNTM(t, map[string]string{"AGENT_MAIL_URL": mailURL}, args...)
				decodeSpawnAssignmentPreflightFailure(t, result, "INTERNAL_ERROR", test.errorFragment)
				fixture.assertNoActuation(t)
				fixture.assertPlanningLookup(t, test.unverified)
				if got := mailCalls(); got != 0 {
					t.Fatalf("planning preflight attempted %d mutating Agent Mail call(s)", got)
				}
			})
		}
	})

	t.Run("cross_cwd_target_custom_gate_produces_no_assignment", func(t *testing.T) {
		if _, err := exec.LookPath("br"); err != nil {
			t.Skipf("br is required for target-policy E2E: %v", err)
		}
		fixture := newSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		projectConfig := filepath.Join(fixture.projectDir, ".ntm", "config.toml")
		if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
			t.Fatalf("create gated target config directory: %v", err)
		}
		if err := os.WriteFile(projectConfig, []byte(strings.Join([]string{
			"[assign]",
			`operator_gated_labels = ["spawn-specialist-only"]`,
			"",
		}, "\n")), 0o600); err != nil {
			t.Fatalf("write gated target config: %v", err)
		}

		fixture.mustBR(t, "init", "--prefix=spawnpolicy", "--json")
		beadTitle := "SPAWN_CUSTOM_GATE_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		beadID := strings.TrimSpace(string(fixture.mustBR(
			t, "create", beadTitle, "--type=task", "--priority=1", "--silent",
		)))
		if beadID == "" || strings.ContainsAny(beadID, " \t\r\n") {
			t.Fatalf("unexpected gated bead ID %q", beadID)
		}
		fixture.mustBR(t, "update", beadID, "--add-label", "spawn-specialist-only", "--json")
		writeSpawnFakeBV(t, filepath.Join(fixture.fakeBin, "bv"), beadID, beadTitle)
		mailCalls, mailURL := spawnAssignmentPreflightMailCounter(t)

		result := fixture.runNTM(t, map[string]string{"AGENT_MAIL_URL": mailURL}, fixture.spawnArgs(selectedConfig)...)
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("gated spawn exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var output spawnAssignmentOutput
		decodeSpawnAssignmentSingleJSON(t, result.stdout, &output)
		if output.Success || output.ErrorCode != "ASSIGNMENT_FAILED" || output.Session != fixture.session ||
			output.WorkingDir != fixture.projectDir || len(output.Agents) != 1 || len(output.Assignments) != 1 {
			t.Fatalf("gated spawn output=%+v", output)
		}
		assignment := output.Assignments[0]
		if assignment.BeadID != "" || assignment.Claimed || assignment.PromptSent ||
			assignment.ClaimError != "no work assignment was produced for this eligible agent" ||
			assignment.ClaimActor != "" || assignment.IdempotencyKey != "" || assignment.DispatchReceiptID != "" ||
			len(assignment.ReservationIDs) != 0 {
			t.Fatalf("gated spawn fabricated assignment=%+v", assignment)
		}
		fixture.assertSessionExists(t)
		fixture.assertBeadOpen(t, beadID)
		fixture.assertNoAssignmentDurability(t)
		if got := mailCalls(); got != 0 {
			t.Fatalf("filtered target gate attempted %d mutating Agent Mail call(s)", got)
		}
		paneOutput := fixture.captureSession(t)
		if strings.Contains(paneOutput, beadTitle) {
			t.Fatalf("filtered target-gated work reached agent pane: %s", paneOutput)
		}
	})
}

// TestE2EAtomicRobotSpawnSafetyRecheckBuiltBinary proves the built robot CLI
// rejects a session that appears after its first safety check but before any
// session, pane, or agent lifecycle mutation.
func TestE2EAtomicRobotSpawnSafetyRecheckBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newSpawnAssignmentPreflightFixture(t)
	selectedConfig := fixture.writeGlobalConfig(t)
	tmuxWrapper := filepath.Join(fixture.fakeBin, "tmux-safety-recheck")
	probeCount := filepath.Join(fixture.root, "tmux-safety-probes")
	tmuxAudit := filepath.Join(fixture.root, "tmux-safety-audit")
	actuationAttempt := filepath.Join(fixture.root, "tmux-actuation-attempt")
	wrapper := `#!/bin/sh
set -eu

command_name=${1:-}
if [ "$#" -gt 0 ]; then shift; fi
printf '%s' "$command_name" >> "$NTM_E2E_SAFETY_AUDIT"
for argument in "$@"; do
  printf '\t%s' "$argument" >> "$NTM_E2E_SAFETY_AUDIT"
done
printf '\n' >> "$NTM_E2E_SAFETY_AUDIT"

case "$command_name" in
  has-session)
    if [ "$*" != "-t $NTM_E2E_SAFETY_SESSION" ]; then
      printf 'unexpected has-session target: %s\n' "$*" >&2
      exit 96
    fi
    count=0
    if [ -f "$NTM_E2E_SAFETY_PROBES" ]; then
      IFS= read -r count < "$NTM_E2E_SAFETY_PROBES"
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$NTM_E2E_SAFETY_PROBES"
    if [ "$count" -eq 1 ]; then
      printf "can't find session: %s\n" "$NTM_E2E_SAFETY_SESSION" >&2
      exit 1
    fi
    if [ "$count" -eq 2 ]; then
      exit 0
    fi
    printf 'unexpected safety probe %s\n' "$count" >&2
    exit 95
    ;;
  list-sessions|list-panes)
    exec "$NTM_E2E_REAL_TMUX" "$command_name" "$@"
    ;;
  *)
    printf '%s\n' "$command_name" > "$NTM_E2E_SAFETY_ACTUATION"
    printf 'lifecycle command crossed robot spawn safety boundary: %s\n' "$command_name" >&2
    exit 97
    ;;
esac
`
	if err := os.WriteFile(tmuxWrapper, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write robot spawn safety tmux wrapper: %v", err)
	}

	result := fixture.runNTM(
		t,
		map[string]string{
			"NTM_TMUX_BINARY":          tmuxWrapper,
			"NTM_E2E_REAL_TMUX":        fixture.tmuxPath,
			"NTM_E2E_SAFETY_SESSION":   fixture.session,
			"NTM_E2E_SAFETY_PROBES":    probeCount,
			"NTM_E2E_SAFETY_AUDIT":     tmuxAudit,
			"NTM_E2E_SAFETY_ACTUATION": actuationAttempt,
		},
		"--config="+selectedConfig,
		"--robot-format=json",
		"--robot-spawn="+fixture.session,
		"--spawn-cc=1",
		"--spawn-no-user",
		"--spawn-dir="+fixture.projectDir,
		"--spawn-safety",
	)
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("robot safety recheck exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}

	var output struct {
		Success    bool                   `json:"success"`
		Session    string                 `json:"session"`
		WorkingDir string                 `json:"working_dir"`
		Agents     []spawnAssignmentAgent `json:"agents"`
		Error      string                 `json:"error"`
		ErrorCode  string                 `json:"error_code"`
		Hint       string                 `json:"hint"`
	}
	decodeSpawnAssignmentSingleJSON(t, result.stdout, &output)
	wantError := fmt.Sprintf(
		"session '%s' already exists (--spawn-safety mode prevents reuse; use 'ntm kill %s' first)",
		fixture.session,
		fixture.session,
	)
	wantHint := "Choose a new session name or disable --spawn-safety"
	if output.Success || output.Session != fixture.session || output.WorkingDir != fixture.projectDir ||
		output.Error != wantError || output.ErrorCode != "INVALID_FLAG" || output.Hint != wantHint {
		t.Fatalf("robot safety recheck output=%+v, want exact INVALID_FLAG conflict contract", output)
	}
	if output.Agents == nil || len(output.Agents) != 0 {
		t.Fatalf("robot safety recheck agents=%+v, want initialized empty array", output.Agents)
	}
	var rawEnvelope map[string]json.RawMessage
	decodeSpawnAssignmentSingleJSON(t, result.stdout, &rawEnvelope)
	if agents, ok := rawEnvelope["agents"]; !ok || string(bytes.TrimSpace(agents)) != "[]" {
		t.Fatalf("robot safety recheck agents JSON=%s, want literal [] in envelope=%s", agents, result.stdout)
	}

	probeData, err := os.ReadFile(probeCount)
	if err != nil {
		t.Fatalf("read robot spawn safety probe count: %v", err)
	}
	if got := strings.TrimSpace(string(probeData)); got != "2" {
		t.Fatalf("robot spawn safety probes=%q, want exactly two", got)
	}
	auditData, err := os.ReadFile(tmuxAudit)
	if err != nil {
		t.Fatalf("read robot spawn safety tmux audit: %v", err)
	}
	auditLines := strings.Split(strings.TrimSpace(string(auditData)), "\n")
	wantProbe := "has-session\t-t\t" + fixture.session
	if len(auditLines) != 5 || !strings.HasPrefix(auditLines[0], "list-sessions\t-F\t") ||
		!strings.HasPrefix(auditLines[1], "list-panes\t-a\t-F\t") || auditLines[2] != wantProbe ||
		!strings.HasPrefix(auditLines[3], "list-panes\t-a\t-F\t") || auditLines[4] != wantProbe {
		t.Fatalf("robot spawn safety tmux topology=%q, want read-only session and pane inventory, probe, admission pane inventory, probe", auditLines)
	}
	if _, err := os.Stat(actuationAttempt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("robot spawn safety boundary admitted a lifecycle command: %v", err)
	}
	fixture.assertNoActuation(t)
}

// TestE2EAtomicWorktreeCommandCancellationBuiltBinary proves SIGINT reaches
// both worktree command families and that each joins its blocked Git process
// without concealing or extending filesystem mutation.
func TestE2EAtomicWorktreeCommandCancellationBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)

	t.Run("singular_provision_reports_checkout_retained_after_add", func(t *testing.T) {
		fixture := newSpawnAssignmentPreflightFixture(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		startedDir := filepath.Join(fixture.root, "singular-add-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create singular worktree start directory: %v", err)
		}
		gitAudit := filepath.Join(fixture.root, "singular-git-audit")
		wrapper := `#!/bin/sh
set -eu
trap '' INT
printf '%s\n' "$*" >> "$NTM_E2E_WORKTREE_GIT_AUDIT"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$NTM_E2E_REAL_GIT" "$@"
  status=$?
  if [ "$status" -ne 0 ]; then exit "$status"; fi
  : > "$NTM_E2E_WORKTREE_STARTED/add-completed"
  exec sleep 30
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`
		fixture.writeGitWrapper(t, wrapper)
		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_E2E_REAL_GIT":           realGit,
			"NTM_E2E_WORKTREE_GIT_AUDIT": gitAudit,
			"NTM_E2E_WORKTREE_STARTED":   startedDir,
		})

		sessionID := "retained-e2e"
		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.projectDir,
			env,
			startedDir,
			1,
			syscall.SIGINT,
			"worktree", "provision", "cc", sessionID,
		)
		if result.signalToJoin > 10*time.Second {
			t.Fatalf("singular worktree cancellation took %v from signal to joined return, want at most 10s", result.signalToJoin)
		}
		if result.exitCode != 1 {
			t.Fatalf("singular worktree cancellation exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}

		worktreeName := fmt.Sprintf("agent-%d-cc-session-%d-%s", len("cc"), len(sessionID), sessionID)
		retainedPath := filepath.Clean(filepath.Join(fixture.projectDir, "..", worktreeName))
		retainedBranch := "agent/cc/" + sessionID
		combinedOutput := string(result.stdout) + string(result.stderr)
		for _, fragment := range []string{"context canceled", "checkout retained at " + retainedPath, "branch " + retainedBranch} {
			if !strings.Contains(combinedOutput, fragment) {
				t.Fatalf("singular cancellation output omits %q: stdout=%s stderr=%s", fragment, result.stdout, result.stderr)
			}
		}
		if stat, err := os.Stat(retainedPath); err != nil || !stat.IsDir() {
			t.Fatalf("reported retained checkout is missing: stat=%v err=%v", stat, err)
		}
		if stat, err := os.Stat(filepath.Join(retainedPath, ".git")); err != nil || stat.IsDir() {
			t.Fatalf("reported retained checkout has invalid .git marker: stat=%v err=%v", stat, err)
		}

		auditData, err := os.ReadFile(gitAudit)
		if err != nil {
			t.Fatalf("read singular worktree Git audit: %v", err)
		}
		audit := string(auditData)
		if strings.Count(audit, "worktree add") != 1 || strings.Contains(audit, "rev-parse HEAD") ||
			strings.Contains(audit, "worktree remove") || strings.Contains(audit, "branch -D") {
			t.Fatalf("singular worktree crossed retained-checkout cancellation boundary: %q", audit)
		}
	})

	t.Run("plural_remove_preserves_checkout_and_skips_fallback", func(t *testing.T) {
		fixture := newSpawnAssignmentPreflightFixture(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		sessionName := filepath.Base(fixture.projectDir)
		agentName := "cc"
		worktreePath := filepath.Join(fixture.projectDir, ".ntm", "worktrees", sessionName, agentName)
		branchName := fmt.Sprintf("ntm/%s/%s", sessionName, agentName)
		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
			t.Fatalf("create plural worktree parent: %v", err)
		}
		fixture.runGit(t, "worktree", "add", "-b", branchName, worktreePath)

		startedDir := filepath.Join(fixture.root, "plural-remove-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create plural worktree start directory: %v", err)
		}
		gitAudit := filepath.Join(fixture.root, "plural-git-audit")
		wrapper := `#!/bin/sh
set -eu
trap '' INT
printf '%s\n' "$*" >> "$NTM_E2E_WORKTREE_GIT_AUDIT"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "remove" ]; then
  : > "$NTM_E2E_WORKTREE_STARTED/remove-blocked"
  exec sleep 30
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`
		fixture.writeGitWrapper(t, wrapper)
		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_E2E_REAL_GIT":           realGit,
			"NTM_E2E_WORKTREE_GIT_AUDIT": gitAudit,
			"NTM_E2E_WORKTREE_STARTED":   startedDir,
		})

		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.projectDir,
			env,
			startedDir,
			1,
			syscall.SIGINT,
			"worktrees", "remove", agentName,
		)
		if result.signalToJoin > 10*time.Second {
			t.Fatalf("plural worktree cancellation took %v from signal to joined return, want at most 10s", result.signalToJoin)
		}
		if result.exitCode != 1 || !strings.Contains(string(result.stdout)+string(result.stderr), "context canceled") {
			t.Fatalf("plural worktree cancellation exit=%d, want context cancellation; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}

		time.Sleep(100 * time.Millisecond)
		if stat, err := os.Stat(worktreePath); err != nil || !stat.IsDir() {
			t.Fatalf("plural cancellation removed target checkout: stat=%v err=%v", stat, err)
		}
		branchCtx, branchCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer branchCancel()
		branchCmd := exec.CommandContext(branchCtx, realGit, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
		branchCmd.Dir = fixture.projectDir
		branchCmd.Env = append([]string(nil), fixture.env...)
		if output, err := branchCmd.CombinedOutput(); err != nil {
			t.Fatalf("plural cancellation deleted target branch: %v output=%s", err, output)
		}

		auditData, err := os.ReadFile(gitAudit)
		if err != nil {
			t.Fatalf("read plural worktree Git audit: %v", err)
		}
		audit := string(auditData)
		if strings.Count(audit, "worktree remove") != 1 || strings.Contains(audit, "worktree prune") || strings.Contains(audit, "branch -D") {
			t.Fatalf("plural worktree cancellation reached fallback mutation: %q", audit)
		}
	})
}

// TestE2EAtomicWorktreeAutoProvisionCancellationBuiltBinary proves the built
// auto-provision command carries SIGINT through pane discovery and pane update.
// The checkout remains reported and no cd is sent after cancellation.
func TestE2EAtomicWorktreeAutoProvisionCancellationBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)

	t.Run("pane_update_is_canceled_before_cd", func(t *testing.T) {
		fixture := newSpawnAssignmentPreflightFixture(t)
		fixture.initializeCommittedGitRepo(t)
		sessionName := filepath.Base(fixture.projectDir)
		sessionID := sessionName + "-cc-1"
		worktreeName := fmt.Sprintf("agent-%d-cc-session-%d-%s", len("cc"), len(sessionID), sessionID)
		retainedPath := filepath.Clean(filepath.Join(fixture.projectDir, "..", worktreeName))
		retainedBranch := "agent/cc/" + sessionID

		selectedConfig := filepath.Join(fixture.configDir, "ntm", "config.toml")
		if err := os.MkdirAll(filepath.Dir(selectedConfig), 0o700); err != nil {
			t.Fatalf("create auto-provision config directory: %v", err)
		}
		configBody := fmt.Sprintf("projects_base = %s\n[spawn_pacing]\nenabled = false\n", strconv.Quote(fixture.root))
		if err := os.WriteFile(selectedConfig, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write auto-provision config: %v", err)
		}

		startedDir := filepath.Join(fixture.root, "auto-provision-pane-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create auto-provision pane marker directory: %v", err)
		}
		tmuxAudit := filepath.Join(fixture.root, "auto-provision-tmux-audit")
		lateCD := filepath.Join(fixture.root, "auto-provision-late-cd")
		tmuxWrapper := filepath.Join(fixture.fakeBin, "tmux-auto-provision-cancel")
		wrapper := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_WORKTREE_TMUX_AUDIT"
case "${1:-}" in
  has-session)
    exit 0
    ;;
  list-panes)
    printf '%%1_NTM_SEP_0_NTM_SEP_%s__cc_1_NTM_SEP_bash_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_12345_NTM_SEP_0\n' "$NTM_E2E_WORKTREE_SESSION"
    exit 0
    ;;
  send-keys)
    if [ "$*" = "send-keys -t %1 C-c" ]; then
      if [ ! -d "$NTM_E2E_WORKTREE_RETAINED_PATH" ]; then
        printf 'pane update started before checkout existed: %s\n' "$NTM_E2E_WORKTREE_RETAINED_PATH" >&2
        exit 96
      fi
      : > "$NTM_E2E_WORKTREE_STARTED/pane-update-blocked"
      trap '' INT
      exec sleep 30
    fi
    case "$*" in
      *"cd "*|*" Enter") : > "$NTM_E2E_WORKTREE_LATE_CD" ;;
    esac
    printf 'unexpected pane mutation after cancellation boundary: %s\n' "$*" >&2
    exit 97
    ;;
  *)
    printf 'unexpected tmux command: %s\n' "$*" >&2
    exit 98
    ;;
esac
`
		if err := os.WriteFile(tmuxWrapper, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write auto-provision tmux wrapper: %v", err)
		}
		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                tmuxWrapper,
			"NTM_E2E_WORKTREE_LATE_CD":       lateCD,
			"NTM_E2E_WORKTREE_RETAINED_PATH": retainedPath,
			"NTM_E2E_WORKTREE_SESSION":       sessionName,
			"NTM_E2E_WORKTREE_STARTED":       startedDir,
			"NTM_E2E_WORKTREE_TMUX_AUDIT":    tmuxAudit,
		})

		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.projectDir,
			env,
			startedDir,
			1,
			syscall.SIGINT,
			"--config="+selectedConfig,
			"worktree", "auto-provision", sessionName,
		)
		if result.signalToJoin > 10*time.Second {
			t.Fatalf("auto-provision cancellation took %v from signal to joined return, want at most 10s", result.signalToJoin)
		}
		combinedOutput := string(result.stdout) + string(result.stderr)
		if result.exitCode != 1 {
			t.Fatalf("auto-provision cancellation exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		for _, fragment := range []string{
			"context canceled",
			"checkout provisioned at " + retainedPath,
			"branch " + retainedBranch,
		} {
			if !strings.Contains(combinedOutput, fragment) {
				t.Fatalf("auto-provision cancellation output omits %q: stdout=%s stderr=%s", fragment, result.stdout, result.stderr)
			}
		}
		if stat, err := os.Stat(retainedPath); err != nil || !stat.IsDir() {
			t.Fatalf("auto-provision retained checkout missing: stat=%v err=%v", stat, err)
		}
		if stat, err := os.Stat(filepath.Join(retainedPath, ".git")); err != nil || stat.IsDir() {
			t.Fatalf("auto-provision retained checkout .git marker invalid: stat=%v err=%v", stat, err)
		}
		if _, err := os.Stat(lateCD); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("auto-provision sent a late cd or Enter after cancellation: %v", err)
		}
		auditData, err := os.ReadFile(tmuxAudit)
		if err != nil {
			t.Fatalf("read auto-provision tmux audit: %v", err)
		}
		audit := string(auditData)
		if strings.Count(audit, "has-session") != 1 || strings.Count(audit, "list-panes") != 1 ||
			strings.Count(audit, "send-keys -t %1 C-c") != 1 || strings.Contains(audit, "cd ") || strings.Contains(audit, " Enter") {
			t.Fatalf("auto-provision tmux calls crossed cancellation boundary: %q", audit)
		}
	})

	t.Run("completed_add_is_materialized_without_pane_mutation", func(t *testing.T) {
		fixture := newSpawnAssignmentPreflightFixture(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		sessionName := filepath.Base(fixture.projectDir)
		sessionID := sessionName + "-cc-1"
		worktreeName := fmt.Sprintf("agent-%d-cc-session-%d-%s", len("cc"), len(sessionID), sessionID)
		retainedPath := filepath.Clean(filepath.Join(fixture.projectDir, "..", worktreeName))
		retainedBranch := "agent/cc/" + sessionID

		selectedConfig := filepath.Join(fixture.configDir, "ntm", "config.toml")
		if err := os.MkdirAll(filepath.Dir(selectedConfig), 0o700); err != nil {
			t.Fatalf("create retained-add config directory: %v", err)
		}
		configBody := fmt.Sprintf("projects_base = %s\n[spawn_pacing]\nenabled = false\n", strconv.Quote(fixture.root))
		if err := os.WriteFile(selectedConfig, []byte(configBody), 0o600); err != nil {
			t.Fatalf("write retained-add config: %v", err)
		}

		startedDir := filepath.Join(fixture.root, "auto-provision-add-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create retained-add marker directory: %v", err)
		}
		gitAudit := filepath.Join(fixture.root, "auto-provision-add-git-audit")
		gitWrapper := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_WORKTREE_GIT_AUDIT"
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$NTM_E2E_REAL_GIT" "$@"
  command_status=$?
  if [ "$command_status" -ne 0 ]; then exit "$command_status"; fi
  : > "$NTM_E2E_WORKTREE_STARTED/add-completed"
  trap '' INT
  exec sleep 30
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`
		fixture.writeGitWrapper(t, gitWrapper)

		tmuxAudit := filepath.Join(fixture.root, "auto-provision-add-tmux-audit")
		tmuxMutation := filepath.Join(fixture.root, "auto-provision-add-tmux-mutation")
		tmuxWrapper := filepath.Join(fixture.fakeBin, "tmux-auto-provision-add-cancel")
		tmuxScript := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_WORKTREE_TMUX_AUDIT"
case "${1:-}" in
  has-session)
    exit 0
    ;;
  list-panes)
    printf '%%1_NTM_SEP_0_NTM_SEP_%s__cc_1_NTM_SEP_bash_NTM_SEP_120_NTM_SEP_40_NTM_SEP_1_NTM_SEP_12345_NTM_SEP_0\n' "$NTM_E2E_WORKTREE_SESSION"
    exit 0
    ;;
  send-keys)
    : > "$NTM_E2E_WORKTREE_TMUX_MUTATION"
    printf 'pane mutation crossed retained-add cancellation boundary: %s\n' "$*" >&2
    exit 97
    ;;
  *)
    printf 'unexpected tmux command: %s\n' "$*" >&2
    exit 98
    ;;
esac
`
		if err := os.WriteFile(tmuxWrapper, []byte(tmuxScript), 0o700); err != nil {
			t.Fatalf("write retained-add tmux wrapper: %v", err)
		}

		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":                tmuxWrapper,
			"NTM_E2E_REAL_GIT":               realGit,
			"NTM_E2E_WORKTREE_GIT_AUDIT":     gitAudit,
			"NTM_E2E_WORKTREE_SESSION":       sessionName,
			"NTM_E2E_WORKTREE_STARTED":       startedDir,
			"NTM_E2E_WORKTREE_TMUX_AUDIT":    tmuxAudit,
			"NTM_E2E_WORKTREE_TMUX_MUTATION": tmuxMutation,
		})

		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.projectDir,
			env,
			startedDir,
			1,
			syscall.SIGINT,
			"--config="+selectedConfig,
			"worktree", "auto-provision", sessionName,
		)
		if result.signalToJoin > 10*time.Second {
			t.Fatalf("retained-add cancellation took %v from signal to joined return, want at most 10s", result.signalToJoin)
		}
		combinedOutput := string(result.stdout) + string(result.stderr)
		if result.exitCode != 1 {
			t.Fatalf("retained-add cancellation exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		for _, fragment := range []string{
			"context canceled",
			"checkout retained at " + retainedPath,
			"branch " + retainedBranch,
		} {
			if !strings.Contains(combinedOutput, fragment) {
				t.Fatalf("retained-add cancellation output omits %q: stdout=%s stderr=%s", fragment, result.stdout, result.stderr)
			}
		}
		if stat, err := os.Stat(retainedPath); err != nil || !stat.IsDir() {
			t.Fatalf("retained-add checkout missing: stat=%v err=%v", stat, err)
		}
		if _, err := os.Stat(tmuxMutation); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retained-add cancellation reached pane mutation: %v", err)
		}
		gitData, err := os.ReadFile(gitAudit)
		if err != nil {
			t.Fatalf("read retained-add git audit: %v", err)
		}
		if audit := string(gitData); strings.Count(audit, "worktree add") != 1 || strings.Contains(audit, "rev-parse HEAD") {
			t.Fatalf("retained-add Git calls crossed cancellation boundary: %q", audit)
		}
		tmuxData, err := os.ReadFile(tmuxAudit)
		if err != nil {
			t.Fatalf("read retained-add tmux audit: %v", err)
		}
		if audit := string(tmuxData); strings.Count(audit, "has-session") != 1 || strings.Count(audit, "list-panes") != 1 || strings.Contains(audit, "send-keys") {
			t.Fatalf("retained-add tmux calls crossed cancellation boundary: %q", audit)
		}
	})
}

// TestE2EAtomicNormalSpawnAssignmentSafetyPreflightBuiltBinary proves the
// human spawn --assign command performs the same target-policy and actionable
// admission before tmux, pane, Agent Mail, readiness, prompt, or ledger work.
func TestE2EAtomicNormalSpawnAssignmentSafetyPreflightBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("invalid_or_missing_policy_is_pre_lifecycle", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			configure     func(*testing.T, *spawnAssignmentPreflightFixture) string
			errorCode     string
			errorFragment string
		}{
			{
				name: "missing selected global policy",
				configure: func(_ *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					return filepath.Join(fixture.root, "missing-normal-spawn.toml")
				},
				errorCode:     "INVALID_FLAG",
				errorFragment: "config",
			},
			{
				name: "invalid selected global policy",
				configure: func(t *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					path := filepath.Join(fixture.root, "invalid-normal-spawn.toml")
					if err := os.WriteFile(path, []byte("not valid toml {{{\n"), 0o600); err != nil {
						t.Fatalf("write invalid normal spawn selected config: %v", err)
					}
					return path
				},
				errorCode:     "INVALID_FLAG",
				errorFragment: "config",
			},
			{
				name: "invalid target project policy from outside target",
				configure: func(t *testing.T, fixture *spawnAssignmentPreflightFixture) string {
					globalPath := fixture.writeGlobalConfig(t)
					projectConfig := filepath.Join(fixture.projectDir, ".ntm", "config.toml")
					if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
						t.Fatalf("create normal spawn target config directory: %v", err)
					}
					if err := os.WriteFile(projectConfig, []byte("[assign\ninvalid = true\n"), 0o600); err != nil {
						t.Fatalf("write invalid normal spawn target config: %v", err)
					}
					return globalPath
				},
				errorCode:     "INVALID_FLAG",
				errorFragment: "project",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newNormalSpawnAssignmentPreflightFixture(t)
				fixture.writeFailIfCalledTools(t)
				selectedConfig := test.configure(t, fixture)
				mailCalls, mailURL := spawnAssignmentPreflightMailCounter(t)
				result := fixture.runNTM(
					t,
					map[string]string{
						"AGENT_MAIL_URL":    mailURL,
						"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir),
					},
					fixture.normalSpawnArgs(selectedConfig)...,
				)

				decodeSpawnAssignmentPreflightFailure(t, result, test.errorCode, test.errorFragment)
				fixture.assertNoActuation(t)
				fixture.assertToolLogEmpty(t)
				if got := mailCalls(); got != 0 {
					t.Fatalf("normal spawn policy preflight attempted %d mutating Agent Mail call(s)", got)
				}
			})
		}
	})

	t.Run("unverified_plan_or_labels_is_pre_lifecycle", func(t *testing.T) {
		for _, test := range []struct {
			name          string
			unverified    string
			errorFragment string
		}{
			{name: "plan command failure", unverified: "plan", errorFragment: "plan"},
			{name: "missing live label evidence", unverified: "labels", errorFragment: "label"},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newNormalSpawnAssignmentPreflightFixture(t)
				selectedConfig := fixture.writeGlobalConfig(t)
				fixture.writeUnverifiedPlanningTools(t, test.unverified)
				mailCalls, mailURL := spawnAssignmentPreflightMailCounter(t)
				result := fixture.runNTM(
					t,
					map[string]string{
						"AGENT_MAIL_URL":    mailURL,
						"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir),
					},
					fixture.normalSpawnArgs(selectedConfig)...,
				)

				decodeSpawnAssignmentPreflightFailure(t, result, "INTERNAL_ERROR", test.errorFragment)
				fixture.assertNoActuation(t)
				fixture.assertPlanningLookup(t, test.unverified)
				if got := mailCalls(); got != 0 {
					t.Fatalf("normal spawn planning preflight attempted %d mutating Agent Mail call(s)", got)
				}
			})
		}
	})

	t.Run("verified_empty_actionable_set_is_successful_no_work", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.writeVerifiedEmptyPlanningTools(t)

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalSpawnArgs(selectedConfig)...,
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("normal empty-plan spawn exit=%d, want 0; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var output normalSpawnAssignmentOutput
		decodeSpawnAssignmentSingleJSON(t, result.stdout, &output)
		if !output.Success || len(output.Errors) != 0 || output.Spawn.Session != fixture.session ||
			output.Spawn.WorkingDirectory != fixture.projectDir || output.Assign == nil ||
			len(output.Assign.Assignments) != 0 || len(output.Assign.Skipped) != 0 {
			t.Fatalf("normal empty-plan spawn output=%+v", output)
		}
		fixture.assertSessionExists(t)
		if _, err := os.Stat(fixture.launchMarker); err != nil {
			t.Fatalf("verified empty plan did not launch the requested agent: %v", err)
		}

		logText := fixture.targetPlanningLog(t)
		for command, wantCount := range map[string]int{
			"tool=bv\targs=--robot-triage":   1,
			"tool=bv\targs=--robot-plan":     1,
			"tool=bv\targs=--robot-insights": 1,
		} {
			if got := strings.Count(logText, command); got != wantCount {
				t.Fatalf("normal empty-plan call %q count=%d, want %d; log=%s", command, got, wantCount, logText)
			}
		}
		if strings.Contains(logText, "tool=br\t") {
			t.Fatalf("verified empty plan performed unnecessary live-label lookup: %s", logText)
		}
	})

	t.Run("target_custom_gate_reuses_one_verified_plan", func(t *testing.T) {
		if _, err := exec.LookPath("br"); err != nil {
			t.Skipf("br is required for normal spawn target-policy E2E: %v", err)
		}
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		projectConfig := filepath.Join(fixture.projectDir, ".ntm", "config.toml")
		if err := os.MkdirAll(filepath.Dir(projectConfig), 0o700); err != nil {
			t.Fatalf("create normal spawn gated target config directory: %v", err)
		}
		if err := os.WriteFile(projectConfig, []byte(strings.Join([]string{
			"[assign]",
			`operator_gated_labels = ["normal-spawn-specialist-only"]`,
			"",
		}, "\n")), 0o600); err != nil {
			t.Fatalf("write normal spawn gated target config: %v", err)
		}

		fixture.mustBR(t, "init", "--prefix=normalspawnpolicy", "--json")
		beadTitle := "NORMAL_SPAWN_CUSTOM_GATE_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		beadID := strings.TrimSpace(string(fixture.mustBR(
			t, "create", beadTitle, "--type=task", "--priority=1", "--silent",
		)))
		if beadID == "" || strings.ContainsAny(beadID, " \t\r\n") {
			t.Fatalf("unexpected normal spawn gated bead ID %q", beadID)
		}
		fixture.mustBR(t, "update", beadID, "--add-label", "normal-spawn-specialist-only", "--json")
		writeSpawnFakeBV(t, filepath.Join(fixture.fakeBin, "bv"), beadID, beadTitle)

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalSpawnArgs(selectedConfig)...,
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("normal gated spawn exit=%d, want 0; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var output normalSpawnAssignmentOutput
		decodeSpawnAssignmentSingleJSON(t, result.stdout, &output)
		if !output.Success || len(output.Errors) != 0 || output.Spawn.Session != fixture.session ||
			output.Spawn.WorkingDirectory != fixture.projectDir || output.Assign == nil ||
			len(output.Assign.Assignments) != 0 || len(output.Assign.Skipped) != 1 ||
			output.Assign.Skipped[0].BeadID != beadID || output.Assign.Skipped[0].Reason != "operator_gated" {
			t.Fatalf("normal gated spawn output=%+v", output)
		}
		fixture.assertSessionExists(t)
		fixture.assertBeadOpen(t, beadID)
		paneOutput := fixture.captureSession(t)
		if strings.Contains(paneOutput, beadTitle) {
			t.Fatalf("normal spawn dispatched target-gated work: %s", paneOutput)
		}

		logText := fixture.targetPlanningLog(t)
		for command, wantCount := range map[string]int{
			"tool=bv\targs=--robot-triage":   1,
			"tool=bv\targs=--robot-plan":     1,
			"tool=bv\targs=--robot-insights": 1,
		} {
			if got := strings.Count(logText, command); got != wantCount {
				t.Fatalf("normal spawn planning call %q count=%d, want %d; log=%s", command, got, wantCount, logText)
			}
		}
	})
}

// TestE2EAtomicNormalSpawnWorktreeAdmissionBuiltBinary proves normal spawn
// finishes every worktree admission and provisioning step before it mutates
// tmux, and that a successful launch actually runs inside the isolated checkout.
func TestE2EAtomicNormalSpawnWorktreeAdmissionBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("invalid_admission_does_not_run_pre_spawn_hook", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		hookMarker := filepath.Join(fixture.root, "invalid-admission-hook-ran")
		fixture.writePreSpawnHook(t, fmt.Sprintf("printf hook-ran > %q", hookMarker))
		stalePath := fixture.normalWorktreePath("cc_1")
		if err := os.MkdirAll(stalePath, 0o700); err != nil {
			t.Fatalf("create stale worktree target: %v", err)
		}

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "not a valid git worktree")
		fixture.assertNoActuation(t)
		if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid worktree admission ran pre-spawn hook: %v", err)
		}
	})

	t.Run("unborn_repository_fails_without_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.runGit(t, "init", "-b", "main")
		hookMarker := filepath.Join(fixture.root, "unborn-admission-hook-ran")
		fixture.writePreSpawnHook(t, fmt.Sprintf("printf hook-ran > %q", hookMarker))

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "no valid HEAD commit")
		fixture.assertNoActuation(t)
		if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unborn repository admission ran pre-spawn hook: %v", err)
		}
	})

	t.Run("stale_target_fails_without_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		stalePath := filepath.Join(fixture.projectDir, ".ntm", "worktrees", fixture.session, "cc_1")
		if err := os.MkdirAll(stalePath, 0o700); err != nil {
			t.Fatalf("create stale worktree target: %v", err)
		}

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "not a valid git worktree")
		fixture.assertNoActuation(t)
	})

	t.Run("existing_target_branch_fails_without_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		fixture.runGit(t, "branch", "ntm/"+fixture.session+"/cc_1")
		hookMarker := filepath.Join(fixture.root, "branch-admission-hook-ran")
		fixture.writePreSpawnHook(t, fmt.Sprintf("printf hook-ran > %q", hookMarker))

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "already exists without its expected worktree path")
		fixture.assertNoActuation(t)
		if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("conflicting branch admission ran pre-spawn hook: %v", err)
		}
	})

	t.Run("shared_name_for_multiple_agents_fails_without_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		args := append(fixture.normalWorktreeSpawnArgs(selectedConfig, 2), "--worktree-name=shared")

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			args...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "only valid for single-agent spawns")
		fixture.assertNoActuation(t)
	})

	t.Run("detached_existing_worktree_fails_without_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		worktreePath := fixture.normalWorktreePath("cc_1")
		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
			t.Fatalf("create worktree parent: %v", err)
		}
		fixture.runGit(t, "worktree", "add", "-b", "ntm/"+fixture.session+"/cc_1", worktreePath)
		fixture.runGitInDir(t, worktreePath, "checkout", "--detach", "--quiet")

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "detached HEAD")
		fixture.assertNoActuation(t)
	})

	t.Run("wrong_branch_worktree_fails_before_hook_tmux_or_agent", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		worktreePath := fixture.normalWorktreePath("cc_1")
		if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
			t.Fatalf("create worktree parent: %v", err)
		}
		fixture.runGit(t, "worktree", "add", "-b", "different-branch", worktreePath)
		hookMarker := filepath.Join(fixture.root, "wrong-branch-hook-ran")
		fixture.writePreSpawnHook(t, fmt.Sprintf("printf hook-ran > %q", hookMarker))

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "would collide")
		fixture.assertNoActuation(t)
		if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wrong-branch admission ran pre-spawn hook: %v", err)
		}
	})

	t.Run("successful_admission_runs_hook_before_provisioning", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		hookMarker := filepath.Join(fixture.root, "admitted-hook-ran")
		fixture.writePreSpawnHook(t, fmt.Sprintf("printf hook-ran > %q", hookMarker))
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		fixture.writeGitWrapper(t, `#!/bin/sh
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  if [ ! -f "$NTM_E2E_HOOK_MARKER" ]; then
    echo 'worktree provisioning ran before pre-spawn hook' >&2
    exit 75
  fi
  echo 'provisioning observed pre-spawn hook marker' >&2
  exit 73
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)

		result := fixture.runNTM(
			t,
			map[string]string{
				"NTM_PROJECTS_BASE":   filepath.Dir(fixture.projectDir),
				"NTM_E2E_REAL_GIT":    realGit,
				"NTM_E2E_HOOK_MARKER": hookMarker,
			},
			fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "provisioning observed pre-spawn hook marker")
		if _, err := os.Stat(hookMarker); err != nil {
			t.Fatalf("successful admission did not run hook marker: %v", err)
		}
		fixture.assertNoActuation(t)
	})

	t.Run("second_provision_failure_reports_first_affected_path", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		counterPath := filepath.Join(fixture.root, "worktree-add-count")
		fixture.writeGitWrapper(t, `#!/bin/sh
count=0
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  if [ -f "$NTM_E2E_WORKTREE_COUNTER" ]; then count=$(cat "$NTM_E2E_WORKTREE_COUNTER"); fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$NTM_E2E_WORKTREE_COUNTER"
  if [ "$count" -eq 2 ]; then
    echo 'injected second worktree provisioning failure' >&2
    exit 73
  fi
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)

		result := fixture.runNTM(
			t,
			map[string]string{
				"NTM_PROJECTS_BASE":        filepath.Dir(fixture.projectDir),
				"NTM_E2E_REAL_GIT":         realGit,
				"NTM_E2E_WORKTREE_COUNTER": counterPath,
			},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 2)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "injected second worktree provisioning failure")
		firstPath := fixture.normalWorktreePath("cc_1")
		assertNormalWorktreeFailureMutation(t, result.stdout, false, []string{firstPath})
		fixture.assertNoActuation(t)
		if _, err := os.Stat(filepath.Join(firstPath, ".git")); err != nil {
			t.Fatalf("first provisioned worktree is missing: %v", err)
		}
		if _, err := os.Stat(fixture.normalWorktreePath("cc_2")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed second provisioning left target path: %v", err)
		}
	})

	t.Run("cancellation_during_provisioning_has_no_late_actuation", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		startedDir := filepath.Join(fixture.root, "blocking-worktree-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create blocking worktree marker directory: %v", err)
		}
		fixture.writeGitWrapper(t, `#!/bin/sh
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  : > "$NTM_E2E_BLOCK_STARTED/worktree-add"
  exec sleep 30
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)
		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_PROJECTS_BASE":     filepath.Dir(fixture.projectDir),
			"NTM_E2E_REAL_GIT":      realGit,
			"NTM_E2E_BLOCK_STARTED": startedDir,
		})
		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.outsideDir,
			env,
			startedDir,
			1,
			syscall.SIGTERM,
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		document := assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		if document["partial_mutation"] != false || document["affected_worktree_paths"] != nil {
			t.Fatalf("canceled pre-mutation provisioning envelope=%v", document)
		}
		time.Sleep(250 * time.Millisecond)
		fixture.assertNoActuation(t)
		if _, err := os.Stat(fixture.normalWorktreePath("cc_1")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled provisioning actuated after response: %v", err)
		}
	})

	t.Run("cancellation_after_git_add_reports_retained_worktree", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		startedDir := filepath.Join(fixture.root, "post-add-block-started")
		if err := os.MkdirAll(startedDir, 0o700); err != nil {
			t.Fatalf("create post-add marker directory: %v", err)
		}
		fixture.writeGitWrapper(t, `#!/bin/sh
if [ "${1:-}" = "worktree" ] && [ "${2:-}" = "add" ]; then
  "$NTM_E2E_REAL_GIT" "$@" || exit $?
  : > "$NTM_E2E_BLOCK_STARTED/worktree-added"
  exec sleep 30
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)
		env := spawnAssignmentMergeEnv(fixture.env, map[string]string{
			"NTM_PROJECTS_BASE":     filepath.Dir(fixture.projectDir),
			"NTM_E2E_REAL_GIT":      realGit,
			"NTM_E2E_BLOCK_STARTED": startedDir,
		})
		result := runSpawnSignalCanceledCLI(
			t,
			fixture.ntmPath,
			fixture.outsideDir,
			env,
			startedDir,
			1,
			syscall.SIGTERM,
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		worktreePath := fixture.normalWorktreePath("cc_1")
		assertNormalWorktreeFailureMutation(t, result.stdout, false, []string{worktreePath})
		fixture.assertNoActuation(t)
		if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err != nil {
			t.Fatalf("retained worktree missing after canceled wrapper: %v", err)
		}
	})

	t.Run("post_provision_validation_fails_before_tmux", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		counterPath := filepath.Join(fixture.root, "post-provision-symbolic-ref-count")
		fixture.writeGitWrapper(t, `#!/bin/sh
if [ "${1:-}" = "symbolic-ref" ]; then
  output=$("$NTM_E2E_REAL_GIT" "$@" 2>/dev/null)
  status=$?
  if [ "$status" -eq 0 ]; then
    count=0
    if [ -f "$NTM_E2E_WORKTREE_COUNTER" ]; then count=$(cat "$NTM_E2E_WORKTREE_COUNTER"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$NTM_E2E_WORKTREE_COUNTER"
    if [ "$count" -eq 1 ]; then "$NTM_E2E_REAL_GIT" checkout --detach --quiet; fi
  fi
  printf '%s\n' "$output"
  exit "$status"
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)

		result := fixture.runNTM(
			t,
			map[string]string{
				"NTM_PROJECTS_BASE":        filepath.Dir(fixture.projectDir),
				"NTM_E2E_REAL_GIT":         realGit,
				"NTM_E2E_WORKTREE_COUNTER": counterPath,
			},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "detached HEAD")
		worktreePath := fixture.normalWorktreePath("cc_1")
		assertNormalWorktreeFailureMutation(t, result.stdout, false, []string{worktreePath})
		fixture.assertNoActuation(t)
	})

	t.Run("post_provision_invalidation_fails_at_launch_boundary", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("resolve real git: %v", err)
		}
		counterPath := filepath.Join(fixture.root, "symbolic-ref-count")
		fixture.writeGitWrapper(t, `#!/bin/sh
if [ "${1:-}" = "symbolic-ref" ]; then
  output=$("$NTM_E2E_REAL_GIT" "$@" 2>/dev/null)
  status=$?
  if [ "$status" -eq 0 ]; then
    count=0
    if [ -f "$NTM_E2E_WORKTREE_COUNTER" ]; then count=$(cat "$NTM_E2E_WORKTREE_COUNTER"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$NTM_E2E_WORKTREE_COUNTER"
    if [ "$count" -eq 2 ]; then "$NTM_E2E_REAL_GIT" checkout --detach --quiet; fi
  fi
  printf '%s\n' "$output"
  exit "$status"
fi
exec "$NTM_E2E_REAL_GIT" "$@"
`)

		result := fixture.runNTM(
			t,
			map[string]string{
				"NTM_PROJECTS_BASE":        filepath.Dir(fixture.projectDir),
				"NTM_E2E_REAL_GIT":         realGit,
				"NTM_E2E_WORKTREE_COUNTER": counterPath,
			},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		assertNormalWorktreeSpawnFailure(t, result, "detached HEAD")
		worktreePath := fixture.normalWorktreePath("cc_1")
		assertNormalWorktreeFailureMutation(t, result.stdout, true, []string{worktreePath})
		fixture.assertSessionExists(t)
		fixture.assertAgentNotLaunched(t)
	})

	t.Run("safety_recheck_rejects_session_created_by_hook", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		fixture.writePreSpawnHook(t, `tmux new-session -d -s "$NTM_SESSION" -c "$NTM_PROJECT_DIR"`)
		args := append(fixture.normalWorktreeSpawnArgsWithHooks(selectedConfig, 1), "--safety")

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			args...,
		)
		wantError := fmt.Sprintf("session '%s' already exists (--safety mode prevents reuse; use 'ntm kill %s' first)", fixture.session, fixture.session)
		assertNormalWorktreeSpawnFailure(t, result, wantError)
		assertNormalWorktreeFailureMutation(t, result.stdout, true, []string{fixture.normalWorktreePath("cc_1")})
		fixture.assertSessionExists(t)
		fixture.assertSessionPaneCount(t, 1)
		fixture.assertAgentNotLaunched(t)
	})

	t.Run("successful_spawn_launches_from_isolated_checkout", func(t *testing.T) {
		fixture := newNormalSpawnAssignmentPreflightFixture(t)
		selectedConfig := fixture.writeGlobalConfig(t)
		fixture.initializeCommittedGitRepo(t)
		writeSpawnWorkingDirectoryMarkerAgent(t, filepath.Join(fixture.fakeBin, "claude"), fixture.launchMarker)

		result := fixture.runNTM(
			t,
			map[string]string{"NTM_PROJECTS_BASE": filepath.Dir(fixture.projectDir)},
			fixture.normalWorktreeSpawnArgs(selectedConfig, 1)...,
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("worktree spawn exit=%d, want 0; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		fixture.assertSessionExists(t)

		expectedDir := filepath.Join(fixture.projectDir, ".ntm", "worktrees", fixture.session, "cc_1")
		deadline := time.Now().Add(5 * time.Second)
		var marker []byte
		for time.Now().Before(deadline) {
			marker, _ = os.ReadFile(fixture.launchMarker)
			if len(bytes.TrimSpace(marker)) > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if got := filepath.Clean(strings.TrimSpace(string(marker))); got != expectedDir {
			t.Fatalf("agent working directory=%q, want isolated checkout %q; stdout=%s", got, expectedDir, result.stdout)
		}
		fixture.runGitInDir(t, expectedDir, "rev-parse", "--is-inside-work-tree")
	})
}

// TestE2EAtomicServeRESTSpawnBuiltBinary proves the production HTTP server keeps
// spawn responses truthful while launching only into its private tmux server.
func TestE2EAtomicServeRESTSpawnBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate serve E2E port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release serve E2E port: %v", err)
	}

	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{projectDir, homeDir, configDir, fakeBin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create REST spawn fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated REST spawn E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated shell config: %v", err)
	}
	launchMarker := filepath.Join(root, "fake-claude-launched")
	writeSpawnLaunchMarkerAgent(t, filepath.Join(fakeBin, "claude"), launchMarker)

	env := spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})

	logPath := filepath.Join(root, "serve.log")
	serverLog, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create serve log: %v", err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverCmd := exec.CommandContext(
		serverCtx,
		ntmPath,
		"serve",
		"--host=127.0.0.1",
		fmt.Sprintf("--port=%d", port),
	)
	serverCmd.Dir = projectDir
	serverCmd.Env = append([]string(nil), env...)
	serverCmd.Stdout = serverLog
	serverCmd.Stderr = serverLog
	serverGroup, groupErr := testutil.NewProcessGroupForTest(serverCtx, serverCmd)
	if groupErr != nil {
		stopServer()
		_ = serverLog.Close()
		t.Fatalf("create owned process group for ntm serve: %v", groupErr)
	}
	serverCmd.Cancel = func() error {
		return serverGroup.Signal(os.Kill)
	}
	serverCmd.WaitDelay = 2 * time.Second
	if err := serverCmd.Start(); err != nil {
		stopServer()
		closeErr := serverGroup.Close()
		_ = serverLog.Close()
		t.Fatalf("start ntm serve: %v; close owned process group: %v", err, closeErr)
	}
	type serverWaitResult struct {
		commandErr error
		closeErr   error
	}
	serverDone := make(chan serverWaitResult, 1)
	go func() {
		serverDone <- serverWaitResult{
			commandErr: serverCmd.Wait(),
			closeErr:   serverGroup.Close(),
		}
	}()
	t.Cleanup(func() {
		stopServer()
		_ = serverGroup.Signal(os.Kill)
		select {
		case result := <-serverDone:
			if result.closeErr != nil {
				t.Errorf("close owned process group for ntm serve: %v (command error: %v)", result.closeErr, result.commandErr)
			}
		case <-time.After(45 * time.Second):
			t.Errorf("ntm serve did not stop within 45s after process-group cancellation")
		}
		_ = serverLog.Close()

		ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, tmuxPath, "kill-server")
		cmd.Env = append([]string(nil), env...)
		output, cleanupErr := cmd.CombinedOutput()
		if ctxErr := ctx.Err(); ctxErr != nil {
			t.Errorf("REST spawn tmux cleanup context ended: %v output=%s", ctxErr, output)
			return
		}
		if cleanupErr != nil && !isBenignTmuxCleanupError(output) {
			t.Errorf("clean up REST spawn tmux server: %v output=%s", cleanupErr, output)
		}
	})

	readServerLog := func() string {
		data, readErr := os.ReadFile(logPath)
		if readErr != nil {
			return fmt.Sprintf("<read serve log: %v>", readErr)
		}
		return string(data)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.Now().Add(defaultTmuxSetupTimeout)
	for {
		request, requestErr := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/health", nil)
		if requestErr != nil {
			t.Fatalf("create serve readiness request: %v", requestErr)
		}
		resp, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ntm serve did not become ready: %v log=%s", requestErr, readServerLog())
		}
		time.Sleep(100 * time.Millisecond)
	}

	type restSpawnAgent struct {
		Pane  string `json:"pane"`
		Type  string `json:"type"`
		Ready bool   `json:"ready"`
	}
	type restSpawnEnvelope struct {
		Success   bool             `json:"success"`
		Timestamp string           `json:"timestamp"`
		Session   string           `json:"session"`
		Agents    []restSpawnAgent `json:"agents"`
		Error     string           `json:"error"`
		ErrorCode string           `json:"error_code"`
		Hint      string           `json:"hint"`
	}
	postSpawn := func(t *testing.T, session string) (int, restSpawnEnvelope) {
		t.Helper()
		request, err := http.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			baseURL+"/api/v1/sessions/"+url.PathEscape(session)+"/agents/spawn",
			strings.NewReader(`{"cc_count":1,"wait_ready":true}`),
		)
		if err != nil {
			t.Fatalf("create REST spawn request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("REST spawn request: %v log=%s", err, readServerLog())
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		closeErr := response.Body.Close()
		if err != nil {
			t.Fatalf("read REST spawn response: %v", err)
		}
		if closeErr != nil {
			t.Fatalf("close REST spawn response: %v", closeErr)
		}
		var envelope restSpawnEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode REST spawn response: %v raw=%s", err, body)
		}
		return response.StatusCode, envelope
	}

	session := fmt.Sprintf("ntm-e2e-rest-spawn-%d-%d", os.Getpid(), time.Now().UnixNano())
	status, spawned := postSpawn(t, session)
	if status != http.StatusOK || !spawned.Success || spawned.Timestamp == "" || spawned.Session != session || spawned.Error != "" || spawned.ErrorCode != "" {
		t.Fatalf("successful REST spawn status=%d envelope=%+v log=%s", status, spawned, readServerLog())
	}
	if len(spawned.Agents) != 2 {
		t.Fatalf("spawned agents = %+v, want user and claude", spawned.Agents)
	}
	claudeReady := false
	for _, agent := range spawned.Agents {
		if agent.Pane == "" {
			t.Fatalf("spawned agent missing pane: %+v", agent)
		}
		if agent.Type == "claude" && agent.Ready {
			claudeReady = true
		}
	}
	if !claudeReady {
		t.Fatalf("spawned agents did not include ready fake claude: %+v", spawned.Agents)
	}
	if _, err := os.Stat(launchMarker); err != nil {
		t.Fatalf("fake claude launch marker: %v", err)
	}

	checkSession := func(t *testing.T, commandEnv []string, target string) ([]byte, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, tmuxPath, "has-session", "-t", target)
		if commandEnv != nil {
			cmd.Env = append([]string(nil), commandEnv...)
		}
		return cmd.CombinedOutput()
	}
	if output, err := checkSession(t, env, session); err != nil {
		t.Fatalf("private tmux session missing: %v output=%s", err, output)
	}
	if _, err := checkSession(t, nil, session); err == nil {
		t.Fatalf("REST spawn leaked session %q onto the host tmux socket", session)
	}

	invalidSession := "invalid--reserved-label-separator"
	status, failed := postSpawn(t, invalidSession)
	if status != http.StatusBadRequest || failed.Success || failed.Timestamp == "" ||
		failed.ErrorCode != "INVALID_FLAG" || failed.Error == "" || failed.Hint == "" {
		t.Fatalf("invalid REST spawn status=%d envelope=%+v log=%s", status, failed, readServerLog())
	}
	if _, err := checkSession(t, env, invalidSession); err == nil {
		t.Fatalf("invalid REST spawn created private session %q", invalidSession)
	}
}

// TestE2ESpawnAssignmentPartialCoverageBuiltBinary proves a built robot-spawn
// process reports every eligible agent even when triage has less work than the
// spawned topology, without duplicating any assignment side effect.
func TestE2ESpawnAssignmentPartialCoverageBuiltBinary(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newSpawnAssignmentCLIFixture(t)
	baseArgs := fixture.spawnArgs()
	args := make([]string, 0, len(baseArgs))
	for _, arg := range baseArgs {
		switch arg {
		case "--spawn-cc=1":
			arg = "--spawn-cc=2"
		case "--timeout=8s":
			arg = "--timeout=20s"
		case "--spawn-names=" + spawnAssignmentDisplayName:
			arg = "--spawn-names=" + spawnAssignmentDisplayName + ",NoWorkAgent"
		}
		// --spawn-wait is kept: this test proves PARTIAL ASSIGNMENT COVERAGE
		// (two eligible agents, one bead), not the no-wait spawn path. The
		// claim gate requires a fresh idle observation (SafeToDispatch), so
		// claiming immediately after typing the launch command races the
		// fake agent's first paint and fails closed nondeterministically.
		args = append(args, arg)
	}

	result := fixture.runNTM(t, args...)
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("partial-coverage spawn exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(result.stdout)))
	var output spawnAssignmentOutput
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("decode partial-coverage spawn JSON: %v raw=%s", err, result.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("partial-coverage spawn emitted multiple JSON documents: err=%v extra=%v raw=%s", err, trailing, result.stdout)
	}
	if output.Success || output.Timestamp == "" || output.Session != fixture.session || output.WorkingDir != fixture.projectDir ||
		output.Mode != "orchestrator" || output.AssignStrategy != "top-n" || output.ErrorCode != "ASSIGNMENT_FAILED" || output.Error == "" {
		t.Fatalf("partial-coverage spawn envelope=%+v", output)
	}
	if len(output.Agents) != 2 || len(output.Assignments) != 2 {
		t.Fatalf("partial-coverage spawn agents/assignments=%+v/%+v", output.Agents, output.Assignments)
	}
	for i, agent := range output.Agents {
		if agent.Type != "claude" || agent.Error != "" || agent.Title != fmt.Sprintf("%s__cc_%d", fixture.session, i+1) {
			t.Fatalf("partial-coverage spawned agent[%d]=%+v", i, agent)
		}
	}
	if output.Agents[0].Name != spawnAssignmentDisplayName || output.Agents[1].Name != "NoWorkAgent" {
		t.Fatalf("partial-coverage spawned names=%+v", output.Agents)
	}

	const noWorkError = "no work assignment was produced for this eligible agent"
	var assigned *spawnAssignmentResponse
	var noWork *spawnAssignmentResponse
	claimedAndSent := 0
	for i := range output.Assignments {
		assignment := &output.Assignments[i]
		if assignment.Claimed && assignment.PromptSent {
			claimedAndSent++
			assigned = assignment
		}
		if assignment.ClaimError == noWorkError {
			noWork = assignment
		}
	}
	if claimedAndSent != 1 || assigned == nil || noWork == nil {
		t.Fatalf("partial-coverage assignment split=%+v", output.Assignments)
	}
	if assigned.AgentType != "claude" || assigned.BeadID != fixture.beadID || assigned.BeadTitle != fixture.beadTitle ||
		assigned.Priority != "P1" || assigned.ClaimActor == "" || assigned.IdempotencyKey == "" || assigned.DispatchReceiptID == "" ||
		!equalInts(assigned.ReservationIDs, []int{spawnAssignmentReservationID}) || assigned.ClaimError != "" || assigned.PromptError != "" {
		t.Fatalf("partial-coverage assigned receipt=%+v", assigned)
	}
	decodedKey, err := hex.DecodeString(assigned.IdempotencyKey)
	if err != nil || len(decodedKey) != 32 {
		t.Fatalf("partial-coverage idempotency key=%q bytes=%d err=%v", assigned.IdempotencyKey, len(decodedKey), err)
	}
	wantActor := spawnAssignmentRecipient + "/ntm-" + assigned.IdempotencyKey[:12]
	if assigned.ClaimActor != wantActor || !strings.Contains(assigned.DispatchReceiptID, fixture.paneID) {
		t.Fatalf("partial-coverage atomic identity receipt=%+v want_actor=%q pane_id=%q", assigned, wantActor, fixture.paneID)
	}
	if noWork.AgentType != "claude" || noWork.Pane == "" || noWork.Pane == assigned.Pane || noWork.BeadID != "" || noWork.BeadTitle != "" ||
		noWork.Priority != "" || noWork.Claimed || noWork.PromptSent || noWork.ClaimActor != "" || noWork.IdempotencyKey != "" ||
		noWork.DispatchReceiptID != "" || len(noWork.ReservationIDs) != 0 || noWork.PromptError != "" {
		t.Fatalf("partial-coverage synthesized no-work receipt=%+v", noWork)
	}

	fixture.waitForMarkerCount(t, 1)
	markerCountsBefore := fixture.sessionMarkerCounts(t)
	if markerCountsBefore[fixture.paneID] != 1 || totalSpawnMarkerCount(markerCountsBefore) != 1 {
		t.Fatalf("partial-coverage prompt counts=%v, want exactly one on assigned pane %s", markerCountsBefore, fixture.paneID)
	}
	fixture.assertBead(t, "in_progress", assigned.ClaimActor)
	beadBefore := fixture.mustBR(t, "show", fixture.beadID, "--json")
	record := fixture.readAssignment(t)
	assertSpawnAssignmentRecord(t, record, *assigned, fixture)
	ledgerPath := filepath.Join(fixture.homeDir, ".ntm", "sessions", fixture.session, "assignments.json")
	ledgerBefore, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read partial-coverage assignment ledger: %v", err)
	}
	var ledger spawnAssignmentLedger
	if err := json.Unmarshal(ledgerBefore, &ledger); err != nil || len(ledger.Assignments) != 1 || ledger.Assignments[fixture.beadID] == nil {
		t.Fatalf("partial-coverage ledger=%+v err=%v raw=%s", ledger, err, ledgerBefore)
	}
	mailBefore := fixture.stub.assertCleanMutationCount(t, 1)

	time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
	fixture.assertBead(t, "in_progress", assigned.ClaimActor)
	beadAfter := fixture.mustBR(t, "show", fixture.beadID, "--json")
	if !bytes.Equal(bytes.TrimSpace(beadAfter), bytes.TrimSpace(beadBefore)) {
		t.Fatalf("partial-coverage bead mutated after response: before=%s after=%s", beadBefore, beadAfter)
	}
	ledgerAfter, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("reread partial-coverage assignment ledger: %v", err)
	}
	if !bytes.Equal(ledgerAfter, ledgerBefore) {
		t.Fatalf("partial-coverage ledger mutated after response: before=%s after=%s", ledgerBefore, ledgerAfter)
	}
	markerCountsAfter := fixture.sessionMarkerCounts(t)
	if !equalSpawnMarkerCounts(markerCountsAfter, markerCountsBefore) {
		t.Fatalf("partial-coverage prompt side effects continued after response: before=%v after=%v", markerCountsBefore, markerCountsAfter)
	}
	mailAfter := fixture.stub.assertCleanMutationCount(t, 1)
	if mailAfter != mailBefore {
		t.Fatalf("partial-coverage Agent Mail calls continued after response: before=%+v after=%+v", mailBefore, mailAfter)
	}
}

// TestE2ESpawnAssignFailureReturnsNonzeroSingleJSON proves the built CLI does
// not report a successful process when the requested post-spawn assignment
// phase fails after tmux topology has already been created.
func TestE2ESpawnAssignFailureReturnsNonzeroSingleJSON(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newAtomicAssignmentCLIFixture(t)
	projectsBase := filepath.Join(fixture.root, "spawn-assign-projects")
	projectDir := filepath.Join(projectsBase, fixture.session)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("create spawn assignment failure project: %v", err)
	}
	fixture.mustBRAt(t, projectDir, "init", "--prefix=spawnfail", "--json")
	fakeBin := filepath.Join(fixture.root, "spawn-assign-failure-bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatalf("create spawn assignment failure bin: %v", err)
	}
	writeSpawnEmptyBV(t, filepath.Join(fakeBin, "bv"))

	result := fixture.runNTM(t, map[string]string{
		"NTM_PROJECTS_BASE": projectsBase,
		"PATH":              fakeBin + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
	},
		"--json", "spawn", fixture.session,
		"--cod=1", "--no-user", "--no-hooks", "--no-cass-context", "--no-recovery",
		"--assign", "--ready-timeout=1s", "--assign-timeout=2s",
	)
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("spawn assignment failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	var envelope struct {
		Success   bool     `json:"success"`
		ErrorCode string   `json:"error_code"`
		Error     string   `json:"error"`
		Errors    []string `json:"errors"`
		Spawn     struct {
			Session string `json:"session"`
		} `json:"spawn"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode spawn assignment failure: %v raw=%s", err, result.stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("spawn assignment failure emitted multiple JSON documents: err=%v extra=%v raw=%s", err, extra, result.stdout)
	}
	if envelope.Success || envelope.ErrorCode != "ASSIGNMENT_FAILED" || envelope.Error == "" || len(envelope.Errors) == 0 || envelope.Spawn.Session != fixture.session {
		t.Fatalf("spawn assignment failure envelope=%+v", envelope)
	}
}

// TestE2ESpawnAssignmentSignalCancellationMatrix proves that command-root
// cancellation reaches every production assignment workflow and that each
// command joins its external workers before returning a single terminal JSON
// document. The blocked commands are real child processes; Beads and tmux are
// isolated but otherwise use their production binaries and durable stores.
func TestE2ESpawnAssignmentSignalCancellationMatrix(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("normal_assign_SIGINT_cancels_BV_discovery", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		marker := fmt.Sprintf("NTM_CANCEL_NORMAL_%d", time.Now().UnixNano())
		beadID := fixture.createBead(t, marker)
		env, startedDir := installSpawnSignalBlockingCommand(t, fixture.env, "bv", "", "*")
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGINT,
			"--json", "assign", fixture.session,
			"--repo="+fixture.projectDir,
			"--beads="+beadID,
			"--limit=1",
			"--auto",
			"--reserve-files=false",
			"--timeout=30s",
		)
		assertSpawnSignalJSONFailure(t, result, "")
		fixture.assertBead(t, beadID, "open", "")
		fixture.assertLedgerHasNoAssignment(t, beadID)
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	})

	t.Run("direct_assign_SIGTERM_cancels_atomic_delivery_before_enter", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		marker := fmt.Sprintf("NTM_CANCEL_DIRECT_%d", time.Now().UnixNano())
		beadID := fixture.createBead(t, marker)
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, fixture.env, fixture.tmuxPath)
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGTERM,
			atomicDirectArgs(fixture, beadID, marker, false)...,
		)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		logData, err := os.ReadFile(sendLog)
		if err != nil {
			t.Fatalf("read direct-assignment staged tmux send log: %v", err)
		}
		logText := string(logData)
		if strings.Count(logText, "send-keys") != 1 || !strings.Contains(logText, " -l ") || strings.Contains(logText, " Enter") {
			t.Fatalf("canceled direct assignment tmux calls=%q, want one literal stage and no Enter", logText)
		}
		record := fixture.readLedgerAssignment(t, beadID)
		if record.ClaimState != "claimed" || record.ClaimActor == "" || record.ClaimedAt == nil ||
			record.DispatchState != "sending" || record.DispatchAttempts != 1 || record.DispatchedAt != nil ||
			record.DispatchReceiptID != "" || record.PromptSent != "" {
			t.Fatalf("canceled direct assignment durable boundary = %+v", record)
		}
		fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	})

	t.Run("direct_send_SIGINT_cancels_after_staging_before_enter", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		marker := fmt.Sprintf("NTM_CANCEL_SEND_%d", time.Now().UnixNano())
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, fixture.env, fixture.tmuxPath)
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGINT,
			"--json", "send", fixture.session,
			"--pane="+fixture.panes[0].ID,
			"--no-hooks",
			"--no-cass-check",
			marker,
		)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")

		// Cancellation lands during the one-second double-Enter delay. Waiting
		// past the complete legacy protocol proves no orphaned timer or process
		// can submit either Enter after the command has returned.
		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		logData, err := os.ReadFile(sendLog)
		if err != nil {
			t.Fatalf("read staged tmux send log: %v", err)
		}
		logText := string(logData)
		if strings.Count(logText, "send-keys") != 1 || !strings.Contains(logText, " -l ") || strings.Contains(logText, " Enter") {
			t.Fatalf("canceled direct send tmux calls=%q, want one literal stage and no Enter", logText)
		}
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
		assertSpawnTimelineExcludesMarker(t, filepath.Join(fixture.root, "data", "ntm", "timelines"), marker)
	})

	t.Run("cli_add_SIGTERM_cancels_agent_launch_before_enter", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		fakeAgentDir := filepath.Join(fixture.root, "add-agent-bin")
		if err := os.MkdirAll(fakeAgentDir, 0o700); err != nil {
			t.Fatalf("create add fake-agent directory: %v", err)
		}
		launchMarker := filepath.Join(fixture.root, "add-agent-launched")
		writeSpawnLaunchMarkerAgent(t, filepath.Join(fakeAgentDir, "codex"), launchMarker)
		env := atomicAssignmentMergeEnv(fixture.env, map[string]string{
			"PATH": fakeAgentDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(fixture.env, "PATH"),
		})
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, env, fixture.tmuxPath)
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGTERM,
			"--json", "add", fixture.session, "--cod=1", "--no-cass-context",
		)
		assertSpawnSignalCodedJSONError(t, result, "TIMEOUT")

		// The pane split and prompt stage may already have completed. Waiting past
		// the Enter delay proves the canceled command cannot submit or launch the
		// agent after its process has returned.
		time.Sleep(tmux.DefaultEnterDelay + 250*time.Millisecond)
		assertSpawnStagedWithoutEnter(t, sendLog)
		if _, err := os.Stat(launchMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("added agent launched after cancellation: marker error=%v", err)
		}
	})

	t.Run("cli_spawn_SIGINT_cancels_agent_launch_before_enter", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		spawnSession := fmt.Sprintf("ntm-e2e-cli-launch-%d", time.Now().UnixNano())
		projectsBase := filepath.Join(fixture.root, "spawn-projects")
		spawnDir := filepath.Join(projectsBase, spawnSession)
		fakeAgentDir := filepath.Join(fixture.root, "spawn-agent-bin")
		if err := os.MkdirAll(spawnDir, 0o700); err != nil {
			t.Fatalf("create CLI spawn project: %v", err)
		}
		if err := os.MkdirAll(fakeAgentDir, 0o700); err != nil {
			t.Fatalf("create CLI spawn fake-agent directory: %v", err)
		}
		launchMarker := filepath.Join(fixture.root, "cli-agent-launched")
		writeSpawnLaunchMarkerAgent(t, filepath.Join(fakeAgentDir, "claude"), launchMarker)
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, fixture.env, fixture.tmuxPath)
		env = atomicAssignmentMergeEnv(env, map[string]string{
			"NTM_PROJECTS_BASE": projectsBase,
			"PATH":              fakeAgentDir + string(os.PathListSeparator) + atomicAssignmentEnvValue(env, "PATH"),
		})
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, spawnDir, env, startedDir, 1, syscall.SIGINT,
			"--json", "spawn", spawnSession,
			"--cc=1", "--no-user", "--no-hooks", "--no-cass-context", "--no-recovery",
		)
		assertSpawnSignalCodedJSONError(t, result, "TIMEOUT")
		time.Sleep(500 * time.Millisecond)
		assertSpawnStagedWithoutEnter(t, sendLog)
		if _, err := os.Stat(launchMarker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CLI agent launched after cancellation: marker error=%v", err)
		}
	})

	t.Run("robot_spawn_SIGTERM_cancels_agent_launch_before_enter", func(t *testing.T) {
		fixture := newSpawnAssignmentCLIFixture(t)
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, fixture.env, fixture.tmuxPath)
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGTERM, fixture.spawnArgs()...)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		time.Sleep(500 * time.Millisecond)
		assertSpawnStagedWithoutEnter(t, sendLog)
		fixture.assertBead(t, "open", "")
		fixture.assertMarkerCount(t, 0)
		fixture.stub.assertNoCalls(t)
	})

	t.Run("distribute_SIGINT_cancels_after_staging_before_enter", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		marker := fmt.Sprintf("NTM_CANCEL_DISTRIBUTE_%d", time.Now().UnixNano())
		beadID := fixture.createBead(t, marker)
		fixture.primePaneForSafeDispatch(t, 0)
		fixture.primePaneForSafeDispatch(t, 1)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(fixture.root, "config"))
		registry := agentmail.NewSessionAgentRegistry(fixture.session, fixture.projectDir)
		for pane, endpoint := range fixture.panes {
			registry.AddAgent(endpoint.Title, endpoint.ID, fmt.Sprintf("DistributeAgent%d", pane))
		}
		if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
			t.Fatalf("save distribute session project mapping: %v", err)
		}
		env, startedDir, sendLog := installSpawnSignalStagingTMUX(t, fixture.env, fixture.tmuxPath)
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 1, syscall.SIGINT,
			"--json", "send", fixture.session,
			"--distribute",
			"--dist-limit=1",
			"--dist-auto",
		)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		assertSpawnStagedWithoutEnter(t, sendLog)
		record := fixture.readLedgerAssignment(t, beadID)
		if record.ClaimState != "claimed" || record.ClaimActor == "" || record.ClaimedAt == nil ||
			record.DispatchState != "sending" || record.DispatchAttempts != 1 || record.DispatchedAt != nil ||
			record.DispatchReceiptID != "" || record.PromptSent != "" {
			t.Fatalf("canceled distribute durable boundary = %+v", record)
		}
		fixture.assertBead(t, beadID, "in_progress", record.ClaimActor)
		stagedCopies := 0
		for pane := range fixture.panes {
			stagedCopies += strings.Count(fixture.capturePane(t, pane), marker)
		}
		if stagedCopies != 1 {
			t.Fatalf("canceled distribute marker staged copies=%d, want exactly one unsubmitted copy", stagedCopies)
		}
	})

	t.Run("robot_bulk_parallel_SIGTERM_joins_claim_workers", func(t *testing.T) {
		fixture := newAtomicAssignmentCLIFixture(t)
		marker := fmt.Sprintf("NTM_CANCEL_BULK_%d", time.Now().UnixNano())
		firstBead := fixture.createBead(t, marker+"_ONE")
		secondBead := fixture.createBead(t, marker+"_TWO")
		fixture.primePaneForSafeDispatch(t, 0)
		fixture.primePaneForSafeDispatch(t, 1)
		env, startedDir := installSpawnSignalBlockingCommand(t, fixture.env, "tmux", fixture.tmuxPath, "capture-pane")
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, env, startedDir, 2, syscall.SIGTERM,
			"--robot-format=json",
			"--robot-bulk-assign="+fixture.session,
			"--from-bv",
			"--bulk-strategy=ready",
			"--bulk-parallel",
		)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		for _, beadID := range []string{firstBead, secondBead} {
			fixture.assertBead(t, beadID, "open", "")
			assertAtomicSignalCancellationNoActuation(t, fixture, beadID)
		}
		fixture.assertMarkerCounts(t, marker, map[int]int{0: 0, 1: 0})
	})

	t.Run("robot_spawn_SIGINT_cancels_assignment_claim", func(t *testing.T) {
		fixture := newSpawnAssignmentCLIFixture(t)
		startedDir := t.TempDir()
		fixture.stub.blockAgentsPath = filepath.Join(startedDir, "agents-read")
		fixture.stub.blockAgentsAt = 1
		result := runSpawnSignalCanceledCLI(t, fixture.ntmPath, fixture.projectDir, fixture.env, startedDir, 1, syscall.SIGINT, fixture.spawnArgs()...)
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		fixture.assertBead(t, "open", "")
		fixture.assertMarkerCount(t, 0)
		counts := fixture.stub.assertCleanMutationCount(t, 0)
		if counts.ensure < 1 || counts.list < fixture.stub.blockAgentsAt {
			t.Fatalf("spawn assignment cancellation stopped before assignment admission: counts=%+v", counts)
		}
		assertSpawnSignalCancellationNoActuation(t, fixture)
	})
}

// TestE2ESpawnPromptCanonicalDispatch runs the built CLI against a private
// real tmux server. It proves user and init prompts reach the exact spawned
// pane in order, final-message redaction applies at delivery, the init phase
// exposes a canonical receipt, and the identity preamble names the physical
// pane deterministically.
func TestE2ESpawnPromptCanonicalDispatch(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Skipf("br is required for spawn init E2E: %v", err)
	}

	root := t.TempDir()
	session := fmt.Sprintf("ntm-e2e-spawn-prompt-%d-%d", os.Getpid(), time.Now().UnixNano())
	projectsBase := filepath.Join(root, "projects")
	projectDir := filepath.Join(projectsBase, session)
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{projectDir, homeDir, configDir, fakeBin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create spawn prompt fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated zsh configuration: %v", err)
	}
	writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))
	writeSpawnEmptyBV(t, filepath.Join(fakeBin, "bv"))
	if err := os.MkdirAll(filepath.Join(configDir, "ntm"), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "ntm", "config.toml"), []byte(strings.Join([]string{
		"[agent_mail]",
		"enabled = false",
		"",
		"[redaction]",
		"mode = \"redact\"",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write spawn prompt config: %v", err)
	}

	env := spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            projectsBase,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, tmuxPath, env) })

	brCtx, brCancel := context.WithTimeout(context.Background(), 15*time.Second)
	brCmd := exec.CommandContext(brCtx, brPath, "init", "--prefix=spawnprompt", "--json")
	brCmd.Dir = projectDir
	brCmd.Env = append([]string(nil), env...)
	brOutput, brErr := brCmd.CombinedOutput()
	brCancel()
	if brErr != nil {
		t.Fatalf("initialize isolated Beads fixture: %v output=%s", brErr, brOutput)
	}

	const secret = "hunter2hunter2"
	userMarker := "SPAWN_USER_PROMPT_MARKER"
	initMarker := "SPAWN_INIT_PROMPT_MARKER"
	args := []string{
		"--json", "spawn", session,
		"--cc=1", "--no-user", "--no-hooks", "--no-cass-context", "--no-recovery",
		"--prompt=" + userMarker + " password=" + secret,
		"--assign", "--init-prompt=" + initMarker + " password=" + secret,
		"--with-agent-name", "--ready-timeout=8s", "--assign-timeout=5s",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	cmd := exec.CommandContext(ctx, ntmPath, args...)
	cmd.Dir = projectDir
	cmd.Env = append([]string(nil), env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	cancel()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("spawn prompt CLI timed out: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if runErr != nil || strings.TrimSpace(stderr.String()) != "" {
		debugCtx, debugCancel := context.WithTimeout(context.Background(), 5*time.Second)
		debugCmd := exec.CommandContext(debugCtx, tmuxPath, "capture-pane", "-p", "-t", session, "-S", "-200")
		debugCmd.Env = append([]string(nil), env...)
		debugOutput, debugErr := debugCmd.CombinedOutput()
		debugCancel()
		t.Fatalf(
			"spawn prompt CLI error=%v stdout=%s stderr=%s pane_capture_error=%v pane=%q",
			runErr, stdout.String(), stderr.String(), debugErr, debugOutput,
		)
	}

	var output spawnPromptCLIOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode spawn prompt output: %v raw=%s", err, stdout.String())
	}
	if !output.Spawn.Created || output.Spawn.Session != session || !output.Init.PromptSent || output.Init.AgentsReached != 1 {
		t.Fatalf("spawn/init envelope = %+v", output)
	}
	if len(output.Init.Receipts) != 1 {
		t.Fatalf("init receipts = %+v, want one", output.Init.Receipts)
	}
	receipt := output.Init.Receipts[0]
	if receipt.Status != "delivered" || receipt.Protocol != "double_enter" || receipt.Target.Ref.PaneID == "" ||
		receipt.Target.AgentType != "cc" || receipt.Redaction.Mode != "redact" || receipt.Redaction.Findings == 0 {
		t.Fatalf("init receipt = %+v", receipt)
	}

	captureCtx, captureCancel := context.WithTimeout(context.Background(), 5*time.Second)
	captureCmd := exec.CommandContext(captureCtx, tmuxPath, "capture-pane", "-p", "-t", receipt.Target.Ref.PaneID, "-S", "-2000")
	captureCmd.Env = append([]string(nil), env...)
	captured, captureErr := captureCmd.CombinedOutput()
	captureCancel()
	if captureErr != nil {
		t.Fatalf("capture spawned pane: %v output=%s", captureErr, captured)
	}
	paneOutput := string(captured)
	compactOutput := strings.ReplaceAll(paneOutput, "\n", "")
	userDelivery := "RECEIVED:" + userMarker
	initDelivery := "RECEIVED:" + initMarker
	userIndex := strings.Index(compactOutput, userDelivery)
	identity := fmt.Sprintf("You are agent `%s_claude_%d`", session, receipt.Target.Ref.PaneIndex)
	identityIndex := strings.Index(compactOutput, identity)
	initIndex := strings.Index(compactOutput, initDelivery)
	if userIndex < 0 || identityIndex <= userIndex || initIndex <= identityIndex {
		t.Fatalf("prompt order user=%d identity=%d init=%d pane=%q", userIndex, identityIndex, initIndex, paneOutput)
	}
	if strings.Contains(paneOutput, secret) || !strings.Contains(paneOutput, "[REDACTED:PASSWORD:") {
		t.Fatalf("final-message redaction failed: pane=%q", paneOutput)
	}
	if strings.Count(compactOutput, userDelivery) != 1 || strings.Count(compactOutput, initDelivery) != 1 {
		t.Fatalf("prompt delivery count mismatch: pane=%q", paneOutput)
	}
}

// TestE2ESpawnPromptFailureTruthfulness exercises prompt failures through the
// built CLI and a private real tmux server. Every JSON case must fail nonzero
// with one terminal document, while reporting that the session/panes created
// before the failure still exist. Human mode must return the same failure as a
// normal command error and must not print a success footer.
func TestE2ESpawnPromptFailureTruthfulness(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	projectsBase := filepath.Join(root, "projects")
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{
		projectsBase,
		homeDir,
		configDir,
		fakeBin,
		filepath.Join(root, "data"),
		filepath.Join(configDir, "ntm"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create prompt failure fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated zsh configuration: %v", err)
	}
	writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))

	configPath := filepath.Join(configDir, "ntm", "config.toml")
	writeConfig := func(t *testing.T, promptConfig string) {
		t.Helper()
		contents := strings.Join([]string{
			"[agents]",
			"claude = \"claude {{if .Model}}--model {{shellQuote .Model}}{{end}} {{if .SystemPromptFile}}--append-system-prompt-file {{shellQuote .SystemPromptFile}}{{end}}\"",
			"",
			"[agent_mail]",
			"enabled = false",
			"",
			"[redaction]",
			"mode = \"off\"",
			"",
			promptConfig,
		}, "\n")
		if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
			t.Fatalf("write prompt failure config: %v", err)
		}
	}
	writeConfig(t, "")

	env := spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            projectsBase,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, tmuxPath, env) })

	failingTmux := filepath.Join(fakeBin, "tmux-fail-requested-prompt")
	failingTmuxScript := `#!/bin/sh
if [ "${1:-}" = "send-keys" ]; then
    case "$*" in
        *"$NTM_E2E_FAIL_PROMPT"*)
            printf 'injected requested prompt failure for %s\n' "$NTM_E2E_FAIL_PROMPT" >&2
            exit 97
            ;;
    esac
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(failingTmux, []byte(failingTmuxScript), 0o700); err != nil {
		t.Fatalf("write prompt-failing tmux wrapper: %v", err)
	}
	blockingTmux := filepath.Join(fakeBin, "tmux-block-requested-prompt")
	blockingTmuxScript := `#!/bin/sh
if [ "${1:-}" = "send-keys" ]; then
    case "$*" in
        *"$NTM_E2E_FAIL_PROMPT"*)
            : > "$NTM_E2E_PROMPT_STARTED"
            exec sleep 600
            ;;
    esac
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(blockingTmux, []byte(blockingTmuxScript), 0o700); err != nil {
		t.Fatalf("write prompt-blocking tmux wrapper: %v", err)
	}

	type processResult struct {
		stdout   []byte
		stderr   []byte
		exitCode int
	}
	runCLI := func(t *testing.T, dir string, extraEnv map[string]string, args ...string) processResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath, args...)
		cmd.Dir = dir
		cmd.Env = spawnAssignmentMergeEnv(env, extraEnv)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("prompt failure command timed out: args=%q stdout=%s stderr=%s", args, stdout.String(), stderr.String())
		}
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run prompt failure command: %v", runErr)
			}
			exitCode = exitErr.ExitCode()
		}
		return processResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
	}
	runCanceledCLI := func(t *testing.T, dir string, extraEnv map[string]string, startedPath string, args ...string) processResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath, args...)
		cmd.Dir = dir
		cmd.Env = spawnAssignmentMergeEnv(env, extraEnv)
		group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
		if groupErr != nil {
			t.Fatalf("create owned process group for cancellable prompt command: %v", groupErr)
		}
		cmd.Cancel = func() error {
			return group.Signal(os.Kill)
		}
		cmd.WaitDelay = 2 * time.Second
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			closeErr := group.Close()
			t.Fatalf("start cancellable prompt command: %v; close owned process group: %v", err, closeErr)
		}
		waitDone := make(chan error, 1)
		go func() { waitDone <- cmd.Wait() }()
		joined := false
		var waitErr error
		var closeErr error
		joinResult := func(commandErr error) error {
			waitErr = commandErr
			closeErr = group.Close()
			joined = true
			return errors.Join(waitErr, closeErr)
		}
		abortAndJoin := func() error {
			if joined {
				return errors.Join(waitErr, closeErr)
			}
			cancel()
			_ = group.Signal(os.Kill)
			return joinResult(<-waitDone)
		}
		defer func() {
			if joined {
				return
			}
			_ = abortAndJoin()
			if closeErr != nil {
				t.Errorf("close owned process group for cancellable prompt command: %v", closeErr)
			}
		}()

		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(startedPath); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat prompt dispatch marker: %v", err)
			}
			select {
			case commandErr := <-waitDone:
				joinErr := joinResult(commandErr)
				t.Fatalf("prompt command exited before cancellation point: %v stdout=%s stderr=%s", joinErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				joinErr := abortAndJoin()
				t.Fatalf("prompt command never reached blocked dispatch: join_err=%v stdout=%s stderr=%s", joinErr, stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("signal blocked prompt command: %v", err)
		}
		select {
		case commandErr := <-waitDone:
			_ = joinResult(commandErr)
		case <-ctx.Done():
			joinErr := abortAndJoin()
			t.Fatalf("blocked prompt command did not stop after signal: join_err=%v stdout=%s stderr=%s", joinErr, stdout.String(), stderr.String())
		}
		if closeErr != nil {
			t.Fatalf("close owned process group for canceled prompt command: %v", closeErr)
		}
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("wait for canceled prompt command: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		return processResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
	}

	type failureEnvelope struct {
		Success         bool     `json:"success"`
		GeneratedAt     string   `json:"generated_at"`
		Session         string   `json:"session"`
		Error           string   `json:"error"`
		ErrorCode       string   `json:"error_code"`
		Code            string   `json:"code"`
		PartialMutation bool     `json:"partial_mutation"`
		SessionMayExist bool     `json:"session_may_exist"`
		AffectedPaneIDs []string `json:"affected_pane_ids"`
	}
	assertJSONFailure := func(t *testing.T, result processResult, session, code, errorFragment string) failureEnvelope {
		t.Helper()
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("prompt failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		decoder := json.NewDecoder(bytes.NewReader(result.stdout))
		var envelope failureEnvelope
		if err := decoder.Decode(&envelope); err != nil {
			t.Fatalf("decode prompt failure document: %v raw=%s", err, result.stdout)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			t.Fatalf("prompt failure emitted multiple JSON documents: err=%v extra=%v raw=%s", err, extra, result.stdout)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(result.stdout, &raw); err != nil {
			t.Fatalf("decode raw prompt failure document: %v", err)
		}
		if value, ok := raw["success"]; !ok || string(value) != "false" {
			t.Fatalf("prompt failure omitted explicit success:false: raw=%s", result.stdout)
		}
		if envelope.Success || envelope.GeneratedAt == "" || envelope.Session != session ||
			envelope.ErrorCode != code || envelope.Code != code ||
			!envelope.PartialMutation || !envelope.SessionMayExist ||
			len(envelope.AffectedPaneIDs) == 0 || !strings.Contains(envelope.Error, errorFragment) {
			t.Fatalf("prompt failure envelope=%+v, want session=%s code=%s fragment=%q", envelope, session, code, errorFragment)
		}
		if bytes.Contains(result.stdout, []byte(`"created":true`)) || bytes.Contains(result.stdout, []byte(`"total_added"`)) {
			t.Fatalf("prompt failure leaked a success payload: %s", result.stdout)
		}
		return envelope
	}
	assertSessionExists := func(t *testing.T, session string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, tmuxPath, "has-session", "-t", session)
		cmd.Env = append([]string(nil), env...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("partially mutated session %s is missing: %v output=%s", session, err, output)
		}
	}
	createProject := func(t *testing.T, session string) string {
		t.Helper()
		dir := filepath.Join(projectsBase, session)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create prompt failure project %s: %v", session, err)
		}
		return dir
	}
	createShellSession := func(t *testing.T, session, dir string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, tmuxPath, "new-session", "-d", "-s", session, "-c", dir)
		cmd.Env = append([]string(nil), env...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create add fixture session %s: %v output=%s", session, err, output)
		}
	}
	failingEnv := func(marker string) map[string]string {
		return map[string]string{
			"NTM_TMUX_BINARY":     failingTmux,
			"NTM_E2E_REAL_TMUX":   tmuxPath,
			"NTM_E2E_FAIL_PROMPT": marker,
		}
	}
	baseSpawnArgs := func(session string) []string {
		return []string{"spawn", session, "--cc=1", "--no-user", "--no-hooks", "--no-cass-context", "--no-recovery"}
	}

	t.Run("add_explicit_prompt_json_send_failure", func(t *testing.T) {
		writeConfig(t, "")
		session := fmt.Sprintf("ntm-e2e-add-prompt-json-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		createShellSession(t, session, dir)
		marker := "ADD_EXPLICIT_PROMPT_FAILURE"
		result := runCLI(t, dir, failingEnv(marker),
			"--json", "add", session, "--cc=1", "--no-cass-context", "--prompt="+marker,
		)
		assertJSONFailure(t, result, session, "PROMPT_SEND_FAILED", "sending explicit prompt")
		assertSessionExists(t, session)
	})

	t.Run("add_explicit_prompt_human_send_failure", func(t *testing.T) {
		writeConfig(t, "")
		session := fmt.Sprintf("ntm-e2e-add-prompt-human-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		createShellSession(t, session, dir)
		marker := "ADD_HUMAN_PROMPT_FAILURE"
		result := runCLI(t, dir, failingEnv(marker),
			"add", session, "--cc=1", "--no-cass-context", "--prompt="+marker,
		)
		if result.exitCode != 1 || !strings.Contains(string(result.stderr), "sending explicit prompt") {
			t.Fatalf("human add prompt failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if bytes.Contains(result.stdout, []byte("Added 1 agent")) || json.Valid(result.stdout) {
			t.Fatalf("human add prompt failure printed false success/JSON: %s", result.stdout)
		}
		assertSessionExists(t, session)
	})

	t.Run("add_explicit_prompt_cancellation_is_timeout", func(t *testing.T) {
		writeConfig(t, "")
		session := fmt.Sprintf("ntm-e2e-add-prompt-cancel-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		createShellSession(t, session, dir)
		marker := "ADD_CANCEL_PROMPT_FAILURE"
		startedPath := filepath.Join(root, session+"-dispatch-started")
		result := runCanceledCLI(t, dir, map[string]string{
			"NTM_TMUX_BINARY":        blockingTmux,
			"NTM_E2E_REAL_TMUX":      tmuxPath,
			"NTM_E2E_FAIL_PROMPT":    marker,
			"NTM_E2E_PROMPT_STARTED": startedPath,
		}, startedPath,
			"--json", "add", session, "--cc=1", "--no-cass-context", "--prompt="+marker,
		)
		assertJSONFailure(t, result, session, "TIMEOUT", "added-agent prompt canceled")
		assertSessionExists(t, session)
	})

	t.Run("add_explicit_prompt_waits_for_agent_readiness", func(t *testing.T) {
		writeConfig(t, "")
		readyFile := filepath.Join(root, fmt.Sprintf("add-agent-ready-%d", time.Now().UnixNano()))
		writeSpawnGatedFakeClaude(t, filepath.Join(fakeBin, "claude"), readyFile)
		defer writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))

		session := fmt.Sprintf("ntm-e2e-add-prompt-ready-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		createShellSession(t, session, dir)
		marker := fmt.Sprintf("ADD_READY_PROMPT_%d", time.Now().UnixNano())

		ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath,
			"--json", "add", session, "--cc=1", "--no-cass-context", "--prompt="+marker,
		)
		cmd.Dir = dir
		cmd.Env = append([]string(nil), env...)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
		if groupErr != nil {
			t.Fatalf("create owned process group for readiness-gated add: %v", groupErr)
		}
		cmd.Cancel = func() error {
			return group.Signal(os.Kill)
		}
		cmd.WaitDelay = 10 * time.Second
		if err := cmd.Start(); err != nil {
			closeErr := group.Close()
			t.Fatalf("start readiness-gated add: %v; close owned process group: %v", err, closeErr)
		}
		joined := false
		var commandErr error
		var closeErr error
		join := func() {
			if joined {
				return
			}
			commandErr = cmd.Wait()
			closeErr = group.Close()
			joined = true
		}
		defer func() {
			if joined {
				return
			}
			cancel()
			_ = group.Signal(os.Kill)
			join()
			if closeErr != nil {
				t.Errorf("close owned process group for readiness-gated add: %v", closeErr)
			}
		}()

		var paneID string
		readPane := func() string {
			t.Helper()
			if paneID == "" {
				listCtx, listCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer listCancel()
				listCmd := exec.CommandContext(listCtx, tmuxPath,
					"list-panes", "-s", "-t", session, "-F", "#{pane_id}|#{pane_title}",
				)
				listCmd.Env = append([]string(nil), env...)
				if output, err := listCmd.Output(); err == nil {
					for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
						parts := strings.SplitN(line, "|", 2)
						if len(parts) == 2 && strings.Contains(parts[1], "__cc_") {
							paneID = parts[0]
							break
						}
					}
				}
			}
			if paneID == "" {
				return ""
			}
			captureCtx, captureCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer captureCancel()
			captureCmd := exec.CommandContext(captureCtx, tmuxPath, "capture-pane", "-p", "-t", paneID, "-S", "-200")
			captureCmd.Env = append([]string(nil), env...)
			output, _ := captureCmd.Output()
			return string(output)
		}

		waitingDeadline := time.Now().Add(defaultTmuxSetupTimeout)
		for time.Now().Before(waitingDeadline) {
			captured := readPane()
			if strings.Contains(captured, "WAITING_FOR_E2E_READY") {
				if strings.Contains(captured, marker) {
					t.Fatalf("prompt reached pane before readiness gate opened: %s", captured)
				}
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if captured := readPane(); !strings.Contains(captured, "WAITING_FOR_E2E_READY") {
			t.Fatalf("added agent never reached readiness gate: pane=%q stdout=%s stderr=%s", captured, stdout.String(), stderr.String())
		} else if strings.Contains(captured, marker) {
			t.Fatalf("prompt was actuated before readiness: %s", captured)
		}
		if err := os.WriteFile(readyFile, []byte("ready\n"), 0o600); err != nil {
			t.Fatalf("open added-agent readiness gate: %v", err)
		}
		join()
		if closeErr != nil {
			t.Fatalf("close owned process group for readiness-gated add: %v", closeErr)
		}
		if commandErr != nil {
			t.Fatalf("readiness-gated add failed: %v stdout=%s stderr=%s", commandErr, stdout.String(), stderr.String())
		}
		if strings.TrimSpace(stderr.String()) != "" {
			t.Fatalf("readiness-gated add stderr=%q, want empty", stderr.String())
		}
		var response struct {
			TotalAdded int `json:"total_added"`
			NewPanes   []struct {
				PaneID      string `json:"pane_id"`
				PaneTarget  string `json:"pane_target"`
				WindowIndex int    `json:"window_index"`
				Index       int    `json:"index"`
				Title       string `json:"title"`
			} `json:"new_panes"`
		}
		decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode readiness-gated add response: %v raw=%s", err, stdout.String())
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			t.Fatalf("readiness-gated add emitted multiple JSON documents: err=%v extra=%v raw=%s", err, extra, stdout.String())
		}
		if response.TotalAdded != 1 || len(response.NewPanes) != 1 || response.NewPanes[0].PaneID != paneID ||
			response.NewPanes[0].PaneTarget != fmt.Sprintf("%d.%d", response.NewPanes[0].WindowIndex, response.NewPanes[0].Index) {
			t.Fatalf("readiness-gated add response=%+v", response)
		}
		deliveryDeadline := time.Now().Add(defaultTmuxSetupTimeout)
		for time.Now().Before(deliveryDeadline) {
			if strings.Contains(readPane(), "RECEIVED:"+marker) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("prompt was not delivered after readiness gate opened: pane=%q", readPane())
	})

	// #232: a recovery source that outruns the recovery window degrades the
	// spawn to PARTIAL_RECOVERY instead of aborting it after the panes were
	// already created. The window stays bounded and the agents still get their
	// prompt.
	t.Run("spawn_recovery_deadline_is_bounded_and_degrades_to_partial", func(t *testing.T) {
		writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))
		writeConfig(t, strings.Join([]string{
			"[recovery]",
			"enabled = true",
			"include_agent_mail = false",
			"include_cm_memories = false",
			"include_beads_context = true",
			"auto_inject_on_spawn = true",
			"timeout_seconds = 2",
		}, "\n"))
		brStarted := filepath.Join(root, fmt.Sprintf("recovery-br-started-%d", time.Now().UnixNano()))
		quotedStarted := strings.ReplaceAll(brStarted, "'", "'\"'\"'")
		blockingBR := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
    if [ "$arg" = "list" ]; then
        : > '%s'
        while :; do sleep 1; done
    fi
done
printf '[]\n'
`, quotedStarted)
		if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(blockingBR), 0o700); err != nil {
			t.Fatalf("write recovery-blocking br: %v", err)
		}

		session := fmt.Sprintf("ntm-e2e-recovery-timeout-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
			t.Fatalf("create recovery beads directory: %v", err)
		}
		marker := fmt.Sprintf("RECOVERY_TIMEOUT_STILL_DELIVERS_%d", time.Now().UnixNano())
		startedAt := time.Now()
		result := runCLI(t, dir, nil,
			"--json", "spawn", session, "--cc=1", "--no-user", "--no-hooks", "--no-cass-context", "--prompt="+marker,
		)
		elapsed := time.Since(startedAt)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("timed-out recovery spawn exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if elapsed > 20*time.Second {
			t.Fatalf("recovery timeout took %s, want bounded near the 2s window", elapsed)
		}
		if _, err := os.Stat(brStarted); err != nil {
			t.Fatalf("blocking br was not reached: %v", err)
		}
		var response struct {
			Created  bool `json:"created"`
			Recovery *struct {
				Enabled   bool     `json:"enabled"`
				Partial   bool     `json:"partial"`
				ErrorCode string   `json:"error_code"`
				Warnings  []string `json:"warnings"`
			} `json:"recovery"`
		}
		decoder := json.NewDecoder(bytes.NewReader(result.stdout))
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode timed-out recovery spawn: %v raw=%s", err, result.stdout)
		}
		if !response.Created || response.Recovery == nil || !response.Recovery.Enabled ||
			!response.Recovery.Partial || response.Recovery.ErrorCode != "PARTIAL_RECOVERY" {
			t.Fatalf("timed-out recovery status=%+v raw=%s", response, result.stdout)
		}
		warnings := strings.Join(response.Recovery.Warnings, "\n")
		if !strings.Contains(warnings, "beads") || !strings.Contains(warnings, "timed out") {
			t.Fatalf("timed-out recovery warnings=%q, want the timed-out source named", warnings)
		}
		assertSessionExists(t, session)
		capture := func() string {
			t.Helper()
			captureCtx, captureCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer captureCancel()
			captureCmd := exec.CommandContext(captureCtx, tmuxPath, "capture-pane", "-p", "-t", session+":0.0", "-S", "-200")
			captureCmd.Env = append([]string(nil), env...)
			output, _ := captureCmd.Output()
			return string(output)
		}
		deliveryDeadline := time.Now().Add(defaultTmuxSetupTimeout)
		delivered := false
		for time.Now().Before(deliveryDeadline) {
			if strings.Contains(capture(), "RECEIVED:"+marker) {
				delivered = true
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !delivered {
			t.Fatalf("partial recovery spawn never delivered the prompt: pane=%q", capture())
		}
	})

	t.Run("spawn_partial_recovery_is_structured_in_success_json", func(t *testing.T) {
		writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))
		writeConfig(t, strings.Join([]string{
			"[recovery]",
			"enabled = true",
			"include_agent_mail = false",
			"include_cm_memories = false",
			"include_beads_context = true",
			"auto_inject_on_spawn = true",
		}, "\n"))
		failingBR := `#!/bin/sh
printf 'injected recovery source failure\n' >&2
exit 73
`
		if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(failingBR), 0o700); err != nil {
			t.Fatalf("write recovery-failing br: %v", err)
		}

		session := fmt.Sprintf("ntm-e2e-recovery-partial-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
			t.Fatalf("create partial recovery beads directory: %v", err)
		}
		result := runCLI(t, dir, nil,
			"--json", "spawn", session, "--cc=1", "--no-user", "--no-hooks", "--no-cass-context",
		)
		if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("partial recovery spawn exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		var response struct {
			Created  bool `json:"created"`
			Recovery *struct {
				Enabled   bool     `json:"enabled"`
				Applied   bool     `json:"applied"`
				Partial   bool     `json:"partial"`
				ErrorCode string   `json:"error_code"`
				Warnings  []string `json:"warnings"`
			} `json:"recovery"`
		}
		decoder := json.NewDecoder(bytes.NewReader(result.stdout))
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("decode partial recovery spawn: %v raw=%s", err, result.stdout)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			t.Fatalf("partial recovery spawn emitted multiple JSON documents: err=%v extra=%v raw=%s", err, extra, result.stdout)
		}
		if !response.Created || response.Recovery == nil || !response.Recovery.Enabled || response.Recovery.Applied ||
			!response.Recovery.Partial || response.Recovery.ErrorCode != "PARTIAL_RECOVERY" || len(response.Recovery.Warnings) != 1 ||
			!strings.Contains(response.Recovery.Warnings[0], "beads") {
			t.Fatalf("partial recovery status=%+v", response)
		}
	})

	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "persona_prompt_preparation", flag: "--persona=reviewer"},
		{name: "profile_prompt_preparation", flag: "--profiles=reviewer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeConfig(t, "")
			session := fmt.Sprintf("ntm-e2e-%s-%d", strings.ReplaceAll(test.name, "_", "-"), time.Now().UnixNano())
			dir := createProject(t, session)
			ntmDir := filepath.Join(dir, ".ntm")
			if err := os.MkdirAll(ntmDir, 0o700); err != nil {
				t.Fatalf("create persona .ntm directory: %v", err)
			}
			if err := os.WriteFile(filepath.Join(ntmDir, "prompts"), []byte("path collision"), 0o600); err != nil {
				t.Fatalf("create persona prompt path collision: %v", err)
			}
			args := append([]string{"--json"}, baseSpawnArgs(session)...)
			args = append(args, test.flag)
			result := runCLI(t, dir, nil, args...)
			assertJSONFailure(t, result, session, "INTERNAL_ERROR", "prompts path is not a directory")
			assertSessionExists(t, session)
		})
	}

	t.Run("persona_launch_prompt_send_failure", func(t *testing.T) {
		writeConfig(t, "")
		session := fmt.Sprintf("ntm-e2e-persona-send-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		args := append([]string{"--json"}, baseSpawnArgs(session)...)
		args = append(args, "--persona=reviewer")
		result := runCLI(t, dir, failingEnv("reviewer.md"), args...)
		assertJSONFailure(t, result, session, "PROMPT_SEND_FAILED", "sending persona/profile reviewer launch prompt")
		assertSessionExists(t, session)
	})

	t.Run("configured_default_prompt_resolution_failure", func(t *testing.T) {
		missingPrompt := filepath.Join(root, "missing-default-prompt.md")
		writeConfig(t, fmt.Sprintf("[prompts]\ncc_default_file = %q\n", missingPrompt))
		session := fmt.Sprintf("ntm-e2e-default-resolve-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		args := append([]string{"--json"}, baseSpawnArgs(session)...)
		result := runCLI(t, dir, nil, args...)
		assertJSONFailure(t, result, session, "INTERNAL_ERROR", "default prompt resolution")
		assertSessionExists(t, session)
	})

	t.Run("configured_default_prompt_human_resolution_failure", func(t *testing.T) {
		missingPrompt := filepath.Join(root, "missing-human-default-prompt.md")
		writeConfig(t, fmt.Sprintf("[prompts]\ncc_default_file = %q\n", missingPrompt))
		session := fmt.Sprintf("ntm-e2e-default-human-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		result := runCLI(t, dir, nil, baseSpawnArgs(session)...)
		if result.exitCode != 1 || !strings.Contains(string(result.stderr), "default prompt resolution") {
			t.Fatalf("human default prompt failure exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if bytes.Contains(result.stdout, []byte("Session ready")) || json.Valid(result.stdout) {
			t.Fatalf("human default prompt failure printed false success/JSON: %s", result.stdout)
		}
		assertSessionExists(t, session)
	})

	t.Run("configured_default_prompt_send_failure", func(t *testing.T) {
		marker := "DEFAULT_PROMPT_SEND_FAILURE"
		writeConfig(t, fmt.Sprintf("[prompts]\ncc_default = %q\n", marker))
		session := fmt.Sprintf("ntm-e2e-default-send-%d", time.Now().UnixNano())
		dir := createProject(t, session)
		args := append([]string{"--json"}, baseSpawnArgs(session)...)
		result := runCLI(t, dir, failingEnv(marker), args...)
		assertJSONFailure(t, result, session, "PROMPT_SEND_FAILED", "spawn prompt setup failed")
		assertSessionExists(t, session)
	})
}

// TestE2ESpawnResumeGlobalJSONSingleDocument proves a globally enabled JSON
// resume owns the complete spawn response. The nested spawn lifecycle must stay
// silent, and a handoff dispatch failure must remain one truthful nonzero JSON
// document with the per-pane outcome attached.
func TestE2ESpawnResumeGlobalJSONSingleDocument(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	projectsBase := filepath.Join(root, "projects")
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{projectsBase, homeDir, configDir, fakeBin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create resume spawn fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated zsh configuration: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "ntm"), 0o700); err != nil {
		t.Fatalf("create resume spawn config directory: %v", err)
	}
	configBody := "[agent_mail]\nenabled = false\n\n[recovery]\nenabled = false\nauto_inject_on_spawn = false\n\n[spawn_pacing]\nenabled = false\n"
	if err := os.WriteFile(filepath.Join(configDir, "ntm", "config.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatalf("write resume spawn config: %v", err)
	}
	writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))

	baseEnv := spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            projectsBase,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_RECOVERY_ENABLED":         "false",
		"NTM_RECOVERY_AUTO_INJECT":     "false",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, tmuxPath, baseEnv) })

	type resumeSpawnOutput struct {
		Success   bool   `json:"success"`
		Action    string `json:"action"`
		ErrorCode string `json:"error_code,omitempty"`
		Error     string `json:"error,omitempty"`
		SpawnInfo *struct {
			Session     string   `json:"session"`
			PaneCount   int      `json:"pane_count"`
			PanesFailed int      `json:"panes_failed"`
			PaneIDs     []string `json:"pane_ids,omitempty"`
		} `json:"spawn_info,omitempty"`
	}

	runResume := func(t *testing.T, session string, extraEnv map[string]string) (resumeSpawnOutput, int) {
		t.Helper()
		projectDir := filepath.Join(projectsBase, session)
		if err := os.MkdirAll(projectDir, 0o700); err != nil {
			t.Fatalf("create resume project directory: %v", err)
		}
		h := handoff.New(session)
		h.Goal = "Resume through one global JSON document"
		h.Now = "Deliver this handoff to the spawned agent"
		h.Status = handoff.StatusComplete
		h.Outcome = handoff.OutcomeSucceeded
		handoffPath, err := handoff.NewWriter(projectDir).Write(h, "resume-global-json")
		if err != nil {
			t.Fatalf("write resume handoff: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath,
			"--json", "resume", session, "--from="+handoffPath, "--spawn", "--cc=1",
		)
		cmd.Dir = projectDir
		cmd.Env = spawnAssignmentMergeEnv(baseEnv, extraEnv)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("global-JSON resume spawn timed out: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run global-JSON resume spawn: %v", runErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if strings.TrimSpace(stderr.String()) != "" {
			t.Fatalf("global-JSON resume spawn stderr=%q", stderr.String())
		}

		decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		var output resumeSpawnOutput
		if err := decoder.Decode(&output); err != nil {
			t.Fatalf("decode global-JSON resume spawn: %v raw=%s", err, stdout.String())
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			t.Fatalf("global-JSON resume spawn emitted nested/trailing output: err=%v trailing=%v raw=%s", err, trailing, stdout.String())
		}
		return output, exitCode
	}

	t.Run("success", func(t *testing.T) {
		session := fmt.Sprintf("ntm-e2e-resume-json-ok-%d-%d", os.Getpid(), time.Now().UnixNano())
		output, exitCode := runResume(t, session, nil)
		if exitCode != 0 || !output.Success || output.Action != "spawn" || output.ErrorCode != "" || output.Error != "" {
			t.Fatalf("successful global-JSON resume exit=%d output=%+v", exitCode, output)
		}
		if output.SpawnInfo == nil || output.SpawnInfo.Session != session || output.SpawnInfo.PaneCount != 1 || output.SpawnInfo.PanesFailed != 0 || len(output.SpawnInfo.PaneIDs) != 1 {
			t.Fatalf("successful global-JSON resume spawn info=%+v", output.SpawnInfo)
		}
	})

	t.Run("dispatch_failure", func(t *testing.T) {
		wrapperPath := filepath.Join(fakeBin, "tmux-fail-paste")
		wrapper := `#!/bin/sh
if [ "$1" = "paste-buffer" ]; then
    printf 'injected handoff paste failure\n' >&2
    exit 91
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
			t.Fatalf("write failing tmux wrapper: %v", err)
		}
		session := fmt.Sprintf("ntm-e2e-resume-json-fail-%d-%d", os.Getpid(), time.Now().UnixNano())
		output, exitCode := runResume(t, session, map[string]string{
			"NTM_TMUX_BINARY":   wrapperPath,
			"NTM_E2E_REAL_TMUX": tmuxPath,
		})
		if exitCode != 1 || output.Success || output.Action != "spawn" || output.ErrorCode != "RESUME_FAILED" || output.Error == "" {
			t.Fatalf("failed global-JSON resume exit=%d output=%+v", exitCode, output)
		}
		if output.SpawnInfo == nil || output.SpawnInfo.Session != session || output.SpawnInfo.PaneCount != 0 || output.SpawnInfo.PanesFailed != 1 || len(output.SpawnInfo.PaneIDs) != 0 {
			t.Fatalf("failed global-JSON resume spawn info=%+v", output.SpawnInfo)
		}
	})

	t.Run("sigint", func(t *testing.T) {
		session := fmt.Sprintf("ntm-e2e-resume-json-cancel-%d-%d", os.Getpid(), time.Now().UnixNano())
		projectDir := filepath.Join(projectsBase, session)
		if err := os.MkdirAll(projectDir, 0o700); err != nil {
			t.Fatalf("create canceled resume spawn project: %v", err)
		}
		h := handoff.New(session)
		h.Goal = fmt.Sprintf("NTM_RESUME_SPAWN_CANCEL_%d", time.Now().UnixNano())
		h.Now = "Cancel after staging the resume handoff"
		h.Status = handoff.StatusComplete
		h.Outcome = handoff.OutcomeSucceeded
		handoffPath, err := handoff.NewWriter(projectDir).Write(h, "resume-spawn-cancel")
		if err != nil {
			t.Fatalf("write canceled resume spawn handoff: %v", err)
		}

		stateRoot := filepath.Join(root, fmt.Sprintf("resume-spawn-cancel-%d", time.Now().UnixNano()))
		if err := os.MkdirAll(stateRoot, 0o700); err != nil {
			t.Fatalf("create canceled resume spawn state: %v", err)
		}
		stagedPath := filepath.Join(stateRoot, "staged")
		enterLog := filepath.Join(stateRoot, "enter.log")
		commandLog := filepath.Join(stateRoot, "commands.log")
		wrapperPath := filepath.Join(fakeBin, fmt.Sprintf("tmux-resume-spawn-cancel-%d", time.Now().UnixNano()))
		wrapper := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_RESUME_SPAWN_COMMANDS"
command_name=${1:-}
target=
enter=0
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then target=$argument; fi
    if [ "$argument" = "Enter" ]; then enter=1; fi
    previous=$argument
done
if [ "$command_name" = "paste-buffer" ]; then
    "$NTM_E2E_REAL_TMUX" "$@"
    status=$?
    printf '%s\n' "$target" > "$NTM_E2E_RESUME_SPAWN_STAGED"
    exit "$status"
fi
if [ "$command_name" = "send-keys" ] && [ "$enter" -eq 1 ]; then
    printf '%s\n' "$target" >> "$NTM_E2E_RESUME_SPAWN_ENTER_LOG"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write canceled resume spawn tmux wrapper: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, ntmPath,
			"--json", "resume", session, "--from="+handoffPath, "--spawn", "--cc=1",
		)
		cmd.Dir = projectDir
		cmd.Env = spawnAssignmentMergeEnv(baseEnv, map[string]string{
			"NTM_TMUX_BINARY":                wrapperPath,
			"NTM_E2E_REAL_TMUX":              tmuxPath,
			"NTM_E2E_RESUME_SPAWN_STAGED":    stagedPath,
			"NTM_E2E_RESUME_SPAWN_ENTER_LOG": enterLog,
			"NTM_E2E_RESUME_SPAWN_COMMANDS":  commandLog,
		})
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		process := startOwnedSpawnProcess(t, ctx, cmd, "canceled resume spawn")

		waitForResumeInjectStage(t, process, stagedPath, &stdout, &stderr)
		enterBefore, err := os.ReadFile(enterLog)
		if err != nil {
			t.Fatalf("read pre-cancel resume spawn Enter log: %v", err)
		}
		if err := process.signal(os.Interrupt); err != nil {
			t.Fatalf("interrupt staged resume spawn: %v", err)
		}
		waitErr := waitForResumeInjectExit(t, process, &stdout, &stderr)
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("canceled resume spawn returned no process status: %v", waitErr)
		}
		result := spawnSignalProcessResult{
			stdout:   stdout.Bytes(),
			stderr:   stderr.Bytes(),
			exitCode: exitErr.ExitCode(),
		}
		assertSpawnSignalJSONFailure(t, result, "TIMEOUT")
		var output resumeSpawnOutput
		if err := json.Unmarshal(result.stdout, &output); err != nil {
			t.Fatalf("decode canceled resume spawn JSON: %v raw=%s", err, result.stdout)
		}
		if output.Success || output.Action != "spawn" || output.ErrorCode != "TIMEOUT" || output.Error == "" {
			t.Fatalf("canceled resume spawn output=%+v", output)
		}

		commandsBefore, err := os.ReadFile(commandLog)
		if err != nil {
			t.Fatalf("read returned resume spawn command log: %v", err)
		}
		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		enterAfter, err := os.ReadFile(enterLog)
		if err != nil {
			t.Fatalf("read post-cancel resume spawn Enter log: %v", err)
		}
		if !bytes.Equal(enterAfter, enterBefore) {
			t.Fatalf("resume spawn submitted Enter after cancellation: before=%q after=%q", enterBefore, enterAfter)
		}
		commandsAfter, err := os.ReadFile(commandLog)
		if err != nil {
			t.Fatalf("read post-cancel resume spawn command log: %v", err)
		}
		if !bytes.Equal(commandsAfter, commandsBefore) {
			t.Fatalf("resume spawn issued tmux commands after cancellation: before=%q after=%q", commandsBefore, commandsAfter)
		}
	})
}

type resumeInjectProcessOutput struct {
	Success   bool   `json:"success"`
	Action    string `json:"action"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
	Inject    *struct {
		Session     string `json:"session"`
		PanesSent   int    `json:"panes_sent"`
		PanesFailed int    `json:"panes_failed"`
	} `json:"inject_info,omitempty"`
}

type resumeInjectE2EFixture struct {
	canonical  *canonicalPaneFixture
	projectDir string
	handoff    string
	marker     string
	user       string
	agents     []string
}

// TestE2EResumeInjectUnifiedDispatch crosses the built-process and real-tmux
// boundary for the resume injection path. It proves the shared dispatcher
// excludes the user pane, keeps going after one target fails, reports complete
// failure truthfully, and joins a canceled double-Enter delivery before the
// terminal JSON document is emitted.
func TestE2EResumeInjectUnifiedDispatch(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	t.Run("success", func(t *testing.T) {
		fixture := newResumeInjectE2EFixture(t)
		result := fixture.run(t, nil)
		var output resumeInjectProcessOutput
		decodeCLIJSONSuccess(t, result, &output)
		fixture.assertOutput(t, output, 3, 0, "")
		fixture.assertMarkerTargets(t, fixture.agents)
	})

	t.Run("partial", func(t *testing.T) {
		fixture := newResumeInjectE2EFixture(t)
		failedAddress := fixture.agents[1]
		failedPaneID := fixture.canonical.panes[failedAddress].ID
		wrapper := fixture.writeFailureWrapper(t)
		result := fixture.run(t, map[string]string{
			"NTM_TMUX_BINARY":          wrapper,
			"NTM_E2E_REAL_TMUX":        fixture.canonical.tmuxPath,
			"NTM_E2E_RESUME_FAIL_PANE": failedPaneID,
		})
		var output resumeInjectProcessOutput
		decodeCLIJSONFailure(t, result, &output)
		fixture.assertOutput(t, output, 2, 1, "RESUME_FAILED")
		fixture.assertMarkerTargets(t, []string{fixture.agents[0], fixture.agents[2]})
	})

	t.Run("all_fail", func(t *testing.T) {
		fixture := newResumeInjectE2EFixture(t)
		wrapper := fixture.writeFailureWrapper(t)
		result := fixture.run(t, map[string]string{
			"NTM_TMUX_BINARY":          wrapper,
			"NTM_E2E_REAL_TMUX":        fixture.canonical.tmuxPath,
			"NTM_E2E_RESUME_FAIL_ALL":  "1",
			"NTM_E2E_RESUME_FAIL_PANE": "",
		})
		var output resumeInjectProcessOutput
		decodeCLIJSONFailure(t, result, &output)
		fixture.assertOutput(t, output, 0, 3, "RESUME_FAILED")
		fixture.assertMarkerTargets(t, nil)
	})

	t.Run("sigint", func(t *testing.T) {
		fixture := newResumeInjectE2EFixture(t)
		wrapper, stagedPath, enterLog, commandLog := fixture.writeCancellationWrapper(t)
		ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, fixture.canonical.ntmPath,
			"--json", "resume", fixture.canonical.session,
			"--from="+fixture.handoff, "--inject",
		)
		cmd.Dir = fixture.projectDir
		cmd.Env = mergeProcessEnv(fixture.canonical.env, map[string]string{
			"NTM_TMUX_BINARY":          wrapper,
			"NTM_E2E_REAL_TMUX":        fixture.canonical.tmuxPath,
			"NTM_E2E_RESUME_STAGED":    stagedPath,
			"NTM_E2E_RESUME_ENTER_LOG": enterLog,
			"NTM_E2E_RESUME_COMMANDS":  commandLog,
		})
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		process := startOwnedSpawnProcess(t, ctx, cmd, "resume injection cancellation process")

		waitForResumeInjectStage(t, process, stagedPath, &stdout, &stderr)
		if err := process.signal(os.Interrupt); err != nil {
			t.Fatalf("interrupt staged resume injection: %v", err)
		}
		waitErr := waitForResumeInjectExit(t, process, &stdout, &stderr)
		exitErr := new(exec.ExitError)
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("canceled resume injection returned no process status: %v", waitErr)
		}
		processResult := spawnSignalProcessResult{
			stdout:   stdout.Bytes(),
			stderr:   stderr.Bytes(),
			exitCode: exitErr.ExitCode(),
		}
		assertSpawnSignalJSONFailure(t, processResult, "TIMEOUT")
		var output resumeInjectProcessOutput
		if err := json.Unmarshal(processResult.stdout, &output); err != nil {
			t.Fatalf("decode canceled resume injection JSON: %v raw=%s", err, processResult.stdout)
		}
		if output.Success || output.Action != "inject" || output.ErrorCode != "TIMEOUT" || output.Error == "" {
			t.Fatalf("canceled resume injection output=%+v", output)
		}

		commandsBefore, err := os.ReadFile(commandLog)
		if err != nil {
			t.Fatalf("read canceled resume tmux command log: %v", err)
		}
		stagedTarget, err := os.ReadFile(stagedPath)
		if err != nil {
			t.Fatalf("read canceled resume staged target: %v", err)
		}
		if strings.TrimSpace(string(stagedTarget)) != fixture.canonical.panes[fixture.agents[0]].ID {
			t.Fatalf("resume staged target=%q, want first agent %s", stagedTarget, fixture.canonical.panes[fixture.agents[0]].ID)
		}

		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		enterData, err := os.ReadFile(enterLog)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read canceled resume Enter log: %v", err)
		}
		if strings.TrimSpace(string(enterData)) != "" {
			t.Fatalf("resume injection submitted Enter after cancellation: %q", enterData)
		}
		commandsAfter, err := os.ReadFile(commandLog)
		if err != nil {
			t.Fatalf("reread canceled resume tmux command log: %v", err)
		}
		if !bytes.Equal(commandsAfter, commandsBefore) {
			t.Fatalf("resume injection issued tmux commands after cancellation: before=%q after=%q", commandsBefore, commandsAfter)
		}
		for _, address := range fixture.agents[1:] {
			if strings.Contains(fixture.canonical.capturePane(t, address), fixture.marker) {
				t.Fatalf("canceled resume injection staged marker in pending pane %s", address)
			}
		}
		if strings.Contains(fixture.canonical.capturePane(t, fixture.user), fixture.marker) {
			t.Fatalf("canceled resume injection leaked marker to user pane %s", fixture.user)
		}
	})
}

func newResumeInjectE2EFixture(t *testing.T) *resumeInjectE2EFixture {
	t.Helper()
	canonical := newCanonicalPaneFixture(t)
	userAddress := "0.0"
	userPane := canonical.panes[userAddress]
	canonical.mustTMUX(t, "select-pane", "-t", userPane.ID, "-T", canonical.session)
	userPane.Title = canonical.session
	userPane.Type = tmux.AgentUser
	canonical.panes[userAddress] = userPane

	marker := fmt.Sprintf("NTM_RESUME_INJECT_%d", time.Now().UnixNano())
	projectDir := filepath.Join(canonical.runtimeRoot, "resume-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("create resume injection project: %v", err)
	}
	h := handoff.New(canonical.session)
	h.Goal = marker
	h.Now = "Continue the resume injection end-to-end proof"
	h.Status = handoff.StatusComplete
	h.Outcome = handoff.OutcomeSucceeded
	h.Next = []string{"Verify unified delivery receipts"}
	handoffPath, err := handoff.NewWriter(projectDir).Write(h, "resume-inject-e2e")
	if err != nil {
		t.Fatalf("write resume injection handoff: %v", err)
	}
	return &resumeInjectE2EFixture{
		canonical:  canonical,
		projectDir: projectDir,
		handoff:    handoffPath,
		marker:     marker,
		user:       userAddress,
		agents:     []string{"0.1", "1.0", "1.1"},
	}
}

func (f *resumeInjectE2EFixture) run(t *testing.T, extraEnv map[string]string) robotProcessResult {
	t.Helper()
	return f.canonical.runNTMInDir(t, f.projectDir, extraEnv,
		"--json", "resume", f.canonical.session, "--from="+f.handoff, "--inject",
	)
}

func (f *resumeInjectE2EFixture) assertOutput(t *testing.T, output resumeInjectProcessOutput, sent, failed int, errorCode string) {
	t.Helper()
	if output.Success != (errorCode == "") || output.Action != "inject" || output.ErrorCode != errorCode {
		t.Fatalf("resume injection terminal output=%+v", output)
	}
	if errorCode == "" && output.Error != "" {
		t.Fatalf("successful resume injection reported error=%q", output.Error)
	}
	if errorCode != "" && output.Error == "" {
		t.Fatalf("failed resume injection omitted error: %+v", output)
	}
	if output.Inject == nil || output.Inject.Session != f.canonical.session || output.Inject.PanesSent != sent || output.Inject.PanesFailed != failed {
		t.Fatalf("resume injection receipt=%+v, want session=%s sent=%d failed=%d", output.Inject, f.canonical.session, sent, failed)
	}
}

func (f *resumeInjectE2EFixture) assertMarkerTargets(t *testing.T, want []string) {
	t.Helper()
	wantSet := make(map[string]struct{}, len(want))
	for _, address := range want {
		wantSet[address] = struct{}{}
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		ready := true
		for _, address := range want {
			if !strings.Contains(f.canonical.capturePane(t, address), f.marker) {
				ready = false
				break
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resume marker %q did not reach expected panes %v", f.marker, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for address := range f.canonical.panes {
		_, expected := wantSet[address]
		contains := strings.Contains(f.canonical.capturePane(t, address), f.marker)
		if contains != expected {
			t.Errorf("resume marker %q presence in %s=%t, want %t", f.marker, address, contains, expected)
		}
	}
}

func (f *resumeInjectE2EFixture) writeFailureWrapper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(f.canonical.runtimeRoot, "bin", fmt.Sprintf("tmux-resume-fail-%d", time.Now().UnixNano()))
	script := `#!/bin/sh
set -eu
command_name=${1:-}
target=
literal=0
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then target=$argument; fi
    if [ "$argument" = "-l" ]; then literal=1; fi
    previous=$argument
done
if { [ "$command_name" = "paste-buffer" ] || { [ "$command_name" = "send-keys" ] && [ "$literal" -eq 1 ]; }; } &&
   { [ "${NTM_E2E_RESUME_FAIL_ALL:-0}" = "1" ] || [ "$target" = "${NTM_E2E_RESUME_FAIL_PANE:-}" ]; }; then
    printf 'injected resume delivery failure for %s\n' "$target" >&2
    exit 91
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write resume injection failure wrapper: %v", err)
	}
	return path
}

func (f *resumeInjectE2EFixture) writeCancellationWrapper(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := filepath.Join(f.canonical.runtimeRoot, fmt.Sprintf("resume-cancel-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create resume cancellation state: %v", err)
	}
	path := filepath.Join(f.canonical.runtimeRoot, "bin", fmt.Sprintf("tmux-resume-cancel-%d", time.Now().UnixNano()))
	stagedPath := filepath.Join(root, "staged")
	enterLog := filepath.Join(root, "enter.log")
	commandLog := filepath.Join(root, "commands.log")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_RESUME_COMMANDS"
command_name=${1:-}
target=
literal=0
enter=0
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then target=$argument; fi
    if [ "$argument" = "-l" ]; then literal=1; fi
    if [ "$argument" = "Enter" ]; then enter=1; fi
    previous=$argument
done
if [ "$command_name" = "paste-buffer" ] || { [ "$command_name" = "send-keys" ] && [ "$literal" -eq 1 ]; }; then
    "$NTM_E2E_REAL_TMUX" "$@"
    status=$?
    printf '%s\n' "$target" > "$NTM_E2E_RESUME_STAGED"
    exit "$status"
fi
if [ "$command_name" = "send-keys" ] && [ "$enter" -eq 1 ]; then
    printf '%s\n' "$target" >> "$NTM_E2E_RESUME_ENTER_LOG"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write resume injection cancellation wrapper: %v", err)
	}
	return path, stagedPath, enterLog, commandLog
}

func waitForResumeInjectStage(t *testing.T, process *ownedSpawnProcess, stagedPath string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(stagedPath); err == nil {
			return
		}
		if process.poll() {
			t.Fatalf("resume injection exited before staging: command_err=%v close_err=%v stdout=%s stderr=%s",
				process.commandErr, process.closeErr, stdout.String(), stderr.String())
		}
		if time.Now().After(deadline) {
			_ = process.signal(syscall.SIGQUIT)
			abortErr := process.abort(defaultTmuxSetupTimeout)
			if !process.joined {
				t.Fatalf("resume injection did not stage or join after timeout: abort_err=%v close_err=%v; live output omitted",
					abortErr, process.closeErr)
			}
			t.Fatalf("resume injection did not stage before timeout: abort_err=%v close_err=%v stdout=%s stderr=%s",
				abortErr, process.closeErr, stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForResumeInjectExit(t *testing.T, process *ownedSpawnProcess, stdout, stderr *bytes.Buffer) error {
	t.Helper()
	if !process.wait(30 * time.Second) {
		_ = process.signal(syscall.SIGQUIT)
		abortErr := process.abort(defaultTmuxSetupTimeout)
		if !process.joined {
			t.Fatalf("resume injection did not join after cancellation: abort_err=%v close_err=%v; live output omitted",
				abortErr, process.closeErr)
		}
		t.Fatalf("resume injection did not join after cancellation: abort_err=%v close_err=%v stdout=%s stderr=%s",
			abortErr, process.closeErr, stdout.String(), stderr.String())
		return nil
	}
	if process.closeErr != nil {
		t.Fatalf("close owned process group for resume injection: %v", process.closeErr)
	}
	return process.commandErr
}

// TestE2ESpawnPromptInterruptCancelsPendingDispatch proves an interrupted
// built spawn process cancels assignment readiness before init dispatch. The
// fake agent becomes dispatchable only after the process exits; any continued
// post-interrupt init path would then deliver the marker.
func TestE2ESpawnPromptInterruptCancelsPendingDispatch(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	session := fmt.Sprintf("ntm-e2e-spawn-interrupt-%d-%d", os.Getpid(), time.Now().UnixNano())
	projectsBase := filepath.Join(root, "projects")
	projectDir := filepath.Join(projectsBase, session)
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	readyFile := filepath.Join(root, "agent-ready")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{projectDir, homeDir, configDir, fakeBin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create interrupted spawn fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated zsh configuration: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "ntm"), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "ntm", "config.toml"), []byte("[agent_mail]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("write interrupted spawn config: %v", err)
	}
	writeSpawnGatedFakeClaude(t, filepath.Join(fakeBin, "claude"), readyFile)
	writeSpawnEmptyBV(t, filepath.Join(fakeBin, "bv"))

	env := spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            projectsBase,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, tmuxPath, env) })

	marker := fmt.Sprintf("SPAWN_INTERRUPT_MUST_NOT_DELIVER_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ntmPath,
		"--json", "spawn", session,
		"--cc=1", "--no-user", "--no-hooks", "--no-cass-context", "--no-recovery",
		"--assign", "--init-prompt="+marker, "--ready-timeout=30s", "--assign-timeout=5s",
	)
	cmd.Dir = projectDir
	cmd.Env = append([]string(nil), env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process := startOwnedSpawnProcess(t, ctx, cmd, "interrupted spawn")

	var beforeInterrupt []byte
	readyDeadline := time.Now().Add(defaultTmuxSetupTimeout)
	for time.Now().Before(readyDeadline) {
		captureCtx, captureCancel := context.WithTimeout(context.Background(), time.Second)
		captureCmd := exec.CommandContext(captureCtx, tmuxPath, "capture-pane", "-p", "-t", session, "-S", "-200")
		captureCmd.Env = append([]string(nil), env...)
		captured, captureErr := captureCmd.CombinedOutput()
		captureCancel()
		if captureErr == nil && bytes.Contains(captured, []byte("WAITING_FOR_E2E_READY")) {
			beforeInterrupt = captured
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Contains(beforeInterrupt, []byte("WAITING_FOR_E2E_READY")) {
		signalErr := process.signal(os.Interrupt)
		var abortErr error
		if !process.wait(defaultTmuxSetupTimeout) {
			abortErr = process.abort(defaultTmuxSetupTimeout)
		}
		if !process.joined {
			t.Fatalf("fake agent did not enter its readiness gate or join: signal_err=%v abort_err=%v close_err=%v; live output omitted",
				signalErr, abortErr, process.closeErr)
		}
		t.Fatalf("fake agent did not enter its readiness gate: signal_err=%v abort_err=%v command_err=%v close_err=%v stdout=%s stderr=%s",
			signalErr, abortErr, process.commandErr, process.closeErr, stdout.String(), stderr.String())
	}

	if err := process.signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt spawn process: %v", err)
	}
	if !process.wait(45 * time.Second) {
		_ = process.signal(syscall.SIGQUIT)
		abortErr := process.abort(defaultTmuxSetupTimeout)
		if !process.joined {
			t.Fatalf("interrupted spawn did not cancel assignment readiness or join: abort_err=%v close_err=%v; live output omitted",
				abortErr, process.closeErr)
		}
		t.Fatalf("interrupted spawn did not cancel assignment readiness before returning: abort_err=%v close_err=%v stdout=%s stderr=%s",
			abortErr, process.closeErr, stdout.String(), stderr.String())
	}
	if process.closeErr != nil {
		t.Fatalf("close owned process group for interrupted spawn: %v", process.closeErr)
	}
	if process.commandErr == nil {
		t.Fatal("interrupted spawn exited successfully, want a cancellation error")
	}

	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("release fake agent readiness gate: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	captureCtx, captureCancel := context.WithTimeout(context.Background(), 5*time.Second)
	captureCmd := exec.CommandContext(captureCtx, tmuxPath, "capture-pane", "-p", "-t", session, "-S", "-2000")
	captureCmd.Env = append([]string(nil), env...)
	afterInterrupt, captureErr := captureCmd.CombinedOutput()
	captureCancel()
	if captureErr != nil {
		t.Fatalf("capture interrupted spawn pane: %v output=%s", captureErr, afterInterrupt)
	}
	if bytes.Contains(afterInterrupt, []byte(marker)) {
		t.Fatalf("prompt marker was delivered after interrupted spawn returned: pane=%q", afterInterrupt)
	}
}

// TestE2ESpawnInterruptCancelsPrelaunchWaits exercises the two longest waits
// before agent process actuation. The fake executables leave an on-disk marker
// as their first instruction, so marker absence proves cancellation returned
// before tmux could launch either agent.
func TestE2ESpawnInterruptCancelsPrelaunchWaits(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	fixtureRoot := t.TempDir()

	t.Run("configured pane initialization delay", func(t *testing.T) {
		runSpawnPrelaunchCancellationE2E(t, ntmPath, filepath.Join(fixtureRoot, "i"), spawnPrelaunchCancellationCase{
			name:        "pane-init",
			agentFlag:   "--cc=1",
			agentBinary: "claude",
			config:      "[agent_mail]\nenabled = false\n\n[tmux]\npane_init_delay_ms = 30000\n",
			waitForBlockedPhase: func(t *testing.T, _, _ string, _ []string, outputPath string) bool {
				t.Helper()
				return waitForSpawnE2ECondition(10*time.Second, func() bool {
					out, readErr := os.ReadFile(outputPath)
					return readErr == nil && bytes.Contains(out, []byte("Waiting for panes to initialize"))
				})
			},
		})
	})

	t.Run("Codex cooldown", func(t *testing.T) {
		runSpawnPrelaunchCancellationE2E(t, ntmPath, filepath.Join(fixtureRoot, "c"), spawnPrelaunchCancellationCase{
			name:        "codex-cooldown",
			agentFlag:   "--cod=1",
			agentBinary: "codex",
			config:      "[agent_mail]\nenabled = false\n",
			extraArgs:   []string{"--no-user"},
			extraEnv:    map[string]string{"NTM_DISABLE_CODEX_PREFLIGHT": "1"},
			prepare: func(t *testing.T, projectDir string) {
				t.Helper()
				tracker := ratelimit.NewRateLimitTracker(projectDir)
				tracker.RecordRateLimitWithCooldown("openai", "spawn", 30)
				if err := tracker.SaveToDir(projectDir); err != nil {
					t.Fatalf("persist Codex cooldown fixture: %v", err)
				}
			},
			waitForBlockedPhase: func(t *testing.T, _, _ string, _ []string, outputPath string) bool {
				t.Helper()
				return waitForSpawnE2ECondition(10*time.Second, func() bool {
					out, readErr := os.ReadFile(outputPath)
					return readErr == nil && bytes.Contains(out, []byte("Codex cooldown active; waiting"))
				})
			},
		})
	})
}

type spawnPrelaunchCancellationCase struct {
	name                string
	agentFlag           string
	agentBinary         string
	config              string
	extraArgs           []string
	extraEnv            map[string]string
	prepare             func(*testing.T, string)
	waitForBlockedPhase func(*testing.T, string, string, []string, string) bool
}

func runSpawnPrelaunchCancellationE2E(t *testing.T, ntmPath, root string, tc spawnPrelaunchCancellationCase) {
	t.Helper()
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	session := fmt.Sprintf("ntm-e2e-spawn-%s-%d-%d", tc.name, os.Getpid(), time.Now().UnixNano())
	projectsBase := filepath.Join(root, "projects")
	projectDir := filepath.Join(projectsBase, session)
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	fakeBin := filepath.Join(root, "bin")
	launchMarker := filepath.Join(root, "agent-launched")
	outputPath := filepath.Join(root, "spawn-output.log")
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, dir := range []string{projectDir, homeDir, configDir, fakeBin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create prelaunch fixture directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(homeDir, ".zshrc"), []byte("# isolated E2E shell\n"), 0o600); err != nil {
		t.Fatalf("create isolated zsh configuration: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "ntm"), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "ntm", "config.toml"), []byte(tc.config), 0o600); err != nil {
		t.Fatalf("write prelaunch config: %v", err)
	}
	writeSpawnLaunchMarkerAgent(t, filepath.Join(fakeBin, tc.agentBinary), launchMarker)
	if tc.prepare != nil {
		tc.prepare(t, projectDir)
	}

	overrides := map[string]string{
		"HOME":                         homeDir,
		"XDG_CONFIG_HOME":              configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  tmuxRoot,
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NTM_PROJECTS_BASE":            projectsBase,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	}
	for key, value := range tc.extraEnv {
		overrides[key] = value
	}
	env := spawnAssignmentIsolatedEnv(overrides)
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, tmuxPath, env) })

	logFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open prelaunch output: %v", err)
	}
	defer logFile.Close()

	args := []string{"spawn", session, tc.agentFlag, "--no-hooks", "--no-cass-context", "--no-recovery"}
	args = append(args, tc.extraArgs...)
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ntmPath, args...)
	cmd.Dir = projectDir
	cmd.Env = append([]string(nil), env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	process := startOwnedSpawnProcess(t, ctx, cmd, "prelaunch spawn")

	if !tc.waitForBlockedPhase(t, tmuxPath, session, env, outputPath) {
		signalErr := process.signal(os.Interrupt)
		var abortErr error
		if !process.wait(defaultTmuxSetupTimeout) {
			abortErr = process.abort(defaultTmuxSetupTimeout)
		}
		if !process.joined {
			t.Fatalf("spawn never entered expected blocked phase or joined: signal_err=%v abort_err=%v close_err=%v; live output omitted",
				signalErr, abortErr, process.closeErr)
		}
		_ = logFile.Sync()
		out, _ := os.ReadFile(outputPath)
		t.Fatalf("spawn never entered expected blocked phase: signal_err=%v abort_err=%v command_err=%v close_err=%v output=%s",
			signalErr, abortErr, process.commandErr, process.closeErr, out)
	}
	if err := process.signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt prelaunch spawn: %v", err)
	}
	if !process.wait(45 * time.Second) {
		_ = process.signal(syscall.SIGQUIT)
		abortErr := process.abort(defaultTmuxSetupTimeout)
		if !process.joined {
			t.Fatalf("prelaunch wait did not honor cancellation or join: abort_err=%v close_err=%v; live output omitted",
				abortErr, process.closeErr)
		}
		_ = logFile.Sync()
		out, _ := os.ReadFile(outputPath)
		t.Fatalf("prelaunch wait did not honor command cancellation: abort_err=%v close_err=%v output=%s",
			abortErr, process.closeErr, out)
	}
	if process.closeErr != nil {
		t.Fatalf("close owned process group for prelaunch spawn: %v", process.closeErr)
	}
	if process.commandErr == nil {
		t.Fatal("interrupted prelaunch spawn exited successfully")
	}
	if err := logFile.Sync(); err != nil {
		t.Fatalf("sync prelaunch output: %v", err)
	}
	if _, err := os.Stat(launchMarker); !errors.Is(err, os.ErrNotExist) {
		out, _ := os.ReadFile(outputPath)
		t.Fatalf("agent executable ran after cancellation: marker_error=%v output=%s", err, out)
	}
}

func waitForSpawnE2ECondition(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

type spawnSignalProcessResult struct {
	stdout       []byte
	stderr       []byte
	exitCode     int
	signalToJoin time.Duration
}

type ownedSpawnProcess struct {
	cmd        *exec.Cmd
	group      *testutil.ProcessGroupForTest
	done       chan error
	label      string
	joined     bool
	commandErr error
	closeErr   error
}

func startOwnedSpawnProcess(t *testing.T, ctx context.Context, cmd *exec.Cmd, label string) *ownedSpawnProcess {
	t.Helper()
	group, err := testutil.NewProcessGroupForTest(ctx, cmd)
	if err != nil {
		t.Fatalf("create owned process group for %s: %v", label, err)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		closeErr := group.Close()
		t.Fatalf("start %s: %v; close owned process group: %v", label, err, closeErr)
	}
	process := &ownedSpawnProcess{
		cmd:   cmd,
		group: group,
		done:  make(chan error, 1),
		label: label,
	}
	go func() { process.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !process.joined {
			if abortErr := process.abort(defaultTmuxSetupTimeout); abortErr != nil {
				t.Errorf("abort %s during cleanup: %v", process.label, abortErr)
			}
			if !process.joined {
				return
			}
		}
		if process.closeErr != nil {
			t.Errorf("close owned process group for %s: %v", process.label, process.closeErr)
		}
	})
	return process
}

func (p *ownedSpawnProcess) finish(commandErr error) {
	if p.joined {
		return
	}
	p.commandErr = commandErr
	p.closeErr = errors.Join(p.closeErr, p.group.Close())
	p.joined = true
}

func (p *ownedSpawnProcess) poll() bool {
	if p.joined {
		return true
	}
	select {
	case commandErr := <-p.done:
		p.finish(commandErr)
		return true
	default:
		return false
	}
}

func (p *ownedSpawnProcess) wait(timeout time.Duration) bool {
	if p.joined {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case commandErr := <-p.done:
		p.finish(commandErr)
		return true
	case <-timer.C:
		return false
	}
}

func (p *ownedSpawnProcess) abort(timeout time.Duration) error {
	if p.joined {
		return nil
	}
	signalErr := p.group.Signal(os.Kill)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	if p.wait(timeout) {
		return signalErr
	}
	p.closeErr = errors.Join(p.closeErr, p.group.Close())
	return errors.Join(signalErr, fmt.Errorf("%s did not join within %s", p.label, timeout))
}

func (p *ownedSpawnProcess) signal(signal os.Signal) error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(signal)
}

func installSpawnSignalBlockingCommand(t *testing.T, baseEnv []string, commandName, realPath, matchArg string) ([]string, string) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	startedDir := filepath.Join(root, "started")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create blocking command directory: %v", err)
	}
	if err := os.MkdirAll(startedDir, 0o700); err != nil {
		t.Fatalf("create blocking command marker directory: %v", err)
	}
	fifoPath := filepath.Join(root, "blocked.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("create blocking command FIFO: %v", err)
	}
	script := `#!/bin/sh
set -eu
if [ "$NTM_E2E_BLOCK_MATCH" = "*" ] || [ "${1:-}" = "$NTM_E2E_BLOCK_MATCH" ]; then
    : > "$NTM_E2E_BLOCK_STARTED/$$"
    exec 3< "$NTM_E2E_BLOCK_FIFO"
    exit 97
fi
exec "$NTM_E2E_BLOCK_REAL" "$@"
`
	commandPath := filepath.Join(binDir, commandName)
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write blocking %s wrapper: %v", commandName, err)
	}
	if realPath == "" {
		realPath = "/bin/false"
	}
	pathValue := atomicAssignmentEnvValue(baseEnv, "PATH")
	if pathValue == "" {
		pathValue = os.Getenv("PATH")
	}
	overrides := map[string]string{
		"PATH":                  binDir + string(os.PathListSeparator) + pathValue,
		"NTM_E2E_BLOCK_FIFO":    fifoPath,
		"NTM_E2E_BLOCK_MATCH":   matchArg,
		"NTM_E2E_BLOCK_REAL":    realPath,
		"NTM_E2E_BLOCK_STARTED": startedDir,
	}
	if commandName == "tmux" {
		overrides["NTM_TMUX_BINARY"] = commandPath
	}
	return atomicAssignmentMergeEnv(baseEnv, overrides), startedDir
}

// installSpawnSignalStagingTMUX records successful tmux send-keys calls and
// signals the test only after the literal prompt has been staged. The CLI is
// then interrupted while its context-aware double-Enter delay is active.
func installSpawnSignalStagingTMUX(t *testing.T, baseEnv []string, realPath string) ([]string, string, string) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	startedDir := filepath.Join(root, "started")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("create staging tmux wrapper directory: %v", err)
	}
	if err := os.MkdirAll(startedDir, 0o700); err != nil {
		t.Fatalf("create staging tmux marker directory: %v", err)
	}
	logPath := filepath.Join(root, "send-keys.log")
	script := `#!/bin/sh
set -eu
literal=0
if [ "${1:-}" = "send-keys" ] || [ "${1:-}" = "paste-buffer" ]; then
    for arg in "$@"; do
        if [ "$arg" = "-l" ]; then literal=1; fi
    done
    printf '%s\n' "$*" >> "$NTM_E2E_TMUX_SEND_LOG"
fi
"$NTM_E2E_TMUX_REAL" "$@"
if { [ "${1:-}" = "send-keys" ] && [ "$literal" -eq 1 ]; } || [ "${1:-}" = "paste-buffer" ]; then
    : > "$NTM_E2E_BLOCK_STARTED/$$"
fi
`
	commandPath := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(commandPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write staging tmux wrapper: %v", err)
	}
	pathValue := atomicAssignmentEnvValue(baseEnv, "PATH")
	if pathValue == "" {
		pathValue = os.Getenv("PATH")
	}
	return atomicAssignmentMergeEnv(baseEnv, map[string]string{
		"PATH":                  binDir + string(os.PathListSeparator) + pathValue,
		"NTM_TMUX_BINARY":       commandPath,
		"NTM_E2E_TMUX_REAL":     realPath,
		"NTM_E2E_TMUX_SEND_LOG": logPath,
		"NTM_E2E_BLOCK_STARTED": startedDir,
	}), startedDir, logPath
}

func assertSpawnTimelineExcludesMarker(t *testing.T, timelineDir, marker string) {
	t.Helper()
	err := filepath.WalkDir(timelineDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(marker)) {
			return fmt.Errorf("timeline %s contains canceled prompt marker", path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func runSpawnSignalCanceledCLI(t *testing.T, ntmPath, dir string, env []string, startedDir string, wantStarted int, sig syscall.Signal, args ...string) spawnSignalProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ntmPath, args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process := startOwnedSpawnProcess(t, ctx, cmd, fmt.Sprintf("signal cancellation command %q", args))
	waitForSpawnSignalStarts(t, process, startedDir, wantStarted, defaultTmuxSetupTimeout, args, &stdout, &stderr)
	signaledAt := time.Now()
	if err := process.signal(sig); err != nil {
		t.Fatalf("signal cancellation command %q with %s: %v", args, sig, err)
	}
	if !process.wait(45 * time.Second) {
		_ = process.signal(syscall.SIGQUIT)
		abortErr := process.abort(defaultTmuxSetupTimeout)
		if !process.joined {
			t.Fatalf("signal cancellation command did not join after %s: args=%q abort_err=%v close_err=%v; live output omitted",
				sig, args, abortErr, process.closeErr)
		}
		t.Fatalf("signal cancellation command did not join after %s: args=%q abort_err=%v close_err=%v stdout=%s stderr=%s",
			sig, args, abortErr, process.closeErr, stdout.String(), stderr.String())
	}
	if process.closeErr != nil {
		t.Fatalf("close owned process group for signal cancellation command %q: %v", args, process.closeErr)
	}
	signalToJoin := time.Since(signaledAt)
	waitErr := process.commandErr
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("wait for signal cancellation command %q: %v", args, waitErr)
		}
		exitCode = exitErr.ExitCode()
	}
	return spawnSignalProcessResult{
		stdout:       stdout.Bytes(),
		stderr:       stderr.Bytes(),
		exitCode:     exitCode,
		signalToJoin: signalToJoin,
	}
}

func waitForSpawnSignalStarts(t *testing.T, process *ownedSpawnProcess, startedDir string, want int, timeout time.Duration, args []string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		entries, err := os.ReadDir(startedDir)
		if err == nil && len(entries) >= want {
			return
		}
		if process.poll() {
			t.Fatalf("signal cancellation command exited before blocked workers started: args=%q markers=%d want=%d command_err=%v close_err=%v stdout=%s stderr=%s",
				args, len(entries), want, process.commandErr, process.closeErr, stdout.String(), stderr.String())
		}
		if time.Now().After(deadline) {
			_ = process.signal(syscall.SIGQUIT)
			abortErr := process.abort(defaultTmuxSetupTimeout)
			if !process.joined {
				t.Fatalf("signal cancellation command did not start %d blocked worker(s) or join: args=%q markers=%d read_err=%v abort_err=%v close_err=%v; live output omitted",
					want, args, len(entries), err, abortErr, process.closeErr)
			}
			t.Fatalf("signal cancellation command did not start %d blocked worker(s): args=%q markers=%d read_err=%v abort_err=%v close_err=%v stdout=%s stderr=%s",
				want, args, len(entries), err, abortErr, process.closeErr, stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertSpawnSignalJSONFailure(t *testing.T, result spawnSignalProcessResult, wantErrorCode string) map[string]any {
	t.Helper()
	if result.exitCode != 1 {
		t.Fatalf("canceled CLI exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	if trimmed := bytes.TrimSpace(result.stderr); len(trimmed) != 0 {
		t.Fatalf("canceled JSON CLI wrote stderr=%s", trimmed)
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode canceled CLI JSON: %v raw=%s", err, result.stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("canceled CLI emitted more than one JSON document: err=%v extra=%v raw=%s", err, extra, result.stdout)
	}
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("canceled CLI success=%v, want false: %s", document["success"], result.stdout)
	}
	if wantErrorCode != "" {
		gotErrorCode := document["error_code"]
		if gotErrorCode == nil {
			if structured, ok := document["error"].(map[string]any); ok {
				gotErrorCode = structured["code"]
			}
		}
		if gotErrorCode != wantErrorCode {
			t.Fatalf("canceled CLI error_code=%v, want %s: %s", gotErrorCode, wantErrorCode, result.stdout)
		}
	}
	return document
}

func assertSpawnSignalCodedJSONError(t *testing.T, result spawnSignalProcessResult, wantCode string) {
	t.Helper()
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("canceled coded CLI exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode canceled coded JSON: %v raw=%s", err, result.stdout)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("canceled coded CLI emitted multiple JSON documents: %v raw=%s", err, result.stdout)
	}
	if document["code"] != wantCode || strings.TrimSpace(fmt.Sprint(document["error"])) == "" {
		t.Fatalf("canceled coded CLI document=%v, want code=%s", document, wantCode)
	}
}

func assertSpawnStagedWithoutEnter(t *testing.T, logPath string) {
	t.Helper()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read staged tmux send log: %v", err)
	}
	logText := string(logData)
	staged := strings.Contains(logText, "send-keys") || strings.Contains(logText, "paste-buffer")
	if !staged || strings.Contains(logText, " Enter") {
		t.Fatalf("staged tmux calls=%q, want prompt staging and no Enter", logText)
	}
}

func assertAtomicSignalCancellationNoActuation(t *testing.T, fixture *atomicAssignmentCLIFixture, beadID string) {
	t.Helper()
	ledger, err := fixture.readLedger()
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read atomic cancellation ledger: %v", err)
	}
	record := ledger.Assignments[beadID]
	if record == nil {
		return
	}
	if record.ClaimState == "claimed" || record.ClaimedAt != nil || record.ReservationAttempts != 0 ||
		record.ReservationCompleted || record.DispatchAttempts != 0 || record.DispatchedAt != nil ||
		record.DispatchReceiptID != "" || record.PromptSent != "" {
		t.Fatalf("canceled atomic assignment crossed an actuation boundary: %+v", record)
	}
}

func assertSpawnSignalCancellationNoActuation(t *testing.T, fixture *spawnAssignmentCLIFixture) {
	t.Helper()
	path := filepath.Join(fixture.homeDir, ".ntm", "sessions", fixture.session, "assignments.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read spawn cancellation ledger: %v", err)
	}
	var ledger spawnAssignmentLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode spawn cancellation ledger: %v raw=%s", err, data)
	}
	record := ledger.Assignments[fixture.beadID]
	if record == nil {
		return
	}
	if record.ClaimState == "claimed" || record.ClaimedAt != nil || record.ReservationAttempts != 0 ||
		record.ReservationCompleted || record.DispatchAttempts != 0 || record.DispatchedAt != nil ||
		record.DispatchReceiptID != "" || record.PromptSent != "" {
		t.Fatalf("canceled spawn assignment crossed an actuation boundary: %+v", record)
	}
}

type spawnAssignmentCLIFixture struct {
	ntmPath        string
	tmuxPath       string
	brPath         string
	session        string
	projectDir     string
	homeDir        string
	configDir      string
	env            []string
	paneID         string
	paneIndex      int
	beadID         string
	beadTitle      string
	marker         string
	expectedPrompt string
	stub           *spawnAssignmentMCPStub
}

type spawnAssignmentPreflightFixture struct {
	ntmPath            string
	tmuxPath           string
	root               string
	projectDir         string
	outsideDir         string
	homeDir            string
	configDir          string
	fakeBin            string
	session            string
	toolLog            string
	launchMarker       string
	env                []string
	durabilityBaseline map[string]spawnAssignmentPathSnapshot
}

type spawnAssignmentPathSnapshot struct {
	exists bool
	mode   os.FileMode
	data   []byte
}

func newSpawnAssignmentPreflightFixture(t *testing.T) *spawnAssignmentPreflightFixture {
	t.Helper()
	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	root := t.TempDir()
	// macOS t.TempDir() lives under /var, a symlink to /private/var. The
	// product resolves and reports canonical paths (worktree retention
	// messages, Agent Mail project keys), so hold the resolved form or
	// substring/equality assertions against reported paths fail.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	fixture := &spawnAssignmentPreflightFixture{
		ntmPath:      ntmPath,
		tmuxPath:     tmuxPath,
		root:         root,
		projectDir:   filepath.Join(root, "target-project"),
		outsideDir:   filepath.Join(root, "outside-target"),
		homeDir:      filepath.Join(root, "home"),
		configDir:    filepath.Join(root, "config"),
		fakeBin:      filepath.Join(root, "bin"),
		session:      fmt.Sprintf("ntm-spawn-policy-%d-%d", os.Getpid(), time.Now().UnixNano()),
		toolLog:      filepath.Join(root, "tool-calls.log"),
		launchMarker: filepath.Join(root, "agent-launched"),
	}
	for _, dir := range []string{
		fixture.projectDir,
		fixture.outsideDir,
		fixture.homeDir,
		fixture.configDir,
		fixture.fakeBin,
		filepath.Join(root, "data"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create spawn preflight directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.homeDir, ".zshrc"), []byte("# isolated spawn preflight shell\n"), 0o600); err != nil {
		t.Fatalf("write isolated spawn preflight shell config: %v", err)
	}
	writeSpawnLaunchMarkerAgent(t, filepath.Join(fixture.fakeBin, "claude"), fixture.launchMarker)
	fixture.env = spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":                         fixture.homeDir,
		"XDG_CONFIG_HOME":              fixture.configDir,
		"XDG_DATA_HOME":                filepath.Join(root, "data"),
		"TMUX_TMPDIR":                  testutil.ShortTmuxTempDir(t),
		"PATH":                         fixture.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SHELL":                        "/bin/bash",
		"SPAWN_PREFLIGHT_TOOL_LOG":     fixture.toolLog,
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"NTM_TEST_MODE":                "1",
		"NTM_CONFIG":                   "",
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
	})
	fixture.snapshotAssignmentDurability(t)
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, fixture.tmuxPath, fixture.env) })
	return fixture
}

func newNormalSpawnAssignmentPreflightFixture(t *testing.T) *spawnAssignmentPreflightFixture {
	t.Helper()
	fixture := newSpawnAssignmentPreflightFixture(t)
	fixture.session = filepath.Base(fixture.projectDir)
	fixture.snapshotAssignmentDurability(t)
	return fixture
}

func (f *spawnAssignmentPreflightFixture) snapshotAssignmentDurability(t *testing.T) {
	t.Helper()
	f.durabilityBaseline = make(map[string]spawnAssignmentPathSnapshot)
	for _, path := range f.assignmentDurabilityPaths() {
		snapshot, err := readSpawnAssignmentPathSnapshot(path)
		if err != nil {
			t.Fatalf("snapshot initial spawn preflight durability %s: %v", path, err)
		}
		f.durabilityBaseline[path] = snapshot
	}
}

func (f *spawnAssignmentPreflightFixture) writeGlobalConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(f.configDir, "ntm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create spawn preflight global config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("[spawn_pacing]\nenabled = false\n"), 0o600); err != nil {
		t.Fatalf("write spawn preflight global config: %v", err)
	}
	return path
}

func (f *spawnAssignmentPreflightFixture) writeFailIfCalledTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"bv", "br"} {
		body := fmt.Sprintf(`#!/bin/sh
printf 'cwd=%%s\ttool=%s\targs=%%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
echo '%s must not be called before policy validation' >&2
exit 97
`, name, name)
		if err := os.WriteFile(filepath.Join(f.fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fail-if-called %s: %v", name, err)
		}
	}
}

func (f *spawnAssignmentPreflightFixture) writeUnverifiedPlanningTools(t *testing.T, unverified string) {
	t.Helper()
	triage := `{"generated_at":"2026-07-18T00:00:00Z","data_hash":"spawn-preflight","triage":{"recommendations":[{"id":"spawn-preflight-plan-item","title":"unverified planning item","status":"open","priority":1,"score":100,"action":"claim"}]}}`
	plan := `{"generated_at":"2026-07-18T00:00:00Z","plan":{"tracks":[{"track_id":"preflight","items":[{"id":"spawn-preflight-plan-item","title":"unverified planning item","status":"open","priority":1}]}]}}`
	bvBody := fmt.Sprintf(`#!/bin/sh
printf 'cwd=%%s\ttool=bv\targs=%%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
case "$1" in
  --robot-triage) printf '%%s\n' '%s' ;;
  --robot-plan) printf '%%s\n' '%s' ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, triage, plan)
	if unverified == "plan" {
		bvBody = fmt.Sprintf(`#!/bin/sh
printf 'cwd=%%s\ttool=bv\targs=%%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
case "$1" in
  --robot-triage) printf '%%s\n' '%s' ;;
  --robot-plan) echo 'synthetic plan failure' >&2; exit 73 ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, triage)
	} else if unverified != "labels" {
		t.Fatalf("unknown unverified planning mode %q", unverified)
	}
	if err := os.WriteFile(filepath.Join(f.fakeBin, "bv"), []byte(bvBody), 0o755); err != nil {
		t.Fatalf("write unverified-plan bv: %v", err)
	}
	brBody := `#!/bin/sh
printf 'cwd=%s\ttool=br\targs=%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
if [ "$1" != "--lock-timeout" ] || [ "$2" != "5000" ]; then
  echo "missing canonical br lock prefix: $*" >&2
  exit 64
fi
shift 2
case "$1" in
  ready|list) printf '[]\n' ;;
  *) echo "unexpected br args: $*" >&2; exit 64 ;;
esac
`
	if err := os.WriteFile(filepath.Join(f.fakeBin, "br"), []byte(brBody), 0o755); err != nil {
		t.Fatalf("write unverified-label br: %v", err)
	}
}

func (f *spawnAssignmentPreflightFixture) writeVerifiedEmptyPlanningTools(t *testing.T) {
	t.Helper()
	triage := `{"generated_at":"2026-07-18T00:00:00Z","data_hash":"normal-spawn-empty","triage":{"recommendations":[]}}`
	plan := `{"generated_at":"2026-07-18T00:00:00Z","plan":{"tracks":[]}}`
	insights := `{"Cycles":[]}`
	bvBody := fmt.Sprintf(`#!/bin/sh
printf 'cwd=%%s\ttool=bv\targs=%%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
case "$1" in
  --robot-triage) printf '%%s\n' '%s' ;;
  --robot-plan) printf '%%s\n' '%s' ;;
  --robot-insights) printf '%%s\n' '%s' ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, triage, plan, insights)
	if err := os.WriteFile(filepath.Join(f.fakeBin, "bv"), []byte(bvBody), 0o755); err != nil {
		t.Fatalf("write verified-empty bv: %v", err)
	}
	brBody := `#!/bin/sh
printf 'cwd=%s\ttool=br\targs=%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
echo 'empty verified plan must not require live-label lookup' >&2
exit 97
`
	if err := os.WriteFile(filepath.Join(f.fakeBin, "br"), []byte(brBody), 0o755); err != nil {
		t.Fatalf("write verified-empty br guard: %v", err)
	}
}

func (f *spawnAssignmentPreflightFixture) spawnArgs(selectedConfig string) []string {
	return []string{
		"--config=" + selectedConfig,
		"--robot-format=json",
		"--robot-spawn=" + f.session,
		"--spawn-cc=1",
		"--spawn-no-user",
		"--spawn-dir=" + f.projectDir,
		"--spawn-assign-work",
		"--strategy=top-n",
	}
}

func (f *spawnAssignmentPreflightFixture) normalSpawnArgs(selectedConfig string) []string {
	return []string{
		"--config=" + selectedConfig,
		"--json",
		"spawn",
		f.session,
		"--cc=1",
		"--no-user",
		"--no-hooks",
		"--no-cass-context",
		"--no-recovery",
		"--assign",
		"--ready-timeout=5s",
		"--assign-timeout=5s",
		"--init-prompt=SPAWN_PREFLIGHT_MUST_NOT_REACH_A_PANE",
	}
}

func (f *spawnAssignmentPreflightFixture) normalWorktreeSpawnArgs(selectedConfig string, claudeCount int) []string {
	return []string{
		"--config=" + selectedConfig,
		"--json",
		"spawn",
		f.session,
		fmt.Sprintf("--cc=%d", claudeCount),
		"--no-user",
		"--no-hooks",
		"--no-cass-context",
		"--no-recovery",
		"--worktrees",
	}
}

func (f *spawnAssignmentPreflightFixture) normalWorktreeSpawnArgsWithHooks(selectedConfig string, claudeCount int) []string {
	args := f.normalWorktreeSpawnArgs(selectedConfig, claudeCount)
	withHooks := make([]string, 0, len(args)-1)
	for _, arg := range args {
		if arg != "--no-hooks" {
			withHooks = append(withHooks, arg)
		}
	}
	return withHooks
}

func (f *spawnAssignmentPreflightFixture) normalWorktreePath(agentName string) string {
	return filepath.Join(f.projectDir, ".ntm", "worktrees", f.session, agentName)
}

func (f *spawnAssignmentPreflightFixture) writePreSpawnHook(t *testing.T, command string) {
	t.Helper()
	path := filepath.Join(f.configDir, "ntm", "hooks.toml")
	body := fmt.Sprintf("[[command_hooks]]\nevent = \"pre-spawn\"\nname = \"worktree-race\"\ncommand = %s\ntimeout = \"10s\"\n", strconv.Quote(command))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write pre-spawn hook: %v", err)
	}
}

func (f *spawnAssignmentPreflightFixture) writeGitWrapper(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.fakeBin, "git"), []byte(body), 0o700); err != nil {
		t.Fatalf("write worktree git wrapper: %v", err)
	}
}

func (f *spawnAssignmentPreflightFixture) initializeCommittedGitRepo(t *testing.T) {
	t.Helper()
	f.runGit(t, "init", "-b", "main")
	f.runGit(t, "config", "user.name", "NTM Worktree E2E")
	f.runGit(t, "config", "user.email", "ntm-worktree-e2e@example.invalid")
	f.runGit(t, "config", "commit.gpgsign", "false")
	f.runGit(t, "commit", "--allow-empty", "-m", "initial worktree fixture")
}

func (f *spawnAssignmentPreflightFixture) runGit(t *testing.T, args ...string) {
	t.Helper()
	f.runGitInDir(t, f.projectDir, args...)
}

func (f *spawnAssignmentPreflightFixture) runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree fixture git %q in %s: %v output=%s", args, dir, err, output)
	}
}

func (f *spawnAssignmentPreflightFixture) runNTM(t *testing.T, envOverrides map[string]string, args ...string) spawnAssignmentProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	cmd.Dir = f.outsideDir
	cmd.Env = spawnAssignmentMergeEnv(f.env, envOverrides)
	group, groupErr := testutil.NewProcessGroupForTest(ctx, cmd)
	if groupErr != nil {
		t.Fatalf("create owned process group for spawn assignment preflight %q: %v", args, groupErr)
	}
	cmd.Cancel = func() error {
		return group.Signal(os.Kill)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	commandErr := cmd.Run()
	if closeErr := group.Close(); closeErr != nil {
		t.Fatalf("close owned process group for spawn assignment preflight %q: %v", args, closeErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("spawn assignment preflight timed out after descendant cleanup: %q stdout=%s stderr=%s", args, stdout.String(), stderr.String())
	}
	exitCode := 0
	if commandErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(commandErr, &exitErr) {
			t.Fatalf("run spawn assignment preflight: %v", commandErr)
		}
		exitCode = exitErr.ExitCode()
	}
	t.Logf("[E2E-SPAWN-PREFLIGHT] exit=%d stdout=%s stderr=%s", exitCode,
		truncateString(stdout.String(), 1000), truncateString(stderr.String(), 1000))
	return spawnAssignmentProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func (f *spawnAssignmentPreflightFixture) mustBR(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "br", args...)
	cmd.Dir = f.projectDir
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("spawn preflight br timed out: %q", args)
	}
	if err != nil {
		t.Fatalf("spawn preflight br %q: %v output=%s", args, err, output)
	}
	return output
}

func (f *spawnAssignmentPreflightFixture) assertNoActuation(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "has-session", "-t", f.session)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if absenceErr := classifyTmuxSessionAbsence(ctx.Err(), output, err); absenceErr != nil {
		t.Fatalf("preflight no-actuation proof for tmux session %q failed: %v", f.session, absenceErr)
	}
	if _, err := os.Stat(f.launchMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight failure launched agent: marker error=%v", err)
	}
	f.assertNoAssignmentDurability(t)
}

func classifyTmuxSessionAbsence(ctxErr error, output []byte, commandErr error) error {
	if ctxErr != nil {
		return fmt.Errorf("tmux has-session did not complete: %w", ctxErr)
	}
	if commandErr == nil {
		return errors.New("tmux session still exists")
	}
	if isBenignTmuxCleanupError(output) {
		return nil
	}
	return fmt.Errorf("tmux has-session failed without canonical absence evidence: %w: %s", commandErr, strings.TrimSpace(string(output)))
}

func TestE2ESpawnAssignmentNoActuationTmuxClassification(t *testing.T) {
	tests := []struct {
		name       string
		ctxErr     error
		output     string
		commandErr error
		wantErr    bool
	}{
		{name: "canonical absent session", output: "can't find session: missing", commandErr: errors.New("exit status 1")},
		{name: "timeout with benign output", ctxErr: context.DeadlineExceeded, output: "no server running on /tmp/tmux/default", commandErr: context.DeadlineExceeded, wantErr: true},
		{name: "permission failure", output: "error connecting to /tmp/tmux/default (Permission denied)", commandErr: errors.New("exit status 1"), wantErr: true},
		{name: "corrupt socket failure", output: "error connecting to /tmp/tmux/default (protocol error)", commandErr: errors.New("exit status 1"), wantErr: true},
		{name: "session still exists", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := classifyTmuxSessionAbsence(test.ctxErr, []byte(test.output), test.commandErr)
			if (err != nil) != test.wantErr {
				t.Fatalf("classifyTmuxSessionAbsence() error=%v, wantErr=%t", err, test.wantErr)
			}
			if test.ctxErr != nil && !errors.Is(err, test.ctxErr) {
				t.Fatalf("classifyTmuxSessionAbsence() error=%v, want errors.Is(%v)", err, test.ctxErr)
			}
		})
	}
}

func (f *spawnAssignmentPreflightFixture) assertNoAssignmentDurability(t *testing.T) {
	t.Helper()
	for _, path := range f.assignmentDurabilityPaths() {
		before, ok := f.durabilityBaseline[path]
		if !ok {
			t.Fatalf("spawn preflight durability baseline omits %s", path)
		}
		after, err := readSpawnAssignmentPathSnapshot(path)
		if err != nil {
			t.Fatalf("read spawn preflight durability %s: %v", path, err)
		}
		if before.exists != after.exists || before.mode != after.mode || !bytes.Equal(before.data, after.data) {
			t.Fatalf("spawn preflight changed assignment durability at %s: before=%+v after=%+v", path, before, after)
		}
	}
}

func (f *spawnAssignmentPreflightFixture) assignmentDurabilityPaths() []string {
	primary := filepath.Join(f.homeDir, ".ntm", "sessions", f.session, "assignments.json")
	return []string{
		primary,
		primary + ".bak",
		filepath.Join(f.configDir, "ntm", "sessions", f.session),
	}
}

func readSpawnAssignmentPathSnapshot(path string) (spawnAssignmentPathSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return spawnAssignmentPathSnapshot{}, nil
	}
	if err != nil {
		return spawnAssignmentPathSnapshot{}, err
	}
	snapshot := spawnAssignmentPathSnapshot{exists: true, mode: info.Mode()}
	if info.IsDir() {
		return snapshot, nil
	}
	snapshot.data, err = os.ReadFile(path)
	return snapshot, err
}

func (f *spawnAssignmentPreflightFixture) assertToolLogEmpty(t *testing.T) {
	t.Helper()
	if targetLog := f.targetPlanningLog(t); targetLog != "" {
		t.Fatalf("policy failure invoked target-project planning tools: %s", targetLog)
	}
}

func (f *spawnAssignmentPreflightFixture) assertPlanningLookup(t *testing.T, unverified string) {
	t.Helper()
	logText := f.targetPlanningLog(t)
	for _, want := range []string{"tool=bv\targs=--robot-triage", "tool=bv\targs=--robot-plan"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("planning preflight log omits %q: %s", want, logText)
		}
	}
	if unverified == "plan" {
		if strings.Contains(logText, "tool=br\t") {
			t.Fatalf("failed plan unexpectedly reached live-label lookup: %s", logText)
		}
		return
	}
	for _, want := range []string{
		"tool=br\targs=--lock-timeout 5000 ready --json --limit 100000",
		"tool=br\targs=--lock-timeout 5000 list --json --status open --limit 100000",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("label preflight log omits %q: %s", want, logText)
		}
	}
}

func (f *spawnAssignmentPreflightFixture) targetPlanningLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.toolLog)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read spawn preflight tool log: %v", err)
	}
	prefix := "cwd=" + f.projectDir + "\t"
	var targetRows []string
	for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(row, prefix) {
			targetRows = append(targetRows, strings.TrimPrefix(row, prefix))
		}
	}
	return strings.Join(targetRows, "\n")
}

func (f *spawnAssignmentPreflightFixture) assertSessionExists(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "has-session", "-t", f.session)
	cmd.Env = append([]string(nil), f.env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filtered target-gate session is missing: %v output=%s", err, output)
	}
}

func (f *spawnAssignmentPreflightFixture) assertAgentNotLaunched(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.launchMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree failure launched agent: marker error=%v", err)
	}
}

func (f *spawnAssignmentPreflightFixture) assertSessionPaneCount(t *testing.T, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "list-panes", "-t", f.session, "-F", "#{pane_id}")
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list safety-race session panes: %v output=%s", err, output)
	}
	got := len(strings.Fields(string(output)))
	if got != want {
		t.Fatalf("safety-race pane count=%d, want unchanged %d; output=%s", got, want, output)
	}
}

func (f *spawnAssignmentPreflightFixture) captureSession(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.tmuxPath, "capture-pane", "-p", "-t", f.session, "-S", "-2000")
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture filtered target-gate session: %v output=%s", err, output)
	}
	return string(output)
}

func (f *spawnAssignmentPreflightFixture) assertBeadOpen(t *testing.T, beadID string) {
	t.Helper()
	data := f.mustBR(t, "show", beadID, "--json")
	var rows []spawnAssignmentBead
	if err := json.Unmarshal(data, &rows); err != nil {
		var row spawnAssignmentBead
		if objectErr := json.Unmarshal(data, &row); objectErr != nil {
			t.Fatalf("decode gated bead state: array=%v object=%v raw=%s", err, objectErr, data)
		}
		rows = []spawnAssignmentBead{row}
	}
	if len(rows) != 1 || rows[0].ID != beadID || rows[0].Status != "open" || rows[0].Assignee != "" {
		t.Fatalf("gated bead state=%+v, want open and unassigned", rows)
	}
}

func decodeSpawnAssignmentPreflightFailure(t *testing.T, result spawnAssignmentProcessResult, code, fragment string) spawnAssignmentOutput {
	t.Helper()
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("spawn preflight exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var output spawnAssignmentOutput
	decodeSpawnAssignmentSingleJSON(t, result.stdout, &output)
	if output.Success || output.ErrorCode != code || output.Error == "" ||
		!strings.Contains(strings.ToLower(output.Error), strings.ToLower(fragment)) {
		t.Fatalf("spawn preflight output=%+v, want code=%s error containing %q", output, code, fragment)
	}
	return output
}

func assertNormalWorktreeSpawnFailure(t *testing.T, result spawnAssignmentProcessResult, fragment string) {
	t.Helper()
	if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("worktree preflight exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	if !strings.Contains(strings.ToLower(string(result.stdout)), strings.ToLower(fragment)) {
		t.Fatalf("worktree preflight stdout=%s, want error containing %q", result.stdout, fragment)
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(result.stdout)))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode worktree preflight JSON: %v raw=%s", err, result.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("worktree preflight emitted multiple JSON documents: err=%v trailing=%v raw=%s", err, trailing, result.stdout)
	}
	if success, ok := document["success"].(bool); !ok || success {
		t.Fatalf("worktree preflight success=%v, want false: %s", document["success"], result.stdout)
	}
}

func assertNormalWorktreeFailureMutation(t *testing.T, raw []byte, sessionMayExist bool, wantPaths []string) {
	t.Helper()
	var document struct {
		PartialMutation       bool     `json:"partial_mutation"`
		SessionMayExist       bool     `json:"session_may_exist"`
		AffectedWorktreePaths []string `json:"affected_worktree_paths"`
	}
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode worktree mutation failure: %v raw=%s", err, raw)
	}
	if !document.PartialMutation || document.SessionMayExist != sessionMayExist || !slices.Equal(document.AffectedWorktreePaths, wantPaths) {
		t.Fatalf("worktree mutation envelope=%+v, want session_may_exist=%t affected=%v", document, sessionMayExist, wantPaths)
	}
}

func decodeSpawnAssignmentSingleJSON(t *testing.T, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode spawn assignment JSON: %v raw=%s", err, raw)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("spawn assignment output contains trailing data: err=%v raw=%s", err, raw)
	}
}

func spawnAssignmentPreflightMailCounter(t *testing.T) (func() int, string) {
	t.Helper()
	var mu sync.Mutex
	mutations := 0
	mutatingTools := map[string]struct{}{
		"ensure_project":            {},
		"register_agent":            {},
		"file_reservation_paths":    {},
		"release_file_reservations": {},
		"send_message":              {},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		reader := http.MaxBytesReader(w, r.Body, 1<<20)
		decodeErr := json.NewDecoder(reader).Decode(&request)
		if decodeErr != nil {
			mu.Lock()
			mutations++
			mu.Unlock()
		} else if request.Method == "tools/call" {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				mu.Lock()
				mutations++
				mu.Unlock()
			} else if _, mutating := mutatingTools[params.Name]; mutating {
				mu.Lock()
				mutations++
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": "preflight Agent Mail mutation guard",
			},
		})
	}))
	t.Cleanup(server.Close)
	readCalls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return mutations
	}
	return readCalls, server.URL + "/mcp/"
}

type spawnAssignmentProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type spawnPromptCLIOutput struct {
	Spawn  spawnResponse   `json:"spawn"`
	Init   spawnPromptInit `json:"init"`
	Assign json.RawMessage `json:"assign"`
}

type spawnPromptInit struct {
	PromptSent    bool                 `json:"prompt_sent"`
	AgentsReached int                  `json:"agents_reached"`
	Receipts      []spawnPromptReceipt `json:"receipts"`
}

type spawnPromptReceipt struct {
	Target struct {
		Ref struct {
			PaneID      string `json:"pane_id"`
			WindowIndex int    `json:"window_index"`
			PaneIndex   int    `json:"pane_index"`
		} `json:"ref"`
		Address   string `json:"address"`
		AgentType string `json:"agent_type"`
	} `json:"target"`
	Status    string `json:"status"`
	Protocol  string `json:"protocol"`
	Redaction struct {
		Mode     string `json:"mode"`
		Findings int    `json:"findings"`
	} `json:"redaction"`
}

type spawnAssignmentOutput struct {
	Success        bool                      `json:"success"`
	Timestamp      string                    `json:"timestamp"`
	Session        string                    `json:"session"`
	WorkingDir     string                    `json:"working_dir"`
	Agents         []spawnAssignmentAgent    `json:"agents"`
	Mode           string                    `json:"mode"`
	AssignStrategy string                    `json:"assign_strategy"`
	Assignments    []spawnAssignmentResponse `json:"assignments"`
	Error          string                    `json:"error"`
	ErrorCode      string                    `json:"error_code"`
}

type normalSpawnAssignmentOutput struct {
	Success bool     `json:"success"`
	Errors  []string `json:"errors"`
	Spawn   struct {
		Session          string `json:"session"`
		WorkingDirectory string `json:"working_directory"`
	} `json:"spawn"`
	Init   json.RawMessage `json:"init"`
	Assign *struct {
		Assignments []json.RawMessage              `json:"assignments"`
		Skipped     []normalSpawnAssignmentSkipped `json:"skipped"`
	} `json:"assign"`
}

type normalSpawnAssignmentSkipped struct {
	BeadID string `json:"bead_id"`
	Reason string `json:"reason"`
}

type spawnAssignmentAgent struct {
	Pane  string `json:"pane"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Ready bool   `json:"ready"`
	Error string `json:"error"`
}

type spawnAssignmentResponse struct {
	Pane              string `json:"pane"`
	AgentType         string `json:"agent_type"`
	BeadID            string `json:"bead_id"`
	BeadTitle         string `json:"bead_title"`
	Priority          string `json:"priority"`
	Claimed           bool   `json:"claimed"`
	PromptSent        bool   `json:"prompt_sent"`
	ClaimActor        string `json:"claim_actor"`
	IdempotencyKey    string `json:"idempotency_key"`
	DispatchReceiptID string `json:"dispatch_receipt_id"`
	ReservationIDs    []int  `json:"reservation_ids"`
	ClaimError        string `json:"claim_error"`
	PromptError       string `json:"prompt_error"`
}

type spawnAssignmentLedger struct {
	SessionName string                            `json:"session_name"`
	Assignments map[string]*spawnAssignmentRecord `json:"assignments"`
	Version     int                               `json:"version"`
}

type spawnAssignmentRecord struct {
	BeadID                string     `json:"bead_id"`
	BeadTitle             string     `json:"bead_title"`
	Pane                  int        `json:"pane"`
	AgentType             string     `json:"agent_type"`
	AgentName             string     `json:"agent_name"`
	Status                string     `json:"status"`
	PromptSent            string     `json:"prompt_sent"`
	IdempotencyKey        string     `json:"idempotency_key"`
	ClaimActor            string     `json:"claim_actor"`
	ClaimState            string     `json:"claim_state"`
	ClaimStatus           string     `json:"claim_status"`
	ClaimAttempts         int        `json:"claim_attempts"`
	ClaimedAt             *time.Time `json:"claimed_at"`
	ReservationRequired   bool       `json:"reservation_required"`
	ReservationInputPaths []string   `json:"reservation_input_paths"`
	ReservationState      string     `json:"reservation_state"`
	ReservationAttempts   int        `json:"reservation_attempts"`
	ReservationCompleted  bool       `json:"reservation_completed"`
	ReservationAgent      string     `json:"reservation_agent"`
	ReservationTarget     string     `json:"reservation_target"`
	ReservationRequested  []string   `json:"reservation_requested"`
	ReservedPaths         []string   `json:"reserved_paths"`
	ReservationIDs        []int      `json:"reservation_ids"`
	ReservationExpiresAt  *time.Time `json:"reservation_expires_at"`
	ReservationError      string     `json:"reservation_error"`
	DispatchState         string     `json:"dispatch_state"`
	DispatchTarget        string     `json:"dispatch_target"`
	OccupancyKey          string     `json:"occupancy_key"`
	PendingPrompt         string     `json:"pending_prompt"`
	DispatchAttempts      int        `json:"dispatch_attempts"`
	DispatchStartedAt     *time.Time `json:"dispatch_started_at"`
	DispatchedAt          *time.Time `json:"dispatched_at"`
	DispatchReceiptID     string     `json:"dispatch_receipt_id"`
}

type spawnAssignmentBead struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

func newSpawnAssignmentCLIFixture(t *testing.T) *spawnAssignmentCLIFixture {
	t.Helper()

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Skipf("br is required for spawn assignment E2E: %v", err)
	}

	root := t.TempDir()
	// macOS t.TempDir() lives under /var, a symlink to /private/var. The
	// production coordinator resolves the project path before Agent Mail
	// lookups (effective_project_key), so the fixture must hold the resolved
	// form or the seeded pane registry's project key never matches and every
	// claim fails with "no canonical Agent Mail identity" (bd-fe90b
	// precedent).
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	fixture := &spawnAssignmentCLIFixture{
		ntmPath:    ntmPath,
		tmuxPath:   tmuxPath,
		brPath:     brPath,
		session:    fmt.Sprintf("ntm-e2e-spawn-assign-%d-%d", os.Getpid(), time.Now().UnixNano()),
		projectDir: filepath.Join(root, "project"),
		homeDir:    filepath.Join(root, "home"),
		configDir:  filepath.Join(root, "config"),
		marker:     fmt.Sprintf("NTM_SPAWN_ASSIGN_%d", time.Now().UnixNano()),
	}
	fixture.beadTitle = fixture.marker

	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{
		fixture.projectDir,
		fixture.homeDir,
		fixture.configDir,
		filepath.Join(root, "data"),
		fakeBin,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture directory %s: %v", dir, err)
		}
	}

	fixture.env = spawnAssignmentIsolatedEnv(map[string]string{
		"HOME":            fixture.homeDir,
		"XDG_CONFIG_HOME": fixture.configDir,
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"TMUX_TMPDIR":     tmuxRoot,
		// No test here asserts monitor state, and the detached resilience
		// monitor keeps writing under HOME after the tmux server dies,
		// racing t.TempDir cleanup ("directory not empty") under load.
		"NTM_DISABLE_INTERNAL_MONITOR": "1",
		"PATH":                         fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"AGENT_MAIL_URL":               "http://127.0.0.1:1/mcp/",
		"AGENT_MAIL_TOKEN":             "",
		"HTTP_PROXY":                   "",
		"HTTPS_PROXY":                  "",
		"ALL_PROXY":                    "",
		"NO_PROXY":                     "127.0.0.1,localhost",
		"NO_COLOR":                     "1",
		"TERM":                         "xterm-256color",
		"NTM_CONFIG":                   "",
		"NTM_OUTPUT_FORMAT":            "",
		"NTM_ROBOT_FORMAT":             "",
		"TOON_DEFAULT_FORMAT":          "",
	})

	writeSpawnFakeClaude(t, filepath.Join(fakeBin, "claude"))
	fixture.mustBR(t, "init", "--prefix=spawne2e", "--json")
	fixture.beadID = strings.TrimSpace(string(fixture.mustBR(
		t, "create", fixture.beadTitle, "--type=task", "--priority=1", "--silent",
	)))
	if fixture.beadID == "" || strings.ContainsAny(fixture.beadID, " \t\r\n") {
		t.Fatalf("unexpected br create output %q", fixture.beadID)
	}
	fixture.assertBead(t, "open", "")
	writeSpawnFakeBV(t, filepath.Join(fakeBin, "bv"), fixture.beadID, fixture.beadTitle)

	fixture.stub = &spawnAssignmentMCPStub{
		projectDir: fixture.projectDir,
		beadID:     fixture.beadID,
		recipient:  spawnAssignmentRecipient,
		path:       spawnAssignmentPath,
		token:      spawnAssignmentToken,
	}
	server := httptest.NewUnstartedServer(fixture.stub)
	server.Config.ReadHeaderTimeout = 2 * time.Second
	server.Config.ReadTimeout = 2 * time.Second
	server.Config.WriteTimeout = 2 * time.Second
	server.Config.IdleTimeout = 2 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	fixture.env = spawnAssignmentMergeEnv(fixture.env, map[string]string{
		"AGENT_MAIL_URL": server.URL + "/mcp/",
	})

	tmuxConfig := filepath.Join(root, "tmux.conf")
	tmuxConfigBody := strings.Join([]string{
		"set -g base-index 0",
		"setw -g pane-base-index 0",
		"set -g renumber-windows off",
		"set -g status off",
		"setw -g allow-rename off",
		"setw -g automatic-rename off",
		"",
	}, "\n")
	if err := os.WriteFile(tmuxConfig, []byte(tmuxConfigBody), 0o600); err != nil {
		t.Fatalf("write tmux config: %v", err)
	}
	t.Cleanup(func() { cleanupSpawnPrivateTmuxServer(t, fixture.tmuxPath, fixture.env) })
	fixture.mustTMUX(t, "-f", tmuxConfig, "new-session", "-d", "-s", fixture.session,
		"-x", "160", "-y", "48", "-c", fixture.projectDir, "bash --noprofile --norc")
	fixture.waitForInitialPane(t)
	fixture.seedAgentRegistry(t)
	fixture.expectedPrompt = expectedSpawnWorkPrompt(fixture.beadID, fixture.beadTitle)

	return fixture
}

func (f *spawnAssignmentCLIFixture) spawnArgs() []string {
	return []string{
		"--robot-format=json",
		"--robot-spawn=" + f.session,
		"--spawn-cc=1",
		"--spawn-no-user",
		"--spawn-dir=" + f.projectDir,
		"--spawn-wait",
		"--timeout=8s",
		"--spawn-assign-work",
		"--strategy=top-n",
		"--spawn-names=" + spawnAssignmentDisplayName,
		"--require-reservation",
		"--reservation-paths=" + spawnAssignmentPath,
	}
}

func (f *spawnAssignmentCLIFixture) runNTM(t *testing.T, args ...string) spawnAssignmentProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	cmd.Dir = f.projectDir
	cmd.Env = append([]string(nil), f.env...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("ntm spawn assignment timed out: %q", args)
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run ntm spawn assignment: %v", err)
		}
	}
	t.Logf("[E2E-SPAWN-ASSIGN] exit=%d stdout=%s stderr=%s", exitCode,
		truncateString(stdout.String(), 800), truncateString(stderr.String(), 800))
	return spawnAssignmentProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func decodeSpawnAssignmentOutput(t *testing.T, result spawnAssignmentProcessResult) spawnAssignmentOutput {
	t.Helper()
	if result.exitCode != 0 || len(bytes.TrimSpace(result.stderr)) != 0 {
		t.Fatalf("spawn assignment exit=%d stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	var output spawnAssignmentOutput
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(result.stdout)))
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("decode spawn JSON: %v raw=%s", err, result.stdout)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("spawn output contains trailing data: err=%v raw=%s", err, result.stdout)
	}
	return output
}

func assertSpawnAssignmentOutput(t *testing.T, output spawnAssignmentOutput, f *spawnAssignmentCLIFixture) spawnAssignmentResponse {
	t.Helper()
	if !output.Success || output.Timestamp == "" || output.Session != f.session ||
		output.WorkingDir != f.projectDir || output.Mode != "orchestrator" ||
		output.AssignStrategy != "top-n" || output.Error != "" || output.ErrorCode != "" {
		t.Fatalf("spawn output envelope = %+v", output)
	}
	if len(output.Agents) != 1 {
		t.Fatalf("spawn agents = %+v", output.Agents)
	}
	agent := output.Agents[0]
	wantTitle := f.session + "__cc_1"
	if agent.Pane != fmt.Sprintf("0.%d", f.paneIndex) || agent.Name != spawnAssignmentDisplayName ||
		agent.Type != "claude" || agent.Title != wantTitle || !agent.Ready || agent.Error != "" {
		t.Fatalf("spawn agent = %+v", agent)
	}
	if len(output.Assignments) != 1 {
		t.Fatalf("spawn assignments = %+v", output.Assignments)
	}
	assignment := output.Assignments[0]
	if assignment.Pane != fmt.Sprint(f.paneIndex) || assignment.AgentType != "claude" ||
		assignment.BeadID != f.beadID || assignment.BeadTitle != f.beadTitle || assignment.Priority != "P1" ||
		!assignment.Claimed || !assignment.PromptSent || assignment.ClaimActor == "" ||
		assignment.IdempotencyKey == "" || assignment.DispatchReceiptID == "" ||
		!equalInts(assignment.ReservationIDs, []int{spawnAssignmentReservationID}) ||
		assignment.ClaimError != "" || assignment.PromptError != "" {
		t.Fatalf("spawn assignment = %+v", assignment)
	}
	decodedKey, err := hex.DecodeString(assignment.IdempotencyKey)
	if err != nil || len(decodedKey) != 32 {
		t.Fatalf("idempotency key %q is not 256-bit hex: bytes=%d err=%v", assignment.IdempotencyKey, len(decodedKey), err)
	}
	wantActor := spawnAssignmentRecipient + "/ntm-" + assignment.IdempotencyKey[:12]
	if assignment.ClaimActor != wantActor {
		t.Fatalf("claim actor = %q, want exact registered identity %q", assignment.ClaimActor, wantActor)
	}
	if !strings.Contains(assignment.DispatchReceiptID, f.paneID) {
		t.Fatalf("dispatch receipt %q does not identify stable pane %q", assignment.DispatchReceiptID, f.paneID)
	}
	return assignment
}

func assertSpawnAssignmentRecord(t *testing.T, record *spawnAssignmentRecord, response spawnAssignmentResponse, f *spawnAssignmentCLIFixture) {
	t.Helper()
	if record.BeadID != f.beadID || record.BeadTitle != f.beadTitle || record.Pane != f.paneIndex ||
		record.AgentType != "claude" || record.AgentName != spawnAssignmentRecipient || record.Status != "assigned" ||
		record.PromptSent != f.expectedPrompt || record.IdempotencyKey != response.IdempotencyKey ||
		record.ClaimActor != response.ClaimActor || record.ClaimState != "claimed" ||
		record.ClaimStatus != "in_progress" || record.ClaimAttempts != 1 || record.ClaimedAt == nil {
		t.Fatalf("durable claim identity/state = %+v", record)
	}
	if !record.ReservationRequired || !equalStrings(record.ReservationInputPaths, []string{spawnAssignmentPath}) ||
		record.ReservationState != "reserved" || record.ReservationAttempts != 1 || !record.ReservationCompleted ||
		record.ReservationAgent != spawnAssignmentRecipient || record.ReservationTarget != f.paneID ||
		!equalStrings(record.ReservationRequested, []string{spawnAssignmentPath}) ||
		!equalStrings(record.ReservedPaths, []string{spawnAssignmentPath}) ||
		!equalInts(record.ReservationIDs, []int{spawnAssignmentReservationID}) ||
		record.ReservationExpiresAt == nil || !record.ReservationExpiresAt.After(time.Now()) || record.ReservationError != "" {
		t.Fatalf("durable reservation receipt = %+v", record)
	}
	if record.DispatchState != "sent" || record.DispatchTarget != f.paneID || record.OccupancyKey != f.paneID ||
		record.PendingPrompt != "" || record.DispatchAttempts != 1 || record.DispatchStartedAt == nil ||
		record.DispatchedAt == nil || record.DispatchReceiptID != response.DispatchReceiptID {
		t.Fatalf("durable dispatch receipt = %+v", record)
	}
	if record.ClaimedAt.After(*record.DispatchStartedAt) || record.DispatchStartedAt.After(*record.DispatchedAt) {
		t.Fatalf("claim-reserve-dispatch order violated: claim=%s dispatch-start=%s dispatched=%s",
			record.ClaimedAt, record.DispatchStartedAt, record.DispatchedAt)
	}
}

func (f *spawnAssignmentCLIFixture) seedAgentRegistry(t *testing.T) {
	t.Helper()
	registry := agentmail.NewSessionAgentRegistry(f.session, f.projectDir)
	registry.AddAgent(f.session+"__cc_1", f.paneID, spawnAssignmentRecipient)
	registry.SetRegistrationToken(spawnAssignmentRecipient, spawnAssignmentToken)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		t.Fatalf("marshal Agent Mail pane registry: %v", err)
	}
	// The product loads the registry from os.UserConfigDir(): on Linux that
	// honors XDG_CONFIG_HOME, but on macOS it is always
	// "$HOME/Library/Application Support" — seed both bases so the claim's
	// canonical-identity resolution finds the pane registry on either OS.
	for _, base := range []string{
		filepath.Join(f.configDir, "ntm", "sessions"),
		filepath.Join(f.homeDir, "Library", "Application Support", "ntm", "sessions"),
	} {
		path := filepath.Join(base, f.session,
			agentmail.ProjectSlugFromPath(f.projectDir), "agent_registry.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create Agent Mail registry directory: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write Agent Mail pane registry: %v", err)
		}
	}
}

func (f *spawnAssignmentCLIFixture) waitForInitialPane(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		output, err := f.tmuxOutput(ctx, "list-panes", "-t", f.session,
			"-F", "#{window_index}|#{pane_index}|#{pane_id}")
		cancel()
		if err == nil {
			parts := strings.Split(strings.TrimSpace(string(output)), "|")
			var window int
			if len(parts) == 3 {
				if _, scanErr := fmt.Sscanf(parts[0]+" "+parts[1], "%d %d", &window, &f.paneIndex); scanErr == nil &&
					window == 0 && strings.HasPrefix(parts[2], "%") {
					f.paneID = parts[2]
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("private tmux session did not expose its initial pane")
}

func (f *spawnAssignmentCLIFixture) waitForMarkerCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.markerCount(t) == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.assertMarkerCount(t, want)
}

func (f *spawnAssignmentCLIFixture) assertMarkerCount(t *testing.T, want int) {
	t.Helper()
	if got := f.markerCount(t); got != want {
		t.Fatalf("work prompt marker count = %d, want %d; pane=%q", got, want, f.capturePane(t))
	}
}

func (f *spawnAssignmentCLIFixture) markerCount(t *testing.T) int {
	t.Helper()
	return strings.Count(f.capturePane(t), f.marker)
}

func (f *spawnAssignmentCLIFixture) sessionMarkerCounts(t *testing.T) map[string]int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := f.tmuxOutput(ctx, "list-panes", "-s", "-t", f.session, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list spawned panes for marker count: %v", err)
	}
	counts := make(map[string]int)
	for _, paneID := range strings.Fields(string(output)) {
		captured, captureErr := f.tmuxOutput(ctx, "capture-pane", "-p", "-t", paneID, "-S", "-2000")
		if captureErr != nil {
			t.Fatalf("capture spawned pane %s for marker count: %v", paneID, captureErr)
		}
		counts[paneID] = strings.Count(string(captured), f.marker)
	}
	return counts
}

func totalSpawnMarkerCount(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func equalSpawnMarkerCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for paneID, count := range left {
		if right[paneID] != count {
			return false
		}
	}
	return true
}

func (f *spawnAssignmentCLIFixture) capturePane(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := f.tmuxOutput(ctx, "capture-pane", "-p", "-t", f.paneID, "-S", "-2000")
	if err != nil {
		t.Fatalf("capture fake spawned agent: %v", err)
	}
	return string(output)
}

func (f *spawnAssignmentCLIFixture) assertBead(t *testing.T, wantStatus, wantAssignee string) {
	t.Helper()
	output := f.mustBR(t, "show", f.beadID, "--json")
	var rows []spawnAssignmentBead
	if err := json.Unmarshal(output, &rows); err != nil {
		var row spawnAssignmentBead
		if objectErr := json.Unmarshal(output, &row); objectErr != nil {
			t.Fatalf("decode br show: array=%v object=%v raw=%s", err, objectErr, output)
		}
		rows = []spawnAssignmentBead{row}
	}
	if len(rows) != 1 || rows[0].ID != f.beadID || rows[0].Status != wantStatus || rows[0].Assignee != wantAssignee {
		t.Fatalf("bead state = %+v, want id=%s status=%s assignee=%s", rows, f.beadID, wantStatus, wantAssignee)
	}
}

func (f *spawnAssignmentCLIFixture) readAssignment(t *testing.T) *spawnAssignmentRecord {
	t.Helper()
	path := filepath.Join(f.homeDir, ".ntm", "sessions", f.session, "assignments.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spawn assignment ledger: %v", err)
	}
	var ledger spawnAssignmentLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatalf("decode spawn assignment ledger: %v raw=%s", err, data)
	}
	if ledger.SessionName != f.session || ledger.Version < 4 {
		t.Fatalf("spawn assignment ledger header = session:%q version:%d", ledger.SessionName, ledger.Version)
	}
	record := ledger.Assignments[f.beadID]
	if record == nil {
		t.Fatalf("spawn assignment ledger missing %s: %+v", f.beadID, ledger.Assignments)
	}
	return record
}

func (f *spawnAssignmentCLIFixture) mustBR(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.brPath, args...)
	cmd.Dir = f.projectDir
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("br command timed out: %q", args)
	}
	if err != nil {
		t.Fatalf("br %q: %v output=%s", args, err, output)
	}
	return output
}

func (f *spawnAssignmentCLIFixture) mustTMUX(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	if err := f.runTMUX(ctx, args...); err != nil {
		t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
}

func cleanupSpawnPrivateTmuxServer(t *testing.T, tmuxPath string, env []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTmuxSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tmuxPath, "kill-server")
	cmd.Env = append([]string(nil), env...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Errorf("private tmux cleanup timed out after %s: output=%s", defaultTmuxSetupTimeout, output)
		return
	}
	if err != nil && !isBenignTmuxCleanupError(output) {
		t.Errorf("clean up private tmux server: %v output=%s", err, output)
	}
}

func (f *spawnAssignmentCLIFixture) runTMUX(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (f *spawnAssignmentCLIFixture) tmuxOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func writeSpawnFakeClaude(t *testing.T, path string) {
	t.Helper()
	// The fake Claude must be a non-shell foreground process: prompt dispatch
	// crosses the PANE_AGENT_DEAD liveness gate (ntm-0g0b), and a `#!/bin/sh`
	// fake reports pane_current_command "bash" on macOS (the kernel resolves
	// the interpreter, not the script name). The wrapper execs the fakeshell
	// fixture (bd-h4t0j) in echo mode, preserving the historical surface:
	// "Claude Code v0.0.0" banner for the ready gate, the "❯" composer glyph
	// for the bd-dp9oy visibility gate, tty echo disabled, and one
	// "RECEIVED:<line>" transcript row per submitted line.
	fakeshellBin, err := ensureFakeshellBin()
	if err != nil {
		t.Fatalf("build fakeshell fixture: %v", err)
	}
	content := fmt.Sprintf(`#!/bin/sh
exec %s --mode=echo --tty-noecho --prompt='❯ ' --banner='Claude Code v0.0.0\n● Ready'
`, tmux.ShellQuote(fakeshellBin))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake Claude executable: %v", err)
	}
}

func writeSpawnGatedFakeClaude(t *testing.T, path, readyFile string) {
	t.Helper()
	// The gate itself stays in the sh wrapper (WAITING chrome, no composer,
	// not agent-ready), then execs the fakeshell fixture so the post-gate
	// pane is a non-shell foreground process with the "❯" composer glyph —
	// otherwise prompt delivery after the gate opens fails PANE_AGENT_DEAD
	// (see writeSpawnFakeClaude).
	fakeshellBin, err := ensureFakeshellBin()
	if err != nil {
		t.Fatalf("build fakeshell fixture: %v", err)
	}
	quotedReadyFile := strings.ReplaceAll(readyFile, "'", "'\"'\"'")
	content := fmt.Sprintf(`#!/bin/sh
ready_file='%s'
printf 'WAITING_FOR_E2E_READY\n'
while [ ! -f "$ready_file" ]; do
    sleep 0.05
done
exec %s --mode=echo --tty-noecho --prompt='❯ ' --banner='● Ready'
`, quotedReadyFile, tmux.ShellQuote(fakeshellBin))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write gated fake Claude executable: %v", err)
	}
}

func writeSpawnLaunchMarkerAgent(t *testing.T, path, markerPath string) {
	t.Helper()
	// Non-shell foreground process for the PANE_AGENT_DEAD gate (see
	// writeSpawnFakeClaude). This writer serves multiple agent binaries
	// (claude and codex), so the prompt carries both composer glyphs — each
	// type's bd-dp9oy visibility check only looks for its own marker on the
	// bottom-most line, and an empty composer follows either glyph.
	fakeshellBin, err := ensureFakeshellBin()
	if err != nil {
		t.Fatalf("build fakeshell fixture: %v", err)
	}
	quotedMarker := strings.ReplaceAll(markerPath, "'", "'\"'\"'")
	content := fmt.Sprintf(`#!/bin/sh
printf 'launched\n' > '%s'
exec %s --mode=echo --tty-noecho --prompt='❯ › ' --banner='Claude Code v0.0.0\n● Ready'
`, quotedMarker, tmux.ShellQuote(fakeshellBin))
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write launch-marker agent executable: %v", err)
	}
}

func writeSpawnWorkingDirectoryMarkerAgent(t *testing.T, path, markerPath string) {
	t.Helper()
	quotedMarker := strings.ReplaceAll(markerPath, "'", "'\"'\"'")
	content := fmt.Sprintf(`#!/bin/sh
pwd > '%s'
stty -echo
printf 'Claude Code v0.0.0\n\342\227\217 Ready\n\342\235\257 \n'
while IFS= read -r line; do
    printf 'RECEIVED:%%s\n' "$line"
done
`, quotedMarker)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write working-directory marker agent executable: %v", err)
	}
}

func writeSpawnFakeBV(t *testing.T, path, beadID, title string) {
	t.Helper()
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Fatalf("find br for fake bv readiness projection: %v", err)
	}
	triagePayload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"data_hash":    "spawn-assignment-e2e",
		"triage": map[string]any{
			"meta": map[string]any{
				"version": "e2e", "generated_at": time.Now().UTC().Format(time.RFC3339Nano),
				"phase2_ready": true, "issue_count": 1, "compute_time_ms": 1,
			},
			"quick_ref": map[string]any{
				"open_count": 1, "actionable_count": 1, "blocked_count": 0,
				"in_progress_count": 0, "top_picks": []any{},
			},
			"recommendations": []map[string]any{{
				"id": beadID, "title": title, "type": "task", "status": "ready",
				"priority": 1, "score": 100.0, "action": "claim",
				"reasons": []string{"spawn CLI E2E"},
			}},
		},
	}
	planPayload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"plan": map[string]any{
			"tracks": []map[string]any{{
				"track_id": "spawn-assignment-e2e",
				"items": []map[string]any{{
					"id": beadID, "title": title, "status": "open", "priority": 1,
				}},
			}},
		},
	}
	emptyPlanPayload := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"plan":         map[string]any{"tracks": []any{}},
	}
	insightsPayload := map[string]any{"Cycles": []any{}}
	triageEncoded, err := json.Marshal(triagePayload)
	if err != nil {
		t.Fatalf("encode fake bv triage: %v", err)
	}
	planEncoded, err := json.Marshal(planPayload)
	if err != nil {
		t.Fatalf("encode fake bv plan: %v", err)
	}
	emptyPlanEncoded, err := json.Marshal(emptyPlanPayload)
	if err != nil {
		t.Fatalf("encode empty fake bv plan: %v", err)
	}
	insightsEncoded, err := json.Marshal(insightsPayload)
	if err != nil {
		t.Fatalf("encode fake bv insights: %v", err)
	}
	quotedBR := strings.ReplaceAll(brPath, "'", "'\"'\"'")
	quotedBeadID := strings.ReplaceAll(beadID, "'", "'\"'\"'")
	content := fmt.Sprintf(`#!/bin/sh
if [ -n "$SPAWN_PREFLIGHT_TOOL_LOG" ]; then
  printf 'cwd=%%s\ttool=bv\targs=%%s\n' "$PWD" "$*" >> "$SPAWN_PREFLIGHT_TOOL_LOG"
fi
case "$1" in
  --robot-triage) printf '%%s\n' '%s' ;;
  --robot-plan)
    if '%s' show '%s' --json | grep -Eq '"status"[[:space:]]*:[[:space:]]*"open"'; then
      printf '%%s\n' '%s'
    else
      printf '%%s\n' '%s'
    fi
    ;;
  --robot-insights) printf '%%s\n' '%s' ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, triageEncoded, quotedBR, quotedBeadID, planEncoded, emptyPlanEncoded, insightsEncoded)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bv executable: %v", err)
	}
}

func writeSpawnEmptyBV(t *testing.T, path string) {
	t.Helper()
	triage := `{"generated_at":"2026-07-13T00:00:00Z","data_hash":"spawn-prompt-e2e","triage":{"meta":{"version":"e2e","generated_at":"2026-07-13T00:00:00Z","phase2_ready":true,"issue_count":0,"compute_time_ms":1},"quick_ref":{"open_count":0,"actionable_count":0,"blocked_count":0,"in_progress_count":0,"top_picks":[]},"recommendations":[]}}`
	plan := `{"generated_at":"2026-07-13T00:00:00Z","plan":{"tracks":[]}}`
	insights := `{"Bottlenecks":[],"Keystones":[],"Hubs":[],"Authorities":[],"Cycles":[]}`
	content := fmt.Sprintf(`#!/bin/sh
case "$1" in
  --robot-triage) printf '%%s\n' '%s' ;;
  --robot-plan) printf '%%s\n' '%s' ;;
  --robot-insights) printf '%%s\n' '%s' ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, triage, plan, insights)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write empty fake bv executable: %v", err)
	}
}

func expectedSpawnWorkPrompt(beadID, title string) string {
	return fmt.Sprintf("Work on bead %s: %s\n\nUse `br show %s` to see full details.\n"+
		"This bead has been marked as in_progress.\n\nContext:\n- spawn CLI E2E\n\n"+
		"When done, close it with: `br close %s --reason \"Completed\"`", beadID, title, beadID, beadID)
}

func spawnAssignmentIsolatedEnv(overrides map[string]string) []string {
	replaced := map[string]struct{}{
		"HOME": {}, "XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {}, "XDG_STATE_HOME": {}, "XDG_CACHE_HOME": {},
		"PWD": {}, "OLDPWD": {}, "GIT_DIR": {}, "GIT_WORK_TREE": {}, "BR_DB": {}, "BD_DB": {}, "BEADS_DB": {}, "AGENT_NAME": {},
		"PATH": {},
		"TMUX": {}, "TMUX_PANE": {}, "TMUX_TMPDIR": {},
		"NTM_CONFIG": {}, "NTM_OUTPUT_FORMAT": {}, "NTM_ROBOT_FORMAT": {}, "TOON_DEFAULT_FORMAT": {},
		"AGENT_MAIL_URL": {}, "AGENT_MAIL_TOKEN": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	}
	for key := range overrides {
		replaced[key] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := replaced[key]; !skip {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func spawnAssignmentMergeEnv(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type spawnAssignmentMCPStub struct {
	mu         sync.Mutex
	projectDir string
	beadID     string
	recipient  string
	path       string
	token      string
	ensure     int
	list       int
	reserve    int
	errors     []string
	// blockAgentsPath makes the agents resource wait for request cancellation
	// after recording a marker. It is used only by the signal-cancellation E2E.
	blockAgentsPath string
	blockAgentsAt   int
}

func (s *spawnAssignmentMCPStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/mcp/" {
		s.failRPC(w, nil, -32600, fmt.Sprintf("unexpected HTTP request %s %s", r.Method, r.URL.Path))
		return
	}
	var request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	reader := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		s.failRPC(w, nil, -32700, "decode request: "+err.Error())
		return
	}
	if request.JSONRPC != "2.0" {
		s.failRPC(w, request.ID, -32600, "jsonrpc must be 2.0")
		return
	}

	switch request.Method {
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.failRPC(w, request.ID, -32602, "decode tool call: "+err.Error())
			return
		}
		s.handleTool(w, request.ID, params.Name, params.Arguments)
	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			s.failRPC(w, request.ID, -32602, "decode resource read: "+err.Error())
			return
		}
		s.handleResource(r.Context(), w, request.ID, params.URI)
	default:
		s.failRPC(w, request.ID, -32601, "unexpected method: "+request.Method)
	}
}

func (s *spawnAssignmentMCPStub) handleTool(w http.ResponseWriter, id any, name string, args map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch name {
	case "health_check":
		s.writeResult(w, id, map[string]any{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	case "ensure_project":
		if got, _ := args["human_key"].(string); got != s.projectDir {
			s.failRPCLocked(w, id, -32602, fmt.Sprintf("ensure_project human_key=%q want=%q", got, s.projectDir))
			return
		}
		s.ensure++
		s.writeResult(w, id, map[string]any{
			"id": spawnAssignmentProjectID, "slug": "spawn-assignment-e2e",
			"human_key": s.projectDir, "created_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	case "fetch_inbox", "list_file_reservations", "list_reservations":
		s.writeResult(w, id, []any{})
	case "file_reservation_paths":
		paths, ok := anyStringSlice(args["paths"])
		ttl, ttlOK := args["ttl_seconds"].(float64)
		if project, _ := args["project_key"].(string); project != s.projectDir ||
			args["agent_name"] != s.recipient || !ok || !equalStrings(paths, []string{s.path}) ||
			args["exclusive"] != true || !ttlOK || int(ttl) != 3600 ||
			args["reason"] != "bead assignment: "+s.beadID || args["registration_token"] != s.token {
			s.failRPCLocked(w, id, -32602, fmt.Sprintf("invalid reservation arguments: %#v", args))
			return
		}
		s.reserve++
		now := time.Now().UTC()
		s.writeResult(w, id, map[string]any{
			"granted": []map[string]any{{
				"id": spawnAssignmentReservationID, "path_pattern": s.path,
				"agent_name": s.recipient, "project_id": spawnAssignmentProjectID,
				"exclusive": true, "reason": "bead assignment: " + s.beadID,
				"created_ts": now.Format(time.RFC3339Nano),
				"expires_ts": now.Add(time.Hour).Format(time.RFC3339Nano),
			}},
			"conflicts": []any{},
		})
	default:
		s.failRPCLocked(w, id, -32601, "unexpected tool: "+name)
	}
}

func (s *spawnAssignmentMCPStub) handleResource(ctx context.Context, w http.ResponseWriter, id any, resourceURI string) {
	s.mu.Lock()
	if strings.HasPrefix(resourceURI, "resource://file_reservations/") {
		defer s.mu.Unlock()
		if !strings.Contains(resourceURI, url.QueryEscape(s.projectDir)) && !strings.Contains(resourceURI, url.PathEscape(s.projectDir)) {
			s.failRPCLocked(w, id, -32602, "unexpected reservation resource URI: "+resourceURI)
			return
		}
		s.writeResult(w, id, map[string]any{
			"contents": []map[string]any{{
				"uri": resourceURI, "mimeType": "application/json", "text": "[]",
			}},
		})
		return
	}
	const prefix = "resource://agents/"
	if !strings.HasPrefix(resourceURI, prefix) {
		s.failRPCLocked(w, id, -32602, "unexpected resource URI: "+resourceURI)
		s.mu.Unlock()
		return
	}
	project, err := url.PathUnescape(strings.TrimPrefix(resourceURI, prefix))
	if err != nil || project != s.projectDir {
		s.failRPCLocked(w, id, -32602, fmt.Sprintf("agents project=%q err=%v want=%q", project, err, s.projectDir))
		s.mu.Unlock()
		return
	}
	s.list++
	blockPath := s.blockAgentsPath
	blockAt := s.blockAgentsAt
	listCount := s.list
	s.mu.Unlock()
	if blockPath != "" && (blockAt <= 0 || listCount == blockAt) {
		if err := os.WriteFile(blockPath, []byte("started\n"), 0o600); err != nil {
			s.mu.Lock()
			s.errors = append(s.errors, "write agents block marker: "+err.Error())
			s.mu.Unlock()
			return
		}
		<-ctx.Done()
		return
	}
	agentsJSON, err := json.Marshal(map[string]any{
		"agents": []map[string]any{{
			"id": spawnAssignmentAgentID, "name": s.recipient, "program": "claude-code",
			"model": "e2e", "task_description": "spawn assignment E2E",
			"project_id":     spawnAssignmentProjectID,
			"inception_ts":   time.Now().UTC().Format(time.RFC3339Nano),
			"last_active_ts": time.Now().UTC().Format(time.RFC3339Nano),
		}},
	})
	if err != nil {
		s.failRPC(w, id, -32603, "encode agents resource: "+err.Error())
		return
	}
	s.writeResult(w, id, map[string]any{
		"contents": []map[string]any{{
			"uri": resourceURI, "mimeType": "application/json", "text": string(agentsJSON),
		}},
	})
}

func (s *spawnAssignmentMCPStub) writeResult(w http.ResponseWriter, id, result any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *spawnAssignmentMCPStub) failRPC(w http.ResponseWriter, id any, code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRPCLocked(w, id, code, message)
}

func (s *spawnAssignmentMCPStub) failRPCLocked(w http.ResponseWriter, id any, code int, message string) {
	s.errors = append(s.errors, message)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

type spawnAssignmentMCPCounts struct {
	ensure  int
	list    int
	reserve int
}

func (s *spawnAssignmentMCPStub) assertCleanMutationCount(t *testing.T, reserve int) spawnAssignmentMCPCounts {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errors) != 0 || s.ensure == 0 || s.list == 0 || s.reserve != reserve {
		t.Fatalf("Agent Mail MCP calls ensure/list/reserve=%d/%d/%d want discovery>0/reserve=%d errors=%v",
			s.ensure, s.list, s.reserve, reserve, s.errors)
	}
	return spawnAssignmentMCPCounts{ensure: s.ensure, list: s.list, reserve: s.reserve}
}

func (s *spawnAssignmentMCPStub) assertNoCalls(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errors) != 0 || s.ensure != 0 || s.list != 0 || s.reserve != 0 {
		t.Fatalf("Agent Mail MCP calls ensure/list/reserve=%d/%d/%d, want no calls; errors=%v",
			s.ensure, s.list, s.reserve, s.errors)
	}
}

func anyStringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}
