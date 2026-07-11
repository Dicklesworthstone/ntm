#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
inventory_format='#{session_name}\t#{session_id}\t#{session_created}\t#{session_windows}\t#{session_attached}'
host_before=$(env -u TMUX -u TMUX_TMPDIR \
  tmux list-sessions -F "$inventory_format" 2>&1 | sort || true)
sentinel_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-sentinel-tmux.XXXXXX")
poison_root=$(mktemp -d "${TMPDIR:-/tmp}/ntm-poison-tmux.XXXXXX")
sentinel_socket="ntm-sentinel-$$"
sentinel_session="ntm-sentinel-$$"
sentinel_anchor="ntm-sentinel-anchor-$$"
poison_socket="ntm-poison-$$"
poison_session="ntm-poison-$$"

cleanup() {
  env -u TMUX TMUX_TMPDIR="$sentinel_root" \
    tmux -L "$sentinel_socket" kill-session -t "$sentinel_session" \
    2>/dev/null || true
  env -u TMUX TMUX_TMPDIR="$sentinel_root" \
    tmux -L "$sentinel_socket" kill-session -t "$sentinel_anchor" \
    2>/dev/null || true
  env -u TMUX TMUX_TMPDIR="$poison_root" \
    tmux -L "$poison_socket" kill-session -t "$poison_session" \
    2>/dev/null || true
  rm -rf "$sentinel_root" "$poison_root"
}
trap cleanup EXIT

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" new-session -d -s "$sentinel_session"
env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" new-session -d -s "$sentinel_anchor"

# Prove the identity-bearing oracle detects a same-name replacement.
sentinel_before=$(env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" list-sessions -F "$inventory_format")
env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" kill-session -t "$sentinel_session"
env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" new-session -d -s "$sentinel_session"
sentinel_after=$(env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" list-sessions -F "$inventory_format")
if [[ "$sentinel_before" == "$sentinel_after" ]]; then
  printf 'identity inventory missed same-name replacement\n' >&2
  exit 1
fi
env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" new-session -d -s "$poison_session"

# Poison the inherited routing environment. Guarded tests and host inventory
# checks must ignore this server without killing its sentinel session.
export TMUX_TMPDIR="$poison_root"
export TMUX=$(env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" display-message -p -t "$poison_session" \
    '#{socket_path},#{pid},0')

# Preserve the poisoned inherited routing here. Each guarded TestMain must
# replace it with its own process-owned private root before any tmux mutation.
test_output=$(go test -v ./internal/cli ./internal/robot ./internal/status \
  ./tests/testutil \
  -run 'TestPrintSnapshotWithSession|TestIsolation|TestSharedHelpersIgnoreRouteSwap' \
  -count=1)
printf '%s\n' "$test_output"
if ! grep -q -- '--- PASS: TestSharedHelpersIgnoreRouteSwap' <<<"$test_output"; then
  printf 'canonical harness did not run TestSharedHelpersIgnoreRouteSwap\n' >&2
  exit 1
fi

env -u TMUX TMUX_TMPDIR="$sentinel_root" \
  tmux -L "$sentinel_socket" has-session -t "$sentinel_session"
env -u TMUX TMUX_TMPDIR="$poison_root" \
  tmux -L "$poison_socket" has-session -t "$poison_session"

host_after=$(env -u TMUX -u TMUX_TMPDIR \
  tmux list-sessions -F "$inventory_format" 2>&1 | sort || true)
if [[ "$host_before" != "$host_after" ]]; then
  printf 'host default tmux inventory changed\nbefore:\n%s\nafter:\n%s\n' \
    "$host_before" "$host_after" >&2
  exit 1
fi

printf 'PASS: private and poisoned sentinels survived; host default tmux inventory unchanged\n'
