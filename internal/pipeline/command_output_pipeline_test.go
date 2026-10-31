//go:build !windows

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBranchPredicateCaptureRejectsOverflowBeforeDispatch(t *testing.T) {
	for _, script := range []string{"printf good-extra", "printf good; printf noisy-stderr >&2"} {
		e := newCommandTestExecutor(t)
		e.limits.MaxCommandStdoutBytes = 4
		e.limits.MaxCommandStderrBytes = 4
		step := &Step{ID: "choose", Branch: "$(" + script + ")", Branches: map[string]interface{}{
			"good":    Step{ID: "selected", Command: "printf dispatched > marker"},
			"default": Step{ID: "fallback", Command: "printf dispatched > marker"},
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		key, err := e.resolveBranch(ctx, step)
		if key != "" || !errors.Is(err, errCommandOutputLimit) {
			cancel()
			t.Fatalf("overflow produced usable branch key %q: %v", key, err)
		}
		result := e.executeBranch(ctx, step, &Workflow{Name: "capture-boundary"})
		cancel()
		if result.Status != StatusFailed || result.Error == nil || !strings.Contains(result.Error.Message, errCommandOutputLimit.Error()) {
			t.Fatalf("branch did not surface output rejection: %+v", result)
		}
		if _, err := os.Stat(filepath.Join(e.config.ProjectDir, "marker")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial or default branch was dispatched: %v", err)
		}
	}
}

func TestBranchPredicateCapturePreservesNormalSelection(t *testing.T) {
	e := newCommandTestExecutor(t)
	e.limits.MaxCommandStdoutBytes = 4
	e.limits.MaxCommandStderrBytes = 4
	step := &Step{ID: "choose", Branch: "$(printf good)", Branches: map[string]interface{}{
		"good":    Step{ID: "selected", Command: "printf yes > marker"},
		"default": Step{ID: "fallback", Command: "exit 99"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := e.executeBranch(ctx, step, &Workflow{Name: "capture-boundary"})
	if result.Status != StatusCompleted {
		t.Fatalf("valid branch failed: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, "marker"))
	if err != nil || string(data) != "yes" {
		t.Fatalf("wrong branch selected: %q %v", data, err)
	}
}

func TestControlledPipelineRejectsOversizedBranchBeforeDefault(t *testing.T) {
	root := t.TempDir()
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bounded-decision", Steps: []Step{
		{ID: "choose", Branch: "$(printf overflowing-key)", Branches: map[string]interface{}{
			"default": Step{ID: "must-not-run", Command: "printf dispatched > marker"},
		}},
	}}
	workflow.Settings.Limits.MaxCommandStdoutBytes = 4
	cfg := DefaultExecutorConfig("bounded-decision")
	cfg.ProjectDir, cfg.RunID = root, "bounded-decision-run"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
	if err == nil || state == nil || state.Status != StatusFailed {
		t.Fatalf("foreground pipeline accepted oversized predicate: %+v %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreground pipeline dispatched default branch from invalid output: %v", err)
	}
}

func TestControlledPipelineRejectsUnsettledSelectorOutput(t *testing.T) {
	for _, kind := range []string{"branch", "foreach"} {
		for _, stream := range []string{"stdout", "stderr"} {
			for _, exitCode := range []int{0, 7} {
				t.Run(fmt.Sprintf("%s/%s/exit_%d", kind, stream, exitCode), func(t *testing.T) {
					root := t.TempDir()
					script, release := escapedCommandOutputFixture(t, root, stream, exitCode)
					step := Step{ID: "select", OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}}
					if kind == "branch" {
						step.Branch = "$(" + script + ")"
						step.Branches = map[string]interface{}{
							"good":    Step{ID: "selected", Command: "printf selected > marker"},
							"default": Step{ID: "fallback", Command: "printf default > marker"},
						}
					} else {
						step.Foreach = &ForeachConfig{Beads: "$(" + script + ")", Steps: []Step{{ID: "body", Command: "printf body > marker"}}}
					}
					workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "unsettled-selector", Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
					cfg := DefaultExecutorConfig("unsettled-selector")
					cfg.ProjectDir, cfg.RunID = root, "run-unsettled-selector"
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
					if err == nil || state == nil || state.Status != StatusFailed {
						t.Fatalf("pipeline admitted an uncertain selector: %+v %v", state, err)
					}
					result := state.Steps[step.ID]
					if result.Status != StatusFailed || result.Attempts != 1 || result.Error == nil ||
						!strings.Contains(result.Error.Message, errCommandOutputUnsettled.Error()) {
						t.Fatalf("uncertain selector was retried or reduced to an ordinary exit failure: %+v", result)
					}
					if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("incomplete selector output dispatched downstream work: %v", err)
					}
					prior, err := LoadState(root, cfg.RunID)
					if err != nil {
						t.Fatal(err)
					}
					receipt, exists := prior.CommandEvaluations[step.ID]
					if !exists || receipt.Status != commandExecutionUnresolved || receipt.ProcessID <= 0 ||
						receipt.StartedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) ||
						!strings.Contains(receipt.Error, errCommandOutputUnsettled.Error()) ||
						!strings.Contains(receipt.Error, stream+" output capture did not reach EOF") {
						t.Fatalf("selector uncertainty was not retained independently of its step result: %+v", receipt)
					}
					before := commandOperatorRecoveryBytes(t, cfg)
					if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); !errors.Is(err, errCommandOutputUnsettled) {
						t.Fatalf("ordinary resume repeated an unresolved selector: %v", err)
					}
					if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
						t.Fatal("rejected selector resume changed its saved evidence")
					}
					if data, err := os.ReadFile(filepath.Join(root, "selector.launches")); err != nil || string(data) != "launch\n" {
						t.Fatalf("uncertain selector was executed more than once: %q %v", data, err)
					}
					release()
					if data, err := os.ReadFile(filepath.Join(root, "late-effect")); err != nil || string(data) != "late" {
						t.Fatalf("fixture did not demonstrate work surviving the rejected selector: %q %v", data, err)
					}
				})
			}
		}
	}
}

