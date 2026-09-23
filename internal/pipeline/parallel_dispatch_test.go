//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func parallelDispatchConfig(t *testing.T) ExecutorConfig {
	t.Helper()
	cfg := DefaultExecutorConfig("parallel-dispatch")
	cfg.ProjectDir = t.TempDir()
	cfg.RunID = "run-parallel-dispatch"
	cfg.GlobalTimeout = 5 * time.Second
	cfg.DefaultTimeout = 3 * time.Second
	return cfg
}

func parallelDispatchWorkflow(children ...Step) *Workflow {
	return &Workflow{SchemaVersion: SchemaVersion, Name: "parallel-dispatch", Steps: []Step{
		{ID: "work", Parallel: ParallelSpec{Steps: children}},
	}}
}

func TestParallelDispatchControlledCommandsActuallyOverlap(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	// Neither command can finish until the other has started. Serial dispatch
	// (or routing commands to a tmux prompt) cannot satisfy this rendezvous.
	workflow := parallelDispatchWorkflow(
		Step{ID: "left", Command: "printf ready > left; while [ ! -f right ]; do sleep 0.01; done; printf alpha", OutputVar: "shared"},
		Step{ID: "right", Command: "printf ready > right; while [ ! -f left ]; do sleep 0.01; done; printf beta", OutputVar: "shared"},
	)
	workflow.Settings.Limits.SubstepParallelMax = 2
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("parallel command rendezvous: state=%+v error=%v", state, err)
	}
	if !reflect.DeepEqual(state.Variables["shared"], []string{"alpha", "beta"}) {
		t.Fatalf("lost declaration-ordered command outputs: %#v", state.Variables["shared"])
	}
	for id, want := range map[string]string{"work_left": "alpha", "work_right": "beta"} {
		if got := state.Steps[id]; got.Status != StatusCompleted || got.Output != want || got.PaneUsed != "" {
			t.Fatalf("command child %s was not executed as a command: %+v", id, got)
		}
		if got := state.Variables["steps."+id+".output"]; got != want {
			t.Fatalf("missing canonical child output: %s = %#v", id, got)
		}
	}
	persisted, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil || persisted.Status != StatusCompleted || !persisted.ParallelState["work"].AllSubstepsSettled {
		t.Fatalf("parallel completion was not durable: %+v %v", persisted, err)
	}
}

func TestParallelDispatchNestedGroupsRunAndResumeWithoutRepeatingChildren(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(Step{ID: "nested", Parallel: ParallelSpec{Steps: []Step{
		{ID: "once", Command: "printf x >> once; printf saved"},
		{ID: "finish", Command: "test -f release && printf finished > marker"},
	}}})
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Steps["work_nested_once"].Status != StatusCompleted {
		t.Fatalf("nested group did not preserve its completed child: %+v %v", state, err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("load nested recovery workflow: %v %+v", err, validation)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume nested commands: %+v %v", resumed, err)
	}
	for name, want := range map[string]string{"once": "x", "marker": "finished"} {
		data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, want %q: %v", name, data, want, err)
		}
	}
}

func TestParallelDispatchCommandsUseRetriesAndSuccessHooks(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(Step{
		ID: "retry", Command: "if [ ! -f attempted ]; then printf x > attempted; exit 7; fi; printf recovered",
		OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond},
		OnSuccess: []Step{{ID: "receipt", Command: "printf '%s' '${steps.work_retry.output}' > receipt"}},
	})
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("retry: %+v %v", state, err)
	}
	if got := state.Steps["work_retry"]; got.Attempts != 2 || got.Output != "recovered" || got.Error != nil {
		t.Fatalf("lost canonical retry result: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "receipt"))
	if err != nil || string(data) != "recovered" {
		t.Fatalf("success hook could not consume its command parent's output: %q %v", data, err)
	}
}

func TestParallelDispatchFailFastCancelsRunningCommandSibling(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(
		Step{ID: "failure", Command: "while [ ! -f started ]; do sleep 0.01; done; exit 9"},
		Step{ID: "sibling", Command: "printf running > started; sleep 30; printf escaped > escaped"},
	)
	workflow.Steps[0].OnError = ErrorActionFailFast
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("fail-fast: %+v %v", state, err)
	}
	if state.Steps["work_failure"].Status != StatusFailed || state.Steps["work_sibling"].Status != StatusCancelled {
		t.Fatalf("failure did not stop actual sibling work: %+v", state.Steps)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "started")); err != nil || string(data) != "running" {
		t.Fatalf("fixture did not prove the sibling actually started: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "escaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled sibling performed its final effect: %v", err)
	}
}

