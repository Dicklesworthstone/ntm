package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These native executor tests exercise actual shell commands and checkpoint
// files through Run/ResumeWithOptions. They require the complete pipeline
// package; a helper-only recovery harness is not a substitute for running them.
func TestResumeForceReplayRunsCompletedLoopSuffix(t *testing.T) {
	for _, kind := range []string{"loop_items", "foreach_rounds"} {
		t.Run(kind, func(t *testing.T) {
			project := t.TempDir()
			iterations := filepath.Join(project, "iterations.log")
			independent := filepath.Join(project, "independent.log")
			body := Step{ID: "work", Command: "printf '%s\n' '${loop.index}' >> " + strconv.Quote(iterations)}
			batch := Step{ID: "batch"}
			want := "0\n1\n2\n1\n2"
			if kind == "loop_items" {
				batch.Loop = &LoopConfig{Items: "${vars.items}", Steps: []Step{body}}
			} else {
				body.Command = "printf '%s:%s\n' '${loop.index}' '${round}' >> " + strconv.Quote(iterations)
				batch.Foreach = &ForeachConfig{Items: "${vars.items}", MaxRounds: IntOrExpr{Value: 2}, Steps: []Step{body}}
				want = "0:1\n0:2\n1:1\n1:2\n2:1\n2:2\n1:1\n1:2\n2:1\n2:2"
			}
			workflow := &Workflow{
				SchemaVersion: SchemaVersion, Name: "force-replay-" + kind, Settings: DefaultWorkflowSettings(),
				Steps: []Step{
					{ID: "independent", Command: "printf once >> " + strconv.Quote(independent)}, batch,
				},
			}
			cfg := DefaultExecutorConfig("force-replay-session")
			cfg.ProjectDir = project
			cfg.DefaultTimeout = 5 * time.Second
			first, err := NewExecutor(cfg).Run(context.Background(), workflow, map[string]interface{}{
				"items": []interface{}{"a", "b", "c"},
			}, nil)
			if err != nil {
				t.Fatalf("initial run: %v", err)
			}
			prior, err := LoadState(project, first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			final, err := NewExecutor(cfg).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
				Mode: ResumeModeForceIter, StepID: "batch", Iteration: 1,
			}, nil)
			if err != nil || final.Status != StatusCompleted {
				t.Fatalf("forced resume failed: state=%+v err=%v", final, err)
			}
			data, err := os.ReadFile(iterations)
			if err != nil || strings.TrimSpace(string(data)) != want {
				t.Fatalf("iteration replay = %q, want %q (read error: %v)", data, want, err)
			}
			data, err = os.ReadFile(independent)
			if err != nil || string(data) != "once" {
				t.Fatalf("independent work repeated: %q (%v)", data, err)
			}
		})
	}
}

func TestResumeForceReplayRefusalPreservesDiskAndSideEffects(t *testing.T) {
	project := t.TempDir()
	marker := filepath.Join(project, "consumer.log")
	workflow := &Workflow{
		SchemaVersion: SchemaVersion, Name: "force-dependent-safety", Settings: DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "batch", Foreach: &ForeachConfig{Items: "${vars.items}", Steps: []Step{{ID: "work", Command: "printf item"}}}},
			{ID: "consumer", DependsOn: []string{"batch"}, Command: "printf once >> " + strconv.Quote(marker)},
		},
	}
	cfg := DefaultExecutorConfig("force-replay-session")
	cfg.ProjectDir = project
	cfg.DefaultTimeout = 5 * time.Second
	first, err := NewExecutor(cfg).Run(context.Background(), workflow, map[string]interface{}{
		"items": []interface{}{"a", "b"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := LoadState(project, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewExecutor(cfg).ResumeWithOptions(context.Background(), workflow, prior, ResumeOptions{
		Mode: ResumeModeForceIter, StepID: "batch", Iteration: 0,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "downstream recovery evidence") {
		t.Fatalf("unsafe forced replay was not refused: %v", err)
	}
	persisted, err := LoadState(project, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(persisted)
	if err != nil || string(before) != string(after) {
		t.Fatal("refused replay rewrote the durable checkpoint")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "once" {
		t.Fatalf("refused replay repeated consumer side effects: %q (%v)", data, err)
	}
}
