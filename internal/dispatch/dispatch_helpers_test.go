package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// PlanDelivery adapts the function to ProtocolPlanner (test-only adapter; the
// production adapter method was removed as dead code).
func (f ProtocolPlannerFunc) PlanDelivery(ctx context.Context, target Target, submit bool) (ProtocolPlan, error) {
	return f(ctx, target, submit)
}

// Wait adapts the function to Pacer (test-only adapter).
func (f PacerFunc) Wait(ctx context.Context, pace Pace) error {
	return f(ctx, pace)
}

// The fake executable is the external tmux boundary, not a replacement
// deliverer. These tests exercise the application's actual Prepare/Dispatch
// service and TMUXDeliverer, including the retry-safe error wrapper.
func installTrustDialogTMUX(t *testing.T, capture string) (screen, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux executable requires a POSIX shell")
	}
	dir := t.TempDir()
	screen, log = filepath.Join(dir, "screen"), filepath.Join(dir, "commands")
	if err := os.WriteFile(screen, []byte(capture), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "tmux")
	if err := os.WriteFile(binary, []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$NTM_TEST_TRUST_LOG"
case " $* " in
  *" capture-pane "*) cat "$NTM_TEST_TRUST_SCREEN" ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binary)
	t.Setenv("NTM_TEST_TRUST_LOG", log)
	t.Setenv("NTM_TEST_TRUST_SCREEN", screen)
	original := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = original })
	return screen, log
}

func TestTMUXDelivererTrustMenuRefusalIsPreActuationAndRetrySafe(t *testing.T) {
	for _, tc := range []struct {
		name, capture string
		kind          tmux.AgentType
	}{
		{"claude cursor", "Quick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm · Esc to cancel", tmux.AgentClaude},
		{"claude numbered", "Quick safety check: Is this a project you created or one you trust?\n❯ 1. Yes, I trust this folder\n  2. No, exit\nEnter to confirm · Esc to cancel", tmux.AgentClaude},
		{"antigravity", "Do you trust the contents of this project?\n> Yes, I trust this folder\n  No, exit\n↑/↓ Navigate · enter Confirm", tmux.AgentAntigravity},
		{"grok hotkeys", "Grok Build may run or modify contents in this directory,\nposing security risks.\nYes, proceed  y\nNo, quit  n", tmux.AgentGrok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, log := installTrustDialogTMUX(t, tc.capture)
			svc, err := NewService(Ports{Redactor: AllowAllRedactor{}, Deliverer: TMUXDeliverer{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.Execute(context.Background(), Request{
				Session: "trust-test",
				Panes: []tmux.Pane{{
					ID: "%12", WindowIndex: 0, Index: 1, Type: tc.kind,
					Command: string(tc.kind), Title: "trust-test__agent_1",
				}},
				Message: "never send this prompt into a trust menu", ClearInput: true,
			})
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != ErrDelivery || !IsGuaranteedNoDeliveryActuation(err) {
				t.Fatalf("error = %v; want retry-safe pre-actuation dispatch failure", err)
			}
			if !strings.Contains(err.Error(), "PANE_INTERACTIVE_GATE") || !strings.Contains(err.Error(), "--robot-answer-dialog") {
				t.Fatalf("missing actionable gate diagnosis: %v", err)
			}
			if result.Success || result.Delivered != 0 || result.Failed != 1 || len(result.Receipts) != 1 || result.Receipts[0].Status != ReceiptFailed {
				t.Fatalf("refused gate reported a delivery: %+v", result)
			}
			commands, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "capture-pane") {
				t.Fatalf("only the preflight capture is permitted, got commands:\n%s", commands)
			}
		})
	}
}

func TestTMUXDelivererTrustGateRecoveryAndShellExemption(t *testing.T) {
	gate := "Do you trust this folder?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm"
	screen, log := installTrustDialogTMUX(t, gate)
	delivery := Delivery{
		Session: "trust-test", Message: "run the review", Protocol: ProtocolStageOnly,
		Target: Target{Ref: tmux.PaneRef{ID: "%12"}, AgentType: tmux.AgentClaude,
			Pane: tmux.Pane{ID: "%12", Type: tmux.AgentClaude, Command: "claude"}},
	}
	if err := (TMUXDeliverer{}).Deliver(context.Background(), delivery); !IsGuaranteedNoDeliveryActuation(err) {
		t.Fatalf("initial gate refusal = %v", err)
	}
	// After an explicit dialog answer the ordinary send can be retried. The
	// old gate remaining in visible history must not latch this refusal.
	if err := os.WriteFile(screen, []byte(gate+"\nThe workspace is ready.\n❯ "), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (TMUXDeliverer{}).Deliver(context.Background(), delivery); err != nil {
		t.Fatalf("retry after trust dialog cleared: %v", err)
	}
	// A deliberately targeted user shell is not an agent startup gate, even
	// when the operator is printing a trust-menu fixture.
	if err := os.WriteFile(screen, []byte(gate), 0600); err != nil {
		t.Fatal(err)
	}
	delivery.Target.AgentType = tmux.AgentUser
	delivery.Target.Pane.Type = tmux.AgentUser
	delivery.Target.Pane.Command = "bash"
	if err := (TMUXDeliverer{}).Deliver(context.Background(), delivery); err != nil {
		t.Fatalf("explicit user shell send blocked by gate text: %v", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(commands), delivery.Message); got != 2 {
		t.Fatalf("wanted exactly the recovered agent and explicit shell sends, got %d:\n%s", got, commands)
	}
}

func TestTMUXDelivererCancelledTrustPreflightDoesNotActuate(t *testing.T) {
	_, log := installTrustDialogTMUX(t, "Do you trust this folder?\n❯ Yes\n No\nEnter to confirm")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (TMUXDeliverer{}).Deliver(ctx, Delivery{
		Target:   Target{AgentType: tmux.AgentClaude, Ref: tmux.PaneRef{ID: "%12"}},
		Protocol: ProtocolStageOnly, Message: "never sent",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delivery error = %v", err)
	}
	if data, err := os.ReadFile(log); !os.IsNotExist(err) {
		t.Fatal(fmt.Sprintf("cancelled delivery performed I/O: %s, error=%v", data, err))
	}
}
