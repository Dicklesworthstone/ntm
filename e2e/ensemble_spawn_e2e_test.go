//go:build e2e && ensemble_experimental
// +build e2e,ensemble_experimental

package e2e

// ensemble_spawn_e2e_test.go is the fakeagent live-tmux E2E bar for shipping
// the ensemble_experimental build tag in release builds
// (bd-ws5-ship-or-cut-jv0rc.3, F3).
//
// The bar, per the bead: `ntm ensemble spawn` must pass the same fakeagent
// live-tmux standard as regular spawn — spawn N modes, deliver, collect:
//
//   1. ensemble spawn creates the tmux session with one pane per mode;
//   2. each pane's agent receives its mode-specific prompt through the
//      composer (proven by the fakeagent fixture's own submit event log,
//      ground truth the CLI envelope cannot fake);
//   3. `ntm ensemble status` reflects the spawn's progress from persisted
//      state.
//
// The ntm binary under test is built with -tags ensemble_experimental; the
// panes run the fakeagent fixture via a `cod` wrapper on an isolated PATH,
// inside an isolated tmux server (TMUX_TMPDIR) with isolated HOME/XDG dirs.
//
// Run: go test -tags "e2e,ensemble_experimental" -run TestEnsembleSpawnFakeagentE2E ./e2e/

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const ensembleE2EQuestion = "ENSEMBLE-E2E-QUESTION what single risk matters most in this repo?"

// ensembleSpawnFixture isolates one ensemble spawn run: its own tmux server,
// HOME/XDG dirs, fake agent bin dir, and per-pane fixture event logs.
type ensembleSpawnFixture struct {
	t        *testing.T
	ntmPath  string
	session  string
	env      []string
	eventDir string
	projDir  string
	tmuxRoot string
}

func newEnsembleSpawnFixture(t *testing.T) *ensembleSpawnFixture {
	t.Helper()

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	ntmPath := ntmBinary(t, "ensemble_experimental")
	fakeagentPath, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent fixture: %v", err)
	}

	runtimeRoot := t.TempDir()
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	eventDir := filepath.Join(runtimeRoot, "events")
	projDir := filepath.Join(runtimeRoot, "project")
	fakeBin := filepath.Join(runtimeRoot, "bin")
	for _, dir := range []string{
		filepath.Join(runtimeRoot, "home"),
		filepath.Join(runtimeRoot, "config"),
		filepath.Join(runtimeRoot, "data"),
		fakeBin, eventDir, projDir, tmuxRoot,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture dir %s: %v", dir, err)
		}
	}

	// A minimal but real project for the context-pack generator.
	if err := os.WriteFile(filepath.Join(projDir, "README.md"),
		[]byte("# ensemble e2e project\n\nA tiny fixture project.\n"), 0o644); err != nil {
		t.Fatalf("write fixture project README: %v", err)
	}

	// The ensemble pane launcher sends the bare agent alias (`cod`) to each
	// pane's shell. This wrapper resolves ahead of any real codex CLI and
	// execs the fakeagent fixture with per-pane control/log files keyed by
	// the tmux pane id ("%N" -> "N").
	wrapper := strings.Join([]string{
		"#!/bin/sh",
		`pane="${TMUX_PANE#%}"`,
		fmt.Sprintf(`exec %q --persona=codex --control=%q/"$pane".control --log=%q/"$pane".jsonl`,
			fakeagentPath, eventDir, eventDir),
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(fakeBin, "cod"), []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write cod wrapper: %v", err)
	}

	fixture := &ensembleSpawnFixture{
		t:        t,
		ntmPath:  ntmPath,
		session:  fmt.Sprintf("ntm-e2e-ens-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000),
		eventDir: eventDir,
		projDir:  projDir,
		tmuxRoot: tmuxRoot,
	}
	fixture.env = isolatedProcessEnv(map[string]string{
		"HOME":            filepath.Join(runtimeRoot, "home"),
		"XDG_CONFIG_HOME": filepath.Join(runtimeRoot, "config"),
		"XDG_DATA_HOME":   filepath.Join(runtimeRoot, "data"),
		"TMUX_TMPDIR":     tmuxRoot,
		"PATH":            fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"SHELL":           "/bin/sh",
		// Pane shells write $HISTFILE into $HOME on exit, racing t.TempDir's
		// RemoveAll after kill-server ("directory not empty" flake).
		"HISTFILE": "/dev/null",
		"NO_COLOR": "1",
		"TERM":     "xterm-256color",
	})

	t.Cleanup(func() {
		kill := exec.Command("tmux", "kill-server")
		kill.Env = fixture.env
		_ = kill.Run()
		// kill-server returns before panes finish dying; wait for the server
		// socket to go away so no pane process still writes under the temp
		// HOME while t.TempDir cleanup removes it.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			probe := exec.Command("tmux", "list-sessions")
			probe.Env = fixture.env
			if err := probe.Run(); err != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
	})

	return fixture
}

func (f *ensembleSpawnFixture) runNTM(timeout time.Duration, args ...string) (string, int) {
	f.t.Helper()
	cmd := exec.Command(f.ntmPath, args...)
	cmd.Env = f.env
	cmd.Dir = f.projDir
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	go func() {
		select {
		case <-done:
		case <-time.After(timeout):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}()
	<-done
	exit := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			f.t.Fatalf("run ntm %v: %v (output=%s)", args, runErr, out)
		}
	}
	return string(out), exit
}

