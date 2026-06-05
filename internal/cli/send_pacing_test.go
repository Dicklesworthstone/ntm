package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/swarmslo"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestBuildDispatchPacingDecisionRequiresOptIn(t *testing.T) {
	t.Parallel()

	got := buildDispatchPacingDecision(SendOptions{}, "proj", []tmux.Pane{
		{Index: 1, Type: tmux.AgentClaude},
	})

	if got != nil {
		t.Fatalf("buildDispatchPacingDecision returned %+v, want nil without flag or injection", got)
	}
}

func TestBuildDispatchPacingDecisionUsesInjectedSignals(t *testing.T) {
	t.Parallel()

	got := buildDispatchPacingDecision(SendOptions{
		DispatchPacingInput: &coordinator.DispatchPacingInput{
			SLO: swarmslo.Summary{
				TimeToFirstAck: swarmslo.Distribution{Pending: 2},
			},
		},
	}, "proj", []tmux.Pane{
		{Index: 1, Type: tmux.AgentClaude},
		{Index: 2, Type: tmux.AgentCodex},
	})

	if got == nil {
		t.Fatal("buildDispatchPacingDecision returned nil, want decision")
	}
	if strings.Compare(got.Session, "proj") != 0 {
		t.Fatalf("Session = %q, want proj", got.Session)
	}
	if got.RequestedTargets < 2 || got.RequestedTargets > 2 {
		t.Fatalf("RequestedTargets = %d, want 2", got.RequestedTargets)
	}
	if strings.Compare(string(got.Decision), string(coordinator.DispatchPacingStagger)) != 0 {
		t.Fatalf("Decision = %s, want stagger; reasons=%v", got.Decision, got.ReasonCodes)
	}
	if !sendPacingHasReason(got.ReasonCodes, coordinator.DispatchPacingReasonAckPending) ||
		!sendPacingHasReason(got.ReasonCodes, coordinator.DispatchPacingReasonMixedAgents) {
		t.Fatalf("ReasonCodes = %v, want pending ack and mixed agents", got.ReasonCodes)
	}
}

func TestSendPacingJSONOmittedByDefault(t *testing.T) {
	t.Parallel()

	payloads := []any{
		SendResult{Success: true, Session: "proj", Targets: []int{1}, Delivered: 1},
		SendDryRunResult{Success: true, DryRun: true, Session: "proj", WouldSend: []SendDryRunEntry{}},
	}
	for _, payload := range payloads {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", payload, err)
		}
		if strings.Contains(string(data), "dispatch_pacing") {
			t.Fatalf("JSON = %s, want dispatch_pacing omitted", data)
		}
	}
}

func TestSendPacingJSONEmitsDecisionWhenSet(t *testing.T) {
	t.Parallel()

	decision := coordinator.EvaluateDispatchPacing(coordinator.DispatchPacingInput{
		Session:          "proj",
		RequestedTargets: 2,
	})
	payloads := []any{
		SendResult{Success: true, Session: "proj", Targets: []int{1, 2}, Delivered: 2, DispatchPacing: &decision},
		SendDryRunResult{Success: true, DryRun: true, Session: "proj", WouldSend: []SendDryRunEntry{}, DispatchPacing: &decision},
	}
	for _, payload := range payloads {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", payload, err)
		}
		if !strings.Contains(string(data), "dispatch_pacing") ||
			!strings.Contains(string(data), coordinator.DispatchPacingSchemaVersion) {
			t.Fatalf("JSON = %s, want dispatch pacing schema", data)
		}
	}
}

func sendPacingHasReason(reasons []coordinator.DispatchPacingReasonCode, want coordinator.DispatchPacingReasonCode) bool {
	for _, reason := range reasons {
		if strings.Compare(string(reason), string(want)) == 0 {
			return true
		}
	}
	return false
}