func TestControlledPipelineRetriesSettledSelectorExitFailure(t *testing.T) {
	for _, kind := range []string{"branch", "foreach"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			expr := `$(printf 'launch\n' >> selector.launches
if [ ! -f retry-ready ]; then printf ready > retry-ready; printf diagnostic >&2; exit 7; fi
printf good)`
			step := Step{ID: "select", OnError: ErrorActionRetry, RetryCount: 1, RetryDelay: Duration{Duration: time.Millisecond}}
			if kind == "branch" {
				step.Branch = expr
				step.Branches = map[string]interface{}{"good": Step{ID: "selected", Command: "printf dispatched > marker"}}
			} else {
				step.Foreach = &ForeachConfig{Beads: expr, Steps: []Step{{ID: "body", Command: "printf dispatched > marker"}}}
			}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "settled-selector-retry", Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
			cfg := DefaultExecutorConfig("settled-selector-retry")
			cfg.ProjectDir, cfg.RunID = root, "run-settled-selector-retry"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted || state.Steps[step.ID].Attempts != 2 {
				t.Fatalf("settled exit failure lost its ordinary retry semantics: %+v %v", state, err)
			}
			if record := state.CommandEvaluations[step.ID]; len(state.CommandEvaluations) != 1 || record.Status != commandExecutionSettled || string(record.Output) != "good" {
				t.Fatalf("successful retry lost its reusable output: %+v", state.CommandEvaluations)
			}
			saved, err := LoadState(root, cfg.RunID)
			if err != nil || saved.CommandEvaluations[step.ID].Status != commandExecutionSettled || string(saved.CommandEvaluations[step.ID].Output) != "good" {
				t.Fatalf("successful output was not durable: %+v %v", saved, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "selector.launches")); err != nil || string(data) != "launch\nlaunch\n" {
				t.Fatalf("settled selector did not retry exactly once: %q %v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "marker")); err != nil || string(data) != "dispatched" {
				t.Fatalf("successful selector retry failed to dispatch its work: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineRejectsUnsettledBeadQueryOutput(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			root := t.TempDir()
			script, release := escapedCommandOutputFixtureWithOutput(t, root, stream, 0,
				`{"issues":[{"id":"bd-live","title":"Live","status":"open"}]}`)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "br"), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			step := Step{ID: "query", BeadQuery: &BeadQueryStep{Status: "open"},
				OnError: ErrorActionRetry, RetryCount: 2, RetryDelay: Duration{Duration: time.Millisecond}}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "unsettled-bead-query", Settings: DefaultWorkflowSettings(),
				Steps: []Step{step, {ID: "consumer", DependsOn: []string{step.ID}, Command: "printf dispatched > marker"}}}
			cfg := DefaultExecutorConfig("unsettled-bead-query")
			cfg.ProjectDir, cfg.RunID = root, "run-unsettled-bead-query"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			if err == nil || state == nil || state.Status != StatusFailed {
				t.Fatalf("pipeline admitted uncertain br output: %+v %v", state, err)
			}
			result := state.Steps[step.ID]
			if result.Status != StatusFailed || result.Attempts != 1 || result.Output != "" || result.ParsedData != nil ||
				result.Error == nil || result.Error.Type != "execution_unknown" ||
				!strings.Contains(result.Error.Message, errCommandOutputUnsettled.Error()) {
				t.Fatalf("uncertain query was retried or admitted records: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncertain query dispatched consumers: %v", err)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			receipt, exists := prior.CommandEvaluations[step.ID]
			if !exists || receipt.Status != commandExecutionUnresolved || receipt.ProcessID <= 0 ||
				receipt.StartedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) ||
				!strings.Contains(receipt.Error, errCommandOutputUnsettled.Error()) ||
				!strings.Contains(receipt.Error, stream+" output capture did not reach EOF") {
				t.Fatalf("uncertain query lacks durable process evidence: %+v", receipt)
			}
			before := commandOperatorRecoveryBytes(t, cfg)
			if _, err := NewExecutor(cfg).Resume(context.Background(), workflow, prior, nil); !errors.Is(err, errCommandOutputUnsettled) {
				t.Fatalf("ordinary resume repeated an unresolved query: %v", err)
			}
			if !bytes.Equal(before, commandOperatorRecoveryBytes(t, cfg)) {
				t.Fatal("rejected query resume changed its saved evidence")
			}
			if data, err := os.ReadFile(filepath.Join(root, "selector.launches")); err != nil || string(data) != "launch\n" {
				t.Fatalf("uncertain query was executed more than once: %q %v", data, err)
			}
			release()
			if data, err := os.ReadFile(filepath.Join(root, "late-effect")); err != nil || string(data) != "late" {
				t.Fatalf("fixture did not demonstrate work surviving the rejected query: %q %v", data, err)
			}
		})
	}
}

