//go:build e2e
// +build e2e

package e2e

// context_rotation_e2e_test.go is the live E2E proof of context rotation
// during long sessions (ntm-8ice), unblocked by bd-rpmg8's production wiring:
// SessionCoordinator.RunCycle -> maybeCheckContextRotation -> rotationChecker
// (internal/coordinator/rotation.go), triggered off TRANSCRIPT-SOURCED context
// usage and gated by [rotation] usage_percent_threshold in config.
//
// Every scenario runs the REAL ntm binary (`ntm coordinator run --once`)
// against a real tmux session holding cwd-pinned fakeagent panes, with a
// hermetic temp HOME carrying:
//   - the Claude transcript fixture (~/.claude/projects/<munged-cwd>/*.jsonl)
//     that supplies the transcript-sourced usage reading,
//   - the pending-rotation store the trigger writes
//     (~/.ntm/rotation_history/pending.jsonl) and the rotation audit log
//     (~/.ntm/rotation_history/rotations.jsonl),
//
// and NTM_CONFIG pointing at a per-scenario config file.
//
// Scenario -> ground truth map:
//  1. Threshold trigger: 150k-token fixture vs claude-fable-5's 200k window
//     (75%) with usage_percent_threshold=60 -> `ntm rotate context pending`
//     and --robot-context pending_rotations both show the pending rotation;
//     evidence (tokens/limit/source path/confidence) is asserted from the
//     coordinator's structured slog on stderr. A second cycle must NOT
//     re-enqueue (cross-process store dedupe).
//  2. Safety gates: the same over-threshold setup with the pane working
//     (Control "work"), rate-limited (Control "ratelimit"), showing an
//     interactive gate (Control "gate trust"), or holding unsubmitted
//     composer text -> NO pending, and the skip reason is logged.
//  3. Default off / below threshold: no [rotation] section -> the trigger
//     never runs regardless of usage; threshold=60 with a 50% fixture -> no
//     pending.
//  4. auto_confirm=true: the rotation EXECUTES through the existing
//     Rotator.ConfirmRotation path. [agents] claude in the temp config points
//     at a wrapper that launches the fakeagent binary, so the replacement
//     pane is a real agent process: the old pane is killed (fixture logs its
//     SIGHUP), a new pane with the SAME canonical title but a different pane
//     ID runs the wrapper's fakeagent, the handoff context is delivered to
//     it, the pending entry is consumed, and a success record lands in the
//     rotation audit log. A follow-up cycle with the replacement's fresh
//     transcript (the newest for the cwd, exactly what a real relaunched CLI
//     writes) produces NO new rotation.
//  5. Ambiguity: two same-persona panes sharing one cwd get no transcript
//     reading at all -> no pending, no skip, nothing.
//
// KNOWN DELTA vs the original bead text (asserting what the machinery
// actually does):
//   - The pending record persists only session/agent/pane/percent/timeouts;
//     the tokens/source/confidence evidence travels via slog + the attention
//     feed of the coordinator PROCESS (in-memory), so cross-process the
//     evidence is asserted from the coordinator's stderr, not from the
//     pending stores.
//   - "No immediate re-rotation on the NEXT coordinator cycle": within one
//     long-running coordinator process this is the 0484af4b monitor-rebind
//     regression (covered by internal/coordinator's
//     TestRotationChecker_AutoConfirm unit test). Across `--once` processes
//     the checker is stateless by design and transcript-driven: protection
//     against immediate re-rotation comes from the replacement agent's fresh
//     transcript becoming the newest for the cwd. Scenario 4 proves exactly
//     that; it also documents (without executing) that a stale over-threshold
//     transcript that never gets superseded WOULD legitimately re-trigger.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// crotClaudeWindow is the registry context window for claude-fable-5
// (internal/models/registry.go); the fixtures are sized against it.
const crotClaudeWindow = 200000

// Structured slog markers emitted by internal/coordinator/rotation.go. The
// assertions below key on these exact strings; if they change, the trigger's
// observability contract changed and this suite must be revisited.
const (
	crotLogEnqueued      = "context rotation enqueued from transcript usage"
	crotLogSkipped       = "context rotation skipped by safety gate"
	crotLogAutoConfirmed = "context rotation auto-confirmed"
	crotLogConfirmFailed = "context rotation auto-confirm failed"
)

// crotPendingEnvelope mirrors cli.PendingRotationsResult (`ntm rotate context
// pending --json`).
type crotPendingEnvelope struct {
	Pending []struct {
		AgentID        string  `json:"agent_id"`
		SessionName    string  `json:"session_name"`
		ContextPercent float64 `json:"context_percent"`
		TimeoutSeconds int     `json:"timeout_seconds"`
		DefaultAction  string  `json:"default_action"`
		CreatedAt      string  `json:"created_at"`
	} `json:"pending"`
	Count int `json:"count"`
}

