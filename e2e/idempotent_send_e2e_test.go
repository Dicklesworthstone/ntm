//go:build e2e
// +build e2e

package e2e

// idempotent_send_e2e_test.go (bd-7gnyg) exercises the durable idempotent
// send lifecycle (#245: --op-id claim/replay/conflict/takeover plus
// --robot-send-receipt) against a REAL tmux session running the fakeagent
// fixture (bd-kur07). Every scenario asserts both the robot envelope AND the
// fixture's own JSONL event log — the ground truth the store-level unit
// tests cannot observe: whether keystrokes actually landed in the pane.
//
// State isolation: --op-id requires the runtime projection store, and the
// e2e ntm binary would otherwise use the real user state DB. Every scenario
// therefore runs ntm with NTM_CONFIG pointing at a per-test temp config
// file; state.DefaultPath derives state.db from the NTM_CONFIG directory
// (see internal/state/store.go DefaultPath), so each scenario gets a fresh
// send_operations table. The frozen runNTMFixture helper cannot pass env,
// so this file carries its own exec wrapper (idemScenario.runRaw).
//
// Scenario 5 workaround (documented per the bead): the 10-minute staleness
// window is not overridable from outside the binary, and SIGKILLing ntm
// mid-dispatch is not deterministic. Instead the test runs a real send to
// completion, then rewinds the completed send_operations row back to
// in_progress via store.DB() — first with a fresh created_at (a live
// concurrent claimant: retry must report OPERATION_IN_PROGRESS), then with
// a created_at older than the window (a crashed claimant: retry must take
// the claim over and deliver again for real).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// =============================================================================
// Envelope views (decoded subsets of the robot JSON envelopes)
// =============================================================================

