package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// roundOverridesCtxKey scopes per-iteration round/rounds_remaining bindings
// onto a context.Context so parallel foreach iterations can each carry their
// own values without racing on shared state.Variables (bd-2ubxp.20).
type roundOverridesCtxKey struct{}

// withRoundOverrides returns a derived context that exposes the supplied
// round-binding overlay to substitution call sites. Pass nil to clear.
func withRoundOverrides(ctx context.Context, overrides map[string]interface{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, roundOverridesCtxKey{}, overrides)
}

// roundOverridesFromCtx returns the round-binding overlay attached to ctx, or
// nil if none. The map should be treated as read-only by callers.
func roundOverridesFromCtx(ctx context.Context) map[string]interface{} {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(roundOverridesCtxKey{}).(map[string]interface{})
	return v
}

// buildRoundOverrides constructs the overlay map for a single round of a
// foreach iteration. Keys mirror the historical pushRoundVars bindings:
// `round` / `rounds_remaining` (top-level shortcuts) and `loop.round` /
// `loop.rounds_remaining` (loop-namespaced form). Values are int so the
// substitutor's formatValue handles printing identically to the prior path.
func buildRoundOverrides(round, maxRounds int) map[string]interface{} {
	rem := maxRounds - round
	return map[string]interface{}{
		"round":                 round,
		"rounds_remaining":      rem,
		"loop.round":            round,
		"loop.rounds_remaining": rem,
	}
}

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

// Per-iteration round bindings are no longer written to state.Variables.
// Instead, the round loop in executeForeachIteration derives a child ctx
// via withRoundOverrides; substitution helpers consult that ctx and pass
// the overlay to Substitutor.SetLocalOverrides. This keeps parallel
// iterations from racing on shared state.Variables["round"] (bd-2ubxp.20).

// rewriteRoundStepIDs returns a copy of the body slice whose top-level step
// IDs are suffixed with `_round<N>`. This is the contract that keeps per-round
// state.Steps entries from clobbering each other (last-writer-wins erases
// earlier rounds' results otherwise).
//
// The copy is intentionally shallow — only `out[i].ID` is rewritten, and
// `out[i].Foreach`/`out[i].Loop` continue to point at the same backing
// configs. That is correct because the dispatchers that handle nested
// blocks chain the parent step's ID into every child step's state.Steps key:
//
//   - Foreach: materializeForeachSteps prefixes each materialized step ID with
//     `<parent.ID>_iter<N>_<child.ID>`, where parent.ID is the round-suffixed
//     value from this slice. (Verified by TestForeachMaxRounds_NestedForeach
//     KeepsRoundUnique.)
//   - Loop:   loops.go does `step.ID + "_iter" + N + "_" + nested.ID`, same
//     chaining rule.
//
// Branch and Parallel dispatchers do NOT chain parent.ID into nested keys
// today (see executeBranch / executeParallel), so a Branch or Parallel block
// nested directly inside a max_rounds body would collide across rounds. That
// is a separate concern from rewriteRoundStepIDs — fixing it requires
// teaching those dispatchers to namespace their children by parent.ID, not
// recursing here.
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
