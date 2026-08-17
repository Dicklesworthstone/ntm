#!/usr/bin/env bash
# test-guard-hook.sh — runner for the pre-commit guard hook E2E proof
# (bd-ws1-truth-safety-l5ddi.1) in e2e/guard_hook_e2e_test.go.
#
# The scenarios need real temp git repos, an httptest fake Agent Mail server,
# and state-DB row assertions, which live in Go; this runner builds the ntm
# binary and executes that suite (forced-fallback install, conflicting
# reservation blocks naming the holder, no-conflict pass, unreachable-server
# fail-open with WARN + degraded-event row, NTM_GUARD_STRICT=1 fail-closed,
# doctor surfacing, installed-script placebo pin).
#
# Usage: ./e2e/test-guard-hook.sh [extra go-test args...]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

RUN_PATTERN='TestGuardHookFallbackE2E'
RUN_PATTERN+='|TestGuardHookInstalledScriptPinned'

exec go test -tags e2e -count=1 -timeout 600s -run "$RUN_PATTERN" -v ./e2e/ "$@"
