#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../scripts/e2e/lib/logging.sh"

parallel=1
filter=""
timeout_seconds=300

usage() {
	cat <<'EOF'
Usage: e2e/e2e-runner.sh [--parallel N] [--filter PATTERN] [--timeout SECONDS]

Discovers test-*.sh scripts beside this runner, writes per-test stdout/stderr
and JSONL event logs, and writes an aggregate summary.json. Set E2E_NTM_BIN to
test an existing binary; otherwise each scenario builds an isolated binary.
EOF
}

while (( $# > 0 )); do
	case "$1" in
		--parallel) parallel="$2"; shift 2 ;;
		--filter) filter="$2"; shift 2 ;;
		--timeout) timeout_seconds="$2"; shift 2 ;;
		-h|--help) usage; exit 0 ;;
		*) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
	esac
done

if ! [[ "$parallel" =~ ^[1-9][0-9]*$ ]] || ! [[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
	printf '%s\n' '--parallel and --timeout must be positive integers' >&2
	exit 2
fi

E2E_TEST_NAME=runner
E2E_RUN_DIR="${E2E_RUN_DIR:-${E2E_REPO_ROOT}/logs/e2e-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
E2E_TEST_LOG_DIR="$E2E_RUN_DIR/$E2E_TEST_NAME"
mkdir -p "$E2E_TEST_LOG_DIR"
mkdir -p "$E2E_RUN_DIR/results"

tests=()
shopt -s nullglob
for test_script in "$SCRIPT_DIR"/test-*.sh; do
	name="$(basename "$test_script")"
	if [[ -z "$filter" || "$name" == *"$filter"* ]]; then
		tests+=("$test_script")
	fi
done
shopt -u nullglob

if (( ${#tests[@]} == 0 )); then
	log_warn discovery 'no shell E2E tests matched the requested filter'
	printf '%s\n' 'No shell E2E tests matched.' >&2
	exit 2
fi

run_one() {
	local test_script="$1"
	local name started finished status watchdog
	name="$(basename "$test_script")"
	started="$(date +%s)"
	(
		sleep "$timeout_seconds"
		kill -TERM "$$" 2>/dev/null || true
	) &
	watchdog=$!
	set +e
	E2E_RUN_DIR="$E2E_RUN_DIR" E2E_NTM_BIN="${E2E_NTM_BIN:-}" bash "$test_script" >"$E2E_RUN_DIR/$name.stdout.log" 2>"$E2E_RUN_DIR/$name.stderr.log"
	status=$?
	set -e
	kill "$watchdog" 2>/dev/null || true
	wait "$watchdog" 2>/dev/null || true
	finished="$(date +%s)"
	printf '%s\t%s\t%s\n' "$name" "$status" "$(( (finished - started) * 1000 ))" >"$E2E_RUN_DIR/results/$name.tsv"
}

pids=()
for test_script in "${tests[@]}"; do
	while (( ${#pids[@]} >= parallel )); do
		wait "${pids[0]}" || true
		pids=("${pids[@]:1}")
	done
	run_one "$test_script" &
	pids+=("$!")
done
for pid in "${pids[@]}"; do
	wait "$pid" || true
done

total=0
passed=0
failed=0
summary_tests=""
for result in "$E2E_RUN_DIR"/results/*.tsv; do
	IFS=$'\t' read -r name status duration_ms <"$result"
	total=$(( total + 1 ))
	if (( status == 0 )); then
		passed=$(( passed + 1 ))
		printf '[%s] PASS: %s (%sms)\n' "$(date '+%F %T')" "$name" "$duration_ms"
	else
		failed=$(( failed + 1 ))
		printf '[%s] FAIL: %s (%sms); see %s\n' "$(date '+%F %T')" "$name" "$duration_ms" "$E2E_RUN_DIR/$name.stderr.log" >&2
	fi
	entry="{\"name\":\"$(json_escape "$name")\",\"status\":$status,\"duration_ms\":$duration_ms}"
	if [[ -n "$summary_tests" ]]; then summary_tests+=","; fi
	summary_tests+="$entry"
done

summary_path="$E2E_RUN_DIR/summary.json"
printf '{"finished_at":"%s","total":%s,"passed":%s,"failed":%s,"tests":[%s]}\n' \
	"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$total" "$passed" "$failed" "$summary_tests" >"$summary_path"
log_info summary "E2E runner completed" 0 total "$total" passed "$passed" failed "$failed" summary "$summary_path"
printf '[%s] Summary: passed %s/%s, failed %s\n' "$(date '+%F %T')" "$passed" "$total" "$failed"

(( failed == 0 ))
