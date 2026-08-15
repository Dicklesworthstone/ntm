package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// --- transcript fixtures (seeded under a temp HOME, mirroring
// internal/context/transcript_test.go) -------------------------------------

// claudeUsageLine builds a realistic Claude Code assistant transcript entry.
func claudeUsageLine(model string, input, cacheCreate, cacheRead, output int) string {
	return fmt.Sprintf(`{"parentUuid":"x","cwd":"/Users/x/proj","sessionId":"abc","type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","model":"%s","usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}},"timestamp":"2026-08-14T00:00:00.000Z"}`,
		model, input, cacheCreate, cacheRead, output)
}

// seedClaudeTranscript writes a Claude transcript for cwd under home.
func seedClaudeTranscript(t *testing.T, home, cwd string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", ntmctx.MungeProjectPath(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedCodexTranscript writes a Codex rollout transcript for cwd under home.
func seedCodexTranscript(t *testing.T, home, cwd string, totalTokens, contextWindow int) string {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "08", "14")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-test.jsonl")
	lines := []string{
		fmt.Sprintf(`{"timestamp":"2026-08-14T02:33:59.696Z","type":"session_meta","payload":{"session_id":"019f","cwd":%q,"model_provider":"openai"}}`, cwd),
		`{"timestamp":"2026-08-14T02:34:00.000Z","type":"turn_context","payload":{"cwd":"/x","model":"gpt-5.6-sol"}}`,
		fmt.Sprintf(`{"timestamp":"2026-08-14T02:34:06.831Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_output_tokens":0,"total_tokens":%d},"model_context_window":%d}}}`,
			totalTokens, totalTokens, contextWindow),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// redirectPendingStore isolates the global pending rotation store for a test.
func redirectPendingStore(t *testing.T) *ntmctx.PendingRotationStore {
	t.Helper()
	orig := ntmctx.DefaultPendingRotationStore
	store := ntmctx.NewPendingRotationStoreWithPath(filepath.Join(t.TempDir(), "pending.jsonl"))
	ntmctx.DefaultPendingRotationStore = store
	t.Cleanup(func() { ntmctx.DefaultPendingRotationStore = orig })
	return store
}

// rotationTestEnv wires a rotationChecker with a real Rotator (real
// pending/confirm storage machinery) plus fake tmux collaborators: fake pane
// lists, fake pane cwds, and fake capture strings for the safety gates.
// Transcript usage flows through the REAL LatestAgentTranscriptUsage against
// fixtures seeded under a temp HOME.
type rotationTestEnv struct {
	rc        *rotationChecker
	published []robot.ActuationRecord
	confirmed []string
}

func newRotationTestEnv(t *testing.T, threshold float64, autoConfirm bool, panes []tmux.Pane, cwds map[string]string, captures map[string]string, captureErrs map[string]error) *rotationTestEnv {
	t.Helper()

	rotCfg := config.DefaultContextRotationConfig()
	rotCfg.Enabled = true
	rotCfg.RequireConfirm = true
	rotCfg.ConfirmTimeoutSec = 300

	ctxMonitor := ntmctx.NewContextMonitor(ntmctx.DefaultMonitorConfig())
	rotator := ntmctx.NewRotator(ntmctx.RotatorConfig{
		Monitor: ctxMonitor,
		Config:  rotCfg,
	})

	env := &rotationTestEnv{}
	rc := &rotationChecker{
		session:     "rotsess",
		workDir:     "/work",
		threshold:   threshold,
		autoConfirm: autoConfirm,
		rotator:     rotator,
		ctxMonitor:  ctxMonitor,
		getPanes: func(string) ([]tmux.Pane, error) {
			return panes, nil
		},
		paneCwd: func(paneID string) (string, bool) {
			cwd, ok := cwds[paneID]
			return cwd, ok
		},
		transcriptUsage: func(agentType, cwd string) (*ntmctx.TranscriptUsage, bool) {
			return ntmctx.LatestAgentTranscriptUsage(agentType, cwd, time.Time{})
		},
		capturePane: func(paneID string, lines int) (string, error) {
			if err, ok := captureErrs[paneID]; ok {
				return "", err
			}
			return captures[paneID], nil
		},
		contextLimit: func(model string) int {
			// Deterministic registry window for the decision table; the
			// transcript-window path is exercised by the Codex fixtures.
			return 200000
		},
		storedPending: ntmctx.GetPendingRotationByID,
		publish: func(record robot.ActuationRecord) {
			env.published = append(env.published, record)
		},
		now: time.Now,
	}
	rc.enqueue = func(agentID, paneID string, usagePct float64) *ntmctx.PendingRotation {
		return rc.rotator.EnqueuePendingRotation(rc.session, agentID, paneID, usagePct, rc.workDir)
	}
	rc.confirm = func(agentID string) ntmctx.RotationResult {
		env.confirmed = append(env.confirmed, agentID)
		return ntmctx.RotationResult{
			Success:    true,
			OldAgentID: agentID,
			NewAgentID: agentID,
			State:      ntmctx.RotationStateCompleted,
			Timestamp:  time.Now(),
		}
	}
	env.rc = rc
	return env
}

func ccPane(id, title string) tmux.Pane {
	return tmux.Pane{ID: id, Title: title, Type: tmux.AgentClaude, Width: 120}
}

func codPane(id, title string) tmux.Pane {
	return tmux.Pane{ID: id, Title: title, Type: tmux.AgentCodex, Width: 120}
}

// idleCapture is a fresh capture of an idle agent pane: no live spinner, no
// rate limit chatter, no interactive gate, empty composer.
const idleCapture = "● Done. All tests pass.\n\n❯ \n"

// TestRotationChecker_DecisionTable exercises the trigger and every mandatory
// safety gate against real transcript fixtures and fake capture strings.
func TestRotationChecker_DecisionTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := "/Users/x/proj"

	// Claude fixture: 1 + 1312 + 170000 + 1528 = 172841 tokens.
	// Against the fixed 200000 registry window: 86.4% usage.
	transcriptPath := seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 170000, 1528))
	const wantTokens = 1 + 1312 + 170000 + 1528

	workingCapture := "✻ Simmering… (esc to interrupt · 12s)\n\n ❯\n"
	rateLimitCapture := "Error: rate limit exceeded. Please try again later.\n\n❯ \n"
	gateCapture := "  Browser didn't open? Use the URL below:\n  https://auth.example.com/device\n"
	composerCapture := "● Done.\n\n❯ fix the failing test in foo_test.go\n"
	queuedCapture := "● Done.\n\n❯ \n  Press up to edit queued messages\n"

	cases := []struct {
		name        string
		threshold   float64
		capture     string
		captureErr  error
		wantAction  string
		wantSkip    string
		wantPending bool
	}{
		{name: "below threshold is untouched", threshold: 95, capture: idleCapture, wantAction: "", wantPending: false},
		{name: "above threshold idle pane enqueues", threshold: 80, capture: idleCapture, wantAction: "enqueued", wantPending: true},
		{name: "working pane is skipped", threshold: 80, capture: workingCapture, wantAction: "skipped", wantSkip: "working"},
		{name: "rate limited pane is skipped", threshold: 80, capture: rateLimitCapture, wantAction: "skipped", wantSkip: "rate_limited"},
		{name: "gated pane is skipped", threshold: 80, capture: gateCapture, wantAction: "skipped", wantSkip: "interactive_gate:"},
		{name: "unsubmitted composer input is skipped", threshold: 80, capture: composerCapture, wantAction: "skipped", wantSkip: "unsubmitted_input"},
		{name: "queued messages are skipped", threshold: 80, capture: queuedCapture, wantAction: "skipped", wantSkip: "queued_messages"},
		{name: "capture failure is skipped", threshold: 80, captureErr: errors.New("boom"), wantAction: "skipped", wantSkip: "capture_failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := redirectPendingStore(t)
			pane := ccPane("%7", "rotsess__cc_1")
			env := newRotationTestEnv(t, tc.threshold, false,
				[]tmux.Pane{pane},
				map[string]string{"%7": cwd},
				map[string]string{"%7": tc.capture},
				map[string]error{},
			)
			if tc.captureErr != nil {
				env.rc.capturePane = func(string, int) (string, error) { return "", tc.captureErr }
			}

			decisions := env.rc.runOnce(t.Context())
			for _, d := range decisions {
				t.Logf("decision: %+v", d)
			}

			if tc.wantAction == "" {
				if len(decisions) != 0 {
					t.Fatalf("decisions = %+v, want none", decisions)
				}
				if len(env.published) != 0 {
					t.Fatalf("published = %+v, want none", env.published)
				}
				return
			}

			if len(decisions) != 1 {
				t.Fatalf("got %d decisions, want 1: %+v", len(decisions), decisions)
			}
			d := decisions[0]
			if d.Action != tc.wantAction {
				t.Fatalf("action = %q, want %q (decision %+v)", d.Action, tc.wantAction, d)
			}
			if tc.wantSkip != "" && !strings.HasPrefix(d.SkipReason, tc.wantSkip) {
				t.Fatalf("skip reason = %q, want prefix %q", d.SkipReason, tc.wantSkip)
			}

			// Evidence must be transcript-sourced.
			if d.Tokens != wantTokens {
				t.Errorf("evidence tokens = %d, want %d", d.Tokens, wantTokens)
			}
			if d.Limit != 200000 {
				t.Errorf("evidence limit = %d, want 200000", d.Limit)
			}
			if d.Source != transcriptPath {
				t.Errorf("evidence source = %q, want %q", d.Source, transcriptPath)
			}
			if d.Confidence == "" {
				t.Error("evidence confidence is empty")
			}
			wantPct := float64(wantTokens) / 200000 * 100
			if d.UsagePct < wantPct-0.01 || d.UsagePct > wantPct+0.01 {
				t.Errorf("usage pct = %.3f, want %.3f", d.UsagePct, wantPct)
			}

			// Every decision must reach the attention feed with evidence.
			if len(env.published) != 1 {
				t.Fatalf("published %d records, want 1: %+v", len(env.published), env.published)
			}
			rec := env.published[0]
			if rec.Action != "context_rotation" || rec.Session != "rotsess" {
				t.Errorf("published record = %+v", rec)
			}
			if !strings.Contains(rec.MessagePreview, fmt.Sprintf("tokens=%d", wantTokens)) ||
				!strings.Contains(rec.MessagePreview, "limit=200000") ||
				!strings.Contains(rec.MessagePreview, "source="+transcriptPath) ||
				!strings.Contains(rec.MessagePreview, "confidence=") {
				t.Errorf("published evidence missing fields: %q", rec.MessagePreview)
			}

			// Pending storage must match the CLI 'rotate context pending' surface.
			stored, err := store.Get("rotsess__cc_1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantPending {
				if stored == nil {
					t.Fatal("no stored pending rotation after enqueue")
				}
				if stored.PaneID != "%7" || stored.SessionName != "rotsess" {
					t.Errorf("stored pending = %+v", stored)
				}
				if !env.rc.rotator.HasPendingRotation("rotsess__cc_1") {
					t.Error("rotator lost in-memory pending rotation")
				}
			} else if stored != nil {
				t.Errorf("unexpected stored pending rotation: %+v", stored)
			}

			if len(env.confirmed) != 0 {
				t.Errorf("auto-confirm fired with auto_confirm=false: %v", env.confirmed)
			}
		})
	}
}

