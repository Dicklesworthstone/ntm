package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/state"
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

// The runner below is the function called by the Cobra command. Fakes replace
// only session/provider/transport I/O, not its planning or admission logic.
func packInjectionTestDeps(t *testing.T) contextInjectDeps {
	t.Helper()
	dir := t.TempDir()
	return contextInjectDeps{
		resolve: func(context.Context, string) (string, error) { return "resolved-demo", nil },
		project: func(context.Context, string) (string, error) { return dir, nil },
		panes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{
				{ID: "%0", Index: 0, Type: tmux.AgentClaude},
				{ID: "%1", Index: 1, Type: "claude-code"},
				{ID: "%2", Index: 2, Type: tmux.AgentCodex},
				{ID: "%9", Index: 9, Type: "user"},
			}, nil
		},
		build: func(_ context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
			return &ntmctx.ContextPackFull{
				ContextPack: state.ContextPack{ID: "pack-" + opts.AgentType, AgentType: state.AgentType(opts.AgentType), RenderedPrompt: "context-for-" + opts.AgentType},
				Components: map[string]*ntmctx.PackComponent{
					"s2p": {Data: json.RawMessage(`"real source"`)},
					"cm":  {Error: "cm not installed"},
				},
			}, nil
		},
		send: func(context.Context, tmux.Pane, string) error { t.Fatal("unexpected send"); return nil },
	}
}

func TestRunContextInjectionBuildsPerCanonicalAgentBeforeSending(t *testing.T) {
	t.Parallel()
	deps := packInjectionTestDeps(t)
	baseBuild := deps.build
	var built, sent []string
	deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
		if len(sent) != 0 {
			t.Fatal("sent before every pack was prepared")
		}
		if opts.SessionID != "resolved-demo" || opts.Task != "implement task" || opts.BeadID != "bd-work" || !reflect.DeepEqual(opts.Files, []string{"*.go"}) {
			t.Fatalf("build lost task scope: %+v", opts)
		}
		built = append(built, opts.AgentType)
		return baseBuild(ctx, opts)
	}
	deps.send = func(_ context.Context, pane tmux.Pane, text string) error {
		if !reflect.DeepEqual(built, []string{"cc", "cod"}) {
			t.Fatalf("bad build grouping: %v", built)
		}
		want := "context-for-" + string(agent.AgentType(pane.Type).Canonical())
		if text != want {
			t.Fatalf("wrong agent payload: %q, want %q", text, want)
		}
		sent = append(sent, pane.ID)
		return nil
	}
	result, err := runContextInjection(context.Background(), contextInjectOptions{Session: "demo", Pane: -1, Build: true, FilesArg: "*.go", Task: "implement task", BeadID: "bd-work"}, deps)
	if err != nil || !result.Success || !reflect.DeepEqual(sent, []string{"%0", "%1", "%2"}) || !reflect.DeepEqual(result.PanesInjected, []int{0, 1, 2}) {
		t.Fatalf("pack delivery failed: %+v, sent=%v, err=%v", result, sent, err)
	}
	if result.Deliveries[0].PackID != "pack-cc" || result.Deliveries[1].PackID != "pack-cc" || result.Deliveries[2].PackID != "pack-cod" || len(result.Warnings) != 2 {
		t.Fatalf("pack metadata or provider warnings lost: %+v", result)
	}
}

