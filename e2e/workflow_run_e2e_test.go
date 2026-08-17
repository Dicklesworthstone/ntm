//go:build e2e
// +build e2e

package e2e

// workflow_run_e2e_test.go proves `ntm workflow run` drives the workflow
// RuntimeCoordinator against LIVE fakeagent sessions through the gated
// dispatch path (bd-ws2-wire-or-delete-ykmcz.2). The red-green E2E asserts
// the documented ping-pong red/green turn-taking from pane transcripts; the
// C2b suite covers the remaining builtins and user-TOML resolution.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// workflowRunJSON mirrors cli.WorkflowRunResult for envelope assertions.
type workflowRunJSON struct {
	Success      bool   `json:"success"`
	Workflow     string `json:"workflow"`
	Session      string `json:"session"`
	Coordination string `json:"coordination"`
	Agents       []struct {
		Role string `json:"role"`
		Pane string `json:"pane"`
	} `json:"agents"`
	Stages      []string `json:"stages"`
	Transitions int      `json:"transitions"`
	Completed   bool     `json:"completed"`
	Reason      string   `json:"reason"`
	Error       string   `json:"error"`
}

// startFakeagentTeam creates one tmux session running `count` fakeagent
// panes, titled <session>__cc_1..N so NTM types them as agent panes (the
// bd-h4t0j fixture contract: non-shell foreground + composer glyphs).
func startFakeagentTeam(t *testing.T, count int) (string, []*fakeagentPane) {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available")
	}

	session := fmt.Sprintf("ntm-e2e-wf-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	dir := t.TempDir()

	launch := func(i int) (string, string, string) {
		controlPath := filepath.Join(dir, fmt.Sprintf("control-%d", i))
		logPath := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", i))
		cmd := fmt.Sprintf("%s --persona=claude --control=%s --log=%s",
			tmux.ShellQuote(bin), tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
		return cmd, controlPath, logPath
	}

	// Track which pane ID runs which fixture instance: tmux inserts split
	// panes by position, so launch order and topology order DIVERGE for 3+
	// panes — the control/log paths must be joined on the pane ID tmux
	// reports for each launch, never on enumeration order.
	type fixtureFiles struct{ control, log string }
	filesByPane := make(map[string]fixtureFiles, count)

	cmd0, control0, log0 := launch(1)
	pane0, err := tmux.DefaultClient.Run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session,
		"-x", "200", "-y", "50", cmd0)
	if err != nil {
		t.Fatalf("create fakeagent team session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tmux.DefaultClient.Run("kill-session", "-t", session)
	})
	filesByPane[strings.TrimSpace(pane0)] = fixtureFiles{control: control0, log: log0}

	for i := 2; i <= count; i++ {
		cmdN, controlN, logN := launch(i)
		paneN, err := tmux.DefaultClient.Run("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", session+":0", cmdN)
		if err != nil {
			t.Fatalf("split fakeagent pane %d: %v", i, err)
		}
		// Rebalance so every fakeagent keeps a workable viewport.
		_, _ = tmux.DefaultClient.Run("select-layout", "-t", session+":0", "even-vertical")
		filesByPane[strings.TrimSpace(paneN)] = fixtureFiles{control: controlN, log: logN}
	}

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) != count {
		t.Fatalf("enumerate team panes: err=%v got=%d want=%d", err, len(panes), count)
	}
	panes = tmux.SortPanesByTopology(panes)

	team := make([]*fakeagentPane, 0, count)
	for i, pane := range panes {
		title := fmt.Sprintf("%s__cc_%d", session, i+1)
		if err := tmux.SetPaneTitle(pane.ID, title); err != nil {
			t.Fatalf("title pane %s: %v", pane.ID, err)
		}
		files, ok := filesByPane[pane.ID]
		if !ok {
			t.Fatalf("no fixture files recorded for pane %s (recorded: %v)", pane.ID, filesByPane)
		}
		fp := &fakeagentPane{
			t:           t,
			Session:     session,
			PaneID:      pane.ID,
			Persona:     "claude",
			controlPath: files.control,
			logPath:     files.log,
		}
		if _, ok := fp.WaitForEvent("start", "", 10*time.Second); !ok {
			capture, _ := tmux.CapturePaneOutput(pane.ID, 40)
			t.Fatalf("fakeagent pane %d never started; pane shows:\n%s", i+1, capture)
		}
		team = append(team, fp)
	}
	return session, team
}

