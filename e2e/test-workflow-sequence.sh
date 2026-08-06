#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-workflow-sequence
trap 'e2e_finish "$?"' EXIT

session="e2e-workflow-sequence-$$"
e2e_spawn "$session" --cc=2

sequence_project="$E2E_TEST_TMPDIR/projects/workflow-sequence"
mkdir -p "$sequence_project"
pane_one="$("$E2E_REAL_TMUX" list-panes -t "$session" -F '#{pane_id}' | sed -n '1p')"
pane_two="$("$E2E_REAL_TMUX" list-panes -t "$session" -F '#{pane_id}' | sed -n '2p')"
[[ -n "$pane_one" && -n "$pane_two" ]] || e2e_fail 'two panes are available for independent sequence progress'
log_assertion_pass 'two panes are available for independent sequence progress'

pushd "$sequence_project" >/dev/null

log_step_start sequence_create
created="$($E2E_NTM_BIN --robot-sequence=review --sequence-action=create --sequence-steps='["inspect","challenge","summarize"]')"
assert_contains "$created" '"success":true' 'sequence creation succeeds'
assert_contains "$created" '"name":"review"' 'sequence creation identifies the durable sequence'
[[ -f '.ntm/workflows/sequences/review.json' ]] || e2e_fail 'sequence state is durable under project .ntm'
log_assertion_pass 'sequence state is durable under project .ntm'
log_step_end sequence_create

log_step_start sequence_independent_panes
first_next="$($E2E_NTM_BIN --robot-sequence=review --sequence-pane="$pane_one")"
assert_contains "$first_next" '"prompt":"inspect"' 'first pane receives its first prompt'
first_advanced="$($E2E_NTM_BIN --robot-sequence=review --sequence-action=advance --sequence-pane="$pane_one")"
assert_contains "$first_advanced" '"prompt":"challenge"' 'first pane advances to its second prompt'
second_next="$($E2E_NTM_BIN --robot-sequence=review --sequence-pane="$pane_two")"
assert_contains "$second_next" '"prompt":"inspect"' 'second pane remains at its independent first prompt'
log_step_end sequence_independent_panes

log_step_start sequence_resume_and_completion
reloaded="$($E2E_NTM_BIN --robot-sequence=review --sequence-pane="$pane_one")"
assert_contains "$reloaded" '"prompt":"challenge"' 'new process reloads first pane position from durable state'
"$E2E_NTM_BIN" --robot-sequence=review --sequence-action=advance --sequence-pane="$pane_one" >/dev/null
completed="$($E2E_NTM_BIN --robot-sequence=review --sequence-action=advance --sequence-pane="$pane_one")"
assert_contains "$completed" '"complete":true' 'first pane reports sequence completion'
retry="$($E2E_NTM_BIN --robot-sequence=review --sequence-action=advance --sequence-pane="$pane_one")"
assert_contains "$retry" '"advanced":false' 'completed pane advance is idempotent'
log_step_end sequence_resume_and_completion

popd >/dev/null