func TestParallelDispatchNestedScopeResolutionIsExact(t *testing.T) {
	workflow := parallelDispatchWorkflow(Step{ID: "nested", Parallel: ParallelSpec{Steps: []Step{
		{ID: "leaf", Command: "true", OutputVar: "nested_result"},
		{ID: "leaf_extra", Command: "true"},
	}}})
	graph := NewDependencyGraph(workflow)
	step, canonical, ok := graph.ResolveScopedRuntimeStep("work_nested_leaf")
	if !ok || canonical != "leaf" || step.OutputVar != "nested_result" {
		t.Fatalf("nested runtime child did not resolve: %+v %q %v", step, canonical, ok)
	}
	for _, id := range []string{"work_nested_leaf_missing", "work_nestedness_leaf", "other_nested_leaf"} {
		if _, _, ok := graph.ResolveScopedRuntimeStep(id); ok {
			t.Fatalf("unknown result %q was mistaken for completed work", id)
		}
	}
}

func TestParallelDispatchContinueRetainsFailureAndSuccessfulOutput(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(
		Step{ID: "bad", Command: "exit 3", OutputVar: "shared"},
		Step{ID: "good", Command: "printf useful", OutputVar: "shared"},
	)
	workflow.Steps[0].OnError = ErrorActionContinue
	workflow.Steps = append(workflow.Steps, Step{ID: "after", DependsOn: []string{"work"}, Command: "printf after > after"})
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("continue: %+v %v", state, err)
	}
	if !reflect.DeepEqual(state.Variables["shared"], []string{"", "useful"}) {
		t.Fatalf("changed failed-slot aggregation: %#v", state.Variables["shared"])
	}
	if group := state.Steps["work"]; group.Error == nil || len(group.Error.Aggregated) != 1 {
		t.Fatalf("continue hid the failed child: %+v", group)
	}
	if state.Steps["after"].Status != StatusCompleted {
		t.Fatal("continue prevented downstream work")
	}
}

func TestParallelDispatchDryRunAndSkippedCommandsDoNotRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			cfg := parallelDispatchConfig(t)
			cfg.DryRun = dry
			child := Step{ID: "never", Command: "printf side-effect > unexpected"}
			if !dry {
				child.When = "false"
			}
			state, err := RunControlledPipeline(context.Background(), parallelDispatchWorkflow(child), nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("read-only dispatch: %+v %v", state, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "unexpected")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only child ran")
			}
			if dry {
				if _, err := os.Stat(filepath.Join(cfg.ProjectDir, ".ntm")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("dry run wrote state")
				}
			} else if state.Steps["work_never"].SkipKind != SkipKindWhenCondition {
				t.Fatal("lost structured conditional skip")
			}
		})
	}
}

func TestParallelDispatchParsesCommandOutputWithoutNamedVariable(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(Step{ID: "json", Command: `printf '{"ok":true}'`, OutputParse: OutputParse{Type: "json"}})
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil {
		t.Fatalf("parsed output: %+v %v", state, err)
	}
	parsed, ok := state.Steps["work_json"].ParsedData.(map[string]interface{})
	if !ok || parsed["ok"] != true {
		t.Fatalf("output_parse was ignored without output_var: %#v", state.Steps["work_json"].ParsedData)
	}
	if state.Variables["steps.work_json.data"] == nil {
		t.Fatal("parsed child output not available to downstream steps")
	}
}