// crotContextEnvelope extracts the --robot-context fields this suite asserts.
type crotContextEnvelope struct {
	Success bool   `json:"success"`
	Session string `json:"session"`
	Agents  []struct {
		Pane         string  `json:"pane"`
		Source       string  `json:"source"`
		UsagePercent float64 `json:"usage_percent"`
	} `json:"agents"`
	PendingRotations []struct {
		AgentID        string  `json:"agent_id"`
		SessionName    string  `json:"session_name"`
		PaneID         string  `json:"pane_id"`
		ContextPercent float64 `json:"context_percent"`
		DefaultAction  string  `json:"default_action"`
		WorkDir        string  `json:"work_dir"`
	} `json:"pending_rotations"`
}

// crotRunNTM invokes the freshly built ntm binary with HOME/XDG_CONFIG_HOME
// rooted at home and NTM_CONFIG at cfgPath, so transcript discovery, the
// pending/history stores (both under $HOME/.ntm), and config are all
// hermetic. stdout and stderr are returned separately: the coordinator's
// structured slog evidence lands on stderr while --json envelopes land on
// stdout.
func crotRunNTM(t *testing.T, logger *TestLogger, home, cfgPath string, args ...string) (string, string, int) {
	t.Helper()
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	cmd := exec.Command(bin, args...)
	// Later duplicates win (os/exec documented behavior): the isolated tmux
	// server env (TMUX_TMPDIR etc.) is inherited, only home/config move.
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"NTM_CONFIG="+cfgPath,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	exit := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run ntm %v: %v", args, runErr)
		}
	}
	if logger != nil {
		logger.Log("[NTM] HOME=%s args=%v exit=%d", home, args, exit)
		logger.Log("[NTM] stdout=%s", strings.TrimSpace(stdout.String()))
		logger.Log("[NTM] stderr=%s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), exit
}

// crotRunCoordinatorOnce executes exactly one real coordinator cycle against
// session and returns (stdout, stderr) after asserting success.
//
// Deliberately NOT --json: a machine invocation (any --json/--robot- arg)
// routes through suppressRobotDiagnostics (internal/cli/root.go), which
// swaps slog to io.Discard for the whole process — silencing the rotation
// trigger's evidence lines this suite asserts on. Human mode keeps the
// default slog handler on stderr and prints a deterministic completion line
// on stdout.
func crotRunCoordinatorOnce(t *testing.T, logger *TestLogger, home, cfgPath, session string) (string, string) {
	t.Helper()
	stdout, stderr, exit := crotRunNTM(t, logger, home, cfgPath,
		"coordinator", "run", session, "--once")
	if exit != 0 {
		t.Fatalf("coordinator run --once exit=%d stdout=%s stderr=%s", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, "Coordinator cycle complete for "+session) {
		t.Fatalf("coordinator run --once did not report cycle completion: stdout=%s stderr=%s", stdout, stderr)
	}
	return stdout, stderr
}

// crotFetchPending runs `ntm rotate context pending --json` under the
// hermetic HOME and returns the decoded envelope (all sessions; callers
// filter). The pending store lives at $HOME/.ntm/rotation_history/pending.jsonl.
func crotFetchPending(t *testing.T, logger *TestLogger, home, cfgPath string) *crotPendingEnvelope {
	t.Helper()
	stdout, stderr, exit := crotRunNTM(t, logger, home, cfgPath,
		"rotate", "context", "pending", "--json")
	if exit != 0 {
		t.Fatalf("rotate context pending exit=%d stdout=%s stderr=%s", exit, stdout, stderr)
	}
	var envelope crotPendingEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("parse pending envelope: %v (%s)", err, stdout)
	}
	logger.LogJSON("pending_rotations_cli", envelope)
	return &envelope
}

// crotPendingForSession filters the CLI pending envelope down to one session.
func crotPendingForSession(envelope *crotPendingEnvelope, session string) []int {
	var idx []int
	for i, p := range envelope.Pending {
		if p.SessionName == session {
			idx = append(idx, i)
		}
	}
	return idx
}

// crotAssertNoPending asserts the pending store holds nothing for session.
func crotAssertNoPending(t *testing.T, logger *TestLogger, home, cfgPath, session string) {
	t.Helper()
	envelope := crotFetchPending(t, logger, home, cfgPath)
	if hits := crotPendingForSession(envelope, session); len(hits) != 0 {
		t.Fatalf("expected NO pending rotations for %s, got %d: %+v", session, len(hits), envelope.Pending)
	}
}

// crotFetchRobotContext runs --robot-context and decodes the envelope.
func crotFetchRobotContext(t *testing.T, logger *TestLogger, home, cfgPath, session string) *crotContextEnvelope {
	t.Helper()
	stdout, stderr, exit := crotRunNTM(t, logger, home, cfgPath, "--robot-context="+session)
	if exit != 0 {
		t.Fatalf("--robot-context exit=%d stdout=%s stderr=%s", exit, stdout, stderr)
	}
	var envelope crotContextEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("parse robot-context envelope: %v (%s)", err, stdout)
	}
	if !envelope.Success {
		t.Fatalf("robot-context reported failure: %s", stdout)
	}
	logger.LogJSON("robot_context_pending", envelope.PendingRotations)
	return &envelope
}

