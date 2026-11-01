//go:build unix

package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func decisionTestExecutor(t *testing.T, step Step) (*Executor, *Workflow, ExecutorConfig) {
	t.Helper()
	cfg := DefaultExecutorConfig("branch-checkpoint")
	cfg.ProjectDir, cfg.RunID = t.TempDir(), "run-branch-checkpoint"
	cfg.GlobalTimeout, cfg.DefaultTimeout = 5*time.Second, 3*time.Second
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "branch-checkpoint", Steps: []Step{step}}
	e := NewExecutor(cfg)
	e.graph = NewDependencyGraph(workflow)
	e.state = &ExecutionState{RunID: cfg.RunID, WorkflowID: workflow.Name, Session: cfg.Session, Status: StatusRunning,
		Steps: make(map[string]StepResult), Variables: make(map[string]interface{})}
	e.limits = workflow.Settings.Limits.EffectiveLimits()
	return e, workflow, cfg
}

func decisionTestStep() Step {
	return Step{ID: "route", Branch: "$(printf x >> selections; cat choice)", Branches: map[string]interface{}{
		"a": []Step{{ID: "work", Command: "test -f release && printf original > marker"}},
		"b": []Step{{ID: "work", Command: "printf wrong > wrong-branch"}},
	}}
}

func decisionTestWrite(t *testing.T, root, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBranchDecisionIsDurableBeforeChildDispatch(t *testing.T) {
	step := decisionTestStep()
	step.Branches["a"] = []Step{{ID: "work", Command: "grep -q 'branch_decision' .ntm/pipelines/run-branch-checkpoint.json && printf child > marker"}}
	_, workflow, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
	st, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || st == nil || st.Status != StatusCompleted {
		t.Fatalf("controlled branch: %+v %v", st, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "marker"))
	if err != nil || string(data) != "child" {
		t.Fatalf("child did not observe its saved decision: %q %v", data, err)
	}
	checkpoint, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := decodeBranchDecision(branchDecisionID("route"), checkpoint.Steps[branchDecisionID("route")])
	if err != nil || decision.BranchKey != "a" {
		t.Fatalf("missing decision: %+v %v", decision, err)
	}
}

func TestBranchDecisionBinarySelectorKeepsRawDefaultAcrossResume(t *testing.T) {
	raw := []byte{'x', 0, 0xff, 0xfe, '\n'}
	step := Step{ID: "route", Branch: "$(printf x >> selections; cat choice)", Branches: map[string]interface{}{
		"default": []Step{{ID: "work", Command: "printf x >> body-attempts; test -f release && printf original > marker"}},
		// Neither per-byte JSON replacement nor grouped UTF-8 replacement
		// may turn an unmatched raw selector into an authored alias branch.
		"x\x00\ufffd\ufffd": []Step{{ID: "work", Command: "printf wrong > wrong-branch"}},
		"x\x00\ufffd":       []Step{{ID: "work", Command: "printf wrong > wrong-branch"}},
	}}
	_, workflow, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", string(raw))
	first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || first == nil || first.Status != StatusFailed || first.Steps["route_work"].Error == nil {
		t.Fatalf("fixture did not fail inside the selected default body: %+v %v", first, err)
	}
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatalf("binary selector could not checkpoint a decision: %v", err)
	}
	decisionID := branchDecisionID(step.ID)
	decision, err := decodeBranchDecision(decisionID, prior.Steps[decisionID])
	if err != nil || decision.BranchKey != "default" || !utf8.ValidString(decision.SelectorKey) ||
		prior.Steps[decisionID].Output != decision.SelectorKey {
		t.Fatalf("binary selector lost its raw-match decision or stable display: %+v %v", decision, err)
	}
	record := prior.CommandEvaluations[step.ID]
	wantHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if record.Status != commandExecutionSettled || !bytes.Equal(record.Output, raw) || record.OutputHash != wantHash {
		t.Fatalf("textual decision normalization changed raw source evidence: %+v", record)
	}
	for name, want := range map[string]string{"selections": "x", "body-attempts": "x"} {
		if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); err != nil || string(data) != want {
			t.Fatalf("first %s=%q want %q: %v", name, data, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "wrong-branch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("display normalization changed original branch matching: %v", err)
	}

	// A fresh source would now select the replacement-character alias. Resume
	// must retain the original default and retry only its settled failed body.
	decisionTestWrite(t, cfg.ProjectDir, "choice", "x\x00\ufffd\ufffd")
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	frozen, validation, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !validation.Valid {
		t.Fatalf("load original frozen branch: %+v %v", validation, err)
	}
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume failed to retain the binary selector's default: %+v %v", resumed, err)
	}
	if kept := resumed.CommandEvaluations[step.ID]; !kept.StartedAt.Equal(record.StartedAt) || !bytes.Equal(kept.Output, raw) || kept.OutputHash != wantHash {
		t.Fatalf("body retry changed or repeated its completed source: %+v", kept)
	}
	for name, want := range map[string]string{"selections": "x", "body-attempts": "xx", "marker": "original"} {
		if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); err != nil || string(data) != want {
			t.Fatalf("resumed %s=%q want %q: %v", name, data, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "wrong-branch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume selected a normalized alias instead of saved default: %v", err)
	}
	final, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := decodeBranchDecision(decisionID, final.Steps[decisionID])
	if err != nil || reloaded.BranchKey != "default" || reloaded.SelectorKey != decision.SelectorKey ||
		!bytes.Equal(final.CommandEvaluations[step.ID].Output, raw) || final.CommandEvaluations[step.ID].OutputHash != wantHash {
		t.Fatalf("final JSON reload changed the selected branch or raw evidence: %+v %v", reloaded, err)
	}
}

