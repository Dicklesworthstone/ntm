//go:build e2e
// +build e2e

package e2e

// send_tracking_ack_e2e_test.go — live E2E proof for robot-send tracking
// (ntm-qce2) and robot-ack waiting/timeout behavior (ntm-g70w), driven
// against the fakeagent fixture (bd-kur07) in real tmux sessions.
//
// Ground truth discipline: every delivery assertion is checked against the
// fixture's JSONL event log (submit / ack_emit / composer_change events), not
// robot envelopes alone. Every scenario uses its own tmux session and logs
// envelopes, fixture event summaries, and per-phase timings via TestLogger.
//
// Empirical contracts these tests pin down (verified live before writing):
//
//   - A tracked send to a pane that submits and echoes the message is
//     acknowledged via "echo_detected" on the first poll: the transcript echo
//     IS an acknowledgment to the detector. A live agent pane therefore
//     cannot produce the --track timeout path; holding a successful tracked
//     send in "pending" requires a pane that accepts keys but renders
//     nothing (a shell with `stty -echo`), which is what the timeout
//     scenario uses.
//   - Ack timeout is a structured failure: top-level success=false with
//     error_code=TIMEOUT and process exit 1, while the embedded send section
//     still reports its own success and the pending list names the
//     unacknowledged pane keys.
//   - --op-id composed with --track is rejected up front with INVALID_FLAG
//     (v1.23.0 contract) and nothing is delivered.
//   - A malformed --panes selector on --robot-ack is rejected with
//     INVALID_FLAG before the ack engine runs: the error envelope carries no
//     session/confirmations/pending fields at all (regression guard against
//     the old silent pane-0 scan).
//   - --msg switches --robot-ack into echo-detection mode (ack_type
//     "echo_detected"); without --msg that ack type is unreachable.
//
// Supersession note (qce2 scope): the legacy
// tests/e2e/robot_mode_test.go:TestRobotSendTrackingCapabilities is env-gated
// (NTM_E2E_TESTS=1), requires a live `codex` binary (skips otherwise), and
// historically depended on the obsolete `spawn --project-dir` flag; its
// current body no longer passes --project-dir, but neighbouring tests in that
// legacy file still do, so the file cannot run against today's spawn CLI.
// That test lives outside e2e/ (the deterministic suite) and is superseded by
// TestRobotSendTrack* below, which prove the same tracked-send contract
// deterministically against the fixture instead of a live Codex process.

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Envelope mirrors (assertion targets for the real JSON surfaces)
// =============================================================================

// robotMetaSection mirrors the `_meta` block on terminal robot envelopes.
type robotMetaSection struct {
	ExitCode int    `json:"exit_code"`
	Command  string `json:"command"`
}

// robotAckConfirmation mirrors robot.AckConfirmation.
type robotAckConfirmation struct {
	Pane      string `json:"pane"`
	AckType   string `json:"ack_type"`
	AckAt     string `json:"ack_at"`
	LatencyMs int    `json:"latency_ms"`
}

// robotAckFailure mirrors robot.AckFailure.
type robotAckFailure struct {
	Pane   string `json:"pane"`
	Reason string `json:"reason"`
}

// robotAckSection mirrors robot.AckOutput's JSON surface.
type robotAckSection struct {
	Success       bool                   `json:"success"`
	Error         string                 `json:"error"`
	ErrorCode     string                 `json:"error_code"`
	Session       string                 `json:"session"`
	SentAt        time.Time              `json:"sent_at"`
	CompletedAt   time.Time              `json:"completed_at"`
	Confirmations []robotAckConfirmation `json:"confirmations"`
	Pending       []string               `json:"pending"`
	Failed        []robotAckFailure      `json:"failed"`
	TimeoutMs     int                    `json:"timeout_ms"`
	TimedOut      bool                   `json:"timed_out"`
}

// robotSendSection mirrors the send half of robot.SendAndAckOutput.
type robotSendSection struct {
	Success    bool      `json:"success"`
	Session    string    `json:"session"`
	SentAt     time.Time `json:"sent_at"`
	Targets    []string  `json:"targets"`
	Successful []string  `json:"successful"`
	Failed     []struct {
		Pane  string `json:"pane"`
		Error string `json:"error"`
	} `json:"failed"`
	MessagePreview string `json:"message_preview"`
}

// trackedSendEnvelope mirrors robot.SendAndAckOutput.
type trackedSendEnvelope struct {
	Success   bool             `json:"success"`
	Error     string           `json:"error"`
	ErrorCode string           `json:"error_code"`
	Meta      robotMetaSection `json:"_meta"`
	Send      robotSendSection `json:"send"`
	Ack       robotAckSection  `json:"ack"`
}

// liveAckTypes are the ack_type values GetAck can actually confirm with
// (robot.AckType constants minus "none", which never appears in confirmations).
var liveAckTypes = map[string]bool{
	"prompt_returned": true,
	"echo_detected":   true,
	"explicit_ack":    true,
	"output_started":  true,
}