// crotWriteConfig writes an isolated NTM config file and returns its path.
func crotWriteConfig(t *testing.T, logger *TestLogger, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write isolated config: %v", err)
	}
	logger.Log("[CONFIG] %s:\n%s", path, body)
	return path
}

// crotConfigBody builds the scenario config. threshold <= 0 omits the
// [rotation] section entirely (the default-off contract). claudeCommand, when
// non-empty, becomes [agents] claude so replacement panes launch the
// fakeagent. try_compact_first is disabled so auto-confirm exercises the
// rotation path directly instead of detouring through compaction prompts;
// confirm_timeout_sec is stretched so the pending entry cannot expire between
// the coordinator cycle and the assertions.
func crotConfigBody(threshold float64, autoConfirm bool, claudeCommand string) string {
	var b strings.Builder
	b.WriteString("# hermetic config written by context_rotation_e2e_test.go (ntm-8ice)\n")
	if threshold > 0 {
		fmt.Fprintf(&b, "[rotation]\nusage_percent_threshold = %.1f\nauto_confirm = %t\n\n", threshold, autoConfirm)
	}
	b.WriteString("[context_rotation]\ntry_compact_first = false\nconfirm_timeout_sec = 600\n")
	if claudeCommand != "" {
		fmt.Fprintf(&b, "\n[agents]\nclaude = %q\n", claudeCommand)
	}
	return b.String()
}

// crotWriteClaudeTranscript writes a Claude Code style JSONL transcript for
// projDir under home and returns its path. The four usage ints map onto the
// assistant entry's message.usage fields; the transcript-sourced token count
// is their sum. Local copy of the transcript suite's helper (that file is
// frozen).
func crotWriteClaudeTranscript(t *testing.T, logger *TestLogger, home, projDir, name string, input, cacheRead, cacheCreation, output int) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", ntmctx.MungeProjectPath(projDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir claude projects dir: %v", err)
	}
	path := filepath.Join(dir, name)
	lines := []string{
		`{"parentUuid":null,"sessionId":"crot-fixture","type":"user","message":{"role":"user","content":"seed prompt"},"timestamp":"2026-08-15T10:00:00.000Z"}`,
		fmt.Sprintf(`{"parentUuid":"u1","sessionId":"crot-fixture","type":"assistant","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-fable-5","usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":%d}},"timestamp":"2026-08-15T10:00:05.000Z"}`,
			input, cacheRead, cacheCreation, output),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude fixture: %v", err)
	}
	logger.Log("[FIXTURE] path=%s tokens=%d", path, input+cacheRead+cacheCreation+output)
	return path
}

// crotResolvedTempDir returns a symlink-free temp directory: on macOS
// t.TempDir lives under /var -> /private/var, while tmux reports
// #{pane_current_path} resolved, and the transcript correlation requires an
// exact match. Local copy (transcript suite is frozen).
func crotResolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir symlinks: %v", err)
	}
	return dir
}

// crotStartPanes creates one tmux session with count fakeagent panes, all
// cwd'd to dir via `-c` (the rotation checker correlates transcripts by
// #{pane_current_path}). Titles follow NTM's "<session>__cc_<n>" contract so
// the panes read as Claude agents. Local copy of the transcript suite's
// helper with a suite-specific session prefix.
func crotStartPanes(t *testing.T, dir string, count int) (string, []*fakeagentPane) {
	t.Helper()
	bin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}
	if !tmux.DefaultClient.IsInstalled() {
		t.Skip("tmux not available")
	}
	session := fmt.Sprintf("ntm-e2e-crot-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)

	panes := make([]*fakeagentPane, 0, count)
	for i := 0; i < count; i++ {
		ctlDir := t.TempDir()
		controlPath := filepath.Join(ctlDir, "control")
		logPath := filepath.Join(ctlDir, "events.jsonl")
		launch := fmt.Sprintf("%s --persona=claude --control=%s --log=%s",
			tmux.ShellQuote(bin), tmux.ShellQuote(controlPath), tmux.ShellQuote(logPath))
		if i == 0 {
			if _, err := tmux.DefaultClient.Run("new-session", "-d", "-s", session,
				"-x", "200", "-y", "50", "-c", dir, launch); err != nil {
				t.Fatalf("create fakeagent session: %v", err)
			}
			t.Cleanup(func() {
				_, _ = tmux.DefaultClient.Run("kill-session", "-t", session)
			})
		} else {
			if _, err := tmux.DefaultClient.Run("split-window", "-d", "-t", session+":0", "-c", dir, launch); err != nil {
				t.Fatalf("split fakeagent pane %d: %v", i, err)
			}
			_, _ = tmux.DefaultClient.Run("select-layout", "-t", session+":0", "tiled")
		}
		panes = append(panes, &fakeagentPane{
			t:           t,
			Session:     session,
			Persona:     "claude",
			controlPath: controlPath,
			logPath:     logPath,
		})
	}

	tmuxPanes, err := tmux.GetPanes(session)
	if err != nil || len(tmuxPanes) != count {
		t.Fatalf("enumerate fakeagent panes: err=%v got=%d want=%d", err, len(tmuxPanes), count)
	}
	for i, tp := range tmuxPanes {
		title := fmt.Sprintf("%s__cc_%d", session, i+1)
		if err := tmux.SetPaneTitle(tp.ID, title); err != nil {
			t.Fatalf("title fakeagent pane %s: %v", tp.ID, err)
		}
		panes[i].PaneID = tp.ID
	}
	for _, p := range panes {
		if _, ok := p.WaitForEvent("start", "", 10*time.Second); !ok {
			capture, _ := tmux.CapturePaneOutput(p.PaneID, 40)
			t.Fatalf("fakeagent pane %s did not start; pane shows:\n%s", p.PaneID, capture)
		}
	}
	return session, panes
}