// startWorkflowRun launches `ntm workflow run` asynchronously and returns a
// wait function yielding stdout, stderr, and the exit code.
func startWorkflowRun(t *testing.T, logger *TestLogger, args ...string) func() (string, string, int) {
	return startWorkflowRunEnv(t, logger, nil, args...)
}

// startWorkflowRunEnv is startWorkflowRun with extra environment entries
// (e.g. XDG_CONFIG_HOME for user-template resolution).
func startWorkflowRunEnv(t *testing.T, logger *TestLogger, extraEnv []string, args ...string) func() (string, string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ntm %v: %v", args, err)
	}
	logger.Log("[RUN] started: ntm %s (pid %d)", strings.Join(args, " "), cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
		}
	})
	return func() (string, string, int) {
		t.Helper()
		select {
		case err := <-done:
			exit := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exit = ee.ExitCode()
				} else {
					t.Fatalf("wait ntm workflow run: %v", err)
				}
			}
			logger.Log("[RUN] exit=%d", exit)
			logger.Log("[RUN] stdout=%s", strings.TrimSpace(stdout.String()))
			logger.Log("[RUN] stderr=%s", strings.TrimSpace(stderr.String()))
			return stdout.String(), stderr.String(), exit
		case <-time.After(10 * time.Minute):
			_ = cmd.Process.Kill()
			t.Fatalf("ntm workflow run did not exit; stdout=%s stderr=%s", stdout.String(), stderr.String())
			return "", "", -1
		}
	}
}

// mustCapture returns recent pane content for transcript assertions.
func mustCapture(t *testing.T, paneID string) string {
	t.Helper()
	capture, err := tmux.CapturePaneOutput(paneID, 120)
	if err != nil {
		t.Fatalf("capture %s: %v", paneID, err)
	}
	return capture
}

// writeRedGreenProject creates a minimal Go module where `go test ./...`
// passes, so red-green's command_success trigger can fire.
func writeRedGreenProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module demo\n\ngo 1.22\n",
		"demo.go": "package demo\n\n// Answer is the demo implementation the green agent \"writes\".\nfunc Answer() int { return 42 }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// `ntm workflow run red-green` on a live two-pane fakeagent session must
