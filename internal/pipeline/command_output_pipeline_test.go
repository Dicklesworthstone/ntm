//go:build !windows

package pipeline

import (
	"context"
	"errors"
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