// This transport models only pane I/O. Commands, template rendering, retries,
// dependency scheduling, state writes and pane locks use production code.
type parallelDispatchTransport struct {
	mu                   sync.Mutex
	panes                []tmux.Pane
	outputs              map[string]string
	pastes               []string
	messages             []string
	failFirst            bool
	failFirstObservation bool
	changePaneAfterPaste bool
	verified             int
}

func newParallelDispatchTransport() *parallelDispatchTransport {
	return &parallelDispatchTransport{panes: []tmux.Pane{
		{ID: "%1", Index: 1, PID: 4101, Type: tmux.AgentType("claude"), Width: 100},
		{ID: "%2", Index: 2, PID: 4102, Type: tmux.AgentType("claude"), Width: 100},
	}, outputs: make(map[string]string)}
}
func (m *parallelDispatchTransport) GetPanes(string) ([]tmux.Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	panes := append([]tmux.Pane(nil), m.panes...)
	// Metadata loading also enumerates panes before routing. Change topology
	// only after the first dispatch, so this fixture tests a retry retaining
	// the selected target rather than depending on the metadata lookup count.
	if m.changePaneAfterPaste && len(m.pastes) > 0 {
		panes[0].Index = 3
		panes = append(panes, tmux.Pane{ID: "%99", Index: 1, PID: 4199, Type: tmux.AgentClaude, Width: 100})
	}
	return panes, nil
}
func (m *parallelDispatchTransport) PasteKeys(target, content string, enter bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pastes = append(m.pastes, target)
	m.messages = append(m.messages, content)
	if m.failFirst && len(m.pastes) == 1 {
		return errors.New("transient paste failure")
	}
	m.outputs[target] += content + "\n"
	return nil
}
func (m *parallelDispatchTransport) CapturePaneOutput(target string, _ int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failFirstObservation && m.verified > 0 {
		m.failFirstObservation = false
		return "", errors.New("temporary capture failure after confirmed delivery")
	}
	return m.outputs[target], nil
}
func (m *parallelDispatchTransport) VerifySubmission(ctx context.Context, _, _, _ string, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verified++
	return ctx.Err()
}

func TestParallelDispatchTemplateRetriesStayOnSelectedPane(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	path := filepath.Join(cfg.ProjectDir, "instructions.md")
	if err := os.WriteFile(path, []byte("Do <TASK>"), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := parallelDispatchWorkflow(Step{ID: "template", Template: path, Params: map[string]interface{}{"TASK": "real work"},
		Pane: PaneSpec{Index: 1}, Wait: WaitTime, Timeout: Duration{Duration: time.Millisecond},
		OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond}})
	transport := newParallelDispatchTransport()
	transport.failFirstObservation, transport.changePaneAfterPaste = true, true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(transport)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("parallel template: %+v %v", state, err)
	}
	if got := state.Steps["work_template"]; got.Attempts != 2 || got.PaneUsed != "%1" || got.Error != nil {
		t.Fatalf("template retry: %+v", got)
	}
	if !reflect.DeepEqual(transport.pastes, []string{"%1"}) || !reflect.DeepEqual(transport.messages, []string{"Do real work"}) {
		t.Fatalf("confirmed template was resent or re-routed: %v %q", transport.pastes, transport.messages)
	}
	if transport.verified != 1 {
		t.Fatalf("submission verification calls = %d, want 1 successful paste", transport.verified)
	}
}