// drive the documented ping-pong red/green turn-taking: the red (tester)
// pane is prompted first; creating a *_test.go file hands off to the green
// (implementer) pane; `go test ./...` passing hands back to red. Both the
// fixture event logs (ground truth submits) and the tmux transcripts must
// show the exchange.
func TestWorkflowRunRedGreenTurnTaking(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-red-green")
	defer logger.Close()

	session, team := startFakeagentTeam(t, 2)
	red, green := team[0], team[1]
	logger.Log("[SETUP] session=%s red=%s green=%s", session, red.PaneID, green.PaneID)

	projectRoot := writeRedGreenProject(t)
	logger.Log("[SETUP] project root=%s", projectRoot)

	wait := startWorkflowRun(t, logger,
		"workflow", "run", "red-green",
		"--session", session,
		"--project-root", projectRoot,
		"--var", "feature=demo Answer function",
		"--max-transitions", "2",
		"--interval", "1s",
		"--trigger-timeout", "60s",
		"--timeout", "5m",
		"--json",
	)

	// Turn 1: the red/tester pane receives the opening stage prompt through
	// the gated dispatch path (submission-verified: the fixture logs a submit).
	turn1, ok := red.WaitForEvent("submit", "stage red turn 1", 90*time.Second)
	if !ok {
		t.Fatalf("red pane never submitted turn 1; events=%+v pane:\n%s", red.Events(), mustCapture(t, red.PaneID))
	}
	logger.LogJSON("red_turn1_submit", turn1)
	if green.CountEvents("submit") != 0 {
		t.Fatalf("green pane acted before the red→green handoff; events=%+v", green.Events())
	}

	// The "tester" finishes its turn: a *_test.go file appears, firing the
	// documented file_created transition red → green.
	testFile := filepath.Join(projectRoot, "demo_test.go")
	testSrc := "package demo\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(\"wrong answer\")\n\t}\n}\n"
	if err := os.WriteFile(testFile, []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	logger.Log("[ACT] created %s to fire file_created(*_test.go)", testFile)

	// Turn 2: the green/implementer pane is engaged.
	turn2, ok := green.WaitForEvent("submit", "stage green turn 2", 120*time.Second)
	if !ok {
		t.Fatalf("green pane never submitted turn 2; events=%+v pane:\n%s", green.Events(), mustCapture(t, green.PaneID))
	}
	logger.LogJSON("green_turn2_submit", turn2)

	// Turn 3: `go test ./...` passes in the project, firing command_success
	// green → red — the ping-pong hands back to the tester.
	turn3, ok := red.WaitForEvent("submit", "stage red turn 3", 120*time.Second)
	if !ok {
		t.Fatalf("red pane never submitted turn 3; events=%+v pane:\n%s", red.Events(), mustCapture(t, red.PaneID))
	}
	logger.LogJSON("red_turn3_submit", turn3)

	// Transcript assertions from the live panes themselves.
	redCapture := mustCapture(t, red.PaneID)
	greenCapture := mustCapture(t, green.PaneID)
	logger.Log("[TRANSCRIPT] red pane:\n%s", redCapture)
	logger.Log("[TRANSCRIPT] green pane:\n%s", greenCapture)
	if !strings.Contains(redCapture, "[ntm workflow red-green] stage red") {
		t.Fatal("red pane transcript missing the red stage prompt")
	}
	if !strings.Contains(greenCapture, "[ntm workflow red-green] stage green") {
		t.Fatal("green pane transcript missing the green stage prompt")
	}
	if strings.Contains(greenCapture, "stage red turn") {
		t.Fatal("green pane transcript shows a red-stage prompt: turn-taking leaked across panes")
	}

	stdout, stderr, exit := wait()
	if exit != 0 {
		t.Fatalf("workflow run exit=%d stderr=%s", exit, stderr)
	}
	var result workflowRunJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("parse result JSON: %v (%s)", err, stdout)
	}
	logger.LogJSON("workflow_run_result", result)
	if !result.Success || result.Workflow != "red-green" || result.Coordination != "ping-pong" {
		t.Fatalf("unexpected result envelope: %+v", result)
	}
	if strings.Join(result.Stages, ",") != "red,green,red" {
		t.Fatalf("stages = %v, want [red green red]", result.Stages)
	}
	if result.Transitions != 2 || result.Reason != "max-transitions" {
		t.Fatalf("transitions=%d reason=%s, want 2/max-transitions", result.Transitions, result.Reason)
	}
	if len(result.Agents) != 2 || result.Agents[0].Role != "red" || result.Agents[1].Role != "green" {
		t.Fatalf("agent mapping = %+v", result.Agents)
	}
	logger.Log("[PASS] red-green ping-pong: red→green→red across live panes, %d verified submits", red.CountEvents("submit")+green.CountEvents("submit"))
}

// waitWorkflowResult waits for the run to exit and decodes the JSON envelope.
func waitWorkflowResult(t *testing.T, logger *TestLogger, wait func() (string, string, int)) workflowRunJSON {
	t.Helper()
	stdout, stderr, exit := wait()
	if exit != 0 {
		t.Fatalf("workflow run exit=%d stderr=%s", exit, stderr)
	}
	var result workflowRunJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("parse result JSON: %v (%s)", err, stdout)
	}
	logger.LogJSON("workflow_run_result", result)
	return result
}

