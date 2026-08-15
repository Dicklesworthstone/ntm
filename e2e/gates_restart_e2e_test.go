//go:build e2e
// +build e2e

package e2e

// gates_restart_e2e_test.go (bd-y0l7u) proves the v1.23.0 interactive-gate /
// blocked-health / smart-restart-decline / restart-prompt-gating stack against
// a REAL tmux server and the runnable fakeagent fixture (bd-kur07). Every
// scenario asserts from the robot envelope PLUS pane/fixture ground truth
// (fixture JSONL event log, tmux pane captures, PID lineage), never from the
// envelope alone.
//
// Scenario -> test map:
//  1. Gate detection across surfaces (+ quoted-phrase negative):
//     TestGatesE2EGateDetectionAcrossHealthSurfaces
//  2. Auto-restart refusal (gated pane never restarted):
//     TestGatesE2EHealthRestartStuckLeavesGatedPaneUntouched
//  3a. Smart-restart SKIPPED + prompt not_attempted on a gated pane:
//     TestGatesE2ESmartRestartSkipsGatedPaneWithPromptNotAttempted
//  3b. Smart-restart exit-sequence failure (SHELL_NOT_RETURNED):
//     TestGatesE2ESmartRestartExitSequenceUnconfirmed
//  4. Restart prompt gating (rf0ka), delivered + withheld paths:
//     TestGatesE2ERestartPanePromptDeliveredAfterReadiness
//     TestGatesE2ERestartPanePromptWithheldWhenShellStaysForeground
//  5. Rate-limit precedence over gate flagging:
//     TestGatesE2ERateLimitPrecedenceOverGateFlagging
//
// Scenario 3c (unknown-state skip with probe detail in the reason) could not
// be constructed honestly with the fixture: manufacturing a pane the probe
// classifies as UNKNOWN while it still resolves as an agent pane requires an
// unparseable-yet-agent-typed frame the fixture cannot render without lying
// about what a real agent shows. The decideRestart branch is unit-tested in
// internal/robot/smart_restart_test.go; here we cover the gated-skip and
// exit-failure branches live and note the gap.
//
// The shared fixture harness (fakeagent_harness_test.go) launches fakeagent as
// the pane's direct command, which leaves the pane with no shell parent. The
// smart-restart shell-return confirmation and the health process checks key on
// production topology (pane_pid = shell, agent = child), so this file carries
// a local workaround, startGatesFakeagentPane, which starts the pane on its
// default shell and types the fixture launch command into it — without
// touching the frozen shared files.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Envelope shapes (local to this suite; field names mirror internal/robot)
// =============================================================================

type gatesIsWorkingPane struct {
	AgentType            string `json:"agent_type"`
	IsWorking            bool   `json:"is_working"`
	IsIdle               bool   `json:"is_idle"`
	IsRateLimited        bool   `json:"is_rate_limited"`
	IndicatorBasis       string `json:"indicator_basis"`
	Recommendation       string `json:"recommendation"`
	RecommendationReason string `json:"recommendation_reason"`
	PanePID              int    `json:"pane_pid"`
}

type gatesIsWorkingEnvelope struct {
	Success bool                          `json:"success"`
	Session string                        `json:"session"`
	Panes   map[string]gatesIsWorkingPane `json:"panes"`
	Summary struct {
		RateLimitedCount int                 `json:"rate_limited_count"`
		ByRecommendation map[string][]string `json:"by_recommendation"`
	} `json:"summary"`
}

type gatesAgentHealthPane struct {
	AgentType            string `json:"agent_type"`
	Recommendation       string `json:"recommendation"`
	RecommendationReason string `json:"recommendation_reason"`
}

type gatesAgentHealthEnvelope struct {
	Success bool                            `json:"success"`
	Panes   map[string]gatesAgentHealthPane `json:"panes"`
}

type gatesSessionHealthAgent struct {
	Pane      int    `json:"pane"`
	AgentType string `json:"agent_type"`
	Health    string `json:"health"`
	LastError string `json:"last_error"`
}

type gatesSessionHealthEnvelope struct {
	Success bool                      `json:"success"`
	Session string                    `json:"session"`
	Agents  []gatesSessionHealthAgent `json:"agents"`
}

type gatesRestartStuckEnvelope struct {
	Success    bool   `json:"success"`
	ErrorCode  string `json:"error_code"`
	StuckPanes []int  `json:"stuck_panes"`
	Restarted  []int  `json:"restarted"`
	Failed     []int  `json:"failed"`
	Threshold  string `json:"threshold"`
}

type gatesPromptOutcome struct {
	Requested bool   `json:"requested"`
	Status    string `json:"status"`
}

type gatesRestartSequence struct {
	ExitMethod      string `json:"exit_method"`
	ShellConfirmed  bool   `json:"shell_confirmed"`
	LaunchAttempted bool   `json:"launch_attempted"`
	AgentLaunched   bool   `json:"agent_launched"`
}

type gatesStructuredError struct {
	Code  string `json:"code"`
	Phase string `json:"phase"`
}

type gatesSmartRestartAction struct {
	Action          string                `json:"action"`
	Reason          string                `json:"reason"`
	Warning         string                `json:"warning"`
	Error           string                `json:"error"`
	RestartSequence *gatesRestartSequence `json:"restart_sequence"`
	PromptOutcome   *gatesPromptOutcome   `json:"prompt_outcome"`
	StructuredError *gatesStructuredError `json:"structured_error"`
}

type gatesSmartRestartEnvelope struct {
	Success   bool                               `json:"success"`
	ErrorCode string                             `json:"error_code"`
	Actions   map[string]gatesSmartRestartAction `json:"actions"`
	Summary   struct {
		Restarted     int                 `json:"restarted"`
		Skipped       int                 `json:"skipped"`
		Failed        int                 `json:"failed"`
		PanesByAction map[string][]string `json:"panes_by_action"`
	} `json:"summary"`
}

type gatesRestartPanePIDs struct {
	Before int `json:"before"`
	After  int `json:"after"`
}

type gatesRestartPaneFailure struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

