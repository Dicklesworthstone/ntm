#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
host_before=$(tmux list-sessions -F '#{session_name}' 2>&1 | sort || true)
sentinel_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-sentinel-tmux.XXXXXX")
test_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-test-tmux.XXXXXX")
sentinel_socket="ntm-sentinel-$$"
sentinel_session="ntm-sentinel-$$"

cleanup() {
  env -u TMUX TMUX_TMPDIR="$sentinel_root" \
    tmux -L "$sentinel_socket" kill-session -t "$sentinel_session" \
    2>/dev/null || true
  rm -rf "$sentinel_root" "$test_root"
}
trap cleanup EXIT

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" new-session -d -s "$sentinel_session"

env -u TMUX TMUX_TMPDIR="$test_root" \
  go test ./internal/cli ./internal/robot ./internal/status \
    ./tests/testutil -run 'TestPrintSnapshotWithSession|^$' -count=1

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" has-session -t "$sentinel_session"

host_after=$(tmux list-sessions -F '#{session_name}' 2>&1 | sort || true)
if [[ "$host_before" != "$host_after" ]]; then
  printf 'host default tmux inventory changed\nbefore:\n%s\nafter:\n%s\n' \
    "$host_before" "$host_after" >&2
  exit 1
fi

printf 'PASS: private sentinel survived; host default tmux inventory unchanged\n'
