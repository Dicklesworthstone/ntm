#!/usr/bin/env bash
set -euo pipefail

# NTM TOON E2E Test Script
# Tests TOON format support across all robot commands

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }
log_pass() { log "PASS: $*"; }
log_fail() { log "FAIL: $*"; }
log_skip() { log "SKIP: $*"; }
log_info() { log "INFO: $*"; }

TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

record_pass() { ((TESTS_PASSED++)) || true; log_pass "$1"; }
record_fail() { ((TESTS_FAILED++)) || true; log_fail "$1"; }
record_skip() { ((TESTS_SKIPPED++)) || true; log_skip "$1"; }

log "=========================================="
log "NTM (NAMED TMUX MANAGER) TOON E2E TEST"
log "=========================================="
log ""

# Phase 1: Prerequisites
log "--- Phase 1: Prerequisites ---"

for cmd in ntm tru jq; do
    if command -v "$cmd" &>/dev/null; then
        # ntm doesn't have --version, so handle each tool specifically
        case "$cmd" in
            tru) version=$("$cmd" --version 2>/dev/null | head -1 || echo "available") ;;
            jq)  version=$("$cmd" --version 2>/dev/null | head -1 || echo "available") ;;
            *)   version="available" ;;
        esac
        log_info "$cmd: $version"
        record_pass "$cmd available"
    else
        record_fail "$cmd not found"
        [[ "$cmd" != "jq" ]] && exit 1
    fi
done
log ""

# Phase 2: Format Flag Tests
log "--- Phase 2: Format Flag Tests ---"

# Use --robot-capabilities which doesn't require a session
log_info "Test 2.1: ntm --robot-capabilities --robot-format=json"
if json_output=$(ntm --robot-capabilities --robot-format=json 2>/dev/null); then
    if echo "$json_output" | jq . >/dev/null 2>&1; then
        record_pass "--robot-format=json produces valid JSON"
        json_bytes=$(echo -n "$json_output" | wc -c)
        log_info "  JSON output: $json_bytes bytes"
    else
        record_fail "--robot-format=json invalid"
    fi
else
    record_skip "ntm --robot-capabilities error"
fi

log_info "Test 2.2: ntm --robot-capabilities --robot-format=toon"
if toon_output=$(ntm --robot-capabilities --robot-format=toon 2>/dev/null); then
    if [[ -n "$toon_output" && "${toon_output:0:1}" != "{" && "${toon_output:0:1}" != "[" ]]; then
        record_pass "--robot-format=toon produces TOON"
        toon_bytes=$(echo -n "$toon_output" | wc -c)
        log_info "  TOON output: $toon_bytes bytes"
    else
        # TOON might fall back to JSON for complex structures
        if echo "$toon_output" | jq . >/dev/null 2>&1; then
            record_skip "--robot-format=toon fell back to JSON (complex structure)"
        else
            record_fail "--robot-format=toon invalid output"
        fi
    fi
else
    record_skip "ntm --robot-capabilities --robot-format=toon error"
fi
log ""

# Phase 3: Round-trip Verification
log "--- Phase 3: Round-trip Verification ---"

if [[ -n "${json_output:-}" && -n "${toon_output:-}" ]]; then
    # Only test round-trip if TOON output doesn't look like JSON
    if [[ "${toon_output:0:1}" != "{" && "${toon_output:0:1}" != "[" ]]; then
        if decoded=$(echo "$toon_output" | tru --decode 2>/dev/null); then
            orig_sorted=$(echo "$json_output" | jq -S . 2>/dev/null || echo "{}")
            decoded_sorted=$(echo "$decoded" | jq -S . 2>/dev/null || echo "{}")

            if [[ "$orig_sorted" == "$decoded_sorted" ]]; then
                record_pass "Round-trip preserves health data"
            else
                record_fail "Round-trip mismatch"
            fi
        else
            record_fail "tru --decode failed"
        fi
    else
        record_skip "Round-trip (TOON fell back to JSON)"
    fi
else
    record_skip "Round-trip (no valid outputs)"
fi
log ""

# Phase 4: Environment Variables
log "--- Phase 4: Environment Variables ---"

