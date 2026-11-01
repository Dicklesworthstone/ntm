package pipeline

import (
	"encoding/json"
	"fmt"
	"time"
)

type aggregatedErrorSummary struct {
	// Iteration is a pointer so the foreach path can preserve iteration 0
	// while the parallel path (which has no iteration concept) leaves it
	// unset and out of the serialized JSON details (bd-u92d2).
	Iteration *int   `json:"iteration,omitempty"`
	StepID    string `json:"step_id,omitempty"`
	Type      string `json:"type,omitempty"`
	Message   string `json:"message"`
}

func aggregateForeachErrors(iterations []foreachIterationResult, stepID string, total int) *StepError {
	entries := make([]StepError, 0)
	summaries := make([]aggregatedErrorSummary, 0)
	for _, iteration := range iterations {
		entry, ok := foreachIterationFailureError(iteration)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		idx := iteration.Index
		summaries = append(summaries, aggregatedErrorSummary{
			Iteration: &idx,
			Type:      entry.Type,
			Message:   entry.Message,
		})
	}
	if len(entries) == 0 {
		for _, iteration := range iterations {
			entry, ok := foreachIterationAnyError(iteration)
			if !ok {
				continue
			}
			entries = append(entries, entry)
			idx := iteration.Index
			summaries = append(summaries, aggregatedErrorSummary{
				Iteration: &idx,
				Type:      entry.Type,
				Message:   entry.Message,
			})
		}
	}
	if len(entries) == 0 {
		return &StepError{
			Type:      "foreach",
			Message:   fmt.Sprintf("foreach step %q failed", stepID),
			Timestamp: time.Now(),
		}
	}

	details, _ := json.Marshal(summaries)
	first := entries[0]
	return &StepError{
		Type:       "foreach",
		Message:    fmt.Sprintf("%d of %d iterations failed (first: %s)", len(entries), total, first.Message),
		Details:    string(details),
		Timestamp:  time.Now(),
		Aggregated: entries,
	}
}

func foreachIterationFailureError(iteration foreachIterationResult) (StepError, bool) {
	// bd-5f60g: collect every failed body step in this iteration, not just
	// the first one. With on_error=continue the iteration may record
	// several failed StepResults, but the original code returned after the
	// first match so later failures disappeared from result.Error.Aggregated
	// and the JSON summary in result.Error.Details. Surface every failure
	// so incident reports stay complete.
	var failures []StepError
	for _, result := range iteration.Results {
		if result.Status != StatusFailed {
			continue
		}
		entry := stepResultAggregatedError(result)
		entry.Message = fmt.Sprintf("iter-%d: %s", iteration.Index, entry.Message)
		failures = append(failures, entry)
	}
	if iteration.admissionError != nil {
		entry := cloneStepErrorValue(*iteration.admissionError)
		entry.Message = fmt.Sprintf("iter-%d: %s", iteration.Index, entry.Message)
		failures = append(failures, entry)
	}
	if len(failures) > 0 {
		first := failures[0]
		if len(failures) == 1 {
			return first, true
		}
		summary := first
		summary.Message = fmt.Sprintf("%s (+%d more failed steps in iter-%d)", first.Message, len(failures)-1, iteration.Index)
		summary.Aggregated = failures
		return summary, true
	}
	if iteration.Error == "" || len(iteration.Results) > 0 {
		return StepError{}, false
	}
	return StepError{
		Type:      "foreach_iteration",
		Message:   fmt.Sprintf("iter-%d: %s", iteration.Index, iteration.Error),
		Timestamp: time.Now(),
	}, true
}

func foreachIterationAnyError(iteration foreachIterationResult) (StepError, bool) {
	if entry, ok := foreachIterationFailureError(iteration); ok {
		return entry, true
	}
	for _, result := range iteration.Results {
		if result.Status != StatusCancelled {
			continue
		}
		entry := stepResultAggregatedError(result)
		entry.Message = fmt.Sprintf("iter-%d: %s", iteration.Index, entry.Message)
		return entry, true
	}
	return StepError{}, false
}

func aggregateParallelErrors(results []StepResult, total int) *StepError {
	entries := make([]StepError, 0)
	summaries := make([]aggregatedErrorSummary, 0)
	for _, result := range results {
		if result.Status != StatusFailed {
			continue
		}
		entry := stepResultAggregatedError(result)
		entries = append(entries, entry)
		summaries = append(summaries, aggregatedErrorSummary{
			StepID:  result.StepID,
			Type:    entry.Type,
			Message: entry.Message,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	details, _ := json.Marshal(summaries)
	first := entries[0]
	return &StepError{
		Type:       "parallel",
		Message:    fmt.Sprintf("%d of %d parallel steps failed (first: %s)", len(entries), total, first.Message),
		Details:    string(details),
		Timestamp:  time.Now(),
		Aggregated: entries,
	}
}

func stepResultAggregatedError(result StepResult) StepError {
	if result.Error != nil {
		entry := cloneStepErrorValue(*result.Error)
		if entry.Message == "" {
			entry.Message = fmt.Sprintf("step %q failed", result.StepID)
		}
		return entry
	}
	message := resultErrorMessage(result)
	if message == "" {
		message = fmt.Sprintf("step %q failed", result.StepID)
	}
	return StepError{
		Type:      string(result.Status),
		Message:   message,
		Timestamp: time.Now(),
	}
}

func cloneStepErrorValue(err StepError) StepError {
	if len(err.Aggregated) == 0 {
		return err
	}
	err.Aggregated = append([]StepError(nil), err.Aggregated...)
	for i := range err.Aggregated {
		err.Aggregated[i] = cloneStepErrorValue(err.Aggregated[i])
	}
	return err
}
