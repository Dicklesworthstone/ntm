package pipeline

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// OutputVarMode controls how repeated writes to the same output_var are
// merged for concurrent constructs.
type OutputVarMode string

const (
	OutputVarModeAggregate OutputVarMode = "aggregate"
	OutputVarModeLast      OutputVarMode = "last"
	OutputVarModeCollect   OutputVarMode = "collect"
)

func normalizeOutputVarMode(mode OutputVarMode) OutputVarMode {
	if mode == "" {
		return OutputVarModeAggregate
	}
	return mode
}

func isValidOutputVarMode(mode OutputVarMode) bool {
	switch normalizeOutputVarMode(mode) {
	case OutputVarModeAggregate, OutputVarModeLast, OutputVarModeCollect:
		return true
	default:
		return false
	}
}

func validateOutputVarMode(mode OutputVarMode, field string, result *ValidationResult) {
	if mode == "" || isValidOutputVarMode(mode) {
		return
	}
	result.addError(ParseError{
		Field:   field,
		Message: fmt.Sprintf("invalid output_var_mode value: %s", mode),
		Hint:    "Valid values: aggregate, last, collect",
	})
}

func validateOutputVarCollisions(step *Step, stepField string, result *ValidationResult) {
	validateOutputVarMode(step.OutputVarMode, stepField+".output_var_mode", result)
	validateForeachOutputVarMode(step.Foreach, step.OutputVarMode, step.OutputVar, stepField+".foreach", result)
	validateForeachOutputVarMode(step.ForeachPane, step.OutputVarMode, step.OutputVar, stepField+".foreach_pane", result)

	groups := parallelOutputVarGroups(step.Parallel.Steps)
	for name, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		ids := parallelStepIDs(step.Parallel.Steps, indices)
		result.addWarning(ParseError{
			Field:   stepField + ".parallel.output_var",
			Message: fmt.Sprintf("parallel sub-steps share output_var %q: %s", name, strings.Join(ids, ", ")),
			Hint:    "Default output_var_mode=aggregate stores a []string in declaration order; use distinct output_var names or output_var_mode:last to opt into last-completion semantics.",
		})
		// bd-ctxcf: a colliding output_var group must have a single
		// unambiguous effective mode. parallelGroupOutputVarMode picks the
		// first non-empty mode and silently ignores later conflicting ones,
		// so a workflow that declares output_var_mode:collect on one substep
		// and output_var_mode:last on another would store a different shape
		// depending on declaration order. Reject the conflict at validation
		// time so authors must pick one mode (typically by setting it on
		// the parent step or making one declaration explicit).
		if conflict := conflictingParallelOutputVarModes(step.Parallel.Steps, indices); conflict != nil {
			result.addError(ParseError{
				Field:   stepField + ".parallel.output_var_mode",
				Message: fmt.Sprintf("parallel sub-steps share output_var %q with conflicting output_var_mode values: %s", name, strings.Join(conflict, ", ")),
				Hint:    "Set output_var_mode on at most one sub-step in the group, or declare it on the parent step.",
			})
			continue
		}
		if parallelGroupOutputVarMode(step.Parallel.Steps, indices) == OutputVarModeLast {
			result.addWarning(ParseError{
				Field:   stepField + ".parallel.output_var_mode",
				Message: fmt.Sprintf("output_var_mode:last for parallel output_var %q is non-deterministic", name),
				Hint:    "Parallel last mode uses completion order; prefer aggregate or collect unless last-writer-wins is intentional.",
			})
		}
	}
}

// conflictingParallelOutputVarModes returns the distinct non-empty
// output_var_mode values declared across a set of colliding parallel
// sub-step indices, or nil if at most one distinct non-empty mode exists.
// "Non-empty" matters because a sub-step that omits the mode inherits the
// group default and is not in conflict; only explicit, divergent values are.
func conflictingParallelOutputVarModes(steps []Step, indices []int) []string {
	seen := make(map[OutputVarMode]struct{})
	var modes []string
	for _, index := range indices {
		mode := steps[index].OutputVarMode
		if mode == "" {
			continue
		}
		if _, ok := seen[mode]; ok {
			continue
		}
		seen[mode] = struct{}{}
		modes = append(modes, string(mode))
	}
	if len(modes) < 2 {
		return nil
	}
	sort.Strings(modes)
	return modes
}