func TestBranchChildHashMatchesDurableJSONRepresentation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	result := StepResult{
		StepID: "route_fanout", Status: StatusCompleted, Output: "done",
		StartedAt: now, FinishedAt: now,
		ParsedData: map[string]interface{}{
			"ratio": json.Number("1.0"),
			"nested": []interface{}{map[string]interface{}{"score": json.Number("2.50")}},
		},
	}
	outputs := map[string]interface{}{
		"collected_parsed": map[string]interface{}{"ratio": json.Number("1.0")},
	}
	liveHash, err := branchChildHash(result, outputs)
	if err != nil {
		t.Fatal(err)
	}

	// Model the exact representation change made by ExecutionState JSON
	// persistence: interface-backed numbers become ordinary float64 values.
	encoded, err := json.Marshal(struct {
		Result  StepResult             `json:"result"`
		Outputs map[string]interface{} `json:"outputs,omitempty"`
	}{result, outputs})
	if err != nil {
		t.Fatal(err)
	}
	var durable struct {
		Result  StepResult             `json:"result"`
		Outputs map[string]interface{} `json:"outputs,omitempty"`
	}
	if err := json.Unmarshal(encoded, &durable); err != nil {
		t.Fatal(err)
	}
	durableHash, err := branchChildHash(durable.Result, durable.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	if liveHash != durableHash {
		t.Fatalf("branch child hash changed across durable JSON representation: live=%s durable=%s", liveHash, durableHash)
	}
}

func TestBranchDecisionReusesSourceSuccessBeforeAdmissionCheckpoint(t *testing.T) {
	step := decisionTestStep()
	e, workflow, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
	key, err := e.resolveBranch(context.Background(), &step)
	if err != nil || key != "a" {
		t.Fatalf("source completion: %q %v", key, err)
	}
	// Simulate owner loss after the command's success checkpoint but before
	// prepareBranchDecision has admitted the key or dispatched any child.
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := prior.Steps[branchDecisionID(step.ID)]; exists {
		t.Fatal("fixture passed the intended pre-admission crash boundary")
	}
	if record := prior.CommandEvaluations[step.ID]; record.Status != commandExecutionSettled || string(record.Output) != "a" {
		t.Fatalf("completed source output was not durable: %+v", record)
	}
	decisionTestWrite(t, cfg.ProjectDir, "choice", "b")
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), workflow, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume from saved source output: %+v %v", resumed, err)
	}
	for name, want := range map[string]string{"selections": "x", "marker": "original"} {
		data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q want %q: %v", name, data, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "wrong-branch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume admitted a fresh selector result: %v", err)
	}
}

