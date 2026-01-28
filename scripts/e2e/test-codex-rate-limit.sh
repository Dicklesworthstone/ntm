#!/usr/bin/env bash
# E2E Test: Codex rate limit cooldown gating
# Verifies that rate limit output updates the tracker and subsequent spawns wait for cooldown.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/log.sh"

TEST_PREFIX="e2e-codex-rl-$$"
CREATED_SESSIONS=()
E2E_TAG="[E2E-CODEX-RL]"
BEAD_ID="bd-3qoly"

cleanup() {
    log_section "Cleanup"
    for session in "${CREATED_SESSIONS[@]}"; do
        tmux kill-session -t "$session" 2>/dev/null || true
    done
}
trap cleanup EXIT

setup_env() {
    BASE_DIR="$(mktemp -d)"
    PROJECTS_DIR="${BASE_DIR}/projects"
    BIN_DIR="${BASE_DIR}/bin"
    CONFIG_PATH="${BASE_DIR}/config.toml"
    STUB_BIN="${BIN_DIR}/codex-stub"

    mkdir -p "$PROJECTS_DIR" "$BIN_DIR"

cat <<EOF >"$STUB_BIN"
#!/usr/bin/env bash
echo "Error: rate limit exceeded. Retry-After: 10"
# Keep process alive so the monitor has time to observe output.
sleep 30
EOF
    chmod +x "$STUB_BIN"

    cat <<EOF >"$CONFIG_PATH"
projects_base = "${PROJECTS_DIR}"

[agents]
claude = "cc"
codex = "${STUB_BIN}"
gemini = "gmi"

[resilience]
health_check_seconds = 1
rate_limit = { detect = true, notify = false }
EOF

    export NTM_CONFIG="$CONFIG_PATH"
    export NTM_PROJECTS_BASE="$PROJECTS_DIR"
}

main() {
    log_init "test-codex-rate-limit"

    require_ntm
    require_tmux

    setup_env
    log_info "${E2E_TAG} bead=${BEAD_ID} config=${NTM_CONFIG} projects_base=${NTM_PROJECTS_BASE}"

    local session="${TEST_PREFIX}-cooldown"
    CREATED_SESSIONS+=("$session")

    log_section "Spawn codex stub to generate rate-limit history"
    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} step=spawn_initial"
    if ! ntm_spawn "$session" --cod 1 --no-user; then
        log_error "Failed to spawn session with codex stub"
        return 1
    fi

    local rate_limits="${PROJECTS_DIR}/${session}/.ntm/rate_limits.json"
    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} step=await_tracker path=${rate_limits}"
    if wait_for 20 "rate limit tracker file" test -f "$rate_limits"; then
        log_assert_eq "yes" "yes" "rate_limits.json created"
    else
        log_assert_eq "no" "yes" "rate_limits.json created"
        return 1
    fi

    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} step=await_rate_limit_record"
    if wait_for 20 "rate limit event recorded" grep -Eq '"total_rate_limits": [1-9]' "$rate_limits"; then
        log_assert_eq "yes" "yes" "rate limit event recorded"
    else
        log_assert_eq "no" "yes" "rate limit event recorded"
        return 1
    fi

    local content
    content=$(cat "$rate_limits")
    log_assert_contains "$content" "\"openai\"" "rate limit tracker includes openai provider"
    log_assert_contains "$content" "\"cooldown_until\"" "rate limit tracker includes cooldown"

    tmux kill-session -t "$session" 2>/dev/null || true

    log_section "Spawn again and verify cooldown wait"
    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} step=spawn_cooldown"
    local output
    local exit_code=0
    output=$(echo "y" | ntm spawn "$session" --cod 1 --no-user 2>&1) || exit_code=$?

    log_assert_eq "$exit_code" "0" "spawn completes successfully after cooldown"
    log_assert_contains "$output" "Codex cooldown active; waiting" "spawn honors codex cooldown"
    log_info "${E2E_TAG} bead=${BEAD_ID} session=${session} step=spawn_complete"

    log_summary
}

main "$@"