type idemAdmission struct {
	Target string `json:"target"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

type idemOperation struct {
	OperationID   string          `json:"operation_id"`
	Status        string          `json:"status"`
	Replayed      bool            `json:"replayed"`
	PayloadSHA256 string          `json:"payload_sha256"`
	PayloadBytes  int64           `json:"payload_bytes"`
	Admissions    []idemAdmission `json:"admissions"`
}

type idemSendEnvelope struct {
	Success    bool     `json:"success"`
	Error      string   `json:"error"`
	ErrorCode  string   `json:"error_code"`
	Hint       string   `json:"hint"`
	Session    string   `json:"session"`
	Targets    []string `json:"targets"`
	Successful []string `json:"successful"`
	Failed     []struct {
		Pane  string `json:"pane"`
		Error string `json:"error"`
	} `json:"failed"`
	Operation *idemOperation `json:"operation"`
}

type idemReceiptEnvelope struct {
	Success   bool           `json:"success"`
	Error     string         `json:"error"`
	ErrorCode string         `json:"error_code"`
	Session   string         `json:"session"`
	Operation *idemOperation `json:"operation"`
	Outcome   *struct {
		Success    bool     `json:"success"`
		Targets    []string `json:"targets"`
		Successful []string `json:"successful"`
		ErrorCode  string   `json:"error_code"`
	} `json:"outcome"`
}

// idemKeystrokeEvents are the fixture event names that can only be produced
// by bytes arriving on the pane's stdin — the ground-truth signal that NTM
// typed something. Renders and control-file events are deliberately excluded.
var idemKeystrokeEvents = []string{
	"paste_begin", "paste_end", "key", "composer_change",
	"submit", "submit_empty", "swallow_strand", "picker_swallow_enter", "csi",
}

func idemKeystrokeCount(p *fakeagentPane) int {
	total := 0
	for _, name := range idemKeystrokeEvents {
		total += p.CountEvents(name)
	}
	return total
}

// =============================================================================
// Scenario harness
// =============================================================================

type idemScenario struct {
	t       *testing.T
	logger  *TestLogger
	pane    *fakeagentPane
	cfgPath string
	dbPath  string
	store   *state.Store
	started time.Time
}

// newIdemScenario builds one isolated scenario: a fresh fakeagent tmux
// session (skips when tmux is unavailable) plus a fresh NTM_CONFIG dir so
// the ntm child process gets its own state.db.
func newIdemScenario(t *testing.T, name, persona string) *idemScenario {
	t.Helper()
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, name)
	pane := startFakeagentSession(t, persona, 0, 0)

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# bd-7gnyg isolated e2e config\n"), 0o644); err != nil {
		t.Fatalf("write isolated config: %v", err)
	}
	s := &idemScenario{
		t:       t,
		logger:  logger,
		pane:    pane,
		cfgPath: cfgPath,
		dbPath:  filepath.Join(cfgDir, "state.db"),
		started: time.Now(),
	}
	logger.Log("[SETUP] session=%s pane=%s persona=%s", pane.Session, pane.PaneID, pane.Persona)
	logger.Log("[SETUP] NTM_CONFIG=%s state_db=%s", cfgPath, s.dbPath)
	return s
}

func (s *idemScenario) close() {
	s.logger.Log("[TIMING] scenario wall time: %s", time.Since(s.started).Round(time.Millisecond))
	s.logFixtureSummary("final")
	s.logger.Close()
}

// runRaw invokes the e2e ntm binary with the scenario's isolated NTM_CONFIG.
// Local variant of the frozen runNTMFixture, which cannot pass env.
func (s *idemScenario) runRaw(args ...string) (stdout, stderr string, exit int) {
	s.t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		s.t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "NTM_CONFIG="+s.cfgPath)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	start := time.Now()
	runErr := cmd.Run()
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			s.t.Fatalf("run ntm %v: %v", args, runErr)
		}
	}
	s.logger.Log("[NTM] args=%v exit=%d duration=%s", args, exit, time.Since(start).Round(time.Millisecond))
	s.logger.Log("[NTM] stdout=%s", strings.TrimSpace(outBuf.String()))
	if errBuf.Len() > 0 {
		s.logger.Log("[NTM] stderr=%s", strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), errBuf.String(), exit
}

// send runs a --robot-send invocation and decodes the envelope, logging the
// FULL raw envelope (not just the typed subset) for the audit trail.
func (s *idemScenario) send(args ...string) (idemSendEnvelope, int) {
	s.t.Helper()
	stdout, stderr, exit := s.runRaw(args...)
	var full map[string]any
	if err := json.Unmarshal([]byte(stdout), &full); err != nil {
		s.t.Fatalf("parse send envelope: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	s.logger.LogJSON("send_envelope", full)
	var env idemSendEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		s.t.Fatalf("decode send envelope subset: %v", err)
	}
	return env, exit
}

// receipt runs --robot-send-receipt and decodes the envelope.
func (s *idemScenario) receipt(opID string) (idemReceiptEnvelope, int) {
	s.t.Helper()
	stdout, stderr, exit := s.runRaw("--robot-send-receipt=" + opID)
	var full map[string]any
	if err := json.Unmarshal([]byte(stdout), &full); err != nil {
		s.t.Fatalf("parse receipt envelope: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	s.logger.LogJSON("receipt_envelope", full)
	var env idemReceiptEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		s.t.Fatalf("decode receipt envelope subset: %v", err)
	}
	return env, exit
}

// getStore opens the SAME state.db path the ntm child derives from
// NTM_CONFIG, so the test can read (and, for scenario 5, seed) the durable
// send_operations rows. Migrations are idempotent across processes.
func (s *idemScenario) getStore() *state.Store {
	s.t.Helper()
	if s.store == nil {
		st, err := state.Open(s.dbPath)
		if err != nil {
			s.t.Fatalf("open isolated state db %s: %v", s.dbPath, err)
		}
		if err := st.Migrate(); err != nil {
			st.Close()
			s.t.Fatalf("migrate isolated state db: %v", err)
		}
		s.store = st
		s.t.Cleanup(func() { st.Close() })
	}
	return s.store
}

func (s *idemScenario) row(opID string) *state.SendOperation {
	s.t.Helper()
	row, err := s.getStore().GetSendOperation(opID, s.pane.Session)
	if err != nil {
		s.t.Fatalf("read send_operations row for %s: %v", opID, err)
	}
	return row
}

func (s *idemScenario) logFixtureSummary(label string) {
	counts := map[string]int{}
	for _, ev := range s.pane.Events() {
		counts[ev.Event]++
	}
	s.logger.LogJSON("fixture_event_counts_"+label, counts)
}

// failf dumps the state row, fixture event summary, and pane content before
// failing — the bead's automatic-capture-on-failure requirement.
func (s *idemScenario) failf(opID string, format string, args ...any) {
	s.t.Helper()
	if opID != "" {
		if row, err := s.getStore().GetSendOperation(opID, s.pane.Session); err == nil {
			s.logger.LogJSON("state_row_on_failure", row)
		} else {
			s.logger.Log("[FAIL] state row read error: %v", err)
		}
	}
	s.logFixtureSummary("on_failure")
	if capture, err := tmux.CapturePaneOutput(s.pane.PaneID, 40); err == nil {
		s.logger.Log("[FAIL] pane capture:\n%s", capture)
	}
	s.t.Fatalf(format, args...)
}

// addIdemFakeagentPane splits a second fixture pane into the scenario's
// session and titles it <session>__cc_<index>. The frozen harness only
// creates single-pane sessions, so the multi-pane topology needed by
// scenario 4 is assembled here with raw tmux.
func addIdemFakeagentPane(t *testing.T, base *fakeagentPane, index int) *fakeagentPane {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	existing := map[string]bool{}
	panes, err := tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes before split: %v", err)
	}
	for _, p := range panes {
		existing[p.ID] = true
	}

	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")
	launch := fmt.Sprintf("%s --persona=claude --control=%s --log=%s",
		tmux.ShellQuote(bin), tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-h", "-t", base.PaneID, launch); err != nil {
		t.Fatalf("split second fakeagent pane: %v", err)
	}
	panes, err = tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes after split: %v", err)
	}
	newID := ""
	for _, p := range panes {
		if !existing[p.ID] {
			newID = p.ID
			break
		}
	}
	if newID == "" {
		t.Fatalf("second fakeagent pane not found after split (have %d panes)", len(panes))
	}
	title := fmt.Sprintf("%s__cc_%d", base.Session, index)
	if err := tmux.SetPaneTitle(newID, title); err != nil {
		t.Fatalf("title second fakeagent pane: %v", err)
	}
	pane := &fakeagentPane{
		t:           t,
		Session:     base.Session,
		PaneID:      newID,
		Persona:     "claude",
		controlPath: controlPath,
		logPath:     logPath,
	}
	if _, ok := pane.WaitForEvent("start", "", 10*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(newID, 40)
		t.Fatalf("second fakeagent pane did not start; pane shows:\n%s", capture)
	}
	return pane
}

func idemSHA256(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// =============================================================================
// Scenario 1: first send with --op-id — delivery, completed operation block,
// submitted admissions, and a durable receipt.
// =============================================================================

func TestIdempotentSendFirstSendCompletesAndReceipt(t *testing.T) {
	s := newIdemScenario(t, "idem-send-1-first-send", "claude")
	defer s.close()

	opID := fmt.Sprintf("e2e-idem-first-%d", time.Now().UnixNano())
	msg := "idem first send " + opID
	env, exit := s.send("--robot-send="+s.pane.Session, "--msg="+msg, "--type=claude", "--op-id="+opID)
	if exit != 0 || !env.Success {
		s.failf(opID, "first send failed: exit=%d success=%v error=%q code=%q", exit, env.Success, env.Error, env.ErrorCode)
	}
	if len(env.Targets) != 1 || len(env.Successful) != 1 {
		s.failf(opID, "targets=%v successful=%v, want exactly one of each", env.Targets, env.Successful)
	}
	if env.Operation == nil {
		s.failf(opID, "envelope has no operation block despite --op-id")
	}
	if env.Operation.OperationID != opID || env.Operation.Status != state.SendOperationCompleted || env.Operation.Replayed {
		s.failf(opID, "operation = %+v, want id=%s status=completed replayed=false", env.Operation, opID)
	}
	if len(env.Operation.Admissions) != 1 || env.Operation.Admissions[0].State != "submitted" {
		s.failf(opID, "admissions = %+v, want exactly one 'submitted'", env.Operation.Admissions)
	}
	// No CASS injection: the delivered payload must be the caller's message.
	if wantSHA := idemSHA256(msg); env.Operation.PayloadSHA256 != wantSHA || env.Operation.PayloadBytes != int64(len(msg)) {
		s.failf(opID, "payload digest = %s/%d, want %s/%d", env.Operation.PayloadSHA256, env.Operation.PayloadBytes, wantSHA, len(msg))
	}

	// Ground truth: the fixture logged the actual submission.
	if ev, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture never logged the submit; events=%+v", s.pane.Events())
	} else {
		s.logger.LogJSON("fixture_submit_event", ev)
	}
	if got := s.pane.CountEvents("submit"); got != 1 {
		s.failf(opID, "fixture submit count = %d, want 1", got)
	}

	// Ground truth: the durable row is completed and matches the envelope.
	row := s.row(opID)
	s.logger.LogJSON("state_row_after_first_send", row)
	if row == nil || row.Status != state.SendOperationCompleted || row.PayloadSHA256 != env.Operation.PayloadSHA256 {
		s.failf(opID, "state row = %+v, want completed with payload sha %s", row, env.Operation.PayloadSHA256)
	}

	// Receipt query returns the recorded outcome.
	receipt, rexit := s.receipt(opID)
	if rexit != 0 || !receipt.Success {
		s.failf(opID, "receipt query failed: exit=%d success=%v error=%q", rexit, receipt.Success, receipt.Error)
	}
	if receipt.Session != s.pane.Session {
		s.failf(opID, "receipt session = %q, want %q", receipt.Session, s.pane.Session)
	}
	if receipt.Operation == nil || receipt.Operation.Status != state.SendOperationCompleted {
		s.failf(opID, "receipt operation = %+v, want completed", receipt.Operation)
	}
	if len(receipt.Operation.Admissions) != 1 || receipt.Operation.Admissions[0].State != "submitted" {
		s.failf(opID, "receipt admissions = %+v, want one 'submitted'", receipt.Operation.Admissions)
	}
	if receipt.Outcome == nil || !receipt.Outcome.Success || len(receipt.Outcome.Successful) != 1 {
		s.failf(opID, "receipt outcome = %+v, want recorded success with one target", receipt.Outcome)
	}
	s.logger.Log("[PASS] first send delivered, recorded, and receipt-queryable")
}

// =============================================================================
// Scenario 2: byte-identical retry replays the recorded outcome with NO
// second submission observed in the fixture event log.
// =============================================================================

func TestIdempotentSendIdenticalRetryReplays(t *testing.T) {
	s := newIdemScenario(t, "idem-send-2-identical-retry", "claude")
	defer s.close()

	opID := fmt.Sprintf("e2e-idem-retry-%d", time.Now().UnixNano())
	msg := "idem retry payload " + opID
	args := []string{"--robot-send=" + s.pane.Session, "--msg=" + msg, "--type=claude", "--op-id=" + opID}

	env, exit := s.send(args...)
	if exit != 0 || !env.Success || env.Operation == nil || env.Operation.Replayed {
		s.failf(opID, "first send failed or was unexpectedly replayed: exit=%d env=%+v", exit, env)
	}
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture never logged the first submit")
	}
	baselineSubmits := s.pane.CountEvents("submit")
	baselineKeystrokes := idemKeystrokeCount(s.pane)
	s.logger.Log("[BASELINE] submits=%d keystroke_events=%d", baselineSubmits, baselineKeystrokes)

	retry, retryExit := s.send(args...) // byte-identical
	if retryExit != 0 || !retry.Success {
		s.failf(opID, "identical retry failed: exit=%d error=%q code=%q", retryExit, retry.Error, retry.ErrorCode)
	}
	if retry.Operation == nil || !retry.Operation.Replayed || retry.Operation.Status != state.SendOperationCompleted {
		s.failf(opID, "retry operation = %+v, want replayed=true status=completed", retry.Operation)
	}
	if len(retry.Successful) != 1 {
		s.failf(opID, "retry successful = %v, want the original single target replayed", retry.Successful)
	}

	// Ground truth the unit tests cannot make: NOTHING was typed again.
	time.Sleep(1500 * time.Millisecond) // grace for any (forbidden) late keystrokes
	if got := s.pane.CountEvents("submit"); got != baselineSubmits {
		s.failf(opID, "fixture submit count = %d after replay, want %d (no second submission)", got, baselineSubmits)
	}
	if got := idemKeystrokeCount(s.pane); got != baselineKeystrokes {
		s.failf(opID, "fixture keystroke events = %d after replay, want %d (zero new keystrokes)", got, baselineKeystrokes)
	}
	s.logger.Log("[PASS] replayed=true with zero new fixture keystrokes")
}

// =============================================================================
// Scenario 3: conflicting reuse (different --msg, same --op-id) is rejected
// with IDEMPOTENCY_CONFLICT and zero new fixture keystrokes.
// =============================================================================

func TestIdempotentSendConflictingReuseRejected(t *testing.T) {
	s := newIdemScenario(t, "idem-send-3-conflict", "claude")
	defer s.close()

	opID := fmt.Sprintf("e2e-idem-conflict-%d", time.Now().UnixNano())
	msg := "idem conflict original " + opID
	env, exit := s.send("--robot-send="+s.pane.Session, "--msg="+msg, "--type=claude", "--op-id="+opID)
	if exit != 0 || !env.Success {
		s.failf(opID, "first send failed: exit=%d error=%q", exit, env.Error)
	}
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture never logged the first submit")
	}
	baselineKeystrokes := idemKeystrokeCount(s.pane)
	originalSHA := env.Operation.PayloadSHA256

	conflict, conflictExit := s.send("--robot-send="+s.pane.Session, "--msg=DIFFERENT payload reusing "+opID, "--type=claude", "--op-id="+opID)
	if conflict.Success {
		s.failf(opID, "conflicting reuse reported success: %+v", conflict)
	}
	if conflict.ErrorCode != "IDEMPOTENCY_CONFLICT" {
		s.failf(opID, "conflict error_code = %q, want IDEMPOTENCY_CONFLICT (error=%q)", conflict.ErrorCode, conflict.Error)
	}
	if conflictExit == 0 {
		s.failf(opID, "conflicting reuse exited 0, want nonzero")
	}
	if conflict.Operation == nil || conflict.Operation.PayloadSHA256 != originalSHA {
		s.failf(opID, "conflict envelope operation = %+v, want the ORIGINAL recorded operation (sha %s)", conflict.Operation, originalSHA)
	}

	// Ground truth: zero new keystrokes reached the pane.
	time.Sleep(1500 * time.Millisecond)
	if got := idemKeystrokeCount(s.pane); got != baselineKeystrokes {
		s.failf(opID, "fixture keystroke events = %d after conflict, want %d", got, baselineKeystrokes)
	}
	// Durable row untouched: still the original completed outcome.
	row := s.row(opID)
	s.logger.LogJSON("state_row_after_conflict", row)
	if row == nil || row.Status != state.SendOperationCompleted || row.PayloadSHA256 != originalSHA {
		s.failf(opID, "state row mutated by conflicting reuse: %+v", row)
	}
	s.logger.Log("[PASS] conflict rejected, zero keystrokes, durable row untouched")
}

// =============================================================================
// Scenario 4: selector-based retry after topology change. --all binds the
// SELECTOR, not the resolved panes, so killing a pane between attempts must
// still yield a replay — never a conflict.
// =============================================================================

func TestIdempotentSendSelectorRetrySurvivesTopologyChange(t *testing.T) {
	s := newIdemScenario(t, "idem-send-4-topology", "claude")
	defer s.close()
	pane2 := addIdemFakeagentPane(t, s.pane, 2)
	s.logger.Log("[SETUP] second fixture pane=%s title=%s__cc_2", pane2.PaneID, s.pane.Session)

	opID := fmt.Sprintf("e2e-idem-topo-%d", time.Now().UnixNano())
	msg := "idem topology payload " + opID
	args := []string{"--robot-send=" + s.pane.Session, "--msg=" + msg, "--all", "--op-id=" + opID}

	env, exit := s.send(args...)
	if exit != 0 || !env.Success {
		s.failf(opID, "broadcast send failed: exit=%d error=%q", exit, env.Error)
	}
	if len(env.Targets) != 2 || len(env.Successful) != 2 {
		s.failf(opID, "targets=%v successful=%v, want both panes", env.Targets, env.Successful)
	}
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "pane 1 fixture never logged the submit")
	}
	if _, ok := pane2.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "pane 2 fixture never logged the submit; events=%+v", pane2.Events())
	}
	pane1Submits := s.pane.CountEvents("submit")

	// Topology change: pane 2 dies between attempts.
	if _, err := tmux.DefaultClient.Run("kill-pane", "-t", pane2.PaneID); err != nil {
		s.failf(opID, "kill second pane: %v", err)
	}
	s.logger.Log("[TOPOLOGY] killed pane %s; retrying byte-identical --all send", pane2.PaneID)

	retry, retryExit := s.send(args...) // byte-identical, one pane fewer alive
	if retry.ErrorCode == "IDEMPOTENCY_CONFLICT" {
		s.failf(opID, "selector retry after topology change was rejected as a conflict: %+v", retry)
	}
	if retryExit != 0 || !retry.Success {
		s.failf(opID, "selector retry failed: exit=%d error=%q code=%q", retryExit, retry.Error, retry.ErrorCode)
	}
	if retry.Operation == nil || !retry.Operation.Replayed {
		s.failf(opID, "retry operation = %+v, want replayed=true (selector binding)", retry.Operation)
	}
	if len(retry.Successful) != 2 {
		s.failf(opID, "replayed successful = %v, want the ORIGINAL two-target outcome", retry.Successful)
	}
	// Ground truth: the surviving pane received nothing new.
	time.Sleep(1500 * time.Millisecond)
	if got := s.pane.CountEvents("submit"); got != pane1Submits {
		s.failf(opID, "surviving pane submit count = %d after replay, want %d", got, pane1Submits)
	}
	s.logger.Log("[PASS] --all retry replayed (not conflicted) across a topology change")
}

// =============================================================================
// Scenario 5: in-progress claim and stale-claim takeover. See the file
// header for why the row is seeded via store.DB() instead of a mid-dispatch
// SIGKILL: the staleness window is not externally overridable, so the test
// reconstructs the crashed-claimant row states directly in the SAME isolated
// state.db the ntm child uses.
// =============================================================================

func TestIdempotentSendInProgressAndStaleTakeover(t *testing.T) {
	s := newIdemScenario(t, "idem-send-5-takeover", "claude")
	defer s.close()

	opID := fmt.Sprintf("e2e-idem-takeover-%d", time.Now().UnixNano())
	msg := "idem takeover payload " + opID
	args := []string{"--robot-send=" + s.pane.Session, "--msg=" + msg, "--type=claude", "--op-id=" + opID}

	env, exit := s.send(args...)
	if exit != 0 || !env.Success {
		s.failf(opID, "first send failed: exit=%d error=%q", exit, env.Error)
	}
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture never logged the first submit")
	}
	if got := s.pane.CountEvents("submit"); got != 1 {
		s.failf(opID, "fixture submit count = %d after first send, want 1", got)
	}
	s.logger.LogJSON("state_row_after_first_send", s.row(opID))

	// Rewind: a LIVE concurrent claimant (in_progress, fresh created_at).
	store := s.getStore()
	rewind := func(createdAt time.Time, label string) {
		s.t.Helper()
		res, err := store.DB().Exec(`
			UPDATE send_operations
			SET status = ?, outcome_json = '', completed_at = NULL, created_at = ?
			WHERE operation_id = ? AND session_name = ?`,
			state.SendOperationInProgress, createdAt.UTC(), opID, s.pane.Session,
		)
		if err != nil {
			s.failf(opID, "rewind row (%s): %v", label, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			s.failf(opID, "rewind row (%s) affected %d rows, want 1", label, n)
		}
		s.logger.Log("[SEED] row rewound to in_progress created_at=%s (%s)", createdAt.UTC().Format(time.RFC3339), label)
		s.logger.LogJSON("state_row_seeded_"+label, s.row(opID))
	}

	rewind(time.Now().UTC(), "fresh_claim")
	inProgress, inProgressExit := s.send(args...)
	if inProgress.Success || inProgress.ErrorCode != "OPERATION_IN_PROGRESS" {
		s.failf(opID, "retry against fresh claim = success=%v code=%q, want OPERATION_IN_PROGRESS", inProgress.Success, inProgress.ErrorCode)
	}
	if inProgressExit == 0 {
		s.failf(opID, "in-progress retry exited 0, want nonzero")
	}
	if inProgress.Operation == nil || inProgress.Operation.Status != state.SendOperationInProgress {
		s.failf(opID, "in-progress operation = %+v, want status=in_progress", inProgress.Operation)
	}
	for _, adm := range inProgress.Operation.Admissions {
		if adm.State != "unknown" {
			s.failf(opID, "in-progress admission = %+v, want state=unknown", adm)
		}
	}
	// Ground truth: the blocked retry typed nothing.
	if got := s.pane.CountEvents("submit"); got != 1 {
		s.failf(opID, "fixture submit count = %d after in-progress refusal, want still 1", got)
	}
	s.logger.Log("[PASS-5a] fresh in_progress claim refused with OPERATION_IN_PROGRESS, no delivery")

	// Rewind again: a CRASHED claimant (created_at beyond the 10-min window).
	staleCreatedAt := time.Now().UTC().Add(-11 * time.Minute)
	rewind(staleCreatedAt, "stale_claim")
	takeover, takeoverExit := s.send(args...)
	if takeoverExit != 0 || !takeover.Success {
		s.failf(opID, "stale takeover retry failed: exit=%d error=%q code=%q", takeoverExit, takeover.Error, takeover.ErrorCode)
	}
	if takeover.Operation == nil || takeover.Operation.Replayed || takeover.Operation.Status != state.SendOperationCompleted {
		s.failf(opID, "takeover operation = %+v, want a FRESH completed execution (replayed=false)", takeover.Operation)
	}
	// Ground truth: the takeover really delivered a second time.
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture has no submit events after takeover")
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.pane.CountEvents("submit") < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := s.pane.CountEvents("submit"); got != 2 {
		s.failf(opID, "fixture submit count = %d after takeover, want 2 (a real second delivery)", got)
	}
	row := s.row(opID)
	s.logger.LogJSON("state_row_after_takeover", row)
	if row == nil || row.Status != state.SendOperationCompleted {
		s.failf(opID, "state row = %+v, want completed after takeover", row)
	}
	// TakeOverStaleSendOperation refreshes created_at; a row still carrying
	// the stale seed means the takeover path never ran.
	if !row.CreatedAt.After(staleCreatedAt.Add(10 * time.Minute)) {
		s.failf(opID, "row created_at = %v, want refreshed past the stale seed %v", row.CreatedAt, staleCreatedAt)
	}
	s.logger.Log("[PASS-5b] stale claim taken over and executed fresh (2nd submit observed)")
}

// =============================================================================
// Scenario 6: a preflight failure AFTER the claim releases it, so the
// operation ID is immediately reusable. Seam (same as the unit tests):
// a pane titled <session>__grok_1 fails dispatch Prepare with
// prompt_delivery_unsupported (NOT_IMPLEMENTED) after the claim succeeded.
// =============================================================================

func TestIdempotentSendPreflightFailureReleasesClaim(t *testing.T) {
	s := newIdemScenario(t, "idem-send-6-preflight-release", "claude")
	defer s.close()

	opID := fmt.Sprintf("e2e-idem-preflight-%d", time.Now().UnixNano())
	msg := "idem preflight payload " + opID
	args := []string{"--robot-send=" + s.pane.Session, "--msg=" + msg, "--pane=" + s.pane.PaneID, "--op-id=" + opID}

	// Retitle the fixture pane as a grok agent: prompt delivery unsupported.
	if err := tmux.SetPaneTitle(s.pane.PaneID, s.pane.Session+"__grok_1"); err != nil {
		s.failf(opID, "retitle pane as grok: %v", err)
	}
	baselineKeystrokes := idemKeystrokeCount(s.pane)

	failed, failedExit := s.send(args...)
	if failed.Success {
		s.failf(opID, "send to grok-titled pane succeeded, want a preflight failure: %+v", failed)
	}
	if failed.ErrorCode != "NOT_IMPLEMENTED" {
		s.failf(opID, "preflight error_code = %q, want NOT_IMPLEMENTED (error=%q)", failed.ErrorCode, failed.Error)
	}
	if failedExit == 0 {
		s.failf(opID, "preflight failure exited 0, want nonzero")
	}
	// Ground truth: nothing was typed into the pane.
	time.Sleep(1500 * time.Millisecond)
	if got := idemKeystrokeCount(s.pane); got != baselineKeystrokes {
		s.failf(opID, "fixture keystroke events = %d after preflight failure, want %d", got, baselineKeystrokes)
	}
	// The claim must have been RELEASED: no durable row remains.
	if row := s.row(opID); row != nil {
		s.failf(opID, "state row = %+v, want claim released (no row) after preflight failure", row)
	}
	s.logger.Log("[PASS-6a] preflight failure after claim: NOT_IMPLEMENTED, zero keystrokes, claim released")

	// The op-id is immediately reusable on a valid pane.
	if err := tmux.SetPaneTitle(s.pane.PaneID, s.pane.Session+"__cc_1"); err != nil {
		s.failf(opID, "restore claude pane title: %v", err)
	}
	retry, retryExit := s.send(args...) // byte-identical, now against a valid target
	if retryExit != 0 || !retry.Success {
		s.failf(opID, "reuse after release failed: exit=%d error=%q code=%q", retryExit, retry.Error, retry.ErrorCode)
	}
	if retry.Operation == nil || retry.Operation.Replayed || retry.Operation.Status != state.SendOperationCompleted {
		s.failf(opID, "reused operation = %+v, want a fresh completed execution", retry.Operation)
	}
	if _, ok := s.pane.WaitForEvent("submit", msg, 15*time.Second); !ok {
		s.failf(opID, "fixture never logged the submit after reuse; events=%+v", s.pane.Events())
	}
	if got := s.pane.CountEvents("submit"); got != 1 {
		s.failf(opID, "fixture submit count = %d after reuse, want exactly 1", got)
	}
	row := s.row(opID)
	s.logger.LogJSON("state_row_after_reuse", row)
	if row == nil || row.Status != state.SendOperationCompleted {
		s.failf(opID, "state row = %+v, want completed after reuse", row)
	}
	s.logger.Log("[PASS-6b] released op-id immediately reusable; fresh claim delivered for real")
}
