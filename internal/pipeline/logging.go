package pipeline

import (
	"fmt"
	"log/slog"
)

const (
	EventRunStart         = "pipeline.run.start"
	EventRunComplete      = "pipeline.run.complete"
	EventRunError         = "pipeline.run.error"
	EventStepStart        = "pipeline.step.start"
	EventStepComplete     = "pipeline.step.complete"
	EventStepError        = "pipeline.step.error"
	EventStepSkip         = "pipeline.step.skip"
	EventStepRetry        = "pipeline.step.retry"
	EventDispatch         = "pipeline.dispatch"
	EventCommandExec      = "pipeline.command.exec"
	EventCommandResult    = "pipeline.command.result"
	EventCommandCancelled = "pipeline.command.cancelled"
	// EventCommandHeartbeat is emitted periodically while a long-running
	// command step is still executing, so dashboards can distinguish
	// "still working" from "stuck" without tailing stdout (bd-zfdjd.7).
	EventCommandHeartbeat  = "pipeline.command.heartbeat"
	EventTemplateRender    = "pipeline.template.render"
	EventTemplateCancelled = "pipeline.template.cancelled"
	EventForeachStart      = "pipeline.foreach.start"
	EventForeachIter       = "pipeline.foreach.iteration"
	EventForeachComplete   = "pipeline.foreach.complete"
	EventBranchEval        = "pipeline.branch.eval"
	EventBranchDispatch    = "pipeline.branch.dispatch"
	EventOnFailureFire     = "pipeline.on_failure.fire"
	EventOnSuccessFire     = "pipeline.on_success.fire"
	EventSubstResolve      = "pipeline.subst.resolve"
	EventSubstWarn         = "pipeline.subst.warn"
	EventLoopIterStart     = "pipeline.loop.iteration.start"
	EventLoopExit          = "pipeline.loop.exit"
)

const (
	FieldRunID                = "run_id"
	FieldWorkflow             = "workflow"
	FieldStepID               = "step_id"
	FieldStepKind             = "step_kind"
	FieldPaneID               = "pane_id"
	FieldPaneIndex            = "pane_index"
	FieldAgentType            = "agent_type"
	FieldIteration            = "iteration"
	FieldIterationTotal       = "iteration_total"
	FieldItemSummary          = "item_summary"
	FieldDurationMS           = "duration_ms"
	FieldStatus               = "status"
	FieldExitCode             = "exit_code"
	FieldSubstitutionKey      = "substitution_key"
	FieldSubstitutionResolved = "substitution_resolved"
	FieldRecoveryPane         = "recovery_pane"
	FieldRecoveryTemplate     = "recovery_template"
	FieldRuntimeVariable      = "runtime_variable"
	FieldFailureAction        = "failure_action"
	FieldSignalSent           = "signal_sent"
)

const (
	StepKindUnknown     = "unknown"
	StepKindPrompt      = "prompt"
	StepKindCommand     = "command"
	StepKindTemplate    = "template"
	StepKindParallel    = "parallel"
	StepKindLoop        = "loop"
	StepKindLoopControl = "loop_control"
	StepKindForeach     = "foreach"
	StepKindForeachPane = "foreach_pane"
	StepKindBranch      = "branch"
)

const maxItemSummaryLen = 120

// Logger returns a slog logger with the current pipeline run identity attached.
func (e *Executor) Logger() *slog.Logger {
	if e == nil {
		return slog.Default().With(FieldRunID, "", FieldWorkflow, "")
	}

	e.stateMu.RLock()
	runID, workflow := "", ""
	if e.state != nil {
		runID = e.state.RunID
		workflow = e.state.WorkflowID
	}
	e.stateMu.RUnlock()

	return slog.Default().With(
		FieldRunID, runID,
		FieldWorkflow, workflow,
	)
}

// stepLogger returns a logger with stable per-step pipeline fields attached.
func (e *Executor) stepLogger(step *Step) *slog.Logger {
	logger := e.Logger()
	if step == nil {
		return logger.With(
			FieldStepID, "",
			FieldStepKind, StepKindUnknown,
			FieldAgentType, StepKindUnknown,
		)
	}

	kind := stepKind(step)
	agentType := step.Agent
	if agentType == "" {
		agentType = kind
	}

	return logger.With(
		FieldStepID, step.ID,
		FieldStepKind, kind,
		FieldAgentType, agentType,
	)
}

// iterLogger returns a logger with foreach iteration identity attached.
func (e *Executor) iterLogger(parent *slog.Logger, iter, total int, item interface{}) *slog.Logger {
	if parent == nil {
		parent = e.Logger()
	}
	return parent.With(
		FieldIteration, iter,
		FieldIterationTotal, total,
		FieldItemSummary, summarizeLogItem(item),
	)
}

func stepKind(step *Step) string {
	switch {
	case step == nil:
		return StepKindUnknown
	case step.Command != "":
		return StepKindCommand
	case step.Template != "":
		return StepKindTemplate
	case step.Foreach != nil:
		return StepKindForeach
	case step.ForeachPane != nil:
		return StepKindForeachPane
	case step.Branch != "" || len(step.Branches) > 0:
		return StepKindBranch
	case step.Loop != nil:
		return StepKindLoop
	case step.Parallel.Flag || len(step.Parallel.Steps) > 0:
		return StepKindParallel
	case step.Prompt != "" || step.PromptFile != "":
		return StepKindPrompt
	case step.LoopControl != "":
		return StepKindLoopControl
	default:
		return StepKindUnknown
	}
}

func summarizeLogItem(item interface{}) string {
	summary := fmt.Sprintf("%v", item)
	if len(summary) <= maxItemSummaryLen {
		return summary
	}
	return summary[:maxItemSummaryLen] + "..."
}
