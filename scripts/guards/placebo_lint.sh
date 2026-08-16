#!/usr/bin/env bash
# placebo_lint.sh — WS0-G5 placebo lint (bd-ws0-guards-klz98.6).
#
# Stub-marker language in production code is a CI failure unless tied to an open
# bead. Case-insensitive ERE patterns from ci/placebo_patterns.txt are grepped
# over internal/ and cmd/ (*.go, excluding *_test.go).
#
# A hit requires an inline `placebo-waiver: <bead-id>` on the SAME line
# (`placebo-waiver: prose` only for files on the patterns file's documented
# prose-exception list). Waived (file, bead) pairs are ratcheted in BOTH
# directions against ci/allowlists/placebo.txt: an unlisted waiver fails (new
# debt needs a bead + allowlist line first), and a listed pair with no
# surviving inline waiver fails (stale line — delete it in this PR).
# scripts/check_allowlists.sh verifies the waiver beads are OPEN where br exists.
#
# The script self-tests first (mandatory canary): the fixture in
# scripts/guards/testdata/placebo_canary/ contains an unwaivered "For now" AND
# the verbatim D5 simulator comment; the lint must fire on both or it aborts.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=lib/ratchet.sh
. "$REPO_ROOT/scripts/guards/lib/ratchet.sh"

PATTERNS_FILE="${PATTERNS_FILE:-ci/placebo_patterns.txt}"
ALLOWLIST="${PLACEBO_ALLOWLIST:-ci/allowlists/placebo.txt}"
CANARY_DIR="scripts/guards/testdata/placebo_canary"

patterns_tmp="$(mktemp)"
trap 'rm -f "$patterns_tmp"' EXIT
grep -vE '^#|^[[:space:]]*$|^prose-exception:' "$PATTERNS_FILE" > "$patterns_tmp"
if ! [ -s "$patterns_tmp" ]; then
  echo "placebo_lint: no patterns found in $PATTERNS_FILE" >&2
  exit 1
fi
prose_files="$(sed -n 's/^prose-exception:\([^[:space:]]*\).*/\1/p' "$PATTERNS_FILE")"

# scan <root>... — emits:
#   V\t<file>:<line>\t<content>   unwaivered violation
#   B\t<file>:<line>\t<content>   bad prose waiver (file not on exception list)
#   W\t<file>\t<bead-id>          properly waived hit
scan() {
  grep -rniE -f "$patterns_tmp" --include='*.go' "$@" 2>/dev/null \
    | grep -v '_test\.go:' \
    | while IFS= read -r hit; do
        file="${hit%%:*}"; rest="${hit#*:}"
        line="${rest%%:*}"; content="${rest#*:}"
        case "$content" in
          *placebo-waiver:*)
            id="$(printf '%s' "$content" | sed -n 's/.*placebo-waiver:[[:space:]]*\([^[:space:]]*\).*/\1/p')"
            if [ "$id" = "prose" ]; then
              if printf '%s\n' "$prose_files" | grep -qxF "$file"; then
                continue  # documented prose exception; not a violation, not ratcheted
              fi
              printf 'B\t%s:%s\t%s\n' "$file" "$line" "$content"
            else
              printf 'W\t%s\t%s\n' "$file" "$id"
            fi
            ;;
          *)
            printf 'V\t%s:%s\t%s\n' "$file" "$line" "$content"
            ;;
        esac
      done
}

# --- mandatory canary: the guard must fire on its own fixture ---------------
canary_out="$(scan "$CANARY_DIR")"
if ! printf '%s\n' "$canary_out" | grep -q '^V'; then
  echo "placebo_lint: CANARY FAILURE — lint did not fire on $CANARY_DIR fixture" >&2
  exit 1
fi
if ! printf '%s\n' "$canary_out" | grep -qi 'in production, this would dispatch to actual handlers'; then
  echo "placebo_lint: CANARY FAILURE — lint missed the verbatim D5 simulator comment" >&2
  exit 1
fi

# --- real scan --------------------------------------------------------------
out="$(scan internal cmd)"
rc=0

violations="$(printf '%s\n' "$out" | awk -F'\t' '$1=="V"{print $2"\t"$3}')"
bad_prose="$(printf '%s\n' "$out" | awk -F'\t' '$1=="B"{print $2"\t"$3}')"
if [ -n "$violations" ]; then
  echo "placebo_lint: FAIL — stub-marker language without an inline 'placebo-waiver: <bead-id>' on the same line:" >&2
  printf '%s\n' "$violations" | sed 's/^/  /' >&2
  echo "  Waiver protocol is bead-first: open a bead, add the inline waiver AND the ci/allowlists/placebo.txt line." >&2
  rc=1
fi
if [ -n "$bad_prose" ]; then
  echo "placebo_lint: FAIL — 'placebo-waiver: prose' on file(s) not in the prose-exception list ($PATTERNS_FILE):" >&2
  printf '%s\n' "$bad_prose" | sed 's/^/  /' >&2
  rc=1
fi

waived_tmp="$(mktemp)"
printf '%s\n' "$out" | awk -F'\t' '$1=="W"{print $2"\t"$3}' | sed '/^$/d' > "$waived_tmp"
if ! ratchet_compare "$ALLOWLIST" "$waived_tmp"; then
  rc=1
fi
rm -f "$waived_tmp"

if [ "$rc" -ne 0 ]; then
  echo "placebo_lint.sh: FAILED" >&2
  exit 1
fi
echo "placebo_lint.sh: OK (canary fired; tree clean against $ALLOWLIST)"
