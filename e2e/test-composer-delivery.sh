#!/usr/bin/env bash
# test-composer-delivery.sh (bd-hy0f9): shell runner for the composer
# visibility / delivery readiness / submission rescue Go E2E suite
# (composer_delivery_e2e_test.go). Discoverable by e2e/e2e-runner.sh and
# runnable standalone. The Go suite drives the fakeagent fixture on an
# isolated tmux server it creates and tears down itself; this wrapper adds
# JSONL step logging, tees the full go test output into e2e/logs/, and
# verifies no fixture sessions leaked onto the shared default tmux server.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-composer-delivery
trap 'e2e_finish "$?"' EXIT

GO_RUN_PATTERN='TestComposerDelivery'
GO_TIMEOUT="${E2E_COMPOSER_GO_TIMEOUT:-900s}"

mkdir -p "$SCRIPT_DIR/logs"
tee_log="$SCRIPT_DIR/logs/test-composer-delivery-$(date -u +%Y%m%dT%H%M%SZ)-$$.log"

log_step_start vet_composer_suite
(cd "$E2E_REPO_ROOT" && go vet -tags e2e ./e2e/) 2>&1 | tee -a "$tee_log"
log_step_end vet_composer_suite

log_step_start composer_delivery_go_suite
log_info composer_delivery_go_suite "running Go E2E suite" 0 \
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
log_info composer_delivery_go_suite "go test finished" "$duration_ms" exit_code "$status" tee_log "$tee_log"
if (( status != 0 )); then
	e2e_fail "composer delivery Go E2E suite failed (exit $status); see $tee_log"
fi
log_assertion_pass "composer delivery Go E2E suite passed"
log_step_end composer_delivery_go_suite "$duration_ms"

log_step_start leaked_session_check
# The Go suite runs on its own isolated tmux server and kills it on exit.
# Prove nothing leaked onto the user's shared default server.
leaked="$(TMUX_TMPDIR= TMUX= "$E2E_REAL_TMUX" list-sessions -F '#{session_name}' 2>/dev/null | grep '^ntm-e2e-fake-' || true)"
if [[ -n "$leaked" ]]; then
	e2e_fail "leaked fakeagent sessions on the default tmux server: $leaked"
fi
log_assertion_pass "no leaked ntm-e2e-fake-* sessions on the default tmux server"
log_step_end leaked_session_check
