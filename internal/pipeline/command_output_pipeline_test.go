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
			if len(state.CommandEvaluations) != 0 {
				t.Fatalf("settled evaluations retained unresolved launch receipts: %+v", state.CommandEvaluations)
			}
			saved, err := LoadState(root, cfg.RunID)
			if err != nil || len(saved.CommandEvaluations) != 0 {
				t.Fatalf("settlement proof was not durably cleared: %+v %v", saved, err)
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