type gatesRestartPaneEnvelope struct {
	Success             bool                            `json:"success"`
	Error               string                          `json:"error"`
	ErrorCode           string                          `json:"error_code"`
	Restarted           []string                        `json:"restarted"`
	Failed              []gatesRestartPaneFailure       `json:"failed"`
	PromptSent          bool                            `json:"prompt_sent"`
	PromptError         string                          `json:"prompt_error"`
	PromptDelivery      map[string]string               `json:"prompt_delivery"`
	AgentRelaunched     map[string]bool                 `json:"agent_relaunched"`
	AgentRelaunchStatus map[string]string               `json:"agent_relaunch_status"`
	PaneShellPIDs       map[string]gatesRestartPanePIDs `json:"pane_shell_pids"`
}

// =============================================================================
// Local harness helpers (workarounds live here, not in the frozen files)
// =============================================================================

// startGatesFakeagentPane creates an isolated session whose single pane runs
// a NON-INTERACTIVE shell that forks the fakeagent as its child (the trailing
// `sleep` forces the fork instead of an exec). This gives production topology
// — pane_pid is a shell, the agent is its child — which smart-restart's
// shell-return confirmation and the health process checks key on, while
// avoiding an interactive shell's job control, which would steal the terminal
// (and corrupt its termios state) when a test quiesces the fixture with
// SIGSTOP.
func startGatesFakeagentPane(t *testing.T, logger *TestLogger, persona string) *fakeagentPane {
	t.Helper()
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available, skipping gates E2E test")
	}
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}

	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")
	session := fmt.Sprintf("ntm-e2e-gates-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	title := session + "__cc_1"
	if persona == "codex" {
		title = session + "__cod_1"
	}

	launch := fmt.Sprintf("%s --persona=%s --control=%s --log=%s; sleep 86400",
		tmux.ShellQuote(bin), persona, tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", session,
		"-x", "120", "-y", "40", launch); err != nil {
		t.Fatalf("create gates session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tmux.DefaultClient.Run("kill-session", "-t", session)
	})

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("enumerate gates panes: err=%v panes=%d", err, len(panes))
	}
	paneID := panes[0].ID
	shellPID := panes[0].PID

	pane := &fakeagentPane{
		t:           t,
		Session:     session,
		PaneID:      paneID,
		Persona:     persona,
		controlPath: controlPath,
		logPath:     logPath,
	}
	if _, ok := pane.WaitForEvent("start", "", 60*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(paneID, 40)
		t.Fatalf("fakeagent did not start under the pane shell; pane shows:\n%s", capture)
	}
	if err := tmux.SetPaneTitle(paneID, title); err != nil {
		t.Fatalf("title gates pane: %v", err)
	}

	if logger != nil {
		logger.Log("[PID-LINEAGE] session=%s pane=%s shell_pid=%d fixture_pid=%d persona=%s",
			session, paneID, shellPID, gatesFixturePID(pane), persona)
	}
	return pane
}

// startGatesRespawnableFakeagentPane creates a session whose pane behaves
// like an ntm-spawned pane for RESTART purposes: the pane's creation command
// is a plain interactive shell — so `respawn-pane -k` (what --robot-restart-pane
// uses) lands back in a shell exactly as production panes do — and the fixture
// is launched by typing into that shell, leaving it the shell's foreground
// job. The shell is bash --noprofile --norc: deterministic startup with no
// prompt frameworks that could swallow the relaunch keystrokes ntm types into
// the respawned pane. (The plain startGatesFakeagentPane creates the pane WITH
// the fixture command, which respawn-pane would faithfully re-run — turning a
// restart test into a fixture-resurrection test.)
func startGatesRespawnableFakeagentPane(t *testing.T, logger *TestLogger, persona string) *fakeagentPane {
	t.Helper()
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available, skipping gates E2E test")
	}
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}

	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")
	session := fmt.Sprintf("ntm-e2e-gates-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	title := session + "__cc_1"
	if persona == "codex" {
		title = session + "__cod_1"
	}

	if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", session,
		"-x", "120", "-y", "40"); err != nil {
		t.Fatalf("create gates session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tmux.DefaultClient.Run("kill-session", "-t", session)
	})
	if _, err := tmux.DefaultClient.Run("set-option", "-t", session,
		"default-command", "/bin/bash --noprofile --norc"); err != nil {
		t.Fatalf("pin session default-command: %v", err)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("enumerate gates panes: err=%v panes=%d", err, len(panes))
	}
	paneID := panes[0].ID

	// Swap the user's login shell for the pinned plain bash, then launch the
	// fixture as that shell's foreground job.
	if _, err := tmux.DefaultClient.Run("respawn-pane", "-k", "-t", paneID); err != nil {
		t.Fatalf("respawn pane onto plain bash: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	launch := fmt.Sprintf("%s --persona=%s --control=%s --log=%s",
		tmux.ShellQuote(bin), persona, tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("send-keys", "-t", paneID, launch, "Enter"); err != nil {
		t.Fatalf("type fakeagent launch into pane shell: %v", err)
	}

	pane := &fakeagentPane{
		t:           t,
		Session:     session,
		PaneID:      paneID,
		Persona:     persona,
		controlPath: controlPath,
		logPath:     logPath,
	}
	if _, ok := pane.WaitForEvent("start", "", 60*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(paneID, 40)
		t.Fatalf("fakeagent did not start under the pane shell; pane shows:\n%s", capture)
	}
	if err := tmux.SetPaneTitle(paneID, title); err != nil {
		t.Fatalf("title gates pane: %v", err)
	}
	if logger != nil {
		logger.Log("[PID-LINEAGE] session=%s pane=%s shell_pid=%d fixture_pid=%d persona=%s (respawnable)",
			session, paneID, gatesShellPID(t, session), gatesFixturePID(pane), persona)
	}
	return pane
}

// gatesRunNTM runs the freshly built ntm binary with optional extra
// environment entries ("KEY=VALUE"), logging args, exit code, and output.
func gatesRunNTM(t *testing.T, logger *TestLogger, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	started := time.Now()
	cmd := exec.Command(bin, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run ntm %v: %v", args, err)
		}
	}
	if logger != nil {
		logger.Log("[NTM] args=%v exit=%d elapsed=%s", args, exit, time.Since(started).Round(time.Millisecond))
		logger.Log("[NTM] output=%s", strings.TrimSpace(string(out)))
	}
	return string(out), exit
}

