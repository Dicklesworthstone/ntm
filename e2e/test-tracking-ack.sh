#!/usr/bin/env bash
# test-tracking-ack.sh — thin runner for the tracked-send / robot-ack live E2E
# suite (ntm-qce2 + ntm-g70w) in e2e/send_tracking_ack_e2e_test.go.
#
# Unlike the bash-native test-*.sh suites, the tracking/ack scenarios need the
# fakeagent fixture's JSONL ground truth and envelope-level JSON assertions,
# which live in Go. This runner just builds and executes that suite against an
# isolated tmux server (the suite's TestMain handles isolation and cleanup).
#
# Usage: ./e2e/test-tracking-ack.sh [extra go-test args...]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

RUN_PATTERN='TestRobotSendTrackConfirmsAgainstFakeagent'
RUN_PATTERN+='|TestRobotSendTrackTimeoutPendingPane'
RUN_PATTERN+='|TestRobotSendTrackRejectsOpID'
RUN_PATTERN+='|TestRobotSendPanesFilterLiveReproof'
RUN_PATTERN+='|TestRobotAckDetectsFixtureOutput'
RUN_PATTERN+='|TestRobotAckTimeoutExpiry'
RUN_PATTERN+='|TestRobotAckMultiPanePartial'
RUN_PATTERN+='|TestRobotAckMalformedPaneSelector'
RUN_PATTERN+='|TestRobotAckEchoDetectionVsPlainMode'

exec go test -tags e2e -count=1 -timeout 900s -run "$RUN_PATTERN" -v ./e2e/ "$@"
