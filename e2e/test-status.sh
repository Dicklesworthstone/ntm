#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-status
trap 'e2e_finish "$?"' EXIT

assert_valid_json() {
	local json="$1"
	local description="$2"
	if ! jq -e . >/dev/null 2>&1 <<<"$json"; then
		e2e_fail "$description"
		return 1
	fi
	log_assertion_pass "$description"
}

assert_json_value() {
	local json="$1"
	local query="$2"
	local expected="$3"
	local description="$4"
	local actual
	if ! actual="$(jq -r "$query" <<<"$json" 2>/dev/null)"; then
		e2e_fail "$description (query failed: $query)"
		return 1
	fi
	if [[ "$actual" != "$expected" ]]; then
		e2e_fail "$description (expected $expected, got $actual)"
		return 1
	fi
	log_assertion_pass "$description"
}

log_step_start status_missing_session
missing_session="e2e-status-missing-$$"
missing_json="$("$E2E_NTM_BIN" status "$missing_session" --json)"
assert_valid_json "$missing_json" 'missing-session status is valid JSON'
assert_json_value "$missing_json" '.session' "$missing_session" 'missing-session JSON identifies requested session'
assert_json_value "$missing_json" '.exists' 'false' 'missing-session JSON reports no session'
log_step_end status_missing_session

session="e2e-status-$$"
e2e_spawn "$session" --cc=1 --cod=1 --gmi=1

log_step_start status_schema_and_agent_detection
status_json="$("$E2E_NTM_BIN" status "$session" --json)"
assert_valid_json "$status_json" 'active-session status is valid JSON'
assert_json_value "$status_json" 'has("generated_at")' 'true' 'status JSON has generated_at'
assert_json_value "$status_json" '.session' "$session" 'status JSON identifies active session'
assert_json_value "$status_json" '.exists' 'true' 'status JSON reports active session'
assert_json_value "$status_json" '.panes | type' 'array' 'status JSON panes is an array'
assert_json_value "$status_json" '.agent_counts | type' 'object' 'status JSON agent_counts is an object'
assert_json_value "$status_json" '.panes | length' '3' 'status JSON reports each spawned pane'
assert_json_value "$status_json" '.agent_counts.claude' '1' 'status JSON detects one Claude agent'
assert_json_value "$status_json" '.agent_counts.codex' '1' 'status JSON detects one Codex agent'
assert_json_value "$status_json" '.agent_counts.gemini' '1' 'status JSON detects one Gemini agent'
assert_json_value "$status_json" '.agent_counts.total' '3' 'status JSON agent total is accurate'
assert_json_value "$status_json" '[.panes[].type] | sort | join(",")' 'claude,codex,gemini' 'status JSON labels every agent type'
pane_count="$("$E2E_REAL_TMUX" list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')"
[[ "$pane_count" == "3" ]] || e2e_fail 'status fixture has three panes'
log_assertion_pass 'status fixture has three panes'
log_step_end status_schema_and_agent_detection

log_step_start status_text_table
status_text="$("$E2E_NTM_BIN" status "$session")"
assert_contains "$status_text" "$session" 'text status identifies active session'
assert_contains "$status_text" 'Panes' 'text status renders pane table'
assert_contains "$status_text" 'Claude' 'text status renders Claude label'
assert_contains "$status_text" 'Codex' 'text status renders Codex label'
assert_contains "$status_text" 'Gemini' 'text status renders Gemini label'
assert_contains "$status_text" 'Pane numbers are tmux selectors' 'text status documents pane selectors'
log_step_end status_text_table

log_step_start status_during_spawn
spawning_session="e2e-status-spawning-$$"
export NTM_TEST_SPAWN_PANE_DELAY_MS=250
"$E2E_NTM_BIN" spawn "$spawning_session" --no-user --create-dir --no-cass-context --no-recovery --no-hooks --cc=2 >/dev/null 2>&1 &
spawn_pid=$!
E2E_SESSIONS+=("$spawning_session")
sleep 0.1
spawning_json="$("$E2E_NTM_BIN" status "$spawning_session" --json)"
assert_valid_json "$spawning_json" 'status stays valid JSON while agents spawn'
assert_json_value "$spawning_json" '.session' "$spawning_session" 'spawn-in-progress status identifies requested session'
if ! wait "$spawn_pid"; then
	e2e_fail 'spawn-in-progress fixture completed successfully'
fi
export NTM_TEST_SPAWN_PANE_DELAY_MS=0
spawning_complete_json="$("$E2E_NTM_BIN" status "$spawning_session" --json)"
assert_json_value "$spawning_complete_json" '.exists' 'true' 'spawned session exists after agents finish starting'
assert_json_value "$spawning_complete_json" '.panes | length' '2' 'spawned session reports both panes after startup'
log_step_end status_during_spawn

log_step_start status_with_ansi_and_long_pane_output
long_output="$(printf '%4096s' '' | tr ' ' x)"
"$E2E_REAL_TMUX" send-keys -t "$session:0.0" -l $'\033[31m'"$long_output"$'\033[0m'
"$E2E_REAL_TMUX" send-keys -t "$session:0.0" Enter
sleep 0.1
ansi_json="$("$E2E_NTM_BIN" status "$session" --json)"
assert_valid_json "$ansi_json" 'status remains valid JSON after ANSI long pane output'
assert_json_value "$ansi_json" '.panes | length' '3' 'long pane output does not lose panes'
assert_json_value "$ansi_json" '.agent_counts.total' '3' 'long pane output preserves agent count'
log_step_end status_with_ansi_and_long_pane_output

log_step_start status_multiple_sessions
second_session="e2e-status-second-$$"
e2e_spawn "$second_session" --cc=2
first_status="$("$E2E_NTM_BIN" status "$session" --json)"
second_status="$("$E2E_NTM_BIN" status "$second_session" --json)"
assert_json_value "$first_status" '.session' "$session" 'first session remains available after second spawn'
assert_json_value "$first_status" '.agent_counts.total' '3' 'first session counts remain isolated'
assert_json_value "$second_status" '.session' "$second_session" 'second session status identifies requested session'
assert_json_value "$second_status" '.agent_counts.claude' '2' 'second session reports its own Claude agents'
assert_json_value "$second_status" '.panes | length' '2' 'second session reports its own panes'
log_step_end status_multiple_sessions

log_step_start status_after_pane_killed
"$E2E_REAL_TMUX" kill-pane -t "$second_session:0.1"
killed_pane_json="$("$E2E_NTM_BIN" status "$second_session" --json)"
assert_valid_json "$killed_pane_json" 'status remains valid JSON after a pane is killed'
assert_json_value "$killed_pane_json" '.exists' 'true' 'session remains after one of two panes is killed'
assert_json_value "$killed_pane_json" '.panes | length' '1' 'status excludes killed pane'
assert_json_value "$killed_pane_json" '.agent_counts.claude' '1' 'status updates Claude count after pane kill'
assert_json_value "$killed_pane_json" '.agent_counts.total' '1' 'status updates total after pane kill'
log_step_end status_after_pane_killed
