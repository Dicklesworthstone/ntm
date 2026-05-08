package pipeline

import (
	"fmt"
	"strconv"
	"strings"
)

// resolveForeachMaxRounds returns the resolved max_rounds for a foreach step.
// Returns 1 when MaxRounds is unset (single round, the historical default
// behavior). An explicit literal or expression that fails to resolve to a
// positive integer returns an error so the iteration fails closed (bd-2ubxp.14).
//
// The expression form ("${defaults.hard_caps.foo}", "${vars.cap}", etc.) is
// resolved against the executor's substitutor with workflow defaults applied,
// matching LoopExecutor.resolveIntOrExpr's contract for max_iterations.
func (e *Executor) resolveForeachMaxRounds(parent *Step) (int, error) {
	fc := parent.Foreach
	if fc == nil {
		fc = parent.ForeachPane
	}
	if fc == nil {
		return 1, nil
	}
	mr := fc.MaxRounds
	if mr.Expr == "" && mr.Value <= 0 {
		return 1, nil
	}
	if mr.Expr == "" {
		return mr.Value, nil
	}

	e.varMu.RLock()
	e.stateMu.RLock()
	workflowID := ""
	if e.state != nil {
		workflowID = e.state.WorkflowID
	}
	sub := NewSubstitutor(e.state, e.config.Session, workflowID)
	sub.SetDefaults(e.defaults)
	sub.SetMaxDepth(e.limits.MaxSubstitutionDepth)
	resolved, subErr := sub.SubstituteStrict(e.substituteRuntimeVariables(mr.Expr))
	e.stateMu.RUnlock()
	e.varMu.RUnlock()

	if subErr != nil {
		return 0, fmt.Errorf("resolve max_rounds expression %q: %w", mr.Expr, subErr)
	}
	parsed, parseErr := strconv.Atoi(strings.TrimSpace(resolved))
	if parseErr != nil {
		return 0, fmt.Errorf("resolve max_rounds expression %q: parse %q as integer: %w", mr.Expr, resolved, parseErr)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("resolve max_rounds expression %q: value %d must be > 0", mr.Expr, parsed)
	}
	return parsed, nil
}

// pushRoundVars exposes ${round} (1-based) and ${rounds_remaining} as
// loop-local variables for an iteration's body steps. The values are stored
// under both bare ("round") and loop-namespaced ("loop.round") keys so
// authors can reference either ${round} or ${loop.round}, matching the dual
// shape used for ${item} / ${loop.item}.
//
// Returns a release function that restores the previous bindings (or removes
// the keys entirely if they were absent before). Safe to call exactly once
// (matches the pushPaneMetadataVars / popPaneMetadataVars contract).
func (e *Executor) pushRoundVars(round, maxRounds int) func() {
	if e == nil || e.state == nil {
		return func() {}
	}
	e.varMu.Lock()
	if e.state.Variables == nil {
		e.state.Variables = make(map[string]interface{})
	}
	keys := []string{"round", "rounds_remaining", "loop.round", "loop.rounds_remaining"}
	prev := make(map[string]interface{}, len(keys))
	had := make(map[string]bool, len(keys))
	for _, k := range keys {
		if v, ok := e.state.Variables[k]; ok {
			prev[k] = v
			had[k] = true
		}
	}
	e.state.Variables["round"] = round
	e.state.Variables["rounds_remaining"] = maxRounds - round
	e.state.Variables["loop.round"] = round
	e.state.Variables["loop.rounds_remaining"] = maxRounds - round
	e.varMu.Unlock()

	return func() {
		if e == nil || e.state == nil {
			return
		}
		e.varMu.Lock()
		defer e.varMu.Unlock()
		for _, k := range keys {
			if had[k] {
				e.state.Variables[k] = prev[k]
			} else {
				delete(e.state.Variables, k)
			}
		}
	}
}

// rewriteRoundStepIDs deep-clones an iteration's body steps and suffixes each
// step's ID with `_round<N>` so per-round results land under unique keys in
// state.Steps. Without this, last-writer-wins erases earlier rounds' results
// from state.Steps even though iterResult.Results preserves order.
func rewriteRoundStepIDs(steps []Step, round int) []Step {
	if len(steps) == 0 {
		return steps
	}
	out := make([]Step, len(steps))
	for i := range steps {
		out[i] = steps[i]
		if out[i].ID != "" {
			out[i].ID = fmt.Sprintf("%s_round%d", out[i].ID, round)
		}
	}
	return out
}
