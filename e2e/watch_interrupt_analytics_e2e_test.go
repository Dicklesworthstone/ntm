//go:build e2e
// +build e2e

package e2e

// watch_interrupt_analytics_e2e_test.go — live proofs for the W3
// watch/interrupt lane (bd-ws7-docs-ux-truth-tqh3l.4 + .6), built on the
// bd-kur07 fakeagent harness per the bd-h4t0j fixture contract (liveness-
// compliant, composer-glyph-rendering fake agents; NOT the stale
// canonical-pane fixtures).
//
//   - H6: `ntm interrupt --json` is best-effort per pane. One pane killed
//     mid-interrupt (deterministically, via an NTM_TMUX_BINARY fault
//     injector that kills the pane on its send-keys call and fails like
//     tmux does) must NOT stop the sweep: the remaining fixtures still log
//     the C-c keystroke, the envelope lists the one failure with
//     error_code PARTIAL_INTERRUPT, and the process exits 1 (the repo
//     exit-code contract's "partial = error" value).
//   - H4: `ntm activity --watch --json` and `ntm watch --json` stream
//     NDJSON frames (previously --json was silently ignored).
//   - H4: analytics chars_sent EQUALS the payload's byte count (exact
//     value, multibyte payload), accumulates across sends, and an agent
//     registered via `ntm add` is counted and attributed — not lumped
//     into the spawn-created agent types.

import (
	"bufio"
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

// addFakeagentSplitPane splits a new fakeagent pane into an existing fixture
// session, titled "<session>__cc_<n>" so NTM detects a Claude agent.
func addFakeagentSplitPane(t *testing.T, base *fakeagentPane, n int) *fakeagentPane {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")

	before, err := tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes before split: %v", err)
	}
	known := make(map[string]bool, len(before))
	for _, p := range before {
		known[p.ID] = true
	}

	launch := fmt.Sprintf("%s --persona=claude --control=%s --log=%s",
		tmux.ShellQuote(bin), tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", base.Session+":0", launch); err != nil {
		t.Fatalf("split fakeagent pane: %v", err)
	}

	after, err := tmux.GetPanes(base.Session)
	if err != nil {
		t.Fatalf("enumerate panes after split: %v", err)
	}
	var paneID string
	for _, p := range after {
		if !known[p.ID] {
			paneID = p.ID
			break
		}
	}
	if paneID == "" {
		t.Fatalf("split pane not found (before=%d after=%d)", len(before), len(after))
	}
	title := fmt.Sprintf("%s__cc_%d", base.Session, n)
	if err := tmux.SetPaneTitle(paneID, title); err != nil {
		t.Fatalf("title split pane: %v", err)
	}

	pane := &fakeagentPane{
		t:           t,
		Session:     base.Session,
		PaneID:      paneID,
		Persona:     "claude",
		controlPath: controlPath,
		logPath:     logPath,
	}
	if _, ok := pane.WaitForEvent("start", "", 10*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(paneID, 40)
		t.Fatalf("split fakeagent did not start; pane shows:\n%s", capture)
	}
	return pane
}

// writeInterruptFaultInjector writes a tmux wrapper that behaves exactly like
// the real tmux except that the FIRST send-keys aimed at doomedPaneID kills
// that pane (the "pane killed mid-interrupt" scenario) and then fails the
// way tmux fails when a pane vanishes between enumeration and delivery.
func writeInterruptFaultInjector(t *testing.T, dir, realTmux, doomedPaneID string) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/bash
real=%s
doomed=%q
if [ "$1" = "send-keys" ]; then
  for a in "$@"; do
    if [ "$a" = "$doomed" ]; then
      "$real" kill-pane -t "$doomed" >/dev/null 2>&1
      echo "can't find pane: $doomed" >&2
      exit 1
    fi
  done
