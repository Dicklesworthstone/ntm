package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ompFixture loads a verbatim omp v18.2.3 tmux capture from the agent
// package's testdata (the single home of the omp chrome fixtures).
func ompFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(ompFixturePath(t, name))
	if err != nil {
		t.Fatalf("load omp fixture %s: %v", name, err)
	}
	return string(data)
}

func ompFixturePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "agent", "testdata", name))
	if err != nil {
		t.Fatalf("resolve omp fixture %s: %v", name, err)
	}
	return path
}

func TestDetectAgentFromCommand_OmpExecutableOnly(t *testing.T) {
	for _, cmd := range []string{"omp", "omp --auto-approve", "/home/u/.bun/bin/omp --model opus", "OMP"} {
		if got := detectAgentFromCommand(cmd); got != AgentOmp {
			t.Errorf("detectAgentFromCommand(%q) = %q, want omp", cmd, got)
		}
	}
	for _, cmd := range []string{"libomp-bench", "rg omp", "gcc -fopenmp main.c", "ompi_info", "vim omp.go"} {
		if got := detectAgentFromCommand(cmd); got == AgentOmp {
			t.Errorf("detectAgentFromCommand(%q) = omp, want not omp", cmd)
		}
	}
	if got := detectAgentFromArgv([]string{"/home/u/.bun/bin/omp", "--auto-approve"}); got != AgentOmp {
		t.Errorf("argv[0] omp = %q, want omp", got)
	}
	if got := detectAgentFromArgv([]string{"rg", "omp", "src"}); got == AgentOmp {
		t.Error("an omp argument must not classify the pane as omp")
	}
}

func TestDetectAgentFromSelfTitle_Omp(t *testing.T) {
	for _, title := range []string{"π > Reply With the Word Ok", "π ⠙ Fix the parser", "π > omp-probe"} {
		if got := detectAgentFromSelfTitle(title); got != AgentOmp {
			t.Errorf("detectAgentFromSelfTitle(%q) = %q, want omp", title, got)
		}
	}
	for _, title := range []string{"π", "pi > x", "π is irrational", "proj__cc_1"} {
		if got := detectAgentFromSelfTitle(title); got == AgentOmp {
			t.Errorf("detectAgentFromSelfTitle(%q) = omp, want not omp", title)
		}
	}
}

func TestParsePaneAgentTypeOption_Omp(t *testing.T) {
	if got := ParsePaneAgentTypeOption("omp"); got != AgentOmp {
		t.Fatalf("ParsePaneAgentTypeOption(omp) = %q, want omp", got)
	}
	if got := FormatPaneName("proj", "oh-my-pi", 3, ""); got != "proj__omp_3" {
		t.Fatalf("FormatPaneName = %q, want proj__omp_3", got)
	}
}

func TestNeedsBufferSend_Omp(t *testing.T) {
	if needsBufferSend(AgentOmp, "single line with /path and @README.md") {
		t.Error("single-line omp prompts are typed")
	}
	if !needsBufferSend(AgentOmp, "line one\nline two") {
		t.Error("multi-line omp prompts must use bracketed paste so newlines stay in the draft")
	}
}

func TestComposerClearKeys_Omp(t *testing.T) {
	got := ComposerClearKeys(AgentOmp)
	if len(got) != 1 || got[0] != "C-c" {
		t.Fatalf("ComposerClearKeys(omp) = %v, want [C-c]", got)
	}
}

func TestInspectComposer_OmpRealCaptures(t *testing.T) {
	tests := []struct {
		file  string
		state ComposerState
	}{
		{"omp_nerd_idle_done.txt", ComposerState{MarkerVisible: true}},
		{"omp_nerd_working.txt", ComposerState{MarkerVisible: true}},
		{"omp_nerd_draft.txt", ComposerState{MarkerVisible: true, HoldsText: true}},
		{"omp_nerd_multiline_draft.txt", ComposerState{MarkerVisible: true, HoldsText: true}},
		{"omp_nerd_paste_token.txt", ComposerState{MarkerVisible: true, HoldsText: true}},
		{"omp_ascii_idle_done.txt", ComposerState{MarkerVisible: true}},
		{"omp_nerd_model_selector.txt", ComposerState{}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			if got := InspectComposer(ompFixture(t, tc.file), AgentOmp); got != tc.state {
				t.Fatalf("InspectComposer = %+v, want %+v", got, tc.state)
			}
		})
	}
}

