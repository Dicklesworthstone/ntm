#!/usr/bin/env bash
# E2E Test Runner
# Master script for running E2E tests with parallel execution and JSON logging.
#
# Usage:
#   ./e2e-runner.sh                    # Run all tests
#   ./e2e-runner.sh test-spawn.sh      # Run specific test
#   ./e2e-runner.sh --parallel 4       # Run with 4 parallel jobs
#   ./e2e-runner.sh --filter "spawn"   # Run tests matching pattern

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${SCRIPT_DIR}/logs"
RESULTS_FILE="${LOG_DIR}/results_$(date +%Y%m%d_%H%M%S).json"

# Default options
PARALLEL_JOBS=1
FILTER=""
VERBOSE=0
DRY_RUN=0
TESTS=()

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --parallel|-p)
            PARALLEL_JOBS="$2"
            shift 2
            ;;
        --filter|-f)
            FILTER="$2"
            shift 2
            ;;
        --verbose|-v)
            VERBOSE=1
            shift
            ;;
        --dry-run|-n)
            DRY_RUN=1
            shift
            ;;
        --help|-h)
            cat <<EOF
E2E Test Runner for ntm

Usage: $(basename "$0") [OPTIONS] [TESTS...]

Options:
  -p, --parallel N    Run N tests in parallel (default: 1)
  -f, --filter PATTERN  Only run tests matching pattern
  -v, --verbose       Show verbose output
  -n, --dry-run       Show what would run without executing
  -h, --help          Show this help

Examples:
  $(basename "$0")                      # Run all tests
  $(basename "$0") test-spawn.sh        # Run specific test
  $(basename "$0") -p 4                 # Run with 4 parallel jobs
  $(basename "$0") -f spawn             # Run tests containing "spawn"

Environment:
  E2E_LOG_DIR         Override log directory (default: ./logs)
  E2E_DEBUG           Set to 1 for debug output
  E2E_TIMEOUT         Default test timeout in seconds (default: 300)
EOF
            exit 0
            ;;
        *)
            TESTS+=("$1")
            shift
            ;;
    esac
done

# Setup
mkdir -p "$LOG_DIR"
export E2E_LOG_DIR="$LOG_DIR"

# Test-only spawn pacing to avoid resource spikes under parallel runs.
if [[ "${PARALLEL_JOBS}" -gt 1 ]]; then
    export NTM_TEST_MODE="${NTM_TEST_MODE:-1}"
    export NTM_TEST_SPAWN_PANE_DELAY_MS="${NTM_TEST_SPAWN_PANE_DELAY_MS:-150}"
    export NTM_TEST_SPAWN_AGENT_DELAY_MS="${NTM_TEST_SPAWN_AGENT_DELAY_MS:-120}"
fi

# Find test scripts (outputs one per line for mapfile)
find_tests() {
    for f in "${SCRIPT_DIR}"/test-*.sh; do
        if [[ -f "$f" && -x "$f" ]]; then
            echo "$f"
        fi
    done
}

# Filter tests by pattern (outputs one per line for mapfile)
filter_tests() {
    local pattern="$1"
    shift

    for test in "$@"; do
        if [[ "$(basename "$test")" == *"$pattern"* ]]; then
            echo "$test"
        fi
    done
}

# Run a single test
run_test() {
    local test_script="$1"
    local test_name
    test_name=$(basename "$test_script" .sh)

    local start_ts
    start_ts=$(date +%s)

    echo ">>> Running: $test_name"

    local exit_code=0
    local output=""
    local log_file="${LOG_DIR}/${test_name}_runner.log"

    if [[ $DRY_RUN -eq 1 ]]; then
        echo "    [DRY-RUN] Would execute: $test_script"
        output='{"test":"'"$test_name"'","result":"skipped","reason":"dry_run"}'
        exit_code=0
    else
        if [[ $VERBOSE -eq 1 ]]; then
            output=$("$test_script" 2>&1 | tee "$log_file") || exit_code=$?
        else
            output=$("$test_script" 2>"$log_file") || exit_code=$?
        fi
    fi

    local end_ts
    end_ts=$(date +%s)
    local duration=$((end_ts - start_ts))

    # Parse the JSON summary from output (last line should be JSON)
    local json_result
    json_result=$(echo "$output" | tail -1)

    # Validate it's JSON
    if ! echo "$json_result" | jq . >/dev/null 2>&1; then
        json_result="{\"test\":\"$test_name\",\"result\":\"error\",\"error\":\"invalid output\",\"duration_ms\":$((duration * 1000))}"
    fi

    # Add runner metadata
    json_result=$(echo "$json_result" | jq -c ". + {\"runner_exit_code\": $exit_code, \"runner_log\": \"$log_file\"}")

    echo "$json_result"

    if [[ $exit_code -eq 0 ]]; then
        echo "    PASS ($duration s)"
    else
        echo "    FAIL (exit $exit_code, $duration s)"
    fi

    return $exit_code
}

# Run tests sequentially
run_sequential() {
    local tests=("$@")
    local results=()
    local passed=0
    local failed=0

    for test in "${tests[@]}"; do
        local result
        if result=$(run_test "$test"); then
            ((passed++)) || true
        else
            ((failed++)) || true
        fi
        results+=("$result")
    done

    # Write results file
    {
        echo "{"
        echo "  \"timestamp\": \"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\","
        echo "  \"total\": ${#tests[@]},"
        echo "  \"passed\": $passed,"
        echo "  \"failed\": $failed,"
        echo "  \"parallel\": false,"
        echo "  \"results\": ["
        local first=1
        for r in "${results[@]}"; do
            if [[ $first -eq 0 ]]; then
                echo ","
            fi
            first=0
            echo -n "    $r"
        done
        echo ""
        echo "  ]"
        echo "}"
    } > "$RESULTS_FILE"

    return $failed
}