fi
exec "$real" "$@"
`, tmux.ShellQuote(realTmux), doomedPaneID)
	path := filepath.Join(dir, "tmux-fault-injector.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fault injector: %v", err)
	}
	return path
}

// H6 proof: kill one pane mid-interrupt → the other panes are STILL
// interrupted, the envelope lists the one failure per-pane, and the exit
// code is the documented partial-failure value (1, per the AGENTS.md robot
// exit-code contract: any success:false envelope exits nonzero).
func TestInterruptE2EBestEffortPartialFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "interrupt-best-effort-partial")
	defer logger.Close()

	pane1 := startFakeagentSession(t, "claude", 0, 0)
	pane2 := addFakeagentSplitPane(t, pane1, 2)
	pane3 := addFakeagentSplitPane(t, pane1, 3)
	fixtures := map[string]*fakeagentPane{
		pane1.PaneID: pane1,
		pane2.PaneID: pane2,
		pane3.PaneID: pane3,
	}
	logger.Log("[SETUP] session=%s panes=%s,%s,%s", pane1.Session, pane1.PaneID, pane2.PaneID, pane3.PaneID)

	// Doom the pane the interrupt loop will visit in the MIDDLE of its
	// sweep, so surviving panes exist on both sides of the failure and
	// "kept going after the error" is actually proven.
	panes, err := tmux.GetPanes(pane1.Session)
	if err != nil || len(panes) != 3 {
		t.Fatalf("enumerate panes: err=%v n=%d", err, len(panes))
	}
	doomedID := panes[1].ID
	survivors := []*fakeagentPane{fixtures[panes[0].ID], fixtures[panes[2].ID]}
	if survivors[0] == nil || survivors[1] == nil || fixtures[doomedID] == nil {
		t.Fatalf("pane bookkeeping mismatch: order=%v", []string{panes[0].ID, panes[1].ID, panes[2].ID})
	}
	logger.Log("[SETUP] doomed(middle)=%s survivors=%s,%s", doomedID, panes[0].ID, panes[2].ID)

	realTmux := tmux.BinaryPath()
	injector := writeInterruptFaultInjector(t, t.TempDir(), realTmux, doomedID)

	out, exit := gatesRunNTM(t, logger, []string{"NTM_TMUX_BINARY=" + injector},
		"interrupt", pane1.Session, "--json")

	// Exit-code contract: partial failure is a failure → exit 1.
	if exit != 1 {
		t.Fatalf("interrupt exit=%d, want 1 (partial failure); output=%s", exit, out)
	}

	var envelope struct {
		Success     bool   `json:"success"`
		Interrupted int    `json:"interrupted"`
		Failed      int    `json:"failed"`
		ErrorCode   string `json:"error_code"`
		Error       string `json:"error"`
		Panes       []struct {
			PaneID string `json:"pane_id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"panes"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("parse interrupt envelope: %v (%s)", err, out)
	}
	if envelope.Success {
		t.Fatalf("success=true despite a failed pane: %s", out)
	}
	if envelope.ErrorCode != "PARTIAL_INTERRUPT" {
		t.Fatalf("error_code=%q, want PARTIAL_INTERRUPT: %s", envelope.ErrorCode, out)
	}
	if envelope.Interrupted != 2 || envelope.Failed != 1 {
		t.Fatalf("interrupted=%d failed=%d, want 2/1: %s", envelope.Interrupted, envelope.Failed, out)
	}
	if len(envelope.Panes) != 3 {
		t.Fatalf("envelope lists %d panes, want per-pane results for all 3: %s", len(envelope.Panes), out)
	}
	for _, pr := range envelope.Panes {
		if pr.PaneID == doomedID {
			if pr.Status != "failed" || !strings.Contains(pr.Error, "can't find pane") {
				t.Fatalf("doomed pane result wrong: %+v", pr)
			}
		} else if pr.Status != "interrupted" || pr.Error != "" {
			t.Fatalf("surviving pane result wrong: %+v", pr)
		}
	}

	// Ground truth the envelope cannot fake: BOTH surviving fixtures logged
	// the C-c keystroke — including the one AFTER the failure point.
	for i, surv := range survivors {
		if _, ok := surv.WaitForEvent("key", "c-c", 10*time.Second); !ok {
			t.Fatalf("survivor %d (%s) never received C-c; events=%+v", i, surv.PaneID, surv.Events())
		}
	}
	// And the doomed pane really was killed mid-interrupt.
	remaining, _ := tmux.GetPanes(pane1.Session)
	if len(remaining) != 2 {
		t.Fatalf("doomed pane still present: %d panes remain", len(remaining))
	}
	logger.Log("[PASS] partial interrupt: 2 interrupted (incl. post-failure pane), 1 failed, exit 1")
}

