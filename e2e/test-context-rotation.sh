#!/usr/bin/env bash
# E2E runner for live context rotation (ntm-8ice, wired by bd-rpmg8).
# Drives the Go E2E suite in e2e/context_rotation_e2e_test.go: hermetic temp
# HOMEs with Claude transcript fixtures and per-scenario NTM_CONFIG files,
# cwd-pinned fakeagent panes, and the real ntm binary running
# `coordinator run --once` to exercise the transcript-usage rotation trigger,
# its safety gates, default-off behavior, auto-confirm execution, and the
# ambiguous-cwd refusal.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-context-rotation
trap 'e2e_finish "$?"' EXIT

# Persistent per-scenario Go test logs (NewTestLogger) plus the teed go test
# output live under e2e/logs/.
GO_LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$GO_LOG_DIR"
RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
GO_TEST_TEE="$GO_LOG_DIR/test-context-rotation-$RUN_STAMP.log"

log_step_start go_vet
(cd "$E2E_REPO_ROOT" && go vet -tags e2e ./e2e/)
log_step_end go_vet

log_step_start go_test_context_rotation
# E2E_NTM_BIN (built by e2e_test_setup) is honored by the Go suite's
# ensureE2ENTMBin; E2E_LOG_DIR routes NewTestLogger scenario logs to e2e/logs/.
(
	cd "$E2E_REPO_ROOT" &&
		E2E_LOG_DIR="$GO_LOG_DIR" go test -tags e2e -count=1 -timeout 900s \
			-run 'TestContextRotationE2E' -v ./e2e/
) 2>&1 | tee "$GO_TEST_TEE"
log_step_end go_test_context_rotation

log_step_start leaked_session_check
# The Go suite runs on its own isolated tmux server and kills its sessions;
# verify nothing named for this suite leaked onto this script's server either.
leaked="$("$E2E_REAL_TMUX" list-sessions -F '#{session_name}' 2>/dev/null | grep -c '^ntm-e2e-crot-' || true)"
if [[ "$leaked" != "0" ]]; then
	e2e_fail "leaked context-rotation tmux sessions: $leaked"
fi
log_assertion_pass 'no leaked ntm-e2e-crot-* sessions'
log_step_end leaked_session_check

log_info summary "context-rotation E2E complete" 0 go_test_log "$GO_TEST_TEE"
