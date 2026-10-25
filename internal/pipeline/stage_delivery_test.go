package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the same stage runner Execute uses, with controlled pane transport.
// File locking, identity checks, output alignment and idle polling are real.
// No global client or clock is replaced; each test owns its executor and state.
type stageDeliveryClient struct {
	TmuxClient
	pane        tmux.Pane
	endpoint    string
	before      string
	after       string
	prompt      string
	prepares    int
	pastes      int
	legacyPaste int
	verifies    int
	width       int
	prepare     func(context.Context, int) error
	captureErr  error
	pasteErr    error
	verifyErr   error
	echoOnly    bool
	onPaste     func(int, string)
}

func (c *stageDeliveryClient) Endpoint() string { return c.endpoint }
func (c *stageDeliveryClient) GetPanes(string) ([]tmux.Pane, error) {
	return []tmux.Pane{c.pane}, nil
}
func (c *stageDeliveryClient) GetPanesContext(ctx context.Context, session string) ([]tmux.Pane, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.GetPanes(session)
}
func (c *stageDeliveryClient) PrepareDelivery(ctx context.Context, _ tmux.Pane, _ string) error {
	c.prepares++
	if c.prepare != nil {
		return c.prepare(ctx, c.prepares)
	}
	return ctx.Err()
}
func (c *stageDeliveryClient) CapturePaneOutputContext(ctx context.Context, _ string, _ int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.captureErr != nil {
		return "", c.captureErr
	}
	if c.pastes == 0 {
		return c.before, nil
	}
	if c.after != "" {
		return c.after, nil
	}
	output := c.before + "\n" + c.prompt
	if !c.echoOnly {
		output += "\nStage answer."
	}
	return output, nil
}
func (c *stageDeliveryClient) PasteKeys(string, string, bool) error {
	c.legacyPaste++
	return errors.New("stage used uncancellable transport despite a context-aware port")
}
func (c *stageDeliveryClient) PasteKeysContext(ctx context.Context, target, prompt string, enter bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target != c.pane.ID || !enter {
		return errors.New("stage lost its physical target or submit request")
	}
	c.pastes++
	c.prompt = prompt
	if c.onPaste != nil {
		c.onPaste(c.pastes, prompt)
	}
	return c.pasteErr
}
func (c *stageDeliveryClient) VerifySubmission(ctx context.Context, target, prompt, _ string, width int) error {
	c.verifies++
	c.width = width
	if target != c.pane.ID || prompt != c.prompt {
		return errors.New("verified a different delivery")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return c.verifyErr
}

type stageIdleDetector struct {
	status.Detector
	calls  int
	onIdle func(int)
}

func (d *stageIdleDetector) Detect(string) (status.AgentStatus, error) {
	d.calls++
	if d.onIdle != nil {
		d.onIdle(d.calls)
	}
	return status.AgentStatus{State: status.StateIdle}, nil
}

func newStageDeliveryFixture(t *testing.T) (*Executor, *stageDeliveryClient, *stageIdleDetector) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	client := &stageDeliveryClient{
		pane:     tmux.Pane{ID: "%71", PID: 7100, Type: tmux.AgentClaude, Command: "claude", Width: 93},
		endpoint: "local", before: "Existing transcript.",
	}
	detector := &stageIdleDetector{}
	cfg := DefaultExecutorConfig("stage-test")
	cfg.PaneLockWait = 50 * time.Millisecond
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(client)
	executor.SetDetector(detector)
	return executor, client, detector
}

