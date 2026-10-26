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
if [ "$1" = if-shell ] && [ "${NTM_TEST_PANE_CHANGED:-}" = 1 ]; then
  printf 'ntm_pane_identity_changed\n'
  exit 0
fi
case " $* " in
  *" display-message "*)
    case "$*" in
      *"@ntm_agent_type"*) printf '[cod]\n' ;;
      *"@ntm_rotation_token"*) printf '[%s]\n' "${NTM_TEST_ROTATION_TOKEN:-}" ;;
    esac
    ;;
  *" capture-pane "*) cat "$NTM_TEST_TRUST_SCREEN" ;;
  *" send-keys "*)
    case "$*" in
      *" Escape")
        if [ -n "${NTM_TEST_COMPOSER_AFTER_ESCAPE:-}" ]; then
          cat "$NTM_TEST_COMPOSER_AFTER_ESCAPE" > "$NTM_TEST_TRUST_SCREEN"
        fi
        ;;
      *" C-u")
        if [ -n "${NTM_TEST_COMPOSER_AFTER_CLEAR:-}" ]; then
          cat "$NTM_TEST_COMPOSER_AFTER_CLEAR" > "$NTM_TEST_TRUST_SCREEN"
        fi
        ;;
    esac
    ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", binary)
	t.Setenv("NTM_TEST_TRUST_LOG", log)
	t.Setenv("NTM_TEST_TRUST_SCREEN", screen)
	t.Setenv("NTM_TEST_COMPOSER_AFTER_ESCAPE", "")
	t.Setenv("NTM_TEST_COMPOSER_AFTER_CLEAR", "")
	t.Setenv("NTM_TEST_ROTATION_TOKEN", "")
	t.Setenv("NTM_TEST_PANE_CHANGED", "")
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
			if len(lines) != 2 || !strings.Contains(lines[0], "@ntm_rotation_token") || !strings.Contains(lines[1], "capture-pane") {
				t.Fatalf("only quarantine inspection and preflight capture are permitted, got commands:\n%s", commands)
			}
		})
	}
}