// =============================================================================
// Helpers (frozen files provide startFakeagentSession/runNTMFixture; these
// build on them without modifying them)
// =============================================================================

// ntmAsyncResult carries an asynchronous ntm invocation's outcome. It never
// touches *testing.T so it is safe to produce from a goroutine.
type ntmAsyncResult struct {
	out  string
	exit int
	err  error
}

// startNTMFixtureAsync launches the freshly built ntm binary in a goroutine
// and returns a channel that yields exactly one result. No *testing.T method
// is called off the test goroutine.
func startNTMFixtureAsync(t *testing.T, logger *TestLogger, args ...string) <-chan ntmAsyncResult {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	logger.Log("[NTM-ASYNC] launching ntm %v", args)
	ch := make(chan ntmAsyncResult, 1)
	go func() {
		cmd := exec.Command(bin, args...)
		out, runErr := cmd.CombinedOutput()
		res := ntmAsyncResult{out: string(out)}
		if runErr != nil {
			if ee, ok := runErr.(*exec.ExitError); ok {
				res.exit = ee.ExitCode()
			} else {
				res.err = runErr
			}
		}
		ch <- res
	}()
	return ch
}

// waitNTMAsync blocks for the async invocation's result and logs it.
func waitNTMAsync(t *testing.T, logger *TestLogger, ch <-chan ntmAsyncResult, max time.Duration, label string) ntmAsyncResult {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("%s: run ntm: %v", label, res.err)
		}
		logger.Log("[NTM-ASYNC] %s exit=%d", label, res.exit)
		logger.Log("[NTM-ASYNC] %s output=%s", label, strings.TrimSpace(res.out))
		return res
	case <-time.After(max):
		t.Fatalf("%s: ntm did not finish within %s", label, max)
		return ntmAsyncResult{}
	}
}

// armAckUntil repeatedly appends an `ack TEXT` control verb to the fixture
// pane (arming AFTER the CLI was launched, per the tracked-ack flow) until
// the async ntm invocation completes. Re-arming every ~750ms guarantees at
// least one emission lands after the ack engine captured its baseline, no
// matter how long CLI startup takes. Runs entirely on the test goroutine.
func armAckUntil(t *testing.T, logger *TestLogger, pane *fakeagentPane, text string, ch <-chan ntmAsyncResult, max time.Duration, label string) (ntmAsyncResult, int) {
	t.Helper()
	deadline := time.Now().Add(max)
	arms := 0
	for {
		select {
		case res := <-ch:
			if res.err != nil {
				t.Fatalf("%s: run ntm: %v", label, res.err)
			}
			logger.Log("[NTM-ASYNC] %s exit=%d after %d ack arm(s)", label, res.exit, arms)
			logger.Log("[NTM-ASYNC] %s output=%s", label, strings.TrimSpace(res.out))
			return res, arms
		default:
		}
		if time.Now().After(deadline) {
			dumpScenarioFailure(t, logger, pane)
			t.Fatalf("%s: ntm did not finish within %s (%d ack arms)", label, max, arms)
		}
		pane.Control("ack " + text) // sleeps ~250ms internally
		arms++
		time.Sleep(500 * time.Millisecond)
	}
}

// splitFakeagentPane adds a second fixture pane to an existing fixture
// session, mirroring startFakeagentSession's split/title pattern with the
// canonical "<session>__cc_N" title so NTM classifies it as an agent pane.
func splitFakeagentPane(t *testing.T, base *fakeagentPane, persona string, ordinal int) *fakeagentPane {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	before, err := tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes before split: %v", err)
	}
	existing := make(map[string]bool, len(before))
	for _, p := range before {
		existing[p.ID] = true
	}

	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")
	launch := fmt.Sprintf("%s --persona=%s --control=%s --log=%s",
		tmux.ShellQuote(bin), persona, tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("split-window", "-h", "-t", base.Session, launch); err != nil {
		t.Fatalf("split fakeagent pane: %v", err)
	}

	after, err := tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes after split: %v", err)
	}
	var newID string
	for _, p := range after {
		if !existing[p.ID] {
			newID = p.ID
			break
		}
	}
	if newID == "" {
		t.Fatalf("split did not produce a new pane (before=%d after=%d)", len(before), len(after))
	}

	alias := "cc"
	if persona == "codex" {
		alias = "cod"
	}
	title := fmt.Sprintf("%s__%s_%d", base.Session, alias, ordinal)
	if err := tmux.SetPaneTitle(newID, title); err != nil {
		t.Fatalf("title split fakeagent pane: %v", err)
	}

	pane := &fakeagentPane{
		t:           t,
		Session:     base.Session,
		PaneID:      newID,
		Persona:     persona,
		controlPath: controlPath,
		logPath:     logPath,
	}
	if _, ok := pane.WaitForEvent("start", "", 10*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(newID, 40)
		t.Fatalf("split fakeagent pane never started; pane shows:\n%s", capture)
	}
	return pane
}