// review-pipeline (review-gate pattern): the author is engaged first, the
// manual submit-for-review handoff engages both reviewers, and a reviewer
// verdict matching the approval pattern moves the flow to its terminal
// complete stage — asserted from fixture submits and pane transcripts.
func TestWorkflowRunReviewPipelineApprovalGate(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-review-pipeline")
	defer logger.Close()

	session, team := startFakeagentTeam(t, 3)
	author, reviewer1, reviewer2 := team[0], team[1], team[2]
	logger.Log("[SETUP] session=%s author=%s reviewers=%s,%s", session, author.PaneID, reviewer1.PaneID, reviewer2.PaneID)

	wait := startWorkflowRun(t, logger,
		"workflow", "run", "review-pipeline",
		"--session", session,
		"--project-root", t.TempDir(),
		"--var", "feature=demo widget",
		"--fire-manual",
		"--max-transitions", "4",
		"--interval", "1s",
		"--timeout", "5m",
		"--json",
	)

	if _, ok := author.WaitForEvent("submit", "stage implement turn 1", 90*time.Second); !ok {
		t.Fatalf("author never received the implement stage; events=%+v pane:\n%s", author.Events(), mustCapture(t, author.PaneID))
	}
	// --fire-manual fires "Submit for review": both reviewers are engaged.
	if _, ok := reviewer1.WaitForEvent("submit", "stage review", 90*time.Second); !ok {
		t.Fatalf("reviewer1 never received the review stage; events=%+v", reviewer1.Events())
	}
	if _, ok := reviewer2.WaitForEvent("submit", "stage review", 90*time.Second); !ok {
		t.Fatalf("reviewer2 never received the review stage; events=%+v", reviewer2.Events())
	}

	// One reviewer issues an approval verdict (approval_mode=any).
	reviewer1.Control("ack lgtm")
	logger.Log("[ACT] reviewer1 emitted approval verdict via ack")

	result := waitWorkflowResult(t, logger, wait)
	if !result.Completed || result.Reason != "completed" {
		t.Fatalf("workflow did not complete via approval: %+v", result)
	}
	if strings.Join(result.Stages, ",") != "implement,review,complete" {
		t.Fatalf("stages = %v, want [implement review complete]", result.Stages)
	}
	if !strings.Contains(mustCapture(t, author.PaneID), "[ntm workflow review-pipeline] stage implement") {
		t.Fatal("author transcript missing the implement stage prompt")
	}
	if !strings.Contains(mustCapture(t, reviewer1.PaneID), "[ntm workflow review-pipeline] stage review") {
		t.Fatal("reviewer transcript missing the review stage prompt")
	}
	logger.Log("[PASS] review-gate: implement → review (both reviewers) → approval → complete")
}

