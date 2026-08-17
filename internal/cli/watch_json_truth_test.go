package cli

// watch_json_truth_test.go — unit proofs for bd-ws7-docs-ux-truth-tqh3l.4
// (H4): watch surfaces must emit valid NDJSON frames under --json, extract
// --copy/--apply under --json must fail with a loud UNSUPPORTED_COMBINATION
// envelope, and analytics chars_sent must equal payload bytes and accumulate
// (including agents registered via `ntm add`). Every flag combination
// asserts either valid NDJSON frames or an error envelope — never silence.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// captureStdoutForTest redirects os.Stdout and returns a fetch func that
// closes the pipe and returns everything written.
func captureStdoutForTest(t *testing.T) func() string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	return func() string {
		_ = w.Close()
		<-done
		os.Stdout = orig
		return buf.String()
	}
}

// parseNDJSONLines asserts every non-empty line is exactly one standalone
// JSON object and returns the decoded frames.
func parseNDJSONLines(t *testing.T, out string) []map[string]interface{} {
	t.Helper()
	var frames []map[string]interface{}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame map[string]interface{}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("line %d is not a standalone JSON object (NDJSON framing broken): %q: %v", i, line, err)
		}
		frames = append(frames, frame)
	}
	return frames
}

// The activity watch NDJSON loop must emit one frame per tick even when data
// collection fails (nonexistent session): a success:false frame with an
// error, agents, and summary — the stream is NEVER silent under --json.
func TestActivityWatchNDJSONLoop_ErrorFramesNeverSilent(t *testing.T) {
	session := fmt.Sprintf("ntm-ndjson-unit-%d", os.Getpid())
	var buf bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	err := activityWatchNDJSONLoop(ctx, &buf, session, activityOptions{interval: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("activityWatchNDJSONLoop returned error: %v", err)
	}

	frames := parseNDJSONLines(t, buf.String())
	if len(frames) < 2 {
		t.Fatalf("want >=2 NDJSON frames from the watch stream, got %d (output %q)", len(frames), buf.String())
	}
	for i, frame := range frames {
		if success, ok := frame["success"].(bool); !ok || success {
			t.Fatalf("frame %d: success = %v, want false for a nonexistent session", i, frame["success"])
		}
		if errMsg, _ := frame["error"].(string); errMsg == "" {
			t.Fatalf("frame %d: error must be populated, got %+v", i, frame)
		}
		if frame["session"] != session {
			t.Fatalf("frame %d: session = %v, want %q", i, frame["session"], session)
		}
		if _, ok := frame["agents"]; !ok {
			t.Fatalf("frame %d: agents array missing (arrays-never-null contract)", i)
		}
	}
}

// The single-shot --json envelope and the NDJSON frames share one shape.
func TestBuildActivityJSONEnvelope_Shape(t *testing.T) {
	since := time.Now().Add(-90 * time.Second)
	result := &activityResult{
		Session:    "shape-session",
		CapturedAt: time.Now(),
		Agents: []agentInfo{{
			Pane: 1, AgentType: "claude", State: "GENERATING",
			Confidence: 0.9, Velocity: 12.5, Duration: 90 * time.Second, StateSince: since,
		}},
		Summary: map[string]int{"GENERATING": 1},
	}
	env := buildActivityJSONEnvelope(result)
	if !env.Success || env.Session != "shape-session" {
		t.Fatalf("envelope header wrong: %+v", env)
	}
	if len(env.Agents) != 1 || env.Agents[0].AgentType != "claude" || env.Agents[0].StateSince == "" {
		t.Fatalf("agent row wrong: %+v", env.Agents)
	}
	if env.Summary["GENERATING"] != 1 {
		t.Fatalf("summary wrong: %+v", env.Summary)
	}
	// Frames must be single-line under compact encoding.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(env); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Fatalf("compact frame spans %d newlines, want exactly 1", got)
	}
}