// gatesDumpDiagnostics captures the pane and dumps the fixture event log when
// the test failed — call via defer right after the pane exists.
func gatesDumpDiagnostics(t *testing.T, logger *TestLogger, pane *fakeagentPane) {
	if !t.Failed() {
		return
	}
	capture, _ := tmux.CapturePaneOutput(pane.PaneID, 60)
	logger.Log("[FAIL-DIAG] pane %s capture:\n%s", pane.PaneID, capture)
	logger.LogJSON("fail_diag_fixture_events", pane.Events())
}

// gatesFixturePID returns the fixture process PID recorded in its "start"
// event (the fixture logs os.Getpid() as N).
func gatesFixturePID(pane *fakeagentPane) int {
	for _, ev := range pane.Events() {
		if ev.Event == "start" {
			return ev.N
		}
	}
	return 0
}

// gatesShellPID re-reads the pane's current shell PID from tmux.
func gatesShellPID(t *testing.T, session string) int {
	t.Helper()
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("re-enumerate panes for %s: err=%v panes=%d", session, err, len(panes))
	}
	return panes[0].PID
}

// gatesKeyEventCount counts fixture "key" events — every keystroke the
// fixture received. Ground truth that a surface sent zero keys.
func gatesKeyEventCount(pane *fakeagentPane) int {
	count := 0
	for _, ev := range pane.Events() {
		if ev.Event == "key" {
			count++
		}
	}
	return count
}

// gatesFixtureAlive proves the fixture process is still running by watching
// its event log grow (it logs a render event every 500ms), avoiding
// platform-specific signal probes.
func gatesFixtureAlive(pane *fakeagentPane) bool {
	before := len(pane.Events())
	time.Sleep(1500 * time.Millisecond)
	return len(pane.Events()) > before
}

// gatesSolePane returns the single entry of a per-pane envelope map.
func gatesSolePane[V any](t *testing.T, panes map[string]V) (string, V) {
	t.Helper()
	if len(panes) != 1 {
		t.Fatalf("expected exactly 1 pane entry, got %d: %v", len(panes), panes)
	}
	for k, v := range panes {
		return k, v
	}
	var zero V
	return "", zero
}

func gatesDecode(t *testing.T, label, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("parse %s envelope: %v (%s)", label, err, raw)
	}
}

// gatesInjectTranscript lands a line of text in the fixture transcript via
// the fixture's own "ack" verb, waiting for the ack_emit event (ground truth
// the text is in the pane). Direct --robot-send delivery into fakeagent panes
// is already proven live by the frozen fakeagent_harness_test.go suite; this
// suite's shell-hosted panes report the host shell as the tty foreground
// process-group leader, which robot-send's dead-agent gate rightly refuses,
// so transcript content is arranged fixture-side instead.
func gatesInjectTranscript(t *testing.T, pane *fakeagentPane, text string) {
	t.Helper()
	pane.Control("ack " + text)
	if _, ok := pane.WaitForEvent("ack_emit", text[:min(len(text), 40)], 10*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
		t.Fatalf("fixture never emitted transcript text %q; events=%+v pane:\n%s",
			text, pane.Events(), capture)
	}
	time.Sleep(600 * time.Millisecond) // let the repaint land
}

const gatesTrustPhrase = "do you trust the contents of this project"

// gatesQuiesceFixture stops the fixture's cosmetic repaint loop with SIGSTOP
// and waits until the pane's activity timestamp is stale. The fixture repaints
// every 500ms even when idle/gated — a fixture artifact: a real agent's modal
// gate paints once and then waits. The status detector's velocity heuristic
// (ActivityThreshold, 5s) would otherwise classify the static gate screen as
// "producing output" forever. The returned func resumes the fixture; it is
// also registered as a cleanup.
func gatesQuiesceFixture(t *testing.T, logger *TestLogger, pane *fakeagentPane) func() {
	t.Helper()
	pid := gatesFixturePID(pane)
	if pid <= 0 {
		t.Fatal("fixture PID unknown; cannot quiesce")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find fixture process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP fixture %d: %v", pid, err)
	}
	if logger != nil {
		logger.Log("[QUIESCE] SIGSTOP fixture pid=%d; waiting out the 5s activity threshold", pid)
	}
	time.Sleep(6500 * time.Millisecond)
	resumed := false
	resume := func() {
		if resumed {
			return
		}
		resumed = true
		// The pane shell is non-interactive (no job control), so it never
		// steals the terminal while the fixture is stopped and a plain
		// SIGCONT resumes it cleanly with its raw tty state intact.
		_ = proc.Signal(syscall.SIGCONT)
	}
	t.Cleanup(resume)
	return resume
}

// gatesArmTrustGate raises the trust gate and waits until the gate screen is
// actually painted (render events carry the gate name as data).
func gatesArmTrustGate(t *testing.T, pane *fakeagentPane) {
	t.Helper()
	pane.Control("gate trust")
	if _, ok := pane.WaitForEvent("render", "trust", 5*time.Second); !ok {
		t.Fatalf("gate screen never rendered; events=%+v", pane.Events())
	}
	// One extra render cycle so the pane content tmux captures is the gate.
	time.Sleep(600 * time.Millisecond)
}

// =============================================================================
// Scenario 1 — gate detection end-to-end across surfaces, plus the
// quoted-phrase-under-work-chrome negative.
// =============================================================================