func TestAdHocStageRefusesUnsafeDelivery(t *testing.T) {
	for _, mode := range []string{"first gate", "final gate", "capture", "respawn", "endpoint", "service", "agent type", "cancel", "paste", "verify"} {
		t.Run(mode, func(t *testing.T) {
			executor, client, _ := newStageDeliveryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			failure := errors.New("fixture refusal")
			switch mode {
			case "capture":
				client.captureErr = failure
			case "paste":
				client.pasteErr = failure
			case "verify":
				client.verifyErr = failure
			}
			client.prepare = func(_ context.Context, call int) error {
				if mode == "first gate" && call == 1 || mode == "final gate" && call == 2 {
					return failure
				}
				if call == 2 {
					switch mode {
					case "respawn":
						client.pane.PID++
					case "endpoint":
						client.endpoint = "ssh:replacement"
					case "service":
						client.pane.Service = "cm"
					case "agent type":
						client.pane.Type = tmux.AgentCodex
					case "cancel":
						cancel()
					}
				}
				return nil
			}
			_, err := executor.executeAdHocStage(ctx, Stage{AgentType: "cc", Prompt: "Do the task"}, client.pane.ID, "", AgentDeliveryState{})
			if err == nil {
				t.Fatal("unsafe stage succeeded")
			}
			wantPastes := 0
			if mode == "paste" || mode == "verify" {
				wantPastes = 1
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "inspect pane before retrying") {
					t.Fatalf("uncertain delivery cause or recovery guidance lost: %v", err)
				}
			}
			if client.pastes != wantPastes || client.legacyPaste != 0 {
				t.Fatalf("unexpected delivery or retry: pastes=%d legacy=%d want=%d", client.pastes, client.legacyPaste, wantPastes)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			// Every refusal must release the real per-user file lock.
			release, lockErr := executor.acquirePaneLockCrossProcess(context.Background(), client.pane.ID)
			if lockErr != nil {
				t.Fatalf("stage leaked its lock: %v", lockErr)
			}
			release()
		})
	}
}

