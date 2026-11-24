package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoopResumeConsumesFailureAtEveryBoundary(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"items", "while", "times", "until"} {
		for _, boundary := range []string{"between_iterations", "inside_iteration", "lower_bound"} {
			if boundary == "lower_bound" && kind != "while" {
				continue
			}
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				e := newCommandTestExecutor(t)
				e.config.GlobalTimeout = 20 * time.Second
				e.state.Status, e.state.Session = StatusRunning, e.config.Session
				e.state.Variables["cursor"] = "initial"
				barrier := Step{ID: "barrier", Command: `if [ '${loop.index}' = 1 ] && [ ! -f resume ]; then printf reached > blocked; sleep 30; fi`}
				advisory := Step{ID: "advisory", Command: `printf '%s\n' '${loop.index}' >> effects; exit 7`, OnError: ErrorActionContinue}
				output := Step{ID: "output", Command: `printf '%s' '${loop.index}'`, OutputVar: "cursor"}
				body := []Step{barrier, advisory, output}
				if boundary != "between_iterations" {
					body = []Step{advisory, barrier, output}
				}
				loop := &LoopConfig{Collect: "outputs", MaxIterations: IntOrExpr{Value: 3}, Steps: body}
				switch kind {
				case "items":
					loop.Items = `["a","b","c"]`
				case "while":
					loop.While = `${vars.cursor} != "2"`
					loop.MaxIterations = IntOrExpr{Expr: "${vars.cap}"}
					e.state.Variables["cap"] = 3
				case "times":
					loop.Times = 3
				case "until":
					loop.Until = `[ '${loop.index}' -ge 2 ]`
				}
				workflow := &Workflow{
					SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
					Steps: []Step{{ID: "repeat", Loop: loop}},
				}
				e.graph = NewDependencyGraph(workflow)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan LoopResult, 1)
				go func() {
					done <- e.loopExec.ExecuteLoop(ctx, &workflow.Steps[0], workflow)
				}()
				deadline := time.After(10 * time.Second)
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
			waitForBarrier:
				for {
					if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "blocked")); err == nil {
						break
					}
					select {
					case result := <-done:
						t.Fatalf("loop stopped before interruption boundary: %+v", result)
					case <-deadline:
						cancel()
						<-done
						t.Fatal("loop did not reach interruption boundary")
					case <-ticker.C:
						continue waitForBarrier
					}
				}
				cancel()
				if result := <-done; result.Status != StatusCancelled {
					t.Fatalf("interrupted loop = %+v; want cancelled", result)
				}
				prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
				if err != nil {
					t.Fatal(err)
				}
				progress := prior.ForeachState["repeat"]
				wantConsumed := 1
				if boundary != "between_iterations" {
					wantConsumed = 2
				}
				if len(progress.CompletedIterationIDs) != 1 || len(progress.CollectedOutputs) != 1 ||
					len(progress.ConsumedFailures) != wantConsumed {
					t.Fatalf("lost completed or partial iteration evidence: %+v", progress)
				}
				if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "resume"), []byte("ready"), 0600); err != nil {
					t.Fatal(err)
				}
				if boundary == "lower_bound" {
					prior.Variables["cap"] = 1
				}
				final, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
				if boundary == "lower_bound" {
					if err == nil || final == nil || final.Status != StatusFailed ||
						final.Steps["repeat"].Error == nil || !strings.Contains(final.Steps["repeat"].Error.Message, "unfinished consumed iteration") {
						t.Fatalf("lowered bound silently discarded admitted work: %+v, %v", final, err)
					}
					if got := final.ForeachState["repeat"]; got.Total != progress.Total || got.CurrentIteration != progress.CurrentIteration {
						t.Fatalf("lowered bound rewrote progress before refusal: %+v", got)
					}
					data, readErr := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects"))
					if readErr != nil || string(data) != "0\n1\n" {
						t.Fatalf("bound rejection repeated effects: %q, %v", data, readErr)
					}
					return
				}
				if err != nil || final.Status != StatusCompleted {
					t.Fatalf("resume = %+v, %v", final, err)
				}
				data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects"))
				if err != nil || string(data) != "0\n1\n2\n" {
					t.Fatalf("accepted effects repeated: %q, %v", data, err)
				}
				if got := final.Variables["outputs"]; !reflect.DeepEqual(got, []interface{}{"0", "1", "2"}) {
					t.Fatalf("collected outputs duplicated or lost: %#v", got)
				}
				for i := 0; i < 3; i++ {
					id := fmt.Sprintf("repeat_iter%d_advisory", i)
					if result := final.Steps[id]; result.Status != StatusFailed || result.ConsumedByLoop != "repeat" {
						t.Fatalf("accepted failure diagnostics lost for %s: %+v", id, result)
					}
				}
				if _, err := LoadState(e.config.ProjectDir, e.state.RunID); err != nil {
					t.Fatalf("final checkpoint cannot round-trip: %v", err)
				}
			})
		}
	}
}