func TestGatesE2EGateDetectionAcrossHealthSurfaces(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-detection")
	defer logger.Close()

	pane := startGatesFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)
	scenarioStart := time.Now()

	gatesArmTrustGate(t, pane)
	logger.Log("[SETUP] trust gate armed on %s", pane.PaneID)
	resumeFixture := gatesQuiesceFixture(t, logger, pane)

	// --- (a) --robot-is-working -------------------------------------------
	out, exit := gatesRunNTM(t, logger, nil, "--robot-is-working="+pane.Session)
	if exit != 0 {
		t.Fatalf("robot-is-working exit=%d output=%s", exit, out)
	}
	var isWorking gatesIsWorkingEnvelope
	gatesDecode(t, "is-working", out, &isWorking)
	logger.LogJSON("is_working_gated", isWorking)
	paneKey, work := gatesSolePane(t, isWorking.Panes)
	if work.Recommendation != "MANUAL_INTERVENTION" {
		t.Fatalf("gated pane recommendation = %q, want MANUAL_INTERVENTION (reason %q)",
			work.Recommendation, work.RecommendationReason)
	}
	if work.IndicatorBasis != "interactive_gate" {
		t.Fatalf("gated pane indicator_basis = %q, want interactive_gate", work.IndicatorBasis)
	}
	if !strings.Contains(strings.ToLower(work.RecommendationReason), gatesTrustPhrase) {
		t.Fatalf("recommendation_reason %q does not carry the gate text %q",
			work.RecommendationReason, gatesTrustPhrase)
	}
	if !strings.Contains(work.RecommendationReason, "needs a keystroke") {
		t.Fatalf("recommendation_reason %q does not mention the keystroke requirement", work.RecommendationReason)
	}
	if bucket := isWorking.Summary.ByRecommendation["MANUAL_INTERVENTION"]; len(bucket) != 1 || bucket[0] != paneKey {
		t.Fatalf("summary.by_recommendation MANUAL_INTERVENTION = %v, want [%s]", bucket, paneKey)
	}
	logger.Log("[PID-LINEAGE] is-working reports pane_pid=%d (tmux shell pid=%d)",
		work.PanePID, gatesShellPID(t, pane.Session))

	// Ground truth: the gate question is really on screen.
	capture, err := tmux.CapturePaneOutput(pane.PaneID, 40)
	if err != nil {
		t.Fatalf("capture gated pane: %v", err)
	}
	if !strings.Contains(strings.ToLower(capture), gatesTrustPhrase) {
		t.Fatalf("pane capture does not show the trust gate:\n%s", capture)
	}

	// --- (b) --robot-agent-health -----------------------------------------
	out, _ = gatesRunNTM(t, logger, nil, "--robot-agent-health="+pane.Session, "--no-caut")
	var agentHealth gatesAgentHealthEnvelope
	gatesDecode(t, "agent-health", out, &agentHealth)
	logger.LogJSON("agent_health_gated", agentHealth)
	_, health := gatesSolePane(t, agentHealth.Panes)
	if health.Recommendation != "MANUAL_INTERVENTION" {
		t.Fatalf("agent-health recommendation = %q, want MANUAL_INTERVENTION (reason %q)",
			health.Recommendation, health.RecommendationReason)
	}
	if !strings.Contains(strings.ToLower(health.RecommendationReason), gatesTrustPhrase) {
		t.Fatalf("agent-health reason %q does not carry the gate text", health.RecommendationReason)
	}

	// --- (c) --robot-health=SESSION (session health surface) ---------------
	out, _ = gatesRunNTM(t, logger, nil, "--robot-health="+pane.Session)
	var sessionHealth gatesSessionHealthEnvelope
	gatesDecode(t, "session-health", out, &sessionHealth)
	logger.LogJSON("session_health_gated", sessionHealth)
	if !sessionHealth.Success || len(sessionHealth.Agents) != 1 {
		t.Fatalf("session health envelope unexpected: %s", out)
	}
	blockedAgent := sessionHealth.Agents[0]
	if blockedAgent.Health != "blocked" {
		t.Fatalf("session health agent health = %q, want blocked (last_error %q)",
			blockedAgent.Health, blockedAgent.LastError)
	}
	if !strings.Contains(blockedAgent.LastError, "interactive gate") ||
		!strings.Contains(blockedAgent.LastError, "needs a keystroke") {
		t.Fatalf("session health last_error %q does not explain the gate", blockedAgent.LastError)
	}
	logger.Log("[TIMING] gated-surface assertions done in %s", time.Since(scenarioStart).Round(time.Millisecond))

	// --- Negative: quoted gate phrase in the transcript while WORK chrome is
	// live must NOT flag. Dismiss the gate (Enter confirms), submit a message
	// that QUOTES the phrase, then raise live working chrome.
	negativeStart := time.Now()
	resumeFixture()
	if _, err := tmux.DefaultClient.Run("send-keys", "-t", pane.PaneID, "Enter"); err != nil {
		t.Fatalf("dismiss gate: %v", err)
	}
	if _, ok := pane.WaitForEvent("key", "enter", 5*time.Second); !ok {
		t.Fatalf("gate-dismiss Enter never reached the fixture; events=%+v", pane.Events())
	}

	quote := "for the record, the tool earlier asked: " + gatesTrustPhrase + " (quoting only, no gate is up)"
	gatesInjectTranscript(t, pane, quote)
	pane.Control("work 60")
	if _, ok := pane.WaitForEvent("control", "work 60", 5*time.Second); !ok {
		t.Fatalf("work verb never consumed; events=%+v", pane.Events())
	}
	time.Sleep(800 * time.Millisecond) // let the working chrome paint

	capture, err = tmux.CapturePaneOutput(pane.PaneID, 40)
	if err != nil {
		t.Fatalf("capture working pane: %v", err)
	}
	lower := strings.ToLower(capture)
	if !strings.Contains(lower, gatesTrustPhrase) {
		t.Fatalf("negative setup broken: quoted phrase not visible in pane tail:\n%s", capture)
	}
	if !strings.Contains(lower, "esc to interrupt") {
		t.Fatalf("negative setup broken: working chrome not visible in pane tail:\n%s", capture)
	}

	out, exit = gatesRunNTM(t, logger, nil, "--robot-is-working="+pane.Session)
	if exit != 0 {
		t.Fatalf("robot-is-working (negative) exit=%d output=%s", exit, out)
	}
	var negative gatesIsWorkingEnvelope
	gatesDecode(t, "is-working-negative", out, &negative)
	logger.LogJSON("is_working_quoted_phrase_under_work_chrome", negative)
	_, negWork := gatesSolePane(t, negative.Panes)
	if negWork.Recommendation == "MANUAL_INTERVENTION" {
		t.Fatalf("quoted gate phrase under live work chrome was flagged: %+v", negWork)
	}
	if negWork.IndicatorBasis == "interactive_gate" {
		t.Fatalf("indicator_basis = interactive_gate for a quoted phrase: %+v", negWork)
	}
	if !negWork.IsWorking {
		t.Fatalf("negative pane should be classified working (chrome live): %+v", negWork)
	}
	logger.Log("[TIMING] negative case done in %s", time.Since(negativeStart).Round(time.Millisecond))
	logger.Log("[PASS] gate flagged on all three surfaces; quoted phrase under work chrome not flagged")
}