// `ntm watch --json` pane output must be an NDJSON pane_output frame with
// pane identity and the non-blank lines — not the styled text stream.
func TestEmitWatchPaneOutput_JSONFrame(t *testing.T) {
	prev := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prev })

	fetch := captureStdoutForTest(t)
	pane := tmux.Pane{ID: "%7", Index: 2, Title: "proj__cc_1", Type: tmux.AgentClaude}
	emitWatchPaneOutput("proj", pane, "line one\n\n   \nline two", watchOptions{}, theme.Current())
	out := fetch()

	frames := parseNDJSONLines(t, out)
	if len(frames) != 1 {
		t.Fatalf("want exactly 1 pane_output frame, got %d (%q)", len(frames), out)
	}
	frame := frames[0]
	if frame["event"] != "pane_output" {
		t.Fatalf("event = %v, want pane_output", frame["event"])
	}
	if frame["session"] != "proj" || frame["pane"] != "proj__cc_1" || frame["pane_id"] != "%7" {
		t.Fatalf("pane identity wrong: %+v", frame)
	}
	lines, ok := frame["lines"].([]interface{})
	if !ok || len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("lines = %v, want the two non-blank lines", frame["lines"])
	}
	if ts, _ := frame["timestamp"].(string); ts == "" {
		t.Fatalf("timestamp missing: %+v", frame)
	}

	// Blank-only output emits nothing — but plain text mode is unaffected
	// (the JSON contract is about framing, not about inventing events).
	fetch2 := captureStdoutForTest(t)
	emitWatchPaneOutput("proj", pane, "\n   \n", watchOptions{}, theme.Current())
	if got := strings.TrimSpace(fetch2()); got != "" {
		t.Fatalf("blank-only output produced a frame: %q", got)
	}
}

// In text mode emitWatchPaneOutput must keep printing the human stream (the
// NDJSON branch must not swallow it).
func TestEmitWatchPaneOutput_TextModeStillPrints(t *testing.T) {
	prev := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = prev })

	fetch := captureStdoutForTest(t)
	pane := tmux.Pane{ID: "%7", Index: 2, Title: "proj__cc_1", Type: tmux.AgentClaude}
	emitWatchPaneOutput("proj", pane, "hello text stream", watchOptions{noColor: true, noTimestamps: true}, theme.Current())
	out := fetch()
	if !strings.Contains(out, "hello text stream") || !strings.Contains(out, "proj__cc_1") {
		t.Fatalf("text stream lost: %q", out)
	}
}

// Every emitted watch frame is one line and carries event + timestamp.
func TestEmitWatchJSONFrame_Framing(t *testing.T) {
	fetch := captureStdoutForTest(t)
	emitWatchJSONFrame(watchJSONFrame{Event: "watch_started", Session: "s1"})
	emitWatchJSONFrame(watchJSONFrame{Event: "bead_status", Session: "s1", Bead: "bd-1", Status: "in_progress"})
	out := fetch()

	frames := parseNDJSONLines(t, out)
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d", len(frames))
	}
	if frames[0]["event"] != "watch_started" || frames[1]["event"] != "bead_status" {
		t.Fatalf("events wrong: %+v", frames)
	}
	for i, f := range frames {
		if ts, _ := f["timestamp"].(string); ts == "" {
			t.Fatalf("frame %d missing timestamp", i)
		}
	}
	if frames[1]["status"] != "in_progress" || frames[1]["bead"] != "bd-1" {
		t.Fatalf("bead_status fields wrong: %+v", frames[1])
	}
}

// extract --copy/--apply under --json used to be silently dropped; each
// combination must now emit a loud UNSUPPORTED_COMBINATION failure envelope
// and exit non-zero via the errJSONFailure sentinel.
func TestRunExtract_CopyApplyUnderJSON_LoudUnsupportedCombination(t *testing.T) {
	prev := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prev })

	cases := []struct {
		name      string
		copyFlag  bool
		applyFlag bool
		wantInMsg string
	}{
		{"copy", true, false, "--copy"},
		{"apply", false, true, "--apply"},
		{"copy_and_apply", true, true, "--copy/--apply"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fetch := captureStdoutForTest(t)
			err := runExtract("any-session", "", "", false, 100, tc.copyFlag, tc.applyFlag, 0)
			out := fetch()

			if !errors.Is(err, errJSONFailure) {
				t.Fatalf("err = %v, want errJSONFailure (non-zero exit)", err)
			}
			var envelope struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
				Code    string `json:"code"`
				Hint    string `json:"hint"`
			}
			if uerr := json.Unmarshal([]byte(out), &envelope); uerr != nil {
				t.Fatalf("output is not a JSON envelope: %v (%q)", uerr, out)
			}
			if envelope.Success {
				t.Fatalf("success = true, want false: %q", out)
			}
			if envelope.Code != "UNSUPPORTED_COMBINATION" {
				t.Fatalf("code = %q, want UNSUPPORTED_COMBINATION", envelope.Code)
			}
			if !strings.Contains(envelope.Error, tc.wantInMsg) || !strings.Contains(envelope.Error, "--json") {
				t.Fatalf("error %q does not name the unsupported combination %s + --json", envelope.Error, tc.wantInMsg)
			}
			if envelope.Hint == "" {
				t.Fatalf("hint missing — the error must tell the caller what to do instead")
			}
		})
	}
}