// specialist-team (pipeline pattern): design engages the architect; the
// manual design-approval handoff engages BOTH build implementers
// (parallel_within_stage); the qa pane is never engaged in this bounded run.
func TestWorkflowRunSpecialistTeamPipelineHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-specialist-team")
	defer logger.Close()

	session, team := startFakeagentTeam(t, 4)
	architect, build1, build2, qa := team[0], team[1], team[2], team[3]
	logger.Log("[SETUP] session=%s architect=%s build=%s,%s qa=%s", session, architect.PaneID, build1.PaneID, build2.PaneID, qa.PaneID)

	wait := startWorkflowRun(t, logger,
		"workflow", "run", "specialist-team",
		"--session", session,
		"--project-root", t.TempDir(),
		"--var", "project=demo pipeline",
		"--fire-manual",
		"--max-transitions", "1",
		"--interval", "1s",
		"--timeout", "5m",
		"--json",
	)

	if _, ok := architect.WaitForEvent("submit", "stage design turn 1", 90*time.Second); !ok {
		t.Fatalf("architect never received the design stage; events=%+v pane:\n%s", architect.Events(), mustCapture(t, architect.PaneID))
	}
	if _, ok := build1.WaitForEvent("submit", "stage build", 90*time.Second); !ok {
		t.Fatalf("build1 never received the build stage; events=%+v", build1.Events())
	}
	if _, ok := build2.WaitForEvent("submit", "stage build", 90*time.Second); !ok {
		t.Fatalf("build2 never received the build stage; events=%+v", build2.Events())
	}

	result := waitWorkflowResult(t, logger, wait)
	if result.Reason != "max-transitions" || result.Transitions != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if strings.Join(result.Stages, ",") != "design,build" {
		t.Fatalf("stages = %v, want [design build]", result.Stages)
	}
	if qa.CountEvents("submit") != 0 {
		t.Fatalf("qa pane was engaged before the build→qa trigger; events=%+v", qa.Events())
	}
	if !strings.Contains(mustCapture(t, build2.PaneID), "[ntm workflow specialist-team] stage build") {
		t.Fatal("build transcript missing the build stage prompt")
	}
	logger.Log("[PASS] pipeline: design → (manual approval) → build fan-out, qa untouched")
}

// parallel-explore (parallel pattern): all three approach panes are engaged
// simultaneously and the flowless workflow completes after the fan-out.
func TestWorkflowRunParallelExploreFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-parallel-explore")
	defer logger.Close()

	session, team := startFakeagentTeam(t, 3)
	logger.Log("[SETUP] session=%s panes=%s,%s,%s", session, team[0].PaneID, team[1].PaneID, team[2].PaneID)

	wait := startWorkflowRun(t, logger,
		"workflow", "run", "parallel-explore",
		"--session", session,
		"--project-root", t.TempDir(),
		"--var", "problem=demo search speed",
		"--var", "approach_a=cache layer",
		"--var", "approach_b=index rebuild",
		"--timeout", "5m",
		"--json",
	)

	for i, pane := range team {
		if _, ok := pane.WaitForEvent("submit", "[ntm workflow parallel-explore] stage (parallel)", 90*time.Second); !ok {
			t.Fatalf("approach pane %d never engaged; events=%+v pane:\n%s", i+1, pane.Events(), mustCapture(t, pane.PaneID))
		}
	}

	result := waitWorkflowResult(t, logger, wait)
	if !result.Completed || result.Reason != "completed" || result.Transitions != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Agents) != 3 {
		t.Fatalf("agent mapping = %+v, want 3 roles", result.Agents)
	}
	for _, pane := range team {
		if !strings.Contains(mustCapture(t, pane.PaneID), "[ntm workflow parallel-explore] stage (parallel)") {
			t.Fatalf("pane %s transcript missing the parallel fan-out prompt", pane.PaneID)
		}
	}
	logger.Log("[PASS] parallel: three approaches engaged simultaneously, run completed")
}

const customRelayTOML = `name = "custom-relay"
description = "user-defined relay workflow"
coordination = "ping-pong"

[[agents]]
profile = "claude"
role = "left"

[[agents]]
profile = "claude"
role = "right"

[flow]
initial = "left"

[[flow.transitions]]
from = "left"
to = "right"
[flow.transitions.trigger]
type = "agent_says"
pattern = "LEFT-BATON"
role = "left"

[[flow.transitions]]
from = "right"
to = "left"
[flow.transitions.trigger]
type = "agent_says"
pattern = "RIGHT-BATON"
role = "right"
`