func TestOmpComposerHoldsPayload(t *testing.T) {
	draft := ompFixture(t, "omp_nerd_draft.txt")
	if !ompComposerHoldsPayload(draft, "reply with the word ok") {
		t.Error("the delivered prompt sitting in the composer must count as held")
	}
	if ompComposerHoldsPayload(draft, "a completely different prompt") {
		t.Error("an unrelated draft must not count as the delivered payload")
	}
	if ompComposerHoldsPayload(ompFixture(t, "omp_nerd_working.txt"), "reply with the word ok") {
		t.Error("a submitted prompt echoed into the transcript is not held in the composer")
	}
	if !ompComposerHoldsPayload(ompFixture(t, "omp_nerd_multiline_draft.txt"), "first line of a two line prompt\nsecond line of it") {
		t.Error("a multi-line draft holds its first line")
	}
	if !ompComposerHoldsPayload(ompFixture(t, "omp_nerd_paste_token.txt"), "paste line 0\npaste line 1") {
		t.Error("a collapsed paste token is the payload by construction")
	}
}

// startOmpEmulatorPane runs script in a fresh 240x60 tmux session (wide
// enough that the 220-column fixtures never wrap) and returns the pane ID
// once the pane has painted.
func startOmpEmulatorPane(t *testing.T, script string) string {
	t.Helper()
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	name := fmt.Sprintf("ntm_test_omp_%d", time.Now().UnixNano())
	scriptPath := filepath.Join(t.TempDir(), "omp_emulator.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := DefaultClient.RunSilent("new-session", "-d", "-s", name, "-x", "240", "-y", "60", "bash "+scriptPath); err != nil {
		t.Skipf("failed to create omp emulator session: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(name) })
	paneID, err := DefaultClient.Run("display-message", "-p", "-t", name, "#{pane_id}")
	if err != nil {
		t.Fatalf("resolve emulator pane: %v", err)
	}
	return strings.TrimSpace(paneID)
}

func waitForPaneContains(t *testing.T, paneID, needle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := CapturePaneVisible(paneID); err == nil && strings.Contains(out, needle) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, _ := CapturePaneVisible(paneID)
	t.Fatalf("pane never showed %q; last capture:\n%s", needle, out)
}

func TestComposerReadyForDelivery_OmpLivePane(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	ctx := context.Background()
	cases := []struct {
		name    string
		fixture string
		needle  string
		ready   bool
	}{
		{name: "idle composer", fixture: "omp_nerd_idle_done.txt", needle: "Union Alpha", ready: true},
		{name: "working composer", fixture: "omp_nerd_working.txt", needle: "Working", ready: true},
		{name: "completion list below composer", fixture: "omp_nerd_autocomplete.txt", needle: "prewalk", ready: false},
		{name: "model selector overlay", fixture: "omp_nerd_model_selector.txt", needle: "Esc close", ready: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane := startOmpEmulatorPane(t, fmt.Sprintf("clear; cat %q; sleep 60\n", ompFixturePath(t, tc.fixture)))
			waitForPaneContains(t, pane, tc.needle)
			ready, reason := ComposerReadyForDelivery(ctx, pane, AgentOmp, 240)
			if ready != tc.ready {
				t.Fatalf("ComposerReadyForDelivery = (%v, %q), want ready=%v", ready, reason, tc.ready)
			}
		})
	}

	t.Run("blank booting pane", func(t *testing.T) {
		pane := startOmpEmulatorPane(t, "clear; sleep 60\n")
		time.Sleep(300 * time.Millisecond)
		if ready, reason := ComposerReadyForDelivery(ctx, pane, AgentOmp, 240); ready || !strings.Contains(reason, "booting") {
			t.Fatalf("blank omp pane = (%v, %q), want refused as booting", ready, reason)
		}
	})
}

func TestClearComposerContext_OmpLivePane(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	ctx := context.Background()
	draft := ompFixturePath(t, "omp_nerd_multiline_draft.txt")
	empty := ompFixturePath(t, "omp_nerd_idle_done.txt")

	t.Run("ctrl-c drains a multi-line draft in one round", func(t *testing.T) {
		script := fmt.Sprintf("trap 'clear; cat %q' INT\nclear; cat %q\nwhile true; do sleep 1; done\n", empty, draft)
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, "second line of it")
		result, err := ClearComposerContext(ctx, pane, AgentOmp)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Cleared || !result.Verified || result.Rounds != 1 {
			t.Fatalf("result = %+v, want cleared+verified in one round", result)
		}
	})

	t.Run("an empty composer gets no keys", func(t *testing.T) {
		// Ctrl+C twice on an empty omp draft quits omp; a clear of an
		// already-empty composer must therefore send nothing. The emulator
		// exits on any SIGINT so a stray press is observable.
		script := fmt.Sprintf("trap 'clear; echo GOT_CTRL_C; sleep 60' INT\nclear; cat %q\nwhile true; do sleep 1; done\n", empty)
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, "Union Alpha")
		result, err := ClearComposerContext(ctx, pane, AgentOmp)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Cleared || !result.Verified || result.Rounds != 0 {
			t.Fatalf("result = %+v, want cleared+verified with zero rounds", result)
		}
		time.Sleep(300 * time.Millisecond)
		if out, _ := CapturePaneVisible(pane); strings.Contains(out, "GOT_CTRL_C") {
			t.Fatal("ClearComposerContext sent Ctrl+C to an empty omp composer")
		}
	})

	t.Run("a draft that survives every round is reported, not looped on", func(t *testing.T) {
		script := fmt.Sprintf("trap '' INT\nclear; cat %q\nwhile true; do sleep 1; done\n", draft)
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, "second line of it")
		result, err := ClearComposerContext(ctx, pane, AgentOmp)
		if err != nil {
			t.Fatal(err)
		}
		if result.Cleared || !result.Verified || result.Blocker != ComposerBlockerInput ||
			result.Rounds != composerClearMaxRounds || !strings.Contains(result.Residual, "second line of it") {
			t.Fatalf("result = %+v, want verified input blocker after %d rounds", result, composerClearMaxRounds)
		}
	})
}

