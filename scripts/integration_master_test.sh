#!/usr/bin/env bash
# Master Integration Test Suite: Full NTM System Flow
# Implements bd-ak7b: end-to-end system flow with phased logging + JSON report.

set -uo pipefail
# Intentionally not using -e so we can log and record failures per phase.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/e2e/lib/log.sh"
set +e

TEST_ID="integration-001"
TEST_NAME="integration_master_test"
TEST_PREFIX="e2e-master-$$"

E2E_LOG_DIR="${E2E_LOG_DIR:-/tmp/ntm-e2e-logs}"
export E2E_LOG_DIR
mkdir -p "$E2E_LOG_DIR"

REPORT_DIR="$E2E_LOG_DIR"
REPORT_FILE="${REPORT_DIR}/integration_master_report_$(date -u +%Y%m%d_%H%M%S).json"

PHASE_NAMES=()
PHASE_STATUS=()
PHASE_DURATIONS=()

_current_phase=""
_current_phase_start_ms=0

phase_start() {
    _current_phase="$1"
    _current_phase_start_ms=$(_millis)
    log_section "Phase: ${_current_phase}"
}

phase_end() {
    local status="$1"
    local end_ms
    end_ms=$(_millis)
    local duration_ms=$((end_ms - _current_phase_start_ms))

    PHASE_NAMES+=("${_current_phase}")
    PHASE_STATUS+=("${status}")
    PHASE_DURATIONS+=("${duration_ms}")

    if [[ "$status" == "pass" ]]; then
        log_info "Phase passed: ${_current_phase} (${duration_ms}ms)"
    elif [[ "$status" == "skip" ]]; then
        log_skip "Phase skipped: ${_current_phase}"
    else
        log_error "Phase failed: ${_current_phase} (${duration_ms}ms)"
    fi

    # Count each phase as an assertion to surface failures in summary.
    log_assert_eq "$status" "pass" "phase ${_current_phase}"
}

write_report_json() {
    local total=${#PHASE_NAMES[@]}
    local passed=0
    local failed=0

    for i in "${!PHASE_STATUS[@]}"; do
        case "${PHASE_STATUS[$i]}" in
            pass)
                ((passed++)) || true
                ;;
            *)
                ((failed++)) || true
                ;;
        esac
    done

    local duration_ms
    duration_ms=$(_elapsed_ms)
    local duration_seconds=$((duration_ms / 1000))

    mkdir -p "$REPORT_DIR"

    {
        echo "{"
        echo "  \"test_id\": \"${TEST_ID}\","
        echo "  \"timestamp\": \"$(_timestamp)\","
        echo "  \"duration_seconds\": ${duration_seconds},"
        echo "  \"phases\": ["
        local first=1
        for i in "${!PHASE_NAMES[@]}"; do
            if [[ $first -eq 0 ]]; then
                echo ","
            fi
            first=0
            printf "    {\"name\": \"%s\", \"status\": \"%s\", \"duration_ms\": %s}" \
                "${PHASE_NAMES[$i]}" "${PHASE_STATUS[$i]}" "${PHASE_DURATIONS[$i]}"
        done
        echo ""
        echo "  ],"
        echo "  \"summary\": {\"total\": ${total}, \"passed\": ${passed}, \"failed\": ${failed}}"
        echo "}"
    } > "$REPORT_FILE"

    log_info "Wrote report: $REPORT_FILE"
    log_assert_valid_json "$(cat "$REPORT_FILE")" "report JSON is valid"
}

require_br() {
    if ! command -v br &>/dev/null; then
        log_error "br not found in PATH"
        return 1
    fi
    return 0
}

cleanup() {
    log_section "Cleanup"
    if [[ -n "${SESSION_NAME:-}" ]]; then
        log_exec ntm kill "$SESSION_NAME" --force
    fi

    if [[ -n "${PROJECTS_BASE:-}" && -d "${PROJECTS_BASE}" ]]; then
        log_info "Leaving projects base for inspection: ${PROJECTS_BASE}"
        log_info "Manual cleanup required (deletion disabled by safety policy)"
    fi
}
trap cleanup EXIT