func (f *ensembleSpawnFixture) tmux(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// paneEvents reads the fixture event log for one pane id ("%N" form).
func (f *ensembleSpawnFixture) paneEvents(paneID string) []fakeagentEvent {
	f.t.Helper()
	logPath := filepath.Join(f.eventDir, strings.TrimPrefix(paneID, "%")+".jsonl")
	data, err := os.ReadFile(logPath)
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
			f.t.Fatalf("invalid fixture event line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// submitsFor returns all submit-event payloads logged by one pane's fixture.
func (f *ensembleSpawnFixture) submitsFor(paneID string) []string {
	var submits []string
	for _, ev := range f.paneEvents(paneID) {
		if ev.Event == "submit" {
			submits = append(submits, ev.Data)
		}
	}
	return submits
}

type ensembleSpawnEnvelope struct {
	Success    bool     `json:"success"`
	Session    string   `json:"session"`
	Modes      []string `json:"modes"`
	Assignment string   `json:"assignment"`
	Status     string   `json:"status"`
	Injected   bool     `json:"injected"`
	Error      string   `json:"error,omitempty"`
}

type ensembleStatusEnvelope struct {
	Session      string `json:"session"`
	Exists       bool   `json:"exists"`
	Question     string `json:"question"`
	Status       string `json:"status"`
	StatusCounts struct {
		Pending int `json:"pending"`
		Working int `json:"working"`
		Done    int `json:"done"`
		Error   int `json:"error"`
	} `json:"status_counts"`
	Assignments []struct {
		ModeID    string `json:"mode_id"`
		AgentType string `json:"agent_type"`
		Status    string `json:"status"`
		PaneName  string `json:"pane_name"`
	} `json:"assignments"`
}

// TestEnsembleSpawnFakeagentE2E is the release bar for the
// ensemble_experimental tag: ensemble spawn against live tmux + fakeagent
// panes must create the session, dispatch each mode's prompt through the
// composer (fixture-verified), and report progress via ensemble status.
func TestEnsembleSpawnFakeagentE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "ensemble-spawn-fakeagent")
	defer logger.Close()

	fixture := newEnsembleSpawnFixture(t)
	modes := []string{"deductive", "failure-mode"}
	logger.Log("[SETUP] session=%s modes=%v eventDir=%s", fixture.session, modes, fixture.eventDir)

	// --- Step 1: spawn the ensemble ------------------------------------
	out, exit := fixture.runNTM(180*time.Second,
		"--json", "ensemble", "spawn", fixture.session,
		"--modes="+strings.Join(modes, ","),
		"--question="+ensembleE2EQuestion,
		"--agent-mix=cod=2",
		"--assignment=round-robin",
		"--no-cache",
		"--project="+fixture.projDir,
	)
	logger.Log("[SPAWN] exit=%d output=%s", exit, strings.TrimSpace(out))
	if exit != 0 {
		t.Fatalf("ensemble spawn exit=%d output=%s", exit, out)
	}
	var envelope ensembleSpawnEnvelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("parse ensemble spawn envelope: %v (%s)", err, out)
	}
	if !envelope.Success || envelope.Error != "" {
		t.Fatalf("ensemble spawn envelope not successful: %+v", envelope)
	}
	if envelope.Session != fixture.session {
		t.Fatalf("envelope session = %q, want %q", envelope.Session, fixture.session)
	}
	if len(envelope.Modes) != len(modes) {
		t.Fatalf("envelope modes = %v, want %d modes %v", envelope.Modes, len(modes), modes)
	}
	if !envelope.Injected {
		t.Fatalf("envelope reports prompts were not injected: %+v", envelope)
	}
	if envelope.Status != "active" {
		t.Fatalf("envelope status = %q, want %q", envelope.Status, "active")
	}

	// --- Step 2: the tmux session exists with one pane per mode --------
	panesOut, err := fixture.tmux("list-panes", "-s", "-t", fixture.session,
		"-F", "#{pane_id}\t#{pane_title}")
	if err != nil {
		t.Fatalf("list ensemble panes: %v (%s)", err, panesOut)
	}
	logger.Log("[PANES]\n%s", panesOut)
	paneByTitle := make(map[string]string) // title -> pane id
	var paneIDs []string
	for _, line := range strings.Split(panesOut, "\n") {
		id, title, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("unexpected list-panes line %q", line)
		}
		paneByTitle[title] = id
		paneIDs = append(paneIDs, id)
	}
	if len(paneIDs) != len(modes) {
		t.Fatalf("ensemble session has %d panes, want %d: %s", len(paneIDs), len(modes), panesOut)
	}

	// --- Step 3: every pane's fixture proves composer delivery ---------
	// The fixture logs a submit event only when a composer submission
	// actually happened inside the pane — ground truth the CLI cannot fake.
	deadline := time.Now().Add(90 * time.Second)
	for _, paneID := range paneIDs {
		for {
			submits := fixture.submitsFor(paneID)
			if len(submits) > 0 {
				break
			}
			if time.Now().After(deadline) {
				capture, _ := fixture.tmux("capture-pane", "-p", "-t", paneID)
				t.Fatalf("pane %s fixture never logged a submit; events=%+v pane:\n%s",
					paneID, fixture.paneEvents(paneID), capture)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// --- Step 4: status reflects progress from persisted state ---------
	statusOut, exit := fixture.runNTM(60*time.Second,
		"ensemble", "status", fixture.session, "--format=json")
	logger.Log("[STATUS] exit=%d output=%s", exit, strings.TrimSpace(statusOut))
	if exit != 0 {
		t.Fatalf("ensemble status exit=%d output=%s", exit, statusOut)
	}
	var status ensembleStatusEnvelope
	if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
		t.Fatalf("parse ensemble status: %v (%s)", err, statusOut)
	}
	if !status.Exists || status.Session != fixture.session {
		t.Fatalf("status does not reflect the spawned session: %+v", status)
	}
	if status.Question != ensembleE2EQuestion {
		t.Fatalf("status question = %q, want %q", status.Question, ensembleE2EQuestion)
	}
	if status.Status != "active" {
		t.Fatalf("status = %q, want %q", status.Status, "active")
	}
	if len(status.Assignments) != len(modes) {
		t.Fatalf("status has %d assignments, want %d: %+v", len(status.Assignments), len(modes), status)
	}
	if status.StatusCounts.Error != 0 {
		t.Fatalf("status reports assignment errors: %+v", status.StatusCounts)
	}

	// --- Step 5: each pane received ITS mode's prompt, and only its own -
	seenModes := make(map[string]bool)
	for _, assignment := range status.Assignments {
		if assignment.Status == "error" {
			t.Fatalf("assignment %+v is in error", assignment)
		}
		paneID, ok := paneByTitle[assignment.PaneName]
		if !ok {
			t.Fatalf("assignment pane %q not found among panes %v", assignment.PaneName, paneByTitle)
		}
		submits := fixture.submitsFor(paneID)
		if len(submits) != 1 {
			t.Fatalf("pane %s (%s) logged %d submits, want exactly 1: %v",
				paneID, assignment.ModeID, len(submits), submits)
		}
		payload := submits[0]
		if !strings.Contains(payload, ensembleE2EQuestion) {
			t.Fatalf("pane %s submit is missing the question; payload head: %.300s", paneID, payload)
		}
		ownMarker := "**ID**: " + assignment.ModeID
		if !strings.Contains(payload, ownMarker) {
			t.Fatalf("pane %s submit is missing its mode marker %q; payload head: %.300s",
				paneID, ownMarker, payload)
		}
		for _, other := range modes {
			if other == assignment.ModeID {
				continue
			}
			if strings.Contains(payload, "**ID**: "+other) {
				t.Fatalf("pane %s (%s) also received mode %q's preamble — dispatch is not mode-disjoint",
					paneID, assignment.ModeID, other)
			}
		}
		seenModes[assignment.ModeID] = true
		logger.Log("[VERIFIED] pane=%s mode=%s submit_len=%d", paneID, assignment.ModeID, len(payload))
	}
	for _, mode := range modes {
		if !seenModes[mode] {
			t.Fatalf("mode %q was never assigned/verified; assignments=%+v", mode, status.Assignments)
		}
	}

	logger.Log("[PASS] ensemble spawn created %d panes, composer-delivered %d disjoint mode prompts, status=active",
		len(paneIDs), len(modes))
}