func TestVerifyOmpSubmission_OmpLivePane(t *testing.T) {
	if testing.Short() {
		t.Skip("drives live tmux panes")
	}
	ctx := context.Background()
	message := "reply with the word ok"

	t.Run("stranded draft gets one rescue Enter", func(t *testing.T) {
		script := fmt.Sprintf("clear; cat %q\nread -r _\nclear; cat %q\nsleep 60\n",
			ompFixturePath(t, "omp_nerd_draft.txt"), ompFixturePath(t, "omp_nerd_working.txt"))
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, message)
		confirmed, rescued, err := VerifyOmpSubmissionContext(ctx, pane, message, 240)
		if err != nil || !confirmed || !rescued {
			t.Fatalf("VerifyOmpSubmission = (confirmed=%v, rescued=%v, err=%v), want confirmed after rescue", confirmed, rescued, err)
		}
	})

	t.Run("busy pane still holding the payload is rescued, not confirmed", func(t *testing.T) {
		// Mid-turn the composer stays live, so "working" is no evidence the
		// payload submitted: an Enter swallowed by the completion list leaves
		// it in the box while the previous turn's spinner keeps running.
		working := ompFixture(t, "omp_nerd_working.txt")
		lines := strings.Split(strings.TrimRight(working, "\n"), "\n")
		last := lines[len(lines)-1]
		if !strings.HasPrefix(last, "╰─") || !strings.HasSuffix(last, "─╯") {
			t.Fatalf("fixture bottom row changed: %q", last)
		}
		lines[len(lines)-1] = "╰─ " + message + strings.Repeat(" ", 20) + "─╯"
		stranded := filepath.Join(t.TempDir(), "busy_stranded.txt")
		if err := os.WriteFile(stranded, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf("clear; cat %q\nread -r _\nclear; cat %q\nsleep 60\n", stranded, ompFixturePath(t, "omp_unicode_steering.txt"))
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, message)
		confirmed, rescued, err := VerifyOmpSubmissionContext(ctx, pane, message, 240)
		if err != nil || !confirmed || !rescued {
			t.Fatalf("VerifyOmpSubmission = (confirmed=%v, rescued=%v, err=%v), want confirmed after rescue", confirmed, rescued, err)
		}
	})

	t.Run("already submitted needs no rescue", func(t *testing.T) {
		script := fmt.Sprintf("clear; cat %q\nsleep 60\n", ompFixturePath(t, "omp_nerd_idle_done.txt"))
		pane := startOmpEmulatorPane(t, script)
		waitForPaneContains(t, pane, "Union Alpha")
		confirmed, rescued, err := VerifyOmpSubmissionContext(ctx, pane, message, 240)
		if err != nil || !confirmed || rescued {
			t.Fatalf("VerifyOmpSubmission = (confirmed=%v, rescued=%v, err=%v), want confirmed without rescue", confirmed, rescued, err)
		}
	})
}
