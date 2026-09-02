package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func testPane(id string, window, index int, agentType tmux.AgentType, variant string, tags ...string) tmux.Pane {
	return tmux.Pane{
		ID:          id,
		WindowIndex: window,
		Index:       index,
		NTMIndex:    index + 1,
		Title:       fmt.Sprintf("test__%s_%d", agentType, index+1),
		Type:        agentType,
		Variant:     variant,
		Tags:        append([]string(nil), tags...),
	}
}

func topologyFixture() []tmux.Pane {
	return []tmux.Pane{
		testPane("%4", 2, 0, tmux.AgentCodex, "o3", "backend"),
		testPane("%2", 1, 1, tmux.AgentClaude, "opus", "frontend", "api"),
		testPane("%1", 0, 0, tmux.AgentUser, "", "operator"),
		testPane("%3", 1, 0, tmux.AgentUnknown, "", "shell"),
	}
}

func requireCode(t *testing.T, err error, want ErrorCode) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", want)
	}
	var dispatchErr *Error
	if !errors.As(err, &dispatchErr) {
		t.Fatalf("error %T (%v) is not *dispatch.Error", err, err)
	}
	if dispatchErr.Code != want {
		t.Fatalf("error code = %q, want %q (%v)", dispatchErr.Code, want, err)
	}
	return dispatchErr
}

func targetAddresses(targets []Target) []string {
	result := make([]string, len(targets))
	for i := range targets {
		result[i] = targets[i].Address
	}
	return result
}

