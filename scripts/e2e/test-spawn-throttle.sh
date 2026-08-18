#!/usr/bin/env bash
# E2E Test: Spawn pacing (bd-azzp0)
# Validates spawn throttling behavior. (The scheduler backoff phases were
# removed with internal/scheduler in the v1.27.0 dead-code sweep.)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/log.sh"

E2E_TAG="[E2E-SPAWN-THROTTLE]"
BEAD_ID="bd-azzp0"
TEST_PREFIX="e2e-spawn-throttle-$$"
CREATED_SESSIONS=()

cleanup() {
    log_section "Cleanup"
    for session in "${CREATED_SESSIONS[@]}"; do
        ntm_cleanup "$session"
    done
    cleanup_sessions "${TEST_PREFIX}"
}
trap cleanup EXIT

main() {
    log_init "test-spawn-throttle"

    require_ntm
    require_tmux
    require_jq

    log_section "Spawn pacing (pane creation)"
    test_spawn_pacing

    log_summary
}

test_spawn_pacing() {
    local session="${TEST_PREFIX}-pace"
    local agent_count=6
    local pane_delay_ms="${NTM_TEST_SPAWN_PANE_DELAY_MS:-200}"
    local agent_delay_ms="${NTM_TEST_SPAWN_AGENT_DELAY_MS:-150}"
    local log_dir="${E2E_LOG_DIR:-${SCRIPT_DIR}/logs}"

    CREATED_SESSIONS+=("$session")

    export NTM_TEST_MODE=1
    export NTM_TEST_SPAWN_PANE_DELAY_MS="$pane_delay_ms"
    export NTM_TEST_SPAWN_AGENT_DELAY_MS="$agent_delay_ms"

    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} pane_delay_ms=${pane_delay_ms} agent_delay_ms=${agent_delay_ms}"

    local spawn_log="${log_dir}/${session}_spawn.log"
    local start_ms
    start_ms=$(_millis)

    (echo "y" | ntm spawn "$session" --cc "${agent_count}" >"$spawn_log" 2>&1) &
    local spawn_pid=$!

    if ! wait_for 15 "session exists" tmux has-session -t "$session"; then
        log_error "${E2E_TAG} session=${session} step=wait_session failed"
        wait "$spawn_pid" || true
        return 1
    fi

    local last_count
    last_count=$(tmux list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')

    while kill -0 "$spawn_pid" 2>/dev/null; do
        if tmux has-session -t "$session" 2>/dev/null; then
            local count
            count=$(tmux list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')
            if [[ "$count" -gt "$last_count" ]]; then
                local ts
                ts=$(_millis)
                log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} pane_count=${count} ts_ms=${ts}"
                last_count="$count"
            fi
        fi
        sleep 0.05
    done

    local exit_code=0
    wait "$spawn_pid" || exit_code=$?
    local end_ms
    end_ms=$(_millis)
    local duration_ms=$((end_ms - start_ms))

    log_info "${E2E_TAG} session=${session} spawn_exit=${exit_code} duration_ms=${duration_ms}"
    log_assert_eq "$exit_code" "0" "spawn exit code is 0"

    local pane_count
    pane_count=$(tmux list-panes -t "$session" -F '#{pane_id}' | wc -l | tr -d ' ')
    log_assert_eq "$pane_count" "$((agent_count + 1))" "spawn created expected pane count"

    if grep -qiE "EAGAIN|resource temporarily unavailable" "$spawn_log"; then
        log_assert_eq "EAGAIN" "none" "spawn output has no EAGAIN errors"
    else
        log_assert_eq "none" "none" "spawn output has no EAGAIN errors"
    fi

    local expected_min=$(((agent_count - 1) * (pane_delay_ms + agent_delay_ms)))
    local min_ok=$((expected_min * 7 / 10))
    log_info "${E2E_TAG} session=${session} expected_min_ms=${expected_min} min_ok_ms=${min_ok}"

    if [[ "$duration_ms" -ge "$min_ok" ]]; then
        log_assert_eq "pass" "pass" "spawn duration respects pacing"
    else
        log_assert_eq "fail" "pass" "spawn duration respects pacing"
    fi

    local split_times=()
    if [[ -f "$spawn_log" ]]; then
        while IFS= read -r ts; do
            split_times+=("$ts")
        done < <(grep "event=pane_split" "$spawn_log" | sed -E 's/.*ts_ms=([0-9]+).*/\1/')
    fi

    if [[ "${#split_times[@]}" -ge 2 ]]; then
        local min_spacing=999999
        for ((i = 1; i < ${#split_times[@]}; i++)); do
            local diff=$((split_times[i] - split_times[i-1]))
            if [[ "$diff" -lt "$min_spacing" ]]; then
                min_spacing="$diff"
            fi
        done
        local expected_spacing=$((pane_delay_ms * 7 / 10))
        log_info "${E2E_TAG} session=${session} min_spacing_ms=${min_spacing} expected_spacing_ms=${expected_spacing}"
        if [[ "$min_spacing" -ge "$expected_spacing" ]]; then
            log_assert_eq "pass" "pass" "pane spacing respects pacing"
        else
            log_assert_eq "fail" "pass" "pane spacing respects pacing"
        fi
    else
        log_assert_eq "${#split_times[@]}" "2" "pane split timestamps recorded"
    fi
}

main "$@"
