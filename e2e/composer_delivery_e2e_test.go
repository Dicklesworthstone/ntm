//go:build e2e
// +build e2e

package e2e

// composer_delivery_e2e_test.go (bd-hy0f9) proves the v1.23.0
// delivery-verification stack live against the fakeagent TUI fixture
// (bd-kur07) inside real tmux sessions:
//
//  1. readiness refusal (bd-dp9oy): a gate screen refuses --robot-send with
//     the typed "not ready for delivery" error and ZERO keystrokes reach the
//     pane; dismissing the gate lets the same send succeed;
//  2. stranded-composer rescue (ntm-8ubn): strand 2 swallows both protocol
//     Enters, the verification rescue Enter submits, and the envelope still
//     reports success; strand 99 exhausts the rescue and the send fails
//     typed ("codex submission unconfirmed");
//  3. composer visibility (bd-v8dqd): --enter=false stages text and
//     --robot-is-working reports unsubmitted_input=true (false after
//     submit); a queued-messages footer in the capture tail flips
//     queued_messages, and the same footer buried >10 lines deep does not;
//  4. narrow-pane liveness (bd-eeifh): a 26-column codex pane in its work
//     window reports is_working=true / not SAFE_TO_RESTART, with 120-column
//     parity;
//  5. --verify-render: render_evidence is present and truthful in both the
//     delivered and the failed case.
//
// Every scenario asserts from BOTH the robot envelope and the fixture's
// JSONL event log / pane capture, so the envelopes cannot vouch for
// themselves.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Envelope shapes (local names; suite_test.go owns IsWorkingResult etc.)
// =============================================================================

type cdSendError struct {
	Pane  string `json:"pane"`
	Error string `json:"error"`
}

type cdRenderEvidence struct {
	Pane                 string `json:"pane"`
	Delivered            bool   `json:"delivered"`
	DeliveredAndRendered bool   `json:"delivered_and_rendered"`
	BaselineAvailable    bool   `json:"baseline_available"`
	RenderAvailable      bool   `json:"render_available"`
	RenderChanged        bool   `json:"render_changed"`
	CaptureError         string `json:"capture_error,omitempty"`
}

type cdSendEnvelope struct {
	Success        bool               `json:"success"`
	Error          string             `json:"error,omitempty"`
	ErrorCode      string             `json:"error_code,omitempty"`
	Targets        []string           `json:"targets"`
	Successful     []string           `json:"successful"`
	Failed         []cdSendError      `json:"failed"`
	RenderEvidence []cdRenderEvidence `json:"render_evidence,omitempty"`
}

func (e cdSendEnvelope) failedErrors() string {
	parts := make([]string, 0, len(e.Failed))
	for _, f := range e.Failed {
		parts = append(parts, f.Error)
	}
	return strings.Join(parts, " | ")
}

type cdPaneWork struct {
	AgentType        string `json:"agent_type"`
	IsWorking        bool   `json:"is_working"`
	IsIdle           bool   `json:"is_idle"`
	Recommendation   string `json:"recommendation"`
	SafeToDispatch   bool   `json:"safe_to_dispatch"`
	UnsubmittedInput bool   `json:"unsubmitted_input"`
	QueuedMessages   bool   `json:"queued_messages"`
}

type cdIsWorkingEnvelope struct {
	Success bool                  `json:"success"`
	Error   string                `json:"error,omitempty"`
	Session string                `json:"session"`
	Panes   map[string]cdPaneWork `json:"panes"`
}

type cdStatusSession struct {
	Name       string `json:"name"`
	AgentCount int    `json:"agent_count"`
}

type cdStatusEnvelope struct {
	Success       bool              `json:"success"`
	SchemaVersion string            `json:"schema_version"`
	Sessions      []cdStatusSession `json:"sessions"`
}

// =============================================================================
// Helpers
// =============================================================================

// cdInputEvents are fixture event names that can only be produced by bytes
// arriving on the fixture's stdin — the ground truth for "keystrokes reached
// the pane". Renders, control verbs, and ack emissions are excluded.
var cdInputEvents = map[string]bool{
	"key":                  true,
	"composer_change":      true,
	"paste_begin":          true,
	"paste_end":            true,
	"csi":                  true,
	"submit":               true,
	"submit_empty":         true,
	"swallow_strand":       true,
	"picker_swallow_enter": true,
}