// TestRotationChecker_CodexTranscriptWindow verifies the Codex path: the
// transcript-reported context window (not the models registry) sets the limit.
func TestRotationChecker_CodexTranscriptWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirectPendingStore(t)
	cwd := "/Users/x/codexproj"
	path := seedCodexTranscript(t, home, cwd, 240000, 258400) // 92.9%

	pane := codPane("%3", "rotsess__cod_1")
	env := newRotationTestEnv(t, 80, false,
		[]tmux.Pane{pane},
		map[string]string{"%3": cwd},
		map[string]string{"%3": idleCapture},
		nil,
	)
	// The registry seam would report a wrong window; the transcript's own
	// window must win.
	env.rc.contextLimit = func(string) int { t.Error("registry consulted despite transcript window"); return 1 }

	decisions := env.rc.runOnce(t.Context())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	if len(decisions) != 1 || decisions[0].Action != "enqueued" {
		t.Fatalf("decisions = %+v, want one enqueued", decisions)
	}
	if decisions[0].Limit != 258400 || decisions[0].Tokens != 240000 {
		t.Errorf("evidence = %+v, want tokens=240000 limit=258400", decisions[0])
	}
	if decisions[0].Source != path {
		t.Errorf("source = %q, want %q", decisions[0].Source, path)
	}
}

