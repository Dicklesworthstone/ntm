#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
host_before=$(env -u TMUX -u TMUX_TMPDIR \
  tmux list-sessions -F '#{session_name}' 2>&1 | sort || true)
sentinel_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-sentinel-tmux.XXXXXX")
test_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-test-tmux.XXXXXX")
poison_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-poison-tmux.XXXXXX")
sentinel_socket="ntm-sentinel-$$"
sentinel_session="ntm-sentinel-$$"
poison_socket="ntm-poison-$$"
poison_session="ntm-poison-$$"

cleanup() {
  env -u TMUX TMUX_TMPDIR="$sentinel_root" \
    tmux -L "$sentinel_socket" kill-session -t "$sentinel_session" \
    2>/dev/null || true
  env -u TMUX TMUX_TMPDIR="$poison_root" \
    tmux -L "$poison_socket" kill-session -t "$poison_session" \
    2>/dev/null || true
  rm -rf "$sentinel_root" "$test_root" "$poison_root"
}
trap cleanup EXIT

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" new-session -d -s "$sentinel_session"
env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" new-session -d -s "$poison_session"

# Poison the inherited routing environment. Guarded tests and host inventory
# checks must ignore this server without killing its sentinel session.
export TMUX_TMPDIR="$poison_root"
export TMUX=$(env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" display-message -p -t "$poison_session" \
    '#{socket_path},#{pid},0')

env -u TMUX TMUX_TMPDIR="$test_root" \
  go test ./internal/cli ./internal/robot ./internal/status \
    ./tests/testutil -run 'TestPrintSnapshotWithSession|^$' -count=1

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" has-session -t "$sentinel_session"
env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" has-session -t "$poison_session"

host_after=$(env -u TMUX -u TMUX_TMPDIR \
  tmux list-sessions -F '#{session_name}' 2>&1 | sort || true)
if [[ "$host_before" != "$host_after" ]]; then
  printf 'host default tmux inventory changed\nbefore:\n%s\nafter:\n%s\n' \
    "$host_before" "$host_after" >&2
  exit 1
fi

printf 'PASS: private and poisoned sentinels survived; host default tmux inventory unchanged\n'