// cdCountInputEventsSince counts input-derived fixture events after the
// baseline index into the event log.
func cdCountInputEventsSince(pane *fakeagentPane, baseline int) (int, []fakeagentEvent) {
	events := pane.Events()
	if baseline > len(events) {
		baseline = len(events)
	}
	var matched []fakeagentEvent
	for _, ev := range events[baseline:] {
		if cdInputEvents[ev.Event] {
			matched = append(matched, ev)
		}
	}
	return len(matched), matched
}

// cdDumpFixture records the fixture event log and a pane capture, the
// mandated failure-diagnosis artifacts.
func cdDumpFixture(logger *TestLogger, pane *fakeagentPane) {
	logger.LogJSON("fixture_events", pane.Events())
	if capture, err := tmux.CapturePaneOutput(pane.PaneID, 60); err == nil {
		logger.Log("[CAPTURE] pane %s:\n%s", pane.PaneID, capture)
	} else {
		logger.Log("[CAPTURE] pane %s capture failed: %v", pane.PaneID, err)
	}
}

func cdFatalf(t *testing.T, logger *TestLogger, pane *fakeagentPane, format string, args ...interface{}) {
	t.Helper()
	cdDumpFixture(logger, pane)
	t.Fatalf(format, args...)
}

func cdParseSendEnvelope(t *testing.T, logger *TestLogger, pane *fakeagentPane, out, label string) cdSendEnvelope {
	t.Helper()
	var envelope cdSendEnvelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		cdFatalf(t, logger, pane, "parse %s send envelope: %v (%s)", label, err, out)
	}
	logger.LogJSON(label+"_send_envelope", envelope)
	return envelope
}

// cdIsWorkingSolePane runs --robot-is-working for the fixture session and
// returns the single pane's status plus the parsed envelope.
func cdIsWorkingSolePane(t *testing.T, logger *TestLogger, pane *fakeagentPane, label string) cdPaneWork {
	t.Helper()
	out, exit := runNTMFixture(t, logger, "--robot-is-working="+pane.Session)
	if exit != 0 {
		cdFatalf(t, logger, pane, "%s: robot-is-working exit=%d output=%s", label, exit, out)
	}
	var envelope cdIsWorkingEnvelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		cdFatalf(t, logger, pane, "%s: parse is-working envelope: %v (%s)", label, err, out)
	}
	logger.LogJSON(label+"_is_working", envelope)
	if !envelope.Success {
		cdFatalf(t, logger, pane, "%s: is-working success=false error=%q", label, envelope.Error)
	}
	if len(envelope.Panes) != 1 {
		cdFatalf(t, logger, pane, "%s: expected exactly 1 pane in is-working envelope, got %d: %s", label, len(envelope.Panes), out)
	}
	for _, status := range envelope.Panes {
		return status
	}
	return cdPaneWork{} // unreachable
}

// =============================================================================
// Scenario 1: readiness refusal (bd-dp9oy)
// =============================================================================

// A pane showing a full-screen gate (no composer marker) must refuse
// --robot-send with the typed "not ready for delivery" error and send ZERO
// keystrokes; once the gate is dismissed the identical send succeeds.
func TestComposerDeliveryReadinessRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-readiness-refusal")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s persona=%s", pane.Session, pane.PaneID, pane.Persona)

	pane.Control("gate trust")
	if _, ok := pane.WaitForEvent("render", "trust", 5*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never rendered the trust gate")
	}
	baseline := len(pane.Events())
	logger.Log("[GATE] trust gate rendered; event baseline=%d", baseline)

	const msg = "readiness gate probe payload"
	start := time.Now()
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=claude")
	logger.Log("[TIMING] gated send took %s", time.Since(start).Round(time.Millisecond))
	if exit == 0 {
		cdFatalf(t, logger, pane, "gated send unexpectedly exited 0: %s", out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "gated")
	if envelope.Success || len(envelope.Successful) != 0 {
		cdFatalf(t, logger, pane, "gated send reported success: %s", out)
	}
	if len(envelope.Failed) != 1 || !strings.Contains(envelope.failedErrors(), "not ready for delivery") {
		cdFatalf(t, logger, pane, "gated send failure is not the typed readiness refusal: %s", out)
	}
	logger.Log("[PASS] typed refusal: %s", envelope.failedErrors())

	// Ground truth: the refusal must have fired BEFORE any keys were typed.
	if count, matched := cdCountInputEventsSince(pane, baseline); count != 0 {
		cdFatalf(t, logger, pane, "refused send still delivered %d input event(s) to the pane: %+v", count, matched)
	}
	logger.Log("[PASS] zero input events reached the fixture during the refused send")

	// Dismiss the gate the way a human would: one Enter directly to the tty.
	if _, err := tmux.DefaultClient.Run("send-keys", "-t", pane.PaneID, "Enter"); err != nil {
		cdFatalf(t, logger, pane, "send gate-dismiss Enter: %v", err)
	}
	if _, ok := pane.WaitForEvent("key", "enter", 5*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never observed the gate-dismiss Enter")
	}

	out, exit = runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=claude")
	if exit != 0 {
		cdFatalf(t, logger, pane, "post-dismiss send exit=%d output=%s", exit, out)
	}
	envelope = cdParseSendEnvelope(t, logger, pane, out, "post_dismiss")
	if !envelope.Success || len(envelope.Successful) != 1 {
		cdFatalf(t, logger, pane, "post-dismiss send not successful: %s", out)
	}
	if ev, ok := pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never logged the post-dismiss submit")
	} else {
		logger.LogJSON("post_dismiss_submit_event", ev)
	}
	logger.Log("[PASS] identical send succeeded once the gate was dismissed")
}