// streamNTMNDJSON starts an ntm command, collects NDJSON stdout lines until
// stop returns true (or timeout), then SIGINTs the process and returns the
// lines plus the exit code.
func streamNTMNDJSON(t *testing.T, logger *TestLogger, timeout time.Duration, stop func(lines []string) bool, args ...string) ([]string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ntm %v: %v", args, err)
	}

	linesCh := make(chan string, 256)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			linesCh <- scanner.Text()
		}
		close(linesCh)
	}()

	var lines []string
	deadline := time.After(timeout)
collect:
	for {
		select {
		case line, ok := <-linesCh:
			if !ok {
				break collect
			}
			lines = append(lines, line)
			if stop(lines) {
				break collect
			}
		case <-deadline:
			break collect
		}
	}
	_ = cmd.Process.Signal(os.Interrupt)
	// Drain remaining output so Wait can finish.
	go func() {
		for range linesCh {
		}
	}()
	waitErr := cmd.Wait()
	exit := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		}
	}
	if logger != nil {
		logger.Log("[NTM-STREAM] args=%v exit=%d lines=%d", args, exit, len(lines))
		for i, l := range lines {
			logger.Log("[NTM-STREAM] %d: %s", i, l)
		}
	}
	return lines, exit
}

// H4 proof (live): `ntm activity --watch --json` streams NDJSON frames —
// one compact JSON envelope per tick, each listing the fakeagent — and
// terminates cleanly on SIGINT with exit 0.
func TestActivityWatchJSONStreamsNDJSONFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "activity-watch-ndjson")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)

	lines, exit := streamNTMNDJSON(t, logger, 20*time.Second,
		func(lines []string) bool { return len(lines) >= 3 },
		"activity", pane.Session, "--watch", "--json", "--interval", "300")
	if exit != 0 {
		t.Fatalf("activity --watch --json exit=%d, want 0 after SIGINT", exit)
	}
	if len(lines) < 2 {
		t.Fatalf("want >=2 NDJSON frames, got %d", len(lines))
	}
	for i, line := range lines {
		var frame struct {
			Success bool   `json:"success"`
			Session string `json:"session"`
			Agents  []struct {
				AgentType string `json:"agent_type"`
				State     string `json:"state"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame %d is not standalone JSON (NDJSON framing broken): %q: %v", i, line, err)
		}
		if !frame.Success || frame.Session != pane.Session {
			t.Fatalf("frame %d wrong: %s", i, line)
		}
		if len(frame.Agents) != 1 || frame.Agents[0].AgentType != "claude" {
			t.Fatalf("frame %d does not list the fakeagent: %s", i, line)
		}
	}
	logger.Log("[PASS] %d NDJSON activity frames, all valid", len(lines))
}

// H4 proof (live): `ntm watch --json` streams watch_started + pane_output
// NDJSON frames, and new fixture output arrives as a pane_output frame
// containing the emitted token.
func TestWatchJSONStreamsPaneOutputFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "watch-ndjson")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)
	token := fmt.Sprintf("H4-WATCH-TOKEN-%d", time.Now().UnixNano())

	// Arm the fixture to print the token shortly after the watch starts.
	go func() {
		time.Sleep(2 * time.Second)
		pane.Control("ack " + token)
	}()

	sawToken := func(lines []string) bool {
		for _, l := range lines {
			if strings.Contains(l, token) {
				return true
			}
		}
		return false
	}
	lines, exit := streamNTMNDJSON(t, logger, 25*time.Second, sawToken,
		"watch", pane.Session, "--json", "--interval", "200")
	if exit != 0 {
		t.Fatalf("watch --json exit=%d, want 0 after SIGINT", exit)
	}
	if len(lines) == 0 {
		t.Fatal("watch --json emitted nothing (silence is the audited sin)")
	}

	var sawStart, sawTokenFrame bool
	for i, line := range lines {
		var frame struct {
			Event   string   `json:"event"`
			Session string   `json:"session"`
			PaneID  string   `json:"pane_id"`
			Lines   []string `json:"lines"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame %d is not standalone JSON: %q: %v", i, line, err)
		}
		if frame.Event == "" || frame.Session != pane.Session {
			t.Fatalf("frame %d missing event/session: %s", i, line)
		}
		if frame.Event == "watch_started" {
			sawStart = true
		}
		if frame.Event == "pane_output" && strings.Contains(strings.Join(frame.Lines, "\n"), token) {
			sawTokenFrame = true
			if frame.PaneID != pane.PaneID {
				t.Fatalf("token frame attributes wrong pane: %s", line)
			}
		}
	}
	if !sawStart {
		t.Fatal("no watch_started frame")
	}
	if !sawTokenFrame {
		t.Fatalf("fixture token %q never arrived as a pane_output frame", token)
	}
	logger.Log("[PASS] watch --json: watch_started + pane_output(token) frames valid")
}