// =============================================================================
// Scenario 2 — auto-restart surface must leave the gated pane untouched.
// =============================================================================

func TestGatesE2EHealthRestartStuckLeavesGatedPaneUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-restart-stuck-refusal")
	defer logger.Close()

	pane := startGatesFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)
	gatesArmTrustGate(t, pane)
	resumeFixture := gatesQuiesceFixture(t, logger, pane)

	shellPIDBefore := gatesShellPID(t, pane.Session)
	fixturePID := gatesFixturePID(pane)
	keysBefore := gatesKeyEventCount(pane)
	logger.Log("[PID-LINEAGE] before restart-stuck: shell_pid=%d fixture_pid=%d key_events=%d",
		shellPIDBefore, fixturePID, keysBefore)

	// The health engine's refusal contract: the pane is blocked, and the reason
	// carries the "needs a keystroke" explanation that shouldAutoRestartHealthState
	// keys on. This is the same HealthBlocked state the restart engine consults.
	out, _ := gatesRunNTM(t, logger, nil, "--robot-health="+pane.Session)
	var sessionHealth gatesSessionHealthEnvelope
	gatesDecode(t, "session-health", out, &sessionHealth)
	if len(sessionHealth.Agents) != 1 || sessionHealth.Agents[0].Health != "blocked" {
		t.Fatalf("expected blocked agent before restart-stuck: %s", out)
	}
	if !strings.Contains(sessionHealth.Agents[0].LastError, "needs a keystroke") {
		t.Fatalf("blocked reason %q does not mention the keystroke", sessionHealth.Agents[0].LastError)
	}

	// Run the auto-restart surface (default 5m threshold). The gated pane
	// must NOT be restarted.
	out, exit := gatesRunNTM(t, logger, nil,
		"--robot-health-restart-stuck="+pane.Session)
	if exit != 0 {
		t.Fatalf("robot-health-restart-stuck exit=%d output=%s", exit, out)
	}
	var stuck gatesRestartStuckEnvelope
	gatesDecode(t, "restart-stuck", out, &stuck)
	logger.LogJSON("restart_stuck_gated", stuck)
	if !stuck.Success {
		t.Fatalf("restart-stuck reported failure: %s", out)
	}
	if len(stuck.Restarted) != 0 || len(stuck.Failed) != 0 {
		t.Fatalf("gated pane was touched by restart-stuck: restarted=%v failed=%v",
			stuck.Restarted, stuck.Failed)
	}

	// Ground truth: same shell PID, fixture still alive and rendering, gate
	// still on screen, zero keystrokes delivered.
	resumeFixture()
	shellPIDAfter := gatesShellPID(t, pane.Session)
	if shellPIDAfter != shellPIDBefore {
		t.Fatalf("pane shell PID changed %d -> %d; the pane was respawned", shellPIDBefore, shellPIDAfter)
	}
	if !gatesFixtureAlive(pane) {
		t.Fatalf("fixture stopped rendering after restart-stuck; events=%+v", pane.Events())
	}
	if pid := gatesFixturePID(pane); pid != fixturePID {
		t.Fatalf("fixture PID changed %d -> %d", fixturePID, pid)
	}
	capture, err := tmux.CapturePaneOutput(pane.PaneID, 40)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}
	if !strings.Contains(strings.ToLower(capture), gatesTrustPhrase) {
		t.Fatalf("trust gate no longer on screen after restart-stuck:\n%s", capture)
	}
	if keysAfter := gatesKeyEventCount(pane); keysAfter != keysBefore {
		t.Fatalf("restart-stuck delivered keystrokes to the gated pane: %d -> %d", keysBefore, keysAfter)
	}
	logger.Log("[PID-LINEAGE] after restart-stuck: shell_pid=%d fixture_pid=%d (unchanged)",
		shellPIDAfter, fixturePID)

	// GAP (reported, not asserted): AutoRestartUnhealthyAgent's explicit
	// refusal reason ("a restart cannot answer it") is not reachable from any
	// CLI surface — internal/robot/health.go GetAutoRestartStuck classifies by
	// idle time only and never consults shouldAutoRestartHealthState, so the
	// blocked pane is protected here only because its constant repaints keep
	// IdleSinceSeconds ~0. The refusal-reason contract itself is covered by
	// the smart-restart surface (scenario 3a).
	logger.Log("[GAP] restart-stuck surface exposes no per-pane refusal reason; see test comment")
	logger.Log("[PASS] gated pane untouched by --robot-health-restart-stuck")
}

// =============================================================================
// Scenario 3a — smart-restart SKIPPED on a gated pane, prompt not_attempted,
// zero keystrokes.
// =============================================================================