// =============================================================================
// Scenario 2: stranded-composer rescue (ntm-8ubn)
// =============================================================================

// strand 2 swallows both protocol Enters; submission verification detects
// the stranded payload and rescues it with exactly one extra Enter. Fixture
// ground truth: exactly 2 swallow_strand events, then exactly 1 submit
// carrying the message — while the envelope still reads delivered.
func TestComposerDeliveryStrandRescue(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-strand-rescue")
	defer logger.Close()

	pane := startFakeagentSession(t, "codex", 0, 0)
	pane.Control("strand 2")
	logger.Log("[SETUP] session=%s pane=%s armed strand 2", pane.Session, pane.PaneID)

	const msg = "stranded composer rescue payload"
	start := time.Now()
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=codex")
	logger.Log("[TIMING] rescued send took %s", time.Since(start).Round(time.Millisecond))
	if exit != 0 {
		cdFatalf(t, logger, pane, "rescued send exit=%d output=%s", exit, out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "rescued")
	if !envelope.Success || len(envelope.Successful) != 1 || len(envelope.Failed) != 0 {
		cdFatalf(t, logger, pane, "rescued send envelope wrong: %s", out)
	}

	if _, ok := pane.WaitForEvent("submit", msg, 20*time.Second); !ok {
		cdFatalf(t, logger, pane, "rescue never submitted the payload")
	}
	swallowed := pane.CountEvents("swallow_strand")
	submits := pane.CountEvents("submit")
	if swallowed != 2 {
		cdFatalf(t, logger, pane, "swallow_strand count = %d, want exactly 2 (both protocol Enters)", swallowed)
	}
	if submits != 1 {
		cdFatalf(t, logger, pane, "submit count = %d, want exactly 1 (the rescue Enter)", submits)
	}
	logger.Log("[PASS] 2 protocol Enters swallowed, 1 rescue submit, envelope delivered=submitted")
}

// strand 99 also swallows the rescue Enter, so submission stays unconfirmed
// after the verifier's polling: the envelope must report failure with the
// typed "codex submission unconfirmed" error, and the fixture must show the
// exact swallow count (2 protocol + 1 rescue = 3) with zero submits.
func TestComposerDeliveryStrandRescueExhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-strand-exhausted")
	defer logger.Close()

	pane := startFakeagentSession(t, "codex", 0, 0)
	pane.Control("strand 99")
	logger.Log("[SETUP] session=%s pane=%s armed strand 99", pane.Session, pane.PaneID)

	const msg = "unrescued strand payload"
	start := time.Now()
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=codex")
	logger.Log("[TIMING] exhausted send took %s", time.Since(start).Round(time.Millisecond))
	if exit == 0 {
		cdFatalf(t, logger, pane, "exhausted send unexpectedly exited 0: %s", out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "exhausted")
	if envelope.Success || len(envelope.Successful) != 0 {
		cdFatalf(t, logger, pane, "exhausted send reported success: %s", out)
	}
	if len(envelope.Failed) != 1 || !strings.Contains(envelope.failedErrors(), "codex submission unconfirmed") {
		cdFatalf(t, logger, pane, "exhausted send failure is not the typed unconfirmed-submission error: %s", out)
	}
	if !strings.Contains(envelope.Error, "1 of 1 sends failed") {
		cdFatalf(t, logger, pane, "envelope error = %q, want the '1 of 1 sends failed' summary", envelope.Error)
	}

	// Ground truth: both protocol Enters plus exactly one rescue Enter were
	// swallowed and nothing ever submitted.
	if swallowed := pane.CountEvents("swallow_strand"); swallowed != 3 {
		cdFatalf(t, logger, pane, "swallow_strand count = %d, want exactly 3 (2 protocol + 1 rescue)", swallowed)
	}
	if submits := pane.CountEvents("submit"); submits != 0 {
		cdFatalf(t, logger, pane, "submit count = %d, want 0 (submission must remain unconfirmed)", submits)
	}
	logger.Log("[PASS] rescue exhausted: typed failure, 3 swallows, 0 submits")
}

// =============================================================================
// Scenario 3: composer visibility (bd-v8dqd)
// =============================================================================

// --enter=false stages text without submitting; --robot-is-working must
// report unsubmitted_input=true while it sits there and false after a bare
// Enter submits it. A queued-messages footer within the capture tail flips
// queued_messages=true; the same footer buried more than 10 lines deep does
// not. --robot-status is probed for the session; NOTE (discrepancy vs the
// bead): the v2 status schema returns per-session headers only, so
// unsubmitted_input is intentionally asserted ABSENT there.
func TestComposerDeliveryComposerVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-visibility")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s persona=%s", pane.Session, pane.PaneID, pane.Persona)

	const msg = "staged composer visibility payload"
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=claude", "--enter=false")
	if exit != 0 {
		cdFatalf(t, logger, pane, "stage-only send exit=%d output=%s", exit, out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "stage_only")
	if !envelope.Success || len(envelope.Successful) != 1 {
		cdFatalf(t, logger, pane, "stage-only send not successful: %s", out)
	}
	if _, ok := pane.WaitForEvent("composer_change", msg, 10*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture composer never received the staged text")
	}
	if submits := pane.CountEvents("submit"); submits != 0 {
		cdFatalf(t, logger, pane, "stage-only send submitted (%d submit events), --enter=false must not submit", submits)
	}
	logger.Log("[GROUND-TRUTH] payload staged in composer, zero submits")

	status := cdIsWorkingSolePane(t, logger, pane, "staged")
	if !status.UnsubmittedInput {
		cdFatalf(t, logger, pane, "is-working unsubmitted_input=false while the composer visibly holds the payload")
	}
	logger.Log("[PASS] is-working reports unsubmitted_input=true for the staged payload")

	// --robot-status: the bead predicted per-agent unsubmitted_input here.
	// The ACTUAL v2 surface returns per-session headers with NO agent
	// detail, and is projection-store-backed: an ad-hoc tmux session that
	// was never collected into the runtime projection may not be listed at
	// all. Assert the schema-level truth (envelope succeeds, v2 schema, no
	// per-agent composer fields anywhere) and log what was observed.
	rawStatus, exit := runNTMFixture(t, logger, "--robot-status")
	if exit != 0 {
		cdFatalf(t, logger, pane, "robot-status exit=%d output=%s", exit, rawStatus)
	}
	var statusEnvelope cdStatusEnvelope
	if err := json.Unmarshal([]byte(rawStatus), &statusEnvelope); err != nil {
		cdFatalf(t, logger, pane, "parse robot-status envelope: %v (%s)", err, rawStatus)
	}
	logger.LogJSON("robot_status", statusEnvelope)
	if !statusEnvelope.Success {
		cdFatalf(t, logger, pane, "robot-status success=false: %s", rawStatus)
	}
	if statusEnvelope.SchemaVersion != "ntm.robot.status.v2" {
		cdFatalf(t, logger, pane, "robot-status schema_version = %q; header-only expectations below need re-verification", statusEnvelope.SchemaVersion)
	}
	if strings.Contains(rawStatus, `"unsubmitted_input"`) {
		cdFatalf(t, logger, pane, "robot-status now emits unsubmitted_input; update this test to assert its value (the bd-hy0f9 discrepancy note is obsolete)")
	}
	foundSession := false
	for _, sess := range statusEnvelope.Sessions {
		if sess.Name == pane.Session {
			foundSession = true
		}
	}
	logger.Log("[DISCREPANCY] robot-status v2 returns session headers only and reads the runtime projection store; unsubmitted_input is NOT exposed there (bead expected it) — it lives on --robot-is-working. Fixture session listed=%v (unlisted is normal: the ad-hoc session was never projected)", foundSession)

	// Queued-messages footer in the live tail flips queued_messages=true.
	pane.Control("ack Press up to edit queued messages")
	if _, ok := pane.WaitForEvent("ack_emit", "queued messages", 5*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never emitted the queued-messages footer")
	}
	status = cdIsWorkingSolePane(t, logger, pane, "queued_footer_live")
	if !status.QueuedMessages {
		cdFatalf(t, logger, pane, "is-working queued_messages=false while the queued footer is in the capture tail")
	}
	if !status.UnsubmittedInput {
		cdFatalf(t, logger, pane, "is-working lost unsubmitted_input while the payload is still staged")
	}
	logger.Log("[PASS] queued footer in the live tail => queued_messages=true (unsubmitted_input still true)")

	// Bury the footer >10 lines above the bottom: the tail-anchored scan
	// must stop matching it.
	for i := 0; i < 12; i++ {
		pane.Control(fmt.Sprintf("ack filler transcript line %02d", i))
	}
	if _, ok := pane.WaitForEvent("ack_emit", "filler transcript line 11", 10*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never emitted the last filler line")
	}
	status = cdIsWorkingSolePane(t, logger, pane, "queued_footer_buried")
	if status.QueuedMessages {
		cdFatalf(t, logger, pane, "is-working queued_messages=true although the footer is buried %d+ lines above the tail window", 12)
	}
	logger.Log("[PASS] buried queued footer => queued_messages=false")

	// A bare Enter submits the staged payload; visibility must clear.
	if _, err := tmux.DefaultClient.Run("send-keys", "-t", pane.PaneID, "Enter"); err != nil {
		cdFatalf(t, logger, pane, "send bare Enter: %v", err)
	}
	if ev, ok := pane.WaitForEvent("submit", msg, 10*time.Second); !ok {
		cdFatalf(t, logger, pane, "bare Enter never submitted the staged payload")
	} else {
		logger.LogJSON("submit_event", ev)
	}
	status = cdIsWorkingSolePane(t, logger, pane, "submitted")
	if status.UnsubmittedInput {
		cdFatalf(t, logger, pane, "is-working unsubmitted_input=true after the payload visibly submitted")
	}
	logger.Log("[PASS] unsubmitted_input=false after submission")
}