// analyticsEnv builds an isolated HOME (events log) + NTM_CONFIG whose
// [agents] claude command execs the fakeagent, returning env entries and the
// added-fixture's event-log path.
func analyticsEnv(t *testing.T, dir string) (env []string, addedLogPath string) {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mk home: %v", err)
	}
	addedControl := filepath.Join(dir, "added-control")
	addedLogPath = filepath.Join(dir, "added-events.jsonl")
	wrapper := filepath.Join(dir, "fake-claude.sh")
	body := fmt.Sprintf("#!/bin/bash\nexec %s --persona=claude --control=%s --log=%s\n",
		tmux.ShellQuote(bin), tmux.ShellQuote(addedControl), tmux.ShellQuote(addedLogPath))
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatalf("write claude wrapper: %v", err)
	}
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[agents]\nclaude = \""+wrapper+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return []string{"HOME=" + home, "NTM_CONFIG=" + cfg}, addedLogPath
}

// runNTMIsolated runs ntm with duplicate-safe env overrides (HOME etc.).
func runNTMIsolated(t *testing.T, logger *TestLogger, overrides []string, args ...string) (string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	keys := make(map[string]bool, len(overrides))
	for _, kv := range overrides {
		keys[strings.SplitN(kv, "=", 2)[0]] = true
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if keys[strings.SplitN(kv, "=", 2)[0]] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, overrides...)

	cmd := exec.Command(bin, args...)
	cmd.Env = env
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
		logger.Log("[NTM-ISO] args=%v exit=%d", args, exit)
		logger.Log("[NTM-ISO] output=%s", strings.TrimSpace(string(out)))
	}
	return string(out), exit
}

type analyticsBreakdownStats struct {
	Count     int `json:"count"`
	Prompts   int `json:"prompts"`
	CharsSent int `json:"chars_sent"`
}

type analyticsSnapshot struct {
	TotalCharsSent int                                `json:"total_chars_sent"`
	TotalAgents    int                                `json:"total_agents"`
	AgentBreakdown map[string]analyticsBreakdownStats `json:"agent_breakdown"`
}

func fetchAnalytics(t *testing.T, logger *TestLogger, env []string) analyticsSnapshot {
	t.Helper()
	out, exit := runNTMIsolated(t, logger, env, "analytics", "--days", "1", "--format", "json")
	if exit != 0 {
		t.Fatalf("analytics exit=%d: %s", exit, out)
	}
	var snap analyticsSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("parse analytics JSON: %v (%s)", err, out)
	}
	return snap
}