// splitMutedShellPane splits off a pane running a shell with terminal echo
// disabled: it accepts delivered keys but never renders new output. This is
// the only honest way to hold a SUCCESSFUL tracked send in the pending state,
// because a fixture pane's own submit echo already counts as an
// acknowledgment to the detector (see the file header).
func splitMutedShellPane(t *testing.T, session string) (paneID string, paneIndex int) {
	t.Helper()
	before, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("enumerate panes before muted split: %v", err)
	}
	existing := make(map[string]bool, len(before))
	for _, p := range before {
		existing[p.ID] = true
	}
	if _, err := tmux.DefaultClient.Run("split-window", "-h", "-t", session, "sh -c 'stty -echo; exec sleep 600'"); err != nil {
		t.Fatalf("split muted shell pane: %v", err)
	}
	after, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("enumerate panes after muted split: %v", err)
	}
	for _, p := range after {
		if !existing[p.ID] {
			return p.ID, p.Index
		}
	}
	t.Fatalf("muted split did not produce a new pane")
	return "", 0
}

// zeroHistoryLimit sets the global tmux history-limit to 0 for panes created
// while it is active, returning an idempotent restore func (also registered
// as a cleanup). Rationale: the ack engine captures 200 lines of scrollback,
// and tmux 3.x pushes each of the fixture's 500ms full-screen repaints into
// history, so a FRESH fixture pane's capture grows ~2 lines per repaint and
// falsely confirms "output_started" even though the visible screen never
// changed. (Aged panes whose history already exceeds the capture window do
// not exhibit this — the periodic pattern makes shifted windows identical.)
// Disabling history for the silent-pane scenarios makes the capture reflect
// only the visible screen, which is byte-stable across repaints.
func zeroHistoryLimit(t *testing.T, logger *TestLogger) (restore func()) {
	t.Helper()
	// A bare `start-server` exits again with no sessions, so hold the server
	// open with a throwaway session while the option is being applied.
	holder := fmt.Sprintf("ntm-e2e-histopt-%d", time.Now().UnixNano())
	if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", holder, "sleep 120"); err != nil {
		t.Fatalf("create history-limit holder session: %v", err)
	}
	prev := ""
	if out, err := tmux.DefaultClient.Run("show-options", "-gv", "history-limit"); err == nil {
		prev = strings.TrimSpace(out)
	}
	if _, err := tmux.DefaultClient.Run("set-option", "-g", "history-limit", "0"); err != nil {
		t.Fatalf("set global history-limit 0: %v", err)
	}
	logger.Log("[SETUP] global history-limit 0 (was %q)", prev)
	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		if prev != "" {
			_, _ = tmux.DefaultClient.Run("set-option", "-g", "history-limit", prev)
		} else {
			_, _ = tmux.DefaultClient.Run("set-option", "-gu", "history-limit")
		}
		_, _ = tmux.DefaultClient.Run("kill-session", "-t", holder)
	}
	t.Cleanup(restore)
	return restore
}

// paneIndexFor resolves a pane ID to its current tmux pane index.
func paneIndexFor(t *testing.T, session, paneID string) int {
	t.Helper()
	panes, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("enumerate panes: %v", err)
	}
	for _, p := range panes {
		if p.ID == paneID {
			return p.Index
		}
	}
	t.Fatalf("pane %s not found in session %s", paneID, session)
	return -1
}

// logFixtureEventSummary logs a compact event-name histogram for a fixture.
func logFixtureEventSummary(logger *TestLogger, label string, pane *fakeagentPane) {
	counts := map[string]int{}
	for _, ev := range pane.Events() {
		counts[ev.Event]++
	}
	logger.LogJSON(label+"_fixture_event_counts", counts)
}

// dumpScenarioFailure captures pane contents and full fixture event logs so
// a failed scenario leaves a diagnosable trail.
func dumpScenarioFailure(t *testing.T, logger *TestLogger, panes ...*fakeagentPane) {
	t.Helper()
	for _, pane := range panes {
		capture, err := tmux.CapturePaneOutput(pane.PaneID, 60)
		if err != nil {
			logger.Log("[FAIL-DUMP] pane %s capture failed: %v", pane.PaneID, err)
		} else {
			logger.Log("[FAIL-DUMP] pane %s (%s) capture:\n%s", pane.PaneID, pane.Persona, capture)
		}
		logger.LogJSON("fail_dump_events_"+pane.PaneID, pane.Events())
	}
}

// =============================================================================
// qce2 — robot-send --track
// =============================================================================