// Analytics chars_sent: per-agent chars must EQUAL the event's payload byte
// count (exact-sum division across multi-type targets) and accumulate across
// sends; agents registered via `ntm add` (agent_add events) must be counted
// while agent_spawn events stay excluded (already covered by session_create).
func TestAggregateStats_CharsSentEqualsPayloadAndAddCounted(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -30)

	testEvents := []events.Event{
		// Spawn-created agents: counted via session_create only.
		{Timestamp: now.Add(-5 * time.Hour), Type: events.EventSessionCreate, Session: "s", Data: map[string]interface{}{"codex_count": float64(1)}},
		{Timestamp: now.Add(-5 * time.Hour), Type: events.EventAgentSpawn, Session: "s", Data: map[string]interface{}{"agent_type": "codex"}},
		// The audit's blind spot: an `ntm add`-registered claude agent.
		{Timestamp: now.Add(-4 * time.Hour), Type: events.EventAgentAdd, Session: "s", Data: map[string]interface{}{"agent_type": "claude"}},
		// Send 1 to the added claude pane: 42 payload bytes.
		{Timestamp: now.Add(-3 * time.Hour), Type: events.EventPromptSend, Session: "s", Data: map[string]interface{}{"prompt_length": float64(42), "target_types": "claude"}},
		// Send 2 accumulates: 58 more bytes.
		{Timestamp: now.Add(-2 * time.Hour), Type: events.EventPromptSend, Session: "s", Data: map[string]interface{}{"prompt_length": float64(58), "target_types": "claude"}},
		// Multi-type send: 43 bytes split exactly (remainder to first target).
		{Timestamp: now.Add(-1 * time.Hour), Type: events.EventPromptSend, Session: "s", Data: map[string]interface{}{"prompt_length": float64(43), "target_types": "claude,codex"}},
	}

	stats := aggregateStats(testEvents, 30, "", cutoff)

	// Exact-value totals: 42 + 58 + 43.
	if stats.TotalCharsSent != 143 {
		t.Fatalf("TotalCharsSent = %d, want 143", stats.TotalCharsSent)
	}

	claude := stats.AgentBreakdown["claude"]
	codex := stats.AgentBreakdown["codex"]

	// Send 1 alone must EQUAL its payload (42); with send 2 it accumulates
	// to 100; the multi-type send adds 22 (21 + remainder 1).
	if claude.CharsSent != 122 {
		t.Fatalf("claude.CharsSent = %d, want 122 (42+58 accumulated + 22 of the split)", claude.CharsSent)
	}
	if codex.CharsSent != 21 {
		t.Fatalf("codex.CharsSent = %d, want 21", codex.CharsSent)
	}
	// Per-agent chars for the split event sum to its payload exactly.
	if got := (claude.CharsSent - 100) + codex.CharsSent; got != 43 {
		t.Fatalf("split event chars sum to %d, want the exact 43 payload bytes", got)
	}

	// ntm-add agent counted, attributed to claude, not lumped elsewhere.
	if claude.Count != 1 {
		t.Fatalf("claude.Count = %d, want 1 (agent_add must be counted)", claude.Count)
	}
	// codex: 1 from session_create; the agent_spawn event must NOT double it.
	if codex.Count != 1 {
		t.Fatalf("codex.Count = %d, want 1 (agent_spawn must not double-count)", codex.Count)
	}
	if stats.TotalAgents != 2 {
		t.Fatalf("TotalAgents = %d, want 2 (1 spawned + 1 added)", stats.TotalAgents)
	}
}

// buildSessionDetails counts add-registered agents into the session summary.
func TestBuildSessionDetails_CountsAgentAdd(t *testing.T) {
	now := time.Now().UTC()
	testEvents := []events.Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: events.EventSessionCreate, Session: "s", Data: map[string]interface{}{"claude_count": float64(2)}},
		{Timestamp: now.Add(-1 * time.Hour), Type: events.EventAgentAdd, Session: "s", Data: map[string]interface{}{"agent_type": "codex"}},
	}
	details := buildSessionDetails(testEvents)
	if len(details) != 1 {
		t.Fatalf("got %d sessions, want 1", len(details))
	}
	if details[0].AgentCount != 3 {
		t.Fatalf("AgentCount = %d, want 3 (2 spawned + 1 added)", details[0].AgentCount)
	}
}