// =============================================================================
// Scenario 4: narrow-pane liveness (bd-eeifh live proof)
// =============================================================================

// A 26-column codex pane in its work window hard-wraps the working chrome
// ("esc to interrupt" splits across rows); --robot-is-working must still
// report is_working=true and must not recommend SAFE_TO_RESTART. The same
// assertions hold at 120 columns for parity.
func TestComposerDeliveryNarrowPaneLiveness(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-narrow-liveness")
	defer logger.Close()

	widths := []struct {
		name  string
		width int
	}{
		{name: "narrow26", width: 26},
		{name: "parity120", width: 120},
	}
	for _, tc := range widths {
		start := time.Now()
		pane := startFakeagentSession(t, "codex", tc.width, 40)
		logger.Log("[SETUP:%s] session=%s pane=%s width=%d", tc.name, pane.Session, pane.PaneID, tc.width)
		pane.Control("work 60")
		if _, ok := pane.WaitForEvent("control", "work 60", 5*time.Second); !ok {
			cdFatalf(t, logger, pane, "%s: fixture never consumed the work verb", tc.name)
		}
		// Let at least two render ticks paint the working chrome at the
		// real (possibly wrapping) width before observing.
		time.Sleep(1200 * time.Millisecond)

		status := cdIsWorkingSolePane(t, logger, pane, tc.name)
		if !status.IsWorking || status.IsIdle {
			cdFatalf(t, logger, pane, "%s: is_working=%v is_idle=%v, want working (bd-eeifh liveness)", tc.name, status.IsWorking, status.IsIdle)
		}
		if status.Recommendation == "SAFE_TO_RESTART" {
			cdFatalf(t, logger, pane, "%s: recommendation=SAFE_TO_RESTART for a visibly working pane", tc.name)
		}
		logger.Log("[PASS:%s] is_working=true recommendation=%s (%s)", tc.name, status.Recommendation, time.Since(start).Round(time.Millisecond))
	}
}

