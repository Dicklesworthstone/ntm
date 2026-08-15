//go:build e2e
// +build e2e

package e2e

// fakeagent_harness_test.go builds and drives the fakeagent TUI fixture
// (bd-kur07) inside real tmux sessions, and proves the fixture satisfies
// NTM's live delivery contracts end-to-end. The helpers here are shared by
// the v1.23.0-verification E2E suites (idempotent send, composer/delivery,
// gates/restart, tracking, ack).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

var (
	fakeagentBinOnce sync.Once
	fakeagentBinPath string
	fakeagentBinErr  error
)

// ensureFakeagentBin builds the fixture binary once per test process.
func ensureFakeagentBin() (string, error) {
	fakeagentBinOnce.Do(func() {
		wd, err := os.Getwd()
		if err != nil {
			fakeagentBinErr = fmt.Errorf("getwd: %w", err)
			return
		}
		repoRoot, err := findRepoRoot(wd)
		if err != nil {
			fakeagentBinErr = err
			return
		}
		outDir, err := os.MkdirTemp("", "ntm-fakeagent-bin-*")
		if err != nil {
			fakeagentBinErr = fmt.Errorf("mkdtemp: %w", err)
			return
		}
		outPath := filepath.Join(outDir, "fakeagent")
		cmd := exec.Command("go", "build", "-o", outPath, "./e2e/fakeagent")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeagentBinErr = fmt.Errorf("go build ./e2e/fakeagent: %w output=%s", err, string(out))
			return
		}
		fakeagentBinPath = outPath
	})
	return fakeagentBinPath, fakeagentBinErr
}

// fakeagentEvent mirrors the fixture's JSONL event-log records.
type fakeagentEvent struct {
	TS    string `json:"ts"`
	Event string `json:"event"`
	Data  string `json:"data,omitempty"`
	N     int    `json:"n,omitempty"`
}

// fakeagentPane is one live fixture pane inside a test tmux session.
type fakeagentPane struct {
	t           *testing.T
	Session     string
	PaneID      string
	Persona     string
	controlPath string
	logPath     string
}

// Control appends a verb line to the fixture's polled control file.
func (p *fakeagentPane) Control(verb string) {
	p.t.Helper()
	f, err := os.OpenFile(p.controlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		p.t.Fatalf("open control file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(verb + "\n"); err != nil {
		p.t.Fatalf("write control verb %q: %v", verb, err)
	}
	// The fixture polls every 100ms; give it two cycles.
	time.Sleep(250 * time.Millisecond)
}

// Events parses the fixture's JSONL event log.
func (p *fakeagentPane) Events() []fakeagentEvent {
	p.t.Helper()
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return nil
	}
	var events []fakeagentEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev fakeagentEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			p.t.Fatalf("invalid fixture event log line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// WaitForEvent polls until an event with the given name (and, when non-empty,
// data containing wantData) appears, or the timeout elapses.
func (p *fakeagentPane) WaitForEvent(name, wantData string, timeout time.Duration) (fakeagentEvent, bool) {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range p.Events() {
			if ev.Event != name {
				continue
			}
			if wantData != "" && !strings.Contains(ev.Data, wantData) {
				continue
			}
			return ev, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fakeagentEvent{}, false
}

// CountEvents returns how many events with the given name were logged.
func (p *fakeagentPane) CountEvents(name string) int {
	count := 0
	for _, ev := range p.Events() {
		if ev.Event == name {
			count++
		}
	}
	return count
}

// startFakeagentSession creates an isolated tmux session whose only pane
// runs the fixture, titled so NTM detects the requested agent type.
// width/height of 0 use 120x40.
func startFakeagentSession(t *testing.T, persona string, width, height int) *fakeagentPane {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available")
	}
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 40
	}

	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	logPath := filepath.Join(dir, "events.jsonl")
	session := fmt.Sprintf("ntm-e2e-fake-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)

	// NTM pane titles follow "<session>__<type>_<n>" (paneNameRegex);
	// a bare "cc_1" parses as a user pane and target selection drops it.
	title := session + "__cc_1"
	if persona == "codex" {
		title = session + "__cod_1"
	}

	launch := fmt.Sprintf("%s --persona=%s --control=%s --log=%s",
		tmux.ShellQuote(bin), persona, tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
	if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", session,
		"-x", fmt.Sprint(width), "-y", fmt.Sprint(height), launch); err != nil {
		t.Fatalf("create fakeagent session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tmux.DefaultClient.Run("kill-session", "-t", session)
	})

	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) == 0 {
		t.Fatalf("enumerate fakeagent panes: err=%v panes=%d", err, len(panes))
	}
	paneID := panes[0].ID
	if err := tmux.SetPaneTitle(paneID, title); err != nil {
		t.Fatalf("title fakeagent pane: %v", err)
	}

	pane := &fakeagentPane{
		t:           t,
		Session:     session,
		PaneID:      paneID,
		Persona:     persona,
		controlPath: controlPath,
		logPath:     logPath,
	}

	// The fixture logs "start" as its first event; wait for it so tests
	// never race the process launch.
	if _, ok := pane.WaitForEvent("start", "", 10*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(paneID, 40)
		t.Fatalf("fakeagent did not start; pane shows:\n%s", capture)
	}
	return pane
}

// runNTM invokes the freshly built ntm binary against the fixture session,
// returning combined output. The environment inherits the test process (the
// fixture session lives on the default tmux server).
func runNTMFixture(t *testing.T, logger *TestLogger, args ...string) (string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, args...)
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
		logger.Log("[NTM] args=%v exit=%d", args, exit)
		logger.Log("[NTM] output=%s", strings.TrimSpace(string(out)))
	}
	return string(out), exit
}