func TestAdHocStageSharesWorkflowPaneLock(t *testing.T) {
	executor, client, _ := newStageDeliveryFixture(t)
	// A distinct executor owns the same kernel lock, as a file workflow does.
	owner := NewExecutor(DefaultExecutorConfig("other-project"))
	release, err := owner.acquirePaneLockCrossProcess(context.Background(), client.pane.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = executor.executeAdHocStage(context.Background(), Stage{AgentType: "cc", Prompt: "Must wait"}, client.pane.ID, "", AgentDeliveryState{})
	if !errors.Is(err, ErrPaneBusyOtherProcess) || client.prepares != 0 || client.pastes != 0 {
		t.Fatalf("stage overlapped an existing owner: err=%v prepares=%d pastes=%d", err, client.prepares, client.pastes)
	}
}

func TestAdHocStageResponseAndHandoff(t *testing.T) {
	for _, mode := range []string{"same process", "replaced process", "different endpoint"} {
		t.Run(mode, func(t *testing.T) {
			executor, client, detector := newStageDeliveryFixture(t)
			previous := AgentDeliveryState{PaneID: client.pane.ID, PanePID: client.pane.PID, Endpoint: client.endpoint}
			if mode == "replaced process" {
				previous.PanePID--
			}
			if mode == "different endpoint" {
				previous.Endpoint = "ssh:old"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := executor.executeAdHocStage(ctx, Stage{AgentType: "claude", Prompt: "Review this"}, client.pane.ID, "Literal ${env.NOT_EXPANDED}", previous)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "same process" {
				if !strings.Contains(client.prompt, "See previous output above") || strings.Contains(client.prompt, "NOT_EXPANDED") {
					t.Fatalf("same-process handoff duplicated output: %q", client.prompt)
				}
			} else if !strings.Contains(client.prompt, "Result from previous stage:\nLiteral ${env.NOT_EXPANDED}") {
				t.Fatalf("new lifetime lost literal previous output: %q", client.prompt)
			}
			if client.prepares != 2 || client.pastes != 1 || client.verifies != 1 || client.width != 93 || detector.calls < idleStablePolls {
				t.Fatalf("stage skipped its delivery or completion gates: %+v idle=%d", client, detector.calls)
			}
			if !strings.Contains(result.Output, "Stage answer.") || strings.Contains(result.Output, client.before) || result.PanePID != client.pane.PID {
				t.Fatalf("stage output was not isolated to the original delivery: %+v", result)
			}
		})
	}
}

func TestAdHocStageRequiresMoreThanIdlePromptEcho(t *testing.T) {
	executor, client, detector := newStageDeliveryFixture(t)
	client.echoOnly = true
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_, err := executor.executeAdHocStage(ctx, Stage{AgentType: "cc", Prompt: "Unanswered task"}, client.pane.ID, "", AgentDeliveryState{})
	if !errors.Is(err, context.DeadlineExceeded) || client.pastes != 1 || detector.calls < idleStablePolls {
		t.Fatalf("idle prompt echo was accepted or retried: err=%v sends=%d idle=%d", err, client.pastes, detector.calls)
	}
}

func TestAdHocStageRejectsReplacedResponse(t *testing.T) {
	executor, client, detector := newStageDeliveryFixture(t)
	detector.onIdle = func(call int) {
		if call == 1 {
			client.pane.PID++
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	_, err := executor.executeAdHocStage(ctx, Stage{AgentType: "cc", Prompt: "Original task"}, client.pane.ID, "", AgentDeliveryState{})
	if err == nil || !strings.Contains(err.Error(), "changed process lifetime") || client.pastes != 1 {
		t.Fatalf("adopted a replacement agent's reply: err=%v sends=%d", err, client.pastes)
	}
}

// pi uses an inline transcript and a bottom-pinned rule pair with a cwd and
// model/usage footer. Drafts between the rules are never response evidence.
func piStagePane(transcript, draft string) string {
	rule := strings.Repeat("─", 48)
	return transcript + "\n\n" + rule + "\n" + draft + "\n" + rule + "\n~/projects/demo (main)\n0.0%/1.0M (auto)  DeepSeek-V4.1-Flash"
}

func TestPiAdHocStagesProgressAcrossFullRepaints(t *testing.T) {
	executor, client, _ := newStageDeliveryFixture(t)
	client.pane.Type, client.pane.Command = tmux.AgentType("pi"), "pi"
	client.before = piStagePane("Pi v1.3.0\nAn unrelated earlier turn.", "")
	client.onPaste = func(call int, prompt string) {
		answer := "First response: review the inventory."
		if call == 2 {
			answer = "Second response: inventory review is complete."
		}
		client.after = piStagePane("Pi v1.3.0\n\n"+prompt+"\n\n"+answer, "draft text is not an answer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := executor.executeAdHocStage(ctx, Stage{AgentType: "pi", Prompt: "Inspect the inventory."}, client.pane.ID, "", AgentDeliveryState{})
	if err != nil {
		t.Fatalf("first repainted stage failed: %v", err)
	}
	second, err := executor.executeAdHocStage(ctx, Stage{AgentType: "pi", Prompt: "Verify the preceding inventory review."}, client.pane.ID, first.Output, first)
	if err != nil {
		t.Fatalf("second repainted stage failed: %v", err)
	}
	if client.pastes != 2 || client.verifies != 2 || !strings.Contains(client.prompt, "See previous output above") {
		t.Fatalf("pipeline did not progress exactly once per stage: %+v", client)
	}
	if !strings.Contains(first.Output, "First response:") || !strings.Contains(second.Output, "Second response:") {
		t.Fatalf("same-frame answers lost: first=%q second=%q", first.Output, second.Output)
	}
	for _, output := range []string{first.Output, second.Output} {
		if strings.Contains(output, "An unrelated earlier turn") || strings.Contains(output, "draft text") || strings.Contains(output, "DeepSeek") {
			t.Fatalf("history or composer reached downstream output: %q", output)
		}
	}
	if strings.Contains(second.Output, "First response:") {
		t.Fatalf("second stage misattributed prior response: %q", second.Output)
	}
}

func TestPiAdHocRepaintRequiresMoreThanIdleEcho(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete composer", true: "incomplete footer"}[incomplete], func(t *testing.T) {
			executor, client, detector := newStageDeliveryFixture(t)
			client.pane.Type, client.pane.Command = tmux.AgentType("pi"), "pi"
			client.before = piStagePane("Old conversation", "")
			client.onPaste = func(_ int, prompt string) {
				client.after = piStagePane(prompt, "A plausible answer still in the composer")
				if incomplete {
					client.after = strings.TrimSuffix(client.after, "0.0%/1.0M (auto)  DeepSeek-V4.1-Flash")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			_, err := executor.executeAdHocStage(ctx, Stage{AgentType: "pi", Prompt: "Review the intake."}, client.pane.ID, "", AgentDeliveryState{})
			if !errors.Is(err, context.DeadlineExceeded) || client.pastes != 1 || detector.calls < idleStablePolls {
				t.Fatalf("idle echo/draft fabricated progress: err=%v sends=%d idle=%d", err, client.pastes, detector.calls)
			}
		})
	}
}

func TestPiAdHocStageRefusesIncompleteInitialBoundary(t *testing.T) {
	executor, client, _ := newStageDeliveryFixture(t)
	client.pane.Type, client.pane.Command = tmux.AgentType("pi"), "pi"
	client.before = "old transcript\n" + strings.Repeat("─", 48) + "\nunsent draft"
	_, err := executor.executeAdHocStage(context.Background(), Stage{AgentType: "pi", Prompt: "Review the intake."}, client.pane.ID, "", AgentDeliveryState{})
	if !errors.Is(err, errIncompleteAgentCapture) || client.pastes != 0 {
		t.Fatalf("incomplete initial boundary authorized delivery: err=%v sends=%d", err, client.pastes)
	}
}

func TestAgentDeliveryPiRepaintKeepsEvidenceBoundaries(t *testing.T) {
	const prompt = "Review the intake."
	digest := sha256.Sum256([]byte(status.NormalizeAgentEcho(prompt)))
	base := AgentDeliveryState{AgentType: "pi", BeforeOutput: "Old conversation", PromptEchoHash: hex.EncodeToString(digest[:]), PromptEchoLength: len(prompt), PiPromptEchoHash: hex.EncodeToString(digest[:]), PiPromptEchoLength: len(prompt)}
	for _, tc := range []struct {
		name, before, output, current string
		ready, refuse, unchanged      bool
	}{
		{"blank repaint", base.BeforeOutput, "", "\n", false, false, true},
		{"partial old frame", base.BeforeOutput, "", "Old", false, false, true},
		{"startup only after empty boundary", "", "", "Pi v1.3.0", false, false, true},
		{"startup and echo after empty boundary", "", "", "Pi v1.3.0\n" + prompt, false, false, false},
		{"same-frame response", base.BeforeOutput, "", prompt + "\nCurrent response", true, false, false},
		{"prompt first then repaint", base.BeforeOutput + "\n" + prompt, "\n" + prompt, prompt + "\nCurrent response", true, false, false},
		{"echo only repaint", base.BeforeOutput, "", prompt, false, false, false},
		{"old prompt ambiguous", prompt + "\nOld conversation", "", prompt + "\nWrong turn", false, true, true},
		{"duplicate prompt", base.BeforeOutput, "", prompt + "\nA\n" + prompt + "\nB", false, true, true},
		{"unknown replacement", base.BeforeOutput, "", "Another task\nAnother response", false, true, true},
		{"blank after partial answer", prompt + "\nPartial answer", prompt + "\nPartial answer", "", false, false, true},
		{"replacement after partial answer", prompt + "\nPartial answer", prompt + "\nPartial answer", prompt + "\nUnrelated replacement", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			record.BeforeOutput, record.Output = tc.before, tc.output
			got, ready, err := observeAgentDelivery(record, tc.current)
			if (err != nil) != tc.refuse || ready != tc.ready {
				t.Fatalf("observation: record=%+v ready=%v err=%v", got, ready, err)
			}
			if tc.unchanged && (got.BeforeOutput != record.BeforeOutput || got.Output != record.Output) {
				t.Fatalf("unattributed frame changed evidence: before=%+v after=%+v", record, got)
			}
			if !tc.unchanged && strings.Count(got.Output, prompt) != 1 {
				t.Fatalf("repaint dropped or duplicated prompt attribution: %q", got.Output)
			}
		})
	}
	// A blank repaint followed by restoration of the original screen never
	// promotes its old text to a fresh response.
	record, _, err := observeAgentDelivery(base, "")
	if err != nil {
		t.Fatal(err)
	}
	restored, ready, err := observeAgentDelivery(record, base.BeforeOutput)
	if err != nil || ready || restored.Output != "" {
		t.Fatalf("restored old history became a response: %+v, %v, %v", restored, ready, err)
	}
	for _, agentType := range []string{"cc", "cod", "omp"} {
		record := base
		record.AgentType = agentType
		if _, _, err := observeAgentDelivery(record, prompt+"\nCurrent response"); err == nil {
			t.Fatalf("%s acquired pi's repaint exception", agentType)
		}
	}
	legacy := base
	legacy.PromptEchoLength = 0
	legacy.PiPromptEchoHash, legacy.PiPromptEchoLength = "", 0
	if _, _, err := observeAgentDelivery(legacy, prompt+"\nCurrent response"); err == nil {
		t.Fatal("legacy receipt without complete echo identity recovered a replacement")
	}
	legacy.BeforeOutput = ""
	observed, ready, err := observeAgentDelivery(legacy, "Pi v1.3.0\n"+prompt)
	if err == nil || ready || observed.BeforeOutput != "" || observed.Output != "" {
		t.Fatalf("legacy empty boundary trusted startup text: %+v, %v, %v", observed, ready, err)
	}
}

func TestPiDeliveryDistinguishesRenderedPromptFromResponse(t *testing.T) {
	const prompt, rendered = "Review **changes**", "Review changes"
	rawHash := sha256.Sum256([]byte(prompt))
	renderedHash := sha256.Sum256([]byte(rendered))
	base := AgentDeliveryState{
		AgentType: "pi", BeforeOutput: "Old conversation",
		PromptEchoHash: hex.EncodeToString(rawHash[:]), PromptEchoLength: len(prompt),
		PiPromptEchoHash: hex.EncodeToString(renderedHash[:]), PiPromptEchoLength: len(rendered),
	}
	for _, tc := range []struct {
		name, before, current string
		ready, refuse         bool
	}{
		{"rendered append is only an echo", base.BeforeOutput, base.BeforeOutput + "\n" + rendered, false, false},
		{"rendered repaint is only an echo", base.BeforeOutput, rendered, false, false},
		{"raw repaint is only an echo", base.BeforeOutput, prompt, false, false},
		{"rendered repaint with answer", base.BeforeOutput, rendered + "\nThe changes are correct.", true, false},
		{"mid-word wrapping is not an answer", base.BeforeOutput, base.BeforeOutput + "\nReview cha\nnges", false, false},
		{"transformed echo with unrelated text has no anchor", base.BeforeOutput, base.BeforeOutput + "\nReview cha\nnges\nThe changes are correct.", false, false},
		{"startup before an echo is not an answer", base.BeforeOutput, base.BeforeOutput + "\nStarting the review\n" + rendered, false, false},
		{"both renderings are ambiguous", base.BeforeOutput, prompt + "\nA\n" + rendered + "\nB", false, true},
		{"prior rendered echo is stale", rendered + "\nOld answer", prompt + "\nA different answer", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			record.BeforeOutput = tc.before
			observed, ready, err := observeAgentDelivery(record, tc.current)
			if ready != tc.ready || (err != nil) != tc.refuse {
				t.Fatalf("rendered echo attribution: output=%q ready=%v err=%v", observed.Output, ready, err)
			}
		})
	}
	legacy := base
	legacy.PiPromptEchoHash, legacy.PiPromptEchoLength = "", 0
	if _, ready, err := observeAgentDelivery(legacy, base.BeforeOutput+"\n"+rendered); err == nil || ready {
		t.Fatalf("legacy receipt classified a transformed echo as response: ready=%v err=%v", ready, err)
	}
}

func TestStageWaitCancellationWinsOverLateIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detector := &stageIdleDetector{onIdle: func(call int) {
		if call == idleStablePolls {
			cancel()
		}
	}}
	if err := waitForIdle(ctx, detector, "%71"); !errors.Is(err, context.Canceled) {
		t.Fatalf("late final idle observation hid cancellation: %v", err)
	}
}

func TestExecuteRejectsCancellationBeforeTMUX(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Execute(ctx, Pipeline{Session: "no-server-needed", Stages: []Stage{{AgentType: "cc", Prompt: "Must not send"}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled public execution reached tmux: %v", err)
	}
	if err := Execute(nil, Pipeline{}); err == nil {
		t.Fatal("nil context must not reach transport")
	}
}