// =============================================================================
// Scenario 5: --verify-render evidence
// =============================================================================

// A delivered send with --verify-render must carry one render_evidence
// entry that is truthful end-to-end: delivered, baseline and post captures
// available, render changed, delivered_and_rendered=true.
func TestComposerDeliveryVerifyRender(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-verify-render")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s persona=%s", pane.Session, pane.PaneID, pane.Persona)

	const msg = "verify render evidence payload"
	start := time.Now()
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=claude", "--verify-render")
	logger.Log("[TIMING] verify-render send took %s", time.Since(start).Round(time.Millisecond))
	if exit != 0 {
		cdFatalf(t, logger, pane, "verify-render send exit=%d output=%s", exit, out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "verify_render")
	if !envelope.Success || len(envelope.Successful) != 1 {
		cdFatalf(t, logger, pane, "verify-render send not successful: %s", out)
	}
	if len(envelope.RenderEvidence) != 1 {
		cdFatalf(t, logger, pane, "render_evidence entries = %d, want 1: %s", len(envelope.RenderEvidence), out)
	}
	ev := envelope.RenderEvidence[0]
	if !ev.Delivered || !ev.DeliveredAndRendered || !ev.BaselineAvailable || !ev.RenderAvailable || !ev.RenderChanged || ev.CaptureError != "" {
		cdFatalf(t, logger, pane, "render evidence not truthful for a delivered send: %+v", ev)
	}
	if _, ok := pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		cdFatalf(t, logger, pane, "fixture never logged the submit behind the render evidence")
	}
	logger.Log("[PASS] delivered_and_rendered=true and the fixture confirms the submit")
}