func TestControlledPipelineReusesForeachSourceBeforeAdmissionCheckpoint(t *testing.T) {
	e := newCommandTestExecutor(t)
	step := Step{ID: "items", Foreach: &ForeachConfig{Beads: "$(printf x >> selections; cat source-items)",
		Steps: []Step{{ID: "body", Command: "printf '%s\\n' '${item}' >> admitted"}}}}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "source-items"), []byte("alpha\nbeta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := e.resolveForeachItems(e.commandEvaluationContext(context.Background(), &step), step.Foreach, "foreach")
	if err != nil || len(items) != 2 {
		t.Fatalf("initial source: %+v %v", items, err)
	}
	prior, err := LoadState(e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.ForeachState) != 0 || len(prior.Steps) != 0 || prior.CommandEvaluations[step.ID].Status != commandExecutionSettled {
		t.Fatal("fixture did not stop after source success and before item admission")
	}
	if err := os.WriteFile(filepath.Join(e.config.ProjectDir, "source-items"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireRunControl(context.Background(), e.config.ProjectDir, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	resumed, err := NewExecutor(e.config).Resume(owner.Context(), workflow, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted {
		t.Fatalf("resume lost the completed source: %+v %v", resumed, err)
	}
	if resumed.ForeachState[step.ID].ItemsFingerprint != computeForeachItemsFingerprint(items) {
		t.Fatal("resume did not bind iteration progress to the original source items")
	}
	for name, want := range map[string]string{"selections": "x", "admitted": "alpha\nbeta\n"} {
		if data, err := os.ReadFile(filepath.Join(e.config.ProjectDir, name)); err != nil || string(data) != want {
			t.Fatalf("%s = %q want %q: %v", name, data, want, err)
		}
	}
}

func TestControlledPipelineRetainsForeachSourceAcrossChildFailure(t *testing.T) {
	for _, mode := range []ResumeMode{ResumeModeContinue, ResumeModeRestartFailed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			step := Step{ID: "items", Foreach: &ForeachConfig{Beads: "$(printf x >> selections; cat source-items)",
				Steps: []Step{{ID: "body", Command: "if [ '${item}' = beta ] && [ ! -f release ]; then exit 7; fi; printf '%s\\n' '${item}' >> admitted"}}}}
			workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "stable-foreach-source", Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
			cfg := DefaultExecutorConfig("stable-foreach-source")
			cfg.ProjectDir, cfg.RunID = root, "run-stable-foreach-source"
			if err := os.WriteFile(filepath.Join(root, "source-items"), []byte("alpha\nbeta\n"), 0600); err != nil {
				t.Fatal(err)
			}
			first, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err == nil || first == nil || first.Status != StatusFailed {
				t.Fatalf("fixture did not fail in its second item: %+v %v", first, err)
			}
			prior, err := LoadState(root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			record := prior.CommandEvaluations[step.ID]
			if record.Status != commandExecutionSettled || len(prior.ForeachState[step.ID].CompletedIterationIDs) != 1 {
				t.Fatalf("fixture lacks successful source and partial iteration progress: %+v %+v", record, prior.ForeachState[step.ID])
			}
			for name, content := range map[string]string{"source-items": "changed\n", "release": "go"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			owner, err := AcquireRunControl(context.Background(), root, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			cfg.WorkflowFile = prior.WorkflowFile
			frozen, valid, err := LoadResumeWorkflow(prior.WorkflowFile)
			if err != nil || !valid.Valid {
				t.Fatalf("recovery definition: %v %+v", err, valid)
			}
			resumed, err := NewExecutor(cfg).ResumeWithOptions(owner.Context(), frozen, prior, ResumeOptions{Mode: mode}, nil)
			if err != nil || resumed == nil || resumed.Status != StatusCompleted {
				t.Fatalf("resume failed to retain the admitted item list: %+v %v", resumed, err)
			}
			if kept := resumed.CommandEvaluations[step.ID]; kept.Status != commandExecutionSettled || !kept.StartedAt.Equal(record.StartedAt) {
				t.Fatalf("child failure refreshed its successful source: %+v", kept)
			}
			for name, want := range map[string]string{"selections": "x", "admitted": "alpha\nbeta\n"} {
				if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != want {
					t.Fatalf("%s = %q want %q: %v", name, data, want, err)
				}
			}
		})
	}
}

func TestControlledPipelineReusesBeadQueryBeforeResultCheckpoint(t *testing.T) {
	e := newCommandTestExecutor(t)
	root := e.config.ProjectDir
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	br := filepath.Join(bin, "br")
	writeBr := func(id string) {
		t.Helper()
		script := "#!/bin/sh\nprintf x >> selections\nprintf '%s' '{\"issues\":[{\"id\":\"" + id + "\",\"status\":\"open\"}]}'\n"
		if err := os.WriteFile(br, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeBr("bd-original")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	step := Step{ID: "query", BeadQuery: &BeadQueryStep{Status: "open"}}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: e.state.WorkflowID, Settings: DefaultWorkflowSettings(), Steps: []Step{step}}
	resolver := IterationSourceResolver{ProjectDir: root}
	if _, err := resolver.runBr(e.commandEvaluationContext(context.Background(), &step), step.BeadQuery.brListArgs()); err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(root, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior.Steps) != 0 || prior.CommandEvaluations[step.ID].Status != commandExecutionSettled {
		t.Fatal("fixture did not stop before typed result admission")
	}
	writeBr("bd-changed")
	owner, err := AcquireRunControl(context.Background(), root, e.state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	resumed, err := NewExecutor(e.config).Resume(owner.Context(), workflow, prior, nil)
	if err != nil || resumed == nil || resumed.Status != StatusCompleted || !strings.Contains(resumed.Steps[step.ID].Output, "bd-original") || strings.Contains(resumed.Steps[step.ID].Output, "bd-changed") {
		t.Fatalf("resume lost the original typed query result: %+v %v", resumed, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "selections")); err != nil || string(data) != "x" {
		t.Fatalf("completed query effects were repeated: %q %v", data, err)
	}
}