func TestGatesE2ESmartRestartSkipsGatedPaneWithPromptNotAttempted(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-smart-restart-skip")
	defer logger.Close()

	pane := startGatesFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)
	gatesArmTrustGate(t, pane)
	resumeFixture := gatesQuiesceFixture(t, logger, pane)

	shellPIDBefore := gatesShellPID(t, pane.Session)
	keysBefore := gatesKeyEventCount(pane)

	out, exit := gatesRunNTM(t, logger, nil,
		"--robot-smart-restart="+pane.Session, "--prompt=gates-e2e post-restart prompt")
	if exit != 0 {
		t.Fatalf("robot-smart-restart exit=%d output=%s", exit, out)
	}
	var smart gatesSmartRestartEnvelope
	gatesDecode(t, "smart-restart", out, &smart)
	logger.LogJSON("smart_restart_gated_skip", smart)
	if !smart.Success {
		t.Fatalf("all-skipped smart-restart should keep success:true: %s", out)
	}
	paneKey, action := gatesSolePane(t, smart.Actions)
	if action.Action != "SKIPPED" {
		t.Fatalf("gated pane action = %q, want SKIPPED (reason %q)", action.Action, action.Reason)
	}
	if !strings.Contains(action.Reason, "Blocked on an interactive gate screen") ||
		!strings.Contains(action.Reason, "a human keystroke can") {
		t.Fatalf("skip reason %q is not the MANUAL_INTERVENTION gate decline", action.Reason)
	}
	if !strings.Contains(action.Reason, "requested prompt was NOT delivered") {
		t.Fatalf("skip reason %q does not state the prompt was withheld", action.Reason)
	}
	if action.PromptOutcome == nil || !action.PromptOutcome.Requested ||
		action.PromptOutcome.Status != "not_attempted" {
		t.Fatalf("prompt_outcome = %+v, want requested=true status=not_attempted", action.PromptOutcome)
	}
	if smart.Summary.Skipped != 1 {
		t.Fatalf("summary.skipped = %d, want 1", smart.Summary.Skipped)
	}
	if bucket := smart.Summary.PanesByAction["SKIPPED"]; len(bucket) != 1 || bucket[0] != paneKey {
		t.Fatalf("panes_by_action SKIPPED = %v, want [%s]", bucket, paneKey)
	}

	// Ground truth: zero new key events (no exit sequence, no prompt), same
	// shell PID, fixture alive, gate still up.
	resumeFixture()
	if keysAfter := gatesKeyEventCount(pane); keysAfter != keysBefore {
		t.Fatalf("smart-restart sent keystrokes to a SKIPPED pane: %d -> %d", keysBefore, keysAfter)
	}
	if pid := gatesShellPID(t, pane.Session); pid != shellPIDBefore {
		t.Fatalf("shell PID changed %d -> %d on a SKIPPED pane", shellPIDBefore, pid)
	}
	if !gatesFixtureAlive(pane) {
		t.Fatal("fixture stopped rendering after a SKIPPED smart-restart")
	}
	capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
	if !strings.Contains(strings.ToLower(capture), gatesTrustPhrase) {
		t.Fatalf("gate vanished after SKIPPED smart-restart:\n%s", capture)
	}
	logger.Log("[PASS] gated pane SKIPPED, prompt not_attempted, zero keystrokes, PID lineage intact")
}

// =============================================================================
// Scenario 3b — exit-sequence failure: the fixture ignores Ctrl+C while idle,
// so the agent never returns to a shell and smart-restart must fail loud with
// SHELL_NOT_RETURNED — and never blame "Agent is idle".
// =============================================================================

func TestGatesE2ESmartRestartExitSequenceUnconfirmed(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-smart-restart-shell-not-returned")
	defer logger.Close()

	pane := startGatesFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)

	shellPIDBefore := gatesShellPID(t, pane.Session)
	ctrlCBefore := 0
	for _, ev := range pane.Events() {
		if ev.Event == "key" && ev.Data == "c-c" {
			ctrlCBefore++
		}
	}

	// --force guarantees the decision reaches the execution path regardless of
	// whether the probe classifies the idle fixture as SAFE_TO_RESTART or
	// UNKNOWN (see decideRestart). The fixture ignores C-c while not working,
	// so the double-Ctrl+C exit sequence cannot reach a shell.
	started := time.Now()
	out, exit := gatesRunNTM(t, logger, nil,
		"--robot-smart-restart="+pane.Session, "--force")
	logger.Log("[TIMING] smart-restart (exit-failure path) took %s", time.Since(started).Round(time.Millisecond))
	if exit == 0 {
		t.Fatalf("smart-restart against an unkillable agent reported exit 0: %s", out)
	}
	var smart gatesSmartRestartEnvelope
	gatesDecode(t, "smart-restart", out, &smart)
	logger.LogJSON("smart_restart_shell_not_returned", smart)
	if smart.Success {
		t.Fatalf("envelope success=true despite exit-sequence failure: %s", out)
	}
	if smart.ErrorCode != "SHELL_NOT_RETURNED" {
		t.Fatalf("top-level error_code = %q, want SHELL_NOT_RETURNED", smart.ErrorCode)
	}
	_, action := gatesSolePane(t, smart.Actions)
	if action.Action != "FAILED" {
		t.Fatalf("action = %q, want FAILED (reason %q)", action.Action, action.Reason)
	}
	if !strings.Contains(action.Reason, "restart exit sequence unconfirmed") {
		t.Fatalf("failure reason %q lacks 'restart exit sequence unconfirmed'", action.Reason)
	}
	if strings.Contains(action.Reason, "Agent is idle") {
		t.Fatalf("failure reason %q blames idleness instead of the exit sequence", action.Reason)
	}
	if action.StructuredError == nil || action.StructuredError.Code != "SHELL_NOT_RETURNED" {
		t.Fatalf("structured_error = %+v, want code SHELL_NOT_RETURNED", action.StructuredError)
	}
	if action.RestartSequence == nil {
		t.Fatal("restart_sequence missing from FAILED action")
	}
	if action.RestartSequence.ExitMethod != "double_ctrl_c" {
		t.Fatalf("exit_method = %q, want double_ctrl_c", action.RestartSequence.ExitMethod)
	}
	if action.RestartSequence.ShellConfirmed {
		t.Fatal("shell_confirmed=true despite the agent surviving")
	}
	if action.RestartSequence.LaunchAttempted || action.RestartSequence.AgentLaunched {
		t.Fatalf("relaunch was attempted after an unconfirmed exit: %+v", action.RestartSequence)
	}
	if smart.Summary.Failed != 1 {
		t.Fatalf("summary.failed = %d, want 1", smart.Summary.Failed)
	}

	// Ground truth: the fixture received the two Ctrl+C bytes, survived them,
	// and the pane shell was never replaced.
	ctrlCAfter := 0
	for _, ev := range pane.Events() {
		if ev.Event == "key" && ev.Data == "c-c" {
			ctrlCAfter++
		}
	}
	if ctrlCAfter-ctrlCBefore < 2 {
		t.Fatalf("fixture logged %d new c-c keys, want >= 2 (double_ctrl_c)", ctrlCAfter-ctrlCBefore)
	}
	if !gatesFixtureAlive(pane) {
		t.Fatal("fixture died — the exit sequence should not have killed it")
	}
	if pid := gatesShellPID(t, pane.Session); pid != shellPIDBefore {
		t.Fatalf("shell PID changed %d -> %d despite FAILED restart", shellPIDBefore, pid)
	}
	logger.Log("[PASS] SHELL_NOT_RETURNED surfaced with honest reason; fixture survived %d Ctrl+C bytes",
		ctrlCAfter-ctrlCBefore)
}