// TestRotationChecker_AmbiguousCwdIsIgnored verifies the fixed ambiguity rule
// (mirroring robot's resolvePaneTranscripts): when two same-type panes share
// one cwd, neither is attributed a transcript and the trigger never fires.
func TestRotationChecker_AmbiguousCwdIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirectPendingStore(t)
	cwd := "/Users/x/proj"
	seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 190000, 1528))

	env := newRotationTestEnv(t, 50, false,
		[]tmux.Pane{ccPane("%1", "rotsess__cc_1"), ccPane("%2", "rotsess__cc_2")},
		map[string]string{"%1": cwd, "%2": cwd},
		map[string]string{"%1": idleCapture, "%2": idleCapture},
		nil,
	)

	decisions := env.rc.runOnce(t.Context())
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none for ambiguous cwd attribution", decisions)
	}
	if len(env.published) != 0 {
		t.Fatalf("published = %+v, want none", env.published)
	}
}

// TestRotationChecker_AutoConfirm verifies auto_confirm executes through the
// confirm path after enqueueing, and that the rotator's monitor knows the
// agent.
func TestRotationChecker_AutoConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := redirectPendingStore(t)
	cwd := "/Users/x/proj"
	seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 170000, 1528))

	env := newRotationTestEnv(t, 80, true,
		[]tmux.Pane{ccPane("%7", "rotsess__cc_1")},
		map[string]string{"%7": cwd},
		map[string]string{"%7": idleCapture},
		nil,
	)

	decisions := env.rc.runOnce(t.Context())
	for _, d := range decisions {
		t.Logf("decision: %+v", d)
	}
	if len(decisions) != 1 || decisions[0].Action != "auto_confirmed" {
		t.Fatalf("decisions = %+v, want one auto_confirmed", decisions)
	}
	if len(env.confirmed) != 1 || env.confirmed[0] != "rotsess__cc_1" {
		t.Fatalf("confirmed = %v, want [rotsess__cc_1]", env.confirmed)
	}
	// Enqueue request + confirm outcome must both be published.
	if len(env.published) != 2 {
		t.Fatalf("published %d records, want 2: %+v", len(env.published), env.published)
	}
	if env.published[0].ReasonCode != "context_rotation_enqueued" ||
		env.published[1].ReasonCode != "context_rotation_auto_confirm" {
		t.Errorf("reason codes = %q, %q", env.published[0].ReasonCode, env.published[1].ReasonCode)
	}
	if state := env.rc.ctxMonitor.GetState("rotsess__cc_1"); state == nil {
		t.Error("agent not registered in rotator monitor before confirm")
	}
	// The pending entry stays owned by the confirm path (faked here), so the
	// store still holds it; a real ConfirmRotation removes it.
	if stored, err := store.Get("rotsess__cc_1"); err != nil || stored == nil {
		t.Fatalf("stored pending = %v, %v; want enqueued entry", stored, err)
	}
}