// Scenario 1: a tracked send to a live fixture pane, with an explicit ack
// armed AFTER the send is in flight, must confirm within the timeout with
// real pane attribution, a real ack type, and sane sent_at/completed_at
// ordering — and the fixture log must prove the submit actually happened.
func TestRobotSendTrackConfirmsAgainstFakeagent(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-send-track-confirm")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s (%s)", pane.Session, pane.PaneID, time.Since(start).Round(time.Millisecond))

	ch := startNTMFixtureAsync(t, logger,
		"--robot-send="+pane.Session, "--msg=ping", "--type=claude",
		"--track", "--timeout=20s", "--poll=500ms")
	res, arms := armAckUntil(t, logger, pane, "PONG-RESPONSE", ch, 120*time.Second, "track-confirm")
	logger.Log("[TIMING] tracked send completed after %s (%d ack arms)", time.Since(start).Round(time.Millisecond), arms)

	var env trackedSendEnvelope
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse tracked envelope: %v (%s)", err, res.out)
	}
	logger.LogJSON("tracked_envelope", env)

	if res.exit != 0 || !env.Success {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("tracked send must succeed: exit=%d success=%v error=%q", res.exit, env.Success, env.Error)
	}
	if !env.Send.Success || env.Send.Session != pane.Session {
		t.Fatalf("send section must succeed for %q: %+v", pane.Session, env.Send)
	}
	if len(env.Send.Targets) != 1 || env.Send.Targets[0] != "0" ||
		len(env.Send.Successful) != 1 || env.Send.Successful[0] != "0" {
		t.Fatalf("expected exactly pane key \"0\" targeted and delivered: %+v", env.Send)
	}
	if env.Ack.TimedOut || len(env.Ack.Pending) != 0 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("confirmed track must not time out or leave pending panes: %+v", env.Ack)
	}
	if len(env.Ack.Confirmations) != 1 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("expected exactly one confirmation: %+v", env.Ack)
	}
	conf := env.Ack.Confirmations[0]
	if conf.Pane != "0" {
		t.Fatalf("confirmation attributed to wrong pane: %+v", conf)
	}
	if !liveAckTypes[conf.AckType] {
		t.Fatalf("confirmation ack_type %q is not a real AckType", conf.AckType)
	}
	if conf.LatencyMs < 0 || conf.LatencyMs > env.Ack.TimeoutMs {
		t.Fatalf("confirmation latency_ms %d outside [0,%d]", conf.LatencyMs, env.Ack.TimeoutMs)
	}
	if conf.AckAt == "" {
		t.Fatalf("confirmation missing ack_at: %+v", conf)
	}
	if env.Ack.SentAt.IsZero() || env.Ack.CompletedAt.IsZero() || env.Ack.CompletedAt.Before(env.Ack.SentAt) {
		t.Fatalf("ack sent_at/completed_at ordering broken: sent_at=%s completed_at=%s", env.Ack.SentAt, env.Ack.CompletedAt)
	}

	// Fixture ground truth: the message was genuinely submitted, and the
	// arm-after-send control flow genuinely emitted the explicit ack text.
	if _, ok := pane.WaitForEvent("submit", "ping", 15*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture never logged the submit")
	}
	if _, ok := pane.WaitForEvent("ack_emit", "PONG-RESPONSE", 10*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture never emitted the armed ack text")
	}
	logFixtureEventSummary(logger, "track_confirm", pane)
	logger.Log("[PASS] ack_type=%s latency_ms=%d in %s", conf.AckType, conf.LatencyMs, time.Since(start).Round(time.Millisecond))
}