// crotRegisterFailureDump logs the live pane captures, the fixture HOME's
// transcript/store listing, and the pending+history store contents when the
// test fails. Register AFTER starting panes so it runs before session cleanup.
func crotRegisterFailureDump(t *testing.T, logger *TestLogger, home string, panes []*fakeagentPane) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, p := range panes {
			capture, err := tmux.CapturePaneOutput(p.PaneID, 60)
			logger.Log("[DUMP] pane=%s capture_err=%v capture:\n%s", p.PaneID, err, capture)
			logger.LogJSON("fixture_events_"+p.PaneID, p.Events())
		}
		_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, ierr := d.Info(); ierr == nil {
				logger.Log("[DUMP] home_file=%s mtime=%s size=%d", path, info.ModTime().Format(time.RFC3339), info.Size())
			}
			return nil
		})
		for _, store := range []string{
			filepath.Join(home, ".ntm", "rotation_history", "pending.jsonl"),
			filepath.Join(home, ".ntm", "rotation_history", "rotations.jsonl"),
		} {
			if data, err := os.ReadFile(store); err == nil {
				logger.Log("[DUMP] %s:\n%s", store, strings.TrimSpace(string(data)))
			}
		}
	})
}

// crotAgentID returns the canonical agent ID (pane title) for pane index n.
func crotAgentID(session string, n int) string {
	return fmt.Sprintf("%s__cc_%d", session, n)
}

// crotOverThresholdFixture writes the standard over-threshold Claude fixture:
// 1 + 148000 + 1500 + 499 = 150000 tokens = 75% of the 200000 window.
func crotOverThresholdFixture(t *testing.T, logger *TestLogger, home, projDir, name string) string {
	t.Helper()
	return crotWriteClaudeTranscript(t, logger, home, projDir, name, 1, 148000, 1500, 499)
}

// =============================================================================
// Scenario 1 — threshold trigger end-to-end: over-threshold transcript usage
// produces a pending rotation with evidence, visible through BOTH
// `ntm rotate context pending` and --robot-context, and a second cycle does
// not duplicate it.
// =============================================================================