// runCustomRelay drives one custom-relay run (already started) to a single
// left→right handoff and asserts it from the fixture logs.
func runCustomRelay(t *testing.T, logger *TestLogger, left, right *fakeagentPane, wait func() (string, string, int)) {
	t.Helper()
	if _, ok := left.WaitForEvent("submit", "stage left turn 1", 90*time.Second); !ok {
		t.Fatalf("left pane never engaged; events=%+v pane:\n%s", left.Events(), mustCapture(t, left.PaneID))
	}
	left.Control("ack LEFT-BATON")
	logger.Log("[ACT] left pane passed the baton")
	if _, ok := right.WaitForEvent("submit", "stage right turn 2", 120*time.Second); !ok {
		t.Fatalf("right pane never engaged; events=%+v pane:\n%s", right.Events(), mustCapture(t, right.PaneID))
	}
	result := waitWorkflowResult(t, logger, wait)
	if result.Workflow != "custom-relay" || result.Transitions != 1 || strings.Join(result.Stages, ",") != "left,right" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.Contains(mustCapture(t, right.PaneID), "[ntm workflow custom-relay] stage right") {
		t.Fatal("right pane transcript missing the custom workflow prompt")
	}
}

// User-supplied TOML must be runnable BOTH ways the docs describe: by
// explicit path, and by bare name from the documented user search directory
// (~/.config/ntm/workflows/, honoring XDG_CONFIG_HOME) — each proven by a
// live coordination handoff, not just resolution.
func TestWorkflowRunUserTOMLByPathAndByName(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-user-toml")
	defer logger.Close()

	tomlDir := t.TempDir()
	tomlPath := filepath.Join(tomlDir, "custom-relay.toml")
	if err := os.WriteFile(tomlPath, []byte(customRelayTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run 1: by explicit path.
	session1, team1 := startFakeagentTeam(t, 2)
	logger.Log("[SETUP] by-path session=%s toml=%s", session1, tomlPath)
	wait1 := startWorkflowRun(t, logger,
		"workflow", "run", tomlPath,
		"--session", session1,
		"--project-root", t.TempDir(),
		"--max-transitions", "1",
		"--interval", "1s",
		"--timeout", "5m",
		"--json",
	)
	runCustomRelay(t, logger, team1[0], team1[1], wait1)
	logger.Log("[PASS] user TOML by path executed a live left→right handoff")

	// Run 2: by bare name from the documented user config search dir.
	xdgHome := t.TempDir()
	userWorkflows := filepath.Join(xdgHome, "ntm", "workflows")
	if err := os.MkdirAll(userWorkflows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userWorkflows, "custom-relay.toml"), []byte(customRelayTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	session2, team2 := startFakeagentTeam(t, 2)
	logger.Log("[SETUP] by-name session=%s XDG_CONFIG_HOME=%s", session2, xdgHome)
	wait2 := startWorkflowRunEnv(t, logger, []string{"XDG_CONFIG_HOME=" + xdgHome},
		"workflow", "run", "custom-relay",
		"--session", session2,
		"--project-root", t.TempDir(),
		"--max-transitions", "1",
		"--interval", "1s",
		"--timeout", "5m",
		"--json",
	)
	runCustomRelay(t, logger, team2[0], team2[1], wait2)
	logger.Log("[PASS] user TOML by bare name executed a live left→right handoff")
}

// An unknown workflow reference must fail with the documented not-found
// error listing every builtin name — no silent fallback, no dispatch.
func TestWorkflowRunNotFoundListsBuiltins(t *testing.T) {
	if testing.Short() {
		t.Skip("live E2E skipped in -short")
	}
	logger := NewTestLogger(t, "workflow-run-not-found")
	defer logger.Close()

	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, "workflow", "run", "definitely-missing-flow")
	out, runErr := cmd.CombinedOutput()
	logger.Log("[RUN] output=%s", strings.TrimSpace(string(out)))
	if runErr == nil {
		t.Fatalf("unknown workflow must exit non-zero; output=%s", out)
	}
	text := string(out)
	if !strings.Contains(text, "not found") {
		t.Fatalf("missing not-found message: %s", text)
	}
	for _, name := range []string{"red-green", "review-pipeline", "specialist-team", "parallel-explore"} {
		if !strings.Contains(text, name) {
			t.Fatalf("not-found error must list builtin %q: %s", name, text)
		}
	}
	logger.Log("[PASS] not-found error lists all four builtins and fails closed")
}