// Scenario 2: the tracked-send timeout path. A live fixture pane cannot stay
// silent (its submit echo is an acknowledgment), so the target is a muted
// shell pane that accepts the delivery but never renders output. The send
// succeeds; the ack section must time out with the pane pending; the actual
// exit contract is success=false + error_code=TIMEOUT + process exit 1.
func TestRobotSendTrackTimeoutPendingPane(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-send-track-timeout")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	mutedID, mutedIndex := splitMutedShellPane(t, pane.Session)
	selector := fmt.Sprintf("%d", mutedIndex)
	logger.Log("[SETUP] session=%s fixture=%s muted=%s (index %d)", pane.Session, pane.PaneID, mutedID, mutedIndex)

	const wantTimeout = 6 * time.Second
	out, exit := runNTMFixture(t, logger,
		"--robot-send="+pane.Session, "--msg=tracked message nobody acknowledges",
		"--panes="+selector, "--track", "--timeout=6s", "--poll=500ms")
	logger.Log("[TIMING] tracked send returned after %s", time.Since(start).Round(time.Millisecond))

	var env trackedSendEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse tracked envelope: %v (%s)", err, out)
	}
	logger.LogJSON("tracked_timeout_envelope", env)

	// The send itself succeeded — the timeout is an ack-layer failure.
	if !env.Send.Success || len(env.Send.Successful) != 1 || env.Send.Successful[0] != selector {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("send section must report successful delivery to pane %s: %+v", selector, env.Send)
	}
	// Actual exit contract (verified live): overall success=false, TIMEOUT.
	if exit != 1 || env.Success || env.ErrorCode != "TIMEOUT" {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("timeout must fail the envelope: exit=%d success=%v error_code=%q", exit, env.Success, env.ErrorCode)
	}
	if !env.Ack.TimedOut || env.Ack.ErrorCode != "TIMEOUT" {
		t.Fatalf("ack section must be timed_out with TIMEOUT code: %+v", env.Ack)
	}
	if len(env.Ack.Pending) != 1 || env.Ack.Pending[0] != selector {
		t.Fatalf("pending must name exactly the muted pane %q: %+v", selector, env.Ack.Pending)
	}
	if len(env.Ack.Confirmations) != 0 {
		t.Fatalf("no confirmations expected from a muted pane: %+v", env.Ack.Confirmations)
	}
	if env.Ack.TimeoutMs != int(wantTimeout.Milliseconds()) {
		t.Fatalf("timeout_ms must echo the requested timeout: %d", env.Ack.TimeoutMs)
	}
	// Envelope-internal duration: dispatch + full ack wait. Assert loosely.
	waited := env.Ack.CompletedAt.Sub(env.Ack.SentAt)
	if waited < wantTimeout || waited > 3*wantTimeout {
		t.Fatalf("ack wall time %s outside [%s,%s]", waited, wantTimeout, 3*wantTimeout)
	}
	// The fixture pane was filtered out: it must have seen nothing.
	if n := pane.CountEvents("submit"); n != 0 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture pane must not receive the filtered send, saw %d submits", n)
	}
	logFixtureEventSummary(logger, "track_timeout", pane)
	logger.Log("[PASS] pending=%v waited=%s in %s", env.Ack.Pending, waited.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}