func validateForeachOutputVarMode(foreach *ForeachConfig, stepMode OutputVarMode, outputVar string, field string, result *ValidationResult) {
	if foreach == nil {
		return
	}
	validateOutputVarMode(foreach.OutputVarMode, field+".output_var_mode", result)
	mode := foreach.OutputVarMode
	if mode == "" {
		mode = stepMode
	}
	if outputVar != "" && foreach.Parallel && normalizeOutputVarMode(mode) == OutputVarModeLast {
		result.addWarning(ParseError{
			Field:   field + ".output_var_mode",
			Message: fmt.Sprintf("output_var_mode:last for parallel foreach output_var %q is non-deterministic", outputVar),
			Hint:    "Parallel foreach last mode uses completion order; prefer aggregate or collect unless last-writer-wins is intentional.",
		})
	}
}

func parallelOutputVarGroups(steps []Step) map[string][]int {
	groups := make(map[string][]int)
	for i, step := range steps {
		if step.OutputVar == "" {
			continue
		}
		groups[step.OutputVar] = append(groups[step.OutputVar], i)
	}
	return groups
}

func parallelStepIDs(steps []Step, indices []int) []string {
	ids := make([]string, 0, len(indices))
	for _, index := range indices {
		id := steps[index].ID
		if id == "" {
			id = fmt.Sprintf("#%d", index)
		}
		ids = append(ids, id)
	}
	return ids
}

func parallelGroupOutputVarMode(steps []Step, indices []int) OutputVarMode {
	for _, index := range indices {
		if steps[index].OutputVarMode != "" {
			return normalizeOutputVarMode(steps[index].OutputVarMode)
		}
	}
	return OutputVarModeAggregate
}

func (e *Executor) storeParallelOutputVars(parent *Step, results []StepResult, completionOrder []string) {
	if e == nil || e.state == nil || parent == nil {
		return
	}
	groups := parallelOutputVarGroups(parent.Parallel.Steps)
	if len(groups) == 0 {
		return
	}

	e.varMu.Lock()
	defer e.varMu.Unlock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}

	for name, indices := range groups {
		mode := parallelGroupOutputVarMode(parent.Parallel.Steps, indices)
		if len(indices) == 1 {
			storeSingleParallelOutputVar(e.state, name, results[indices[0]])
			continue
		}

		switch mode {
		case OutputVarModeLast:
			if result, ok := lastCompletedParallelResult(indices, results, completionOrder); ok {
				storeSingleParallelOutputVar(e.state, name, result)
			}
			slog.Debug("parallel output_var last mode is non-deterministic",
				"run_id", e.state.RunID,
				"parent_step_id", parent.ID,
				"output_var", name,
			)
		case OutputVarModeCollect:
			outputs := make(map[string]string, len(indices))
			parsed := make(map[string]interface{}, len(indices))
			hasParsed := false
			for _, index := range indices {
				result := results[index]
				if result.Status != StatusCompleted {
					continue
				}
				stepID := result.StepID
				if stepID == "" {
					stepID = parent.Parallel.Steps[index].ID
				}
				outputs[stepID] = result.Output
				if result.ParsedData != nil {
					parsed[stepID] = result.ParsedData
					hasParsed = true
				}
			}
			e.state.Variables[name] = outputs
			if hasParsed {
				e.state.Variables[name+"_parsed"] = parsed
			}
		default:
			outputs := make([]string, 0, len(indices))
			parsed := make([]interface{}, 0, len(indices))
			hasParsed := false
			for _, index := range indices {
				result := results[index]
				if result.Status != StatusCompleted {
					continue
				}
				outputs = append(outputs, result.Output)
				if result.ParsedData != nil {
					parsed = append(parsed, result.ParsedData)
					hasParsed = true
				}
			}
			e.state.Variables[name] = outputs
			if hasParsed {
				e.state.Variables[name+"_parsed"] = parsed
			}
		}
	}
}

func storeSingleParallelOutputVar(state *ExecutionState, name string, result StepResult) {
	if name == "" || result.Status != StatusCompleted {
		return
	}
	state.Variables[name] = result.Output
	if result.ParsedData != nil {
		state.Variables[name+"_parsed"] = result.ParsedData
	}
}

func lastCompletedParallelResult(indices []int, results []StepResult, completionOrder []string) (StepResult, bool) {
	indexByID := make(map[string]int, len(indices))
	indexSet := make(map[int]struct{}, len(indices))
	for _, index := range indices {
		indexSet[index] = struct{}{}
		if id := results[index].StepID; id != "" {
			indexByID[id] = index
		}
	}

	for i := len(completionOrder) - 1; i >= 0; i-- {
		index, ok := indexByID[completionOrder[i]]
		if !ok {
			continue
		}
		if results[index].Status == StatusCompleted {
			return results[index], true
		}
	}

	ordered := append([]int(nil), indices...)
	sort.Ints(ordered)
	for i := len(ordered) - 1; i >= 0; i-- {
		index := ordered[i]
		if _, ok := indexSet[index]; ok && results[index].Status == StatusCompleted {
			return results[index], true
		}
	}
	return StepResult{}, false
}
