#!/usr/bin/env bash
# Shared ratchet helper for the WS0 reality guards (bd-ws0-guards-klz98.1).
#
# ratchet_compare <allowlist-file> <current-violations-file> [entry-class-prefix]
#
#   <allowlist-file>          ci/allowlists/*.txt (tab-separated entry\tbead\treason;
#                             # comments; `# permanent:` section allowed)
#   <current-violations-file> one "<entry>\t<bead-id>" pair per line for waived/known
#                             violations, or "<entry>\t-" when the gate has no bead
#                             association for a live violation (then only the entry
#                             column is compared).
#   [entry-class-prefix]      optional ERE anchored at start of entry; only allowlist
#                             entries matching it participate in the ratchet (lets one
#                             file carry classes computed by different consumers).
#
# Ratchet in BOTH directions:
#   - a current violation whose (entry,bead) pair is not allowlisted => NEW DEBT, fail
#   - an allowlisted pair with no matching current violation        => STALE LINE, fail
#
# Prints offending lines; returns 0 iff both sets match exactly.

ratchet_extract_pairs() {
  # allowlist -> "entry<TAB>bead" pairs (non-comment, non-permanent), sorted unique
  local allowlist="$1" class="${2:-}"
  awk -F'\t' '
    /^#[[:space:]]*permanent:/ { perm=1; next }
    /^#/ || /^[[:space:]]*$/   { next }
    perm && $2 == "permanent"  { next }
    { print $1 "\t" $2 }
  ' "$allowlist" | { if [ -n "$class" ]; then grep -E "^${class}" || true; else cat; fi; } | sort -u
}

ratchet_compare() {
  local allowlist="$1" current="$2" class="${3:-}"
  local allowed cur rc=0
  allowed="$(ratchet_extract_pairs "$allowlist" "$class")"
  cur="$(sort -u "$current")"

  local new_debt stale
  new_debt="$(comm -23 <(printf '%s\n' "$cur" | sed '/^$/d') <(printf '%s\n' "$allowed" | sed '/^$/d'))"
  stale="$(comm -13 <(printf '%s\n' "$cur" | sed '/^$/d') <(printf '%s\n' "$allowed" | sed '/^$/d'))"

  if [ -n "$new_debt" ]; then
    echo "RATCHET FAIL — new debt (violation not allowlisted in $allowlist):" >&2
    printf '  %s\n' $'entry\tbead' >&2
    printf '%s\n' "$new_debt" | sed 's/^/  /' >&2
    echo "  Waiver protocol: open a bead FIRST, then add the '<entry>\\t<bead-id>\\t<reason>' line." >&2
    rc=1
  fi
  if [ -n "$stale" ]; then
    echo "RATCHET FAIL — stale allowlist line(s) in $allowlist (entry no longer matches a real violation — delete in this PR):" >&2
    printf '%s\n' "$stale" | sed 's/^/  /' >&2
    rc=1
  fi
  return $rc
}
