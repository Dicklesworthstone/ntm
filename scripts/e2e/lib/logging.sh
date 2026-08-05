#!/usr/bin/env bash
# Shared helpers for the opt-in shell E2E suite. Source this file from a test
# script; do not execute it directly.

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	printf '%s\n' 'scripts/e2e/lib/logging.sh must be sourced by an E2E test script.' >&2
	exit 2
fi

E2E_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_SCRIPTS_DIR="$(cd "$E2E_LIB_DIR/.." && pwd)"
E2E_REPO_ROOT="$(cd "$E2E_SCRIPTS_DIR/../.." && pwd)"

json_escape() {
	local value="$1"
	value=${value//\\/\\\\}
	value=${value//\"/\\\"}
	value=${value//$'\n'/\\n}
	value=${value//$'\r'/\\r}
	value=${value//$'\t'/\\t}
	printf '%s' "$value"
}

json_context() {
	local context="{"
	local key value
	local first=1
	while (( $# >= 2 )); do
		key="$1"
		value="$2"
		shift 2
		if (( first == 0 )); then
			context+="," 
		fi
		context+="\"$(json_escape "$key")\":\"$(json_escape "$value")\""
		first=0
	done
	context+="}"
	printf '%s' "$context"
}

log_json() {
	local level="$1"
	local step="$2"
	local message="$3"
	local duration_ms=0
	if (( $# >= 4 )); then
		duration_ms="$4"
		shift 4
	else
		shift 3
	fi
	local timestamp context line
	timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	context="$(json_context "$@")"
	line="{\"timestamp\":\"$timestamp\",\"level\":\"$(json_escape "$level")\",\"test\":\"$(json_escape "$E2E_TEST_NAME")\",\"step\":\"$(json_escape "$step")\",\"message\":\"$(json_escape "$message")\",\"duration_ms\":$duration_ms,\"context\":$context}"
	printf '%s\n' "$line" | tee -a "$E2E_TEST_LOG_DIR/events.jsonl"
}

log_info() { log_json INFO "$@"; }
log_warn() { log_json WARN "$@"; }
log_error() { log_json ERROR "$@"; }
log_debug() {
	if [[ "${E2E_DEBUG:-0}" == "1" ]]; then
		log_json DEBUG "$@"
	fi
}

log_step_start() { log_info "$1" "step started"; }
log_step_end() { log_info "$1" "step completed" "${2:-0}"; }
log_assertion_pass() { log_info assertion "$1"; }
log_assertion_fail() { log_error assertion "$1"; }

e2e_fail() {
	log_assertion_fail "$1"
	return 1
}

assert_contains() {
	local haystack="$1"
	local needle="$2"
	local description="$3"
	if [[ "$haystack" != *"$needle"* ]]; then
		e2e_fail "$description (missing: $needle)"
		return 1
	fi
	log_assertion_pass "$description"
}

assert_pane_contains() {
	local pane_id="$1"
	local expected="$2"
	local content
	content="$("$E2E_REAL_TMUX" capture-pane -p -t "$pane_id" 2>&1)" || {
		e2e_fail "capture pane $pane_id"
		return 1
	}
	assert_contains "$content" "$expected" "pane $pane_id contains expected message"
}

assert_command_fails() {
	local description="$1"
	shift
	local output status
	set +e
	output="$("$@" 2>&1)"
	status=$?
	set -e
	if (( status == 0 )); then
		e2e_fail "$description unexpectedly succeeded"
		return 1
	fi
	log_info expected_failure "$description" 0 exit_code "$status" output "$output"
}

run_logged_command() {
	local step="$1"
	shift
	local started finished duration status stdout_path stderr_path
	started="$(date +%s)"
	stdout_path="$E2E_TEST_LOG_DIR/${step}.stdout.log"
	stderr_path="$E2E_TEST_LOG_DIR/${step}.stderr.log"
	set +e
	"$@" >"$stdout_path" 2>"$stderr_path"
	status=$?
	set -e
	finished="$(date +%s)"
	duration=$(( (finished - started) * 1000 ))
	log_json INFO "$step" "command completed" "$duration" command "$*" exit_code "$status" stdout "$stdout_path" stderr "$stderr_path"
	return "$status"
}

e2e_test_setup() {
	E2E_TEST_NAME="$1"
	E2E_RUN_DIR="${E2E_RUN_DIR:-${E2E_REPO_ROOT}/logs/e2e-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
	E2E_TEST_LOG_DIR="$E2E_RUN_DIR/$E2E_TEST_NAME"
	mkdir -p "$E2E_TEST_LOG_DIR"

	E2E_REAL_TMUX="$(command -v tmux)"
	if [[ -z "$E2E_REAL_TMUX" ]]; then
		log_error preflight 'tmux is required for shell E2E tests'
		return 1
	fi
	if ! command -v go >/dev/null 2>&1; then
		log_error preflight 'go is required to build the NTM test binary'
		return 1
	fi
	E2E_GOMODCACHE="$(go env GOMODCACHE)"
	E2E_GOCACHE="$(go env GOCACHE)"

	E2E_TEST_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/ntm-e2e-${E2E_TEST_NAME}.XXXXXX")"
	E2E_SESSIONS=()
	mkdir -p "$E2E_TEST_TMPDIR/bin" "$E2E_TEST_TMPDIR/home" "$E2E_TEST_TMPDIR/projects"
	E2E_TMUX_TMPDIR="$(mktemp -d /tmp/ntm-e2e-tmux.XXXXXX)"

	printf '%s\n' 'package main' 'import ("bufio"; "fmt"; "os")' 'func main() { scanner := bufio.NewScanner(os.Stdin); for scanner.Scan() { fmt.Printf("FAKE_AGENT_RECEIVED:%s\n", scanner.Text()) }; select {} }' >"$E2E_TEST_TMPDIR/fake_agent.go"
	go build -o "$E2E_TEST_TMPDIR/bin/fake-agent" "$E2E_TEST_TMPDIR/fake_agent.go"
	ln -s fake-agent "$E2E_TEST_TMPDIR/bin/claude"
	ln -s fake-agent "$E2E_TEST_TMPDIR/bin/codex"
	ln -s fake-agent "$E2E_TEST_TMPDIR/bin/gemini"

	export E2E_REAL_TMUX
	export HOME="$E2E_TEST_TMPDIR/home"
	export XDG_CONFIG_HOME="$E2E_TEST_TMPDIR/home/.config"
	export GOMODCACHE="$E2E_GOMODCACHE"
	export GOCACHE="$E2E_GOCACHE"
	export PATH="$E2E_TEST_TMPDIR/bin:$PATH"
	export NTM_PROJECTS_BASE="$E2E_TEST_TMPDIR/projects"
	export NTM_TEST_MODE=1
	export NTM_TEST_SPAWN_PANE_DELAY_MS=0
	export NTM_TEST_SPAWN_AGENT_DELAY_MS=0
	export TMUX=""
	export TMUX_PANE=""
	export TMUX_TMPDIR="$E2E_TMUX_TMPDIR"
	export NTM_TMUX_BINARY="$E2E_REAL_TMUX"

	E2E_NTM_BIN="${E2E_NTM_BIN:-}"
	if [[ -z "$E2E_NTM_BIN" ]]; then
		E2E_NTM_BIN="$E2E_TEST_TMPDIR/ntm"
		log_step_start build_ntm
		go build -o "$E2E_NTM_BIN" "$E2E_REPO_ROOT/cmd/ntm"
		log_step_end build_ntm
	fi
	export E2E_NTM_BIN
	log_info setup "isolated test environment ready" 0 artifact_root "$E2E_TEST_TMPDIR" tmux_tmpdir "$TMUX_TMPDIR"
}

e2e_spawn() {
	local session="$1"
	shift
	"$E2E_NTM_BIN" spawn "$session" --no-user --create-dir --no-cass-context --no-recovery --no-hooks "$@"
	E2E_SESSIONS+=("$session")
	log_info spawn "spawned fake-agent session" 0 session "$session"
}

e2e_capture_diagnostics() {
	local diagnostics_dir pane session pane_file
	diagnostics_dir="$E2E_TEST_LOG_DIR/diagnostics"
	mkdir -p "$diagnostics_dir"

	"$E2E_REAL_TMUX" list-sessions >"$diagnostics_dir/tmux-sessions.log" 2>&1 || true
	"$E2E_REAL_TMUX" list-panes -a -F '#{session_name}:#{window_index}.#{pane_index}' >"$diagnostics_dir/tmux-panes.log" 2>&1 || true
	while IFS= read -r pane; do
		[[ -n "$pane" ]] || continue
		pane_file="${pane//[^[:alnum:]._-]/_}"
		"$E2E_REAL_TMUX" capture-pane -p -t "$pane" >"$diagnostics_dir/pane-${pane_file}.log" 2>&1 || true
	done <"$diagnostics_dir/tmux-panes.log"
	ps -axo pid=,ppid=,state=,etime=,command= >"$diagnostics_dir/processes.log" 2>&1 || true
	for session in "${E2E_SESSIONS[@]:-}"; do
		"$E2E_NTM_BIN" status "$session" --json >"$diagnostics_dir/status-${session}.json" 2>&1 || true
		"$E2E_REAL_TMUX" show-messages -t "$session" >"$diagnostics_dir/tmux-messages-${session}.log" 2>&1 || true
	done
	log_error diagnostics "captured failure diagnostics" 0 diagnostics_dir "$diagnostics_dir"
}

e2e_cleanup() {
	local session monitor_pid
	for session in "${E2E_SESSIONS[@]:-}"; do
		while IFS= read -r monitor_pid; do
			[[ -n "$monitor_pid" ]] || continue
			kill -TERM "$monitor_pid" 2>/dev/null || true
			log_info teardown "stopped isolated session monitor" 0 session "$session" pid "$monitor_pid"
		done < <(pgrep -f "internal-monitor ${session}$" 2>/dev/null || true)
		if "$E2E_REAL_TMUX" has-session -t "$session" 2>/dev/null; then
			"$E2E_REAL_TMUX" kill-session -t "$session"
			log_info teardown "killed isolated tmux session" 0 session "$session"
		fi
	done
	log_info teardown "artifacts retained for diagnosis" 0 artifact_root "$E2E_TEST_TMPDIR"
}

e2e_finish() {
	local status="$1"
	if (( status != 0 )); then
		e2e_capture_diagnostics
	fi
	e2e_cleanup
	exit "$status"
}