func TestBranchDecisionRestartFailedPreservesAdmittedSource(t *testing.T) {
	for _, scenario := range []string{"failed body", "unrelated failure"} {
		t.Run(scenario, func(t *testing.T) {
			step := decisionTestStep()
			step.Branches["a"] = []Step{{ID: "work", Command: "printf x >> body-attempts; test -f release && printf original > marker"}}
			_, workflow, cfg := decisionTestExecutor(t, step)
			decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
			wantBodyAttempts := "xx"
			if scenario == "unrelated failure" {
				decisionTestWrite(t, cfg.ProjectDir, "release", "go")
				workflow.Steps = append(workflow.Steps, Step{ID: "unrelated", DependsOn: []string{step.ID},
					Command: "printf x >> other-attempts; test -f other-release"})
				wantBodyAttempts = "x"
			}
			first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || first == nil || first.Status != StatusFailed {
				t.Fatalf("fixture did not stop at its intended failure: %+v %v", first, err)
			}
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			record := prior.CommandEvaluations[step.ID]
			if record.Status != commandExecutionSettled {
				t.Fatalf("source did not retain successful output: %+v", record)
			}
			decisionTestWrite(t, cfg.ProjectDir, "choice", "b")
			decisionTestWrite(t, cfg.ProjectDir, "release", "go")
			decisionTestWrite(t, cfg.ProjectDir, "other-release", "go")
			owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			cfg.WorkflowFile = prior.WorkflowFile
			frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
			if err != nil || !valid.Valid {
				t.Fatalf("recovery definition: %v %+v", err, valid)
			}
			resumed, err := NewExecutor(cfg).ResumeWithOptions(owner.Context(), frozen, prior, ResumeOptions{Mode: ResumeModeRestartFailed}, nil)
			if err != nil || resumed == nil || resumed.Status != StatusCompleted {
				t.Fatalf("restart-failed could not resume the failed work: %+v %v", resumed, err)
			}
			if kept := resumed.CommandEvaluations[step.ID]; kept.Status != commandExecutionSettled || !kept.StartedAt.Equal(record.StartedAt) || kept.OutputHash != record.OutputHash {
				t.Fatalf("restart-failed discarded unrelated or admitted source evidence: %+v", kept)
			}
			for name, want := range map[string]string{"selections": "x", "body-attempts": wantBodyAttempts, "marker": "original"} {
				data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q want %q: %v", name, data, want, err)
				}
			}
			if scenario == "unrelated failure" {
				if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "other-attempts")); err != nil || string(data) != "xx" {
					t.Fatalf("restart-failed did not retry the selected failed command: %q %v", data, err)
				}
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "wrong-branch")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restart-failed reevaluated the admitted branch: %v", err)
			}
		})
	}
}