// Scenario 3: --op-id composed with --track must be rejected with a typed
// INVALID_FLAG envelope naming the incompatibility (v1.23.0 contract), and
// nothing may be delivered to the pane.
func TestRobotSendTrackRejectsOpID(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-send-track-opid-reject")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	out, exit := runNTMFixture(t, logger,
		"--robot-send="+pane.Session, "--msg=never delivered", "--type=claude",
		"--track", "--op-id=e2e-opid-123", "--timeout=5s")

	var env struct {
		Success   bool             `json:"success"`
		Error     string           `json:"error"`
		ErrorCode string           `json:"error_code"`
		Hint      string           `json:"hint"`
		Meta      robotMetaSection `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse rejection envelope: %v (%s)", err, out)
	}
	logger.LogJSON("opid_rejection_envelope", env)

	if exit != 1 || env.Success {
		t.Fatalf("op-id+track must fail: exit=%d success=%v", exit, env.Success)
	}
	if env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("expected INVALID_FLAG, got %q", env.ErrorCode)
	}
	if !strings.Contains(env.Error, "--op-id is not supported with --track") {
		t.Fatalf("error must name the flag incompatibility: %q", env.Error)
	}
	if env.Meta.Command != "robot-send" || env.Meta.ExitCode != 1 {
		t.Fatalf("_meta must attribute the failure to robot-send with exit 1: %+v", env.Meta)
	}
	if env.Hint == "" {
		t.Fatalf("rejection must carry an actionable hint")
	}

	// The rejection happens before dispatch: the fixture must stay untouched.
	time.Sleep(1500 * time.Millisecond)
	if n := pane.CountEvents("submit"); n != 0 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("rejected send must not deliver, saw %d submits", n)
	}
	if n := pane.CountEvents("composer_change"); n != 0 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("rejected send must not type into the composer, saw %d composer changes", n)
	}
	logFixtureEventSummary(logger, "opid_reject", pane)
	logger.Log("[PASS] INVALID_FLAG rejection with no side effects in %s", time.Since(start).Round(time.Millisecond))
}

// Scenario 4: live re-proof of --panes target filtering with two fixture
// panes. Only the selected pane's fixture log may show the submit — envelope
// claims are cross-checked against both panes' ground truth.
func TestRobotSendPanesFilterLiveReproof(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-send-panes-filter")
	defer logger.Close()
	start := time.Now()

	pane1 := startFakeagentSession(t, "claude", 200, 50)
	pane2 := splitFakeagentPane(t, pane1, "claude", 2)
	selector := fmt.Sprintf("%d", paneIndexFor(t, pane1.Session, pane2.PaneID))
	logger.Log("[SETUP] session=%s cc_1=%s cc_2=%s selector=%s", pane1.Session, pane1.PaneID, pane2.PaneID, selector)

	marker := fmt.Sprintf("FILTER-PROOF-%d", time.Now().UnixNano())
	out, exit := runNTMFixture(t, logger,
		"--robot-send="+pane1.Session, "--msg="+marker, "--panes="+selector)

	var env robotSendSection
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse send envelope: %v (%s)", err, out)
	}
	logger.LogJSON("filtered_send_envelope", env)

	if exit != 0 || !env.Success {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("filtered send must succeed: exit=%d %+v", exit, env)
	}
	if len(env.Targets) != 1 || env.Targets[0] != selector ||
		len(env.Successful) != 1 || env.Successful[0] != selector {
		t.Fatalf("envelope must target exactly pane %s: %+v", selector, env)
	}

	// Ground truth: pane 2 submitted the marker; pane 1 saw nothing at all.
	if _, ok := pane2.WaitForEvent("submit", marker, 15*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("selected pane's fixture log never recorded the submit")
	}
	if n := pane1.CountEvents("submit"); n != 0 {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("filtered-out pane must not submit, saw %d submits", n)
	}
	for _, ev := range pane1.Events() {
		if ev.Event == "composer_change" && strings.Contains(ev.Data, marker) {
			dumpScenarioFailure(t, logger, pane1, pane2)
			t.Fatalf("filtered-out pane received the payload in its composer: %+v", ev)
		}
	}
	logFixtureEventSummary(logger, "filter_cc1", pane1)
	logFixtureEventSummary(logger, "filter_cc2", pane2)
	logger.Log("[PASS] only pane %s submitted %q in %s", selector, marker, time.Since(start).Round(time.Millisecond))
}

// =============================================================================
// g70w — robot-ack
// =============================================================================

// Scenario 5: --robot-ack after a plain send detects fresh fixture output
// with correct pane attribution and a real ack type.
func TestRobotAckDetectsFixtureOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-ack-detect")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s", pane.Session, pane.PaneID)

	// Plain send first (no --track): the ack is observed by a separate call.
	sendOut, sendExit := runNTMFixture(t, logger,
		"--robot-send="+pane.Session, "--msg=plain send before ack", "--type=claude")
	if sendExit != 0 {
		t.Fatalf("plain send failed: exit=%d output=%s", sendExit, sendOut)
	}
	if _, ok := pane.WaitForEvent("submit", "plain send before ack", 15*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture never logged the plain send submit")
	}
	logger.Log("[TIMING] plain send delivered after %s", time.Since(start).Round(time.Millisecond))

	ch := startNTMFixtureAsync(t, logger,
		"--robot-ack="+pane.Session, "--timeout=20s", "--poll=500ms")
	res, arms := armAckUntil(t, logger, pane, "NTM-ACK-SIGNAL-DELTA", ch, 120*time.Second, "ack-detect")

	var env robotAckSection
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse ack envelope: %v (%s)", err, res.out)
	}
	logger.LogJSON("ack_detect_envelope", env)

	if res.exit != 0 || !env.Success || env.TimedOut {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("ack must confirm before timeout: exit=%d success=%v timed_out=%v", res.exit, env.Success, env.TimedOut)
	}
	if env.Session != pane.Session {
		t.Fatalf("ack envelope names wrong session: %q", env.Session)
	}
	if len(env.Confirmations) != 1 || env.Confirmations[0].Pane != "0" {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("expected one confirmation attributed to pane \"0\": %+v", env.Confirmations)
	}
	conf := env.Confirmations[0]
	if !liveAckTypes[conf.AckType] {
		t.Fatalf("ack_type %q is not a real AckType", conf.AckType)
	}
	if conf.LatencyMs < 0 || conf.LatencyMs > env.TimeoutMs {
		t.Fatalf("latency_ms %d outside [0,%d]", conf.LatencyMs, env.TimeoutMs)
	}
	if len(env.Pending) != 0 {
		t.Fatalf("no pending panes expected: %+v", env.Pending)
	}
	// Ground truth: the fixture really emitted the armed text.
	if _, ok := pane.WaitForEvent("ack_emit", "NTM-ACK-SIGNAL-DELTA", 10*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture never emitted the armed ack text (%d arms)", arms)
	}
	logFixtureEventSummary(logger, "ack_detect", pane)
	logger.Log("[PASS] ack_type=%s latency_ms=%d in %s", conf.AckType, conf.LatencyMs, time.Since(start).Round(time.Millisecond))
}

// Scenario 6: --robot-ack timeout expiry against a silent fixture pane. The
// pending list must name the pane, and the envelope's own sent_at →
// completed_at window must be within ±50% of the requested timeout.
func TestRobotAckTimeoutExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-ack-timeout")
	defer logger.Close()
	start := time.Now()

	restore := zeroHistoryLimit(t, logger)
	pane := startFakeagentSession(t, "claude", 0, 0)
	restore()
	logger.Log("[SETUP] session=%s pane=%s (silent: no ack armed)", pane.Session, pane.PaneID)

	const wantTimeout = 6 * time.Second
	out, exit := runNTMFixture(t, logger,
		"--robot-ack="+pane.Session, "--timeout=6s", "--poll=500ms")
	logger.Log("[TIMING] robot-ack returned after %s wall time", time.Since(start).Round(time.Millisecond))

	var env robotAckSection
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse ack envelope: %v (%s)", err, out)
	}
	logger.LogJSON("ack_timeout_envelope", env)

	if exit != 1 || env.Success || env.ErrorCode != "TIMEOUT" || !env.TimedOut {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("silent pane must yield TIMEOUT failure: exit=%d success=%v code=%q timed_out=%v",
			exit, env.Success, env.ErrorCode, env.TimedOut)
	}
	if len(env.Pending) != 1 || env.Pending[0] != "0" {
		t.Fatalf("pending must name exactly pane \"0\": %+v", env.Pending)
	}
	if len(env.Confirmations) != 0 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("silent pane must not be confirmed: %+v", env.Confirmations)
	}
	if env.TimeoutMs != int(wantTimeout.Milliseconds()) {
		t.Fatalf("timeout_ms must echo the requested timeout: %d", env.TimeoutMs)
	}
	waited := env.CompletedAt.Sub(env.SentAt)
	if waited < wantTimeout/2 || waited > wantTimeout*3/2 {
		t.Fatalf("ack window %s outside ±50%% of %s", waited, wantTimeout)
	}
	logFixtureEventSummary(logger, "ack_timeout", pane)
	logger.Log("[PASS] timed out honestly after %s (requested %s) in %s",
		waited.Round(time.Millisecond), wantTimeout, time.Since(start).Round(time.Millisecond))
}

// Scenario 7: multi-pane partial acknowledgment. Two fixture panes are
// watched; only cc_2 is armed. Confirmations and pending must each name the
// right pane, and the run still reports the timeout contract for the
// unacknowledged remainder.
func TestRobotAckMultiPanePartial(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-ack-partial")
	defer logger.Close()
	start := time.Now()

	restore := zeroHistoryLimit(t, logger)
	pane1 := startFakeagentSession(t, "claude", 200, 50)
	pane2 := splitFakeagentPane(t, pane1, "claude", 2)
	restore()
	pane2Key := fmt.Sprintf("%d", paneIndexFor(t, pane1.Session, pane2.PaneID))
	logger.Log("[SETUP] session=%s cc_1=%s cc_2=%s (arming only cc_2)", pane1.Session, pane1.PaneID, pane2.PaneID)

	ch := startNTMFixtureAsync(t, logger,
		"--robot-ack="+pane1.Session, "--timeout=12s", "--poll=500ms")
	res, _ := armAckUntil(t, logger, pane2, "PARTIAL-ACK-BRAVO", ch, 120*time.Second, "ack-partial")

	var env robotAckSection
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("parse ack envelope: %v (%s)", err, res.out)
	}
	logger.LogJSON("ack_partial_envelope", env)

	// Partial coverage is still a timeout for the silent pane.
	if res.exit != 1 || env.Success || env.ErrorCode != "TIMEOUT" || !env.TimedOut {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("partial ack must report TIMEOUT for the silent pane: exit=%d %+v", res.exit, env)
	}
	if len(env.Confirmations) != 1 || env.Confirmations[0].Pane != pane2Key {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("confirmations must name exactly the armed pane %q: %+v", pane2Key, env.Confirmations)
	}
	if !liveAckTypes[env.Confirmations[0].AckType] {
		t.Fatalf("ack_type %q is not a real AckType", env.Confirmations[0].AckType)
	}
	if len(env.Pending) != 1 || env.Pending[0] != "0" {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("pending must name exactly the silent pane \"0\": %+v", env.Pending)
	}
	// Ground truth attribution: cc_2 emitted, cc_1 never did.
	if _, ok := pane2.WaitForEvent("ack_emit", "PARTIAL-ACK-BRAVO", 10*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("armed pane's fixture log never recorded the ack emission")
	}
	if n := pane1.CountEvents("ack_emit"); n != 0 {
		dumpScenarioFailure(t, logger, pane1, pane2)
		t.Fatalf("silent pane must not emit acks, saw %d", n)
	}
	logFixtureEventSummary(logger, "partial_cc1", pane1)
	logFixtureEventSummary(logger, "partial_cc2", pane2)
	logger.Log("[PASS] confirmed=%s pending=%v in %s", pane2Key, env.Pending, time.Since(start).Round(time.Millisecond))
}

// Scenario 8: a malformed --panes selector must be rejected with INVALID_FLAG
// before the ack engine runs. Regression guard: the old behavior silently
// scanned pane 0; the proof it cannot recur is structural — the error
// envelope carries no session/confirmations/pending fields at all.
func TestRobotAckMalformedPaneSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-ack-bad-selector")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	out, exit := runNTMFixture(t, logger,
		"--robot-ack="+pane.Session, "--panes=zzz", "--timeout=30s")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("parse rejection envelope: %v (%s)", err, out)
	}
	var env struct {
		Success   bool             `json:"success"`
		Error     string           `json:"error"`
		ErrorCode string           `json:"error_code"`
		Meta      robotMetaSection `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse rejection envelope fields: %v (%s)", err, out)
	}
	logger.LogJSON("bad_selector_envelope", env)

	if exit != 1 || env.Success {
		t.Fatalf("malformed selector must fail: exit=%d success=%v", exit, env.Success)
	}
	if env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("expected INVALID_FLAG, got %q (error=%q)", env.ErrorCode, env.Error)
	}
	if !strings.Contains(env.Error, "invalid pane selector") || !strings.Contains(env.Error, "zzz") {
		t.Fatalf("error must name the malformed selector: %q", env.Error)
	}
	if env.Meta.Command != "robot-ack" {
		t.Fatalf("_meta must attribute the failure to robot-ack: %+v", env.Meta)
	}
	// Structural regression guard: no ack scan happened, so the envelope has
	// no ack-output surface whatsoever (and pane 0 was never watched).
	for _, forbidden := range []string{"session", "confirmations", "pending", "sent_at", "completed_at"} {
		if _, present := raw[forbidden]; present {
			t.Fatalf("rejection envelope must not contain %q — the ack engine ran despite the invalid selector: %s", forbidden, out)
		}
	}
	logger.Log("[PASS] INVALID_FLAG with no ack surface in %s", time.Since(start).Round(time.Millisecond))
}