func TestParallelDispatchUnknownTemplateDeliveryIsNotRetried(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	path := filepath.Join(cfg.ProjectDir, "instructions.md")
	if err := os.WriteFile(path, []byte("Do <TASK>"), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := parallelDispatchWorkflow(Step{ID: "template", Template: path, Params: map[string]interface{}{"TASK": "real work"},
		Pane: PaneSpec{Index: 1}, Wait: WaitNone, OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond}})
	transport := newParallelDispatchTransport()
	transport.failFirst, transport.changePaneAfterPaste = true, true
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(transport)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("unknown template delivery was accepted: %+v %v", state, err)
	}
	if got := state.Steps["work_template"]; got.Attempts != 1 || got.PaneUsed != "%1" || got.Error == nil || !strings.Contains(got.Error.Message, "unknown") {
		t.Fatalf("unknown template outcome lost: %+v", got)
	}
	if !reflect.DeepEqual(transport.pastes, []string{"%1"}) || transport.verified != 0 || state.AgentDeliveries["work_template"].Status != agentDeliverySending {
		t.Fatalf("ambiguous delivery was repeated or discarded: pastes=%v verified=%d receipt=%+v", transport.pastes, transport.verified, state.AgentDeliveries["work_template"])
	}
}

func TestParallelDispatchSuccessHookCanReuseParentPane(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	cfg.GlobalTimeout = time.Second
	workflow := parallelDispatchWorkflow(Step{ID: "parent", Prompt: "parent", Pane: PaneSpec{Index: 1}, Wait: WaitNone,
		OnSuccess: []Step{{ID: "hook", Prompt: "hook", Pane: PaneSpec{Index: 1}, Wait: WaitNone}}})
	transport := newParallelDispatchTransport()
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(transport)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Steps["work_parent_on_success_hook"].Status != StatusCompleted {
		t.Fatalf("same-pane success hook blocked behind its own parent: %+v %v", state, err)
	}
	if !reflect.DeepEqual(transport.pastes, []string{"%1", "%1"}) {
		t.Fatalf("hook did not execute on parent pane: %v", transport.pastes)
	}
}

func TestParallelDispatchSuccessHookDoesNotInheritParentTarget(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	workflow := parallelDispatchWorkflow(Step{ID: "parent", Prompt: "parent", Pane: PaneSpec{Index: 1}, Wait: WaitNone,
		OnSuccess: []Step{{ID: "hook", Prompt: "other pane", Pane: PaneSpec{Index: 2}, Wait: WaitNone}}})
	transport := newParallelDispatchTransport()
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(transport)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Steps["work_parent_on_success_hook"].PaneUsed != "%2" {
		t.Fatalf("hook inherited pinned parent pane: %+v %v", state, err)
	}
	if !reflect.DeepEqual(transport.pastes, []string{"%1", "%2"}) {
		t.Fatalf("incorrect targets: %v", transport.pastes)
	}
}

func TestParallelDispatchMixedPromptTemplateAndCommand(t *testing.T) {
	cfg := parallelDispatchConfig(t)
	path := filepath.Join(cfg.ProjectDir, "instructions.md")
	if err := os.WriteFile(path, []byte("template <TASK>"), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := parallelDispatchWorkflow(
		Step{ID: "command", Command: "printf command-result", OutputVar: "collected", OutputVarMode: OutputVarModeCollect},
		Step{ID: "prompt", Prompt: "prompt-result", Pane: PaneSpec{Index: 1}, Wait: WaitTime, Timeout: Duration{Duration: time.Millisecond}, OutputVar: "collected"},
		Step{ID: "template", Template: path, Params: map[string]interface{}{"TASK": "result"}, Pane: PaneSpec{Index: 2}, Wait: WaitTime, Timeout: Duration{Duration: time.Millisecond}, OutputVar: "collected"},
	)
	transport := newParallelDispatchTransport()
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(transport)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("mixed group: %+v %v", state, err)
	}
	outputs, ok := state.Variables["collected"].(map[string]string)
	if !ok || len(outputs) != 3 || outputs["work_command"] != "command-result" || !strings.Contains(outputs["work_template"], "template result") {
		t.Fatalf("mixed child outputs missing from collect: %#v", state.Variables["collected"])
	}
	if len(transport.pastes) != 2 {
		t.Fatalf("command dispatched to an agent: %v", transport.pastes)
	}
}