func TestContextRotationE2EThresholdEnqueuesPending(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "context-rotation-threshold")
	t.Cleanup(logger.Close)

	home := t.TempDir()
	projDir := crotResolvedTempDir(t)
	fixture := crotOverThresholdFixture(t, logger, home, projDir, "session-hot.jsonl")
	cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(60, false, ""))

	session, panes := crotStartPanes(t, projDir, 1)
	crotRegisterFailureDump(t, logger, home, panes)
	agentID := crotAgentID(session, 1)
	logger.Log("[SETUP] session=%s pane=%s agent=%s projDir=%s", session, panes[0].PaneID, agentID, projDir)

	_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)

	// Evidence contract: the enqueue decision is slog'd with the transcript
	// ground truth (tokens, limit, source path, confidence) — the same fields
	// published to the attention feed.
	if !strings.Contains(stderr, crotLogEnqueued) {
		t.Fatalf("coordinator stderr missing %q:\n%s", crotLogEnqueued, stderr)
	}
	for _, evidence := range []string{
		"tokens=150000",
		"limit=200000",
		"usage_percent=75",
		"threshold=60",
		"source=" + fixture,
		"confidence=high",
		"auto_confirm=false",
	} {
		if !strings.Contains(stderr, evidence) {
			t.Fatalf("enqueue evidence %q missing from coordinator stderr:\n%s", evidence, stderr)
		}
	}
	logger.Log("[EVIDENCE] enqueue slog carries tokens/limit/source/confidence verbatim")

	// Pending via the rotate CLI (reads the persistent store the trigger wrote).
	pendingEnv := crotFetchPending(t, logger, home, cfgPath)
	hits := crotPendingForSession(pendingEnv, session)
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 pending rotation for %s, got %d: %+v", session, len(hits), pendingEnv.Pending)
	}
	entry := pendingEnv.Pending[hits[0]]
	if entry.AgentID != agentID {
		t.Fatalf("pending agent_id=%q want %q", entry.AgentID, agentID)
	}
	if math.Abs(entry.ContextPercent-75) > 0.01 {
		t.Fatalf("pending context_percent=%v want 75 (150000/200000)", entry.ContextPercent)
	}
	if entry.TimeoutSeconds <= 0 {
		t.Fatalf("pending timeout_seconds=%d want > 0 (confirm_timeout_sec=600)", entry.TimeoutSeconds)
	}

	// Pending via --robot-context pending_rotations (same store, robot surface).
	ctxEnv := crotFetchRobotContext(t, logger, home, cfgPath, session)
	if len(ctxEnv.PendingRotations) != 1 {
		t.Fatalf("robot-context pending_rotations = %+v, want exactly 1", ctxEnv.PendingRotations)
	}
	robotPending := ctxEnv.PendingRotations[0]
	if robotPending.AgentID != agentID || robotPending.SessionName != session {
		t.Fatalf("robot pending identity wrong: %+v", robotPending)
	}
	if robotPending.PaneID != panes[0].PaneID {
		t.Fatalf("robot pending pane_id=%q want %q", robotPending.PaneID, panes[0].PaneID)
	}
	if robotPending.WorkDir != projDir {
		t.Fatalf("robot pending work_dir=%q want %q", robotPending.WorkDir, projDir)
	}
	if math.Abs(robotPending.ContextPercent-75) > 0.01 {
		t.Fatalf("robot pending context_percent=%v want 75", robotPending.ContextPercent)
	}

	// The robot context surface must agree the reading is transcript ground
	// truth at 75% — the same signal the trigger acted on.
	if len(ctxEnv.Agents) != 1 || ctxEnv.Agents[0].Source != "transcript" {
		t.Fatalf("robot-context agents=%+v want 1 transcript-sourced agent", ctxEnv.Agents)
	}

	// Second cycle: the stored pending entry (written by the FIRST process)
	// must suppress a duplicate enqueue in a brand-new coordinator process.
	_, stderr2 := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)
	if strings.Contains(stderr2, crotLogEnqueued) {
		t.Fatalf("second cycle re-enqueued despite stored pending entry:\n%s", stderr2)
	}
	pendingEnv2 := crotFetchPending(t, logger, home, cfgPath)
	if hits2 := crotPendingForSession(pendingEnv2, session); len(hits2) != 1 {
		t.Fatalf("pending count after second cycle = %d, want still exactly 1", len(hits2))
	}
	logger.Log("[PASS] threshold trigger enqueued once with evidence; cross-process dedupe held")
}

// =============================================================================
// Scenario 2 — safety gates live: a pane over threshold must NOT get a
// pending rotation while it is working, rate-limited, showing an interactive
// gate, or holding unsubmitted composer text; every skip is logged with its
// reason.
// =============================================================================

func TestContextRotationE2ESafetyGatesBlockRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}

	cases := []struct {
		name       string
		skipReason string
		arm        func(t *testing.T, logger *TestLogger, pane *fakeagentPane)
	}{
		{
			name:       "working",
			skipReason: "reason=working",
			arm: func(t *testing.T, logger *TestLogger, pane *fakeagentPane) {
				// 120s window comfortably outlives the coordinator cycle.
				pane.Control("work 120")
				logger.Log("[ARM] work 120 -> live spinner chrome")
			},
		},
		{
			name:       "rate_limited",
			skipReason: "reason=rate_limited",
			arm: func(t *testing.T, logger *TestLogger, pane *fakeagentPane) {
				pane.Control("ratelimit claude")
				logger.Log("[ARM] ratelimit claude -> usage-limit banner in transcript")
			},
		},
		{
			name: "interactive_gate",
			// The reason carries the detected gate prompt and is therefore
			// slog-quoted: reason="interactive_gate:do you trust ...".
			skipReason: `reason="interactive_gate:`,
			arm: func(t *testing.T, logger *TestLogger, pane *fakeagentPane) {
				pane.Control("gate trust")
				logger.Log("[ARM] gate trust -> full-screen trust dialog, no composer")
			},
		},
		{
			name:       "unsubmitted_input",
			skipReason: "reason=unsubmitted_input",
			arm: func(t *testing.T, logger *TestLogger, pane *fakeagentPane) {
				const draft = "draft context-rotation e2e text, deliberately never submitted"
				if err := tmux.SendKeys(pane.PaneID, draft, false); err != nil {
					t.Fatalf("type composer draft: %v", err)
				}
				// Confirm the composer really holds the text before firing the
				// coordinator (the gate reads a fresh capture).
				deadline := time.Now().Add(5 * time.Second)
				for {
					capture, _ := tmux.CapturePaneOutput(pane.PaneID, 40)
					if strings.Contains(capture, draft) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("composer never showed the draft; capture:\n%s", capture)
					}
					time.Sleep(200 * time.Millisecond)
				}
				logger.Log("[ARM] composer holds unsubmitted draft")
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			logger := NewTestLogger(t, "context-rotation-gate-"+tc.name)
			t.Cleanup(logger.Close)

			home := t.TempDir()
			projDir := crotResolvedTempDir(t)
			crotOverThresholdFixture(t, logger, home, projDir, "session-gated.jsonl")
			cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(60, false, ""))

			session, panes := crotStartPanes(t, projDir, 1)
			crotRegisterFailureDump(t, logger, home, panes)
			logger.Log("[SETUP] session=%s pane=%s gate=%s", session, panes[0].PaneID, tc.name)

			tc.arm(t, logger, panes[0])

			_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)

			if !strings.Contains(stderr, crotLogSkipped) {
				t.Fatalf("coordinator stderr missing %q (the gate should have fired):\n%s", crotLogSkipped, stderr)
			}
			if !strings.Contains(stderr, tc.skipReason) {
				t.Fatalf("skip reason %q missing from coordinator stderr:\n%s", tc.skipReason, stderr)
			}
			if strings.Contains(stderr, crotLogEnqueued) {
				t.Fatalf("gated pane was still enqueued:\n%s", stderr)
			}
			crotAssertNoPending(t, logger, home, cfgPath, session)
			logger.LogJSON("fixture_event_summary", map[string]int{
				"control": panes[0].CountEvents("control"),
				"render":  panes[0].CountEvents("render"),
				"submit":  panes[0].CountEvents("submit"),
			})
			logger.Log("[PASS] %s gate blocked rotation with logged reason", tc.name)
		})
	}
}

