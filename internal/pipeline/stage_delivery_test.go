package pipeline

import (
	"context"
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
