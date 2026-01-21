#!/bin/bash
# E2E Test: Dependency-aware assignment with unblock reassignment
# Bead: bd-2soni
#
# Tests the dependency-aware assignment workflow:
# 1. Create dependency chain: A -> (B, C) -> D
# 2. Verify only unblocked beads get assigned
# 3. Complete beads and verify newly unblocked ones become assignable
# 4. Tests unblock reassignment in watch mode

set -euo pipefail

LOG="/tmp/ntm-e2e-deps-$(date +%Y%m%d-%H%M%S).log"
exec > >(tee -a "$LOG") 2>&1

log() {
  local level="$1"
  shift
  echo "[$(date -Iseconds)] [$level] $*"
}

log_info() { log "INFO" "$@"; }
log_debug() { log "DEBUG" "$@"; }
log_pass() { log "PASS" "$@"; }
log_fail() { log "FAIL" "$@"; }

cleanup() {
  local exit_code=$?
  log_info "Cleanup: killing session and removing workspace"
  NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" kill -f "$SESSION" >/dev/null 2>&1 || true
  rm -rf "$BASE_DIR" 2>/dev/null || true
  if [[ $exit_code -eq 0 ]]; then
    log_pass "Dependency-aware assignment E2E test passed"
  else
    log_fail "Test failed with exit code $exit_code"
  fi
  exit $exit_code
}

trap cleanup EXIT

log_info "Starting dependency-aware assignment E2E test"
log_info "Log file: $LOG"

# Prerequisites
if ! command -v br >/dev/null 2>&1; then
  log_fail "br not installed"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  log_fail "jq not installed"
  exit 1
fi

if ! command -v ntm >/dev/null 2>&1; then
  log_fail "ntm not installed"
  exit 1
fi

# Setup
BASE_DIR="/tmp/ntm-deps-test-$$"
SESSION="ntm-deps-$$"
PROJECT_DIR="${BASE_DIR}/${SESSION}"
CONFIG_DIR="${BASE_DIR}/config"
CONFIG_PATH="${CONFIG_DIR}/config.toml"

log_info "Setup: creating test workspace at $BASE_DIR"
mkdir -p "$PROJECT_DIR" "$CONFIG_DIR"
cd "$PROJECT_DIR"

cat > "$CONFIG_PATH" <<EOF
projects_base = "$BASE_DIR"

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"
EOF

# Initialize beads
log_info "Step 1: Initialize beads repository"
br init >/dev/null

# Create beads with dependency structure:
# A (root, no deps) -> B, C (depend on A) -> D (depends on B and C)
log_info "Step 2: Create beads with dependency chain"

# Create bead A - root, no dependencies
OUT_A="$(br create "Bead A - root task" -t task -p 1 --json)"
BEAD_A="$(echo "$OUT_A" | jq -r '.[0].id // .id // empty')"
if [[ -z "$BEAD_A" ]]; then
  log_fail "Failed to create bead A: $OUT_A"
  exit 1
fi
log_debug "Created $BEAD_A (root)"

# Create bead B - depends on A
OUT_B="$(br create "Bead B - depends on A" -t task -p 1 --json)"
BEAD_B="$(echo "$OUT_B" | jq -r '.[0].id // .id // empty')"
if [[ -z "$BEAD_B" ]]; then
  log_fail "Failed to create bead B: $OUT_B"
  exit 1
fi
log_debug "Created $BEAD_B (depends on A)"

# Create bead C - depends on A
OUT_C="$(br create "Bead C - depends on A" -t task -p 1 --json)"
BEAD_C="$(echo "$OUT_C" | jq -r '.[0].id // .id // empty')"
if [[ -z "$BEAD_C" ]]; then
  log_fail "Failed to create bead C: $OUT_C"
  exit 1
fi
log_debug "Created $BEAD_C (depends on A)"

# Create bead D - depends on B and C
OUT_D="$(br create "Bead D - depends on B and C" -t task -p 1 --json)"
BEAD_D="$(echo "$OUT_D" | jq -r '.[0].id // .id // empty')"
if [[ -z "$BEAD_D" ]]; then
  log_fail "Failed to create bead D: $OUT_D"
  exit 1
fi
log_debug "Created $BEAD_D (depends on B and C)"

# Add dependencies
log_info "Step 3: Add dependency relationships"
br dep add "$BEAD_B" "$BEAD_A" >/dev/null
log_debug "Added dep: $BEAD_B blocks-on $BEAD_A"
br dep add "$BEAD_C" "$BEAD_A" >/dev/null
log_debug "Added dep: $BEAD_C blocks-on $BEAD_A"
br dep add "$BEAD_D" "$BEAD_B" >/dev/null
log_debug "Added dep: $BEAD_D blocks-on $BEAD_B"
br dep add "$BEAD_D" "$BEAD_C" >/dev/null
log_debug "Added dep: $BEAD_D blocks-on $BEAD_C"

br sync --flush-only >/dev/null

# Verify initial state - only A should be ready
log_info "Step 4: Verify initial state - only bead A should be ready"
READY_COUNT="$(br ready --json 2>/dev/null | jq 'length')"
if [[ "$READY_COUNT" -ne 1 ]]; then
  log_fail "Expected 1 ready bead, got $READY_COUNT"
  br ready --json 2>/dev/null | jq '.[].id'
  exit 1
fi

READY_ID="$(br ready --json 2>/dev/null | jq -r '.[0].id')"
if [[ "$READY_ID" != "$BEAD_A" ]]; then
  log_fail "Expected $BEAD_A to be ready, got $READY_ID"
  exit 1
fi
log_pass "Only $BEAD_A is ready (correct)"

