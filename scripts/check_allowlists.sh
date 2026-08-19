#!/usr/bin/env bash
# check_allowlists.sh — WS0 shared allowlist validator (bd-ws0-guards-klz98.1).
#
# Validates every ci/allowlists/*.txt:
#   1. Line format: tab-separated `<entry>\t<bead-id>\t<reason>`, all fields non-empty;
#      `#` comments and blank lines allowed; a `# permanent:` marker starts the
#      permanent section, whose entries use the literal `permanent` as bead-id and
#      must justify the tool blind spot in the reason.
#   2. Every non-permanent bead-id has a valid shape (bd-...).
#   3. When `br` is installed (local dev): every non-permanent bead-id must reference
#      an existing, NON-closed bead — a closed bead with a live allowlist line means
#      someone closed work the gate says is not done. CI (no br) degrades gracefully
#      to format + shape validation only; the ratchet semantics still hold in CI via
#      the per-gate comm diffs (scripts/guards/*.sh).
#   4. Inline G5 waivers (`placebo-waiver: <bead-id>` in internal/ and cmd/) get the
#      same bead checks; `placebo-waiver: prose` is valid only for files on the
#      prose-exception list in ci/placebo_patterns.txt.
#
# Waiver protocol: adding a line = adding a bead first. No other mechanism.
#
# Env overrides (used by the self-test fixtures):
#   ALLOWLIST_DIR   default ci/allowlists
#   PATTERNS_FILE   default ci/placebo_patterns.txt
#   SRC_SCAN_DIRS   default "internal cmd" (space-separated; empty skips inline scan)
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

ALLOWLIST_DIR="${ALLOWLIST_DIR:-ci/allowlists}"
PATTERNS_FILE="${PATTERNS_FILE:-ci/placebo_patterns.txt}"
SRC_SCAN_DIRS="${SRC_SCAN_DIRS-internal cmd}"

BEAD_ID_RE='^(bd|ntm)-[a-z0-9][a-z0-9._-]*$'  # ntm- prefixed beads are real workspace beads too
FAIL=0

fail() { echo "check_allowlists: FAIL: $*" >&2; FAIL=1; }

HAVE_BR=0
if command -v br >/dev/null 2>&1; then
  HAVE_BR=1
else
  echo "check_allowlists: 'br' not installed — degrading to format + bead-ID-shape validation (CI mode)"
fi

# check_bead <id> <context>
# Memoized per bead ID: the seeded allowlists carry thousands of lines over a
# handful of beads, so one `br show` per LINE turns this script from seconds
# into >10 minutes locally (added by bd-ws0-guards-klz98.2 at G1/G2 seed time;
# validation semantics unchanged).
declare -A BEAD_STATUS_CACHE
check_bead() {
  local id="$1" ctx="$2"
  if ! [[ "$id" =~ $BEAD_ID_RE ]]; then
    fail "$ctx: bead-id '$id' does not look like a bead ID"
    return
  fi
  if [ "$HAVE_BR" = 1 ]; then
    local json status
    if [ -n "${BEAD_STATUS_CACHE[$id]:-}" ]; then
      status="${BEAD_STATUS_CACHE[$id]}"
      case "$status" in
        missing) fail "$ctx: bead '$id' not found (waiver protocol requires an existing bead)" ;;
        unparsed) fail "$ctx: could not parse status for bead '$id'" ;;
        closed) fail "$ctx: bead '$id' is CLOSED but a live allowlist/waiver line references it — the gate, not the ledger, is the arbiter of closed" ;;
      esac
      return
    fi
    if ! json="$(br show "$id" --json 2>/dev/null)"; then
      BEAD_STATUS_CACHE[$id]=missing
      fail "$ctx: bead '$id' not found (waiver protocol requires an existing bead)"
      return
    fi
    # Take the FIRST status field: br show --json embeds dependency/dependent
    # rows whose own "status" fields follow the bead's top-level one, and a
    # greedy match would report a closed child as the bead's status.
    status="$(printf '%s' "$json" | tr -d ' \n' | grep -o '"status":"[a-z_]*"' | head -1 | cut -d'"' -f4)"
    if [ -z "$status" ]; then
      BEAD_STATUS_CACHE[$id]=unparsed
      fail "$ctx: could not parse status for bead '$id'"
    elif [ "$status" = "closed" ]; then
      BEAD_STATUS_CACHE[$id]=closed
      fail "$ctx: bead '$id' is CLOSED but a live allowlist/waiver line references it — the gate, not the ledger, is the arbiter of closed"
    else
      BEAD_STATUS_CACHE[$id]="$status"
    fi
  fi
}

# --- 1+2+3: allowlist files ------------------------------------------------
shopt -s nullglob
files=("$ALLOWLIST_DIR"/*.txt)
if [ "${#files[@]}" -eq 0 ]; then
  fail "no allowlist files found in $ALLOWLIST_DIR"
fi

for f in "${files[@]}"; do
  perm=0
  lineno=0
  while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    ctx="$f:$lineno"
    case "$line" in
      '') continue ;;
      '#'*)
        if [[ "$line" =~ ^#[[:space:]]*permanent: ]]; then perm=1; fi
        continue ;;
    esac
    IFS=$'\t' read -r entry bead reason extra <<<"$line"
    if [ -z "${entry:-}" ] || [ -z "${bead:-}" ] || [ -z "${reason:-}" ] || [ -n "${extra:-}" ]; then
      fail "$ctx: malformed line (need exactly 3 non-empty tab-separated fields): $line"
      continue
    fi
    if [ "$bead" = "permanent" ]; then
      if [ "$perm" -ne 1 ]; then
        fail "$ctx: 'permanent' entry outside the '# permanent:' section"
      fi
      # permanent entries must name the tool blind spot they compensate
      if [ "${#reason}" -lt 8 ]; then
        fail "$ctx: permanent entry reason must name the tool blind spot it compensates"
      fi
      continue
    fi
    check_bead "$bead" "$ctx (entry: $entry)"
  done < "$f"
done

# --- 4: inline placebo waivers ---------------------------------------------
if [ -n "$SRC_SCAN_DIRS" ]; then
  prose_files=""
  if [ -f "$PATTERNS_FILE" ]; then
    prose_files="$(sed -n 's/^prose-exception:\([^[:space:]]*\).*/\1/p' "$PATTERNS_FILE")"
  fi
  # shellcheck disable=SC2086
  while IFS=: read -r file _ line; do
    id="$(printf '%s' "$line" | sed -n 's/.*placebo-waiver:[[:space:]]*\([^[:space:]]*\).*/\1/p')"
    [ -n "$id" ] || continue
    if [ "$id" = "prose" ]; then
      if ! printf '%s\n' "$prose_files" | grep -qxF "$file"; then
        fail "$file: 'placebo-waiver: prose' but file is not on the prose-exception list in $PATTERNS_FILE"
      fi
      continue
    fi
    check_bead "$id" "$file (inline placebo-waiver)"
  done < <(grep -rn --include='*.go' 'placebo-waiver:' $SRC_SCAN_DIRS 2>/dev/null | grep -v '_test\.go:' || true)
fi

if [ "$FAIL" -ne 0 ]; then
  echo "check_allowlists.sh: FAILED" >&2
  exit 1
fi
echo "check_allowlists.sh: OK (${#files[@]} allowlist file(s) validated; br=$([ "$HAVE_BR" = 1 ] && echo yes || echo absent))"
