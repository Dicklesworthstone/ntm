#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-send
trap 'e2e_finish "$?"' EXIT

session="e2e-send-$$"
e2e_spawn "$session" --cc=1 --cod=1

log_step_start send_to_one_pane
message="pane-target-$$"
"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$message"
sleep 1
assert_pane_contains "$session:0.0" "FAKE_AGENT_RECEIVED:$message"
log_step_end send_to_one_pane

log_step_start send_to_all_agents
broadcast="broadcast-$$"
"$E2E_NTM_BIN" send "$session" --all --no-cass-check --force-non-interactive "$broadcast"
sleep 1
assert_pane_contains "$session:0.0" "FAKE_AGENT_RECEIVED:$broadcast"
assert_pane_contains "$session:0.1" "FAKE_AGENT_RECEIVED:$broadcast"
log_step_end send_to_all_agents

log_step_start send_special_characters
special_message=$'quotes " and newline\nsecond line'
"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$special_message"
sleep 1
assert_pane_contains "$session:0.0" 'FAKE_AGENT_RECEIVED:quotes " and newline'
assert_pane_contains "$session:0.0" 'FAKE_AGENT_RECEIVED:second line'
log_step_end send_special_characters

log_step_start invalid_target_rejected
assert_command_fails 'invalid pane selector is rejected' "$E2E_NTM_BIN" send "$session" --pane=99 --no-cass-check --force-non-interactive ignored
log_step_end invalid_target_rejected