// =============================================================================
// Scenario 3 — default off & below threshold.
// =============================================================================

func TestContextRotationE2EDefaultOffAndBelowThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}

	t.Run("default_off", func(t *testing.T) {
		logger := NewTestLogger(t, "context-rotation-default-off")
		t.Cleanup(logger.Close)

		home := t.TempDir()
		projDir := crotResolvedTempDir(t)
		// Massively over any plausible threshold — must still be ignored.
		crotOverThresholdFixture(t, logger, home, projDir, "session-defaultoff.jsonl")
		// No [rotation] section at all: usage_percent_threshold stays 0 and
		// maybeCheckContextRotation must return before building the checker.
		cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(0, false, ""))

		session, panes := crotStartPanes(t, projDir, 1)
		crotRegisterFailureDump(t, logger, home, panes)
		logger.Log("[SETUP] session=%s pane=%s (threshold unset)", session, panes[0].PaneID)

		_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)
		for _, marker := range []string{crotLogEnqueued, crotLogSkipped, crotLogAutoConfirmed} {
			if strings.Contains(stderr, marker) {
				t.Fatalf("default-off cycle emitted rotation activity %q:\n%s", marker, stderr)
			}
		}
		crotAssertNoPending(t, logger, home, cfgPath, session)
		logger.Log("[PASS] threshold unset -> trigger fully off despite 75%% usage")
	})

	t.Run("below_threshold", func(t *testing.T) {
		logger := NewTestLogger(t, "context-rotation-below-threshold")
		t.Cleanup(logger.Close)

		home := t.TempDir()
		projDir := crotResolvedTempDir(t)
		// 1 + 99000 + 500 + 499 = 100000 tokens = 50% < 60% threshold.
		crotWriteClaudeTranscript(t, logger, home, projDir, "session-cool.jsonl", 1, 99000, 500, 499)
		cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(60, false, ""))

		session, panes := crotStartPanes(t, projDir, 1)
		crotRegisterFailureDump(t, logger, home, panes)
		logger.Log("[SETUP] session=%s pane=%s usage=50%% threshold=60%%", session, panes[0].PaneID)

		_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)
		for _, marker := range []string{crotLogEnqueued, crotLogSkipped, crotLogAutoConfirmed} {
			if strings.Contains(stderr, marker) {
				t.Fatalf("below-threshold cycle emitted rotation activity %q:\n%s", marker, stderr)
			}
		}
		crotAssertNoPending(t, logger, home, cfgPath, session)
		logger.Log("[PASS] 50%% usage below 60%% threshold -> no pending")
	})
}

// =============================================================================
// Scenario 4 — auto_confirm=true executes the rotation for real.
//
// What ConfirmRotation/rotateAgent (internal/context/rotation.go) actually
// does, and therefore what is asserted:
//  1. requests a handoff summary from the OLD pane (pasted + submitted),
//  2. spawns a replacement pane in the same session/workdir via
//     tmux split-window, titles it with the SAME canonical agent ID
//     (FormatPaneName reuses the extracted index), and types the configured
//     [agents] claude command into it — here a wrapper that execs the
//     fakeagent binary, making the replacement a real agent process,
//  3. delivers the handoff context to the replacement,
//  4. kills the old pane,
//  5. removes the pending entry and appends a success record to
//     ~/.ntm/rotation_history/rotations.jsonl.
//
// The replacement is NOT the old process respawned: it is a NEW pane (new
// pane ID, new PID) whose title rebinds the canonical agent identity.
// =============================================================================