// H4 proof (live): after a fakeagent send of a known multibyte payload,
// chars_sent EQUALS the payload's byte count (exact value — "nonzero" would
// pass a wrong counter), a second send accumulates, AND the agent registered
// via `ntm add` (the audit's named blind spot) is counted and attributed to
// the added agent's type — not lumped into the pre-existing codex pane.
func TestAnalyticsE2ECharsSentExactAndAddAgentAttributed(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "analytics-chars-sent")
	defer logger.Close()

	// Base session: one CODEX fixture pane, so claude attribution can only
	// come from the pane registered via `ntm add`.
	base := startFakeagentSession(t, "codex", 0, 0)
	env, addedLog := analyticsEnv(t, t.TempDir())

	out, exit := runNTMIsolated(t, logger, env, "add", base.Session, "--cc=1")
	if exit != 0 {
		t.Fatalf("ntm add exit=%d: %s", exit, out)
	}

	// The added pane runs the fakeagent via the isolated [agents] claude
	// command; wait for its start event (fixture ground truth).
	added := &fakeagentPane{t: t, Session: base.Session, Persona: "claude", logPath: addedLog}
	if _, ok := added.WaitForEvent("start", "", 15*time.Second); !ok {
		panes, _ := tmux.GetPanes(base.Session)
		t.Fatalf("added fakeagent never started; panes=%+v", panes)
	}

	payload1 := fmt.Sprintf("H4-CHARS-αβγ-№1-%d", time.Now().UnixNano())
	payload2 := fmt.Sprintf("H4-CHARS-second-送信-%d", time.Now().UnixNano())
	want1 := len(payload1) // BYTES (multibyte runes make chars≠bytes detectable)
	want2 := len(payload2)
	if want1 == len([]rune(payload1)) {
		t.Fatal("payload1 must contain multibyte runes so byte-vs-rune counting is distinguishable")
	}

	// Send 1 to the added claude pane only.
	out, exit = runNTMIsolated(t, logger, env, "send", base.Session, payload1, "--cc")
	if exit != 0 {
		t.Fatalf("send 1 exit=%d: %s", exit, out)
	}
	if _, ok := added.WaitForEvent("submit", payload1, 20*time.Second); !ok {
		t.Fatalf("added fixture never logged submit of payload1; events=%+v", added.Events())
	}

	snap := fetchAnalytics(t, logger, env)
	logger.LogJSON("analytics_after_send1", snap)
	if snap.TotalCharsSent != want1 {
		t.Fatalf("total_chars_sent=%d, want EXACTLY the %d payload bytes", snap.TotalCharsSent, want1)
	}
	claude := snap.AgentBreakdown["claude"]
	if claude.CharsSent != want1 {
		t.Fatalf("claude.chars_sent=%d, want %d attributed to the ADDED agent", claude.CharsSent, want1)
	}
	if claude.Count != 1 {
		t.Fatalf("claude.count=%d, want 1 (the ntm-add registration must be counted)", claude.Count)
	}
	if codex := snap.AgentBreakdown["codex"]; codex.CharsSent != 0 {
		t.Fatalf("codex.chars_sent=%d, want 0 — payload must not be lumped into other agents", codex.CharsSent)
	}

	// Send 2 accumulates.
	out, exit = runNTMIsolated(t, logger, env, "send", base.Session, payload2, "--cc")
	if exit != 0 {
		t.Fatalf("send 2 exit=%d: %s", exit, out)
	}
	if _, ok := added.WaitForEvent("submit", payload2, 20*time.Second); !ok {
		t.Fatalf("added fixture never logged submit of payload2; events=%+v", added.Events())
	}

	snap = fetchAnalytics(t, logger, env)
	logger.LogJSON("analytics_after_send2", snap)
	if snap.TotalCharsSent != want1+want2 {
		t.Fatalf("total_chars_sent=%d, want accumulated %d", snap.TotalCharsSent, want1+want2)
	}
	if claude := snap.AgentBreakdown["claude"]; claude.CharsSent != want1+want2 {
		t.Fatalf("claude.chars_sent=%d, want accumulated %d", claude.CharsSent, want1+want2)
	}
	logger.Log("[PASS] chars_sent exact (%d, then %d) and ntm-add agent attributed", want1, want1+want2)
}