// =============================================================================
// Live acceptance for the fixture itself (bd-kur07 acceptance criteria)
// =============================================================================

// A robot-send to a fakeagent pane must pass the dead-pane and readiness
// gates, deliver, verify submission, and the fixture's own event log must
// prove the submit happened — the ground truth robot envelopes cannot fake.
func TestFakeagentLiveRobotSendSubmits(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "fakeagent-live-send")
	defer logger.Close()

	pane := startFakeagentSession(t, "claude", 0, 0)
	logger.Log("[SETUP] session=%s pane=%s persona=%s", pane.Session, pane.PaneID, pane.Persona)

	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg=hello from e2e", "--type=claude")
	if exit != 0 {
		t.Fatalf("robot-send exit=%d output=%s", exit, out)
	}
	var envelope struct {
		Success    bool     `json:"success"`
		Successful []string `json:"successful"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("parse send envelope: %v (%s)", err, out)
	}
	if !envelope.Success || len(envelope.Successful) != 1 {
		t.Fatalf("send not successful: %s", out)
	}

	ev, ok := pane.WaitForEvent("submit", "hello from e2e", 15*time.Second)
	if !ok {
		capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
		t.Fatalf("fixture never logged the submit; events=%+v pane:\n%s", pane.Events(), capture)
	}
	logger.LogJSON("submit_event", ev)
	logger.Log("[PASS] delivered and submitted; %d swallow_strand events (want 0)", pane.CountEvents("swallow_strand"))
}

// The strand control verb must reproduce the stranded-composer state and be
// rescued by submission verification: the double-Enter protocol's Enters are
// swallowed, the rescue Enter submits, and the envelope still reports success
// (delivered means submitted).
func TestFakeagentLiveStrandIsRescued(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "fakeagent-live-strand")
	defer logger.Close()

	pane := startFakeagentSession(t, "codex", 0, 0)
	pane.Control("strand 2")
	logger.Log("[SETUP] armed strand 2 on %s", pane.PaneID)

	out, exit := runNTMFixture(t, logger, "--robot-send="+pane.Session, "--msg=stranded prompt payload", "--type=codex")
	if exit != 0 {
		t.Fatalf("robot-send exit=%d output=%s", exit, out)
	}
	var envelope struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("parse send envelope: %v (%s)", err, out)
	}
	if !envelope.Success {
		t.Fatalf("send reported failure despite rescue path: %s", out)
	}

	if _, ok := pane.WaitForEvent("submit", "stranded prompt payload", 20*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
		t.Fatalf("rescue never submitted; events=%+v pane:\n%s", pane.Events(), capture)
	}
	swallowed := pane.CountEvents("swallow_strand")
	if swallowed == 0 {
		t.Fatal("strand mode never engaged: no swallow_strand events; the test proved nothing")
	}
	logger.Log("[PASS] %d submit attempt(s) swallowed, then rescued to a real submit", swallowed)
}