// Complete the body and its consumption checkpoints without publishing the
// parent result, reproducing the crash window before scheduler settlement.
func completedLoopFailureFixture(t *testing.T, inherited bool) (*Executor, *Workflow, *ExecutionState) {
	t.Helper()
	e := newCommandTestExecutor(t)
	e.state.Status, e.state.Session = StatusRunning, e.config.Session
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "repeat", Loop: &LoopConfig{
			Times: 2, MaxIterations: IntOrExpr{Value: 2}, Collect: "outputs",
			Steps: []Step{
				{ID: "advisory", Command: `printf '%s\n' '${loop.index}' >> effects; exit 7`, OnError: ErrorActionContinue},
				{ID: "output", Command: `printf '%s' '${loop.index}'`},
			},
		}}},
	}
	if inherited {
		workflow.Settings.OnError = ErrorActionContinue
		workflow.Steps[0].Loop.Steps[0].OnError = ""
	}
	e.graph = NewDependencyGraph(workflow)
	result := e.loopExec.ExecuteLoop(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("fixture loop failed: %+v", result)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return e, workflow, prior
}

func TestConsumedLoopFailureStateSchemaCompatibility(t *testing.T) {
	t.Parallel()
	_, _, prior := completedLoopFailureFixture(t, false)
	data, err := marshalStateForDisk(prior)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Version int `json:"state_schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	if header.Version != PipelineStateSchemaVersion || header.Version < 2 {
		t.Fatalf("consumption receipts allow an unsafe older reader: version=%d", header.Version)
	}
	legacy := []byte(`{"state_schema_version":1,"run_id":"legacy-loop","steps":{},"variables":{}}`)
	state, err := decodeState(legacy, "legacy-loop")
	if err != nil || state.RunID != "legacy-loop" {
		t.Fatalf("legacy state cannot be read: %+v, %v", state, err)
	}
}

func TestConsumedLoopFailureRejectsChangedEvidenceBeforeResume(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"result", "marker", "ledger", "native_missing", "native_unsettled", "policy", "definition", "loop_definition"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			e, workflow, prior := completedLoopFailureFixture(t, true)
			const id = "repeat_iter0_advisory"
			switch change {
			case "result":
				result := prior.Steps[id]
				result.Output = "changed"
				prior.Steps[id] = result
			case "marker":
				result := prior.Steps[id]
				result.ConsumedByLoop = ""
				prior.Steps[id] = result
			case "ledger":
				progress := prior.ForeachState["repeat"]
				delete(progress.ConsumedFailures, id)
				prior.ForeachState["repeat"] = progress
			case "native_missing":
				delete(prior.CommandExecutions, id)
			case "native_unsettled":
				record := prior.CommandExecutions[id]
				record.Status, record.SettledAt, record.Result = commandExecutionRunning, time.Time{}, StepResult{}
				prior.CommandExecutions[id] = record
			case "policy":
				workflow.Settings.OnError = ErrorActionFail
			case "definition":
				workflow.Steps[0].Loop.Steps[0].Command = "printf changed"
			case "loop_definition":
				workflow.Steps[0].Loop.Times = 0
				workflow.Steps[0].Loop.While = "false"
			}
			before, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			disk, err := os.ReadFile(pipelineStatePath(e.config.ProjectDir, prior.RunID))
			if err != nil {
				t.Fatal(err)
			}
			e.state, e.graph = prior, NewDependencyGraph(workflow)
			err = e.applyResumeOptions(workflow, ResumeOptions{Mode: ResumeModeContinue, KeepState: true})
			if err == nil {
				t.Fatal("resume accepted changed consumption evidence behind completed iterations")
			}
			after, marshalErr := json.Marshal(prior)
			if marshalErr != nil || string(before) != string(after) {
				t.Fatalf("rejected resume changed in-memory evidence: %v", marshalErr)
			}
			current, readErr := os.ReadFile(pipelineStatePath(e.config.ProjectDir, prior.RunID))
			if readErr != nil || string(disk) != string(current) {
				t.Fatalf("rejected resume rewrote its checkpoint: %v", readErr)
			}
		})
	}
}

func TestConsumedLoopFailureRoundTripAndScopedReset(t *testing.T) {
	t.Parallel()
	e, workflow, prior := completedLoopFailureFixture(t, false)
	const id = "repeat_iter0_advisory"
	// Typed in-memory parsed data must hash identically after JSON decoding.
	result := prior.Steps[id]
	result.ParsedData = []int{1, 2}
	prior.Steps[id] = result
	progress := prior.ForeachState["repeat"]
	receipt := progress.ConsumedFailures[id]
	var err error
	receipt.ResultHash, err = parallelCheckpointHash(result)
	if err != nil {
		t.Fatal(err)
	}
	progress.ConsumedFailures[id] = receipt
	prior.ForeachState["repeat"] = progress
	if err := SaveState(e.config.ProjectDir, prior); err != nil {
		t.Fatal(err)
	}
	prior, err = LoadState(e.config.ProjectDir, prior.RunID)
	if err != nil {
		t.Fatalf("typed result failed checkpoint round-trip: %v", err)
	}
	e.state = prior
	if err := e.validateLeafReplayScopeLocked(workflow, prior, id, StepKindCommand); err == nil {
		t.Fatal("accepted child can be replayed independently of its iteration")
	}
	e.forceResumeIteration("repeat", 1)
	if len(e.state.ForeachState["repeat"].ConsumedFailures) != 1 {
		t.Fatal("force-iter did not preserve only the earlier consumption receipt")
	}
	if _, exists := e.state.Steps["repeat_iter1_advisory"]; exists {
		t.Fatal("force-iter retained a pruned result marker")
	}
	if err := validateConsumedLoopFailures(e.state); err != nil {
		t.Fatal(err)
	}
	resultLoop := e.loopExec.ExecuteLoop(context.Background(), &workflow.Steps[0], workflow)
	if resultLoop.Status != StatusCompleted {
		t.Fatalf("force-iter could not replay its scoped suffix: %+v", resultLoop)
	}
	data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects"))
	if err != nil || string(data) != "0\n1\n1\n" {
		t.Fatalf("force-iter repeated wrong effects: %q, %v", data, err)
	}
	e.resetResumeState(workflow)
	if len(e.state.ForeachState) != 0 || len(e.state.Steps) != 0 {
		t.Fatal("whole reset left consumption evidence")
	}
}

func TestLoopRejectedFailureDoesNotAdvanceIteration(t *testing.T) {
	t.Parallel()
	e := newCommandTestExecutor(t)
	e.state.Status, e.state.Session = StatusRunning, e.config.Session
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "repeat", Loop: &LoopConfig{
			Times: 2, MaxIterations: IntOrExpr{Value: 2},
			Steps: []Step{
				{ID: "strict", Command: "printf 'attempt\\n' >> effects; exit 7", OnError: ErrorActionFail},
				{ID: "tail", Command: "printf unexpected > tail"},
			},
		}}},
	}
	e.graph = NewDependencyGraph(workflow)
	result := e.loopExec.ExecuteLoop(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || len(e.state.ForeachState["repeat"].CompletedIterationIDs) != 0 ||
		len(e.state.ForeachState["repeat"].ConsumedFailures) != 0 {
		t.Fatalf("unaccepted failure advanced the loop: %+v", result)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	final, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
	if err == nil || final == nil || final.Status != StatusFailed {
		t.Fatalf("strict failure was accepted by resume: %+v, %v", final, err)
	}
	data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "effects"))
	if err != nil || strings.Count(string(data), "attempt\n") != 2 {
		t.Fatalf("unfinished iteration was not retried: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "tail")); !os.IsNotExist(err) {
		t.Fatalf("strict failure dispatched its tail: %v", err)
	}
}

func TestConsumedLoopFailureRestoresCompletedDescendantOutputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind          string
		trimVariables bool
	}{{"times", false}, {"times", true}, {"while", false}, {"while", true}} {
		t.Run(fmt.Sprintf("%s_trim_variables_%t", tc.kind, tc.trimVariables), func(t *testing.T) {
			t.Parallel()
			e := newCommandTestExecutor(t)
			e.state.Status, e.state.Session = StatusRunning, e.config.Session
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
				Steps: []Step{
					{ID: "init", Command: "printf old", OutputVar: "shared"},
					{ID: "repeat", DependsOn: []string{"init"}, Loop: &LoopConfig{
						Times: 1, MaxIterations: IntOrExpr{Value: 1},
						Steps: []Step{
							{ID: "nested", OnError: ErrorActionContinue, Loop: &LoopConfig{
								Times: 1, MaxIterations: IntOrExpr{Value: 1},
								Steps: []Step{
									{ID: "fresh", Command: "printf new", OutputVar: "shared"},
									{ID: "strict", Command: "printf 'once\\n' >> effects; exit 7", OnError: ErrorActionFail},
								},
							}},
							{ID: "barrier", Command: "if [ ! -f resume ]; then printf reached > blocked; sleep 30; fi"},
							{ID: "tail", Command: `printf '%s\n' '${vars.shared}' >> observed`},
						},
					}},
				},
			}
			if tc.kind == "while" {
				workflow.Steps[1].Loop.Times = 0
				workflow.Steps[1].Loop.While = `${vars.shared} != "new"`
				workflow.Steps[1].Loop.MaxIterations.Value = 2
			}
			e.graph = NewDependencyGraph(workflow)
			initial := e.executeStep(context.Background(), &workflow.Steps[0], workflow)
			if initial.Status != StatusCompleted {
				t.Fatalf("initial output producer failed: %+v", initial)
			}
			e.state.Steps["init"] = initial
			// This fixture invokes the loop directly to capture its exact
			// interruption boundary; publish init as the main scheduler does.
			storeOutputVar(e.state, "shared", initial.Output, initial.ParsedData)
			if err := e.graph.MarkExecuted("init"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan LoopResult, 1)
			go func() {
				done <- e.loopExec.ExecuteLoop(ctx, &workflow.Steps[1], workflow)
			}()
			deadline := time.After(10 * time.Second)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "blocked")); err == nil {
					break
				}
				select {
				case result := <-done:
					t.Fatalf("nested failure stopped before barrier: %+v", result)
				case <-deadline:
					cancel()
					<-done
					t.Fatal("nested loop did not reach interruption boundary")
				case <-ticker.C:
				}
			}
			cancel()
			if result := <-done; result.Status != StatusCancelled {
				t.Fatalf("loop did not preserve partial iteration: %+v", result)
			}
			prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if prior.Variables["shared"] != "new" {
				t.Fatalf("fixture did not replace the old output: %#v", prior.Variables["shared"])
			}
			if tc.trimVariables {
				prior.Variables = make(map[string]interface{})
			}
			if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "resume"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
			if err != nil || final.Status != StatusCompleted {
				t.Fatalf("resume failed to restore nested output: %+v, %v", final, err)
			}
			for name, want := range map[string]string{"observed": "new\n", "effects": "once\n"} {
				data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s after resume = %q, %v; want %q", name, data, err, want)
				}
			}
			if result := final.Steps["repeat_iter0_nested"]; result.Status != StatusFailed || result.ConsumedByLoop != "repeat" {
				t.Fatalf("container failure diagnostics were promoted or discarded: %+v", result)
			}
		})
	}
}

func TestLoopDoesNotConsumeFailureWithRunningDescendant(t *testing.T) {
	t.Parallel()
	e := newCommandTestExecutor(t)
	e.state.Status, e.state.Session = StatusRunning, e.config.Session
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
		Steps: []Step{{ID: "repeat", Loop: &LoopConfig{
			Times: 1, MaxIterations: IntOrExpr{Value: 1},
			Steps: []Step{
				{ID: "nested", OnError: ErrorActionContinue, Loop: &LoopConfig{
					Times: 1, MaxIterations: IntOrExpr{Value: 1},
					Steps: []Step{
						{ID: "sidecar", Command: "sleep 30", Wait: WaitNone},
						{ID: "strict", Command: "exit 7", OnError: ErrorActionFail},
					},
				}},
				{ID: "tail", Command: "printf unexpected > tail"},
			},
		}}},
	}
	e.graph = NewDependencyGraph(workflow)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		e.backgroundCommandWG.Wait()
	})
	result := e.loopExec.ExecuteLoop(ctx, &workflow.Steps[0], workflow)
	if result.Status != StatusFailed || result.Error == nil || result.Error.Type != "recovery" {
		t.Fatalf("running descendant was consumed with its failed container: %+v", result)
	}
	if len(e.state.ForeachState["repeat"].ConsumedFailures) != 0 ||
		len(e.state.ForeachState["repeat"].CompletedIterationIDs) != 0 {
		t.Fatal("unsettled container advanced its enclosing iteration")
	}
	if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "tail")); !os.IsNotExist(err) {
		t.Fatalf("unsettled container released tail effects: %v", err)
	}
	const sidecarID = "repeat_iter0_nested_iter0_sidecar"
	record, exists := e.loadCommandExecution(sidecarID)
	if !exists || record.Status != commandExecutionRunning || !record.Background {
		t.Fatalf("live descendant receipt was lost or changed: %+v", record)
	}
}

func TestLoopConsumesProvenPredispatchFailure(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"command", "template"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e := newCommandTestExecutor(t)
			e.state.Status, e.state.Session = StatusRunning, e.config.Session
			child := Step{ID: "advisory", OnError: ErrorActionContinue}
			if kind == "command" {
				child.Command = "echo ${vars.undefined}"
			} else {
				child.Template = "missing-template.md"
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(),
				Steps: []Step{{ID: "repeat", Loop: &LoopConfig{
					Times: 1, MaxIterations: IntOrExpr{Value: 1}, Steps: []Step{child},
				}}},
			}
			e.graph = NewDependencyGraph(workflow)
			result := e.loopExec.ExecuteLoop(context.Background(), &workflow.Steps[0], workflow)
			if result.Status != StatusCompleted {
				t.Fatalf("proven predispatch failure blocked continue: %+v", result)
			}
			prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
			if err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(e.config).Resume(context.Background(), workflow, prior, nil)
			if err != nil || final.Status != StatusCompleted || final.Steps["repeat_iter0_advisory"].Status != StatusFailed {
				t.Fatalf("predispatch proof did not survive resume: %+v, %v", final, err)
			}
		})
	}
}