// TestRotationChecker_ExistingPendingNotRepublished verifies a pending
// rotation is not re-enqueued or re-published every tick.
func TestRotationChecker_ExistingPendingNotRepublished(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirectPendingStore(t)
	cwd := "/Users/x/proj"
	seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 170000, 1528))

	env := newRotationTestEnv(t, 80, false,
		[]tmux.Pane{ccPane("%7", "rotsess__cc_1")},
		map[string]string{"%7": cwd},
		map[string]string{"%7": idleCapture},
		nil,
	)

	first := env.rc.runOnce(t.Context())
	if len(first) != 1 || first[0].Action != "enqueued" {
		t.Fatalf("first pass = %+v, want one enqueued", first)
	}
	second := env.rc.runOnce(t.Context())
	if len(second) != 0 {
		t.Fatalf("second pass = %+v, want none while pending exists", second)
	}
	if len(env.published) != 1 {
		t.Fatalf("published %d records, want 1", len(env.published))
	}
}

// TestRotationChecker_StorePendingFromAnotherProcess verifies the checker
// respects pending rotations another process wrote to the shared store.
func TestRotationChecker_StorePendingFromAnotherProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	redirectPendingStore(t)
	cwd := "/Users/x/proj"
	seedClaudeTranscript(t, home, cwd,
		claudeUsageLine("claude-opus-4-6", 1, 1312, 170000, 1528))

	if err := ntmctx.AddPendingRotation(&ntmctx.PendingRotation{
		AgentID:     "rotsess__cc_1",
		SessionName: "rotsess",
		PaneID:      "%7",
		CreatedAt:   time.Now(),
		TimeoutAt:   time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	env := newRotationTestEnv(t, 80, false,
		[]tmux.Pane{ccPane("%7", "rotsess__cc_1")},
		map[string]string{"%7": cwd},
		map[string]string{"%7": idleCapture},
		nil,
	)

	decisions := env.rc.runOnce(t.Context())
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none when store already holds a pending rotation", decisions)
	}
}

