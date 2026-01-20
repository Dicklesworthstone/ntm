#!/usr/bin/env bash
set -euo pipefail

log_e2e() {
    local test="$1"
    local step="$2"
    local status="$3"
    local details="$4"
    echo "[E2E-ROUNDROBIN] $(date -Iseconds) test=${test} step=${step} status=${status} details=\"${details}\""
}

require_cmd() {
    local cmd="$1"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        log_e2e "$TEST_NAME" "prereq" "fail" "missing=${cmd}"
        exit 1
    fi
}

TEST_NAME="round_robin_distribute"
SESSION="e2e-rr-${RANDOM}-$$"

TMP_ROOT="$(mktemp -d)"
PROJECTS_BASE="${TMP_ROOT}/projects"
CONFIG_DIR="${TMP_ROOT}/config"
CONFIG_PATH="${CONFIG_DIR}/config.toml"
PROJECT_DIR="${PROJECTS_BASE}/${SESSION}"

cleanup() {
    tmux kill-session -t "${SESSION}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "${PROJECTS_BASE}" "${CONFIG_DIR}"

cat > "${CONFIG_PATH}" <<EOF
projects_base = "${PROJECTS_BASE}"

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"
EOF

require_cmd ntm
require_cmd tmux
require_cmd br
require_cmd jq

log_e2e "${TEST_NAME}" "spawn_agents" "start" "cc=2 cod=2 gmi=1"
echo "y" | ntm --config "${CONFIG_PATH}" spawn "${SESSION}" --cc=2 --cod=2 --gmi=1 >/dev/null

sleep 1

agent_pane_count="$(tmux list-panes -t "${SESSION}" -F '#{pane_title}' | grep -E '__(cc|cod|gmi)_' | wc -l | tr -d ' ')"
if [[ "${agent_pane_count}" != "5" ]]; then
    log_e2e "${TEST_NAME}" "spawn_agents" "fail" "expected=5 got=${agent_pane_count}"
    exit 1
fi
log_e2e "${TEST_NAME}" "spawn_agents" "complete" "panes=${agent_pane_count}"

mkdir -p "${PROJECT_DIR}"
if [[ ! -d "${PROJECT_DIR}/.beads" ]]; then
    (cd "${PROJECT_DIR}" && br init >/dev/null)
fi

log_e2e "${TEST_NAME}" "create_beads" "start" "count=10"
bead_ids=()
for i in $(seq 1 10); do
    out="$(cd "${PROJECT_DIR}" && br create "RR Test ${i}" -t task -p 2 --json)"
    bead_id="$(echo "${out}" | jq -r '.id // .bead_id // .beadID // .beadId // empty')"
    if [[ -z "${bead_id}" ]]; then
        log_e2e "${TEST_NAME}" "create_beads" "fail" "index=${i} reason=missing_id"
        exit 1
    fi
    bead_ids+=("${bead_id}")
done
log_e2e "${TEST_NAME}" "create_beads" "complete" "count=${#bead_ids[@]}"

log_e2e "${TEST_NAME}" "assign" "start" "strategy=round-robin limit=10"
assign_json="$(cd "${PROJECT_DIR}" && ntm --config "${CONFIG_PATH}" --json assign "${SESSION}" --strategy=round-robin --limit=10)"
echo "${assign_json}" | jq . >/dev/null
assigned_count="$(echo "${assign_json}" | jq '.assigned | length')"
log_e2e "${TEST_NAME}" "assign" "complete" "assigned=${assigned_count} agents=5"

if [[ "${assigned_count}" != "10" ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "assigned=${assigned_count} expected=10"
    exit 1
fi

summary_assigned="$(echo "${assign_json}" | jq '.summary.assigned')"
summary_idle="$(echo "${assign_json}" | jq '.summary.idle_agents')"
if [[ "${summary_assigned}" != "10" ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "summary_assigned=${summary_assigned} expected=10"
    exit 1
fi
if [[ "${summary_idle}" != "5" ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "summary_idle_agents=${summary_idle} expected=5"
    exit 1
fi

mapfile -t pane_lines < <(tmux list-panes -t "${SESSION}" -F '#{pane_index} #{pane_title}' | sort -n -k1)
agent_panes=()
agent_labels=()
declare -A pane_to_label

for line in "${pane_lines[@]}"; do
    pane_index="${line%% *}"
    pane_title="${line#* }"
    if [[ "${pane_title}" =~ __cc_ || "${pane_title}" =~ __cod_ || "${pane_title}" =~ __gmi_ ]]; then
        label="${pane_title#${SESSION}__}"
        label="${label%%[*}"
        pane_to_label["${pane_index}"]="${label}"
        agent_panes+=("${pane_index}")
        agent_labels+=("${label}")
    fi
done

if [[ "${#agent_panes[@]}" -ne 5 ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "agent_panes=${#agent_panes[@]} expected=5"
    exit 1
fi

mapfile -t assigned_panes < <(echo "${assign_json}" | jq -r '.assigned[].pane')
mapfile -t assigned_ids < <(echo "${assign_json}" | jq -r '.assigned[].bead_id')

if [[ "${#assigned_ids[@]}" -ne 10 ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "assigned_ids=${#assigned_ids[@]} expected=10"
    exit 1
fi

dup_ids="$(printf "%s\n" "${assigned_ids[@]}" | sort | uniq -d)"
if [[ -n "${dup_ids}" ]]; then
    log_e2e "${TEST_NAME}" "verify" "fail" "duplicate_beads=${dup_ids}"
    exit 1
fi

declare -A counts
for i in "${!assigned_panes[@]}"; do
    expected_pane="${agent_panes[$((i % ${#agent_panes[@]}))]}"
    actual_pane="${assigned_panes[$i]}"
    if [[ "${actual_pane}" != "${expected_pane}" ]]; then
        log_e2e "${TEST_NAME}" "verify" "fail" "index=$((i+1)) expected_pane=${expected_pane} actual_pane=${actual_pane}"
        exit 1
    fi
    counts["${actual_pane}"]=$((counts["${actual_pane}"] + 1))
done

for idx in "${!agent_panes[@]}"; do
    pane="${agent_panes[$idx]}"
    label="${agent_labels[$idx]}"
    log_e2e "${TEST_NAME}" "verify" "checking" "agent=${label} expected=2"
    count="${counts[$pane]:-0}"
    if [[ "${count}" != "2" ]]; then
        log_e2e "${TEST_NAME}" "verify" "fail" "agent=${label} count=${count} expected=2"
        exit 1
    fi
done

log_e2e "${TEST_NAME}" "verify" "pass" "all agents got exactly 2 beads"