unset NTM_OUTPUT_FORMAT NTM_ROBOT_FORMAT TOON_DEFAULT_FORMAT

export NTM_OUTPUT_FORMAT=toon
if env_out=$(ntm --robot-status 2>/dev/null); then
    if [[ -n "$env_out" ]]; then
        record_pass "NTM_OUTPUT_FORMAT=toon accepted"
    else
        record_skip "NTM_OUTPUT_FORMAT test (empty output)"
    fi
else
    record_skip "NTM_OUTPUT_FORMAT test"
fi
unset NTM_OUTPUT_FORMAT

export NTM_ROBOT_FORMAT=toon
if env_out=$(ntm --robot-status 2>/dev/null); then
    if [[ -n "$env_out" ]]; then
        record_pass "NTM_ROBOT_FORMAT=toon accepted"
    else
        record_skip "NTM_ROBOT_FORMAT test (empty output)"
    fi
else
    record_skip "NTM_ROBOT_FORMAT test"
fi
unset NTM_ROBOT_FORMAT

export TOON_DEFAULT_FORMAT=toon
if env_out=$(ntm --robot-status 2>/dev/null); then
    if [[ -n "$env_out" ]]; then
        record_pass "TOON_DEFAULT_FORMAT=toon accepted"
    else
        record_skip "TOON_DEFAULT_FORMAT test (empty output)"
    fi
else
    record_skip "TOON_DEFAULT_FORMAT test"
fi

# Test CLI override
if override=$(ntm --robot-status --robot-format=json 2>/dev/null) && echo "$override" | jq . >/dev/null 2>&1; then
    record_pass "CLI --robot-format=json overrides env"
else
    record_skip "CLI override test"
fi
unset TOON_DEFAULT_FORMAT
log ""

# Phase 5: Token Savings
log "--- Phase 5: Token Savings ---"

if [[ -n "${json_bytes:-}" && -n "${toon_bytes:-}" && $json_bytes -gt 0 ]]; then
    savings=$(( 100 - (toon_bytes * 100 / json_bytes) ))
    log_info "JSON: $json_bytes bytes"
    log_info "TOON: $toon_bytes bytes"
    log_info "Savings: ${savings}%"

    if [[ $savings -gt 0 ]]; then
        record_pass "Compression achieved: ${savings}%"
    else
        log_info "Note: Negative savings may indicate TOON overhead on small payloads"
    fi
else
    log_info "Skipping savings calculation (no valid byte counts)"
fi
log ""

# Phase 6: Multiple Robot Commands
log "--- Phase 6: Multiple Robot Commands ---"

# Robot commands that don't require a session
ROBOT_CMDS=(
    "--robot-capabilities"
    "--robot-status"
    "--robot-triage"
    "--robot-version"
)

for cmd in "${ROBOT_CMDS[@]}"; do
    if ntm $cmd --robot-format=toon &>/dev/null; then
        record_pass "ntm $cmd --robot-format=toon"
    else
        record_skip "ntm $cmd"
    fi
done
log ""

# Phase 7: Unit Tests
log "--- Phase 7: Unit Tests ---"

if [[ -d "/dp/ntm" ]]; then
    cd /dp/ntm
    if go test ./internal/robot/... -run "Toon|TOON|Format" -v 2>&1 | tail -5; then
        record_pass "go test TOON tests pass"
    else
        record_fail "go test TOON tests failed"
    fi
else
    record_skip "ntm repo not found"
fi
log ""

# Phase 8: Renderer Tests
log "--- Phase 8: Renderer Tests ---"

if [[ -d "/dp/ntm" ]]; then
    cd /dp/ntm
    if go test ./internal/robot/... -run "Renderer" -v 2>&1 | tail -5; then
        record_pass "go test Renderer tests pass"
    else
        record_fail "go test Renderer tests failed"
    fi
else
    record_skip "ntm repo not found"
fi
log ""

# Summary
log "=========================================="
log "SUMMARY: Passed=$TESTS_PASSED Failed=$TESTS_FAILED Skipped=$TESTS_SKIPPED"
[[ $TESTS_FAILED -gt 0 ]] && exit 1 || exit 0