# Run tests in parallel
run_parallel() {
    local tests=("$@")
    local job_count=$PARALLEL_JOBS
    local results_dir="${LOG_DIR}/parallel_$$"
    mkdir -p "$results_dir"

    echo "Running ${#tests[@]} tests with $job_count parallel jobs"

    local pids=()
    local test_idx=0

    for test in "${tests[@]}"; do
        # Wait if we have too many jobs
        while [[ ${#pids[@]} -ge $job_count ]]; do
            for i in "${!pids[@]}"; do
                if ! kill -0 "${pids[$i]}" 2>/dev/null; then
                    unset 'pids[i]'
                fi
            done
            pids=("${pids[@]}")
            sleep 0.1
        done

        # Start test in background
        (
            result=$(run_test "$test")
            echo "$result" > "${results_dir}/result_${test_idx}.json"
        ) &
        pids+=($!)
        ((test_idx++)) || true
    done

    # Wait for all remaining jobs
    for pid in "${pids[@]}"; do
        wait "$pid" 2>/dev/null || true
    done

    # Collect results
    local passed=0
    local failed=0
    local results=()

    for ((i=0; i<${#tests[@]}; i++)); do
        local result_file="${results_dir}/result_${i}.json"
        if [[ -f "$result_file" ]]; then
            local result
            result=$(cat "$result_file")
            results+=("$result")

            local test_result
            test_result=$(echo "$result" | jq -r '.result // "error"')
            if [[ "$test_result" == "pass" ]]; then
                ((passed++)) || true
            else
                ((failed++)) || true
            fi
        fi
    done

    rm -rf "$results_dir"

    # Write results file
    {
        echo "{"
        echo "  \"timestamp\": \"$(date -u +"%Y-%m-%dT%H:%M:%SZ")\","
        echo "  \"total\": ${#tests[@]},"
        echo "  \"passed\": $passed,"
        echo "  \"failed\": $failed,"
        echo "  \"parallel\": true,"
        echo "  \"parallel_jobs\": $job_count,"
        echo "  \"results\": ["
        local first=1
        for r in "${results[@]}"; do
            if [[ $first -eq 0 ]]; then
                echo ","
            fi
            first=0
            echo -n "    $r"
        done
        echo ""
        echo "  ]"
        echo "}"
    } > "$RESULTS_FILE"

    return $failed
}

# Main
main() {
    echo "======================================"
    echo "  NTM E2E Test Runner"
    echo "======================================"
    echo ""

    # Check prerequisites
    if ! command -v jq &>/dev/null; then
        echo "ERROR: jq is required but not installed" >&2
        exit 1
    fi

    if ! command -v ntm &>/dev/null; then
        echo "WARNING: ntm not found in PATH, tests may fail" >&2
    fi

    # Get tests to run
    local tests_to_run=()

    if [[ ${#TESTS[@]} -gt 0 ]]; then
        # Specific tests provided
        for t in "${TESTS[@]}"; do
            if [[ -f "$t" ]]; then
                tests_to_run+=("$t")
            elif [[ -f "${SCRIPT_DIR}/$t" ]]; then
                tests_to_run+=("${SCRIPT_DIR}/$t")
            else
                echo "WARNING: Test not found: $t" >&2
            fi
        done
    else
        # Find all tests
        mapfile -t tests_to_run < <(find_tests)
    fi

    # Apply filter
    if [[ -n "$FILTER" ]]; then
        local filtered
        mapfile -t filtered < <(filter_tests "$FILTER" "${tests_to_run[@]}")
        tests_to_run=("${filtered[@]}")
    fi

    if [[ ${#tests_to_run[@]} -eq 0 ]]; then
        echo "No tests to run"
        exit 0
    fi

    echo "Tests to run: ${#tests_to_run[@]}"
    for t in "${tests_to_run[@]}"; do
        echo "  - $(basename "$t")"
    done
    echo ""

    local start_ts
    start_ts=$(date +%s)
    local exit_code=0

    # Run tests
    if [[ $PARALLEL_JOBS -gt 1 ]]; then
        run_parallel "${tests_to_run[@]}" || exit_code=$?
    else
        run_sequential "${tests_to_run[@]}" || exit_code=$?
    fi

    local end_ts
    end_ts=$(date +%s)
    local total_duration=$((end_ts - start_ts))

    echo ""
    echo "======================================"
    echo "  Results Summary"
    echo "======================================"
    echo ""

    # Parse and display results
    if [[ -f "$RESULTS_FILE" ]]; then
        local total passed failed
        total=$(jq -r '.total' "$RESULTS_FILE")
        passed=$(jq -r '.passed' "$RESULTS_FILE")
        failed=$(jq -r '.failed' "$RESULTS_FILE")

        echo "Total:   $total"
        echo "Passed:  $passed"
        echo "Failed:  $failed"
        echo "Duration: ${total_duration}s"
        echo ""
        echo "Results file: $RESULTS_FILE"

        # Show failed tests
        if [[ $failed -gt 0 ]]; then
            echo ""
            echo "Failed tests:"
            jq -r '.results[] | select(.result != "pass") | "  - \(.test): \(.result)"' "$RESULTS_FILE"
        fi
    fi

    echo ""
    if [[ $exit_code -eq 0 ]]; then
        echo "ALL TESTS PASSED"
    else
        echo "SOME TESTS FAILED"
    fi

    exit $exit_code
}

main