// =============================================================================
// Scenario 4 (rf0ka) — restart prompt gating on --robot-restart-pane.
// =============================================================================

// gatesWriteRestartConfig writes an isolated NTM config whose [agents] claude
// command is the given executable line, returning NTM_CONFIG env entries.
func gatesWriteRestartConfig(t *testing.T, dir, claudeCommand string) []string {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.toml")
	content := "[agents]\nclaude = \"" + claudeCommand + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write isolated config: %v", err)
	}
	return []string{"NTM_CONFIG=" + cfgPath}
}

func gatesWriteScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// Success path: the relaunched command is the fakeagent itself (via the
// isolated [agents] claude command), so after the readiness gate passes the
// prompt must deliver, and the relaunched fixture's event log must prove the
// submit.
func TestGatesE2ERestartPanePromptDeliveredAfterReadiness(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-restart-pane-prompt-delivered")
	defer logger.Close()

	pane := startGatesRespawnableFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)

	fakeagentBin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	dir := t.TempDir()
	relaunchControl := filepath.Join(dir, "control2")
	relaunchLog := filepath.Join(dir, "events2.jsonl")
	// Seed the relaunched fixture's control file with an ack whose text
	// satisfies the spawn readiness detector ("claude code v" / "welcome
	// back"): the fixture's own chrome intentionally does not include a real
	// agent banner, and the readiness gate needs one. The ack lands in the
	// transcript ~400ms after the fixture starts.
	if err := os.WriteFile(relaunchControl,
		[]byte("ack Welcome back - fake claude code v9.9 ready\n"), 0o644); err != nil {
		t.Fatalf("seed relaunch control file: %v", err)
	}
	wrapper := gatesWriteScript(t, dir, "fake-claude.sh", fmt.Sprintf(
		"#!/bin/bash\nexec %s --persona=claude --control=%s --log=%s\n",
		tmux.ShellQuote(fakeagentBin), tmux.ShellQuote(relaunchControl), tmux.ShellQuote(relaunchLog)))
	env := gatesWriteRestartConfig(t, dir, wrapper)

	shellPIDBefore := gatesShellPID(t, pane.Session)
	promptToken := fmt.Sprintf("GATES-E2E-RESTART-PROMPT-%d", time.Now().UnixNano())

	started := time.Now()
	out, exit := gatesRunNTM(t, logger, env,
		"--robot-restart-pane="+pane.Session, "--restart-prompt="+promptToken)
	logger.Log("[TIMING] restart-pane (delivered path) took %s", time.Since(started).Round(time.Millisecond))
	if exit != 0 {
		t.Fatalf("robot-restart-pane exit=%d output=%s", exit, out)
	}
	var restart gatesRestartPaneEnvelope
	gatesDecode(t, "restart-pane", out, &restart)
	logger.LogJSON("restart_pane_prompt_delivered", restart)
	if !restart.Success {
		t.Fatalf("restart-pane reported failure: %s", out)
	}
	if len(restart.Restarted) != 1 {
		t.Fatalf("restarted = %v, want exactly one pane", restart.Restarted)
	}
	paneKey := restart.Restarted[0]
	if pids, ok := restart.PaneShellPIDs[paneKey]; !ok || pids.Before == 0 || pids.After == 0 || pids.Before == pids.After {
		t.Fatalf("pane_shell_pids[%s] = %+v, want a real before->after PID change", paneKey, restart.PaneShellPIDs[paneKey])
	} else {
		if pids.Before != shellPIDBefore {
			t.Fatalf("envelope before-PID %d disagrees with observed shell PID %d", pids.Before, shellPIDBefore)
		}
		logger.Log("[PID-LINEAGE] respawn evidence: shell %d -> %d", pids.Before, pids.After)
	}
	if status := restart.AgentRelaunchStatus[paneKey]; status != "ready" {
		t.Fatalf("agent_relaunch_status[%s] = %q, want ready", paneKey, status)
	}
	if !restart.AgentRelaunched[paneKey] {
		t.Fatalf("agent_relaunched[%s] = false, want true", paneKey)
	}
	if status := restart.PromptDelivery[paneKey]; status != "delivered" {
		t.Fatalf("prompt_delivery[%s] = %q, want delivered (prompt_error %q)", paneKey, status, restart.PromptError)
	}
	if !restart.PromptSent {
		t.Fatal("prompt_sent = false, want true")
	}

	// Ground truth: the RELAUNCHED fixture logged the submit of the prompt.
	relaunched := &fakeagentPane{
		t:           t,
		Session:     pane.Session,
		PaneID:      pane.PaneID,
		Persona:     "claude",
		controlPath: relaunchControl,
		logPath:     relaunchLog,
	}
	defer gatesDumpDiagnostics(t, logger, relaunched)
	if ev, ok := relaunched.WaitForEvent("submit", promptToken, 20*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
		t.Fatalf("relaunched fixture never logged the prompt submit; events=%+v pane:\n%s",
			relaunched.Events(), capture)
	} else {
		logger.LogJSON("relaunched_submit_event", ev)
	}
	logger.Log("[PASS] restart relaunched the configured agent and delivered the prompt after readiness")
}