func TestTMUXDelivererRejectsRotationAfterPlanningWithoutActuation(t *testing.T) {
	_, log := installTrustDialogTMUX(t, "❯ ")
	svc, err := NewService(Ports{Redactor: AllowAllRedactor{}, Deliverer: TMUXDeliverer{}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.Prepare(t.Context(), Request{
		Session: "rotation-test", Message: "new assignment must wait", ClearInput: true,
		Panes: []tmux.Pane{{ID: "%12", Index: 1, Type: tmux.AgentClaude, Command: "claude"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The rotation begins after the caller captured and prepared its pane.
	t.Setenv("NTM_TEST_ROTATION_TOKEN", "continuation-1")
	result, err := svc.Dispatch(t.Context(), prepared)
	if err == nil || !strings.Contains(err.Error(), string(ErrRotationPending)) || !IsGuaranteedNoDeliveryActuation(err) {
		t.Fatalf("expected retry-safe rotation refusal, got result=%+v err=%v", result, err)
	}
	if result.Delivered != 0 || result.Success {
		t.Fatalf("quarantined pane reported delivery: %+v", result)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(commands)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "@ntm_rotation_token") {
		t.Fatalf("quarantine must refuse before capture, clear, paste, or Enter:\n%s", commands)
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

func TestTMUXDelivererOmpEmptyComposerHintDoesNotClearOrQuit(t *testing.T) {
	const capture = "╭── 󰵗   K3   project ──2%──────────────󰁨────────1M───╮\n" +
		"╰─                         󰘶 󰌒 to change thinking effort ─╯\n"
	_, log := installTrustDialogTMUX(t, capture)
	if state := tmux.InspectComposer(capture, tmux.AgentOmp); !state.MarkerVisible || state.HoldsText {
		t.Fatalf("empty composer with shortcut hint = %+v", state)
	}
	svc, err := NewService(Ports{Redactor: AllowAllRedactor{}, Deliverer: TMUXDeliverer{}})
	if err != nil {
		t.Fatal(err)
	}
	const message = "review the next task"
	result, err := svc.Execute(context.Background(), Request{
		Session: "omp-hint-test",
		Panes:   []tmux.Pane{{ID: "%12", Type: tmux.AgentOmp, Command: "omp", Width: 120}},
		Message: message, ClearInput: true, Submit: true,
	})
	if err != nil || !result.Success || result.Delivered != 1 || result.Failed != 0 {
		t.Fatalf("clear-and-send refused an empty omp composer: result=%+v error=%v", result, err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "C-c") {
		t.Fatalf("empty composer got a quit-capable Ctrl+C:\n%s", commands)
	}
	if strings.Count(string(commands), message) != 1 {
		t.Fatalf("prompt must reach the actual transport exactly once:\n%s", commands)
	}
}

func TestTMUXDelivererCodexComposerClearAndModalRefusal(t *testing.T) {
	const idle = "› Ask Codex to do anything\n47% context left\n"
	const modal = "› an earlier user prompt\nBrowsing transcript · ↑↓/jk scroll · ←→/hl prompts · ⌃t details · ↵ rewind · esc back\n"
	cases := []struct {
		name, capture, afterEscape, afterClear, wantError string
		clearInput                                        bool
		escapes, clears                                   int
	}{
		{name: "empty composer receives no clearing keys", capture: idle, clearInput: true},
		{name: "ultra empty composer receives no clearing keys", capture: "» Ask Codex to do anything\n47% context left\n", clearInput: true},
		{name: "initializing pane receives no blind clearing keys", capture: "initializing\n", clearInput: true, wantError: "not ready for delivery"},
		{name: "draft clears without entering history", capture: "› old draft\n47% context left\n", afterClear: idle, clearInput: true, clears: 1},
		{name: "one Escape restores the live composer", capture: modal, afterEscape: idle, clearInput: true, escapes: 1},
		{name: "history restores a draft before clearing", capture: modal, afterEscape: "› preserved draft\n47% context left\n", afterClear: idle, clearInput: true, escapes: 1, clears: 1},
		{name: "markerless history still recovers", capture: "Browsing · esc back\n", afterEscape: idle, clearInput: true, escapes: 1},
		{name: "persistent history refuses without more keys", capture: modal, clearInput: true, escapes: 1, wantError: "COMPOSER_NOT_CLEARED"},
		{name: "history exit requires a visible composer", capture: modal, afterEscape: "initializing\n", clearInput: true, escapes: 1, wantError: "COMPOSER_NOT_CLEARED"},
		{name: "ordinary send cannot rewind", capture: modal, wantError: "Enter would rewind"},
		{name: "history overrides old working chrome", capture: "• Working (5s • esc to interrupt)\n" + modal, wantError: "Enter would rewind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			screen, log := installTrustDialogTMUX(t, tc.capture)
			for key, capture := range map[string]string{
				"NTM_TEST_COMPOSER_AFTER_ESCAPE": tc.afterEscape,
				"NTM_TEST_COMPOSER_AFTER_CLEAR":  tc.afterClear,
			} {
				if capture == "" {
					continue
				}
				path := filepath.Join(filepath.Dir(screen), key)
				if err := os.WriteFile(path, []byte(capture), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(key, path)
			}
			svc, err := NewService(Ports{Redactor: AllowAllRedactor{}, Deliverer: TMUXDeliverer{}})
			if err != nil {
				t.Fatal(err)
			}
			const message = "review the next task"
			result, err := svc.Execute(context.Background(), Request{
				Session: "codex-clear-test",
				Panes:   []tmux.Pane{{ID: "%12", Type: tmux.AgentCodex, Command: "codex", Width: 120}},
				Message: message, ClearInput: tc.clearInput, Submit: true,
			})
			commands, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			calls := string(commands)
			if strings.Count(calls, " Escape\n") != tc.escapes || strings.Count(calls, " C-u\n") != tc.clears || strings.Count(calls, " C-e\n") != tc.clears {
				t.Fatalf("unexpected editing of composer/modal:\n%s", calls)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || !IsGuaranteedNoDeliveryActuation(err) || result.Success || result.Delivered != 0 || result.Failed != 1 {
					t.Fatalf("modal refusal: result=%+v error=%v", result, err)
				}
				if strings.Contains(calls, message) || strings.Contains(calls, " Enter\n") || strings.Contains(calls, " BSpace\n") {
					t.Fatalf("modal received prompt, Enter, or drain keys:\n%s", calls)
				}
				return
			}
			if err != nil || !result.Success || result.Delivered != 1 || result.Failed != 0 {
				t.Fatalf("clear-and-send: result=%+v error=%v", result, err)
			}
			if strings.Count(calls, message) != 1 {
				t.Fatalf("recovered prompt was not transported exactly once:\n%s", calls)
			}
		})
	}
}

func TestCodexSubmissionVerificationNeverRewindsTranscript(t *testing.T) {
	const message = "an earlier user prompt"
	const capture = "› " + message + "\nBrowsing transcript · ↑↓/jk scroll · ←→/hl prompts · ⌃t details · ↵ rewind · esc back\n"
	_, log := installTrustDialogTMUX(t, capture)
	confirmed, rescued, err := tmux.VerifyCodexSubmissionContext(context.Background(), "%12", message, 120)
	if confirmed || rescued || !errors.Is(err, tmux.ErrCodexTranscriptBrowsing) {
		t.Fatalf("history was confirmed or rescued: confirmed=%v rescued=%v error=%v", confirmed, rescued, err)
	}
	// The service must not turn this positive modal diagnosis into the
	// fail-open result reserved for an inconclusive capture failure.
	if err := VerifyAgentSubmission(context.Background(), "%12", message, tmux.AgentCodex, 120); !errors.Is(err, tmux.ErrCodexTranscriptBrowsing) {
		t.Fatalf("submission wrapper hid the rewind hazard: %v", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "send-keys") {
		t.Fatalf("submission verification sent a key into history:\n%s", commands)
	}
}

func TestAgentSubmissionVerificationPreservesRestartIdentityRefusal(t *testing.T) {
	const message = "review the next task"
	for _, tc := range []struct {
		kind    tmux.AgentType
		capture string
	}{
		{tmux.AgentCodex, "› " + message + "\n47% context left\n"},
		{tmux.AgentClaude, "❯ " + message + "\n"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			_, log := installTrustDialogTMUX(t, tc.capture)
			t.Setenv("NTM_TEST_PANE_CHANGED", "1")
			ctx, err := tmux.WithPaneMutationGuard(t.Context(), "%12", 123)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAgentSubmission(ctx, "%12", message, tc.kind, 120); !errors.Is(err, tmux.ErrPaneIdentityChanged) {
				t.Fatalf("submission verifier hid restart ownership loss: %v", err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "if-shell -F") {
				t.Fatal("regression did not reach guarded rescue-key delivery")
			}
			for _, call := range strings.Split(string(calls), "\n") {
				if strings.HasPrefix(call, "send-keys ") {
					t.Fatal("submission verifier sent an unguarded rescue key")
				}
			}
		})
	}
}