// TestRunCycle_RotationTriggerGating is the integration-shaped tick test: the
// coordinator cycle invokes the rotation checker exactly when the threshold is
// configured, and never touches it otherwise (default-off guarantee).
func TestRunCycle_RotationTriggerGating(t *testing.T) {
	origGetPanesWithActivity := getPanesWithActivity
	origCaptureForHealthCheckWithCtx := captureForHealthCheckWithCtx
	t.Cleanup(func() {
		getPanesWithActivity = origGetPanesWithActivity
		captureForHealthCheckWithCtx = origCaptureForHealthCheckWithCtx
	})
	getPanesWithActivity = func(session string) ([]tmux.PaneActivity, error) {
		return []tmux.PaneActivity{
			{
				Pane: tmux.Pane{
					ID:    "%0",
					Index: 0,
					Title: "rot-tick__cc_1",
					Type:  tmux.AgentClaude,
				},
				LastActivity: time.Now().UTC(),
			},
		}, nil
	}
	captureForHealthCheckWithCtx = func(_ context.Context, paneID string) (string, error) {
		return idleCapture, nil
	}

	c := New("rot-tick", t.TempDir(), nil, "Coordinator")
	c.monitor = NewAgentMonitor(c.session, nil, c.projectKey)

	calls := 0
	c.rotation = &rotationChecker{
		session:   "rot-tick",
		threshold: 90,
		getPanes: func(string) ([]tmux.Pane, error) {
			calls++
			return nil, nil
		},
	}

	// Threshold 0 (default): the checker must never run.
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 0 {
		t.Fatalf("rotation checker ran %d times with threshold 0, want 0", calls)
	}

	// Threshold configured: the tick runs the checker once per cycle.
	c.config.RotationUsageThreshold = 90
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 1 {
		t.Fatalf("rotation checker ran %d times, want 1", calls)
	}
	if _, err := c.RunCycle(t.Context()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if calls != 2 {
		t.Fatalf("rotation checker ran %d times after two cycles, want 2", calls)
	}
}

// TestMaybeCheckContextRotation_DefaultOffBuildsNothing pins the default-off
// guarantee at the construction level: no checker object is ever created when
// the threshold is unset.
func TestMaybeCheckContextRotation_DefaultOffBuildsNothing(t *testing.T) {
	c := New("rot-off", t.TempDir(), nil, "Coordinator")
	c.maybeCheckContextRotation(t.Context())
	if c.rotation != nil {
		t.Fatal("rotation checker constructed despite threshold 0")
	}
}