// Scenario 9: --msg switches --robot-ack into echo-detection mode. The same
// fixture emission pattern yields ack_type "echo_detected" when --msg matches
// the emitted text, and can never yield it in plain mode (echo detection is
// only attempted when a message is supplied).
func TestRobotAckEchoDetectionVsPlainMode(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "robot-ack-echo-vs-plain")
	defer logger.Close()
	start := time.Now()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s", pane.Session, pane.PaneID)

	// Plain mode: no --msg, so echo detection is structurally unreachable.
	chPlain := startNTMFixtureAsync(t, logger,
		"--robot-ack="+pane.Session, "--panes=0", "--timeout=20s", "--poll=500ms")
	resPlain, _ := armAckUntil(t, logger, pane, "PAYLOAD-DELTA-71", chPlain, 120*time.Second, "ack-plain-mode")

	var plainEnv robotAckSection
	if err := json.Unmarshal([]byte(resPlain.out), &plainEnv); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse plain-mode envelope: %v (%s)", err, resPlain.out)
	}
	logger.LogJSON("plain_mode_envelope", plainEnv)
	if resPlain.exit != 0 || !plainEnv.Success || len(plainEnv.Confirmations) != 1 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("plain mode must confirm: exit=%d %+v", resPlain.exit, plainEnv)
	}
	plainConf := plainEnv.Confirmations[0]
	if plainConf.Pane != "0" || !liveAckTypes[plainConf.AckType] {
		t.Fatalf("plain-mode confirmation malformed: %+v", plainConf)
	}
	if plainConf.AckType == "echo_detected" {
		t.Fatalf("plain mode (no --msg) can never be echo_detected: %+v", plainConf)
	}
	logger.Log("[TIMING] plain mode confirmed as %s after %s", plainConf.AckType, time.Since(start).Round(time.Millisecond))

	// Echo mode: --msg matching the emitted text must confirm as
	// echo_detected (the message echo plus trailing output).
	echoText := "PAYLOAD-ECHO-92"
	chEcho := startNTMFixtureAsync(t, logger,
		"--robot-ack="+pane.Session, "--panes=0", "--msg="+echoText, "--timeout=20s", "--poll=500ms")
	resEcho, _ := armAckUntil(t, logger, pane, echoText, chEcho, 120*time.Second, "ack-echo-mode")

	var echoEnv robotAckSection
	if err := json.Unmarshal([]byte(resEcho.out), &echoEnv); err != nil {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("parse echo-mode envelope: %v (%s)", err, resEcho.out)
	}
	logger.LogJSON("echo_mode_envelope", echoEnv)
	if resEcho.exit != 0 || !echoEnv.Success || len(echoEnv.Confirmations) != 1 {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("echo mode must confirm: exit=%d %+v", resEcho.exit, echoEnv)
	}
	echoConf := echoEnv.Confirmations[0]
	if echoConf.Pane != "0" {
		t.Fatalf("echo-mode confirmation attributed to wrong pane: %+v", echoConf)
	}
	if echoConf.AckType != "echo_detected" {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("echo mode with matching --msg must confirm as echo_detected, got %q", echoConf.AckType)
	}
	// Ground truth: the fixture emitted the echoed text.
	if _, ok := pane.WaitForEvent("ack_emit", echoText, 10*time.Second); !ok {
		dumpScenarioFailure(t, logger, pane)
		t.Fatalf("fixture never emitted the echo-mode text")
	}
	logFixtureEventSummary(logger, "echo_vs_plain", pane)
	logger.Log("[PASS] plain=%s echo=%s in %s", plainConf.AckType, echoConf.AckType, time.Since(start).Round(time.Millisecond))
}
