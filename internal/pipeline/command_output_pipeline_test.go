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

func TestSelectorOperatorRecoveryPublishesOnlyOnOrdinaryResume(t *testing.T) {
	for _, kind := range []string{"branch", "foreach"} {
		t.Run(kind, func(t *testing.T) {
			output, admitted := []byte("good"), "good\n"
			if kind == "foreach" {
				output, admitted = []byte("alpha\nbeta\n"), "alpha\nbeta\n"
			}
			cfg, workflow, _, _ := selectorOperatorRecoveryFixture(t, kind, output, 0, false)
			entry := selectorRecoveryEntry(t, cfg)
			reason := "Verified source and descendants finished; complete original stdout captured"
			accepted, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, output)
			if err != nil || accepted.Replayed {
				t.Fatalf("accept source: %+v %v", accepted, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "admitted")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("acceptance itself dispatched a consumer: %v", err)
			}
			// Re-execution would observe different items/choice and add another
			// source effect; only accepted bytes may reach normal admission.
			if err := os.WriteFile(filepath.Join(cfg.ProjectDir, "source-output"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(owner.Context(), 5*time.Second)
			resumed, resumeErr := NewExecutor(cfg).Resume(ctx, workflow, prior, nil)
			cancel()
			owner.Close()
			if resumeErr != nil || resumed == nil || resumed.Status != StatusCompleted {
				t.Fatalf("ordinary resume did not consume accepted %s: %+v %v", kind, resumed, resumeErr)
			}
			for name, want := range map[string]string{"source-effects": "once\n", "admitted": admitted} {
				if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, name)); err != nil || string(data) != want {
					t.Fatalf("%s=%q want %q: %v", name, data, want, err)
				}
			}
			if kind == "foreach" && resumed.ForeachState[entry.StepID].ItemsFingerprint != computeForeachItemsFingerprint([]interface{}{"alpha", "beta"}) {
				t.Fatal("accepted source did not bind progress to the original iteration list")
			}
			before := selectorRecoveryCheckpoint(t, cfg)
			again, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, reason, 0, append([]byte(nil), output...))
			if err != nil || !again.Replayed {
				t.Fatalf("immutable decision was not idempotent after parent completion: %+v %v", again, err)
			}
			for _, different := range []struct {
				reason string
				exit   int
				output []byte
			}{{reason + " changed", 0, output}, {reason, 7, output}, {reason, 0, []byte("different")}} {
				if _, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, different.reason, different.exit, different.output); err == nil {
					t.Fatal("a completed operator decision was replaced")
				}
			}
			if !bytes.Equal(before, selectorRecoveryCheckpoint(t, cfg)) {
				t.Fatal("idempotence or changed-decision refusal rewrote the checkpoint")
			}
		})
	}
}

func TestSelectorOperatorRecoveryRetainsNonzeroOutcomeAcrossResume(t *testing.T) {
	for _, kind := range []string{"branch", "foreach"} {
		t.Run(kind, func(t *testing.T) {
			cfg, workflow, _, _ := selectorOperatorRecoveryFixture(t, kind, []byte("good"), 7, false)
			entry := selectorRecoveryEntry(t, cfg)
			if _, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID, entry.StepID, entry.ReceiptToken, "Verified final exit status and complete original stdout", 7, []byte("good")); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				prior, err := LoadState(cfg.ProjectDir, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				owner, err := AcquireRunControl(context.Background(), cfg.ProjectDir, cfg.RunID)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(owner.Context(), 5*time.Second)
				resumed, resumeErr := NewExecutor(cfg).Resume(ctx, workflow, prior, nil)
				cancel()
				owner.Close()
				if resumeErr == nil || resumed == nil || resumed.Status != StatusFailed {
					t.Fatalf("accepted failure became usable selector output: %+v %v", resumed, resumeErr)
				}
				result := resumed.Steps[entry.StepID]
				if result.Error == nil || !strings.Contains(result.Error.Message, "status 7") || result.Error.Type == "execution_unknown" || result.Attempts != 1 {
					t.Fatalf("normal source failure policy lost its accepted exit outcome: %+v", result)
				}
				if record := resumed.CommandEvaluations[entry.StepID]; record.Status != commandEvaluationAccepted || record.ResolutionExitCode != 7 {
					t.Fatalf("failure handling discarded immutable acceptance: %+v", record)
				}
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "admitted")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("nonzero accepted output dispatched body work: %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "source-effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("retry or resume reran the accepted failed selector: %q %v", data, err)
			}
		})
	}
}