func TestPlanTargetsDeterministicTopologyAndDefaultUserExclusion(t *testing.T) {
	t.Parallel()
	targets, err := PlanTargets(Request{Panes: topologyFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targetAddresses(targets), []string{"1.0", "1.1", "2.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	if targets[0].AgentType != tmux.AgentUnknown || targets[1].AgentType != tmux.AgentClaude || targets[2].AgentType != tmux.AgentCodex {
		t.Fatalf("agent types not topology-aligned: %+v", targets)
	}
}

func TestPlanTargetsIncludeUserAndExplicitUserSelection(t *testing.T) {
	t.Parallel()
	t.Run("broad include", func(t *testing.T) {
		targets, err := PlanTargets(Request{Panes: topologyFixture(), IncludeUser: true})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := targetAddresses(targets), []string{"0.0", "1.0", "1.1", "2.0"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("addresses = %v, want %v", got, want)
		}
	})
	t.Run("explicit user", func(t *testing.T) {
		targets, err := PlanTargets(Request{Panes: topologyFixture(), Selectors: []string{"%1"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"0.0"}) {
			t.Fatalf("addresses = %v, want [0.0]", got)
		}
	})
}

func TestPlanTargetsSelectorAliasesDeduplicateByPaneRef(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentUser, ""),
		testPane("%2", 0, 1, tmux.AgentClaude, "opus"),
	}
	targets, err := PlanTargets(Request{Panes: panes, Selectors: []string{"1", "%2", "0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Ref.ID != "%2" || targets[0].Address != "1" {
		t.Fatalf("alias resolution = %+v, want one pane %%2 at address 1", targets)
	}
}

func TestPlanTargetsSelectorsFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		selectors []string
		excludes  []string
	}{
		{name: "malformed selector", selectors: []string{"1.nope"}},
		{name: "missing selector", selectors: []string{"%999"}},
		{name: "mixed valid invalid", selectors: []string{"%2", "bad"}},
		{name: "malformed exclusion", excludes: []string{"-1"}},
		{name: "missing exclusion", excludes: []string{"9.9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanTargets(Request{Panes: topologyFixture(), Selectors: tc.selectors, ExcludeSelectors: tc.excludes})
			requireCode(t, err, ErrInvalidSelector)
		})
	}
}

func TestPlanTargetsRequireSingleSelectorRejectsAmbiguousOrMalformedRequests(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentUser, ""),
		testPane("%2", 1, 0, tmux.AgentClaude, ""),
		testPane("%3", 1, 1, tmux.AgentCodex, ""),
	}
	targets, err := PlanTargets(Request{Panes: panes, Selectors: []string{"%3"}, RequireSingleSelector: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Ref.ID != "%3" || targets[0].Address != "1.1" {
		t.Fatalf("targets = %+v", targets)
	}
	_, err = PlanTargets(Request{Panes: panes, Selectors: []string{"1"}, RequireSingleSelector: true})
	var selectorErr *tmux.PaneSelectorError
	if !errors.As(err, &selectorErr) || selectorErr.Kind != tmux.PaneSelectorAmbiguous {
		t.Fatalf("ambiguous error = %T %v", err, err)
	}
	_, err = PlanTargets(Request{Panes: panes, RequireSingleSelector: true})
	requireCode(t, err, ErrInvalidRequest)
	_, err = PlanTargets(Request{Panes: panes, Selectors: []string{"%2", "%3"}, RequireSingleSelector: true})
	requireCode(t, err, ErrInvalidRequest)
}

func TestPlanTargetsExclusionsAreTopologyAwareAndAliasDeduplicated(t *testing.T) {
	t.Parallel()
	targets, err := PlanTargets(Request{
		Panes:            topologyFixture(),
		ExcludeSelectors: []string{"%2", "1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targetAddresses(targets), []string{"1.0", "2.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
}

func TestPlanTargetsAgentVariantAndTagFilters(t *testing.T) {
	t.Parallel()
	t.Run("agent alias and variant", func(t *testing.T) {
		targets, err := PlanTargets(Request{
			Panes: topologyFixture(),
			AgentFilters: []AgentFilter{
				{Type: "claude", Variant: "OPUS"},
				{Type: "cc", Variant: "opus"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"1.1"}) {
			t.Fatalf("addresses = %v, want [1.1]", got)
		}
	})
	t.Run("tag matching can explicitly select user", func(t *testing.T) {
		targets, err := PlanTargets(Request{Panes: topologyFixture(), Tags: []string{" OPERATOR ", "operator"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"0.0"}) {
			t.Fatalf("addresses = %v, want [0.0]", got)
		}
	})
	t.Run("selectors filters and tags intersect", func(t *testing.T) {
		targets, err := PlanTargets(Request{
			Panes:        topologyFixture(),
			Selectors:    []string{"1"},
			AgentFilters: []AgentFilter{{Type: tmux.AgentClaude}},
			Tags:         []string{"api"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"1.1"}) {
			t.Fatalf("addresses = %v, want [1.1]", got)
		}
	})
	t.Run("unknown is an explicit observable classification", func(t *testing.T) {
		targets, err := PlanTargets(Request{Panes: topologyFixture(), AgentFilters: []AgentFilter{{Type: tmux.AgentUnknown}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"1.0"}) {
			t.Fatalf("addresses = %v, want [1.0]", got)
		}
	})
}

func TestPlanTargetsRejectsInvalidFilters(t *testing.T) {
	t.Parallel()
	_, err := PlanTargets(Request{Panes: topologyFixture(), AgentFilters: []AgentFilter{{Type: "made-up"}}})
	requireCode(t, err, ErrInvalidRequest)
	_, err = PlanTargets(Request{Panes: topologyFixture(), Tags: []string{"  "}})
	requireCode(t, err, ErrInvalidRequest)
}

func TestPlanTargetsSkipFirstAndExplicitSelectorConflict(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentUser, ""),
		testPane("%3", 0, 2, tmux.AgentCodex, ""),
	}
	targets, err := PlanTargets(Request{Panes: panes, SkipFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := targetAddresses(targets); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("addresses = %v, want [2]", got)
	}
	_, err = PlanTargets(Request{Panes: panes, SkipFirst: true, Selectors: []string{"2"}})
	requireCode(t, err, ErrInvalidRequest)
}

func TestPlanTargetsDeduplicatesIdenticalSnapshotsAndRejectsConflicts(t *testing.T) {
	t.Parallel()
	pane := testPane("%1", 0, 0, tmux.AgentClaude, "")
	targets, err := PlanTargets(Request{Panes: []tmux.Pane{pane, pane}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	conflict := pane
	conflict.WindowIndex = 1
	_, err = PlanTargets(Request{Panes: []tmux.Pane{pane, conflict}})
	requireCode(t, err, ErrInvalidRequest)
}

func TestPlanTargetsRejectsEmptyOrFullyFilteredPlans(t *testing.T) {
	t.Parallel()
	_, err := PlanTargets(Request{})
	requireCode(t, err, ErrNoTargets)
	_, err = PlanTargets(Request{Panes: topologyFixture(), AgentFilters: []AgentFilter{{Type: tmux.AgentAider}}})
	requireCode(t, err, ErrNoTargets)
}

func TestNewServiceRequiresExplicitSafetyAndActuationPorts(t *testing.T) {
	t.Parallel()
	_, err := NewService(Ports{Deliverer: DelivererFunc(func(context.Context, Delivery) error { return nil })})
	requireCode(t, err, ErrInvalidRequest)
	_, err = NewService(Ports{Redactor: AllowAllRedactor{}})
	requireCode(t, err, ErrInvalidRequest)
	service, err := NewService(Ports{
		Redactor:  AllowAllRedactor{},
		Deliverer: DelivererFunc(func(context.Context, Delivery) error { return nil }),
	})
	if err != nil || service == nil {
		t.Fatalf("defaulted service = %v, %v", service, err)
	}
}

func TestPrepareBuildsThenRedactsEveryFinalMessageBeforeDelivery(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%3", 0, 2, tmux.AgentCodex, ""),
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentGemini, ""),
	}
	var built, redacted int
	var delivered []Delivery
	var paces []Pace
	service, err := NewService(Ports{
		Builder: FinalMessageBuilderFunc(func(_ context.Context, in BuildInput) (string, error) {
			built++
			return in.BaseMessage + " secret-for-" + in.Target.Address, nil
		}),
		Redactor: FinalMessageRedactorFunc(func(_ context.Context, target Target, message string) (RedactionResult, error) {
			redacted++
			wantSuffix := "secret-for-" + target.Address
			if !strings.Contains(message, wantSuffix) {
				t.Fatalf("redactor saw %q, missing builder output %q", message, wantSuffix)
			}
			return RedactionResult{
				Message:    strings.ReplaceAll(message, "secret", "[REDACTED]"),
				Mode:       "redact",
				Findings:   1,
				Categories: map[string]int{"TEST": 1},
				Warnings:   []string{"redacted test content"},
			}, nil
		}),
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			if built != len(panes) || redacted != len(panes) {
				t.Fatalf("delivery began before complete preflight: built=%d redacted=%d", built, redacted)
			}
			if strings.Contains(delivery.Message, "secret") {
				t.Fatalf("delivery retained unredacted message: %q", delivery.Message)
			}
			delivered = append(delivered, delivery)
			return nil
		}),
		Pacer: PacerFunc(func(_ context.Context, pace Pace) error {
			paces = append(paces, pace)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj",
		Panes:   panes,
		Message: "work",
		Submit:  true,
		Delay:   25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Delivered != 3 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(delivered) != 3 || len(paces) != 2 {
		t.Fatalf("deliveries=%d paces=%d, want 3 and 2", len(delivered), len(paces))
	}
	if got := []string{delivered[0].Target.Address, delivered[1].Target.Address, delivered[2].Target.Address}; !reflect.DeepEqual(got, []string{"0", "1", "2"}) {
		t.Fatalf("delivery order = %v", got)
	}
	for i, receipt := range result.Receipts {
		if receipt.Status != ReceiptDelivered || receipt.Redaction.Findings != 1 || receipt.Protocol != ProtocolDoubleEnter {
			t.Fatalf("receipt %d = %+v", i, receipt)
		}
	}
	if !reflect.DeepEqual(result.Warnings, []string{"redacted test content"}) {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-for") || strings.Contains(string(encoded), `"Message"`) {
		t.Fatalf("safe result leaked final message: %s", encoded)
	}
}

// TestPrepareAcceptsGrokInMixedBatch replaces the phase-one refusal test:
// after GH#251 phase 2, a mixed batch containing a Grok Build pane flows
// through the full prompt pipeline and delivers to every pane, grok included.
func TestPrepareAcceptsGrokInMixedBatch(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentType(" XAI_GROK_BUILD "), ""),
		testPane("%3", 0, 2, tmux.AgentCodex, ""),
	}

	var built, redacted, planned, delivered int
	var deliveredTypes []tmux.AgentType
	service, err := NewService(Ports{
		Builder: FinalMessageBuilderFunc(func(_ context.Context, in BuildInput) (string, error) {
			built++
			return in.BaseMessage, nil
		}),
		Redactor: FinalMessageRedactorFunc(func(_ context.Context, _ Target, message string) (RedactionResult, error) {
			redacted++
			return RedactionResult{Message: message}, nil
		}),
		Protocols: ProtocolPlannerFunc(func(_ context.Context, _ Target, _ bool) (ProtocolPlan, error) {
			planned++
			return ProtocolPlan{Protocol: ProtocolStageOnly}, nil
		}),
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			delivered++
			deliveredTypes = append(deliveredTypes, delivery.Target.AgentType.Canonical())
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Execute(context.Background(), Request{
		Session: "proj",
		Panes:   panes,
		Message: "work",
		Submit:  false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil (grok delivery supported in phase 2)", err)
	}
	if delivered != 3 || built != 3 || redacted != 3 || planned != 3 {
		t.Fatalf("prompt pipeline calls = build:%d redact:%d protocol:%d deliver:%d, want 3 each", built, redacted, planned, delivered)
	}
	grokDelivered := false
	for _, at := range deliveredTypes {
		if at == tmux.AgentGrok {
			grokDelivered = true
		}
	}
	if !grokDelivered {
		t.Fatalf("delivered agent types = %v, want to include grok", deliveredTypes)
	}
	if result.Delivered != 3 {
		t.Fatalf("result.Delivered = %d, want 3", result.Delivered)
	}
}

func TestDefaultProtocolPlannerCharacterization(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentUser, ""),
		testPane("%3", 0, 2, tmux.AgentUnknown, ""),
		testPane("%4", 0, 3, tmux.AgentType("future-agent"), ""),
	}
	var deliveries []Delivery
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			deliveries = append(deliveries, delivery)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(context.Background(), Request{
		Session:     "proj",
		Panes:       panes,
		Message:     "work",
		Submit:      true,
		IncludeUser: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ProtocolPlan{
		{Protocol: ProtocolDoubleEnter, EnterDelay: tmux.DoubleEnterFirstDelay, SecondEnterDelay: tmux.DoubleEnterSecondDelay},
		{Protocol: ProtocolSingleEnter, EnterDelay: tmux.ShellEnterDelay},
		{Protocol: ProtocolSingleEnter, EnterDelay: tmux.ShellEnterDelay},
		{Protocol: ProtocolSingleEnter, EnterDelay: tmux.ShellEnterDelay},
	}
	for i, delivery := range deliveries {
		got := ProtocolPlan{Protocol: delivery.Protocol, EnterDelay: delivery.EnterDelay, SecondEnterDelay: delivery.SecondEnterDelay}
		if got != want[i] {
			t.Fatalf("delivery %d plan = %+v, want %+v", i, got, want[i])
		}
	}

	deliveries = nil
	_, err = service.Execute(context.Background(), Request{
		Session:     "proj",
		Panes:       panes,
		Message:     "stage",
		Submit:      false,
		IncludeUser: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, delivery := range deliveries {
		if delivery.Protocol != ProtocolStageOnly || delivery.EnterDelay != 0 || delivery.SecondEnterDelay != 0 {
			t.Fatalf("staged delivery %d = %+v", i, delivery)
		}
	}
}

func TestPrepareRedactionBlockIsAtomicAndHasPerPaneReceipts(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
		testPane("%3", 0, 2, tmux.AgentGemini, ""),
	}
	var deliveries int
	service, err := NewService(Ports{
		Redactor: FinalMessageRedactorFunc(func(_ context.Context, target Target, message string) (RedactionResult, error) {
			if target.Address == "1" {
				return RedactionResult{Mode: "block", Findings: 1, Categories: map[string]int{"KEY": 1}, Blocked: true}, nil
			}
			return RedactionResult{Message: message, Mode: "block"}, nil
		}),
		Deliverer: DelivererFunc(func(context.Context, Delivery) error {
			deliveries++
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{Session: "proj", Panes: panes, Message: "secret", Submit: true})
	dispatchErr := requireCode(t, err, ErrRedactionBlocked)
	if dispatchErr.Target == nil || dispatchErr.Target.Address != "1" {
		t.Fatalf("blocked target = %+v, want address 1", dispatchErr.Target)
	}
	if deliveries != 0 {
		t.Fatalf("delivery calls = %d, want 0", deliveries)
	}
	if result.Blocked != 1 || result.Skipped != 2 || result.Delivered != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := []ReceiptStatus{result.Receipts[0].Status, result.Receipts[1].Status, result.Receipts[2].Status}; !reflect.DeepEqual(got, []ReceiptStatus{ReceiptSkipped, ReceiptBlocked, ReceiptSkipped}) {
		t.Fatalf("statuses = %v", got)
	}
	if !result.Receipts[1].Redaction.Blocked || result.Receipts[1].Redaction.Findings != 1 {
		t.Fatalf("blocked receipt = %+v", result.Receipts[1])
	}
}

func TestPrepareFailuresAreAtomic(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
	}
	for _, tc := range []struct {
		name      string
		ports     Ports
		wantCode  ErrorCode
		wantIndex int
	}{
		{
			name: "builder error",
			ports: Ports{
				Builder: FinalMessageBuilderFunc(func(_ context.Context, in BuildInput) (string, error) {
					if in.Target.Address == "1" {
						return "", errors.New("builder down")
					}
					return in.BaseMessage, nil
				}),
				Redactor: AllowAllRedactor{},
			},
			wantCode:  ErrMessageBuild,
			wantIndex: 1,
		},
		{
			name: "redactor error",
			ports: Ports{
				Redactor: FinalMessageRedactorFunc(func(_ context.Context, target Target, message string) (RedactionResult, error) {
					if target.Address == "0" {
						return RedactionResult{}, errors.New("scanner down")
					}
					return RedactionResult{Message: message}, nil
				}),
			},
			wantCode:  ErrRedaction,
			wantIndex: 0,
		},
		{
			name: "invalid redaction receipt",
			ports: Ports{
				Redactor: FinalMessageRedactorFunc(func(_ context.Context, _ Target, message string) (RedactionResult, error) {
					return RedactionResult{Message: message, Findings: -1}, nil
				}),
			},
			wantCode:  ErrRedaction,
			wantIndex: 0,
		},
		{
			name: "protocol error",
			ports: Ports{
				Redactor: AllowAllRedactor{},
				Protocols: ProtocolPlannerFunc(func(_ context.Context, target Target, _ bool) (ProtocolPlan, error) {
					if target.Address == "1" {
						return ProtocolPlan{}, errors.New("protocol unavailable")
					}
					return ProtocolPlan{Protocol: ProtocolDoubleEnter}, nil
				}),
			},
			wantCode:  ErrProtocol,
			wantIndex: 1,
		},
		{
			name: "inconsistent protocol",
			ports: Ports{
				Redactor: AllowAllRedactor{},
				Protocols: ProtocolPlannerFunc(func(context.Context, Target, bool) (ProtocolPlan, error) {
					return ProtocolPlan{Protocol: ProtocolStageOnly}, nil
				}),
			},
			wantCode:  ErrProtocol,
			wantIndex: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deliveries int
			ports := tc.ports
			ports.Deliverer = DelivererFunc(func(context.Context, Delivery) error {
				deliveries++
				return nil
			})
			service, err := NewService(ports)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Execute(context.Background(), Request{Session: "proj", Panes: panes, Message: "work", Submit: true})
			dispatchErr := requireCode(t, err, tc.wantCode)
			if dispatchErr.Target == nil || dispatchErr.Target.Address != fmt.Sprint(tc.wantIndex) {
				t.Fatalf("target = %+v, want address %d", dispatchErr.Target, tc.wantIndex)
			}
			if deliveries != 0 {
				t.Fatalf("delivery calls = %d, want 0", deliveries)
			}
			if result.Delivered != 0 || result.Skipped+result.Failed+result.Blocked != len(panes) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestDispatchContinuesAfterDeliveryFailureAndPacesEveryAttempt(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
		testPane("%3", 0, 2, tmux.AgentGemini, ""),
	}
	var attempted []string
	var paces []Pace
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			attempted = append(attempted, delivery.Target.Address)
			if delivery.Target.Address == "1" {
				return errors.New("pane busy")
			}
			return nil
		}),
		Pacer: PacerFunc(func(_ context.Context, pace Pace) error {
			paces = append(paces, pace)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj", Panes: panes, Message: "work", Submit: true, Delay: time.Second,
	})
	requireCode(t, err, ErrDelivery)
	if got, want := attempted, []string{"0", "1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attempted = %v, want %v", got, want)
	}
	if len(paces) != 2 || paces[0].Previous.Address != "0" || paces[0].Next.Address != "1" || paces[0].Ordinal != 1 {
		t.Fatalf("paces = %+v", paces)
	}
	if result.Success || result.Delivered != 2 || result.Failed != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := []ReceiptStatus{result.Receipts[0].Status, result.Receipts[1].Status, result.Receipts[2].Status}; !reflect.DeepEqual(got, []ReceiptStatus{ReceiptDelivered, ReceiptFailed, ReceiptDelivered}) {
		t.Fatalf("statuses = %v", got)
	}
}

func TestDispatchStopOnFailureSkipsRemainingTargets(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
		testPane("%3", 0, 2, tmux.AgentGemini, ""),
	}
	var attempted []string
	var paceCount int
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			attempted = append(attempted, delivery.Target.Address)
			if delivery.Target.Address == "1" {
				return errors.New("pane busy")
			}
			return nil
		}),
		Pacer: PacerFunc(func(context.Context, Pace) error { paceCount++; return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj", Panes: panes, Message: "work", Submit: true, Delay: time.Second, StopOnFailure: true,
	})
	requireCode(t, err, ErrDelivery)
	if !reflect.DeepEqual(attempted, []string{"0", "1"}) || paceCount != 1 {
		t.Fatalf("attempted=%v paceCount=%d", attempted, paceCount)
	}
	if result.Delivered != 1 || result.Failed != 1 || result.Skipped != 1 || result.Receipts[2].Status != ReceiptSkipped {
		t.Fatalf("result = %+v", result)
	}
}

func TestDispatchPacingFailureStopsBeforeNextDelivery(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
		testPane("%3", 0, 2, tmux.AgentGemini, ""),
	}
	var attempted []string
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			attempted = append(attempted, delivery.Target.Address)
			return nil
		}),
		Pacer: PacerFunc(func(context.Context, Pace) error { return errors.New("clock stopped") }),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj", Panes: panes, Message: "work", Submit: true, Delay: time.Second,
	})
	requireCode(t, err, ErrPacing)
	if !reflect.DeepEqual(attempted, []string{"0"}) {
		t.Fatalf("attempted = %v, want [0]", attempted)
	}
	if result.Delivered != 1 || result.Failed != 1 || result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDryRunPreflightsButNeverPacesOrDelivers(t *testing.T) {
	t.Parallel()
	var built, redacted, paced, delivered int
	service, err := NewService(Ports{
		Builder: FinalMessageBuilderFunc(func(_ context.Context, in BuildInput) (string, error) {
			built++
			return in.BaseMessage, nil
		}),
		Redactor: FinalMessageRedactorFunc(func(_ context.Context, _ Target, message string) (RedactionResult, error) {
			redacted++
			return RedactionResult{Message: message, Mode: "warn"}, nil
		}),
		Deliverer: DelivererFunc(func(context.Context, Delivery) error { delivered++; return nil }),
		Pacer:     PacerFunc(func(context.Context, Pace) error { paced++; return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj",
		Panes: []tmux.Pane{
			testPane("%1", 0, 0, tmux.AgentClaude, ""),
			testPane("%2", 0, 1, tmux.AgentCodex, ""),
		},
		Message: "work", Submit: true, Delay: time.Second, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !result.DryRun || built != 2 || redacted != 2 || paced != 0 || delivered != 0 {
		t.Fatalf("result=%+v built=%d redacted=%d paced=%d delivered=%d", result, built, redacted, paced, delivered)
	}
	for _, receipt := range result.Receipts {
		if receipt.Status != ReceiptPrepared {
			t.Fatalf("receipt status = %q, want prepared", receipt.Status)
		}
	}
}

func TestPreparedIsSingleUseAndServiceBound(t *testing.T) {
	t.Parallel()
	var deliveries int
	newService := func() *Service {
		service, err := NewService(Ports{
			Redactor:  AllowAllRedactor{},
			Deliverer: DelivererFunc(func(context.Context, Delivery) error { deliveries++; return nil }),
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	owner := newService()
	other := newService()
	prepared, err := owner.Prepare(context.Background(), Request{
		Session: "proj", Panes: []tmux.Pane{testPane("%1", 0, 0, tmux.AgentClaude, "")}, Message: "work", Submit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Dispatch(context.Background(), prepared)
	requireCode(t, err, ErrPreparedByOtherSvc)
	if foreign.Success || foreign.DryRun {
		t.Fatalf("foreign-service rejection result = %+v, want success=false dry_run=false", foreign)
	}
	if deliveries != 0 {
		t.Fatalf("cross-service dispatch delivered %d times", deliveries)
	}
	result, err := owner.Dispatch(context.Background(), prepared)
	if err != nil || result.Delivered != 1 {
		t.Fatalf("first owner dispatch = %+v, %v", result, err)
	}
	replayed, err := owner.Dispatch(context.Background(), prepared)
	requireCode(t, err, ErrAlreadyDispatched)
	if replayed.Success || replayed.DryRun {
		t.Fatalf("rejected replay result = %+v, want success=false dry_run=false", replayed)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestServiceValidationAndCancellationFailClosed(t *testing.T) {
	t.Parallel()
	service, err := NewService(Ports{
		Redactor:  AllowAllRedactor{},
		Deliverer: DelivererFunc(func(context.Context, Delivery) error { t.Fatal("unexpected delivery"); return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := Request{Session: "proj", Panes: []tmux.Pane{testPane("%1", 0, 0, tmux.AgentClaude, "")}, Message: "work", Submit: true}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		req  Request
	}{
		{name: "nil context", ctx: nil, req: base},
		{name: "missing session", ctx: context.Background(), req: func() Request { r := base; r.Session = " "; return r }()},
		{name: "missing message", ctx: context.Background(), req: func() Request { r := base; r.Message = "\n"; return r }()},
		{name: "negative delay", ctx: context.Background(), req: func() Request { r := base; r.Delay = -time.Millisecond; return r }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Execute(tc.ctx, tc.req)
			requireCode(t, err, ErrInvalidRequest)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Execute(ctx, base)
	requireCode(t, err, ErrInvalidRequest)
}

func TestExplicitEmptyMessagePolicyStagesOrSubmitsWithoutWeakeningDefaultValidation(t *testing.T) {
	t.Parallel()
	var delivered Delivery
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			delivered = delivery
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session:           "proj",
		Panes:             []tmux.Pane{testPane("%1", 0, 0, tmux.AgentClaude, "")},
		Message:           "",
		AllowEmptyMessage: true,
		Submit:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || delivered.Message != "" || delivered.Protocol != ProtocolDoubleEnter {
		t.Fatalf("result=%+v delivery=%+v", result, delivered)
	}
}

func TestPlanAndPreparedDefensiveCopies(t *testing.T) {
	t.Parallel()
	pane := testPane("%1", 0, 0, tmux.AgentClaude, "", "api")
	service, err := NewService(Ports{
		Redactor:  AllowAllRedactor{},
		Deliverer: DelivererFunc(func(context.Context, Delivery) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(context.Background(), Request{Session: "proj", Panes: []tmux.Pane{pane}, Message: "work", Submit: true})
	if err != nil {
		t.Fatal(err)
	}
	targets := prepared.Targets()
	targets[0].Address = "corrupt"
	targets[0].Tags[0] = "corrupt"
	preview := prepared.Preview()
	if preview.Targets[0].Address != "0" || preview.Targets[0].Tags[0] != "api" {
		t.Fatalf("prepared state mutated through Targets(): %+v", preview.Targets[0])
	}
	preview.Receipts[0].Warnings = append(preview.Receipts[0].Warnings, "corrupt")
	preview2 := prepared.Preview()
	if len(preview2.Receipts[0].Warnings) != 0 {
		t.Fatalf("prepared warnings mutated through Preview(): %v", preview2.Receipts[0].Warnings)
	}
}

func TestTargetOrdererControlsDeliveryOrderWithExactPermutationValidation(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		testPane("%1", 0, 0, tmux.AgentClaude, ""),
		testPane("%2", 0, 1, tmux.AgentCodex, ""),
		testPane("%3", 0, 2, tmux.AgentGemini, ""),
	}
	var delivered []string
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Orderer: TargetOrdererFunc(func(_ context.Context, input OrderInput) ([]Target, error) {
			return []Target{input.Targets[2], input.Targets[0], input.Targets[1]}, nil
		}),
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			delivered = append(delivered, delivery.Target.Address)
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{Session: "proj", Panes: panes, Message: "work", Submit: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"2", "0", "1"}; !reflect.DeepEqual(delivered, want) || !reflect.DeepEqual(targetAddresses(result.Targets), want) {
		t.Fatalf("delivered=%v result targets=%v want=%v", delivered, targetAddresses(result.Targets), want)
	}

	for _, tc := range []struct {
		name    string
		orderer TargetOrderer
	}{
		{
			name: "missing",
			orderer: TargetOrdererFunc(func(_ context.Context, input OrderInput) ([]Target, error) {
				return input.Targets[:2], nil
			}),
		},
		{
			name: "duplicate",
			orderer: TargetOrdererFunc(func(_ context.Context, input OrderInput) ([]Target, error) {
				return []Target{input.Targets[0], input.Targets[0], input.Targets[2]}, nil
			}),
		},
		{
			name: "foreign",
			orderer: TargetOrdererFunc(func(_ context.Context, input OrderInput) ([]Target, error) {
				result := append([]Target(nil), input.Targets...)
				result[1].Ref.ID = "%999"
				return result, nil
			}),
		},
		{
			name: "port error",
			orderer: TargetOrdererFunc(func(context.Context, OrderInput) ([]Target, error) {
				return nil, errors.New("seed unavailable")
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			invalidService, newErr := NewService(Ports{
				Redactor: AllowAllRedactor{},
				Orderer:  tc.orderer,
				Deliverer: DelivererFunc(func(context.Context, Delivery) error {
					calls++
					return nil
				}),
			})
			if newErr != nil {
				t.Fatal(newErr)
			}
			_, executeErr := invalidService.Execute(context.Background(), Request{Session: "proj", Panes: panes, Message: "work", Submit: true})
			requireCode(t, executeErr, ErrOrdering)
			if calls != 0 {
				t.Fatalf("delivery calls = %d, want 0", calls)
			}
		})
	}
}

func TestLifecycleHooksExposeStableApplicationOrderAndSafeFinish(t *testing.T) {
	t.Parallel()
	var events []string
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(_ context.Context, delivery Delivery) error {
			events = append(events, "deliver:"+delivery.Target.Address)
			return nil
		}),
		Pacer: PacerFunc(func(_ context.Context, pace Pace) error {
			events = append(events, "pace:"+pace.Previous.Address+">"+pace.Next.Address)
			return nil
		}),
		Lifecycle: LifecycleHooks{
			RequestAccepted: func(_ context.Context, req Request) error {
				events = append(events, "accepted:"+req.Session)
				return nil
			},
			TargetsPlanned: func(_ context.Context, _ Request, targets []Target) error {
				events = append(events, "planned:"+strings.Join(targetAddresses(targets), ","))
				return nil
			},
			Prepared: func(_ context.Context, _ Request, deliveries []Delivery) error {
				events = append(events, fmt.Sprintf("prepared:%d", len(deliveries)))
				return nil
			},
			BeforeDispatch: func(_ context.Context, _ Request, deliveries []Delivery) error {
				events = append(events, fmt.Sprintf("before:%d", len(deliveries)))
				return nil
			},
			AfterReceipt: func(_ context.Context, _ Delivery, receipt Receipt) {
				events = append(events, "receipt:"+receipt.Target.Address+":"+string(receipt.Status))
			},
			Finished: func(_ context.Context, _ Request, result Result, finishErr error) {
				events = append(events, fmt.Sprintf("finished:%d:%v", result.Delivered, finishErr == nil))
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{
		Session: "proj",
		Panes: []tmux.Pane{
			testPane("%1", 0, 0, tmux.AgentClaude, ""),
			testPane("%2", 0, 1, tmux.AgentCodex, ""),
		},
		Message: "work", Submit: true, Delay: time.Second,
	})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{
		"accepted:proj",
		"planned:0,1",
		"prepared:2",
		"before:2",
		"deliver:0",
		"receipt:0:delivered",
		"pace:0>1",
		"deliver:1",
		"receipt:1:delivered",
		"finished:2:true",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events =\n%v\nwant =\n%v", events, want)
	}
}

func TestLifecycleFailureAbortsBeforeActuationAndFinishesOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		hooks func(*int) LifecycleHooks
	}{
		{
			name: "request accepted",
			hooks: func(finished *int) LifecycleHooks {
				return LifecycleHooks{
					RequestAccepted: func(context.Context, Request) error { return errors.New("audit unavailable") },
					Finished:        func(context.Context, Request, Result, error) { *finished++ },
				}
			},
		},
		{
			name: "targets planned",
			hooks: func(finished *int) LifecycleHooks {
				return LifecycleHooks{
					TargetsPlanned: func(context.Context, Request, []Target) error { return errors.New("pre-hook failed") },
					Finished:       func(context.Context, Request, Result, error) { *finished++ },
				}
			},
		},
		{
			name: "prepared",
			hooks: func(finished *int) LifecycleHooks {
				return LifecycleHooks{
					Prepared: func(context.Context, Request, []Delivery) error { return errors.New("checkpoint failed") },
					Finished: func(context.Context, Request, Result, error) { *finished++ },
				}
			},
		},
		{
			name: "before dispatch",
			hooks: func(finished *int) LifecycleHooks {
				return LifecycleHooks{
					BeforeDispatch: func(context.Context, Request, []Delivery) error { return errors.New("dcg blocked") },
					Finished:       func(context.Context, Request, Result, error) { *finished++ },
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finished := 0
			delivered := 0
			service, err := NewService(Ports{
				Redactor: AllowAllRedactor{},
				Deliverer: DelivererFunc(func(context.Context, Delivery) error {
					delivered++
					return nil
				}),
				Lifecycle: tc.hooks(&finished),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Execute(context.Background(), Request{
				Session: "proj", Panes: []tmux.Pane{testPane("%1", 0, 0, tmux.AgentClaude, "")}, Message: "work", Submit: true,
			})
			requireCode(t, err, ErrLifecycle)
			if delivered != 0 || finished != 1 {
				t.Fatalf("delivered=%d finished=%d, want 0 and 1", delivered, finished)
			}
		})
	}
}

func TestTMUXDelivererRejectsInvalidOrUnrepresentableProtocols(t *testing.T) {
	t.Parallel()
	deliverer := TMUXDeliverer{}
	target := targetFromPane(testPane("%1", 0, 0, tmux.AgentClaude, ""), false)
	for _, tc := range []struct {
		name     string
		delivery Delivery
		want     string
	}{
		{name: "unknown protocol", delivery: Delivery{Protocol: DeliveryProtocol("triple_enter")}, want: "unsupported delivery protocol"},
		{name: "negative delay", delivery: Delivery{Protocol: ProtocolSingleEnter, EnterDelay: -time.Millisecond}, want: "cannot be negative"},
		{name: "stage-only delay", delivery: Delivery{Protocol: ProtocolStageOnly, EnterDelay: time.Millisecond}, want: "cannot include enter delays"},
		{name: "single-enter second delay", delivery: Delivery{Protocol: ProtocolSingleEnter, SecondEnterDelay: time.Millisecond}, want: "cannot include a second-enter delay"},
		{name: "custom double-enter timing", delivery: Delivery{Protocol: ProtocolDoubleEnter, EnterDelay: time.Millisecond, SecondEnterDelay: time.Millisecond}, want: "requires delays"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.delivery.Target = target
			err := deliverer.Deliver(context.Background(), tc.delivery)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Deliver() error = %v, want substring %q", err, tc.want)
			}
			if !IsGuaranteedNoDeliveryActuation(err) {
				t.Fatalf("Deliver() error = %v, want guaranteed-no-delivery-actuation classification", err)
			}
		})
	}
}

func TestDeliveryActuationClassificationSurvivesServiceWrapping(t *testing.T) {
	service, err := NewService(Ports{
		Redactor: AllowAllRedactor{},
		Deliverer: DelivererFunc(func(context.Context, Delivery) error {
			return GuaranteeNoDeliveryActuation(errors.New("codex 0.152 composer not ready"))
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(context.Background(), Request{
		Session: "proj", Panes: []tmux.Pane{testPane("%1", 0, 0, tmux.AgentCodex, "")}, Message: "work", Submit: true,
	})
	if !IsGuaranteedNoDeliveryActuation(err) {
		t.Fatalf("wrapped service error = %v, want guaranteed-no-delivery-actuation classification", err)
	}
}

// Delivery to a dead agent pane must refuse loudly (ntm-0g0b): keystrokes
// into a bare shell can EXECUTE message text.
func TestRefuseDeadAgentPane(t *testing.T) {
	dead := tmux.Pane{Type: tmux.AgentClaude, Title: "proj__cc_1", Command: "zsh"}
	if err := RefuseDeadAgentPane(dead); err == nil {
		t.Fatal("dead agent pane accepted for delivery")
	} else if !strings.Contains(err.Error(), "PANE_AGENT_DEAD") {
		t.Fatalf("refusal error %q should carry PANE_AGENT_DEAD", err.Error())
	}
	if err := RefuseDeadAgentPane(tmux.Pane{Type: tmux.AgentClaude, Command: "claude"}); err != nil {
		t.Fatalf("live agent pane refused: %v", err)
	}
	if err := RefuseDeadAgentPane(tmux.Pane{Type: tmux.AgentUser, Command: "zsh"}); err != nil {
		t.Fatalf("user pane refused: %v", err)
	}
}
