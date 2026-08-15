#!/usr/bin/env bash
# test-slb-approval.sh (bd-cx733): shell runner for the SLB two-person
# approval workflow Go E2E suite (slb_approval_e2e_test.go). Discoverable by
# e2e/e2e-runner.sh and runnable standalone. The Go suite is hermetic (temp
# HOME for ~/.ntm/policy.yaml, temp NTM_CONFIG dir for state.db) and creates
# at most one short-lived tmux session on the isolated E2E server, which it
# kills itself; this wrapper adds JSONL step logging, tees the full go test
# output into e2e/logs/, and verifies no slb-e2e-* sessions leaked onto the
# shared default tmux server.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-slb-approval
trap 'e2e_finish "$?"' EXIT

GO_RUN_PATTERN='TestSLBApproval'
GO_TIMEOUT="${E2E_SLB_APPROVAL_GO_TIMEOUT:-600s}"

mkdir -p "$SCRIPT_DIR/logs"
tee_log="$SCRIPT_DIR/logs/test-slb-approval-$(date -u +%Y%m%dT%H%M%SZ)-$$.log"

log_step_start vet_slb_approval_suite
(cd "$E2E_REPO_ROOT" && go vet -tags e2e ./e2e/) 2>&1 | tee -a "$tee_log"
log_step_end vet_slb_approval_suite

log_step_start slb_approval_go_suite
log_info slb_approval_go_suite "running Go E2E suite" 0 \
	run_pattern "$GO_RUN_PATTERN" timeout "$GO_TIMEOUT" tee_log "$tee_log" ntm_bin "$E2E_NTM_BIN"
started="$(date +%s)"
set +e
(
	cd "$E2E_REPO_ROOT" &&
		E2E_LOG_DIR="$E2E_TEST_LOG_DIR" E2E_NTM_BIN="$E2E_NTM_BIN" \
			go test -tags e2e -count=1 -run "$GO_RUN_PATTERN" -timeout "$GO_TIMEOUT" -v ./e2e/
) 2>&1 | tee -a "$tee_log"
status=${PIPESTATUS[0]}
set -e
finished="$(date +%s)"
duration_ms=$(( (finished - started) * 1000 ))
log_info slb_approval_go_suite "go test finished" "$duration_ms" exit_code "$status" tee_log "$tee_log"
if (( status != 0 )); then
	e2e_fail "SLB approval Go E2E suite failed (exit $status); see $tee_log"
fi
log_assertion_pass "SLB approval Go E2E suite passed"
log_step_end slb_approval_go_suite "$duration_ms"

log_step_start leaked_session_check
# The suite's only tmux session lives on the isolated E2E server and is
# killed in-test. Prove nothing leaked onto the user's shared default server.
leaked="$(TMUX_TMPDIR= TMUX= "$E2E_REAL_TMUX" list-sessions -F '#{session_name}' 2>/dev/null | grep '^slb-e2e-' || true)"
if [[ -n "$leaked" ]]; then
	e2e_fail "leaked slb-e2e-* sessions on the default tmux server: $leaked"
fi
log_assertion_pass "no leaked slb-e2e-* sessions on the default tmux server"
log_step_end leaked_session_check
