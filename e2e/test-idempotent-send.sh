#!/usr/bin/env bash
# test-idempotent-send.sh (bd-7gnyg): thin runner for the idempotent-send
# E2E scenarios (--op-id claim/replay/conflict/takeover + receipts). The
# scenarios themselves live in e2e/idempotent_send_e2e_test.go behind the
# e2e build tag; this script wires them into the shell E2E runner's
# discovery, log layout, and summary conventions.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The Go tests skip themselves without tmux, but e2e_test_setup hard-fails;
# skip cleanly here so runs on tmux-less hosts do not count as failures.
if ! command -v tmux >/dev/null 2>&1; then
	printf '%s\n' 'SKIP: tmux is not available; idempotent-send E2E requires a real tmux server.'
	exit 0
fi
if ! command -v go >/dev/null 2>&1; then
	printf '%s\n' 'SKIP: go toolchain is not available.'
	exit 0
fi

source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-idempotent-send
trap 'e2e_finish "$?"' EXIT

# The Go harness honors E2E_LOG_DIR (NewTestLogger) and E2E_NTM_BIN
# (ensureE2ENTMBin); route both at the artifacts of THIS run so per-scenario
# structured logs land next to the runner's events.jsonl.
export E2E_LOG_DIR="$E2E_TEST_LOG_DIR/scenarios"
mkdir -p "$E2E_LOG_DIR"
log_info setup "go scenario logs routed" 0 log_dir "$E2E_LOG_DIR" ntm_bin "$E2E_NTM_BIN"

log_step_start go_vet
vet_started="$(date +%s)"
set +e
(cd "$E2E_REPO_ROOT" && go vet -tags e2e ./e2e/) 2>&1 | tee "$E2E_TEST_LOG_DIR/go-vet.log"
vet_status=${PIPESTATUS[0]}
set -e
vet_finished="$(date +%s)"
log_step_end go_vet $(( (vet_finished - vet_started) * 1000 ))
if (( vet_status != 0 )); then
	e2e_fail "go vet -tags e2e ./e2e/ failed (exit=$vet_status); see $E2E_TEST_LOG_DIR/go-vet.log"
fi
log_assertion_pass 'go vet -tags e2e ./e2e/ is clean'

log_step_start go_test_idempotent_send
test_started="$(date +%s)"
set +e
(cd "$E2E_REPO_ROOT" && go test -tags e2e -count=1 -run 'TestIdempotentSend' -timeout 600s -v ./e2e/) 2>&1 | tee "$E2E_TEST_LOG_DIR/go-test.log"
test_status=${PIPESTATUS[0]}
set -e
test_finished="$(date +%s)"
log_step_end go_test_idempotent_send $(( (test_finished - test_started) * 1000 ))
if (( test_status != 0 )); then
	e2e_fail "idempotent-send Go E2E scenarios failed (exit=$test_status); see $E2E_TEST_LOG_DIR/go-test.log and $E2E_LOG_DIR"
fi
log_assertion_pass 'all idempotent-send scenarios passed (claim, replay, conflict, topology replay, takeover, preflight release)'