func TestBranchDecisionRestartFailedRefreshesUnadmittedSource(t *testing.T) {
	step := decisionTestStep()
	_, workflow, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", "unknown")
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || first == nil || first.Status != StatusFailed {
		t.Fatalf("fixture admitted an unmatched branch: %+v %v", first, err)
	}
	decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := prior.Steps[branchDecisionID(step.ID)]; exists {
		t.Fatal("unmatched source unexpectedly acquired an admission checkpoint")
	}
	cfg.WorkflowFile = prior.WorkflowFile
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("recovery definition: %v %+v", err, valid)
	}
	ordinary, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err == nil || ordinary == nil || ordinary.Status != StatusFailed || !strings.Contains(err.Error(), "no matching key: unknown") {
		t.Fatalf("ordinary resume discarded the original unusable result: %+v %v", ordinary, err)
	}
	if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "selections")); err != nil || string(data) != "x" {
		t.Fatalf("ordinary resume repeated successful source effects: %q %v", data, err)
	}
	prior, err = LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewExecutor(cfg).ResumeWithOptions(owner.Context(), frozen, prior, ResumeOptions{Mode: ResumeModeRestartFailed}, nil)
	if err != nil || restarted == nil || restarted.Status != StatusCompleted {
		t.Fatalf("explicit replay failed to refresh unadmitted source output: %+v %v", restarted, err)
	}
	for name, want := range map[string]string{"selections": "xx", "marker": "original"} {
		if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q want %q: %v", name, data, want, err)
		}
	}
}

