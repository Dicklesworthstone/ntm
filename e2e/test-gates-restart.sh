#!/usr/bin/env bash
# test-gates-restart.sh (bd-y0l7u) — shell runner for the interactive-gate /
# blocked-health / smart-restart-decline / restart-prompt-gating Go E2E suite
# (gates_restart_e2e_test.go). Follows the test-send.sh + e2e-runner.sh
# conventions: sources the shared logging library, emits JSONL step events,
# captures diagnostics on failure, and additionally tees the full `go test`
# output into e2e/logs/ alongside the per-scenario TestLogger logs.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-gates-restart
trap 'e2e_finish "$?"' EXIT

# Persistent human-readable logs live beside the suite (e2e/logs/), in
# addition to the runner's JSONL event stream in $E2E_TEST_LOG_DIR.
GATES_LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$GATES_LOG_DIR"
GATES_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
GATES_GO_LOG="$GATES_LOG_DIR/test-gates-restart-$GATES_STAMP.log"

log_step_start preflight
if ! "$E2E_REAL_TMUX" -V >/dev/null 2>&1; then
	e2e_fail 'tmux is required for the gates/restart E2E suite'
fi
go version >/dev/null 2>&1 || e2e_fail 'go toolchain is required'
log_assertion_pass 'tmux and go toolchains present'
log_step_end preflight

# The Go suite isolates its own tmux server (TestMain) and builds its own
# fixture + ntm binaries; E2E_NTM_BIN (built by e2e_test_setup, or inherited
# from the caller per e2e-runner.sh) is forwarded so the Go tests exercise the
# same binary as the shell suite. Per-scenario TestLogger files land in
# e2e/logs/ via E2E_LOG_DIR.
log_step_start go_e2e_gates_restart
gates_started="$(date +%s)"
set +e
(
	cd "$E2E_REPO_ROOT" &&
		E2E_LOG_DIR="$GATES_LOG_DIR" \
		E2E_NTM_BIN="${E2E_NTM_BIN:-}" \
		go test -tags e2e -count=1 -timeout 900s \
			-run 'TestGatesE2E' -v ./e2e/
) 2>&1 | tee "$GATES_GO_LOG"
gates_status=${PIPESTATUS[0]}
set -e
gates_finished="$(date +%s)"
gates_duration_ms=$(((gates_finished - gates_started) * 1000))
log_json INFO go_e2e_gates_restart 'go test completed' "$gates_duration_ms" \
	exit_code "$gates_status" go_log "$GATES_GO_LOG"
if ((gates_status != 0)); then
	e2e_fail "go E2E gates/restart suite failed (see $GATES_GO_LOG)"
fi
log_assertion_pass 'go E2E gates/restart suite passed'
log_step_end go_e2e_gates_restart "$gates_duration_ms"

# Leak check: the Go suite runs on an isolated tmux server that TestMain tears
# down; verify no gates sessions leaked onto THIS runner's tmux server either.
log_step_start leak_check
leaked="$("$E2E_REAL_TMUX" list-sessions -F '#{session_name}' 2>/dev/null | grep -E '^ntm-e2e-(gates|fake)-' || true)"
if [[ -n "$leaked" ]]; then
	e2e_fail "leaked tmux sessions after gates E2E: $leaked"
fi
log_assertion_pass 'no leaked ntm-e2e gates sessions'
log_step_end leak_check
