#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-send
trap 'e2e_finish "$?"' EXIT

assert_pane_contains_within() {
	local pane_id="$1"
	local expected="$2"
	local description="$3"
	local content
	for ((attempt = 0; attempt < 20; attempt++)); do
		content="$("$E2E_REAL_TMUX" capture-pane -pJ -S - -t "$pane_id" 2>&1)" || {
			e2e_fail "capture pane $pane_id"
			return 1
		}
		if [[ "$content" == *"$expected"* ]]; then
			log_assertion_pass "$description"
			return 0
		fi
		sleep 0.05
	done
	e2e_fail "$description (message did not arrive within 1s)"
	return 1
}

assert_pane_lacks() {
	local pane_id="$1"
	local unexpected="$2"
	local description="$3"
	local content
	content="$("$E2E_REAL_TMUX" capture-pane -pJ -S - -t "$pane_id" 2>&1)" || {
		e2e_fail "capture pane $pane_id"
		return 1
	}
	if [[ "$content" == *"$unexpected"* ]]; then
		e2e_fail "$description (unexpected: $unexpected)"
		return 1
	fi
	log_assertion_pass "$description"
}

session="e2e-send-$$"
e2e_spawn "$session" --cc=1 --cod=1

log_step_start send_to_one_pane
message="pane-target-$$"
"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$message"
assert_pane_contains_within "$session:0.0" "FAKE_AGENT_RECEIVED:$message" 'specific-pane message arrives within 1s'
log_step_end send_to_one_pane

log_step_start send_to_agent_type
type_message="claude-target-$$"
"$E2E_NTM_BIN" send "$session" --cc --no-cass-check --force-non-interactive "$type_message"
assert_pane_contains_within "$session:0.0" "FAKE_AGENT_RECEIVED:$type_message" 'Claude type target receives message'
assert_pane_lacks "$session:0.1" "FAKE_AGENT_RECEIVED:$type_message" 'Codex pane is excluded from Claude type target'
log_step_end send_to_agent_type

log_step_start send_to_all_agents
broadcast="broadcast-$$"
"$E2E_NTM_BIN" send "$session" --all --no-cass-check --force-non-interactive "$broadcast"
assert_pane_contains_within "$session:0.0" "FAKE_AGENT_RECEIVED:$broadcast" 'broadcast reaches Claude pane within 1s'
assert_pane_contains_within "$session:0.1" "FAKE_AGENT_RECEIVED:$broadcast" 'broadcast reaches Codex pane within 1s'
log_step_end send_to_all_agents

log_step_start send_special_characters
special_message=$'quotes " and newline\nsecond line'
"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$special_message"
assert_pane_contains_within "$session:0.0" 'FAKE_AGENT_RECEIVED:quotes " and newline' 'quoted multiline message first line arrives'
assert_pane_contains_within "$session:0.0" 'FAKE_AGENT_RECEIVED:second line' 'quoted multiline message second line arrives'
log_step_end send_special_characters

log_step_start send_long_and_trailing_newline_messages
long_token="long-token-$$"
long_message=''
for ((index = 0; index < 63; index++)); do
	long_message+="$long_token"
done
long_message+=$'\n'
"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$long_message"
assert_pane_contains_within "$session:0.0" "FAKE_AGENT_RECEIVED:$long_token" 'long trailing-newline message arrives within 1s'
log_step_end send_long_and_trailing_newline_messages

log_step_start send_rapid_successive_messages
for index in 1 2 3; do
	rapid_message="rapid-$index-$$"
	"$E2E_NTM_BIN" send "$session" --pane=0 --no-cass-check --force-non-interactive "$rapid_message"
done
for index in 1 2 3; do
	assert_pane_contains_within "$session:0.0" "FAKE_AGENT_RECEIVED:rapid-$index-$$" "rapid message $index arrives"
done
log_step_end send_rapid_successive_messages

log_step_start invalid_target_rejected
assert_command_fails 'invalid pane selector is rejected' "$E2E_NTM_BIN" send "$session" --pane=99 --no-cass-check --force-non-interactive ignored
log_step_end invalid_target_rejected
