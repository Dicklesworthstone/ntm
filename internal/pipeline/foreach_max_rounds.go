package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// roundOverridesCtxKey scopes iteration bindings onto a context.Context so
// parallel foreach rounds and loops carry their own values without writing
// shared state.Variables.
type roundOverridesCtxKey struct{}

// withRoundOverrides returns a derived context with an immutable merged
// iteration overlay. Nested loops/rounds shadow only their own keys, preserving
// outer aliases and round bindings. Pass nil to clear the overlay.
func withRoundOverrides(ctx context.Context, overrides map[string]interface{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if overrides == nil {
		return context.WithValue(ctx, roundOverridesCtxKey{}, overrides)
	}
	parent := roundOverridesFromCtx(ctx)
	merged := make(map[string]interface{}, len(parent)+len(overrides))
	for key, value := range parent {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return context.WithValue(ctx, roundOverridesCtxKey{}, merged)
}

// roundOverridesFromCtx returns the iteration overlay attached to ctx, or nil
// if none. The map must be treated as read-only by callers.
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
// Returns 1 when MaxRounds is unset, set to 0, or resolves through an
// expression to a non-positive integer — the single-round historical
// default. A substitution failure or a non-integer expression result still
// returns a fail-closed error so misconfigured expressions don't silently
// degrade (bd-2ubxp.14).
//
// bd-wapme: literal `max_rounds: 0` and expression-resolved `0` are now
// treated identically (both → single round). They previously diverged:
// literal 0 silently became 1, while expression 0 erroed loudly. The
// "consistent error" alternative would have required a presence flag on
// IntOrExpr to disambiguate "explicit 0" from "field omitted", which
// breaks reflect.DeepEqual round-trip equality used by the json/toml
// schema tests. Authors who want zero-rounds-as-error can gate via a
// `when:` clause on the parent step.
//
// The expression form ("${defaults.hard_caps.foo}", "${vars.cap}", etc.) is
// resolved against the executor's substitutor with workflow defaults applied,
// matching LoopExecutor.resolveIntOrExpr's contract for max_iterations.
//
// bd-ltghx: a resolved value above the configured cap is clamped to it
// with a Warn-level slog event so a misconfigured `max_rounds:
// ${vars.from_external}` cannot drive the body loop unbounded. Literal
// values are NOT clamped — the workflow author chose them explicitly and
// parser validation has already rejected negative literals; if a workflow
// genuinely needs more rounds it can spell that out with an integer.
//
// bd-iz5hd: the cap is sourced from `limits.max_foreach_rounds` (default
// DefaultMaxRounds=100), so operators with a legitimate need >100 can raise
// it in workflow settings without rewriting expression-driven workflows
// into literals.
func (e *Executor) resolveForeachMaxRounds(ctx context.Context, parent *Step) (int, error) {
	fc := parent.Foreach
	if fc == nil {
		fc = parent.ForeachPane
	}
	if fc == nil {
		return 1, nil
	}
	mr := fc.MaxRounds
	// bd-wapme: an unset MaxRounds and a literal `max_rounds: 0` are
	// indistinguishable at the IntOrExpr layer (both produce
	// IntOrExpr{Value: 0, Expr: ""}) so they share the single-round
	// default. Negative literals are rejected at parse time
	// (parser.go::validateStep) — this defensive `<= 0` branch matches
	// the historical contract.
	if mr.Expr == "" && mr.Value <= 0 {
		return 1, nil
	}
	if mr.Expr == "" {
		return mr.Value, nil
	}

	// bd-8wo27: lock order is stateMu before varMu (matches the canonical
	// pattern in executor.go applyStartFrom). The previous order
	// (varMu→stateMu) created an AB-BA deadlock window against any
	// goroutine using the canonical order — sync.RWMutex's writer-
	// starvation guard would block a concurrent stateMu.RLock() behind a
	// pending stateMu.Lock(), at the same time the canonical-order writer
	// was waiting on varMu we held. Holding stateMu first matches every
	// other call site that nests the two locks, so a future caller cannot
	// reintroduce the cycle without also flipping executor.go.
	e.stateMu.RLock()
	e.varMu.RLock()
	workflowID := ""
	if e.state != nil {
		workflowID = e.state.WorkflowID
	}
	sub := NewSubstitutor(e.state, e.config.Session, workflowID)
	sub.SetDefaults(e.defaults)
	sub.SetMaxDepth(e.limits.MaxSubstitutionDepth)
	sub.SetLocalOverrides(roundOverridesFromCtx(ctx))
	resolved, subErr := sub.SubstituteStrict(e.substituteRuntimeVariables(mr.Expr))
	e.varMu.RUnlock()
	e.stateMu.RUnlock()

	if subErr != nil {
		return 0, fmt.Errorf("resolve max_rounds expression %q: %w", mr.Expr, subErr)
	}
	parsed, parseErr := strconv.Atoi(strings.TrimSpace(resolved))
	if parseErr != nil {
		return 0, fmt.Errorf("resolve max_rounds expression %q: parse %q as integer: %w", mr.Expr, resolved, parseErr)
	}
	// bd-wapme: a non-positive expression result is treated identically to
	// a literal `max_rounds: 0` — silently default to a single round. The
	// previous error-on-zero behaviour diverged from the literal path and
	// surprised authors refactoring `max_rounds: 0` into
	// `max_rounds: ${vars.zero}`. A non-integer (parseErr above) still
	// fails closed because that's a configuration mistake, not a
	// semantically-equivalent edge case.
	if parsed <= 0 {
		slog.Debug("foreach.max_rounds expression resolved to non-positive value, defaulting to 1 round",
			"run_id", e.runIDForLog(),
			"step_id", parent.ID,
			"agent_type", "foreach",
			"expression", mr.Expr,
			"resolved", parsed,
		)
		return 1, nil
	}
	roundsCap := e.limits.MaxForeachRounds
	if roundsCap <= 0 {
		roundsCap = DefaultMaxRounds
	}
	if parsed > roundsCap {
		slog.Warn("foreach.max_rounds clamped to safety cap",
			"run_id", e.runIDForLog(),
			"step_id", parent.ID,
			"agent_type", "foreach",
			"requested", parsed,
			"cap", roundsCap,
			"expression", mr.Expr,
			"hint", "raise limits.max_foreach_rounds in workflow settings to opt into a higher cap, or use a literal integer max_rounds",
		)
		parsed = roundsCap
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
//   - OnSuccess: runOnSuccessSteps derives `<parent.ID>_on_success_<child.ID>`
//     (or `<parent.ID>_on_success_<N>` for anonymous children), so explicit
//     on_success IDs inside a foreach body inherit the materialized parent ID.
//   - Branch and Parallel: executeBranch / executeParallel rewrite child IDs
//     through scopedChildStepID, so child state keys inherit the materialized
//     parent ID before they are stored.
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
