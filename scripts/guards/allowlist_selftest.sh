#!/usr/bin/env bash
# allowlist_selftest.sh — fixture test for the WS0 shared allowlist infra
# (bd-ws0-guards-klz98.1 acceptance: malformed line, closed-bead line, and stale
# line must each FAIL). Runs as part of scripts/guards/run_all.sh, including in
# CI where br is absent (the closed-bead fixture degrades to a bead-ID-shape
# fixture there, matching check_allowlists.sh's documented CI behavior).
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT" || exit 1
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
rc=0
fail() { echo "allowlist_selftest: FAIL: $*" >&2; rc=1; }

run_check() { # run_check <allowlist-dir> — runs check_allowlists.sh against a fixture dir
  ALLOWLIST_DIR="$1" SRC_SCAN_DIRS="" bash scripts/check_allowlists.sh >/dev/null 2>&1
}

# 1. A well-formed fixture must PASS (guards against a validator that fails everything).
# The bead on the live line must be OPEN where br exists (check_allowlists.sh rejects
# closed beads), so discover one dynamically instead of pinning an ID that will
# eventually close (v1.26.0 audit finding: the pinned bd-ws0-guards-klz98 closed and
# this fixture started failing). CI without br only shape-checks the ID, so the
# static fallback keeps that path honest.
open_id="bd-ws0-guards-klz98"
if command -v br >/dev/null 2>&1; then
  # First bead-shaped token on the line, NOT a greedy sed capture: greedy
  # matching grabbed the LAST token, so an open bead whose TITLE mentions a
  # closed bead id poisoned the "open" fixture and failed check 1.
  found_open="$(br list --status=open --limit=1 2>/dev/null | grep -oE 'bd-[a-z0-9][a-z0-9._-]*' | head -1)"
  [ -n "$found_open" ] && open_id="$found_open"
fi
mkdir -p "$TMP/ok"
printf '# comment\nentry-a\t%s\tvalid open bead\n# permanent:\nentry-b\tpermanent\tcompensates reflection blind spot\n' "$open_id" > "$TMP/ok/one.txt"
run_check "$TMP/ok" || fail "well-formed allowlist rejected"

# 2. Malformed line (2 fields) must FAIL.
mkdir -p "$TMP/malformed"
printf 'entry-a\tbd-ws0-guards-klz98\n' > "$TMP/malformed/one.txt"
run_check "$TMP/malformed" && fail "malformed 2-field line accepted"

# 3. Bad bead-ID shape must FAIL (this is the CI-mode closed-bead proxy).
mkdir -p "$TMP/badshape"
printf 'entry-a\tnot-a-bead-id\treason text\n' > "$TMP/badshape/one.txt"
run_check "$TMP/badshape" && fail "non-bead-shaped id accepted"

# 4. Closed/missing bead must FAIL where br exists (CI without br skips honestly).
if command -v br >/dev/null 2>&1; then
  mkdir -p "$TMP/missing"
  printf 'entry-a\tbd-zzzz-does-not-exist\treason text\n' > "$TMP/missing/one.txt"
  run_check "$TMP/missing" && fail "missing bead accepted"
  # Same first-token discipline as the open-id discovery above: a closed
  # bead whose title mentions an OPEN bead id must not poison this fixture.
  closed_id="$(br list --status=closed --limit=1 2>/dev/null | grep -oE 'bd-[a-z0-9][a-z0-9._-]*' | head -1)"
  if [ -n "$closed_id" ]; then
    mkdir -p "$TMP/closed"
    printf 'entry-a\t%s\treason text\n' "$closed_id" > "$TMP/closed/one.txt"
    run_check "$TMP/closed" && fail "CLOSED bead $closed_id accepted on a live allowlist line"
  fi
fi

# 5. Permanent entry outside the '# permanent:' section must FAIL.
mkdir -p "$TMP/permout"
printf 'entry-a\tpermanent\tcompensates reflection blind spot\n' > "$TMP/permout/one.txt"
run_check "$TMP/permout" && fail "permanent entry outside permanent section accepted"

# 6. Ratchet both directions (shared lib used by every gate):
allow="$TMP/ratchet.txt"
printf 'entry-x\tbd-ws0-guards-klz98\treason\n' > "$allow"
cur="$TMP/current.txt"
#   6a. matching sets pass
printf 'entry-x\tbd-ws0-guards-klz98\n' > "$cur"
ratchet_compare "$allow" "$cur" >/dev/null 2>&1 || fail "ratchet rejected an exactly-matching set"
#   6b. new violation not in list fails (new debt)
printf 'entry-x\tbd-ws0-guards-klz98\nentry-new\tbd-ws0-guards-klz98\n' > "$cur"
ratchet_compare "$allow" "$cur" >/dev/null 2>&1 && fail "ratchet accepted NEW DEBT (unlisted violation)"
#   6c. listed entry with no current violation fails (stale line)
: > "$cur"
ratchet_compare "$allow" "$cur" >/dev/null 2>&1 && fail "ratchet accepted a STALE allowlist line"

if [ "$rc" -ne 0 ]; then
  echo "allowlist_selftest.sh: FAILED" >&2
  exit 1
fi
echo "allowlist_selftest.sh: OK (format, bead, and both-direction ratchet fixtures all fire)"