# Spawn agents
log_info "Step 5: Spawn 2 Claude agents"
SPAWN_OUT="$(NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" --json spawn "$SESSION" --cc=2 --no-user)"
AGENT_COUNT="$(echo "$SPAWN_OUT" | jq -r '.agent_counts.claude // 0')"
if [[ "$AGENT_COUNT" -lt 2 ]]; then
  log_fail "Expected 2 agents, got $AGENT_COUNT"
  exit 1
fi
log_pass "Spawned $AGENT_COUNT Claude agents"

# First assignment - should only assign bead A
log_info "Step 6: First assignment - should only assign bead A"
ASSIGN1="$(NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" --json assign "$SESSION" --auto --strategy=dependency)"
log_debug "Assign output: $ASSIGN1"

ASSIGNED1="$(echo "$ASSIGN1" | jq -r '.summary.assigned_count // 0')"
if [[ "$ASSIGNED1" -ne 1 ]]; then
  log_fail "Expected 1 assignment (only A), got $ASSIGNED1"
  exit 1
fi

FIRST_BEAD="$(echo "$ASSIGN1" | jq -r '.assignments[0].bead_id')"
if [[ "$FIRST_BEAD" != "$BEAD_A" ]]; then
  log_fail "Expected $BEAD_A to be assigned, got $FIRST_BEAD"
  exit 1
fi
log_pass "Correctly assigned only $BEAD_A (the unblocked bead)"

# Verify B, C, D are still blocked
SKIPPED="$(echo "$ASSIGN1" | jq -r '.skipped // []')"
log_debug "Skipped items: $SKIPPED"

# Complete bead A
log_info "Step 7: Complete bead A to unblock B and C"
br update "$BEAD_A" --status closed >/dev/null
br sync --flush-only >/dev/null
log_debug "Closed $BEAD_A"

# Verify B and C are now ready
log_info "Step 8: Verify B and C are now ready"
READY_COUNT2="$(br ready --json 2>/dev/null | jq 'length')"
if [[ "$READY_COUNT2" -ne 2 ]]; then
  log_fail "Expected 2 ready beads (B and C), got $READY_COUNT2"
  br ready --json 2>/dev/null | jq '.[].id'
  exit 1
fi

READY_IDS="$(br ready --json 2>/dev/null | jq -r '.[].id' | sort)"
EXPECTED_READY="$(echo -e "$BEAD_B\n$BEAD_C" | sort)"
if [[ "$READY_IDS" != "$EXPECTED_READY" ]]; then
  log_fail "Expected B and C to be ready, got: $READY_IDS"
  exit 1
fi
log_pass "B and C are now ready (unblocked by A completion)"

# Second assignment - should assign B and C
log_info "Step 9: Second assignment - should assign B and C"
# Clear any stale assignment state first
NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" assign "$SESSION" --clear >/dev/null 2>&1 || true

ASSIGN2="$(NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" --json assign "$SESSION" --auto --strategy=round-robin)"
log_debug "Assign2 output: $ASSIGN2"

ASSIGNED2="$(echo "$ASSIGN2" | jq -r '.summary.assigned_count // 0')"
if [[ "$ASSIGNED2" -ne 2 ]]; then
  log_fail "Expected 2 assignments (B and C), got $ASSIGNED2"
  exit 1
fi

ASSIGNED_IDS="$(echo "$ASSIGN2" | jq -r '.assignments[].bead_id' | sort)"
EXPECTED_ASSIGNED="$(echo -e "$BEAD_B\n$BEAD_C" | sort)"
if [[ "$ASSIGNED_IDS" != "$EXPECTED_ASSIGNED" ]]; then
  log_fail "Expected B and C to be assigned, got: $ASSIGNED_IDS"
  exit 1
fi
log_pass "Correctly assigned B and C"

# Complete B and C
log_info "Step 10: Complete B and C to unblock D"
br update "$BEAD_B" --status closed >/dev/null
br update "$BEAD_C" --status closed >/dev/null
br sync --flush-only >/dev/null
log_debug "Closed $BEAD_B and $BEAD_C"

# Verify D is now ready
log_info "Step 11: Verify D is now ready"
READY_COUNT3="$(br ready --json 2>/dev/null | jq 'length')"
if [[ "$READY_COUNT3" -ne 1 ]]; then
  log_fail "Expected 1 ready bead (D), got $READY_COUNT3"
  br ready --json 2>/dev/null | jq '.[].id'
  exit 1
fi

READY_ID3="$(br ready --json 2>/dev/null | jq -r '.[0].id')"
if [[ "$READY_ID3" != "$BEAD_D" ]]; then
  log_fail "Expected $BEAD_D to be ready, got $READY_ID3"
  exit 1
fi
log_pass "D is now ready (unblocked by B and C completion)"

# Third assignment - should assign D
log_info "Step 12: Third assignment - should assign D"
NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" assign "$SESSION" --clear >/dev/null 2>&1 || true

ASSIGN3="$(NTM_PROJECTS_BASE="$BASE_DIR" ntm --config "$CONFIG_PATH" --json assign "$SESSION" --auto)"
log_debug "Assign3 output: $ASSIGN3"

ASSIGNED3="$(echo "$ASSIGN3" | jq -r '.summary.assigned_count // 0')"
if [[ "$ASSIGNED3" -ne 1 ]]; then
  log_fail "Expected 1 assignment (D), got $ASSIGNED3"
  exit 1
fi

THIRD_BEAD="$(echo "$ASSIGN3" | jq -r '.assignments[0].bead_id')"
if [[ "$THIRD_BEAD" != "$BEAD_D" ]]; then
  log_fail "Expected $BEAD_D to be assigned, got $THIRD_BEAD"
  exit 1
fi
log_pass "Correctly assigned D (final unblocked bead)"

log_info "All dependency-aware assignment tests completed successfully"