func TestContextRotationE2EAutoConfirmExecutesRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "context-rotation-auto-confirm")
	t.Cleanup(logger.Close)

	fakeagentBin, err := ensureFakeagentBin()
	if err != nil {
		t.Fatalf("build fakeagent: %v", err)
	}

	home := t.TempDir()
	projDir := crotResolvedTempDir(t)
	fixture := crotOverThresholdFixture(t, logger, home, projDir, "session-hot.jsonl")
	// Age the hot fixture slightly (still "high" confidence, < 10min) so the
	// post-rotation fresh transcript is unambiguously newest by mtime.
	aged := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(fixture, aged, aged); err != nil {
		t.Fatalf("age hot fixture: %v", err)
	}

	// The replacement agent command: a wrapper execing the fakeagent binary
	// with its own control/log files, so the replacement pane runs a REAL
	// controllable agent process whose lifecycle we can observe.
	wrapDir := t.TempDir()
	replacementControl := filepath.Join(wrapDir, "control-replacement")
	replacementLog := filepath.Join(wrapDir, "events-replacement.jsonl")
	wrapper := filepath.Join(wrapDir, "fake-claude.sh")
	wrapperBody := fmt.Sprintf("#!/bin/bash\nexec %s --persona=claude --control=%s --log=%s\n",
		tmux.ShellQuote(fakeagentBin), tmux.ShellQuote(replacementControl), tmux.ShellQuote(replacementLog))
	if err := os.WriteFile(wrapper, []byte(wrapperBody), 0o755); err != nil {
		t.Fatalf("write replacement wrapper: %v", err)
	}
	cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(60, true, wrapper))

	session, panes := crotStartPanes(t, projDir, 1)
	crotRegisterFailureDump(t, logger, home, panes)
	oldPane := panes[0]
	agentID := crotAgentID(session, 1)
	logger.Log("[SETUP] session=%s old_pane=%s agent=%s wrapper=%s", session, oldPane.PaneID, agentID, wrapper)

	before, err := tmux.GetPanes(session)
	if err != nil || len(before) != 1 {
		t.Fatalf("pre-rotation pane enumeration: err=%v panes=%d", err, len(before))
	}
	oldPaneID, oldPanePID := before[0].ID, before[0].PID

	_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)

	if !strings.Contains(stderr, crotLogEnqueued) {
		t.Fatalf("auto-confirm cycle never enqueued:\n%s", stderr)
	}
	if !strings.Contains(stderr, "auto_confirm=true") {
		t.Fatalf("enqueue slog missing auto_confirm=true:\n%s", stderr)
	}
	if strings.Contains(stderr, crotLogConfirmFailed) {
		t.Fatalf("auto-confirm FAILED:\n%s", stderr)
	}
	if !strings.Contains(stderr, crotLogAutoConfirmed) {
		t.Fatalf("coordinator stderr missing %q:\n%s", crotLogAutoConfirmed, stderr)
	}

	// Replacement pane reality: exactly one pane, same canonical title,
	// DIFFERENT pane ID and PID (split-window replacement, old pane killed).
	after, err := tmux.GetPanes(session)
	if err != nil {
		t.Fatalf("post-rotation pane enumeration: %v", err)
	}
	if len(after) != 1 {
		ids := make([]string, 0, len(after))
		for _, p := range after {
			ids = append(ids, p.ID+"("+p.Title+")")
		}
		t.Fatalf("expected exactly 1 pane after rotation, got %d: %v", len(after), ids)
	}
	replacement := after[0]
	if replacement.ID == oldPaneID {
		t.Fatalf("pane ID unchanged (%s): no replacement pane was created", oldPaneID)
	}
	if replacement.PID == oldPanePID {
		t.Fatalf("pane PID unchanged (%d): replacement is not a new process tree", oldPanePID)
	}
	if replacement.Title != agentID {
		t.Fatalf("replacement title=%q want %q (canonical identity must rebind)", replacement.Title, agentID)
	}
	logger.Log("[REPLACEMENT] pane %s(pid %d) -> %s(pid %d), title %q rebound",
		oldPaneID, oldPanePID, replacement.ID, replacement.PID, replacement.Title)

	// The old fixture received the handoff-summary request and then died with
	// the pane: its event log must end in a terminate (SIGHUP from kill-pane).
	// The submit of the summary prompt is also ground truth that step 1 ran.
	if oldPane.CountEvents("submit") == 0 {
		t.Fatalf("old fixture never logged the handoff-summary submit; events=%+v", oldPane.Events())
	}
	if _, ok := oldPane.WaitForEvent("terminate", "", 10*time.Second); !ok {
		logger.Log("[NOTE] old fixture logged no terminate event (SIGHUP may outrun the log write); pane removal already proven above")
	}
	logger.LogJSON("old_fixture_event_summary", map[string]int{
		"submit":    oldPane.CountEvents("submit"),
		"terminate": oldPane.CountEvents("terminate"),
	})

	// The replacement is the wrapper's REAL fakeagent: it logged start, and it
	// received + submitted the handoff context (SendBuffer with enter).
	replacementPane := &fakeagentPane{
		t:           t,
		Session:     session,
		PaneID:      replacement.ID,
		Persona:     "claude",
		controlPath: replacementControl,
		logPath:     replacementLog,
	}
	if _, ok := replacementPane.WaitForEvent("start", "", 15*time.Second); !ok {
		capture, _ := tmux.CapturePaneOutput(replacement.ID, 60)
		t.Fatalf("replacement fakeagent never started; pane shows:\n%s", capture)
	}
	if _, ok := replacementPane.WaitForEvent("submit", "", 15*time.Second); !ok {
		if _, pasted := replacementPane.WaitForEvent("paste_end", "", 2*time.Second); !pasted {
			capture, _ := tmux.CapturePaneOutput(replacement.ID, 60)
			t.Fatalf("replacement fixture saw neither handoff submit nor paste; events=%+v pane:\n%s",
				replacementPane.Events(), capture)
		}
		logger.Log("[NOTE] handoff reached the replacement as a paste; submit Enter landed pre-raw-mode")
	}
	logger.LogJSON("replacement_fixture_event_summary", map[string]int{
		"start":  replacementPane.CountEvents("start"),
		"submit": replacementPane.CountEvents("submit"),
	})

	// Pending entry consumed by the confirm; audit record persisted.
	crotAssertNoPending(t, logger, home, cfgPath, session)
	historyPath := filepath.Join(home, ".ntm", "rotation_history", "rotations.jsonl")
	historyData, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("read rotation audit log %s: %v", historyPath, err)
	}
	successRecorded := false
	for _, line := range strings.Split(strings.TrimSpace(string(historyData)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid rotation history line %q: %v", line, err)
		}
		logger.LogJSON("rotation_history_record", record)
		if record["session_name"] == session && record["agent_id"] == agentID && record["success"] == true {
			successRecorded = true
		}
	}
	if !successRecorded {
		t.Fatalf("no successful rotation record for %s/%s in %s:\n%s", session, agentID, historyPath, historyData)
	}

	// NEXT cycle: the replacement agent's own fresh transcript is now the
	// newest for the cwd (exactly what a relaunched CLI produces), so the
	// trigger must see 5% usage and do nothing — no immediate re-rotation.
	// (Had the stale 75% transcript remained newest, a re-trigger would be the
	// CORRECT transcript-driven behavior; the in-process monitor rebind
	// regression (0484af4b) is pinned by the coordinator unit tests.)
	crotWriteClaudeTranscript(t, logger, home, projDir, "session-fresh.jsonl", 1, 9000, 500, 499)
	_, stderr2 := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)
	for _, marker := range []string{crotLogEnqueued, crotLogAutoConfirmed, crotLogSkipped} {
		if strings.Contains(stderr2, marker) {
			t.Fatalf("post-rotation cycle emitted rotation activity %q:\n%s", marker, stderr2)
		}
	}
	crotAssertNoPending(t, logger, home, cfgPath, session)
	stable, err := tmux.GetPanes(session)
	if err != nil || len(stable) != 1 || stable[0].ID != replacement.ID {
		t.Fatalf("replacement pane disturbed by follow-up cycle: err=%v panes=%+v", err, stable)
	}
	if replacementPane.CountEvents("terminate") != 0 {
		t.Fatalf("replacement fixture terminated during follow-up cycle; events=%+v", replacementPane.Events())
	}
	logger.Log("[PASS] auto-confirm executed a real rotation; fresh transcript prevented re-rotation")
}

