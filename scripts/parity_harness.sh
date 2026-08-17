#!/usr/bin/env bash
# parity_harness.sh — hermetic-serve CLI-vs-REST parity harness (WS4, bd-ws4-openapi-parity-wpwck.1)
#
# Builds ntm, boots `ntm serve` hermetically (temp config/state, loopback, free
# port, default local auth), sets NTM_TEST_SERVER, and runs the CLI-vs-REST
# parity suite in tests/integration/parity_gate_test.go UN-SKIPPED.
#
# A skipped parity test is a FAILURE (the E3 lesson): the script parses
# `go test -json` output and asserts executed-count > 0 and skipped-count == 0.
#
# Usage: scripts/parity_harness.sh
# Used by: the `parity-harness` job in .github/workflows/ci.yml, and runnable
# locally / by wave gates as a single command.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ntm-parity-harness.XXXXXX")"
SERVER_PID=""

cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> Building ntm"
go build -o "$WORK/bin/ntm" ./cmd/ntm

# Hermetic config/state: ntm resolves its config dir from NTM_CONFIG, then
# XDG_CONFIG_HOME (internal/config/config.go). Point both at the temp dir so
# the server and the CLI under test never touch the real user config/state.
export XDG_CONFIG_HOME="$WORK/config"
export NTM_CONFIG="$WORK/config/ntm/config.toml"
export NTM_DISABLE_INTERNAL_MONITOR=1
mkdir -p "$WORK/config/ntm"

# Pick a free loopback port.
pick_port() {
  local candidate
  for _ in $(seq 1 20); do
    candidate=$(( (RANDOM % 20000) + 20000 ))
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      echo "$candidate"
      return 0
    fi
    exec 3>&- 2>/dev/null || true
  done
  echo "error: could not find a free port" >&2
  return 1
}
PORT="$(pick_port)"

echo "==> Starting hermetic ntm serve on 127.0.0.1:$PORT (auth=local)"
"$WORK/bin/ntm" serve --host 127.0.0.1 --port "$PORT" >"$WORK/serve.log" 2>&1 &
SERVER_PID=$!

# Wait for liveness.
BASE_URL="http://127.0.0.1:$PORT"
for i in $(seq 1 60); do
  if curl -fsS -m 2 "$BASE_URL/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "error: ntm serve exited early; log follows" >&2
    cat "$WORK/serve.log" >&2
    exit 1
  fi
  if [[ "$i" == 60 ]]; then
    echo "error: server did not become healthy in 60s; log follows" >&2
    cat "$WORK/serve.log" >&2
    exit 1
  fi
  sleep 1
done
echo "==> Server healthy at $BASE_URL"

export NTM_TEST_SERVER="$BASE_URL"

# The integration package's TestMain (tests/integration/status_test.go) silently
# no-ops the ENTIRE package — reporting "ok" with zero tests executed — unless
# NTM_INTEGRATION_TESTS is set and tmux is installed. That silent no-op is the
# E3 failure mode this harness exists to close; the executed-count gate below
# catches it if this ever regresses.
export NTM_INTEGRATION_TESTS=1
if ! command -v tmux >/dev/null 2>&1; then
  echo "error: tmux is required (TestMain no-ops the integration package without it)" >&2
  exit 1
fi

echo "==> Running CLI-vs-REST parity suite (un-skipped)"
GOTEST_JSON="$WORK/gotest.json"
set +e
go test -count=1 -timeout=10m -json -run 'TestParityCLIvsREST' ./tests/integration/ | tee "$GOTEST_JSON"
TEST_EXIT=${PIPESTATUS[0]}
set -e

# Executed-count gate: a suite that silently skips (or matches zero tests) must
# fail the job even though `go test` exits 0.
count_action() {
  # Count per-test terminal events for the given action.
  grep '"Test":"TestParityCLIvsREST' "$GOTEST_JSON" | grep -c "\"Action\":\"$1\"" || true
}
PASS_COUNT="$(count_action pass)"
FAIL_COUNT="$(count_action fail)"
SKIP_COUNT="$(count_action skip)"
EXECUTED=$((PASS_COUNT + FAIL_COUNT))

echo "==> Parity suite summary: executed=$EXECUTED pass=$PASS_COUNT fail=$FAIL_COUNT skip=$SKIP_COUNT"

if [[ "$TEST_EXIT" != 0 ]]; then
  echo "error: parity suite failed (go test exit $TEST_EXIT)" >&2
  exit "$TEST_EXIT"
fi
if [[ "$SKIP_COUNT" != 0 ]]; then
  echo "error: $SKIP_COUNT parity test(s) skipped — a skipped parity test in CI is a failure" >&2
  exit 1
fi
if [[ "$EXECUTED" -le 0 ]]; then
  echo "error: executed-count is 0 — parity suite did not run" >&2
  exit 1
fi

echo "==> Hermetic-serve parity harness PASSED (executed-count=$EXECUTED)"
