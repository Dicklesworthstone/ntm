package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func deliveryTestRequests() []contextInjectionRequest {
	return uniformContextRequests([]tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude},
		{ID: "%2", Index: 2, Type: tmux.AgentCodex},
		{ID: "%3", Index: 3, Type: tmux.AgentGrok},
	}, "private context\nsecond line\n")
}

func TestContextDeliveryPartialFailurePreservesReceipts(t *testing.T) {
	t.Parallel()
	failure := errors.New("transport failed after paste")
	var called []string
	receipts, err := dispatchContextRequests(context.Background(), "demo", deliveryTestRequests(), false,
		func(_ context.Context, pane tmux.Pane, text string) error {
			called = append(called, pane.ID)
			if text != "private context\nsecond line\n" {
				t.Fatalf("multiline payload changed: %q", text)
			}
			if pane.ID == "%2" {
				return failure
			}
			return nil
		})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "delivered panes [1 3]") {
		t.Fatalf("partial failure lost outcome/cause: %v", err)
	}
	if !reflect.DeepEqual(called, []string{"%1", "%2", "%3"}) {
		t.Fatalf("independent targets were skipped or retried: %v", called)
	}
	if receipts[0].Status != "delivered" || receipts[1].Status != "uncertain" || receipts[2].Status != "delivered" {
		t.Fatalf("incorrect receipts: %+v", receipts)
	}
	result := contextInjectionResult("demo", []string{"AGENTS.md"}, 28, false, false, receipts, err)
	if result.Success || !reflect.DeepEqual(result.PanesInjected, []int{1, 3}) || result.Error == "" {
		t.Fatalf("false success or lost partial result: %+v", result)
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil || strings.Contains(string(raw), "private context") {
		t.Fatalf("receipts must serialize without prompt contents: %s, %v", raw, marshalErr)
	}
}

func TestContextDeliveryCancellationStopsUnattemptedTargets(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"before", "during_error", "after_success"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "before" {
				cancel()
			}
			calls := 0
			receipts, err := dispatchContextRequests(ctx, "demo", deliveryTestRequests(), false,
				func(ctx context.Context, _ tmux.Pane, _ string) error {
					calls++
					cancel()
					if scenario == "during_error" {
						return ctx.Err()
					}
					return nil
				})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			wantCalls, firstStatus := 1, "delivered"
			if scenario == "before" {
				wantCalls, firstStatus = 0, "not_attempted"
			} else if scenario == "during_error" {
				firstStatus = "uncertain"
			}
			if calls != wantCalls || receipts[0].Status != firstStatus || receipts[1].Status != "not_attempted" || receipts[2].Status != "not_attempted" {
				t.Fatalf("cancellation dispatched later work: calls=%d receipts=%+v", calls, receipts)
			}
		})
	}
}

func TestContextDeliveryDryRunDoesNotClaimDelivery(t *testing.T) {
	t.Parallel()
	receipts, err := dispatchContextRequests(context.Background(), "demo", deliveryTestRequests(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := contextInjectionResult("demo", nil, 28, false, true, receipts, nil)
	if !result.Success || !result.DryRun || len(result.PanesInjected) != 0 || !reflect.DeepEqual(result.PanesPlanned, []int{1, 2, 3}) {
		t.Fatalf("dry-run claimed actual delivery: %+v", result)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"panes_injected":[]`) || !strings.Contains(string(raw), `"injected_files":[]`) {
		t.Fatalf("empty results must be arrays: %s", raw)
	}
}

func TestContextDeliveryPreflightsWholeBatch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"empty_batch", "duplicate", "empty_target", "empty_content", "nil_sender"} {
		t.Run(scenario, func(t *testing.T) {
			requests := deliveryTestRequests()
			calls := 0
			var sender contextPaneSender = func(context.Context, tmux.Pane, string) error { calls++; return nil }
			switch scenario {
			case "empty_batch":
				requests = nil
			case "duplicate":
				requests[2].Pane.ID = requests[0].Pane.ID
			case "empty_target":
				requests[2].Pane.ID = " "
			case "empty_content":
				requests[2].Content = " \n\t"
			case "nil_sender":
				sender = nil
			}
			receipts, err := dispatchContextRequests(context.Background(), "demo", requests, false, sender)
			if err == nil || calls != 0 {
				t.Fatalf("invalid batch was sent: calls=%d err=%v", calls, err)
			}
			for _, receipt := range receipts {
				if receipt.Status != "not_attempted" {
					t.Fatalf("preflight claimed an attempted send: %+v", receipt)
				}
			}
		})
	}
}

func TestContextDeliveryAllFailuresAndSuccess(t *testing.T) {
	t.Parallel()
	failure := errors.New("send failed")
	for _, fail := range []bool{true, false} {
		receipts, err := dispatchContextRequests(context.Background(), "demo", deliveryTestRequests(), false,
			func(context.Context, tmux.Pane, string) error {
				if fail {
					return failure
				}
				return nil
			})
		result := contextInjectionResult("demo", nil, 28, false, false, receipts, err)
		if fail {
			if result.Success || len(result.PanesInjected) != 0 || !errors.Is(err, failure) {
				t.Fatalf("all-failed batch: %+v, %v", result, err)
			}
		} else if !result.Success || len(result.PanesInjected) != 3 || err != nil {
			t.Fatalf("successful batch: %+v, %v", result, err)
		}
	}
}

func TestContextUniformRobotInjectionReturnsPartialFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("pane unavailable")
	panes := []tmux.Pane{{ID: "%1", Index: 1, Type: tmux.AgentClaude}, {ID: "%2", Index: 2, Type: tmux.AgentCodex}}
	injected, err := injectContextIntoPanes("demo", panes, "context", false, func(target, _ string, enter bool) error {
		if !enter {
			t.Fatal("context was not submitted")
		}
		if target == "%2" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !reflect.DeepEqual(injected, []int{1}) {
		t.Fatalf("robot injection swallowed partial failure: %v, %v", injected, err)
	}
}