func TestRunContextInjectionPreparationFailureSendsNothing(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"provider_error", "missing_source", "invalid_source", "wrong_agent", "max_bytes", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			baseBuild := deps.build
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps.build = func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
				pack, _ := baseBuild(ctx, opts)
				if opts.AgentType == "cod" {
					switch scenario {
					case "provider_error":
						return nil, errors.New("builder failed")
					case "missing_source":
						pack.Components["s2p"].Error = "file missing"
					case "invalid_source":
						pack.Components["s2p"].Data = json.RawMessage(`null`)
					case "wrong_agent":
						pack.AgentType = "cc"
					case "max_bytes":
						pack.RenderedPrompt = strings.Repeat("large", 100)
					case "canceled":
						cancel()
					}
				}
				return pack, nil
			}
			result, err := runContextInjection(ctx, contextInjectOptions{Session: "demo", Pane: -1, Build: true, FilesArg: "main.go", MaxBytes: 100}, deps)
			if err == nil || result.Success || len(result.PanesInjected) != 0 {
				t.Fatalf("preparation failure was admitted: %+v, %v", result, err)
			}
			for _, receipt := range result.Deliveries {
				if receipt.Status != "not_attempted" {
					t.Fatalf("failed preparation delivered work: %+v", receipt)
				}
			}
		})
	}
}

func TestRunContextInjectionStoredPackValidation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"dry_run", "deliver", "wrong_type", "missing", "wrong_id", "oversized", "empty", "bad_utf8"} {
		t.Run(scenario, func(t *testing.T) {
			deps := packInjectionTestDeps(t)
			deps.project = func(context.Context, string) (string, error) {
				t.Fatal("stored pack must not read project files")
				return "", nil
			}
			calls := 0
			deps.send = func(_ context.Context, pane tmux.Pane, text string) error {
				calls++
				if pane.ID != "%0" || text != "stored context" {
					t.Fatalf("stored payload changed: %s %q", pane.ID, text)
				}
				return nil
			}
			deps.load = func(id string) (*state.ContextPack, error) {
				if id != "pack-stored" {
					t.Fatalf("wrong artifact request: %q", id)
				}
				pack := &state.ContextPack{ID: id, AgentType: "claude", RenderedPrompt: "stored context", TokenCount: 1}
				switch scenario {
				case "wrong_type":
					pack.AgentType = "cod"
				case "missing":
					return nil, nil
				case "wrong_id":
					pack.ID = "pack-other"
				case "oversized":
					pack.RenderedPrompt = strings.Repeat("x", ntmctx.GetTokenBudget("cc")*4+1)
				case "empty":
					pack.RenderedPrompt = " "
				case "bad_utf8":
					pack.RenderedPrompt = string([]byte{0xff})
				}
				return pack, nil
			}
			result, err := runContextInjection(context.Background(), contextInjectOptions{Session: "demo", Pane: 0, PackID: "pack-stored", DryRun: scenario == "dry_run"}, deps)
			if scenario == "deliver" {
				if err != nil || !result.Success || calls != 1 || len(result.PanesInjected) != 1 {
					t.Fatalf("stored delivery: %+v %v calls=%d", result, err, calls)
				}
			} else if scenario == "dry_run" {
				if err != nil || !result.Success || calls != 0 || len(result.PanesInjected) != 0 || len(result.PanesPlanned) != 1 {
					t.Fatalf("stored plan: %+v %v calls=%d", result, err, calls)
				}
			} else if err == nil || result.Success || calls != 0 {
				t.Fatalf("invalid artifact sent: %+v %v calls=%d", result, err, calls)
			}
		})
	}
}

func TestRunContextInjectionRejectsInvalidOptionsBeforeIO(t *testing.T) {
	t.Parallel()
	cases := []contextInjectOptions{
		{Session: "demo", Pane: -1, Build: true, PackID: "pack-x"},
		{Session: "demo", Pane: -1, PackID: "pack-x", FilesArg: "main.go"},
		{Session: "demo", Pane: -1, PackSet: true},
		{Session: "demo", Pane: -1, FilesSet: true},
		{Session: "demo", Pane: -1, FilesArg: "one.go,,two.go"},
		{Session: "demo", Pane: -1, Task: "orphaned task"},
		{Session: "demo", Pane: -1, MaxBytes: -1},
		{Session: "demo", Pane: -2},
		{Session: "demo", Pane: 0, All: true},
	}
	for _, opts := range cases {
		deps := contextInjectDeps{resolve: func(context.Context, string) (string, error) {
			t.Fatal("invalid flags reached external I/O")
			return "", nil
		}}
		result, err := runContextInjection(context.Background(), opts, deps)
		if err == nil || result.Success {
			t.Fatalf("invalid flags accepted: %+v", opts)
		}
	}
}

