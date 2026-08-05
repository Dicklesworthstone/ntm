#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-spawn
trap e2e_cleanup EXIT

session="e2e-spawn-$$"
log_step_start spawn_single_agent
e2e_spawn "$session" --cc=1:test-model
"$E2E_REAL_TMUX" has-session -t "$session"
pane_count="$("$E2E_REAL_TMUX" list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')"
[[ "$pane_count" == "1" ]] || e2e_fail "single-agent spawn created one pane"
log_assertion_pass 'single-agent spawn created one pane'
status_json="$("$E2E_NTM_BIN" status "$session" --json)"
assert_contains "$status_json" "$session" 'status JSON identifies spawned session'
log_step_end spawn_single_agent

multi_session="e2e-spawn-multi-$$"
log_step_start spawn_mixed_agents
e2e_spawn "$multi_session" --cc=2 --cod=1 --gmi=1
multi_count="$("$E2E_REAL_TMUX" list-panes -t "$multi_session" -F '#{pane_id}' | wc -l | tr -d ' ')"
[[ "$multi_count" == "4" ]] || e2e_fail "mixed spawn created four agent panes"
log_assertion_pass 'mixed spawn created four agent panes'
log_step_end spawn_mixed_agents

log_step_start spawn_duplicate_rejected
assert_command_fails 'spawning an existing session is rejected' "$E2E_NTM_BIN" spawn "$session" --no-user --cc=1 --create-dir --no-cass-context --no-recovery --no-hooks
log_step_end spawn_duplicate_rejected

log_step_start spawn_stress
stress_session="e2e-spawn-stress-$$"
e2e_spawn "$stress_session" --cc=10
stress_count="$("$E2E_REAL_TMUX" list-panes -t "$stress_session" -F '#{pane_id}' | wc -l | tr -d ' ')"
[[ "$stress_count" == "10" ]] || e2e_fail 'ten-agent spawn created ten panes'
log_assertion_pass 'ten-agent spawn created ten panes'
log_step_end spawn_stress
