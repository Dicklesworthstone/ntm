#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-status
trap 'e2e_finish "$?"' EXIT

log_step_start status_missing_session
missing_session="e2e-status-missing-$$"
missing_json="$("$E2E_NTM_BIN" status "$missing_session" --json)"
assert_contains "$missing_json" "$missing_session" 'missing-session JSON identifies requested session'
assert_contains "$missing_json" 'false' 'missing-session JSON reports no session'
log_step_end status_missing_session

session="e2e-status-$$"
e2e_spawn "$session" --cc=1 --cod=1

log_step_start status_single_session
status_json="$("$E2E_NTM_BIN" status "$session" --json)"
assert_contains "$status_json" "$session" 'status JSON identifies active session'
assert_contains "$status_json" 'true' 'status JSON reports active session'
pane_count="$("$E2E_REAL_TMUX" list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')"
[[ "$pane_count" == "2" ]] || e2e_fail 'status fixture has two panes'
log_assertion_pass 'status fixture has two panes'
log_step_end status_single_session

log_step_start status_multiple_sessions
second_session="e2e-status-second-$$"
e2e_spawn "$second_session" --cc=1
first_status="$("$E2E_NTM_BIN" status "$session" --json)"
second_status="$("$E2E_NTM_BIN" status "$second_session" --json)"
assert_contains "$first_status" "$session" 'first session remains available after second spawn'
assert_contains "$second_status" "$second_session" 'second session status identifies requested session'
log_step_end status_multiple_sessions