func TestBranchDecisionResumeDoesNotReevaluateChangedSelector(t *testing.T) {
	_, workflow, cfg := decisionTestExecutor(t, decisionTestStep())
	decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
	first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err == nil || first == nil || first.Status != StatusFailed {
		t.Fatalf("expected blocked first attempt: %+v %v", first, err)
	}
	decisionTestWrite(t, cfg.ProjectDir, "choice", "b")
	decisionTestWrite(t, cfg.ProjectDir, "release", "go")
	owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
	if err != nil || !valid.Valid {
		t.Fatalf("recovery definition: %v %+v", err, valid)
	}
	cfg.WorkflowFile = prior.WorkflowFile
	resumed, err := NewExecutor(cfg).Resume(owner.Context(), frozen, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	for name, want := range map[string]string{"selections": "x", "marker": "original"} {
		data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q want %q: %v", name, data, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "wrong-branch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery selected a different branch: %v", err)
	}
}

func TestBranchDecisionDefaultRetainsStableAnonymousIdentity(t *testing.T) {
	step := Step{ID: "route", Branch: "$(printf x >> selections; cat choice)", Branches: map[string]interface{}{
		"default": []Step{{Command: "true"}},
	}}
	e, _, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", "unknown")
	first, body, err := e.prepareBranchDecision(context.Background(), &step)
	if err != nil || first.BranchKey != "default" || first.SelectorKey != "unknown" || len(body) != 1 || body[0].ID != "route.default.0" {
		t.Fatalf("default identity: %+v %+v %v", first, body, err)
	}
	decisionTestWrite(t, cfg.ProjectDir, "choice", "different")
	fresh := NewExecutor(cfg)
	fresh.state, err = LoadState(cfg.ProjectDir, cfg.RunID)
	if err != nil {
		t.Fatal(err)
	}
	second, next, err := fresh.prepareBranchDecision(context.Background(), &step)
	if err != nil || !sameBranchDecision(first, second) || len(next) != 1 || body[0].ID != next[0].ID {
		t.Fatalf("default changed after reload: %+v %+v %v", second, next, err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "selections"))
	if err != nil || string(data) != "x" {
		t.Fatalf("default selector reran: %q %v", data, err)
	}
}

func TestBranchDecisionRejectsDefinitionChangesWithoutRewritingEvidence(t *testing.T) {
	step := decisionTestStep()
	e, _, cfg := decisionTestExecutor(t, step)
	decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
	if _, _, err := e.prepareBranchDecision(context.Background(), &step); err != nil {
		t.Fatal(err)
	}
	path := pipelineStatePath(cfg.ProjectDir, cfg.RunID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	step.Branches["a"] = []Step{{ID: "changed", Command: "printf changed"}}
	if _, _, err := e.prepareBranchDecision(context.Background(), &step); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatalf("definition drift accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("rejection rewrote recovery evidence: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "selections"))
	if err != nil || string(data) != "x" {
		t.Fatalf("changed definition re-ran selector: %q %v", data, err)
	}
}

func TestBranchDecisionStorageFailurePreventsChildEffects(t *testing.T) {
	step := Step{ID: "route", Branch: "a", Branches: map[string]interface{}{
		"a": []Step{{ID: "work", Command: "printf unsafe > marker"}},
	}}
	e, workflow, cfg := decisionTestExecutor(t, step)
	// Isolate the decision save itself. Shell selectors now checkpoint their
	// launch intent first, so a shell-created obstruction would test a
	// different persistence boundary (or fail to obstruct an existing file).
	if err := os.MkdirAll(pipelineStatePath(cfg.ProjectDir, cfg.RunID), 0700); err != nil {
		t.Fatal(err)
	}
	result := e.executeBranch(context.Background(), &step, workflow)
	if result.Status != StatusFailed || !errors.Is(e.checkpointFailure(), ErrCheckpointFailed) {
		t.Fatalf("decision save failure was ignored: %+v %v", result, e.checkpointFailure())
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child ran without a durable decision: %v", err)
	}
}

func TestBranchDecisionRejectsMalformedStateBeforeResumePrunesIt(t *testing.T) {
	for _, damage := range []string{"status", "type", "version", "identity", "hash", "output", "timestamp"} {
		t.Run(damage, func(t *testing.T) {
			step := decisionTestStep()
			e, _, cfg := decisionTestExecutor(t, step)
			decisionTestWrite(t, cfg.ProjectDir, "choice", "a")
			if _, _, err := e.prepareBranchDecision(context.Background(), &step); err != nil {
				t.Fatal(err)
			}
			id := branchDecisionID(step.ID)
			record := e.state.Steps[id]
			metadata := record.ParsedData.(map[string]interface{})
			switch damage {
			case "status":
				record.Status = StatusFailed
			case "type":
				record.AgentType = "claude"
			case "version":
				metadata["version"] = 999
			case "identity":
				metadata["parent_id"] = "unrelated"
			case "hash":
				metadata["definition_hash"] = "bad"
			case "output":
				record.Output = "forged"
			case "timestamp":
				record.FinishedAt = time.Time{}
			}
			e.state.Steps[id] = record
			if err := SaveState(cfg.ProjectDir, e.state); err == nil {
				t.Fatal("saved a malformed internal decision")
			}
			raw, err := json.Marshal(e.state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pipelineStatePath(cfg.ProjectDir, cfg.RunID), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadState(cfg.ProjectDir, cfg.RunID); err == nil || !strings.Contains(err.Error(), "branch decision") {
				t.Fatalf("loader accepted damaged recovery evidence: %v", err)
			}
		})
	}
}

func TestBranchDecisionRequiresChoiceEvidenceForLegacyChildWork(t *testing.T) {
	for _, evidence := range []string{"result", "inflight", "command", "delivery", "foreach", "parallel"} {
		t.Run(evidence, func(t *testing.T) {
			step := decisionTestStep()
			e, _, cfg := decisionTestExecutor(t, step)
			decisionTestWrite(t, cfg.ProjectDir, "choice", "b")
			id := "route_work"
			switch evidence {
			case "result":
				e.state.Steps[id] = StepResult{StepID: id, Status: StatusCompleted}
			case "inflight":
				e.state.InFlightSteps = map[string]InFlightStepState{id: {StepID: id}}
			case "command":
				e.state.CommandExecutions = map[string]CommandExecutionState{id: {}}
			case "delivery":
				e.state.AgentDeliveries = map[string]AgentDeliveryState{id: {}}
			case "foreach":
				e.state.ForeachState = map[string]ForeachIterationState{id: {StepID: id}}
			case "parallel":
				e.state.ParallelState = map[string]ParallelGroupState{id: {StepID: id}}
			}
			if _, _, err := e.prepareBranchDecision(context.Background(), &step); err == nil || !strings.Contains(err.Error(), "no saved decision") {
				t.Fatalf("legacy progress guessed a path: %v", err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "selections")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("selector ran before legacy evidence was checked: %v", err)
			}
		})
	}
}

func TestBranchDecisionResolvesOnlyWithinItsActualRuntimeScope(t *testing.T) {
	branch := Step{ID: "route", Branch: "a", Branches: map[string]interface{}{"a": []Step{{ID: "leaf", Command: "true"}}}}
	workflow := &Workflow{Steps: []Step{
		branch,
		{ID: "parallel", Parallel: ParallelSpec{Steps: []Step{{ID: "nested", Branch: branch.Branch, Branches: branch.Branches}}}},
		{ID: "loop", Loop: &LoopConfig{Steps: []Step{branch}}},
		{ID: "foreach", Foreach: &ForeachConfig{Steps: []Step{branch}}},
	}, PostPipelineSteps: []Step{{ID: "post", Branch: branch.Branch, Branches: branch.Branches}}}
	graph := NewDependencyGraph(workflow)
	for _, parent := range []string{"route", "parallel_nested", "loop_iter2_route", "foreach_iter3_route", "post"} {
		id := branchDecisionID(parent)
		resolved, canonical, ok := graph.ResolveScopedRuntimeStep(id)
		if !ok || resolved.ID != id || canonical != id || resolved.Branch == "" || resolved.OutputVar != "" {
			t.Fatalf("decision lost its exact scope: %q %+v %q %v", id, resolved, canonical, ok)
		}
		if graph.IsExecuted(parent) {
			t.Fatalf("retaining %q marked its incomplete body complete", id)
		}
	}
	for _, parent := range []string{"missing", "parallel", "foreach_iter-1_route", "foreach_iter03_route", "route_leaf"} {
		if _, _, ok := graph.ResolveScopedRuntimeStep(branchDecisionID(parent)); ok {
			t.Fatalf("invented decision owner %q", parent)
		}
	}
}

func TestBranchDecisionRejectsInvalidBodyBeforePublishing(t *testing.T) {
	for i, body := range []interface{}{[]Step{}, []Step{{ID: "same", Command: "true"}, {ID: "same", Command: "true"}}, []Step{{ID: "route", Command: "true"}}} {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			step := Step{ID: "route", Branch: "a", Branches: map[string]interface{}{"a": body}}
			e, _, _ := decisionTestExecutor(t, step)
			if _, _, err := e.prepareBranchDecision(context.Background(), &step); err == nil {
				t.Fatal("accepted ambiguous branch body")
			}
			if _, exists := e.state.Steps[branchDecisionID(step.ID)]; exists {
				t.Fatal("published decision for a malformed body")
			}
		})
	}
}