// Withheld path: the isolated [agents] claude command prints an agent-looking
// banner (so the relaunch readiness gate passes honestly) but leaves an
// interactive SHELL in the foreground. The delivery gate must withhold the
// prompt — RESTART_PROMPT_NOT_DELIVERED, zero prompt keystrokes.
func TestGatesE2ERestartPanePromptWithheldWhenShellStaysForeground(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-restart-pane-prompt-withheld")
	defer logger.Close()

	pane := startGatesRespawnableFakeagentPane(t, logger, "claude")
	defer gatesDumpDiagnostics(t, logger, pane)

	dir := t.TempDir()
	wrapper := gatesWriteScript(t, dir, "shell-claude.sh",
		"#!/bin/bash\n"+
			"printf 'Welcome back - fake claude code v9.9 (banner only)\\n'\n"+
			"exec /bin/bash --noprofile --norc -i\n")
	env := gatesWriteRestartConfig(t, dir, wrapper)

	promptToken := fmt.Sprintf("GATES-E2E-WITHHELD-PROMPT-%d", time.Now().UnixNano())
	started := time.Now()
	out, exit := gatesRunNTM(t, logger, env,
		"--robot-restart-pane="+pane.Session, "--restart-prompt="+promptToken)
	logger.Log("[TIMING] restart-pane (withheld path) took %s", time.Since(started).Round(time.Millisecond))
	if exit == 0 {
		t.Fatalf("restart-pane with a withheld prompt reported exit 0: %s", out)
	}
	var restart gatesRestartPaneEnvelope
	gatesDecode(t, "restart-pane", out, &restart)
	logger.LogJSON("restart_pane_prompt_withheld", restart)
	if restart.Success {
		t.Fatalf("envelope success=true despite withheld prompt: %s", out)
	}
	if restart.ErrorCode != "RESTART_PROMPT_NOT_DELIVERED" {
		t.Fatalf("error_code = %q, want RESTART_PROMPT_NOT_DELIVERED (error %q)", restart.ErrorCode, restart.Error)
	}
	if len(restart.Restarted) != 1 {
		t.Fatalf("restarted = %v, want the respawn itself to have succeeded", restart.Restarted)
	}
	paneKey := restart.Restarted[0]
	if status := restart.AgentRelaunchStatus[paneKey]; status != "ready" {
		t.Fatalf("agent_relaunch_status[%s] = %q, want ready (the withheld path requires a ready relaunch)", paneKey, status)
	}
	if status := restart.PromptDelivery[paneKey]; status != "skipped" {
		t.Fatalf("prompt_delivery[%s] = %q, want skipped", paneKey, status)
	}
	var withheldFailure *gatesRestartPaneFailure
	for i := range restart.Failed {
		if strings.Contains(restart.Failed[i].Reason, "prompt withheld") {
			withheldFailure = &restart.Failed[i]
			break
		}
	}
	if withheldFailure == nil {
		t.Fatalf("no per-pane withheld failure recorded: %+v", restart.Failed)
	}
	if !strings.HasPrefix(withheldFailure.Reason, "RESTART_PROMPT_NOT_DELIVERED: prompt withheld, no keystrokes sent") {
		t.Fatalf("withheld reason = %q, want the RESTART_PROMPT_NOT_DELIVERED prefix", withheldFailure.Reason)
	}
	if !strings.Contains(withheldFailure.Reason, "shell") {
		t.Fatalf("withheld reason %q does not blame the shell foreground", withheldFailure.Reason)
	}

	// Ground truth: the pane shows the interactive shell and the banner, and
	// the prompt text never appeared anywhere in it — zero prompt keystrokes.
	capture, err := tmux.CapturePaneOutput(pane.PaneID, 60)
	if err != nil {
		t.Fatalf("capture withheld pane: %v", err)
	}
	if !strings.Contains(capture, "banner only") {
		t.Fatalf("relaunch wrapper never ran; pane shows:\n%s", capture)
	}
	if strings.Contains(capture, promptToken) {
		t.Fatalf("withheld prompt text leaked into the pane:\n%s", capture)
	}
	logger.Log("[PASS] prompt withheld from shell foreground with RESTART_PROMPT_NOT_DELIVERED; capture proves zero prompt keystrokes")
}

// =============================================================================
// Scenario 5 — rate-limit precedence: a rate-limited pane keeps the WAIT
// recommendation even with a gate phrase sitting in its transcript tail.
// =============================================================================

func TestGatesE2ERateLimitPrecedenceOverGateFlagging(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "gates-ratelimit-precedence")
	defer logger.Close()

	pane := startGatesFakeagentPane(t, logger, "codex")
	defer gatesDumpDiagnostics(t, logger, pane)

	// Put a gate PHRASE into the transcript (the hazard gate-detection would
	// flag on an idle pane), then raise the provider usage-limit banner.
	quote := "note to self: the onboarding gate says " + gatesTrustPhrase + " - answer it later"
	gatesInjectTranscript(t, pane, quote)
	pane.Control("ratelimit codex")
	if _, ok := pane.WaitForEvent("control", "ratelimit codex", 5*time.Second); !ok {
		t.Fatalf("ratelimit verb never consumed; events=%+v", pane.Events())
	}
	time.Sleep(800 * time.Millisecond)

	capture, err := tmux.CapturePaneOutput(pane.PaneID, 40)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}
	lower := strings.ToLower(capture)
	if !strings.Contains(lower, "hit your usage limit") {
		t.Fatalf("usage-limit banner not visible:\n%s", capture)
	}
	if !strings.Contains(lower, gatesTrustPhrase) {
		t.Fatalf("gate phrase not visible in the tail (precedence not exercised):\n%s", capture)
	}

	out, exit := gatesRunNTM(t, logger, nil, "--robot-is-working="+pane.Session)
	if exit != 0 {
		t.Fatalf("robot-is-working exit=%d output=%s", exit, out)
	}
	var isWorking gatesIsWorkingEnvelope
	gatesDecode(t, "is-working", out, &isWorking)
	logger.LogJSON("is_working_rate_limited", isWorking)
	_, work := gatesSolePane(t, isWorking.Panes)
	if !work.IsRateLimited {
		t.Fatalf("is_rate_limited = false with the banner on screen: %+v", work)
	}
	if work.Recommendation != "RATE_LIMITED_WAIT" {
		t.Fatalf("recommendation = %q, want RATE_LIMITED_WAIT", work.Recommendation)
	}
	if work.Recommendation == "MANUAL_INTERVENTION" || work.IndicatorBasis == "interactive_gate" {
		t.Fatalf("gate flagging outranked the rate limit: %+v", work)
	}
	if work.IndicatorBasis != "rate_limit_indicator" {
		t.Fatalf("indicator_basis = %q, want rate_limit_indicator", work.IndicatorBasis)
	}
	if isWorking.Summary.RateLimitedCount != 1 {
		t.Fatalf("summary.rate_limited_count = %d, want 1", isWorking.Summary.RateLimitedCount)
	}
	logger.Log("[PASS] rate limit takes precedence: WAIT recommendation, no gate flag, banner + quoted phrase both on screen")
}