// A failed send (strand 99 exhausts the rescue) with --verify-render must
// report the failure truthfully: success=false, the typed unconfirmed
// error, and a render_evidence entry with delivered=false and
// delivered_and_rendered=false even though the screen visibly changed
// (the stranded payload is sitting in the composer).
func TestComposerDeliveryVerifyRenderFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "composer-delivery-verify-render-failure")
	defer logger.Close()

	pane := startFakeagentSession(t, "codex", 0, 0)
	pane.Control("strand 99")
	logger.Log("[SETUP] session=%s pane=%s armed strand 99", pane.Session, pane.PaneID)

	const msg = "verify render failure payload"
	start := time.Now()
	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg="+msg, "--type=codex", "--verify-render")
	logger.Log("[TIMING] verify-render failure send took %s", time.Since(start).Round(time.Millisecond))
	if exit == 0 {
		cdFatalf(t, logger, pane, "failed verify-render send unexpectedly exited 0: %s", out)
	}
	envelope := cdParseSendEnvelope(t, logger, pane, out, "verify_render_failure")
	if envelope.Success || len(envelope.Successful) != 0 {
		cdFatalf(t, logger, pane, "failed verify-render send reported success: %s", out)
	}
	if !strings.Contains(envelope.failedErrors(), "codex submission unconfirmed") {
		cdFatalf(t, logger, pane, "failure is not the typed unconfirmed-submission error: %s", out)
	}
	if len(envelope.RenderEvidence) != 1 {
		cdFatalf(t, logger, pane, "render_evidence entries = %d, want 1: %s", len(envelope.RenderEvidence), out)
	}
	ev := envelope.RenderEvidence[0]
	if ev.Delivered || ev.DeliveredAndRendered {
		cdFatalf(t, logger, pane, "render evidence claims delivery for a failed send: %+v", ev)
	}
	if !ev.BaselineAvailable || !ev.RenderAvailable {
		cdFatalf(t, logger, pane, "render evidence captures unavailable on a live pane: %+v", ev)
	}
	// The screen DID change (the stranded payload renders in the composer);
	// the evidence must still refuse delivered_and_rendered without a
	// successful delivery — render change alone is not delivery.
	if !ev.RenderChanged {
		cdFatalf(t, logger, pane, "render_changed=false although the stranded payload visibly repainted the composer: %+v", ev)
	}
	// Ground truth: nothing submitted.
	if submits := pane.CountEvents("submit"); submits != 0 {
		cdFatalf(t, logger, pane, "submit count = %d, want 0 for the failed send", submits)
	}
	logger.Log("[PASS] failed send: delivered=false, delivered_and_rendered=false, render_changed=true (truthful), 0 submits")
}