func TestBranchDecisionDryRunDoesNotEvaluateOrWrite(t *testing.T) {
	_, workflow, cfg := decisionTestExecutor(t, decisionTestStep())
	cfg.DryRun = true
	state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
	if err != nil || state == nil || state.Status != StatusCompleted {
		t.Fatalf("dry run: %+v %v", state, err)
	}
	if _, exists := state.Steps[branchDecisionID("route")]; exists {
		t.Fatal("dry run fabricated an evaluated decision")
	}
	for _, name := range []string{"selections", ".ntm"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry run created %s: %v", name, err)
		}
	}
}

// sameBranchDecision compares decisions the way a reload sees them: a nil and
// an empty Children/Scope map are the same decision (the JSON round trip of an
// omitempty map yields either).
func sameBranchDecision(a, b branchDecision) bool {
	if a.Version != b.Version || a.ParentID != b.ParentID || a.DefinitionHash != b.DefinitionHash ||
		a.SelectorKey != b.SelectorKey || a.BranchKey != b.BranchKey {
		return false
	}
	if (len(a.Children) != 0 || len(b.Children) != 0) && !reflect.DeepEqual(a.Children, b.Children) {
		return false
	}
	if (len(a.Scope) != 0 || len(b.Scope) != 0) && !reflect.DeepEqual(a.Scope, b.Scope) {
		return false
	}
	return true
}