main() {
    log_init "$TEST_NAME"

    require_ntm
    require_tmux
    require_jq

    if [[ -z "${NTM_BIN:-}" ]]; then
        NTM_BIN="/tmp/ntm-master-bin-${TEST_PREFIX}"
        log_section "Build"
        log_info "Building ntm binary: ${NTM_BIN}"
        if ! log_exec go build -o "$NTM_BIN" ./cmd/ntm; then
            log_error "Failed to build ntm binary; aborting test run"
            log_summary
            return 1
        fi
    fi
    export PATH="$(dirname "$NTM_BIN"):$PATH"

    PROJECTS_BASE=$(mktemp -d -t ntm-master-e2e-XXXX)
    export NTM_PROJECTS_BASE="$PROJECTS_BASE"

    PROJECT_NAME="${TEST_PREFIX}-project"
    SESSION_NAME="$PROJECT_NAME"
    PROJECT_DIR="${PROJECTS_BASE}/${PROJECT_NAME}"
    mkdir -p "$PROJECT_DIR"
    printf "E2E master integration test\n" > "${PROJECT_DIR}/README.md"

    # Phase 1: Project Initialization
    phase_start "init"
    local status="pass"
    if log_exec ntm init "$PROJECT_DIR" --non-interactive --no-hooks --force; then
        if [[ ! -f "${PROJECT_DIR}/.ntm/config.toml" ]]; then
            status="fail"
            log_error "config.toml missing after init"
        fi
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 2: Template-based Spawn
    phase_start "spawn_template"
    status="pass"
    if log_exec ntm spawn "$SESSION_NAME" --template review-pipeline --no-user; then
        if ! tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
            status="fail"
            log_error "tmux session not found after spawn"
        fi
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 3: CM Context Loading
    phase_start "context_build"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm context build --task "Master integration test" --agent cc --bead "${TEST_PREFIX}" --files README.md --verbose --json; then
            local output
            output="$(get_last_output)"
            log_assert_valid_json "$output" "context build JSON"
        else
            status="fail"
        fi
        popd >/dev/null || true
    else
        status="fail"
        log_error "failed to enter project dir: ${PROJECT_DIR}"
    fi
    phase_end "$status"

    # Phase 4: Task Assignment
    phase_start "task_assignment"
    status="pass"
    if require_br; then
        if pushd "$PROJECT_DIR" >/dev/null; then
            if ! log_exec br init --json; then
                status="fail"
            fi
            if log_exec br create "E2E master integration task" -t task -p 2 --json; then
                if ! log_exec ntm assign "$SESSION_NAME" --auto --limit 1 --strategy round-robin --json; then
                    status="fail"
                else
                    local output
                    output="$(get_last_output)"
                    log_assert_valid_json "$output" "assign output JSON"
                fi
            else
                status="fail"
            fi
            popd >/dev/null || true
        else
            status="fail"
            log_error "failed to enter project dir: ${PROJECT_DIR}"
        fi
    else
        status="skip"
    fi
    phase_end "$status"

    # Phase 5: File Reservation
    phase_start "file_reservation"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm --json lock "$SESSION_NAME" "README.md" --reason "master integration test" --ttl 5m; then
            local output
            output="$(get_last_output)"
            if ! echo "$output" | jq -e '.success == true' >/dev/null 2>&1; then
                status="fail"
            fi
            if ! log_exec ntm --json unlock "$SESSION_NAME" --all; then
                status="fail"
            fi
        else
            local output
            output="$(get_last_output)"
            if echo "$output" | grep -qi "agent mail"; then
                status="skip"
            else
                status="fail"
            fi
        fi
        popd >/dev/null || true
    else
        status="fail"
        log_error "failed to enter project dir: ${PROJECT_DIR}"
    fi
    phase_end "$status"

    # Phase 6: Agent Communication (Agent Mail)
    phase_start "agent_communication"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm mail send "$SESSION_NAME" --all --subject "E2E master integration" "Agent mail test message"; then
            if log_exec ntm mail inbox "$SESSION_NAME" --json --limit 5; then
                local output
                output="$(get_last_output)"
                log_assert_valid_json "$output" "mail inbox JSON"
            else
                status="fail"
            fi
        else
            local output
            output="$(get_last_output)"
            if echo "$output" | grep -qi "agent mail"; then
                status="skip"
            else
                status="fail"
            fi
        fi
        popd >/dev/null || true
    else
        status="fail"
        log_error "failed to enter project dir: ${PROJECT_DIR}"
    fi
    phase_end "$status"

    # Phase 7: Context Monitoring
    phase_start "context_monitoring"
    status="pass"
    if log_exec ntm status "$SESSION_NAME" --json; then
        local output
        output="$(get_last_output)"
        log_assert_valid_json "$output" "status JSON"
        if ! echo "$output" | jq -e '.panes | length > 0' >/dev/null 2>&1; then
            status="fail"
        fi
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 8: Cost Tracking (Quota)
    phase_start "cost_tracking"
    status="pass"
    if log_exec ntm --json quota "$SESSION_NAME"; then
        local output
        output="$(get_last_output)"
        log_assert_valid_json "$output" "quota JSON"
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 9: Staggered Operations
    phase_start "staggered_operations"
    status="pass"
    local stagger_session="${TEST_PREFIX}-stagger"
    if log_exec ntm --json spawn "$stagger_session" --cc=2 --no-user --stagger-mode=fixed --stagger-delay=1s --prompt "stagger-test"; then
        local output
        output="$(get_last_output)"
        log_assert_valid_json "$output" "stagger spawn JSON"
        if ! echo "$output" | jq -e '.stagger.enabled == true' >/dev/null 2>&1; then
            status="fail"
        fi
        log_exec ntm --json kill "$stagger_session" --force || status="fail"
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 10: Handoff
    phase_start "handoff"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm handoff create "$SESSION_NAME" --goal "Master integration test" --now "Proceed to remaining phases" --json; then
            local output
            output="$(get_last_output)"
            log_assert_valid_json "$output" "handoff JSON"
        else
            status="fail"
        fi
        popd >/dev/null || true
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 11: Prompt History
    phase_start "prompt_history"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm --json send "$SESSION_NAME" --all "history test prompt"; then
            local output
            output="$(get_last_output)"
            log_assert_valid_json "$output" "send JSON"
            if log_exec ntm history --session "$SESSION_NAME" --limit 5 --json; then
                output="$(get_last_output)"
                log_assert_valid_json "$output" "history JSON"
                if ! echo "$output" | jq -e '.entries | length >= 1' >/dev/null 2>&1; then
                    status="fail"
                fi
            else
                status="fail"
            fi
        else
            status="fail"
        fi
        popd >/dev/null || true
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 12: Session Summarization
    phase_start "session_summarization"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm summary "$SESSION_NAME" --format json; then
            local output
            output="$(get_last_output)"
            log_assert_valid_json "$output" "summary JSON"
        else
            status="fail"
        fi
        popd >/dev/null || true
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 13: Output Archive (CASS)
    phase_start "output_archive"
    status="pass"
    if log_exec ntm --json cass status; then
        local output
        output="$(get_last_output)"
        log_assert_valid_json "$output" "cass status JSON"
        if echo "$output" | jq -e '.error == "cass_not_installed"' >/dev/null 2>&1; then
            status="skip"
        fi
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 14: Effectiveness Scoring
    phase_start "effectiveness_scoring"
    status="pass"
    if log_exec ntm --json agents stats; then
        local output
        output="$(get_last_output)"
        log_assert_valid_json "$output" "agents stats JSON"
    else
        status="fail"
    fi
    phase_end "$status"

    # Phase 15: Session Recovery
    phase_start "session_recovery"
    status="pass"
    if pushd "$PROJECT_DIR" >/dev/null; then
        if log_exec ntm resume "$SESSION_NAME" --dry-run --json; then
            local output
            output="$(get_last_output)"
            log_assert_valid_json "$output" "resume JSON"
        else
            status="fail"
        fi
        popd >/dev/null || true
    else
        status="fail"
    fi
    phase_end "$status"

    write_report_json

    local exit_code=0
    log_summary || exit_code=$?

    # Emit report JSON last so the runner can parse it.
    cat "$REPORT_FILE"

    return $exit_code
}

main "$@"