func TestContextPackSelectionRefusesShellAndAmbiguousTargets(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{{ID: "%0", Index: 0, Type: tmux.AgentClaude}, {ID: "%1", Index: 1, Type: "user"}, {ID: "%2", Index: 2, Type: "unknown"}}
	selected, err := selectContextPackPanes(panes, -1, false, "demo")
	if err != nil || len(selected) != 1 || selected[0].ID != "%0" {
		t.Fatalf("agent-at-zero not selected: %+v %v", selected, err)
	}
	for _, index := range []int{1, 2} {
		if _, err := selectContextPackPanes(panes, index, false, "demo"); err == nil {
			t.Fatalf("non-agent %d accepted", index)
		}
	}
	if _, err := selectContextPackPanes(panes, -1, true, "demo"); err == nil {
		t.Fatal("--all admitted a shell")
	}
	panes = append(panes, tmux.Pane{ID: "%3", Index: 0, Type: tmux.AgentCodex})
	if _, err := selectContextPackPanes(panes, 0, false, "demo"); err == nil {
		t.Fatal("ambiguous pane index silently selected first match")
	}
}

type contextStoreTestFake struct {
	pack              *state.ContextPack
	readErr, writeErr error
	writes            int
	read              func()
	concurrentInsert  bool
}

func (s *contextStoreTestFake) GetContextPack(string) (*state.ContextPack, error) {
	if s.read != nil {
		s.read()
	}
	return s.pack, s.readErr
}
func (s *contextStoreTestFake) CreateContextPack(pack *state.ContextPack) error {
	s.writes++
	if s.writeErr == nil || s.concurrentInsert {
		copy := *pack
		s.pack = &copy
	}
	return s.writeErr
}
func TestContextPackPersistenceIsRequiredAndIdempotent(t *testing.T) {
	t.Parallel()
	pack := &state.ContextPack{ID: "pack-test", AgentType: "cc", RenderedPrompt: "saved context"}
	for _, store := range []*contextStoreTestFake{{}, {}} {
		for i := 0; i < 2; i++ {
			if err := persistContextPack(context.Background(), store, pack); err != nil {
				t.Fatal(err)
			}
		}
		if store.writes != 1 || store.pack.RenderedPrompt != pack.RenderedPrompt {
			t.Fatalf("cached pack not persisted exactly once in each store: %+v", store)
		}
	}
	failure := errors.New("disk full")
	store := &contextStoreTestFake{writeErr: failure}
	if err := persistContextPack(context.Background(), store, pack); !errors.Is(err, failure) {
		t.Fatalf("persistence failure swallowed: %v", err)
	}
	store = &contextStoreTestFake{readErr: failure}
	if err := persistContextPack(context.Background(), store, pack); !errors.Is(err, failure) || store.writes != 0 {
		t.Fatalf("read failure mutated storage: %v", err)
	}
	store = &contextStoreTestFake{pack: &state.ContextPack{ID: pack.ID, RenderedPrompt: "different"}}
	if err := persistContextPack(context.Background(), store, pack); err == nil || store.writes != 0 || store.pack.RenderedPrompt != "different" {
		t.Fatalf("conflicting artifact overwritten: %+v %v", store, err)
	}
	store = &contextStoreTestFake{writeErr: errors.New("duplicate ID"), concurrentInsert: true}
	if err := persistContextPack(context.Background(), store, pack); err != nil {
		t.Fatalf("identical concurrent insert rejected: %v", err)
	}
}
func TestContextPackPersistenceCancellationPreventsWrite(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &contextStoreTestFake{read: cancel}
	err := persistContextPack(ctx, store, &state.ContextPack{ID: "pack-test"})
	if !errors.Is(err, context.Canceled) || store.writes != 0 {
		t.Fatalf("canceled persistence mutated storage: %v writes=%d", err, store.writes)
	}
}
