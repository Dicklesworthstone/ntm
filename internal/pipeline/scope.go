package pipeline

import "strings"

type scopedValue struct {
	value  interface{}
	exists bool
}

// VariableScope records the previous values for a narrow set of variables so
// loop and branch bodies can shadow them and then restore the outer context.
type VariableScope struct {
	values map[string]scopedValue
}

// CaptureVariableScope snapshots only the requested keys. Callers are
// responsible for holding the appropriate variable lock.
func CaptureVariableScope(vars map[string]interface{}, keys ...string) VariableScope {
	scope := VariableScope{values: make(map[string]scopedValue, len(keys))}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		value, exists := vars[key]
		scope.values[key] = scopedValue{value: value, exists: exists}
	}
	return scope
}

// Restore puts a previously captured scope back into vars. Callers are
// responsible for holding the appropriate variable lock.
func (s VariableScope) Restore(vars map[string]interface{}) {
	for key, prev := range s.values {
		if prev.exists {
			vars[key] = prev.value
		} else {
			delete(vars, key)
		}
	}
}

func loopScopeKeys(varName string) []string {
	keys := []string{
		"loop." + varName,
		"loop.item",
		"loop.index",
		"loop.count",
		"loop.first",
		"loop.last",
	}
	return dedupeScopeKeys(keys)
}

func dedupeScopeKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func captureAllVariables(vars map[string]interface{}) map[string]interface{} {
	if vars == nil {
		return nil
	}
	snapshot := make(map[string]interface{}, len(vars))
	for k, v := range vars {
		snapshot[k] = v
	}
	return snapshot
}

// restoreBranchVariables undoes a branch body's variable writes without
// disturbing writes made concurrently by sibling steps.
//
// A whole-map restore is wrong for a branch: it is reachable from a `parallel:
// true` foreach body, so replacing the map with a pre-branch snapshot discarded
// every output_var and steps.<id>.* key a sibling iteration wrote while the
// branch body ran. This instead:
//
//   - reverts keys that existed in the snapshot to their snapshot value, and
//   - removes keys added since the snapshot only when the branch owns them,
//     meaning a steps.* key namespaced under one of the executed body step IDs,
//     or an output_var declared by the body.
//
// Anything else added during the body belongs to a concurrent sibling, or is
// deliberate global signaling such as runtime.*, and is left alone. Leaking a
// variable is recoverable; destroying a sibling's output is not.
func restoreBranchVariables(state *ExecutionState, snapshot map[string]interface{}, bodyStepIDs []string, ownedOutputVars map[string]struct{}) {
	if state == nil {
		return
	}
	if state.Variables == nil {
		if snapshot != nil {
			state.Variables = captureAllVariables(snapshot)
		}
		return
	}

	for key, prev := range snapshot {
		state.Variables[key] = prev
	}

	for key := range state.Variables {
		if _, existed := snapshot[key]; existed {
			continue
		}
		if branchOwnsVariable(key, bodyStepIDs, ownedOutputVars) {
			delete(state.Variables, key)
		}
	}
}

// branchOwnsVariable reports whether a variable key was created by the branch
// body rather than by a concurrently running sibling.
func branchOwnsVariable(key string, bodyStepIDs []string, ownedOutputVars map[string]struct{}) bool {
	if _, ok := ownedOutputVars[key]; ok {
		return true
	}
	if !strings.HasPrefix(key, "steps.") {
		return false
	}
	for _, id := range bodyStepIDs {
		if id == "" {
			continue
		}
		prefix := "steps." + id
		if key == prefix || strings.HasPrefix(key, prefix+".") || strings.HasPrefix(key, prefix+"_") {
			return true
		}
	}
	return false
}

// branchBodyOutputVars collects the output_var names a branch body declares, so
// the deferred restore can remove them without guessing.
func branchBodyOutputVars(steps []Step) map[string]struct{} {
	owned := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if name := strings.TrimSpace(step.OutputVar); name != "" {
			owned[name] = struct{}{}
		}
	}
	return owned
}

// extractRuntimeKeys returns a copy of every "runtime.*" entry plus the
// nested "runtime" map (if any) in vars. Used so branch / scoped blocks
// can persist on_failure runtime-action signals across a snapshot
// restore (bd-afwly).
func extractRuntimeKeys(vars map[string]interface{}) map[string]interface{} {
	if len(vars) == 0 {
		return nil
	}
	out := make(map[string]interface{})
	for k, v := range vars {
		if k == "runtime" || strings.HasPrefix(k, "runtime.") {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeRuntimeKeys writes runtime.* entries from extra into state.Variables,
// preserving any existing nested "runtime" map by merging child keys.
func mergeRuntimeKeys(state *ExecutionState, extra map[string]interface{}) {
	if state == nil || len(extra) == 0 {
		return
	}
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}
	for k, v := range extra {
		if k == "runtime" {
			incoming, _ := v.(map[string]interface{})
			if incoming == nil {
				continue
			}
			existing, _ := state.Variables["runtime"].(map[string]interface{})
			if existing == nil {
				existing = make(map[string]interface{})
				state.Variables["runtime"] = existing
			}
			for ck, cv := range incoming {
				existing[ck] = cv
			}
			continue
		}
		state.Variables[k] = v
	}
}
