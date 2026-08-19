package pipeline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestPipelineLogConstantsAreStable(t *testing.T) {
	wantEvents := map[string]string{
		"EventRunStart":        "pipeline.run.start",
		"EventRunComplete":     "pipeline.run.complete",
		"EventRunError":        "pipeline.run.error",
		"EventStepStart":       "pipeline.step.start",
		"EventStepComplete":    "pipeline.step.complete",
		"EventStepError":       "pipeline.step.error",
		"EventStepSkip":        "pipeline.step.skip",
		"EventStepRetry":       "pipeline.step.retry",
		"EventDispatch":        "pipeline.dispatch",
		"EventCommandExec":     "pipeline.command.exec",
		"EventCommandResult":   "pipeline.command.result",
		"EventTemplateRender":  "pipeline.template.render",
		"EventForeachStart":    "pipeline.foreach.start",
		"EventForeachIter":     "pipeline.foreach.iteration",
		"EventForeachComplete": "pipeline.foreach.complete",
		"EventBranchEval":      "pipeline.branch.eval",
		"EventBranchDispatch":  "pipeline.branch.dispatch",
		"EventOnFailureFire":   "pipeline.on_failure.fire",
		"EventOnSuccessFire":   "pipeline.on_success.fire",
		"EventSubstResolve":    "pipeline.subst.resolve",
		"EventSubstWarn":       "pipeline.subst.warn",
		"EventLoopIterStart":   "pipeline.loop.iteration.start",
		"EventLoopExit":        "pipeline.loop.exit",
	}

	gotEvents := map[string]string{
		"EventRunStart":        EventRunStart,
		"EventRunComplete":     EventRunComplete,
		"EventRunError":        EventRunError,
		"EventStepStart":       EventStepStart,
		"EventStepComplete":    EventStepComplete,
		"EventStepError":       EventStepError,
		"EventStepSkip":        EventStepSkip,
		"EventStepRetry":       EventStepRetry,
		"EventDispatch":        EventDispatch,
		"EventCommandExec":     EventCommandExec,
		"EventCommandResult":   EventCommandResult,
		"EventTemplateRender":  EventTemplateRender,
		"EventForeachStart":    EventForeachStart,
		"EventForeachIter":     EventForeachIter,
		"EventForeachComplete": EventForeachComplete,
		"EventBranchEval":      EventBranchEval,
		"EventBranchDispatch":  EventBranchDispatch,
		"EventOnFailureFire":   EventOnFailureFire,
		"EventOnSuccessFire":   EventOnSuccessFire,
		"EventSubstResolve":    EventSubstResolve,
		"EventSubstWarn":       EventSubstWarn,
		"EventLoopIterStart":   EventLoopIterStart,
		"EventLoopExit":        EventLoopExit,
	}

	for name, want := range wantEvents {
		if got := gotEvents[name]; got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestExecutorLoggerBindsRunAndWorkflow(t *testing.T) {
	var buf bytes.Buffer
	restore := capturePipelineLogs(t, &buf)
	defer restore()

	executor := NewExecutor(DefaultExecutorConfig("test-session"))
	executor.state = &ExecutionState{
		RunID:      "run-123",
		WorkflowID: "incident-response",
	}

	executor.Logger().Info(EventRunStart)

	events := parseJSONLEvents(t, &buf)
	assertEvent(t, events, EventRunStart,
		FieldRunID, "run-123",
		FieldWorkflow, "incident-response",
	)
}

func TestStepLoggerBindsStepIdentity(t *testing.T) {
	var buf bytes.Buffer
	restore := capturePipelineLogs(t, &buf)
	defer restore()

	executor := NewExecutor(DefaultExecutorConfig("test-session"))
	executor.state = &ExecutionState{
		RunID:      "run-123",
		WorkflowID: "incident-response",
	}

	executor.stepLogger(&Step{
		ID:      "render-mo",
		Agent:   "codex",
		Prompt:  "review this",
		Command: "",
	}).Info(EventStepStart)

	events := parseJSONLEvents(t, &buf)
	assertEvent(t, events, EventStepStart,
		FieldRunID, "run-123",
		FieldWorkflow, "incident-response",
		FieldStepID, "render-mo",
		FieldStepKind, StepKindPrompt,
		FieldAgentType, "codex",
	)
}

func TestStepKindClassification(t *testing.T) {
	tests := []struct {
		name string
		step *Step
		want string
	}{
		{name: "nil", step: nil, want: StepKindUnknown},
		{name: "prompt", step: &Step{Prompt: "hello"}, want: StepKindPrompt},
		{name: "prompt file", step: &Step{PromptFile: "MO.md"}, want: StepKindPrompt},
		{name: "command", step: &Step{Command: "go test ./..."}, want: StepKindCommand},
		{name: "template", step: &Step{Template: "review.md"}, want: StepKindTemplate},
		{name: "parallel flag", step: &Step{Parallel: ParallelSpec{Flag: true}}, want: StepKindParallel},
		{name: "parallel steps", step: &Step{Parallel: ParallelSpec{Steps: []Step{{ID: "a", Prompt: "a"}}}}, want: StepKindParallel},
		{name: "loop", step: &Step{Loop: &LoopConfig{Times: 1}}, want: StepKindLoop},
		{name: "loop control", step: &Step{LoopControl: LoopControlBreak}, want: StepKindLoopControl},
		{name: "foreach", step: &Step{Foreach: &ForeachConfig{Items: "a,b"}}, want: StepKindForeach},
		{name: "foreach pane", step: &Step{ForeachPane: &ForeachConfig{Items: "0,1"}}, want: StepKindForeachPane},
		{name: "branch", step: &Step{Branch: "printf yes"}, want: StepKindBranch},
		{name: "unknown", step: &Step{}, want: StepKindUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepKind(tt.step); got != tt.want {
				t.Fatalf("stepKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func capturePipelineLogs(t *testing.T, w io.Writer) func() {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))

	return func() {
		slog.SetDefault(previous)
	}
}

func parseJSONLEvents(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()

	scanner := bufio.NewScanner(r)
	var events []map[string]any
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("failed to parse slog JSON line %q: %v", string(line), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed to scan slog JSONL: %v", err)
	}
	return events
}

func assertEvent(t *testing.T, events []map[string]any, name string, kvPairs ...string) {
	t.Helper()

	if len(kvPairs)%2 != 0 {
		t.Fatalf("assertEvent requires key/value pairs, got %d values", len(kvPairs))
	}

	for _, event := range events {
		if fmtEventValue(event["msg"]) != name {
			continue
		}
		for i := 0; i < len(kvPairs); i += 2 {
			key, want := kvPairs[i], kvPairs[i+1]
			if got := fmtEventValue(event[key]); got != want {
				t.Fatalf("event %q field %q = %q, want %q; full event: %#v", name, key, got, want, event)
			}
		}
		return
	}
	t.Fatalf("event %q not found in %#v", name, events)
}

func assertNoEvent(t *testing.T, events []map[string]any, name string) {
	t.Helper()

	for _, event := range events {
		if fmtEventValue(event["msg"]) == name {
			t.Fatalf("event %q unexpectedly present: %#v", name, event)
		}
	}
}

func eventCount(events []map[string]any, name string) int {
	count := 0
	for _, event := range events {
		if fmtEventValue(event["msg"]) == name {
			count++
		}
	}
	return count
}

func fmtEventValue(v any) string {
	switch tv := v.(type) {
	case nil:
		return ""
	case string:
		return tv
	case float64:
		return strconv.FormatFloat(tv, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(tv))
	}
}