func TestSelectorOperatorRecoveryReopensPrematureCompletion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		unresolved bool
		status     ExecutionStatus
		exitCode   int
	}{
		{"starting_branch_success", "branch", false, StatusFailed, 0},
		{"starting_foreach_failure", "foreach", false, StatusCancelled, 7},
		{"unresolved_branch_failure", "branch", true, StatusFailed, 7},
		{"unresolved_foreach_success", "foreach", true, StatusCancelled, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := []byte("good")
			if tc.kind == "foreach" {
				output = []byte("alpha\nbeta\n")
			}
			cfg, workflow, prior, _ := selectorOperatorRecoveryFixture(t, tc.kind, output, tc.exitCode, tc.unresolved)
			original := selectorRecoveryEntry(t, cfg)
			finished := time.Now().UTC()
			prior.Status, prior.FinishedAt, prior.UpdatedAt = StatusCompleted, finished, finished
			prior.Steps[original.StepID] = StepResult{StepID: original.StepID, Status: tc.status,
				Error: &StepError{Type: "execution_unknown", Message: "original source settlement was not observed"}}
			prior.Variables["retained"] = "unrelated value"
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			before := selectorRecoveryCheckpoint(t, cfg)
			entry := selectorRecoveryEntry(t, cfg)
			if !entry.CanAccept || entry.ReceiptToken != original.ReceiptToken || !bytes.Equal(before, selectorRecoveryCheckpoint(t, cfg)) {
				t.Fatalf("false workflow completion hid or changed its unfinished selector: %+v", entry)
			}
			const reason = "Verified the original source and descendants ended and recovered complete stdout"
			accepted, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID,
				entry.StepID, entry.ReceiptToken, reason, tc.exitCode, output)
			if err != nil || accepted.Replayed || accepted.Selector.ReceiptToken != entry.ReceiptToken {
				t.Fatalf("recover falsely completed selector: %+v %v", accepted, err)
			}
			saved, err := LoadState(cfg.ProjectDir, cfg.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != StatusFailed || !saved.FinishedAt.Equal(finished) ||
				!saved.UpdatedAt.Equal(accepted.Selector.ResolvedAt) || !saved.LastCheckpointAt.Equal(prior.LastCheckpointAt) ||
				saved.Steps[entry.StepID].Status != tc.status || saved.Variables["retained"] != "unrelated value" ||
				len(saved.ForeachState) != 0 || len(saved.Steps) != len(prior.Steps) {
				t.Fatalf("acceptance published selector work or did not repair terminal status: %+v", saved)
			}
			if len(saved.Errors) != len(prior.Errors)+1 {
				t.Fatalf("status repair did not add one audit diagnostic: %+v", saved.Errors)
			}
			diagnostic := saved.Errors[len(saved.Errors)-1]
			if diagnostic.Type != "recovery" || diagnostic.StepID != entry.StepID || !diagnostic.Fatal ||
				!diagnostic.Timestamp.Equal(accepted.Selector.ResolvedAt) || !strings.Contains(diagnostic.Message, "unsettled") || !strings.Contains(diagnostic.Message, "resume") {
				t.Fatalf("status repair lost its reason: %+v", diagnostic)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "admitted")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("acceptance admitted selector work: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			final, err := NewExecutor(cfg).Resume(ctx, workflow, saved, nil)
			if tc.exitCode == 0 {
				if err != nil || final.Status != StatusCompleted {
					t.Fatalf("verified selector could not resume after status repair: %+v %v", final, err)
				}
				want := "good\n"
				if tc.kind == "foreach" {
					want = string(output)
				}
				if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "admitted")); err != nil || string(data) != want {
					t.Fatalf("accepted source did not use normal admission: %q %v", data, err)
				}
			} else {
				if err == nil || final.Status != StatusFailed {
					t.Fatalf("accepted unsuccessful selector lost its failure: %+v %v", final, err)
				}
				if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "admitted")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("accepted failed source admitted work: %v", err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "source-effects")); err != nil || string(data) != "once\n" {
				t.Fatalf("recovery repeated the original source: %q %v", data, err)
			}
			before = selectorRecoveryCheckpoint(t, cfg)
			again, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID,
				entry.StepID, entry.ReceiptToken, reason, tc.exitCode, output)
			if err != nil || !again.Replayed || !bytes.Equal(before, selectorRecoveryCheckpoint(t, cfg)) {
				t.Fatalf("repeating acceptance reopened a finished selector outcome: %+v %v", again, err)
			}
		})
	}
}

func TestSelectorOperatorRecoveryPrematureCompletionRefusalsPreserveState(t *testing.T) {
	for _, status := range []ExecutionStatus{StatusCompleted, StatusSkipped, StatusPending, StatusRunning, ""} {
		t.Run(string(status), func(t *testing.T) {
			output := []byte("good")
			cfg, _, prior, _ := selectorOperatorRecoveryFixture(t, "branch", output, 0, false)
			entry := selectorRecoveryEntry(t, cfg)
			prior.Status = StatusCompleted
			prior.Steps[entry.StepID] = StepResult{StepID: entry.StepID, Status: status}
			if err := SaveState(cfg.ProjectDir, prior); err != nil {
				t.Fatal(err)
			}
			before := selectorRecoveryCheckpoint(t, cfg)
			entry = selectorRecoveryEntry(t, cfg)
			if entry.CanAccept {
				t.Fatalf("completed workflow without an unsuccessful source advertised recovery: %+v", entry)
			}
			if _, err := AcceptCommandEvaluationContext(context.Background(), cfg.ProjectDir, cfg.RunID,
				entry.StepID, entry.ReceiptToken, "Verified the original selector ended", 0, output); err == nil {
				t.Fatal("acceptance replaced potentially consumed selector output")
			}
			if !bytes.Equal(before, selectorRecoveryCheckpoint(t, cfg)) {
				t.Fatal("refused selector recovery changed workflow status or evidence")
			}
		})
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
