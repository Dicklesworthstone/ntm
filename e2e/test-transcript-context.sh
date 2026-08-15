#!/usr/bin/env bash
# E2E runner for transcript-sourced context accuracy (bd-2dqv5).
# Drives the Go E2E suite in e2e/transcript_context_e2e_test.go: hermetic
# fixture transcripts (Claude projects + Codex rollouts) under per-test temp
# HOMEs, fakeagent panes cwd'd to the correlated project dirs, and the real
# ntm binary queried via --robot-context / --robot-snapshot.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

e2e_test_setup test-transcript-context
trap 'e2e_finish "$?"' EXIT

# Persistent per-scenario Go test logs (NewTestLogger) plus the teed go test
# output live under e2e/logs/.
GO_LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$GO_LOG_DIR"
RUN_STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
GO_TEST_TEE="$GO_LOG_DIR/test-transcript-context-$RUN_STAMP.log"

log_step_start go_vet
(cd "$E2E_REPO_ROOT" && go vet -tags e2e ./e2e/)
log_step_end go_vet

log_step_start go_test_transcript_context
# E2E_NTM_BIN (built by e2e_test_setup) is honored by the Go suite's
# ensureE2ENTMBin; E2E_LOG_DIR routes NewTestLogger scenario logs to e2e/logs/.
(
	cd "$E2E_REPO_ROOT" &&
		E2E_LOG_DIR="$GO_LOG_DIR" go test -tags e2e -count=1 -timeout 600s \
			-run 'TestTranscriptContext' -v ./e2e/
) 2>&1 | tee "$GO_TEST_TEE"
log_step_end go_test_transcript_context

log_step_start leaked_session_check
# The Go suite runs on its own isolated tmux server and kills its sessions;
# verify nothing named for this suite leaked onto this script's server either.
leaked="$("$E2E_REAL_TMUX" list-sessions -F '#{session_name}' 2>/dev/null | grep -c '^ntm-e2e-tctx-' || true)"
if [[ "$leaked" != "0" ]]; then
	e2e_fail "leaked transcript-context tmux sessions: $leaked"
fi
log_assertion_pass 'no leaked ntm-e2e-tctx-* sessions'
log_step_end leaked_session_check

log_info summary "transcript-context E2E complete" 0 go_test_log "$GO_TEST_TEE"