// =============================================================================
// Scenario 5 — ambiguity: two same-persona panes in one cwd cannot be told
// apart by (agent type, cwd) correlation, so NEITHER gets a transcript
// reading and the trigger never fires — even with a valid over-threshold
// transcript present.
// =============================================================================

func TestContextRotationE2EAmbiguousCwdNoReading(t *testing.T) {
	if testing.Short() {
		t.Skip("live tmux E2E skipped in -short")
	}
	logger := NewTestLogger(t, "context-rotation-ambiguous")
	t.Cleanup(logger.Close)

	home := t.TempDir()
	projDir := crotResolvedTempDir(t)
	crotOverThresholdFixture(t, logger, home, projDir, "session-ambiguous.jsonl")
	cfgPath := crotWriteConfig(t, logger, t.TempDir(), crotConfigBody(60, false, ""))

	session, panes := crotStartPanes(t, projDir, 2)
	crotRegisterFailureDump(t, logger, home, panes)
	logger.Log("[SETUP] session=%s panes=%s,%s sharing cwd=%s", session, panes[0].PaneID, panes[1].PaneID, projDir)

	_, stderr := crotRunCoordinatorOnce(t, logger, home, cfgPath, session)
	for _, marker := range []string{crotLogEnqueued, crotLogSkipped, crotLogAutoConfirmed} {
		if strings.Contains(stderr, marker) {
			t.Fatalf("ambiguous cwd group produced rotation activity %q:\n%s", marker, stderr)
		}
	}
	crotAssertNoPending(t, logger, home, cfgPath, session)
	logger.Log("[PASS] ambiguous 2-pane cwd group: no transcript attribution, no rotation")
}
